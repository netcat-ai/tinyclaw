import fs from 'node:fs';
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

  return {
    serverPort: parseInteger('AGENT_SERVER_PORT', 8888),
    clawmanGrpcAddr: process.env.CLAWMAN_GRPC_ADDR?.trim() || undefined,
    clawmanInternalBaseURL:
      process.env.CLAWMAN_INTERNAL_BASE_URL?.trim() || undefined,
    clawmanInternalToken:
      process.env.CLAWMAN_INTERNAL_TOKEN?.trim() || undefined,
    anthropicApiKey: process.env.ANTHROPIC_API_KEY?.trim() || undefined,
    anthropicBaseUrl: process.env.ANTHROPIC_BASE_URL?.trim() || undefined,
    claudeCodeOauthToken:
      process.env.CLAUDE_CODE_OAUTH_TOKEN?.trim() || undefined,
    agentIdleAfterSec: parseInteger('AGENT_IDLE_AFTER_SEC', 300),
    agentLogLevel: process.env.AGENT_LOG_LEVEL?.trim() || 'info',
    claudeRuntimeTimeoutMs: parseInteger('CLAUDE_RUNTIME_TIMEOUT_MS', 120000),
    agentWorkdir: process.env.AGENT_WORKDIR?.trim() || process.cwd(),
    agentTmpdir: process.env.AGENT_TMPDIR?.trim() || '/tmp',
    agentRuntimeMode: parseRuntimeMode(process.env.AGENT_RUNTIME_MODE?.trim()),
    claudeModel:
      process.env.CLAUDE_MODEL?.trim() || 'claude-sonnet-4-6',
    claudeSystemPromptAppend:
      process.env.CLAUDE_SYSTEM_PROMPT_APPEND?.trim() || undefined,
    claudeAllowedTools: parseCsv(process.env.CLAUDE_ALLOWED_TOOLS?.trim()),
    claudeDisallowedTools: parseCsv(process.env.CLAUDE_DISALLOWED_TOOLS?.trim()),
    claudeMaxTurns: parseInteger('CLAUDE_MAX_TURNS', 16),
  };
}
