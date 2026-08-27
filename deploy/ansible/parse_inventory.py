#!/usr/bin/env python3
import sys
import re
import subprocess
import json

def get_local_ips():
    ips = {'127.0.0.1', 'localhost', '::1'}
    try:
        out = subprocess.check_output(['hostname', '-I'], text=True).strip()
        for ip in out.split():
            ips.add(ip.strip())
    except Exception:
        pass
    return ips

def parse_inventory(file_path):
    try:
        with open(file_path, 'r') as f:
            content = f.read()
    except Exception as e:
        return f"Error reading inventory: {e}"

    local_ips = get_local_ips()
    
    # Try parsing via PyYAML if available
    try:
        import yaml
        data = yaml.safe_load(content)
        hosts = {}
        if isinstance(data, dict):
            # Check all.children.metanode_cluster.hosts or all.hosts or top-level hosts
            hosts = (data.get('all', {}).get('children', {}).get('metanode_cluster', {}).get('hosts', {})
                     or data.get('all', {}).get('hosts', {})
                     or data.get('hosts', {}))
        global_vars = (data.get('all', {}).get('vars', {}) 
                       or data.get('all', {}).get('children', {}).get('metanode_cluster', {}).get('vars', {}) 
                       or {})
        global_conn = str(global_vars.get('ansible_connection', '')).strip().lower()

        if hosts:
            node_map = {}
            is_synconly_map = {}
            is_rpc_map = {}
            user_map = {}
            pass_map = {}
            host_owner_map = {}

            for host_key, hvars in hosts.items():
                if not isinstance(hvars, dict):
                    hvars = {}
                ip = str(hvars.get('ansible_host', host_key)).strip()
                ansible_conn = str(hvars.get('ansible_connection', global_conn)).strip().lower()
                node_ids = hvars.get('node_ids', [])
                synconly_nodes = hvars.get('synconly_nodes', [])
                rpc_nodes = hvars.get('rpc_nodes', [])
                ansible_user = hvars.get('ansible_user', global_vars.get('ansible_user', 'your_user'))
                ansible_ssh_pass = hvars.get('ansible_ssh_pass', global_vars.get('ansible_ssh_pass', 'your_password'))

                # VALIDATION 1: Check if ansible_connection: local is on a non-local IP
                if ansible_conn == 'local' and ip not in local_ips:
                    print(f"\n\033[0;31m❌ [LỖI CẤU HÌNH INVENTORY] Host '{host_key}' ({ip}) bị gán 'ansible_connection: local' nhưng KHÔNG PHẢI máy local!\033[0m", file=sys.stderr)
                    if global_conn == 'local' and 'ansible_connection' not in hvars:
                        print(f"\033[0;33m   ⚠️ NGUYÊN NHÂN: Bạn đang để 'ansible_connection: local' trong phần global (all.vars), khiến tất cả các host (kể cả máy remote {ip}) đều bị chạy trên máy local hiện tại!\033[0m", file=sys.stderr)
                        print(f"\033[0;36m   👉 KHẮC PHỤC: Hãy XÓA 'ansible_connection: local' khỏi vars chung. Chỉ đặt 'ansible_connection: local' cho riêng từng host là máy local ({', '.join(sorted(local_ips))}).\033[0m\n", file=sys.stderr)
                    else:
                        print(f"\033[0;36m   👉 KHẮC PHỤC: Hãy XÓA dòng 'ansible_connection: local' trong host '{host_key}' để Ansible kết nối qua SSH tới {ip}.\033[0m\n", file=sys.stderr)
                    sys.exit(1)

                for nid in node_ids:
                    # VALIDATION 2: Check duplicate node_ids across multiple hosts
                    if nid in node_map:
                        prev_host = host_owner_map.get(nid, 'unknown')
                        prev_ip = node_map[nid]
                        print(f"\n\033[0;31m❌ [LỖI CẤU HÌNH INVENTORY] Trùng lặp node_id [{nid}]!\033[0m", file=sys.stderr)
                        print(f"\033[0;33m   - Node {nid} được khai báo ở cả host '{host_key}' ({ip}) và host '{prev_host}' ({prev_ip}).\033[0m", file=sys.stderr)
                        print(f"\033[0;36m   👉 KHẮC PHỤC: Mỗi node_id chỉ được gán cho duy nhất 1 host trong inventory.yml.\033[0m\n", file=sys.stderr)
                        sys.exit(1)

                    node_map[nid] = ip
                    is_synconly_map[nid] = (nid in synconly_nodes)
                    is_rpc_map[nid] = (nid in rpc_nodes)
                    user_map[nid] = ansible_user
                    pass_map[nid] = ansible_ssh_pass
                    host_owner_map[nid] = host_key

            return node_map, is_synconly_map, is_rpc_map, user_map, pass_map
    except ImportError:
        pass

    # Fallback to regex parser
    hosts_block = re.search(r'hosts:\s*\n((?:\s+.+\n?)+)', content)
    if not hosts_block:
        return "No hosts block found"
        
    hosts_text = hosts_block.group(1)
    entries = re.split(r'\n(?=\s{8}\S)', hosts_text)
    node_map = {}
    is_synconly_map = {}
    is_rpc_map = {}
    user_map = {}
    pass_map = {}
    host_owner_map = {}

    for entry in entries:
        entry = entry.strip()
        if not entry:
            continue
        lines = entry.split('\n')
        host_key = lines[0].split(':')[0].strip()
        ip = host_key
        ansible_conn = ""
        node_ids = []
        synconly_nodes = []
        rpc_nodes = []
        ansible_user = "your_user"
        ansible_ssh_pass = "your_password"

        for line in lines[1:]:
            line = line.strip()
            if line.startswith('ansible_host:'):
                ip = line.split(':', 1)[1].strip().strip('"').strip("'")
            elif line.startswith('ansible_connection:'):
                ansible_conn = line.split(':', 1)[1].strip().strip('"').strip("'").lower()
            elif line.startswith('node_ids:'):
                node_ids_match = re.search(r'\[(.*?)\]', line)
                if node_ids_match:
                    node_ids = [int(x.strip()) for x in node_ids_match.group(1).split(',') if x.strip()]
            elif line.startswith('synconly_nodes:'):
                match = re.search(r'\[(.*?)\]', line)
                if match:
                    synconly_nodes = [int(x.strip()) for x in match.group(1).split(',') if x.strip()]
            elif line.startswith('rpc_nodes:'):
                match = re.search(r'\[(.*?)\]', line)
                if match:
                    rpc_nodes = [int(x.strip()) for x in match.group(1).split(',') if x.strip()]
            elif line.startswith('ansible_user:'):
                ansible_user = line.split(':', 1)[1].strip().strip('"').strip("'")
            elif line.startswith('ansible_ssh_pass:'):
                ansible_ssh_pass = line.split(':', 1)[1].strip().strip('"').strip("'")
        
        if not re.match(r'^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$', ip):
            ip_match = re.search(r'\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}', ip)
            if ip_match:
                ip = ip_match.group(0)

        # VALIDATION 1
        if ansible_conn == 'local' and ip not in local_ips:
            print(f"\n\033[0;31m❌ [LỖI CẤU HÌNH INVENTORY] Host '{host_key}' cấu hình sai 'ansible_connection: local'!\033[0m", file=sys.stderr)
            print(f"\033[0;33m   - ansible_host: {ip}\033[0m", file=sys.stderr)
            print(f"\033[0;33m   - Các IP máy local hợp lệ: {', '.join(sorted(local_ips))}\033[0m", file=sys.stderr)
            print(f"\033[0;31m   ⚠️ NGUY HIỂM: IP '{ip}' KHÔNG PHẢI là máy local này. Nếu để 'ansible_connection: local', Ansible sẽ deploy đè lên máy này thay vì SSH sang {ip}!\033[0m", file=sys.stderr)
            print(f"\033[0;36m   👉 KHẮC PHỤC: Hãy XÓA dòng 'ansible_connection: local' trong host '{host_key}' của inventory.yml để Ansible kết nối qua SSH tới {ip}.\033[0m\n", file=sys.stderr)
            sys.exit(1)

        for nid in node_ids:
            # VALIDATION 2
            if nid in node_map:
                prev_host = host_owner_map.get(nid, 'unknown')
                prev_ip = node_map[nid]
                print(f"\n\033[0;31m❌ [LỖI CẤU HÌNH INVENTORY] Trùng lặp node_id [{nid}]!\033[0m", file=sys.stderr)
                print(f"\033[0;33m   - Node {nid} được khai báo ở cả host '{host_key}' ({ip}) và host '{prev_host}' ({prev_ip}).\033[0m", file=sys.stderr)
                print(f"\033[0;36m   👉 KHẮC PHỤC: Mỗi node_id chỉ được gán cho duy nhất 1 host trong inventory.yml.\033[0m\n", file=sys.stderr)
                sys.exit(1)

            node_map[nid] = ip
            is_synconly_map[nid] = (nid in synconly_nodes)
            is_rpc_map[nid] = (nid in rpc_nodes)
            user_map[nid] = ansible_user
            pass_map[nid] = ansible_ssh_pass
            host_owner_map[nid] = host_key
            
    return node_map, is_synconly_map, is_rpc_map, user_map, pass_map

if __name__ == '__main__':
    if len(sys.argv) < 3:
        print("Usage: parse_inventory.py <inventory_file> <target_node>")
        sys.exit(1)
        
    inv_file = sys.argv[1]
    target = sys.argv[2]
    
    result = parse_inventory(inv_file)
    if isinstance(result, str):
        print(result, file=sys.stderr)
        sys.exit(1)
        
    node_map, is_synconly_map, is_rpc_map, user_map, pass_map = result
        
    if target == 'json':
        out = {
            "nodes": {},
            "roles": {},
            "tcp_nodes": {}
        }
        for nid, ip in node_map.items():
            key = f"m{nid}"
            url = f"http://{ip}:{10746 + nid}"
            tcp = f"{ip}:{6200 + nid}"
            is_sync = is_synconly_map.get(nid, False)
            out["nodes"][key] = url
            out["roles"][key] = "synconly" if is_sync else "validator"
            out["tcp_nodes"][key] = tcp
        print(json.dumps(out, indent=2))
        sys.exit(0)
        
    if target == 'auth':
        out = {"users": {}, "passes": {}}
        for nid, ip in node_map.items():
            key = f"m{nid}"
            out["users"][key] = user_map.get(nid, "your_user")
            out["passes"][key] = pass_map.get(nid, "your_password")
        print(json.dumps(out))
        sys.exit(0)
        
    if target == 'roles':
        out = []
        for nid in sorted(node_map.keys()):
            ip = node_map[nid]
            role = "SyncOnly" if is_synconly_map.get(nid, False) else "Validator"
            rpc_status = "[RPC Enabled]" if is_rpc_map.get(nid, False) else ""
            out.append(f"   - Node {nid} ({ip}): {role} {rpc_status}")
            
        print("\n".join(out))
        sys.exit(0)

    if target == 'all':
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
