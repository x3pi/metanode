# Kế hoạch chi tiết: hoàn thiện node thực thi CHỈ hỗ trợ giao dịch chuẩn Ethereum (EIP-2718) — RPC + TCP

Trạng thái: **SẴN SÀNG GIAO AGENT** (viết 2026-10-06 sau khi đọc code ở commit `a25cb135`). Mọi "hiện trạng" có dẫn file; mục nào chưa kiểm ghi rõ ở mục 9.
Đọc trước: `AGENTS.md` (Zero-Fork, KISS, `build_check.sh`, tóm tắt tiếng Việt cuối response), `PROJECT_STRUCTURE.md`, `note/plan_eth_native_eip2718.md` (kế hoạch tổng, G0–G6), `note/plan_tcp_eth_only_ingress.md` (pha A — ĐÃ XONG ở `a25cb135`), `note/tcp_eth_client_guide.md`.

## 0. Quyết định mới của user (2026-10-06) — thay đổi phạm vi
**Không còn hỗ trợ tương thích simple-chain cũ (`bls_legacy`, dapp ký BLS/proto).** Hệ quả:
- Không còn "chế độ": node **luôn** là eth-only. Các guard `SecpOnlyTxSignatures()` ở `a25cb135` (kể cả `requireSecpModeForRawEth`, nhánh `bls_legacy` trong `eth_tx_converter.go`) trở thành **mã chết cần xóa**, không phải giữ.
- Bản cũ được bảo tồn tại: tag `legacy-simple-chain-bls-v1`, nhánh `legacy/simple-chain-bls` (cùng commit `a25cb135`). Cần hỗ trợ dapp BLS ⇒ dùng nhánh đó, không backport vào đây.
- Công việc mới làm trên nhánh **`feat/eth-only-node`**, worktree riêng **`/home/abc/chain-n/metanode-eth-only`** (không dùng chung working tree `/home/abc/chain-n/metanode` của các agent khác).
- Worktree mới KHÔNG có artifact build (thư mục `target/`, thư viện `execution/pkg/mvm/**/build`): chạy `consensus/metanode/scripts/build_check.sh` lần đầu để build (lâu hơn bình thường). Cụm local đang chạy ở cổng 4200/4201/8646 thuộc worktree cũ — test ở đây dùng cổng cô lập 31xxx, KHÔNG đụng cụm đó và KHÔNG đụng cụm 231/230.
- **Không `git push`** nhánh/tag nếu user chưa yêu cầu. `git add` theo tên file, không `git add <thư mục>`. Commit nhỏ, mỗi commit build_check sạch.

## 1. Mục tiêu / định nghĩa "xong"
1. Đường vào giao dịch của người dùng (TCP + RPC) **chỉ** nhận envelope EIP-2718 ký secp256k1. Không còn proto tx, device key, type 0xFF, chữ ký BLS của người dùng.
2. Dapp dùng thư viện Eth bất kỳ (ethers/viem/web3j/foundry/hardhat/MetaMask) chạy không cần patch: ví ước tính phí được, gửi được, nhận receipt/lỗi chuẩn.
3. Một hash/giao dịch = `keccak256(envelope)` (W4 — consensus-critical, cần user duyệt + cutover).
4. Giao dịch hệ thống của node (BLS identity: rollup, attestation, cross-chain, registry) **vẫn chạy** (xem mục 3 — rủi ro lớn nhất khi gỡ code).
5. `build_check.sh` sạch, `go test -race` các package sửa xanh, E2E trên cụm cô lập xanh, state root 4 validator khớp.

## 2. Hiện trạng đã kiểm (sau pha A) — khoảng cách còn lại
### 2.1 TCP (`execution/cmd/simple_chain`, `pkg/network`)
- Đã có: `SendRawTransaction`/`SendRawTransactions` (`processor/transaction_processor.go`, route ở `routes/routes.go`), `ValidateEthTxEnvelope` (`pkg/transaction/eth_validation.go`), ánh xạ `SetEthHashMapblsHash`, client Go + tool live `cmd/tool/test_raw_eth_tcp_live`.
- Còn lại: 3 command legacy vẫn đăng ký (`SendTransaction`, `SendTransactions`, `SendTransactionWithDeviceKey`) chỉ bị chặn theo chế độ; batch **bỏ qua im lặng tx lỗi** (chỉ log + `continue`) và chỉ trả hash tx đầu; mọi lỗi chuyển đổi dùng chung mã `InvalidSign (18)`; không giới hạn kích thước envelope/batch tường minh; logic dựng tx bị **nhân đôi** giữa `app.go` (converter tiêm vào processor, gồm xử lý blob sidecar) và `eth_tx_converter.go` (`buildMetaTxFromEthTx`).
### 2.2 RPC node (`cmd/simple_chain/*.go`, namespace `eth`, `mtn`, `admin`, `debug`)
Phương thức KHÔNG chuẩn Eth còn đăng ký (cần xóa):
- `eth_sendRawTransaction` biến thể 3 tham số `(input, inputEth, pubKeyBls)` nhận proto tx trực tiếp (`rpc_transaction.go:291-`); biến thể 1 tham số mới là chuẩn.
- `eth_sendTransaction` (`rpc_transaction.go:266`, nhận proto trong `Data`, thực chất không làm gì), `eth_getSendRawTransaction` (`:477`, dựng tx proto với device key rỗng), `eth_sendRawTransactionWithDeviceKey` (`MetaAPI` và `MtnAPI.SendRawTransactionWithDeviceKey`, `mtn_api.go`), `mtn_registerBlsKeyWithSignature` (`mtn_api.go`), `mtn_getDeviceKey`.
- `eth_estimateGas` nhận thử "chuẩn cũ" (hex proto tx) trước khi parse đối tượng chuẩn (`rpc_block.go:649-662`) — bỏ nhánh proto.
- `SendRawEthTransaction` (`:~655`) vẫn chọn khóa BLS (`blsKeyStore`, `GatewayBLSKey`, `keyPair`) rồi gọi `buildMetaTxFromEthTx` + đóng gói `TransactionWithDeviceKey` + `ProcessTransactionFromRpcWithDeviceKey` — vòng vèo dư thừa trong secp mode. `ethSendRawTxMiddleware` (`backend.go:~367`) chặn HTTP và gọi cùng hàm.
- `mtn_sendCrossChainTransfer` (`mtn_api.go:854`) là công cụ devnet dùng khóa BLS/ECDSA cứng — **xếp vào mục "kiểm / cô lập"**, không phải API người dùng.
### 2.3 Khả năng tương thích ví/công cụ (lệch đã thấy)
- Block: `gasLimit=0`, `gasUsed=0`, `baseFeePerGas=0` cứng (`rpc_block.go:48-53`) trong khi `eth_feeHistory` báo base fee phẳng `MINIMUM_BASE_FEE=100000` (`rpc_block.go:824`), `eth_gasPrice` = `MINIMUM_BASE_FEE`, `eth_maxPriorityFeePerGas` = `0x5f5e100` cứng (`backend.go:118-129`), cổng admission đòi `MaxGasPrice ≥ MINIMUM_BASE_FEE` (`validation.go:435`) ⇒ **số liệu phí trả về không nhất quán** (ví/viem/ethers dùng `baseFeePerGas` + `gasLimit` của block để tính phí).
- Receipt (`rpc_transaction.go:~855-880`): `logsBloom` rỗng cứng; `cumulativeGasUsed` = `BLOCK_GAS_LIMIT` (sai: phải là tổng gas lũy kế trong block); `effectiveGasPrice` lấy `rcp.GasFee()`.
- Lỗi `eth_sendRawTransaction`: trả `revertError` hoặc `-32000` + chuỗi tự do; **không có** thông điệp chuẩn geth (`nonce too low`, `insufficient funds for gas * price + value`, `already known`, `replacement transaction underpriced`, `intrinsic gas too low`, `exceeds block gas limit`, `invalid sender`…) — thư viện Eth phân loại lỗi theo chuỗi này.
- `eth_getTransactionCount`: dùng trạng thái "live" kể cả `pending` (`rpc_state.go:289-319`) — chưa kiểm hành vi khi có tx queued/nonce gap (mục 9).
### 2.4 Gateway `cmd/rpc` (module Go riêng, binary `rpc-client`) và bản sao `cmd/rpc-client`
- `handlers/send_raw_transaction.go`: gateway **re-sign BLS**: giữ `PKS` (kho khóa BLS theo địa chỉ), top-up native coin cho tài khoản mới (`SendOwnerTransfer`), account handler `ContractsInterceptor`, rồi gửi `SendTransactionWithDeviceKey`. Đây là kiến trúc hoàn toàn của thế giới cũ.
- Hai bản sao gần trùng nhau (`execution/cmd/rpc-client`, `execution/cmd/rpc/cmd/rpc-client`).
### 2.5 Còn BLS ở lõi (không phải API người dùng)
- `signature_enforcement.go` (`sigPolicy`, `isNodeBLSIdentity`), `validation.go` (nhánh `0xFF`, nhánh `pol.secp`, nhánh BLS, bypass `isSubNodeLagging`), `tx_validator_pool_core.go:258`, `transaction.go` (`SignSecpProto`, `ValidSecpProtoSign`, device key trong hash/proto), `config.go` (`TxSignatureMode`, `GatewayBLSKey`), `rpc_client/client.go` (client dựng tx device key).

## 3. Rủi ro lớn nhất: giao dịch hệ thống của node (KHÔNG được làm hỏng)
Giao dịch rollup system, attestation, cross-chain relay, registry/đăng ký… do worker của node tạo và ký bằng **BLS identity của node** (`isNodeBLSIdentity`; `validation.go` chỉ cho phép `RollupSystemAddress` từ node identity). Chúng dùng proto `Transaction` + `Sign` BLS, KHÔNG phải envelope Eth. Gỡ code BLS bừa bãi sẽ làm sập chúng. Quy tắc:
1. **W1.0 bắt buộc trước mọi xóa:** liệt kê MỌI nơi tạo/gửi tx proto ký BLS (grep `NewTransaction(`, `SetSign(`, `TransactionWithDeviceKey`, `ProcessTransactionFromRpc`, các worker `CommitteeAttestationWorker`, relayer, root-anchor submitter, rollup raftfeed…) và phân loại: (a) người dùng → xóa; (b) hệ thống/node → **giữ**, chuyển sang đường nội bộ không phơi ra TCP/RPC công khai (hoặc giữ như cũ nếu đã nội bộ).
2. Đường nội bộ của (b) giữ nguyên cơ chế BLS node-identity; chỉ **cổng công khai** (TCP/RPC) bị khóa về envelope Eth. Thêm test: tx hệ thống vẫn được nhận/thực thi sau khi xóa phần người dùng.
3. Nếu một tx hệ thống hiện đi qua cổng công khai (ví dụ relayer gọi `eth_sendRawTransaction` bằng ECDSA — `pkg/cross_chain/rootanchor/client.go:279,412` đã dùng envelope Eth, tốt) thì giữ nguyên.
Kết quả W1.0 ghi vào `note/` (bảng producer → loại → đường vào → quyết định); user duyệt bảng trước khi xóa nhóm (b).

## 4. Các luồng công việc (workstream), theo thứ tự
Thứ tự: **W1.0 → W2 → W3 → W1 (xóa) → W5 → W6 → W7 → (cổng duyệt) W4.** W4 (đổi hash) là thay đổi consensus-critical cần cutover có wipe, tách riêng, không làm nếu user chưa duyệt.

### W1.0 — Kiểm kê (không đổi code)
Bảng producer tx (mục 3); danh sách mọi tham chiếu tới 3 command legacy, RPC legacy, `0xFF`, `TxSignatureMode`, `GatewayBLSKey`, device key trong `execution/`, `consensus/`, `deploy/`, `ci.sh`, `scripts/`, `docs/`, `portal/`. Dùng codegraph (`codegraph_impact/callers`) theo AGENTS.md Part 4; fallback grep. Kiểm kê tham chiếu **deploy/ansible/systemd** tới binary `rpc-client`/`cmd/rpc`.
Chấp nhận: có file `note/eth_only_inventory.md`, user duyệt.

### W2 — Hoàn thiện TCP
- **W2.1 Một converter duy nhất.** Gom logic `UnmarshalBinary → ValidateEthTxEnvelope → NewTransactionFromEth → blob sidecar (KZG verify, blob_store.Put, bỏ sidecar)` vào MỘT hàm dùng chung cho TCP và RPC (đặt ở package mà cả `processor` và `main` import được; tiêm bằng 1 hàm, đã có cơ chế `SetRawEthConverter`). Xóa bản trùng giữa `app.go` và `eth_tx_converter.go`. Giữ đúng hành vi blob hiện tại.
- **W2.2 Batch báo lỗi từng tx.** `ProcessRawTransactionsFromClient`: mỗi tx lỗi (decode/validate/pool) trả `TransactionError` kèm **hash tx đó** (ethHash nếu giải mã được, zero nếu không) và chỉ số trong batch; phản hồi thành công trả danh sách hash đã nhận (không chỉ hash đầu). Quyết định giao thức (không đổi proto nếu tránh được): phản hồi thành công = `rlp([][]byte{hash...})` theo thứ tự batch, tx lỗi là hash zero + một `TransactionError` riêng; client Go cập nhật theo. Ghi rõ trong `note/tcp_eth_client_guide.md`.
- **W2.3 Mã lỗi tường minh.** Thay `InvalidSign (18)` dùng chung bằng mã riêng cho: decode lỗi, sai chainId, pre-EIP-155, malleable-s, sender không khôi phục được, vượt kích thước, nonce thấp/cao, thiếu tiền, trùng (`already known`)… Dùng/ thêm hằng trong `pkg/transaction` (không tái dùng số mã đã có nghĩa khác — kiểm trước).
- **W2.4 Giới hạn tường minh.** Kích thước envelope tối đa (theo EIP-3860 initcode 49152 + calldata hợp lý; blob 0x03 kèm sidecar xét theo giới hạn message `pkg/network` — đo, đừng đoán), số tx tối đa/batch, độ sâu RLP. Vượt ⇒ từ chối tại chỗ (không dispatch). Dùng `injectionQueue`/`AddTransactionsToPool` sẵn có, KHÔNG thêm hàng đợi/worker mới.
- **W2.5 Receipt/subscription qua TCP:** xác nhận receipt đẩy về client theo hash nào (đã đăng ký cả `metaHash` và `ethHash` ở `StoreTxHashConnEntry`); chuẩn hóa để client nhận receipt theo **ethHash**; test.
Chấp nhận: test đơn vị cho từng mục (kể cả batch hỗn hợp tốt/xấu), `go test -race`, e2e `test_raw_eth_tcp_live` mở rộng (batch có tx xấu), `build_check.sh` sạch.

### W3 — Hoàn thiện RPC (tương thích ví/công cụ)
- **W3.1 Hợp nhất đường gửi.** `eth_sendRawTransaction` (1 tham số) → cùng hàm converter W2.1 → đưa thẳng vào pool; bỏ chọn khóa BLS (`blsKeyStore`, `GatewayBLSKey`), bỏ đóng gói `TransactionWithDeviceKey` ở đường người dùng. Giữ `rpcTxConcurrencyLimiter` (đã có lý do cụ thể, xem comment `rpc_transaction.go`) và nhánh `EnablePrivateGateway`/speculative nếu còn dùng — kiểm xem còn cần không (W1.0).
- **W3.2 Lỗi chuẩn geth.** Ánh xạ lỗi admission → thông điệp/ mã JSON-RPC như go-ethereum (`nonce too low`, `nonce too high`, `insufficient funds for gas * price + value`, `already known`, `replacement transaction underpriced`, `intrinsic gas too low`, `exceeds block gas limit`, `invalid sender`, `transaction type not supported`…). Kiểm bằng ethers/viem: lỗi phải được phân loại đúng.
- **W3.3 Nhất quán phí (chốt v1 = phí phẳng).** Một nguồn sự thật `flatFee = MINIMUM_BASE_FEE`: block header JSON `baseFeePerGas = flatFee`, `eth_gasPrice = flatFee`, `eth_maxPriorityFeePerGas` = giá trị hợp lý nhất quán (bỏ hằng `0x5f5e100` nếu không có căn cứ; chốt công thức và ghi ADR), `eth_feeHistory` khớp, cổng admission khớp. Xác định `effectiveGasPrice` cho tx 1559 = `min(maxFeePerGas, flatFee + tip)` hoặc quy ước tương đương **đã ghi ADR** và nhất quán giữa admission, thực thi, receipt, `eth_getTransactionByHash`. Tất định: không phụ thuộc thời gian/trạng thái cục bộ.
- **W3.4 Block/receipt đúng chuẩn.** `gasLimit` thật (`BLOCK_GAS_LIMIT`), `gasUsed` thật (tổng), `receipt.cumulativeGasUsed` lũy kế đúng, `logsBloom` đúng theo log của receipt/block, `transactions[]` đúng loại, `eth_getTransactionByHash` đủ trường (`type`, `v/r/s` hoặc `yParity`, `accessList`, `maxFeePerGas`, `input` thô, `chainId`) cho cả tx pending. Lưu ý `input` thô phụ thuộc W4 (hiện `ToEthTransaction()` gỡ bọc `CallData`).
- **W3.5 `eth_getTransactionCount("pending")`/nonce queued.** Kiểm và định nghĩa hành vi (nonce tuần tự trong mempool, gap), test với 2 tx liên tiếp gửi nhanh bằng ethers.
- **W3.6 Xóa RPC không chuẩn** (mục 2.2) — gộp vào W1 nhưng làm sau khi W3.1 xong để không còn tham chiếu.
- **W3.7 `eth_estimateGas`/`eth_call`:** bỏ nhánh proto; kiểm state override, `from` mặc định, revert data đúng định dạng (`execution reverted` + data) cho viem/ethers.
Chấp nhận: bộ test tích hợp bằng ethers + viem (Node) và web3j (JVM) trên cụm cô lập: chuyển tiền, deploy contract, gọi hàm, đọc event, ước tính phí, lỗi nonce/funds đúng thông điệp; `hive rpc-compat` chạy (nếu cài được) với danh sách fail có giải trình.

### W1 — Xóa code legacy (sau khi W2, W3 xanh và bảng W1.0 được duyệt)
Làm theo từng commit nhỏ, mỗi commit build + test xanh:
1. TCP: xóa 3 route legacy + handler (`ProcessTransactionFromClient`, `ProcessTransactionsFromClient`, `ProcessTransactionFromClientWithDeviceKey`) + hằng command ở 4 file `command(s).go` + danh sách trong `pkg/network/handler.go` + `connection_client` (2 bản) + `rpc-client/client-tcp`.
2. RPC: xóa các method mục 2.2 (trừ nhóm "devnet/cô lập" đã quyết ở W1.0), bỏ `EstimateGas` proto.
3. Lõi: xóa nhánh `0xFF` (`SignSecpProto`, `ValidSecpProtoSign`, `secpProtoError`, `tx_validator_pool_core.go:258`), `sigPolicy.secp`/`blsAllowed` cho người dùng (giữ `isNodeBLSIdentity` cho tx hệ thống), bypass `isSubNodeLagging` cho người dùng (đã có comment: nó nguy hiểm trong secp), `TxSignatureMode` + `validateTxSignatureMode` (**config cũ có `"tx_signature_mode":"secp"` vẫn phải load được: bỏ qua trường; giá trị `"bls_legacy"` ⇒ lỗi khởi động rõ ràng**), `GatewayBLSKey`, kho khóa BLS người dùng, device key (`SavePendingDeviceKey`, `GetDeviceKey`, ...). Cẩn thận lược đồ DB: không đổi định dạng dữ liệu nếu chưa có cutover.
4. Xóa `requireSecpModeForRawEth`, nhánh `bls_legacy` trong converter, test `TestLegacyMode_*` (thay bằng test "legacy command không tồn tại ⇒ lỗi unknown command").
5. Proto `Transaction`: KHÔNG xóa tag; đánh `reserved`/deprecated trường người dùng-BLS **chỉ ở W4/cutover** (3 bản sao: `execution/pkg/proto`, `execution/cmd/rpc/pkg/proto`, `consensus/metanode/proto`). Không tái dùng số tag.
Chấp nhận: grep chứng minh không còn đường người dùng→BLS; test tx hệ thống (mục 3) vẫn xanh; `build_check.sh` sạch (không warning).

### W5 — Gateway, tool, client, CI
- **Gateway `cmd/rpc` + `cmd/rpc-client`:** **quyết định: gỡ khỏi node mới** (re-sign BLS, top-up, account handler thuộc thế giới cũ; dapp kết nối thẳng RPC của node đã có CORS, rate limiter, WS; bản cũ còn ở nhánh legacy). Cập nhật `deploy/`, `ci.sh`, `PROJECT_STRUCTURE.md`, ansible/systemd nếu có tham chiếu. Nếu W1.0 phát hiện cần proxy/TLS/WS riêng ⇒ viết passthrough mỏng chuyển `eth_sendRawTransaction` sang `SendRawTransaction`, KHÔNG kế thừa PKS/top-up. Ghi quyết định vào ADR.
- **Tool/benchmark/e2e** đang ký BLS/proto: `cmd/tool/tx_sender`, `tps_benchmark_multi_node`, `e2e_account_gate`, `tool-test-chain/test-tcp/client-tcp`, `register_chains`, `live_asset_bridge`, `fund_tps_bench_accounts`, `pkg/txsender`, `pkg/rpc_client` (hàm `BuildTransactionWithDeviceKey*`) … chuyển sang ký secp256k1 + gửi envelope; xóa phần device key. Kiểm từng tool xem còn dùng được không, xóa tool chết.
- **CI:** `ci.sh run-now` phải pass 100% với giao dịch envelope thuần (E2E gate 14, co-attestation 4 validator, cross-chain 26) — chạy thật, không suy đoán (xem memory về audit giả).
- **Benchmark:** đo lại tx/s với `ecrecover` (số cũ 7060 → 6400 khi bật BLS batch verify); đề xuất batch/cache `ecrecover`.

### W6 — Kiểm chứng tương thích
Ma trận (cụm cô lập 31xxx, 4 validator): ethers v6, viem, web3j (Android/JVM), foundry (`forge create`, `cast send/call/receipt`), hardhat deploy; mỗi cái: chuyển, deploy, gọi, event/log, lỗi nonce/funds, ước tính phí. `ethereum/tests` TransactionTests (vector decode/validity) và `hive rpc-compat` nếu cài được; ghi mọi fail có giải trình (khác biệt chủ đích: `eth_getProof`, không MPT…). Kết quả thật (lệnh + output) lưu `note/`; mục chưa chạy ghi "chưa chạy".

### W7 — Tài liệu / cấu trúc
`PROJECT_STRUCTURE.md` (Last updated + mục command/RPC/xóa gateway), `note/tcp_eth_client_guide.md` (cập nhật batch/lỗi), hướng dẫn dapp (web3j/ethers/viem), cập nhật `note/plan_eth_native_eip2718.md` (đánh dấu G4/G5 phần đã làm, ghi quyết định bỏ legacy).

### W4 — Một hash = keccak(envelope) (CỔNG DUYỆT, consensus-critical)
Chỉ bắt đầu khi user duyệt (cần cutover có wipe chung chain 991). Thiết kế đã chốt trong `plan_eth_native_eip2718.md` G1: thêm `bytes raw_envelope` (tag mới) vào proto `Transaction` ở **3 bản sao**; `hash = keccak256(raw_envelope)` tính **giống hệt** ở Go (`Transaction.Hash`) và Rust (`consensus/metanode/src/types/tx_hash.rs`, dùng ở `node/queue.rs`, `executor_client/block_sending.rs` (tập `seen`), `commit_processor/executor.rs`, `network/rpc.rs`, `network/tx_socket_server.rs`, `network/peer_rpc/server.rs`, `node/transition/tx_recovery.rs`); tx hệ thống (không có envelope) cần quy ước hash riêng đã ghi ADR (ví dụ hash proto như cũ cho loại hệ thống) — **bắt buộc chốt trước khi code**. Xác thực chữ ký trên bytes gốc, lưu `input` thô (bỏ bọc `CallData`/`DeployData`), bỏ `SetEthHashMapblsHash`. Test chéo Go↔Rust (cùng input ⇒ cùng hash; sai = fork), property test `hash == go-ethereum tx.Hash()`, mọi validator nâng cấp đồng thời. Đọc memory "Phuong an A" trước khi đụng `GLOBAL_TX_CACHE`/`parking_lot` trong consensus-core.

## 5. Nguyên tắc bất biến (AGENTS.md)
- Zero-Fork: tx chưa verify ⇒ từ chối tại chỗ hoặc giữ PENDING; không dùng timeout/sleep để quyết định dispatch; mọi quyết định admission/thực thi tất định (không `time.Now()`, không map iteration, không trạng thái cục bộ).
- KISS/YAGNI: không thêm worker/queue/interface ngoài hàm converter tiêm; dùng `injectionQueue`/`AddTransactionsToPool` có sẵn (đã có buffer giới hạn).
- Không blocking I/O trong event loop; không đổi interface Go↔Rust (FFI) ngoài W4.
- Mỗi lần sửa code: `consensus/metanode/scripts/build_check.sh` sạch (Go+Rust+FFI, không warning); báo cáo cuối có khối tóm tắt tiếng Việt (AGENTS.md Part 5); cập nhật `PROJECT_STRUCTURE.md` khi đổi cấu trúc.

## 6. Quyết định đã chốt (không hỏi lại)
Bỏ hẳn legacy; eth-only vô điều kiện; phí phẳng v1; giữ blob 0x03/7702 như hiện trạng (chỉ thêm test); cấm pre-EIP-155 và `s` cao; giữ NOMT + header riêng; gỡ gateway BLS `cmd/rpc` khỏi node mới (passthrough mỏng chỉ khi W1.0 chứng minh cần); tx hệ thống BLS node-identity được giữ.

## 7. Còn mở (cần user)
1. **Duyệt bảng W1.0** (nhất là nhóm tx hệ thống) trước khi xóa.
2. **Duyệt W4 + thời điểm cutover** (đổi hash + wipe; gộp với runbook cutover chain 991).
3. "Derive key" là gì (khóa con theo ứng dụng? khóa công khai kèm tx?) — chỉ cần trước W4/G5; hiện không chặn W1–W3.

## 8. Kiểm thử bắt buộc theo luồng
W2: batch hỗn hợp tốt/xấu, vượt kích thước, nonce lặp, receipt theo ethHash. W3: lỗi chuẩn theo từng thư viện, nhất quán phí (giá ví ước tính luôn ≥ ngưỡng admission), block/receipt trường đúng, nonce pending. W1: tx hệ thống còn chạy; lệnh legacy ⇒ unknown command; config cũ vẫn load. Tất cả `go test -race`; E2E cụm 4 validator state root khớp.

## 9. Chưa kiểm / rủi ro
- Chưa liệt kê đầy đủ producer tx hệ thống (W1.0) — rủi ro lớn nhất.
- Chưa xác minh hành vi `eth_getTransactionCount(pending)` với tx queued/nonce gap.
- Chưa kiểm giới hạn message `pkg/network` với blob sidecar qua TCP.
- Chưa chạy thật ethers/viem/web3j/foundry/hive trên chain này (W6).
- Số tx/s sau khi bỏ BLS user-verify và thêm `ecrecover` chưa đo.
- Tham chiếu deploy/ansible tới gateway `rpc-client` chưa kiểm.
- Worktree mới cần build từ đầu (lâu); tránh xung đột cổng với cụm local đang chạy ở worktree cũ.
