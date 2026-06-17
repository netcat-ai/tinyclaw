#!/usr/bin/env python3
import argparse
import json
import os
import subprocess
import tempfile
from pathlib import Path


DEFAULT_ROOM_ID = 11
IMPORT_NAME = "gugu_history_memory_v1"


MEMORY_ITEMS = [
    {
        "type": "fact",
        "key": "gugu.room.identity",
        "content": "姑姑一群是微信房间 24933085811@chatroom，群名“姑姑的钻粉只此一群❤️”；在 tinyclaw 本地 room_id=11。",
    },
    {
        "type": "fact",
        "key": "gugu.related_rooms",
        "content": "相关群包括 8群“姑姑的钻粉不止八群❤️”(44865760049@chatroom) 与“小僵尸钻粉9群”(54330126714@chatroom)；tinyclaw 当前默认只写入姑姑一群，未注册的群不要声称已接入。",
    },
    {
        "type": "fact",
        "key": "gugu.report.window",
        "content": "姑姑日报默认统计窗口为每日 07:00 到次日 07:00；多日期特刊也应按 07:00 窗口拼接。",
    },
    {
        "type": "fact",
        "key": "gugu.index.guzhi",
        "content": "姑指是群消息数指数；交易量是发言人数。涨跌对比必须使用同口径窗口，例如周末特刊应与上周末同时间段比较。",
    },
    {
        "type": "fact",
        "key": "gugu.index.xiao",
        "content": "含肖量/语境含肖量用于估算围绕 IBO/肖淑洁的话题占比；直接词包括 ibo、姑姑、肖淑洁、xsj、肖醋、肖总、肖姐，语境词包括主播、直播间、开播、下播、钻粉、开钻、礼物、切片、嘉年华、打榜、团播、户外、休息、补课、红包、运营、黑屏、B站。",
    },
    {
        "type": "fact",
        "key": "gugu.term.wachao",
        "content": "蛙超指斗鱼团播 S10 女主播足球比赛；不要把它误解成普通足球或真实足球夜聊。",
    },
    {
        "type": "fact",
        "key": "gugu.summary.weekend_20260613",
        "content": "2026-06-13 07:00 至 2026-06-15 07:00 周末特刊主题包括女帝登基、偷菜夜班、蛙超团播足球、CS/真实足球夜聊；本期姑指 5196，1群 4855、8群 341。",
    },
    {
        "type": "preference",
        "key": "gugu.report.tone",
        "content": "姑姑群简报应有趣、短、适合手机看；写群聊节奏、话题和梗，不给人贴标签，不做发言排行、贡献榜、阵营或人设画像，不拉踩。",
    },
    {
        "type": "preference",
        "key": "gugu.report.sections",
        "content": "日报结构优先服务两个目标：懒得爬楼的人快速了解最近 24 小时，以及保留群聊趣味。可用今日概览、今日节奏、重点话题、重要信息、代表性原话、待确认事项、群内氛围。",
    },
    {
        "type": "preference",
        "key": "gugu.report.quotes",
        "content": "精彩原话只摘录温和、有代表性、能还原氛围的短句；不配毒舌点评，不摘隐私、消费金额、家庭、工作、身体状况、情绪崩溃等内容。",
    },
    {
        "type": "preference",
        "key": "gugu.report.visual",
        "content": "移动端 HTML 报告适合使用纸张质感、红/青强调色、紧凑卡片和少量插图；特刊可插入封面图和主题插图，图片应使用本地相对路径。",
    },
    {
        "type": "preference",
        "key": "gugu.report.charting",
        "content": "趋势图按日报窗口统计，折线图展示最近 7 个 07:00-次日07:00 窗口；多日汇总图必须标清统计口径。",
    },
]


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
            "LOCAL_POSTGRES_CONTAINER",
            "LOCAL_POSTGRES_USER",
            "LOCAL_POSTGRES_DB",
        }:
            continue
        os.environ[key] = value.strip().strip('"').strip("'")


def env(name, default=""):
    return os.environ.get(name, default).strip()


def sql_literal(value):
    if value is None:
        return "NULL"
    return "'" + str(value).replace("'", "''") + "'"


def build_sql(room_id, items):
    statements = [
        "BEGIN;",
        "CREATE TABLE IF NOT EXISTS memory_items_backup_before_gugu_history_import AS TABLE memory_items WITH DATA;",
        f"""
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM rooms WHERE id = {int(room_id)}) THEN
    RAISE EXCEPTION 'room_id {int(room_id)} does not exist';
  END IF;
END $$;
""",
    ]

    for item in items:
        payload = {
            "import": IMPORT_NAME,
            "type": item["type"],
            "key": item["key"],
            "source": "OpenCLI historical gugu reports and user-corrected terms",
        }
        statements.append(
            """
WITH upserted AS (
  INSERT INTO memory_items (
    room_id, type, key, content, status,
    source_message_from_id, source_message_to_id,
    created_at, updated_at
  )
  VALUES (
    {room_id}, {typ}, {key}, {content}, 'active',
    0, 0, NOW(), NOW()
  )
  ON CONFLICT (room_id, type, key) DO UPDATE
  SET content = EXCLUDED.content,
      status = EXCLUDED.status,
      updated_at = NOW()
  RETURNING id, room_id
)
INSERT INTO memory_change_audit (memory_item_id, room_id, action, payload)
SELECT id, room_id, 'historical_import', {payload}::jsonb
FROM upserted;
""".format(
                room_id=int(room_id),
                typ=sql_literal(item["type"]),
                key=sql_literal(item["key"]),
                content=sql_literal(item["content"]),
                payload=sql_literal(json.dumps(payload, ensure_ascii=False)),
            )
        )

    statements.extend(
        [
            "COMMIT;",
            f"""
SELECT type, key, left(content, 120) AS content_preview
FROM memory_items
WHERE room_id = {int(room_id)}
ORDER BY type, key;
""",
        ]
    )
    return "\n".join(statements)


def print_dry_run(room_id, items):
    print(f"room_id: {room_id}")
    print(f"items: {len(items)}")
    for item in items:
        print(f"- [{item['type']}] {item['key']}: {item['content']}")


def run_psql(sql):
    container = env("LOCAL_POSTGRES_CONTAINER", "tinyclaw-postgres-local")
    db_user = env("LOCAL_POSTGRES_USER", "tinyclaw")
    db_name = env("LOCAL_POSTGRES_DB", "tinyclaw")

    with tempfile.NamedTemporaryFile("w", suffix=".sql", delete=False) as f:
        f.write(sql)
        sql_path = f.name
    try:
        subprocess.run(
            ["docker", "exec", "-i", container, "psql", "-v", "ON_ERROR_STOP=1", "-U", db_user, "-d", db_name],
            input=Path(sql_path).read_bytes(),
            check=True,
        )
    finally:
        os.unlink(sql_path)


def main():
    parser = argparse.ArgumentParser(description="Import curated gugu historical memories into local tinyclaw.")
    parser.add_argument("--room-id", type=int, default=DEFAULT_ROOM_ID, help="target tinyclaw rooms.id")
    parser.add_argument("--dry-run", action="store_true", help="print proposed memory items without writing DB")
    args = parser.parse_args()

    load_dotenv(".env")
    if args.dry_run:
        print_dry_run(args.room_id, MEMORY_ITEMS)
        return

    run_psql(build_sql(args.room_id, MEMORY_ITEMS))


if __name__ == "__main__":
    main()
