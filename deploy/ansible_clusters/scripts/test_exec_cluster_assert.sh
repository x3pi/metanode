#!/usr/bin/env bash
# Test suite for Ansible exec_cluster role syntax and Raft secret assertion.
# Validates:
# 1. ansible-playbook --syntax-check on deploy.yml with inventory.example.yml
# 2. Positive assert case where raft_secret_source correctly points to cluster bootstrap replica
# 3. Negative assert case where inventory MISSING raft_secret_source fails at assert step
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ANSIBLE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

echo "=== Step 1: Checking ansible-playbook syntax for deploy.yml ==="
ansible-playbook --syntax-check "${ANSIBLE_DIR}/deploy.yml" -i "${ANSIBLE_DIR}/inventory.example.yml"
echo "✅ Syntax check PASSED"

TMP_DIR=$(mktemp -d)
trap 'rm -rf "${TMP_DIR}"' EXIT

cat > "${TMP_DIR}/test_assert.yml" << 'EOF'
---
- name: Test Raft Secret Source Assertion
  hosts: exec_clusters
  connection: local
  gather_facts: false
  tasks:
    - name: Require an explicit bootstrap source for the shared Raft HMAC secret
      ansible.builtin.assert:
        that:
          - >-
            raft_secret_source is defined and
            raft_secret_source in groups['exec_clusters'] and
            (hostvars[raft_secret_source].raft_bootstrap | default(false) | bool) and
            ((hostvars[raft_secret_source].cluster_id | string) == (cluster_id | string))
        fail_msg: >-
          {{ inventory_hostname }}: raft_secret_source must name the bootstrap replica in this cluster.
          Refusing to generate a per-replica secret because that would break Raft HMAC authentication.
EOF

echo "=== Step 2: Testing valid inventory with correct raft_secret_source ==="
cat > "${TMP_DIR}/valid_inventory.yml" << 'EOF'
all:
  hosts:
    replica1:
      ansible_connection: local
      cluster_id: 1
      raft_bootstrap: true
      raft_secret_source: replica1
    replica2:
      ansible_connection: local
      cluster_id: 1
      raft_bootstrap: false
      raft_secret_source: replica1
  children:
    exec_clusters:
      hosts:
        replica1:
        replica2:
EOF

ansible-playbook -i "${TMP_DIR}/valid_inventory.yml" "${TMP_DIR}/test_assert.yml" > "${TMP_DIR}/valid_output.log" 2>&1
echo "✅ Valid inventory assert PASSED"

echo "=== Step 3: Testing invalid inventory MISSING raft_secret_source ==="
cat > "${TMP_DIR}/missing_source_inventory.yml" << 'EOF'
all:
  hosts:
    replica1:
      ansible_connection: local
      cluster_id: 1
      raft_bootstrap: true
      raft_secret_source: replica1
    replica2:
      ansible_connection: local
      cluster_id: 1
      raft_bootstrap: false
      # Intentionally missing raft_secret_source
  children:
    exec_clusters:
      hosts:
        replica1:
        replica2:
EOF

set +e
ansible-playbook -i "${TMP_DIR}/missing_source_inventory.yml" "${TMP_DIR}/test_assert.yml" > "${TMP_DIR}/invalid_output.log" 2>&1
EXIT_CODE=$?
set -e

if [ $EXIT_CODE -eq 0 ]; then
  echo "❌ Expected assert failure, but playbook succeeded!"
  cat "${TMP_DIR}/invalid_output.log"
  exit 1
fi

if grep -q "raft_secret_source must name the bootstrap replica in this cluster" "${TMP_DIR}/invalid_output.log"; then
  echo "✅ Missing raft_secret_source correctly failed at assert step with expected message"
else
  echo "❌ Playbook failed but message did not match expected assertion message:"
  cat "${TMP_DIR}/invalid_output.log"
  exit 1
fi

echo "=== All Ansible exec_cluster assertion tests PASSED ==="
