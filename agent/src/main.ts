import { bootstrapAgent } from './bootstrap.js';
import { AgentConsumer } from './consumer.js';
import { loadEnv } from './env.js';
import { WecomEgressClient } from './egress.js';
import { createRedisClient } from './redis.js';
import { createRuntime } from './runtime.js';

const env = loadEnv();
const redis = createRedisClient(env);
const runtime = createRuntime(env);
const egress = new WecomEgressClient(env);
const consumer = new AgentConsumer(redis, env, runtime, egress);

let shutdownRequested = false;

function requestShutdown(signal: string): void {
  if (shutdownRequested) {
    return;
  }

  shutdownRequested = true;
  console.log(
    JSON.stringify({
      level: 'info',
      msg: 'shutdown_requested',
      signal,
      room_id: env.roomId,
    }),
  );
  consumer.requestStop();
}

process.on('SIGTERM', () => requestShutdown('SIGTERM'));
process.on('SIGINT', () => requestShutdown('SIGINT'));

try {
  await bootstrapAgent(redis, env);
  console.log(
    JSON.stringify({
      level: 'info',
      msg: 'agent_ready',
      room_id: env.roomId,
      stream_key: env.streamKey,
      consumer_group: env.consumerGroup,
      consumer_name: env.consumerName,
      runtime_mode: env.agentRuntimeMode,
    }),
  );
  await consumer.run();
} catch (error) {
  const details = error instanceof Error ? error.message : String(error);
  console.error(
    JSON.stringify({
      level: 'error',
      msg: 'agent_fatal',
      room_id: env.roomId,
      error: details,
    }),
  );
  process.exitCode = 1;
} finally {
  try {
    await redis.quit();
  } catch {
    await redis.disconnect();
  }
}
