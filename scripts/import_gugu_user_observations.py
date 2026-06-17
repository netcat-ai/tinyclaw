#!/usr/bin/env python3
import argparse
import datetime as dt
import glob
import hashlib
import html
import json
import os
import re
import sqlite3
import subprocess
import tempfile
from collections import Counter, defaultdict
from pathlib import Path

try:
    import zstandard as zstd
except ImportError as exc:
    raise SystemExit(
        "zstandard is required. Run with ~/git/wechat-decrypt/.venv/bin/python "
        "or install zstandard for python3."
    ) from exc


DEFAULT_ROOM_ID = 11
DEFAULT_GROUP_ID = "24933085811@chatroom"
DEFAULT_GROUP_NAME = "姑姑的钻粉只此一群❤️"
DEFAULT_DECRYPTED_DIR = str(Path.home() / "git/wechat-decrypt/decrypted")
IMPORT_NAME = "gugu_user_observation_v1"

DIRECT_XIAO_TERMS = [
    "ibo",
    "IBO",
    "姑姑",
    "肖淑洁",
    "xsj",
    "XSJ",
    "肖醋",
    "肖总",
    "肖姐",
]

CONTEXT_XIAO_TERMS = [
    "主播",
    "直播间",
    "开播",
    "下播",
    "钻粉",
    "开钻",
    "礼物",
    "切片",
    "嘉年华",
    "打榜",
    "团播",
    "户外",
    "休息",
    "补课",
    "红包",
    "运营",
    "黑屏",
    "B站",
    "b站",
]

SUPPORTIVE_TERMS = [
    "支持",
    "相信",
    "喜欢",
    "可爱",
    "好看",
    "辛苦",
    "心疼",
    "别骂",
    "别带",
    "保护",
    "挺好",
    "可以了",
    "加油",
]

CRITICAL_TERMS = [
    "休息",
    "诈骗",
    "黑屏",
    "韭菜",
    "割",
    "止损",
    "坐牢",
    "背刺",
    "退",
    "破防",
    "不播",
    "跑路",
    "嘴硬",
    "离谱",
    "逆天",
    "无语",
]

MEME_TERMS = [
    "哈哈",
    "笑死",
    "绷",
    "乐",
    "典",
    "草",
    "抽象",
    "节目效果",
    "开香槟",
    "别急",
    "急了",
    "赢",
    "输",
]

INFO_TERMS = [
    "补课",
    "录播",
    "切片",
    "链接",
    "直播",
    "开播",
    "下播",
    "公告",
    "截图",
    "哪里看",
    "B站",
    "b站",
    "微博",
    "视频",
]

LGF_TERMS = ["lgf", "LGF", "老公粉", "老公"]


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


def parse_time(value):
    return int(dt.datetime.strptime(value, "%Y-%m-%d %H:%M:%S").timestamp())


def format_time(value):
    return dt.datetime.fromtimestamp(int(value)).strftime("%Y-%m-%d %H:%M:%S")


def base_type(value):
    try:
        return int(value) & 0xFFFFFFFF
    except Exception:
        return 0


def type_name(value):
    return {
        1: "文本",
        3: "图片",
        34: "语音",
        42: "名片",
        43: "视频",
        47: "表情",
        48: "位置",
        49: "链接/文件",
        50: "通话",
        10000: "系统",
        10002: "撤回",
    }.get(base_type(value), f"type={value}")


def strip_sender(raw):
    text = raw or ""
    if ":\n" in text:
        sender, body = text.split(":\n", 1)
        return sender, body
    match = re.match(r"^([A-Za-z0-9_@.\-]+):(.*)$", text, re.S)
    if match:
        return match.group(1), match.group(2)
    return "", text


def xml_text(body):
    body = html.unescape(body or "")
    prefix = re.split(r"<\?xml|<msg|<msgsource|<videomsg|<img", body, maxsplit=1, flags=re.I)[0]
    prefix = re.sub(r"\s+", " ", prefix).strip()
    if prefix and not re.fullmatch(r"[0-9A-Za-z+/=_:\-.\s]{16,}", prefix):
        return prefix
    values = []
    for tag in ("title", "des", "content"):
        values.extend(re.findall(rf"<{tag}[^>]*>(.*?)</{tag}>", body, flags=re.S | re.I))
    cleaned = " ".join(re.sub(r"<[^>]+>", " ", item) for item in values)
    if not cleaned:
        cleaned = re.sub(r"<[^>]+>", " ", body)
    return re.sub(r"\s+", " ", cleaned).strip()


def media_text(body, fallback):
    text = xml_text(body)
    if not text or re.fullmatch(r"[0-9A-Za-z+/=_:\-.\s]{16,}", text):
        return fallback
    return text


def display_content(raw, local_type):
    sender, body = strip_sender(raw)
    kind = base_type(local_type)
    if kind == 1:
        text = re.sub(r"\s+", " ", body).strip()
    elif kind == 3:
        text = media_text(body, "[图片]")
    elif kind == 34:
        text = "[语音]"
    elif kind == 43:
        text = media_text(body, "[视频]")
    elif kind == 47:
        text = "[表情]"
    elif kind == 49:
        text = xml_text(body) or "[链接/文件]"
    elif kind == 10000:
        text = xml_text(body) or re.sub(r"\s+", " ", body).strip()
    else:
        text = xml_text(body) or re.sub(r"\s+", " ", body).strip()
    return sender, text


def decompress_content(dctx, content, ct):
    if ct == 4 and isinstance(content, bytes):
        try:
            return dctx.decompress(content).decode("utf-8", "replace")
        except Exception:
            return ""
    if isinstance(content, bytes):
        return content.decode("utf-8", "replace")
    return content or ""


def load_contact_names(decrypted_dir):
    contact_db = Path(decrypted_dir) / "contact" / "contact.db"
    names = {}
    if not contact_db.exists():
        return names
    conn = sqlite3.connect(contact_db)
    try:
        for username, remark, nick in conn.execute("SELECT username, remark, nick_name FROM contact"):
            names[username] = remark or nick or username
    finally:
        conn.close()
    return names


def load_messages(decrypted_dir, group_id, since, until):
    message_dir = Path(decrypted_dir) / "message"
    if not message_dir.is_dir():
        raise SystemExit(f"消息目录不存在: {message_dir}")

    start_ts = parse_time(since)
    end_ts = parse_time(until)
    table = "Msg_" + hashlib.md5(group_id.encode()).hexdigest()
    names = load_contact_names(decrypted_dir)
    dctx = zstd.ZstdDecompressor()
    messages = []

    for db_path in sorted(glob.glob(str(message_dir / "message_[0-9]*.db"))):
        conn = sqlite3.connect(db_path)
        try:
            exists = conn.execute(
                "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?",
                (table,),
            ).fetchone()
            if not exists:
                continue
            id_to_username = {rowid: username for rowid, username in conn.execute("SELECT rowid, user_name FROM Name2Id")}
            rows = conn.execute(
                f"""SELECT local_id, local_type, create_time, real_sender_id, message_content, WCDB_CT_message_content
                    FROM [{table}]
                    WHERE create_time >= ? AND create_time < ?
                    ORDER BY create_time ASC""",
                (start_ts, end_ts),
            ).fetchall()
            for local_id, local_type, create_time, real_sender_id, content, ct in rows:
                raw = decompress_content(dctx, content, ct)
                sender, text = display_content(raw, local_type)
                if not text or "revokemsg" in text:
                    continue
                kind = type_name(local_type)
                if kind in {"系统", "撤回"}:
                    continue
                username = sender or id_to_username.get(real_sender_id, "")
                if not username:
                    continue
                messages.append(
                    {
                        "local_id": local_id,
                        "sender": username,
                        "sender_display": names.get(username, username),
                        "time": format_time(create_time),
                        "timestamp": int(create_time),
                        "type": kind,
                        "content": text,
                    }
                )
        finally:
            conn.close()

    messages.sort(key=lambda item: (item["timestamp"], item["local_id"]))
    return messages


def count_terms(texts, terms):
    total = 0
    for text in texts:
        for term in terms:
            if term in text:
                total += 1
    return total


def top_terms(texts, terms, limit=5):
    counts = Counter()
    for text in texts:
        for term in terms:
            if term in text:
                counts[term] += 1
    return [term for term, _ in counts.most_common(limit)]


def classify_user(stat):
    count = stat["count"]
    direct = stat["direct_xiao"]
    contextual = stat["context_xiao"]
    supportive = stat["supportive"]
    critical = stat["critical"]
    meme = stat["meme"]
    info = stat["info"]
    lgf = stat["lgf"]

    xiao_ratio = (direct + contextual) / max(count, 1)
    short_ratio = stat["short_messages"] / max(count, 1)

    candidates = []
    if lgf > 0:
        candidates.append(("lgf 相关语境参与", min(0.55 + lgf / max(count, 1), 0.82)))
    if direct + contextual >= 8 and supportive >= max(3, critical * 1.4):
        candidates.append(("偏肖白/维护支持语气", min(0.55 + (supportive - critical) / max(count, 1) + xiao_ratio, 0.86)))
    if direct + contextual >= 8 and critical >= max(3, supportive * 1.4):
        candidates.append(("偏肖黑/吐槽质疑语气", min(0.55 + (critical - supportive) / max(count, 1) + xiao_ratio, 0.86)))
    if meme >= max(5, supportive + critical) or (meme >= 8 and short_ratio >= 0.45):
        candidates.append(("乐子人/围观接梗", min(0.52 + meme / max(count, 1) + short_ratio / 3, 0.88)))
    if info >= 8 and info >= meme:
        candidates.append(("信息搬运/补课型", min(0.52 + info / max(count, 1) + xiao_ratio / 2, 0.84)))
    if direct + contextual >= 10 and not candidates:
        candidates.append(("IBO 主线参与", min(0.5 + xiao_ratio, 0.78)))

    if not candidates:
        return "倾向不明/泛话题参与", 0.35

    label, confidence = max(candidates, key=lambda item: item[1])
    return label, round(confidence, 2)


def style_summary(stat):
    parts = []
    avg_len = stat["avg_len"]
    short_ratio = stat["short_messages"] / max(stat["count"], 1)
    if short_ratio >= 0.55:
        parts.append("短句接话较多")
    elif avg_len >= 24:
        parts.append("长句解释较多")
    else:
        parts.append("中短句讨论为主")
    if stat["meme"] >= 5:
        parts.append("常接梗或调侃")
    if stat["info"] >= 5:
        parts.append("会参与补课/信息同步")
    if stat["critical"] >= 5:
        parts.append("遇到争议话题时吐槽和质疑较多")
    if stat["supportive"] >= 5:
        parts.append("遇到 IBO 相关话题时维护和支持表达较多")
    return "，".join(parts[:3])


def build_observations(messages, top_n, since, until, group_name):
    by_sender = defaultdict(list)
    for message in messages:
        by_sender[message["sender"]].append(message)

    top_senders = sorted(by_sender.items(), key=lambda item: len(item[1]), reverse=True)[:top_n]
    observations = []
    for sender, sender_messages in top_senders:
        texts = [str(item["content"] or "") for item in sender_messages]
        text_lengths = [len(text) for text in texts]
        active_days = {item["time"][:10] for item in sender_messages}
        stat = {
            "sender": sender,
            "display": sender_messages[-1]["sender_display"] or sender,
            "count": len(sender_messages),
            "active_days": len(active_days),
            "first_time": sender_messages[0]["time"],
            "last_time": sender_messages[-1]["time"],
            "avg_len": round(sum(text_lengths) / max(len(text_lengths), 1), 1),
            "short_messages": sum(1 for length in text_lengths if length <= 8),
            "direct_xiao": count_terms(texts, DIRECT_XIAO_TERMS),
            "context_xiao": count_terms(texts, CONTEXT_XIAO_TERMS),
            "supportive": count_terms(texts, SUPPORTIVE_TERMS),
            "critical": count_terms(texts, CRITICAL_TERMS),
            "meme": count_terms(texts, MEME_TERMS),
            "info": count_terms(texts, INFO_TERMS),
            "lgf": count_terms(texts, LGF_TERMS),
        }
        label, confidence = classify_user(stat)
        xiao_ratio = ((stat["direct_xiao"] + stat["context_xiao"]) / max(stat["count"], 1)) * 100
        terms = top_terms(
            texts,
            DIRECT_XIAO_TERMS + CONTEXT_XIAO_TERMS + SUPPORTIVE_TERMS + CRITICAL_TERMS + MEME_TERMS + INFO_TERMS + LGF_TERMS,
        )
        evidence = [
            f"发言 {stat['count']} 条、活跃 {stat['active_days']} 天",
            f"IBO/姑姑相关语境约 {xiao_ratio:.1f}%",
            f"常见触发词：{'、'.join(terms) if terms else '无明显集中词'}",
            f"表达习惯：{style_summary(stat)}",
        ]
        key_hash = hashlib.sha1(sender.encode()).hexdigest()[:12]
        content = (
            f"近 60 天观察（{since} 至 {until}，{group_name}）：群友「{stat['display']}」"
            f"的近期发言倾向可参考为“{label}”，置信度 {confidence:.2f}。"
            f"依据：{'; '.join(evidence)}。"
            "这只是窗口期文本观察，不代表真实身份、长期立场或性格定论；回答相关问题时应说“近期发言更偏……”，不要直接断言 TA 是某类人。"
            f" 本地 sender={sender}。"
        )
        observations.append(
            {
                "type": "preference",
                "key": f"gugu.user_context.{key_hash}",
                "content": content,
                "display": stat["display"],
                "sender": sender,
                "message_count": stat["count"],
                "label": label,
                "confidence": confidence,
                "evidence": evidence,
            }
        )
    return observations


def response_policy_item(since, until):
    return {
        "type": "preference",
        "key": "gugu.user_context.response_policy",
        "content": (
            f"当用户询问姑姑群某位群友的肖白/肖黑/lgf/乐子人等倾向时，只能基于最近 60 天窗口（{since} 至 {until}）"
            "描述“近期发言倾向观察”，必须带不确定性；不要把观察写成真实身份、稳定性格、阵营定论或拉踩评价。"
        ),
    }


def build_sql(room_id, items):
    statements = [
        "BEGIN;",
        "CREATE TABLE IF NOT EXISTS memory_items_backup_before_gugu_user_observation_import AS TABLE memory_items WITH DATA;",
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
            "source": "recent 60 days decrypted WeChat messages",
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
SELECT type, key, left(content, 140) AS content_preview
FROM memory_items
WHERE room_id = {int(room_id)}
  AND key LIKE 'gugu.user_context.%'
ORDER BY key;
""",
        ]
    )
    return "\n".join(statements)


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


def write_outputs(out_dir, base, observations):
    Path(out_dir).mkdir(parents=True, exist_ok=True)
    json_path = Path(out_dir) / f"{base}.user-observations.json"
    html_path = Path(out_dir) / f"{base}.user-observations.html"
    json_path.write_text(json.dumps(observations, ensure_ascii=False, indent=2), encoding="utf-8")
    rows = "\n".join(
        f"<tr><td>{escape_html(item['display'])}</td><td>{item['message_count']}</td>"
        f"<td>{escape_html(item['label'])}</td><td>{item['confidence']:.2f}</td>"
        f"<td>{escape_html('；'.join(item['evidence']))}</td></tr>"
        for item in observations
        if item["key"] != "gugu.user_context.response_policy"
    )
    html_path.write_text(
        f"""<!doctype html>
<meta charset="utf-8">
<title>姑姑群用户语境观察</title>
<style>
body {{ font-family: -apple-system, BlinkMacSystemFont, "PingFang SC", sans-serif; margin: 32px; color: #241b15; background: #f7efe3; }}
table {{ border-collapse: collapse; width: 100%; background: #fffaf1; }}
th, td {{ border: 1px solid #d7c7af; padding: 10px; vertical-align: top; }}
th {{ text-align: left; background: #efe0ca; }}
.note {{ color: #7a6b5b; }}
</style>
<h1>姑姑群用户语境观察</h1>
<p class="note">窗口期观察，不代表真实身份、长期立场或性格定论。</p>
<table>
<thead><tr><th>群友</th><th>发言数</th><th>近期倾向</th><th>置信度</th><th>依据</th></tr></thead>
<tbody>{rows}</tbody>
</table>
""",
        encoding="utf-8",
    )
    return json_path, html_path


def escape_html(value):
    return html.escape(str(value), quote=True)


def compact(value):
    return value.replace("-", "").replace(":", "").replace(" ", "")


def main():
    parser = argparse.ArgumentParser(description="Import recent gugu top speaker observations into local tinyclaw memory.")
    parser.add_argument("--room-id", type=int, default=DEFAULT_ROOM_ID, help="target tinyclaw rooms.id")
    parser.add_argument("--group-id", default=DEFAULT_GROUP_ID, help="WeChat chatroom id")
    parser.add_argument("--group-name", default=DEFAULT_GROUP_NAME, help="display name used in memory content")
    parser.add_argument("--decrypted-dir", default=DEFAULT_DECRYPTED_DIR, help="WeChat decrypted DB directory")
    parser.add_argument("--days", type=int, default=60, help="observation window in days")
    parser.add_argument("--top", type=int, default=20, help="number of top speakers to import")
    parser.add_argument("--until", default="", help="exclusive end time, default now, format YYYY-MM-DD HH:MM:SS")
    parser.add_argument("--out-dir", default="scripts/output/gugu-user-observation", help="local report output directory")
    parser.add_argument("--dry-run", action="store_true", help="print observations without writing DB")
    args = parser.parse_args()

    load_dotenv(".env")
    until_dt = dt.datetime.strptime(args.until, "%Y-%m-%d %H:%M:%S") if args.until else dt.datetime.now()
    since_dt = until_dt - dt.timedelta(days=args.days)
    since = since_dt.strftime("%Y-%m-%d %H:%M:%S")
    until = until_dt.strftime("%Y-%m-%d %H:%M:%S")

    messages = load_messages(args.decrypted_dir, args.group_id, since, until)
    observations = [response_policy_item(since, until), *build_observations(messages, args.top, since, until, args.group_name)]
    base = f"gugu-{compact(since)}-{compact(until)}"
    json_path, html_path = write_outputs(args.out_dir, base, observations)

    print(f"messages: {len(messages)}")
    print(f"observations: {len(observations)}")
    print(f"json: {json_path}")
    print(f"html: {html_path}")
    for item in observations:
        if item["key"] == "gugu.user_context.response_policy":
            continue
        print(f"- {item['display']} | {item['message_count']} | {item['label']} | {item['confidence']:.2f}")

    if not args.dry_run:
        run_psql(build_sql(args.room_id, observations))


if __name__ == "__main__":
    main()
