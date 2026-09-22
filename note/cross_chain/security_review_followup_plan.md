# Cross-Chain Security Review — Việc còn lại cho agent tiếp theo

> **Đây là kế hoạch bàn giao, không phải audit report.** Nguồn gốc: `/code-review xhigh` chạy trên
> toàn bộ luồng cross-chain (`gateway.go`, `relayer.go`, `relayer_daemon/*`, `gateway_handler.go`,
> `cross_chain_precompile.cpp`, `epoch_sync.go`, `types.go`, `asset_registry.go`, `root_anchor.go`)
> ngày 2026-09-22, chạy dưới dạng nhiều finder agent song song. **Phiên chạy bị rate-limit (session
> limit) làm gián đoạn 2 lần** — chỉ 4/9 angle thật sự hoàn thành và trả về finding chi tiết:
> `language-pitfall`, `wrapper/proxy correctness`, `cross-file tracer`, `efficiency`. 5 angle còn
> lại (`line-by-line diff scan`, `removed-behavior auditor`, `altitude/root-cause depth`,
> `CLAUDE.md/AGENTS.md conventions`, `invariant auditor` — angle này fail 2 lần liên tiếp) **chưa
> từng chạy xong, không có finding nào từ chúng cả**. Đừng coi review này là đã bao phủ toàn diện —
> nếu muốn phủ đầy đủ, cần chạy lại riêng các angle đó.

---

## 0. Đã xử lý xong trong phiên vừa rồi (đừng làm lại)

- **[HIGH] `RelayBatch` biến revert on-chain thành log, không phải lỗi** → mất dấu vĩnh viễn
  (permanent fund lock) khi `attestCommit`/`claimMessage` revert thật (velocity-limit mới thêm tạo
  kịch bản kích hoạt thật cho bug này). Đã sửa: track `unresolved`, trả lỗi để kích hoạt đúng cơ chế
  backoff/retry có sẵn ở `WatchChainPair`, có phân biệt riêng trường hợp "thua race" (người khác đã
  claim trước) là vô hại. Test: `TestRelayerDaemon_AttestCommitReverts_BatchKeptForRetry`.
- **Công thức ngưỡng BFT quorum cũ `(totalStake*2+2)/3`** còn sót 3 chỗ trong
  `relayer_daemon/daemon.go` (dòng 873/965/1280 cũ) dù `gateway.go` đã sửa thành công thức chuẩn
  `(2*TotalStake)/3+1` từ trước — đã đồng bộ lại cả 3.
- Cả 2 nằm trong commit `33eca2eb`. `build_check.sh --all` + toàn bộ `pkg/cross_chain`,
  `pkg/blockchain/tx_processor` test suite pass sạch tại thời điểm commit.

---

## 1. Ưu tiên cao — nên làm trước (MEDIUM severity, rõ ràng, không cần quyết định thiết kế)

### 1.1 `creditReserveAllocation` thất bại không có hàng đợi retry
**Vị trí:** `execution/pkg/cross_chain/relayer_daemon/daemon.go:709-737` (số dòng TRƯỚC khi tôi sửa
`RelayBatch` ở mục 0 — dòng thật sự giờ đã lệch xuống ~15 dòng do đoạn comment mới chèn ở đầu hàm,
tìm bằng cách grep `"creditReserveAllocation"` trong hàm `RelayBatch`).

**Vấn đề:** comment tại chỗ nói rõ ý định: "an toàn vì có thể re-run lần sau do idempotent" — nhưng
KHÔNG có gì thực sự re-run nó. Không giống `pendingRefunds` (có hàng đợi retry thật, dùng bởi
`processFailedClaim`), `creditReserveAllocation` thất bại (gửi lỗi HOẶC revert on-chain) chỉ log
warning rồi thôi — message đã claim thành công thật (tiền đã tới tay user), nhưng Reserve's ledger
(`PerChainAllocation[destChainID]`) vĩnh viễn ghi thiếu, có thể sau này khiến destChainID's outbound
transfer hợp lệ bị từ chối oan vì ceiling tính sai.

**Đề xuất fix:** thêm `pendingCredits map[common.Hash]*pendingCredit` (struct tương tự
`pendingRefund`, giữ `msg`/`commitRoot`/`proof`/`successCert`) + hàm `retryPendingCredits` gọi từ
`BatchAndRelay` giống hệt cách `retryPendingRefunds` đã được gọi (dòng ~1077 cũ). Khi
`creditReserveAllocation` gửi lỗi hoặc revert, thêm vào `pendingCredits` thay vì chỉ log.

**Test cần thêm:** mô phỏng `creditReserveAllocation` revert 1 lần rồi thành công ở tick sau, xác
nhận `PerChainAllocation[destChainID]` cuối cùng vẫn đúng.

**Độ phức tạp:** trung bình — chủ yếu copy-paste pattern `pendingRefunds` đã có sẵn, rủi ro thấp.

---

### 1.2 `mustUint64` âm thầm truncate `uint256` vượt 2^64
**Vị trí:** `execution/pkg/blockchain/tx_processor/gateway_handler.go:2319-2325`
```go
func mustUint64(v interface{}) uint64 {
	if b, ok := v.(*big.Int); ok {
		return b.Uint64() // KHÔNG check b.IsUint64() trước
	}
	u, _ := v.(uint64)
	return u
}
```
`big.Int.Uint64()` doc Go: "if x cannot be represented in a uint64, the result is undefined" — thực
tế trả về 64 bit thấp (truncate), không panic/lỗi.

**Vấn đề:** mọi tham số `uint256` từ ABI calldata đi qua hàm này (attacker/caller kiểm soát hoàn
toàn) đều có thể bị truncate âm thầm thay vì bị reject. Rủi ro thật cụ thể nhất: `outbound()`'s
`destChainId` (`gateway_handler.go` dòng ~696, `DestChainID: mustUint64(args[0])`) — 1 giá trị
`2^64 + 5` sẽ bị truncate thành `5`; nếu chain 5 tình cờ là 1 chain THẬT đã đăng ký, message được
gửi tới đích không phải ý định thật của người gọi (tiền tự đốt của chính họ, không phải đường cướp
tiền người khác — nhưng vẫn là 1 lỗ hổng input-validation thật). Cùng lớp lỗi (không qua
`mustUint64` mà qua `mustBigInt(args[N]).Uint64()` trực tiếp) cũng xuất hiện ở `LeafIndex`,
`epoch`, `sourceChainId`, `nonce` ở nhiều method khác — nhưng các trường đó ít rủi ro hơn vì giá trị
sai chỉ khiến proof/cert verify FAIL (không silently alias sang 1 entity hợp lệ khác).

**Vì sao CHƯA sửa trong phiên này:** sửa đúng cách cần audit lại TẤT CẢ ~30+ call site của
`mustUint64`/`.Uint64()` trực tiếp trong `gateway_handler.go` để quyết định: chỗ nào cần reject cứng
(như `destChainId`), chỗ nào có thể giữ nguyên (vì giá trị sai tự fail ở bước verify sau đó) — phạm
vi lớn hơn 1 lần vá nhanh, rủi ro đụng vào file dispatch ABI trung tâm nếu làm vội.

**Đề xuất fix (2 lựa chọn, agent tiếp theo tự cân nhắc hoặc hỏi user):**
- **(a) Tối thiểu, chỉ chặn chỗ rủi ro nhất:** thêm 1 check tường minh ngay sau dòng
  `DestChainID: mustUint64(args[0])` trong case `"outbound"`: `if !args[0].(*big.Int).IsUint64() { return nil, nil, fmt.Errorf("outbound: destChainId overflows uint64") }`. Rẻ, an toàn, không đụng
  helper dùng chung.
- **(b) Toàn diện:** đổi `mustUint64` trả về `(uint64, error)`, cập nhật TẤT CẢ call site — an toàn
  hơn nhưng là refactor lớn trên file trung tâm, cần review kỹ + chạy full test suite.

**Khuyến nghị:** làm (a) trước (rẻ, đóng đúng lỗ hổng rủi ro nhất), để (b) làm follow-up riêng nếu
muốn triệt để.

---

### 1.3 Poll loop giữ `ChainRegistry` snapshot cũ suốt cả vòng lặp
**Vị trí:** `execution/pkg/cross_chain/relayer_daemon/daemon.go` — `pollAndAggregateCommitCert`,
`pollAndAggregateFailureCert`, `pollAndAggregateSuccessCert` (3 hàm, tìm bằng grep tên hàm — số dòng
đã lệch do fix ở mục 0). Mỗi hàm fetch `reg` (ChainRegistry) đúng 1 lần ở đầu, rồi dùng lại suốt
vòng lặp `for i := 0; i < maxIterations; i++` (có thể chạy hàng chục giây).

**Vấn đề:** nếu committee của chain đó rotate thật giữa lúc đang poll (`committeeUpdate`/
`updateCommitteeWithRecoveryCert`), daemon vẫn verify share theo committee CŨ, rồi khi submit lên
chain, check `cert.Epoch != registry.Epoch` on-chain (đã có sẵn, đúng) sẽ reject — **fail closed,
không fork**, nhưng lãng phí 1 tx + trễ relay tới tick sau (khi `reg` được fetch lại mới).

**Đề xuất fix:** fetch lại `reg` mỗi vài iteration (không cần mỗi iteration — quá tốn RPC — có thể
mỗi N giây hoặc N iteration), hoặc đơn giản hơn: fetch lại ngay trước khi submit cert cuối cùng để
xác nhận epoch chưa đổi.

**Độ ưu tiên thấp hơn 1.1/1.2** vì đã fail-closed sẵn (không phải fund-safety bug, chỉ là hiệu năng/
độ trễ relay) — làm nếu có thời gian, không khẩn cấp.

---

## 2. Ưu tiên thấp hơn — dead code / bất nhất, không khẩn cấp

### 2.1 `RelayerDaemon.RelayMessage` là dead code, KHÔNG an toàn nếu ai đó nối lại
**Vị trí:** `daemon.go:290-359` (số dòng gần đúng, đã có thể lệch nhẹ).

Xác nhận qua grep: **0 caller production** — chỉ tồn tại trong file, không ai gọi. Nếu dùng: gọi
thẳng `verifyAndExecute` tới `msg.DestChainID`, KHÔNG đi qua 2-hop Reserve routing, KHÔNG có
`processFailedClaim`/refund-cert pursuit, KHÔNG credit Reserve ledger — bỏ qua toàn bộ phần cứng hoá
mà `RelayBatch` đã có. `verifyAndExecute`'s `AttestCommit` nội bộ luôn set `enforceCeiling=true`,
nên bất kỳ transfer thật nào với `SourceChainID != ReserveChainID` và `Value > 0` sẽ luôn fail
(`ErrNonReserveCeilingAttestation`).

**Đề xuất:** xoá hẳn (không ai dùng, không mất chức năng gì) HOẶC thêm comment cảnh báo rõ ràng ở
đầu hàm "KHÔNG DÙNG — xem RelayBatch thay thế" nếu muốn giữ cho mục đích tham khảo/test. Xoá là lựa
chọn sạch hơn theo đúng tinh thần KISS/YAGNI của AGENTS.md.

### 2.2 2 cơ chế lấy cross-chain sender song song, 1 cái luôn chết
**Vị trí:** ABI-level `getOriginalSender()`/`isCalledByGateway()` (view method, đọc
`GatewayEngine.ActiveContext` — `gateway_handler.go:2089-2097` gần đúng) LUÔN trả `ErrNoActiveContext`
vì `ActiveContext` luôn `nil` tại thời điểm 1 tx mới load engine (`ClaimMessage` tự clear nó trước
khi return/persist). Cơ chế THẬT SỰ hoạt động là precompile C++ (`getOriginalSender()`/
`getSourceChainId()` qua `mvmE.SetCrossChainContext`, `cross_chain_precompile.cpp:21-32`).

**Đề xuất:** xoá ABI-level view method chết (gây nhầm lẫn cho dApp author gọi nhầm) hoặc sửa nó đọc
đúng nguồn thật — cần đọc kỹ xem `ActiveContext` có ý nghĩa nào khác không trước khi xoá (tránh phá
API đang được ai đó dùng, dù có vẻ luôn trả lỗi).

### 2.3 Vài chỗ thiếu nil-guard đối xứng (không reachable hiện tại)
- `HashAccountLeaf` (`epoch_sync.go:320-334`) thiếu `len(raw) <= 32`/nil-guard mà các hàm hash chị
  em cùng file (`padTo32`, `CanonicalEncodeMessage`, `HashAggregateValueLeaf`) đều có. Reachable từ
  `ClaimDeadChainBalance` (`gateway.go:2144`) nhưng hiện an toàn vì `mustBigInt` ở tầng ABI đã đảm
  bảo giá trị hợp lệ. Fix: thêm guard giống hàm chị em, rẻ và nhất quán.
- `ClaimMessage` (`gateway.go:1456`) không nil-check `attested.ClaimedAmount` trong khi
  `FinalizeFailedAfterExecutionRevert` (`gateway.go:1570`) cùng field lại có check. Không
  reachable hiện tại (mọi `AttestedCommits` entry tạo với `ClaimedAmount: big.NewInt(0)`) nhưng bất
  nhất — thêm guard cho khớp.
- `AssetRegistryEngine.LockAndBridgeAsset` (`asset_registry.go:193`) panic nếu `tip` nil — xác nhận
  0 caller production, chỉ cần fix nếu có ý định dùng hàm này sau này.
- `GetPendingCommitteeAttestationShares`/3 hàm chị em (`gateway.go:961-968` +3 chỗ tương tự) chỉ
  shallow-copy slice, `[]byte` bên trong vẫn alias chung backing array — vi phạm doc-comment
  "defensive copy" của chính type đó. Vô hại hiện tại (không goroutine nào giữ engine qua nhiều
  tx), nhưng nên fix nếu doc-comment đã hứa vậy.

### 2.4 Hiệu năng (không phải bảo mật, nhưng ghi lại cho đầy đủ — từ angle "efficiency" đã hoàn thành)
Không khẩn cấp, không ảnh hưởng an toàn/fork — chỉ liệt kê để không mất:
- `relayer_daemon/daemon.go` (poll functions) + `root_anchor.go:143` (`VerifyQuorumVotes`): scan
  `bytes.Equal` lồng nhau O(N×M) thay vì dùng `map[string]bool` — nên fix nếu committee lớn.
- `gateway_handler.go` (6 chỗ, ví dụ dòng ~777): `crypto.Keccak256Hash` của selector string hằng số
  bị tính lại mỗi lần gọi thay vì hoist ra biến package-level (pattern đã đúng ở
  `gatewayStateStorageKey` nhưng thiếu ở đây).
- `asset_registry.go:265` (`VerifyAssetConservationInvariant`): scan toàn bộ map thay vì index theo
  asset, kèm 1 `fmt.Sscanf` bị bỏ kết quả (dead code nhỏ).
- `relayer_daemon/metrics.go:221`: `StartBalanceMonitor` query từng chain tuần tự thay vì
  concurrent — nên fix nếu số chain lớn.
- `gateway.go:951` (4 hàm `AddPending*Share`): O(n) scan trước khi append → O(C²) cho cả chu kỳ thu
  thập quorum — nên fix bằng `map[string]bool` tracking.
- `gateway.go:1903` (`Refund()` fallback path): linear-scan toàn bộ `AttestedCommits` (không bao giờ
  prune, chỉ tăng theo thời gian) — nên thêm index phụ nếu map này lớn dần theo thời gian sống của
  chain.

---

## 3. Cách làm việc — lưu ý rút ra từ phiên vừa rồi

- **`AGENTS.md` ở root repo là bắt buộc đọc trước khi sửa** — đặc biệt phần Zero-Fork Invariant
  (không dùng timeout/Duration để quyết định dispatch) và yêu cầu chạy `build_check.sh` sau khi sửa.
- **Sau MỌI thay đổi signature hàm dùng chung** (ví dụ thêm tham số `blockTime`), phải `go build`
  rồi `go vet ./...` để bắt hết call site còn thiếu — riêng ABI Pack (`h.abi.Pack("method", args...)`)
  KHÔNG bị bắt bởi build/vet (arg count mismatch chỉ lỗi ở RUNTIME), phải chạy test thật mới phát
  hiện được.
- **Khi sửa 1 pattern lặp lại ở nhiều `case` block giống hệt nhau trong cùng 1 file lớn** (ví dụ
  daemon_test.go có nhiều mock-server `switch method.Name` với cùng field index), đừng regex-replace
  hàng loạt theo index tuyệt đối — dễ đụng nhầm case KHÁC dùng chung dải index (đã xảy ra thật:
  sửa `claimMessage` làm hỏng luôn `refund`/`refundReserveAllocation` vì cùng dùng `args[13..18]`
  nhưng KHÔNG cùng đổi ABI). Luôn xác nhận riêng từng `case` trước khi sửa.
- **Test cũ giả định hành vi CŨ (ví dụ "rút 100% ceiling trong 1 lần thành công") có thể FAIL đúng
  khi thêm 1 lớp phòng thủ mới** — đừng vội coi đó là bug của fix mới, đọc kỹ xem test đang assert
  hành vi nào trước khi sửa test hay sửa code.
- **`gofmt -l` + `go vet ./pkg/...` + `build_check.sh --all`** là bộ 3 bắt buộc chạy trước khi commit
  bất kỳ thay đổi Go/Rust/C++ nào trong repo này.
- File `scan1.json`/`scan2.json` ở root repo là rác từ 1 tool khác (semgrep pro login failure), không
  phải của cross-chain work — không cần quan tâm, đừng xoá mà không hỏi trước.
