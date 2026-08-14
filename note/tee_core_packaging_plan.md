# Kế Hoạch Đóng Gói Lõi Thực Thi (mvm+Xapian) Cho TrustZone (OHtee/tzdriver)

> **Loại tài liệu:** Kế hoạch triển khai — bổ sung, KHÔNG thay thế các tài liệu TEE đã có.
> **Khác biệt tiền đề so với các tài liệu TEE trước đó** (`tee_master_architecture.md`,
> `tee_implementation_plan.md`, `docs/tee_evm_architecture.md`): các tài liệu đó giả định
> target là **OP-TEE trên Orange Pi** (Secure RAM 16-32MB) và kết luận bắt buộc phải viết
> lại lõi thực thi bằng `revm` (Rust `no_std`), tách rời hoàn toàn Xapian ra khỏi TEE.
>
> Tài liệu này giả định target thật là **TrustZone qua OHtee/tzdriver** (kiểu iTrustee/
> OpenHarmony), với Secure RAM đủ rộng để **chạy được Xapian trong RAM bên trong TEE**
> (theo xác nhận của người yêu cầu kế hoạch, 2026-08-14) — do đó **giữ nguyên mvm (C++,
> fork Microsoft eEVM) và Xapian**, không viết lại bằng revm. Nếu sau này target đổi sang
> phần cứng yếu hơn (OP-TEE thật), quay lại dùng 3 tài liệu kia thay vì tài liệu này.

---

## 1. Phạm vi đã chốt

- **Vào TA (Secure World) sau này:** chỉ lõi C++ — `mvm` (`execution/pkg/mvm/c_mvm/`) +
  `linker` (`execution/pkg/mvm/linker/`, bao gồm Xapian tại `linker/src/xapian/`). Hai phần
  này đã được build sẵn thành 1 static lib duy nhất (`libmvm_linker.a`) tiêu thụ qua cgo —
  đúng ranh giới module cần giữ.
- **Ở lại Normal World vĩnh viễn:** toàn bộ lớp Go host (`execution/cmd/simple_chain`,
  `execution/pkg/blockchain`, `tx_processor`, `grouptxns`, RPC, mạng, lưu trữ bền vững...).
  Lý do: runtime Go (GC + goroutine scheduler) không chạy được trong TA của bất kỳ hệ TEE
  nào (không riêng OP-TEE) — đây là giới hạn của Go, không phải của phần cứng cụ thể nào.
- **Mục tiêu của giai đoạn này:** làm sạch ranh giới Go↔C++ hiện tại (cgo) sao cho khi có
  phần cứng OHtee thật, chỉ cần viết thêm **1 lớp gọi mới** (qua session API kiểu
  TEEC_OpenSession/TEEC_InvokeCommand) thay cho cgo, mà **không phải sửa lại mvm/Xapian**.

---

## 2. Hiện trạng thật của ranh giới cgo (khảo sát trực tiếp `mvm_linker.hpp`, 2026-08-14)

Ranh giới Go↔C++ hôm nay **không sạch một chiều** — có 2 luồng gọi ngược nhau:

### A. Go → C++ (vào core) — ổn, tự nhiên map thành TA command sau này
`execute`, `executeBatch`, `deploy`, `call`, `sendNative`, `processNativeMintBurn`,
`noncePlusOne`, `commit_full_db`, `revert_full_db`, `ReplayFullDbLogs`,
`clear_xapian_tx_buffer`, `commit_xapian_tx_buffer`, `MVM_commitAllXapian`.

### B. C++ → Go (callback GIỮA CHỪNG lúc thực thi 1 tx) — nợ kỹ thuật thật, chặn đường lên TA
8 hàm cgo `//export` mà C++ gọi ngược vào Go trong khi đang chạy:

| Hàm | Định nghĩa Go | Vai trò | Trạng thái |
|---|---|---|---|
| `GetChainId` | `mvm_api.go:1269` | đọc chain id — từ global `config.ConfigApp`, không theo mvmId | ✅ B1, `3bd58b63` |
| `GetBlockHash` | `mvm_api.go:1253` | đọc block hash theo số — từ global singleton `blockchain.GetBlockChainInstance()`, không theo mvmId | ✅ B1, `c11b28c4` |
| `GetBlobHash` | `mvm_api.go:1361` | đọc versioned hash (EIP-4844) | ✅ B1, `3bd58b63` |
| `GetBlobBaseFee` | `mvm_api.go:1385` | đọc blob base fee | ✅ B1, `3bd58b63` |
| `GetCrossChainSender` | `mvm_api.go:1307` | đọc sender liên chain | ✅ B1, `3bd58b63` |
| `GetCrossChainSourceId` | `mvm_api.go:1334` | đọc source id liên chain | ✅ B1, `3bd58b63` |
| `GoLogString`/`GoLogBytes` | `logger.go:50`/`:93` | log ra ngoài giữa chừng | ✅ B2, `2e53df25` |
| `ExtensionGetOrCreateSimpleDb` | `extension.go:288` | **mở 1 key-value DB runtime** cho precompile "SimpleDb" — không chỉ đọc context, còn đọc/ghi state phía Go giữa lúc contract đang chạy | ⏳ B3, chưa làm — cần quyết định của bạn |

**Tất cả 7 hàm còn lại đã đóng (B1 6/6 + B2). Chỉ còn `ExtensionGetOrCreateSimpleDb` (B3).**

**Vì sao đây là vấn đề cho TA:** trong TA, gọi ngược Normal World giữa chừng = 1
world-switch tốn kém mỗi lần, và phá vỡ nguyên tắc "gộp cả batch vào 1 lệnh" (session
command kiểu GlobalPlatform vốn có hình dạng 1 lệnh vào → 1 kết quả ra, không thiết kế cho
callback đồng bộ giữa chừng). `ExtensionGetOrCreateSimpleDb` nặng nhất vì nó không chỉ đọc
context tĩnh mà đọc/ghi state runtime — cần quyết định thiết kế riêng (xem B3).
`GetChainId`/`GetBlockHash` nặng thêm 1 bậc nữa: chúng đọc từ **global singleton**
(`config.ConfigApp`, `blockchain.GetBlockChainInstance()`), không nhận `mvmId` để tra theo
từng phiên — phát hiện khi viết harness B6 (2026-08-14), cần B1 thay bằng dữ liệu truyền
thẳng vào input thay vì đọc singleton.

**Lưu ý:** `GetChainId`/`GetBlobHash` được thêm theo đúng 1 tiền lệ cố ý từ trước ("callback
về Go" — xem `project_evm_production_hardening` trong lịch sử làm việc) — tiền lệ đó hợp lý
cho host Linux thường nhưng chính là thứ phải bỏ ở đây.

`SetXapianBasePath`/`InitCppFileLog` cho thấy Xapian/mvm ghi thẳng filesystem qua path Host
truyền vào lúc khởi tạo — cần biết API lưu trữ thật của OHtee (tmpfs trong TA? storage API
riêng?) mới bọc đúng lớp trừu tượng (xem B4).

---

## 3. Kế hoạch từng bước (B1–B6)

**Nguyên tắc xuyên suốt: không đổi logic mvm/Xapian, chỉ đổi CÁCH dữ liệu ra/vào ranh giới.**

- [x] **B1 — Gộp context Go→C++ thành 1 struct đầu vào duy nhất.** ✅ Xong hoàn toàn 6/6
  (2026-08-14, commit `3bd58b63` cho 5/6 đầu + `c11b28c4` đóng nốt BLOCKHASH). `deploy`/
  `call`/`execute` (3 entry point chạy interpreter, KHÔNG đụng
  `executeBatch`/`sendNative`/`processNativeMintBurn`/`noncePlusOne` vì chúng không bao giờ
  chạm opcode liên quan) nay nhận thêm 8 tham số cuối (`MVM_B1_CONTEXT_PARAMS`, đều nullable
  = "không cung cấp"), gộp vào `BlockContext` (mới: `chain_id`, `blob_versioned_hashes`,
  `blob_base_fee`, `cross_chain_sender`, `cross_chain_source_id`, `block_hashes`) qua
  `ParseTxContext()` + `CreateBlockContext()` (default arg — 4 call site không đụng không
  cần sửa gì). `MyGlobalState`'s 6 getter đọc thẳng từ `blockContext` thay vì gọi ngược Go.

  **`GetBlockHash`/BLOCKHASH (đóng sau, commit `c11b28c4`) — thiết kế khác hẳn 5 field kia,
  KHÔNG fetch vô điều kiện.** Lý do: fetch tới 256 hash thật (mỗi cái là 1 cache lookup, hoặc
  khi miss là 1 lần đọc DB thật / fallback O(N) walkback qua
  `blockchain.GetBlockChainInstance().GetBlockHashByNumber`) trên MỌI lời gọi bất kể có dùng
  hay không sẽ là overhead thật, tránh được — khác hẳn `chain_id`/blob context (chỉ 1 lần đọc
  global rẻ). Giải pháp: `HasBlockhashOpcode` (mvm_api.go) quét byte `0x40` trong bytecode
  SẮP chạy (code đã deploy cho Call/Execute qua `smartContractDb.Code()`, constructor cho
  Deploy) trước — chỉ fetch khi có khả năng dùng thật. Quét cố tình bảo thủ (byte `0x40` nằm
  trong dữ liệu PUSH vẫn tính là "có thể dùng") — over-fetch chỉ tốn công thừa, under-fetch
  mới là lỗi thật, nên lệch về hướng an toàn có chủ đích. Bắt được thêm 1 bug tiềm ẩn của
  code cũ khi thay: callback cũ KHÔNG kiểm tra cờ `success` trước khi đọc kết quả — query
  ngoài phạm vi 256 block là undefined behavior, không phải trả về 0 sạch như code mới.

  **3 lỗi thật bắt được nhờ quy trình verify, không phải đoán:**
  1. `get_cross_chain_sender`/`get_cross_chain_source_id` cần ABI-encode đúng 32 byte
     (`cross_chain_precompile.cpp` check `source_bin.size() == 32` tường minh) — soi thẳng
     code tiêu thụ thay vì đoán, tránh được lỗi âm thầm trả 8/20 byte.
  2. `config.ConfigApp` có thể `nil` trong test — callback cũ chỉ đọc lười (chỉ khi bytecode
     thật sự chạy CHAINID), code mới đọc mỗi lần gọi `Deploy`/`Call`/`Execute` bất kể có dùng
     CHAINID không → crash toàn bộ test suite ngay lần chạy đầu (`go test ./...`), bắt được
     và sửa bằng nil-guard phòng thủ trước khi coi là xong.
  3. `Return` của 1 `Deploy` thành công là **địa chỉ CREATE-derived**, không phải bytes RETURN
     gốc của constructor — phát hiện qua test đầu tiên fail với giá trị trông như address,
     xác nhận bằng `crypto.CreateAddress`, sửa lại dùng `MapCodeChange` thay vì `Return`.

  **Verify:** `bash pkg/mvm/build.sh` (rebuild C++) sạch; `go build/vet/test -count=1 ./...`
  sạch toàn module; 6 test trong `ta_boundary_harness_test.go` (ChainId, Blob+BlobBaseFee,
  CrossChain — CrossChain phải tự assemble 1 constructor thực hiện low-level CALL vì precompile
  chỉ dispatch từ trong `is_precompile()` lúc xử lý opcode CALL, không phải từ entry point của
  `deploy`/`call`/`execute` —, và `BlockHash` (mới, commit `c11b28c4`, phải seed thẳng cache
  của singleton `BlockChain` — lần chạy đầu crash vì bare `StorageManager` không có backing
  thật cho nhánh fallback khi cache-miss, sửa bằng cách seed đúng đủ để không bao giờ miss
  trong phạm vi lookback của test) ổn định qua `go test -count=3`.

  **B1 đóng hoàn toàn — không còn callback C++→Go nào trong đường thực thi giữa chừng.**
- [x] **B2 — Gộp log ra thành buffer trả về, bỏ callback log inline.** ✅ Xong (2026-08-14,
  commit `2e53df25`). `MyLogger` (`my_logger.h`/`.cpp`) giờ tích `(flag, message)` vào
  `buffered_logs` thay vì gọi ngược `GoLogString`/`GoLogBytes`. `run()` (chokepoint duy nhất
  mà `deploy`/`call`/`execute`/`executeBatch` đều đi qua) trích buffer ra qua 1 out-parameter
  mới TRƯỚC khi return (ở cả nhánh thành công lẫn 2 catch block riêng, vì `logger` là biến cục
  bộ, mất khi `run()` return) → `processResult()` đóng gói thành blob nhị phân
  `[4-byte flag][4-byte len][msg]` lặp lại trên `ExecuteResult.b_native_logs` (field mới, free
  trong `freeResult()` như mọi buffer khác). Go: `extractNativeLogs` (helpers.go) giải mã
  thành `MVMExecuteResult.NativeLogs`; `logger.go` tách chung `logAtFlag()` cho cả
  `GoLogString`/`GoLogBytes` (không còn được C++ gọi, giữ lại `//export` như tiền lệ B1) và
  hàm mới `FlushNativeLogs()`, gọi 1 lần sau `extractExecuteResult` ở `Call`/`Execute`/`Deploy`/
  `ExecuteBatch` — cùng thứ tự, cùng level như callback cũ, chỉ khác là sau khi return thay vì
  giữa chừng. `sendNative`/`processNativeMintBurn`/`noncePlusOne` không đụng (không bao giờ
  chạm interpreter nên luôn rỗng, `FlushNativeLogs(nil)` là no-op).

  **Verify:** `bash pkg/mvm/build.sh` sạch; `go build/vet/test -count=1 ./...` sạch toàn
  module; test mới `TestTABoundary_NativeLogs_SurvivesSerialization` (deploy constructor chạy
  BALANCE — nơi duy nhất hiện tại gọi `NativeLogger.LogString` — kiểm log line sống sót qua
  `serializeRoundTrip`) pass ngay lần chạy đầu, ổn định qua `go test -count=3`.
- **B3 — Xử lý riêng `ExtensionGetOrCreateSimpleDb` (khó nhất, cần quyết định của bạn).**
  Hai hướng: (a) đưa hẳn key-value store này vào trong core C++ (nếu dữ liệu đủ nhỏ để sống
  trong TA), hoặc (b) giữ ở Go nhưng chuyển từ "callback mid-execution" sang mô hình 2 pha
  (Host đọc DB trước → đưa read-set vào input; ghi kết quả sau, khi core trả output) — không
  có cách né việc quyết định, vì ảnh hưởng ngữ nghĩa hợp đồng đang dùng SimpleDb.
- **B4 — Bọc lớp storage-abstraction quanh Xapian/`SetXapianBasePath`.** Không đổi Xapian,
  chỉ đổi đường vào: 1 interface storage mỏng thay cho path Host trực tiếp — cần xác nhận
  API lưu trữ thật của OHtee để bọc đúng.
- [x] **B5 — Go-side: thu cgo rải rác về 1 interface `ExecutionEngine`.** ✅ Xong
  (2026-08-14, commit `46126c01` + `5a06ba48`). `pkg/mvm/engine.go` khai báo interface
  `ExecutionEngine` khớp đúng method set của `*MVMApi` (assertion `var _ ExecutionEngine =
  (*MVMApi)(nil)`); 8 tham số hàm helper nội bộ trong `vm_processor.go`/
  `vm_processor_debug.go` (các hàm `sendNative`/`execute`/`deploy`/`call`-style được gọi từ
  `ProcessTransaction`) đã đổi từ `mvmE *mvm.MVMApi` sang `mvmE mvm.ExecutionEngine`. Build
  sạch ngay lần đầu, không cascade sang chỗ nào khác — xác nhận toàn bộ call graph các hàm
  này chỉ dùng method có trong interface. `cross_chain_inbound.go`/`cross_chain_outbound.go`/
  `transaction_processor_offchain.go` CHƯA đổi (dùng `GetOrCreateMVMApi` inline ngay trong
  cùng hàm, không thread qua helper khác — chưa có lợi ích rõ ràng để đổi ngay, để dành khi
  thật sự cần swap implementation). Verify: `go build && go vet && go test -count=1 ./...`
  toàn module, 0 regression, hành vi giữ nguyên 100% (thuần đổi kiểu tại compile-time).
- [x] **B6 — Giả lập ranh giới TA ngay trên Linux (kiểm chứng sớm).** ✅ Xong (2026-08-14,
  commit `2f5ec128`). `pkg/mvm/ta_boundary_harness_test.go` (package `mvm_test`, external —
  không rủi ro import cycle) ép request/kết quả qua vòng JSON marshal→unmarshal thật vào 1
  struct hoàn toàn mới trước khi engine dùng/trước khi assert — bắt được bất kỳ chỗ nào còn
  ngầm chia sẻ backing array (điều 1 lần copy Go value bình thường không lộ ra, nhưng world
  switch thật sẽ vỡ). 2 test: `TestTABoundary_NativeTransfer_SurvivesSerialization` (đường
  native fast-path) và `TestTABoundary_Deploy_SurvivesSerialization` (deploy 1 contract tối
  giản, tự tay assemble bytecode CODECOPY+RETURN hằng số 42) — **cả 2 pass ngay lần chạy
  đầu** trên engine cgo thật, không cần sửa lại bytecode.

  **Phạm vi nói thẳng (ghi ngay trong doc comment của file, không giấu):** harness này chỉ
  chứng minh ranh giới cho các đường KHÔNG phụ thuộc 6 callback C++→Go còn lại (mục 2B) —
  cố tình không setup `blockchain.GetBlockChainInstance()`/`config.ConfigApp` (2 global mà
  `GetBlockHash`/`GetChainId` đọc vào) vì che nó đi sẽ giấu mất đúng thứ B1 cần sửa, không
  chứng minh gì cả. Bytecode dùng BLOCKHASH/CHAINID/BLOBHASH/BLOBBASEFEE/cross-chain context
  vẫn CHƯA được kiểm chứng bởi harness này cho tới khi B1 xong.

  **Lợi ích phụ:** đây cũng là bộ test đầu tiên của repo thực sự chạy bytecode EVM thật qua
  ranh giới cgo (trước đây `pkg/mvm` không có test nào làm việc này — xác nhận qua lịch sử
  làm việc `project_evm_production_hardening`), độc lập với động cơ TEE packaging.

  Verify: `go build && go vet && go test -count=1 ./...` toàn module sạch; 2 test mới ổn
  định qua `go test -count=5`.

**Thứ tự ưu tiên đề xuất:** B5 trước tiên (rẻ nhất, an toàn nhất — xem mục 4), song song B1+B2
(giảm ngay 6/8 callback). B3+B4 chờ quyết định thiết kế trước khi code.

---

## 4. Đảm bảo an toàn cho chain đang chạy (trả lời trực tiếp câu hỏi "có ảnh hưởng chain hiện tại không")

**Không tự động an toàn — mức rủi ro khác nhau theo từng bước, và phải chủ động khống chế
bằng quy trình, không phải mặc định "refactor thì chắc chắn không sao".**

| Bước | Đụng vào hot path thực thi? | Rủi ro thật |
|---|---|---|
| B5 (bọc interface Go) | Không — cùng lời gọi cgo, chỉ đổi cách tổ chức code Go | **Thấp nhất**, an toàn nếu implementation giữ nguyên y hệt |
| B6 (harness giả lập, binary riêng) | Không — công cụ test độc lập, không nằm trong đường chạy production | **Không** |
| B2 (log qua buffer) | Có, nhưng chỉ đường log, không đụng logic tính toán | Thấp |
| B1 (gộp context đầu vào) | **Có** — đổi chữ ký `execute`/`executeBatch`, đổi thời điểm C++ đọc chain id/block hash | **Trung bình** — sai thứ tự/giá trị context có thể gây khác kết quả thực thi giữa các node → nguy cơ fork nếu không kiểm chứng kỹ |
| B3 (SimpleDb) | **Có** — đổi ngữ nghĩa 1 precompile hợp đồng đang dùng | **Cao nhất** — có thể đổi hành vi hợp đồng hiện có |
| B4 (storage abstraction Xapian) | Gián tiếp — đổi nơi Xapian đọc/ghi | Trung bình — có thể lệch dữ liệu nếu path/format đổi |

**Quy trình bắt buộc để giữ an toàn** (áp dụng đúng nguyên tắc Zero-Fork Invariant đã dùng
xuyên suốt các phiên làm việc trước — không báo/commit thành công khi còn khả năng lệch
trạng thái giữa các node):

1. **Làm trên nhánh riêng**, không đụng `dev`/`main` cho tới khi verify xong.
2. **B5 land trước, độc lập**, verify bằng `go build && go vet && go test -count=1 ./...`
   toàn bộ module (giống quy trình đã dùng cho các fix trước) — vì hành vi không đổi, đây
   là bước rẻ để có ngay điểm tựa an toàn cho các bước sau.
3. **B1-B4 bắt buộc kiểm chứng "song song, đối chiếu"** trước khi thay hẳn đường cũ: chạy
   cả đường cgo cũ và đường mới trên **cùng 1 tập giao dịch thật**, so từng byte kết quả
   (`ExecuteResult`, state root, receipts) — chỉ cắt sang đường mới khi khớp 100% qua một
   lượng block đủ lớn, giống mô hình shadow-mode đã đề xuất cho engine swap.
4. **Không test trên cluster m1/m2 (cluster thật đang chạy) trước khi xong bước 3** — dùng
   `private_chain_3node/` (cluster test local) hoặc build/test riêng lẻ trước.
5. **B3 (SimpleDb) cần review riêng của bạn trước khi code**, không tự quyết hướng (a)/(b)
   ở trên — vì ảnh hưởng trực tiếp hợp đồng đang chạy thật.

**Tóm lại:** nếu tuân thủ đúng quy trình trên (đặc biệt bước 3), kế hoạch này thiết kế để
không ảnh hưởng chain hiện tại — nhưng đây là cam kết đến từ QUY TRÌNH kiểm chứng, không phải
từ bản chất "chỉ đóng gói lại thì chắc chắn an toàn". B1/B3/B4 là các bước thực sự đụng vào
logic thực thi và cần được đối xử với mức cẩn trọng như bất kỳ thay đổi consensus-critical
nào khác.

---

## 5. Việc còn mở / cần bạn quyết định trước khi code

- Hướng xử lý `ExtensionGetOrCreateSimpleDb` (B3): giữ ở Go 2-pha, hay đưa hẳn vào C++?
- API lưu trữ thật của OHtee cho Xapian (B4): tmpfs trong TA, hay storage API riêng của SDK?
- Xác nhận: TA của OHtee/tzdriver có hỗ trợ C++ với exception/RTTI/multi-threading như
  `libmvm_linker.a` hiện dùng không, hay có giới hạn subset ngôn ngữ cần biết trước để B1-B2
  không code ra thứ không build được cho target thật.
