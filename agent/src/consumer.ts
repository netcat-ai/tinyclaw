import type { RedisClientType } from 'redis';

import { WecomEgressClient } from './egress.js';
import { ackMessage, InvalidIngressMessageError, readNextMessage } from './redis.js';
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
      let currentMessage: Awaited<ReturnType<typeof readNextMessage>> = null;
      try {
        currentMessage = await readNextMessage(
          this.redis,
          this.env,
          this.env.agentReadBlockMs,
        );
        if (!currentMessage) {
          continue;
        }

        console.log(
          JSON.stringify({
            level: 'info',
            msg: 'message_received',
            room_id: this.env.roomId,
            stream_id: currentMessage.streamEntryId,
            msgid: currentMessage.msgid,
          }),
        );

        const result = await this.runtime.run(currentMessage);
        await this.egress.sendReply(currentMessage, result);
        await ackMessage(this.redis, this.env, currentMessage.streamEntryId);

        console.log(
          JSON.stringify({
            level: 'info',
            msg: 'message_acked',
            room_id: this.env.roomId,
            stream_id: currentMessage.streamEntryId,
            msgid: currentMessage.msgid,
          }),
        );
      } catch (error) {
        const details = error instanceof Error ? error.message : String(error);
        const streamId =
          error instanceof InvalidIngressMessageError
            ? error.streamId
            : currentMessage?.streamEntryId ?? null;
        console.error(
          JSON.stringify({
            level: 'error',
            msg: 'message_processing_failed',
            room_id: this.env.roomId,
            stream_id: streamId,
            msgid: currentMessage?.msgid ?? null,
            error: details,
          }),
        );
      }
    }
  }
}
