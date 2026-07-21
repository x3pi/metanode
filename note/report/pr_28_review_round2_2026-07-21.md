# Review PR #28 — "Fix block stm" — Vòng 2

So với vòng 1 (`note/report/pr_28_review_2026-07-21.md`, commit `b0d1b9fc`), PR đã có thêm 1 commit mới: `42d66447` — "implement speculative execution cancellation, track block-STM abort statistics, and optimize dependency injection order". Head hiện tại: `42d66447`.

## Kết luận: **vẫn CHƯA sẵn sàng merge**, nhưng phần lớn rủi ro ở vòng 1 đã được xử lý tốt

## 1. Những gì commit mới đã fix (so với vòng 1)

- **Mục 3 vòng 1 (SLOAD im lặng trả 0)** — đã có comment giải thích rõ trong `my_storage.cpp`: `get_rs.success=false` xảy ra khi (a) slot thực sự trống — theo spec EVM phải trả 0, hoặc (b) tx đang bị Suspend bởi MVCC. Trả 0 an toàn vì tầng Go kiểm tra `BlockingVersion` sau khi thực thi xong và vứt bỏ toàn bộ kết quả nếu tx bị suspend. Hợp lý, không còn là ẩn số.
- **Mục 5 vòng 1 (đảo thứ tự `injectVirtualDependency`)** — commit mới đảo ngược lại, đưa thứ tự về đúng như trước khi có PR (khai báo dependency **trước** khi ghi Xapian, ở tất cả ~8 vị trí trong cả `FullDatabase` và `FullDatabaseV1`). Diff cuối cùng so với `dev` cho phần này gần như bằng 0 — không còn rủi ro.
- **Mục 4 vòng 1 (thiếu test cho suspend/wake-up)** — đã thêm `true_block_stm_suspend_test.go` (153 dòng, 2 test case): `TestTrueBlockSTM_SuspendWakeupLogic` (mô phỏng tx bị suspend rồi được đánh thức) và `TestTrueBlockSTM_SuspendWakeupRace_DoubleCheck` (kiểm tra race giữa "vừa xong" và "vừa suspend"). Tôi build và chạy thử — **cả 2 test đều PASS**. Lưu ý nhỏ: test 1 dùng `time.Sleep(200ms)` để đồng bộ thay vì tín hiệu tường minh — có thể flaky dưới tải CI cao, nhưng chấp nhận được cho mức test hiện tại. Test 2 copy lại logic thay vì gọi thẳng `execOne` thật — kiểm được ý tưởng nhưng không bắt được nếu code thật trôi khỏi bản copy này.
- **Bonus fix phát hiện thêm**: `mvcc_memory.go` đổi điều kiện estimate từ `estVer < requestVersion` thành `estVer <= requestVersion`. Đây là fix một lỗi thật: với so sánh cũ, nếu tx ngay trước đó (adjacent, không phải xa hơn) để lại estimate, tx đọc sau **không** phát hiện ra để suspend đúng cách — sẽ đọc phải data cũ, rồi phải chờ `validateOne` phát hiện sai lệch và abort/retry sau, gây lãng phí CPU (không sai kết quả cuối, nhưng phá vỡ mục đích tối ưu chính của cơ chế suspend). Dấu hiệu cho thấy cơ chế mới vẫn đang được tinh chỉnh qua từng vòng, đúng như dự đoán ở vòng 1.

## 2. Lỗi mới phát hiện — chặn merge

**`pkg/blockchain/tx_processor/mvcc/mvcc_memory_test.go` không được cập nhật theo signature mới của `Read()`.**

PR đổi `Read()` (ở `VersionedAccountState`, `VersionedStorage`, `MVCCAccountMap`, `MVCCStorageMap`) từ trả về 3 giá trị sang 4 giá trị (thêm `blockingVer Version`). File `true_block_stm_cascade_test.go` (cùng thư mục cha) đã được sửa theo đúng, nhưng **file test khác `mvcc/mvcc_memory_test.go` thì không** — 18 chỗ gọi `.Read(...)` trong file này vẫn destructure 3 giá trị.

Xác nhận bằng cách build + chạy thật trên commit `42d66447`:
```
go build ./...      → OK, sạch
go vet ./...         → FAIL: pkg/blockchain/tx_processor/mvcc/mvcc_memory_test.go:24:21:
                        assignment mismatch: 3 variables but v.Read returns 4 values (18 lỗi tương tự)
go test ./...         → FAIL cùng lý do (build failed cho package mvcc)
```
Đây là lỗi biên dịch thật 100%, không phải do môi trường — sẽ chặn `go vet`/`go test` trong CI (job `go-build-test`) ngay cả sau khi đã fix phần cài đặt gói native bên dưới. Cần tác giả cập nhật 18 chỗ này (thêm `_` cho giá trị thứ 4) trước khi merge.

## 3. Đã tìm ra nguyên nhân CI fail — đúng như anh nghi ngờ: thiếu cài `mpfr`

Tôi build lại `execution/pkg/mvm` (MVM C++ linker) trên máy local với đúng bộ gói mà `.github/workflows/go-ci.yml` cài (`build-essential cmake libtbb-dev libxapian-dev`) thì **local vẫn build được** — vì máy tôi tình cờ đã có sẵn `libmpfr-dev`/`libgmp-dev`/`uuid-dev`/`libleveldb-dev` từ trước (không phải do workflow cài). Việc này che mất vấn đề thật ở vòng 1.

Kiểm tra trực tiếp source code (không đoán) xác nhận các gói còn thiếu trong bước "Install native build dependencies" của `.github/workflows/go-ci.yml`:

| Gói thiếu | Cần vì |
|---|---|
| `libmpfr-dev` | `#include <mpfr.h>` trong `my_extension.cpp`, `crypto_handlers.cpp`, `utils.cpp`, `utils.h`, `my_extension.h` (hàm lượng giác, mpfr encode cho contract) |
| `libgmp-dev` | `#include` GMP transitively qua mpfr + dùng trực tiếp `mpz_*` trong `crypto_handlers.cpp` (modexp) |
| `uuid-dev` | `#include <uuid/uuid.h>` trong `xapian_manager.cpp` |
| `libleveldb-dev` | cờ `-lleveldb` trong CGO LDFLAGS (`mvm_api.go`) — cần cho bước `go build` sau đó |

Xác nhận thêm: **`dev` branch hiện tại (không phải chỉ riêng PR #28) cũng đang đỏ CI vì đúng lý do này** — kiểm tra commit HEAD của `dev` (`169d1d0e`) qua GitHub API cũng thấy `go-build-test: failure`. Vậy đây là **lỗi hạ tầng CI có sẵn từ trước, không phải do PR #28 gây ra** — nhưng PR #28 (và mọi PR khác) sẽ tiếp tục bị chặn bởi nó cho tới khi được sửa.

### Phát hiện thêm: dù sửa xong gói apt, `go build` vẫn sẽ fail vì thiếu bước build thư viện Rust NOMT FFI

`pkg/nomt_ffi/bridge.go` link `-lmtn_nomt` trỏ tới `<repo_root>/target/release/libmtn_nomt.a` — là artifact của crate Rust `mtn-nomt-ffi` (`execution/pkg/nomt_ffi/rust_lib`), được dùng thật trong `pkg/trie/nomt_state_trie.go`, `pkg/trie/trie_factory.go`, `cmd/simple_chain/rpc_state.go`. Workflow `go-ci.yml` **không có bước nào build crate Rust này** (không cài Rust toolchain, không chạy `cargo build`) trước khi chạy `go build`. Script build chuẩn của dự án (`consensus/metanode/scripts/build_check.sh:129`) build đúng bằng lệnh `cargo build --release -p mtn-nomt-ffi` chạy từ repo root — tôi dùng chính xác lệnh này.

### Đã sửa `.github/workflows/go-ci.yml` (chưa commit/push)

```diff
       - uses: actions/setup-go@v5
         with:
           go-version-file: execution/go.mod
           cache-dependency-path: execution/go.sum
 
+      - name: Setup Rust toolchain (NOMT FFI)
+        uses: dtolnay/rust-toolchain@stable
+
+      - name: Cache Cargo (NOMT FFI)
+        uses: Swatinem/rust-cache@v2
+        with:
+          workspaces: "."
+
       - name: Install native build dependencies (MVM C++ linker)
         run: |
           sudo apt-get update
           sudo apt-get install -y --no-install-recommends \
-            build-essential cmake libtbb-dev libxapian-dev
+            build-essential cmake libtbb-dev libxapian-dev \
+            libmpfr-dev libgmp-dev uuid-dev libleveldb-dev
+
+      - name: Build NOMT Rust FFI library (libmtn_nomt.a, needed by CGO LDFLAGS)
+        working-directory: .
+        run: cargo build --release -p mtn-nomt-ffi
 
       - name: Build MVM C++ linker
         working-directory: execution/pkg/mvm
         run: bash build.sh
```

Đã kiểm chứng cục bộ:
- `bash build.sh` (MVM C++ linker) build sạch trên commit `42d66447`.
- `cargo build --release -p mtn-nomt-ffi` chạy đúng từ repo root, ra đúng `target/release/libmtn_nomt.a`.
- `go build ./...` sạch khi thư viện NOMT có sẵn đúng đường dẫn.

**Chưa commit/push** thay đổi này — đây là sửa hạ tầng CI dùng chung cho cả repo (ảnh hưởng mọi PR, không riêng #28), nên cần anh xác nhận trước: sửa thẳng trên `dev`, hay đẩy lên nhánh `fix-block-stm` của PR #28 luôn (vì workflow chạy theo file trên chính nhánh PR khi trigger `pull_request`, sửa trên `dev` không tự áp dụng cho PR đang mở tới khi PR merge/rebase `dev` vào).

## 4. Việc còn lại trước khi merge PR #28

1. Sửa 18 chỗ gọi `.Read()` sai signature trong `mvcc_memory_test.go` (mục 2) — bắt buộc, dễ sửa.
2. Áp dụng fix CI ở mục 3 (cần anh xác nhận nơi push).
3. Vẫn nên có ít nhất 1 review approve và mô tả PR (hiện vẫn trống) trước khi merge — PR ảnh hưởng trực tiếp logic tính state root song song.
4. Khuyến nghị: chạy lại CI xanh hoàn toàn (go build + go vet + go test) trước khi merge để tự xác nhận, thay vì chỉ dựa vào review này.
