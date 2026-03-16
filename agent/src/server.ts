import http, { type Server } from 'node:http';

import { z } from 'zod';

import type {
  AgentChatRequest,
  AgentChatResponse,
  AgentEnv,
} from './types.js';
import type { AgentRuntime } from './runtime.js';

const chatRequestSchema = z.object({
  msgid: z.string().min(1),
  room_id: z.string().min(1),
  tenant_id: z.string().default(''),
  chat_type: z.string().default(''),
  text: z.string().min(1),
});

async function readJsonBody(req: http.IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];

  for await (const chunk of req) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
  }

  const body = Buffer.concat(chunks).toString('utf8');
  if (!body.trim()) {
    return {};
  }

  return JSON.parse(body);
}

function writeJson(
  res: http.ServerResponse,
  statusCode: number,
  payload: unknown,
): void {
  const body = JSON.stringify(payload);
  res.writeHead(statusCode, {
    'Content-Type': 'application/json',
    'Content-Length': Buffer.byteLength(body),
  });
  res.end(body);
}

function normalizeRequest(input: z.infer<typeof chatRequestSchema>): AgentChatRequest {
  return {
    msgid: input.msgid,
    roomId: input.room_id,
    tenantId: input.tenant_id,
    chatType: input.chat_type,
    text: input.text,
  };
}

export function createAgentServer(
  env: AgentEnv,
  runtime: AgentRuntime,
): Server {
  return http.createServer(async (req, res) => {
    const method = req.method ?? 'GET';
    const url = req.url ?? '/';

    if (method === 'GET' && (url === '/' || url === '/healthz')) {
      writeJson(res, 200, {
        status: 'ok',
        runtime_mode: env.agentRuntimeMode,
      });
      return;
    }

    if (method === 'POST' && url === '/v1/chat') {
      try {
        const body = await readJsonBody(req);
        const input = chatRequestSchema.parse(body);
        const result: AgentChatResponse = await runtime.run(normalizeRequest(input));
        writeJson(res, 200, result);
      } catch (error) {
        const details = error instanceof Error ? error.message : String(error);
        const statusCode =
          error instanceof SyntaxError || error instanceof z.ZodError ? 400 : 500;
        console.error(
          JSON.stringify({
            level: 'error',
            msg: 'chat_request_failed',
            error: details,
          }),
        );
        writeJson(res, statusCode, { error: details });
      }
      return;
    }

    writeJson(res, 404, { error: 'not_found' });
  });
}
