import { randomUUID } from 'node:crypto';
import fs from 'node:fs';
import fsp from 'node:fs/promises';
import http, { type Server } from 'node:http';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { Readable } from 'node:stream';

import { z } from 'zod';

import type { AgentEnv, AgentRequest, ExecutionResult, FileEntry } from './types.js';
import type { AgentRuntime } from './runtime.js';

const agentMessageSchema = z.object({
  seq: z.number().int().nonnegative(),
  msgid: z.string().min(1).optional(),
  from_id: z.string().optional(),
  from_name: z.string().optional(),
  msg_time: z.string().optional(),
  payload: z.string().min(1),
});

const agentRequestSchema = z.object({
  msgid: z.string().min(1).optional(),
  room_id: z.string().optional(),
  tenant_id: z.string().optional(),
  chat_type: z.string().optional(),
  messages: z.array(agentMessageSchema).min(1),
});

const executeRequestSchema = z.object({
  command: z.string().min(1),
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

async function readMultipartForm(req: http.IncomingMessage): Promise<FormData> {
  const request = new Request('http://sandbox.local/upload', {
    method: req.method,
    headers: req.headers as HeadersInit,
    body: Readable.toWeb(req) as BodyInit,
    duplex: 'half',
  } as RequestInit & { duplex: 'half' });
  return await request.formData();
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

function normalizeAgentRequest(input: z.infer<typeof agentRequestSchema>): AgentRequest {
  return {
    msgid: input.msgid ?? randomUUID(),
    roomId: input.room_id ?? 'sandbox-local',
    tenantId: input.tenant_id ?? '',
    chatType: input.chat_type ?? '',
    messages: input.messages.map(message => ({
      seq: message.seq,
      msgid: message.msgid ?? randomUUID(),
      fromId: message.from_id ?? '',
      fromName: message.from_name,
      msgTime: message.msg_time,
      payload: message.payload,
    })),
  };
}

function stripQuery(url: string): string {
  const question = url.indexOf('?');
  return question >= 0 ? url.slice(0, question) : url;
}

function decodeSandboxPath(prefix: string, url: string): string {
  const rawPath = stripQuery(url).slice(prefix.length);
  const decoded = decodeURIComponent(rawPath || '.');
  return decoded || '.';
}

function resolveSandboxPath(baseDir: string, requestedPath: string): string {
  const root = path.resolve(baseDir);
  const cleanPath = requestedPath.replace(/^\/+/, '') || '.';
  const fullPath = path.resolve(root, cleanPath);
  if (fullPath !== root && !fullPath.startsWith(root + path.sep)) {
    throw new Error('access denied');
  }
  return fullPath;
}

function writeBinary(
  res: http.ServerResponse,
  statusCode: number,
  content: Buffer,
  filename: string,
): void {
  res.writeHead(statusCode, {
    'Content-Type': 'application/octet-stream',
    'Content-Length': content.length,
    'Content-Disposition': `attachment; filename="${filename}"`,
  });
  res.end(content);
}

async function executeCommand(env: AgentEnv, command: string): Promise<ExecutionResult> {
  return await new Promise((resolve) => {
    const child = spawn('sh', ['-lc', command], {
      cwd: env.agentWorkdir,
      env: process.env,
    });

    const stdoutChunks: Buffer[] = [];
    const stderrChunks: Buffer[] = [];
    let settled = false;

    const finish = (result: ExecutionResult) => {
      if (settled) {
        return;
      }
      settled = true;
      resolve(result);
    };

    const timeoutHandle = setTimeout(() => {
      child.kill('SIGTERM');
      setTimeout(() => child.kill('SIGKILL'), 1_000).unref();
      finish({
        stdout: Buffer.concat(stdoutChunks).toString('utf8'),
        stderr: `command timed out after ${env.executeTimeoutMs}ms`,
        exit_code: 124,
      });
    }, env.executeTimeoutMs);

    child.stdout.on('data', chunk => {
      stdoutChunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
    });
    child.stderr.on('data', chunk => {
      stderrChunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
    });
    child.on('error', error => {
      clearTimeout(timeoutHandle);
      finish({
        stdout: '',
        stderr: error.message,
        exit_code: 1,
      });
    });
    child.on('close', code => {
      clearTimeout(timeoutHandle);
      finish({
        stdout: Buffer.concat(stdoutChunks).toString('utf8'),
        stderr: Buffer.concat(stderrChunks).toString('utf8'),
        exit_code: code ?? 1,
      });
    });
  });
}

async function handleAgentRequest(
  req: http.IncomingMessage,
  res: http.ServerResponse,
  runtime: AgentRuntime,
): Promise<void> {
  try {
    const body = await readJsonBody(req);
    const input = agentRequestSchema.parse(body);
    const result = await runtime.run(normalizeAgentRequest(input));
    writeJson(res, 200, result);
  } catch (error) {
    const details = error instanceof Error ? error.message : String(error);
    if (error instanceof SyntaxError || error instanceof z.ZodError) {
      writeJson(res, 400, { error: details });
      return;
    }

    console.error(
      JSON.stringify({
        level: 'error',
        msg: 'agent_request_failed',
        error: details,
      }),
    );
    writeJson(res, 200, {
      stdout: '',
      stderr: details,
      exit_code: 1,
    } satisfies ExecutionResult);
  }
}

async function handleExecuteRequest(
  env: AgentEnv,
  req: http.IncomingMessage,
  res: http.ServerResponse,
): Promise<void> {
  try {
    const body = await readJsonBody(req);
    const input = executeRequestSchema.parse(body);
    const result = await executeCommand(env, input.command);
    writeJson(res, 200, result);
  } catch (error) {
    const details = error instanceof Error ? error.message : String(error);
    const statusCode =
      error instanceof SyntaxError || error instanceof z.ZodError ? 400 : 500;
    writeJson(res, statusCode, { error: details });
  }
}

async function handleUploadRequest(
  env: AgentEnv,
  req: http.IncomingMessage,
  res: http.ServerResponse,
): Promise<void> {
  try {
    const form = await readMultipartForm(req);
    const file = form.get('file');
    if (!(file instanceof File)) {
      writeJson(res, 400, { error: 'multipart form field "file" is required' });
      return;
    }

    const requestedPath = String(form.get('path') ?? path.basename(file.name));
    const targetPath = resolveSandboxPath(env.agentWorkdir, requestedPath);
    await fsp.mkdir(path.dirname(targetPath), { recursive: true });
    await fsp.writeFile(targetPath, Buffer.from(await file.arrayBuffer()));
    writeJson(res, 200, { path: requestedPath, message: 'uploaded' });
  } catch (error) {
    const details = error instanceof Error ? error.message : String(error);
    const statusCode = details === 'access denied' ? 403 : 500;
    writeJson(res, statusCode, { error: details });
  }
}

async function handleDownloadRequest(
  env: AgentEnv,
  req: http.IncomingMessage,
  res: http.ServerResponse,
): Promise<void> {
  try {
    const requestedPath = decodeSandboxPath('/download/', req.url ?? '/download/.');
    const targetPath = resolveSandboxPath(env.agentWorkdir, requestedPath);
    const stats = await fsp.stat(targetPath);
    if (!stats.isFile()) {
      writeJson(res, 404, { error: 'file not found' });
      return;
    }

    writeBinary(res, 200, await fsp.readFile(targetPath), path.basename(targetPath));
  } catch (error) {
    const details = error instanceof Error ? error.message : String(error);
    if ((error as NodeJS.ErrnoException)?.code === 'ENOENT') {
      writeJson(res, 404, { error: 'file not found' });
      return;
    }
    const statusCode = details === 'access denied' ? 403 : 500;
    writeJson(res, statusCode, { error: details });
  }
}

async function handleListRequest(
  env: AgentEnv,
  req: http.IncomingMessage,
  res: http.ServerResponse,
): Promise<void> {
  try {
    const requestedPath = decodeSandboxPath('/list/', req.url ?? '/list/.');
    const targetPath = resolveSandboxPath(env.agentWorkdir, requestedPath);
    const stats = await fsp.stat(targetPath);
    if (!stats.isDirectory()) {
      writeJson(res, 404, { error: 'path is not a directory' });
      return;
    }

    const dirEntries = await fsp.readdir(targetPath, { withFileTypes: true });
    const entries: FileEntry[] = await Promise.all(
      dirEntries.map(async entry => {
        const entryPath = path.join(targetPath, entry.name);
        const entryStats = await fsp.stat(entryPath);
        return {
          name: entry.name,
          size: entryStats.size,
          type: entry.isDirectory() ? 'directory' : 'file',
          mod_time: entryStats.mtimeMs / 1000,
        };
      }),
    );
    writeJson(res, 200, entries);
  } catch (error) {
    const details = error instanceof Error ? error.message : String(error);
    if ((error as NodeJS.ErrnoException)?.code === 'ENOENT') {
      writeJson(res, 404, { error: 'path not found' });
      return;
    }
    const statusCode = details === 'access denied' ? 403 : 500;
    writeJson(res, statusCode, { error: details });
  }
}

async function handleExistsRequest(
  env: AgentEnv,
  req: http.IncomingMessage,
  res: http.ServerResponse,
): Promise<void> {
  try {
    const requestedPath = decodeSandboxPath('/exists/', req.url ?? '/exists/.');
    const targetPath = resolveSandboxPath(env.agentWorkdir, requestedPath);
    const exists = fs.existsSync(targetPath);
    writeJson(res, 200, { path: requestedPath, exists });
  } catch (error) {
    const details = error instanceof Error ? error.message : String(error);
    const statusCode = details === 'access denied' ? 403 : 500;
    writeJson(res, statusCode, { error: details });
  }
}

export function createAgentServer(
  env: AgentEnv,
  runtime: AgentRuntime,
): Server {
  return http.createServer(async (req, res) => {
    const method = req.method ?? 'GET';
    const url = req.url ?? '/';
    const pathname = stripQuery(url);

    if (method === 'GET' && (pathname === '/' || pathname === '/healthz')) {
      writeJson(res, 200, {
        status: 'ok',
        runtime_mode: env.agentRuntimeMode,
      });
      return;
    }

    if (method === 'POST' && pathname === '/agent') {
      await handleAgentRequest(req, res, runtime);
      return;
    }

    if (method === 'POST' && pathname === '/execute') {
      await handleExecuteRequest(env, req, res);
      return;
    }

    if (method === 'POST' && pathname === '/upload') {
      await handleUploadRequest(env, req, res);
      return;
    }

    if (method === 'GET' && pathname.startsWith('/download/')) {
      await handleDownloadRequest(env, req, res);
      return;
    }

    if (method === 'GET' && pathname.startsWith('/list/')) {
      await handleListRequest(env, req, res);
      return;
    }

    if (method === 'GET' && pathname.startsWith('/exists/')) {
      await handleExistsRequest(env, req, res);
      return;
    }

    writeJson(res, 404, { error: 'not_found' });
  });
}
