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

**Các câu hỏi V1-V4 — ĐÃ được trả lời bằng đọc code (2026-10-05, agent lập kế hoạch). Agent thực thi chỉ cần xác nhận lại nhanh, đừng làm lại từ đầu:**

- **V1 (SC DB có nằm trong cam kết state không?) → KHÔNG trực tiếp.** `BlockHeader` chỉ có `AccountStatesRoot`, `StakeStatesRoot`, `ReceiptRoot`, `TransactionsRoot`… không có root của contract storage (`pkg/proto/block.proto:11-31`). Contract storage nằm trong namespace NOMT dùng chung (`SharedContractStorageNamespace`) với root ghi riêng ở changelog theo block (`chain_state.go:529-555`, `alignSmartContractStorage`); `loadStorageTrie` lấy root từ `AccountState.SmartContractState().StorageRoot()` và địa chỉ hệ thống `0x…72` không có `SmartContractState` (`smart_contract_db.go:906-911`). → **Cờ đăng ký KHÔNG nên đặt trong SC DB** (không được header cam kết, lệch giữa replica chỉ lộ gián tiếp qua tx bị thực thi khác nhau). **Dùng phương án B: cờ trong `AccountState`.**
- **V2 (ai đề xuất system event?) → mỗi node đề xuất bằng danh tính BLS RIÊNG của nó** (`app.keyPair.Address()`, nonce riêng, `app.go:394-440`), hàng đợi pool khoá theo `(sender, nonce)`. Hệ quả: nhiều node có thể đề xuất cùng một sự kiện đăng ký ⇒ handler BẮT BUỘC idempotent. **Còn phải xác nhận:** các node trong một cụm có dùng chung khoá BLS với "khoá cụm" (`floatIdentityKey` dùng ở `RegisterAccount`) không? Quyết định kiểm tra `ClusterKey == khoá BLS của cụm` dựa vào đó.
- **V3 (genesis alloc nạp ở đâu?) →** `cmd/simple_chain/app_blockchain.go` (các vòng `for _, account := range app.genesis.Alloc` ở ~1037, 1058, 1303), chuyển `state.JsonAccountState` → `AccountState`. Với phương án B chỉ cần đặt cờ ở bước chuyển đổi này.
- **V4 (parent đã có chỉ mục sự kiện đăng ký chưa?) → CHƯA.** `SetAccountRegistry` chỉ ghi ánh xạ địa chỉ→khoá cụm (`state.go:464-500`); chỉ có chỉ mục chuyển tiền (`GetInboundTransfers`, `store.go:71`). Phải thêm.

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

**State (đã chốt phương án B):** thêm trường `bool ParentRegistered = 10;` vào `message AccountState` (`pkg/proto/state.proto`) + getter/setter trong `pkg/state/account_state.go`, **cập nhật `Copy()`**, `JsonAccountState` (genesis), `state_merger.go` (nơi gộp thay đổi tài khoản của Block-STM), mutation trong `account_state_db_mutations.go` (`SetParentRegistered(addr)`, đi cùng đường với `SetNonce/AddBalance` mà `liveAccountStateAccessor` đã dùng), và mọi codec đọc `AccountState` (kiểm `pkg/mvm/*codec*`, tz). Trường `false` mặc định không sinh byte protobuf ⇒ **tài khoản cũ giữ nguyên root**; chỉ tài khoản được đăng ký mới thay đổi. Vì cờ nằm trong `AccountState`, nó được `AccountStatesRoot` cam kết trực tiếp, đi cùng snapshot/state-sync, và cổng gate **không cần tra cứu thêm**: `VerifyTransaction` và `verifySignatures` đã nạp sẵn `AccountState` người gửi (`as`/`loadState(i)`). Cursor của worker chỉ là vị trí poll cục bộ (không phải consensus state), lưu bền cục bộ như cursor của `ReceiveWorker`; áp dụng sự kiện idempotent nên poll lại an toàn.

**Truy cập (hàm thuần):** `as.ParentRegistered()` trên `AccountState` trước block.

**Luồng sự kiện:**
1. `RegistrationWorker` (file mới `pkg/rollup/worker_registration.go`, theo khuôn `ReceiveWorker`): poll `GetInboundAccountRegistrations(blsKeyPair.PublicKey(), cursor)` bằng `QuorumClient`; mỗi sự kiện → system tx qua cùng `eventProposer`/nonce allocator (`app.go`). Hàng đợi/batch có giới hạn, một goroutine, không blocking I/O trong vòng xử lý chung; cursor lưu bền, chỉ tiến sau khi event đã được áp dụng on-chain (đọc cờ để xác nhận), poll lại an toàn vì áp dụng idempotent.
2. System tx tới `RollupSystemAddress` với payload có `Kind`/loại mới `account_registered {User, ClusterKey, ParentSeq}`; `RollupSystemHandler` rẽ nhánh sang `AccountRegistryHandler.Apply` (file mới, ví dụ `pkg/rollup/account_registry_handler.go`):
   - từ chối nếu `ClusterKey` ≠ khoá BLS của cụm này;
   - idempotent: đã có cờ ⇒ no-op thành công;
   - ghi cờ + tăng cursor; tiêu nonce như các system event khác (xem comment `rollup_system_handler.go` về nonce khi lỗi).
   - Không dùng/đụng máy trạng thái `Next(...)` của chuyển tiền; đây là loại sự kiện riêng.

**Gate (người gửi):**
- Thêm lỗi mới `transaction.AccountNotRegistered` (`pkg/transaction/transaction_error.go`, đăng ký vào bảng mã lỗi).
- `sigPolicy` có `senderRegisteredError(tx, as)`:
  `secp && gate bật && !isNodeBLSIdentity(tx, as) && !as.ParentRegistered()` ⇒ lỗi. Dùng `as` đã nạp sẵn nên **không phải đổi chữ ký các hàm** `verifySignatures/checkTxSignature`.
- `VerifyTransaction`: gọi sau xác thực chữ ký (trả mã thử lại được, không giữ trong pool).
- Bộ lọc thực thi (`checkTxSignature` và các hàm gọi nó): thêm cùng kiểm tra trên `as`; drop tx của người gửi chưa đăng ký (không receipt, không tăng nonce) — **hàm thuần của state trước block**, giống drop chữ ký sai. Lưu ý `sigCacheKey` chỉ ghi nhớ kết quả chữ ký: kiểm tra đăng ký phải nằm NGOÀI cache (state đổi theo thời gian).
- Lưu ý khác biệt thứ tự: tx của người dùng trong cùng block với system tx đăng ký của chính họ sẽ bị drop (state trước block). Chấp nhận; admission đã chặn không cho gửi sớm.

**Genesis:** mọi địa chỉ trong `alloc` được đặt `ParentRegistered=true` lúc chuyển `JsonAccountState`→`AccountState` (cộng danh sách tường minh `registered_accounts` tuỳ chọn). Nếu không, tài khoản dev/devnet không dùng được chain.

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
| 0 | Xác nhận nhanh V1-V4 (đã có đáp án ở §1) và câu hỏi còn lại của V2 (khoá node vs khoá cụm) | — | Ghi kết quả có dẫn chứng `file:dòng` |
| 1 | Parent: chỉ mục + `GetAccountRegistrations` (3 store) + test vòng ghi/đọc/cursor/phân trang | `pkg/parentchain/{store,db_store,tree_store,state}.go` | `RegisterAccount` sinh đúng 1 event, `Seq` đơn điệu theo cụm, đăng ký trùng không sinh event |
| 2 | Parent: `Client`/`http_rpc`/`QuorumClient` API + test (f+1 đồng ý; 1 node nói dối ⇒ không tiến) | `client.go`, `http_rpc.go`, `quorum_client.go` | test quorum bất đồng ⇒ lỗi |
| 3 | Exec: trường `ParentRegistered` trong `AccountState` (proto, `Copy`, `JsonAccountState`, `state_merger`, mutation, codec) | `pkg/proto/state.proto`, `pkg/state`, `pkg/account_state_db`, `tx_processor/state_merger.go`, `pkg/mvm` | round-trip marshal/copy/merge; sống qua restart/snapshot; **root tài khoản cũ không đổi** (test so root trước/sau thêm trường) |
| 4 | Exec: `AccountRegistryHandler` + nhánh trong `RollupSystemHandler` | `pkg/rollup/account_registry_handler.go`, `tx_processor/rollup_system_handler.go` | idempotent; sai `ClusterKey` bị từ chối; nonce xử lý đúng khi lỗi |
| 5 | Exec: `RegistrationWorker` + nối vào `app.go` (khởi động/dừng, hàng đợi có giới hạn) | `pkg/rollup/worker_registration.go`, `cmd/simple_chain/app.go` | không goroutine rò rỉ; poll lại không áp dụng hai lần |
| 6 | Gate: lỗi mới, `sigPolicy.senderRegisteredError`, `VerifyTransaction`, `checkTxSignature` (kiểm tra ngoài cache chữ ký) | `tx_processor/{validation,signature_enforcement}.go`, `transaction_error.go` | ma trận gate (§4) xanh; chain `bls_legacy` không đổi verdict |
| 7 | Genesis: đặt cờ cho alloc + `registered_accounts` | `pkg/config/genesis.go`, `app_blockchain.go` (~1037/1058/1303), `JsonAccountState` | chain mới khởi động, tài khoản alloc gửi được tx |
| 8 | Config `account_gate` + validate + mặc định | `pkg/config/config.go` (+test) | giá trị lạ ⇒ lỗi; chỉ hợp lệ với `secp` |
| 9 | Tài liệu: `note/secp256k1_proto_tcp_architecture_design.md` (§mới), `PROJECT_STRUCTURE.md` (module mới, đổi kênh parent↔exec, đổi proto nếu có), runbook onboarding người dùng | — | — |
| 10 | E2E trên devnet cô lập (xem §5) | — | báo cáo thật trong `note/` |

Các bước 1-2 (parent) và 3-5 (exec) độc lập, có thể giao song song cho hai agent; bước 6 phụ thuộc 3.

## 4. Kế hoạch test (BẮT BUỘC, đầy đủ — không bỏ mục nào)

### 4.0 Quy tắc chống "xanh giả" (đã từng gặp trong dự án này)

- Mỗi test **từ chối/âm tính** phải kiểm tra **đúng mã lỗi / thông điệp mong đợi**, không chỉ "có lỗi". Ví dụ đã gặp: bộ test bảo mật báo PASS cho "nonce cũ / gas 0 / thiếu số dư" nhưng thực ra tx bị từ chối vì lý do khác (`account has no BLS public key`). Test phải chứng minh tx bị chặn **vì quy tắc đang kiểm**, và có ca đối chứng tx tương tự **được chấp nhận** (để chứng minh test không từ chối mọi thứ).
- Mỗi test bảo mật phải qua **kiểm tra đột biến**: tạm bỏ/đảo quy tắc ⇒ test tương ứng phải FAIL; khôi phục ⇒ PASS. Ghi lại từng đột biến đã thử và kết quả thật trong báo cáo.
- Không ghi PASS nếu chưa đọc output thật. Dán lệnh + phần đầu/cuối output + hash cho mọi kết quả E2E.
- Hàm xác thực/thực thi mới phải là hàm thuần; mọi test xác định (không phụ thuộc đồng hồ, thứ tự goroutine, map iteration).

### 4.1 Đơn vị / tích hợp (chạy `go test -race -count=3` cho package liên quan)

| ID | Phạm vi | Khẳng định cần chứng minh |
| :--- | :--- | :--- |
| P1 | Parent chỉ mục | `RegisterAccount` thành công ⇒ đúng 1 event; đăng ký trùng ⇒ `ErrAccountAlreadyRegistered` và không event; `Seq` tăng đơn điệu **theo cụm**; cụm A không thấy event của cụm B; phân trang/cursor không bỏ sót, không lặp; cùng kết quả ở `MemoryStore`, `DBStore`, `TreeStore` (test bảng dùng chung cho 3 store) |
| P2 | Parent proof | Sự kiện nằm trong state có proof (NOMT) giống chuyển tiền |
| Q1 | QuorumClient | đủ f+1 đồng ý mới tiến cursor; 1 node nói dối / thiếu ⇒ lỗi, KHÔNG áp dụng; bất đồng giữa node ⇒ lỗi; cursor không tiến khi lỗi |
| S1 | `AccountState` | marshal/unmarshal/`Copy()` giữ `ParentRegistered`; **root của tài khoản cũ (trường = false) không đổi so với trước khi thêm trường** (so hash cố định); trường `true` đổi root; state merger không làm mất cờ khi gộp Block-STM |
| H1 | Handler | sai `ClusterKey` ⇒ từ chối đúng lỗi; đã đăng ký ⇒ no-op thành công (idempotent); payload rác/thiếu trường ⇒ lỗi, không panic, không ghi cờ; lỗi vẫn tiêu nonce đúng như các system event khác |
| H2 | **Tính xác định** | hai/ba replica độc lập áp **cùng chuỗi event** (kể cả bị xáo trộn thứ tự nhận nhưng cùng thứ tự thực thi, và sự kiện bị gửi trùng) ⇒ cùng `AccountStatesRoot` và cùng giá trị cờ |
| G1 | Ma trận gate | {đã đăng ký, chưa đăng ký, danh tính node, tài khoản genesis, tx hệ thống rollup} × {`secp`+gate bật, `secp`+gate tắt, `bls_legacy`} × {admission `VerifyTransaction`, bộ lọc thực thi}: kiểm tra từng ô bằng đúng mã lỗi; **admission và bộ lọc luôn cùng verdict** |
| G2 | Cache | tx bị chặn vì chưa đăng ký, sau khi đăng ký (cờ đổi) phải được chấp nhận ngay dù chữ ký đã nằm trong cache chữ ký (cache chỉ nhớ kết quả chữ ký, không nhớ gate) |
| G3 | Cùng block | tx của người dùng nằm cùng block với system tx đăng ký của chính họ ⇒ bị drop (state trước block) và gửi lại block sau được chấp nhận; verdict giống nhau trên mọi replica |
| G4 | Hồi quy | toàn bộ test hiện có của `tx_processor`, `rollup`, `parentchain`, `config`, `transaction`, `blockchain` xanh; chain `bls_legacy` và `secp`+gate tắt **không đổi verdict** (so với trước thay đổi, dùng bộ test chữ ký/chain-binding hiện có) |
| C1 | Config | `account_gate` giá trị lạ ⇒ không khởi động; chỉ hợp lệ khi `tx_signature_mode="secp"` |
| W1 | Worker | không rò rỉ goroutine; hàng đợi/batch có giới hạn (test với 10k sự kiện không phình bộ nhớ); poll lại sau restart không áp dụng hai lần; cursor chỉ tiến sau khi cờ đã nhìn thấy trong state |
| X1 | Replay xuyên cụm | hai cụm **cùng `chainId`**: địa chỉ đăng ký ở cụm A; tx đã ký nộp sang cụm B ⇒ từ chối `AccountNotRegistered` (admission) và bị drop (bộ lọc thực thi, mô phỏng proposer Byzantine). Ca đối chứng: cùng tx ở cụm A được chấp nhận |
| F1 | Fuzz | payload system event ngẫu nhiên (≥ 2000 vòng, seed cố định) không panic, không bao giờ đặt cờ |

### 4.2 Kiểm tra đột biến (ghi bảng kết quả)

Tối thiểu: (a) bỏ kiểm tra gate ở admission; (b) bỏ kiểm tra gate ở bộ lọc thực thi; (c) bỏ kiểm tra `ClusterKey`; (d) bỏ tính idempotent; (e) đặt kiểm tra gate TRONG cache chữ ký; (f) bỏ miễn trừ danh tính node. Mỗi đột biến phải làm ít nhất một test FAIL. Khôi phục rồi chạy lại toàn bộ.

### 4.3 Kiểm thử hỗn loạn / hồi phục (cụm thật, cô lập)

- `kill -9` một validator giữa lúc event đăng ký đang được áp dụng ⇒ sau khởi động lại không mất, không áp dụng đôi, các node cùng `AccountStatesRoot`.
- Parent tạm dừng ⇒ tài khoản đã đăng ký vẫn gửi được; đăng ký mới chờ; khi parent chạy lại, onboarding tiếp tục không cần can thiệp.
- Một node parent trả dữ liệu sai (giả lập) ⇒ cụm thực thi không áp dụng.
- Khởi động lại toàn bộ cụm ⇒ cờ còn (sống qua restart/snapshot/state-sync).

### 4.4 Tải / hiệu năng

- So sánh TPS và độ trễ admission trước/sau thay đổi trên cùng cấu hình (gate chỉ đọc một trường đã nạp sẵn, kỳ vọng không đổi đáng kể). Ghi số đo thật; nếu tụt > 5% phải giải thích.
- 10k tài khoản đăng ký hàng loạt: worker không nghẽn, hàng đợi có giới hạn, không làm chậm consensus.

## 5. Kiểm thử End-to-End (BẮT BUỘC, trên cụm cô lập)

### 5.1 Điều kiện tiên quyết

- KHÔNG dùng/đụng cụm parent_chain của người dùng (`deploy/cluster/local_parent_chain/`, cổng 18601-18604, 19001-19604) hay cụm 231/230. Dựng devnet riêng (`execution/scripts/test/run_devnet.sh`: parent + exec1 + exec2, hoặc `mtn-orchestrator.sh`), cổng/thư mục riêng; đọc kỹ script trước khi chạy; chạy qua file script (không để cụm `config-master-node` trong dòng lệnh shell của bạn — `pkill -f` sẽ tự giết nó).
- **Smoke thanh khoản TRƯỚC khi test tính năng:** gửi một tx thường và xác nhận `eth_blockNumber` tăng và có receipt. Lần thử trước (2026-10-05) cụm 5 node không tạo block (STARTUP-SYNC kẹt; genesis `epoch_timestamp_ms` cũ ~114 ngày); commit `92ba56db` của agent khác có vẻ đã sửa — kiểm lại, đừng giả định. Nếu cụm không tạo block thì báo cáo trung thực là E2E chưa chạy được, KHÔNG ghi PASS.
- Binary dựng từ đúng commit đang kiểm; ghi `git rev-parse HEAD` vào báo cáo.
- Devnet hiện có exec1/exec2 cùng `chainId` 991 — **giữ nguyên** làm kịch bản replay; thêm biến thể chain ID khác nhau làm đối chứng.

### 5.2 Kịch bản (mỗi kịch bản: lệnh, kết quả mong đợi, kết quả thật, bằng chứng)

| # | Kịch bản | Mong đợi |
| :--- | :--- | :--- |
| E1 | Smoke: tx thường từ tài khoản genesis | có receipt, block tăng, mọi node cùng block hash |
| E2 | Người dùng mới (chưa đăng ký) ký tx secp (ETH và `0xFF`) gửi vào exec1 | từ chối **đúng mã `AccountNotRegistered`**; số dư không đổi; nonce không đổi |
| E3 | Người dùng đăng ký trên parent (chữ ký ECDSA của user + BLS của cụm) | parent có event; sau độ trễ hữu hạn exec1 có cờ (đọc qua RPC `mtn_getAccountState`/tương đương); mọi node exec1 cùng `AccountStatesRoot` |
| E4 | Cùng người dùng gửi lại tx (ETH và `0xFF`) | thành công, có receipt, số dư đúng |
| E5 | **Replay:** lấy chính tx đã ký ở E4 nộp vào exec2 (cùng chainId 991, cùng địa chỉ có số dư ở exec2 qua genesis/nạp cục bộ) | từ chối `AccountNotRegistered`; số dư exec2 không đổi. Đối chứng: tắt gate ở exec2 ⇒ tx replay **được** thực thi (chứng minh kịch bản có nguy cơ thật khi không có gate) |
| E6 | Người dùng cố đăng ký lần hai ở cụm khác | parent từ chối `ErrAccountAlreadyRegistered` |
| E7 | Sai chữ ký người dùng / sai chữ ký cụm khi đăng ký | parent từ chối; không event |
| E8 | Tài khoản genesis (đã đánh dấu) gửi tx | thành công, không cần đăng ký |
| E9 | Danh tính node: tx hệ thống rollup / chuyển tiền xuyên chain vẫn chạy | bất biến (conservation guard `mtn_getConservation` giữ đúng) |
| E10 | Người nhận chưa đăng ký nhận tiền | thành công (không chặn người nhận); người nhận không gửi đi được cho tới khi đăng ký |
| E11 | Chaos (mục 4.3) trên cụm thật | đúng kỳ vọng ở 4.3 |
| E12 | Zero-fork: sau mọi kịch bản so sánh block hash + state root giữa TẤT CẢ node của từng cụm; `grep -E 'FORK|PANIC|DIVERGE|fork_guard'` log phải rỗng | trùng khớp 100% |
| E13 | Chain `bls_legacy` (cụm đối chứng): tx dapp BLS và tx ETH chạy như cũ; gate không có tác dụng | không hồi quy |

### 5.3 Báo cáo E2E

`note/account_gate_e2e_<YYYYMMDD>.md`: commit đã test, cấu hình cụm, từng kịch bản (lệnh, output thật rút gọn, hash tx/receipt, block hash/state root từng node), bảng đột biến, số đo hiệu năng, mọi bất thường và kịch bản **chưa chạy được kèm lý do thật**. Kết thúc bằng khối tóm tắt tiếng Việt theo `AGENTS.md` Part 5.

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
3. Kịch bản replay xuyên cụm (X1 và E5) bị chặn, có bằng chứng và có ca đối chứng khi tắt gate.
4. E2E trên devnet cô lập có receipt thật hoặc báo cáo trung thực lý do chưa chạy được.
5. `PROJECT_STRUCTURE.md` và tài liệu thiết kế đã cập nhật; commit theo từng nhóm tệp, KHÔNG push khi chưa được phép.
6. Khối tóm tắt tiếng Việt theo `AGENTS.md` Part 5 ở cuối báo cáo.
