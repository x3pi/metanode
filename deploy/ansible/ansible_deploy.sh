#!/bin/bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║  ANSIBLE MULTI-SERVER CLUSTER DEPLOYMENT WRAPPER                  ║
# ║                                                                   ║
# ║  Usage: ./ansible_deploy.sh [OPTIONS]                             ║
# ║  Options:                                                         ║
# ║    --start             Start nodes (re-distribute binaries)       ║
# ║    --restart           Fast restart systemd services              ║
# ║    --setup             Fresh setup (gen keys, clears data)        ║
# ║    --stop              Stop nodes                                 ║
# ║    --clean             Clear data before starting nodes           ║
# ║    --only-node N       Only apply actions to node N               ║
# ║    --restore-node N    Restore node N from snapshot url           ║
# ║    --snapshot-url U    Snapshot URL to use (e.g. http://ip:8604)  ║
# ║    --open-ports        Open firewall ports for the nodes          ║
# ║    --all-monitors      Run monitors mutually across ALL machines  ║
# ╚═══════════════════════════════════════════════════════════════════╝

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Load environment variables from .env if exists
load_env_file() {
    local env_file="$1"
    if [ -f "$env_file" ]; then
        while IFS= read -r line || [ -n "$line" ]; do
            if [[ "$line" =~ ^[[:space:]]*# ]] || [[ -z "$line" ]]; then
                continue
            fi
            if [[ "$line" =~ = ]]; then
                local key val
                key=$(echo "${line%%=*}" | xargs)
                val=$(echo "${line#*=}" | xargs)
                val="${val%\"}"
                val="${val#\"}"
                val="${val%\'}"
                val="${val#\'}"
                export "$key"="$val"
            fi
        done < "$env_file"
    fi
}

TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-""}"
TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-""}"

load_telegram_config() {
    local target_yml="$1"
    local token=""
    local chat_id=""

    # 1. First priority: Read directly from target YAML (inventory.yml or monitors/inventory.yml)
    # BUG FIX: under `set -euo pipefail`, a `grep` that matches nothing (the normal case for
    # any cluster without Telegram configured, e.g. a local dev cluster) exits 1, which
    # pipefail propagates through `| head | awk | sed` and kills the WHOLE script right here
    # with zero output -- reproduced live testing this on local 232's inventory.yml (no
    # telegram_bot_token line). `|| true` on each grep keeps a genuine no-match a normal,
    # silent "not configured" case instead of a fatal, unexplained script exit.
    if [ -f "$target_yml" ]; then
        token=$(grep -E '^\s*(telegram_bot_token|bot_token):' "$target_yml" 2>/dev/null | head -n 1 | awk '{print $2}' | sed 's/["\x27]//g' || true)
        chat_id=$(grep -E '^\s*(telegram_chat_id|chat_id):' "$target_yml" 2>/dev/null | head -n 1 | awk '{print $2}' | sed 's/["\x27]//g' || true)
    fi

    # 2. Fallback to .env if not found in YAML
    if [ -z "$token" ]; then
        load_env_file "${SCRIPT_DIR}/.env"
        load_env_file "${SCRIPT_DIR}/../.env"
        [ -n "${TELEGRAM_BOT_TOKEN:-}" ] && token="$TELEGRAM_BOT_TOKEN"
        [ -n "${TELEGRAM_CHAT_ID:-}" ] && chat_id="$TELEGRAM_CHAT_ID"
    fi

    [ -n "$token" ] && TELEGRAM_BOT_TOKEN="$token"
    [ -n "$chat_id" ] && TELEGRAM_CHAT_ID="$chat_id"
    export TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-""}"
    export TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-"-1003867050625"}"
}

send_telegram_notification() {
    local message="$1"
    if [ -n "$TELEGRAM_BOT_TOKEN" ]; then
        curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
            -d "chat_id=${TELEGRAM_CHAT_ID}" \
            -d "parse_mode=HTML" \
            --data-urlencode "text=${message}" > /dev/null 2>&1 || true
    fi
}

INVENTORY="${SCRIPT_DIR}/inventory.yml"
PLAYBOOK="${SCRIPT_DIR}/deploy.yml"

# ANSIBLE-VAULT SUPPORT (2026-09, GitHub issue #104 hardening): purely opt-in and
# backward-compatible -- if you never touch ansible-vault, VAULT_ARGS stays empty
# and every ansible-playbook call below behaves exactly as before. To use it:
# encrypt just the password fields in inventory.yml with
# `ansible-vault encrypt_string --vault-password-file .vault_pass 'mat_khau_sudo' --name ansible_become_pass`
# and paste the `!vault |` block it prints in place of the plaintext value (see
# inventory.example.yml's "Cách B" comment for the full recipe). Store the vault
# password itself in `${SCRIPT_DIR}/.vault_pass` (already gitignored, same as
# inventory.yml) or point ANSIBLE_VAULT_PASSWORD_FILE at wherever you keep it --
# either way ansible-playbook decrypts transparently at run time, so nothing
# else in this script needs to know or care that a value is vault-encrypted.
VAULT_ARGS=()
if [ -n "${ANSIBLE_VAULT_PASSWORD_FILE:-}" ] && [ -f "${ANSIBLE_VAULT_PASSWORD_FILE}" ]; then
    VAULT_ARGS=(--vault-password-file "${ANSIBLE_VAULT_PASSWORD_FILE}")
elif [ -f "${SCRIPT_DIR}/.vault_pass" ]; then
    VAULT_ARGS=(--vault-password-file "${SCRIPT_DIR}/.vault_pass")
fi

# ==============================================================================
# 1. NEW CLI PARSER & LEGACY ADAPTER (PHASE 1)
# ==============================================================================
# ==============================================================================
# 1. LEGACY ARGUMENT ADAPTER (Rewriting / Translation Layer)
# ==============================================================================
if [[ $# -gt 0 ]] && [[ "$1" == --* && "$1" != "--help" && "$1" != "-h" ]]; then
    echo -e "\033[0;33m⚠️  [LEGACY ADAPTER] Đang sử dụng cú pháp cờ cũ. Khuyến nghị chuyển sang cú pháp subcommand mới.\033[0m"

    LEGACY_CMD=""
    HAS_TARGET="false"
    NEW_ARGS=()
    ACTION_COUNT=0
    STANDALONE_OPEN_PORTS="false"

    while [[ "$#" -gt 0 ]]; do
        case $1 in
            --start)       LEGACY_CMD="deploy"; ACTION_COUNT=$((ACTION_COUNT+1)) ;;
            --stop)        LEGACY_CMD="stop"; ACTION_COUNT=$((ACTION_COUNT+1)) ;;
            --restart)     LEGACY_CMD="restart"; ACTION_COUNT=$((ACTION_COUNT+1)) ;;
            --reset-all)   LEGACY_CMD="reset-all"; NEW_ARGS+=(--yes-reset-all --overwrite); ACTION_COUNT=$((ACTION_COUNT+1)) ;;
            --gen-keys)    LEGACY_CMD="gen-keys"; ACTION_COUNT=$((ACTION_COUNT+1)) ;;
            --open-ports)  STANDALONE_OPEN_PORTS="true" ;;
            --restore-node)
                LEGACY_CMD="restore"; ACTION_COUNT=$((ACTION_COUNT+1))
                if [[ $# -lt 2 || "$2" =~ ^-- ]]; then echo -e "\033[0;31m❌ Thiếu node cho restore\033[0m" >&2; exit 1; fi
                NEW_ARGS+=(--node "$2"); HAS_TARGET="true"; shift
                ;;
            --only-node)
                if [[ $# -lt 2 || "$2" =~ ^-- ]]; then echo -e "\033[0;31m❌ Thiếu node cho --only-node\033[0m" >&2; exit 1; fi
                NEW_ARGS+=(--node "$2"); HAS_TARGET="true"; shift
                ;;
            --snapshot-url)
                if [[ $# -lt 2 || "$2" =~ ^-- ]]; then echo -e "\033[0;31m❌ Thiếu URL cho --snapshot-url\033[0m" >&2; exit 1; fi
                NEW_ARGS+=(--snapshot-url "$2"); shift
                ;;
            --btrfs-size)
                if [[ $# -lt 2 || "$2" =~ ^-- ]]; then echo -e "\033[0;31m❌ Thiếu kích thước cho --btrfs-size\033[0m" >&2; exit 1; fi
                NEW_ARGS+=(--btrfs-size "$2"); shift
                ;;
            --bin-dir|--prebuilt-bin|--use-prebuilt)
                if [[ $# -gt 1 && ! "$2" =~ ^-- ]]; then
                    NEW_ARGS+=(--bin-dir "$2"); shift
                else
                    NEW_ARGS+=(--bin-dir "${SCRIPT_DIR}/../bin")
                fi
                ;;
            --skip-build)  NEW_ARGS+=(--bin-dir "${SCRIPT_DIR}/../bin") ;;
            --fast)        NEW_ARGS+=(--fast) ;;
            --debug-cpp)   NEW_ARGS+=(--debug-cpp) ;;
            --overwrite)   NEW_ARGS+=(--overwrite) ;;
            --clean)
                echo -e "\033[0;31m❌ [LỖI] Flag --clean độc lập đã bị loại bỏ. Hãy dùng lệnh 'reset-data' hoặc 'reset-all' tùy mục đích.\033[0m" >&2
                exit 1
                ;;
            --all-monitors|--monitor-all)
                echo -e "\033[0;31m❌ [LỖI] Legacy flags --all-monitors không còn được hỗ trợ ngầm định. Vui lòng dùng lệnh 'monitors' riêng.\033[0m" >&2
                exit 1
                ;;
            *)
                echo -e "\033[0;31m❌ [LỖI] Cờ legacy không hợp lệ: $1\033[0m" >&2
                exit 1
                ;;
        esac
        shift
    done

    # Xử lý cờ --open-ports (nếu đi kèm action khác như --start thì thành option --open-ports; nếu đứng một mình thì thành command open-ports)
    if [[ "$STANDALONE_OPEN_PORTS" == "true" ]]; then
        if [[ -n "$LEGACY_CMD" ]]; then
            NEW_ARGS+=(--open-ports)
        else
            LEGACY_CMD="open-ports"
            ACTION_COUNT=$((ACTION_COUNT+1))
        fi
    fi

    if [[ $ACTION_COUNT -gt 1 ]]; then
        echo -e "\033[0;31m❌ [LỖI] Các tham số legacy xung đột nhau. Không thể kết hợp nhiều hành động chính.\033[0m" >&2
        exit 1
    fi
    if [[ -z "$LEGACY_CMD" ]]; then
        echo -e "\033[0;31m❌ [LỖI] Không có action chính nào được chỉ định.\033[0m" >&2
        exit 1
    fi

    # Ngầm định tác động toàn cụm (--all) cho các lệnh cluster nếu caller không chỉ định --only-node
    if [[ "$HAS_TARGET" == "false" ]] && [[ "$LEGACY_CMD" =~ ^(deploy|start|stop|restart|open-ports)$ ]]; then
        NEW_ARGS+=(--all)
    fi

    # Ghi đè lại mảng positional parameters (ARGV rewrite)
    set -- "$LEGACY_CMD" "${NEW_ARGS[@]}"
fi

# ==============================================================================
# 2. UNIFIED STRICT CLI PARSER & VALIDATOR (Single Source of Truth)
# ==============================================================================
COMMAND="${1:-}"
TARGET_NODE=""
ALL_NODES="false"
INVENTORY="${SCRIPT_DIR}/inventory.yml"
BIN_DIR=""
FAST="false"
DEBUG_CPP="false"
SNAPSHOT_URL=""
BTRFS_SIZE_VAL=""
WITH_FIREWALL="false"
OVERWRITE="false"
YES_RESET_ALL="false"

shift || true

case "$COMMAND" in
    deploy|start|stop|restart|restore|reset-data|reset-all|gen-keys|open-ports|monitors|build) ;;
    -h|--help|help|"") COMMAND="help" ;;
    *) echo -e "\033[0;31m❌ [LỖI] Command không hợp lệ: $COMMAND\033[0m"; exit 1 ;;
esac

while [[ "$#" -gt 0 ]]; do
    case $1 in
        --inventory)
            if [[ $# -lt 2 || "$2" =~ ^-- ]]; then echo -e "\033[0;31m❌ Thiếu đường dẫn cho --inventory\033[0m"; exit 1; fi
            INVENTORY="$2"; shift
            ;;
        --node)
            if [[ -n "$TARGET_NODE" ]]; then echo -e "\033[0;31m❌ [LỖI] Option --node bị lặp lại.\033[0m"; exit 1; fi
            if [[ $# -lt 2 || "$2" =~ ^-- ]]; then echo -e "\033[0;31m❌ Thiếu giá trị cho --node\033[0m"; exit 1; fi
            TARGET_NODE="$2"; shift
            ;;
        --all)
            if [[ "$ALL_NODES" == "true" ]]; then echo -e "\033[0;31m❌ [LỖI] Option --all bị lặp lại.\033[0m"; exit 1; fi
            ALL_NODES="true"
            ;;
        --bin-dir)
            if [[ $# -lt 2 || "$2" =~ ^-- ]]; then echo -e "\033[0;31m❌ Thiếu đường dẫn cho --bin-dir\033[0m"; exit 1; fi
            BIN_DIR="$2"; shift
            ;;
        --fast) FAST="true" ;;
        --debug-cpp) DEBUG_CPP="true" ;;
        --snapshot-url)
            if [[ $# -lt 2 || "$2" =~ ^-- ]]; then echo -e "\033[0;31m❌ Thiếu URL cho --snapshot-url\033[0m"; exit 1; fi
            SNAPSHOT_URL="$2"; shift
            ;;
        --btrfs-size)
            if [[ $# -lt 2 || "$2" =~ ^-- ]]; then echo -e "\033[0;31m❌ Thiếu giá trị cho --btrfs-size\033[0m"; exit 1; fi
            BTRFS_SIZE_VAL="$2"; shift
            ;;
        --open-ports) WITH_FIREWALL="true" ;;
        --overwrite) OVERWRITE="true" ;;
        --yes-reset-all) YES_RESET_ALL="true" ;;
        *) echo -e "\033[0;31m❌ [LỖI] Option không hợp lệ hoặc sai vị trí: $1\033[0m"; exit 1 ;;
    esac
    shift
done

if [[ "$COMMAND" == "help" ]]; then
    echo "Usage: $0 <command> [options]"
    echo ""
    echo "Commands:"
    echo "  deploy          Triển khai (build + distribute + start)"
    echo "  start           Khởi động service (chưa hỗ trợ độc lập)"
    echo "  restart         Khởi động lại service nhanh"
    echo "  stop            Dừng service"
    echo "  restore         Khôi phục dữ liệu từ snapshot"
    echo "  reset-data      Xóa dữ liệu node"
    echo "  reset-all       Xóa toàn cụm và tạo mới (nguy hiểm)"
    echo "  gen-keys        Tạo keys cục bộ"
    echo "  open-ports      Cấu hình tường lửa"
    echo "  build           Biên dịch mã nguồn (chưa hỗ trợ)"
    echo ""
    echo "Options:"
    echo "  --node N, --all      Chỉ định mục tiêu"
    echo "  --inventory PATH     Sử dụng inventory khác (mặc định: inventory.yml)"
    echo "  --fast               Biên dịch nhanh"
    echo "  --debug-cpp          Bật debug C++"
    echo "  --bin-dir DIR        Sử dụng prebuilt binaries"
    echo "  --open-ports         Mở cổng firewall"
    echo "  --yes-reset-all      Xác nhận phá hủy cụm"
    echo "  --snapshot-url URL   URL để khôi phục snapshot"
    echo "  --btrfs-size SIZE    Kích thước phân vùng BTRFS"
    exit 0
fi

# Validation "--node N" vs "--all"
if [[ "$COMMAND" =~ ^(deploy|start|stop|restart|open-ports|reset-data)$ ]]; then
    if [[ -n "$TARGET_NODE" && "$ALL_NODES" == "true" ]]; then
        echo -e "\033[0;31m❌ [LỖI] --node và --all loại trừ nhau.\033[0m"
        exit 1
    fi
    if [[ -z "$TARGET_NODE" && "$ALL_NODES" == "false" ]]; then
        echo -e "\033[0;31m❌ [LỖI] Lệnh $COMMAND yêu cầu phải có --node N hoặc --all.\033[0m"
        exit 1
    fi
fi

# Map to legacy Ansible Extra Vars behavior
ACTION=""
KEEP_DATA="true"
RESTORE_NODE="none"
OPEN_PORTS="false"
ALL_MONITORS="false" # Not used yet
BUILD_FAST="$FAST"
USE_PREBUILT="false"
PREBUILT_BIN_DIR="$BIN_DIR"

if [[ -n "$BIN_DIR" ]]; then
    USE_PREBUILT="true"
fi

case "$COMMAND" in
    build)
        ACTION="build"
        if [[ -n "$TARGET_NODE" || "$ALL_NODES" == "true" || -n "$BIN_DIR" || -n "$SNAPSHOT_URL" || -n "$BTRFS_SIZE_VAL" || "$WITH_FIREWALL" == "true" || "$YES_RESET_ALL" == "true" ]]; then
            echo -e "\033[0;31m❌ [LỖI] Lệnh build chỉ hỗ trợ các cờ biên dịch (--fast, --debug-cpp).\033[0m"
            exit 1
        fi
        ;;
    deploy)
        ACTION="deploy"
        KEEP_DATA="true"
        if [[ "$WITH_FIREWALL" == "true" ]]; then OPEN_PORTS="true"; fi
        ;;
    start)
        ACTION="start"
        if [[ "$USE_PREBUILT" == "true" || "$FAST" == "true" || "$DEBUG_CPP" == "true" ]]; then
            echo -e "\033[0;31m❌ [LỖI] Các cờ build (--bin-dir, --fast, --debug-cpp) không hợp lệ với service commands.\033[0m"
            exit 1
        fi
        ;;
    stop|restart|open-ports)
        if [[ "$COMMAND" == "stop" ]]; then ACTION="stop"
        elif [[ "$COMMAND" == "restart" ]]; then ACTION="restart"
        elif [[ "$COMMAND" == "open-ports" ]]; then ACTION="open_ports"
        fi
        if [[ "$USE_PREBUILT" == "true" || "$FAST" == "true" || "$DEBUG_CPP" == "true" ]]; then
            echo -e "\033[0;31m❌ [LỖI] Các cờ build (--bin-dir, --fast, --debug-cpp) không hợp lệ với service commands.\033[0m"
            exit 1
        fi
        ;;
    restore)
        if [[ -z "$TARGET_NODE" || "$ALL_NODES" == "true" ]]; then
            echo -e "\033[0;31m❌ [LỖI] Lệnh restore chỉ chấp nhận --node N.\033[0m"
            exit 1
        fi
        if [[ -z "$SNAPSHOT_URL" ]]; then
            echo -e "\033[0;31m❌ [LỖI] Lệnh restore bắt buộc phải có --snapshot-url URL.\033[0m"
            exit 1
        fi
        ACTION="deploy"
        KEEP_DATA="false"
        RESTORE_NODE="$TARGET_NODE"
        ;;
    reset-data)
        if [[ -z "$TARGET_NODE" || "$ALL_NODES" == "true" ]]; then
            echo -e "\033[0;31m❌ [LỖI] Lệnh reset-data chỉ chấp nhận --node N.\033[0m"
            exit 1
        fi
        ACTION="deploy"
        KEEP_DATA="false"
        ;;
    reset-all)
        if [[ -n "$TARGET_NODE" ]]; then
            echo -e "\033[0;31m❌ [LỖI] Lệnh reset-all không nhận selector --node.\033[0m"
            exit 1
        fi
        if [[ "$YES_RESET_ALL" == "false" ]]; then
            echo -e "\033[0;31m❌ [LỖI] Phải truyền cờ --yes-reset-all để xác nhận phá hủy toàn cụm.\033[0m"
            exit 1
        fi
        ACTION="setup"
        KEEP_DATA="false"
        OVERWRITE="true"
        TARGET_NODE="all"
        if [[ "$WITH_FIREWALL" == "true" ]]; then OPEN_PORTS="true"; fi
        ;;
    gen-keys)
        ACTION="gen_keys"
        if [[ "$OVERWRITE" != "true" && -f "${SCRIPT_DIR}/../systemd/genesis.json" ]]; then
            echo -e "\n\033[0;31m❌ [LỖI DỪNG THỰC THI] Keys và genesis.json đã tồn tại tại deploy/systemd/!\033[0m"
            echo -e "\033[0;33m   Để tránh vô tình ghi đè phá hủy keys của Validator, lệnh gen-keys mặc định không ghi đè.\033[0m"
            echo -e "\033[0;36m   👉 Nếu thực sự muốn tạo lại toàn bộ keys và genesis mới, hãy thêm cờ: --overwrite\033[0m\n"
            exit 1
        fi
        ;;
    monitors)
        echo -e "\033[0;31m❌ [LỖI] Lệnh monitors chưa được hỗ trợ trong wrapper.\033[0m"
        exit 1
        ;;
esac

if [[ -z "$TARGET_NODE" && "$ALL_NODES" == "true" ]]; then
    TARGET_NODE="all"
fi

DEPLOY_SOURCE="${DEPLOY_SOURCE:-"Manual (Local Machine)"}"
# BUG FIX (2026-09-10): must query the metanode repo (SCRIPT_DIR), not the caller's cwd.
# When invoked by an absolute path from a DIFFERENT repo (e.g. metanode-suite's
# run_restart_test.sh calling "${ANSIBLE_DIR}/ansible_deploy.sh" without cd-ing into
# metanode first), a bare `git rev-parse` here inherited the caller's cwd and reported
# THAT repo's branch/commit instead -- e.g. logged "Branch: master | Commit: 33e70d4"
# (metanode-suite's own state) during every node_chaos_restart rolling-restart step,
# even though the metanode repo actually deploying the binaries was correctly on dev.
# Purely a misleading log/Telegram label, never a wrong-branch deploy -- but confusing
# enough during a live incident investigation to fix outright with `git -C`.
if command -v git >/dev/null 2>&1 && git -C "$SCRIPT_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    GIT_BRANCH=$(git -C "$SCRIPT_DIR" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
    GIT_COMMIT=$(git -C "$SCRIPT_DIR" rev-parse --short HEAD 2>/dev/null || echo "unknown")
    if [[ "$GIT_BRANCH" != "unknown" ]] || [[ "$GIT_COMMIT" != "unknown" ]]; then
        if [[ ! "$DEPLOY_SOURCE" =~ Branch: ]] && [[ ! "$DEPLOY_SOURCE" =~ $GIT_BRANCH ]]; then
            DEPLOY_SOURCE="$DEPLOY_SOURCE (Branch: $GIT_BRANCH | Commit: $GIT_COMMIT)"
        fi
    fi
fi

# Resolve Telegram configuration from YAML:
# If --all-monitors flag is active, read from monitors/inventory.yml.
# Otherwise read from standard inventory.yml.
if [ "$ALL_MONITORS" == "true" ]; then
    TG_CONFIG_YML="${SCRIPT_DIR}/monitors/inventory.yml"
else
    TG_CONFIG_YML="${SCRIPT_DIR}/inventory.yml"
fi
load_telegram_config "$TG_CONFIG_YML"

if [ -z "$TELEGRAM_BOT_TOKEN" ]; then
    echo -e "\033[0;33m⚠️ [CẢNH BÁO] Không tìm thấy telegram_bot_token trong ${TG_CONFIG_YML} (hoặc .env). Thông báo Telegram sẽ bị tắt.\033[0m\n"
fi

# Resolve prebuilt binary path if enabled
if [ "$USE_PREBUILT" == "true" ]; then
    if [ -z "$PREBUILT_BIN_DIR" ]; then
        CANDIDATES=(
            "${SCRIPT_DIR}/../bin"
            "${SCRIPT_DIR}/../private_chain_kit/bin"
            "${SCRIPT_DIR}/../../metanode-deploy/bin"
            "${SCRIPT_DIR}/../../../metanode-suite/private-chain-v1/private_chain_kit/bin"
        )
        for cand in "${CANDIDATES[@]}"; do
            if [ -f "$cand/metanode" ] && [ -f "$cand/simple_chain" ]; then
                PREBUILT_BIN_DIR="$cand"
                break
            fi
        done
        if [ -z "$PREBUILT_BIN_DIR" ]; then
            PREBUILT_BIN_DIR="${SCRIPT_DIR}/../bin"
        fi
    fi

    if [ -d "$PREBUILT_BIN_DIR" ]; then
        PREBUILT_BIN_DIR="$(cd "$PREBUILT_BIN_DIR" && pwd)"
    fi

    MISSING_BINS=()
    if [ ! -f "${PREBUILT_BIN_DIR}/metanode" ]; then
        MISSING_BINS+=("metanode")
    fi
    if [ ! -f "${PREBUILT_BIN_DIR}/simple_chain" ]; then
        MISSING_BINS+=("simple_chain")
    fi

    if [ ${#MISSING_BINS[@]} -gt 0 ]; then
        echo -e "\n\033[0;31m❌ [LỖI PREBUILT BINARY] Không tìm thấy file nhị phân (${MISSING_BINS[*]}) tại:\033[0m"
        echo -e "   \033[0;33m${PREBUILT_BIN_DIR}\033[0m"
        echo -e "\033[0;36m   👉 Hãy chạy script build trước để tạo các file nhị phân:\033[0m"
        echo -e "      \033[1;32m./deploy/build_private_chain_bins.sh\033[0m"
        echo -e "\033[0;36m   👉 Hoặc chỉ định đường dẫn chứa file binary đã có sẵn:\033[0m"
        echo -e "      \033[1;32m./ansible_deploy.sh --prebuilt-bin /duong/dan/chua/bin\033[0m\n"
        exit 1
    fi

    chmod +x "${PREBUILT_BIN_DIR}/metanode" "${PREBUILT_BIN_DIR}/simple_chain" 2>/dev/null || true
    for tool in cross_chain_relayer register_chains bls_pubkey; do
        if [ -f "${PREBUILT_BIN_DIR}/${tool}" ]; then
            chmod +x "${PREBUILT_BIN_DIR}/${tool}" 2>/dev/null || true
        fi
    done
fi

# ==============================================================================
# 1.5. NEW BUILD WORKFLOW & ARTIFACT CONTRACT (PHASE 2)
# ==============================================================================

if [[ "$COMMAND" == "build" ]]; then
    echo -e "\n🔨 [BUILD] Bắt đầu quá trình biên dịch (Build-Only)..."
    BUILD_ARGS=("--build-only")
    if [[ "$FAST" == "true" ]]; then BUILD_ARGS+=("--fast"); fi
    if [[ "$DEBUG_CPP" == "true" ]]; then BUILD_ARGS+=("--debug-cpp"); fi

    if ! bash "${SCRIPT_DIR}/../systemd/build_release.sh" "${BUILD_ARGS[@]}"; then
        echo -e "\033[0;31m❌ [LỖI] Quá trình biên dịch thất bại.\033[0m"
        exit 1
    fi
    echo -e "\n✅ [BUILD] Biên dịch thành công. Các tệp nhị phân đã được đưa vào deploy/bin."
    exit 0
fi

if [[ "$ACTION" == "setup" || "$ACTION" == "deploy" || "$ACTION" == "gen_keys" ]]; then
    if [[ "$USE_PREBUILT" == "false" ]]; then
        echo -e "\n🔨 [BUILD] Biên dịch mã nguồn trước khi deploy (Build-Only)..."
        BUILD_ARGS=("--build-only")
        if [[ "$FAST" == "true" ]]; then BUILD_ARGS+=("--fast"); fi
        if [[ "$DEBUG_CPP" == "true" ]]; then BUILD_ARGS+=("--debug-cpp"); fi
        if [[ "$ACTION" == "gen_keys" ]]; then BUILD_ARGS+=("--rust-only"); fi

        if ! bash "${SCRIPT_DIR}/../systemd/build_release.sh" "${BUILD_ARGS[@]}"; then
            echo -e "\033[0;31m❌ [LỖI DỪNG THỰC THI] Quá trình biên dịch thất bại!\033[0m"
            exit 1
        fi

        # Bắt buộc chuyển sang chế độ prebuilt cho luồng Ansible
        USE_PREBUILT="true"
        PREBUILT_BIN_DIR="${SCRIPT_DIR}/../bin"
        echo -e "✅ [BUILD] Biên dịch thành công. Chuyển Ansible sang dùng prebuilt tại: $PREBUILT_BIN_DIR"
    fi
fi

# Detect Deployer Server IP dynamically
DEPLOY_IP=$(hostname -I | tr ' ' '\n' | grep -E '^(192\.168\.|10\.|172\.)' | head -n 1)
if [ -z "$DEPLOY_IP" ]; then
    DEPLOY_IP=$(hostname -I | awk '{print $1}')
fi

# Check if Git Auto-Deploy Watcher daemon is running
if pgrep -f "auto_rebuild_deploy.sh" >/dev/null 2>&1; then
    WATCHER_STATUS="Đang hoạt động (Active) 🟢"
else
    WATCHER_STATUS="Đã tắt (Inactive) 🔴"
fi

# Resolve Target Node IPs dynamically from inventory.yml
TARGET_NODES_IPS=""
if [ -f "${SCRIPT_DIR}/parse_inventory.py" ]; then
    if ! TARGET_NODES_IPS=$(python3 "${SCRIPT_DIR}/parse_inventory.py" "$INVENTORY" "$TARGET_NODE"); then
        echo -e "\n\033[0;31m❌ [LỖI DỪNG THỰC THI] Cấu hình ${INVENTORY} không hợp lệ! Vui lòng sửa cấu hình theo thông báo trên trước khi tiếp tục.\033[0m\n"
        exit 1
    fi
    rm -f "/tmp/rpc_nodes.json" 2>/dev/null || true
    if ! (umask 077 && python3 "${SCRIPT_DIR}/parse_inventory.py" "$INVENTORY" json > "/tmp/rpc_nodes.json"); then
        echo -e "\n\033[0;31m❌ [LỖI DỪNG THỰC THI] Không thể xuất thông tin RPC từ ${INVENTORY}!\033[0m\n"
        exit 1
    fi
    chmod 0600 "/tmp/rpc_nodes.json" 2>/dev/null || true
fi

# Safety Check: Node tạo snapshot KHÔNG ĐƯỢC PHÉP tự khôi phục chính nó
#
# Logic phân loại node snapshot/synconly (phải khớp CHÍNH XÁC cách deploy.yml tự phân loại,
# và fail-closed nếu không xác minh được) sống trong check_snapshot_node.py -- tách ra file
# riêng thay vì Python inline trong heredoc để có thể unit-test độc lập, xem
# test_check_snapshot_node.py. Guard an toàn dữ liệu không được phép fail-open.
if [ "$RESTORE_NODE" != "none" ] && [ -f "${INVENTORY}" ]; then
    SNAP_CHECK_OUTPUT=$(python3 "${SCRIPT_DIR}/check_snapshot_node.py" "${INVENTORY}" "${RESTORE_NODE}" 2>&1)
    SNAP_CHECK_RC=$?
    if [ $SNAP_CHECK_RC -ne 0 ]; then
        echo -e "\n\033[0;31m❌ [LỖI AN TOÀN] Không thể xác minh Node ${RESTORE_NODE} có phải Node tạo Snapshot hay không!\033[0m"
        echo -e "\033[0;33m   ${SNAP_CHECK_OUTPUT}\033[0m"
        echo -e "\033[0;36m   👉 Kiểm tra lại ${INVENTORY} và đảm bảo đã cài PyYAML (pip install pyyaml), rồi chạy lại.\033[0m\n"
        exit 1
    fi
    IS_SNAP_NODE="$SNAP_CHECK_OUTPUT"
    if [ "$IS_SNAP_NODE" == "true" ]; then
        echo -e "\n\033[0;31m❌ [LỖI AN TOÀN] Node ${RESTORE_NODE} là Node tạo Snapshot (SyncOnly)!\033[0m"
        echo -e "\033[0;33m   ⚠️ Node tạo snapshot KHÔNG ĐƯỢC PHÉP tự khôi phục chính nó.\033[0m"
        echo -e "\033[0;36m   👉 Chỉ được phép khôi phục dữ liệu snapshot trên các Node Validator (vd: 0, 1, 2, 3).\033[0m\n"
        exit 1
    fi
fi

ACTION_LABEL=$(echo "$ACTION" | tr '[:lower:]' '[:upper:]')

echo -e "\n🚀 Starting Ansible ${ACTION_LABEL} with:"
echo "   Command:            $COMMAND"
echo "   Deployer Server IP: $DEPLOY_IP"
echo "   Target Node IPs:    $TARGET_NODES_IPS"
echo "   Source:             $DEPLOY_SOURCE"
echo "   Legacy Action:      $ACTION"
echo "   Target Node:        $TARGET_NODE"
echo "   Keep Data:          $KEEP_DATA"
echo "   Restore Node:       $RESTORE_NODE"
echo "   BTRFS Size:         ${BTRFS_SIZE_VAL:-"(từ inventory.yml)"}"
echo "   Open Ports:         $OPEN_PORTS"
echo "   Build Fast:         $BUILD_FAST"
echo "   Prebuilt Bin:       ${USE_PREBUILT}${PREBUILT_BIN_DIR:+ (Dir: $PREBUILT_BIN_DIR)}"
echo "   Watcher:            $WATCHER_STATUS"

ROLES_OUTPUT=""
if [ -f "${SCRIPT_DIR}/parse_inventory.py" ]; then
    if ! ROLES_OUTPUT=$(python3 "${SCRIPT_DIR}/parse_inventory.py" "$INVENTORY" "roles"); then
        echo -e "\n\033[0;31m❌ [LỖI DỪNG THỰC THI] Không thể đọc vai trò các node từ ${INVENTORY}!\033[0m\n"
        exit 1
    fi
    echo -e "\n📋 Node Roles:"
    echo "$ROLES_OUTPUT"
fi

send_telegram_notification "🚀 <b>[${ACTION_LABEL}]</b> Bắt đầu quá trình Ansible ${ACTION_LABEL}:
- Deployer Server IP: <code>${DEPLOY_IP}</code>
- Target Node IPs: <code>${TARGET_NODES_IPS}</code>
- Source: <code>${DEPLOY_SOURCE}</code>
- Action: <code>${ACTION}</code>
- Target Node: <code>${TARGET_NODE}</code>
- Keep Data: <code>${KEEP_DATA}</code>
- Restore Node: <code>${RESTORE_NODE}</code>
- Prebuilt Bin: <code>${USE_PREBUILT}${PREBUILT_BIN_DIR:+ (Dir: ${PREBUILT_BIN_DIR})}</code>
- BTRFS Size: <code>${BTRFS_SIZE_VAL:-"default"}</code>
- Open Ports: <code>${OPEN_PORTS}</code>
- All Monitors: <code>${ALL_MONITORS}</code>
- Watcher Daemon: <code>${WATCHER_STATUS}</code>

📋 <b>Node Roles:</b>
<pre>
${ROLES_OUTPUT}
</pre>"

# Prepare extra vars
EXTRA_VARS="ansible_action=${ACTION} target_node=${TARGET_NODE} keep_data=${KEEP_DATA} restore_node=${RESTORE_NODE} open_ports=${OPEN_PORTS} ansible_build_fast=${BUILD_FAST} ansible_debug_cpp=${DEBUG_CPP} ansible_use_prebuilt=${USE_PREBUILT} ansible_prebuilt_bin_dir='${PREBUILT_BIN_DIR}' ansible_overwrite=${OVERWRITE}"
if [ -n "$SNAPSHOT_URL" ]; then
    EXTRA_VARS="${EXTRA_VARS} snapshot_url='${SNAPSHOT_URL}'"
fi
if [ -n "$BTRFS_SIZE_VAL" ]; then
    EXTRA_VARS="${EXTRA_VARS} btrfs_size='${BTRFS_SIZE_VAL}'"
fi

# Detect become password from inventory for localhost become tasks.
# SECURITY: pass it via the ANSIBLE_BECOME_PASS env var, NOT `-e ansible_become_pass=...`
# on the ansible-playbook command line -- `-e` extra-vars are visible in plaintext to any
# local user via `ps aux`/`/proc/<pid>/cmdline` for the whole run. The env var achieves the
# same effect (ansible-playbook reads it automatically) without that exposure. Exported here
# so it's in scope for every ansible-playbook invocation below (gen_keys included).
INVENTORY_BECOME_PASS=$(grep -E '^\s*ansible_become_pass:' "$INVENTORY" 2>/dev/null | head -n 1 | awk '{print $2}' | sed 's/["\x27]//g' || true)
if [ -n "$INVENTORY_BECOME_PASS" ]; then
    export ANSIBLE_BECOME_PASS="$INVENTORY_BECOME_PASS"
fi

# Fast Pre-flight Check: Kiểm tra khả năng kết nối mạng tới các server đích trước khi build/deploy
if [ "$ACTION" != "gen_keys" ] && [ -f "${SCRIPT_DIR}/parse_inventory.py" ]; then
    echo -e "\n🔍 [PRE-FLIGHT] Đang kiểm tra kết nối mạng (SSH port) tới các máy chủ đích..."
    if ! python3 "${SCRIPT_DIR}/parse_inventory.py" "$INVENTORY" "check_reachability" "$TARGET_NODE"; then
        echo -e "\n\033[0;31m❌ [LỖI KẾT NỐI SERVER] Máy chủ đích bị timeout hoặc không thể kết nối qua SSH!\033[0m"
        echo -e "\033[0;33m   🛑 Dừng thực thi ngay lập tức để không tốn thời gian biên dịch hay triển khai dở dang.\033[0m\n"
        send_telegram_notification "❌ <b>[${ACTION_LABEL}]</b> Thất bại ngay bước kiểm tra kết nối: Máy chủ đích không phản hồi (Connection timed out / SSH unreachable)!
- Target Node IPs: <code>${TARGET_NODES_IPS}</code>"
        exit 4
    fi
    echo -e "✅ Kết nối tới các máy chủ đích: OK"
fi

if [ "$ACTION" == "gen_keys" ]; then
    echo -e "\n🔑 [GEN-KEYS] Bắt đầu sinh bộ Key & Genesis mẫu cục bộ (Không đụng tới server)..."
    cd "$SCRIPT_DIR"
    exec 200> "${SCRIPT_DIR}/../.bin.lock"
    flock -s 200
    ANSIBLE_PLAYBOOK_CMD=(ansible-playbook -i "$INVENTORY" "$PLAYBOOK" -e "$EXTRA_VARS" "${VAULT_ARGS[@]}" --tags gen_keys)
    "${ANSIBLE_PLAYBOOK_CMD[@]}"
    exit_code=$?
    flock -u 200
    if [ $exit_code -eq 0 ]; then
        echo -e "\n=========================================================="
        echo -e "✅ ĐÃ TẠO XONG KEYS & GENESIS MẪU CỤC BỘ!"
        echo -e "=========================================================="
        echo -e "📁 Vị trí lưu trữ:"
        echo -e "   • Thư mục keys từng node: deploy/systemd/node-X_keys/"
        echo -e "   • File Genesis chung:      deploy/systemd/genesis.json"
        echo -e "\n✏️ BƯỚC TIẾP THEO (NẾU MUỐN SỬA):"
        echo -e "   1. Vào deploy/systemd/node-X_keys thay file key của bạn."
        echo -e "   2. Mở deploy/systemd/genesis.json chỉnh chainId, ví nhận tiền (alloc)..."
        echo -e "\n🚀 KHI ĐÃ SẴN SÀNG KHỞI ĐỘNG CHUỖI TỪ BLOCK 0 VỚI BỘ KEY NÀY:"
        echo -e "   ./ansible_deploy.sh deploy --all"
        echo -e "   (Thêm cờ --open-ports nếu muốn cấu hình tường lửa)"
        echo -e "   (⚠️ Không dùng reset-all để tránh bị tạo đè lại key)"
        echo -e "==========================================================\n"
    fi
    exit $exit_code
fi

if [ "$ACTION" != "open_ports" ]; then
    echo -e "\n⏸ Tạm dừng Health Monitor trên toàn bộ cụm trong quá trình Deploy để tránh cảnh báo sai..."
    if [ -f "${SCRIPT_DIR}/monitors/start_monitors.sh" ]; then
        bash "${SCRIPT_DIR}/monitors/start_monitors.sh" --stop-all >/dev/null 2>&1 || true
    fi
    pkill -9 -f "start_monitors.sh" || true
    pkill -9 -f "block_hash_checker" || true
    pkill -9 -f "vote_monitor" || true
    pkill -9 -f "go run main.go.*--no-stop-flag" || true

    if [ "$KEEP_DATA" == "false" ]; then
        echo -e "🧹 Dọn dẹp cache và log cũ của Monitors do dữ liệu Node bị xoá..."
        rm -f "${SCRIPT_DIR}/monitors/block_hash_checker/ghost_blocks.log"
        rm -f "${SCRIPT_DIR}/monitors/block_hash_checker/block_checker_daemon.log"
        rm -f "${SCRIPT_DIR}/monitors/block_hash_checker/chain_anomalies.log"
        rm -f "${SCRIPT_DIR}/monitors/block_hash_checker/"*.csv
        rm -f "${SCRIPT_DIR}/monitors/vote_monitor/vote_monitor.log"
        rm -f "${SCRIPT_DIR}/monitors/vote_monitor/vote_monitor_daemon.log"
    fi
fi

cd "$SCRIPT_DIR"
set +e
export PYTHONUNBUFFERED=1

exec 200> "${SCRIPT_DIR}/../.bin.lock"
flock -s 200

ANSIBLE_PLAYBOOK_CMD=(ansible-playbook -i "$INVENTORY" "$PLAYBOOK" -e "$EXTRA_VARS" "${VAULT_ARGS[@]}")
"${ANSIBLE_PLAYBOOK_CMD[@]}"
ansible_exit=$?

flock -u 200
set -e

if [ $ansible_exit -eq 0 ]; then
    # Update last deployed commit file if it's a git repo
    if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
        git rev-parse HEAD > "${SCRIPT_DIR}/.last_deployed_commit" 2>/dev/null || true
    fi

    # Read and format Node RPC IPs, WebSocket URLs and TCP Nodes from /tmp/rpc_nodes.json
    RPC_NODES_LIST=""
    WS_NODES_LIST=""
    TCP_NODES_LIST=""
    if [ -f "/tmp/rpc_nodes.json" ]; then
        RPC_NODES_LIST=$(jq -r '.nodes | to_entries[] | "  • \(.key): \(.value)"' /tmp/rpc_nodes.json 2>/dev/null || true)
        WS_NODES_LIST=$(jq -r '.ws_nodes // {} | to_entries[] | "  • \(.key): \(.value)"' /tmp/rpc_nodes.json 2>/dev/null || true)
        TCP_NODES_LIST=$(jq -r '.tcp_nodes | to_entries[] | "  • \(.key): \(.value)"' /tmp/rpc_nodes.json 2>/dev/null || true)
    fi

    echo -e "\n⚙️ Danh sách Node RPC (IP & Port):"
    echo "$RPC_NODES_LIST"

    if [ -n "$WS_NODES_LIST" ]; then
        echo -e "\n🔌 Danh sách Node WebSocket (WS URL):"
        echo "$WS_NODES_LIST"
    fi

    echo -e "\n🌐 Danh sách Node TCP (Consensus P2P):"
    echo "$TCP_NODES_LIST"

    # Tự động đồng bộ cấu hình sang metanode-suite (update-ip.sh)
    UPDATE_IP_SCRIPT="${SCRIPT_DIR}/../../../metanode-suite/scripts/update-ip/update-ip.sh"
    if [ -f "$UPDATE_IP_SCRIPT" ]; then
        echo -e "\n🔄 Đang đồng bộ cấu hình sang metanode-suite (update-ip.sh)..."
        bash "$UPDATE_IP_SCRIPT" >/dev/null 2>&1 || true
        echo "✅ Đã tự động cập nhật cấu hình test-chain & configs (bao gồm WebSocket) trong metanode-suite!"
    fi

    echo -e  "\n📋 *Node Roles:*"
    echo "${ROLES_OUTPUT}"
    send_telegram_notification "✅ <b>[${ACTION_LABEL}]</b> Quá trình Ansible ${ACTION_LABEL} từ <code>${DEPLOY_SOURCE}</code> hoàn tất thành công!
- Target Node IPs: <code>${TARGET_NODES_IPS}</code>
- Watcher Daemon: <code>${WATCHER_STATUS}</code>

📋 <b>Node Roles:</b>
<pre>
${ROLES_OUTPUT}
</pre>

⚙️ <b>Danh sách Node RPC:</b>
<pre>
${RPC_NODES_LIST}
</pre>

🔌 <b>Danh sách Node WebSocket:</b>
<pre>
${WS_NODES_LIST}
</pre>

🌐 <b>Danh sách Node TCP (Consensus P2P):</b>
<pre>
${TCP_NODES_LIST}
</pre>

💡 <b>Xem log nhanh:</b> <code>./fetch_node_logs.sh</code> (thêm <code>--rpc</code> nếu cần log RPC; xem DEPLOY_GUIDE.md)"
else
    ERROR_DESC="Lỗi không xác định"
    case $ansible_exit in
        1) ERROR_DESC="Lỗi chung (General error) - Playbook thất bại hoặc thiếu thư viện" ;;
        2) ERROR_DESC="Lỗi thực thi Ansible hoặc máy chủ không phản hồi (Unreachable / Failed host / Syntax error)" ;;
        3) ERROR_DESC="Lỗi Ansible Inventory - Host không hợp lệ hoặc thiếu quyền" ;;
        4) ERROR_DESC="Lỗi kết nối SSH (Unreachable hosts) - Máy chủ từ chối kết nối hoặc timeout" ;;
        13|141) ERROR_DESC="Bị ngắt kết nối (Broken pipe / SIGPIPE) - Script thoát đột ngột" ;;
        99) ERROR_DESC="Lỗi kịch bản Deploy (Thường do thiếu TTY / chưa xác nhận Y/N)" ;;
        127) ERROR_DESC="Không tìm thấy lệnh (Command not found) - Thiếu Ansible hoặc tiện ích" ;;
        130) ERROR_DESC="Bị người dùng hủy bỏ (Ctrl+C)" ;;
    esac

    send_telegram_notification "❌ <b>[${ACTION_LABEL}]</b> Quá trình Ansible ${ACTION_LABEL} từ <code>${DEPLOY_SOURCE}</code> thất bại với mã lỗi <code>${ansible_exit}</code>: <b>${ERROR_DESC}</b>!
- Target Node IPs: <code>${TARGET_NODES_IPS}</code>
- Watcher Daemon: <code>${WATCHER_STATUS}</code>

🔍 <b>Lệnh lấy log kiểm tra lỗi:</b>
• <b>Tại từng máy node (thay X bằng ID node, ví dụ 0, 1, 2, 3):</b>
  - <b>Consensus logs:</b>
    <code>sudo journalctl -u \"metanode-consensus-*\" -n 100 --no-pager</code>
  - <b>Execution logs:</b>
    <code>tail -n 100 /opt/metanode/node-X/logs/execution/*/execution.log</code>
• <b>Từ xa tại máy Master (chạy từ thư mục ansible):</b>
  - <b>Consensus logs:</b>
    <code>ansible all -i inventory.yml -m shell -a \"sudo journalctl -u 'metanode-consensus-*' -n 100 --no-pager\"</code>
  - <b>Execution logs:</b>
    <code>ansible all -i inventory.yml -m shell -a \"tail -n 100 /opt/metanode/node-*/logs/execution/*/execution.log\"</code>"
fi

MONITOR_SCRIPT="${SCRIPT_DIR}/monitors/start_monitors.sh"
if [ "$ACTION" != "open_ports" ]; then
    if [ -f "$MONITOR_SCRIPT" ] && [ "$ACTION" != "stop" ]; then
        if [ "$ALL_MONITORS" == "true" ]; then
            echo -e "\n▶️ Bật Giám Sát Chéo Đa Máy (Mutual Cross-Monitors) trên TẤT CẢ các máy..."
            bash "$MONITOR_SCRIPT" --all-hosts
        else
            echo -e "\n▶️ Bật lại Health Monitor cục bộ sau khi Deploy xong..."
            bash "$MONITOR_SCRIPT"
        fi
    elif [ "$ACTION" == "stop" ]; then
        echo -e "\n⏸ Không bật lại Health Monitor vì hệ thống đang ở trạng thái STOP..."
        pkill -f "vote_monitor" || true
        if [ "$ALL_MONITORS" == "true" ]; then
            ansible metanode_cluster -i "$INVENTORY" -m shell -a "pkill -f 'start_monitors.sh' || true; pkill -f 'block_hash_checker' || true; pkill -f 'vote_monitor' || true" >/dev/null 2>&1 || true
        fi
    fi
fi

exit $ansible_exit
