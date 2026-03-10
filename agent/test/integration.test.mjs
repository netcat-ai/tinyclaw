import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import http from 'node:http';
import path from 'node:path';
import test from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';

import { createClient } from 'redis';

const TEST_TIMEOUT_MS = 30_000;
const TEST_READ_BLOCK_MS = '250';

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
    throw error;
  }
}

async function stopRedisContainer(name) {
  await runCommand('docker', ['rm', '-f', name]).catch(() => undefined);
}

function startMockEgressServer() {
  const requests = [];
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on('data', chunk => {
      chunks.push(chunk);
    });
    req.on('end', () => {
      const body = Buffer.concat(chunks).toString('utf8');
      requests.push({
        method: req.method,
        url: req.url,
        headers: req.headers,
        body: JSON.parse(body),
      });
      res.statusCode = 200;
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ ok: true }));
    });
  });

  return new Promise((resolve, reject) => {
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      if (!address || typeof address === 'string') {
        reject(new Error('failed to start mock egress server'));
        return;
      }
      resolve({
        server,
        requests,
        baseUrl: `http://127.0.0.1:${address.port}/reply`,
      });
    });
  });
}

function startFixedResponseEgressServer(statusCode) {
  const requests = [];
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on('data', chunk => {
      chunks.push(chunk);
    });
    req.on('end', () => {
      const body = Buffer.concat(chunks).toString('utf8');
      requests.push({
        method: req.method,
        url: req.url,
        headers: req.headers,
        body: JSON.parse(body),
      });
      res.statusCode = statusCode;
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ ok: statusCode >= 200 && statusCode < 300 }));
    });
  });

  return new Promise((resolve, reject) => {
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      if (!address || typeof address === 'string') {
        reject(new Error('failed to start fixed-response egress server'));
        return;
      }
      resolve({
        server,
        requests,
        baseUrl: `http://127.0.0.1:${address.port}/reply`,
      });
    });
  });
}

async function stopServer(server) {
  await new Promise((resolve, reject) => {
    server.close(error => {
      if (error) {
        reject(error);
        return;
      }
      resolve();
    });
  });
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
  baseUrl,
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
      STREAM_PREFIX: 'stream:room',
      CONSUMER_GROUP_PREFIX: 'cg:room',
      CONSUMER_NAME: 'agent-test',
      WECOM_EGRESS_BASE_URL: baseUrl,
      WECOM_EGRESS_TOKEN: 'test-token',
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
    const streamKey = `stream:room:${roomId}`;
    const consumerGroup = `cg:room:${roomId}`;

    const { server, requests, baseUrl } = await startMockEgressServer();
    const { name: redisContainerName, redis, port } = await startRedisContainer();

    const agent = spawnAgent({ roomId, tenantId, chatType, port, baseUrl });

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

      const messageId = await redis.xAdd(streamKey, '*', {
        text: 'hello from integration test',
        trace_id: 'trace-integration-1',
      });

      await waitFor(() => requests.length > 0, 10_000);

      const [request] = requests;
      assert.equal(request.method, 'POST');
      assert.equal(request.headers.authorization, 'Bearer test-token');
      assert.equal(request.body.room_id, roomId);
      assert.equal(request.body.tenant_id, tenantId);
      assert.equal(request.body.chat_type, chatType);
      assert.equal(request.body.source.stream_id, messageId);
      assert.equal(request.body.source.trace_id, 'trace-integration-1');
      assert.equal(request.body.input.text, 'hello from integration test');
      assert.equal(
        request.body.reply.text,
        'Echo from tinyclaw-agent: hello from integration test',
      );

      const pending = await waitFor(async () => {
        const summary = await redis.xPending(streamKey, consumerGroup);
        return summary.pending === 0 ? summary : null;
      }, 10_000);

      assert.equal(pending.pending, 0);
    } finally {
      await stopProcess(agent);
      await redis.quit().catch(() => undefined);
      await stopRedisContainer(redisContainerName);
      await stopServer(server);
    }
  },
);

test(
  'agent leaves the message pending when egress fails',
  { timeout: TEST_TIMEOUT_MS },
  async () => {
    const roomId = `room-${randomUUID().slice(0, 8)}`;
    const tenantId = 'tenant-test';
    const chatType = 'group';
    const streamKey = `stream:room:${roomId}`;
    const consumerGroup = `cg:room:${roomId}`;

    const { server, requests, baseUrl } = await startFixedResponseEgressServer(500);
    const { name: redisContainerName, redis, port } = await startRedisContainer();
    const agent = spawnAgent({ roomId, tenantId, chatType, port, baseUrl });

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

      const messageId = await redis.xAdd(streamKey, '*', {
        text: 'this should stay pending',
        trace_id: 'trace-integration-fail-1',
      });

      await waitFor(() => requests.length > 0, 10_000);
      await waitFor(
        () =>
          stdout.includes('"msg":"message_processing_failed"') ||
          stderr.includes('"msg":"message_processing_failed"'),
        10_000,
      );

      const pending = await waitFor(async () => {
        const summary = await redis.xPending(streamKey, consumerGroup);
        return summary.pending === 1 ? summary : null;
      }, 10_000);

      assert.equal(pending.pending, 1);
      assert.equal(pending.firstId, messageId);
      assert.equal(pending.lastId, messageId);
      assert.equal(requests[0].body.reply.text, 'Echo from tinyclaw-agent: this should stay pending');
      assert.match(
        stdout + stderr,
        /"msg":"message_processing_failed"/,
      );
      assert.doesNotMatch(
        stdout + stderr,
        new RegExp(`"msg":"message_acked".*"stream_id":"${messageId}"`),
      );
    } finally {
      await stopProcess(agent);
      await redis.quit().catch(() => undefined);
      await stopRedisContainer(redisContainerName);
      await stopServer(server);
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
    const streamKey = `stream:room:${roomId}`;
    const consumerGroup = `cg:room:${roomId}`;

    const { server: egressServer, requests: egressRequests, baseUrl } =
      await startMockEgressServer();
    const { name: redisContainerName, redis, port } = await startRedisContainer();
    const agent = spawnAgent({
      roomId,
      tenantId,
      chatType,
      port,
      baseUrl,
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

      const messageId = await redis.xAdd(streamKey, '*', {
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
        const summary = await redis.xPending(streamKey, consumerGroup);
        return summary.pending === 1 ? summary : null;
      }, 10_000);

      assert.equal(egressRequests.length, 0);
      assert.equal(pending.pending, 1);
      assert.equal(pending.firstId, messageId);
      assert.equal(pending.lastId, messageId);
      assert.match(
        stdout + stderr,
        /claude_agent_sdk runtime requires ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN/,
      );
    } finally {
      await stopProcess(agent);
      await redis.quit().catch(() => undefined);
      await stopRedisContainer(redisContainerName);
      await stopServer(egressServer);
    }
  },
);
