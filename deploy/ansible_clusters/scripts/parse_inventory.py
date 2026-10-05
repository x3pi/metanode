#!/usr/bin/env python3
import sys
import os
import json
import fcntl
import tempfile

def parse_inventory(file_path):
    try:
        with open(file_path, 'r') as f:
            content = f.read()
    except Exception as e:
        print(f"Error reading inventory {file_path}: {e}", file=sys.stderr)
        sys.exit(1)

    try:
        import yaml
        def vault_constructor(loader, node):
            return loader.construct_scalar(node)
        yaml.SafeLoader.add_constructor('!vault', vault_constructor)
        data = yaml.safe_load(content)
    except Exception as e:
        print(f"Error parsing YAML from {file_path}: {e}", file=sys.stderr)
        sys.exit(1)

    if not isinstance(data, dict):
        print(f"Invalid YAML structure in {file_path}", file=sys.stderr)
        sys.exit(1)

    global_vars = data.get('all', {}).get('vars', {}) or {}
    p_rpc_port = global_vars.get('parent_chain_rpc_port', 8547)
    p_host = global_vars.get('parent_chain_host', '127.0.0.1')

    children = data.get('all', {}).get('children', {})
    p_nodes = (children.get('parent_chain_nodes') or {}).get('hosts') or {}

    # 1. Parent Chain Nodes (supports multi-validator committee)
    parent_nodes_map = {}
    p_primary = {}
    for h_key, h_val in sorted(p_nodes.items()):
        if not isinstance(h_val, dict):
            h_val = {}
        h_ip = h_val.get('ansible_host', p_host)
        h_port = h_val.get('parent_http_port', p_rpc_port)
        h_net_addr = h_val.get('parent_network_address', '')
        net_port = 4000
        if ':' in h_net_addr:
            try:
                net_port = int(h_net_addr.split(':')[-1])
            except Exception:
                pass
        h_p2p = h_val.get('p2p_port', h_val.get('parent_p2p_port', net_port if net_port != 4000 else global_vars.get('parent_chain_p2p_port', 4000)))
        p_node_info = {
            'name': h_key,
            'ip': h_ip,
            'rpc_port': h_port,
            'rpc_url': f"http://{h_ip}:{h_port}",
            'p2p_port': h_p2p,
            'node_id': h_val.get('parent_node_id', 0),
            'validator_address': h_val.get('parent_validator_address', '')
        }
        parent_nodes_map[h_key] = p_node_info
        if not p_primary:
            p_primary = p_node_info

    p_info = p_primary

    # 2. Exec Clusters
    exec_cluster_group = children.get('exec_clusters', {}) or {}
    exec_children = exec_cluster_group.get('children', {}) or {}
    exec_hosts = exec_cluster_group.get('hosts', {}) or {}

    if exec_hosts and not exec_children:
        grouped = {}
        for h_key, h_val in exec_hosts.items():
            if not isinstance(h_val, dict):
                continue
            cid = h_val.get('cluster_id', 1)
            cname = h_val.get('cluster_name', f"cluster_{cid}")
            cgroup = f"cluster_{cid}"
            if cgroup not in grouped:
                grouped[cgroup] = {
                    'vars': {'cluster_id': cid, 'cluster_name': cname},
                    'hosts': {}
                }
            grouped[cgroup]['hosts'][h_key] = h_val
        exec_clusters = grouped
    else:
        exec_clusters = exec_children
    clusters_data = {}
    all_nodes_rpc = {}
    all_ws_nodes = {}
    all_tcp_nodes = {}
    all_raft_nodes = {}
    all_fwd_nodes = {}

    for p_name, p_node in parent_nodes_map.items():
        all_nodes_rpc[p_name] = p_node['rpc_url']
        all_tcp_nodes[p_name] = f"{p_node['ip']}:{p_node['p2p_port']}"

    for c_key, c_val in sorted(exec_clusters.items()):
        if not isinstance(c_val, dict):
            continue
        c_vars = {**global_vars, **(exec_cluster_group.get('vars') or {}), **(c_val.get('vars') or {})}
        c_id = c_vars.get('cluster_id', 1)
        c_name = c_vars.get('cluster_name', c_key)
        c_chain_id = c_vars.get('chain_id', global_vars.get('chain_id', 991))
        c_hosts = c_val.get('hosts', {}) or {}

        c_replicas = {}
        lead_rpc = ''
        lead_addr = ''
        lead_bls_pub = ''
        idx = 0
        for r_key, r_val in sorted(c_hosts.items()):
            if not isinstance(r_val, dict):
                r_val = {}
            # Match inventory inheritance: host values override group defaults.
            r_val = {**c_vars, **r_val}
            r_ip = r_val.get('ansible_host', '127.0.0.1')
            r_rpc = r_val.get('rpc_port', 8545)
            r_p2p = r_val.get('p2p_port', 4200)
            r_raft = r_val.get('raft_port', 7110)
            r_fwd = r_val.get('forward_port', 7210)
            r_boot = r_val.get('raft_bootstrap', False)
            r_addr = r_val.get('address', '')
            r_bls_pub = r_val.get('bls_pubkey', '')

            rpc_url = f"http://{r_ip}:{r_rpc}"
            ws_url = f"ws://{r_ip}:{r_rpc}/ws"
            tcp_ep = f"{r_ip}:{r_p2p}"
            raft_ep = f"{r_ip}:{r_raft}"
            fwd_ep = f"{r_ip}:{r_fwd}"

            if not lead_rpc or r_boot:
                lead_rpc = rpc_url
                lead_addr = r_addr
                lead_bls_pub = r_bls_pub

            all_nodes_rpc[r_key] = rpc_url
            all_ws_nodes[r_key] = ws_url
            all_tcp_nodes[r_key] = tcp_ep
            all_raft_nodes[r_key] = raft_ep
            all_fwd_nodes[r_key] = fwd_ep

            c_replicas[r_key] = {
                'index': idx,
                'ip': r_ip,
                'rpc_port': r_rpc,
                'rpc_url': rpc_url,
                'ws_url': ws_url,
                'p2p_port': r_p2p,
                'raft_port': r_raft,
                'forward_port': r_fwd,
                'address': r_addr,
                'bls_pubkey': r_bls_pub,
                'is_bootstrap_leader': r_boot
            }
            idx += 1

        clusters_data[str(c_id)] = {
            'cluster_id': c_id,
            'cluster_name': c_name,
            'chain_id': c_chain_id,
            'primary_rpc': lead_rpc,
            'primary_address': lead_addr,
            'primary_bls_pubkey': lead_bls_pub,
            'replicas': c_replicas
        }

    return {
        'root_anchor': p_info.get('rpc_url', f"http://{p_host}:{p_rpc_port}"),
        'parent': p_info,
        'parent_nodes': parent_nodes_map,
        'nodes': all_nodes_rpc,
        'rpc_nodes': all_nodes_rpc,
        'ws_nodes': all_ws_nodes,
        'tcp_nodes': all_tcp_nodes,
        'raft_nodes': all_raft_nodes,
        'forward_nodes': all_fwd_nodes,
        'clusters': clusters_data
    }

def export_tmp_files(info, rpc_nodes_file=None):
    if not rpc_nodes_file:
        rpc_nodes_file = os.environ.get("RPC_NODES_JSON_PATH", "/tmp/rpc_nodes.json")
    legacy_private_chains_file = "/tmp/private_chains.json"
    p_rpc = info.get('parent', {}).get('rpc_url') or info.get('root_anchor', 'http://127.0.0.1:8547')
    clusters_info = info.get('clusters', {})

    private_chains_map = {}
    for cid, c in clusters_info.items():
        cid_str = str(cid)
        c_name = c.get('cluster_name', f'exec{cid}')
        alias_name = 'chain_a' if cid_str == '1' else ('chain_b' if cid_str == '2' else f'chain_{cid}')

        c_rpc = {}
        c_ws = {}
        c_tcp = {}
        for idx, (r_name, r) in enumerate(c.get('replicas', {}).items()):
            m_key = f"m{idx}"
            c_rpc[m_key] = r['rpc_url']
            c_ws[m_key] = r['ws_url']
            c_tcp[m_key] = f"{r['ip']}:{r['p2p_port']}"

        c_entry = {
            'cluster_id': int(cid),
            'cluster_name': c_name,
            'chain_id': c.get('chain_id', 991),
            'validators': len(c.get('replicas', {})),
            'rpc_url': c.get('primary_rpc', ''),
            'ws_url': f"{c.get('primary_rpc', '').replace('http', 'ws')}/ws",
            'address': c.get('primary_address', ''),
            'bls_pubkey': c.get('primary_bls_pubkey', ''),
            'rpc_nodes': c_rpc,
            'ws_nodes': c_ws,
            'tcp_nodes': c_tcp
        }

        private_chains_map[alias_name] = c_entry

    # 1. Write merged rpc_nodes_file
    try:
        def is_clean_key(k):
            return not k.startswith('parent_node_') and not k.startswith('exec')

        os.makedirs(os.path.dirname(os.path.abspath(rpc_nodes_file)), exist_ok=True)
        lock_file = f"{rpc_nodes_file}.lock"
        with open(lock_file, 'a', encoding='utf-8') as lock:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
            # Re-read only while holding the shared lock so no public-chain
            # exporter can be overwritten with stale data.
            existing = {}
            if os.path.isfile(rpc_nodes_file):
                try:
                    with open(rpc_nodes_file, 'r', encoding='utf-8') as f:
                        content = f.read().strip()
                        if content:
                            existing = json.loads(content)
                except Exception:
                    existing = {}
            out = dict(existing)
            for map_name in ('nodes', 'rpc_nodes', 'ws_nodes', 'tcp_nodes', 'raft_nodes', 'forward_nodes'):
                merged = {k: v for k, v in existing.get(map_name, {}).items() if is_clean_key(k)}
                merged.update(info.get(map_name, {}))
                out[map_name] = merged
            out.update({
                'root_anchor': p_rpc,
                'parent': info.get('parent', {}),
                'parent_nodes': info.get('parent_nodes', {}),
                # This is the single shared endpoint/configuration file (mode 0600).
                'private_chains': private_chains_map
            })
            tmp_file = None
            try:
                with tempfile.NamedTemporaryFile(mode='w', encoding='utf-8', dir='/tmp', delete=False) as f:
                    tmp_file = f.name
                    json.dump(out, f, indent=2)
                os.chmod(tmp_file, 0o600)
                os.replace(tmp_file, rpc_nodes_file)
            finally:
                if tmp_file and os.path.exists(tmp_file):
                    os.unlink(tmp_file)
        # The unified file replaces this retired compatibility artifact.
        if os.path.exists(legacy_private_chains_file):
            os.unlink(legacy_private_chains_file)

        # Export namespace-specific files for start_monitors.sh and block_hash_checker
        active_clusters = set()
        for cid, c_data in info.get('clusters', {}).items():
            c_name = c_data.get('cluster_name', f"exec{cid}")
            c_replicas = c_data.get('replicas', {})
            if c_replicas:
                active_clusters.add(c_name)
                c_file = f"/tmp/rpc_nodes.{c_name}.json"
                c_nodes = {r_k: r_v['rpc_url'] for r_k, r_v in c_replicas.items()}
                c_content = {
                    'nodes': c_nodes,
                    'rpc_nodes': c_nodes,
                    'ws_nodes': {r_k: r_v['ws_url'] for r_k, r_v in c_replicas.items()},
                    'tcp_nodes': {r_k: f"{r_v['ip']}:{r_v['p2p_port']}" for r_k, r_v in c_replicas.items()},
                    'raft_nodes': {r_k: f"{r_v['ip']}:{r_v['raft_port']}" for r_k, r_v in c_replicas.items()},
                    'chain_id': c_data.get('chain_id', 991),
                    'cluster_name': c_name
                }
                try:
                    with open(c_file, 'w', encoding='utf-8') as cf:
                        json.dump(c_content, cf, indent=2)
                    os.chmod(c_file, 0o600)
                except Exception:
                    pass

        # Tự động dọn dẹp các file rác exec*.json cũ không có trong inventory hiện tại
        import glob
        for old_cf in glob.glob('/tmp/rpc_nodes.exec*.json'):
            cf_name = os.path.basename(old_cf).replace('rpc_nodes.', '').replace('.json', '')
            if cf_name not in active_clusters:
                try:
                    os.unlink(old_cf)
                except Exception:
                    pass

        if info.get('parent_nodes'):
            p_file = "/tmp/rpc_nodes.parent.json"
            p_nodes = {p_k: p_v['rpc_url'] for p_k, p_v in info['parent_nodes'].items()}
            p_content = {
                'nodes': p_nodes,
                'rpc_nodes': p_nodes,
                'parent_nodes': info['parent_nodes']
            }
            try:
                with open(p_file, 'w', encoding='utf-8') as pf:
                    json.dump(p_content, pf, indent=2)
                os.chmod(p_file, 0o600)
            except Exception:
                pass
        else:
            p_file = "/tmp/rpc_nodes.parent.json"
            if os.path.exists(p_file):
                try:
                    os.unlink(p_file)
                except Exception:
                    pass
    except Exception as e:
        raise RuntimeError(f"Could not write {rpc_nodes_file}: {e}") from e

def print_summary(info):
    print("⚙️ RPC Endpoints (IP & Port):")
    for k, v in info['rpc_nodes'].items():
        print(f"  • {k}: {v}")

    if info['ws_nodes']:
        print("\n🔌 WebSocket Endpoints (WS URL):")
        for k, v in info['ws_nodes'].items():
            print(f"  • {k}: {v}")

    print("\n🌐 TCP Endpoints (P2P / Consensus):")
    for k, v in info['tcp_nodes'].items():
        print(f"  • {k}: {v}")

    if info['raft_nodes']:
        print("\n⛵ Raft Consensus & Forwarding Endpoints:")
        for k, v in info['raft_nodes'].items():
            fwd = info['forward_nodes'].get(k, '')
            fwd_str = f" (Forward: {fwd})" if fwd else ""
            print(f"  • {k}: {v}{fwd_str}")

def check_live_status(info):
    import urllib.request
    print("🔍 Live Service Health Status (Live RPC Check):")
    for name, rpc_url in info['rpc_nodes'].items():
        if 'parent' in name:
            try:
                req = urllib.request.Request(f"{rpc_url}/inbound", headers={'User-Agent': 'curl/7.68.0'})
                with urllib.request.urlopen(req, timeout=2) as resp:
                    print(f"  • {name} ({rpc_url}): ✅ ONLINE (HTTP RPC OK)")
            except Exception:
                print(f"  • {name} ({rpc_url}): ❌ OFFLINE (Unreachable)")
        else:
            try:
                data = json.dumps({"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}).encode('utf-8')
                req = urllib.request.Request(rpc_url, data=data, headers={'Content-Type': 'application/json'})
                with urllib.request.urlopen(req, timeout=2) as resp:
                    res = json.loads(resp.read().decode('utf-8'))
                    b_hex = res.get('result', '')
                    if b_hex:
                        b_num = int(b_hex, 16)
                        print(f"  • {name} ({rpc_url}): ✅ ONLINE (Block: {b_num} / {b_hex})")
                    else:
                        print(f"  • {name} ({rpc_url}): ⚠️ WARNING (Invalid payload)")
            except Exception:
                print(f"  • {name} ({rpc_url}): ❌ OFFLINE (Unreachable)")

if __name__ == '__main__':
    inv_file = sys.argv[1] if len(sys.argv) > 1 else 'inventory.yml'
    mode = sys.argv[2] if len(sys.argv) > 2 else 'summary'

    parsed = parse_inventory(inv_file)
    custom_target = sys.argv[3] if len(sys.argv) > 3 and not sys.argv[3].startswith('-') else None
    export_tmp_files(parsed, rpc_nodes_file=custom_target)

    if mode == 'json':
        print(json.dumps(parsed, indent=2))
    elif mode in ['check', '--check', 'status', '--status']:
        print_summary(parsed)
        print("")
        check_live_status(parsed)
    elif mode in ['summary', 'display', '--summary', '-s']:
        print_summary(parsed)
    elif mode in ['export', '--export']:
        pass
    else:
        print_summary(parsed)
