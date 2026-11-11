#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════════════
#  🧹 METANODE RAFT CLUSTER — CONFIG-DRIVEN NODE REMOVAL (remove_nodes.sh)
#  Đọc cấu hình từ file YAML (mặc định: deploy/remove_nodes/config.yml) & Inventory.
#  Tự động gỡ node khỏi nhóm đồng thuận Raft an toàn (--transfer-first, kiểm tra Quorum),
#  dừng & disable systemd service, tùy chọn xóa dữ liệu, tự động comment (#) khỏi
#  inventory.yml và cập nhật lại cấu hình / Monitor cho các node còn lại.
#
#  CÁC CHỐT CHẶN AN TOÀN (SAFETY GUARDS):
#  1. Đảm bảo số lượng node còn lại trong cụm >= 1 và giữ nguyên Quorum trực tuyến.
#  2. Bảo vệ node giữ vai trò `raft_bootstrap: true` / `raft_secret_source` (từ chối gỡ
#     trừ khi truyền cờ tường minh `--allow-remove-bootstrap`).
#  3. Nghiêm cấm gỡ node có trùng cặp `(ansible_host, node_data_dir)` với node còn lại.
#  4. Hỗ trợ chạy lại an toàn (Idempotent): nếu node đã được gỡ khỏi Raft hoặc đã được
#     comment sẵn trong `inventory.yml`, script tự động nhận diện và xử lý mượt mà.
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
METANODE_ROOT="$(cd "${DEPLOY_DIR}/.." && pwd)"
ANSIBLE_CLUSTERS_DIR="${DEPLOY_DIR}/ansible_clusters"
EXEC_DIR="${METANODE_ROOT}/execution"

CONFIG_FILE="${CONFIG_FILE:-${SCRIPT_DIR}/config.yml}"
DRY_RUN="false"
ASSUME_YES="false"
ALLOW_REMOVE_BOOTSTRAP="false"

for arg in "$@"; do
    case "$arg" in
        --config=*|-c=*)          CONFIG_FILE="${arg#*=}" ;;
        --dry-run)                DRY_RUN="true" ;;
        --yes|-y)                 ASSUME_YES="true" ;;
        --allow-remove-bootstrap) ALLOW_REMOVE_BOOTSTRAP="true" ;;
        --help|-h)
            echo "Cách dùng: ./remove_nodes.sh [--config=config.yml] [--yes|-y] [--dry-run] [--allow-remove-bootstrap]"
            exit 0
            ;;
        *)
            echo "❌ Tham số không hợp lệ: ${arg}" >&2
            echo "Cách dùng: ./remove_nodes.sh [--config=config.yml] [--yes|-y] [--dry-run] [--allow-remove-bootstrap]" >&2
            exit 1
            ;;
    esac
done

if [ ! -f "${CONFIG_FILE}" ]; then
    if [ -f "${SCRIPT_DIR}/config.example.yml" ]; then
        cp "${SCRIPT_DIR}/config.example.yml" "${CONFIG_FILE}"
        echo "ℹ️  Đã tự động tạo ${CONFIG_FILE} từ config.example.yml."
    else
        echo "❌ Không tìm thấy file cấu hình: ${CONFIG_FILE}" >&2
        exit 1
    fi
fi

VAULT_FILE="${ANSIBLE_CLUSTERS_DIR}/.vault_pass"
if [ ! -f "$VAULT_FILE" ] && [ -f "$HOME/.vault_pass" ]; then
    VAULT_FILE="$HOME/.vault_pass"
fi

TMP_WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/metanode_remove_nodes.XXXXXX")"
chmod 700 "${TMP_WORK_DIR}"
cleanup_tmp() {
    rm -rf "${TMP_WORK_DIR}"
}
trap cleanup_tmp EXIT

# ── Đọc config.yml + inventory.yml và kiểm tra điều kiện an toàn (Pre-flight) ──
eval "$(python3 - "${DEPLOY_DIR}" "${CONFIG_FILE}" "${VAULT_FILE}" "${ALLOW_REMOVE_BOOTSTRAP}" << 'PYEOF'
import sys, os, re, yaml, shlex

deploy_dir, cfg_path, vault_file, allow_bootstrap_str = sys.argv[1:5]
allow_remove_bootstrap = (allow_bootstrap_str == "true")

with open(cfg_path, 'r', encoding='utf-8') as cf:
    cfg = yaml.safe_load(cf) or {}

inv_rel = str(cfg.get('inventory', 'ansible_clusters/inventory.yml'))
inv_path = inv_rel if os.path.isabs(inv_rel) else os.path.normpath(os.path.join(deploy_dir, inv_rel))
if not os.path.exists(inv_path):
    sys.stderr.write(f"ERROR: Inventory file not found: {inv_path}\n")
    sys.exit(1)

cluster_name = str(cfg.get('cluster', 'cluster_1'))
remove_nodes = [str(x).strip() for x in (cfg.get('remove_nodes') or []) if str(x).strip()]
clean_data = bool(cfg.get('clean_removed_data', False))
update_inv = bool(cfg.get('update_inventory', True))
restart_remaining = bool(cfg.get('restart_remaining_nodes', True))
restart_parent = bool(cfg.get('restart_parent_chain', False))

if not remove_nodes:
    sys.stderr.write(f"ERROR: 'remove_nodes' list is empty in {cfg_path}\n")
    sys.exit(1)

if len(set(remove_nodes)) != len(remove_nodes):
    sys.stderr.write(f"ERROR: Duplicate entries found in 'remove_nodes': {remove_nodes}\n")
    sys.exit(1)

vault = None
if os.path.exists(vault_file):
    try:
        from ansible.parsing.vault import VaultLib, VaultSecret
        from ansible.constants import DEFAULT_VAULT_ID_MATCH
        with open(vault_file, 'rb') as vf:
            vpass = vf.read().strip()
        vault = VaultLib([(DEFAULT_VAULT_ID_MATCH, VaultSecret(vpass))])
    except Exception:
        pass

class DecryptLoader(yaml.SafeLoader):
    pass

def construct_vault(loader, node):
    val = loader.construct_scalar(node)
    if vault:
        try:
            return vault.decrypt(val.strip()).decode('utf-8').strip()
        except Exception:
            return ""
    return ""

DecryptLoader.add_constructor('!vault', construct_vault)

with open(inv_path, 'r', encoding='utf-8') as f:
    raw_inv_text = f.read()

data = yaml.load(raw_inv_text, Loader=DecryptLoader) or {}
all_vars = data.get('all', {}).get('vars', {}) or {}
exec_children = data.get('all', {}).get('children', {}).get('exec_clusters', {}).get('children', {}) or {}

if cluster_name not in exec_children:
    sys.stderr.write(f"ERROR: Cluster '{cluster_name}' not found in {inv_path}\n")
    sys.exit(1)

cdata = exec_children[cluster_name] or {}
cvars = cdata.get('vars', {}) or {}
chosts = cdata.get('hosts', {}) or {}

def locate_cluster_hosts_range(lines, target_cluster):
    in_exec = False
    exec_indent = -1
    in_children = False
    children_indent = -1
    in_cluster = False
    cluster_indent = -1
    hosts_indent = -1
    hosts_start = -1

    for idx, line in enumerate(lines):
        stripped = line.strip()
        if not stripped or stripped.startswith('#'):
            continue
        indent = len(line) - len(line.lstrip(' '))

        if not in_exec:
            if re.match(r'^exec_clusters\s*:\s*(?:#.*)?$', stripped):
                in_exec = True
                exec_indent = indent
            continue

        if indent <= exec_indent:
            break

        if not in_children:
            if re.match(r'^children\s*:\s*(?:#.*)?$', stripped):
                in_children = True
                children_indent = indent
            continue

        if indent <= children_indent:
            in_children = False
            continue

        if not in_cluster:
            if re.match(r'^' + re.escape(target_cluster) + r'\s*:\s*(?:#.*)?$', stripped):
                in_cluster = True
                cluster_indent = indent
            continue

        if indent <= cluster_indent:
            break

        if hosts_start == -1:
            if re.match(r'^hosts\s*:\s*(?:#.*)?$', stripped):
                hosts_indent = indent
                hosts_start = idx + 1
            continue
        else:
            if indent <= hosts_indent:
                return hosts_indent, hosts_start, idx

    if hosts_start != -1:
        return hosts_indent, hosts_start, len(lines)
    return -1, -1, -1

raw_lines = raw_inv_text.splitlines()
h_indent, h_start, h_end = locate_cluster_hosts_range(raw_lines, cluster_name)

def parse_commented_host(host_name):
    if h_start == -1:
        return None
    pat = re.compile(r'^(\s*)#( ?)' + re.escape(host_name) + r'\s*:\s*(?:#.*)?$')
    for idx in range(h_start, h_end):
        m = pat.match(raw_lines[idx])
        if not m:
            continue
        lead_ws, space_after = m.group(1), m.group(2)
        if len(lead_ws) <= h_indent:
            continue
        def strip_one_comment(s):
            if space_after == ' ':
                return re.sub(r'^(\s*)# ?', r'\1', s, count=1)
            return re.sub(r'^(\s*)#', r'\1', s, count=1)

        header_un = strip_one_comment(raw_lines[idx])
        base_indent = len(header_un) - len(header_un.lstrip(' '))
        collected = [header_un]

        for j in range(idx + 1, h_end):
            lj = raw_lines[j]
            if not lj.strip():
                continue
            if not lj.lstrip(' ').startswith('#'):
                break
            unj = strip_one_comment(lj)
            if not unj.strip() or unj.lstrip(' ').startswith('#'):
                break
            ind_j = len(unj) - len(unj.lstrip(' '))
            if ind_j <= base_indent:
                break
            collected.append(unj)

        try:
            parsed = yaml.load("\n".join(collected) + "\n", Loader=DecryptLoader) or {}
            if isinstance(parsed, dict) and host_name in parsed:
                return parsed[host_name] or {}
        except Exception:
            return None
    return None

def merge_host_vars(hvars):
    merged = dict(all_vars)
    merged.update(cvars)
    merged.update(hvars or {})
    for k in ('ansible_ssh_pass', 'ansible_become_pass'):
        v = str(merged.get(k, ''))
        if 'ansible_become_pass' in v and k != 'ansible_become_pass':
            merged[k] = str(merged.get('ansible_become_pass', ''))
        if 'ansible_ssh_pass' in v and k != 'ansible_ssh_pass':
            merged[k] = str(merged.get('ansible_ssh_pass', ''))
    return merged

active_hosts_map = {hname: merge_host_vars(hvars) for hname, hvars in chosts.items()}
remove_hosts_map = {}
remove_in_inv = {}

for rn in remove_nodes:
    if rn in active_hosts_map:
        remove_hosts_map[rn] = active_hosts_map[rn]
        remove_in_inv[rn] = True
    else:
        commented_vars = parse_commented_host(rn)
        if commented_vars is not None:
            remove_hosts_map[rn] = merge_host_vars(commented_vars)
            remove_in_inv[rn] = False
        else:
            sys.stderr.write(
                f"ERROR: Node '{rn}' not found (neither active nor commented) under cluster '{cluster_name}' in {inv_path}\n"
            )
            sys.exit(1)

remaining_nodes = [h for h in chosts.keys() if h not in remove_nodes]

# ── Safety Guard 1: Phải còn lại ít nhất 1 node trong cụm ─────────────────────
if not remaining_nodes:
    sys.stderr.write(
        f"FATAL SAFETY ERROR: Removing {remove_nodes} would leave 0 remaining active nodes in cluster '{cluster_name}'!\n"
        f"If you want to stop the entire cluster, use: ./deploy_clusters.sh --stop\n"
    )
    sys.exit(1)

# ── Safety Guard 2: Bảo vệ node raft_bootstrap / raft_secret_source ───────────
raft_secret_source = str(cvars.get('raft_secret_source', '')).strip()
for rn in remove_nodes:
    rh = remove_hosts_map[rn]
    is_boot = bool(rh.get('raft_bootstrap', False))
    is_sec_src = (rn == raft_secret_source)
    if (is_boot or is_sec_src) and not allow_remove_bootstrap:
        sys.stderr.write(
            f"FATAL SAFETY ERROR: Refusing to remove '{rn}' because it is configured as "
            f"raft_bootstrap={is_boot} / raft_secret_source='{raft_secret_source}' in {inv_path}.\n"
            f"Ansible's exec_cluster role requires raft_secret_source to remain in the cluster with raft_bootstrap: true.\n"
            f"Update raft_secret_source / raft_bootstrap to another remaining node first, or pass --allow-remove-bootstrap.\n"
        )
        sys.exit(1)

# ── Safety Guard 3: Không cho phép trùng hoặc là tiền tố đường dẫn của nhau trên cùng host ──
combined_check = [(h, active_hosts_map[h]) for h in remaining_nodes] + [(rn, remove_hosts_map[rn]) for rn in remove_nodes]
seen_host_dirs = []
for hname, h in combined_check:
    hip = str(h.get('ansible_host', '127.0.0.1')).strip()
    nid = str(h.get('node_id', hname))
    ddir = os.path.normpath(str(h.get('node_data_dir', f"/opt/metanode/{nid}")))
    # Đảm bảo đường dẫn chuẩn có đuôi separator để so khớp tiền tố
    norm_slash = ddir if ddir.endswith('/') else ddir + '/'
    for other_name, other_ip, other_dir, other_slash in seen_host_dirs:
        if hip == other_ip:
            if ddir == other_dir:
                sys.stderr.write(
                    f"FATAL SAFETY ERROR: Nodes '{other_name}' and '{hname}' share the exact same (ansible_host={hip}, "
                    f"node_data_dir={ddir})! Refusing to run to protect active node data.\n"
                )
                sys.exit(1)
            if norm_slash.startswith(other_slash) or other_slash.startswith(norm_slash):
                sys.stderr.write(
                    f"FATAL SAFETY ERROR: Nodes '{other_name}' ({other_dir}) and '{hname}' ({ddir}) have nested/prefix "
                    f"directories on host {hip}! Refusing to run to prevent accidental process termination or data loss.\n"
                )
                sys.exit(1)
    seen_host_dirs.append((hname, hip, ddir, norm_slash))

def emit_host(prefix, hname, h, in_inv=True):
    host_ip = str(h.get('ansible_host', '127.0.0.1')).strip()
    conn = str(h.get('ansible_connection', '')).strip()
    port = str(h.get('ansible_port', 22)).strip()
    user = str(h.get('ansible_user', 'abc')).strip()
    become_pass = str(h.get('ansible_become_pass', ''))
    ssh_pass = str(h.get('ansible_ssh_pass', become_pass))
    node_id = str(h.get('node_id', hname)).strip()
    data_dir = os.path.normpath(str(h.get('node_data_dir', f"/opt/metanode/{node_id}")))
    raft_port = str(h.get('raft_port', 7110)).strip()
    fwd_port = str(h.get('forward_port', 7210)).strip()
    rpc_port = str(h.get('rpc_port', 8747)).strip()
    print(f"{prefix}_ANODE={shlex.quote(hname)}")
    print(f"{prefix}_ID={shlex.quote(node_id)}")
    print(f"{prefix}_HOST={shlex.quote(host_ip)}")
    print(f"{prefix}_CONN={shlex.quote(conn)}")
    print(f"{prefix}_PORT={shlex.quote(port)}")
    print(f"{prefix}_USER={shlex.quote(user)}")
    print(f"{prefix}_PASS={shlex.quote(ssh_pass)}")
    print(f"{prefix}_BECOME_PASS={shlex.quote(become_pass)}")
    print(f"{prefix}_DIR={shlex.quote(data_dir)}")
    print(f"{prefix}_RPORT={shlex.quote(raft_port)}")
    print(f"{prefix}_APORT={shlex.quote(fwd_port)}")
    print(f"{prefix}_RPCPORT={shlex.quote(rpc_port)}")
    print(f"{prefix}_IN_INV={'true' if in_inv else 'false'}")

secret_file = str(cvars.get('raft_secret_file', all_vars.get('raft_secret_file', '/opt/metanode/raft_secret.key')))

print(f"INVENTORY_PATH={shlex.quote(inv_path)}")
print(f"CLUSTER_NAME={shlex.quote(cluster_name)}")
print(f"RAFT_SECRET_FILE={shlex.quote(secret_file)}")
print(f"CLEAN_DATA={'true' if clean_data else 'false'}")
print(f"UPDATE_INVENTORY={'true' if update_inv else 'false'}")
print(f"RESTART_REMAINING={'true' if restart_remaining else 'false'}")
print(f"RESTART_PARENT={'true' if restart_parent else 'false'}")

print(f"KEEP_COUNT={len(remaining_nodes)}")
for idx, kn in enumerate(remaining_nodes):
    emit_host(f"KEEP_{idx}", kn, active_hosts_map[kn], True)

print(f"REMOVE_COUNT={len(remove_nodes)}")
for idx, rn in enumerate(remove_nodes):
    emit_host(f"REM_{idx}", rn, remove_hosts_map[rn], remove_in_inv[rn])
PYEOF
)"

LOCAL_IPS=$(hostname -I 2>/dev/null || true)

is_local_target() {
    local conn="$1" host="$2"
    if [ "$conn" = "local" ] || [ "$host" = "127.0.0.1" ] || [[ " ${LOCAL_IPS} " == *" ${host} "* ]]; then
        return 0
    fi
    return 1
}

run_ssh() {
    local user="$1" host="$2" port="$3" pass="$4"
    shift 4
    if [ -n "$pass" ]; then
        SSHPASS="$pass" sshpass -e ssh -o StrictHostKeyChecking=no -p "$port" "${user}@${host}" "$@"
    else
        ssh -o StrictHostKeyChecking=no -p "$port" "${user}@${host}" "$@"
    fi
}

# ── Chuẩn bị raft_secret.key (đọc local hoặc tải tạm về TMP_WORK_DIR 0600) ────
prepare_raft_secret() {
    if [ -r "${RAFT_SECRET_FILE}" ] && [ -s "${RAFT_SECRET_FILE}" ]; then
        EFFECTIVE_SECRET_FILE="${RAFT_SECRET_FILE}"
        return 0
    fi

    EFFECTIVE_SECRET_FILE="${TMP_WORK_DIR}/raft_secret.key"
    touch "${EFFECTIVE_SECRET_FILE}"
    chmod 600 "${EFFECTIVE_SECRET_FILE}"

    if is_local_target "${KEEP_0_CONN}" "${KEEP_0_HOST}"; then
        if [ -n "${KEEP_0_BECOME_PASS}" ]; then
            printf '%s\n' "${KEEP_0_BECOME_PASS}" | sudo -S -p '' cat "${RAFT_SECRET_FILE}" > "${EFFECTIVE_SECRET_FILE}"
        else
            sudo -n cat "${RAFT_SECRET_FILE}" > "${EFFECTIVE_SECRET_FILE}"
        fi
    else
        if ! run_ssh "${KEEP_0_USER}" "${KEEP_0_HOST}" "${KEEP_0_PORT}" "${KEEP_0_PASS}" \
            "cat '${RAFT_SECRET_FILE}' 2>/dev/null" > "${EFFECTIVE_SECRET_FILE}" || [ ! -s "${EFFECTIVE_SECRET_FILE}" ]; then
            if [ -n "${KEEP_0_BECOME_PASS}" ]; then
                printf '%s\n' "${KEEP_0_BECOME_PASS}" | run_ssh "${KEEP_0_USER}" "${KEEP_0_HOST}" "${KEEP_0_PORT}" "${KEEP_0_PASS}" \
                    "sudo -S -p '' cat '${RAFT_SECRET_FILE}'" > "${EFFECTIVE_SECRET_FILE}"
            else
                run_ssh "${KEEP_0_USER}" "${KEEP_0_HOST}" "${KEEP_0_PORT}" "${KEEP_0_PASS}" \
                    "sudo -n cat '${RAFT_SECRET_FILE}'" > "${EFFECTIVE_SECRET_FILE}"
            fi
        fi
    fi

    if [ ! -s "${EFFECTIVE_SECRET_FILE}" ]; then
        echo "❌ Không thể đọc ${RAFT_SECRET_FILE} từ node '${KEEP_0_ANODE}' (${KEEP_0_HOST})!" >&2
        exit 1
    fi
}

# ── Sinh file JSON cấu hình cụm tạm thời trong TMP_WORK_DIR (quyền 0600) ──────
write_cluster_json() {
    local out_file="$1"
    local start_rem_idx="$2"
    python3 - "${out_file}" "${EFFECTIVE_SECRET_FILE}" "${KEEP_COUNT}" "${REMOVE_COUNT}" "${start_rem_idx}" << 'PYEOF'
import sys, os, json

out_file, secret_file, keep_cnt, rem_cnt, start_rem = (
    sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), int(sys.argv[5])
)
members = []
for i in range(keep_cnt):
    members.append({
        "id": os.environ[f"KEEP_{i}_ID"],
        "raft_addr": f"{os.environ[f'KEEP_{i}_HOST']}:{os.environ[f'KEEP_{i}_RPORT']}",
        "admin_addr": f"{os.environ[f'KEEP_{i}_HOST']}:{os.environ[f'KEEP_{i}_APORT']}",
    })
for j in range(start_rem, rem_cnt):
    members.append({
        "id": os.environ[f"REM_{j}_ID"],
        "raft_addr": f"{os.environ[f'REM_{j}_HOST']}:{os.environ[f'REM_{j}_RPORT']}",
        "admin_addr": f"{os.environ[f'REM_{j}_HOST']}:{os.environ[f'REM_{j}_APORT']}",
    })

fd = os.open(out_file, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, 'w', encoding='utf-8') as f:
    json.dump({"secret_file": secret_file, "members": members}, f, indent=2)
PYEOF
}

export KEEP_COUNT REMOVE_COUNT
for (( i=0; i<KEEP_COUNT; i++ )); do
    for suffix in ID HOST RPORT APORT; do
        v="KEEP_${i}_${suffix}"
        export "${v}=${!v}"
    done
done
for (( i=0; i<REMOVE_COUNT; i++ )); do
    for suffix in ID HOST RPORT APORT; do
        v="REM_${i}_${suffix}"
        export "${v}=${!v}"
    done
done

# ── Hàm dừng & dọn dẹp trực tiếp khi node đã được comment sẵn trong inventory ─
stop_unlisted_node_safe() {
    local conn="$1" user="$2" host="$3" port="$4" pass="$5" become_pass="$6" anode="$7" target_dir="$8"
    if [ -z "$target_dir" ] || [ "$target_dir" = "/" ]; then
        echo "❌ [AN TOÀN] target_dir không hợp lệ ('${target_dir}')! Hủy thao tác." >&2
        exit 1
    fi

    local stop_script
    stop_script=$(cat <<EOF
systemctl stop 'metanode-${anode}' 2>/dev/null || true
systemctl disable 'metanode-${anode}' 2>/dev/null || true
if [ -f '${target_dir}/cluster.pid' ]; then
    pid=\$(cat '${target_dir}/cluster.pid' 2>/dev/null || true)
    if [ -n "\$pid" ] && [ -d "/proc/\$pid" ] && tr '\0' ' ' < "/proc/\$pid/cmdline" 2>/dev/null | grep -q -F '${target_dir}/'; then
        kill -15 "\$pid" 2>/dev/null || true
        sleep 0.5
        kill -9 "\$pid" 2>/dev/null || true
    fi
    rm -f '${target_dir}/cluster.pid'
fi
for pid in \$(pgrep -x simple_chain 2>/dev/null || true); do
    if tr '\0' ' ' < "/proc/\$pid/cmdline" 2>/dev/null | grep -q -F '${target_dir}/'; then
        kill -15 "\$pid" 2>/dev/null || true
        sleep 0.5
        kill -9 "\$pid" 2>/dev/null || true
    fi
done
EOF
)
    if is_local_target "$conn" "$host"; then
        if [ "$(id -u)" -eq 0 ]; then
            bash -c "$stop_script"
        elif [ -n "$become_pass" ]; then
            printf '%s\n' "$become_pass" | sudo -S -p '' bash -c "$stop_script"
        else
            sudo -n bash -c "$stop_script"
        fi
    else
        if [ "$user" = "root" ]; then
            run_ssh "$user" "$host" "$port" "$pass" "$stop_script" \
                || { echo "❌ Lỗi SSH tới ${user}@${host}:${port} khi dừng service '${anode}'!" >&2; exit 1; }
        elif [ -n "$become_pass" ]; then
            local remote_b64
            remote_b64=$(printf '%s' "$stop_script" | base64 -w0)
            printf '%s\n' "$become_pass" | run_ssh "$user" "$host" "$port" "$pass" \
                "sudo -S -p '' bash -c \"\$(echo '${remote_b64}' | base64 -d)\"" \
                || { echo "❌ Lỗi SSH/sudo tới ${user}@${host}:${port} khi dừng service '${anode}'!" >&2; exit 1; }
        else
            local remote_b64
            remote_b64=$(printf '%s' "$stop_script" | base64 -w0)
            run_ssh "$user" "$host" "$port" "$pass" \
                "sudo -n bash -c \"\$(echo '${remote_b64}' | base64 -d)\"" \
                || { echo "❌ Lỗi SSH/sudo tới ${user}@${host}:${port} khi dừng service '${anode}'!" >&2; exit 1; }
        fi
    fi
}

verify_node_stopped_safe() {
    local conn="$1" user="$2" host="$3" port="$4" pass="$5" anode="$6" target_dir="$7"
    if [ -z "$target_dir" ] || [ "$target_dir" = "/" ] || [ "${#target_dir}" -lt 5 ]; then
        echo "❌ [AN TOÀN] target_dir không hợp lệ ('${target_dir}')! Hủy thao tác." >&2
        exit 1
    fi
    local running_pid=""
    # Dùng '[c]onfig.json' để tránh pgrep -f tự khớp với shell `bash -c` của phiên SSH
    if is_local_target "$conn" "$host"; then
        running_pid=$(pgrep -f "${target_dir}/[c]onfig.json" 2>/dev/null || true)
    else
        running_pid=$(run_ssh "$user" "$host" "$port" "$pass" "pgrep -f '${target_dir}/[c]onfig.json' 2>/dev/null || true") \
            || { echo "❌ Lỗi kết nối SSH tới ${user}@${host}:${port} khi kiểm tra tiến trình '${anode}'!" >&2; exit 1; }
    fi
    if [ -n "$running_pid" ]; then
        echo "❌ [AN TOÀN] Tiến trình của node '${anode}' (${target_dir}) vẫn đang chạy trên ${host} (PID: ${running_pid})! Hủy thao tác xóa dữ liệu." >&2
        exit 1
    fi
}

clean_unlisted_node_safe() {
    local conn="$1" user="$2" host="$3" port="$4" pass="$5" become_pass="$6" anode="$7" target_dir="$8"
    verify_node_stopped_safe "$conn" "$user" "$host" "$port" "$pass" "$anode" "$target_dir"
    local clean_cmd="rm -rf '${target_dir}/data' '${target_dir}/snapshot' '${target_dir}/logs' '${target_dir}/explorer' '${target_dir}/explorer-ro' '${target_dir}/raft' '${target_dir}/backup' '${target_dir}/genesis.json'"
    if is_local_target "$conn" "$host"; then
        if [ "$(id -u)" -eq 0 ]; then
            bash -c "$clean_cmd"
        elif [ -n "$become_pass" ]; then
            printf '%s\n' "$become_pass" | sudo -S -p '' bash -c "$clean_cmd"
        else
            bash -c "$clean_cmd"
        fi
    else
        if [ "$user" = "root" ] || [ -z "$become_pass" ]; then
            run_ssh "$user" "$host" "$port" "$pass" "$clean_cmd" \
                || { echo "❌ Lỗi SSH tới ${user}@${host}:${port} khi xóa dữ liệu '${anode}'!" >&2; exit 1; }
        else
            local remote_b64
            remote_b64=$(printf '%s' "$clean_cmd" | base64 -w0)
            printf '%s\n' "$become_pass" | run_ssh "$user" "$host" "$port" "$pass" \
                "sudo -S -p '' bash -c \"\$(echo '${remote_b64}' | base64 -d)\"" \
                || { echo "❌ Lỗi SSH/sudo tới ${user}@${host}:${port} khi xóa dữ liệu '${anode}'!" >&2; exit 1; }
        fi
    fi
}

# ── Hiển thị tóm tắt kế hoạch gỡ node ─────────────────────────────────────────
echo "══════════════════════════════════════════════════════════════════════"
echo "🧹 GỠ NODE KHỎI CỤM RAFT & ĐỒNG BỘ CẤU HÌNH (${CLUSTER_NAME})"
echo "   • File Config   : ${CONFIG_FILE}"
echo "   • Inventory     : ${INVENTORY_PATH}"
echo "   • Node giữ lại  : ${KEEP_COUNT} node"
for (( i=0; i<KEEP_COUNT; i++ )); do
    kanode_v="KEEP_${i}_ANODE"; kid_v="KEEP_${i}_ID"; khost_v="KEEP_${i}_HOST"
    krport_v="KEEP_${i}_RPORT"; kaport_v="KEEP_${i}_APORT"
    echo "     - ${!kanode_v} [ID=${!kid_v}] (${!khost_v}, raft=${!krport_v}, fwd=${!kaport_v})"
done
echo "   • Node cần gỡ   : ${REMOVE_COUNT} node"
for (( i=0; i<REMOVE_COUNT; i++ )); do
    ranode_v="REM_${i}_ANODE"; rid_v="REM_${i}_ID"; rhost_v="REM_${i}_HOST"
    rport_v="REM_${i}_PORT"; ruser_v="REM_${i}_USER"; rdir_v="REM_${i}_DIR"
    rrport_v="REM_${i}_RPORT"; raport_v="REM_${i}_APORT"; rinv_v="REM_${i}_IN_INV"
    inv_status="đang bật trong inventory"
    if [ "${!rinv_v}" != "true" ]; then
        inv_status="đã comment sẵn trong inventory ℹ️"
    fi
    echo "     - ${!ranode_v} [ID=${!rid_v}] (${!ruser_v}@${!rhost_v}:${!rport_v}, raft=${!rrport_v}, fwd=${!raport_v}, dir=${!rdir_v} | ${inv_status})"
done
echo "   • Xóa dữ liệu ổ cứng (clean_removed_data) : ${CLEAN_DATA}"
echo "   • Tự động comment inventory.yml           : ${UPDATE_INVENTORY}"
echo "   • Khởi động lại các node còn lại          : ${RESTART_REMAINING} (Parent Chain: ${RESTART_PARENT})"
echo "══════════════════════════════════════════════════════════════════════"

prepare_raft_secret
CLUSTER_ACTIVE_JSON="${TMP_WORK_DIR}/cluster_active.json"
CLUSTER_REMAINING_JSON="${TMP_WORK_DIR}/cluster_remaining.json"
write_cluster_json "${CLUSTER_ACTIVE_JSON}" 0
write_cluster_json "${CLUSTER_REMAINING_JSON}" "${REMOVE_COUNT}"

# ── Chế độ kiểm tra thử (--dry-run) ───────────────────────────────────────────
if [ "${DRY_RUN}" = "true" ]; then
    echo ""
    echo "🔍 [DRY-RUN] Kiểm tra trạng thái Quorum Raft trực tuyến trước khi gỡ..."
    cd "${EXEC_DIR}"
    for (( i=0; i<REMOVE_COUNT; i++ )); do
        rid_v="REM_${i}_ID"
        RID="${!rid_v}"
        dry_out=""
        if dry_out=$(go run ./cmd/tool/rollup_cluster -config "${CLUSTER_ACTIVE_JSON}" remove-replica \
            --id "${RID}" --transfer-first --dry-run 2>&1); then
            echo "   ✅ [DRY-RUN] Kế hoạch gỡ ${RID} hợp lệ:"
            echo "${dry_out}" | sed 's/^/      /'
        elif [[ "${dry_out}" == *"is not a member"* ]]; then
            echo "   ℹ️  [DRY-RUN] ${RID} hiện không còn trong nhóm Raft (sẽ tự động bỏ qua ở Bước 1)."
        else
            echo "   ❌ [DRY-RUN] Không thể gỡ ${RID} khỏi Raft:" >&2
            echo "${dry_out}" >&2
            exit 1
        fi
    done

    # Kiểm tra các node giữ lại có đủ Quorum lành mạnh không
    rem_check_out=""
    if rem_check_out=$(go run ./cmd/tool/rollup_cluster -config "${CLUSTER_REMAINING_JSON}" check 2>&1); then
        echo "   ✅ [DRY-RUN] Cụm ${KEEP_COUNT} node giữ lại khỏe mạnh, đồng thuận và StateRoot nhất quán 100%!"
    else
        # Khi các node cần gỡ vẫn đang là Voter trong cụm 5 node, check riêng 3 node giữ lại
        # vẫn có thể báo quorum_ok=true nếu >= 3/5; nếu < majority của cấu hình cũ thì kiểm tra số node reachable
        python3 - "${rem_check_out}" "${KEEP_COUNT}" << 'PYEOF'
import sys, json
raw, keep_cnt = sys.argv[1], int(sys.argv[2])
try:
    start = raw.find('{')
    rep = json.loads(raw[start:]) if start >= 0 else {}
except Exception:
    sys.stderr.write(f"ERROR: Cannot parse rollup_cluster check output:\n{raw}\n")
    sys.exit(1)
reachable_ok = sum(1 for r in rep.get("replicas", []) if r.get("reachable") and not r.get("status", {}).get("failed"))
majority_needed = keep_cnt // 2 + 1
if reachable_ok < majority_needed:
    sys.stderr.write(
        f"FATAL DRY-RUN ERROR: Only {reachable_ok}/{keep_cnt} remaining nodes are reachable and healthy "
        f"(need at least {majority_needed} for post-removal quorum)!\n"
    )
    sys.exit(1)
print(f"   ✅ [DRY-RUN] {reachable_ok}/{keep_cnt} node giữ lại đang ONLINE (đủ Quorum >= {majority_needed} sau khi gỡ).")
PYEOF
    fi
    echo "✅ Kiểm tra cấu hình & các chốt chặn an toàn (--dry-run) hoàn tất!"
    exit 0
fi

# ── Hỏi xác nhận [y/N] trước khi thực hiện thao tác thật ──────────────────────
if [ "${ASSUME_YES}" != "true" ]; then
    if [ -t 0 ]; then
        echo ""
        if [ "${CLEAN_DATA}" = "true" ]; then
            echo "⚠️  CẢNH BÁO: Bạn đang bật clean_removed_data=true — toàn bộ thư mục data/ và raft/ của node bị gỡ sẽ bị XÓA VĨNH VIỄN!"
            read -r -p "❓ Bạn có chắc chắn muốn GỠ ${REMOVE_COUNT} node trên khỏi cụm Raft VÀ XÓA DỮ LIỆU của chúng không? [y/N]: " reply
        else
            read -r -p "❓ Bạn có chắc chắn muốn GỠ ${REMOVE_COUNT} node trên khỏi cụm Raft (giữ nguyên dữ liệu trên ổ cứng) không? [y/N]: " reply
        fi
        if [[ ! "$reply" =~ ^[Yy]$ ]]; then
            echo "🛑 Đã dừng lại theo lựa chọn của bạn."
            exit 1
        fi
    else
        echo "❌ Vui lòng chạy trên terminal để xác nhận [y/N] hoặc thêm cờ --yes (-y)." >&2
        exit 1
    fi
fi

# ── BƯỚC 1/5: Gỡ lần lượt từng node khỏi nhóm đồng thuận Raft ─────────────────
echo ""
echo "👉 [Bước 1/5] Gỡ lần lượt từng node khỏi nhóm đồng thuận Raft (remove-replica --transfer-first)..."
cd "${EXEC_DIR}"
for (( i=0; i<REMOVE_COUNT; i++ )); do
    rid_v="REM_${i}_ID"; ranode_v="REM_${i}_ANODE"
    RID="${!rid_v}"; RANODE="${!ranode_v}"
    write_cluster_json "${CLUSTER_ACTIVE_JSON}" "${i}"

    echo "   ➖ Đang gỡ ${RANODE} [ID=${RID}] khỏi nhóm Raft..."
    rm_out=""
    if rm_out=$(go run ./cmd/tool/rollup_cluster -config "${CLUSTER_ACTIVE_JSON}" remove-replica \
        --id "${RID}" \
        --transfer-first 2>&1); then
        echo "${rm_out}" | sed 's/^/      /'
        echo "   ✅ Đã gỡ ${RID} khỏi cấu hình đồng thuận Raft an toàn!"
    elif [[ "${rm_out}" == *"is not a member"* ]]; then
        echo "   ℹ️  ${RID} không còn trong nhóm Raft (đã được gỡ trước đó, bỏ qua)."
    else
        echo "${rm_out}" >&2
        exit 1
    fi
done

# ── BƯỚC 2/5: Dừng systemd service & tùy chọn dọn dữ liệu trên node bị gỡ ────
echo ""
echo "👉 [Bước 2/5] Dừng & vô hiệu hóa service trên các node đã gỡ (clean_removed_data=${CLEAN_DATA})..."
cd "${ANSIBLE_CLUSTERS_DIR}"
for (( i=0; i<REMOVE_COUNT; i++ )); do
    ranode_v="REM_${i}_ANODE"; rconn_v="REM_${i}_CONN"; rhost_v="REM_${i}_HOST"
    rport_v="REM_${i}_PORT"; ruser_v="REM_${i}_USER"; rpass_v="REM_${i}_PASS"
    rbpass_v="REM_${i}_BECOME_PASS"; rdir_v="REM_${i}_DIR"; rinv_v="REM_${i}_IN_INV"

    RANODE="${!ranode_v}"; RCONN="${!rconn_v}"; RHOST="${!rhost_v}"
    RPORT="${!rport_v}"; RUSER="${!ruser_v}"; RPASS="${!rpass_v}"
    RBPASS="${!rbpass_v}"; RDIR="${!rdir_v}"; RINV="${!rinv_v}"

    echo "   🛑 Đang dừng và disable service metanode-${RANODE} trên ${RHOST}..."
    if [ "${RINV}" = "true" ]; then
        ./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --stop --node="${RANODE}" --no-monitor --no-notify
        if [ "${CLEAN_DATA}" = "true" ]; then
            echo "   🧹 Đang xóa dữ liệu của '${RANODE}' (${RHOST}:${RDIR})..."
            verify_node_stopped_safe "${RCONN}" "${RUSER}" "${RHOST}" "${RPORT}" "${RPASS}" "${RANODE}" "${RDIR}"
            ./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --clean --node="${RANODE}" --no-monitor --no-notify
        fi
    else
        stop_unlisted_node_safe "${RCONN}" "${RUSER}" "${RHOST}" "${RPORT}" "${RPASS}" "${RBPASS}" "${RANODE}" "${RDIR}"
        echo "   ✅ Đã dừng và disable metanode-${RANODE} trên ${RHOST}."
        if [ "${CLEAN_DATA}" = "true" ]; then
            echo "   🧹 Đang xóa dữ liệu của '${RANODE}' (${RHOST}:${RDIR})..."
            clean_unlisted_node_safe "${RCONN}" "${RUSER}" "${RHOST}" "${RPORT}" "${RPASS}" "${RBPASS}" "${RANODE}" "${RDIR}"
            echo "   ✅ Đã dọn sạch dữ liệu tại ${RHOST}:${RDIR}."
        fi
    fi
done

# ── BƯỚC 3/5: Tự động comment (#) các node đã gỡ trong inventory.yml ──────────
echo ""
if [ "${UPDATE_INVENTORY}" = "true" ]; then
    echo "👉 [Bước 3/5] Cập nhật file inventory (${INVENTORY_PATH})..."
    ACTIVE_TO_COMMENT=()
    for (( i=0; i<REMOVE_COUNT; i++ )); do
        rinv_v="REM_${i}_IN_INV"; ranode_v="REM_${i}_ANODE"
        if [ "${!rinv_v}" = "true" ]; then
            ACTIVE_TO_COMMENT+=("${!ranode_v}")
        else
            echo "   ℹ️  Node '${!ranode_v}' đã được comment sẵn trong ${INVENTORY_PATH} (bỏ qua)."
        fi
    done

    if [ "${#ACTIVE_TO_COMMENT[@]}" -gt 0 ]; then
        python3 - "${INVENTORY_PATH}" "${CLUSTER_NAME}" "${ACTIVE_TO_COMMENT[@]}" << 'PYEOF'
import sys, os, re, tempfile, yaml

inv_path = sys.argv[1]
cluster_name = sys.argv[2]
targets = set(sys.argv[3:])

class DummyVaultLoader(yaml.SafeLoader):
    pass

DummyVaultLoader.add_constructor('!vault', lambda loader, node: loader.construct_scalar(node))

with open(inv_path, 'r', encoding='utf-8') as f:
    orig_text = f.read()

orig_data = yaml.load(orig_text, Loader=DummyVaultLoader) or {}
orig_exec = orig_data.get('all', {}).get('children', {}).get('exec_clusters', {}).get('children', {}) or {}
orig_cluster_hosts = set((orig_exec.get(cluster_name, {}).get('hosts') or {}).keys())
expected_remaining = orig_cluster_hosts - targets

lines = orig_text.splitlines(keepends=True)

def find_hosts_section(ls, target_cluster):
    in_exec = in_children = in_cluster = False
    exec_ind = child_ind = clust_ind = hosts_ind = -1
    hosts_start = -1
    for idx, line in enumerate(ls):
        s = line.strip()
        if not s or s.startswith('#'):
            continue
        ind = len(line) - len(line.lstrip(' '))
        if not in_exec:
            if re.match(r'^exec_clusters\s*:\s*(?:#.*)?$', s):
                in_exec, exec_ind = True, ind
            continue
        if ind <= exec_ind:
            break
        if not in_children:
            if re.match(r'^children\s*:\s*(?:#.*)?$', s):
                in_children, child_ind = True, ind
            continue
        if ind <= child_ind:
            in_children = False
            continue
        if not in_cluster:
            if re.match(r'^' + re.escape(target_cluster) + r'\s*:\s*(?:#.*)?$', s):
                in_cluster, clust_ind = True, ind
            continue
        if ind <= clust_ind:
            break
        if hosts_start == -1:
            if re.match(r'^hosts\s*:\s*(?:#.*)?$', s):
                hosts_ind, hosts_start = ind, idx + 1
            continue
        else:
            if ind <= hosts_ind:
                return hosts_ind, hosts_start, idx
    if hosts_start != -1:
        return hosts_ind, hosts_start, len(ls)
    return -1, -1, -1

h_ind, h_start, h_end = find_hosts_section(lines, cluster_name)
if h_start == -1:
    sys.stderr.write(f"ERROR: Could not locate hosts section for cluster '{cluster_name}' in {inv_path}\n")
    sys.exit(1)

idx = h_start
while idx < h_end:
    line = lines[idx]
    s = line.strip()
    if not s or s.startswith('#'):
        idx += 1
        continue
    ind = len(line) - len(line.lstrip(' '))
    m = re.match(r'^([A-Za-z0-9_.-]+)\s*:\s*(?:#.*)?$', s)
    if m and ind > h_ind and m.group(1) in targets:
        host_key = m.group(1)
        block_end = idx + 1
        last_yaml_line = idx
        while block_end < h_end:
            nl = lines[block_end]
            ns = nl.strip()
            if not ns:
                block_end += 1
                continue
            if ns.startswith('#'):
                block_end += 1
                continue
            nind = len(nl) - len(nl.lstrip(' '))
            if nind <= ind:
                break
            last_yaml_line = block_end
            block_end += 1

        for k in range(idx, last_yaml_line + 1):
            kl = lines[k]
            ks = kl.strip()
            if not ks or ks.startswith('#'):
                continue
            lines[k] = (" " * ind) + "# " + kl[ind:]
        print(f"   ✅ Đã comment (#) node '{host_key}' trong {inv_path}.")
        idx = last_yaml_line + 1
    else:
        idx += 1

new_text = "".join(lines)
new_data = yaml.load(new_text, Loader=DummyVaultLoader) or {}
new_exec = new_data.get('all', {}).get('children', {}).get('exec_clusters', {}).get('children', {}) or {}
new_cluster_hosts = set((new_exec.get(cluster_name, {}).get('hosts') or {}).keys())

if new_cluster_hosts != expected_remaining:
    sys.stderr.write(
        f"FATAL INVENTORY VALIDATION ERROR: Expected remaining hosts {sorted(expected_remaining)}, "
        f"got {sorted(new_cluster_hosts)}! Aborting without modifying {inv_path}.\n"
    )
    sys.exit(1)

for cname, cval in orig_exec.items():
    if cname == cluster_name:
        continue
    orig_h = set(((cval or {}).get('hosts') or {}).keys())
    new_h = set(((new_exec.get(cname) or {}).get('hosts') or {}).keys())
    if orig_h != new_h:
        sys.stderr.write(f"FATAL INVENTORY VALIDATION ERROR: Unrelated cluster '{cname}' changed! Aborting.\n")
        sys.exit(1)

orig_mode = os.stat(inv_path).st_mode
inv_dir = os.path.dirname(inv_path) or "."
fd, tmp_path = tempfile.mkstemp(prefix=".inventory_remove_", suffix=".yml", dir=inv_dir)
try:
    with os.fdopen(fd, 'w', encoding='utf-8') as tf:
        tf.write(new_text)
    os.chmod(tmp_path, orig_mode)
    os.replace(tmp_path, inv_path)
except Exception:
    if os.path.exists(tmp_path):
        os.unlink(tmp_path)
    raise
PYEOF
    fi
else
    echo "👉 [Bước 3/5] Bỏ qua cập nhật inventory.yml (update_inventory=false)."
fi

# ── BƯỚC 4/5: Đồng bộ cấu hình & Monitor cho các node còn lại ─────────────────
echo ""
cd "${ANSIBLE_CLUSTERS_DIR}"
if [ "${RESTART_PARENT}" = "true" ]; then
    echo "👉 [Bước 4/5] Cập nhật config.json, /tmp/rpc_nodes.json, Monitor và khởi động lại TOÀN BỘ (Parent + Exec)..."
    ./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --restart
elif [ "${RESTART_REMAINING}" = "true" ]; then
    echo "👉 [Bước 4/5] Cập nhật config.json (raft.peers), /tmp/rpc_nodes.json, Monitor và khởi động lại Execution Clusters (--exec-only)..."
    ./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --restart --exec-only
else
    echo "👉 [Bước 4/5] Giữ nguyên cụm trực tuyến 100% không gián đoạn (restart_remaining_nodes=false)..."
    echo "   • Xuất lại cấu hình /tmp/rpc_nodes.json theo inventory mới..."
    ./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --export-config
    echo "   • Đồng bộ và nạp lại Monitor ngầm theo danh sách node mới (không dừng bất kỳ node nào)..."
    ./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --monitor-only
fi

# ── BƯỚC 5/5: Kiểm tra sức khỏe cụm Raft & trạng thái RPC sau khi gỡ ──────────
echo ""
echo "👉 [Bước 5/5] Kiểm tra trạng thái đồng thuận Raft & RPC của các node còn lại..."
cd "${EXEC_DIR}"
check_ok="false"
check_json=""
for attempt in 1 2 3 4 5; do
    if check_json=$(go run ./cmd/tool/rollup_cluster -config "${CLUSTER_REMAINING_JSON}" check 2>&1); then
        check_ok="true"
        break
    fi
    sleep 2
done

if [ "${check_ok}" = "true" ]; then
    python3 - "${check_json}" << 'PYEOF'
import sys, json
raw = sys.argv[1]
start = raw.find('{')
rep = json.loads(raw[start:]) if start >= 0 else {}
replicas = rep.get("replicas", [])
voters = [r["id"] for r in replicas if r.get("voter")]
blocks = [r.get("status", {}).get("last_block") for r in replicas if r.get("reachable")]
print(f"   ✅ Raft Cluster OK: Leader={rep.get('leader')} | Term={rep.get('term')} | Voters={voters} | Block={blocks} | QuorumOK={rep.get('quorum_ok')} | StateRootConsistent={rep.get('state_root_consistent')}")
PYEOF
else
    echo "⚠️  Kết quả kiểm tra Raft sau khi khởi động lại:" >&2
    echo "${check_json}" >&2
    exit 1
fi

echo ""
echo "══════════════════════════════════════════════════════════════════════"
echo "🎉 HOÀN TẤT GỠ NODE! TRẠNG THÁI CỤM HIỆN TẠI:"
echo "══════════════════════════════════════════════════════════════════════"
cd "${ANSIBLE_CLUSTERS_DIR}"
python3 scripts/parse_inventory.py "${INVENTORY_PATH}" status

