# Test: Dual-TX Xapian Commit Deduplication

## Vấn đề cần kiểm chứng

Trong `block_processor_commit.go`, đoạn code sau chỉ gọi `CommitFullDb()` **một lần duy nhất** cho mỗi `mvmId` (contract address) trong cùng 1 block:

```go
if processedMvmIds[mvmId] {
    continue  // ← TX thứ 2 cùng contract bị SKIP!
}
processedMvmIds[mvmId] = true

mvmAPI := mvm.GetMVMApi(mvmId)
mvmAPI.CommitFullDb()  // ← Chỉ commit data của TX đầu tiên?
```

**Câu hỏi**: Nếu TX1 và TX2 đều gọi contract cùng địa chỉ (cùng `mvmId`) trong 1 block,
mỗi tx write document riêng vào Xapian DB → sau block commit, data của TX2 có bị mất không?

## Lý thuyết về cơ chế MVM

Mỗi `mvmAPI` instance giữ một **shared XapianManager** cho cùng contract address.  
Khi TX1 gọi `newDocument()` → write vào in-memory buffer của XapianManager.  
Khi TX2 gọi `newDocument()` → write tiếp vào **cùng buffer** đó.  
`CommitFullDb()` flush toàn bộ buffer → cả TX1 lẫn TX2 data được commit.

→ **Dự đoán**: Data KHÔNG bị mất (buffer tích lũy tất cả writes từ mọi tx).

## Kết quả test

| Scenario | Expected | Actual | Pass? |
|----------|----------|--------|-------|
| TX1 data (slot=0) sau block | có data | ? | ? |
| TX2 data (slot=1) sau block | có data | ? | ? |

## Cách chạy test

```bash
cd /home/abc/nhat/consensus-chain/mtn-simple-2025/cmd/tool/tool-test-chain/test-rpc

# Bước 1: Deploy contract
go run main.go -config config-local.json -data test_parallel_xapian/step1_deploy.json

# Bước 2: Init DB (1 tx riêng)
# → Điền CONTRACT_ADDRESS vào các file step tiếp theo
go run main.go -config config-local.json -data test_parallel_xapian/step2_init.json
#spam
go run test_parallel_xapian/send_dual_tx.go -contract 0xB9A02E18Ba30Cb8675dFAC9A5fD35A7018B71C87 -workers 5

# Bước 4: Verify sau khi block commit
go run main.go -config config-local.json -data test_parallel_xapian/step4_verify.json
```

## Files

- `step1_deploy.json`   → Deploy `DualTxXapianTest` contract
- `step2_init.json`     → Gọi `initDb()` để tạo Xapian DB
- `step3_dual_tx.json`  → Gửi `insertDoc(0, "TX1_data")` + `insertDoc(1, "TX2_data")` **KHÔNG chờ receipt**
- `step4_verify.json`   → Gọi `readDoc(0)` và `readDoc(1)` để kiểm tra data

## Điều kiện BUG xác nhận

Nếu `readDoc(1)` trả về chuỗi rỗng `""` → **BUG CONFIRMED**: data TX2 bị mất do skip CommitFullDb.
