#!/usr/bin/env python3
"""
Telegram Notification Helper for Metanode CI/CD.
Uses Python standard library (urllib) - zero external dependencies required.
"""

import sys
import os
import time
import html
import json
import urllib.request
import urllib.parse
from datetime import datetime

def load_env_file(filepath):
    """Load key-value pairs from .env file into os.environ if not already present."""
    if not os.path.isfile(filepath):
        return
    try:
        with open(filepath, "r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith("#") or "=" not in line:
                    continue
                k, v = line.split("=", 1)
                k = k.strip()
                v = v.strip().strip("'\"")
                if k and k not in os.environ:
                    os.environ[k] = v
    except Exception as e:
        sys.stderr.write(f"Warning loading env {filepath}: {e}\n")

# Auto-load common .env locations dynamically
BASE_DIR = os.path.abspath(os.path.dirname(__file__))
METANODE_ROOT = os.path.abspath(os.path.join(BASE_DIR, "..", ".."))
SUITE_ROOT = os.path.abspath(os.path.join(METANODE_ROOT, "..", "metanode-suite"))

for env_path in [
    os.path.join(BASE_DIR, ".env"),
    os.path.join(BASE_DIR, "..", "ansible", ".env"),
    os.path.join(METANODE_ROOT, ".env"),
    os.path.join(SUITE_ROOT, "scripts", ".env"),
]:
    load_env_file(env_path)

def load_config_credentials():
    """Load telegram bot_token and chat_id from ci_config.yaml dynamically."""
    config_paths = [
        os.path.join(BASE_DIR, "ci_config.yaml"),
        os.path.join(METANODE_ROOT, "ci_config.yaml"),
        os.path.join(METANODE_ROOT, "deploy", "ci", "ci_config.yaml"),
    ]
    for cp in config_paths:
        if os.path.isfile(cp):
            try:
                import yaml
                with open(cp, "r", encoding="utf-8") as f:
                    cfg = yaml.safe_load(f) or {}
                tele = cfg.get("telegram", {})
                b_tok = tele.get("bot_token")
                c_id = tele.get("chat_id")
                if b_tok and c_id:
                    return str(b_tok).strip(), str(c_id).strip()
            except Exception:
                pass
    return None, None

def send_telegram_message(token, chat_id, html_message):
    """Send an HTML message via Telegram Bot API with retries, longer timeout, and curl fallback."""
    if not token or not chat_id:
        cfg_token, cfg_chat_id = load_config_credentials()
        if not token:
            token = cfg_token or os.environ.get("TELEGRAM_BOT_TOKEN")
        if not chat_id:
            chat_id = cfg_chat_id or os.environ.get("TELEGRAM_CHAT_ID")

    if not token or not chat_id:
        sys.stderr.write("⚠️ Telegram token or chat_id is missing in ci_config.yaml and environment. Skipping notification.\n")
        return False

    url = f"https://api.telegram.org/bot{token}/sendMessage"

    # Telegram hard limit is 4096 characters. Truncate safely if needed.
    if len(html_message) > 4000:
        html_message = html_message[:3900] + "\n\n<i>... [Nội dung đã được rút gọn do vượt giới hạn Telegram] ...</i>"

    data = urllib.parse.urlencode({
        "chat_id": str(chat_id),
        "text": html_message,
        "parse_mode": "HTML",
        "disable_web_page_preview": "true"
    }).encode("utf-8")

    # Attempt sending up to 3 times with timeout=25s
    max_attempts = 3
    last_err = None

    for attempt in range(1, max_attempts + 1):
        req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/x-www-form-urlencoded"})
        try:
            with urllib.request.urlopen(req, timeout=25) as resp:
                if resp.status == 200:
                    return True
        except Exception as e:
            last_err = e
            if attempt < max_attempts:
                time.sleep(2)

    # Fallback via curl if urllib failed
    try:
        import subprocess
        curl_cmd = [
            "curl", "-s", "--max-time", "25",
            "-X", "POST", url,
            "-d", f"chat_id={chat_id}",
            "-d", "parse_mode=HTML",
            "-d", "disable_web_page_preview=true",
            "--data-urlencode", f"text={html_message}"
        ]
        res = subprocess.run(curl_cmd, capture_output=True, text=True, timeout=30)
        if res.returncode == 0 and '"ok":true' in res.stdout:
            return True
        if res.returncode != 0:
            last_err = f"curl error: {res.stderr.strip() or res.stdout.strip()}"
    except Exception as curl_e:
        last_err = f"{last_err} | curl fallback: {curl_e}"

    sys.stderr.write(f"❌ Failed to send Telegram notification: {last_err}\n")
    return False

def format_duration(seconds):
    """Format seconds into readable Xm Ys format."""
    mins = int(seconds // 60)
    secs = int(seconds % 60)
    if mins > 0:
        return f"{mins}m {secs}s"
    return f"{secs}s"

def build_start_message(commit_info, branch, server_ip):
    timestamp = datetime.now().strftime("%H:%M:%S %d/%m/%Y")
    short_hash = commit_info.get("hash", "")[:8]
    author = html.escape(commit_info.get("author", "Unknown"))
    msg_subj = html.escape(commit_info.get("message", "No message"))

    return (
        f"🚀 <b>[METANODE CI PIPELINE BẮT ĐẦU]</b>\n\n"
        f"🌿 <b>Nhánh:</b> <code>{html.escape(branch)}</code>\n"
        f"📌 <b>Commit:</b> <code>{short_hash}</code> (bởi <b>{author}</b>)\n"
        f"💬 <b>Nội dung:</b> <i>{msg_subj}</i>\n"
        f"🖥 <b>Server:</b> <code>{server_ip}</code>\n"
        f"🕒 <b>Bắt đầu lúc:</b> <code>{timestamp}</code>\n\n"
        f"⏳ <i>Đang thực thi quy trình kiểm thử tự động...</i>"
    )

def build_test_step_message(commit_info, branch, test_name, duration, step_num, total_steps, extra_info, server_ip):
    timestamp = datetime.now().strftime("%H:%M:%S")
    dur_str = format_duration(duration)
    short_hash = commit_info.get("hash", "")[:8]
    extra_line = f"\n   └─ <i>{html.escape(extra_info)}</i>" if extra_info else ""

    return (
        f"⚡ <b>[TIẾN ĐỘ CI: {step_num}/{total_steps}]</b> <code>{html.escape(branch)}@{short_hash}</code>\n"
        f"✅ <b>{html.escape(test_name)}</b> ({dur_str}){extra_line}\n"
        f"🕒 <i>Xong lúc {timestamp} trên {server_ip}</i>"
    )

def build_finish_success_message(commit_info, branch, total_duration, test_results, server_ip):
    timestamp = datetime.now().strftime("%H:%M:%S %d/%m/%Y")
    short_hash = commit_info.get("hash", "")[:8]
    dur_str = format_duration(total_duration)

    lines = [
        f"🎉 <b>[METANODE CI TẤT CẢ TEST ĐÃ THÀNH CÔNG]</b>\n",
        f"🌿 <b>Nhánh:</b> <code>{html.escape(branch)}</code>",
        f"📌 <b>Commit:</b> <code>{short_hash}</code>",
        f"⏱️ <b>Tổng thời gian:</b> <code>{dur_str}</code>",
        f"🖥 <b>Server:</b> <code>{server_ip}</code>",
        f"🕒 <b>Hoàn tất:</b> <code>{timestamp}</code>\n",
        f"📊 <b>Kết quả từng bài test:</b>"
    ]

    for res in test_results:
        t_name = html.escape(res.get("name", "Test"))
        t_dur = format_duration(res.get("duration", 0))
        extra = res.get("extra", "")
        if extra:
            lines.append(f"  ✅ <b>{t_name}</b> ({t_dur})\n     └─ <i>{html.escape(extra)}</i>")
        else:
            lines.append(f"  ✅ <b>{t_name}</b> ({t_dur})")

    lines.append("\n🏆 <i>Hệ thống đảm bảo tính toàn vẹn và ổn định cao nhất!</i>")
    return "\n".join(lines)

def build_failure_message(commit_info, branch, failed_test_name, exit_code, *args, **kwargs):
    """
    Build failure message for Telegram.
    Extracts up to 20 trailing log lines and omits local log path.
    Supports both signatures:
      (commit_info, branch, test_name, exit_code, tail_logs, server_ip)
      (commit_info, branch, test_name, exit_code, log_path, tail_logs, server_ip)
    """
    if len(args) == 2:
        tail_logs, server_ip = args
    elif len(args) >= 3:
        _log_path, tail_logs, server_ip = args[:3]
    else:
        tail_logs = kwargs.get("tail_logs", "")
        server_ip = kwargs.get("server_ip", "")

    timestamp = datetime.now().strftime("%H:%M:%S %d/%m/%Y")
    short_hash = commit_info.get("hash", "")[:8]
    author = html.escape(commit_info.get("author", "Unknown"))

    # Extract last 20 lines
    lines = str(tail_logs).strip().splitlines()
    if len(lines) > 20:
        lines = lines[-20:]
    clean_tail = html.escape("\n".join(lines)) if lines else "(Không có log chi tiết)"

    return (
        f"🚨 <b>[METANODE CI PHÁT HIỆN LỖI KIỂM THỬ]</b>\n\n"
        f"🌿 <b>Nhánh:</b> <code>{html.escape(branch)}</code>\n"
        f"📌 <b>Commit:</b> <code>{short_hash}</code> (bởi <b>{author}</b>)\n"
        f"📍 <b>Bài test thất bại:</b> <code>{html.escape(failed_test_name)}</code>\n"
        f"⚠️ <b>Mã lỗi (Exit code):</b> <code>{exit_code}</code>\n"
        f"🖥 <b>Server:</b> <code>{server_ip}</code>\n"
        f"🕒 <b>Thời gian dừng:</b> <code>{timestamp}</code>\n\n"
        f"📋 <b>Log lỗi chi tiết (20 dòng cuối):</b>\n"
        f"<pre><code>{clean_tail}</code></pre>"
    )

if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--test":
        print("Sending test message...")
        test_msg = "🔔 <b>[METANODE CI TEST ALERT]</b>\nKết nối bot Telegram thành công!"
        token = sys.argv[2] if len(sys.argv) > 2 else os.environ.get("TELEGRAM_BOT_TOKEN")
        chat_id = sys.argv[3] if len(sys.argv) > 3 else os.environ.get("TELEGRAM_CHAT_ID")
        res = send_telegram_message(token, chat_id, test_msg)
        print("Success:", res)
