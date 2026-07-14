# Review vòng 4: PR #27 — "Fix search ram" (`fix-search-ram` → `dev`)

- **Ngày review:** 2026-07-14
- **Base / Head:** `dev` (1684f38e) ← `fix-search-ram` (**8d780466**)
- **Phạm vi:** 40 file, +1107 / −1542 dòng · `mergeable: true / clean`
- **Link:** https://github.com/x3pi/metanode/pull/27
- **Review trước:** [vòng 1](pr_27_review_fix_search_ram.md) · [vòng 2](pr_27_review_round2_2026-07-14.md) · [vòng 3](pr_27_review_round3_2026-07-14.md)

## 0. Kết luận: **SẴN SÀNG MERGE** (với 1 sửa nhỏ 2 dòng nên làm, và điều kiện chạy test trên máy build đầy đủ)

Vòng 4 (commit `8d780466`) đã xử lý xong **mục bắt buộc duy nhất** của vòng 3 và cả mục nên-sửa chính:

| Mục vòng 3 | Trạng thái |
|---|---|
| **Bắt buộc:** xóa 4 file rác `test_decl*` (gồm 2 binary `.o`) | ✅ **Đã xóa** |
| Nên: thêm abort on-chain vào outer-catch `FullDatabase` V0 | ✅ **Đã thêm** cho `catch (std::exception)` và `catch (...)` — nhưng còn sót 1 nhánh, xem mục 2.1 |
| Theo dõi: search fail-stop, bare-send khi cancel, consumer `MapStorageRead` | ⏳ Chưa động — vẫn là follow-up, không chặn |

## 1. Nội dung mới của vòng 4 (ngoài checklist)

### 1.1 Fix goroutine leak Scrubber trong `chain_state.go` — tốt ✔
Mỗi ChainState ảo (tạo cho `eth_call`/offchain với sentinel `backupPath == "skip_epoch_data"`) trước đây đều khởi động một goroutine Scrubber chạy vĩnh viễn (chu kỳ 24h) → **leak goroutine theo từng eth_call**. Giờ Scrubber chỉ chạy cho node chính. Đã xác minh `"skip_epoch_data"` là sentinel có sẵn, được dùng nhất quán ở toàn bộ 8 call-site tạo ChainState tạm (`debug_api.go`, `transaction_processor_offchain.go`) — fix đúng và có comment giải thích rõ. (Dùng magic string làm cờ thì không đẹp, nhưng đó là convention có sẵn của codebase, không phải lỗi của PR này.)

### 1.2 Xóa 3 utility genesis lỗi thời — an toàn, thực chất là sửa build ✔
`execution/create_genesis.go`, `fix_genesis.go`, `generate_valid_keys.go` — cả 3 đều là `package main` **cùng nằm ở thư mục gốc module**, mỗi file một `func main()` trùng nhau → thư mục gốc `execution/` trước giờ không thể build (`main redeclared`), tức `go build ./...` luôn fail tại đó. Không nơi nào tham chiếu chúng (đã grep). Xóa là đúng và còn giúp `go build ./...` chạy được.

## 2. Còn lại

### 2.1 Sửa 2 dòng (nên làm trước merge): nhánh `catch (const Xapian::Error &)` của V0 vẫn chưa abort
`xapian_handlers.cpp` outer-catch của `FullDatabase` (V0) có 3 nhánh: `Xapian::Error` / `std::exception` / `...`. Vòng 4 thêm abort vào 2 nhánh sau nhưng **bỏ sót nhánh đầu** — và vì `Xapian::Error` **không kế thừa `std::exception`** (thiết kế của Xapian), đây lại chính là nhánh bắt phần lớn lỗi Xapian thật. Kết quả: lỗi Xapian on-chain qua opcode V0 vẫn "im lặng trả `Code(32,0)`" — đúng lỗ hổng mà 2 vòng gần đây cố bịt. (V1 không bị vì outer-catch của nó không có nhánh `Xapian::Error` riêng, nên rơi vào `catch (...)` → abort.) Fix: thêm cùng khối `if (!isOffChain) abort()` vào nhánh `Xapian::Error` của V0.

### 2.2 Follow-up sau merge (không đổi so với vòng 3)
- `XapianSearcher::search` vẫn nuốt `Xapian::Error` nội bộ và trả kết quả rỗng — lỗi I/O trong full-text search on-chain không chạm tới cơ chế abort.
- Bare-send `execIn <- txIndex` trong `execOne`/`validateOne` có thể treo `wg.Wait()` ở edge case cancel + buffer đầy; fix rẻ bằng `select` với `ctx.Done()`.
- `MapStorageRead` chưa có consumer phía Go.
- Ops: node giờ fail-stop (SIGABRT) khi lỗi Xapian on-chain — cần alert restart + quản lý dung lượng/quyền truy cập core dump.

## 3. Kiểm chứng

- `mergeable: true / clean`; không còn file rác/binary trong diff.
- Vòng 3 đã chạy: MVCC tests PASS, `go vet ./pkg/trie/` sạch (code không đổi ở vòng 4).
- **Điều kiện còn lại trước merge (do repo không có CI):** trên máy build đầy đủ chạy `build_release.sh` (cả `--debug-cpp`) + `go build ./... && go test ./pkg/blockchain/tx_processor/...` — bộ test TrueBlockSTM mới của dev phải pass trên code sau merge. Máy review này thiếu CGO linker nên chưa chạy được.

## 4. Tổng kết

Qua 4 vòng, PR đã hội tụ hoàn toàn: mọi mục bắt buộc của các vòng trước đều được xử lý, vòng này còn bổ sung 2 fix có giá trị (goroutine leak Scrubber, dọn duplicate-main). Còn đúng **1 sửa 2 dòng** (mục 2.1 — abort cho nhánh `Xapian::Error` của V0) là nên làm ngay vì nó hoàn thiện chính sách fail-stop mà PR đã cam kết; nếu tác giả chấp nhận rủi ro đó như follow-up thì PR **có thể merge ngay sau khi bộ test pass trên máy build đầy đủ**.
