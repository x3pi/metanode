# Kế hoạch: chặn người gửi chưa đăng ký trên Parent Chain (Account Registration Gate)

> Ngày lập: 2026-10-05. Dành cho agent thực thi. **Chưa có dòng code nào của kế hoạch này được viết.**
> Mục tiêu: một địa chỉ chỉ được **gửi giao dịch** trên cụm thực thi mà nó đã đăng ký trên Parent Chain (`AccountRegistry`, 1 địa chỉ ↔ 1 cụm). Nhờ đó replay xuyên cụm (kể cả khi `chainId` bị cấu hình trùng) không còn khả thi, vì tài khoản chỉ tồn tại như một "người gửi" ở đúng một cụm.
> Phạm vi: chế độ `tx_signature_mode = "secp"` (chain mới, không có lịch sử). Chain `bls_legacy` KHÔNG đổi.

---

## P0. ĐIỀU KIỆN TIÊN QUYẾT BẢO MẬT — phải xong trước khi làm bất cứ bước nào dưới đây

**Phát hiện (2026-10-05, đã chứng minh bằng test chạy thật):** `RollupSystemHandler` áp dụng system event chỉ từ payload JSON và **không kiểm tra ai gửi tx**. Đường nhận tiền `CreditObserved → RPCSubmitted → ClaimedConfirmed` cộng `Target/Value` thẳng từ event, không tra dữ liệu parent. Một tài khoản thường có tiền gas đã mint được 10²⁴ wei vào địa chỉ bất kỳ bằng ba tx đều `RETURNED`. Toàn bộ kế hoạch này (event đăng ký / khoá / thu hồi) đi qua đúng cửa này, nên nếu chưa đóng thì **mọi người dùng có thể tự đặt cờ đăng ký cho chính mình hoặc mint tiền**.

**Bản sửa đã viết và kiểm thử (đột biến xác nhận):** ĐÃ được tích hợp cùng nhánh gate (xem §9); nhánh tham chiếu `sec-rollup-system-auth` (commit `9b413aee`) dùng mã lỗi 69, bản tích hợp dùng mã `70` vì `69` đã là `AccountNotRegistered`. Chỉ danh tính node BLS-native (`isNodeBLSIdentity`: địa chỉ = `keccak(blsPub)[12:]`, khoá BLS đã đăng ký trong state) mới được gửi tx tới `RollupSystemAddress`:
- thực thi (có thẩm quyền): `isAuthorizedRollupSystemSender` ở ĐẦU `HandleTransaction`, sai ⇒ biên lai lỗi `UnauthorizedSystemSender` + tiêu nonce;
- admission: `VerifyTransaction` trả mã lỗi mới `69` (không có miễn trừ "sub node lagging" vì kẻ tấn công trông y hệt tài khoản lagging);
- test: `rollup_system_handler_auth_test.go` (giả mạo bị từ chối, ca đối chứng danh tính node vẫn credit, admission), test cũ `TestRollupSystemHandler_HandleTransaction` đổi sang gửi từ danh tính node (nó từng dựa vào việc thiếu kiểm tra).

**Tích hợp:** agent đang làm kế hoạch này cũng sửa `rollup_system_handler.go` (nhánh `registryHandler`, `rollup.IsAccountRegistrationPayload`). Khối kiểm tra quyền phải nằm **trước cả hai nhánh** (đăng ký và dispatcher) để nhánh đăng ký không bị giả mạo. Hãy lấy nhánh trên (`git cherry-pick 9b413aee` sau khi commit phần của bạn, giải xung đột thủ công) và thêm test: giả mạo event ĐĂNG KÝ từ tài khoản thường phải bị từ chối.

**Còn hở sau bản sửa (cần thiết kế riêng):** mọi danh tính node của cụm đều qua được kiểm tra; một validator Byzantine vẫn có thể gửi event giả (mint, đăng ký/khoá bừa). Event lấy từ parent phải mang **bằng chứng parent xác minh được** (chứng chỉ quorum / proof NOMT so với root parent đã neo trên chain thực thi) và handler kiểm bằng dữ liệu on-chain, không tin người đề xuất. Đây là việc của giai đoạn 2 nhưng phải được ghi nhận là rủi ro mở.

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

**Luồng đăng ký phía người dùng (đã chốt, xem §8.2):** người dùng CHỈ nói chuyện với node thực thi; node và parent chain tự xử lý toàn bộ phần còn lại.

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

## 8. Phụ lục (giai đoạn 2): tài khoản chạy tạm khi mất kết nối Parent Chain

**Vấn đề:** gate §2 làm onboarding người dùng mới phụ thuộc parent. Khi cụm thực thi mất kết nối parent, người dùng chưa đăng ký không gửi được tx (tài khoản đã đăng ký vẫn chạy bình thường). Có muốn cho phép "tài khoản chạy tạm" không?

**Nguyên tắc bất di bất dịch:** việc "parent đang mất kết nối" là quan sát cục bộ, mỗi node thấy khác nhau ⇒ **không được dùng làm đầu vào của verdict thực thi** (sẽ fork; AGENTS.md: không timeout/không heuristic để quyết định). Trạng thái "tạm" chỉ được tạo ra bởi **sự kiện on-chain có thứ tự** và hết hạn theo **số block** của chain thực thi, không theo đồng hồ.

**Mặc định giai đoạn 1 (đã chọn): không có tài khoản tạm.** Người dùng mới chờ (pending) đến khi parent về; tài khoản đã đăng ký, tài khoản genesis và danh tính node (đã miễn gate, có số dư/nonce ngay trong chain, ví dụ self_alloc dùng trả gas cho tx hệ thống) **không bị ảnh hưởng** khi mất parent. Chỉ cần onboarding mới chấp nhận độ trễ.

**Nếu cần giai đoạn 2 — "đăng ký tạm" (provisional registration):**
1. State: thêm `uint64 ProvisionalUntilBlock = 11` vào `AccountState` (0 = không có). Gate cho phép nếu `ParentRegistered || ProvisionalUntilBlock > số block đang xử lý` (số block của block trước, hàm thuần).
2. Cấp: system tx `ProvisionalRegister{User, UserSig, ClusterKey}` do **danh tính node** ký (cụm đồng ý). `UserSig` là chữ ký ECDSA của người dùng trên đúng thông điệp `REGISTER_ACCOUNT_V1:` gắn với khoá cụm này (cùng thông điệp `RegisterAccount` trên parent dùng); handler tự kiểm bằng ecrecover (xác định), đặt `ProvisionalUntilBlock = block hiện tại + W` (W cấu hình theo block), idempotent. Giới hạn số lần cấp mỗi block/epoch (bounded) để không thể bị làm ngập.
3. Chữ ký người dùng nằm **trên chain** ⇒ khi parent về, bất kỳ node nào cũng lấy lại được để gửi `SendRegisterAccount` lên parent (không phải lưu cục bộ, sống qua restart).
4. Xác nhận: sự kiện đăng ký từ parent (§2.3) đặt `ParentRegistered=true` và xoá trạng thái tạm. Hết hạn mà chưa xác nhận ⇒ mất quyền gửi (vẫn nhận được tiền); cấp lại cần sự kiện mới.
5. Đối soát: nếu parent từ chối vì địa chỉ đã đăng ký ở cụm khác (`ErrAccountAlreadyRegistered`), worker đề xuất `ProvisionalRevoke(User)` ⇒ `ProvisionalUntilBlock=0`. Số dư còn lại bị kẹt ở cụm này (không gửi được): phải ghi rõ cho người dùng.
6. Hạn chế tạm: tối thiểu cấm tx xuyên chain/`PARENT_CHAIN_GATEWAY` cho tài khoản chỉ ở trạng thái tạm (cần parent để bảo chứng), và giới hạn giá trị mỗi tx; hạn mức tích luỹ cần bộ đếm trong state (giai đoạn 2b, phức tạp hơn, chỉ làm nếu cần).

**Đánh đổi phải nói rõ với người dùng:** trong cửa sổ tạm, đảm bảo "một địa chỉ một cụm" **không còn đầy đủ** — người dùng có thể ký `REGISTER_ACCOUNT` cho hai cụm và có tài khoản tạm ở cả hai (parent chỉ phân xử sau khi về). Đây đúng là khoảng hở replay xuyên cụm quay lại trong thời gian ngắn. Cách giảm: chain ID duy nhất theo cụm (gắn với khoá BLS, xem `secp256k1_proto_tcp_architecture_design.md` §10) giữ replay bị chặn kể cả trong cửa sổ tạm; W nhỏ; giới hạn giá trị; cấm xuyên chain. Nếu không chấp nhận khoảng hở này thì giữ giai đoạn 1 (chờ).

**Test bổ sung cho giai đoạn 2 (ngoài §4):** hết hạn theo block (biên N-1/N/N+1, giống nhau trên mọi replica); cấp trùng idempotent; sai `UserSig` / chữ ký cho cụm khác bị từ chối đúng lỗi; revoke sau khi parent từ chối; tài khoản tạm không gửi được tx xuyên chain; ngừng parent thật rồi dựng lại ⇒ tài khoản tạm được xác nhận không cần can thiệp; **mô phỏng cùng một người dùng được cấp tạm ở hai cụm cùng chain ID ⇒ chứng minh khoảng hở và chứng minh chain ID khác nhau thì đóng được**; kiểm tra đột biến (bỏ kiểm `UserSig`, bỏ hết hạn, bỏ cấm xuyên chain).

### 8.1 Quyết định của người dùng (2026-10-05, bản cập nhật): đăng ký tạm ở hai cụm — parent chọn bên thắng, bên thua bị KHOÁ HẲN

**Luật trọng tài:** Parent Chain là trọng tài. Cụm nào đăng ký **được parent ghi nhận trước** (`RegisterAccount` thành công; bên đến sau nhận `ErrAccountAlreadyRegistered`) thì tài khoản ở cụm đó thành tài khoản thật. Tài khoản ở cụm còn lại bị **khoá hoàn toàn**.

**Phạm vi:** tính năng rút tiền khỏi tài khoản bị khoá **chưa làm** (bản trước của kế hoạch có "rút sạch", đã bỏ theo yêu cầu). Giai đoạn này, tài khoản bị khoá không gửi được bất kỳ tx nào.

**Máy trạng thái tài khoản (trên mỗi cụm):** `NONE → PROVISIONAL → CONFIRMED` hoặc `PROVISIONAL/NONE → LOCKED`. Gợi ý biểu diễn: enum `RegistrationState` + `HomeClusterKey` trong `AccountState` (hợp nhất `ParentRegistered`/`ProvisionalUntilBlock`).

| Sự kiện on-chain (do danh tính node đề xuất, kèm bằng chứng parent — xem P0) | Chuyển trạng thái |
| :--- | :--- |
| `ProvisionalRegister` (chữ ký user + cụm đồng ý) | `NONE → PROVISIONAL` (hết hạn theo số block) |
| `ParentConfirmed(user, thisCluster)` (parent ghi nhận cụm NÀY trước) | `PROVISIONAL → CONFIRMED` |
| `ParentLocked(user, winnerClusterKey)` (parent ghi nhận cụm KHÁC trước) | `PROVISIONAL/NONE → LOCKED` (lưu `HomeClusterKey`) |
| hết hạn mà parent chưa trả lời | `PROVISIONAL → NONE` (chỉ thôi quyền gửi; vẫn nhận tiền) |

**Quy tắc gate cho LOCKED (hàm thuần, ở admission và bộ lọc thực thi):** **từ chối mọi tx có người gửi là tài khoản LOCKED**, với mã lỗi riêng (`AccountLocked`). Không có ngoại lệ (kể cả chuyển tiền, gọi hợp đồng, gateway). Tài khoản vẫn **nhận** tiền được (không chặn người nhận, xem §2.1). `LOCKED` là trạng thái cuối: không tự thoát, không hết hạn.

**Hệ quả phải ghi rõ cho người dùng và vận hành:**
- Số dư đang có ở cụm bên thua **bị kẹt** cho tới khi có tính năng rút (sẽ thiết kế sau: cần quy tắc rút sạch / chuyển về cụm thắng do chủ tài khoản chọn đích, không tự động đẩy tiền sang cụm thắng vì cụm thắng có thể là cụm độc hại mà người dùng bị lừa ký).
- Tiền nhận thêm sau khi khoá cũng bị kẹt. Nên cảnh báo trên giao diện/RPC: trạng thái tài khoản (`LOCKED`) phải đọc được qua RPC để ví/dapp không gửi tiền vào địa chỉ đã khoá ở cụm đó.
- Vì chưa có đường thoát, việc cấp đăng ký tạm (giai đoạn 2) nên giới hạn giá trị tài khoản tạm và thời hạn ngắn để giảm số tiền có thể bị kẹt.

**Tính xác định / không fork:** `ParentConfirmed/ParentLocked` là system event có thứ tự mang bằng chứng parent; verdict gate là hàm thuần của `AccountState` trước block. Không dùng quan sát mất kết nối, không timeout. Hai cụm không cần liên lạc: mỗi cụm chỉ cần phán quyết của parent.

**Test bắt buộc thêm (ngoài §4):** (L1) cùng một người dùng đăng ký tạm ở cụm A và B, A đăng ký lên parent trước ⇒ A: `CONFIRMED`, B: `LOCKED`, kết quả đúng bất kể thứ tự xử lý ở hai cụm; (L2) ở B mọi tx của người dùng đều bị từ chối **đúng mã `AccountLocked`** (chuyển toàn bộ, chuyển một phần, gọi hợp đồng, gateway, tx `0xFF` và tx ETH), có ca đối chứng cùng tx được chấp nhận ở A; (L3) tiền nhận thêm sau khi khoá vẫn vào được và vẫn bị kẹt (số dư tăng, gửi vẫn bị từ chối); (L4) giả mạo `ParentLocked`/`ParentConfirmed` từ tài khoản thường bị từ chối (P0) và từ node không có bằng chứng bị từ chối (khoá bừa người khác = tấn công từ chối dịch vụ nên phải kiểm kỹ); (L5) hết hạn theo block đúng biên N-1/N/N+1 trên mọi replica; (L6) trạng thái `LOCKED` không bao giờ quay lại `CONFIRMED`/`PROVISIONAL` bằng sự kiện nào (kể cả `ProvisionalRegister` mới); (L7) RPC trả đúng trạng thái; (L8) đột biến: bỏ kiểm tra `AccountLocked`, bỏ kiểm tra bằng chứng, đảo thứ tự thắng/thua, cho phép thoát `LOCKED` ⇒ test tương ứng phải FAIL.

### 8.2 Luồng đăng ký tự động — người dùng chỉ gửi yêu cầu tới node thực thi (quyết định của người dùng, 2026-10-05)

**Nguyên tắc:** người dùng không bao giờ gọi parent chain. Người dùng gửi yêu cầu đăng ký cho **node thực thi**; việc chuyển từ tạm sang thật (hoặc bị khoá) do node thực thi và parent chain tự động xử lý. Người dùng chỉ cần hỏi trạng thái.

**Điểm vào (khuyến nghị: RPC của node thực thi):** `mtn_registerAccount({address, userSig})`.
- `userSig` = chữ ký ECDSA của người dùng trên đúng thông điệp parent yêu cầu (`ComputeRegisterAccountMessage(userAddress, floatIdentityKey)`, domain tag `REGISTER_ACCOUNT_V1:`, gắn khoá cụm này). Client lấy `floatIdentityKey` từ node (`mtn_getClusterIdentity`, thêm mới) để ký đúng thông điệp.
- Vì sao RPC thay vì tx: người dùng chưa đăng ký không qua được gate nên không gửi tx thường được; RPC không cần tạo ngoại lệ gate cho người gửi chưa đăng ký. Bản ghi bền vẫn nằm on-chain (system event bên dưới).
- Node kiểm ngay: chữ ký hợp lệ (ecrecover = `address`), địa chỉ chưa ở trạng thái `CONFIRMED`/`LOCKED`, chính sách onboarding của cụm cho phép (`registration_policy` trong cấu hình: mở có giới hạn tốc độ / danh sách cho phép), rate-limit theo IP và theo địa chỉ, hàng đợi **có giới hạn**; vượt ⇒ từ chối rõ ràng (không treo).
- Trả về ngay `{status: "PENDING"}`; không chờ parent.

**Trạng thái hiển thị cho người dùng:** `mtn_getRegistrationStatus(address)` ⇒ `NONE | PENDING (đã nhận, chờ cấp tạm) | PROVISIONAL (dùng được, hết hạn ở block N) | CONFIRMED | LOCKED (kèm HomeClusterKey) | EXPIRED`. Đọc từ `AccountState` + hàng đợi cục bộ. Ví/dapp dựa vào đây (đặc biệt `LOCKED`).

**Chuỗi tự động (không cần người dùng làm gì thêm):**
1. Node nhận `mtn_registerAccount` ⇒ đưa vào hàng đợi cục bộ (bền, giới hạn).
2. `RegistrationRelayWorker` (một goroutine, theo khuôn `ReceiveWorker`) lấy yêu cầu:
   - phase 1 (không có tạm): ký `clusterSig` bằng khoá BLS của cụm rồi gọi parent `SendRegisterAccount(user, floatIdentityKey, userSig, clusterSig)`;
   - phase 2 (có tạm): trước đó đề xuất system event `ProvisionalRegister` (kèm `userSig` ⇒ nằm on-chain, mọi node lấy lại được) rồi mới gọi parent.
3. Parent xử lý `RegisterAccount` theo luật "ai đến trước": thành công ⇒ sinh `AccountRegisteredEvent` theo cụm; bị trùng ⇒ `ErrAccountAlreadyRegistered`.
4. `RegistrationSyncWorker` (poll `GetInboundAccountRegistrations`, qua `QuorumClient` f+1) và đối soát `GetAccountRegistry(user)` cho các tài khoản đang `PENDING/PROVISIONAL` (theo lô, giới hạn):
   - parent ghi cụm NÀY ⇒ system event `ParentConfirmed` ⇒ `CONFIRMED`;
   - parent ghi cụm KHÁC ⇒ system event `ParentLocked(user, winnerClusterKey)` ⇒ `LOCKED`;
   - chưa có kết quả ⇒ giữ nguyên, thử lại; phase 2: hết hạn theo block ⇒ `EXPIRED`.
5. Mọi chuyển trạng thái là **system event có thứ tự, mang bằng chứng parent, do danh tính node đề xuất** (xem P0). Các thử lại/ poll chỉ là hành vi cục bộ của worker, không là đầu vào của verdict.

**Tự động và an toàn thử lại:** mọi bước idempotent (cùng yêu cầu gửi lại không tạo trạng thái mới; parent từ chối trùng không làm hỏng); worker khởi động lại tiếp tục từ hàng đợi bền; backoff có trần nhưng KHÔNG dùng timeout để quyết định chuyển trạng thái. Parent không truy cập được ⇒ yêu cầu nằm trong hàng đợi (phase 1: người dùng chờ; phase 2: đang `PROVISIONAL` thì vẫn dùng được tới khi hết hạn).

**Khoá cụm ký `clusterSig`:** cần khoá BLS của cụm trong node (câu hỏi V2 còn mở: khoá node `app.keyPair` có chính là `floatIdentityKey` không). Nếu mỗi node có khoá riêng thì chỉ node nắm khoá cụm (hoặc cơ chế ký ngưỡng) mới relay được — ghi quyết định vào báo cáo bước 0.

**Chống lạm dụng onboarding:** rate-limit, hàng đợi giới hạn, `registration_policy`; mỗi yêu cầu tốn một giao dịch trên parent nên cụm có thể gom lô (tối đa N/lần) để không làm nghẽn parent.

**Việc cần thêm vào bảng bước (§3):** (a) RPC `mtn_registerAccount`, `mtn_getRegistrationStatus`, `mtn_getClusterIdentity` + giới hạn tốc độ; (b) hàng đợi bền cho yêu cầu đăng ký; (c) `RegistrationRelayWorker` và `RegistrationSyncWorker`; (d) cấu hình `registration_policy`; (e) tài liệu hướng dẫn client cách ký thông điệp đăng ký.

**Test thêm:** (R1) RPC từ chối chữ ký sai/địa chỉ không khớp/đã `CONFIRMED|LOCKED`/vượt giới hạn tốc độ, đúng mã lỗi; (R2) yêu cầu hợp lệ ⇒ chuỗi tự động đến `CONFIRMED` mà người dùng không gọi thêm gì; (R3) restart node giữa chừng ⇒ không mất yêu cầu, không gửi parent hai lần gây lỗi; (R4) parent tạm dừng rồi chạy lại ⇒ tự tiếp tục; (R5) hai cụm cùng nhận yêu cầu của một địa chỉ ⇒ cụm đến parent trước `CONFIRMED`, cụm kia `LOCKED`, mọi thứ tự xử lý cho cùng kết quả; (R6) hàng đợi đầy ⇒ từ chối rõ ràng, không rò rỉ bộ nhớ; (R7) `mtn_getRegistrationStatus` đúng ở mọi trạng thái; (R8) đột biến: bỏ kiểm `userSig`, bỏ rate-limit, bỏ idempotent.

**E2E thêm (cụm cô lập):** người dùng mới gọi `mtn_registerAccount` (chỉ node thực thi) ⇒ theo dõi `PENDING → (PROVISIONAL) → CONFIRMED` ⇒ gửi tx thành công; kịch bản đăng ký song song ở hai cụm ⇒ một `CONFIRMED`, một `LOCKED`; tắt parent giữa chừng ⇒ tự hồi phục khi parent chạy lại.

## 9. Trạng thái triển khai (2026-10-05) — giai đoạn 1 (không có tài khoản tạm)

**Đã làm và đã kiểm thử (đột biến + `-race` + `build_check.sh`):**
- Parent chain: chỉ mục sự kiện đăng ký theo cụm (`MemoryStore`, `TreeStore`/`DBStore`), HTTP `/inbound_registrations`, `QuorumClient.GetInboundAccountRegistrations` (f+1).
- `AccountState.ParentRegistered` (proto, `Copy`, JSON, mutation, `cmd/rpc` bản sao); root tài khoản cũ không đổi khi trường = false.
- Handler `account_registered` (idempotent, khoá cụm đúng 48 byte) + `RegistrationWorker` (poll, đề xuất system tx, dedupe theo hash payload).
- **P0:** chỉ danh tính node BLS-native được gửi tx tới `RollupSystemAddress` (thực thi + admission, mã 70), áp dụng trước cả nhánh đăng ký.
- Gate người gửi (`sigPolicy.senderRegisteredError`) ở admission và bộ lọc thực thi, ngoài cache chữ ký; config `account_gate` (chỉ với `secp`).
- Genesis: cờ chỉ đặt khi gate bật (chain không dùng gate giữ nguyên root genesis); `registered_accounts` tuỳ chọn.
- **Phía node (mục 8.2, giai đoạn 1):** `rollup.RegistrationRelay` (hàng đợi có giới hạn, mỗi tick một lần thử, kiểm registry trước khi gửi, hỏi lại registry sau khi gửi vì parent chỉ báo trùng qua registry) + RPC `mtn_getClusterIdentity`, `mtn_getRegistrationMessage`, `mtn_registerAccount`, `mtn_getRegistrationStatus`.
- Test tích hợp trong tiến trình nối toàn bộ thành phần thật (parent `RegisterAccount` thật kiểm chữ ký BLS cụm + ECDSA người dùng): người dùng chỉ gọi node ⇒ `CONFIRMED` tự động; cùng địa chỉ ở hai cụm ⇒ parent chọn cụm đến trước, cụm kia `REJECTED`, tx replay bị từ chối ở cụm không phải nhà; sự kiện giả từ tài khoản thường không mở được gate.

**Chưa làm / rủi ro mở (đọc trước khi bật gate trên chain thật):**
1. **E2E trên cụm thật chưa chạy** (chỉ test tích hợp trong tiến trình). Cần cụm cô lập có `tx_signature_mode="secp"` + `account_gate="parent_registered"` + parent chain, chạy kịch bản §5.
2. **Hàng đợi yêu cầu đăng ký bền:** ĐÃ HOÀN THÀNH (lưu bền qua `KVStore` / `storage.Storage`, tự khôi phục và replay PENDING -> CONFIRMED sau khi khởi động lại).
3. **Bằng chứng parent cho system event (P0 còn hở):** mọi danh tính node của cụm đều gửi được event; một validator Byzantine vẫn giả mạo được (mint/đăng ký/khoá bừa). Cần event mang chứng chỉ quorum/proof parent do handler xác minh (mục 10).
4. **Giai đoạn 2 chưa làm:** đăng ký tạm, `ParentLocked`/`LOCKED` (§8). Hiện bên thua chỉ nhận trạng thái `REJECTED` ở relay; tài khoản đơn giản không được đăng ký ở cụm đó (không bị khoá).
5. **Khoá node = khoá cụm** (V2) mới được xác nhận ở mức "code hiện tại giả định `app.keyPair` là danh tính cụm" (`app.go` đã tự đăng ký như vậy); chưa kiểm cụm nhiều validator với khoá khác nhau.
6. Mã HTTP `/inbound_registrations` nuốt lỗi store giống `/inbound` hiện có (trả danh sách rỗng); nên trả 5xx để quorum không coi node lỗi là "đồng ý rỗng".
7. **Legacy chain chưa có chain-binding ở exec-filter cho giao dịch không phải 0xFF:** tx legacy BLS không có chain ID gắn trong envelope chữ ký (chỉ có secp EIP-155 và tx 0xFF); giữ nguyên không sửa ngoài phạm vi.

## 10. Item 2 — parent-verifiable authentication of system events (DESIGN, not implemented)

Residual hole (still open, shown by E8 only for NON-node identities): the rollup system handler accepts events from the node BLS identity of ANY validator, so one Byzantine validator can forge `account_registered` / credit / lock events.

Findings that constrain the design (checked 2026-10-05):
- Parent block headers (`parentchain.Header`) carry NO validator signatures/certificate. A NOMT proof (`nomt_ffi.VerifyProof`) only proves "key is under root R"; nothing deterministic proves R is the parent's real root. So "attach a parent proof to the system tx" is NOT sufficient by itself, and checking R against the parent at execution time would be time-dependent (non-deterministic => fork risk, forbidden by Part 2.5).
- `QuorumClient` (f+1 parent RPC nodes) is a trust decision made per-validator at observation time, fine for deciding to *vote*, not for deterministic execution.

Recommended design (no parent change, deterministic): **intra-cluster f+1 co-attestation.**
1. Each exec validator runs its own RegistrationWorker, observes the event through `QuorumClient` + registry proof, and BLS-signs digest = keccak("ACCT_REG_ATTEST_V1" || chainID || user || clusterKey || parentSeq).
2. The system tx carries the payload plus >= f+1 distinct committee-member signatures over that digest. Handler verifies them deterministically against the committee set in state (same set used by `CommitteeAttestationWorker`), then applies. Single node => f+1 = 1 (current behaviour).
3. The sender check stays (node identity) but is no longer sufficient alone. Same envelope for credit/lock events.
Cost/risk: needs signature exchange between validators (bounded queue; no timeouts for dispatch decisions — events just stay PENDING until f+1 sigs exist) and a committee-key lookup inside tx_processor. Not started: needs the user's go-ahead on (a) accepting f+1 intra-cluster trust vs (b) adding parent-side block certificates (bigger, touches parent consensus).
