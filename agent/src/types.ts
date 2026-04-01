export type AgentRuntimeMode = 'echo' | 'claude_agent_sdk';

export interface AgentEnv {
  serverPort: number;
  clawmanGrpcAddr?: string;
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

export interface AgentRequest {
  msgid: string;
  roomId: string;
  tenantId: string;
  chatType: string;
  messages: AgentMessage[];
}

export interface AgentMessage {
  seq: number;
  msgid: string;
  fromId: string;
  fromName?: string;
  msgTime?: string;
  payload: string;
}

export interface ExecutionResult {
  stdout: string;
  stderr: string;
  exit_code: number;
}
