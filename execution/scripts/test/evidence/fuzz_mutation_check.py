#!/usr/bin/env python3
"""
Fuzz Mutation Check Harness.
Measures the actual verification coverage of verify_evidence.py by applying
random single-token mutations to numeric values in table data rows (rows containing 'evidence:').

Adheres strictly to the specification in note/plan_fuzz_coverage_drift_ttl_20261007.md:
1. Targets table rows starting with '|' and containing 'evidence:'.
2. Excludes code blocks, URLs, text in backticks, dates, list indices, and evidence tag IDs.
3. Finds numeric tokens (integers with commas, decimals, #<block_num>, with optional units/signs).
4. Mutates the LAST DIGIT of a single token to (d + 3) % 10.
5. Runs `verify_evidence.py --strict <dir> --report <mutated_report>` and checks exit code.
6. Reports total tokens, caught count, missed count, detection rate, and list of missed tokens.
"""

import argparse
import glob
import json
import os
import random
import re
import subprocess
import sys
import tempfile

def find_numeric_tokens_in_report(report_content):
    """
    Parses Markdown content and returns a list of numeric tokens located in
    valid table data rows containing 'evidence:'.
    Returns list of dicts:
      {
        "line_no": int (1-based),
        "line_text": str,
        "token": str,
        "start_idx": int (in report_content),
        "end_idx": int (in report_content),
        "last_digit_pos": int (in report_content),
        "original_digit": str
      }
    """
    tokens = []
    lines = report_content.splitlines(keepends=True)
    in_code_block = False
    current_offset = 0

    # Pattern for dates like 2026-10-07
    date_pattern = re.compile(r'\b\d{4}-\d{2}-\d{2}\b')
    # Pattern for URLs
    url_pattern = re.compile(r'https?://\S+')
    # Pattern for backticks
    backtick_pattern = re.compile(r'`[^`]*`')
    # Pattern for evidence tag
    evidence_tag_pattern = re.compile(r'evidence:[a-zA-Z0-9_]+')

    for line_idx, line in enumerate(lines):
        line_no = line_idx + 1
        line_offset = current_offset
        current_offset += len(line)

        stripped = line.strip()
        if stripped.startswith("```"):
            in_code_block = not in_code_block
            continue
        if in_code_block:
            continue

        # Must be a table row with evidence tag
        if not (stripped.startswith("|") and "evidence:" in stripped):
            continue

        # Skip separator rows like | :--- | :---: |
        if re.match(r'^\|(?:\s*:?-+:?\s*\|)+$', stripped):
            continue

        # Find spans to ignore in this line:
        # 1. evidence tags
        # 2. backticks
        # 3. URLs
        # 4. dates
        ignore_spans = []
        for m in evidence_tag_pattern.finditer(line):
            ignore_spans.append((m.start(), m.end()))
        for m in backtick_pattern.finditer(line):
            ignore_spans.append((m.start(), m.end()))
        for m in url_pattern.finditer(line):
            ignore_spans.append((m.start(), m.end()))
        for m in date_pattern.finditer(line):
            ignore_spans.append((m.start(), m.end()))

        def is_ignored(start_pos, end_pos):
            for s, e in ignore_spans:
                if max(start_pos, s) < min(end_pos, e):
                    return True
            return False

        # Match numbers:
        # e.g., #310, +1.62%, -36.27, 6,198.6, 25,000, 5.29s, 83.4s, 47.81 MB
        # We look for token matching:
        # (?:#\d+|[+-]?\d+(?:,\d{3})*(?:\.\d+)?(?:s|ms|µs|us|%|KB|MB|GB)?)
        num_pattern = re.compile(r'(?:#\d+|[+-]?\b\d+(?:,\d{3})*(?:\.\d+)?(?:s|ms|µs|us|%|KB|MB|GB)?\b)')

        for m in num_pattern.finditer(line):
            t_start, t_end = m.start(), m.end()
            if is_ignored(t_start, t_end):
                continue

            token_str = m.group(0)

            # Find the last digit inside the token
            digit_positions = [i for i, c in enumerate(token_str) if c.isdigit()]
            if not digit_positions:
                continue

            last_digit_idx = digit_positions[-1]
            last_digit_char = token_str[last_digit_idx]

            abs_token_start = line_offset + t_start
            abs_token_end = line_offset + t_end
            abs_digit_pos = line_offset + t_start + last_digit_idx

            tokens.append({
                "line_no": line_no,
                "line_text": stripped,
                "token": token_str,
                "start_idx": abs_token_start,
                "end_idx": abs_token_end,
                "last_digit_pos": abs_digit_pos,
                "original_digit": last_digit_char
            })

    return tokens

def get_repo_root():
    try:
        out = subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()
        if os.path.isdir(out):
            return out
    except Exception:
        pass
    cur = os.path.dirname(os.path.abspath(__file__))
    while cur and cur != "/":
        if os.path.isfile(os.path.join(cur, "PROJECT_STRUCTURE.md")):
            return cur
        cur = os.path.dirname(cur)
    return "/home/abc/chain-n/metanode"

def run_fuzz_on_report(manifest_path, seed=11, sample_size=60, sample_all=False, verify_script=None):
    """
    Runs fuzz mutation check for a single manifest and its associated report.
    """
    repo_root = get_repo_root()
    if verify_script is None:
        verify_script = os.path.join(repo_root, "execution/scripts/test/evidence/verify_evidence.py")

    manifest_dir = os.path.dirname(os.path.abspath(manifest_path))
    with open(manifest_path, "r", encoding="utf-8") as f:
        manifest_data = json.load(f)

    report_rel = manifest_data.get("report")
    if not report_rel:
        raise ValueError(f"No 'report' field in {manifest_path}")

    report_path = os.path.join(repo_root, report_rel)
    if not os.path.isfile(report_path):
        report_path = os.path.join(manifest_dir, report_rel)
    if not os.path.isfile(report_path):
        raise FileNotFoundError(f"Report file not found: {report_rel}")

    with open(report_path, "r", encoding="utf-8") as f:
        report_content = f.read()

    tokens = find_numeric_tokens_in_report(report_content)
    total_tokens = len(tokens)

    # Separate tokens matching declared unchecked rules
    unchecked_rules = manifest_data.get("unchecked", [])
    unchecked_tokens = []
    auditable_tokens = []
    for tok in tokens:
        matched_rule = None
        for u in unchecked_rules:
            pat = u.get("line_regex", "")
            if pat and re.search(pat, tok["line_text"]):
                matched_rule = u
                break
        if matched_rule:
            unchecked_tokens.append({
                "token": tok["token"],
                "line_no": tok["line_no"],
                "reason": matched_rule.get("reason", "exempted")
            })
        else:
            auditable_tokens.append(tok)

    # Determine subset to test
    if sample_all or sample_size >= len(auditable_tokens):
        selected_tokens = auditable_tokens
    else:
        rng = random.Random(seed)
        selected_tokens = rng.sample(auditable_tokens, sample_size)

    caught_count = 0
    missed_details = []

    # Verify baseline first
    baseline_cmd = [sys.executable, "-I", verify_script, "--strict", manifest_dir, "--report", report_path]
    res_base = subprocess.run(baseline_cmd, capture_output=True, text=True, check=False)
    if res_base.returncode != 0:
        raise RuntimeError(f"Baseline report failed verification: {manifest_path}\n{res_base.stderr or res_base.stdout}")

    with tempfile.TemporaryDirectory() as tmp_dir:
        temp_report_path = os.path.join(tmp_dir, "mutated_report.md")

        for idx, tok in enumerate(selected_tokens):
            orig_digit = int(tok["original_digit"])
            mutated_digit = str((orig_digit + 3) % 10)

            pos = tok["last_digit_pos"]
            mutated_content = report_content[:pos] + mutated_digit + report_content[pos + 1:]

            with open(temp_report_path, "w", encoding="utf-8") as f:
                f.write(mutated_content)

            cmd = [sys.executable, "-I", verify_script, "--strict", manifest_dir, "--report", temp_report_path]
            res = subprocess.run(cmd, capture_output=True, text=True, check=False)

            if res.returncode != 0:
                caught_count += 1
            else:
                missed_details.append({
                    "line_no": tok["line_no"],
                    "token": tok["token"],
                    "mutated_token": tok["token"][:tok["last_digit_pos"] - tok["start_idx"]] + mutated_digit + tok["token"][tok["last_digit_pos"] - tok["start_idx"] + 1:],
                    "line_text": tok["line_text"]
                })

    tested_count = len(selected_tokens)
    missed_count = tested_count - caught_count
    catch_rate = (caught_count / tested_count) * 100.0 if tested_count > 0 else 0.0

    return {
        "manifest": manifest_path,
        "report": report_rel,
        "total_tokens": total_tokens,
        "auditable_tokens": len(auditable_tokens),
        "unchecked_count": len(unchecked_tokens),
        "tested_tokens": tested_count,
        "caught_count": caught_count,
        "missed_count": missed_count,
        "catch_rate": round(catch_rate, 2),
        "missed_details": missed_details,
        "unchecked_tokens": unchecked_tokens
    }

def main():
    parser = argparse.ArgumentParser(description="Fuzz Mutation Check for Evidence Verification Coverage")
    parser.add_argument("--seed", type=int, required=True, help="Random seed for sampling (MANDATORY)")
    parser.add_argument("--sample", type=int, default=60, help="Number of numeric tokens to sample per report (default: 60)")
    parser.add_argument("--all", action="store_true", help="Test ALL numeric tokens in table data rows")
    parser.add_argument("--manifest", type=str, help="Specific MANIFEST.json to test (default: all in note/evidence/*)")
    parser.add_argument("--output", type=str, help="Save summary report to JSON file")
    args = parser.parse_args()

    repo_root = get_repo_root()

    if args.manifest:
        manifest_files = [args.manifest]
    else:
        manifest_pattern = os.path.join(repo_root, "note/evidence/*/MANIFEST.json")
        manifest_files = sorted(glob.glob(manifest_pattern))

    if not manifest_files:
        print("❌ No MANIFEST.json files found!", file=sys.stderr)
        sys.exit(1)

    print("==================================================================")
    print(f"🎲 FUZZ MUTATION COVERAGE AUDIT (Seed: {args.seed}, Sample: {'ALL' if args.all else args.sample})")
    print("==================================================================")

    results = []
    total_tested = 0
    total_caught = 0

    for mf in manifest_files:
        print(f"\n▶️ Auditing manifest: {os.path.relpath(mf, repo_root)}")
        try:
            res = run_fuzz_on_report(mf, seed=args.seed, sample_size=args.sample, sample_all=args.all)
            results.append(res)

            total_tested += res["tested_tokens"]
            total_caught += res["caught_count"]

            print(f"   Report: {res['report']}")
            print(f"   Tokens found: {res['total_tokens']} | Tested: {res['tested_tokens']}")
            print(f"   Caught: {res['caught_count']} | Missed: {res['missed_count']}")
            print(f"   Catch Rate: {res['catch_rate']:.2f}%")

            if res["missed_details"]:
                print(f"   ⚠️ Missed Tokens Sample (up to 5):")
                for m in res["missed_details"][:5]:
                    print(f"      • Line {m['line_no']}: '{m['token']}' -> '{m['mutated_token']}' | {m['line_text'][:80]}...")

        except Exception as e:
            print(f"   ❌ Error during fuzzing {mf}: {e}", file=sys.stderr)
            import traceback
            traceback.print_exc()

    overall_rate = (total_caught / total_tested * 100.0) if total_tested > 0 else 0.0
    print("\n==================================================================")
    print(f"📊 OVERALL FUZZ AUDIT SUMMARY (Seed: {args.seed}):")
    print(f"   Total Tested: {total_tested}")
    print(f"   Total Caught: {total_caught}")
    print(f"   Total Missed: {total_tested - total_caught}")
    print(f"   Overall Catch Rate: {overall_rate:.2f}%")
    print("==================================================================")

    if args.output:
        with open(args.output, "w", encoding="utf-8") as f:
            json.dump({
                "seed": args.seed,
                "overall_tested": total_tested,
                "overall_caught": total_caught,
                "overall_catch_rate": round(overall_rate, 2),
                "reports": results
            }, f, indent=2)
        print(f"📁 Detailed JSON results saved to: {args.output}")

    # Exit code: 0 if catch rate >= 95%, 1 otherwise
    if overall_rate < 95.0:
        sys.exit(1)
    sys.exit(0)

if __name__ == "__main__":
    main()
