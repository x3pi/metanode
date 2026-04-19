import json
import glob
import os

files = glob.glob("config-*-node*.json")
for file in files:
    with open(file, 'r') as f:
        data = json.load(f)
    if "state_backend" not in data:
        data["state_backend"] = "nomt"
    data["nomt_commit_concurrency"] = 32
    data["nomt_page_cache_mb"] = 1024
    data["nomt_leaf_cache_mb"] = 1024
    with open(file, 'w') as f:
        json.dump(data, f, indent=4)
    print(f"Updated {file}")
