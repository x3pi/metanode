#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

echo "🧪 Running Parent Chain Deploy Tooling Guard Tests..."

TEMP_TEST_DIR=$(mktemp -d /tmp/test_parent_guards_XXXXXX)
trap 'rm -rf "${TEMP_TEST_DIR}"' EXIT

KEYS_DIR="${TEMP_TEST_DIR}/parent_keys"
mkdir -p "${KEYS_DIR}"

# Create mock existing keys
echo '{"private_key_hex":"01","public_key_hex":"02"}' > "${KEYS_DIR}/node_0_protocol_key.json"
echo '{"private_key_hex":"03","public_key_hex":"04"}' > "${KEYS_DIR}/node_0_network_key.json"

echo "  Test 1: gen_parent_keys.py refuses --change-keys without --force"
set +e
python3 "${SCRIPT_DIR}/gen_parent_keys.py" \
    --keys-dir "${KEYS_DIR}" \
    --change-keys 2>"${TEMP_TEST_DIR}/err.log"
RC=$?
set -e

if [ $RC -ne 1 ]; then
    echo "❌ FAILED: gen_parent_keys.py should have exited with code 1, got $RC"
    exit 1
fi
echo "  ✅ PASS: gen_parent_keys.py rejected unconfirmed key change"

echo "  Test 2: gen_parent_keys.py with --change-keys --force creates timestamped backup"
set +e
python3 "${SCRIPT_DIR}/gen_parent_keys.py" \
    --keys-dir "${KEYS_DIR}" \
    --change-keys \
    --force 2>&1 | tee "${TEMP_TEST_DIR}/backup.log"
set -e

BACKUP_DIRS=$(find "${TEMP_TEST_DIR}" -maxdepth 1 -name "parent_keys.previous_*" -type d)
if [ -z "${BACKUP_DIRS}" ]; then
    echo "❌ FAILED: Backup directory parent_keys.previous_* not created"
    exit 1
fi
echo "  ✅ PASS: Found backup directory: ${BACKUP_DIRS}"

echo "  Test 3: gen_parent_keys.py with shorthand -f succeeds"
set +e
python3 "${SCRIPT_DIR}/gen_parent_keys.py" \
    --keys-dir "${KEYS_DIR}" \
    --change-keys \
    -f 2>&1 | tee "${TEMP_TEST_DIR}/backup2.log"
set -e
echo "  ✅ PASS: Shorthand -f accepted"

echo "🎉 All Parent Chain Deploy Tooling Guard Tests Passed Successfully!"

