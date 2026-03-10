import type { AgentEnv, RoomStreamMessage, RuntimeResult } from './types.js';

export class WecomEgressClient {
  constructor(private readonly env: AgentEnv) {}

  async sendReply(
    message: RoomStreamMessage,
    result: RuntimeResult,
  ): Promise<void> {
    const response = await fetch(this.env.wecomEgressBaseUrl, {
      method: 'POST',
      headers: {
        'content-type': 'application/json',
        authorization: `Bearer ${this.env.wecomEgressToken}`,
      },
      body: JSON.stringify({
        room_id: this.env.roomId,
        tenant_id: this.env.tenantId,
        chat_type: this.env.chatType,
        source: {
          stream_id: message.id,
          trace_id: message.traceId ?? null,
        },
        input: {
          text: message.text,
          raw_fields: message.rawFields,
        },
        reply: {
          text: result.text,
          metadata: result.metadata ?? {},
        },
      }),
    });

    if (!response.ok) {
      const body = await response.text();
      throw new Error(
        `egress request failed: ${response.status} ${response.statusText} ${body}`,
      );
    }
  }
}
