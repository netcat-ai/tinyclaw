export type AgentRuntimeMode = 'echo' | 'claude_agent_sdk';

export interface AgentEnv {
  serverPort: number;
  anthropicApiKey?: string;
  anthropicBaseUrl?: string;
  claudeCodeOauthToken?: string;
  agentIdleAfterSec: number;
  agentLogLevel: string;
  claudeRuntimeTimeoutMs: number;
  agentWorkdir: string;
  agentTmpdir: string;
  agentRuntimeMode: AgentRuntimeMode;
  claudeModel: string;
  claudeSystemPromptAppend?: string;
  claudeAllowedTools?: string[];
  claudeDisallowedTools?: string[];
  claudeMaxTurns: number;
}

export interface AgentChatRequest {
  msgid: string;
  roomId: string;
  tenantId: string;
  chatType: string;
  text: string;
}

export interface RuntimeResult {
  text: string;
  metadata?: Record<string, unknown>;
}

export interface AgentChatResponse extends RuntimeResult {}
