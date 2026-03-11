import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import path from 'node:path';
import test from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';

import { createClient } from 'redis';

const TEST_TIMEOUT_MS = 30_000;
const TEST_READ_BLOCK_MS = '250';
const activeRedisContainers = new Set();
let cleanupHandlersInstalled = false;

async function runCommand(command, args) {
  const child = spawn(command, args, {
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  let stdout = '';
  let stderr = '';
  child.stdout.setEncoding('utf8');
  child.stderr.setEncoding('utf8');
  child.stdout.on('data', chunk => {
    stdout += chunk;
  });
  child.stderr.on('data', chunk => {
    stderr += chunk;
  });

  const [code] = await once(child, 'close');
  if (code !== 0) {
    throw new Error(
      `${command} ${args.join(' ')} failed with code ${code}\nstdout:\n${stdout}\nstderr:\n${stderr}`,
    );
  }

  return { stdout, stderr };
}

function installCleanupHandlers() {
  if (cleanupHandlersInstalled) {
    return;
  }
  cleanupHandlersInstalled = true;

  const cleanup = () => {
    for (const name of activeRedisContainers) {
      spawn('docker', ['rm', '-f', name], {
        stdio: 'ignore',
        detached: true,
      }).unref();
    }
  };

  process.on('SIGINT', () => {
    cleanup();
    process.exit(130);
  });
  process.on('SIGTERM', () => {
    cleanup();
    process.exit(143);
  });
  process.on('exit', cleanup);
}

async function waitFor(condition, timeoutMs, intervalMs = 100) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await condition();
    if (value) {
      return value;
    }
    await delay(intervalMs);
  }
  throw new Error(`condition not met within ${timeoutMs}ms`);
}

async function startRedisContainer() {
  installCleanupHandlers();
  const name = `tinyclaw-agent-test-${randomUUID().slice(0, 8)}`;

  await runCommand('docker', [
    'run',
    '--detach',
    '--rm',
    '--name',
    name,
    '-p',
    '127.0.0.1::6379',
    'redis:7-alpine',
  ]);
  activeRedisContainers.add(name);

  try {
    const port = await waitFor(async () => {
      const { stdout } = await runCommand('docker', ['port', name, '6379/tcp']);
      const match = stdout.trim().match(/:(\d+)$/);
      return match ? Number.parseInt(match[1], 10) : null;
    }, 10_000);

    const redis = createClient({
      socket: { host: '127.0.0.1', port },
    });
    await redis.connect();
    await waitFor(async () => {
      try {
        await redis.ping();
        return true;
      } catch {
        return false;
      }
    }, 10_000);

    return { name, port, redis };
  } catch (error) {
    await runCommand('docker', ['rm', '-f', name]).catch(() => undefined);
    activeRedisContainers.delete(name);
    throw error;
  }
}

async function stopRedisContainer(name) {
  await runCommand('docker', ['rm', '-f', name]).catch(() => undefined);
  activeRedisContainers.delete(name);
}

async function waitForStreamMessage(redis, streamKey, timeoutMs) {
  return waitFor(async () => {
    const messages = await redis.xRange(streamKey, '-', '+', {
      COUNT: 1,
    });
    return messages.length > 0 ? messages[0] : null;
  }, timeoutMs);
}

async function stopProcess(child) {
  if (child.exitCode !== null) {
    return;
  }

  child.kill('SIGTERM');
  const closed = once(child, 'close');
  await Promise.race([
    closed,
    delay(8_000).then(async () => {
      child.kill('SIGKILL');
      await closed;
    }),
  ]);
}

function spawnAgent({
  roomId,
  tenantId,
  chatType,
  port,
  runtimeMode = 'echo',
  extraEnv = {},
}) {
  return spawn('node', [path.resolve('dist/main.js')], {
    cwd: path.resolve('.'),
    env: {
      ...process.env,
      ROOM_ID: roomId,
      TENANT_ID: tenantId,
      CHAT_TYPE: chatType,
      REDIS_ADDR: `127.0.0.1:${port}`,
      CONSUMER_GROUP_PREFIX: 'cg:room',
      CONSUMER_NAME: 'agent-test',
      AGENT_RUNTIME_MODE: runtimeMode,
      AGENT_READ_BLOCK_MS: TEST_READ_BLOCK_MS,
      AGENT_WORKDIR: path.resolve('.'),
      AGENT_LOAD_DOTENV: '0',
      ...extraEnv,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
}

test(
  'agent consumes a stream message, posts egress, and clears pending entries',
  { timeout: TEST_TIMEOUT_MS },
  async () => {
    const roomId = `room-${randomUUID().slice(0, 8)}`;
    const tenantId = 'tenant-test';
    const chatType = 'group';
    const inStreamKey = `stream:i:${roomId}`;
    const outStreamKey = `stream:o:${roomId}`;
    const consumerGroup = `cg:room:${roomId}`;

    const { name: redisContainerName, redis, port } = await startRedisContainer();

    const agent = spawnAgent({ roomId, tenantId, chatType, port });

    let stdout = '';
    let stderr = '';
    agent.stdout.setEncoding('utf8');
    agent.stderr.setEncoding('utf8');
    agent.stdout.on('data', chunk => {
      stdout += chunk;
    });
    agent.stderr.on('data', chunk => {
      stderr += chunk;
    });

    try {
      await waitFor(() => stdout.includes('"msg":"agent_ready"') || stderr.includes('"msg":"agent_ready"'), 10_000);

      const messageId = await redis.xAdd(inStreamKey, '*', {
        text: 'hello from integration test',
        trace_id: 'trace-integration-1',
      });

      const egressMessage = await waitForStreamMessage(
        redis,
        outStreamKey,
        10_000,
      );

      assert.equal(egressMessage.message.room_id, roomId);
      assert.equal(
        egressMessage.message.text,
        'Echo from tinyclaw-agent: hello from integration test',
      );
      assert.equal(egressMessage.message.source_id, messageId);

      const pending = await waitFor(async () => {
        const summary = await redis.xPending(inStreamKey, consumerGroup);
        return summary.pending === 0 ? summary : null;
      }, 10_000);

      assert.equal(pending.pending, 0);
    } finally {
      await stopProcess(agent);
      await redis.quit().catch(() => undefined);
      await stopRedisContainer(redisContainerName);
    }
  },
);

test(
  'claude_agent_sdk runtime leaves the message pending when auth is missing',
  { timeout: TEST_TIMEOUT_MS },
  async () => {
    const roomId = `room-${randomUUID().slice(0, 8)}`;
    const tenantId = 'tenant-test';
    const chatType = 'group';
    const inStreamKey = `stream:i:${roomId}`;
    const outStreamKey = `stream:o:${roomId}`;
    const consumerGroup = `cg:room:${roomId}`;

    const { name: redisContainerName, redis, port } = await startRedisContainer();
    const agent = spawnAgent({
      roomId,
      tenantId,
      chatType,
      port,
      runtimeMode: 'claude_agent_sdk',
    });

    let stdout = '';
    let stderr = '';
    agent.stdout.setEncoding('utf8');
    agent.stderr.setEncoding('utf8');
    agent.stdout.on('data', chunk => {
      stdout += chunk;
    });
    agent.stderr.on('data', chunk => {
      stderr += chunk;
    });

    try {
      await waitFor(
        () =>
          stdout.includes('"msg":"agent_ready"') ||
          stderr.includes('"msg":"agent_ready"'),
        10_000,
      );

      const messageId = await redis.xAdd(inStreamKey, '*', {
        text: 'this claude run should fail before execution',
        trace_id: 'trace-claude-auth-missing-1',
      });

      await waitFor(
        () =>
          stdout.includes('"msg":"message_processing_failed"') ||
          stderr.includes('"msg":"message_processing_failed"'),
        10_000,
      );

      const pending = await waitFor(async () => {
        const summary = await redis.xPending(inStreamKey, consumerGroup);
        return summary.pending === 1 ? summary : null;
      }, 10_000);

      assert.equal(pending.pending, 1);
      assert.equal(pending.firstId, messageId);
      assert.equal(pending.lastId, messageId);
      const egressMessages = await redis.xRange(outStreamKey, '-', '+', {
        COUNT: 10,
      });
      assert.equal(egressMessages.length, 0);
      assert.match(
        stdout + stderr,
        /"msg":"message_processing_failed"/,
      );
      assert.match(
        stdout + stderr,
        /claude_agent_sdk runtime requires ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN/,
      );
    } finally {
      await stopProcess(agent);
      await redis.quit().catch(() => undefined);
      await stopRedisContainer(redisContainerName);
    }
  },
);
