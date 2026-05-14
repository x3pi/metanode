# Luồng Khởi Động & Đồng Bộ Bắt Kịp Mạng (Catch-up Sync) Của Metanode

Tài liệu này mô tả chi tiết quá trình một Node khởi động lại (restart) sau khi bị tắt ngang 1-3 Epoch mà **không sử dụng Snapshot**. Quá trình này bao gồm việc tự chữa lành cơ sở dữ liệu (Self-healing) và đồng bộ tốc độ cao (Catch-up) với mạng lưới.

---

## Giai đoạn 1: Khởi động Go và Tự phục hồi (Self-Healing)
**Vị trí Code:** `execution/cmd/simple_chain/app_blockchain.go` (Hàm `initBlockchain`, dòng 511-532)

Khi bạn khởi động lại Node, tiến trình Go (`go-master`) sẽ khởi chạy đầu tiên. Vì bạn KHÔNG copy `metadata.json` mới vào thư mục, biến `metadata` sẽ bằng `nil`. Hệ thống sẽ đi vào cơ chế kiểm tra tính toàn vẹn của dữ liệu để đối phó với hiện tượng "State Drift" (lệch trạng thái giữa RAM/NOMT và đĩa cứng LevelDB) do sập nguồn đột ngột:

1. **So sánh Root:** Node lấy `startStateRoot` từ LevelDB (chứa block P2P tải về nhưng chưa chắc đã thực thi) và `nomtRoot` từ Database NOMT (chứa trạng thái EVM đã thực sự được thực thi và lưu xuống ổ).
2. **Kích hoạt Fallback (Quét lùi tìm chốt an toàn):** Nếu Node trước đó bị tắt nóng/sập nguồn, LevelDB thường lưu block nhanh hơn NOMT. Dẫn đến `startStateRoot != nomtRoot`. Hệ thống lập tức kích hoạt luồng cứu hộ:

```go
} else if startStateRoot != (e_common.Hash{}) && nomtRoot != startStateRoot {
    logger.Warn("🛡️ [SNAPSHOT FIX] State mismatch! NOMT root=%s, startLastBlock #%d stateRoot=%s. "+
        "LevelDB has P2P-synced blocks beyond executed state. Searching for correct block...", ...)

    found := false
    // Quét ngược từ block hiện tại của LevelDB về quá khứ
    for bn := app.startLastBlock.Header().BlockNumber(); bn > 0; bn-- {
        blkHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(bn)
        if !ok { continue }
        blk, err := blockDatabase.GetBlockByHash(blkHash)
        if err != nil || blk == nil { continue }
        
        // So sánh: Block nào có StateRoot khớp với NOMT Root hiện tại?
        if blk.Header().AccountStatesRoot() == nomtRoot {
            correctedGEI := blk.Header().GlobalExecIndex()
            logger.Warn("🛡️ [SNAPSHOT FIX] ✅ Found matching fallback block #%d (stateRoot=%s, GEI=%d).",
                bn, nomtRoot.Hex()[:18]+"...", correctedGEI)
            // Ghi đè điểm xuất phát của Node về block an toàn này
            app.startLastBlock = blk
            found = true
            break
        }
    }
    if !found {
        logger.Fatal("❌ [SNAPSHOT FIX] Could not find any block matching NOMT root... ")
    }
}
```

3. **Ý nghĩa của đoạn code trên:**
   - Hệ thống tự động lùi `app.startLastBlock` về quá khứ (ví dụ lùi từ block 245 xuống 239) cho đến khi tìm thấy block có dữ liệu khớp hoàn toàn với những gì NOMT đang giữ.
   - Nhờ vậy, Node không bị dính "mớ hỗn độn" của các block đang chạy dở dang. 
4. **Kết quả Giai đoạn 1:** Cấu trúc dữ liệu cục bộ được chữa lành. Điểm xuất phát (GEI) được xác định lại một cách hoàn hảo, Go mở kết nối Unix Socket (FFI) sẵn sàng đón luồng đồng bộ từ Rust.

---

## Giai đoạn 2: Khởi động Rust & Phát hiện tụt hậu (SyncOnly)
**Vị trí Code:** Lớp Consensus (Rust) `consensus/metanode/src/node/`

Ngay sau khi Go ổn định ở mốc GEI (Global Exec Index) an toàn (Ví dụ: 239), thành phần Rust bắt đầu chạy:

1. **Đọc trạng thái từ Go:** Rust gọi qua FFI hỏi Go *"Mày đang ở block bao nhiêu?"* -> Go trả lời là GEI=239, Epoch=2.
2. **Khoá hệ thống (Lock Proposals):** Rust kết nối với các Peer khác trong mạng và nhận ra mốc chung của mạng đã ở GEI=677 (chạy xa tít rồi). Nó lập tức in log `[STARTUP-SYNC] Proposals LOCKED` và chuyển Node sang trạng thái **`SyncOnly`**. Ở trạng thái này, Node chỉ im lặng tải dữ liệu, không tham gia biểu quyết để tránh làm hỏng mạng.
3. **Kéo dữ liệu (Fetch):** Hàm `fetch_commits()` của Rust chạy hết tốc lực tải các khối (Commit) còn thiếu từ Node 2, Node 3 về.

---

## Giai đoạn 3: Bơm dữ liệu qua FFI & Thực thi siêu tốc (Fast-Path)
**Vị trí Code:** Giao tiếp FFI & `execution/cmd/simple_chain/processor/block_processor_sync.go`

Đây là giai đoạn Node "bắt kịp" mạng lưới thực sự:

1. **Bơm khối FFI:** Cứ tải được Commit nào, hàm `send_commits_to_go()` của Rust lại bắn cục byte thẳng qua RAM sang hàm `ExecuteBlockStream()` của Go.
2. **Xử lý tại Go (`processSingleEpochData`):** Go hứng dữ liệu và bắt đầu phân tích:
   - **FAST-PATH (Siêu tốc):** Nếu Commit tải về là rỗng (0 transaction) - điều rất hay xảy ra trong Blockchain - Go sẽ **bỏ qua hoàn toàn bước chạy EVM**. Nó chỉ đơn giản là cộng thêm 1 vào biến đếm GEI và nhảy qua Commit tiếp theo. Nhờ điều này, Node đồng bộ hàng ngàn block rỗng chỉ trong 1 tích tắc.
   - **Xử lý Epoch (Epoch-Inflation Guard):** Nếu Go phát hiện cục dữ liệu ghi nhận Epoch lớn hơn hiện tại (Ví dụ đang ở Epoch 2 nhưng dữ liệu là Epoch 3), nó tự động gọi hàm `CheckAndUpdateEpochFromBlock()` để sang trang lịch sử mới và reset các bộ đếm.
   - **Thực thi giao dịch:** Nếu Commit có giao dịch thật, Go chạy giao dịch đó qua Máy ảo (EVM), cập nhật số dư, và ghi xuống NOMT.

---

## Giai đoạn 4: Hoàn tất đồng bộ & Trở lại làm Validator
**Vị trí Code:** Cả Rust và Go

Quá trình lặp lại vòng lặp ở Giai đoạn 3 với tốc độ chóng mặt (Go GEI advanced: 239 -> 437 -> 677).
1. Khi biến `local_gei` của Go bằng đúng với GEI của toàn mạng lưới (Ví dụ: 677).
2. Rust nhận ra đã đuổi kịp mạng (`Local state in sync`).
3. Rust mở khoá hệ thống (`Proposals UNLOCKED`), tự động rũ bỏ thân phận `SyncOnly` và thăng cấp trở lại làm **`Validator`** (nếu nó nằm trong danh sách uỷ ban Epoch đó).
4. Node bắt đầu biểu quyết cho các block mới như chưa hề có cuộc sập nguồn nào xảy ra.

> **Tổng kết:** Thiết kế FFI kết hợp với cơ chế Fast-path giúp Node bắt kịp quãng thời gian chết 1-3 Epoch chỉ trong vòng 10-15 giây mà không cần phải phụ thuộc vào việc chép Snapshot thủ công.
