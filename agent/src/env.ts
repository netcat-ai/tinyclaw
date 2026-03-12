import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

import type { AgentEnv, AgentRuntimeMode } from './types.js';

function loadLocalEnvFiles(): void {
  const mode = process.env.AGENT_LOAD_DOTENV?.trim().toLowerCase();
  if (mode === '0' || mode === 'false' || mode === 'no') {
    return;
  }

  const candidates = [
    path.resolve(process.cwd(), '.env'),
    path.resolve(process.cwd(), '../.env'),
  ];

  for (const filename of candidates) {
    if (!fs.existsSync(filename)) {
      continue;
    }
    process.loadEnvFile(filename);
  }
}

function requireEnv(name: string): string {
  const value = process.env[name]?.trim();
  if (!value) {
    throw new Error(`missing required env: ${name}`);
  }
  return value;
}

function parseInteger(name: string, fallback: number): number {
  const raw = process.env[name]?.trim();
  if (!raw) {
    return fallback;
  }
  const value = Number.parseInt(raw, 10);
  if (Number.isNaN(value)) {
    throw new Error(`invalid integer env ${name}: ${raw}`);
  }
  return value;
}

function parseRuntimeMode(raw?: string): AgentRuntimeMode {
  if (!raw || raw === 'claude_agent_sdk') {
    return 'claude_agent_sdk';
  }
  if (raw === 'echo') {
    return raw;
  }
  throw new Error(`unsupported AGENT_RUNTIME_MODE: ${raw}`);
}

function parseCsv(raw?: string): string[] | undefined {
  if (!raw) {
    return undefined;
  }
  const items = raw
    .split(',')
    .map(item => item.trim())
    .filter(Boolean);
  return items.length > 0 ? items : undefined;
}

export function loadEnv(): AgentEnv {
  loadLocalEnvFiles();

  const roomId = requireEnv('ROOM_ID');
  const tenantId = process.env.TENANT_ID?.trim() || '';
  const chatType = process.env.CHAT_TYPE?.trim() || '';
  const consumerGroupPrefix =
    process.env.CONSUMER_GROUP_PREFIX?.trim() || 'cg:room';

  return {
    roomId,
    tenantId,
    chatType,
    redisAddr: requireEnv('REDIS_ADDR'),
    redisUsername: process.env.REDIS_USERNAME?.trim() || undefined,
    redisPassword: process.env.REDIS_PASSWORD?.trim() || undefined,
    redisDb: parseInteger('REDIS_DB', 0),
    consumerGroupPrefix,
    consumerName: process.env.CONSUMER_NAME?.trim() || os.hostname(),
    anthropicApiKey: process.env.ANTHROPIC_API_KEY?.trim() || undefined,
    anthropicBaseUrl: process.env.ANTHROPIC_BASE_URL?.trim() || undefined,
    claudeCodeOauthToken:
      process.env.CLAUDE_CODE_OAUTH_TOKEN?.trim() || undefined,
    agentIdleAfterSec: parseInteger('AGENT_IDLE_AFTER_SEC', 300),
    agentLogLevel: process.env.AGENT_LOG_LEVEL?.trim() || 'info',
    agentReadBlockMs: parseInteger('AGENT_READ_BLOCK_MS', 5000),
    claudeRuntimeTimeoutMs: parseInteger('CLAUDE_RUNTIME_TIMEOUT_MS', 120000),
    agentWorkdir: process.env.AGENT_WORKDIR?.trim() || '/workspace',
    agentTmpdir: process.env.AGENT_TMPDIR?.trim() || '/tmp',
    agentRuntimeMode: parseRuntimeMode(process.env.AGENT_RUNTIME_MODE?.trim()),
    claudeModel:
      process.env.CLAUDE_MODEL?.trim() ||
      process.env.MODEL_NAME?.trim() ||
      'claude-sonnet-4-5',
    claudeSystemPromptAppend:
      process.env.CLAUDE_SYSTEM_PROMPT_APPEND?.trim() || undefined,
    claudeAllowedTools: parseCsv(process.env.CLAUDE_ALLOWED_TOOLS?.trim()),
    claudeDisallowedTools: parseCsv(process.env.CLAUDE_DISALLOWED_TOOLS?.trim()),
    claudeMaxTurns: parseInteger('CLAUDE_MAX_TURNS', 16),
    streamKey: `stream:i:${roomId}`,
    consumerGroup: `${consumerGroupPrefix}:${roomId}`,
  };
}
