# Kế hoạch: node thực thi tương thích Ethereum đầy đủ (EIP-2718), bỏ chữ ký BLS của người dùng

Trạng thái: **KẾ HOẠCH ĐÃ KIỂM LẠI VỚI CODE (vòng 2, 2026-10-06), chưa triển khai.** Mọi nhận định "hiện trạng" có dẫn nguồn file/dòng; phần chưa kiểm ghi rõ ở mục 7. Quyết định của user: **tương thích Ethereum hoàn toàn, dapp tự chọn thư viện Eth** (không có đường proto thay thế, không giữ type 0xFF chính thức, không ký hộ bằng khóa gateway).
Kế hoạch thực thi chi tiết của bước đầu (G4 pha A, cổng TCP): `note/plan_tcp_eth_only_ingress.md`.

## 1. Hiện trạng (đã đọc code)
| Điểm | Thực tế trong repo |
|---|---|
| Giải mã envelope | `eth_sendRawTransaction` dùng `types.Transaction.UnmarshalBinary` của go-ethereum **v1.15.11** (`execution/go.mod`, không fork) ⇒ type 0x00–0x04 (`rpc_transaction.go`, `pkg/transaction/transaction.go: NewTransactionFromEth`). Type lạ ⇒ `unsupported Ethereum transaction type`. Thêm type mới cần fork go-ethereum. |
| Biểu diễn nội bộ | proto `Transaction` (`pkg/proto/transaction.proto`): `Type uint64` + trường eth rải rác (`GasTipCap/GasFeeCap/AccessList/BlobVersionedHashes/AuthorizationList`) cạnh trường BLS (`Sign`, `LastDeviceKey`, `NewDeviceKey`, `MaxTimeUse`). **Có 3 bản sao `.proto`:** `execution/pkg/proto/`, `execution/cmd/rpc/pkg/proto/`, `consensus/metanode/proto/`. |
| **Hai hash cho một giao dịch** | Go: `Transaction.Hash()` (`transaction.go:661`) = keccak(proto `TransactionHashData`), không phải `keccak(envelope)`. RPC ánh xạ `SetEthHashMapblsHash(ethHash → metaHash)` ở `rpc_transaction.go:343,462,691,757` và dò ngược ở RPC block/receipt/subscription. |
| **Rust cũng băm proto (bổ sung vòng 2)** | `consensus/metanode/src/types/tx_hash.rs` giải mã proto `Transaction` và tính cùng keccak(`TransactionHashData`) để khớp Go. Dùng ở `node/queue.rs`, `executor_client/block_sending.rs` (tập `seen` khử trùng, dòng ~1529), `commit_processor/executor.rs` (~474), `network/rpc.rs`, `network/tx_socket_server.rs`, `network/peer_rpc/server.rs`, `node/transition/tx_recovery.rs`. ⇒ **Rust KHÔNG phải byte mờ**; đổi hash phải đổi cả Rust, mọi validator nâng cấp đồng thời (hash là khóa khử trùng/khôi phục). |
| Chữ ký kiểm trên tx **dựng lại** | `ValidEthSign` (`transaction.go:1131`) gọi `ToEthTransaction()` (dựng lại từ proto, gỡ bọc `CallData`/`DeployData`) rồi `DeriveSenderFromEthTransaction` và so với `FromAddress`: khôi phục sender thật (tốt, không tin `FromAddress`), nhưng xác thực trên bản **tái mã hóa**, không phải bytes gốc ⇒ phụ thuộc round-trip proto↔eth không mất mát. |
| Pre-EIP-155 | `DeriveSenderFromEthTransaction` (`transaction.go:1336`) chủ động hỗ trợ `HomesteadSigner` cho legacy không chainId. Hiện bị chặn **gián tiếp** bởi `ValidChainID` so bằng chainId cấu hình (`validation.go:398`, `transaction.go:1418`) — chưa có từ chối tường minh. |
| Malleable `s` | `ValidSecpProtoSign` có `ValidateSignatureValues(..., true)` (`transaction.go:1185`); đường ETH dựa vào go-ethereum signer (homestead). Cần test tường minh, không giả định. |
| Calldata bị bọc | `Data` bọc `CallData`/`DeployData` proto; `ToEthTransaction` gỡ ra (`transaction.go:136-144`). |
| Re-sign BLS | `buildMetaTxFromEthTx` (`cmd/simple_chain/eth_tx_converter.go`, **package main**) tạo device key bằng `time.Now().Unix()` (không tất định), ký lại bằng khóa BLS gateway; nhánh `secpOnly` (`tx_signature_mode="secp"`) bỏ qua BLS/device key và giữ chữ ký ETH. |
| Chế độ secp | `tx_signature_mode="secp"` (`pkg/config/config.go:217-352`) + type `0xFF` qua TCP (ngoài dải 0x00–0x7f) — lớp vá trên mô hình cũ. |
| Mô hình phí | Phí **phẳng**: `ValidMaxGasPrice(common.MINIMUM_BASE_FEE)` với `MINIMUM_BASE_FEE=100000` (`validation.go:435`, `pkg/common/constant.go:18`). Không có base fee động. RPC block báo `baseFeePerGas = 0` (`rpc_block.go:53`) ⇒ **lệch**: ví ước tính theo baseFee 0 có thể gửi `gasPrice < 100000` và bị từ chối. |
| Blob / 7702 | Đã có: `FromEthBlobTx` + `VerifyBlobSidecar` (KZG), `processAuthorizationList` (`tx_processor/authorization.go`), intrinsic gas (`vm_processor/intrinsic_gas.go`). Đã live-verify (memory "EVM production-hardening"). |
| Tên RPC | Tên thật là `eth_sendRawTransactionWithDeviceKey` (`pkg/rpc_client/client.go:376`); tên `mtn_sendRawTransactionWithDeviceKey` ở bản plan trước **không tồn tại trong Go** (đã sửa). Cần quét lại danh sách `mtn_send*` thật ở G0. |

## 2. Nguyên tắc kiến trúc đích
1. **Một dạng chuẩn duy nhất: bytes envelope EIP-2718.** `hash = keccak256(envelope)`, một hash, không ánh xạ. Người gửi suy ra bằng `ecrecover` (cache), không tin `FromAddress` client cung cấp; **xác thực trên bytes gốc**, không trên bản tái mã hóa.
2. **Người dùng chỉ ký secp256k1.** BLS chỉ còn cho danh tính node/validator/cụm (attestation, cross-chain, committee).
3. **Tính năng riêng của chain đi qua cơ chế Ethereum sẵn có** (precompile / contract hệ thống / calldata có selector). Ví và công cụ Eth dùng nguyên.
4. **Type tự định nghĩa:** không làm trong v1 (cần fork go-ethereum; nếu làm sau phải tránh 0x7E Optimism, 0x64–0x6A Arbitrum, 0x7B/0x7C Celo, nằm trong 0x00–0x7f).
5. **Tất định tuyệt đối:** không `time.Now()`, không map iteration, không trạng thái cục bộ trong đường envelope → thực thi. Thà PENDING chứ không fork (AGENTS.md Part 2.5).

## 3. Bỏ cái cũ hay giữ? (quyết định)
| Thành phần | Quyết định | Lý do |
|---|---|---|
| Chữ ký BLS người dùng, `Sign`, device key (`LastDeviceKey/NewDeviceKey`), `TransactionWithDeviceKey`, `SendTransactionWithDeviceKey` (TCP), `eth_sendRawTransactionWithDeviceKey` (RPC), `RegisterBlsKeyWithSignature`, `GatewayBLSKey` | **BỎ khỏi node thực thi mới** | Mâu thuẫn "không ký BLS"; nguồn không tất định và hai hash. |
| `SetEthHashMapblsHash` + dò ngược ở RPC | **BỎ** (G1) | Biến mất khi hash = keccak(envelope). |
| Bọc `CallData`/`DeployData` | **BỎ**; lưu `input` thô (G1) | Tương thích gas/`input`/tracing. |
| Type `0xFF` + `SignSecpProto`/`ValidSecpProtoSign` | **BỎ** sau G4 pha A | Ngoài EIP-2718; user đã chốt không giữ. |
| `tx_signature_mode` | **Thay bằng cờ genesis cố định** (`tx_format=eip2718`) ở G5; `bls_legacy` chỉ còn ở binary simple-chain cũ | Tránh node hai chế độ. Gộp vào cutover chain 991 (một lần wipe). |
| Account gate, registry/relay/attestation f+1, chain ID chung 991, BLS validator/cụm/cross-chain | **GIỮ** | Độc lập định dạng tx; gate chỉ cần `from` (ecrecover). |
| Type 0x00/0x01/0x02 | **GIỮ** (lõi v1) | |
| Type 0x03 (blob), 0x04 (7702) | **GIỮ nguyên hiện trạng** (đã chạy + live-verify); chỉ bổ sung test vector ở G2, không thêm tính năng | Xóa đi là thay đổi lớn rủi ro; không bật thêm bề mặt mới. |
| Simple chain cũ (dapp BLS) | **GIỮ binary/nhánh riêng** | Dapp BLS/proto ở lại chain cũ; node mới secp/eth thuần. |
| Proto `Transaction` | **Không xóa tag cũ**; đánh `reserved`/deprecated, KHÔNG tái dùng số tag; thêm tag mới `raw_envelope` ở G1 trên **cả 3 bản sao** | An toàn tương thích đọc dữ liệu. |

## 4. Giai đoạn (thứ tự đã sắp lại)
Thứ tự: **G0 → G4A → G1 → G2 → G3 → G4B → G5 → G6.** G4A (cổng TCP nhận envelope, không đổi hash/lưu ⇒ không wipe) làm sớm nhất vì độc lập và giảm rủi ro.

### G0 — Phân tích tác động (không đổi code)
- Theo AGENTS.md: codegraph `impact/callers` cho `Transaction.Hash`, `NewTransactionFromEth`, `ToEthTransaction`, `SetEthHashMapblsHash`, `TransactionWithDeviceKey`, `ValidSecpProtoSign`, `calculate_transaction_hash_single` (Rust). Fallback grep nếu codegraph lỗi.
- **Rust (đã khảo sát sơ bộ ở mục 1, cần hoàn tất):** liệt kê mọi chỗ dùng hash làm khóa (`seen` ở `block_sending.rs`, `tx_recovery.rs`, `GLOBAL_TX_CACHE`, `TxPayloadCache`, `SubmitOrderGate`) và xác định đổi hash có làm đổi thứ tự/khử trùng không. Kết quả quyết định thiết kế G1 (mục dưới).
- Quét danh sách RPC `mtn_*`/`eth_*` thật có liên quan gửi tx (sửa tên sai ở bản cũ).
- Baseline: `ethereum/tests` TransactionTests + `ethereum/hive rpc-compat` trên node hiện tại ⇒ danh sách khoảng cách (chưa chạy; cần cài công cụ, chạy cô lập cổng 31xxx, KHÔNG đụng cụm 231/230).
- ADR cho mục 6; user duyệt trước G1.

### G4A — TCP nhận envelope thô (ĐÃ HOÀN THÀNH, xem `plan_tcp_eth_only_ingress.md`)
Đã triển khai: Command `SendRawTransaction(s)`, chặn 3 command proto cũ ở chế độ secp, từ chối pre-EIP-155 tường minh, kiểm tra malleable s (EIP-2), ánh xạ hash cho receipt (`SetEthHashMapblsHash`), build check 4/4 PASS, go test -race PASS.

### G1 — Dạng chuẩn + một hash (consensus-critical, cùng cutover)
Thiết kế đề xuất (do Rust băm proto): thêm trường `bytes raw_envelope` (tag mới) vào proto `Transaction` ở 3 bản sao; **hash = keccak256(raw_envelope)** tính giống hệt ở Go (`Transaction.Hash`) và Rust (`tx_hash.rs`); các trường eth rải rác trở thành **view dẫn xuất** từ envelope (cache sender, intrinsic gas), không phải nguồn sự thật. Rust vẫn decode proto nhưng chỉ cần `raw_envelope` để băm ⇒ thay đổi Rust nhỏ, tập trung.
- Bỏ `SetEthHashMapblsHash`; chỉ số hash → vị trí dùng hash chuẩn; lưu envelope trong block/DB.
- Test: round-trip mọi type; vector `ethereum/tests`; property test `hash == go-ethereum tx.Hash()` với cùng bytes; **test chéo Go↔Rust: cùng input ⇒ cùng hash** (bắt buộc, lỗi ở đây = fork).
- Tất cả validator nâng cấp đồng thời (khử trùng theo hash); chỉ trong cutover có wipe.

### G2 — Admission + thực thi chuẩn Ethereum
- Xác thực chữ ký trên bytes envelope gốc (`ecrecover` batch/cached); EIP-155 chainId bắt buộc (cấm legacy không chainId, tường minh); từ chối `s` cao (EIP-2); nonce tuần tự (queued vs pending); intrinsic gas (đã có); giới hạn kích thước tx/initcode (EIP-3860).
- **Mô hình phí (chốt v1): phí phẳng** theo `MINIMUM_BASE_FEE`, không base fee động (tất định, không cần baseFee trong header consensus). `effectiveGasPrice` định nghĩa rõ cho 1559: `min(maxFeePerGas, flatFee + tip)` hoặc quy ước tương đương — **chốt công thức ở ADR G0 và kiểm bằng test**; không dùng giá trị thay đổi theo thời gian thực/ cục bộ.
- Receipt kiểu EIP-2718: `type`, `effectiveGasPrice`, `cumulativeGasUsed`, `logsBloom`, `contractAddress`.
- Tất định: loại hẳn `time.Now()`/device key khỏi đường thực thi. Test tái lập: cùng chuỗi tx trên 4 validator ⇒ state root giống nhau.

### G3 — Bề mặt RPC `eth_*`
- Sửa lệch phí: `eth_gasPrice`, `eth_maxPriorityFeePerGas`, `eth_feeHistory`, `baseFeePerGas` trong block phải **nhất quán với phí phẳng** (hiện báo 0 trong khi cổng đòi ≥100000) để ví tự ước tính ra giá được chấp nhận.
- `eth_chainId`, `eth_getTransactionCount (pending)`, `eth_estimateGas`, `eth_getBlockBy*`, `eth_getTransactionByHash/Receipt` (hash chuẩn, `type` hợp lệ), `eth_call` (state override), `eth_getLogs`, subscriptions, `eth_sendRawTransaction` với lỗi chuẩn (`nonce too low`, `insufficient funds`, `already known`…).
- Chạy `hive rpc-compat`; mọi test fail phải có lý do ghi nhận (khác biệt chủ đích, ví dụ `eth_getProof`).
- Bỏ mọi RPC gửi tx dùng device key/BLS.

### G4B — Client/SDK
SDK client = thư viện Eth chuẩn; xóa client BLS người dùng khỏi node mới; tài liệu cho dapp (web3j/ethers/viem). Dapp tự chọn thư viện.

### G5 — Gỡ code cũ khỏi node mới
Xóa theo bảng mục 3 (sau khi G1–G4 xanh). Proto: `reserved`, không tái dùng tag. Dọn test/tài liệu/`PROJECT_STRUCTURE.md`.

### G6 — Kiểm chứng + cutover chung
E2E toàn bộ (gate 14, co-attestation 4 validator, cross-chain 26) với tx envelope thuần; hardhat/foundry deploy + chuyển + sự kiện + `cast`/`ethers`/web3j trên cụm cô lập. Đưa vào **runbook cutover chain 991**: một lần wipe cho chain ID, payload proto, định dạng tx. Không phát hành định dạng tx trước cutover.

## 5. Tiêu chí hoàn thành
- Một hash/giao dịch = `keccak(envelope)`, khớp go-ethereum, **Go và Rust tính giống nhau**; không còn ánh xạ hash.
- Ví/tool Eth chuẩn gửi mọi type hỗ trợ không cần SDK riêng; `hive rpc-compat` + `ethereum/tests` TransactionTests: danh sách fail đều có giải trình.
- Không còn đường BLS người dùng trong node mới (grep + test chứng minh).
- State root nhất quán 4 validator dưới tải; `go test -race`, `build_check.sh` sạch (không warning); E2E cũ xanh.

## 6. Quyết định (đã chốt thay user được ủy quyền, 2026-10-06)
1. **Mô hình phí:** phí phẳng v1 (xem G2); EIP-1559 động hoãn (cần baseFee trong header consensus).
2. **Blob 0x03, EIP-7702 0x04:** giữ nguyên hiện trạng, chỉ thêm test.
3. **Roots trong header:** giữ NOMT + header riêng; chấp nhận `eth_getProof` khác; không đóng gói MPT.
4. **Pre-EIP-155:** cấm tường minh.
5. **Tương thích Eth hoàn toàn, không đường proto thay thế** (user xác nhận).
6. **Còn mở duy nhất — cần user:** "derive key" là gì (khóa con theo ứng dụng? khóa công khai kèm tx?). Khi bỏ device key BLS, khái niệm này cần định nghĩa lại hoặc bỏ; **không chặn G0–G2**, chỉ cần trước G4B/G5.

## 7. Chưa kiểm / rủi ro
- Rust: mới khảo sát vị trí dùng hash; chưa xác nhận tác động lên `GLOBAL_TX_CACHE`/`TxPayloadCache`/`SubmitOrderGate` (G0). Đã có lịch sử lỗi liên quan khóa `GLOBAL_TX_CACHE` — đọc memory "Phuong an A" trước khi sửa.
- Hiệu năng: `ecrecover` cho mọi tx ở mempool + exec filter (số cũ 7060 → 6400 tx/s khi bật BLS batch verify); cần đo secp batch.
- Dữ liệu cũ không đọc được bằng định dạng mới ⇒ chỉ làm trong cutover có wipe.
- Tài khoản/hợp đồng đã triển khai theo `creatorPublicKey` BLS (MVM) cần xem lại (đã ghi trong `plan_production_launch` G7).
- Chưa kiểm thử thực tế web3j/ethers trên chain này (có trong G4A/T8).
- Công thức `effectiveGasPrice` với phí phẳng chưa chốt chi tiết (ADR G0).
