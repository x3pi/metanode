#!/usr/bin/env python3
import sys
import re

def parse_inventory(file_path):
    try:
        with open(file_path, 'r') as f:
            content = f.read()
    except Exception as e:
        return f"Error reading inventory: {e}"
        
    hosts_block = re.search(r'hosts:\s*\n((?:\s+.+\n?)+)', content)
    if not hosts_block:
        return "No hosts block found"
        
    hosts_text = hosts_block.group(1)
    
    # Split by indent level of 8 spaces to get separate host blocks
    entries = re.split(r'\n(?=\s{8}\S)', hosts_text)
    node_map = {}
    is_synconly_map = {}
    for entry in entries:
        entry = entry.strip()
        if not entry:
            continue
        lines = entry.split('\n')
        host_key = lines[0].split(':')[0].strip()
        
        # default IP to the host key if it's a valid IP
        ip = host_key
        node_ids = []
        is_synconly = False
        for line in lines[1:]:
            line = line.strip()
            if line.startswith('ansible_host:'):
                ip = line.split(':', 1)[1].strip().strip('"').strip("'")
            elif line.startswith('node_ids:'):
                # parse [0, 1, etc]
                node_ids_match = re.search(r'\[(.*?)\]', line)
                if node_ids_match:
                    node_ids = [int(x.strip()) for x in node_ids_match.group(1).split(',') if x.strip()]
            elif line.startswith('is_synconly:'):
                val = line.split(':', 1)[1].strip().lower()
                if val == 'true' or val == 'yes':
                    is_synconly = True
        
        # clean up IP
        if not re.match(r'^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$', ip):
            ip_match = re.search(r'\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}', ip)
            if ip_match:
                ip = ip_match.group(0)
                
        for nid in node_ids:
            node_map[nid] = ip
            is_synconly_map[nid] = is_synconly
            
    return node_map, is_synconly_map

if __name__ == '__main__':
    if len(sys.argv) < 3:
        print("Usage: parse_inventory.py <inventory_file> <target_node>")
        sys.exit(1)
        
    inv_file = sys.argv[1]
    target = sys.argv[2]
    
    result = parse_inventory(inv_file)
    if isinstance(result, str):
        print(result)
        sys.exit(1)
        
    node_map, is_synconly_map = result
        
    if target == 'json':
        import json
        out = {"nodes": {}, "rpc_proxies": {}, "tcp_nodes": {}}
        for nid, ip in node_map.items():
            key = f"m{nid}"
            out["nodes"][key] = f"http://{ip}:{10746 + nid}"
            out["rpc_proxies"][key] = f"http://{ip}:{8650 + nid}"
            out["tcp_nodes"][key] = f"{ip}:{6200 + nid}"
        print(json.dumps(out, indent=2))
        sys.exit(0)
        
    if target == 'roles':
        # output roles summary
        out = []
        for nid in sorted(node_map.keys()):
            ip = node_map[nid]
            role = "SyncOnly" if is_synconly_map.get(nid, False) else "Validator"
            
            # check if RPC Node is enabled by looking at the execution.json if available
            is_rpc = False
            exec_json_path = f"/opt/metanode/node-{nid}/config/execution.json"
            try:
                import os, json
                if os.path.exists(exec_json_path):
                    with open(exec_json_path, 'r') as jf:
                        cfg = json.load(jf)
                        if cfg.get("is_rpc_node", False):
                            is_rpc = True
            except Exception:
                pass
                
            rpc_status = "[RPC Enabled]" if is_rpc else ""
            out.append(f"   - Node {nid} ({ip}): {role} {rpc_status}")
            
        print("\n".join(out))
        sys.exit(0)

    if target == 'all':
        # format: Node 0 (IP), Node 1 (IP)... sorted by node_id
        sorted_nodes = sorted(node_map.keys())
        out = []
        for nid in sorted_nodes:
            out.append(f"Node {nid} ({node_map[nid]})")
        print(", ".join(out))
    else:
        try:
            nid = int(target)
            if nid in node_map:
                print(f"Node {nid} ({node_map[nid]})")
            else:
                print(f"Node {target} (Not found in inventory)")
        except ValueError:
            print(f"Invalid target node format: {target}")
