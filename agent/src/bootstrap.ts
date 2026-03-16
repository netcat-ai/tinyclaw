import fs from 'node:fs';

import type { AgentEnv } from './types.js';

export async function bootstrapAgent(env: AgentEnv): Promise<void> {
  fs.mkdirSync(env.agentWorkdir, { recursive: true });
  fs.mkdirSync(env.agentTmpdir, { recursive: true });
}
