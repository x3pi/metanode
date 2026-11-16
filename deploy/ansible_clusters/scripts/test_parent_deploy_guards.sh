#!/usr/bin/env bash
# Guards for destructive deploy actions: there is no --force flag any more; deleting data, overwriting keys and
# production rollback must show a red warning and need an interactive "y". These tests only exercise the refusal
# paths (no terminal, answer n, removed flags), so they never generate keys or touch chain data.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="${SCRIPT_DIR}/../deploy_clusters.sh"
INVENTORY="${SCRIPT_DIR}/../inventory.example.yml"

echo "🧪 Running Parent Chain Deploy Tooling Guard Tests..."

TEMP_TEST_DIR=$(mktemp -d /tmp/test_parent_guards_XXXXXX)
trap 'rm -rf "${TEMP_TEST_DIR}"' EXIT

KEYS_DIR="${TEMP_TEST_DIR}/parent_keys"
mkdir -p "${KEYS_DIR}"

# Create mock existing keys
echo '{"private_key_hex":"01","public_key_hex":"02"}' > "${KEYS_DIR}/node_0_protocol_key.json"
echo '{"private_key_hex":"03","public_key_hex":"04"}' > "${KEYS_DIR}/node_0_network_key.json"
KEYS_SUM_BEFORE=$(cat "${KEYS_DIR}"/* | sha256sum)

fail() {
    echo "❌ FAILED: $*"
    exit 1
}

# run_with_answer ANSWER CMD...: runs CMD on a pseudo-terminal and types ANSWER at the prompt.
run_with_answer() {
    local answer="$1"
    shift
    printf '%s\n' "$answer" | script -qec "$(printf '%q ' "$@")" /dev/null
}

keys_unchanged() {
    [ "$(cat "${KEYS_DIR}"/* | sha256sum)" = "${KEYS_SUM_BEFORE}" ] || fail "existing keys were modified"
    [ -z "$(find "${TEMP_TEST_DIR}" -maxdepth 1 -name 'parent_keys.previous_*')" ] || fail "a key backup was created"
}

echo "  Test 1: gen_parent_keys.py refuses --change-keys without a terminal"
set +e
setsid -w python3 "${SCRIPT_DIR}/gen_parent_keys.py" --keys-dir "${KEYS_DIR}" --change-keys \
    </dev/null >"${TEMP_TEST_DIR}/t1.log" 2>&1
RC=$?
set -e
[ $RC -eq 1 ] || fail "expected exit 1, got $RC"
keys_unchanged
echo "  ✅ PASS: non-interactive key change refused"

echo "  Test 2: gen_parent_keys.py --change-keys answered n keeps the keys"
set +e
OUT=$(run_with_answer n python3 "${SCRIPT_DIR}/gen_parent_keys.py" --keys-dir "${KEYS_DIR}" --change-keys 2>&1)
set -e
echo "$OUT" | grep -q "CẢNH BÁO" || fail "red warning not shown"
echo "$OUT" | grep -q "Đã hủy" || fail "cancel message not shown"
keys_unchanged
echo "  ✅ PASS: answer n cancelled the key change"

echo "  Test 3: gen_parent_keys.py rejects the removed --force flag"
set +e
python3 "${SCRIPT_DIR}/gen_parent_keys.py" --keys-dir "${KEYS_DIR}" --change-keys --force \
    </dev/null >"${TEMP_TEST_DIR}/t3.log" 2>&1
RC=$?
set -e
[ $RC -ne 0 ] || fail "--force should be rejected"
keys_unchanged
echo "  ✅ PASS: --force rejected"

echo "  Test 4: deploy_clusters.sh refuses --reset without a terminal"
set +e
setsid -w "${DEPLOY}" --inventory="${INVENTORY}" --parent-only --reset \
    </dev/null >"${TEMP_TEST_DIR}/t4.log" 2>&1
RC=$?
set -e
[ $RC -eq 1 ] || fail "expected exit 1, got $RC"
echo "  ✅ PASS: non-interactive --reset refused"

echo "  Test 5: deploy_clusters.sh --reset answered n is cancelled"
set +e
OUT=$(run_with_answer n "${DEPLOY}" --inventory="${INVENTORY}" --parent-only --reset 2>&1)
set -e
echo "$OUT" | grep -q "BACKUP CŨ" || fail "red warning does not say the old backup is deleted"
echo "$OUT" | grep -q "Đã hủy" || fail "cancel message not shown"
echo "$OUT" | grep -q "Executing Ansible Playbook" && fail "playbook ran after answer n"
echo "  ✅ PASS: answer n cancelled --reset before any change"

echo "  Test 6: deploy_clusters.sh rejects the removed --force / -f / --wipe-data flags"
for flag in --force -f --wipe-data; do
    set +e
    "${DEPLOY}" --inventory="${INVENTORY}" --reset "$flag" </dev/null >"${TEMP_TEST_DIR}/t6.log" 2>&1
    RC=$?
    set -e
    [ $RC -eq 1 ] || fail "$flag: expected exit 1, got $RC"
    grep -q "đã bị bỏ" "${TEMP_TEST_DIR}/t6.log" || fail "$flag: missing removed-flag message"
done
echo "  ✅ PASS: removed flags rejected"

echo "🎉 All Parent Chain Deploy Tooling Guard Tests Passed Successfully!"
