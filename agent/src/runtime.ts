import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

import { query } from '@anthropic-ai/claude-agent-sdk';

import type { AgentEnv, RoomStreamMessage, RuntimeResult } from './types.js';

const require = createRequire(import.meta.url);
const runtimeDir = path.dirname(fileURLToPath(import.meta.url));
const packageRoot = path.resolve(runtimeDir, '..');

export interface AgentRuntime {
  run(message: RoomStreamMessage): Promise<RuntimeResult>;
}

class EchoRuntime implements AgentRuntime {
  async run(message: RoomStreamMessage): Promise<RuntimeResult> {
    return {
      text: `Echo from tinyclaw-agent: ${message.text}`,
      metadata: {
        runtime_mode: 'echo',
      },
    };
  }
}

function buildClaudePrompt(message: RoomStreamMessage): string {
  const lines = [
    'You are handling a TinyClaw room message.',
    `room_id: ${message.roomId}`,
    `tenant_id: ${message.tenantId}`,
    `chat_type: ${message.chatType}`,
    `msgid: ${message.msgid}`,
  ];

  lines.push('', 'User message:', message.text);
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

class ClaudeAgentSdkRuntime implements AgentRuntime {
  constructor(private readonly env: AgentEnv) {}

  async run(message: RoomStreamMessage): Promise<RuntimeResult> {
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

    let finalResult: RuntimeResult | null = null;
    let finalError: string | null = null;

    for await (const sdkMessage of query({
      prompt: buildClaudePrompt(message),
      options: {
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
    })) {
      if (sdkMessage.type !== 'result') {
        continue;
      }

      if (sdkMessage.subtype === 'success') {
        finalResult = {
          text: sdkMessage.result.trim(),
          metadata: {
            runtime_mode: 'claude_agent_sdk',
            model: this.env.claudeModel,
            session_id: sdkMessage.session_id,
            sdk_result_uuid: sdkMessage.uuid,
            total_cost_usd: sdkMessage.total_cost_usd,
            duration_ms: sdkMessage.duration_ms,
          },
        };
        continue;
      }

      finalError = sdkMessage.errors.join('; ') || sdkMessage.subtype;
    }

    if (finalResult && finalResult.text) {
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
