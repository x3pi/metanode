import sys, os
from datetime import datetime

for i in range(5):
    lines = []
    for ep in [0, 1]:
        filepath = f"/home/abc/chain-n/metanode/consensus/metanode/logs/node_{i}/go-master/epoch_{ep}/Commit.log"
        if os.path.exists(filepath):
            with open(filepath, 'r') as f:
                for line in f:
                    if "Commit:" in line:
                        timestamp_str = line.split("Z:")[0]
                        try:
                            # Parse format: 2026-03-16T03:43:00
                            dt = datetime.fromisoformat(timestamp_str)
                            lines.append(dt)
                        except:
                            pass
    if not lines:
        continue
    
    # sort by time
    lines.sort()
    latest = lines[-1]
    print(f"Node {i} latest block timestamp: {latest.isoformat()}")
