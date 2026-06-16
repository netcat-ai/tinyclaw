#!/usr/bin/env bash
set -euo pipefail

base_url="${CLAWMAN_BASE_URL:-http://127.0.0.1:8081}"
token="${CLAWMAN_API_TOKEN:-dev-token}"
channel="${TINYCLAW_SMOKE_CHANNEL:-local-smoke-$(date +%s%N)}"
message="${TINYCLAW_SMOKE_MESSAGE:-请只回复：tinyclaw local smoke ok}"
ack="${TINYCLAW_SMOKE_ACK:-true}"

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

curl -fsS "${base_url}/healthz" >/dev/null

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

room_id="$(
  curl -fsS \
    -H "Authorization: Bearer ${token}" \
    -H "Content-Type: application/json" \
    -d "${room_payload}" \
    "${base_url}/api/rooms" |
    json_get '["room"]["id"]'
)"

msgid="${channel}-msg-$(date +%s%N)"
message_payload="$(
  python3 - "${room_id}" "${msgid}" "${channel}" "${message}" <<'PY'
import json
import sys
import time

room_id = int(sys.argv[1])
msgid = sys.argv[2]
channel = sys.argv[3]
message = sys.argv[4]

print(json.dumps({
    "room_id": room_id,
    "source": "local",
    "msgid": msgid,
    "action": "send",
    "from": "local-smoke",
    "tolist": ["tinyclaw"],
    "roomid": f"{channel}-room",
    "msgtime": int(time.time()),
    "msgtype": "text",
    "body": {"content": message},
}, ensure_ascii=False))
PY
)"

curl -fsS \
  -H "Authorization: Bearer ${token}" \
  -H "Content-Type: application/json" \
  -d "${message_payload}" \
  "${base_url}/api/messages" >/dev/null

delivery=""
for _ in $(seq 1 24); do
  delivery="$(
    curl -fsS \
      -H "Authorization: Bearer ${token}" \
      "${base_url}/api/deliveries?channels=${channel}"
  )"
  count="$(printf "%s" "${delivery}" | json_get '["deliveries"].__len__()')"
  if [[ "${count}" != "0" ]]; then
    break
  fi
  sleep 5
done

count="$(printf "%s" "${delivery}" | json_get '["deliveries"].__len__()')"
if [[ "${count}" == "0" ]]; then
  echo "timed out waiting for delivery on channel ${channel}" >&2
  exit 1
fi

printf "%s\n" "${delivery}" | python3 -m json.tool

delivery_id="$(printf "%s" "${delivery}" | json_get '["deliveries"][0]["id"]')"
kind="$(printf "%s" "${delivery}" | json_get '["deliveries"][0]["payload"].get("kind", "")')"
if [[ "${kind}" != "agent_output" ]]; then
  echo "delivery kind is ${kind}, expected agent_output" >&2
  exit 1
fi

if [[ "${ack}" == "true" ]]; then
  curl -fsS \
    -X POST \
    -H "Authorization: Bearer ${token}" \
    "${base_url}/api/deliveries/${delivery_id}/ack" >/dev/null

  after_ack="$(
    curl -fsS \
      -H "Authorization: Bearer ${token}" \
      "${base_url}/api/deliveries?channels=${channel}"
  )"
  if python3 - "${delivery_id}" "${after_ack}" <<'PY'
import json
import sys

delivery_id = int(sys.argv[1])
payload = json.loads(sys.argv[2])
if any(delivery.get("id") == delivery_id for delivery in payload.get("deliveries", [])):
    raise SystemExit(1)
PY
  then
    :
  else
    echo "delivery ${delivery_id} is still pending after ack" >&2
    exit 1
  fi
fi
