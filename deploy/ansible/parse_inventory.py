#!/usr/bin/env python3
import sys
import os
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
    default_user = os.environ.get('USER', 'abc')
    
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
            ssh_user_map = {}
            ssh_key_map = {}
            host_owner_map = {}

            for host_key, hvars in hosts.items():
                if not isinstance(hvars, dict):
                    hvars = {}
                ip = str(hvars.get('ansible_host', host_key)).strip()
                ansible_conn = str(hvars.get('ansible_connection', global_conn)).strip().lower()
                node_ids = hvars.get('node_ids', [])
                synconly_nodes = hvars.get('synconly_nodes', [])
                rpc_nodes = hvars.get('rpc_nodes', [])
                ansible_user = hvars.get('ansible_user', global_vars.get('ansible_user', default_user))
                ansible_key = hvars.get('ansible_ssh_private_key_file', global_vars.get('ansible_ssh_private_key_file', ''))

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
                    ssh_user_map[nid] = ansible_user
                    ssh_key_map[nid] = ansible_key
                    host_owner_map[nid] = host_key

            return node_map, is_synconly_map, is_rpc_map, ssh_user_map, ssh_key_map
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
    ssh_user_map = {}
    ssh_key_map = {}
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
        ansible_user = default_user
        ansible_key = ""

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
            elif line.startswith('ansible_ssh_private_key_file:'):
                ansible_key = line.split(':', 1)[1].strip().strip('"').strip("'")
        
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
            ssh_user_map[nid] = ansible_user
            ssh_key_map[nid] = ansible_key
            host_owner_map[nid] = host_key
            
    return node_map, is_synconly_map, is_rpc_map, ssh_user_map, ssh_key_map

def check_reachability(inv_file, target_node='all', timeout=2.0):
    """
    Checks TCP socket connectivity to target remote hosts on their SSH port.
    Returns True if all reachable (or no remote hosts), False if any host fails.
    """
    try:
        with open(inv_file, 'r') as f:
            content = f.read()
    except Exception as e:
        print(f"❌ [LỖI ĐỌC INVENTORY] Không thể đọc {inv_file}: {e}", file=sys.stderr)
        return False

    local_ips = get_local_ips()
    hosts_to_check = []  # list of (host_key, ip, port)

    try:
        import yaml
        data = yaml.safe_load(content)
        hosts = {}
        if isinstance(data, dict):
            hosts = (data.get('all', {}).get('children', {}).get('metanode_cluster', {}).get('hosts', {})
                     or data.get('all', {}).get('hosts', {})
                     or data.get('hosts', {}))
        global_vars = (data.get('all', {}).get('vars', {}) 
                       or data.get('all', {}).get('children', {}).get('metanode_cluster', {}).get('vars', {}) 
                       or {})
        global_conn = str(global_vars.get('ansible_connection', '')).strip().lower()
        global_port = int(global_vars.get('ansible_port', 22))

        if hosts:
            for host_key, hvars in hosts.items():
                if not isinstance(hvars, dict):
                    hvars = {}
                ip = str(hvars.get('ansible_host', host_key)).strip()
                ansible_conn = str(hvars.get('ansible_connection', global_conn)).strip().lower()
                ansible_port = int(hvars.get('ansible_port', global_port))
                node_ids = hvars.get('node_ids', [])

                # Filter by target_node
                if target_node != 'all':
                    try:
                        tnid = int(target_node)
                        if tnid not in node_ids:
                            continue
                    except ValueError:
                        pass

                # Skip local hosts
                if ansible_conn == 'local' or ip in local_ips:
                    continue

                hosts_to_check.append((host_key, ip, ansible_port))
    except Exception:
        # Fallback to regex parser
        hosts_block = re.search(r'hosts:\s*\n((?:\s+.+\n?)+)', content)
        if hosts_block:
            hosts_text = hosts_block.group(1)
            entries = re.split(r'\n(?=\s{8}\S)', hosts_text)
            for entry in entries:
                entry = entry.strip()
                if not entry:
                    continue
                lines = entry.split('\n')
                host_key = lines[0].split(':')[0].strip()
                ip = host_key
                ansible_conn = ""
                ansible_port = 22
                node_ids = []
                for line in lines[1:]:
                    line = line.strip()
                    if line.startswith('ansible_host:'):
                        ip = line.split(':', 1)[1].strip().strip('"').strip("'")
                    elif line.startswith('ansible_connection:'):
                        ansible_conn = line.split(':', 1)[1].strip().strip('"').strip("'").lower()
                    elif line.startswith('ansible_port:'):
                        try:
                            ansible_port = int(line.split(':', 1)[1].strip().strip('"').strip("'"))
                        except ValueError:
                            pass
                    elif line.startswith('node_ids:'):
                        node_ids_match = re.search(r'\[(.*?)\]', line)
                        if node_ids_match:
                            node_ids = [int(x.strip()) for x in node_ids_match.group(1).split(',') if x.strip()]

                if not re.match(r'^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$', ip):
                    ip_match = re.search(r'\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}', ip)
                    if ip_match:
                        ip = ip_match.group(0)

                if target_node != 'all':
                    try:
                        tnid = int(target_node)
                        if tnid not in node_ids:
                            continue
                    except ValueError:
                        pass

                if ansible_conn == 'local' or ip in local_ips:
                    continue

                hosts_to_check.append((host_key, ip, ansible_port))

    if not hosts_to_check:
        return True

    # Deduplicate hosts by (ip, port)
    seen = set()
    unique_hosts = []
    for h in hosts_to_check:
        key = (h[1], h[2])
        if key not in seen:
            seen.add(key)
            unique_hosts.append(h)

    import socket
    import concurrent.futures

    def test_conn(host_info):
        h_key, ip, port = host_info
        try:
            s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            s.settimeout(timeout)
            s.connect((ip, port))
            s.close()
            return host_info, True, "OK"
        except socket.timeout:
            return host_info, False, f"Connection timed out (>{timeout}s)"
        except ConnectionRefusedError:
            return host_info, False, "Connection refused"
        except OSError as e:
            return host_info, False, str(e)
        except Exception as e:
            return host_info, False, str(e)

    failed = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=min(len(unique_hosts), 10)) as executor:
        results = list(executor.map(test_conn, unique_hosts))

    for (h_key, ip, port), success, msg in results:
        if not success:
            failed.append((h_key, ip, port, msg))

    if failed:
        for h_key, ip, port, msg in failed:
            print(f"❌ [LỖI KẾT NỐI SERVER] Không thể kết nối tới máy chủ '{h_key}' ({ip}:{port}): {msg}!", file=sys.stderr)
            print(f"   ⚠️ Máy chủ bị timeout hoặc không phản hồi qua SSH port {port}.", file=sys.stderr)
            print(f"   👉 Vui lòng kiểm tra lại mạng, firewall hoặc bật máy chủ {ip}.", file=sys.stderr)
        return False

    return True

if __name__ == '__main__':
    if len(sys.argv) < 3:
        print("Usage: parse_inventory.py <inventory_file> <target_node|roles|json|check_reachability> [target_node]")
        sys.exit(1)
        
    inv_file = sys.argv[1]
    target = sys.argv[2]

    if target == 'check_reachability':
        target_node = sys.argv[3] if len(sys.argv) > 3 else 'all'
        ok = check_reachability(inv_file, target_node)
        if not ok:
            sys.exit(4)
        print("✅ Kết nối tới các máy chủ đích: OK")
        sys.exit(0)
    
    result = parse_inventory(inv_file)
    if isinstance(result, str):
        print(result, file=sys.stderr)
        sys.exit(1)
        
    node_map, is_synconly_map, is_rpc_map, ssh_user_map, ssh_key_map = result
        
    if target == 'json':
        out = {
            "nodes": {},
            "roles": {},
            "state_history_nodes": {},
            "rpc_nodes": {},
            "ws_nodes": {},
            "tcp_nodes": {},
            "ssh": {}
        }
        for nid, ip in node_map.items():
            key = f"m{nid}"
            url = f"http://{ip}:{10746 + nid}"
            ws_url = f"ws://{ip}:{10746 + nid}/ws"
            tcp = f"{ip}:{6200 + nid}"
            is_sync = is_synconly_map.get(nid, False)
            is_rpc = is_rpc_map.get(nid, False)
            out["nodes"][key] = url
            out["roles"][key] = "synconly" if is_sync else "validator"
            if is_rpc:
                out["state_history_nodes"][key] = url
                out["rpc_nodes"][key] = url
                out["ws_nodes"][key] = ws_url
            out["tcp_nodes"][key] = tcp
            out["ssh"][key] = {
                "user": ssh_user_map.get(nid, "abc"),
                "key": ssh_key_map.get(nid, "")
            }
        print(json.dumps(out, indent=2))
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
