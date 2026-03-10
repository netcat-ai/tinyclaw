export type AgentRuntimeMode = 'echo' | 'openai_compat';

export interface AgentEnv {
  roomId: string;
  tenantId: string;
  chatType: string;
  redisAddr: string;
  redisUsername?: string;
  redisPassword?: string;
  redisDb: number;
  streamPrefix: string;
  consumerGroupPrefix: string;
  consumerName: string;
  wecomEgressBaseUrl: string;
  wecomEgressToken: string;
  modelApiBaseUrl: string;
  modelApiKey: string;
  agentIdleAfterSec: number;
  agentLogLevel: string;
  agentReadBlockMs: number;
  agentWorkdir: string;
  agentTmpdir: string;
  agentRuntimeMode: AgentRuntimeMode;
  modelApiChatPath: string;
  modelName: string;
  streamKey: string;
  consumerGroup: string;
}

export interface RoomStreamMessage {
  id: string;
  streamKey: string;
  roomId: string;
  tenantId: string;
  chatType: string;
  traceId?: string;
  text: string;
  rawFields: Record<string, string>;
}

export interface RuntimeResult {
  text: string;
  metadata?: Record<string, unknown>;
}
