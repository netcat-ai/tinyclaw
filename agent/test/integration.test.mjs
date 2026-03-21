import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import fs from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
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

test('agent serves official sandbox runtime endpoints in echo mode', async () => {
  const port = await getFreePort();
  const workdir = await fs.mkdtemp(path.join(os.tmpdir(), 'tinyclaw-agent-'));
  const agent = spawn('node', [path.resolve('dist/main.js')], {
    cwd: path.resolve('.'),
    env: {
      ...process.env,
      AGENT_SERVER_PORT: String(port),
      AGENT_RUNTIME_MODE: 'echo',
      AGENT_WORKDIR: workdir,
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

    const agentResponse = await fetch(`http://127.0.0.1:${port}/agent`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        msgid: 'msg-integration-1',
        room_id: 'room-integration',
        tenant_id: 'tenant-integration',
        chat_type: 'group',
        messages: [
          {
            seq: 1,
            msgid: 'msg-integration-1',
            from_id: 'user-integration',
            from_name: 'Integration User',
            msg_time: '2026-03-21T00:00:00Z',
            payload: JSON.stringify({
              msgtype: 'text',
              text: { content: 'hello integration' },
            }),
          },
        ],
      }),
    });

    assert.equal(agentResponse.status, 200);
    assert.deepEqual(await agentResponse.json(), {
      stdout: 'Echo from tinyclaw-agent: received 1 messages',
      stderr: '',
      exit_code: 0,
    });

    const executeResponse = await fetch(`http://127.0.0.1:${port}/execute`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        command: 'printf tinyclaw-execute-ok',
      }),
    });

    assert.equal(executeResponse.status, 200);
    assert.deepEqual(await executeResponse.json(), {
      stdout: 'tinyclaw-execute-ok',
      stderr: '',
      exit_code: 0,
    });

    const uploadForm = new FormData();
    uploadForm.append('file', new Blob(['hello file']), 'note.txt');
    const uploadResponse = await fetch(`http://127.0.0.1:${port}/upload`, {
      method: 'POST',
      body: uploadForm,
    });
    assert.equal(uploadResponse.status, 200);

    const existsResponse = await fetch(`http://127.0.0.1:${port}/exists/note.txt`);
    assert.equal(existsResponse.status, 200);
    assert.deepEqual(await existsResponse.json(), {
      path: 'note.txt',
      exists: true,
    });

    const listResponse = await fetch(`http://127.0.0.1:${port}/list/.`);
    assert.equal(listResponse.status, 200);
    const listPayload = await listResponse.json();
    assert.equal(listPayload.length, 1);
    assert.equal(listPayload[0].name, 'note.txt');
    assert.equal(listPayload[0].type, 'file');

    const downloadResponse = await fetch(`http://127.0.0.1:${port}/download/note.txt`);
    assert.equal(downloadResponse.status, 200);
    assert.equal(await downloadResponse.text(), 'hello file');
  } finally {
    await stopProcess(agent);
    await fs.rm(workdir, { recursive: true, force: true });
  }
});
