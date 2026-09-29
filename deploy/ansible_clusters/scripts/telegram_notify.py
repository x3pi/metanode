#!/usr/bin/env python3
"""
Telegram Notification Helper for MetaNode Multi-Cluster Deployment.
Sends rich, formatted HTML alerts for deploy start, node status, test results, and failure events.
Zero external dependencies required (uses standard library urllib).
"""

import sys
import os
import json
import time
import html
import subprocess
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

# Load environment configs from multiple fallback paths
BASE_DIR = os.path.abspath(os.path.dirname(__file__))
ANSIBLE_CLUSTER_DIR = os.path.abspath(os.path.join(BASE_DIR, ".."))
METANODE_ROOT = os.path.abspath(os.path.join(ANSIBLE_CLUSTER_DIR, "../.."))

for p in [
    os.path.join(ANSIBLE_CLUSTER_DIR, ".env"),
    os.path.join(METANODE_ROOT, "deploy", "ansible", ".env"),
    os.path.join(METANODE_ROOT, "deploy", "ci", ".env"),
    os.path.join(METANODE_ROOT, ".env"),
]:
    load_env_file(p)

def send_telegram_message(token=None, chat_id=None, html_message=""):
    """Send an HTML message via Telegram Bot API with retries and timeout."""
    token = token or os.environ.get("TELEGRAM_BOT_TOKEN")
    chat_id = chat_id or os.environ.get("TELEGRAM_CHAT_ID")

    if not token or not chat_id:
        sys.stderr.write("⚠️ Telegram token or chat_id is missing. Notification skipped.\n")
        return False

    url = f"https://api.telegram.org/bot{token}/sendMessage"

    # Truncate if exceeds Telegram limit
    if len(html_message) > 4000:
        html_message = html_message[:3900] + "\n\n<i>... [Nội dung đã được rút gọn do vượt giới hạn Telegram] ...</i>"

    data = urllib.parse.urlencode({
        "chat_id": str(chat_id),
        "text": html_message,
        "parse_mode": "HTML",
        "disable_web_page_preview": "true"
    }).encode("utf-8")

    max_attempts = 3
    for attempt in range(1, max_attempts + 1):
        try:
            req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/x-www-form-urlencoded"})
            with urllib.request.urlopen(req, timeout=15) as resp:
                if resp.status == 200:
                    return True
        except Exception as e:
            if attempt == max_attempts:
                sys.stderr.write(f"Telegram notification error after {max_attempts} attempts: {e}\n")
            time.sleep(1)
    return False

def get_git_info():
    """Retrieve git branch, short hash, and author."""
    try:
        branch = subprocess.check_output(["git", "rev-parse", "--abbrev-ref", "HEAD"], cwd=METANODE_ROOT, text=True).strip()
        commit_hash = subprocess.check_output(["git", "rev-parse", "--short", "HEAD"], cwd=METANODE_ROOT, text=True).strip()
        author = subprocess.check_output(["git", "log", "-1", "--format=%an"], cwd=METANODE_ROOT, text=True).strip()
        commit_msg = subprocess.check_output(["git", "log", "-1", "--format=%s"], cwd=METANODE_ROOT, text=True).strip()
        return {
            "branch": branch,
            "hash": commit_hash,
            "author": author,
            "message": commit_msg,
        }
    except Exception:
        return {
            "branch": "unknown",
            "hash": "unknown",
            "author": "dev",
            "message": "N/A",
        }

def notify_deploy_start(clusters_info="Parent Chain + Exec Clusters", target_env="Local/Devnet"):
    git = get_git_info()
    now_str = datetime.now().strftime("%H:%M:%S %d/%m/%Y")
    msg = (
        f"🚀 <b>[METANODE CLUSTER DEPLOY BẮT ĐẦU]</b>\n\n"
        f"🌿 <b>Nhánh:</b> <code>{html.escape(git['branch'])}</code>\n"
        f"📌 <b>Commit:</b> <code>{git['hash']}</code> (bởi <b>{html.escape(git['author'])}</b>)\n"
        f"💬 <b>Nội dung:</b> <i>{html.escape(git['message'])}</i>\n"
        f"🖥 <b>Môi trường:</b> <code>{target_env}</code>\n"
        f"🎯 <b>Cụm triển khai:</b> {clusters_info}\n"
        f"🕒 <b>Bắt đầu:</b> <code>{now_str}</code>\n\n"
        f"⏳ <i>Đang biên dịch, đồng bộ cấu hình và khởi chạy các service...</i>"
    )
    return send_telegram_message(html_message=msg)

def notify_services_ready(parent_info, exec_clusters_info, duration_secs=0):
    git = get_git_info()
    now_str = datetime.now().strftime("%H:%M:%S %d/%m/%Y")
    
    exec_lines = []
    for c in exec_clusters_info:
        name = c.get('name', 'Cluster')
        cid = c.get('cluster_id', '?')
        rpc = c.get('rpc', '?')
        blk = c.get('block_height', '?')
        exec_lines.append(f"  • <b>{name}</b> (ClusterID <code>{cid}</code>) | RPC: <code>{rpc}</code> | Height: <code>{blk}</code>")
    
    clusters_text = "\n".join(exec_lines) if exec_lines else "  • Các cluster đã sẵn sàng"

    msg = (
        f"✅ <b>[DỊCH VỤ CỤM METANODE ĐÃ SẴN SÀNG]</b>\n\n"
        f"📌 <b>Commit:</b> <code>{git['hash']}</code>\n"
        f"⏱️ <b>Thời gian khởi chạy:</b> <code>{duration_secs:.1f}s</code>\n"
        f"🕒 <b>Thời gian:</b> <code>{now_str}</code>\n\n"
        f"🌐 <b>Parent Chain:</b>\n"
        f"  • RPC: <code>{parent_info.get('rpc', ':8547')}</code> | Height: <code>{parent_info.get('block_height', 'OK')}</code>\n\n"
        f"⚡ <b>Execution Clusters (EVM ChainID 991):</b>\n"
        f"{clusters_text}\n\n"
        f"🛡️ <i>Cơ chế Rollup Float Accounts và routing liên cụm đã kích hoạt!</i>"
    )
    return send_telegram_message(html_message=msg)

def notify_test_results(scenarios, total_duration=0, all_passed=True):
    git = get_git_info()
    now_str = datetime.now().strftime("%H:%M:%S %d/%m/%Y")
    status_icon = "🎉" if all_passed else "⚠️"
    status_title = "KIỂM THỬ TÍCH HỢP HOÀN TẤT THÀNH CÔNG" if all_passed else "KIỂM THỬ PHÁT HIỆN LỖI"

    lines = [
        f"{status_icon} <b>[METANODE CLUSTER - {status_title}]</b>\n",
        f"📌 <b>Commit:</b> <code>{git['hash']}</code>",
        f"⏱️ <b>Tổng thời gian test:</b> <code>{total_duration:.1f}s</code>",
        f"🕒 <b>Thời gian:</b> <code>{now_str}</code>\n",
        f"📊 <b>Chi tiết từng kịch bản sử dụng thực tế:</b>"
    ]

    for s in scenarios:
        s_icon = "✅" if s.get("passed", True) else "❌"
        s_name = html.escape(s.get("name", "Kịch bản"))
        s_dur = f"({s.get('duration', 0):.1f}s)" if "duration" in s else ""
        s_detail = s.get("detail", "")
        lines.append(f"  {s_icon} <b>{s_name}</b> {s_dur}")
        if s_detail:
            lines.append(f"     └─ <i>{html.escape(s_detail)}</i>")

    if all_passed:
        lines.append("\n🏆 <i>Tất cả các tính năng cross-cluster, float accounts & resilience đều hoạt động hoàn hảo!</i>")
    else:
        lines.append("\n🚨 <i>Vui lòng kiểm tra log để biết nguyên nhân thất bại!</i>")

    return send_telegram_message(html_message="\n".join(lines))

def notify_deploy_failure(stage, error_msg, tail_logs=""):
    git = get_git_info()
    now_str = datetime.now().strftime("%H:%M:%S %d/%m/%Y")
    
    clean_tail = ""
    if tail_logs:
        log_lines = str(tail_logs).strip().splitlines()[-20:]
        clean_tail = html.escape("\n".join(log_lines))

    msg = (
        f"🚨 <b>[METANODE CLUSTER DEPLOY THẤT BẠI]</b>\n\n"
        f"🌿 <b>Nhánh:</b> <code>{html.escape(git['branch'])}</code>\n"
        f"📌 <b>Commit:</b> <code>{git['hash']}</code> (bởi <b>{html.escape(git['author'])}</b>)\n"
        f"📍 <b>Giai đoạn lỗi:</b> <code>{html.escape(stage)}</code>\n"
        f"⚠️ <b>Thông điệp lỗi:</b> <code>{html.escape(str(error_msg))}</code>\n"
        f"🕒 <b>Thời gian:</b> <code>{now_str}</code>\n"
    )
    if clean_tail:
        msg += f"\n📋 <b>Log lỗi chi tiết:</b>\n<pre><code>{clean_tail}</code></pre>"

    return send_telegram_message(html_message=msg)

if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--test":
        print("Sending test Telegram message...")
        ok = send_telegram_message(html_message="🔔 <b>[METANODE CLUSTER BOT TEST]</b>\nKết nối bot Telegram thành công!")
        print("Telegram test result:", "OK" if ok else "FAILED")
        sys.exit(0 if ok else 1)
    
    if len(sys.argv) > 1 and sys.argv[1] == "--start":
        notify_deploy_start()
        sys.exit(0)
