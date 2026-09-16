#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════
#  production_readiness_check.sh — Một lệnh duy nhất, chạy xong biết ngay
#  CÓ ĐƯỢC triển khai lên môi trường thật hay KHÔNG.
#
#  Gộp 3 tầng kiểm chứng độc lập, chạy tuần tự, dừng ngay ở tầng đầu tiên fail
#  (không tốn thời gian chạy tiếp các tầng sau nếu nền tảng đã hỏng):
#
#    TẦNG 1 — BUILD:      Go + Rust + FFI compile sạch, không warning.
#    TẦNG 2 — DEPLOY:     ansible_deploy.sh --reset-all chạy ổn định LẶP LẠI
#                         nhiều lần liên tiếp trên cụm nhiều node/host (regression
#                         cho các race condition chỉ lộ ra khi lặp lại, không phải
#                         lần chạy đầu tiên).
#    TẦNG 3 — ỨNG DỤNG:   Toàn bộ ma trận test nghiệp vụ (Block-STM, cross-chain,
#                         chaos restart, snapshot lifecycle + consensus-readiness +
#                         zero-fork, TPS, spam) qua CI pipeline hiện có.
#
#  Vì sao cần gộp thành 1 lệnh: cả 3 tầng này từng bị coi là "đã ổn" độc lập
#  trong khi thực ra có bug -- ví dụ tầng DEPLOY từng pass 1 lần rồi fail lại
#  ở lần lặp thứ 2 do race condition, và tầng ỨNG DỤNG (snapshot_recovery) bị
#  tắt (enabled: false) nên không ai biết nó fail cho tới khi chạy tay. Một
#  lệnh, chạy xong PASS toàn bộ 3 tầng mới nên là điều kiện đủ để bấm nút
#  triển khai thật.
#
#  Usage:
#    ./production_readiness_check.sh [--skip-build] [--reset-rounds N] [--snapshot-loops N] [--full]
#
#    --skip-build        Bỏ qua Tầng 1 (giả định đã build_check.sh sạch từ trước)
#    --reset-rounds N    Số vòng lặp --reset-all ở Tầng 2 (mặc định: 3)
#    --snapshot-loops N  Số vòng lặp riêng cho bài snapshot_recovery ở Tầng 3, ÉP BUỘC bất
#                         kể ci_config.yaml thật đang cấu hình bao nhiêu vòng (mặc định: 3).
#                         Xem giải thích ở comment "BOUNDED SNAPSHOT_RECOVERY" bên dưới --
#                         quan trọng để giữ tầng này NHANH và có thể dự đoán được.
#    --full              Chạy toàn bộ ci.sh (bao gồm cả TPS/spam benchmark, VÀ dùng đúng
#                         ci_config.yaml thật không ép loop -- có thể mất nhiều giờ nếu
#                         config thật đang tinh chỉnh cho soak-test dài hơi). Mặc định chỉ
#                         chạy các bài test CORRECTNESS: blockstm_logic, node_chaos_restart,
#                         snapshot_recovery (bounded).
# ═══════════════════════════════════════════════════════════════════════════
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
ANSIBLE_DIR="$REPO_ROOT/deploy/ansible"
BUILD_CHECK="$REPO_ROOT/consensus/metanode/scripts/build_check.sh"

SKIP_BUILD=false
RESET_ROUNDS=3
SNAPSHOT_LOOPS=3
FULL_SUITE=false

while [[ "$#" -gt 0 ]]; do
    case "$1" in
        --skip-build) SKIP_BUILD=true ;;
        --reset-rounds) RESET_ROUNDS="$2"; shift ;;
        --snapshot-loops) SNAPSHOT_LOOPS="$2"; shift ;;
        --full) FULL_SUITE=true ;;
        *) echo "Unknown flag: $1"; exit 1 ;;
    esac
    shift
done

# BOUNDED SNAPSHOT_RECOVERY (2026-09-09): `./ci.sh run-now --only snapshot_recovery` executes
# whatever `command:` this repo's OWN ci_config.yaml (gitignored, per-environment) has
# configured for that test -- found live that this environment's real config had been tuned to
# `--loop 100` for an extended overnight soak-test, so a naive `--only snapshot_recovery` call
# here took ~8 hours instead of the few minutes a pre-deploy gate needs. Generate a throwaway
# copy of the real config with ONLY that one test's command line overridden to a bounded loop
# count, so this gate's duration never silently depends on how someone last tuned the
# environment's own long-running soak-test config.
run_snapshot_recovery_bounded() {
    local loops="$1"
    local real_config="$REPO_ROOT/deploy/ci/ci_config.yaml"
    if [ ! -f "$real_config" ]; then
        real_config="$REPO_ROOT/deploy/ci/ci_config.yaml.example"
    fi
    local bounded_config
    bounded_config="$(mktemp /tmp/ci_config_readiness_XXXXXX.yaml)"
    if ! python3 -c "
import sys
import yaml

with open('${real_config}') as f:
    cfg = yaml.safe_load(f)

found = False
for t in cfg.get('tests', []):
    if t.get('id') == 'snapshot_recovery':
        t['command'] = './run_snapshot_test.sh --loop ${loops} --count 15'
        t['enabled'] = True
        found = True

if not found:
    sys.exit(1)

with open('${bounded_config}', 'w') as f:
    yaml.safe_dump(cfg, f, allow_unicode=True, sort_keys=False)
"; then
        echo "❌ Không tìm thấy bài test 'snapshot_recovery' trong ${real_config}"
        rm -f "${bounded_config}"
        return 1
    fi

    echo "ℹ️  Dùng cấu hình tạm (${bounded_config}) -- ép snapshot_recovery chạy ${loops} vòng"
    echo "   bất kể ci_config.yaml thật đang cấu hình bao nhiêu vòng."
    cd "$REPO_ROOT" && ./ci.sh run-now --only snapshot_recovery --config "${bounded_config}"
    local rc=$?
    rm -f "${bounded_config}"
    return $rc
}

REPORT_LOG="/tmp/production_readiness_$(date +%Y%m%d_%H%M%S).log"
START_TIME=$(date +%s)

STAGE_RESULTS=()   # each entry: "NAME<US>STATUS<US>DURATIONs", <US>=$'\x1f' (never appears
                    # in normal text, unlike ':' which stage names themselves contain --
                    # e.g. "Tầng 1: Build" -- a ':'-delimited split silently mis-parsed every
                    # stage as FAIL regardless of its real status, found live on the very first
                    # real run of this script)
US=$'\x1f'

log() { echo -e "$1" | tee -a "$REPORT_LOG"; }

record_stage() {
    local name="$1" status="$2" duration="$3"
    STAGE_RESULTS+=("${name}${US}${status}${US}${duration}s")
}

stage_banner() {
    log "\n═══════════════════════════════════════════════════════════"
    log "$1"
    log "═══════════════════════════════════════════════════════════"
}

fail_and_exit() {
    log "\n🛑 [KHÔNG ĐẠT] $1"
    log "   Log chi tiết: $REPORT_LOG"
    print_summary
    exit 1
}

print_summary() {
    local total_duration=$(( $(date +%s) - START_TIME ))
    log "\n═══════════════════════════════════════════════════════════"
    log "📊 TÓM TẮT PRODUCTION READINESS CHECK"
    log "═══════════════════════════════════════════════════════════"
    for r in "${STAGE_RESULTS[@]}"; do
        local name="${r%%$US*}"
        local rest="${r#*$US}"
        local status="${rest%%$US*}"
        local dur="${rest#*$US}"
        if [ "$status" == "PASS" ]; then
            log "   ✅ ${name} — PASS (${dur})"
        else
            log "   ❌ ${name} — FAIL (${dur})"
        fi
    done
    log "   Tổng thời gian: ${total_duration}s"
    log "   Log chi tiết: $REPORT_LOG"
    log "═══════════════════════════════════════════════════════════"
}

log "═══════════════════════════════════════════════════════════"
log "🚀 PRODUCTION READINESS CHECK — $(date '+%Y-%m-%d %H:%M:%S')"
log "   Reset rounds (Tầng 2): $RESET_ROUNDS"
log "   Bộ test ứng dụng (Tầng 3): $([ "$FULL_SUITE" == true ] && echo 'ĐẦY ĐỦ (bao gồm benchmark, dùng ci_config.yaml thật không ép loop)' || echo "CORRECTNESS (snapshot_recovery ép ${SNAPSHOT_LOOPS} vòng)")"
log "═══════════════════════════════════════════════════════════"

# ─── TẦNG 1: BUILD ────────────────────────────────────────────────────────
if [ "$SKIP_BUILD" == true ]; then
    log "\n⏭️  [TẦNG 1/3] BUILD — bỏ qua theo yêu cầu (--skip-build)"
else
    stage_banner "🔨 [TẦNG 1/3] BUILD — Go + Rust + FFI phải compile sạch"
    t0=$(date +%s)
    if bash "$BUILD_CHECK" --all 2>&1 | tee -a "$REPORT_LOG"; then
        record_stage "Tầng 1: Build" "PASS" "$(( $(date +%s) - t0 ))"
        log "✅ [TẦNG 1/3] Build sạch."
    else
        record_stage "Tầng 1: Build" "FAIL" "$(( $(date +%s) - t0 ))"
        fail_and_exit "Build không sạch -- không có ý nghĩa deploy hay chạy các tầng sau. Xem log build ở trên."
    fi
fi

# ─── TẦNG 2: DEPLOY STABILITY (lặp lại) ───────────────────────────────────
stage_banner "🔁 [TẦNG 2/3] DEPLOY STABILITY — ${RESET_ROUNDS} vòng --reset-all liên tiếp"
t0=$(date +%s)
if bash "$SCRIPT_DIR/verify_multi_reset_stability.sh" "$RESET_ROUNDS" 2>&1 | tee -a "$REPORT_LOG"; then
    record_stage "Tầng 2: Deploy Stability (${RESET_ROUNDS} vòng)" "PASS" "$(( $(date +%s) - t0 ))"
    log "✅ [TẦNG 2/3] Deploy ổn định qua ${RESET_ROUNDS} vòng lặp."
else
    record_stage "Tầng 2: Deploy Stability (${RESET_ROUNDS} vòng)" "FAIL" "$(( $(date +%s) - t0 ))"
    fail_and_exit "Deploy KHÔNG ổn định qua nhiều vòng lặp -- race condition/lỗi hạ tầng vẫn còn. Không nên triển khai thật cho tới khi tầng này pass."
fi

# ─── TẦNG 3: ỨNG DỤNG (CI pipeline) ───────────────────────────────────────
stage_banner "🧪 [TẦNG 3/3] ỨNG DỤNG — ma trận test nghiệp vụ qua CI pipeline"
t0=$(date +%s)
cd "$REPO_ROOT"
if [ "$FULL_SUITE" == true ]; then
    CI_CMD="./ci.sh run-now"
    CI_LABEL="toàn bộ pipeline (bao gồm TPS/spam benchmark)"
    if $CI_CMD 2>&1 | tee -a "$REPORT_LOG"; then
        record_stage "Tầng 3: Ứng dụng (${CI_LABEL})" "PASS" "$(( $(date +%s) - t0 ))"
    else
        record_stage "Tầng 3: Ứng dụng (${CI_LABEL})" "FAIL" "$(( $(date +%s) - t0 ))"
        fail_and_exit "Ít nhất 1 bài test ứng dụng FAIL. Xem log CI ở trên để biết bài nào."
    fi
else
    # Chỉ chạy các bài CORRECTNESS (không phải benchmark hiệu năng), tuần tự từng bài để
    # biết rõ đúng bài nào fail thay vì gộp chung 1 lần chạy toàn pipeline.
    CORRECTNESS_TESTS=("blockstm_logic" "node_chaos_restart" "snapshot_recovery")
    ANY_FAIL=false
    for test_id in "${CORRECTNESS_TESTS[@]}"; do
        log "\n▶️  Đang chạy bài test: ${test_id}"
        t_sub=$(date +%s)
        if [ "$test_id" == "snapshot_recovery" ]; then
            # Bounded, không phụ thuộc ci_config.yaml thật -- xem run_snapshot_recovery_bounded ở trên.
            if run_snapshot_recovery_bounded "$SNAPSHOT_LOOPS" 2>&1 | tee -a "$REPORT_LOG"; then
                record_stage "  Tầng 3: ${test_id} (${SNAPSHOT_LOOPS} vòng, ép riêng)" "PASS" "$(( $(date +%s) - t_sub ))"
            else
                record_stage "  Tầng 3: ${test_id} (${SNAPSHOT_LOOPS} vòng, ép riêng)" "FAIL" "$(( $(date +%s) - t_sub ))"
                ANY_FAIL=true
            fi
        else
            if ./ci.sh run-now --only "$test_id" 2>&1 | tee -a "$REPORT_LOG"; then
                record_stage "  Tầng 3: ${test_id}" "PASS" "$(( $(date +%s) - t_sub ))"
            else
                record_stage "  Tầng 3: ${test_id}" "FAIL" "$(( $(date +%s) - t_sub ))"
                ANY_FAIL=true
            fi
        fi
    done
    if [ "$ANY_FAIL" == true ]; then
        record_stage "Tầng 3: Ứng dụng (correctness suite)" "FAIL" "$(( $(date +%s) - t0 ))"
        fail_and_exit "Ít nhất 1 bài test correctness FAIL (xem chi tiết từng bài ở trên)."
    fi
    record_stage "Tầng 3: Ứng dụng (correctness suite)" "PASS" "$(( $(date +%s) - t0 ))"
fi
log "✅ [TẦNG 3/3] Toàn bộ test ứng dụng PASS."

# ─── KẾT QUẢ CUỐI ──────────────────────────────────────────────────────────
print_summary
log "\n🎉 GO — Đủ điều kiện triển khai lên môi trường thật."
exit 0
