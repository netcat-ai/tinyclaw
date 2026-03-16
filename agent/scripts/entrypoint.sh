#!/bin/sh
set -eu

runtime_mode="${AGENT_RUNTIME_MODE:-claude_agent_sdk}"
if [ "$runtime_mode" != "echo" ]; then
  if [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
    echo "missing ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN for claude_agent_sdk runtime" >&2
    exit 1
  fi
fi

exec node /app/dist/main.js
