#!/usr/bin/env bash
set -euo pipefail

base_url="${CLAWMAN_BASE_URL:-http://127.0.0.1:8081}"
admin_user="${TINYCLAW_ADMIN_USER:-admin}"
admin_secret="${CLAWMAN_ADMIN_SECRET:-dev-admin}"
channel="${TINYCLAW_ADMIN_SMOKE_CHANNEL:-admin-smoke-$(date +%s%N)}"

json_get() {
  python3 -c "import json,sys; print(json.load(sys.stdin)$1)"
}

json_payload() {
  python3 - "$@" <<'PY'
import json
import sys

payload = json.loads(sys.argv[1])
print(json.dumps(payload, ensure_ascii=False))
PY
}

request() {
  local method="$1"
  local path="$2"
  local body="${3:-}"
  if [[ "${body}" == "" ]]; then
    curl -fsS -u "${admin_user}:${admin_secret}" -X "${method}" "${base_url}${path}"
    return
  fi
  curl -fsS \
    -u "${admin_user}:${admin_secret}" \
    -X "${method}" \
    -H "Content-Type: application/json" \
    -d "${body}" \
    "${base_url}${path}"
}

curl -fsS "${base_url}/healthz" >/dev/null

rooms="$(request GET "/admin/api/rooms?limit=5")"
printf "%s" "${rooms}" | json_get '["rooms"].__len__()' >/dev/null

agent_payload="$(
  json_payload "{
    \"key\":\"admin_smoke_${channel//-/_}\",
    \"display_name\":\"Admin Smoke\",
    \"description\":\"Local admin smoke agent\",
    \"prompt\":\"You are used by local admin smoke checks.\",
    \"allowed_tools\":[],
    \"enabled\":true
  }"
)"
agent="$(request POST "/admin/api/agents" "${agent_payload}")"
agent_id="$(printf "%s" "${agent}" | json_get '["agent"]["id"]')"

updated_agent_payload="$(
  json_payload "{
    \"key\":\"admin_smoke_${channel//-/_}\",
    \"display_name\":\"Admin Smoke\",
    \"description\":\"Local admin smoke agent updated\",
    \"prompt\":\"You are used by local admin smoke checks after update.\",
    \"allowed_tools\":[],
    \"enabled\":false
  }"
)"
updated_agent="$(request PUT "/admin/api/agents/${agent_id}" "${updated_agent_payload}")"
enabled="$(printf "%s" "${updated_agent}" | json_get '["agent"]["enabled"]')"
if [[ "${enabled}" != "False" ]]; then
  echo "updated agent enabled=${enabled}, expected False" >&2
  exit 1
fi

room_payload="$(
  json_payload "{
    \"channel\":\"${channel}\",
    \"channel_room_id\":\"${channel}-room\",
    \"channel_room_type\":\"group\",
    \"display_name\":\"${channel}\",
    \"outbound_alias\":\"${channel}\",
    \"agent_enabled\":true,
    \"trigger_policy\":{\"mode\":\"always\"}
  }"
)"
room="$(request POST "/admin/api/rooms" "${room_payload}")"
room_id="$(printf "%s" "${room}" | json_get '["room"]["id"]')"

inject_payload="$(
  json_payload "{
    \"sender_id\":\"admin-smoke\",
    \"text\":\"admin smoke message\",
    \"suppress_agent_trigger\":true
  }"
)"
injected="$(request POST "/admin/api/rooms/${room_id}/messages:inject" "${inject_payload}")"
triggered="$(printf "%s" "${injected}" | json_get '["triggered"]')"
if [[ "${triggered}" != "False" ]]; then
  echo "admin injected message triggered=${triggered}, expected False" >&2
  exit 1
fi

timeline="$(request GET "/admin/api/rooms/${room_id}/timeline?limit=20")"
message_count="$(printf "%s" "${timeline}" | json_get '["messages"].__len__()')"
if [[ "${message_count}" == "0" ]]; then
  echo "admin timeline did not include injected message" >&2
  exit 1
fi

memory="$(request GET "/admin/api/rooms/${room_id}/memory?status=active&limit=20")"
printf "%s" "${memory}" | json_get '["items"].__len__()' >/dev/null

printf "%s\n" "${timeline}" | python3 -m json.tool
