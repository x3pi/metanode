# Kế hoạch hiệu năng tiếp theo (bàn giao cho agent khác)

> Tạo: 2026-10-07. Nền: commit `8823ba0b` (perf sig-filter) và `4e45bccb` (fix rpc-client) trên nhánh `dev`.
> Đọc trước: `AGENTS.md` (đặc biệt PART 2.5 Zero-Fork, PART 5 tóm tắt tiếng Việt), `PROJECT_STRUCTURE.md`,
> `note/benchmark_results_p2_2.md`.

## 0. Bối cảnh đã làm (đừng làm lại)

`FilterInvalidSignatures` (`execution/pkg/blockchain/tx_processor/signature_enforcement.go`) chạy trên critical path
của MỌI validator. Trước đây mỗi tx secp (có `RawEnvelope`) bị decode + `ecrecover` 3-4 lần. Đã sửa:

- `transaction.go`: `envelopeEthTx()` memoize decode `RawEnvelope`, dùng chung `*e_types.Transaction` với
  `ToEthTransaction` để cache sender của go-ethereum hit. `ClearCacheHash` cũng xoá cache này.
- `eth_validation.go`: `validateProtoEnvelopeBinding(pTx, owner)`; `ValidateEnvelopeBinding` dùng tx object.
- `signature_enforcement.go`: `boundSigKey` / `storeVerifiedBound`. Entry "bound" chỉ được ghi SAU KHI binding VÀ chữ ký
  cùng qua. Hit trên entry này bỏ qua decode + `ecrecover`. Entry `sigCacheKey` cũ KHÔNG được bỏ qua binding
  (một số đường mempool lưu nó mà chưa chạy binding; nếu bỏ qua thì các node có cache khác nhau sẽ ra verdict khác nhau = fork).
- Kết quả micro-benchmark (512 tx EIP-1559 secp, GOMAXPROCS=8): lạnh 37.4 → 13.3 µs/tx; warm ~6x.
  CHƯA đo TPS end-to-end trên cluster.

**Bất biến phải giữ ở mọi bước dưới đây:** verdict của filter là hàm thuần của (tx, state người gửi, policy chain).
Không dùng timeout/sleep/Duration để quyết định dispatch. Thà pending chứ không fork.

---

## Bước 1 — Đo TPS thật (ƯU TIÊN CAO NHẤT, làm trước)

**Mục tiêu:** có số liệu end-to-end để quyết định Bước 2 và 3 có đáng làm không.

1. `git push origin dev` (hoặc theo quy trình PR của repo). Nhớ lưu ý trong memory: sau khi merge, spot-check
   `git show origin/dev:<path>` vì squash-merge từng làm mất commit.
2. Baseline: checkout `87d825dd` (trước 2 commit trên), build, deploy cluster, chạy
   `./ci.sh run-now --only tps_blast`. Ghi lại tx/s, P95, và dòng log `🔏 [SIG-ENFORCE]` (thời gian filter mỗi block).
3. After: checkout `8823ba0b`, wipe devnet data, deploy lại, chạy lại đúng lệnh trên, ≥3 lần mỗi bên (nhiễu run-to-run ~2%).
4. Xác nhận 100% node cùng block hash / state root (không fork). Deploy phải đồng loạt (verdict không đổi nên
   không cần wipe vì tính đúng đắn, nhưng nên wipe để so benchmark công bằng).
5. Ghi kết quả vào `note/benchmark_results_perf_followups.md` với số liệu THẬT. Không ước lượng, không điền số giả.
   Nếu không chạy được cluster, ghi rõ "chưa đo", đừng bịa.

**Tiêu chí xong:** có bảng before/after cho tx/s, P95 và thời gian `[SIG-ENFORCE]` mỗi block; đã xác nhận không fork.

**Lưu ý:** workload tps_blast cũ chủ yếu là BLS (mốc ~6400 tx/s). Cần xác nhận kịch bản dùng tx secp có `RawEnvelope`
(eth-only); nếu không, cải tiến này sẽ không hiện ra trong số liệu. Kiểm tra `deploy/ci/ci_config.yaml`.

---

## Bước 2 — Giảm allocation của `ValidateProtoEnvelopeBinding` (làm nếu Bước 1 cho thấy ingress/GC là điểm nghẽn)

**Hiện trạng** (`execution/pkg/transaction/eth_validation.go`): mỗi lần gọi dựng cả `pb.Transaction` canonical qua
`FromEthLegacyTx/FromEthEIP1559Tx/...` rồi so từng trường. Benchmark P2-2: ~62 µs, 67 allocs, 2.4 KB mỗi tx
(`go test -bench=BenchmarkValidateProtoEnvelopeBinding -benchmem ./pkg/transaction -run=^$`).

**Hướng làm (chọn một, ưu tiên cái đơn giản):**
- A. Pool `canonicalPb` (`sync.Pool`, reset sau dùng). Cẩn thận: slice trong proto có thể tham chiếu buffer của ethTx.
- B. So sánh trực tiếp từ `ethTx` mà không dựng proto. Rủi ro cao hơn vì phải giữ chính xác từng quy tắc chuyển đổi
  (ví dụ recoveryID legacy, `Sign = R||S||V`, AccessList, AuthorizationList).

**Ràng buộc:**
- Verdict phải IDENTICAL với bản hiện tại cho mọi loại tx (Legacy, 2930, 1559, 4844, 7702). Thêm test so sánh
  bản cũ và bản mới trên bộ tx sinh ngẫu nhiên (property/differential test), gồm cả tx bị sửa từng trường.
- Giữ nguyên các test hiện có: `envelope_binding_p09_test.go`, `signature_enforcement_*_test.go`.
- Không đổi chữ ký hàm public `ValidateProtoEnvelopeBinding`.

**Tiêu chí xong:** allocs/op giảm rõ rệt trong benchmark, test differential pass, `go test ./pkg/transaction ./pkg/blockchain/...` pass.

---

## Bước 3 — Dọn legacy `rpc-client` (ưu tiên thấp)

`execution/cmd/rpc-client/client-tcp/controllers/transaction_controller.go`: `SendNewTransactionWithDeviceKey` giờ chỉ trả
lỗi "đã gỡ" nhưng vẫn được `client.go` (~dòng 808) gọi. Một số tool trong `execution/cmd/tool/*` import package này
(`single_bls_register.go`, `tx_sender`, `test_raw_eth_tcp_live`, `e2e_cross_chain_coattest`).

- Dùng codegraph/grep để liệt kê ai còn gọi các hàm device-key. Nếu không ai dùng thật → xoá hàm + method interface +
  các phần liên quan trong `client.go`. Nếu có tool dùng → báo cho người dùng, đừng tự xoá.
- Cân nhắc thêm một bước `go build ./...` vào `consensus/metanode/scripts/build_check.sh` (hoặc CI) vì script này
  KHÔNG build package `cmd/rpc-client`, nên lỗi từ commit `5746d7b7` đã lọt qua.

---

## Bước 4 — Giao commit pipeline Rust→Go (KHÔNG làm nếu chưa có phê duyệt thiết kế)

Rust giao commit cho Go tuần tự (`send_committed_subdag` chờ Go reply) nên Go không pre-verify được block kế tiếp.
Cho phép 2+ commit in-flight có thể giảm thêm thời gian chờ nhưng có RỦI RO FORK CAO.

- Chỉ bắt đầu bằng một tài liệu thiết kế (`note/design_pipelined_commit_delivery.md`) gồm: thứ tự áp dụng state,
  xử lý khi commit trước fail/discard, tương tác với `CommitVoteMonitor` / `PeerAttestResult`, giới hạn buffer
  (bounded), và cách verify không fork. Gửi người dùng review TRƯỚC khi viết code.
- Đọc memory/`note` về `GLOBAL_TX_CACHE`, `dag_state`, `parking_lot` trong consensus-core trước khi đụng vào.
- Tuyệt đối không dùng timeout để quyết định dispatch.

---

## Quy trình bắt buộc cho mỗi bước (theo AGENTS.md)

1. Phân tích blast radius (ưu tiên codegraph; fallback grep) trước khi sửa code ghi/consensus.
2. Sau khi sửa: `cd consensus/metanode/scripts && ./build_check.sh` phải xanh, không warning. Ngoài ra chạy
   `cd execution && go build ./... && go vet ./pkg/transaction ./pkg/blockchain/...`.
3. Chạy test: `go test ./pkg/transaction ./pkg/blockchain/...` (tx_processor mất ~80s).
4. Cập nhật `PROJECT_STRUCTURE.md` chỉ khi đổi cấu trúc module/FFI/proto.
5. Kết thúc mỗi phản hồi bằng khối "📋 Tóm tắt thay đổi" tiếng Việt (AGENTS.md PART 5).
6. Báo cáo trung thực: nếu test fail hoặc bước bị bỏ qua, nói rõ; không ghi "PASS" nếu chưa chạy thật.
7. Dùng worktree dùng chung cẩn thận: `git add` theo tên file, không `git add <dir>`.

## Thứ tự đề xuất

Bước 1 → (xem số liệu) → Bước 2 nếu đáng → Bước 3 khi rảnh → Bước 4 chỉ khi người dùng duyệt thiết kế.
