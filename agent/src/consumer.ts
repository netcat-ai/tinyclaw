import type { RedisClientType } from 'redis';

import { WecomEgressClient } from './egress.js';
import { ackMessage, readNextMessage } from './redis.js';
import type { AgentEnv } from './types.js';
import type { AgentRuntime } from './runtime.js';

const MAX_RETRIES = 3;

export class AgentConsumer {
  private stopRequested = false;
  private readonly retryCount = new Map<string, number>();

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
        this.retryCount.delete(message.id);

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
        const retries = (this.retryCount.get(message.id) ?? 0) + 1;
        this.retryCount.set(message.id, retries);

        if (retries >= MAX_RETRIES) {
          // Exceeded retry limit — ACK to prevent infinite reprocessing
          await ackMessage(this.redis, this.env, message.id);
          this.retryCount.delete(message.id);
          console.error(
            JSON.stringify({
              level: 'error',
              msg: 'message_dropped_max_retries',
              room_id: this.env.roomId,
              stream_id: message.id,
              retries,
              error: details,
            }),
          );
        } else {
          console.error(
            JSON.stringify({
              level: 'error',
              msg: 'message_processing_failed',
              room_id: this.env.roomId,
              stream_id: message.id,
              retries,
              error: details,
            }),
          );
        }
      }
    }
  }
}
