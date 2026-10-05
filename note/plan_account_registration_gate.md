# Kế hoạch: chặn người gửi chưa đăng ký trên Parent Chain (Account Registration Gate)

> Ngày lập: 2026-10-05. Dành cho agent thực thi. **Chưa có dòng code nào của kế hoạch này được viết.**
> Mục tiêu: một địa chỉ chỉ được **gửi giao dịch** trên cụm thực thi mà nó đã đăng ký trên Parent Chain (`AccountRegistry`, 1 địa chỉ ↔ 1 cụm). Nhờ đó replay xuyên cụm (kể cả khi `chainId` bị cấu hình trùng) không còn khả thi, vì tài khoản chỉ tồn tại như một "người gửi" ở đúng một cụm.
> Phạm vi: chế độ `tx_signature_mode = "secp"` (chain mới, không có lịch sử). Chain `bls_legacy` KHÔNG đổi.

---

## 0. Đọc trước khi làm (bắt buộc)

1. `AGENTS.md` (Zero-Fork Invariant: thà pending chứ không fork; KHÔNG dùng timeout/sleep để quyết định commit/dispatch; mọi queue/worker phải có giới hạn; không blocking I/O trong vòng async). `PROJECT_STRUCTURE.md` (cập nhật khi thêm module/đổi proto/đổi kênh).
2. `note/secp256k1_proto_tcp_architecture_design.md` (§0 hai chế độ, §10 chain ID & replay xuyên cụm) — bối cảnh vì sao cần kế hoạch này.
3. Memory/ghi chú vận hành quan trọng:
   - Cụm `deploy/cluster/local_parent_chain/` (cổng 18601-18604, 19001-19604) là **của người dùng**: không kill/reset/ghi đè. Dựng cụm test riêng, cổng/thư mục khác.
   - `mtn-orchestrator.sh` dùng `pkill -f "simple_chain.*config-master-node"` → tự giết shell nếu dòng lệnh của bạn chứa cụm đó; chạy qua file script.
   - `tx_processor.InitParentChainGatewayHandler` là `sync.Once` singleton: test phải tạo handler trực tiếp.
   - Báo cáo PASS của agent/script từng sai: chỉ ghi PASS khi đã đọc output thật; kiểm tra bằng đột biến (mutation) cho test bảo mật.
   - Không `git add <thư mục>`; `git add <file>`. Không `git push` khi chưa hỏi người dùng.

## 1. Sự thật đã xác minh trong code (đừng đoán lại)

| Sự thật | Vị trí |
| :--- | :--- |
| `RegisterAccount` cần **cả** chữ ký BLS của cụm **và** ECDSA của người dùng, thông điệp có domain tag `REGISTER_ACCOUNT_V1:` gắn với khoá cụm; một địa chỉ chỉ đăng ký 1 lần (`ErrAccountAlreadyRegistered`); danh tính node (địa chỉ = `keccak(blsPub)[12:]`) tự đăng ký chỉ cần chữ ký cụm | `execution/pkg/parentchain/state.go:464` |
| `AccountRegistry` hiện CHỈ dùng để định tuyến chuyển tiền xuyên chain (`GetAccountRegistry`), mempool/thực thi KHÔNG tra nó | `pkg/rollup/worker_send.go:169`, `cmd/simple_chain/mtn_api.go:873` |
| Cụm định danh bằng `FloatIdentityKey` (BLS); chain ID chỉ là trường mô tả | `pkg/parentchain/store.go:21` |
| Đăng ký cụm không cần cấp phép (`min_float_to_register`) | `pkg/parentchain/genesis.go:53` |
| Mẫu "parent → cụm thực thi" đã chạy: `GetInboundTransfers(pubKey, cursor)` ở `Client` interface, `QuorumClient` (xác minh đồng thuận f+1 giữa các node parent), `http_rpc.go`, `DBStore/TreeStore/MemoryStore`; `ReceiveWorker` poll theo cursor rồi biến thành **system tx** qua `eventProposer` (`app.go:394`) → `RollupSystemHandler` → `HandleSystemEvent` | `pkg/parentchain/client.go:48`, `quorum_client.go:511`, `pkg/rollup/worker_receive.go:150` |
| State rollup nằm trong SmartContract DB dưới địa chỉ hệ thống `RollupSystemAddress = 0x…72` (key = keccak(namespace‖id)) | `pkg/rollup/store.go:16,64` |
| Tx hệ thống do node ký bằng khoá BLS của chính nó (danh tính BLS), nonce cấp bởi `eventProposer` | `cmd/simple_chain/app.go:394-470` |
| Chế độ `secp`: `sigPolicy`, `isNodeBLSIdentity`, bộ lọc thực thi `FilterInvalidSignatures`, ép ChainID mọi loại tx | `pkg/blockchain/tx_processor/signature_enforcement.go`, `validation.go` |
| Quy tắc nonce-0 (`InvalidAddressMatchForTx0`) đã được bỏ cho người dùng secp | `validation.go` (`secpUser`) |

**Chưa xác minh — việc đầu tiên của agent (đọc code, không phỏng đoán):**
- (V1) SmartContract DB storage có nằm trong cam kết state (header root) không? Nếu KHÔNG, cờ đăng ký phải nằm ở `AccountState` (đổi proto `state.proto`, §3.2 phương án B) để mọi replica/ sync đồng nhất. Đọc `pkg/blockchain/chain_state.go`, `alignSmartContractStorage`, cách header tính root.
- (V2) `eventProposer` được gọi bởi mọi validator hay chỉ leader/node chỉ định? Hai node cùng đề xuất một event có đụng nhau (pool key `(sender, nonce)`) không? Đọc comment dài trong `app.go:394-440` và `rollup_system_handler.go`.
- (V3) Genesis alloc được nạp ở đâu (`pkg/config/genesis.go`, `app_blockchain.go`) để ghi cờ "đã đăng ký" lúc genesis.
- (V4) Parent chain đã có chỗ nào ghi sự kiện "account registered" có thứ tự chưa? (`SetAccountRegistry` chỉ ghi ánh xạ; cần thêm chỉ mục theo cụm có `seq` tăng dần.)

## 2. Thiết kế

### 2.1 Nguyên tắc

- **KHÔNG truy vấn parent lúc xác thực/thực thi.** Mỗi validator có thể thấy parent ở thời điểm khác nhau → verdict khác nhau → fork. Việc đăng ký phải đi vào chain thực thi như **system event có thứ tự**; quy tắc gate là hàm thuần của state thực thi.
- Chưa đăng ký ⇒ **chờ/từ chối có thể thử lại**, không đoán, không timeout quyết định.
- Chỉ chặn **người gửi** (originator của tx). KHÔNG chặn người nhận / tạo tài khoản bằng cách nhận (sẽ phá EVM: `CREATE/CREATE2`, precompile, địa chỉ hệ thống, phí validator, địa chỉ đốt). Tiền nhận ở cụm không phải "nhà" không tiêu được — ghi rõ trong tài liệu người dùng.
- Miễn gate: danh tính node (`isNodeBLSIdentity`) và tx hệ thống của nó.
- Cưỡng chế ở **hai tầng cùng một hàm thuần**: (a) admission `VerifyTransaction` (trả lỗi có thể thử lại), (b) bộ lọc thực thi consensus (drop, giống chữ ký sai).

### 2.2 Phía Parent Chain — chỉ mục đăng ký theo cụm

- Khi `RegisterAccount` thành công, ghi thêm một bản ghi `AccountRegisteredEvent{ Seq, UserAddress, ClusterKey, ParentBlock }` vào chỉ mục theo `ClusterKey`, `Seq` tăng đơn điệu theo cụm, **trong cùng bước chuyển state** (nằm trong proof/NOMT như `GetInboundTransfers`).
- Thêm API cùng mẫu `GetInboundTransfers`:
  - `Store`: `GetAccountRegistrations(clusterKeyHash common.Hash, cursor uint64) ([]*AccountRegisteredEvent, uint64, error)` — cài ở `MemoryStore`, `DBStore`, `TreeStore` (`pkg/parentchain/store.go`, `db_store.go`, `tree_store.go`).
  - `Client` interface: `GetInboundAccountRegistrations(pubKey cm.PublicKey, cursor uint64) ([]*AccountRegisteredEvent, uint64, error)`; cài ở `http_rpc.go` (server handler + `httpClient`), `quorum_client.go` (**yêu cầu f+1 node parent đồng ý**, giống `GetInboundTransfers`, không đồng ý ⇒ lỗi, không áp dụng).
  - Giới hạn kích thước trang (ví dụ ≤ 256 sự kiện/lần) để bounded.
- Parent chain là BFT có finality, chỉ trả sự kiện từ khối đã commit.

### 2.3 Phía cụm thực thi

**State:** cờ `registered[addr] = true` + con trỏ `registrationCursor`.
- Phương án A (nếu V1 xác nhận SC DB nằm trong state root): địa chỉ hệ thống mới `AccountRegistrySystemAddress` (chọn địa chỉ chưa dùng, ví dụ `0x…73`; grep tránh va chạm với `pkg/common` constants và precompile), key `keccak("acct_registered_v1" ‖ addr)` → `0x01`; cursor ở key riêng. Mẫu code: `pkg/rollup/store.go`.
- Phương án B (nếu SC DB không nằm trong state root): thêm trường vào `AccountState` (proto + `pkg/state/account_state.go`, serialization, mvm/FFI nếu có) — thay đổi định dạng state, cần wipe + redeploy.

**Truy cập (hàm thuần):** `ChainState.IsAccountRegistered(addr common.Address) bool`, đọc state cục bộ.

**Luồng sự kiện:**
1. `RegistrationWorker` (file mới `pkg/rollup/worker_registration.go`, theo khuôn `ReceiveWorker`): poll `GetInboundAccountRegistrations(blsKeyPair.PublicKey(), cursor)` bằng `QuorumClient`; mỗi sự kiện → system tx qua cùng `eventProposer`/nonce allocator (`app.go`). Hàng đợi/batch có giới hạn, một goroutine, không blocking I/O trong vòng xử lý chung; cursor lưu bền, chỉ tiến sau khi event đã được áp dụng on-chain (đọc cờ để xác nhận), poll lại an toàn vì áp dụng idempotent.
2. System tx tới `RollupSystemAddress` với payload có `Kind`/loại mới `account_registered {User, ClusterKey, ParentSeq}`; `RollupSystemHandler` rẽ nhánh sang `AccountRegistryHandler.Apply` (file mới, ví dụ `pkg/rollup/account_registry_handler.go`):
   - từ chối nếu `ClusterKey` ≠ khoá BLS của cụm này;
   - idempotent: đã có cờ ⇒ no-op thành công;
   - ghi cờ + tăng cursor; tiêu nonce như các system event khác (xem comment `rollup_system_handler.go` về nonce khi lỗi).
   - Không dùng/đụng máy trạng thái `Next(...)` của chuyển tiền; đây là loại sự kiện riêng.

**Gate (người gửi):**
- Thêm lỗi mới `transaction.AccountNotRegistered` (`pkg/transaction/transaction_error.go`, đăng ký vào bảng mã lỗi).
- `sigPolicy` (hoặc hàm kề bên) có `senderRegisteredError(tx, as)`:
  `secp && !isNodeBLSIdentity(tx, as) && !chainState.IsAccountRegistered(tx.FromAddress())` ⇒ lỗi.
- `VerifyTransaction`: gọi sau xác thực chữ ký (trả mã thử lại được, không giữ trong pool).
- Bộ lọc thực thi (`verifySignatures`/`checkTxSignature`/`FilterInvalidSignatures`/`PreVerifySignatures`/`PrewarmSignatureCache`): cần truy cập registry → đổi chữ ký hàm để nhận một `registry func(common.Address) bool` (hoặc truyền `*ChainState`). Drop tx của người gửi chưa đăng ký (không receipt, không tăng nonce) — **phải là hàm thuần của state trước block**, giống drop chữ ký sai.
- Lưu ý khác biệt thứ tự: tx của người dùng trong cùng block với system tx đăng ký của chính họ sẽ bị drop (state trước block). Chấp nhận; admission đã chặn không cho gửi sớm.

**Genesis:** mọi địa chỉ trong `alloc` được đánh dấu đã đăng ký lúc nạp genesis (cộng danh sách tường minh `registered_accounts` tuỳ chọn). Nếu không, tài khoản dev/devnet không dùng được chain.

**Cấu hình:** `config.json` thêm `account_gate` (`""`/`"off"` mặc định, `"parent_registered"`), chỉ hợp lệ khi `tx_signature_mode="secp"` (kiểm tra ở `LoadConfig` giống `validateTxSignatureMode`; giá trị lạ ⇒ không khởi động). Mọi validator của một chain PHẢI cùng giá trị (lệch ⇒ fork).

### 2.4 Quyết định đã chốt (mặc định) và việc để lại

| # | Quyết định | Trạng thái |
| :--- | :--- | :--- |
| D1 | Chặn người gửi, không chặn người nhận | Đề xuất, người dùng đã đồng ý hướng "chặn hẳn" |
| D2 | Cờ đăng ký nạp qua system event có thứ tự, không truy vấn parent lúc thực thi | Chốt (an toàn fork) |
| D3 | Genesis alloc tự động coi là đã đăng ký | Mặc định; **hỏi người dùng** nếu muốn liệt kê tường minh |
| D4 | Huỷ/chuyển cụm | **Chưa làm ở giai đoạn 1.** Hiện người dùng bị khoá vĩnh viễn vào một cụm (parent chưa có unregister). Giai đoạn 2: cần thiết kế cả hai bên đồng ý + quy tắc số dư |
| D5 | Parent không truy cập được | Chỉ ảnh hưởng onboarding mới; tài khoản đã đăng ký vẫn chạy bình thường. Không dùng timeout để quyết định gì |

## 3. Các bước thực hiện (theo thứ tự, mỗi bước có test và build_check)

| Bước | Việc | Tệp chính | Tiêu chí xong |
| :--- | :--- | :--- | :--- |
| 0 | Trả lời V1-V4 bằng cách đọc code; ghi kết quả vào đầu file này | — | V1-V4 có đáp án có dẫn chứng `file:dòng` |
| 1 | Parent: chỉ mục + `GetAccountRegistrations` (3 store) + test vòng ghi/đọc/cursor/phân trang | `pkg/parentchain/{store,db_store,tree_store,state}.go` | `RegisterAccount` sinh đúng 1 event, `Seq` đơn điệu theo cụm, đăng ký trùng không sinh event |
| 2 | Parent: `Client`/`http_rpc`/`QuorumClient` API + test (f+1 đồng ý; 1 node nói dối ⇒ không tiến) | `client.go`, `http_rpc.go`, `quorum_client.go` | test quorum bất đồng ⇒ lỗi |
| 3 | Exec: state cờ + `IsAccountRegistered` + cursor (phương án A hoặc B theo V1) | `pkg/rollup/…` hoặc `pkg/state` | round-trip ghi/đọc, sống qua restart/snapshot |
| 4 | Exec: `AccountRegistryHandler` + nhánh trong `RollupSystemHandler` | `pkg/rollup/account_registry_handler.go`, `tx_processor/rollup_system_handler.go` | idempotent; sai `ClusterKey` bị từ chối; nonce xử lý đúng khi lỗi |
| 5 | Exec: `RegistrationWorker` + nối vào `app.go` (khởi động/dừng, hàng đợi có giới hạn) | `pkg/rollup/worker_registration.go`, `cmd/simple_chain/app.go` | không goroutine rò rỉ; poll lại không áp dụng hai lần |
| 6 | Gate: lỗi mới, `sigPolicy`, `VerifyTransaction`, bộ lọc thực thi, đổi chữ ký hàm + cập nhật mọi nơi gọi/test | `tx_processor/{validation,signature_enforcement}.go`, `transaction_error.go` | ma trận gate (§4) xanh; chain `bls_legacy` không đổi verdict |
| 7 | Genesis: ghi cờ cho alloc + `registered_accounts` | `pkg/config/genesis.go`, `app_blockchain.go` | chain mới khởi động, tài khoản alloc gửi được tx |
| 8 | Config `account_gate` + validate + mặc định | `pkg/config/config.go` (+test) | giá trị lạ ⇒ lỗi; chỉ hợp lệ với `secp` |
| 9 | Tài liệu: `note/secp256k1_proto_tcp_architecture_design.md` (§mới), `PROJECT_STRUCTURE.md` (module mới, đổi kênh parent↔exec, đổi proto nếu có), runbook onboarding người dùng | — | — |
| 10 | E2E trên devnet cô lập (xem §5) | — | báo cáo thật trong `note/` |

Các bước 1-2 (parent) và 3-5 (exec) độc lập, có thể giao song song cho hai agent; bước 6 phụ thuộc 3.

## 4. Kế hoạch test

**Đơn vị / tích hợp (bắt buộc, chạy `go test -race` cho package liên quan):**
1. Parent: đăng ký → đúng 1 event; đăng ký trùng → lỗi và không event; cursor/phân trang; `Seq` đơn điệu theo cụm; cụm A không thấy event của cụm B.
2. QuorumClient: đủ f+1 đồng ý mới tiến; 1 node parent trả dữ liệu sai ⇒ không áp dụng.
3. Handler: idempotent; sai `ClusterKey` ⇒ từ chối; replay cùng event ⇒ no-op; **tính xác định**: hai replica áp cùng chuỗi event ⇒ cùng state root / cùng giá trị cờ.
4. Ma trận gate (bảng người gửi × chế độ): {đã đăng ký, chưa đăng ký, danh tính node, tài khoản genesis} × {`secp`+gate bật, `secp`+gate tắt, `bls_legacy`} × {admission, bộ lọc thực thi}; admission và bộ lọc luôn cùng verdict.
5. Hồi quy: toàn bộ test hiện có của `tx_processor`, `rollup`, `parentchain`, `config` vẫn xanh; chain `bls_legacy` không đổi.
6. Replay xuyên cụm (kịch bản gốc của kế hoạch): hai cụm cùng `chainId`; một địa chỉ đăng ký ở cụm A; tx đã ký nộp vào cụm B ⇒ bị từ chối `AccountNotRegistered` (và bị drop ở bộ lọc thực thi nếu proposer Byzantine đưa vào block).
7. Fuzz/ngẫu nhiên: payload system event rác không panic, không ghi cờ.
8. **Kiểm tra đột biến** cho mọi test bảo mật: bỏ gate / bỏ kiểm tra `ClusterKey` / bỏ idempotent ⇒ test tương ứng phải fail. Ghi kết quả thật.

**Bất biến cần chứng minh:** (i) không đường nào làm một địa chỉ gửi được ở hai cụm; (ii) verdict gate là hàm thuần của `(tx, state trước block, policy)`; (iii) parent chậm/sập không làm tx của tài khoản đã đăng ký bị chặn; (iv) không timeout quyết định commit.

## 5. E2E (cụm cô lập)

- KHÔNG dùng/đụng cụm parent_chain của người dùng. Dựng devnet riêng bằng `execution/scripts/test/run_devnet.sh` (parent + exec1 + exec2) hoặc `mtn-orchestrator.sh`, cổng/thư mục riêng; đọc kỹ script trước khi chạy; chạy qua file script (xem §0). Lưu ý devnet hiện cho exec1/exec2 cùng `chainId` 991 — đây chính là kịch bản kiểm thử replay.
- Trước E2E: xác nhận cụm tạo được block (lần trước cụm dev 5 node không tạo block do startup-sync; commit `92ba56db` của agent khác đã xử lý — kiểm lại).
- Kịch bản: (1) người dùng ký tx trước khi đăng ký ⇒ bị từ chối; (2) đăng ký trên parent (user + cluster signature) ⇒ sự kiện về exec ⇒ tx chạy, có receipt; (3) cùng tx nộp sang cụm kia ⇒ từ chối; (4) restart node ⇒ cờ còn; (5) parent tạm dừng ⇒ tài khoản đã đăng ký vẫn gửi được, onboarding mới chờ; (6) so sánh state root giữa các node.
- Báo cáo thật (không ghi PASS nếu chưa đọc output) trong `note/account_gate_e2e_<ngày>.md`.

## 6. Rủi ro và điều cần cảnh báo người dùng

- Đổi state/định dạng ⇒ **wipe + redeploy đồng loạt**; mọi validator cùng `tx_signature_mode` và `account_gate`.
- Người dùng bị khoá vào cụm đã đăng ký (D4); tiền nhận nhầm ở cụm khác không tiêu được.
- Cụm kiểm soát onboarding (cần chữ ký BLS của cụm): cụm có thể từ chối đăng ký người dùng.
- Độ trễ onboarding = finality parent + thời gian relay + thời gian system tx vào block.
- Cụm không bật gate vẫn chấp nhận tx của tài khoản không đăng ký — chỉ ảnh hưởng người dùng của chính cụm đó (đúng như mô hình tin cậy cụm).
- Không đủ để thay thế chain ID duy nhất cho định danh xuyên chain (Root Anchor `ChainRegistry`); hai hướng bổ trợ nhau.

## 7. Định nghĩa hoàn thành

1. `build_check.sh` (Go + Rust + FFI) pass, không warning mới.
2. Mọi test mới + hồi quy xanh với `-race`; test bảo mật đã kiểm tra bằng đột biến.
3. Kịch bản replay xuyên cụm (§4.6) bị chặn, có bằng chứng.
4. E2E trên devnet cô lập có receipt thật hoặc báo cáo trung thực lý do chưa chạy được.
5. `PROJECT_STRUCTURE.md` và tài liệu thiết kế đã cập nhật; commit theo từng nhóm tệp, KHÔNG push khi chưa được phép.
6. Khối tóm tắt tiếng Việt theo `AGENTS.md` Part 5 ở cuối báo cáo.
