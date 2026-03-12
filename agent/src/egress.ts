import type { RedisClientType } from 'redis';

import type { AgentEnv, RoomStreamMessage, RuntimeResult } from './types.js';

export class WecomEgressClient {
  private readonly streamKey: string;

  constructor(
    private readonly redis: RedisClientType,
    private readonly env: AgentEnv,
  ) {
    this.streamKey = `stream:o:${env.roomId}`;
  }

  async sendReply(
    message: RoomStreamMessage,
    result: RuntimeResult,
  ): Promise<void> {
    await this.redis.xAdd(this.streamKey, '*', {
      room_id: this.env.roomId,
      text: result.text,
      msgid: message.msgid,
    });
  }
}
