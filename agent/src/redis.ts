import { createClient, type RedisClientType } from 'redis';

import type { AgentEnv, RoomStreamMessage } from './types.js';

export class InvalidIngressMessageError extends Error {
  constructor(
    readonly streamId: string,
    message: string,
  ) {
    super(message);
    this.name = 'InvalidIngressMessageError';
  }
}

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

function parseIngressMessage(fields: Record<string, string>): {
  msgid: string;
  text: string;
} {
  const msgid = fields.msgid?.trim();
  const kind = fields.kind?.trim();
  const raw = fields.raw?.trim();

  if (!msgid) {
    throw new Error('ingress message missing msgid');
  }
  if (kind !== 'wecom') {
    throw new Error(`unsupported ingress kind: ${kind || '(empty)'}`);
  }
  if (!raw) {
    throw new Error('ingress message missing raw payload');
  }

  try {
    const parsed = JSON.parse(raw) as {
      text?: { content?: unknown };
      markdown?: { content?: unknown };
      image?: { url?: unknown };
      file?: { name?: unknown };
      msgtype?: unknown;
    };

    const text =
      selectString(parsed.text?.content) ||
      selectString(parsed.markdown?.content) ||
      selectString(parsed.image?.url) ||
      selectString(parsed.file?.name);
    if (text) {
      return {
        msgid,
        text,
      };
    }

    const msgType = selectString(parsed.msgtype);
    if (msgType) {
      return {
        msgid,
        text: `[${msgType}]`,
      };
    }
  } catch {
    throw new Error('invalid wecom raw payload');
  }

  throw new Error('unsupported wecom message payload');
}

function selectString(value: unknown): string {
  return typeof value === 'string' ? value : '';
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
  let ingress;
  try {
    ingress = parseIngressMessage(rawFields);
  } catch (error) {
    const details = error instanceof Error ? error.message : String(error);
    throw new InvalidIngressMessageError(entry.id, details);
  }

  return {
    streamEntryId: entry.id,
    msgid: ingress.msgid,
    streamKey: env.streamKey,
    roomId: env.roomId,
    tenantId: env.tenantId,
    chatType: env.chatType,
    text: ingress.text,
  };
}

export async function ackMessage(
  redis: RedisClientType,
  env: AgentEnv,
  messageId: string,
): Promise<void> {
  await redis.xAck(env.streamKey, env.consumerGroup, messageId);
}
