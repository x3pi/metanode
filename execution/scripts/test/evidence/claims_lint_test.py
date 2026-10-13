import os, sys, unittest
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from claims_lint import lint_text

OK_DOC = "# Báo cáo\n[ĐO] số liệu evidence:run1\n## Giới hạn\nx\n## Chưa làm\ny\n"

class T(unittest.TestCase):
    def test_clean_report_passes(self):
        e, w = lint_text(OK_DOC)
        self.assertEqual((e, w), ([], []))
    def test_absolute_without_evidence_is_error(self):
        e, _ = lint_text(OK_DOC + "Kết quả hoàn toàn đúng.\n")
        self.assertTrue(any("hoàn toàn" in x for x in e))
    def test_absolute_with_evidence_nearby_is_ok(self):
        e, _ = lint_text(OK_DOC + "Kết quả 100% khớp (evidence:run1).\n")
        self.assertEqual(e, [])
    def test_absolute_in_code_block_ignored(self):
        e, _ = lint_text(OK_DOC + "```\nhoàn toàn\n```\n")
        self.assertEqual(e, [])
    def test_missing_sections_are_errors(self):
        e, _ = lint_text("# t\n[ĐO] x\n")
        self.assertTrue(any("Giới hạn" in x for x in e) and any("Chưa làm" in x for x in e))
    def test_comparative_percentage_without_tag_is_warning(self):
        _, w = lint_text(OK_DOC + "Nhanh hơn 25% so với bản cũ.\n")
        self.assertTrue(any("comparative" in x for x in w))
    def test_missing_labels_is_warning(self):
        _, w = lint_text("# t\n## Giới hạn\n## Chưa làm\n")
        self.assertTrue(any("labels" in x for x in w))
    # negative control: the linter must NOT pass a report that says 'airtight' with no evidence
    def test_negative_control_airtight(self):
        e, _ = lint_text(OK_DOC + "Công cụ airtight.\n")
        self.assertTrue(e)

if __name__ == "__main__":
    unittest.main()
