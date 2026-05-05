#!/bin/bash
echo "Đang lấy Latest Block..."
LATEST=$(curl -s -X POST http://127.0.0.1:8757 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", false],"id":1}' | jq -r '.result.number')

if [ "$LATEST" == "null" ] || [ -z "$LATEST" ]; then
    echo "❌ Không thể kết nối tới Node 0 (http://127.0.0.1:8757)."
    exit 1
fi

LATEST_DEC=$((LATEST))
LATEST_EPOCH=$(curl -s -X POST http://127.0.0.1:8757 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", false],"id":1}' | jq -r '.result.epoch')
echo "Latest Block hiện tại: $LATEST_DEC ($LATEST), Epoch: $LATEST_EPOCH"
echo "-----------------------------------------------------"
echo "🔍 Đang quét LÙI từ block $LATEST_DEC về 1 để tìm SystemTransactions..."

FOUND=0
for ((i=LATEST_DEC; i>0; i--)); do
  HEX=$(printf "0x%x" $i)
  
  # Lấy độ dài mảng result của system transactions
  RES=$(curl -s -X POST http://127.0.0.1:8757 -H "Content-Type: application/json" -d "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getSystemTransactionsByBlockNumber\",\"params\":[\"$HEX\"],\"id\":1}" | jq -r '.result | length' 2>/dev/null)
  
  if [ "$RES" != "0" ] && [ "$RES" != "null" ] && [ -n "$RES" ]; then
    EPOCH=$(curl -s -X POST http://127.0.0.1:8757 -H "Content-Type: application/json" -d "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$HEX\", false],\"id\":1}" | jq -r '.result.epoch')
    
    echo "🎉 BẮT ĐƯỢC RỒI! Block $i ($HEX) chứa $RES System Transaction(s)! (Nằm ở Epoch: $EPOCH)"
    echo "▶️ Nội dung chi tiết (decoded):"
    curl -s -X POST http://127.0.0.1:8757 -H "Content-Type: application/json" -d "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getSystemTransactionsByBlockNumber\",\"params\":[\"$HEX\"],\"id\":1}" | jq '.result[]'
    echo ""
    
    FOUND=$((FOUND+1))
    
    # Dừng sau khi tìm thấy 5 giao dịch System gần nhất
    if [ "$FOUND" -ge 5 ]; then
        echo "✅ Đã tìm thấy đủ 5 block SystemTransaction gần nhất. Kết thúc script."
        break
    fi
  fi
  
  # In tiến độ mỗi 500 block để không bị spam màn hình
  if (( i % 500 == 0 )); then
      echo "Đang quét... tới block $i"
  fi
done

if [ "$FOUND" -eq 0 ]; then
    echo "❌ Quét toàn bộ chain nhưng KHÔNG TÌM THẤY bất kỳ System Transaction nào!"
    echo ""
    echo "🔍 Debug: Kiểm tra epoch của block đầu và cuối..."
    FIRST_EPOCH=$(curl -s -X POST http://127.0.0.1:8757 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x1", false],"id":1}' | jq -r '.result.epoch')
    echo "   Block #1 epoch: $FIRST_EPOCH"
    echo "   Block #$LATEST_DEC epoch: $LATEST_EPOCH"
    if [ "$FIRST_EPOCH" != "$LATEST_EPOCH" ]; then
        echo "   ⚠️ Epoch đã thay đổi ($FIRST_EPOCH → $LATEST_EPOCH) nhưng không tìm thấy SystemTransaction!"
        echo "   💡 Có thể hệ thống cần rebuild với code mới (fix fast-path system TX persistence)."
    fi
else
    echo ""
    echo "✅ Tổng cộng tìm thấy $FOUND block chứa SystemTransaction."
fi
