import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import net from 'node:net';
import path from 'node:path';
import test from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';

const SHOULD_RUN = process.env.TINYCLAW_RUN_LIVE === '1';
const TEST_TIMEOUT_MS = 60_000;

async function getFreePort() {
  return await new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      server.close(error => {
        if (error) {
          reject(error);
          return;
        }
        resolve(address.port);
      });
    });
  });
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
  'claude_agent_sdk live smoke serves HTTP chat responses',
  { timeout: TEST_TIMEOUT_MS },
  async () => {
    assert.ok(
      process.env.ANTHROPIC_API_KEY || process.env.CLAUDE_CODE_OAUTH_TOKEN,
      'live smoke requires ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN in ../.env',
    );

    const port = await getFreePort();
    const agent = spawn('node', [path.resolve('dist/main.js')], {
      cwd: path.resolve('.'),
      env: {
        ...process.env,
        AGENT_SERVER_PORT: String(port),
        AGENT_RUNTIME_MODE: 'claude_agent_sdk',
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
      await waitFor(
        () =>
          stdout.includes('"msg":"agent_ready"') ||
          stderr.includes('"msg":"agent_ready"'),
        10_000,
      );

      const response = await fetch(`http://127.0.0.1:${port}/v1/chat`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({
          msgid: 'msg-live-smoke-1',
          room_id: 'room-live',
          tenant_id: 'tenant-live',
          chat_type: 'group',
          text: 'Reply with the exact text: tinyclaw-live-ok',
        }),
      });

      assert.equal(response.status, 200);
      const payload = await response.json();
      assert.match(payload.text, /tinyclaw-live-ok/);
    } finally {
      await stopProcess(agent);
    }
  },
);
