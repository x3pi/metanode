#!/usr/bin/env python3
"""
Unit tests for verify_evidence.py proving it catches:
1. Missing raw evidence file
2. Tampered file (SHA-256 / byte mismatch)
3. Forbidden claims without nearby evidence tag
4. Non-existent evidence ID references
5. Valid manifest and report passes cleanly
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
        self.raw_log_name = "test_run.log"
        self.raw_log_path = os.path.join(self.evidence_dir, self.raw_log_name)
        self.raw_content = b"Benchmark finished: 5000 tx/s, confirmed 5000\n"
        with open(self.raw_log_path, "wb") as f:
            f.write(self.raw_content)

        self.sha256 = hashlib.sha256(self.raw_content).hexdigest()
        self.bytes_count = len(self.raw_content)

        self.manifest_path = os.path.join(self.evidence_dir, "MANIFEST.json")
        self.manifest_data = {
            "manifest_version": "1.0",
            "entries": [
                {
                    "id": "run_01",
                    "command": "test_cmd",
                    "cwd": "/tmp",
                    "git_commit": "c081e31a",
                    "started_at": "2026-10-07T10:00:00Z",
                    "finished_at": "2026-10-07T10:01:00Z",
                    "exit_code": 0,
                    "files": [
                        {
                            "path": self.raw_log_name,
                            "sha256": self.sha256,
                            "bytes": self.bytes_count
                        }
                    ]
                }
            ]
        }
        with open(self.manifest_path, "w", encoding="utf-8") as f:
            json.dump(self.manifest_data, f, indent=2)

        self.report_path = os.path.join(self.evidence_dir, "test_report.md")
        self.valid_report = """# Test Report
| Run | TPS | Status |
| --- | --- | --- |
| 1 | 5000 | PASS (evidence:run_01) |
"""
        with open(self.report_path, "w", encoding="utf-8") as f:
            f.write(self.valid_report)

    def tearDown(self):
        self.test_dir.cleanup()

    def run_verifier(self, evidence_dir=None, report_path=None):
        edir = evidence_dir or self.evidence_dir
        cmd = [sys.executable, "-I", VERIFY_SCRIPT, edir]
        if report_path:
            cmd.extend(["--report", report_path])
        return subprocess.run(cmd, capture_output=True, text=True)

    def test_valid_manifest_and_report_passes(self):
        res = self.run_verifier(report_path=self.report_path)
        self.assertEqual(res.returncode, 0, f"Expected 0, got {res.returncode}. Output:\n{res.stdout}\n{res.stderr}")
        self.assertIn("ALL EVIDENCE VERIFICATIONS PASSED", res.stdout)

    def test_catches_missing_raw_file(self):
        os.remove(self.raw_log_path)
        res = self.run_verifier(report_path=self.report_path)
        self.assertNotEqual(res.returncode, 0, "Verifier failed to detect missing raw file!")
        self.assertIn("does not exist on disk", res.stdout)

    def test_catches_tampered_raw_file_sha_mismatch(self):
        with open(self.raw_log_path, "ab") as f:
            f.write(b"tampered byte\n")
        res = self.run_verifier(report_path=self.report_path)
        self.assertNotEqual(res.returncode, 0, "Verifier failed to detect modified file content!")
        self.assertIn("sha256 mismatch", res.stdout)

    def test_catches_forbidden_claim_without_evidence(self):
        bad_report_path = os.path.join(self.evidence_dir, "bad_report.md")
        with open(bad_report_path, "w", encoding="utf-8") as f:
            f.write("""# Bad Report
Hệ thống hoàn toàn không có lỗi và đạt 100% độ tin cậy tuyệt đối.
| Run | TPS |
| 1 | 5000 |
""")
        res = self.run_verifier(report_path=bad_report_path)
        self.assertNotEqual(res.returncode, 0, "Verifier failed to detect forbidden claims without evidence!")
        self.assertIn("Forbidden claim", res.stdout)

    def test_catches_non_existent_evidence_reference(self):
        bad_report_path = os.path.join(self.evidence_dir, "bad_ref.md")
        with open(bad_report_path, "w", encoding="utf-8") as f:
            f.write("""# Bad Reference Report
| Run | TPS | Status |
| --- | --- | --- |
| 1 | 5000 | PASS (evidence:ghost_run_999) |
""")
        res = self.run_verifier(report_path=bad_report_path)
        self.assertNotEqual(res.returncode, 0, "Verifier failed to detect non-existent evidence ID!")
        self.assertIn("Report references evidence ID 'ghost_run_999' which is NOT in MANIFEST.json", res.stdout)

if __name__ == "__main__":
    unittest.main()
