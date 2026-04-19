#!/bin/bash

# Script để phân tích log cho transaction cụ thể
TX_HASH="5bd3218fe11e55dceb25be28f4a87936a0d6c9c9f1413e339f42e144c88cd989"
LOG_FILE="${1:-/dev/stdin}"

echo "========================================="
echo "PHÂN TÍCH LOG CHO TRANSACTION:"
echo "$TX_HASH"
echo "========================================="
echo ""

# 1. Kiểm tra xem transaction có được nhận từ Rust không
echo "1️⃣  KIỂM TRA: Transaction có được nhận từ Rust?"
echo "   Tìm: [TX COMMIT], [TX HASH]"
grep -E "\[TX COMMIT\]|\[TX HASH\].*$TX_HASH" "$LOG_FILE" | head -5
if [ $? -eq 0 ]; then
    echo "   ✅ Transaction ĐÃ được nhận từ Rust"
else
    echo "   ❌ Transaction CHƯA được nhận từ Rust (có thể chưa được gửi từ Rust hoặc bị lỗi unmarshal)"
fi
echo ""

# 2. Kiểm tra xem transaction có trong danh sách để xử lý không
echo "2️⃣  KIỂM TRA: Transaction có trong danh sách để xử lý?"
echo "   Tìm: [TX FLOW] Transaction hashes to be processed"
grep -A 100 "\[TX FLOW\] Transaction hashes to be processed" "$LOG_FILE" | grep "$TX_HASH"
if [ $? -eq 0 ]; then
    echo "   ✅ Transaction CÓ trong danh sách để xử lý"
    # Tìm block number
    BLOCK_NUM=$(grep -B 2 "\[TX FLOW\] Transaction hashes to be processed.*$TX_HASH" "$LOG_FILE" | grep "block #" | tail -1 | grep -o "block #[0-9]*" | grep -o "[0-9]*")
    if [ ! -z "$BLOCK_NUM" ]; then
        echo "   📦 Block number: $BLOCK_NUM"
    fi
else
    echo "   ❌ Transaction KHÔNG có trong danh sách để xử lý"
fi
echo ""

# 3. Kiểm tra xem transaction có bị excluded không
echo "3️⃣  KIỂM TRA: Transaction có bị excluded (loại bỏ) không?"
echo "   Tìm: [EXCLUDED TX]"
grep "\[EXCLUDED TX\].*$TX_HASH" "$LOG_FILE"
if [ $? -eq 0 ]; then
    echo "   ⚠️  Transaction BỊ EXCLUDED - sẽ được xử lý ở batch tiếp theo"
else
    echo "   ✅ Transaction KHÔNG bị excluded"
fi
echo ""

# 4. Kiểm tra xem receipt có được tạo không
echo "4️⃣  KIỂM TRA: Receipt có được tạo không?"
echo "   Tìm: [RECEIPT FLOW]"
grep "\[RECEIPT FLOW\].*$TX_HASH" "$LOG_FILE"
if [ $? -eq 0 ]; then
    echo "   ✅ Receipt ĐÃ được tạo"
    # Lấy status
    grep "\[RECEIPT FLOW\].*$TX_HASH" "$LOG_FILE" | grep -o "status=[0-9]*" | head -1
else
    echo "   ❌ Receipt CHƯA được tạo"
fi
echo ""

# 5. Kiểm tra validation - transaction có thiếu receipt không
echo "5️⃣  KIỂM TRA: Transaction có thiếu receipt (validation)?"
echo "   Tìm: [MISSING RECEIPT]"
grep "\[MISSING RECEIPT\].*$TX_HASH" "$LOG_FILE"
if [ $? -eq 0 ]; then
    echo "   ❌ Transaction THIẾU RECEIPT - đã được xử lý nhưng không có receipt"
    # Lấy thông tin chi tiết
    grep -A 1 "\[MISSING RECEIPT\].*$TX_HASH" "$LOG_FILE"
else
    echo "   ✅ Transaction KHÔNG thiếu receipt (hoặc chưa đến bước validation)"
fi
echo ""

# 6. Kiểm tra xem receipt có được thêm vào trie không
echo "6️⃣  KIỂM TRA: Receipt có được thêm vào trie không?"
echo "   Tìm: [RECEIPT TRIE]"
grep "\[RECEIPT TRIE\].*$TX_HASH" "$LOG_FILE"
if [ $? -eq 0 ]; then
    echo "   ✅ Receipt ĐÃ được thêm vào trie"
else
    echo "   ⚠️  Không có log cụ thể về receipt này trong trie (có thể do log tổng quát)"
fi
echo ""

# 7. Tóm tắt
echo "========================================="
echo "TÓM TẮT:"
echo "========================================="
echo ""

FOUND_IN_RUST=$(grep -E "\[TX COMMIT\]|\[TX HASH\].*$TX_HASH" "$LOG_FILE" | wc -l)
FOUND_IN_LIST=$(grep "\[TX FLOW\] Transaction hashes to be processed" "$LOG_FILE" | grep "$TX_HASH" | wc -l)
FOUND_EXCLUDED=$(grep "\[EXCLUDED TX\].*$TX_HASH" "$LOG_FILE" | wc -l)
FOUND_RECEIPT=$(grep "\[RECEIPT FLOW\].*$TX_HASH" "$LOG_FILE" | wc -l)
FOUND_MISSING=$(grep "\[MISSING RECEIPT\].*$TX_HASH" "$LOG_FILE" | wc -l)

echo "📊 Thống kê:"
echo "   - Xuất hiện trong log từ Rust: $FOUND_IN_RUST lần"
echo "   - Xuất hiện trong danh sách xử lý: $FOUND_IN_LIST lần"
echo "   - Bị excluded: $FOUND_EXCLUDED lần"
echo "   - Receipt được tạo: $FOUND_RECEIPT lần"
echo "   - Thiếu receipt (validation): $FOUND_MISSING lần"
echo ""

# 8. Kết luận
echo "========================================="
echo "KẾT LUẬN:"
echo "========================================="
echo ""

if [ $FOUND_IN_RUST -eq 0 ]; then
    echo "❌ Transaction CHƯA được nhận từ Rust"
    echo "   → Có thể: chưa được gửi, bị lỗi unmarshal, hoặc chưa đến bước này"
elif [ $FOUND_EXCLUDED -gt 0 ]; then
    echo "⚠️  Transaction BỊ EXCLUDED"
    echo "   → Sẽ được xử lý ở batch/block tiếp theo"
elif [ $FOUND_MISSING -gt 0 ]; then
    echo "❌ Transaction THIẾU RECEIPT"
    echo "   → Đã được xử lý nhưng không có receipt được tạo"
    echo "   → Cần kiểm tra ProcessTransactions để xem tại sao receipt không được tạo"
elif [ $FOUND_RECEIPT -gt 0 ]; then
    echo "✅ Transaction ĐÃ CÓ RECEIPT"
    echo "   → Receipt đã được tạo thành công"
else
    echo "⚠️  Transaction ĐÃ ĐƯỢC NHẬN nhưng chưa thấy receipt"
    echo "   → Có thể đang trong quá trình xử lý hoặc chưa đến bước tạo receipt"
fi
echo ""

