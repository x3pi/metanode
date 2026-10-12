#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════════════
# 🛠️ METANODE LOCAL NODE MANAGER
# Quản lý trực tiếp các dịch vụ systemd của MetaNode trên máy cục bộ
# Không cần gọi qua Ansible, không phụ thuộc vault password.
# ═══════════════════════════════════════════════════════════════════════════════

set -e

ACTION="${1:-status}"
FILTER="${2:-all}"

print_banner() {
    echo "═══════════════════════════════════════════════════════════════"
    echo "🖥️  METANODE LOCAL NODE CONTROLLER ($(hostname -I | awk '{print $1}'))"
    echo "═══════════════════════════════════════════════════════════════"
}

get_services() {
    local filter="$1"
    local services=()

    # Tìm các file service metanode đã deploy trên máy
    local found_services
    found_services=$(systemctl list-unit-files "metanode-*.service" --no-legend 2>/dev/null | awk '{print $1}' || true)

    if [ -z "$found_services" ]; then
        # Fallback tìm trong /etc/systemd/system
        found_services=$(ls /etc/systemd/system/metanode-*.service 2>/dev/null | xargs -n1 basename 2>/dev/null || true)
    fi

    for s in $found_services; do
        case "$filter" in
            all|"")
                services+=("$s")
                ;;
            parent|root|parent_chain)
                if [[ "$s" == *"parent"* ]]; then
                    services+=("$s")
                fi
                ;;
            exec|child|exec_cluster|cluster)
                if [[ "$s" == *"exec"* ]]; then
                    services+=("$s")
                fi
                ;;
            *)
                # Khớp theo tên cụ thể (vd: exec1_r1, parent_node_0, 0, r1...)
                if [[ "$s" == *"$filter"* ]]; then
                    services+=("$s")
                fi
                ;;
        esac
    done

    echo "${services[@]}"
}

usage() {
    print_banner
    echo "Cách dùng: $0 [HÀNH ĐỘNG] [BỘ LỌC]"
    echo ""
    echo "⚡ Hành Động (Actions):"
    echo "  status      (Mặc định) Kiểm tra trạng thái các node trên máy này"
    echo "  start       Khởi động dịch vụ"
    echo "  stop        Dừng dịch vụ"
    echo "  restart     Khởi động lại dịch vụ"
    echo "  logs [NAME] Xem log trực tiếp của 1 service qua journalctl"
    echo ""
    echo "🎯 Bộ Lọc (Filters):"
    echo "  all         (Mặc định) Áp dụng cho mọi node MetaNode trên máy"
    echo "  parent      Chỉ áp dụng cho các node Parent Chain trên máy"
    echo "  exec        Chỉ áp dụng cho các node Execution Cluster trên máy"
    echo "  <NAME>      Tên node cụ thể (vd: parent_node_0, exec1_replica1, r2...)"
    echo ""
    echo "Ví dụ:"
    echo "  $0 status"
    echo "  $0 stop all"
    echo "  $0 start parent"
    echo "  $0 restart exec1_replica1"
    echo "  $0 logs exec1_replica1"
    exit 0
}

if [[ "$ACTION" =~ ^(-h|--help|help)$ ]]; then
    usage
fi

SERVICES=($(get_services "$FILTER"))

if [ ${#SERVICES[@]} -eq 0 ]; then
    print_banner
    echo "⚠️  Không tìm thấy service MetaNode nào phù hợp với bộ lọc: '$FILTER'"
    echo "   Các service hiện có trên hệ thống:"
    systemctl list-unit-files "metanode-*.service" --no-legend 2>/dev/null || echo "   (Chưa có service nào được cài đặt)"
    exit 0
fi

case "$ACTION" in
    status)
        print_banner
        echo "📊 Danh sách dịch vụ MetaNode trên máy cục bộ (Bộ lọc: $FILTER):"
        echo ""
        printf "%-35s %-12s %-15s\n" "SERVICE" "LOAD" "ACTIVE"
        echo "─────────────────────────────────────────────────────────────────"
        for s in "${SERVICES[@]}"; do
            active_state=$(systemctl is-active "$s" 2>/dev/null || echo "inactive")
            if [ "$active_state" = "active" ]; then
                state_fmt="\033[0;32m✅ active (running)\033[0m"
            elif [ "$active_state" = "failed" ]; then
                state_fmt="\033[0;31m❌ failed\033[0m"
            else
                state_fmt="\033[0;33m⏸️  inactive (stopped)\033[0m"
            fi
            printf "%-35s %b\n" "$s" "$state_fmt"
        done
        echo ""
        ;;

    start)
        print_banner
        echo "▶️  Đang khởi động ${#SERVICES[@]} dịch vụ (Bộ lọc: $FILTER)..."
        for s in "${SERVICES[@]}"; do
            echo "   • Starting $s..."
            sudo systemctl start "$s"
        done
        echo "✅ Hoàn tất khởi động."
        ;;

    stop)
        print_banner
        echo "🛑 Đang dừng ${#SERVICES[@]} dịch vụ (Bộ lọc: $FILTER)..."
        for s in "${SERVICES[@]}"; do
            echo "   • Stopping $s..."
            sudo systemctl stop "$s" || true
        done
        echo "✅ Hoàn tất dừng dịch vụ."
        ;;

    restart)
        print_banner
        echo "🔄 Đang khởi động lại ${#SERVICES[@]} dịch vụ (Bộ lọc: $FILTER)..."
        for s in "${SERVICES[@]}"; do
            echo "   • Restarting $s..."
            sudo systemctl restart "$s"
        done
        echo "✅ Hoàn tất khởi động lại."
        ;;

    logs)
        TARGET="${FILTER}"
        if [ "$TARGET" = "all" ] || [ -z "$TARGET" ]; then
            TARGET="${SERVICES[0]}"
        fi
        if [[ "$TARGET" != metanode-* ]]; then
            TARGET="metanode-${TARGET}"
        fi
        if [[ "$TARGET" != *.service ]]; then
            TARGET="${TARGET}.service"
        fi
        echo "📜 Xem live logs của $TARGET (Ctrl+C để thoát):"
        sudo journalctl -u "$TARGET" -f -n 50
        ;;

    *)
        echo "❌ Hành động không hợp lệ: '$ACTION'"
        echo "Sử dụng $0 --help để xem hướng dẫn."
        exit 1
        ;;
esac
