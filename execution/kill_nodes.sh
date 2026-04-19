#!/bin/bash

echo "Attempting to stop the simple_chain nodes..."
echo "-------------------------------------"

# Tìm PID của MASTER node dựa vào file config của nó
# Cờ -f tìm kiếm trong toàn bộ dòng lệnh, không chỉ tên tiến trình
MASTER_PID=$(pgrep -f "config-master.json")

# Tìm PID của WRITE node 1 dựa vào file config của nó
WRITE1_PID=$(pgrep -f "config-sub-write.json")

# Tìm PID của WRITE node 2 dựa vào file config của nó
WRITE2_PID=$(pgrep -f "config-sub-write-2.json")

# Biến để chứa tất cả các PID cần kill
PIDS_TO_KILL=""

# Kiểm tra và thêm PID vào danh sách nếu tìm thấy
if [ -n "$MASTER_PID" ]; then
  echo "Found MASTER node PID: $MASTER_PID"
  PIDS_TO_KILL="$PIDS_TO_KILL $MASTER_PID"
else
  echo "MASTER node process not found."
fi
sleep 2
if [ -n "$WRITE1_PID" ]; then
  echo "Found WRITE node (1) PID: $WRITE1_PID"
  PIDS_TO_KILL="$PIDS_TO_KILL $WRITE1_PID"
else
  echo "WRITE node (1) process not found."
fi
sleep 2
if [ -n "$WRITE2_PID" ]; then
  echo "Found WRITE node (2) PID: $WRITE2_PID"
  PIDS_TO_KILL="$PIDS_TO_KILL $WRITE2_PID"
else
  echo "WRITE node (2) process not found."
fi

echo "-------------------------------------"

# Loại bỏ khoảng trắng thừa ở đầu nếu có
PIDS_TO_KILL=$(echo "$PIDS_TO_KILL" | sed 's/^ *//g')

# Nếu có PID trong danh sách thì thực hiện kill
if [ -n "$PIDS_TO_KILL" ]; then
  echo "Sending SIGTERM signal to PIDs: $PIDS_TO_KILL"
  # Gửi tín hiệu SIGTERM (mặc định) để yêu cầu dừng bình thường
  kill $PIDS_TO_KILL
  echo "Stop signal sent."
  echo ""
  echo "You can verify if processes stopped using: ps aux | grep simple_chain"
  echo "If they don't stop after a few seconds, you might need to force kill using:"
  echo "kill -9 $PIDS_TO_KILL"
else
  echo "No running simple_chain node processes found (based on config files)."
fi

echo "-------------------------------------"
echo "🧹 Wiping all local test snapshots from previous test runs..."
rm -rf /home/abc/chain-n/mtn-simple-2025/cmd/simple_chain/snapshot_data_node*
echo "✅ Snapshots cleared."
echo "-------------------------------------"
exit 0