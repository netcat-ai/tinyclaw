import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import * as grpc from '@grpc/grpc-js';
import * as protoLoader from '@grpc/proto-loader';

import type { AgentEnv, AgentRequest, ExecutionResult } from './types.js';
import type { AgentRuntime } from './runtime.js';

type ProtoMessage = {
  kind?: string;
  sandboxId?: string;
  roomId?: string;
  requestId?: string;
  messages?: Array<{
    seq?: number | string;
    msgid?: string;
    roomId?: string;
    fromId?: string;
    fromName?: string;
    msgTime?: string;
    payload?: string;
  }>;
};

type ProtoConnectStream = grpc.ClientDuplexStream<any, any>;

const runtimeDir = path.dirname(fileURLToPath(import.meta.url));
const protoPath = path.resolve(runtimeDir, '../../proto/clawman/v1/clawman.proto');

function loadClientConstructor(): grpc.ServiceClientConstructor {
  const definition = protoLoader.loadSync(protoPath, {
    longs: String,
    enums: String,
    defaults: true,
    oneofs: true,
  });
  const descriptor = grpc.loadPackageDefinition(definition) as any;
  return descriptor.clawman.v1.Clawman as grpc.ServiceClientConstructor;
}

function normalizeBatch(message: ProtoMessage): AgentRequest {
  const roomId = String(message.messages?.[0]?.roomId ?? '');
  const messages = (message.messages ?? []).map(item => ({
    seq: Number(item.seq ?? 0),
    msgid: String(item.msgid ?? ''),
    fromId: String(item.fromId ?? ''),
    fromName: item.fromName ? String(item.fromName) : undefined,
    msgTime: item.msgTime ? String(item.msgTime) : undefined,
    payload: String(item.payload ?? ''),
  }));

  return {
    msgid: String(message.requestId ?? ''),
    roomId,
    tenantId: '',
    chatType: '',
    messages,
  };
}

async function processBatch(
  stream: ProtoConnectStream,
  runtime: AgentRuntime,
  message: ProtoMessage,
): Promise<void> {
  const request = normalizeBatch(message);
  try {
    const result: ExecutionResult = await runtime.run(request);
    stream.write({
      kind: 'result',
      requestId: String(message.requestId ?? ''),
      output: result.stdout,
    });
  } catch (error) {
    const details = error instanceof Error ? error.message : String(error);
    stream.write({
      kind: 'error',
      requestId: String(message.requestId ?? ''),
      error: details,
    });
  }
}

async function connectOnce(env: AgentEnv, runtime: AgentRuntime): Promise<void> {
  if (!env.clawmanGrpcAddr) {
    throw new Error('CLAWMAN_GRPC_ADDR is required for gRPC bridge');
  }
  const ClientCtor = loadClientConstructor();
  const client = new ClientCtor(env.clawmanGrpcAddr, grpc.credentials.createInsecure()) as any;
  const stream = client.RoomChat() as ProtoConnectStream;

  stream.write({
    kind: 'connect',
    sandboxId: os.hostname(),
  });

  let queue = Promise.resolve();

  await new Promise<void>((resolve, reject) => {
    stream.on('data', (message: ProtoMessage) => {
      if (message?.kind !== 'messages') {
        return;
      }
      queue = queue.then(() => processBatch(stream, runtime, message));
    });
    stream.once('end', () => {
      queue.finally(resolve);
    });
    stream.once('close', () => {
      queue.finally(resolve);
    });
    stream.once('error', reject);
  }).finally(() => {
    if (typeof client.close === 'function') {
      client.close();
    }
  });
}

export async function runGrpcBridge(env: AgentEnv, runtime: AgentRuntime): Promise<void> {
  for (;;) {
    try {
      await connectOnce(env, runtime);
    } catch (error) {
      const details = error instanceof Error ? error.message : String(error);
      console.error(
        JSON.stringify({
          level: 'error',
          msg: 'clawman_grpc_bridge_failed',
          error: details,
        }),
      );
      await new Promise(resolve => setTimeout(resolve, 1000));
    }
  }
}
