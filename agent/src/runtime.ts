import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

import { query } from '@anthropic-ai/claude-agent-sdk';

import type { AgentEnv, AgentRequest, ExecutionResult } from './types.js';

const require = createRequire(import.meta.url);
const runtimeDir = path.dirname(fileURLToPath(import.meta.url));
const packageRoot = path.resolve(runtimeDir, '..');

export interface AgentRuntime {
  run(message: AgentRequest): Promise<ExecutionResult>;
}

type ClaudeQuery = ReturnType<typeof query>;

type RuntimeDeps = {
  createQuery: typeof query;
  now: () => number;
};

const runtimeDeps: RuntimeDeps = {
  createQuery: query,
  now: () => Date.now(),
};

class EchoRuntime implements AgentRuntime {
  async run(message: AgentRequest): Promise<ExecutionResult> {
    return {
      stdout: `Echo from tinyclaw-agent: received ${message.messages.length} messages`,
      stderr: '',
      exit_code: 0,
    };
  }
}

function parsePayload(payload: string): unknown {
  try {
    return JSON.parse(payload);
  } catch {
    return payload;
  }
}

function buildClaudePrompt(message: AgentRequest): string {
  const lines = [
    'You are handling a TinyClaw room message.',
    `room_id: ${message.roomId}`,
    `tenant_id: ${message.tenantId}`,
    `chat_type: ${message.chatType}`,
    `msgid: ${message.msgid}`,
  ];

  lines.push(
    '',
    'Messages (JSON):',
    JSON.stringify(
      message.messages.map(item => ({
        seq: item.seq,
        msgid: item.msgid,
        from_id: item.fromId,
        from_name: item.fromName ?? '',
        msg_time: item.msgTime ?? '',
        payload: parsePayload(item.payload),
      })),
      null,
      2,
    ),
  );
  return lines.join('\n');
}

function buildClaudeEnv(env: AgentEnv): Record<string, string> {
  const runtimeEnv: Record<string, string> = {
    ...Object.fromEntries(
      Object.entries(process.env).filter(
        (entry): entry is [string, string] => typeof entry[1] === 'string',
      ),
    ),
    CLAUDE_AGENT_SDK_CLIENT_APP: 'tinyclaw-agent/0.1.0',
  };

  if (env.anthropicApiKey) {
    runtimeEnv.ANTHROPIC_API_KEY = env.anthropicApiKey;
  }
  if (env.anthropicBaseUrl) {
    runtimeEnv.ANTHROPIC_BASE_URL = env.anthropicBaseUrl;
  }
  if (env.claudeCodeOauthToken) {
    runtimeEnv.CLAUDE_CODE_OAUTH_TOKEN = env.claudeCodeOauthToken;
  }

  return runtimeEnv;
}

function resolveClaudeCodeExecutable(): string {
  const explicit = process.env.CLAUDE_CODE_EXECUTABLE?.trim();
  if (explicit) {
    return fs.existsSync(explicit) ? fs.realpathSync(explicit) : explicit;
  }

  try {
    return require.resolve('@anthropic-ai/claude-code/cli.js');
  } catch {
    const localBin = path.resolve(packageRoot, 'node_modules/.bin/claude');
    if (fs.existsSync(localBin)) {
      return fs.realpathSync(localBin);
    }

    throw new Error(
      'Claude Code executable not found. Install @anthropic-ai/claude-code or set CLAUDE_CODE_EXECUTABLE',
    );
  }
}

export class ClaudeAgentSdkRuntime implements AgentRuntime {
  constructor(
    private readonly env: AgentEnv,
    private readonly deps: RuntimeDeps = runtimeDeps,
  ) {}

  async run(message: AgentRequest): Promise<ExecutionResult> {
    if (!this.env.anthropicApiKey && !this.env.claudeCodeOauthToken) {
      throw new Error(
        'claude_agent_sdk runtime requires ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN',
      );
    }
    if (!fs.existsSync(this.env.agentWorkdir)) {
      throw new Error(
        `claude_agent_sdk runtime requires an existing AGENT_WORKDIR: ${this.env.agentWorkdir}`,
      );
    }

    const startedAt = this.deps.now();
    const abortController = new AbortController();
    let timedOut = false;
    let finalResult: ExecutionResult | null = null;
    let finalError: string | null = null;
    let queryHandle: ClaudeQuery | null = null;
    const timeoutHandle = setTimeout(() => {
      timedOut = true;
      console.error(
        JSON.stringify({
          level: 'error',
          msg: 'claude_runtime_timeout',
          room_id: message.roomId,
          msgid: message.msgid,
          timeout_ms: this.env.claudeRuntimeTimeoutMs,
          model: this.env.claudeModel,
        }),
      );
      abortController.abort();
      queryHandle?.close();
    }, this.env.claudeRuntimeTimeoutMs);

    console.log(
      JSON.stringify({
        level: 'info',
        msg: 'claude_runtime_started',
        room_id: message.roomId,
        msgid: message.msgid,
        timeout_ms: this.env.claudeRuntimeTimeoutMs,
        model: this.env.claudeModel,
      }),
    );

    try {
      queryHandle = this.deps.createQuery({
        prompt: buildClaudePrompt(message),
        options: {
          abortController,
          cwd: this.env.agentWorkdir,
          model: this.env.claudeModel,
          maxTurns: this.env.claudeMaxTurns,
          pathToClaudeCodeExecutable: resolveClaudeCodeExecutable(),
          tools: {
            type: 'preset',
            preset: 'claude_code',
          },
          allowedTools: this.env.claudeAllowedTools,
          disallowedTools: this.env.claudeDisallowedTools,
          systemPrompt: this.env.claudeSystemPromptAppend
            ? {
                type: 'preset',
                preset: 'claude_code',
                append: this.env.claudeSystemPromptAppend,
              }
            : undefined,
          permissionMode: 'bypassPermissions',
          allowDangerouslySkipPermissions: true,
          env: buildClaudeEnv(this.env),
        },
      });

      for await (const sdkMessage of queryHandle) {
        if (sdkMessage.type !== 'result') {
          continue;
        }

        if (sdkMessage.subtype === 'success') {
          finalResult = {
            stdout: sdkMessage.result.trim(),
            stderr: '',
            exit_code: 0,
          };
          continue;
        }

        finalError = sdkMessage.errors.join('; ') || sdkMessage.subtype;
      }
    } catch (error) {
      if (timedOut) {
        throw new Error(
          `claude agent sdk timed out after ${this.env.claudeRuntimeTimeoutMs}ms`,
        );
      }
      throw error;
    } finally {
      clearTimeout(timeoutHandle);
      queryHandle?.close();
    }

    if (finalResult) {
      console.log(
        JSON.stringify({
          level: 'info',
          msg: 'claude_runtime_completed',
          room_id: message.roomId,
          msgid: message.msgid,
          duration_ms: this.deps.now() - startedAt,
          model: this.env.claudeModel,
        }),
      );
      return finalResult;
    }

    if (finalError) {
      throw new Error(`claude agent sdk execution failed: ${finalError}`);
    }

    throw new Error('claude agent sdk returned no final result');
  }
}

export function createRuntime(env: AgentEnv): AgentRuntime {
  if (env.agentRuntimeMode === 'echo') {
    return new EchoRuntime();
  }
  return new ClaudeAgentSdkRuntime(env);
}
