# Kế hoạch: đưa node thực thi Ethereum-only (TCP + RPC) tới mức sẵn sàng production

Trạng thái: **SẴN SÀNG GIAO AGENT** (2026-10-06, sau các commit `7529c958`, `ddf50726`, `f8ad85ea` trên `dev`). Mỗi vấn đề dưới đây có **bằng chứng** (file/dòng hoặc lệnh đã chạy); phần chưa kiểm chứng ghi rõ "CHƯA KIỂM".
Tiếp nối: `note/plan_eth_only_node_completion.md` (kế hoạch gốc), `note/eth_only_inventory.md` (kiểm kê W1.0), `note/plan_eth_native_eip2718.md` (kế hoạch tổng, G1 = đổi hash).
Đọc trước: `AGENTS.md` (Zero-Fork, KISS, `build_check.sh`, tóm tắt tiếng Việt cuối response). Chính sách đã chốt: **bỏ hẳn tương thích simple-chain cũ** (bản cuối: tag `legacy-simple-chain-bls-v1` = `a25cb135`); làm trên `dev`, `git add` theo tên file (working tree dùng chung), không push nếu user chưa yêu cầu, không đụng cụm 231/230.

## 0. Hiện trạng đã đạt (đã kiểm)
- `build_check.sh` 4/4 sạch; `go vet` + `go test -race` xanh cho `cmd/simple_chain/...`, `pkg/transaction`, `pkg/network`.
- Live trên cụm local (một endpoint): gửi qua TCP và RPC, deploy contract, event + bloom, pre-EIP-155 / sai chainId / `s` cao / quá cỡ / batch >1000 bị từ chối đúng mã, lệnh proto cũ bị từ chối, `nonce too low` đúng chuẩn geth (`cmd/tool/test_raw_eth_tcp_live`).
- **Chưa** được kiểm: nhiều validator đồng thuận state root dưới tải, `ci.sh run-now`, ethers/viem/web3j/foundry, tx hệ thống sau thay đổi, hiệu năng.

## 1. Cách đọc kế hoạch
Mức: **P0** = chặn production (phải xong), **P1** = cần xong trước khi mở cho dapp ngoài, **P2** = cổng duyệt/tối ưu. Mỗi mục có *Bằng chứng → Việc cần làm → Chấp nhận*. Thứ tự đề xuất: P0-1 → P0-3 → P0-4 → P0-2 → P0-5 → P0-7 → P0-6 (P0-6 là cổng cuối của P0) → P1 → P2.

## 2. P0 — chặn production

### P0-1. Luồng giao dịch vận hành/validator bị hỏng (thiếu trong kiểm kê W1.0)
- **Bằng chứng:** `execution/cmd/simple_chain/tool_register.go:181` (cờ `-tool-register-validator`, `main.go:36,75`) gọi `tcp_trans.SendTransactionWithDeviceKey` (BLS + device key) tới `VALIDATOR_CONTRACT_ADDRESS`; `execution/cmd/tool/fast_setup/main.go:94,106,191,199` ký BLS và gửi `command.SendTransactionWithDeviceKey`. Lệnh này nay bị từ chối vô điều kiện ⇒ **đăng ký validator / thiết lập nhanh không còn chạy**. `note/eth_only_inventory.md` không liệt kê hai producer này.
- **Việc cần làm:** (1) Kiểm kê lại MỌI luồng vận hành gửi tx bằng BLS/device key qua TCP/RPC (grep `SendTransactionWithDeviceKey`, `SetSign(`, `BuildTransactionWithDeviceKey*`, `tcp_trans.`) gồm `cmd/tool/*`, `ci.sh`, `deploy/**`, `scripts/**`, `portal/`; bổ sung vào bảng kiểm kê. (2) **Quyết định kiến trúc (ADR, user duyệt):** tx vận hành của validator/operator đi bằng (a) envelope Eth ký secp256k1 của tài khoản vận hành (cần xác minh contract validator/registry chấp nhận chủ tx là địa chỉ ECDSA — **CHƯA KIỂM**), hoặc (b) kênh nội bộ có xác thực, chỉ nhận từ node chính nó, không mở ở cổng công khai. Không giữ lại lệnh `SendTransactionWithDeviceKey` công khai. (3) Cài đặt theo ADR, chuyển `tool_register.go`, `fast_setup` và mọi tool tương tự. (4) E2E: thêm validator mới vào cụm 4 node bằng đường mới.
- **Chấp nhận:** đăng ký/gỡ validator chạy thật trên cụm cô lập (kết quả lệnh + output lưu `note/`), không còn tham chiếu tới lệnh legacy trong tool vận hành.

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

### P0-7. Mô hình phí: chưa nhất quán/chưa rõ tiền thật bị trừ bao nhiêu
- **Bằng chứng:** `eth_maxPriorityFeePerGas` trả hằng `0x5f5e100` (=1e8) trong khi `eth_gasPrice` = `MINIMUM_BASE_FEE` = 1e5 và `baseFeePerGas` = 1e5 (`cmd/simple_chain/backend.go:118-129`, `rpc_block.go`); ví/viem/ethers dùng `baseFee*2 + priority` ⇒ `maxFeePerGas` ≈ 1e8 (gấp ~1000 lần phí phẳng). Cổng admission chỉ đòi `MaxGasPrice ≥ MINIMUM_BASE_FEE` (`validation.go:435`). **CHƯA KIỂM:** công thức phí thực sự bị trừ (theo `maxFeePerGas`, theo `tip`, hay phí phẳng) và `effectiveGasPrice` trong receipt (test live cho 1 gwei).
- **Việc cần làm:** (1) Đọc code thực thi (`true_block_stm.go`, native gas) xác định chính xác tiền bị trừ cho tx type 0/1/2; (2) viết ADR chốt công thức (khuyến nghị v1: phí phẳng = `MINIMUM_BASE_FEE`, `maxPriorityFee` hợp lý nhỏ, người dùng không bị trừ quá mức) và dùng **một nguồn sự thật** cho `eth_gasPrice`, `eth_maxPriorityFeePerGas`, `eth_feeHistory`, block `baseFeePerGas`, admission, thực thi, `effectiveGasPrice`; (3) test với ethers/viem mặc định (không đặt phí tay): chuyển tiền thành công, số dư bị trừ đúng như ADR.
- **Chấp nhận:** ADR + test số dư chính xác theo từng type; ví mặc định gửi được không quá phí.

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

### P2-1. Một hash = `keccak256(envelope)` (W4, consensus-critical) — **cần user duyệt + cutover có wipe**
Thiết kế đã chốt ở `plan_eth_native_eip2718.md` G1 và `plan_eth_only_node_completion.md` W4: trường `raw_envelope` (tag mới) ở 3 bản `.proto`; hash tính giống hệt ở Go (`Transaction.Hash`) và Rust (`consensus/metanode/src/types/tx_hash.rs` + `queue.rs`, `block_sending.rs`, `executor.rs`, `network/rpc.rs`, `tx_socket_server.rs`, `peer_rpc/server.rs`, `tx_recovery.rs`); xác thực chữ ký trên bytes gốc; lưu `input` thô (bỏ bọc `CallData`); bỏ `SetEthHashMapblsHash`; **chốt trước quy ước hash cho tx hệ thống không có envelope**; test chéo Go↔Rust; mọi validator nâng cấp đồng thời. Đọc memory "Phuong an A" trước khi đụng `GLOBAL_TX_CACHE`/`parking_lot`. Sau W4, P0-3 và một phần P1-2/P1-4 đơn giản đi.

### P2-2. Benchmark và tối ưu
Đo tx/s trước/sau (mốc cũ 7060 → 6400 khi BLS batch verify) với envelope secp: `ecrecover` batch/cache, giảm chuyển đổi lặp; báo cáo số thật; không tối ưu sớm trước P0-4.

### P2-3. Rà soát bảo mật
Chạy `/security-review` trên diff từ `a25cb135` tới nay; fuzz (P0-4); kiểm thử "tx hợp lệ chữ ký nhưng độc hại" (initcode quá lớn, access list khổng lồ, blob sidecar sai KZG, 7702 authorization giả).

### P2-4. Phát hành và đường lùi
Runbook cutover chain 991 cập nhật (một lần wipe cho chainId + payload proto + định dạng tx); **đẩy tag/nhánh legacy lên remote khi user yêu cầu** (`legacy-simple-chain-bls-v1`, `legacy/simple-chain-bls`) — hiện chỉ ở local; ghi rõ đường lùi = checkout tag.

## 5. Cổng "sẵn sàng production" (checklist tổng)
- [ ] P0-1…P0-7 xong, có bằng chứng thật (lệnh + output) trong `note/`.
- [ ] `build_check.sh` sạch, `go build ./...` + `go vet ./...` sạch, `go test -race` xanh các package đã sửa + `pkg/blockchain/tx_processor`.
- [ ] `ci.sh run-now` PASS 100% (chạy thật), 4 validator cùng block hash/state root dưới tải, ≥1 chu kỳ chaos không fork.
- [ ] Ma trận tương thích (P1-5) có kết quả thật.
- [ ] Không còn đường người dùng→BLS; tx hệ thống BLS node-identity vẫn chạy (test).
- [ ] Không có cờ bỏ qua xác thực chữ ký ở deploy production; node từ chối khởi động nếu bật sai chỗ.
- [ ] Metrics + cảnh báo + runbook cập nhật; tài liệu dapp xong.
- [ ] User duyệt quyết định W4 (làm hoặc hoãn) và lịch cutover.

## 6. Nguyên tắc cho agent thực hiện
- Zero-Fork: tx chưa verify ⇒ từ chối tại chỗ hoặc giữ PENDING; không dùng timeout/sleep để quyết định dispatch; admission/thực thi tất định (không `time.Now()`, không map iteration).
- KISS/YAGNI: dùng `injectionQueue`/worker pool có sẵn (đã có buffer giới hạn), không thêm queue/worker/interface mới ngoài cần thiết; không thêm cờ/chế độ tương thích legacy.
- Mỗi lần sửa code: `consensus/metanode/scripts/build_check.sh` sạch (không warning); báo cáo cuối có khối tóm tắt tiếng Việt (AGENTS.md Part 5); cập nhật `PROJECT_STRUCTURE.md` khi đổi cấu trúc.
- **Không tin kết quả PASS do agent khác tự báo** (memory ghi từng sai): tự chạy lại; chỉ ghi PASS khi đã chạy thật.
- Commit nhỏ trên `dev`, `git add` theo tên file; không `git add <thư mục>`; không commit binary (ví dụ `execution/scripts/test/bls_pubkey`); không push khi chưa được yêu cầu.
- Cụm local (4200/4201/8646) chạy binary build cũ: thay đổi node cần restart mới kiểm live — báo user trước khi restart; thử nghiệm lớn dùng cổng cô lập 31xxx.

## 7. Còn mở (cần user)
1. **P0-1:** tx vận hành validator đi theo (a) envelope secp của tài khoản vận hành hay (b) kênh nội bộ xác thực? (cần agent xác minh contract trước, rồi user duyệt ADR).
2. **P0-7:** chốt mô hình phí v1 (khuyến nghị phí phẳng).
3. **P2-1:** có làm W4 (đổi hash, wipe) không và khi nào; "derive key" là gì.
4. Có đẩy tag/nhánh legacy lên remote không (P2-4).
