import type { RedisClientType } from 'redis';

import type { AgentEnv } from './types.js';
import { ensureConsumerGroup, pingRedis } from './redis.js';

export async function bootstrapAgent(
  redis: RedisClientType,
  env: AgentEnv,
): Promise<void> {
  await redis.connect();
  await pingRedis(redis);
  await ensureConsumerGroup(redis, env.streamKey, env.consumerGroup);
}
