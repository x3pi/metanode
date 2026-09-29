#!/usr/bin/env python3
"""
check_inventory_security.py - Centralized Ansible Inventory Security Validator

Validates inventory files against Issue #104 security requirements:
- In production, credentials MUST NOT be stored in plaintext.
- Allowed forms:
    1. Entire file is encrypted with Ansible Vault ($ANSIBLE_VAULT;)
    2. Inline Ansible Vault encrypted string (!vault | ...)
    3. Jinja2 template variable reference to external/group vault (e.g. "{{ vault_become_pass }}")
    4. Empty/null value ("", '', null, ~)
- Checked credential keys:
    - ansible_become_pass / ansible_become_password
    - ansible_ssh_pass / ansible_password
    - ansible_sudo_pass

Usage:
    python3 check_inventory_security.py <inventory_file>
Exit codes:
    0 = Clean (no plaintext credentials found)
    1 = Violations found (prints list of violations to stdout)
"""

import sys
import os
import re

CREDENTIAL_KEY_PATTERN = re.compile(
    r"^\s*(ansible_become_pass|ansible_become_password|ansible_ssh_pass|ansible_password|ansible_sudo_pass):\s*(\S+.*)$"
)

def check_inventory_security(file_path):
    if not file_path or not os.path.isfile(file_path):
        return []

    try:
        with open(file_path, "r", encoding="utf-8", errors="ignore") as f:
            content = f.read()
    except Exception as e:
        print(f"Error reading {file_path}: {e}", file=sys.stderr)
        return []

    # If the entire file is vault-encrypted, it is completely secure
    if content.strip().startswith("$ANSIBLE_VAULT"):
        return []

    violations = []
    lines = content.splitlines()

    for idx, line in enumerate(lines, 1):
        stripped = line.strip()
        # Ignore comments
        if stripped.startswith("#"):
            continue

        m = CREDENTIAL_KEY_PATTERN.match(line)
        if m:
            key = m.group(1)
            raw_val = m.group(2).strip()

            # 1. Check for inline Ansible Vault
            if raw_val.startswith("!vault"):
                continue

            # Strip outer quotes for template / null checks
            clean_val = raw_val.strip("\"'").strip()

            # 2. Check for Jinja2 template references (e.g. "{{ vault_x }}")
            if clean_val.startswith("{{") and clean_val.endswith("}}"):
                continue

            # 3. Check for empty/null placeholders
            if clean_val in ("", "null", "~"):
                continue

            violations.append(f"line {idx}: {key}")

    return violations

def main():
    if len(sys.argv) < 2:
        print("Usage: check_inventory_security.py <inventory_file>", file=sys.stderr)
        sys.exit(1)

    inv_path = sys.argv[1]
    violations = check_inventory_security(inv_path)

    if violations:
        print(", ".join(violations))
        sys.exit(1)
    else:
        sys.exit(0)

if __name__ == "__main__":
    main()
