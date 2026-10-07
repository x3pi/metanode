#!/usr/bin/env python3
"""
Unit tests for fuzz_mutation_check.py.
Verifies:
1. Token extraction correctness on Markdown tables.
2. Ignoring non-table numbers, dates, backtick code, and URLs.
3. Accurate mutation counting (e.g. mock verify script catching exactly k/10 tokens).
4. Determinism of sampling with identical seeds.
"""

import json
import os
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fuzz_mutation_check import find_numeric_tokens_in_report, run_fuzz_on_report

class TestFuzzMutationCheck(unittest.TestCase):
    def test_token_extraction(self):
        sample_md = """# Sample Report
Some text with 1234 outside table.

| Col A | Col B | Col C | Tag |
| :--- | :--- | :--- | :--- |
| `val0` | 5.29s | 6,276.2 MB | evidence:test_tag |
| Block #310 | 2026-10-07 | https://example.com/123 | evidence:test_tag |
"""
        tokens = find_numeric_tokens_in_report(sample_md)
        token_strs = [t["token"] for t in tokens]
        # Should catch: '5.29s', '6,276.2', '#310'
        # Should ignore: '1234' (outside table), `val0`, '2026-10-07' (date), 'https://example.com/123' (URL)
        self.assertIn("5.29s", token_strs)
        self.assertIn("6,276.2", token_strs)
        self.assertIn("#310", token_strs)
        self.assertNotIn("1234", token_strs)
        self.assertNotIn("2026-10-07", token_strs)
        self.assertNotIn("test_tag", token_strs)

    def test_deterministic_seed(self):
        sample_md = "\n".join([f"| Row {i} | {i*10} | evidence:tag |" for i in range(50)])
        tokens = find_numeric_tokens_in_report(sample_md)
        self.assertGreaterEqual(len(tokens), 50)

        # Sampling with same seed must give identical items
        import random
        rng1 = random.Random(42)
        s1 = [t["token"] for t in rng1.sample(tokens, 10)]

        rng2 = random.Random(42)
        s2 = [t["token"] for t in rng2.sample(tokens, 10)]

        self.assertEqual(s1, s2)

    def test_mock_harness_catch_rate(self):
        """
        Creates a temporary mock verify script that catches tokens containing '9',
        and verifies that fuzz_mutation_check accurately reports the catch rate.
        """
        with tempfile.TemporaryDirectory() as tmp_dir:
            # Create a mock report with 5 tokens
            report_path = os.path.join(tmp_dir, "mock_report.md")
            report_content = """| Name | Value | Tag |
| :--- | :--- | :--- |
| ItemA | 10 | evidence:mock |
| ItemB | 20 | evidence:mock |
| ItemC | 30 | evidence:mock |
| ItemD | 40 | evidence:mock |
| ItemE | 50 | evidence:mock |
"""
            with open(report_path, "w", encoding="utf-8") as f:
                f.write(report_content)

            # Create mock MANIFEST.json
            manifest_path = os.path.join(tmp_dir, "MANIFEST.json")
            manifest_content = {
                "manifest_version": "1.0",
                "report": "mock_report.md",
                "entries": []
            }
            with open(manifest_path, "w", encoding="utf-8") as f:
                json.dump(manifest_content, f)

            # Create a mock verify_evidence script
            # In our mutation, digit + 3:
            # 10 -> last digit 0 -> mutated to 3 (not caught)
            # 20 -> mutated to 3
            # If mutated token contains '3', let's say it's caught
            mock_verify_path = os.path.join(tmp_dir, "mock_verify.py")
            with open(mock_verify_path, "w", encoding="utf-8") as f:
                f.write("""import sys, os
# Parse --report arg
report_idx = sys.argv.index("--report") + 1
with open(sys.argv[report_idx], "r") as f:
    text = f.read()

# Let's fail if '13' or '23' appears (caught), but pass on others
if "13" in text or "23" in text:
    sys.exit(1)
sys.exit(0)
""")

            res = run_fuzz_on_report(
                manifest_path=manifest_path,
                seed=42,
                sample_all=True,
                verify_script=mock_verify_path
            )

            # Exactly 2 tokens mutated to 13 and 23, so caught_count must be 2 out of 5
            self.assertEqual(res["total_tokens"], 5)
            self.assertEqual(res["caught_count"], 2)
            self.assertEqual(res["missed_count"], 3)
            self.assertEqual(res["catch_rate"], 40.0)

if __name__ == "__main__":
    unittest.main()
