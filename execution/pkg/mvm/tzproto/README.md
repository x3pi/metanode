# `tzproto` — CA↔TA Wire Protocol Design (GĐ1)

> Deliverable của Giai đoạn 1, `note/tee_dual_mode_execution_plan.md`. Đây là thiết kế
> **trên giấy**: `mvm_tz_protocol.h` chưa được build/link vào bất kỳ target nào — GĐ2 (loopback
> trên x86) là nơi đầu tiên nó thực sự được dùng.

## 1. Trạng thái máy (state machine)

Kênh giao tiếp có đúng 1 slot request/response (không phải ring N-slot) vì quyết định **tuần
tự hóa** đã chốt trong kế hoạch — tại một thời điểm chỉ có 1 phiên gọi đang hoạt động, do Go
host giữ 1 mutex bọc quanh toàn bộ lời gọi vào TZ engine.

```
Host (Go)                          TA (C++)
──────────                          ────────
ghi header+blob (forward cmd)
direction = HOST_TO_TA
request_ready = true  ──────────►  poll request_ready
                                    đọc cmd, xử lý

                    ◄── (nếu cần đọc state / gọi HTTP / …)
                        ghi header+blob (reverse cmd)
                        direction = TA_TO_HOST
                        response_ready = false
                        request_ready = true   (dùng lại field request_ready
                                                 cho hướng ngược — 1 kênh
                                                 song công bán phần, không
                                                 cần field riêng)
poll request_ready (thấy direction=TA_TO_HOST)
xử lý reverse call
ghi response, response_ready = true ──────►    poll response_ready
                                                đọc response, tiếp tục thực thi
                                                (có thể lặp lại nhiều reverse
                                                call trước khi xong)

TA xong, ghi response cuối (forward cmd)
response_ready = true              ────────►  (không áp dụng — hướng ngược)
poll response_ready
đọc response, phiên kết thúc
```

Đây đúng là mô hình đã chứng minh trên phần cứng của `tz-llm-trustzone`
(`all_ring_buffer`'s `request_ready`/`final_answer_ready`), chỉ khác là ở đây có **2 chiều**
lặp lại nhiều lần trong 1 phiên (vì 1 lệnh forward có thể kéo theo N lệnh reverse), không phải
1 request → 1 response như model đó.

## 2. Vì sao không cần xử lý đặc biệt cho `status==3` (Block-STM suspend)

Đây là điểm rủi ro cao nhất được nêu trong kế hoạch GĐ0/GĐ1 — đã xác nhận bằng cách đọc trực
tiếp `my_global_state.cpp` và `true_block_stm.go`:

1. `GlobalStateGet`/`GetStorageValue` trả `status==3`/`STORAGE_SUSPEND` → C++ **throw ngay tại
   chỗ** (`Exception(ET::ErrExecutionReverted, "Block-STM: Estimate Hit (Suspend)")`,
   `my_global_state.cpp:84,134,203,390`).
2. Exception này unwind lên `run()` (`mvm_linker.cpp:211`) — **1 catch-block chung duy nhất**,
   không phân biệt "suspend thật" với "revert thật". Nonce vẫn tăng, logger vẫn flush — y hệt
   một revert bình thường.
3. `processResult()` tính `apply_to_cache = (er==returned || er==halted)` — `false` khi
   `er==threw`, nên diff KHÔNG được ghi vào `State` cache. Nhưng mảng write-set trong
   `ExecuteResult` vẫn có thể chứa dữ liệu tích lũy trước điểm throw.
4. **Tín hiệu "cần thử lại" thật sự không hề được đọc lại từ `ExecuteResult`.** Nó là 1
   side-channel thuần Go: khi `GlobalStateGet`'s Go implementation (`mvm_api.go:1390-1392`)
   phát hiện `mvcc.ErrEstimateHit`, nó set `mvccDB.BlockingVersion` **ngay tại thời điểm trả
   lời reverse-call** — trước khi exception phía C++ kịp unwind. `true_block_stm.go:744-767`
   kiểm tra `BlockingVersion != BaseVersion` ngay sau khi lệnh forward trả về, và nếu có, bỏ
   hẳn `exRs` (return trước khi build receipt) — không bao giờ đọc write-set của lần thực thi
   bị suspend.

**Kết luận:** TA chỉ cần mô phỏng đúng hành vi C++ hôm nay (throw ngay khi nhận `status==3`,
trả `ExecuteResult` bình thường dù có thể chứa diff dở dang) — không cần thêm bất kỳ trường
hay lệnh giao thức nào riêng cho tín hiệu này. Go phía host đã biết phải bỏ kết quả từ chính
hành động của nó khi trả lời reverse-call, độc lập với TA trả gì về sau đó.

**Rủi ro thật duy nhất còn lại:** buffer ghi Xapian theo tx (`ExtensionGetOrCreateSimpleDb`
SET) phải được `ClearXapianTxBuffer` xóa **trước mỗi lần thử lại** — đã có sẵn trong code Go
hôm nay (`true_block_stm.go:741`, gọi vô điều kiện trước mỗi lần gọi forward, không phải phản
ứng sau suspend). Giao thức mới chỉ cần giữ nguyên đúng lệnh này (`MVM_TZ_CMD_CLEAR_XAPIAN_TX_BUFFER`),
không cần thiết kế lại.

## 3. Bảng lệnh đầy đủ

| Command | Hướng | Đối chiếu cgo hôm nay | Trạng thái |
|---|---|---|---|
| `CALL`/`EXECUTE`/`DEPLOY`/`SEND_NATIVE`/`PROCESS_NATIVE_MINT_BURN`/`NONCE_PLUS_ONE` | Host→TA | `mvm_api.go:811,904,1283,1098,1154,1202` | Sống, đủ 6/7 forward |
| `EXECUTE_BATCH` | Host→TA | `mvm_api.go:1030` | **Dead trong Go hôm nay** — không có caller thật; giữ trong header cho đủ interface, ưu tiên thấp nhất ở GĐ2 |
| `CLEAR_ALL_STATE_INSTANCES` | Host→TA | `mvm_api.go:199,1654` | Sống |
| `UPDATE_STATE_NONCE`/`UPDATE_STATE_BALANCE` | Host→TA | `mvm_api.go:207,217` | Sống |
| `CLEAR/COMMIT_XAPIAN_TX_BUFFER(_BATCH)` | Host→TA | `mvm_api.go:1666,1675,1696,1717` | Sống |
| `COMMIT_ALL_XAPIAN` | Host→TA | `mvm_api.go:1658` | Sống |
| `REPLAY_FULL_DB_LOGS` | Host→TA | `mvm_api.go:190` | Sống |
| `GLOBAL_STATE_GET` | TA→Host | `mvm_api.go:1331` | Sống, mang tín hiệu Block-STM suspend (mục 2) |
| `GET_STORAGE_VALUE` | TA→Host | `mvm_api.go:1459` | Sống, cùng cơ chế suspend |
| `EXTENSION_CALL_GET_API` | TA→Host | `extension.go:43` | Sống — **HTTP GET thật**, nguồn không-determinism có từ trước |
| `EXTENSION_EXTRACT_JSON_FIELD` | TA→Host | `extension.go:96` | Sống |
| `EXTENSION_BLST` | TA→Host | `extension.go:151` | Sống |
| `EXTENSION_GET_OR_CREATE_SIMPLE_DB` | TA→Host | `extension.go:287` | Sống — B3 mở lại, không cần Merkle-witness (phạm vi không đổi mô hình tin cậy) |
| `XAPIAN_FILE_OPEN/READ/WRITE/FLUSH_CLOSE` | TA→Host | *(mới)* | Cần vì Xapian vào TA + TA không có filesystem POSIX |

**Đã loại khỏi giao thức (xác nhận dead, không phải bỏ sót):**
- `ClearProcessingPointers` (`mvm_api.go:1426`) — code C++ tự ghi chú "không còn cần thiết".
- `commit_full_db`/`revert_full_db`/`MVM_cancelTransaction` — có `extern` trong
  `mvm_linker.hpp` nhưng **không có bất kỳ call site Go nào** (grep xác nhận 2026-08-15, phát
  hiện mới so với kiểm kê GĐ0 — 3 hàm này còn "chết" hơn cả `ClearProcessingPointers` vì Go
  còn chưa từng wrap chúng).
- `SetXapianBasePath`, `InitCppFileLog`/`CloseCppFileLog`, `testMemLeak`/`testMemLeakGS` —
  cgo-mode-only theo bản chất (đường dẫn đĩa thật / file log C++ / công cụ debug), không có ý
  nghĩa tương đương trong TZ mode.

## 4. Khuôn dạng blob-stream

Mọi trường độ dài thay đổi (calldata, code, mảng storage change, …) đi qua vùng blob theo sau
phần header cố định của mỗi lệnh, dưới dạng chuỗi bản ghi `[uint32_t len][len bytes]` liên
tiếp, **đúng thứ tự đã ghi trong comment của từng struct** trong `mvm_tz_protocol.h`. Đây là
cách phẳng hóa trực tiếp các trường mảng-của-mảng sẵn có trong `ExecuteResult`
(`mvm_linker.hpp:17-72`, vốn đã là `char*`/độ dài thuần túy) — không thiết kế lại định dạng dữ
liệu, chỉ đổi cách nó di chuyển qua ranh giới.

## 5. Việc còn mở cho GĐ2/GĐ3

- `MVM_TZ_BLOB_REGION_SIZE` (hiện đặt 4 MiB làm placeholder) cần đo lại bằng payload thực tế
  lớn nhất (constructor bytecode dài nhất, batch storage-change lớn nhất) trước khi chốt.
- `mvm_tz_xapian_file_open_req_t`'s `flags` — tập giá trị chính xác cần khớp với cách Xapian
  C++ build thật sự mở file (đọc lại `xapian_manager.cpp`/`xapian_registry.cpp` khi bắt tay
  vào GĐ3, không đoán trước).
- Giao thức chưa có cơ chế báo lỗi framing (version mismatch, blob tràn) khác ngoài "TA/Host tự
  refuse" — đủ cho GĐ2 (loopback cùng tiến trình, lỗi framing là lỗi lập trình, không phải điều
  kiện vận hành thật); GĐ3 cần xem lại có cần thêm gì khi có nhiễu phần cứng thật hay không.
