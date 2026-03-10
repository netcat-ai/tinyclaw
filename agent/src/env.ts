import os from 'node:os';

import type { AgentEnv, AgentRuntimeMode } from './types.js';

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
  if (!raw || raw === 'echo') {
    return 'echo';
  }
  if (raw === 'openai_compat') {
    return raw;
  }
  throw new Error(`unsupported AGENT_RUNTIME_MODE: ${raw}`);
}

export function loadEnv(): AgentEnv {
  const roomId = requireEnv('ROOM_ID');
  const tenantId = requireEnv('TENANT_ID');
  const chatType = requireEnv('CHAT_TYPE');
  const streamPrefix = requireEnv('STREAM_PREFIX');
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
    streamPrefix,
    consumerGroupPrefix,
    consumerName: process.env.CONSUMER_NAME?.trim() || os.hostname(),
    wecomEgressBaseUrl: requireEnv('WECOM_EGRESS_BASE_URL'),
    wecomEgressToken: requireEnv('WECOM_EGRESS_TOKEN'),
    modelApiBaseUrl: requireEnv('MODEL_API_BASE_URL'),
    modelApiKey: requireEnv('MODEL_API_KEY'),
    agentIdleAfterSec: parseInteger('AGENT_IDLE_AFTER_SEC', 300),
    agentLogLevel: process.env.AGENT_LOG_LEVEL?.trim() || 'info',
    agentReadBlockMs: parseInteger('AGENT_READ_BLOCK_MS', 5000),
    agentWorkdir: process.env.AGENT_WORKDIR?.trim() || '/workspace',
    agentTmpdir: process.env.AGENT_TMPDIR?.trim() || '/tmp',
    agentRuntimeMode: parseRuntimeMode(process.env.AGENT_RUNTIME_MODE?.trim()),
    modelApiChatPath:
      process.env.MODEL_API_CHAT_PATH?.trim() || '/v1/chat/completions',
    modelName: process.env.MODEL_NAME?.trim() || 'gpt-4.1-mini',
    streamKey: `${streamPrefix}:${roomId}`,
    consumerGroup: `${consumerGroupPrefix}:${roomId}`,
  };
}
