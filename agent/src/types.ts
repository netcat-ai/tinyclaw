export type AgentRuntimeMode = 'echo' | 'claude_agent_sdk';

export interface AgentEnv {
  roomId: string;
  tenantId: string;
  chatType: string;
  redisAddr: string;
  redisUsername?: string;
  redisPassword?: string;
  redisDb: number;
  consumerGroupPrefix: string;
  consumerName: string;
  anthropicApiKey?: string;
  anthropicBaseUrl?: string;
  claudeCodeOauthToken?: string;
  agentIdleAfterSec: number;
  agentLogLevel: string;
  agentReadBlockMs: number;
  claudeRuntimeTimeoutMs: number;
  agentWorkdir: string;
  agentTmpdir: string;
  agentRuntimeMode: AgentRuntimeMode;
  claudeModel: string;
  claudeSystemPromptAppend?: string;
  claudeAllowedTools?: string[];
  claudeDisallowedTools?: string[];
  claudeMaxTurns: number;
  streamKey: string;
  consumerGroup: string;
}

export interface RoomStreamMessage {
  streamEntryId: string;
  msgid: string;
  streamKey: string;
  roomId: string;
  tenantId: string;
  chatType: string;
  text: string;
}

export interface RuntimeResult {
  text: string;
  metadata?: Record<string, unknown>;
}
