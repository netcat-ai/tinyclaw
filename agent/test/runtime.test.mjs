import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

import { ClaudeAgentSdkRuntime } from '../dist/runtime.js';

function buildEnv(overrides = {}) {
  const workdir = fs.mkdtempSync(path.join(os.tmpdir(), 'tinyclaw-runtime-'));

  return {
    env: {
      serverPort: 8888,
      anthropicApiKey: 'test-key',
      anthropicBaseUrl: 'https://example.test',
      claudeCodeOauthToken: undefined,
      agentIdleAfterSec: 300,
      agentLogLevel: 'info',
      claudeRuntimeTimeoutMs: 50,
      agentWorkdir: workdir,
      agentTmpdir: os.tmpdir(),
      agentRuntimeMode: 'claude_agent_sdk',
      claudeModel: 'claude-sonnet-4-6',
      claudeSystemPromptAppend: undefined,
      claudeAllowedTools: undefined,
      claudeDisallowedTools: undefined,
      claudeMaxTurns: 4,
      ...overrides,
    },
    cleanup: () => fs.rmSync(workdir, { recursive: true, force: true }),
  };
}

function buildMessage() {
  return {
    msgid: 'msg-test-1',
    roomId: 'room-test',
    tenantId: 'tenant-test',
    chatType: 'group',
    messages: [
      {
        seq: 1,
        msgid: 'msg-test-1',
        fromId: 'user-test',
        fromName: 'Test User',
        msgTime: '2026-03-21T00:00:00Z',
        payload: JSON.stringify({
          msgtype: 'text',
          text: { content: 'hello' },
        }),
      },
    ],
  };
}

test('claude runtime times out and closes the query', async () => {
  let closeCalled = false;

  const { env, cleanup } = buildEnv();
  const runtime = new ClaudeAgentSdkRuntime(env, {
    now: () => Date.now(),
    createQuery: ({ options }) => {
      const signal = options?.abortController?.signal;

      return {
        async next() {
          await new Promise((resolve, reject) => {
            signal?.addEventListener(
              'abort',
              () => reject(new Error('aborted')),
              { once: true },
            );
          });
          return { done: true, value: undefined };
        },
        async return() {
          return { done: true, value: undefined };
        },
        async throw(error) {
          throw error;
        },
        [Symbol.asyncIterator]() {
          return this;
        },
        close() {
          closeCalled = true;
        },
      };
    },
  });

  try {
    await assert.rejects(
      runtime.run(buildMessage()),
      /claude agent sdk timed out after 50ms/,
    );
    assert.equal(closeCalled, true);
  } finally {
    cleanup();
  }
});

test('claude runtime creates then resumes the configured session id', async () => {
  const optionsSeen = [];

  const { env, cleanup } = buildEnv({ claudeRuntimeTimeoutMs: 500 });
  const runtime = new ClaudeAgentSdkRuntime(env, {
    now: () => Date.now(),
    createQuery: ({ options }) => {
      optionsSeen.push(options);
      let emittedInit = false;
      let emittedResult = false;

      return {
        async next() {
          if (!emittedInit) {
            emittedInit = true;
            return {
              done: false,
              value: {
                type: 'system',
                subtype: 'init',
                session_id: '11111111-1111-4111-8111-111111111111',
              },
            };
          }
          if (!emittedResult) {
            emittedResult = true;
            return {
              done: false,
              value: {
                type: 'result',
                subtype: 'success',
                result: 'ok',
                session_id: '11111111-1111-4111-8111-111111111111',
              },
            };
          }
          return { done: true, value: undefined };
        },
        async return() {
          return { done: true, value: undefined };
        },
        async throw(error) {
          throw error;
        },
        [Symbol.asyncIterator]() {
          return this;
        },
        close() {},
      };
    },
  });

  try {
    const first = await runtime.run(buildMessage());
    const second = await runtime.run(buildMessage());

    assert.equal(first.stdout, 'ok');
    assert.equal(second.stdout, 'ok');
    assert.equal(optionsSeen.length, 2);
    assert.equal(optionsSeen[0].sessionId, undefined);
    assert.equal(optionsSeen[0].resume, undefined);
    assert.equal(optionsSeen[1].sessionId, undefined);
    assert.equal(
      optionsSeen[1].resume,
      '11111111-1111-4111-8111-111111111111',
    );
  } finally {
    cleanup();
  }
});
