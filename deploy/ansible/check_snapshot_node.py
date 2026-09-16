#!/usr/bin/env python3
"""check_snapshot_node.py — is a given node id a snapshot/synconly node?

Extracted out of ansible_deploy.sh's inline restore-node safety check so the
classification logic can be unit-tested in isolation (see
test_check_snapshot_node.py) instead of only being exercised end-to-end
through a full inventory + shell run.

Must match deploy.yml's own node classification EXACTLY (see the jinja set_fact
around "is_sync"/"is_snapshot" there): a node id counts as a snapshot/synconly
node if (a) it appears in the explicit snapshot_nodes/synconly_nodes list of
ANY host, OR (b) it belongs to a host that declares the boolean flag
snapshot_enabled/is_synconly (which applies to ALL of that host's node_ids).
Missing branch (b) is exactly the bug this script fixed: an inventory using
the boolean style silently let a snapshot node restore its own snapshot.

Usage:
    python3 check_snapshot_node.py <inventory.yml> <node_id>

Prints "true" or "false" to stdout and exits 0 on success.
On any error (missing PyYAML, unreadable/malformed inventory, ...), prints an
"ERROR: ..." message to stderr and exits 2 -- the caller (ansible_deploy.sh)
treats a non-zero exit as "could not verify" and aborts rather than silently
proceeding, since this is a data-loss-prevention guard and must fail closed,
never open.
"""
import sys


def is_snapshot_node(inventory_path, node_id):
    """Returns True/False, or raises on any error reading/parsing the inventory."""
    import yaml  # deferred import so the ImportError path below can format its own message

    with open(inventory_path) as f:
        d = yaml.safe_load(f)
    hosts = (d.get("all", {}).get("children", {}).get("metanode_cluster", {}).get("hosts", {})) or {}
    snap_nodes = set()
    for _host, v in hosts.items():
        if not isinstance(v, dict):
            continue
        node_ids = [str(x) for x in v.get("node_ids", [])]
        snap_nodes.update(str(x) for x in v.get("snapshot_nodes", []))
        snap_nodes.update(str(x) for x in v.get("synconly_nodes", []))
        # Per-host boolean flag applies to every node_id on that host (matches deploy.yml).
        if bool(v.get("snapshot_enabled", False)) or bool(v.get("is_synconly", False)):
            snap_nodes.update(node_ids)
    return str(node_id) in snap_nodes


def main(argv):
    if len(argv) != 3:
        print("Usage: check_snapshot_node.py <inventory.yml> <node_id>", file=sys.stderr)
        return 2

    inventory_path, node_id = argv[1], argv[2]

    try:
        import yaml  # noqa: F401  (import here too, so ImportError is caught below uniformly)
    except ImportError as e:
        print("ERROR: PyYAML chưa được cài đặt (%s)" % e, file=sys.stderr)
        return 2

    try:
        result = is_snapshot_node(inventory_path, node_id)
    except Exception as e:  # fail-closed: any error is a hard stop, never a silent "false"
        print("ERROR: %s" % e, file=sys.stderr)
        return 2

    print("true" if result else "false")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
