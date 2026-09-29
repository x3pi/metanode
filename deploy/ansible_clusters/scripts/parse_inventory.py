#!/usr/bin/env python3
import sys
import os
import json

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
    p_nodes = children.get('parent_chain_nodes', {}).get('hosts', {})

    # 1. Parent Node
    p_info = {}
    for h_key, h_val in p_nodes.items():
        if not isinstance(h_val, dict):
            h_val = {}
        h_ip = h_val.get('ansible_host', p_host)
        h_port = h_val.get('parent_http_port', p_rpc_port)
        h_p2p = h_val.get('p2p_port', h_val.get('parent_p2p_port', global_vars.get('parent_chain_p2p_port', 4000)))
        p_info = {
            'name': h_key,
            'ip': h_ip,
            'rpc_port': h_port,
            'rpc_url': f"http://{h_ip}:{h_port}",
            'p2p_port': h_p2p
        }
        break

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

    if p_info:
        all_nodes_rpc[p_info['name']] = p_info['rpc_url']
        all_tcp_nodes[p_info['name']] = f"{p_info['ip']}:{p_info['p2p_port']}"

    for c_key, c_val in sorted(exec_clusters.items()):
        if not isinstance(c_val, dict):
            continue
        c_vars = c_val.get('vars', {}) or {}
        c_id = c_vars.get('cluster_id', 1)
        c_name = c_vars.get('cluster_name', c_key)
        c_chain_id = c_vars.get('chain_id', global_vars.get('chain_id', 991))
        c_hosts = c_val.get('hosts', {}) or {}

        c_replicas = {}
        lead_rpc = ''
        idx = 0
        for r_key, r_val in sorted(c_hosts.items()):
            if not isinstance(r_val, dict):
                r_val = {}
            r_ip = r_val.get('ansible_host', '127.0.0.1')
            r_rpc = r_val.get('rpc_port', 8545)
            r_p2p = r_val.get('p2p_port', 4200)
            r_raft = r_val.get('raft_port', 7110)
            r_fwd = r_val.get('forward_port', 7210)
            r_boot = r_val.get('raft_bootstrap', False)

            rpc_url = f"http://{r_ip}:{r_rpc}"
            ws_url = f"ws://{r_ip}:{r_rpc}/ws"
            tcp_ep = f"{r_ip}:{r_p2p}"
            raft_ep = f"{r_ip}:{r_raft}"
            fwd_ep = f"{r_ip}:{r_fwd}"

            if not lead_rpc or r_boot:
                lead_rpc = rpc_url

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
                'is_bootstrap_leader': r_boot
            }
            idx += 1

        clusters_data[str(c_id)] = {
            'cluster_id': c_id,
            'cluster_name': c_name,
            'chain_id': c_chain_id,
            'primary_rpc': lead_rpc,
            'replicas': c_replicas
        }

    return {
        'parent': p_info,
        'nodes': all_nodes_rpc,
        'rpc_nodes': all_nodes_rpc,
        'ws_nodes': all_ws_nodes,
        'tcp_nodes': all_tcp_nodes,
        'raft_nodes': all_raft_nodes,
        'forward_nodes': all_fwd_nodes,
        'clusters': clusters_data
    }

def export_tmp_files(info):
    # 1. Export /tmp/rpc_nodes.json (standard format compatible with metanode-suite)
    rpc_nodes_file = "/tmp/rpc_nodes.json"
    try:
        with open(rpc_nodes_file, 'w') as f:
            json.dump({
                'nodes': info['nodes'],
                'rpc_nodes': info['rpc_nodes'],
                'ws_nodes': info['ws_nodes'],
                'tcp_nodes': info['tcp_nodes'],
                'raft_nodes': info['raft_nodes'],
                'forward_nodes': info['forward_nodes']
            }, f, indent=2)
        os.chmod(rpc_nodes_file, 0o600)
    except Exception as e:
        print(f"Warning: could not write {rpc_nodes_file}: {e}", file=sys.stderr)

    # 2. Export /tmp/private_chains.json (for metanode-suite update-ip.sh mapping)
    priv_file = "/tmp/private_chains.json"
    try:
        p_rpc = info['parent'].get('rpc_url', 'http://127.0.0.1:8547')
        priv_out = {
            'root_anchor': p_rpc,
            'nodes': {},
            'tcp_nodes': {},
            'chain_nodes': {}
        }
        for cid, c in info['clusters'].items():
            chain_id_str = str(c['chain_id'])
            c_name = c.get('name', f'exec{cid}')
            cid_str = str(cid)

            c_rpc = {}
            c_ws = {}
            c_tcp = {}
            first_tcp = ''
            for idx, (r_name, r) in enumerate(c['replicas'].items()):
                m_key = f"m{idx}"
                c_rpc[m_key] = r['rpc_url']
                c_ws[m_key] = r['ws_url']
                c_tcp[m_key] = f"{r['ip']}:{r['p2p_port']}"
                if not first_tcp:
                    first_tcp = c_tcp[m_key]

            c_entry = {
                'cluster_id': cid,
                'cluster_name': c_name,
                'chain_id': c['chain_id'],
                'validators': len(c['replicas']),
                'rpc_url': c['primary_rpc'],
                'ws_url': f"{c['primary_rpc'].replace('http', 'ws')}/ws",
                'rpc_nodes': c_rpc,
                'ws_nodes': c_ws,
                'tcp_nodes': c_tcp
            }

            keys_to_set = [cid_str, f"cluster_{cid}", c_name]
            if chain_id_str not in priv_out['nodes']:
                keys_to_set.append(chain_id_str)

            for k in keys_to_set:
                priv_out['nodes'][k] = c['primary_rpc']
                priv_out['tcp_nodes'][k] = first_tcp
                priv_out['chain_nodes'][k] = c_entry

        with open(priv_file, 'w') as f:
            json.dump(priv_out, f, indent=2)
        os.chmod(priv_file, 0o600)
    except Exception as e:
        print(f"Warning: could not write {priv_file}: {e}", file=sys.stderr)

def print_summary(info):
    print("⚙️ Danh sách Node RPC (IP & Port):")
    for k, v in info['rpc_nodes'].items():
        print(f"  • {k}: {v}")

    if info['ws_nodes']:
        print("\n🔌 Danh sách Node WebSocket (WS URL):")
        for k, v in info['ws_nodes'].items():
            print(f"  • {k}: {v}")

    print("\n🌐 Danh sách Node TCP (P2P / Consensus):")
    for k, v in info['tcp_nodes'].items():
        print(f"  • {k}: {v}")

    if info['raft_nodes']:
        print("\n⛵ Danh sách Node Raft Consensus & Forward:")
        for k, v in info['raft_nodes'].items():
            fwd = info['forward_nodes'].get(k, '')
            fwd_str = f" (Forward: {fwd})" if fwd else ""
            print(f"  • {k}: {v}{fwd_str}")

if __name__ == '__main__':
    inv_file = sys.argv[1] if len(sys.argv) > 1 else 'inventory.yml'
    mode = sys.argv[2] if len(sys.argv) > 2 else 'summary'

    parsed = parse_inventory(inv_file)
    export_tmp_files(parsed)

    if mode == 'json':
        print(json.dumps(parsed, indent=2))
    elif mode in ['summary', 'display', '--summary', '-s']:
        print_summary(parsed)
    elif mode in ['export', '--export']:
        pass
    else:
        print_summary(parsed)
