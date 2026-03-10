import { createClient, type RedisClientType } from 'redis';

import type { AgentEnv, RoomStreamMessage } from './types.js';

function parseRedisAddress(addr: string): { host: string; port: number } {
  if (addr.startsWith('redis://') || addr.startsWith('rediss://')) {
    const url = new URL(addr);
    return {
      host: url.hostname,
      port: Number.parseInt(url.port || '6379', 10),
    };
  }

  const separator = addr.lastIndexOf(':');
  if (separator === -1) {
    return { host: addr, port: 6379 };
  }

  const host = addr.slice(0, separator);
  const port = Number.parseInt(addr.slice(separator + 1), 10);
  if (Number.isNaN(port)) {
    throw new Error(`invalid REDIS_ADDR port: ${addr}`);
  }
  return { host, port };
}

function flattenFields(
  fields: Map<string, string> | Record<string, string>,
): Record<string, string> {
  const record: Record<string, string> = {};

  if (fields instanceof Map) {
    for (const [key, value] of fields.entries()) {
      record[String(key)] = String(value);
    }
    return record;
  }

  for (const [key, value] of Object.entries(fields)) {
    record[key] = String(value);
  }
  return record;
}

function selectText(fields: Record<string, string>): string {
  const direct = fields.text || fields.content || fields.prompt || fields.body;
  if (direct) {
    return direct;
  }
  return JSON.stringify(fields);
}

export function createRedisClient(env: AgentEnv): RedisClientType {
  if (env.redisAddr.startsWith('redis://') || env.redisAddr.startsWith('rediss://')) {
    return createClient({
      url: env.redisAddr,
      username: env.redisUsername,
      password: env.redisPassword,
      database: env.redisDb,
    });
  }

  const { host, port } = parseRedisAddress(env.redisAddr);
  return createClient({
    socket: {
      host,
      port,
    },
    username: env.redisUsername,
    password: env.redisPassword,
    database: env.redisDb,
  });
}

export async function pingRedis(redis: RedisClientType): Promise<void> {
  await redis.ping();
}

export async function ensureConsumerGroup(
  redis: RedisClientType,
  streamKey: string,
  consumerGroup: string,
): Promise<void> {
  try {
    await redis.xGroupCreate(streamKey, consumerGroup, '0', {
      MKSTREAM: true,
    });
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    if (!message.includes('BUSYGROUP')) {
      throw error;
    }
  }
}

export async function readNextMessage(
  redis: RedisClientType,
  env: AgentEnv,
  blockMs: number,
): Promise<RoomStreamMessage | null> {
  const response = await redis.xReadGroup(
    env.consumerGroup,
    env.consumerName,
    {
      key: env.streamKey,
      id: '>',
    },
    {
      COUNT: 1,
      BLOCK: blockMs,
    },
  );

  if (!response || response.length === 0) {
    return null;
  }

  const [{ messages: entries }] = response;
  if (!entries || entries.length === 0) {
    return null;
  }

  const [entry] = entries;
  const rawFields = flattenFields(
    entry.message as Map<string, string> | Record<string, string>,
  );

  return {
    id: entry.id,
    streamKey: env.streamKey,
    roomId: env.roomId,
    tenantId: env.tenantId,
    chatType: env.chatType,
    traceId: rawFields.trace_id,
    text: selectText(rawFields),
    rawFields,
  };
}

export async function ackMessage(
  redis: RedisClientType,
  env: AgentEnv,
  messageId: string,
): Promise<void> {
  await redis.xAck(env.streamKey, env.consumerGroup, messageId);
}
