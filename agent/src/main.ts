import { bootstrapAgent } from './bootstrap.js';
import { loadEnv } from './env.js';
import { runGrpcBridge } from './grpc.js';
import { createRuntime } from './runtime.js';
import { createAgentServer } from './server.js';

const env = loadEnv();
const runtime = createRuntime(env);
const server = createAgentServer(env);

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
    }),
  );

  server.close(error => {
    if (error) {
      console.error(
        JSON.stringify({
          level: 'error',
          msg: 'agent_shutdown_failed',
          error: error.message,
        }),
      );
      process.exitCode = 1;
      return;
    }

    console.log(
      JSON.stringify({
        level: 'info',
        msg: 'agent_shutdown_complete',
      }),
    );
  });
}

process.on('SIGTERM', () => requestShutdown('SIGTERM'));
process.on('SIGINT', () => requestShutdown('SIGINT'));

try {
  await bootstrapAgent(env);

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(env.serverPort, () => {
      server.off('error', reject);
      const address = server.address();
      const port =
        typeof address === 'object' && address ? address.port : env.serverPort;

      console.log(
        JSON.stringify({
          level: 'info',
          msg: 'agent_ready',
          server_port: port,
          runtime_mode: env.agentRuntimeMode,
        }),
      );
      resolve();
    });
  });

  void runGrpcBridge(env, runtime);
} catch (error) {
  const details = error instanceof Error ? error.message : String(error);
  console.error(
    JSON.stringify({
      level: 'error',
      msg: 'agent_fatal',
      error: details,
    }),
  );
  process.exitCode = 1;
}
