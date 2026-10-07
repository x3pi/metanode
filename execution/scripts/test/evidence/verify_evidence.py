#!/usr/bin/env python3
"""
Verification tool for evidence manifests, raw logs, and markdown reports.
Strict anti-fabrication enforcement. Exits non-zero on any discrepancy.
Usage:
    python3 -I execution/scripts/test/evidence/verify_evidence.py <evidence_dir> [--report <report.md>] [--strict] [--max-mb <MB>]
"""

import argparse
import csv
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

def compute_stat(values, stat_type):
    if not values:
        return None
    if stat_type == "raw":
        return values[0]
    elif stat_type == "mean":
        return sum(values) / len(values)
    elif stat_type == "sd":
        if len(values) < 2:
            return 0.0
        m = sum(values) / len(values)
        variance = sum((x - m) ** 2 for x in values) / (len(values) - 1)
        return math.sqrt(variance)
    elif stat_type == "count":
        return float(len(values))
    elif stat_type == "sum":
        return sum(values)
    elif stat_type == "min":
        return min(values)
    elif stat_type == "max":
        return max(values)
    return None

def extract_values_from_file(evidence_dir, ext_def):
    """
    Extract numeric value(s) from a raw log or CSV file according to extractor definition.
    ext_def: {
        "name": "B1_100_A_mean",
        "file": "log_or_csv_file",
        "regex": "...",
        "group": 1,
        "csv_col": "...",
        "filter": "col=val,col2=val2",
        "stat": "raw|mean|sd|count|sum|min|max|slope",
        "x_col": "CumTxs" (for slope)
    }
    """
    fname = ext_def.get("file")
    if not fname:
        return None, "Extractor missing 'file' field"
    fpath = os.path.join(evidence_dir, fname)
    if not os.path.isfile(fpath):
        return None, f"Extractor file not found on disk: {fname}"

    stat_type = ext_def.get("stat", "raw")

    # CSV mode
    if "csv_col" in ext_def:
        col_name = ext_def["csv_col"]
        filters = {}
        if "filter" in ext_def and ext_def["filter"]:
            for part in ext_def["filter"].split(","):
                if "=" in part:
                    k, v = part.split("=", 1)
                    filters[k.strip()] = v.strip()

        matched_values = []
        x_values = []
        x_col_name = ext_def.get("x_col", "")

        with open(fpath, "r", encoding="utf-8") as f:
            reader = csv.DictReader(f)
            for row in reader:
                # check filters
                match = True
                for fk, fv in filters.items():
                    if row.get(fk, "").strip() != fv:
                        match = False
                        break
                if match and col_name in row:
                    try:
                        val = float(row[col_name])
                        matched_values.append(val)
                        if x_col_name and x_col_name in row:
                            x_values.append(float(row[x_col_name]))
                    except ValueError:
                        pass

        if not matched_values:
            return None, f"No matching CSV values for column '{col_name}' with filter '{filters}'"

        if stat_type == "slope":
            if len(matched_values) < 2 or len(x_values) != len(matched_values):
                return None, "Slope requires >= 2 pairs of (x, y) values"
            # calculate slope m in y = m * x + b
            n = len(matched_values)
            sum_x = sum(x_values)
            sum_y = sum(matched_values)
            sum_xy = sum(x * y for x, y in zip(x_values, matched_values))
            sum_xx = sum(x * x for x in x_values)
            denom = n * sum_xx - sum_x * sum_x
            if denom == 0:
                return None, "Zero denominator in slope computation"
            slope = (n * sum_xy - sum_x * sum_y) / denom
            # adjust unit if needed (e.g. per 100k txs)
            scale = float(ext_def.get("scale", 1.0))
            return slope * scale, None

        return compute_stat(matched_values, stat_type), None

    # Regex mode
    elif "regex" in ext_def:
        pattern = re.compile(ext_def["regex"], re.DOTALL | re.MULTILINE)
        with open(fpath, "r", encoding="utf-8") as f:
            content = f.read()

        group_idx = ext_def.get("group", 1)
        matches = pattern.findall(content)
        if not matches:
            return None, f"Regex '{ext_def['regex']}' found 0 matches in {fname}"

        extracted_floats = []
        for m in matches:
            val_str = ""
            if isinstance(m, tuple):
                if 0 <= group_idx - 1 < len(m):
                    val_str = m[group_idx - 1]
                else:
                    val_str = m[0]
            else:
                val_str = m
            # strip comma / extra chars
            val_str = val_str.replace(",", "").strip()
            try:
                extracted_floats.append(float(val_str))
            except ValueError:
                pass

        if not extracted_floats:
            return None, f"Failed to convert regex matches to floats: {matches[:3]}"

        return compute_stat(extracted_floats, stat_type), None

    return None, "Extractor must specify either 'regex' or 'csv_col'"

def verify_manifest(manifest_path, evidence_dir, strict=False, max_mb=10.0):
    errors = []
    if not os.path.isfile(manifest_path):
        return None, [f"Manifest file not found: {manifest_path}"]

    try:
        with open(manifest_path, "r", encoding="utf-8") as f:
            data = json.load(f)
    except Exception as e:
        return None, [f"Failed to parse manifest JSON: {e}"]

    # Check report field in manifest
    manifest_report = data.get("report")
    if not manifest_report:
        errors.append("MANIFEST.json lacks mandatory 'report' field linking to report markdown file")

    entries = data.get("entries", [])
    if not entries:
        errors.append("Manifest has no entries")

    entry_ids = set()
    manifest_files = {}
    total_bytes = 0

    extractors = {}

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

        git_status = entry.get("git_status", "")
        if git_status:
            print(f"⚠️  [MANIFEST WARNING] Entry {eid}: Recorded working tree was dirty: {git_status}")
            if strict:
                errors.append(f"Entry {eid}: Dirty git status recorded in manifest under --strict mode: {git_status}")

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
            total_bytes += actual_bytes

        # Parse extractors
        for ext_def in entry.get("extract", []):
            name = ext_def.get("name")
            if not name:
                errors.append(f"Entry {eid}: extractor missing 'name'")
                continue
            val, err = extract_values_from_file(evidence_dir, ext_def)
            if err:
                errors.append(f"Entry {eid} extractor '{name}' error: {err}")
            else:
                extractors[f"{eid}#{name}"] = val
                extractors[name] = val

    # External files registry
    external_files = {}
    for ext_finfo in data.get("external", []):
        epath = ext_finfo.get("path") or ext_finfo.get("name")
        if not epath:
            errors.append("External file entry missing 'path' or 'name'")
            continue
        req_fields = ["sha256", "bytes", "stored_at", "reason"]
        for rf in req_fields:
            if rf not in ext_finfo:
                errors.append(f"External file {epath} missing mandatory field '{rf}'")
        external_files[epath] = ext_finfo

    # Check for unregistered files on disk
    known_disk_files = set(manifest_files.keys())
    known_disk_files.add("MANIFEST.json")
    if manifest_report:
        known_disk_files.add(manifest_report)
        known_disk_files.add(os.path.basename(manifest_report))

    for fname in os.listdir(evidence_dir):
        fpath = os.path.join(evidence_dir, fname)
        if os.path.isfile(fpath):
            if fname not in known_disk_files and fname not in external_files:
                errors.append(f"Unregistered file found on disk in evidence directory: {fname}")

    # Check max evidence size limit
    max_bytes = max_mb * 1024 * 1024
    if total_bytes > max_bytes:
        errors.append(
            f"Total evidence size ({total_bytes / 1024 / 1024:.2f} MB) exceeds allowed limit of {max_mb:.1f} MB!"
        )

    return {
        "manifest_data": data,
        "entries": entries,
        "entry_ids": entry_ids,
        "files": manifest_files,
        "external": external_files,
        "extractors": extractors,
        "report": manifest_report,
        "total_bytes": total_bytes
    }, errors

def verify_report(report_path, manifest_info):
    errors = []
    if not os.path.isfile(report_path):
        return [f"Report file not found: {report_path}"]

    with open(report_path, "r", encoding="utf-8") as f:
        content = f.read()

    lines = content.splitlines()
    entry_ids = manifest_info["entry_ids"] if manifest_info else set()
    extractors = manifest_info.get("extractors", {}) if manifest_info else {}

    evidence_id_pattern = re.compile(r"evidence:([a-zA-Z0-9_\-\.]+)(?:#([a-zA-Z0-9_\-\.,]+))?")
    matches = evidence_id_pattern.findall(content)
    referenced_ids = {m[0] for m in matches}

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

    # Validate Table Data Rows:
    # Any row in a markdown table containing numbers MUST have an evidence:<id> tag
    in_table = False
    is_header = False
    for line_num, line in enumerate(lines, start=1):
        stripped = line.strip()
        if stripped.startswith("|") and stripped.endswith("|"):
            # Check if separator row (| :--- | :---: | :---: |)
            if re.match(r"^\|[\s\-\:\*\|]+$", stripped):
                is_header = False
                in_table = True
                continue
            if not in_table:
                # Top header row of table
                is_header = True
                in_table = True
                continue
            if in_table and not is_header:
                # Data row: check if it contains numeric figures
                # Match numbers like 10, 5.495, 1,000, +98.89, -7.3%
                has_number = bool(re.search(r"\d", stripped))
                if has_number:
                    has_tag = bool(evidence_id_pattern.search(stripped))
                    if not has_tag:
                        errors.append(
                            f"Line {line_num}: Data table row contains numbers but lacks mandatory 'evidence:<id>' tag: '{stripped[:60]}...'"
                        )

                # Check extracted values if tagged with evidence:<id>#<names>
                line_matches = evidence_id_pattern.findall(stripped)
                for eid, names_str in line_matches:
                    if names_str:
                        for ext_name in names_str.split(","):
                            ext_name = ext_name.strip()
                            key = f"{eid}#{ext_name}"
                            exp_val = extractors.get(key)
                            if exp_val is None:
                                exp_val = extractors.get(ext_name)

                            if exp_val is not None:
                                # Extract all numbers on the row
                                row_nums = []
                                for num_str in re.findall(r"[-+]?\d*\.?\d+", stripped.replace(",", "")):
                                    try:
                                        row_nums.append(float(num_str))
                                    except ValueError:
                                        pass

                                # Find matching number with <= 0.5% relative error or exact int match
                                found = False
                                for rn in row_nums:
                                    if exp_val == 0:
                                        if abs(rn) < 1e-4:
                                            found = True
                                            break
                                    else:
                                        rel_err = abs(rn - exp_val) / abs(exp_val)
                                        if rel_err <= 0.005:  # <= 0.5% tolerance
                                            found = True
                                            break

                                if not found:
                                    errors.append(
                                        f"Line {line_num}: Number mismatch for extract '{ext_name}' ({eid}#{ext_name})! "
                                        f"Computed value: {exp_val}, but row numbers {row_nums} have no match within 0.5%."
                                    )
        else:
            in_table = False
            is_header = False

    # Check text round counts vs listed items (e.g. "7 vòng giết leader (R3, R6, R11, R14, R17, R20)")
    round_mismatch_pat = re.compile(r"(\d+)\s+vòng\s+([^,;\.\(\n]+)\s*\(([R\d\s,]+)\)")
    for line_num, line in enumerate(lines, start=1):
        for m in round_mismatch_pat.finditer(line):
            claimed_count = int(m.group(1))
            items_str = m.group(3)
            # count items separated by commas
            items = [item.strip() for item in items_str.split(",") if item.strip()]
            if len(items) > 0 and len(items) != claimed_count:
                errors.append(
                    f"Line {line_num}: Text claims {claimed_count} vòng but lists {len(items)} items: '{items_str.strip()}'"
                )

    return errors

def main():
    parser = argparse.ArgumentParser(description="Verify evidence manifest and consistency")
    parser.add_argument("evidence_dir", help="Path to evidence directory")
    parser.add_argument("--report", help="Path to markdown report file to verify against", default=None)
    parser.add_argument("--strict", action="store_true", help="Enforce zero-warning strict mode (fails on dirty git)")
    parser.add_argument("--max-mb", type=float, default=10.0, help="Maximum allowed total evidence size in MB (default: 10)")
    args = parser.parse_args()

    evidence_dir = os.path.abspath(args.evidence_dir)
    manifest_path = os.path.join(evidence_dir, "MANIFEST.json")

    print(f"🔍 Verifying Evidence Directory: {evidence_dir}")
    manifest_info, manifest_errors = verify_manifest(manifest_path, evidence_dir, strict=args.strict, max_mb=args.max_mb)

    all_errors = list(manifest_errors)
    if not manifest_errors and manifest_info:
        print(f"✅ Manifest verified: {len(manifest_info['entries'])} entries, {len(manifest_info['files'])} files intact ({manifest_info['total_bytes'] / 1024 / 1024:.2f} MB).")

    report_path = args.report
    if not report_path and manifest_info and manifest_info.get("report"):
        # Resolve report path from manifest
        rep_candidate = manifest_info["report"]
        if not os.path.isabs(rep_candidate):
            try:
                git_root = subprocess.check_output(
                    ["git", "rev-parse", "--show-toplevel"],
                    cwd=evidence_dir, text=True, stderr=subprocess.DEVNULL
                ).strip()
            except Exception:
                git_root = os.path.abspath(os.path.join(evidence_dir, "..", "..", ".."))

            cand1 = os.path.join(git_root, rep_candidate)
            cand2 = os.path.join(evidence_dir, rep_candidate)
            cand3 = os.path.abspath(os.path.join(evidence_dir, "..", rep_candidate))
            if os.path.isfile(cand1):
                report_path = cand1
            elif os.path.isfile(cand2):
                report_path = cand2
            elif os.path.isfile(cand3):
                report_path = cand3
            else:
                report_path = cand1  # Will error in verify_report
        else:
            report_path = rep_candidate

    if report_path:
        print(f"🔍 Verifying Report Consistency: {report_path}")
        report_errors = verify_report(report_path, manifest_info)
        all_errors.extend(report_errors)
        if not report_errors:
            print(f"✅ Report claims, numbers & references verified against manifest.")
    elif manifest_info and not manifest_info.get("report"):
        all_errors.append("No report file could be resolved for this evidence directory!")

    if all_errors:
        print(f"\n❌ VERIFICATION FAILED with {len(all_errors)} error(s):")
        for err in all_errors:
            print(f"  • {err}")
        sys.exit(1)

    print("\n🎉 ALL EVIDENCE VERIFICATIONS PASSED (Exit 0)")
    sys.exit(0)

if __name__ == "__main__":
    main()
