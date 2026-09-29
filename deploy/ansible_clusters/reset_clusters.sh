#!/usr/bin/env bash
set -e

SUDO_PASS="1234@abcd"

echo "1. Dừng toàn bộ tiến trình Parent Chain và Execution Clusters..."
echo "$SUDO_PASS" | sudo -S pkill -9 -f "/opt/metanode/bin/simple_chain" 2>/dev/null || true
echo "$SUDO_PASS" | sudo -S pkill -9 -f "/opt/metanode/bin/parent_chain" 2>/dev/null || true
sleep 2

echo "2. Xóa sạch dữ liệu DB cũ để đảm bảo Zero State Drift..."
echo "$SUDO_PASS" | sudo -S rm -rf /opt/metanode/exec1_r1/data /opt/metanode/exec1_r1/logs /opt/metanode/exec1_r1/snapshot /opt/metanode/exec1_r1/raft /opt/metanode/exec1_r1/explorer /opt/metanode/exec1_r1/explorer-ro /opt/metanode/exec1_r1/backup
echo "$SUDO_PASS" | sudo -S rm -rf /opt/metanode/exec1_r2/data /opt/metanode/exec1_r2/logs /opt/metanode/exec1_r2/snapshot /opt/metanode/exec1_r2/raft /opt/metanode/exec1_r2/explorer /opt/metanode/exec1_r2/explorer-ro /opt/metanode/exec1_r2/backup
echo "$SUDO_PASS" | sudo -S rm -rf /opt/metanode/exec1_r3/data /opt/metanode/exec1_r3/logs /opt/metanode/exec1_r3/snapshot /opt/metanode/exec1_r3/raft /opt/metanode/exec1_r3/explorer /opt/metanode/exec1_r3/explorer-ro /opt/metanode/exec1_r3/backup
echo "$SUDO_PASS" | sudo -S rm -rf /opt/metanode/exec2/data /opt/metanode/exec2/logs /opt/metanode/exec2/snapshot /opt/metanode/exec2/raft /opt/metanode/exec2/explorer /opt/metanode/exec2/explorer-ro /opt/metanode/exec2/backup
echo "$SUDO_PASS" | sudo -S rm -rf /opt/metanode/parent_chain/parentchain_db /opt/metanode/parent_chain/consensus

echo "$SUDO_PASS" | sudo -S mkdir -p /opt/metanode/exec1_r1/data /opt/metanode/exec1_r1/logs /opt/metanode/exec1_r1/snapshot /opt/metanode/exec1_r1/raft /opt/metanode/exec1_r1/explorer
echo "$SUDO_PASS" | sudo -S mkdir -p /opt/metanode/exec1_r2/data /opt/metanode/exec1_r2/logs /opt/metanode/exec1_r2/snapshot /opt/metanode/exec1_r2/raft /opt/metanode/exec1_r2/explorer
echo "$SUDO_PASS" | sudo -S mkdir -p /opt/metanode/exec1_r3/data /opt/metanode/exec1_r3/logs /opt/metanode/exec1_r3/snapshot /opt/metanode/exec1_r3/raft /opt/metanode/exec1_r3/explorer
echo "$SUDO_PASS" | sudo -S mkdir -p /opt/metanode/exec2/data /opt/metanode/exec2/logs /opt/metanode/exec2/snapshot /opt/metanode/exec2/raft /opt/metanode/exec2/explorer

# Copy fresh binaries
echo "3. Cập nhật binaries mới nhất..."
echo "$SUDO_PASS" | sudo -S cp /home/abc/chain-n/metanode/execution/scripts/test/parent_chain /opt/metanode/bin/parent_chain
echo "$SUDO_PASS" | sudo -S cp /home/abc/chain-n/metanode/execution/scripts/test/simple_chain /opt/metanode/bin/simple_chain
echo "$SUDO_PASS" | sudo -S chmod 755 /opt/metanode/bin/parent_chain /opt/metanode/bin/simple_chain

# Start Parent Chain
echo "4. Khởi chạy Parent Chain (:8547)..."
echo "$SUDO_PASS" | sudo -S bash -c "cd /opt/metanode/parent_chain && nohup /opt/metanode/bin/parent_chain -data-dir /opt/metanode/parent_chain -http :8547 -rust-config /opt/metanode/parent_chain/node_parent.toml >> /var/log/metanode/parent_chain.log 2>&1 & echo \$! > /opt/metanode/parent_chain/parent_chain.pid"

# Wait for Parent Chain
for i in {1..20}; do
  if curl -s http://127.0.0.1:8547/inbound >/dev/null 2>&1; then
    echo "   Parent Chain đã online!"
    break
  fi
  sleep 0.5
done

# Start Exec 1 Replicas
echo "5. Khởi chạy Cluster 1 (Exec 1 - 3 Replicas)..."
echo "$SUDO_PASS" | sudo -S bash -c "cd /opt/metanode/exec1_r1 && nohup env PARENT_CHAIN_URL='http://127.0.0.1:8547' /opt/metanode/bin/simple_chain -config /opt/metanode/exec1_r1/config.json >> /var/log/metanode/exec1_replica1.log 2>&1 & echo \$! > /opt/metanode/exec1_r1/cluster.pid"
echo "$SUDO_PASS" | sudo -S bash -c "cd /opt/metanode/exec1_r2 && nohup env PARENT_CHAIN_URL='http://127.0.0.1:8547' /opt/metanode/bin/simple_chain -config /opt/metanode/exec1_r2/config.json >> /var/log/metanode/exec1_replica2.log 2>&1 & echo \$! > /opt/metanode/exec1_r2/cluster.pid"
echo "$SUDO_PASS" | sudo -S bash -c "cd /opt/metanode/exec1_r3 && nohup env PARENT_CHAIN_URL='http://127.0.0.1:8547' /opt/metanode/bin/simple_chain -config /opt/metanode/exec1_r3/config.json >> /var/log/metanode/exec1_replica3.log 2>&1 & echo \$! > /opt/metanode/exec1_r3/cluster.pid"

# Start Exec 2
echo "6. Khởi chạy Cluster 2 (Exec 2)..."
echo "$SUDO_PASS" | sudo -S bash -c "cd /opt/metanode/exec2 && nohup env PARENT_CHAIN_URL='http://127.0.0.1:8547' /opt/metanode/bin/simple_chain -config /opt/metanode/exec2/config.json >> /var/log/metanode/exec2_replica1.log 2>&1 & echo \$! > /opt/metanode/exec2/cluster.pid"

# Wait for RPC ports
echo "7. Chờ các cổng RPC sẵn sàng..."
for port in 8646 8647; do
  for i in {1..20}; do
    if curl -s -X POST http://127.0.0.1:$port -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | grep -q "result"; then
      echo "   Cổng :$port đã sẵn sàng!"
      break
    fi
    sleep 0.5
  done
done

echo "✅ Reset hoàn tất! Tất cả các cụm node đều đã sẵn sàng hoạt động!"
