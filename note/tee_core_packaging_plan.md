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

| Hàm | Định nghĩa Go | Vai trò |
|---|---|---|
| `GetChainId` | `mvm_api.go:1269` | đọc chain id |
| `GetBlockHash` | `mvm_api.go:1253` | đọc block hash theo số |
| `GetBlobHash` | `mvm_api.go:1361` | đọc versioned hash (EIP-4844) |
| `GetBlobBaseFee` | `mvm_api.go:1385` | đọc blob base fee |
| `GetCrossChainSender` | `mvm_api.go:1307` | đọc sender liên chain |
| `GetCrossChainSourceId` | `mvm_api.go:1334` | đọc source id liên chain |
| `GoLogString`/`GoLogBytes` | `logger.go:50`/`:93` | log ra ngoài giữa chừng |
| `ExtensionGetOrCreateSimpleDb` | `extension.go:288` | **mở 1 key-value DB runtime** cho precompile "SimpleDb" — không chỉ đọc context, còn đọc/ghi state phía Go giữa lúc contract đang chạy |

**Vì sao đây là vấn đề cho TA:** trong TA, gọi ngược Normal World giữa chừng = 1
world-switch tốn kém mỗi lần, và phá vỡ nguyên tắc "gộp cả batch vào 1 lệnh" (session
command kiểu GlobalPlatform vốn có hình dạng 1 lệnh vào → 1 kết quả ra, không thiết kế cho
callback đồng bộ giữa chừng). `ExtensionGetOrCreateSimpleDb` nặng nhất vì nó không chỉ đọc
context tĩnh mà đọc/ghi state runtime — cần quyết định thiết kế riêng (xem B3).

**Lưu ý:** `GetChainId`/`GetBlobHash` được thêm theo đúng 1 tiền lệ cố ý từ trước ("callback
về Go" — xem `project_evm_production_hardening` trong lịch sử làm việc) — tiền lệ đó hợp lý
cho host Linux thường nhưng chính là thứ phải bỏ ở đây.

`SetXapianBasePath`/`InitCppFileLog` cho thấy Xapian/mvm ghi thẳng filesystem qua path Host
truyền vào lúc khởi tạo — cần biết API lưu trữ thật của OHtee (tmpfs trong TA? storage API
riêng?) mới bọc đúng lớp trừu tượng (xem B4).

---

## 3. Kế hoạch từng bước (B1–B6)

**Nguyên tắc xuyên suốt: không đổi logic mvm/Xapian, chỉ đổi CÁCH dữ liệu ra/vào ranh giới.**

- **B1 — Gộp context Go→C++ thành 1 struct đầu vào duy nhất.** Trước khi gọi
  `execute`/`executeBatch`, Go tự thu thập sẵn chain id, block hash liên quan, blob
  hashes/base fee, cross-chain sender/source... đóng thành 1 `ExecutionContext` truyền vào
  1 lần. Xoá dần `GetChainId`/`GetBlockHash`/`GetBlobHash`/`GetBlobBaseFee`/
  `GetCrossChainSender`/`GetCrossChainSourceId` khỏi đường gọi giữa chừng.
- **B2 — Gộp log ra thành buffer trả về, bỏ callback log inline.** `GoLogString`/
  `GoLogBytes` → C++ tự tích log vào buffer nội bộ, trả về cùng `ExecuteResult` khi hàm kết
  thúc; Go in log sau khi nhận kết quả. Rẻ, rủi ro thấp, giảm ngay 2/8 callback.
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
- **B6 — Giả lập ranh giới TA ngay trên Linux (kiểm chứng sớm).** Sau B1-B5, viết 1 harness
  gọi `libmvm_linker.a` chỉ qua struct input/output serialize (không dùng con trỏ chia sẻ
  trực tiếp nữa) — lộ ra chỗ nào code còn ngầm giả định chung address space, sửa trước khi
  có phần cứng OHtee thật.

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
