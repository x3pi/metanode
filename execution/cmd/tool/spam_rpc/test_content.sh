#!/bin/bash

# Gửi request và lưu kết quả vào biến
RESPONSE=$(curl -s -X POST http://127.0.0.1:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "mtn_getAccountState",
    "params": [
      "0x781E6EC6EBDCA11Be4B53865a34C0c7f10b6da6e",
      "latest"
    ]
  }')

# 1. Kiểm tra xem có trường 'result' không
# Dùng `jq -e` sẽ thoát với mã lỗi nếu không tìm thấy
echo "Kiểm tra sự tồn tại của trường 'result.address'..."
echo "$RESPONSE" | jq -e '.result.address' > /dev/null
if [ $? -ne 0 ]; then
  echo "LỖI: Không tìm thấy trường 'result.address'!"
  echo "Response: $RESPONSE"
  exit 1
fi

# 2. Lấy giá trị address và kiểm tra
EXPECTED_ADDR="0x781e6ec6ebdca11be4b53865a34c0c7f10b6da6e"
# Lấy giá trị (thêm -r để xóa dấu ngoặc kép)
ACTUAL_ADDR=$(echo "$RESPONSE" | jq -r '.result.address')

echo "Kiểm tra giá trị của 'address'..."
if [ "$ACTUAL_ADDR" == "$EXPECTED_ADDR" ]; then
  echo "THÀNH CÔNG: Dữ liệu trả về chính xác!"
else
  echo "LỖI: Dữ liệu trả về SAI!"
  echo "  Mong đợi: $EXPECTED_ADDR"
  echo "  Thực tế:  $ACTUAL_ADDR"
  exit 1
fi