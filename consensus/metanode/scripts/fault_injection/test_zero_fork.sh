#!/bin/bash
# test_zero_fork.sh - Metanode Fault Injection Chaos Testing
# Mục đích: Đảm bảo nguyên tắc 100% Zero-Fork (thà pending chứ không fork) khi có sự cố mạng.

set -e

# Đảm bảo chạy dưới quyền root cho tc và iptables
if [ "$EUID" -ne 0 ]; then 
  echo "Vui lòng chạy script với quyền root (sudo)"
  exit 1
fi

echo "🚀 Bắt đầu kịch bản Chaos Testing: Tiêm Lỗi Mạng & Giết Node (Zero-Fork Test)"
echo "------------------------------------------------------------------"

function cleanup() {
    echo "🧹 Dọn dẹp: Xóa rules iptables và tc..."
    tc qdisc del dev eth0 root netem 2>/dev/null || true
    iptables -D INPUT -p tcp --dport 50051 -m statistic --mode random --probability 0.5 -j DROP 2>/dev/null || true
}
trap cleanup EXIT

echo "1. Kiểm tra trạng thái Cluster ban đầu..."
# (Giả định có lệnh metanode-cli hoặc curl RPC)
# metanode-cli status

echo ""
echo "🔥 TÌNH HUỐNG 1: Tiêm độ trễ mạng (Network Latency - 2000ms)"
echo "Sử dụng tc (Traffic Control) để delay eth0 thêm 2s"
tc qdisc add dev eth0 root netem delay 2000ms
sleep 10
echo "=> Cần quan sát: Các node sẽ bị chậm nhận CommitVote. Node không được phép timeout và tự ý dispatch block."
echo "=> Cluster sẽ bị pending tạm thời cho đến khi đủ 2f+1 votes đến trễ."
tc qdisc del dev eth0 root netem
echo "✅ Đã gỡ bỏ delay. Cluster phải tự phục hồi và tiếp tục dispatch block."
sleep 5

echo ""
echo "🔥 TÌNH HUỐNG 2: Rớt mạng ngẫu nhiên (Packet Loss - 50%)"
echo "Sử dụng iptables để drop 50% packets trên cổng P2P (VD: 50051)"
iptables -A INPUT -p tcp --dport 50051 -m statistic --mode random --probability 0.5 -j DROP
sleep 15
echo "=> Cần quan sát: Node có thể bị Insufficient votes, nhưng TUYỆT ĐỐI không được fork."
iptables -D INPUT -p tcp --dport 50051 -m statistic --mode random --probability 0.5 -j DROP
echo "✅ Đã gỡ bỏ packet loss. Chờ hội tụ StateRoot..."
sleep 10

echo ""
echo "🔥 TÌNH HUỐNG 3: Random Node Kill (Mất kết nối đột ngột Leader/Validator)"
echo "Kill ngẫu nhiên 1 tiến trình metanode..."
PID_TO_KILL=$(pgrep metanode | shuf -n 1)
if [ -n "$PID_TO_KILL" ]; then
    echo "Đang kill PID: $PID_TO_KILL"
    kill -9 $PID_TO_KILL
    sleep 5
    echo "=> Cần quan sát: Các node còn lại (nếu >= 2f+1) sẽ thay đổi Leader và tiếp tục."
    echo "=> Trạng thái StateRoot trước và sau khi thay Leader PHẢI giống hệt nhau."
else
    echo "Không tìm thấy tiến trình metanode nào."
fi

echo ""
echo "🔍 BƯỚC KIỂM TRA CUỐI: Xác minh StateRoot (Zero-Fork)"
echo "Sử dụng lệnh RPC để fetch StateRoot hiện tại của tất cả các node còn sống."
echo "(Script giả định sẽ chạy API check state root)"
# for port in 8545 8546 8547; do
#    curl -s -X POST http://localhost:$port -H "Content-Type: application/json" --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
# done

echo "🎉 Test hoàn tất! Hãy kiểm tra log của các node xem có xuất hiện thông báo 'FORK' nào không."
