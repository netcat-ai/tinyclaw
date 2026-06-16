#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
base_url="${CLAWMAN_BASE_URL:-http://127.0.0.1:8081}"
token="${CLAWMAN_API_TOKEN:-dev-token}"
admin_user="${TINYCLAW_ADMIN_USER:-admin}"
admin_secret="${CLAWMAN_ADMIN_SECRET:-dev-admin}"
channel="${TINYCLAW_WECHAT_SMOKE_CHANNEL:-wechat-smoke-$(date +%s%N)}"
room_username="${channel}@chatroom"
message_text="${TINYCLAW_WECHAT_SMOKE_MESSAGE:-虾虾，wechat adapter smoke}"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

json_get() {
  python3 -c "import json,sys; print(json.load(sys.stdin)$1)"
}

fake_wx="${tmp_dir}/wx"
cat >"${fake_wx}" <<'SH'
#!/usr/bin/env sh
set -eu

if [ "$1" = "history" ]; then
  now="$(date +%s)"
  python3 - "$WECHAT_SMOKE_ROOM_USERNAME" "$WECHAT_SMOKE_CHANNEL" "$WECHAT_SMOKE_MESSAGE" "$now" <<'PY'
import json
import sys

room_username = sys.argv[1]
channel = sys.argv[2]
message = sys.argv[3]
now = int(sys.argv[4])

print(json.dumps([{
    "chat": channel,
    "chat_type": "group",
    "content": message,
    "is_group": True,
    "local_id": 7,
    "sender": "小金鱼",
    "timestamp": now,
    "type": "文本",
    "username": room_username,
}], ensure_ascii=False))
PY
  exit 0
fi

printf 'unsupported fake wx command: %s\n' "$*" >&2
exit 2
SH
chmod +x "${fake_wx}"

curl -fsS "${base_url}/healthz" >/dev/null

(
  cd "${root_dir}/tinybridge"
  CLAWMAN_BASE_URL="${base_url}" \
    CLAWMAN_API_TOKEN="${token}" \
    WECHAT_WX_BIN="${fake_wx}" \
    WECHAT_GROUP_ID="${room_username}" \
    WECHAT_GROUP_NAME="${channel}" \
    WECHAT_TARGET_CHATS="${channel}" \
    WECHAT_READ_MODE=history \
    WECHAT_ONCE=true \
    WECHAT_TRIGGER_POLICY='{"mode":"never"}' \
    WECHAT_SMOKE_ROOM_USERNAME="${room_username}" \
    WECHAT_SMOKE_CHANNEL="${channel}" \
    WECHAT_SMOKE_MESSAGE="${message_text}" \
    go run ./cmd/wechat-adapter
)

rooms="$(
  curl -fsS \
    -u "${admin_user}:${admin_secret}" \
    "${base_url}/admin/api/rooms?limit=200"
)"

room_id="$(
  python3 - "${room_username}" "${rooms}" <<'PY'
import json
import sys

room_username = sys.argv[1]
payload = json.loads(sys.argv[2])
for summary in payload.get("rooms", []):
    room = summary.get("room") or {}
    if room.get("channel") == "wechat" and room.get("channel_room_id") == room_username:
        print(room["id"])
        break
else:
    raise SystemExit(f"wechat smoke room not found: {room_username}")
PY
)"

timeline="$(
  curl -fsS \
    -u "${admin_user}:${admin_secret}" \
    "${base_url}/admin/api/rooms/${room_id}/timeline?limit=20"
)"

python3 - "${room_username}" "${message_text}" "${timeline}" <<'PY'
import json
import sys

room_username = sys.argv[1]
message_text = sys.argv[2]
payload = json.loads(sys.argv[3])
expected_msgid = f"wechat:{room_username}:7"

for message in payload.get("messages", []):
    body = message.get("body") or {}
    if (
        message.get("source") == "wechat"
        and message.get("msgid") == expected_msgid
        and message.get("msgtype") == "text"
        and body.get("content") == message_text
    ):
        if message.get("triggered"):
            raise SystemExit("wechat smoke message unexpectedly triggered an agent run")
        break
else:
    raise SystemExit(f"wechat smoke message not found: {expected_msgid}")
PY

printf "%s\n" "${timeline}" | python3 -m json.tool
