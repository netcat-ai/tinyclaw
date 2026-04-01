import http, { type Server } from 'node:http';

import type { AgentEnv } from './types.js';

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

export function createAgentServer(env: AgentEnv): Server {
  return http.createServer((req, res) => {
    const method = req.method ?? 'GET';
    const pathname = new URL(req.url ?? '/', 'http://127.0.0.1').pathname;

    if (method === 'GET' && pathname === '/healthz') {
      writeJson(res, 200, {
        status: 'ok',
        runtime_mode: env.agentRuntimeMode,
      });
      return;
    }

    writeJson(res, 404, { error: 'not found' });
  });
}
