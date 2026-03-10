import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import http from 'node:http';
import path from 'node:path';
import test from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';

import { createClient } from 'redis';

const SHOULD_RUN = process.env.TINYCLAW_RUN_LIVE === '1';
const TEST_TIMEOUT_MS = 60_000;
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

async function waitForWithDiagnostics(condition, timeoutMs, buildDetails) {
  try {
    return await waitFor(condition, timeoutMs);
  } catch (error) {
    const details = await buildDetails();
    const cause = error instanceof Error ? error.message : String(error);
    throw new Error(`${cause}\n${details}`);
  }
}

async function startRedisContainer() {
  const name = `tinyclaw-agent-live-${randomUUID().slice(0, 8)}`;

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

process.loadEnvFile(path.resolve(process.cwd(), '../.env'));

const liveTest = SHOULD_RUN ? test : test.skip;

liveTest(
  'claude_agent_sdk live smoke replies and acks with real Anthropic config',
  { timeout: TEST_TIMEOUT_MS },
  async () => {
    assert.ok(
      process.env.ANTHROPIC_API_KEY || process.env.CLAUDE_CODE_OAUTH_TOKEN,
      'live smoke requires ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN in ../.env',
    );

    const roomId = `room-${randomUUID().slice(0, 8)}`;
    const tenantId = 'tenant-live';
    const chatType = 'group';
    const streamKey = `stream:room:${roomId}`;
    const consumerGroup = `cg:room:${roomId}`;

    const { server, requests, baseUrl } = await startMockEgressServer();
    const { name: redisContainerName, redis, port } = await startRedisContainer();
    const agent = spawn('node', [path.resolve('dist/main.js')], {
      cwd: path.resolve('.'),
      env: {
        ...process.env,
        ROOM_ID: roomId,
        TENANT_ID: tenantId,
        CHAT_TYPE: chatType,
        REDIS_ADDR: `127.0.0.1:${port}`,
        STREAM_PREFIX: 'stream:room',
        CONSUMER_GROUP_PREFIX: 'cg:room',
        CONSUMER_NAME: 'agent-live-test',
        WECOM_EGRESS_BASE_URL: baseUrl,
        WECOM_EGRESS_TOKEN: 'test-token',
        AGENT_RUNTIME_MODE: 'claude_agent_sdk',
        AGENT_READ_BLOCK_MS: TEST_READ_BLOCK_MS,
        AGENT_WORKDIR: path.resolve('.'),
        AGENT_LOAD_DOTENV: '0',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
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
      await waitForWithDiagnostics(
        () =>
          stdout.includes('"msg":"agent_ready"') ||
          stderr.includes('"msg":"agent_ready"'),
        10_000,
        async () => `agent stdout:\n${stdout || '(empty)'}\nagent stderr:\n${stderr || '(empty)'}`,
      );

      const messageId = await redis.xAdd(streamKey, '*', {
        text: 'Reply with exactly: tinyclaw-live-ok',
        trace_id: 'trace-live-smoke-1',
      });

      await waitForWithDiagnostics(
        () =>
          stdout.includes('"msg":"message_received"') ||
          stderr.includes('"msg":"message_received"'),
        10_000,
        async () => {
          const pending = await redis.xPending(streamKey, consumerGroup);
          return [
            'agent never logged message_received',
            `agent stdout:\n${stdout || '(empty)'}`,
            `agent stderr:\n${stderr || '(empty)'}`,
            `redis pending: ${JSON.stringify(pending)}`,
          ].join('\n');
        },
      );

      await waitForWithDiagnostics(
        () => requests.length > 0,
        60_000,
        async () => {
          const pending = await redis.xPending(streamKey, consumerGroup);
          return [
            'egress request was not observed',
            `agent stdout:\n${stdout || '(empty)'}`,
            `agent stderr:\n${stderr || '(empty)'}`,
            `redis pending: ${JSON.stringify(pending)}`,
          ].join('\n');
        },
      );

      const [request] = requests;
      assert.equal(request.body.source.stream_id, messageId);
      assert.match(request.body.reply.text, /tinyclaw-live-ok/);
      assert.equal(request.body.reply.metadata.runtime_mode, 'claude_agent_sdk');

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
