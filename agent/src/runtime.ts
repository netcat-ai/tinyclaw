import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

import { createSdkMcpServer, query, tool as sdkTool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';

import type { AgentEnv, AgentRequest, ExecutionResult } from './types.js';

const require = createRequire(import.meta.url);
const runtimeDir = path.dirname(fileURLToPath(import.meta.url));
const packageRoot = path.resolve(runtimeDir, '..');
const logPreviewLimit = 200;

export interface AgentRuntime {
  run(message: AgentRequest): Promise<ExecutionResult>;
}

type ClaudeQuery = ReturnType<typeof query>;

type RuntimeDeps = {
  createQuery: typeof query;
  fetchFn: typeof fetch;
  now: () => number;
};

const runtimeDeps: RuntimeDeps = {
  createQuery: query,
  fetchFn: fetch,
  now: () => Date.now(),
};

type ParsedPayload = {
  msgtype?: string;
  text?: {
    content?: string;
  };
  markdown?: {
    content?: string;
  };
  image?: {
    url?: string;
    sdkfileid?: string;
  };
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

function parseStructuredPayload(payload: string): ParsedPayload | null {
  try {
    return JSON.parse(payload) as ParsedPayload;
  } catch {
    return null;
  }
}

function truncateForLog(value: string, limit = logPreviewLimit): string {
  const trimmed = value.trim();
  if (trimmed.length <= limit) {
    return trimmed;
  }
  return `${trimmed.slice(0, limit)}...`;
}

function buildMessageSummary(message: AgentRequest): Array<Record<string, unknown>> {
  return message.messages.map(item => ({
    seq: item.seq,
    msgid: item.msgid,
    from_id: item.fromId,
    from_name: item.fromName ?? '',
    msg_time: item.msgTime ?? '',
    payload_length: item.payload.length,
    payload_preview: truncateForLog(item.payload, logPreviewLimit),
  }));
}

function buildClaudePrompt(message: AgentRequest, mediaToolAvailable: boolean): string {
  const lines = [
    'You are handling a TinyClaw room message.',
    `room_id: ${message.roomId}`,
    `tenant_id: ${message.tenantId}`,
    `chat_type: ${message.chatType}`,
    `msgid: ${message.msgid}`,
  ];

  if (mediaToolAvailable) {
    lines.push(
      'If you need to inspect an image from a message, call fetch_wecom_image with room_id, seq, msgid, and sdk_file_id from that image message. The tool downloads the image into the local workspace and returns the local file path.',
    );
  }

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
        image_sdk_file_id:
          parseStructuredPayload(item.payload)?.image?.sdkfileid?.trim() ?? '',
      })),
      null,
      2,
    ),
  );
  return lines.join('\n');
}

function sanitizePathSegment(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) {
    return 'unknown';
  }
  return trimmed.replace(/[^a-zA-Z0-9_-]+/g, '_');
}

function deriveInternalBaseURL(env: AgentEnv): string | undefined {
  if (env.clawmanInternalBaseURL) {
    return env.clawmanInternalBaseURL.replace(/\/+$/, '');
  }
  if (!env.clawmanGrpcAddr) {
    return undefined;
  }
  try {
    const parsed = new URL(`http://${env.clawmanGrpcAddr}`);
    parsed.port = '8081';
    parsed.pathname = '';
    return parsed.toString().replace(/\/+$/, '');
  } catch {
    return undefined;
  }
}

async function downloadMessageImage(
  env: AgentEnv,
  deps: RuntimeDeps,
  request: {
    roomId: string;
    seq: number;
    msgid: string;
    sdkFileID: string;
  },
): Promise<{ path: string; contentType: string; fileName: string }> {
  const baseURL = deriveInternalBaseURL(env);
  if (!baseURL || !env.clawmanInternalToken) {
    throw new Error(
      'image handling requires CLAWMAN_INTERNAL_BASE_URL or CLAWMAN_GRPC_ADDR plus CLAWMAN_INTERNAL_TOKEN',
    );
  }

  const response = await deps.fetchFn(`${baseURL}/internal/media/fetch`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${env.clawmanInternalToken}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      room_id: request.roomId,
      seq: request.seq,
      msgid: request.msgid,
      sdk_file_id: request.sdkFileID,
    }),
  });
  if (!response.ok) {
    const detail = await response.text();
    throw new Error(`fetch image media failed: ${response.status} ${detail}`);
  }

  const fileName =
    response.headers.get('x-tinyclaw-file-name')?.trim() ||
    `${sanitizePathSegment(request.msgid)}.bin`;
  const contentType =
    response.headers.get('content-type')?.trim() ||
    'application/octet-stream';
  const mediaDir = path.join(
    env.agentWorkdir,
    'incoming-media',
    sanitizePathSegment(request.roomId),
  );
  fs.mkdirSync(mediaDir, { recursive: true });
  const localPath = path.join(mediaDir, fileName);
  const bytes = Buffer.from(await response.arrayBuffer());
  fs.writeFileSync(localPath, bytes);

  return {
    path: localPath,
    contentType,
    fileName,
  };
}

export function createFetchWeComImageTool(
  env: AgentEnv,
  deps: RuntimeDeps,
){
  if (!deriveInternalBaseURL(env) || !env.clawmanInternalToken) {
    return null;
  }
  return sdkTool(
    'fetch_wecom_image',
    'Download a WeCom image attachment for a specific message into the local workspace and return the local file path. Use this when you need to inspect the image contents before answering.',
    {
      room_id: z.string().min(1),
      seq: z.number().int().positive(),
      msgid: z.string().min(1),
      sdk_file_id: z.string().min(1),
    },
    async args => {
      const result = await downloadMessageImage(env, deps, {
        roomId: args.room_id,
        seq: args.seq,
        msgid: args.msgid,
        sdkFileID: args.sdk_file_id,
      });
      return {
        content: [
          {
            type: 'text',
            text: `Downloaded image to ${result.path} (${result.contentType})`,
          },
        ],
        structuredContent: {
          local_path: result.path,
          content_type: result.contentType,
          file_name: result.fileName,
        },
      };
    },
  );
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
  private currentSessionID: string | null = null;

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

    const fetchImageTool = createFetchWeComImageTool(this.env, this.deps);
    const mediaServer = fetchImageTool
      ? createSdkMcpServer({
          name: 'tinyclaw-media',
          tools: [fetchImageTool],
        })
      : undefined;
    const startedAt = this.deps.now();
    const abortController = new AbortController();
    const prompt = buildClaudePrompt(message, fetchImageTool !== null);
    const shouldResume = this.currentSessionID !== null;
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
          message_count: message.messages.length,
          claude_session_mode: shouldResume ? 'resume' : 'create',
          timeout_ms: this.env.claudeRuntimeTimeoutMs,
          model: this.env.claudeModel,
      }),
    );

    console.log(
        JSON.stringify({
          level: 'info',
          msg: 'claude_runtime_query_input',
          room_id: message.roomId,
          msgid: message.msgid,
          message_count: message.messages.length,
          messages: buildMessageSummary(message),
          prompt_length: prompt.length,
          prompt_preview: truncateForLog(prompt, logPreviewLimit),
        }),
    );

    try {
      queryHandle = this.deps.createQuery({
        prompt,
        options: {
          abortController,
          cwd: this.env.agentWorkdir,
          model: this.env.claudeModel,
          maxTurns: this.env.claudeMaxTurns,
          resume: shouldResume ? this.currentSessionID ?? undefined : undefined,
          pathToClaudeCodeExecutable: resolveClaudeCodeExecutable(),
          tools: {
            type: 'preset',
            preset: 'claude_code',
          },
          allowedTools: this.env.claudeAllowedTools,
          disallowedTools: this.env.claudeDisallowedTools,
          mcpServers: mediaServer
            ? {
                tinyclawMedia: mediaServer,
              }
            : undefined,
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
        if (
          sdkMessage.type === 'system' &&
          sdkMessage.subtype === 'init' &&
          typeof sdkMessage.session_id === 'string' &&
          sdkMessage.session_id !== ''
        ) {
          this.currentSessionID = sdkMessage.session_id;
          continue;
        }

        if (sdkMessage.type !== 'result') {
          continue;
        }

        if (
          typeof sdkMessage.session_id === 'string' &&
          sdkMessage.session_id !== ''
        ) {
          this.currentSessionID = sdkMessage.session_id;
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
