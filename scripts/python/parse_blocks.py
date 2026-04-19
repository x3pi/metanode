import sys, os
from datetime import datetime

for i in range(5):
    lines = []
    for ep in [0, 1]:
        filepath = f"/home/abc/chain-n/mtn-consensus/metanode/logs/node_{i}/go-master/epoch_{ep}/Commit.log"
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
        print(f"Node {i}: No data")
        continue
    
    # sort by time
    lines.sort()
    # take last 100 blocks
    recent = lines[-100:] if len(lines) >= 100 else lines
    if len(recent) < 2:
        print(f"Node {i}: Not enough data")
        continue
    
    diffs = [(recent[j] - recent[j-1]).total_seconds() for j in range(1, len(recent))]
    avg = sum(diffs) / len(diffs)
    max_d = max(diffs)
    print(f"Node {i}: Avg: {avg:.2f}s, Max: {max_d:.2f}s, Sample Size: {len(diffs)}")
