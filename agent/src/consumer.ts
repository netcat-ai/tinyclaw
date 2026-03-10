import type { RedisClientType } from 'redis';

import { WecomEgressClient } from './egress.js';
import { ackMessage, readNextMessage } from './redis.js';
import type { AgentEnv } from './types.js';
import type { AgentRuntime } from './runtime.js';

export class AgentConsumer {
  private stopRequested = false;

  constructor(
    private readonly redis: RedisClientType,
    private readonly env: AgentEnv,
    private readonly runtime: AgentRuntime,
    private readonly egress: WecomEgressClient,
  ) {}

  requestStop(): void {
    this.stopRequested = true;
  }

  async run(): Promise<void> {
    while (!this.stopRequested) {
      const message = await readNextMessage(
        this.redis,
        this.env,
        this.env.agentReadBlockMs,
      );
      if (!message) {
        continue;
      }

      console.log(
        JSON.stringify({
          level: 'info',
          msg: 'message_received',
          room_id: this.env.roomId,
          stream_id: message.id,
          trace_id: message.traceId ?? null,
        }),
      );

      try {
        const result = await this.runtime.run(message);
        await this.egress.sendReply(message, result);
        await ackMessage(this.redis, this.env, message.id);

        console.log(
          JSON.stringify({
            level: 'info',
            msg: 'message_acked',
            room_id: this.env.roomId,
            stream_id: message.id,
          }),
        );
      } catch (error) {
        const details = error instanceof Error ? error.message : String(error);
        console.error(
          JSON.stringify({
            level: 'error',
            msg: 'message_processing_failed',
            room_id: this.env.roomId,
            stream_id: message.id,
            error: details,
          }),
        );
      }
    }
  }
}
