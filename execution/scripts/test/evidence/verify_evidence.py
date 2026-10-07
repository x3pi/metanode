#!/usr/bin/env python3
"""
Verification tool for evidence manifests, raw logs, and markdown reports.
Strict anti-fabrication enforcement. Exits non-zero on any discrepancy.
Usage:
    python3 -I execution/scripts/test/evidence/verify_evidence.py <evidence_dir> [--report <report.md>]
"""

import argparse
import hashlib
import json
import math
import os
import re
import subprocess
import sys

FORBIDDEN_WORDS = [
    "100%",
    "hoàn toàn",
    "tuyệt đối",
    "không hề",
    "chứng minh 100%"
]

def calculate_sha256_and_bytes(filepath):
    h = hashlib.sha256()
    size = 0
    with open(filepath, "rb") as f:
        while chunk := f.read(65536):
            h.update(chunk)
            size += len(chunk)
    return h.hexdigest(), size

def check_git_commit_exists(commit_hash):
    if not commit_hash or commit_hash == "unknown":
        return True
    try:
        res = subprocess.run(
            ["git", "cat-file", "-e", f"{commit_hash}^{{commit}}"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False
        )
        return res.returncode == 0
    except Exception:
        return False

def verify_manifest(manifest_path, evidence_dir):
    errors = []
    if not os.path.isfile(manifest_path):
        return None, [f"Manifest file not found: {manifest_path}"]

    try:
        with open(manifest_path, "r", encoding="utf-8") as f:
            data = json.load(f)
    except Exception as e:
        return None, [f"Failed to parse manifest JSON: {e}"]

    entries = data.get("entries", [])
    if not entries:
        errors.append("Manifest has no entries")

    entry_ids = set()
    manifest_files = {}

    for entry in entries:
        eid = entry.get("id")
        if not eid:
            errors.append("Found entry without id")
            continue
        if eid in entry_ids:
            errors.append(f"Duplicate entry ID in manifest: {eid}")
        entry_ids.add(eid)

        commit = entry.get("git_commit")
        if commit and not check_git_commit_exists(commit):
            errors.append(f"Entry {eid}: git commit {commit} does not exist in repository")

        files = entry.get("files", [])
        if not files:
            errors.append(f"Entry {eid} has no files listed")

        for finfo in files:
            rel_path = finfo.get("path")
            exp_sha = finfo.get("sha256")
            exp_bytes = finfo.get("bytes")

            full_path = os.path.join(evidence_dir, rel_path)
            if not os.path.isfile(full_path):
                errors.append(f"Entry {eid}: file {rel_path} does not exist on disk")
                continue

            actual_sha, actual_bytes = calculate_sha256_and_bytes(full_path)
            if actual_sha != exp_sha:
                errors.append(
                    f"Entry {eid}: file {rel_path} sha256 mismatch! "
                    f"Expected {exp_sha}, got {actual_sha}"
                )
            if actual_bytes != exp_bytes:
                errors.append(
                    f"Entry {eid}: file {rel_path} byte count mismatch! "
                    f"Expected {exp_bytes}, got {actual_bytes}"
                )

            manifest_files[rel_path] = {
                "entry_id": eid,
                "sha256": actual_sha,
                "bytes": actual_bytes,
                "full_path": full_path
            }

    return {"entries": entries, "entry_ids": entry_ids, "files": manifest_files}, errors

def verify_report(report_path, manifest_data):
    errors = []
    if not os.path.isfile(report_path):
        return [f"Report file not found: {report_path}"]

    with open(report_path, "r", encoding="utf-8") as f:
        content = f.read()

    lines = content.splitlines()
    entry_ids = manifest_data["entry_ids"] if manifest_data else set()

    evidence_id_pattern = re.compile(r"evidence:([a-zA-Z0-9_\-\.]+)")
    referenced_ids = set(evidence_id_pattern.findall(content))

    for ref in referenced_ids:
        if ref not in entry_ids:
            errors.append(f"Report references evidence ID '{ref}' which is NOT in MANIFEST.json")

    # Check for forbidden absolute claims without nearby evidence
    in_code_block = False
    for line_num, line in enumerate(lines, start=1):
        stripped = line.strip()
        if stripped.startswith("```"):
            in_code_block = not in_code_block
            continue
        if in_code_block:
            continue

        lower_line = line.lower()
        for forbidden in FORBIDDEN_WORDS:
            if forbidden.lower() in lower_line:
                # Must have evidence:<id> on this line or within 1 line
                has_evidence = bool(evidence_id_pattern.search(line))
                if not has_evidence and line_num > 1:
                    has_evidence = bool(evidence_id_pattern.search(lines[line_num - 2]))
                if not has_evidence and line_num < len(lines):
                    has_evidence = bool(evidence_id_pattern.search(lines[line_num]))

                if not has_evidence:
                    errors.append(
                        f"Line {line_num}: Forbidden claim '{forbidden}' found without adjacent evidence:<id> reference: '{line.strip()}'"
                    )

    # Check data tables: lines with numbers and pipes
    table_lines = [
        (i, l) for i, l in enumerate(lines, 1)
        if l.strip().startswith("|") and l.strip().endswith("|") and not re.match(r"^\|[\s\-\:]+\|$", l.strip())
    ]
    # If report has tables containing numeric results, ensure evidence is referenced in report
    if table_lines and not referenced_ids:
        errors.append(f"Report contains data tables but NO evidence:<id> tags were found!")

    return errors

def main():
    parser = argparse.ArgumentParser(description="Verify evidence manifest and consistency")
    parser.add_argument("evidence_dir", help="Path to evidence directory")
    parser.add_argument("--report", help="Path to markdown report file to verify against", default=None)
    args = parser.parse_args()

    evidence_dir = os.path.abspath(args.evidence_dir)
    manifest_path = os.path.join(evidence_dir, "MANIFEST.json")

    print(f"🔍 Verifying Evidence Directory: {evidence_dir}")
    manifest_data, manifest_errors = verify_manifest(manifest_path, evidence_dir)

    all_errors = list(manifest_errors)
    if not manifest_errors and manifest_data:
        print(f"✅ Manifest verified: {len(manifest_data['entries'])} entries, {len(manifest_data['files'])} files intact.")

    report_path = args.report
    if not report_path:
        # Auto-detect if evidence_dir name matches note/<name>.md
        base_name = os.path.basename(evidence_dir)
        candidate = os.path.join(os.path.dirname(os.path.dirname(evidence_dir)), f"{base_name}.md")
        if os.path.isfile(candidate):
            report_path = candidate

    if report_path:
        print(f"🔍 Verifying Report Consistency: {report_path}")
        report_errors = verify_report(report_path, manifest_data)
        all_errors.extend(report_errors)
        if not report_errors:
            print(f"✅ Report claims & references verified against manifest.")

    if all_errors:
        print(f"\n❌ VERIFICATION FAILED with {len(all_errors)} error(s):")
        for err in all_errors:
            print(f"  • {err}")
        sys.exit(1)

    print("\n🎉 ALL EVIDENCE VERIFICATIONS PASSED (Exit 0)")
    sys.exit(0)

if __name__ == "__main__":
    main()
