# Kế hoạch giao Gemini: Bounded Mapping Cache (2026-10-08)

**Nguồn:** `note/design_bounded_memory_indexes_20261007.md`, `note/perf_rss_investigation_20261007.md` mục 7.3, evidence `note/evidence/perf_rss_20261007/ttl_saturation_45min*`.
**Mục tiêu:** chặn trần RAM của `txHashToBlockNumberMap` và `ethHashMapBlsHashMap` (`execution/pkg/blockchain/blockchain.go`), bỏ quét prune O(N) mỗi phút. Giữ nguyên hành vi đọc nhờ fallback Pebble đã có sẵn.

## Quy tắc bắt buộc (đọc `AGENTS.md` trước)
- KISS/YAGNI: chỉ sửa hai struct map và hàm prune của chúng. Không thêm interface, worker, thư viện ngoài.
- Không đụng consensus, EVM execution, state root, trie. Nếu grep cho thấy có caller nằm ngoài RPC/Debug API thì DỪNG và báo user.
- Chọn **Phương Án 2 (two-generation map, Go thuần)** theo khuyến nghị của design doc. Đổi sang phương án khác chỉ khi có lý do, và phải ghi lý do.
- Không dùng số liệu bịa. Mọi con số trong báo cáo phải đến từ lệnh đã chạy thật, kèm log lưu trong `note/evidence/`. Agent trước từng báo PASS/benchmark sai: tự chạy lại, đừng tin báo cáo cũ.
- `git add` theo tên file cụ thể, không `git add <dir>`/`-A` (worktree dùng chung với agent khác).
- Comment code bằng tiếng Anh. Kết thúc response bằng khối tóm tắt tiếng Việt (AGENTS.md Part 5).

## Bước 0 — Sửa tài liệu (làm trước, không đổi code)
Trong `note/perf_rss_investigation_20261007.md` mục 7.3 và `note/design_bounded_memory_indexes_20261007.md` mục 2.3:
1. Thay "chứng minh dứt khoát / củng cố dứt khoát" bằng "giả thuyết phù hợp với dữ liệu; chưa xác nhận bằng heap profile".
2. Ghi chú: Cửa sổ 2 có R²=0.1566 (nhiễu), HeapAlloc đỉnh 626 MB > điểm cuối 608 MB; ngưỡng 5%/0.5 MB/phút là tiêu chí đặt chặt.
3. Ghi chú RSS (~7.4→9.5 GB) lớn hơn HeapAlloc rất nhiều nên hai map này không giải thích được toàn bộ RSS.
4. Chạy lại tính slope từ CSV để xác nhận: W1=4.1993, W2=0.7383 (đã được kiểm lại độc lập).
5. Commit riêng: `docs(perf): soften unverified Go map bucket claim, note noise in TTL experiment`.

## Bước 1 — Impact analysis
1. Đọc `PROJECT_STRUCTURE.md`.
2. Dùng codegraph (`codegraph_callers`/`codegraph_impact`), fallback `grep -rn` cho: `txHashToBlockNumber`, `ethHashMapBlsHash`, `GetBlockNumberByTxHashFast`, `GetEthHashMapblsHash`, `pruneTxHashCache`, `pruneEthHashCache`, `mappingCacheTTL`.
3. Đối chiếu với danh sách caller ở design doc mục 3.2; báo cáo caller mới nếu có.
4. Xác nhận các hàm `Store/StoreBatch/Load/Delete` ghi ra Pebble (`storeBatchToDirty`, `Commit`) độc lập với map RAM, tức là bỏ map vẫn không mất dữ liệu.

## Bước 2 — Cài đặt (Phương Án 2)
- Mỗi map có `current` và `old` (cùng kiểu `map[common.Hash]V`), `MaxEntries` là hằng số (đề xuất 50_000, bỏ `addedAt`/`time.Time` nếu không còn cần để giảm ~24 byte/entry).
- `Store`: ghi vào `current`; nếu `len(current) >= MaxEntries` thì `old = current; current = make(map, MaxEntries)`.
- `Load`: tìm `current`, sau đó `old` (hit ở `old` thì nạp lên `current`); miss thì trả về false để caller fallback Pebble như hiện tại.
- `Delete` xóa ở cả hai map. Vẫn dùng `sync.RWMutex` hiện có.
- Xóa `pruneTxHashCache`/`pruneEthHashCache` và các hằng số `mappingCacheTTL` nếu không còn ai dùng (kiểm tra bằng grep). Không động vào `txCacheTTL`, `blockCacheTTL` (`txsCache` là việc khác, ghi vào "ngoài phạm vi").
- Giữ nguyên chữ ký hàm công khai. Không đổi type ở upstream/downstream.

## Bước 3 — Test
- Unit test trong `execution/pkg/blockchain/`: 
  - đầy `MaxEntries` thì swap, entry cũ vẫn đọc được từ `old`, sau hai lần swap thì mất khỏi RAM nhưng `GetBlockNumberByTxHashFast` vẫn trả đúng nhờ fallback Pebble;
  - `len(current)+len(old) <= 2*MaxEntries` sau khi Store 10×MaxEntries;
  - test concurrent Read/Write với `go test -race`.
- Chạy lại `mapping_pebble_fallback_test.go` và `mapping_rebuild_test.go` (phải còn pass).
- Lệnh: `cd execution && go test -race ./pkg/blockchain/...`. Lưu log vào `note/evidence/bounded_mapping_cache_20261008/`.

## Bước 4 — Build check
`cd consensus/metanode/scripts && ./build_check.sh`. Phải sạch lỗi và warning (Go + Rust + FFI). Sửa mọi vấn đề phát sinh.

## Bước 5 — Kiểm chứng bộ nhớ (thực nghiệm thật, có thể giao user chạy)
- Dùng lại `run_ttl_experiment.py` với `ENABLE_DEBUG_PPROF=true`, tải 50 tx/s trong 45 phút trên cụm cô lập, wipe data devnet trước khi chạy.
- Tiêu chí ghi TRƯỚC khi chạy (không đổi sau khi thấy kết quả): HeapAlloc slope W2 ≤ 0.5 MB/phút hoặc ≤ 5% W1; ghi thêm R² và thu `pprof heap` ở phút 10/30/45 để xác nhận map không còn là top alloc.
- Chạy ít nhất 2 lần để xem độ biến thiên. Nếu FAIL, báo kết quả thật, không chỉnh tiêu chí.
- Kiểm tra RPC: chạy `verify_pebble_fallback_rpc.py` (cold cache) và benchmark đọc `eth_getTransactionReceipt` trước/sau để chắc không suy giảm đáng kể.

## Bước 6 — Hoàn tất
- Cập nhật `PROJECT_STRUCTURE.md` nếu đổi file/struct quan trọng; cập nhật `design_bounded_memory_indexes_20261007.md` mục Trạng thái sang "Implemented (Phương Án 2)" kèm evidence thật.
- Commit nhỏ, tách code và docs. Không push khi chưa có user duyệt; không merge qua squash (xem lịch sử PR #162).

## Ngoài phạm vi (ghi nhận, không làm)
- `AddTxToCache`/`txsCache` là nguồn alloc thứ ba trong profile; xử lý ở task riêng.
- RSS ~9.5 GB: cần heap/goroutine/mmap profile riêng (NOMT, Pebble block cache, Rust) để tìm phần chiếm lớn nhất.

## Tiêu chí hoàn thành
- [ ] Doc đã chỉnh (Bước 0)
- [ ] Impact analysis ghi rõ, không có caller ngoài RPC/Debug
- [ ] `go test -race` pass, log lưu trong evidence
- [ ] `build_check.sh` sạch
- [ ] Thí nghiệm 45 phút ×2 với tiêu chí ghi trước, kết quả thật
- [ ] Không thay đổi hành vi consensus/EVM
