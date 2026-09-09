#!/usr/bin/env python3
"""Unit tests for check_snapshot_node.py's node classification logic.

Regression coverage for the PR #115 finding: the restore-node safety check in
ansible_deploy.sh only recognized snapshot/synconly nodes declared via the
snapshot_nodes/synconly_nodes list keys, missing the per-host boolean
snapshot_enabled/is_synconly style that deploy.yml's own node classification
already treats as equivalent -- silently letting a snapshot node restore its
own snapshot when an inventory used that style.

Run with: python3 -m pytest deploy/ansible/test_check_snapshot_node.py -v
      or: python3 deploy/ansible/test_check_snapshot_node.py
"""
import os
import subprocess
import sys
import tempfile
import textwrap
import unittest

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "check_snapshot_node.py")


def run_check(inventory_yaml_text, node_id):
    """Writes inventory_yaml_text to a temp file, runs check_snapshot_node.py against it,
    and returns (stdout_stripped, stderr_stripped, returncode)."""
    with tempfile.NamedTemporaryFile("w", suffix=".yml", delete=False) as f:
        f.write(textwrap.dedent(inventory_yaml_text))
        path = f.name
    try:
        proc = subprocess.run(
            [sys.executable, SCRIPT, path, str(node_id)],
            capture_output=True,
            text=True,
            timeout=10,
        )
        return proc.stdout.strip(), proc.stderr.strip(), proc.returncode
    finally:
        os.unlink(path)


BASE_INVENTORY = """\
    all:
      children:
        metanode_cluster:
          hosts:
            server_1:
              node_ids: [0, 3]
              {server_1_extra}
            server_2:
              node_ids: [1, 2]
              {server_2_extra}
"""


class TestExplicitListStyle(unittest.TestCase):
    def test_node_in_snapshot_nodes_list_is_true(self):
        inv = BASE_INVENTORY.format(
            server_1_extra="snapshot_nodes: [3]",
            server_2_extra="",
        )
        out, err, rc = run_check(inv, 3)
        self.assertEqual(rc, 0, err)
        self.assertEqual(out, "true")

    def test_node_in_synconly_nodes_list_is_true(self):
        inv = BASE_INVENTORY.format(
            server_1_extra="",
            server_2_extra="synconly_nodes: [1]",
        )
        out, err, rc = run_check(inv, 1)
        self.assertEqual(rc, 0, err)
        self.assertEqual(out, "true")

    def test_node_not_in_any_list_is_false(self):
        inv = BASE_INVENTORY.format(
            server_1_extra="snapshot_nodes: [3]",
            server_2_extra="",
        )
        out, err, rc = run_check(inv, 0)
        self.assertEqual(rc, 0, err)
        self.assertEqual(out, "false")


class TestBooleanPerHostStyle(unittest.TestCase):
    """Regression coverage for the actual PR #115 bug: a host-level boolean flag applies
    to ALL of that host's node_ids, matching deploy.yml's own classification."""

    def test_snapshot_enabled_true_applies_to_every_node_id_on_that_host(self):
        inv = BASE_INVENTORY.format(
            server_1_extra="snapshot_enabled: true",
            server_2_extra="",
        )
        for node_id, expected in [(0, "true"), (3, "true"), (1, "false"), (2, "false")]:
            with self.subTest(node_id=node_id):
                out, err, rc = run_check(inv, node_id)
                self.assertEqual(rc, 0, err)
                self.assertEqual(out, expected)

    def test_is_synconly_true_applies_to_every_node_id_on_that_host(self):
        inv = BASE_INVENTORY.format(
            server_1_extra="",
            server_2_extra="is_synconly: true",
        )
        for node_id, expected in [(1, "true"), (2, "true"), (0, "false"), (3, "false")]:
            with self.subTest(node_id=node_id):
                out, err, rc = run_check(inv, node_id)
                self.assertEqual(rc, 0, err)
                self.assertEqual(out, expected)

    def test_snapshot_enabled_false_does_not_mark_the_host(self):
        inv = BASE_INVENTORY.format(
            server_1_extra="snapshot_enabled: false",
            server_2_extra="",
        )
        out, err, rc = run_check(inv, 0)
        self.assertEqual(rc, 0, err)
        self.assertEqual(out, "false")


class TestMixedAndEdgeCases(unittest.TestCase):
    def test_list_and_boolean_styles_can_coexist_across_hosts(self):
        inv = BASE_INVENTORY.format(
            server_1_extra="snapshot_enabled: true",
            server_2_extra="synconly_nodes: [2]",
        )
        for node_id, expected in [(0, "true"), (3, "true"), (2, "true"), (1, "false")]:
            with self.subTest(node_id=node_id):
                out, err, rc = run_check(inv, node_id)
                self.assertEqual(rc, 0, err)
                self.assertEqual(out, expected)

    def test_node_id_not_present_anywhere_is_false(self):
        inv = BASE_INVENTORY.format(server_1_extra="", server_2_extra="")
        out, err, rc = run_check(inv, 99)
        self.assertEqual(rc, 0, err)
        self.assertEqual(out, "false")

    def test_non_dict_host_entry_is_skipped_without_crashing(self):
        inv = """\
        all:
          children:
            metanode_cluster:
              hosts:
                server_1: null
        """
        out, err, rc = run_check(inv, 0)
        self.assertEqual(rc, 0, err)
        self.assertEqual(out, "false")


class TestFailClosed(unittest.TestCase):
    """The safety check must abort (non-zero exit) rather than silently report "false"
    when it cannot actually verify the answer -- a data-loss-prevention guard must never
    fail open."""

    def test_malformed_yaml_fails_closed(self):
        with tempfile.NamedTemporaryFile("w", suffix=".yml", delete=False) as f:
            f.write("all: [this is not: valid: yaml: at: all:")
            path = f.name
        try:
            proc = subprocess.run(
                [sys.executable, SCRIPT, path, "0"],
                capture_output=True,
                text=True,
                timeout=10,
            )
            self.assertNotEqual(proc.returncode, 0, "malformed YAML must not exit 0")
            self.assertIn("ERROR", proc.stderr)
        finally:
            os.unlink(path)

    def test_missing_inventory_file_fails_closed(self):
        proc = subprocess.run(
            [sys.executable, SCRIPT, "/nonexistent/path/inventory.yml", "0"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        self.assertNotEqual(proc.returncode, 0, "missing file must not exit 0")
        self.assertIn("ERROR", proc.stderr)

    def test_missing_top_level_structure_is_handled_gracefully(self):
        out, err, rc = run_check("just_a_string: true\n", 0)
        self.assertEqual(rc, 0, err)
        self.assertEqual(out, "false")


if __name__ == "__main__":
    unittest.main()
