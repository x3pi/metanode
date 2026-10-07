#!/usr/bin/env python3
"""
Unit tests for verify_evidence.py proving it catches:
1. Valid manifest and report passes cleanly
2. Missing raw evidence file
3. Tampered raw file (SHA-256 / byte mismatch)
4. Missing mandatory 'report' field in MANIFEST.json
5. Unregistered files in evidence directory
6. Data table row containing numbers without 'evidence:<id>' tag
7. Number mismatch for extracted value (Mutation Test: 5.495 -> 9.999 fails!)
8. Evidence directory total size exceeding --max-mb
9. Forbidden absolute claims without adjacent evidence tag
10. Dirty git_status failing under --strict mode
11. Mismatch between text round count and listed items (claims 7, lists 6)
12. External files registry validation
"""

import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
VERIFY_SCRIPT = os.path.join(SCRIPT_DIR, "verify_evidence.py")

class TestVerifyEvidence(unittest.TestCase):
    def setUp(self):
        self.test_dir = tempfile.TemporaryDirectory()
        self.evidence_dir = self.test_dir.name
        self.raw_log_name = "benchmark_run.log"
        self.raw_log_path = os.path.join(self.evidence_dir, self.raw_log_name)
        self.raw_content = b"Benchmark finished: Config A: 5.495 +- 0.649 ms, TPS: 5000\n"
        with open(self.raw_log_path, "wb") as f:
            f.write(self.raw_content)

        self.sha256 = hashlib.sha256(self.raw_content).hexdigest()
        self.bytes_count = len(self.raw_content)

        self.report_path = os.path.join(self.evidence_dir, "test_report.md")
        self.valid_report = """# Test Report
| Config | Elapsed (ms) | Status | Evidence |
| :--- | :---: | :---: | :---: |
| Config A | **5.495 ± 0.649** | PASS | evidence:run_01#B1_100_A_mean |
"""
        with open(self.report_path, "w", encoding="utf-8") as f:
            f.write(self.valid_report)

        self.manifest_path = os.path.join(self.evidence_dir, "MANIFEST.json")
        self.manifest_data = {
            "manifest_version": "1.0",
            "report": self.report_path,
            "entries": [
                {
                    "id": "run_01",
                    "command": "test_cmd",
                    "cwd": "/tmp",
                    "git_commit": "c081e31a",
                    "git_status": "",
                    "started_at": "2026-10-07T10:00:00Z",
                    "finished_at": "2026-10-07T10:01:00Z",
                    "exit_code": 0,
                    "files": [
                        {
                            "path": self.raw_log_name,
                            "sha256": self.sha256,
                            "bytes": self.bytes_count
                        }
                    ],
                    "extract": [
                        {
                            "name": "B1_100_A_mean",
                            "file": self.raw_log_name,
                            "regex": r"Config A: (\d+\.\d+)",
                            "group": 1,
                            "stat": "raw",
                            "unit": "ms"
                        }
                    ]
                }
            ]
        }
        with open(self.manifest_path, "w", encoding="utf-8") as f:
            json.dump(self.manifest_data, f, indent=2)

    def tearDown(self):
        self.test_dir.cleanup()

    def run_verifier(self, extra_args=None):
        cmd = [sys.executable, "-I", VERIFY_SCRIPT, self.evidence_dir]
        if extra_args:
            cmd.extend(extra_args)
        return subprocess.run(cmd, capture_output=True, text=True)

    def test_valid_manifest_and_report_passes(self):
        res = self.run_verifier()
        self.assertEqual(res.returncode, 0, f"Expected 0, got {res.returncode}. Output:\n{res.stdout}\n{res.stderr}")
        self.assertIn("ALL EVIDENCE VERIFICATIONS PASSED", res.stdout)

    def test_catches_missing_raw_file(self):
        os.remove(self.raw_log_path)
        res = self.run_verifier()
        self.assertNotEqual(res.returncode, 0)
        self.assertIn("does not exist on disk", res.stdout)

    def test_catches_tampered_raw_file_sha_mismatch(self):
        with open(self.raw_log_path, "ab") as f:
            f.write(b"tampered byte\n")
        res = self.run_verifier()
        self.assertNotEqual(res.returncode, 0)
        self.assertIn("sha256 mismatch", res.stdout)

    def test_catches_missing_report_in_manifest(self):
        del self.manifest_data["report"]
        with open(self.manifest_path, "w", encoding="utf-8") as f:
            json.dump(self.manifest_data, f, indent=2)
        res = self.run_verifier()
        self.assertNotEqual(res.returncode, 0)
        self.assertIn("lacks mandatory 'report' field", res.stdout)

    def test_catches_unregistered_file_in_dir(self):
        rogue_path = os.path.join(self.evidence_dir, "rogue_file.txt")
        with open(rogue_path, "w") as f:
            f.write("untracked rogue data")
        res = self.run_verifier()
        self.assertNotEqual(res.returncode, 0)
        self.assertIn("Unregistered file found on disk", res.stdout)

    def test_catches_table_row_without_evidence_tag(self):
        with open(self.report_path, "w", encoding="utf-8") as f:
            f.write("""# Test Report
| Config | Elapsed (ms) | Status |
| :--- | :---: | :---: |
| Config A | **5.495 ± 0.649** | PASS |
""")
        res = self.run_verifier()
        self.assertNotEqual(res.returncode, 0)
        self.assertIn("lacks mandatory 'evidence:<id>' tag", res.stdout)

    def test_catches_number_mismatch_in_extracted_value(self):
        # MUTATION TEST: Sửa 5.495 thành 9.999
        with open(self.report_path, "w", encoding="utf-8") as f:
            f.write("""# Test Report
| Config | Elapsed (ms) | Status | Evidence |
| :--- | :---: | :---: | :---: |
| Config A | **9.999 ± 0.649** | PASS | evidence:run_01#B1_100_A_mean |
""")
        res = self.run_verifier()
        self.assertNotEqual(res.returncode, 0, "Verifier MUST fail when number 5.495 is mutated to 9.999!")
        self.assertIn("Number mismatch for extract 'B1_100_A_mean'", res.stdout)

    def test_catches_evidence_size_exceeded(self):
        # Max 0.00001 MB (10 bytes limit)
        res = self.run_verifier(extra_args=["--max-mb", "0.00001"])
        self.assertNotEqual(res.returncode, 0)
        self.assertIn("exceeds allowed limit", res.stdout)

    def test_catches_forbidden_claim_without_evidence(self):
        with open(self.report_path, "w", encoding="utf-8") as f:
            f.write("""# Test Report
Hệ thống đạt 100% độ chính xác tuyệt đối.
| Config | Elapsed (ms) | Status | Evidence |
| :--- | :---: | :---: | :---: |
| Config A | **5.495 ± 0.649** | PASS | evidence:run_01#B1_100_A_mean |
""")
        res = self.run_verifier()
        self.assertNotEqual(res.returncode, 0)
        self.assertIn("Forbidden claim", res.stdout)

    def test_catches_strict_dirty_git_status(self):
        self.manifest_data["entries"][0]["git_status"] = " M some_file.go"
        with open(self.manifest_path, "w", encoding="utf-8") as f:
            json.dump(self.manifest_data, f, indent=2)
        # Without --strict, it only warns
        res = self.run_verifier()
        self.assertEqual(res.returncode, 0)
        self.assertIn("[MANIFEST WARNING]", res.stdout)

        # With --strict, it MUST fail
        res_strict = self.run_verifier(extra_args=["--strict"])
        self.assertNotEqual(res_strict.returncode, 0)
        self.assertIn("Dirty git status recorded in manifest under --strict mode", res_strict.stdout)

    def test_catches_mismatched_round_counts(self):
        with open(self.report_path, "w", encoding="utf-8") as f:
            f.write("""# Test Report
Trong thí nghiệm này, chúng tôi chạy 7 vòng giết leader (R3, R6, R11, R14, R17, R20).
| Config | Elapsed (ms) | Status | Evidence |
| :--- | :---: | :---: | :---: |
| Config A | **5.495 ± 0.649** | PASS | evidence:run_01#B1_100_A_mean |
""")
        res = self.run_verifier()
        self.assertNotEqual(res.returncode, 0)
        self.assertIn("Text claims 7 vòng but lists 6 items", res.stdout)

    def test_external_files_registry_passes(self):
        # Declare external file in manifest
        self.manifest_data["external"] = [
            {
                "name": "huge_heap_profile.pb.gz",
                "sha256": "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
                "bytes": 50000000,
                "stored_at": "/tmp/archive/huge_heap_profile.pb.gz",
                "reason": "Large raw profile stored externally to keep repository lightweight"
            }
        ]
        with open(self.manifest_path, "w", encoding="utf-8") as f:
            json.dump(self.manifest_data, f, indent=2)

        res = self.run_verifier()
        self.assertEqual(res.returncode, 0)

if __name__ == "__main__":
    unittest.main()
