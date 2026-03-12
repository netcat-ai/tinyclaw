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
      roomId: 'room-test',
      tenantId: 'tenant-test',
      chatType: 'group',
      redisAddr: '127.0.0.1:6379',
      redisUsername: undefined,
      redisPassword: undefined,
      redisDb: 0,
      consumerGroupPrefix: 'cg:room',
      consumerName: 'agent-test',
      anthropicApiKey: 'test-key',
      anthropicBaseUrl: 'https://example.test',
      claudeCodeOauthToken: undefined,
      agentIdleAfterSec: 300,
      agentLogLevel: 'info',
      agentReadBlockMs: 250,
      claudeRuntimeTimeoutMs: 50,
      agentWorkdir: workdir,
      agentTmpdir: os.tmpdir(),
      agentRuntimeMode: 'claude_agent_sdk',
      claudeModel: 'claude-sonnet-4-5',
      claudeSystemPromptAppend: undefined,
      claudeAllowedTools: undefined,
      claudeDisallowedTools: undefined,
      claudeMaxTurns: 4,
      streamKey: 'stream:i:room-test',
      consumerGroup: 'cg:room:room-test',
      ...overrides,
    },
    cleanup: () => fs.rmSync(workdir, { recursive: true, force: true }),
  };
}

function buildMessage() {
  return {
    streamEntryId: '1-0',
    msgid: 'msg-test-1',
    streamKey: 'stream:i:room-test',
    roomId: 'room-test',
    tenantId: 'tenant-test',
    chatType: 'group',
    text: 'hello',
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

