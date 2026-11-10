#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════════════
#  🚀 METANODE RAFT CLUSTER — FAST SPARSE STATE SYNC & NODE SCALING (add_nodes.sh)
#  Đọc cấu hình từ file YAML (mặc định: deploy/add_nodes/config.yml) & Inventory.
#  Hỗ trợ truyền Sparse File (NOMT 77GB -> 2GB thực tế) có thanh tiến trình (%),
#  tự động giải mã mật khẩu riêng của từng máy (Cloud / LAN / Tiệm Net) và kết nạp Raft.
#
#  CÁC CHỐT CHẶN AN TOÀN (SAFETY GUARDS):
#  1. Bắt buộc mọi node trong `new_nodes` phải có `raft_join_existing_chain: true`
#     và `raft_bootstrap: false` trong inventory (tránh lỗi từ chối khởi động tại
#     raftfeed/node.go:227 khi DB đã có block mà Raft state rỗng).
#  2. Nghiêm cấm `new_nodes` trùng tên với `donor_node` / `existing_nodes`, hoặc
#     trùng cặp `(ansible_host, node_data_dir)` với bất kỳ node nào khác.
#  3. Kiểm tra trước xem các node mới có dữ liệu cũ trong `data/` hoặc `raft/` không:
#     nếu có sẽ thông báo rõ ràng và hỏi `[y/N]` để quyết định xóa hay dừng lại xử lý thủ công.
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

for arg in "$@"; do
    case "$arg" in
        --config=*|-c=*) CONFIG_FILE="${arg#*=}" ;;
        --dry-run)       DRY_RUN="true" ;;
        --yes|-y)        ASSUME_YES="true" ;;
        --help|-h)
            echo "Cách dùng: ./add_nodes.sh [--config=config.yml] [--yes|-y] [--dry-run]"
            exit 0
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

# ── Đọc config.yml + inventory.yml và kiểm tra điều kiện an toàn (Pre-flight) ──
eval "$(python3 - "${DEPLOY_DIR}" "${CONFIG_FILE}" "${VAULT_FILE}" << 'PYEOF'
import sys, os, json, yaml, shlex

deploy_dir, cfg_path, vault_file = sys.argv[1:4]

with open(cfg_path, 'r') as cf:
    cfg = yaml.safe_load(cf) or {}

inv_rel = str(cfg.get('inventory', 'ansible_clusters/inventory.yml'))
inv_path = inv_rel if os.path.isabs(inv_rel) else os.path.normpath(os.path.join(deploy_dir, inv_rel))
if not os.path.exists(inv_path):
    sys.stderr.write(f"ERROR: Inventory file not found: {inv_path}\n")
    sys.exit(1)

cluster_name = str(cfg.get('cluster', 'cluster_1'))
donor_name = str(cfg.get('donor_node', 'exec1_replica3'))
new_nodes = [str(x) for x in (cfg.get('new_nodes') or [])]
existing_nodes = [str(x) for x in (cfg.get('existing_nodes') or [])]
restart_parent = bool(cfg.get('restart_parent_chain', False))
restart_all_exec = bool(cfg.get('restart_all_exec_nodes', True))
catch_up_wait = str(cfg.get('catch_up_wait', '300s'))

if not new_nodes:
    sys.stderr.write(f"ERROR: 'new_nodes' list is empty in {cfg_path}\n")
    sys.exit(1)

if len(set(new_nodes)) != len(new_nodes):
    sys.stderr.write(f"ERROR: Duplicate entries found in 'new_nodes': {new_nodes}\n")
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

class VaultTag(yaml.YAMLObject):
    yaml_tag = '!vault'
    @classmethod
    def from_yaml(cls, loader, node):
        val = loader.construct_scalar(node)
        if vault:
            try:
                return vault.decrypt(val.strip()).decode('utf-8').strip()
            except Exception:
                return ""
        return ""

yaml.SafeLoader.add_constructor('!vault', VaultTag.from_yaml)

with open(inv_path, 'r') as f:
    data = yaml.safe_load(f) or {}

all_vars = data.get('all', {}).get('vars', {}) or {}
exec_children = data.get('all', {}).get('children', {}).get('exec_clusters', {}).get('children', {}) or {}

if cluster_name not in exec_children:
    sys.stderr.write(f"ERROR: Cluster '{cluster_name}' not found in {inv_path}\n")
    sys.exit(1)

cdata = exec_children[cluster_name]
cvars = cdata.get('vars', {}) or {}
chosts = cdata.get('hosts', {}) or {}

hosts_map = {}
for hname, hvars in chosts.items():
    merged = dict(all_vars)
    merged.update(cvars)
    merged.update(hvars or {})
    for k in ('ansible_ssh_pass', 'ansible_become_pass'):
        v = str(merged.get(k, ''))
        if 'ansible_become_pass' in v and k != 'ansible_become_pass':
            merged[k] = str(merged.get('ansible_become_pass', ''))
    hosts_map[hname] = merged

if not existing_nodes:
    existing_nodes = [h for h in chosts.keys() if h not in new_nodes]

# ── Safety Guard 1: Kiểm tra không trùng lặp giữa new_nodes và donor / existing_nodes ──
overlap = set(new_nodes) & (set(existing_nodes) | {donor_name})
if overlap:
    sys.stderr.write(
        f"FATAL SAFETY ERROR: 'new_nodes' ({new_nodes}) overlaps with existing/donor nodes ({sorted(overlap)})! "
        f"Refusing to run to protect existing chain data.\n"
    )
    sys.exit(1)

if donor_name not in existing_nodes:
    sys.stderr.write(f"FATAL SAFETY ERROR: donor_node '{donor_name}' must be one of existing_nodes ({existing_nodes}).\n")
    sys.exit(1)

# ── Safety Guard 2: Kiểm tra mọi new_node đều bật raft_join_existing_chain: true và tắt raft_bootstrap ──
for nn in new_nodes:
    nh = hosts_map.get(nn)
    if not nh:
        sys.stderr.write(f"ERROR: New node '{nn}' not found under cluster '{cluster_name}' in {inv_path}\n")
        sys.exit(1)
    if bool(nh.get('raft_bootstrap', False)):
        sys.stderr.write(
            f"FATAL SAFETY ERROR: New node '{nn}' has raft_bootstrap=true in {inv_path}. "
            f"A joining replica must have raft_bootstrap: false.\n"
        )
        sys.exit(1)
    if not bool(nh.get('raft_join_existing_chain', False)):
        sys.stderr.write(
            f"FATAL CONFIG ERROR: New node '{nn}' is missing 'raft_join_existing_chain: true' in {inv_path}!\n"
            f"Without raft_join_existing_chain: true, simple_chain will refuse to start when its DB has blocks "
            f"and its Raft state is empty (raftfeed/node.go:227).\n"
            f"Please add 'raft_join_existing_chain: true' under '{nn}' in {inv_path}.\n"
        )
        sys.exit(1)

# ── Safety Guard 3: Không cho phép bất kỳ new_node nào trùng (ansible_host, node_data_dir) với node khác ──
seen_host_dirs = {}
for hname, h in hosts_map.items():
    hip = str(h.get('ansible_host', '127.0.0.1')).strip()
    nid = str(h.get('node_id', hname))
    ddir = os.path.normpath(str(h.get('node_data_dir', f"/opt/metanode/{nid}")))
    key = (hip, ddir)
    if key in seen_host_dirs:
        other = seen_host_dirs[key]
        sys.stderr.write(
            f"FATAL SAFETY ERROR: Nodes '{other}' and '{hname}' share the exact same (ansible_host={hip}, "
            f"node_data_dir={ddir}) in {inv_path}! Refusing to run to prevent data loss.\n"
        )
        sys.exit(1)
    seen_host_dirs[key] = hname

def emit_host(prefix, hname):
    h = hosts_map.get(hname)
    if not h:
        sys.stderr.write(f"ERROR: Host '{hname}' not found under cluster '{cluster_name}' in {inv_path}\n")
        sys.exit(1)
    host_ip = str(h.get('ansible_host', '127.0.0.1'))
    conn = str(h.get('ansible_connection', ''))
    port = str(h.get('ansible_port', 22))
    user = str(h.get('ansible_user', 'abc'))
    ssh_pass = str(h.get('ansible_ssh_pass', h.get('ansible_become_pass', '')))
    node_id = str(h.get('node_id', hname))
    data_dir = os.path.normpath(str(h.get('node_data_dir', f"/opt/metanode/{node_id}")))
    raft_port = str(h.get('raft_port', 7110))
    fwd_port = str(h.get('forward_port', 7210))
    print(f"{prefix}_ANODE={shlex.quote(hname)}")
    print(f"{prefix}_ID={shlex.quote(node_id)}")
    print(f"{prefix}_HOST={shlex.quote(host_ip)}")
    print(f"{prefix}_CONN={shlex.quote(conn)}")
    print(f"{prefix}_PORT={shlex.quote(port)}")
    print(f"{prefix}_USER={shlex.quote(user)}")
    print(f"{prefix}_PASS={shlex.quote(ssh_pass)}")
    print(f"{prefix}_DIR={shlex.quote(data_dir)}")
    print(f"{prefix}_RPORT={shlex.quote(raft_port)}")
    print(f"{prefix}_APORT={shlex.quote(fwd_port)}")

print(f"INVENTORY_PATH={shlex.quote(inv_path)}")
print(f"CLUSTER_NAME={shlex.quote(cluster_name)}")
print(f"RESTART_PARENT={'true' if restart_parent else 'false'}")
print(f"RESTART_ALL_EXEC={'true' if restart_all_exec else 'false'}")
print(f"CATCH_UP_WAIT={shlex.quote(catch_up_wait)}")
emit_host("DONOR", donor_name)

secret_file = str(cvars.get('raft_secret_file', all_vars.get('raft_secret_file', '/opt/metanode/raft_secret.key')))
live_members = []
full_members = []
for en in existing_nodes:
    if en in hosts_map:
        eh = hosts_map[en]
        m = {
            "id": str(eh.get('node_id', en)),
            "raft_addr": f"{eh.get('ansible_host', '127.0.0.1')}:{eh.get('raft_port', 7110)}",
            "admin_addr": f"{eh.get('ansible_host', '127.0.0.1')}:{eh.get('forward_port', 7210)}",
        }
        full_members.append(m)
        if en != donor_name:
            live_members.append(m)

with open('/tmp/cluster_live.json', 'w') as lf:
    json.dump({"secret_file": secret_file, "members": live_members}, lf, indent=2)

with open('/tmp/cluster_full.json', 'w') as ff:
    json.dump({"secret_file": secret_file, "members": full_members}, ff, indent=2)

print(f"NEW_COUNT={len(new_nodes)}")
for idx, nn in enumerate(new_nodes):
    emit_host(f"NEW_{idx}", nn)
PYEOF
)"

run_ssh() {
    local user="$1" host="$2" port="$3" pass="$4"
    shift 4
    if [ -n "$pass" ]; then
        SSHPASS="$pass" sshpass -e ssh -o StrictHostKeyChecking=no -p "$port" "${user}@${host}" "$@"
    else
        ssh -o StrictHostKeyChecking=no -p "$port" "${user}@${host}" "$@"
    fi
}

# ── Hàm kiểm tra trước dữ liệu cũ trên các node mới (Hỏi [y/N] ngay từ đầu) ───
check_existing_target_data_upfront() {
    for (( i=0; i<NEW_COUNT; i++ )); do
        local anode_v="NEW_${i}_ANODE" nhost_v="NEW_${i}_HOST" nport_v="NEW_${i}_PORT"
        local nuser_v="NEW_${i}_USER" npass_v="NEW_${i}_PASS" ndir_v="NEW_${i}_DIR"
        local anode="${!anode_v}" host="${!nhost_v}" port="${!nport_v}"
        local user="${!nuser_v}" pass="${!npass_v}" target_dir="${!ndir_v}"

        local info
        info=$(run_ssh "$user" "$host" "$port" "$pass" "
            if { [ -d '${target_dir}/data' ] && [ -n \"\$(ls -A '${target_dir}/data' 2>/dev/null)\" ]; } || \
               { [ -d '${target_dir}/raft' ] && [ -n \"\$(ls -A '${target_dir}/raft' 2>/dev/null)\" ]; }; then
                du -sh '${target_dir}/data' '${target_dir}/raft' 2>/dev/null | tr '\n' ' '
            fi
        ") || { echo "❌ Lỗi kết nối SSH tới ${user}@${host}:${port} khi kiểm tra dữ liệu cũ!" >&2; exit 1; }
        if [ -n "$info" ]; then
            echo ""
            echo "⚠️  [CẢNH BÁO DỮ LIỆU CŨ] Node mới '${anode}' trên máy ${host} đã có sẵn dữ liệu tại:"
            echo "   📂 ${info}"
            if [ "${ASSUME_YES}" != "true" ]; then
                if [ -t 0 ]; then
                    read -r -p "❓ Bạn có muốn XÓA dữ liệu cũ của '${anode}' (${host}:${target_dir}) để đồng bộ dữ liệu mới không? [y/N]: " reply
                    if [[ ! "$reply" =~ ^[Yy]$ ]]; then
                        echo "🛑 Đã dừng lại theo lựa chọn của bạn để tự xử lý thủ công."
                        exit 1
                    fi
                else
                    echo "❌ Phát hiện dữ liệu cũ tại ${host}:${target_dir}. Vui lòng chạy trên terminal để xác nhận [y/N] hoặc thêm cờ --yes (-y)." >&2
                    exit 1
                fi
            else
                echo "   ✅ Đã bật cờ --yes: xác nhận xóa dữ liệu cũ của '${anode}'."
            fi
        fi
    done
}

# ── Hàm làm sạch thư mục đích sau khi tiến trình node mới đã dừng ────────────
clean_target_dir_safe() {
    local user="$1" host="$2" port="$3" pass="$4" anode="$5" target_dir="$6"
    if [ -z "$target_dir" ] || [ "$target_dir" = "/" ]; then
        echo "❌ [AN TOÀN] target_dir không hợp lệ ('${target_dir}')! Hủy thao tác." >&2
        exit 1
    fi
    local running_pid
    # Dùng '[c]onfig.json' để regex pgrep -f chỉ khớp với tiến trình simple_chain thật,
    # không tự khớp với tiến trình shell `bash -c` của phiên SSH.
    running_pid=$(run_ssh "$user" "$host" "$port" "$pass" "pgrep -f '${target_dir}/[c]onfig.json' 2>/dev/null || true")
    if [ -n "$running_pid" ]; then
        echo "❌ [AN TOÀN] Tiến trình của node '${anode}' (${target_dir}) vẫn đang chạy trên ${host} (PID: ${running_pid})! Hủy thao tác." >&2
        exit 1
    fi
    run_ssh "$user" "$host" "$port" "$pass" "
        mkdir -p '${target_dir}/data' '${target_dir}/raft'
        rm -rf '${target_dir}/data/'* '${target_dir}/raft/'*
    "
}

echo "══════════════════════════════════════════════════════════════════════"
echo "🚀 ĐỒNG BỘ DỮ LIỆU & MỞ RỘNG CỤM RAFT (${CLUSTER_NAME})"
echo "   • File Config : ${CONFIG_FILE}"
echo "   • Inventory   : ${INVENTORY_PATH}"
echo "   • Node nguồn  : ${DONOR_ANODE} [ID=${DONOR_ID}] (${DONOR_USER}@${DONOR_HOST}:${DONOR_PORT}, ${DONOR_DIR}/data)"
for (( i=0; i<NEW_COUNT; i++ )); do
    anode_v="NEW_${i}_ANODE"; nid_v="NEW_${i}_ID"; nhost_v="NEW_${i}_HOST"
    nport_v="NEW_${i}_PORT"; nuser_v="NEW_${i}_USER"; ndir_v="NEW_${i}_DIR"
    nrport_v="NEW_${i}_RPORT"; naport_v="NEW_${i}_APORT"
    echo "   • Node mới $((i+1))  : ${!anode_v} [ID=${!nid_v}] (${!nuser_v}@${!nhost_v}:${!nport_v}, raft=${!nrport_v}, fwd=${!naport_v}, dir=${!ndir_v}, join_existing_chain=true ✅)"
done
echo "   • Restart All Exec Nodes: ${RESTART_ALL_EXEC} | Restart Parent Chain: ${RESTART_PARENT}"
if [ "${RESTART_ALL_EXEC}" = "true" ]; then
    echo "   ⚠️  Lưu ý: Bước 4 & 5 sẽ khởi động lại nhanh cụm Execution để cập nhật danh sách raft.peers mới cho mọi node."
fi
echo "══════════════════════════════════════════════════════════════════════"

if [ "${DRY_RUN}" = "true" ]; then
    echo "✅ Kiểm tra cấu hình & các chốt chặn an toàn (--dry-run) hoàn tất!"
    exit 0
fi

# Kiểm tra ngay từ đầu xem các node mới có dữ liệu cũ không và hỏi [y/N] trước khi thao tác
check_existing_target_data_upfront

# ── BƯỚC 1: Mở cổng tường lửa UFW và dừng các node mới ────────────────────────
echo ""
echo "👉 [Bước 1/6] Mở tường lửa UFW (--open-ports) và dừng các node mới..."
cd "${ANSIBLE_CLUSTERS_DIR}"
./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --open-ports --exec-only
for (( i=0; i<NEW_COUNT; i++ )); do
    anode_var="NEW_${i}_ANODE"
    ./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --stop --node="${!anode_var}" --no-monitor --no-notify || true
done

# ── BƯỚC 2: Dừng tạm Follower Donor & Khóa nén Snapshot ──────────────────────
echo ""
echo "👉 [Bước 2/6] Dừng tạm Follower '${DONOR_ANODE}' và giữ Raft Snapshot trên các node đang chạy..."
cd "${ANSIBLE_CLUSTERS_DIR}"
./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --stop --node="${DONOR_ANODE}" --no-monitor --no-notify || true

DONOR_STOPPED="true"
cleanup_donor() {
    if [ "${DONOR_STOPPED}" = "true" ]; then
        echo "🔄 Khởi động lại Follower '${DONOR_ANODE}'..."
        cd "${ANSIBLE_CLUSTERS_DIR}"
        ./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --start --node="${DONOR_ANODE}" --no-monitor --no-notify || true
    fi
}
trap cleanup_donor EXIT

cd "${EXEC_DIR}"
go run ./cmd/tool/rollup_cluster -config /tmp/cluster_live.json hold-snapshots --on on

# ── BƯỚC 3: Truyền dữ liệu Sparse siêu tốc có thanh tiến trình (%) ────────────
DONOR_DATA_DIR="${DONOR_DIR}/data"
FIRST_ANODE="${NEW_0_ANODE}"
FIRST_HOST="${NEW_0_HOST}"
FIRST_PORT="${NEW_0_PORT}"
FIRST_USER="${NEW_0_USER}"
FIRST_PASS="${NEW_0_PASS}"
FIRST_DIR="${NEW_0_DIR}"
FIRST_ID="${NEW_0_ID}"

LOCAL_IPS=$(hostname -I 2>/dev/null || true)
IS_DONOR_LOCAL="false"
if [ "${DONOR_CONN}" = "local" ] || [ "${DONOR_HOST}" = "127.0.0.1" ] || [[ " ${LOCAL_IPS} " == *" ${DONOR_HOST} "* ]]; then
    IS_DONOR_LOCAL="true"
fi

if [ "${IS_DONOR_LOCAL}" = "true" ]; then
    ACTUAL_BYTES=$(du -sb "${DONOR_DATA_DIR}" | awk '{print $1}')
    ACTUAL_HUMAN=$(du -sh "${DONOR_DATA_DIR}" | awk '{print $1}')
    APPARENT_HUMAN=$(du -sh --apparent-size "${DONOR_DATA_DIR}" | awk '{print $1}')
else
    ACTUAL_BYTES=$(run_ssh "${DONOR_USER}" "${DONOR_HOST}" "${DONOR_PORT}" "${DONOR_PASS}" "du -sb ${DONOR_DATA_DIR}" | awk '{print $1}')
    ACTUAL_HUMAN=$(run_ssh "${DONOR_USER}" "${DONOR_HOST}" "${DONOR_PORT}" "${DONOR_PASS}" "du -sh ${DONOR_DATA_DIR}" | awk '{print $1}')
    APPARENT_HUMAN=$(run_ssh "${DONOR_USER}" "${DONOR_HOST}" "${DONOR_PORT}" "${DONOR_PASS}" "du -sh --apparent-size ${DONOR_DATA_DIR}" | awk '{print $1}')
fi

echo ""
echo "👉 [Bước 3/6] Đồng bộ dữ liệu từ ${DONOR_HOST}:${DONOR_DATA_DIR} sang ${FIRST_USER}@${FIRST_HOST}:${FIRST_PORT} (${FIRST_DIR}/data)"
echo "   ℹ️  Dung lượng ảo (NOMT Sparse): ${APPARENT_HUMAN} | Dung lượng thực trên ổ cứng: ${ACTUAL_HUMAN}"
echo "   ⚡ Sử dụng tar --sparse (-S) + zstd (checksum XXH64) + pv để chỉ truyền ${ACTUAL_HUMAN} dữ liệu thực:"

clean_target_dir_safe "${FIRST_USER}" "${FIRST_HOST}" "${FIRST_PORT}" "${FIRST_PASS}" "${FIRST_ANODE}" "${FIRST_DIR}"

if [ "${IS_DONOR_LOCAL}" = "true" ]; then
    tar -C "${DONOR_DATA_DIR}" -cSf - . \
        | pv -p -t -e -r -b -s "${ACTUAL_BYTES}" -N "📦 Truyền DB sang ${FIRST_HOST}" \
        | zstd -1 -T0 --check \
        | run_ssh "${FIRST_USER}" "${FIRST_HOST}" "${FIRST_PORT}" "${FIRST_PASS}" "zstd -d | tar -C ${FIRST_DIR}/data -xSf -"
else
    run_ssh "${DONOR_USER}" "${DONOR_HOST}" "${DONOR_PORT}" "${DONOR_PASS}" "tar -C ${DONOR_DATA_DIR} -cSf - . | zstd -1 -T0 --check" \
        | pv -t -r -b -N "📦 Truyền DB (Nén) ${DONOR_HOST} -> ${FIRST_HOST}" \
        | run_ssh "${FIRST_USER}" "${FIRST_HOST}" "${FIRST_PORT}" "${FIRST_PASS}" "zstd -d | tar -C ${FIRST_DIR}/data -xSf -"
fi

for (( i=1; i<NEW_COUNT; i++ )); do
    nanode_var="NEW_${i}_ANODE"; nhost_var="NEW_${i}_HOST"; nport_var="NEW_${i}_PORT"; nuser_var="NEW_${i}_USER"
    npass_var="NEW_${i}_PASS"; ndir_var="NEW_${i}_DIR"; nid_var="NEW_${i}_ID"
    NANODE="${!nanode_var}"; NHOST="${!nhost_var}"; NPORT="${!nport_var}"; NUSER="${!nuser_var}"
    NPASS="${!npass_var}"; NDIR="${!ndir_var}"; NID="${!nid_var}"

    clean_target_dir_safe "${NUSER}" "${NHOST}" "${NPORT}" "${NPASS}" "${NANODE}" "${NDIR}"

    if [ "${NHOST}" = "${FIRST_HOST}" ] && [ "${NPORT}" = "${FIRST_PORT}" ]; then
        echo "   ⚡ Nhân bản nội bộ trên máy ${NHOST}: ${FIRST_DIR}/data -> ${NDIR}/data ..."
        run_ssh "${NUSER}" "${NHOST}" "${NPORT}" "${NPASS}" \
            "cp -a --sparse=always --reflink=auto ${FIRST_DIR}/data/. ${NDIR}/data/"
        echo "   ✅ Đã nhân bản xong cho ${NID}!"
    else
        echo "   ⚡ Truyền dữ liệu sang máy tiếp theo ${NUSER}@${NHOST}:${NPORT} (${NDIR}/data)..."
        if [ "${IS_DONOR_LOCAL}" = "true" ]; then
            tar -C "${DONOR_DATA_DIR}" -cSf - . \
                | pv -p -t -e -r -b -s "${ACTUAL_BYTES}" -N "📦 Truyền DB sang ${NHOST}" \
                | zstd -1 -T0 --check \
                | run_ssh "${NUSER}" "${NHOST}" "${NPORT}" "${NPASS}" "zstd -d | tar -C ${NDIR}/data -xSf -"
        else
            run_ssh "${DONOR_USER}" "${DONOR_HOST}" "${DONOR_PORT}" "${DONOR_PASS}" "tar -C ${DONOR_DATA_DIR} -cSf - . | zstd -1 -T0 --check" \
                | pv -t -r -b -N "📦 Truyền DB (Nén) ${DONOR_HOST} -> ${NHOST}" \
                | run_ssh "${NUSER}" "${NHOST}" "${NPORT}" "${NPASS}" "zstd -d | tar -C ${NDIR}/data -xSf -"
        fi
    fi
done

# ── BƯỚC 4 & 5: Bật lại Donor & Cập nhật cấu hình qua Ansible ────────────────
echo ""
cd "${ANSIBLE_CLUSTERS_DIR}"
if [ "${RESTART_PARENT}" = "true" ]; then
    echo "👉 [Bước 4 & 5/6] Build, cập nhật config.json và khởi động lại TOÀN BỘ (Parent Chain + Execution Clusters)..."
    ./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --restart
elif [ "${RESTART_ALL_EXEC}" = "true" ]; then
    echo "👉 [Bước 4 & 5/6] Build, cập nhật config.json (danh sách raft.peers mới) và khởi động lại Execution Clusters (--exec-only)..."
    ./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --restart --exec-only
else
    echo "👉 [Bước 4 & 5/6] Chỉ khởi động lại Donor (${DONOR_ANODE}) và các node mới (giữ nguyên các node đang chạy)..."
    ./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --restart --node="${DONOR_ANODE}" --no-monitor --no-notify
    for (( i=0; i<NEW_COUNT; i++ )); do
        anode_var="NEW_${i}_ANODE"
        ./deploy_clusters.sh --inventory="${INVENTORY_PATH}" --restart --node="${!anode_var}" --no-monitor --no-notify
    done
fi
DONOR_STOPPED="false"

# ── BƯỚC 6: Kết nạp các node mới vào nhóm Raft & Nhả khóa Snapshot ───────────
echo ""
echo "👉 [Bước 6/6] Kết nạp các node mới vào cụm Raft (add-replica) và đối chiếu BlockHash..."
cd "${EXEC_DIR}"
for (( i=0; i<NEW_COUNT; i++ )); do
    nid_var="NEW_${i}_ID"; nhost_var="NEW_${i}_HOST"
    nrport_var="NEW_${i}_RPORT"; naport_var="NEW_${i}_APORT"
    NID="${!nid_var}"; NHOST="${!nhost_var}"
    NRPORT="${!nrport_var}"; NAPORT="${!naport_var}"

    echo "   ➕ Đang kết nạp ${NID} (${NHOST}:${NRPORT}, admin: ${NHOST}:${NAPORT})..."
    add_out=""
    if add_out=$(go run ./cmd/tool/rollup_cluster -config /tmp/cluster_full.json add-replica \
        --id "${NID}" \
        --raft-addr "${NHOST}:${NRPORT}" \
        --admin-addr "${NHOST}:${NAPORT}" \
        --catch-up-wait "${CATCH_UP_WAIT}" 2>&1); then
        echo "${add_out}"
        echo "   ✅ ${NID} đã đối chiếu khớp BlockHash 100% với Leader và trở thành Voter!"
    elif [[ "${add_out}" == *"is already a voter"* ]]; then
        echo "   ℹ️  ${NID} đã là Voter chính thức trong cụm Raft (bỏ qua bước thêm lại)."
    else
        echo "${add_out}" >&2
        exit 1
    fi
done

echo ""
echo "🔓 Mở lại chế độ nén Snapshot tự động (hold-snapshots --on off)..."
go run ./cmd/tool/rollup_cluster -config /tmp/cluster_full.json hold-snapshots --on off

echo ""
echo "══════════════════════════════════════════════════════════════════════"
echo "🎉 HOÀN TẤT! TRẠNG THÁI CỤM HIỆN TẠI:"
echo "══════════════════════════════════════════════════════════════════════"
cd "${ANSIBLE_CLUSTERS_DIR}"
python3 scripts/parse_inventory.py "${INVENTORY_PATH}" status
