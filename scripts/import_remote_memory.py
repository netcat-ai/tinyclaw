#!/usr/bin/env python3
import base64
import json
import os
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
from pathlib import Path


def env(name, default=""):
    return os.environ.get(name, default).strip()


def load_dotenv(path):
    env_path = Path(path)
    if not env_path.exists():
        return
    for line in env_path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        if key in os.environ:
            continue
        if key not in {
            "CLAWMAN_BASE_URL",
            "CLAWMAN_ADMIN_SECRET",
            "TINYCLAW_ADMIN_USER",
            "LOCAL_POSTGRES_CONTAINER",
            "LOCAL_POSTGRES_USER",
            "LOCAL_POSTGRES_DB",
        }:
            continue
        os.environ[key] = value.strip().strip('"').strip("'")


def request_json(base_url, client_id, client_secret, path):
    token = base64.b64encode(f"{client_id}:{client_secret}".encode()).decode()
    req = urllib.request.Request(
        base_url.rstrip("/") + path,
        headers={"Authorization": f"Basic {token}"},
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return json.loads(resp.read().decode())
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode(errors="replace")
        raise SystemExit(f"remote API {path} failed: HTTP {exc.code}: {detail}") from exc


def sql_literal(value):
    if value is None:
        return "NULL"
    return "'" + str(value).replace("'", "''") + "'"


def main():
    load_dotenv(".env")

    base_url = env("CLAWMAN_BASE_URL")
    client_id = env("TINYCLAW_ADMIN_USER", "admin")
    client_secret = env("CLAWMAN_ADMIN_SECRET")
    container = env("LOCAL_POSTGRES_CONTAINER", "tinyclaw-postgres-local")
    db_user = env("LOCAL_POSTGRES_USER", "tinyclaw")
    db_name = env("LOCAL_POSTGRES_DB", "tinyclaw")

    if not base_url:
        raise SystemExit("CLAWMAN_BASE_URL is required")
    if not client_secret:
        raise SystemExit(
            "CLAWMAN_ADMIN_SECRET is required; CLAWMAN_API_TOKEN cannot read /admin/api room memory"
        )

    rooms_payload = request_json(base_url, client_id, client_secret, "/admin/api/rooms?limit=500")
    summaries = rooms_payload.get("rooms") or []
    export = []
    for summary in summaries:
        room = summary.get("room") or {}
        room_id = room.get("id")
        if not room_id:
            continue
        memory = request_json(base_url, client_id, client_secret, f"/admin/api/rooms/{room_id}/memory?status=all")
        items = memory.get("items") or []
        if items:
            export.append({"room": room, "items": items})

    total = sum(len(row["items"]) for row in export)
    print(f"remote rooms with memory: {len(export)}, memory_items: {total}")
    if total == 0:
        return

    statements = [
        "BEGIN;",
        "CREATE TABLE IF NOT EXISTS memory_items_backup_before_remote_import AS TABLE memory_items WITH DATA;",
    ]
    for row in export:
        room = row["room"]
        tenant = room.get("tenant_id")
        channel = room.get("channel")
        channel_room_id = room.get("channel_room_id")
        display_name = room.get("display_name") or channel_room_id
        channel_room_type = room.get("channel_room_type") or "group"
        outbound_alias = room.get("outbound_alias") or display_name
        if not tenant or not channel or not channel_room_id:
            print(f"skip room with incomplete identity: {room}", file=sys.stderr)
            continue

        statements.append(
            """
WITH imported_room AS (
  INSERT INTO rooms (tenant_id, channel, channel_room_id, channel_room_type, display_name, outbound_alias)
  VALUES ({tenant}, {channel}, {channel_room_id}, {channel_room_type}, {display_name}, {outbound_alias})
  ON CONFLICT (tenant_id, channel, channel_room_id) DO UPDATE
  SET channel_room_type = EXCLUDED.channel_room_type,
      display_name = EXCLUDED.display_name,
      outbound_alias = EXCLUDED.outbound_alias,
      updated_at = NOW()
  RETURNING id
)
""".format(
                tenant=sql_literal(tenant),
                channel=sql_literal(channel),
                channel_room_id=sql_literal(channel_room_id),
                channel_room_type=sql_literal(channel_room_type),
                display_name=sql_literal(display_name),
                outbound_alias=sql_literal(outbound_alias),
            )
            + "SELECT id FROM imported_room;"
        )
        for item in row["items"]:
            statements.append(
                """
INSERT INTO memory_items (
  room_id, type, key, content, status,
  source_message_from_id, source_message_to_id,
  created_at, updated_at
)
SELECT r.id, {typ}, {key}, {content}, {status},
       {source_from}, {source_to},
       COALESCE({created_at}::timestamptz, NOW()),
       COALESCE({updated_at}::timestamptz, NOW())
FROM rooms r
WHERE r.tenant_id = {tenant}
  AND r.channel = {channel}
  AND r.channel_room_id = {channel_room_id}
ON CONFLICT (room_id, type, key) DO UPDATE
SET content = EXCLUDED.content,
    status = EXCLUDED.status,
    source_message_from_id = EXCLUDED.source_message_from_id,
    source_message_to_id = EXCLUDED.source_message_to_id,
    updated_at = EXCLUDED.updated_at;
""".format(
                    typ=sql_literal(item.get("type")),
                    key=sql_literal(item.get("key")),
                    content=sql_literal(item.get("content") or ""),
                    status=sql_literal(item.get("status") or "active"),
                    source_from=int(item.get("source_message_from_id") or 0),
                    source_to=int(item.get("source_message_to_id") or 0),
                    created_at=sql_literal(item.get("created_at")),
                    updated_at=sql_literal(item.get("updated_at")),
                    tenant=sql_literal(tenant),
                    channel=sql_literal(channel),
                    channel_room_id=sql_literal(channel_room_id),
                )
            )
    statements.append("COMMIT;")
    statements.append("SELECT room_id, type, status, count(*) FROM memory_items GROUP BY room_id, type, status ORDER BY room_id, type, status;")

    with tempfile.NamedTemporaryFile("w", suffix=".sql", delete=False) as f:
        f.write("\n".join(statements))
        sql_path = f.name
    try:
        subprocess.run(
            ["docker", "exec", "-i", container, "psql", "-v", "ON_ERROR_STOP=1", "-U", db_user, "-d", db_name],
            input=open(sql_path, "rb").read(),
            check=True,
        )
    finally:
        os.unlink(sql_path)


if __name__ == "__main__":
    main()
