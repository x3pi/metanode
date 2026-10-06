# Kế hoạch (giao agent thực hiện): TCP chỉ nhận giao dịch chuẩn Ethereum (EIP-2718)

Trạng thái: **SẴN SÀNG THỰC HIỆN**. Viết 2026-10-06. Là bước thực thi đầu tiên của `note/plan_eth_native_eip2718.md` (G4 làm trước, không đổi hash/định dạng lưu ⇒ KHÔNG cần wipe).
Đọc trước: `AGENTS.md` (Zero-Fork, KISS, build_check.sh, tóm tắt tiếng Việt cuối response), `PROJECT_STRUCTURE.md`, `note/plan_eth_native_eip2718.md`.

## 1. Hiện trạng đã xác minh (đường TCP)
Đăng ký route ở `execution/cmd/simple_chain/routes/routes.go:89-98`:
- `SendTransaction` → `processor/transaction_processor.go:304` (body = proto `pb.Transaction`, `Unmarshal` lười ở `executeAndAddTx`, dòng ~239).
- `SendTransactions` → `transaction_processor.go:447` (body = proto `pb.Transactions`, qua `transaction.UnmarshalTransactions`).
- `SendTransactionWithDeviceKey` → `transaction_processor.go:354` (body = `TransactionWithDeviceKey`, BLS + device key).
Không route nào dùng `UnmarshalBinary` của go-ethereum. Type `0xFF` (`SignSecpProto`/`ValidSecpProtoSign`, `validation.go:225`, `signature_enforcement.go:91`, `tx_validator_pool_core.go:258`) là proto ngoài dải EIP-2718.
Phía Rust (`consensus/metanode/src/network/tx_socket_server.rs`) chỉ nhận byte mờ qua FFI ⇒ **không sửa Rust**.
Đường RPC `eth_sendRawTransaction` đã chuẩn: tham chiếu `execution/cmd/simple_chain/eth_tx_converter.go` (nhánh `secpOnly`, dòng ~55) — tái dùng, không viết lại.

## 2. Quyết định đã chốt (không hỏi lại)
1. **Pha A (kế hoạch này):** chỉ đổi cổng TCP. KHÔNG đổi hash nội bộ, proto lưu, FFI, ánh xạ `SetEthHashMapblsHash`. Pha B (hash = keccak(envelope), bỏ `CallData` bọc, xóa code BLS) = G1/G5 của plan chính, làm cùng cutover chain 991.
2. **Chỉ áp dụng khi `tx_signature_mode="secp"`** (`config.SecpOnlyTxSignatures()`). Chế độ `bls_legacy` (simple-chain cũ cho dapp BLS) giữ nguyên hành vi, không động vào.
3. **Giao thức TCP mới, không đổi proto (tránh codegen nhiều bản sao `.proto`):**
   - `SendRawTransaction`: body = bytes envelope EIP-2718 thô (legacy RLP hoặc `type||payload`).
   - `SendRawTransactions`: body = `rlp.EncodeToBytes([][]byte{envelope...})`.
   - Giải mã bằng `ethtypes.Transaction.UnmarshalBinary` (go-ethereum), rồi `transaction.NewTransactionFromEth` — đúng đường RPC đang dùng.
4. **Trong secp mode:** `SendTransaction`, `SendTransactions` (proto), `SendTransactionWithDeviceKey` bị từ chối (trả lỗi code `InvalidSign`/mã tương ứng, ghi log, không panic). Type `0xFF` bị từ chối ở mọi nơi trong mode này.
5. **Cấm tx pre-EIP-155** (không có chainId) và chữ ký malleable (s > n/2, EIP-2). chainId phải bằng chainId genesis.
6. Mô hình phí, blob 0x03, EIP-7702 0x04: **giữ nguyên hành vi hiện tại**, không đụng trong pha này.
7. Không dùng `time.Now()`/map iteration trong đường giải mã→admission. Tx lỗi ⇒ từ chối tại chỗ, không dispatch.

## 3. Việc cần làm (theo thứ tự)
**T0 — Impact analysis (bắt buộc, AGENTS.md Part 4):** codegraph `callers/impact` cho `ProcessTransactionFromClient`, `ProcessTransactionsFromClient`, `ProcessTransactionFromClientWithDeviceKey`, `NewTransactionFromEth`, `ValidSecpProtoSign`; liệt kê mọi client TCP dùng 3 command cũ: `pkg/connection_manager/connection_client/chain_methods.go`, `cmd/rpc/pkg/connection_manager/connection_client/chain_methods.go`, `pkg/txsender/client.go`, `cmd/tool/tx_sender`, `cmd/tool/tps_benchmark_multi_node`, `cmd/tool/e2e_account_gate`, `cmd/tool/tool-test-chain/test-tcp/client-tcp`, `cmd/rpc/pkg/{account_handler,file_handler,robot_handler}`. Ghi kết quả vào PR/commit message.
**T1 — Command + route:** thêm hằng `SendRawTransaction`/`SendRawTransactions` vào `cmd/simple_chain/command/command.go`; đăng ký trong `routes.go`; thêm `command` vào danh sách ở `pkg/network/handler.go:123` (nơi liệt kê các command tx) để hành vi xếp hàng/thứ tự giống command cũ.
**T2 — Handler:** trong `transaction_processor.go` thêm `ProcessRawTransactionFromClient`/`ProcessRawTransactionsFromClient`. Giữ cơ chế hiện có: copy body, đẩy `injectionQueue` (bounded, đã có `InjectionQueueSize`), giải mã ở worker (không giải mã trong readLoop), kiểm tra `pendingOverloaded`. Batch đi qua `AddTransactionsToPool` như `ProcessTransactionsFromClient`. Bổ sung loại `rawEth []byte` vào `injectionRequest`; trả lỗi chuẩn khi `UnmarshalBinary` fail, chainId sai, pre-EIP-155, s cao.
Ràng buộc đã xác minh khi viết T2 (xem mục 7): (a) logic dựng tx từ envelope nằm ở `buildMetaTxFromEthTx` trong **package `main`** (`cmd/simple_chain/eth_tx_converter.go`), package `processor` KHÔNG import được `main` ⇒ tiêm vào `TransactionProcessor` một hàm `func(rawEth []byte) (types.Transaction, error)` (setter như `SetEnvironment`) do `main` cấp, hoặc tách phần dựng tx secp ra package dùng chung; chọn cách ít thay đổi nhất, không tạo interface mới ngoài 1 hàm đó. (b) Đường secp phải KHÔNG dùng khóa BLS node/`blsKeyStore`/`GetDeviceKey`/`time.Now()` (nhánh `!secpOnly` của `buildMetaTxFromEthTx` đã bỏ qua chúng; kiểm tra lại, không để lọt). (c) Sidecar blob 0x03: phải giữ nguyên bước KZG verify + `blob_store.Put` + bỏ sidecar như trong `buildMetaTxFromEthTx`.
**T3 — Chặn đường cũ trong secp mode:** wrapper ở 3 handler cũ: nếu `SecpOnlyTxSignatures()` ⇒ trả lỗi rõ ràng. Cộng thêm: `validation.go`/`tx_validator_pool_core.go` vẫn từ chối `0xFF` (đã có); thêm test chứng minh.
**T4 — Client:** thêm `SendRawTransaction(s)` vào 2 `connection_client`, `txsender`; cập nhật tool (`tx_sender`, `tps_benchmark_multi_node`, `e2e_account_gate`, `client-tcp`) sang ký secp bằng `types.SignTx` và gửi envelope; bỏ nhánh type 0xFF/device key khỏi tool dùng cho chain secp. Tool dùng cho chain `bls_legacy` giữ nguyên.
**T5 — Test:** unit test: round-trip mọi type 0x00–0x02 (+0x03/0x04 nếu đang bật), từ chối pre-EIP-155 / chainId sai / s cao / rác / envelope rỗng / type lạ; secp mode từ chối 3 command cũ; `bls_legacy` vẫn nhận 3 command cũ (regression). Chạy `go test -race` cho các package đã sửa.
**T6 — Build + live:** `consensus/metanode/scripts/build_check.sh` sạch (không warning). Live (cụm cô lập, KHÔNG đụng 231/230): gửi tx qua `SendRawTransaction` bằng ví go-ethereum, xác nhận receipt, state root 4 validator khớp. Chỉ báo PASS khi thật sự chạy; không tin kết quả agent khác (xem memory về audit giả).
**T7 — Tài liệu:** cập nhật `PROJECT_STRUCTURE.md` (command mới, `Last updated`), đánh dấu G4 phần TCP đã xong trong `note/plan_eth_native_eip2718.md`.
**T8 — E2E client ngoài Go (Android/mobile):** tạo test/ví dụ nhỏ dùng **web3j** (JVM, chạy được trên máy dev không cần thiết bị): ký tx type 0x02 bằng khóa secp256k1, gửi `eth_sendRawTransaction` tới cụm cô lập, xác nhận receipt bằng `ethHash`. Nếu có thể, thêm bản `ethers`/`viem` (Node). Ghi kết quả thật (lệnh + output) vào `note/`; nếu không chạy được, ghi rõ "chưa chạy" — không báo PASS suy đoán. Viết `docs/` hướng dẫn ngắn theo mục 3c.

## 3b. Hash trả về và receipt (điểm dễ sai)
Hiện có hai hash: `ethHash = ethTx.Hash()` (keccak envelope) và `metaHash = tx.Hash()` (proto nội bộ). Đường RPC gọi `SetEthHashMapblsHash(ethHash, metaHash)` (`rpc_transaction.go:343,462,691,757`) và trả `ethHash`; mọi `eth_getTransactionReceipt/ByHash` dò ngược qua ánh xạ đó. Đường TCP hiện trả `tx.Hash()` (= metaHash) trong `sendTransactionResult` và đăng ký `StoreTxHashConnEntry` theo metaHash.
Bắt buộc cho `SendRawTransaction(s)`:
1. Gọi `SetEthHashMapblsHash(ethTx.Hash(), tx.Hash())` **trước** khi đưa vào pool (như RPC), để dapp tra receipt bằng hash chuẩn Eth qua RPC.
2. Phản hồi TCP: giữ `StoreTxHashConnEntry`/receipt theo metaHash (nội bộ, không đổi cơ chế), nhưng **phải nêu rõ cho client** hash nào được trả. Quyết định: phản hồi chứa `metaHash` như cũ (không đổi `sendTransactionResult`); client tự biết `ethHash` vì chính nó đã ký (`tx.Hash()` của thư viện Eth). Ghi vào doc client: dùng `ethHash` để tra `eth_getTransactionReceipt`.
3. Test: gửi qua TCP rồi `eth_getTransactionReceipt(ethHash)` trả đúng receipt.
Pha B sẽ xóa ánh xạ này; đừng cố giải ở đây.

## 3c. Hướng dẫn dapp / Android (phần tài liệu cần viết, T8)
- Chain secp: dapp (kể cả Android) chỉ cần ký secp256k1 chuẩn Eth (EIP-155/1559) rồi gửi bytes đã ký. Không tự viết RLP; thư viện Eth đã sinh envelope.
- **Khuyến nghị mobile: RPC `eth_sendRawTransaction`** (HTTP/WebSocket) — đường đã chuẩn, không cần client socket TCP riêng. TCP `SendRawTransaction` dành cho client Go/backend hiệu năng cao.
- Thư viện Android dùng được: web3j (`TransactionEncoder.signMessage`), kethereum, go-ethereum qua `gomobile`, hoặc ethers/viem trong WebView/React Native. **Chưa kiểm thử thực tế trên chain này** ⇒ T8 bắt buộc có e2e.
- Lưu ý: chainId đúng (parent = 991), nonce lấy `eth_getTransactionCount(pending)`, tài khoản phải đăng ký nếu `account_gate=parent_registered`; Android Keystore gốc không hỗ trợ secp256k1 ⇒ giữ khóa trong app, bọc bằng khóa Keystore.
- Dapp đang ký BLS/device key ở lại chain `bls_legacy` hoặc đổi sang secp.

## 4. Ràng buộc / không được làm
- Không đổi `pkg/proto/transaction.proto` và các bản sao; không đổi FFI; không đổi cách tính `Transaction.Hash()`.
- Không `git add <thư mục>` (worktree dùng chung) — `git add` theo tên file.
- Không thêm worker/queue mới; chỉ dùng `injectionQueue` có sẵn (đã có giới hạn bộ đệm).
- Không nhồi pattern phân tán (circuit breaker…) vào helper giải mã.
- Tx chưa verify phải bị từ chối hoặc giữ PENDING, không dispatch theo giả định.

## 5. Tiêu chí hoàn thành
- Chain secp: ví/thư viện Ethereum gửi envelope thô qua TCP thành công; 3 command cũ bị từ chối có test.
- Chain `bls_legacy`: không đổi hành vi (test regression xanh).
- `build_check.sh` sạch, `go test -race` các package sửa xanh, live 4 validator state root khớp.
- Báo cáo kết thúc có khối tóm tắt tiếng Việt theo `AGENTS.md` Part 5.

## 5b. Điểm chưa xác minh (agent phải kiểm tra, không giả định)
- Framing TCP (`pkg/network`, `Message`/`SendBytes`) để client không phải Go tự cài — chỉ cần nếu muốn hỗ trợ TCP ngoài Go; mobile dùng RPC nên không chặn việc này.
- Có kiểm tra pre-EIP-155 / chữ ký malleable / chainId ở đường `eth_sendRawTransaction` hiện tại không? Nếu RPC chưa có, thêm một hàm kiểm tra dùng chung cho cả RPC và TCP (không nhân đôi logic); nếu thêm vào RPC thì ghi rõ thay đổi hành vi.
- Admission của tx secp qua `validation.go` (nhánh `pol.secp`) dùng `tx.ValidEthSign()` — xác nhận nó khôi phục sender từ chữ ký và so với `FromAddress` (không tin `FromAddress` do client cung cấp). Đây là điểm bảo mật: nếu chỉ tin trường proto thì tx forged qua được.
- Blob 0x03 qua TCP: kích thước envelope kèm sidecar so với giới hạn message của `pkg/network`.

## 6. Pha B (sau, KHÔNG làm ở đây)
`tx.Hash() = keccak(envelope)`, lưu envelope thô, bỏ ánh xạ hash/`CallData`, xóa BLS user flow + `0xFF` + `tx_signature_mode` — theo G1/G2/G5 của `plan_eth_native_eip2718.md`, gộp vào wipe cutover chain 991. Cần G0 (tác động lên `GLOBAL_TX_CACHE`, `TxPayloadCache`, `SubmitOrderGate`) trước.

## 7. Nguồn các nhận định (đã đọc code 2026-10-06)
- `eth_tx_converter.go`: `buildMetaTxFromEthTx` (package main) — nhánh `secpOnly` giữ chữ ký ETH, bỏ BLS/device key; đoạn blob sidecar.
- `rpc_transaction.go:343,462,691,757`: `SetEthHashMapblsHash`; `sendRawEthTransactionSync` (dòng ~701) trả `ethTx.Hash()`.
- `transaction_processor.go:547-577`: `processTransactionFromClient` — `StoreTxHashConnEntry(tx.Hash())`, `sendTransactionResult(conn, tx.Hash(), msgID)`.
- `routes.go:89-98`, `validation.go:225-262`, `signature_enforcement.go`, `tx_validator_pool_core.go:258`, `config.go:217-352`.
