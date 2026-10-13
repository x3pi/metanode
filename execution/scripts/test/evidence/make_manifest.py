#!/usr/bin/env python3
"""Append (or create) an entry in <evidence_dir>/MANIFEST.json for files that already exist in that directory.
sha256/bytes, git commit and git status are computed here, never typed by hand.
Usage: make_manifest.py <evidence_dir> --report note/<report>.md --id ID --command "CMD" [--exit-code N]
                        [--started ISO] [--finished ISO] [--cwd DIR] file1 [file2 ...]
"""
import argparse, hashlib, json, os, subprocess, sys, time

def sha(p):
    h = hashlib.sha256(); n = 0
    with open(p, "rb") as f:
        for c in iter(lambda: f.read(65536), b""):
            h.update(c); n += len(c)
    return h.hexdigest(), n

def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True, cwd="/home/abc/chain-n/metanode").stdout.strip()

ap = argparse.ArgumentParser()
ap.add_argument("dir"); ap.add_argument("--report", required=True); ap.add_argument("--id", required=True)
ap.add_argument("--command", required=True); ap.add_argument("--exit-code", type=int, default=0)
ap.add_argument("--started", default=""); ap.add_argument("--finished", default="")
ap.add_argument("--cwd", default="/home/abc/chain-n/metanode"); ap.add_argument("files", nargs="+")
a = ap.parse_args()
mp = os.path.join(a.dir, "MANIFEST.json")
m = json.load(open(mp)) if os.path.isfile(mp) else {"manifest_version": "1.0", "entries": []}
m["report"] = a.report
m["entries"] = [e for e in m["entries"] if e["id"] != a.id]
files = []
for f in a.files:
    s, n = sha(os.path.join(a.dir, f)); files.append({"path": f, "sha256": s, "bytes": n})
m["entries"].append({"id": a.id, "command": a.command, "cwd": a.cwd, "git_commit": git("rev-parse", "HEAD"),
                     # Only CODE state matters for reproducibility: ignore note/ (the evidence and reports being produced).
                     "git_status": ";".join(l for l in git("status", "--porcelain").splitlines() if not l[3:].startswith("note/")),
                     "started_at": a.started, "finished_at": a.finished or time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                     "exit_code": a.exit_code, "files": files})
json.dump(m, open(mp, "w"), indent=2)
print(f"manifest {mp}: entry {a.id} with {len(files)} files")
