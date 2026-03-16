import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import net from 'node:net';
import path from 'node:path';
import test from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';

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

test('agent serves HTTP chat requests in echo mode', async () => {
  const port = await getFreePort();
  const agent = spawn('node', [path.resolve('dist/main.js')], {
    cwd: path.resolve('.'),
    env: {
      ...process.env,
      AGENT_SERVER_PORT: String(port),
      AGENT_RUNTIME_MODE: 'echo',
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

    const health = await fetch(`http://127.0.0.1:${port}/healthz`);
    assert.equal(health.status, 200);

    const response = await fetch(`http://127.0.0.1:${port}/v1/chat`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        msgid: 'msg-integration-1',
        room_id: 'room-integration',
        tenant_id: 'tenant-integration',
        chat_type: 'group',
        text: 'hello integration',
      }),
    });

    assert.equal(response.status, 200);
    const payload = await response.json();
    assert.equal(payload.text, 'Echo from tinyclaw-agent: hello integration');
    assert.equal(payload.metadata.runtime_mode, 'echo');
  } finally {
    await stopProcess(agent);
  }
});
