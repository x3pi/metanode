#!/usr/bin/env python3
"""Lint performance/analysis reports for unverifiable claims.
ERRORS (exit 1):
  R1  absolute wording ("100%", "hoàn toàn", "tuyệt đối", "không hề", "airtight", "chắc chắn", "chứng minh") on a line with no
      `evidence:<id>` / `file:line` reference within +-1 line (code blocks excluded)
  R2  report lacks a "Giới hạn" heading or a "Chưa làm" heading
WARNINGS (exit 0):
  R3  a comparative claim with a percentage ("nhanh hơn", "chậm hơn", "tăng", "giảm", "cải thiện") and no evidence tag on the line
  R4  the report uses none of the claim labels [ĐO] [TÍNH] [ĐỌC CODE] [TRÍCH] [GIẢ THUYẾT] [CHƯA ĐO]
Usage: claims_lint.py report.md [report2.md ...]
"""
import re
import sys

ABSOLUTE = ["100%", "hoàn toàn", "tuyệt đối", "không hề", "airtight", "chắc chắn", "chứng minh"]
EVID = re.compile(r"evidence:[A-Za-z0-9_.\-]+|`?[\w./\-]+\.(?:go|rs|py|sh|md|yml|j2|json|toml|log|csv|txt):\d+")
LABELS = ["[ĐO]", "[TÍNH]", "[ĐỌC CODE]", "[TRÍCH]", "[GIẢ THUYẾT]", "[CHƯA ĐO]"]
COMPARE = re.compile(r"(nhanh hơn|chậm hơn|tăng|giảm|cải thiện)", re.I)
PCT = re.compile(r"\d+(?:[.,]\d+)?\s?%")


def lint_text(text):
    errors, warnings = [], []
    lines = text.splitlines()
    in_code = False
    for i, line in enumerate(lines):
        if line.strip().startswith("```"):
            in_code = not in_code
            continue
        if in_code:
            continue
        window = " ".join(lines[max(0, i - 1): i + 2])
        low = line.lower()
        for w in ABSOLUTE:
            if w in low and not EVID.search(window):
                errors.append(f"line {i+1}: absolute wording '{w}' without an evidence/file:line reference nearby: {line.strip()[:120]}")
                break
        if COMPARE.search(line) and PCT.search(line) and not EVID.search(line):
            warnings.append(f"line {i+1}: comparative percentage without an evidence tag on the line: {line.strip()[:120]}")
    heads = [l.lower() for l in lines if l.startswith("#")]
    if not any("giới hạn" in h for h in heads):
        errors.append("missing a 'Giới hạn' heading")
    if not any("chưa làm" in h for h in heads):
        errors.append("missing a 'Chưa làm' heading")
    if not any(l in text for l in LABELS):
        warnings.append("no claim labels ([ĐO]/[TÍNH]/[ĐỌC CODE]/[TRÍCH]/[GIẢ THUYẾT]/[CHƯA ĐO]) used")
    return errors, warnings


def main(argv):
    rc = 0
    for p in argv[1:]:
        e, w = lint_text(open(p, encoding="utf-8").read())
        print(f"== {p}: {len(e)} error(s), {len(w)} warning(s)")
        for x in e:
            print("  ERROR  ", x)
        for x in w:
            print("  warning", x)
        if e:
            rc = 1
    return rc


if __name__ == "__main__":
    sys.exit(main(sys.argv))
