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
    for entry in entries:
        entry = entry.strip()
        if not entry:
            continue
        lines = entry.split('\n')
        host_key = lines[0].split(':')[0].strip()
        
        # default IP to the host key if it's a valid IP
        ip = host_key
        node_ids = []
        for line in lines[1:]:
            line = line.strip()
            if line.startswith('ansible_host:'):
                ip = line.split(':', 1)[1].strip().strip('"').strip("'")
            elif line.startswith('node_ids:'):
                # parse [0, 1, etc]
                node_ids_match = re.search(r'\[(.*?)\]', line)
                if node_ids_match:
                    node_ids = [int(x.strip()) for x in node_ids_match.group(1).split(',') if x.strip()]
        
        # clean up IP
        if not re.match(r'^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$', ip):
            ip_match = re.search(r'\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}', ip)
            if ip_match:
                ip = ip_match.group(0)
                
        for nid in node_ids:
            node_map[nid] = ip
            
    return node_map

if __name__ == '__main__':
    if len(sys.argv) < 3:
        print("Usage: parse_inventory.py <inventory_file> <target_node>")
        sys.exit(1)
        
    inv_file = sys.argv[1]
    target = sys.argv[2]
    
    node_map = parse_inventory(inv_file)
    if isinstance(node_map, str):
        print(node_map)
        sys.exit(1)
        
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
