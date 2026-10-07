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
                    if stat_type == "count":
                        matched_values.append(1.0)
                        continue
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

    # Tables configuration
    tables = []
    for tdef in data.get("tables", []):
        tname = tdef.get("name", "table")
        tfile = tdef.get("file")
        if not tfile:
            errors.append(f"Table '{tname}' missing 'file' attribute")
            continue
        tpath = os.path.join(evidence_dir, tfile)
        if not os.path.isfile(tpath):
            errors.append(f"Table '{tname}' file not found: {tfile}")
            continue
        try:
            with open(tpath, "r", encoding="utf-8") as f:
                reader = list(csv.DictReader(f))
            tables.append({
                "def": tdef,
                "rows": reader
            })
        except Exception as e:
            errors.append(f"Failed to read CSV table '{tfile}': {e}")

    # Free-text claims configuration
    claims = data.get("claims", [])

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
        "tables": tables,
        "claims": claims,
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

    # Check free-text claims defined in manifest
    for claim in manifest_info.get("claims", []):
        desc = claim.get("description", claim.get("regex", "claim"))
        pat = claim.get("regex")
        if not pat:
            continue
        m = re.search(pat, content, re.DOTALL)
        if not m:
            errors.append(f"Claim verification failed: pattern '{pat}' not found in report ({desc})")
            continue
        if "expected" in claim:
            expected = claim["expected"]
            val_str = m.group(1).replace(",", "").strip()
            try:
                val = float(val_str)
                if abs(val - float(expected)) > 1e-4:
                    errors.append(f"Claim mismatch for '{desc}': report has {val}, expected {expected}")
            except ValueError:
                if val_str != str(expected):
                    errors.append(f"Claim mismatch for '{desc}': report has '{val_str}', expected '{expected}'")
        elif "extractor" in claim:
            ext_name = claim["extractor"]
            exp_val = extractors.get(ext_name)
            if exp_val is None:
                errors.append(f"Claim references unknown extractor '{ext_name}' ({desc})")
                continue
            val_str = m.group(1).replace(",", "").strip()
            try:
                val = float(val_str)
                tol = claim.get("tolerance", 0.005)
                rel_err = abs(val - exp_val) / abs(exp_val) if exp_val != 0 else abs(val)
                if rel_err > tol:
                    errors.append(
                        f"Claim mismatch for '{desc}': report has {val}, extractor '{ext_name}' has {exp_val} "
                        f"(rel err: {rel_err:.4f} > {tol})"
                    )
            except ValueError:
                errors.append(f"Claim numeric conversion failed for '{desc}': '{val_str}'")

    # Parse markdown table blocks
    table_blocks = []
    current_block = []
    for line_num, line in enumerate(lines, start=1):
        stripped = line.strip()
        if stripped.startswith("|") and stripped.endswith("|"):
            current_block.append((line_num, stripped))
        else:
            if current_block:
                table_blocks.append(current_block)
                current_block = []
    if current_block:
        table_blocks.append(current_block)

    # Validate each table block
    for block in table_blocks:
        if len(block) < 2:
            continue
        header_line_num, header_str = block[0]
        sep_line_num, sep_str = block[1]
        if not re.match(r"^\|[\s\-\:\*\|]+$", sep_str):
            # Not a standard table header/separator
            continue

        headers = [c.strip() for c in header_str.strip('|').split('|')]
        data_rows = block[2:]

        # Check if table matches any declared table in manifest
        matched_table_info = None
        for tinfo in manifest_info.get("tables", []):
            tdef = tinfo["def"]
            h_marker = tdef.get("header_marker")
            if h_marker and (h_marker in header_str or any(h_marker in h for h in headers)):
                matched_table_info = tinfo
                break

        if matched_table_info:
            tdef = matched_table_info["def"]
            csv_rows = matched_table_info["rows"]
            tname = tdef.get("name", tdef.get("file", "table"))

            if len(data_rows) != len(csv_rows):
                errors.append(
                    f"Table '{tname}' row count mismatch: report has {len(data_rows)} data rows, but CSV has {len(csv_rows)} rows"
                )

            for r_idx, (r_line_num, r_str) in enumerate(data_rows):
                if r_idx >= len(csv_rows):
                    break
                csv_row = csv_rows[r_idx]
                cells = [c.strip() for c in r_str.strip('|').split('|')]
                if len(cells) != len(headers):
                    errors.append(
                        f"Line {r_line_num}: Table '{tname}' cell count ({len(cells)}) mismatch with headers ({len(headers)})"
                    )
                    continue

                for md_col_name, col_rule in tdef.get("columns", {}).items():
                    # locate column index
                    col_idx = -1
                    for c_i, h in enumerate(headers):
                        if md_col_name in h:
                            col_idx = c_i
                            break
                    if col_idx == -1:
                        errors.append(f"Table '{tname}' column '{md_col_name}' not found in headers {headers}")
                        continue
                    if col_idx >= len(cells):
                        continue

                    raw_md_cell = cells[col_idx]
                    clean_md = raw_md_cell.replace("**", "").replace("`", "").strip()

                    csv_col = col_rule.get("csv_col", md_col_name) if isinstance(col_rule, dict) else col_rule
                    exp_csv = csv_row.get(csv_col, "").strip()

                    # Comparison
                    matched = False
                    if clean_md == exp_csv:
                        matched = True
                    elif clean_md.lstrip('#') == exp_csv.lstrip('#') or clean_md.lstrip('R') == exp_csv.lstrip('R'):
                        matched = True
                    elif clean_md == f"#{exp_csv}" or clean_md == f"R{exp_csv}":
                        matched = True
                    else:
                        # Numeric match
                        try:
                            f_md = float(clean_md.lstrip('#').lstrip('R'))
                            f_csv = float(exp_csv.lstrip('#').lstrip('R'))
                            if abs(f_md - f_csv) < 1e-4:
                                matched = True
                        except ValueError:
                            pass

                    if not matched:
                        errors.append(
                            f"Line {r_line_num}: Table '{tname}' cell mismatch for '{md_col_name}'! "
                            f"Report has '{clean_md}', CSV has '{exp_csv}'"
                        )

        # For every data row in this table block:
        for r_line_num, r_str in data_rows:
            # 1. Check mandatory evidence tag if row contains numbers
            has_number = bool(re.search(r"\d", r_str))
            if has_number:
                has_tag = bool(evidence_id_pattern.search(r_str))
                if not has_tag:
                    errors.append(
                        f"Line {r_line_num}: Data table row contains numbers but lacks mandatory 'evidence:<id>' tag: '{r_str[:60]}...'"
                    )

            # 2. Check arithmetic verdict consistency (e.g. B <= threshold => PASS)
            r_cells = [c.strip() for c in r_str.strip('|').split('|')]
            thresh_cell = next((c for c in r_cells if re.search(r"B\s*(?:<=|≤)", c)), None)
            if thresh_cell:
                m_thresh = re.search(r"B\s*(?:<=|≤)\s*([0-9\.]+)", thresh_cell)
                if m_thresh:
                    thresh_val = float(m_thresh.group(1))
                    b_col_idx = next((i for i, h in enumerate(headers) if "Cấu hình B" in h), -1)
                    b_val = None
                    if b_col_idx != -1 and b_col_idx < len(r_cells):
                        m_b = re.search(r"([0-9\.]+)", r_cells[b_col_idx])
                        if m_b:
                            b_val = float(m_b.group(1))
                    if b_val is not None:
                        if "**FAIL**" in r_str and b_val <= thresh_val:
                            errors.append(
                                f"Line {r_line_num}: Inconsistent verdict! Measured B ({b_val}) <= threshold ({thresh_val}), but row marks FAIL"
                            )
                        elif "**PASS**" in r_str and b_val > thresh_val:
                            errors.append(
                                f"Line {r_line_num}: Inconsistent verdict! Measured B ({b_val}) > threshold ({thresh_val}), but row marks PASS"
                            )

            # 3. Check extracted values tagged with evidence:<id>#<names>
            line_matches = evidence_id_pattern.findall(r_str)
            for eid, names_str in line_matches:
                if names_str:
                    for ext_name in names_str.split(","):
                        ext_name = ext_name.strip()
                        key = f"{eid}#{ext_name}"
                        exp_val = extractors.get(key)
                        if exp_val is None:
                            exp_val = extractors.get(ext_name)

                        if exp_val is not None:
                            row_nums = []
                            for num_str in re.findall(r"[-+]?\d*\.?\d+", r_str.replace(",", "")):
                                try:
                                    row_nums.append(float(num_str))
                                except ValueError:
                                    pass

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
                                    f"Line {r_line_num}: Number mismatch for extract '{ext_name}' ({eid}#{ext_name})! "
                                    f"Computed value: {exp_val}, but row numbers {row_nums} have no match within 0.5%."
                                )

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
