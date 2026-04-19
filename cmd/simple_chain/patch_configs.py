import json
import glob
import os

for filename in glob.glob("config-*.json"):
    with open(filename, "r") as f:
        data = json.load(f)
    
    # Enable snapshot for sub nodes and node0, disable for other masters? 
    # Let's check the current enabled status first but ensure method is hybrid if enabled.
    
    # Actually, in Simple Chain, usually only Node 0 (or all Sub nodes) are configured to take snapshots to save disk.
    # But if snapshot_enabled is true, we must add snapshot_method: hybrid
    
    if "snapshot_enabled" in data and data["snapshot_enabled"]:
        data["snapshot_method"] = "hybrid"
        
        # Determine node ID from filename
        node_id = "0"
        for i in range(5):
            if f"node{i}" in filename:
                node_id = str(i)
                break
                
        # The source dir should be data-write for Sub nodes to capture the fully executed state
        # Master nodes only need to snapshot their data if they are standalone, but usually we snapshot the Sub node
        # because the Sub node has the full Xapian index and executed smart contracts.
        
        if "sub" in filename.lower():
            data["snapshot_source_dir"] = f"./sample/node{node_id}/data-write"
            data["snapshot_server_port"] = 8700 + int(node_id)
        else:
            data["snapshot_source_dir"] = f"./sample/node{node_id}/data"
            # Offsets for master if they also snapshot, though normally only sub is needed.
            data["snapshot_server_port"] = 8600 + int(node_id)
            
    with open(filename, "w") as f:
        json.dump(data, f, indent=4)
        
print("Configs patched")
