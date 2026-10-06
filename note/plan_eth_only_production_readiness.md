# Kế hoạch: đưa node thực thi Ethereum-only (TCP + RPC) tới mức sẵn sàng production

Trạng thái: **SẴN SÀNG GIAO AGENT** (2026-10-06, sau các commit `7529c958`, `ddf50726`, `f8ad85ea` trên `dev`). Mỗi vấn đề dưới đây có **bằng chứng** (file/dòng hoặc lệnh đã chạy); phần chưa kiểm chứng ghi rõ "CHƯA KIỂM".
Tiếp nối: `note/plan_eth_only_node_completion.md` (kế hoạch gốc), `note/eth_only_inventory.md` (kiểm kê W1.0), `note/plan_eth_native_eip2718.md` (kế hoạch tổng, G1 = đổi hash).
**Quyết định đã chốt: `note/adr_eth_only_production_decisions.md` (D1 vận hành validator, D2 phí, D3 đổi hash, D4 tag legacy) — các mục dưới đây đã được cập nhật theo ADR; nếu mâu thuẫn, ADR thắng.**
Đọc trước: `AGENTS.md` (Zero-Fork, KISS, `build_check.sh`, tóm tắt tiếng Việt cuối response). Chính sách đã chốt: **bỏ hẳn tương thích simple-chain cũ** (bản cuối: tag `legacy-simple-chain-bls-v1` = `a25cb135`); làm trên `dev`, `git add` theo tên file (working tree dùng chung), không push nếu user chưa yêu cầu, không đụng cụm 231/230.

## 0. Hiện trạng đã đạt (đã kiểm)
- `build_check.sh` 4/4 sạch; `go vet` + `go test -race` xanh cho `cmd/simple_chain/...`, `pkg/transaction`, `pkg/network`.
- Live trên cụm local (một endpoint): gửi qua TCP và RPC, deploy contract, event + bloom, pre-EIP-155 / sai chainId / `s` cao / quá cỡ / batch >1000 bị từ chối đúng mã, lệnh proto cũ bị từ chối, `nonce too low` đúng chuẩn geth (`cmd/tool/test_raw_eth_tcp_live`).
- **Chưa** được kiểm: nhiều validator đồng thuận state root dưới tải, `ci.sh run-now`, ethers/viem/web3j/foundry, tx hệ thống sau thay đổi, hiệu năng.

## 1. Cách đọc kế hoạch
Mức: **P0** = chặn production (phải xong), **P1** = cần xong trước khi mở cho dapp ngoài, **P2** = cổng duyệt/tối ưu. Mỗi mục có *Bằng chứng → Việc cần làm → Chấp nhận*. **CẬP NHẬT 2026-10-06 (review độc lập sau 14 commit): P0-9 phải sửa NGAY, trước mọi việc khác; chưa được cutover/ra mắt khi chưa xong P0-9.** Thứ tự đề xuất: P0-9 → P0-3 → P0-4 → P0-2 → P0-1/1b → P0-5 → P1 nền tảng (P1-1…P1-4, P1-6) → P0-8 + P0-7 (cùng cutover) → P0-6 (cổng cuối, chạy trên bản đã gộp) → P1-5/7/8 → P2.

## 2. P0 — chặn production

### P0-1. (đã hạ cấp theo ADR D1) Dọn công cụ vận hành chết + kiểm quyền `registerValidator`
- **Kết luận phân tích:** contract validator là handler Go native phân quyền bằng `tx.FromAddress()`, nên tx vận hành bằng **envelope secp** chạy được. `tool_register.go` và `cmd/tool/fast_setup` **vốn đã hỏng từ trước** (ABI/số tham số lệch handler: 8/9 so với 13) và không nằm trong deploy; production đưa validator vào bằng genesis (`deploy/systemd/gen_validator_entry.py`).
- **Việc cần làm:** (1) xóa `cmd/simple_chain/tool_register.go` + cờ `-tool-register-validator` (`main.go:36,75`) + `cmd/tool/fast_setup`; (2) nếu cần công cụ thay thế: viết mới ký bằng go-ethereum, 13 tham số đúng ABI (`abi_contract/validationAbi.go`); (3) quét lại các tool vận hành khác còn dùng lệnh legacy (`grep SendTransactionWithDeviceKey`).
- **P0-1b (MỚI, bảo mật, CHƯA KIỂM):** `handleRegisterValidator` (`validation_transaction.go:129-216`) không kiểm sở hữu `authorityKey`, không có ngưỡng stake (người gọi tự đặt `minSelfDelegation`). Xác minh: committee mỗi epoch được dựng từ `GetAllValidators()` có lọc theo stake/trạng thái không? Nếu không ⇒ bất kỳ tài khoản nào đăng ký vào được tập validator ⇒ **đóng** trước production (chỉ governance/genesis hoặc ngưỡng stake tối thiểu) kèm test (tài khoản thường gọi `registerValidator` không vào được committee).
- **Chấp nhận:** không còn tool gọi lệnh legacy; test P0-1b xanh hoặc bằng chứng committee đã lọc.

### P0-2. Không được phép bỏ qua xác thực chữ ký ở production
- **Bằng chứng:** `pkg/blockchain/tx_processor/validation.go:155-161` và `signature_enforcement.go:389-391` vẫn đọc `SKIP_MEMPOOL_SIG_VERIFY`, chỉ chặn khi thiếu `METANODE_DEVNET=true` hoặc môi trường production (dựa vào biến môi trường); `deploy/cluster/local_devnet/run_load_test.sh:125` bật cứng; tài liệu `pkg/rollup/SEQUENCER_ERC20_STANDARD_TX_PROOF.md` (F0-5) ghi các template ansible chỉ bật khi biến `skip_mempool_sig_verify=true`. Trên chain Ethereum-only, đây là lớp bảo vệ duy nhất cho "ai gửi tx này".
- **Việc cần làm:** (1) Khởi động **fail-fast** (từ chối chạy) nếu cờ bật mà `METANODE_DEVNET` ≠ `true` hoặc chain ID là chain production (991) — không dựa vào chuỗi "production" trong biến môi trường. (2) Xóa cờ khỏi mọi template deploy production; chỉ còn ở script devnet/đo tải, ghi rõ trong tên. (3) Test: cờ bật + production ⇒ node không khởi động; cờ bật + devnet ⇒ cảnh báo to rõ. (4) Kiểm `ci.sh`/`scripts` không dùng cờ cho kiểm thử "xanh" (memory ghi từng có fake-green).
- **Chấp nhận:** test + một lần chạy thật cho cả hai nhánh; grep chứng minh không còn cờ trong template production.

### P0-3. Ghi ánh xạ hash/cache TRƯỚC khi tx được nhận vào pool (hồi quy)
- **Bằng chứng:** `cmd/simple_chain/rpc_transaction.go` (`SendRawEthTransaction`): `bc.AddTxToCache(...)` và `bc.SetEthHashMapblsHash(...)` chạy **trước** `AddTransactionToPool`; `SetEthHashMapblsHash` ghi bền (`storeToDirty`, `pkg/blockchain/blockchain.go:693-697`). Đường TCP (`executeAndAddTx`, và nhánh batch) cũng ánh xạ trước khi nhận. Bản cũ chỉ ánh xạ **sau** khi nhận thành công. ⇒ mỗi tx hợp lệ về chữ ký nhưng bị pool từ chối (thiếu tiền, nonce sai…) vẫn để lại bản ghi bền: vector spam ghi đĩa.
- **Việc cần làm:** chỉ ghi ánh xạ/cache **sau khi** pool nhận (hoặc xóa khi bị từ chối); giới hạn kích thước/TTL của `txsCache` và `ethHashMapBlsHash`; đảm bảo receipt tra theo ethHash vẫn hoạt động (receipt chỉ có sau khi tx vào block nên ánh xạ-sau là đủ — kiểm bằng test đua: nhận tx → tra receipt ngay).
- **Chấp nhận:** test: tx bị từ chối **không** để lại khóa `ethHashMapBlsHash`; test đua; đo số khóa ghi khi bắn N tx bị từ chối = 0.

### P0-4. DoS / khuếch đại ở đường TCP raw
- **Bằng chứng:** `ProcessRawTransactionsFromClient` (`processor/transaction_processor.go`) giải mã + `ecrecover` **trong handler** cho tới 1000 envelope/batch (không qua `injectionQueue`); mỗi tx lỗi gửi một `TransactionError` riêng (tối đa 1000 thông điệp/batch); handler đăng ký không qua `withRateLimit` (`routes/routes.go`); log `logger.Error("❌ [TX REJECTED] ...")` cho mỗi lỗi người dùng (làm ngập log). Chi phí `ecrecover` còn lặp ở converter, kiểm tra admission và thực thi khối (có `sigCache` — mức tái dùng **CHƯA KIỂM**).
- **Việc cần làm:** (1) Đưa việc giải mã/`ecrecover` của batch qua worker pool có giới hạn sẵn có (không tạo hàng đợi mới), hoặc giới hạn tổng chi phí/connection; (2) gộp lỗi batch thành **một** phản hồi có danh sách (chỉ số, mã, hash) thay vì N thông điệp; (3) rate limit theo kết nối/IP cho `SendRawTransaction(s)` (dùng `withRateLimit` hoặc cơ chế sẵn có); (4) hạ log lỗi người dùng xuống `Warn`/có giới hạn tần suất; (5) tái dùng kết quả khôi phục sender (cache theo hash envelope) giữa converter và `ValidEthSign`; (6) fuzz test (Go native fuzz) cho `UnmarshalBinary` + `ValidateEthTxEnvelope` + RLP batch.
- **Chấp nhận:** benchmark chống DoS: 100 kết nối × batch 1000 envelope rác không làm tăng độ trễ block / không cạn worker; fuzz chạy ≥ 10 phút không panic; số `ecrecover` trên mỗi tx hợp lệ được đo và ghi lại.

### P0-5. Giao dịch hệ thống của node phải còn chạy (chưa có test sau khi xóa)
- **Bằng chứng:** bảng kiểm kê nói giữ nguyên (rollup system events qua `AddTransactionToPool`, root-anchor qua envelope), nhưng các commit mới **không thêm test** chứng minh; xóa lệnh legacy + sửa converter có thể làm hỏng.
- **Việc cần làm:** test tích hợp: rollup system tx (BLS node identity → `RollupSystemAddress`) vẫn được nhận/thực thi; tx từ người dùng gửi tới `RollupSystemAddress` bị từ chối (`UnauthorizedSystemSender`); root-anchor submitter gửi envelope thành công; chạy `ci.sh run-now` phần cross-chain/rollup.
- **Chấp nhận:** test xanh + `ci.sh` phần liên quan PASS (chạy thật, lưu output).

### P0-6. Kiểm chứng đa validator dưới tải (cổng cuối của P0)
- **Bằng chứng:** test live hiện chỉ gọi MỘT endpoint; `PROJECT_STRUCTURE.md` đã bỏ câu "zero fork" vì chưa kiểm.
- **Việc cần làm:** trên cụm cô lập 4 validator (cổng 31xxx): (1) tải hỗn hợp (chuyển, deploy, gọi, tx lỗi) qua cả TCP và RPC, nhiều nguồn; (2) sau tải so sánh block hash + state root + receipt root giữa **tất cả** node; (3) chạy `ci.sh run-now` đầy đủ (gate 14, co-attestation 4 validator, cross-chain 26) và 1–2 chu kỳ chaos (kill -9 một node) theo `fork_guard`; (4) `go test -race` đầy đủ các package đã sửa + `pkg/blockchain/tx_processor`.
- **Chấp nhận:** mọi node cùng block hash/state root; không có fork; lưu lệnh + output thật. Mục chưa chạy ghi "chưa chạy", không báo PASS suy đoán.

### P0-7. Mô hình phí (đã chốt ở ADR D2): giá hiệu dụng chuẩn Ethereum trên nền phí phẳng
- **Bằng chứng:** với type 2/3/4 người dùng bị trừ `gas × maxFeePerGas` (tip bị bỏ, không hoàn chênh): `pkg/transaction/transaction.go:1063-1069` (`EffectiveGasPrice` trả `GasFeeCap`), `native_fast_path.go:~201`, `vm_processor_state.go:554`; `eth_maxPriorityFeePerGas` = 1e8 vs `gasPrice`/`baseFee` = 1e5 (`backend.go:118-129`) ⇒ ví mặc định trả gấp ~1000 lần phí phẳng.
- **Việc cần làm (theo ADR D2):** tách `GasPriceCap()` (admission `≥ F`, kiểm đủ tiền `gasLimit × cap + value`) và `EffectiveGasPrice() = min(maxFee, F + tip)` (trừ phí, receipt, `eth_getTransactionByHash.gasPrice`); `F = MINIMUM_BASE_FEE` là nguồn duy nhất; RPC: `eth_gasPrice = F`, `baseFeePerGas = F`, `eth_feeHistory` khớp, `eth_maxPriorityFeePerGas = 0` (fallback 1 wei nếu thư viện từ chối 0). Đổi kết quả chuyển trạng thái ⇒ **phát hành cùng cutover** (mục P0-8), mọi validator đồng thời.
- **Chấp nhận:** test số dư cho type 0/1/2/3/4 với `maxFee` > F: trừ đúng `gas × min(maxFee, F+tip)`; ethers/viem/web3j/foundry mặc định (không đặt phí tay) trả ≈ F; so state root giữa các validator sau đổi.

### P0-9. (CRITICAL — phát hiện khi review 2026-10-06, lỗi do P0-8 `61f32c7b`) Chữ ký không ràng buộc các trường tx với envelope ⇒ có thể giả `to/amount/nonce/data` của tx đã ký
- **Bằng chứng (đã chạy, không suy luận):** probe test tạm (đã xóa) — ký tx type 2 của "nạn nhân" (`to=0x11…`, `value=1 wei`), dựng bằng `NewTransactionFromEth`, rồi đổi trong proto `ToAddress=0x…bad01`, `Amount=1000 coin` và giữ nguyên `RawEnvelope`: `ValidEthSign()=true` và `Hash()==ethHash` (hash không đổi). Nguyên nhân: `ToEthTransaction()` nay dựng từ `RawEnvelope` bỏ qua trường proto (`transaction.go:~163`), `ValidEthSign` (`:1216`) chỉ so sender khôi phục từ envelope với `FromAddress`; thực thi lại đọc `ToAddress()/Amount()/GetNonce()/CallData()` từ **trường proto**. Cache chữ ký `sigCacheKey` (`signature_enforcement.go:26`) khóa theo `hash + sign`, mà hash giờ chỉ là `keccak(envelope)` ⇒ biến thể giả dùng chung khóa cache với tx thật. Trước P0-8 hash phủ mọi trường (`TransactionHashData`) và chữ ký kiểm trên bản dựng từ trường nên không giả được.
- **Tác động:** bất kỳ envelope nào nạn nhân từng ký (trong mempool hoặc lịch sử chuỗi) dùng làm "giấy phép" cho giao dịch tùy ý từ tài khoản nạn nhân (đặt `nonce` = nonce hiện tại, `to` = kẻ tấn công, `amount` = số dư). Cần kẻ tấn công đưa được proto tx vào đường đồng thuận mà không qua converter cục bộ: validator Byzantine làm proposer (đúng mô hình đe dọa mà "sig-enforcement at block execution" từng đóng), hoặc đường nhận tx từ peer (`consensus/metanode/src/network/peer_rpc/server.rs`). Đường TCP/RPC công khai an toàn vì converter dựng cả hai từ cùng `ethTx`. Ngoài ra, biến thể giả cùng hash có thể làm khử trùng theo hash (Rust `seen`, `GLOBAL_TX_CACHE`) loại tx thật (censorship).
- **Việc cần làm:** (1) Khi `RawEnvelope` không rỗng, **mọi trường thực thi phải bằng đúng kết quả dựng lại từ envelope**: decode `RawEnvelope` → dựng proto chuẩn bằng cùng hàm `NewTransactionFromEth` → so từng trường (From, To, Amount, Nonce, MaxGas, MaxGasPrice, GasFeeCap, GasTipCap, Data/CallData/DeployData, Type, ChainID, R/S/V, AccessList, BlobVersionedHashes, MaxFeePerBlobGas, AuthorizationList) hoặc tốt hơn **ghi đè** trường bằng giá trị chuẩn rồi từ chối nếu `From` lệch. Hàm này là hàm thuần của tx (mọi validator cho cùng kết quả, không phụ thuộc trạng thái cục bộ). (2) Đặt kiểm tra ở **mọi** điểm nhận proto không qua converter: giao khối từ Rust sang Go (`UnmarshalTransactions` → trước khi thực thi/`sig enforcement`), `peer_rpc` submit, WAL/replay, sync. (3) Sửa khóa cache chữ ký: phải phủ toàn bộ nội dung proto (hash của proto đã chuẩn hóa) chứ không chỉ `hash(envelope)`; hoặc chỉ cache **sau** khi kiểm ràng buộc trường. (4) Xem lại khử trùng theo hash ở Rust: tx chưa qua kiểm ràng buộc không được chiếm chỗ hash của tx hợp lệ. (5) Tx hệ thống (không có envelope) không đổi.
- **Chấp nhận:** test bảng: với tx thật hợp lệ, đổi **từng** trường nêu trên (một trường mỗi lần) ⇒ bị từ chối ở admission **và** ở thực thi khối (đường BlockSTM); tx thật vẫn được nhận và có `hash == go-ethereum tx.Hash()`; fuzz (Go native) mutate trường proto giữ envelope: không bao giờ được nhận; test hồi quy cache chữ ký (xác thực tx thật trước, rồi gửi biến thể giả ⇒ vẫn bị từ chối); chạy lại E2E 4 validator **kèm kịch bản validator Byzantine** gửi block chứa tx giả trường — phải bị loại ở mọi node, state root không lệch.

### P0-8. (nâng từ P2-1 theo ADR D3) Một hash = `keccak256(envelope)` — chặn ra mắt, làm một lần
Xem ADR D3 và mục P2-1 cũ (thiết kế đã chốt). Gộp vào "Cutover bundle v1" cùng chainId 991 và ngữ nghĩa phí D2. Thứ tự: các P0 khác + P1 nền tảng → P0-8 + P0-7 → kiểm chứng đa validator (P0-6) trên bản gộp → cutover. Quy ước tx hệ thống BLS (không có envelope) giữ hash proto cũ, phân nhánh theo `raw_envelope`; test chéo Go↔Rust bắt buộc.

## 3. P1 — trước khi mở cho dapp ngoài

### P1-1. Hoàn tất xóa code legacy (W1 của kế hoạch gốc, phần còn lại)
- **Bằng chứng (còn sót):** 3 route legacy vẫn đăng ký chỉ để trả lỗi (`routes/routes.go`, thông điệp còn nhắc `tx_signature_mode`); `MetaAPI.SendRawTransactionWithDeviceKey` (`rpc_transaction.go:321`) và endpoint HTTP nhị phân `sendRawTransactionBinHandler` (`backend.go:~636-670`, `rawTxBinSender`); type `0xFF` (`transaction.go`, `validation.go`, `signature_enforcement.go`, `tx_validator_pool_core.go:258`); `TxSignatureMode`/`validateTxSignatureMode` (`pkg/config/config.go:217-352`), `GatewayBLSKey`/`EnablePrivateGateway` (`config.go:246-247`), `blsKeyStore` (`app.go:95,719`; `mtn_api.go:795` trong `SendCrossChainTransfer`); `pkg/rpc_client` (`BuildTransactionWithDeviceKey*`, `eth_sendRawTransactionWithDeviceKey` ở `client.go:376`); gateway `cmd/rpc`, `cmd/rpc-client` (+ dapp web3 `register-private-key-rpc`); các tool dùng lệnh legacy: `cmd/tool/tool-test-chain/test-tcp/**`, `cmd/rpc-client/**`, `cmd/tool/fast_setup`; hằng `SendTransaction*` ở 4 file `command(s).go`; danh sách `pkg/network/handler.go`; deploy: `deploy/ansible_clusters/roles/exec_cluster/templates/exec_config.json.j2`, `deploy/systemd/gen_validator_entry.py`, `deploy/ansible/roles/local_build/tasks/main.yml`, `docs/docs/node-operators/deployment-guide.md` có tham chiếu `rpc-client`/`tx_signature_mode`/`account_gate`.
- **Việc cần làm:** xóa theo từng commit nhỏ (mỗi commit `build_check` sạch + test xanh), **sau P0-1** (nếu không sẽ xóa mất công cụ vận hành). Giữ: `isNodeBLSIdentity` + tx hệ thống BLS của node. Config cũ có `"tx_signature_mode":"secp"` vẫn load (bỏ qua trường), `"bls_legacy"` ⇒ lỗi khởi động rõ ràng. KHÔNG xóa tag proto — `reserved` chỉ ở W4/cutover (3 bản sao `.proto`). Cập nhật deploy/docs/`PROJECT_STRUCTURE.md`.
- **Chấp nhận:** grep các ký hiệu trên = 0 kết quả trong code chạy production; `go build ./...` sạch (xem P1-6).

### P1-2. Hiệu năng receipt/block gas
- **Bằng chứng:** `GetTransactionReceipt` duyệt receipt của tất cả tx đứng trước trong block để tính `cumulativeGasUsed`; `MarshalBlockToMap` duyệt toàn bộ receipt của block để tính `gasUsed` (`rpc_transaction.go`, `rpc_block.go`) — O(số tx) **mỗi lần gọi**. Thứ tự duyệt dùng `blockData.Transactions()` — **CHƯA KIỂM** có trùng thứ tự thực thi (`transactionIndex`/`groupIndex`).
- **Việc cần làm:** tính một lần mỗi block (mảng cumulative gas theo `transactionIndex`), cache theo block hash có giới hạn (LRU); xác minh thứ tự = thứ tự thực thi; test với block nhiều tx; đo độ trễ `eth_getTransactionReceipt`/`eth_getBlockByNumber` ở block 5.000 tx trước/sau.
- **Chấp nhận:** độ trễ không tăng tuyến tính theo kích thước block; `cumulativeGasUsed` của tx cuối = `gasUsed` của block.

### P1-3. Phân loại lỗi bằng kiểu, không bằng chuỗi
- **Bằng chứng:** `pkg/transaction/eth_validation.go: ClassifyEthTxError` và `rpc_transaction.go: formatGethError` so khớp chuỗi con (ví dụ `"nonce"`, `"balance"`, `"chain ID"` khớp rất rộng); đổi câu chữ lỗi ở nơi khác sẽ phân loại sai âm thầm.
- **Việc cần làm:** lỗi có kiểu (sentinel + `errors.Is/As`) từ validation/pool/thực thi → bảng ánh xạ một chỗ (mã nội bộ ↔ thông điệp/mã JSON-RPC chuẩn geth); bao phủ: `nonce too low/high`, `insufficient funds for gas * price + value`, `already known`, `replacement transaction underpriced`, `intrinsic gas too low`, `exceeds block gas limit`, `invalid sender`, `transaction type not supported`, `max initcode size exceeded`, `gas limit reached`.
- **Chấp nhận:** test bảng (mỗi lỗi admission → mã + thông điệp mong đợi); ethers/viem phân loại đúng (`NONCE_EXPIRED`, `INSUFFICIENT_FUNDS`…).

### P1-4. Nonce / pending / thay thế tx
- **Bằng chứng:** `eth_getTransactionCount` dùng trạng thái "live" cho `pending` (`rpc_state.go:289-319`); hành vi khi có tx queued/nonce gap và khi thay thế tx cùng nonce (phí cao hơn) **CHƯA KIỂM**.
- **Việc cần làm:** định nghĩa và test: 2–10 tx liên tiếp bắn nhanh bằng ethers; nonce gap; thay thế cùng nonce phí cao hơn/thấp hơn (`replacement transaction underpriced`); `eth_getTransactionByHash` cho tx pending; `eth_getTransactionCount("pending")` bao gồm mempool; mempool đầy/loại bỏ.
- **Chấp nhận:** kịch bản ethers/viem gửi liên tiếp không lỗi nonce; hành vi gap/thay thế khớp go-ethereum hoặc khác biệt được ghi nhận có chủ đích.

### P1-5. Ma trận tương thích thật (chưa chạy)
- Chạy trên cụm cô lập: **ethers v6, viem, web3j (Android/JVM), foundry (`forge create`, `cast send/call/receipt`), hardhat deploy**: chuyển, deploy, gọi, event/log, ước tính phí, lỗi nonce/funds, revert reason (`execution reverted` + data), `eth_call` state override, subscription (`newHeads`, `logs`, `newPendingTransactions`). Thêm `ethereum/tests` TransactionTests (vector decode/validity) và `ethereum/hive rpc-compat` nếu cài được; mọi fail có giải trình (khác biệt chủ đích: không MPT ⇒ `eth_getProof` khác…).
- **Chấp nhận:** bảng kết quả thật (lệnh + output) lưu `note/`; mục không chạy được ghi "chưa chạy".

### P1-6. Toàn vẹn build/test ngoài `simple_chain`
- **Bằng chứng:** `cd execution && go build ./...` lỗi: `test_revert.go:5:1: no required module provides package .../pkg/mvm/helpers` (file scratch `execution/test_revert.go`, không được git theo dõi); `go vet ./cmd/tool/... ./pkg/...` chưa chạy sạch toàn bộ; `cmd/rpc` là module riêng (`go.mod`) — xác nhận còn cần hay xóa (P1-1).
- **Việc cần làm:** xóa/di chuyển file scratch; `go build ./...` và `go vet ./...` sạch; tool/benchmark/e2e đang ký BLS/proto (`tx_sender`, `tps_benchmark_multi_node`, `e2e_account_gate`, `register_chains`, `live_asset_bridge`, `fund_tps_bench_accounts`, `pkg/txsender`) chuyển sang ký secp + gửi envelope hoặc xóa nếu chết; `ci.sh run-now` dùng đường mới.
- **Chấp nhận:** `go build ./...` + `go vet ./...` sạch; không tool nào gọi lệnh đã bị từ chối.

### P1-7. Quan sát được (observability)
- **Bằng chứng:** chưa có metric riêng cho đường raw (số nhận/từ chối theo mã, kích thước batch, độ trễ chuyển đổi/ecrecover); log lỗi người dùng ở mức ERROR.
- **Việc cần làm:** metrics Prometheus (nhận/từ chối theo mã lỗi, histogram độ trễ conversion/ecrecover, kích thước batch, độ sâu `injectionQueue`); cảnh báo (tỷ lệ từ chối tăng đột biến, queue gần đầy); log lỗi người dùng ở `Warn`/giới hạn tần suất; cập nhật dashboard/`check_metrics.sh` nếu có.
- **Chấp nhận:** metric xuất hiện khi chạy test live; ghi hướng dẫn cảnh báo trong runbook.

### P1-8. Tài liệu cho dapp và vận hành
- Cập nhật `note/tcp_eth_client_guide.md` (định dạng phản hồi batch `rlp([][]byte)`, bảng mã lỗi 71–77 + 34/18…, giới hạn: envelope 128 KB / blob 1 MB / batch 1000), hướng dẫn web3j/ethers/viem, bảng RPC được hỗ trợ/loại bỏ; cập nhật `docs/docs/node-operators/deployment-guide.md`, runbook cutover chain 991, `PROJECT_STRUCTURE.md`.
- **Chấp nhận:** dev ngoài làm theo tài liệu gửi được tx lần đầu không cần hỏi.

## 4. P2 — cổng duyệt / tối ưu

### P2-1. (đã chuyển thành P0-8, giữ lại chi tiết thiết kế) Một hash = `keccak256(envelope)` (W4, consensus-critical, cutover có wipe)
Thiết kế đã chốt ở `plan_eth_native_eip2718.md` G1 và `plan_eth_only_node_completion.md` W4: trường `raw_envelope` (tag mới) ở 3 bản `.proto`; hash tính giống hệt ở Go (`Transaction.Hash`) và Rust (`consensus/metanode/src/types/tx_hash.rs` + `queue.rs`, `block_sending.rs`, `executor.rs`, `network/rpc.rs`, `tx_socket_server.rs`, `peer_rpc/server.rs`, `tx_recovery.rs`); xác thực chữ ký trên bytes gốc; lưu `input` thô (bỏ bọc `CallData`); bỏ `SetEthHashMapblsHash`; **chốt trước quy ước hash cho tx hệ thống không có envelope**; test chéo Go↔Rust; mọi validator nâng cấp đồng thời. Đọc memory "Phuong an A" trước khi đụng `GLOBAL_TX_CACHE`/`parking_lot`. Sau W4, P0-3 và một phần P1-2/P1-4 đơn giản đi.

### P2-2. Benchmark và tối ưu
Đo tx/s trước/sau (mốc cũ 7060 → 6400 khi BLS batch verify) với envelope secp: `ecrecover` batch/cache, giảm chuyển đổi lặp; báo cáo số thật; không tối ưu sớm trước P0-4.

### P2-3. Rà soát bảo mật
Chạy `/security-review` trên diff từ `a25cb135` tới nay; fuzz (P0-4); kiểm thử "tx hợp lệ chữ ký nhưng độc hại" (initcode quá lớn, access list khổng lồ, blob sidecar sai KZG, 7702 authorization giả).

### P2-4. Phát hành và đường lùi
Runbook cutover chain 991 cập nhật (một lần wipe cho chainId + payload proto + định dạng tx); **đẩy tag/nhánh legacy lên remote khi user yêu cầu** (`legacy-simple-chain-bls-v1`, `legacy/simple-chain-bls`) — hiện chỉ ở local; ghi rõ đường lùi = checkout tag.

## 5. Cổng "sẵn sàng production" (checklist tổng)
- [ ] P0-1…P0-9 xong (P0-9 là lỗi nghiêm trọng đã xác nhận bằng probe), có bằng chứng thật (lệnh + output) trong `note/`.
- [ ] `build_check.sh` sạch, `go build ./...` + `go vet ./...` sạch, `go test -race` xanh các package đã sửa + `pkg/blockchain/tx_processor`.
- [ ] `ci.sh run-now` PASS 100% (chạy thật), 4 validator cùng block hash/state root dưới tải, ≥1 chu kỳ chaos không fork.
- [ ] Ma trận tương thích (P1-5) có kết quả thật.
- [ ] Không còn đường người dùng→BLS; tx hệ thống BLS node-identity vẫn chạy (test).
- [ ] Không có cờ bỏ qua xác thực chữ ký ở deploy production; node từ chối khởi động nếu bật sai chỗ.
- [ ] Metrics + cảnh báo + runbook cập nhật; tài liệu dapp xong.
- [ ] User duyệt lịch cutover (W4 + phí đã chốt LÀM, xem ADR D3).

## 6. Nguyên tắc cho agent thực hiện
- Zero-Fork: tx chưa verify ⇒ từ chối tại chỗ hoặc giữ PENDING; không dùng timeout/sleep để quyết định dispatch; admission/thực thi tất định (không `time.Now()`, không map iteration).
- KISS/YAGNI: dùng `injectionQueue`/worker pool có sẵn (đã có buffer giới hạn), không thêm queue/worker/interface mới ngoài cần thiết; không thêm cờ/chế độ tương thích legacy.
- Mỗi lần sửa code: `consensus/metanode/scripts/build_check.sh` sạch (không warning); báo cáo cuối có khối tóm tắt tiếng Việt (AGENTS.md Part 5); cập nhật `PROJECT_STRUCTURE.md` khi đổi cấu trúc.
- **Không tin kết quả PASS do agent khác tự báo** (memory ghi từng sai): tự chạy lại; chỉ ghi PASS khi đã chạy thật.
- Commit nhỏ trên `dev`, `git add` theo tên file; không `git add <thư mục>`; không commit binary (ví dụ `execution/scripts/test/bls_pubkey`); không push khi chưa được yêu cầu.
- Cụm local (4200/4201/8646) chạy binary build cũ: thay đổi node cần restart mới kiểm live — báo user trước khi restart; thử nghiệm lớn dùng cổng cô lập 31xxx.

## 7. Quyết định đã chốt (ADR `adr_eth_only_production_decisions.md`)
1. **D1** Tx vận hành validator = envelope secp; xóa tool chết; thêm kiểm quyền `registerValidator` (P0-1b).
2. **D2** Phí: giá hiệu dụng `min(maxFee, F + tip)` trên phí phẳng F, `maxPriorityFee = 0`; phát hành cùng cutover.
3. **D3** Đổi hash: LÀM trước ra mắt, cùng cutover; "derive key" bỏ khỏi phạm vi.
4. **D4** Đẩy tag/nhánh legacy lên remote — chờ user nói "đẩy" (thao tác công khai).
Còn mở thật sự: lịch cutover chain 991 (user), kết quả xác minh P0-1b (agent).


## 8. Review độc lập lần 2 (2026-10-06, sau commit `c127363a`) — còn thiếu gì để production
**Đã xác nhận bằng chạy thật:** `build_check.sh` 4/4; `go build ./...`, `go vet ./...` sạch; `go test -race` xanh cho `pkg/transaction`, `tx_processor`, `cmd/simple_chain/...`, `transaction_pool`, `network` (test độ trễ P1-2 đã sửa); P0-9 lõi đã đóng (đổi `to/amount/nonce/data/gas/fee/type/chainId/R/S/V/list` bị từ chối, probe của reviewer xác nhận); bằng chứng P0-6 là thật (state root cuối khớp trong log + backup của val0/val1/val3 ở `/tmp/gate_4val_p06`).
**Reviewer đã sửa:** P0-9 còn sót 4 trường proto không bị ràng buộc (`MaxTimeUse`, `LastDeviceKey`, `NewDeviceKey`, `ReadOnly`; `ReadOnly` đổi VM sang chế độ chỉ-đọc, `NewDeviceKey` ghi vào state tài khoản) — đã ràng buộc về giá trị chuẩn + test bảng (commit follow-up).
**Còn thiếu / chưa đạt (agent tiếp theo làm):**
1. **P0-6 yếu ở chỗ then chốt:** kịch bản Byzantine P4.x chỉ gọi hàm ràng buộc ở mức admission, chưa có validator Byzantine thật đưa block chứa tx giả vào đồng thuận và quan sát mọi node loại nó; ca "Forged Signature" (P2.9b) bị từ chối vì *account gate* ("account not registered") chứ không phải vì chữ ký ⇒ chưa chứng minh kiểm chữ ký; tải nhỏ (18–21 block); `ci.sh run-now` chưa chạy (đã khai báo trung thực). Việc: ca chữ ký giả trên tài khoản đã đăng ký; Byzantine proposer thật; tải lớn hơn + mixed nhiều giờ/nhiều nghìn block; chạy ci.sh trên cụm cô lập được user cho phép.
2. **Khử trùng theo hash còn ở Rust ngoài `peer_rpc`:** `node/queue.rs`, `executor_client/block_sending.rs` (tập `seen`), `commit_processor/executor.rs`, `tx_socket_server.rs`, `tx_recovery.rs` vẫn khóa theo `calculate_transaction_hash_single` (= keccak(envelope)). Biến thể giả (cùng envelope) đi vào commit trước có thể chiếm chỗ tx thật. Rà từng chỗ, khóa theo hash toàn bộ bytes hoặc xác minh ràng buộc trước khi ghi nhận; thêm test.
3. **P1-5 chưa làm:** chưa có kết quả ethers v6 / viem / web3j / foundry / hardhat, `ethereum/tests` TransactionTests, `hive rpc-compat`. Đây là điều kiện để dapp ngoài dùng.
4. **P2-2 chưa làm:** chưa benchmark tx/s sau P0-7/P0-8/P0-9 (mốc 7060 → 6400), chưa đo chi phí `ValidateProtoEnvelopeBinding` + `ecrecover` trên mỗi tx (hiện chạy ở admission, tx_processor và pre-verify).
5. **P1-1 chưa xóa hết (mới "deprecated"):** `cmd/rpc`, `cmd/rpc-client` chỉ có `DEPRECATED.md`; `SendTransactionWithDeviceKey` còn ~25 file trong `cmd/tool/tool-test-chain`, `cmd/rpc*`, `cmd/rpc-client`; `ValidSecpProtoSign`/`SignSecpProto`/`Type()==0xFF` còn ở `validation.go`, `transaction.go`, `types/transaction.go`; `tx_signature_mode` còn ở `config.go`, `signature_enforcement.go`, `tx_validator_pool_core.go`, template deploy, `gen_validator_entry.py`; `GetDeviceKey` còn ở `command.go`, `state_processor.go`, `dependencies.go`; `mining.proto` còn `SendRawTransactionWithDeviceKey`. Xóa theo ADR (giữ node-identity BLS).
6. **Cutover chưa diễn tập:** runbook (P1-8) đã viết lại nhưng chưa chạy thử trên cụm cô lập từ template ansible thật; đổi hash + phí + chainId cần một lần wipe đồng thời mọi validator.
7. **Còn mở từ đánh giá trước (`production_readiness_assessment_20261006.md`):** B1 benchmark sau thay đổi NOMT fsync, B2 chạy nhiều máy thật, B4 kiểm khóa validator ở deploy + metric, mật khẩu sudo dev lộ trong repo/lịch sử git.
**Kết luận hiện tại:** đủ điều kiện **testnet/pilot có giám sát**; **chưa** đủ cho mainnet cho tới khi xong mục 1–4 và 6 (và B1/B2).
