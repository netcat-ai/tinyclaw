#!/bin/sh
set -eu

: "${ROOM_ID:?missing ROOM_ID}"
: "${TENANT_ID:?missing TENANT_ID}"
: "${CHAT_TYPE:?missing CHAT_TYPE}"
: "${REDIS_ADDR:?missing REDIS_ADDR}"
: "${STREAM_PREFIX:?missing STREAM_PREFIX}"
: "${WECOM_EGRESS_BASE_URL:?missing WECOM_EGRESS_BASE_URL}"
: "${WECOM_EGRESS_TOKEN:?missing WECOM_EGRESS_TOKEN}"

runtime_mode="${AGENT_RUNTIME_MODE:-claude_agent_sdk}"
if [ "$runtime_mode" != "echo" ]; then
  if [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && [ -z "${MODEL_API_KEY:-}" ]; then
    echo "missing ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN for claude_agent_sdk runtime" >&2
    exit 1
  fi
fi

exec node /app/dist/main.js
