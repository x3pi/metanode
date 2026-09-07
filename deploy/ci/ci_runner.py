#!/usr/bin/env python3
"""
Metanode Continuous Integration Test Runner.
Executes test suites according to ci_config.yaml with timeout control,
realtime logging, pre-test actions (e.g. reset_chain for TPS), and Telegram alerting.
"""

import sys
import os
import time
import argparse
import subprocess
import socket
from datetime import datetime
import yaml
import json

# Import Telegram notifier from current directory
BASE_DIR = os.path.abspath(os.path.dirname(__file__))
sys.path.insert(0, BASE_DIR)
import telegram_notify

def get_server_ip():
    """Detect server IP address."""
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.settimeout(0.5)
        # Doesn't have to be reachable, used to get preferred route
        s.connect(('8.8.8.8', 80))
        ip = s.getsockname()[0]
        s.close()
        return ip
    except Exception:
        try:
            out = subprocess.check_output(["hostname", "-I"], text=True).strip()
            return out.split()[0] if out else "127.0.0.1"
        except Exception:
            return "127.0.0.1"

def get_git_info(repo_path):
    """Extract current git commit info."""
    try:
        commit_hash = subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=repo_path, text=True
        ).strip()
        author = subprocess.check_output(
            ["git", "log", "-1", "--pretty=%an"], cwd=repo_path, text=True
        ).strip()
        message = subprocess.check_output(
            ["git", "log", "-1", "--pretty=%s"], cwd=repo_path, text=True
        ).strip()
        return {"hash": commit_hash, "author": author, "message": message}
    except Exception as e:
        return {"hash": "unknown", "author": "unknown", "message": f"error: {e}"}

def run_shell_cmd(cmd, cwd=None, timeout=None, log_file=None):
    """
    Run a shell command, stream output to log_file (and terminal), with timeout.
    Returns (exit_code, full_output).
    """
    start_time = time.time()
    log_fp = None
    if log_file:
        os.makedirs(os.path.dirname(os.path.abspath(log_file)), exist_ok=True)
        log_fp = open(log_file, "w", encoding="utf-8")

    try:
        proc = subprocess.Popen(
            cmd,
            cwd=cwd,
            shell=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            bufsize=1
        )

        output_lines = []
        try:
            for line in proc.stdout:
                output_lines.append(line)
                sys.stdout.write(line)
                sys.stdout.flush()
                if log_fp:
                    log_fp.write(line)
                    log_fp.flush()
            proc.wait(timeout=timeout)
            exit_code = proc.returncode
        except subprocess.TimeoutExpired:
            proc.kill()
            msg = f"\n❌ [TIMEOUT] Lệnh bị hủy vì vượt quá giới hạn thời gian {timeout}s!\n"
            sys.stdout.write(msg)
            if log_fp:
                log_fp.write(msg)
            output_lines.append(msg)
            exit_code = 124

        if log_fp:
            log_fp.close()

        return exit_code, "".join(output_lines)
    except Exception as e:
        if log_fp:
            log_fp.close()
        return 1, f"Execution failed: {e}"

def tail_file(filepath, n_lines=25):
    """Return last N lines of a file."""
    if not os.path.isfile(filepath):
        return "(Log file not found)"
    try:
        with open(filepath, "r", encoding="utf-8", errors="replace") as f:
            lines = f.readlines()
            return "".join(lines[-n_lines:])
    except Exception as e:
        return f"(Error reading log: {e})"

def main():
    parser = argparse.ArgumentParser(description="Metanode CI/CD Automated Test Runner")
    parser.add_argument("--config", default=os.path.join(BASE_DIR, "ci_config.yaml"), help="Path to config yaml")
    parser.add_argument("--only", help="Run only a specific test ID (e.g. tps_blast, blockstm_logic)")
    parser.add_argument("--pull", action="store_true", help="Pull git remote before running")
    parser.add_argument("--skip-build-check", action="store_true", help="Skip build verification")
    parser.add_argument("--skip-pre-action", action="store_true", help="Skip pre-actions (reset/restart chain)")
    parser.add_argument("--dry-run", action="store_true", help="Print actions without executing")
    args = parser.parse_args()

    # 1. Load config
    if not os.path.isfile(args.config):
        print(f"❌ Không tìm thấy file cấu hình: {args.config}")
        example_cfg = os.path.join(BASE_DIR, "ci_config.yaml.example")
        if os.path.isfile(example_cfg):
            print(f"👉 Vui lòng sao chép từ file mẫu và cấu hình thông số cần thiết:")
            print(f"   cp {example_cfg} {args.config}")
        sys.exit(1)

    with open(args.config, "r", encoding="utf-8") as f:
        config = yaml.safe_load(f)

    DEFAULT_METANODE_DIR = os.path.abspath(os.path.join(BASE_DIR, "..", ".."))
    DEFAULT_SUITE_DIR = os.path.abspath(os.path.join(DEFAULT_METANODE_DIR, "..", "metanode-suite"))

    git_cfg = config.get("git", {})
    raw_repo_path = git_cfg.get("repo_path", "auto")
    if not raw_repo_path or raw_repo_path in ["auto", "."]:
        repo_path = DEFAULT_METANODE_DIR
    elif not os.path.isabs(raw_repo_path):
        repo_path = os.path.abspath(os.path.join(DEFAULT_METANODE_DIR, raw_repo_path))
    else:
        repo_path = os.path.abspath(raw_repo_path)

    raw_suite_path = git_cfg.get("suite_path", "../metanode-suite")
    if not raw_suite_path or raw_suite_path == "auto":
        suite_path = DEFAULT_SUITE_DIR
    elif not os.path.isabs(raw_suite_path):
        suite_path = os.path.abspath(os.path.join(repo_path, raw_suite_path))
    else:
        suite_path = os.path.abspath(raw_suite_path)

    def interpolate_paths(text):
        if not isinstance(text, str):
            return text
        return text.replace("{METANODE_DIR}", repo_path).replace("{SUITE_DIR}", suite_path)

    branch = git_cfg.get("branch", "main")
    remote = git_cfg.get("remote", "origin")

    tele_cfg = config.get("telegram", {})
    tele_enabled = tele_cfg.get("enabled", True)
    tele_token = tele_cfg.get("bot_token")
    tele_chat_id = tele_cfg.get("chat_id")

    chain_actions = config.get("chain_actions", {})
    tests = config.get("tests", [])

    server_ip = get_server_ip()

    # 2. Git Pull if requested
    if args.pull:
        print(f"\n📥 [GIT PULL] Đang kéo mã nguồn mới nhất từ {remote}/{branch}...")
        if not args.dry_run:
            # Khôi phục genesis.json.example về trạng thái sạch của git trước khi pull để tránh merge conflict
            run_shell_cmd("git checkout -- deploy/systemd/genesis.json.example 2>/dev/null || true", cwd=repo_path)
            pull_res, _ = run_shell_cmd(f"git checkout {branch} && git pull {remote} {branch}", cwd=repo_path)
            if pull_res != 0:
                print("❌ Git pull thất bại!")
                sys.exit(pull_res)
            if os.path.isdir(os.path.join(suite_path, ".git")):
                run_shell_cmd("git pull", cwd=suite_path)

    commit_info = get_git_info(repo_path)
    print("=" * 70)
    print(f"🚀 METANODE CI RUNNER")
    print(f"📍 Repo:   {repo_path} ({branch} @ {commit_info['hash'][:8]})")
    print(f"👤 Author: {commit_info['author']}")
    print(f"💬 Commit: {commit_info['message']}")
    print(f"🖥  Server: {server_ip}")
    print("=" * 70)

    # Send Telegram start notification
    if tele_enabled and tele_cfg.get("notify_on_start", True) and not args.dry_run:
        start_msg = telegram_notify.build_start_message(commit_info, branch, server_ip)
        telegram_notify.send_telegram_message(tele_token, tele_chat_id, start_msg)

    # 2.5. Check & Ensure Genesis Keys (Chỉ nạp ví vào genesis.json, KHÔNG sửa genesis.json.example để tránh phình Git)
    genesis_example = os.path.join(repo_path, "deploy", "systemd", "genesis.json.example")
    genesis_active = os.path.join(repo_path, "deploy", "systemd", "genesis.json")
    keys_file = os.path.join(suite_path, "test_tps", "gen_spam_keys", "generated_keys.json")
    manage_genesis = os.path.join(suite_path, "test_tps", "gen_spam_keys", "manage_genesis.py")

    if os.path.isfile(genesis_example) and os.path.isfile(keys_file) and os.path.isfile(manage_genesis):
        try:
            genesis_cfg = config.get("genesis", {})
            required_min_keys = int(genesis_cfg.get("required_min_keys", 50000))
            print(f"\n🔑 [GENESIS CHECK] Kiểm tra file genesis.json (chứa ví test TPS/Spam)...")

            need_update = False
            if not os.path.isfile(genesis_active):
                print(f"⚠️ Chưa có file genesis.json ({genesis_active}). Đang tạo mới từ {genesis_example} và nạp keys...")
                need_update = True
            else:
                with open(genesis_active, "r", encoding="utf-8") as gaf:
                    ga_data = json.load(gaf)
                active_alloc_count = len(ga_data.get("alloc", []))
                print(f"   - Số lượng ví alloc trong genesis.json: {active_alloc_count} (Yêu cầu tối thiểu: {required_min_keys})")
                if active_alloc_count < required_min_keys:
                    print(f"⚠️ genesis.json chỉ có {active_alloc_count} ví (< {required_min_keys}). Đang nạp thêm keys từ {keys_file}...")
                    need_update = True
                else:
                    print(f"✅ genesis.json đã có đủ {active_alloc_count} ví (genesis.json.example trên Git vẫn giữ nguyên bản sạch)!")

            if need_update:
                add_cmd = f"python3 {manage_genesis} add {genesis_example} {keys_file} {genesis_active}"
                r_c, _ = run_shell_cmd(add_cmd, cwd=repo_path)
                if r_c == 0:
                    with open(genesis_active, "r", encoding="utf-8") as gaf:
                        ga_data = json.load(gaf)
                    print(f"✅ Đã nạp thành công {len(ga_data.get('alloc', []))} ví vào genesis.json (giữ nguyên genesis.json.example trên Git)!")
                else:
                    print(f"⚠️ Cảnh báo: Lệnh nạp ví vào genesis.json trả về mã lỗi {r_c}")
        except Exception as e:
            print(f"⚠️ Cảnh báo khi kiểm tra genesis: {e}")

    pipeline_start_time = time.time()

    # 3. Pre-build check if enabled
    build_check_cmd = interpolate_paths(chain_actions.get("build_check_cmd"))
    if build_check_cmd and not args.skip_build_check:
        print(f"\n🔨 [BUILD CHECK] Đang kiểm tra biên dịch hệ thống...")
        if args.dry_run:
            print(f"  [DRY-RUN] Sẽ chạy: {build_check_cmd}")
        else:
            build_code, _ = run_shell_cmd(build_check_cmd, cwd=repo_path)
            if build_code != 0:
                print("❌ Build check thất bại! Dừng toàn bộ pipeline.")
                if tele_enabled and tele_cfg.get("notify_on_test_fail", True):
                    fail_msg = telegram_notify.build_failure_message(
                        commit_info, branch, "Build Check (Go/Rust/FFI)", build_code,
                        "console", "Build compilation failed. See server console.", server_ip
                    )
                    telegram_notify.send_telegram_message(tele_token, tele_chat_id, fail_msg)
                sys.exit(build_code)
            print("✅ Build check biên dịch thành công!")

    # 4. Prepare logs directory
    run_timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    run_log_dir = os.path.join(BASE_DIR, "logs", f"run_{run_timestamp}")
    os.makedirs(run_log_dir, exist_ok=True)

    # 5. Run Test Matrix
    test_results = []
    has_failure = False

    for test in tests:
        test_id = test.get("id")
        test_name = test.get("name", test_id)
        enabled = test.get("enabled", True)
        pre_action = test.get("pre_action", "none")
        raw_cwd = test.get("cwd", suite_path)
        raw_cwd = interpolate_paths(raw_cwd)
        if not os.path.isabs(raw_cwd):
            cwd = os.path.abspath(os.path.join(suite_path, raw_cwd))
        else:
            cwd = os.path.abspath(raw_cwd)

        command = interpolate_paths(test.get("command"))
        timeout_sec = test.get("timeout_seconds", 600)
        continue_on_fail = test.get("continue_on_failure", False)

        # Filter if --only flag was given
        if args.only and test_id != args.only:
            continue

        if not enabled:
            print(f"\n⏭️  [SKIPPED] Bỏ qua bài test: {test_name} (disabled)")
            continue

        print(f"\n" + "-" * 70)
        print(f"🧪 BẮT ĐẦU TEST: {test_name} (ID: {test_id})")
        print(f"📂 Thư mục: {cwd}")
        print(f"⚙️  Lệnh:    {command}")
        print(f"⏱️  Timeout: {timeout_sec}s")
        print(f"🔄 Pre-action: {pre_action}")
        print("-" * 70)

        # Pre-action handling
        if not args.skip_pre_action and not args.dry_run:
            if pre_action in ["reset_chain", "reset_public"]:
                reset_cmd = interpolate_paths(chain_actions.get("reset_cmd") or chain_actions.get("reset_public_cmd"))
                update_ip_cmd = interpolate_paths(chain_actions.get("update_ip_cmd"))
                print(f"👉 [PRE-ACTION] Reset cụm Public Chain để đạt môi trường sạch & TPS tối đa...")
                if reset_cmd:
                    r_code, _ = run_shell_cmd(reset_cmd, cwd=repo_path)
                    if r_code != 0:
                        print(f"⚠️ Cảnh báo: Lệnh reset chain trả về mã lỗi {r_code}")
                if update_ip_cmd:
                    print(f"👉 [PRE-ACTION] Đồng bộ lại IP/RPC endpoints...")
                    run_shell_cmd(update_ip_cmd, cwd=repo_path)
                wait_sec = chain_actions.get("wait_rpc_ready_seconds", 5)
                time.sleep(wait_sec)
            elif pre_action in ["deploy_private", "reset_private"]:
                deploy_private_cmd = interpolate_paths(chain_actions.get("deploy_private_cmd"))
                print(f"👉 [PRE-ACTION] Triển khai lại cụm Private Chains & Relayer Daemon...")
                if deploy_private_cmd:
                    r_code, _ = run_shell_cmd(deploy_private_cmd, cwd=repo_path)
                    if r_code != 0:
                        print(f"⚠️ Cảnh báo: Lệnh deploy private chains trả về mã lỗi {r_code}")
                wait_sec = chain_actions.get("wait_rpc_ready_seconds", 5)
                time.sleep(wait_sec)
            elif pre_action in ["prepare_tps", "reset_tps_chain"]:
                tps_prep_cmd = interpolate_paths(chain_actions.get("prepare_tps_cmd") or chain_actions.get("reset_tps_chain_cmd"))
                print(f"👉 [PRE-ACTION] Nạp 50k ví TPS (lọc trùng), xóa genesis.json cũ và reset cụm node...")
                if tps_prep_cmd:
                    r_code, _ = run_shell_cmd(tps_prep_cmd, cwd=repo_path)
                    if r_code != 0:
                        print(f"⚠️ Cảnh báo: Lệnh prepare_tps trả về mã lỗi {r_code}")
                wait_sec = chain_actions.get("wait_rpc_ready_seconds", 5)
                time.sleep(wait_sec)
            elif pre_action == "restart_chain":
                restart_cmd = interpolate_paths(chain_actions.get("restart_cmd"))
                print(f"👉 [PRE-ACTION] Restart nhanh cụm node...")
                if restart_cmd:
                    run_shell_cmd(restart_cmd, cwd=repo_path)
                wait_sec = chain_actions.get("wait_rpc_ready_seconds", 3)
                time.sleep(wait_sec)
            elif pre_action != "none" and pre_action in chain_actions:
                custom_cmd = interpolate_paths(chain_actions.get(pre_action))
                print(f"👉 [PRE-ACTION] Thực thi hành động tùy biến: {pre_action}...")
                run_shell_cmd(custom_cmd, cwd=repo_path)

        # Execute test
        test_log_file = os.path.join(run_log_dir, f"{test_id}.log")
        test_start = time.time()

        if args.dry_run:
            print(f"  [DRY-RUN] Sẽ thực thi tại {cwd}: {command}")
            test_results.append({"id": test_id, "name": test_name, "duration": 0, "status": "DRY_RUN"})
            continue

        exit_code, _ = run_shell_cmd(command, cwd=cwd, timeout=timeout_sec, log_file=test_log_file)
        test_dur = time.time() - test_start

        # Create/Update 'latest' log symlink
        latest_link = os.path.join(BASE_DIR, "logs", f"latest_{test_id}.log")
        try:
            if os.path.islink(latest_link) or os.path.exists(latest_link):
                os.remove(latest_link)
            os.symlink(test_log_file, latest_link)
        except Exception:
            pass

        if exit_code == 0:
            print(f"✅ THÀNH CÔNG: {test_name} ({telegram_notify.format_duration(test_dur)})")
            
            # Trích xuất thông số TPS / Throughput / Latency nếu có trong log
            extra_info = ""
            if os.path.exists(test_log_file):
                try:
                    with open(test_log_file, "r", encoding="utf-8", errors="ignore") as lf:
                        content = lf.read()
                        import re
                        min_m = re.search(r"Min TPS\s*:\s*(~?[\d\.]+)\s*tx/s", content)
                        max_m = re.search(r"Max TPS\s*:\s*(~?[\d\.]+)\s*tx/s", content)
                        avg_m = re.search(r"Avg TPS\s*:\s*(~?[\d\.]+)\s*tx/s", content)
                        if min_m and max_m and avg_m:
                            min_str = min_m.group(1) if min_m.group(1).startswith("~") else f"~{min_m.group(1)}"
                            max_str = max_m.group(1) if max_m.group(1).startswith("~") else f"~{max_m.group(1)}"
                            avg_str = avg_m.group(1) if avg_m.group(1).startswith("~") else f"~{avg_m.group(1)}"
                            extra_info = f"TPS: Min {min_str} | Max {max_str} | Avg {avg_str} tx/s"
                        else:
                            e2e_m = re.findall(r"End-to-End TPS\s*:\s*(~?[\d\.]+)\s*tx/s", content)
                            if e2e_m:
                                nums = []
                                for x in e2e_m:
                                    try:
                                        nums.append(float(x.replace("~", "").strip()))
                                    except Exception:
                                        pass
                                if len(nums) > 1:
                                    extra_info = f"TPS: Min ~{int(min(nums))} | Max ~{int(max(nums))} | Avg ~{int(sum(nums)/len(nums))} tx/s"
                                elif len(nums) == 1:
                                    val = f"~{int(nums[0])}"
                                    extra_info = f"TPS: Min {val} | Max {val} | Avg {val} tx/s"
                except Exception:
                    pass

            test_results.append({
                "id": test_id,
                "name": test_name,
                "duration": test_dur,
                "status": "PASS",
                "extra": extra_info
            })
        else:
            has_failure = True
            print(f"❌ THẤT BẠI: {test_name} (Mã lỗi: {exit_code}) sau {telegram_notify.format_duration(test_dur)}")
            test_results.append({
                "id": test_id,
                "name": test_name,
                "duration": test_dur,
                "status": "FAIL",
                "exit_code": exit_code
            })

            # Send Telegram notification on failure
            if tele_enabled and tele_cfg.get("notify_on_test_fail", True):
                tail_logs = tail_file(test_log_file, n_lines=25)
                fail_msg = telegram_notify.build_failure_message(
                    commit_info, branch, test_name, exit_code, test_log_file, tail_logs, server_ip
                )
                telegram_notify.send_telegram_message(tele_token, tele_chat_id, fail_msg)

            if not continue_on_fail:
                print(f"🛑 Dừng pipeline do cấu hình 'continue_on_failure: false'.")
                break

    # 6. Pipeline Summary
    total_pipeline_duration = time.time() - pipeline_start_time
    print("\n" + "=" * 70)
    print("📊 BÁO CÁO TỔNG KẾT METANODE CI PIPELINE")
    print(f"⏱️  Tổng thời gian: {telegram_notify.format_duration(total_pipeline_duration)}")
    print("=" * 70)
    for r in test_results:
        st_icon = "✅" if r["status"] == "PASS" else ("⏭️" if r["status"] == "DRY_RUN" else "❌")
        extra_str = f"\n   └─ 📊 {r['extra']}" if r.get("extra") else ""
        print(f"{st_icon} {r['name']} - {r['status']} ({telegram_notify.format_duration(r['duration'])}){extra_str}")
    print("=" * 70)

    if not has_failure and not args.dry_run:
        if tele_enabled and tele_cfg.get("notify_on_finish", True):
            succ_msg = telegram_notify.build_finish_success_message(
                commit_info, branch, total_pipeline_duration, test_results, server_ip
            )
            telegram_notify.send_telegram_message(tele_token, tele_chat_id, succ_msg)
        print("🎉 TẤT CẢ CÁC BÀI TEST ĐÃ HOÀN TẤT THÀNH CÔNG!")
        sys.exit(0)
    elif has_failure:
        print("🚨 PIPELINE KẾT THÚC VỚI LỖI!")
        sys.exit(1)

if __name__ == "__main__":
    main()
