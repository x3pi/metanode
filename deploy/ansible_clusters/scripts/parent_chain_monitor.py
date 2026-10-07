#!/usr/bin/env python3
"""
Parent Chain Liveness & Hash Consistency Monitor
Purely configuration-driven from inventory.yml (Zero hardcoding).

Monitors:
  1. Liveness: Node alive/dead (HTTP GET /status on all parent_chain_nodes)
  2. Hash Consistency & Zero-Fork: Compares last_hash, state_root at same block height, checks fork_detected flag.
Sends alerts to Telegram channel when anomalies are detected.

📋 CHEATSHEET & XEM LOG:
  • Xem log monitor real-time:
      tail -f /tmp/parent_chain_monitor.log
  • Xem log từng node Parent Chain trên server này (234):
      tail -f /var/log/metanode/parent_chain_0.log
      tail -f /var/log/metanode/parent_chain_1.log
  • Xem log node từ xa (server 223):
      tail -f /var/log/metanode/parent_chain_2.log  (khi ssh vào 223)
  • Lệnh điều khiển monitor:
      python3 scripts/parent_chain_monitor.py --once        # Kiểm tra nhanh 1 lần
      python3 scripts/parent_chain_monitor.py --daemon      # Chạy ngầm
      python3 scripts/parent_chain_monitor.py --stop        # Dừng ngầm
"""

import sys
import os
import time
import json
import html
import urllib.request
import urllib.error
import urllib.parse
from datetime import datetime

try:
    sys.stdout.reconfigure(line_buffering=True)
    sys.stderr.reconfigure(line_buffering=True)
except Exception:
    pass

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
ANSIBLE_CLUSTER_DIR = os.path.dirname(SCRIPT_DIR)
DEFAULT_INVENTORY = os.path.join(ANSIBLE_CLUSTER_DIR, "inventory.yml")
PID_FILE = "/tmp/parent_chain_monitor.pid"
LOG_FILE = "/tmp/parent_chain_monitor.log"

def load_config(inventory_path=None):
    """
    Parse inventory.yml directly without hardcoding any nodes or telegram credentials.
    Reads:
      - all.vars.telegram_bot_token (or env TELEGRAM_BOT_TOKEN)
      - all.vars.telegram_chat_id (or env TELEGRAM_CHAT_ID)
      - all.children.parent_chain_nodes.hosts -> ansible_host & parent_http_port
    """
    inv_file = inventory_path or DEFAULT_INVENTORY
    if not os.path.isfile(inv_file):
        sys.exit(f"❌ Lỗi: File cấu hình inventory không tồn tại tại: {inv_file}")

    try:
        import yaml
        def vault_constructor(loader, node):
            return loader.construct_scalar(node)
        yaml.SafeLoader.add_constructor('!vault', vault_constructor)
        with open(inv_file, 'r', encoding='utf-8') as f:
            inv = yaml.safe_load(f.read())
    except Exception as e:
        sys.exit(f"❌ Lỗi khi đọc file YAML {inv_file}: {e}")

    if not isinstance(inv, dict):
        sys.exit(f"❌ Cấu trúc YAML không hợp lệ trong {inv_file}")

    vars_data = inv.get('all', {}).get('vars', {}) or {}
    bot_token = vars_data.get('telegram_bot_token') or os.environ.get("TELEGRAM_BOT_TOKEN")
    chat_id = vars_data.get('telegram_chat_id') or os.environ.get("TELEGRAM_CHAT_ID")
    default_rpc_port = vars_data.get('parent_chain_rpc_port', 18601)

    p_nodes = (inv.get('all', {}).get('children', {}).get('parent_chain_nodes', {}) or {}).get('hosts', {}) or {}
    if not p_nodes:
        sys.exit(f"❌ Không tìm thấy nhóm 'parent_chain_nodes' trong inventory: {inv_file}")

    nodes = {}
    for name, data in p_nodes.items():
        if isinstance(data, dict):
            host = data.get('ansible_host')
            if not host:
                continue
            port = data.get('parent_http_port', default_rpc_port)
            nodes[name] = f"http://{host}:{port}"

    if not nodes:
        sys.exit(f"❌ Không tìm thấy node nào trong 'parent_chain_nodes.hosts' của {inv_file}")

    return nodes, bot_token, chat_id, inv_file

def send_telegram(bot_token, chat_id, message_html):
    """Send alert HTML message to Telegram with error handling."""
    if not bot_token or not chat_id:
        sys.stderr.write("⚠️ Telegram bot_token hoặc chat_id chưa được cấu hình. Bỏ qua thông báo.\n")
        return False
    url = f"https://api.telegram.org/bot{bot_token}/sendMessage"
    payload = {
        "chat_id": chat_id,
        "text": message_html,
        "parse_mode": "HTML",
        "disable_web_page_preview": True
    }
    try:
        data = urllib.parse.urlencode(payload).encode("utf-8")
        req = urllib.request.Request(url, data=data, method="POST")
        with urllib.request.urlopen(req, timeout=10) as resp:
            return resp.status == 200
    except Exception as e:
        sys.stderr.write(f"Failed to send Telegram alert: {e}\n")
        return False

def query_node_status(url, timeout=3):
    """Query /status endpoint of a Parent Chain node."""
    target_url = f"{url.rstrip('/')}/status"
    req = urllib.request.Request(target_url, headers={"User-Agent": "ParentChainMonitor/1.0"})
    start_time = time.time()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            elapsed_ms = int((time.time() - start_time) * 1000)
            if resp.status == 200:
                body = resp.read().decode("utf-8")
                data = json.loads(body)
                return {
                    "online": True,
                    "status_code": resp.status,
                    "latency_ms": elapsed_ms,
                    "chain_id": data.get("chain_id"),
                    "last_block": data.get("last_block", 0),
                    "last_hash": data.get("last_hash", ""),
                    "state_root": data.get("state_root", ""),
                    "fork_detected": data.get("fork_detected", False),
                    "syncing": data.get("syncing", False)
                }
            return {
                "online": False,
                "status_code": resp.status,
                "latency_ms": elapsed_ms,
                "error": f"HTTP {resp.status}"
            }
    except Exception as e:
        elapsed_ms = int((time.time() - start_time) * 1000)
        return {
            "online": False,
            "status_code": 0,
            "latency_ms": elapsed_ms,
            "error": str(e)
        }

class AlertThrottle:
    """Manages cooldowns and state transitions to prevent alert spam."""
    def __init__(self, reminder_interval=300):
        self.reminder_interval = reminder_interval
        self.node_states = {}       # name -> bool (online)
        self.node_last_alert = {}    # name -> float (timestamp)
        self.hash_mismatch_active = False
        self.last_hash_alert_time = 0

    def should_alert_node_down(self, name):
        now = time.time()
        was_online = self.node_states.get(name, True)
        self.node_states[name] = False
        last_alert = self.node_last_alert.get(name, 0)
        if was_online or (now - last_alert > self.reminder_interval):
            self.node_last_alert[name] = now
            return True
        return False

    def should_alert_node_recovered(self, name):
        was_online = self.node_states.get(name, True)
        self.node_states[name] = True
        if not was_online:
            self.node_last_alert.pop(name, None)
            return True
        return False

    def should_alert_hash_mismatch(self):
        now = time.time()
        if not self.hash_mismatch_active or (now - self.last_hash_alert_time > self.reminder_interval):
            self.hash_mismatch_active = True
            self.last_hash_alert_time = now
            return True
        return False

    def should_alert_hash_resolved(self):
        if self.hash_mismatch_active:
            self.hash_mismatch_active = False
            self.last_hash_alert_time = 0
            return True
        return False

def run_monitor_loop(inventory_path=None, interval=5):
    nodes, bot_token, chat_id, inv_file = load_config(inventory_path)
    throttler = AlertThrottle(reminder_interval=300)

    print(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🚀 Starting Parent Chain Monitor")
    print(f"Config Source: {inv_file}")
    print(f"Interval: {interval}s, Monitored Nodes: {len(nodes)}")
    for name, url in nodes.items():
        print(f"  • {name}: {url}")

    while True:
        try:
            results = {}
            for name, url in nodes.items():
                results[name] = query_node_status(url)

            # 1. Liveness Check
            for name, res in results.items():
                if not res["online"]:
                    if throttler.should_alert_node_down(name):
                        err_text = html.escape(str(res.get('error', 'Unreachable')))
                        msg = (
                            f"🚨 <b>[PARENT CHAIN ALERT] Node Offline!</b>\n"
                            f"━━━━━━━━━━━━━━━━━━\n"
                            f"<b>Node:</b> <code>{name}</code>\n"
                            f"<b>Endpoint:</b> <code>{nodes[name]}</code>\n"
                            f"<b>Lỗi:</b> <code>{err_text}</code>\n"
                            f"<b>Thời gian:</b> <code>{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}</code>\n"
                            f"⚠️ Cụm Parent Chain đang thiếu validator. Kiểm tra dịch vụ ngay!"
                        )
                        print(f"❌ {name} OFFLINE: {res.get('error')}")
                        send_telegram(bot_token, chat_id, msg)
                else:
                    if throttler.should_alert_node_recovered(name):
                        msg = (
                            f"✅ <b>[PARENT CHAIN RECOVERY] Node Online</b>\n"
                            f"━━━━━━━━━━━━━━━━━━\n"
                            f"<b>Node:</b> <code>{name}</code>\n"
                            f"<b>Endpoint:</b> <code>{nodes[name]}</code>\n"
                            f"<b>Block:</b> #{res.get('last_block')} | <b>Latency:</b> {res.get('latency_ms')}ms\n"
                            f"<b>Thời gian:</b> <code>{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}</code>"
                        )
                        print(f"✅ {name} RECOVERED")
                        send_telegram(bot_token, chat_id, msg)

            # 2. Hash Consistency & Fork Check (among online nodes)
            online_nodes = {name: res for name, res in results.items() if res["online"]}
            fork_detected_nodes = [name for name, res in online_nodes.items() if res.get("fork_detected")]

            if fork_detected_nodes:
                if throttler.should_alert_hash_mismatch():
                    msg = (
                        f"🚨 <b>[PARENT CHAIN CRITICAL] FORK DETECTED!</b>\n"
                        f"━━━━━━━━━━━━━━━━━━\n"
                        f"<b>Các node báo fork:</b> <code>{', '.join(fork_detected_nodes)}</code>\n"
                        f"<b>Thời gian:</b> <code>{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}</code>\n"
                        f"⚠️ Cờ fork_detected=true! Mạng L1 có mâu thuẫn block. Hãy kiểm tra ngay!"
                    )
                    print(f"🚨 FORK DETECTED on {fork_detected_nodes}")
                    send_telegram(bot_token, chat_id, msg)

            # Compare last_hash and state_root across nodes at same block height
            by_block = {}
            for name, res in online_nodes.items():
                blk = res["last_block"]
                if blk not in by_block:
                    by_block[blk] = []
                by_block[blk].append((name, res["last_hash"], res["state_root"]))

            has_mismatch = False
            mismatch_details = []

            for blk, node_list in by_block.items():
                if len(node_list) > 1:
                    first_name, first_hash, first_root = node_list[0]
                    for name, h, root in node_list[1:]:
                        if h != first_hash or root != first_root:
                            has_mismatch = True
                            mismatch_details.append(
                                f"• Block #{blk}:\n"
                                f"  - {first_name}: hash={first_hash[:16]}... root={first_root[:16]}...\n"
                                f"  - {name}: hash={h[:16]}... root={root[:16]}..."
                            )

            if has_mismatch:
                if throttler.should_alert_hash_mismatch():
                    msg = (
                        f"🚨 <b>[PARENT CHAIN ALERT] Hash / State Root Mismatch!</b>\n"
                        f"━━━━━━━━━━━━━━━━━━\n"
                        f"<b>Chi tiết bất đồng bộ:</b>\n"
                        f"{chr(10).join(mismatch_details)}\n"
                        f"<b>Thời gian:</b> <code>{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}</code>\n"
                        f"⚠️ Các node có hash khác nhau tại cùng block height! Nguy cơ chia rẽ mạng!"
                    )
                    print(f"🚨 HASH MISMATCH DETECTED:\n{chr(10).join(mismatch_details)}")
                    send_telegram(bot_token, chat_id, msg)
            else:
                if throttler.should_alert_hash_resolved():
                    msg = (
                        f"✅ <b>[PARENT CHAIN RESOLVED] Hash Consistency Đồng Bộ</b>\n"
                        f"━━━━━━━━━━━━━━━━━━\n"
                        f"Tất cả {len(online_nodes)} node online đã đồng thuận trên cùng block hash & state root.\n"
                        f"<b>Thời gian:</b> <code>{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}</code>"
                    )
                    print("✅ Hash consistency restored across all nodes")
                    send_telegram(bot_token, chat_id, msg)

        except Exception as e:
            sys.stderr.write(f"Monitor loop error: {e}\n")

        time.sleep(interval)

def check_status_once(inventory_path=None):
    """Run single inspection and print summary table."""
    nodes, _, _, inv_file = load_config(inventory_path)
    print("═══════════════════════════════════════════════════════════════════")
    print("🔍 PARENT CHAIN LIVE HEALTH & HASH CONSISTENCY CHECK")
    print(f"📄 Nguồn cấu hình: {inv_file}")
    print("═══════════════════════════════════════════════════════════════════")
    all_ok = True
    by_block = {}

    for name, url in nodes.items():
        res = query_node_status(url)
        if res["online"]:
            status_str = f"✅ ONLINE ({res['latency_ms']}ms)"
            blk = res["last_block"]
            h = res["last_hash"]
            root = res["state_root"]
            fork = "🚨 FORK!" if res["fork_detected"] else "OK"
            print(f"• {name} ({url}): {status_str}")
            print(f"   Block: #{blk} | Hash: {h[:18]}... | StateRoot: {root[:18]}... | Fork: {fork}")
            if blk not in by_block:
                by_block[blk] = []
            by_block[blk].append((name, h, root))
        else:
            all_ok = False
            print(f"• {name} ({url}): ❌ OFFLINE ({res.get('error')})")

    print("───────────────────────────────────────────────────────────────────")
    has_mismatch = False
    for blk, node_list in by_block.items():
        if len(node_list) > 1:
            first_name, first_hash, first_root = node_list[0]
            for name, h, root in node_list[1:]:
                if h != first_hash or root != first_root:
                    has_mismatch = True
                    print(f"🚨 MISMATCH AT BLOCK #{blk}: {first_name} vs {name}!")

    if not has_mismatch and len(by_block) > 0:
        print("✅ HASH CONSISTENCY: Tất cả các node đều khớp Hash và State Root 100%!")
    if all_ok and not has_mismatch:
        print("🎉 TRẠNG THÁI: TẤT CẢ CÁC NODE PARENT CHAIN HOẠT ĐỘNG HOÀN HẢO!")
    print("═══════════════════════════════════════════════════════════════════")

def start_daemon(inventory_path=None, interval=5):
    """Start monitor process in the background."""
    if os.path.exists(PID_FILE):
        try:
            with open(PID_FILE, 'r') as f:
                old_pid = int(f.read().strip())
            os.kill(old_pid, 0)
            print(f"Parent Chain Monitor already running (PID: {old_pid})")
            return
        except (OSError, ValueError):
            pass

    import subprocess
    cmd = [sys.executable, "-u", os.path.abspath(__file__), f"--interval={interval}"]
    if inventory_path:
        cmd.append(f"--inventory={os.path.abspath(inventory_path)}")
    with open(LOG_FILE, "a") as out:
        proc = subprocess.Popen(cmd, stdout=out, stderr=out, close_fds=True)
    with open(PID_FILE, "w") as f:
        f.write(str(proc.pid))
    print(f"✅ Parent Chain Monitor started in background (PID: {proc.pid}, Log: {LOG_FILE})")

def stop_daemon():
    """Stop the background monitor process."""
    if os.path.exists(PID_FILE):
        try:
            with open(PID_FILE, 'r') as f:
                pid = int(f.read().strip())
            os.kill(pid, 15)
            time.sleep(1)
            os.kill(pid, 9)
        except OSError:
            pass
        try:
            os.remove(PID_FILE)
        except OSError:
            pass
        print("⏹️ Parent Chain Monitor stopped.")
    else:
        print("Parent Chain Monitor is not running.")

if __name__ == "__main__":
    interval = 5
    inventory_file = None
    for arg in sys.argv[1:]:
        if arg.startswith("--interval="):
            try:
                interval = int(arg.split("=")[1])
            except ValueError:
                pass
        elif arg.startswith("--inventory="):
            inventory_file = arg.split("=")[1]
        elif arg == "-i" and len(sys.argv) > sys.argv.index(arg) + 1:
            inventory_file = sys.argv[sys.argv.index(arg) + 1]

    if "--stop" in sys.argv:
        stop_daemon()
    elif "--daemon" in sys.argv:
        start_daemon(inventory_path=inventory_file, interval=interval)
    elif "--status" in sys.argv or "--once" in sys.argv:
        check_status_once(inventory_path=inventory_file)
    elif "--test-alert" in sys.argv:
        _, token, cid, inv_f = load_config(inventory_path=inventory_file)
        ok = send_telegram(token, cid, f"🔔 <b>[PARENT CHAIN MONITOR]</b> Test Alert: Đọc cấu hình từ <code>{inv_f}</code> thành công!")
        print("✅ Alert sent successfully!" if ok else "❌ Failed to send alert.")
    else:
        # Foreground mode
        run_monitor_loop(inventory_path=inventory_file, interval=interval)
