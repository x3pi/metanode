# Kế hoạch: node thực thi tương thích Ethereum đầy đủ (EIP-2718), bỏ chữ ký BLS của người dùng

Trạng thái: **ĐỀ XUẤT KIẾN TRÚC + KẾ HOẠCH** (chưa triển khai). Viết 2026-10-06 sau khi đọc code hiện tại; mọi nhận định "hiện trạng" có dẫn nguồn, phần còn chưa kiểm được ghi rõ ở mục 7.

## 1. Hiện trạng (đã đọc code)
| Điểm | Thực tế trong repo |
|---|---|
| Giải mã envelope | `eth_sendRawTransaction` dùng `types.Transaction.UnmarshalBinary` của go-ethereum v1.15.11 (không fork) ⇒ chấp nhận đúng các type 0x00–0x04 (`rpc_transaction.go`, `pkg/transaction/transaction.go: NewTransactionFromEth`). Type lạ ⇒ `unsupported Ethereum transaction type`. |
| Biểu diễn nội bộ | proto `Transaction` (`pkg/proto/transaction.proto`) với `Type uint64` + các trường eth rải rác (`GasTipCap/GasFeeCap/AccessList/BlobVersionedHashes/AuthorizationList`) cạnh trường BLS (`Sign`, `LastDeviceKey`, `NewDeviceKey`, `MaxTimeUse`). |
| **Hai hash cho một giao dịch** | `Transaction.Hash()` là hash proto nội bộ (`TransactionHashData`), KHÔNG phải `keccak(envelope)`; RPC phải ánh xạ `SetEthHashMapblsHash(ethHash → internalHash)` và dò ngược ở mọi chỗ trả hash (`rpc_block.go`, `rpc_subscription.go`, `GetTransactionByHash`). Đây là nguồn phức tạp & lỗi lớn nhất của tương thích. |
| Calldata bị bọc | `Data` được bọc thêm `CallData`/`DeployData` proto (`ToEthTransaction` phải gỡ ra). Input thật của Ethereum không phải bytes lưu trong chain. |
| Re-sign BLS | `buildMetaTxFromEthTx` (`eth_tx_converter.go`) tạo device key bằng `time.Now().Unix()` (không tất định), ký lại bằng khóa BLS gateway (`GatewayBLSKey`) trừ khi `tx_signature_mode=secp`. |
| Chế độ secp | `tx_signature_mode="secp"` + type `0xFF` qua TCP (không phải type EIP-2718 hợp lệ, ngoài dải 0x00–0x7f) — mới là lớp vá trên mô hình cũ. |
| Mở rộng type | Không thể thêm type mới qua `eth_sendRawTransaction` nếu không fork go-ethereum (switch `decodeTyped` cố định). |

## 2. Nguyên tắc kiến trúc đích
1. **Một dạng chuẩn duy nhất: bytes envelope EIP-2718.** `hash = keccak256(envelope)` (đúng định nghĩa Ethereum) — một hash, không ánh xạ. Khối lưu envelope thô; người gửi suy ra bằng `ecrecover` (cache), không lưu/ tin `FromAddress` do client cung cấp.
2. **Người dùng chỉ ký secp256k1.** BLS chỉ còn cho danh tính node/validator/cụm (attestation, cross-chain, committee) — không bao giờ nằm trên đường giao dịch người dùng.
3. **Tính năng riêng của chain đi qua cơ chế Ethereum sẵn có, không qua type mới:** precompile / contract hệ thống / calldata có selector (ví dụ đăng ký tài khoản parent, derive key). Ví Ethereum ký được, công cụ (ethers/viem/foundry/hardhat/MetaMask) dùng nguyên.
4. **Type tự định nghĩa chỉ khi bất khả kháng** (và khi đó phải fork go-ethereum có kiểm soát, chọn mã type không đụng Optimism 0x7E, Arbitrum 0x64–0x6A, Celo 0x7B/0x7C, nằm trong 0x00–0x7f). Không làm trong v1.
5. **Tất định tuyệt đối:** không `time.Now()`, không map iteration, không trạng thái cục bộ trong đường dẫn từ envelope → thực thi. Thà PENDING chứ không fork.

## 3. Bỏ cái cũ hay giữ? (khuyến nghị)
| Thành phần | Quyết định | Lý do |
|---|---|---|
| Chữ ký BLS người dùng, `Sign`, device key (`LastDeviceKey/NewDeviceKey`), `TransactionWithDeviceKey`, `SendRawTransactionWithDeviceKey`, `mtn_sendRawTransactionWithDeviceKey`, `RegisterBlsKeyWithSignature`, `GatewayBLSKey` | **BỎ khỏi node thực thi mới** | Mâu thuẫn trực tiếp với "không ký BLS nữa"; là nguồn không tất định (`time.Now`) và hai hash. |
| Ánh xạ `ethHash ↔ blsHash`, hàm `SetEthHashMapblsHash`, dò ngược ở RPC | **BỎ** | Biến mất khi hash = keccak(envelope). |
| Bọc `CallData`/`DeployData` | **BỎ**; lưu `input` thô | Tương thích gas/`eth_getTransactionByHash.input`/tracing. |
| Type `0xFF` + `SignSecpProto`/`ValidSecpProtoSign` | **BỎ** (sau khi TCP chuyển sang gửi envelope thô) | Ngoài dải EIP-2718, một cơ chế song song vô ích khi đã có envelope chuẩn. |
| `tx_signature_mode` (`bls_legacy`/`secp`) | **Thay bằng cờ định dạng genesis cố định** (`tx_format=eip2718`); `bls_legacy` chỉ còn ở binary simple-chain cũ | Tránh node ở hai chế độ; cutover chain 991 vốn đã đòi wipe + redeploy ⇒ **gộp thay đổi này vào cùng lần cutover** để khỏi wipe hai lần. |
| Account gate (`parent_registered`), registry/relay/attestation f+1, chain ID chung 991 | **GIỮ** | Độc lập với định dạng tx; gate chỉ cần `from` (ecrecover). |
| BLS cho validator/cụm/cross-chain/committee | **GIỮ** | Không phải chữ ký người dùng. |
| Các type 0x01/0x02/0x03/0x04 hiện có | **GIỮ**; 0x03 (blob) và 0x04 (7702) đã chạy — cần quyết định có bật thật trên mạng này không (mục 6) | |
| Simple chain cũ (dapp BLS) | **GIỮ nguyên binary/nhánh riêng**, không nhồi vào node mới | Đã quyết: dapp cũ chạy chain cũ; node mới secp/eth thuần. Tách nhánh để xóa code BLS khỏi node mới. |
| Proto `Transaction` | **Không xóa tag cũ**; đánh `reserved`/deprecated, KHÔNG tái dùng số tag (bản sao proto ở `consensus/`, `cmd/rpc/` đã từng đụng tag) | An toàn tương thích đọc dữ liệu/ bản sao khác. |

## 4. Giai đoạn
### G0 — Phân tích tác động + chốt quyết định (không đổi code)
- Theo AGENTS.md: dùng codegraph (`codegraph_impact`/`callers`) cho `Transaction.Hash`, `NewTransactionFromEth`, `ToEthTransaction`, `SetEthHashMapblsHash`, `TransactionWithDeviceKey`, `ValidSecpProtoSign`; liệt kê MỌI nơi tx đi qua ranh giới Go↔Rust (FFI): Rust có đọc/ băm byte giao dịch không (cache dedup `GLOBAL_TX_CACHE`, `TxPayloadCache`, `SubmitOrderGate`)? Nếu Rust xem tx là byte mờ thì đổi định dạng không cần đổi FFI.
- Chạy baseline tương thích: bộ `ethereum/tests` **TransactionTests** (vector giải mã/hợp lệ tx), và `ethereum/hive` **rpc-compat** trên node hiện tại ⇒ danh sách khoảng cách (chưa chạy; cần cài công cụ, chạy cô lập 31xxx).
- Ghi ADR cho các quyết định mở ở mục 6; user phê duyệt trước G1.
### G1 — Dạng chuẩn + một hash
- Mã hóa/giải mã tx = envelope thô; `tx.Hash() = keccak(envelope)`; lưu envelope trong block/DB; chỉ số hash → vị trí dùng hash này. Bỏ `SetEthHashMapblsHash`.
- Bộ nhớ: struct nội bộ chỉ là **view** dựng từ envelope (cache sender, intrinsic gas), không phải định dạng lưu/mạng.
- Test: round-trip mọi type; vector `ethereum/tests`; hash bằng go-ethereum `tx.Hash()` với cùng bytes (property test).
### G2 — Admission + thực thi chuẩn Ethereum
- Admission ở mempool và exec filter: `ecrecover` (batch/cached), EIP-155 chainId bắt buộc, từ chối chữ ký malleable (s > n/2 theo EIP-2), nonce tuần tự (queued vs pending), intrinsic gas (đã có), kích thước tx/initcode (EIP-3860), kiểm phí: `maxFeePerGas ≥ baseFee`, `tip ≤ fee` (quyết định mô hình phí ở mục 6), blob/7702 theo luật EIP.
- Receipt kiểu EIP-2718: `type`, `effectiveGasPrice`, `cumulativeGasUsed`, `logsBloom`, `contractAddress`; `receiptsRoot` theo quy ước đã chọn.
- Tất định: loại hẳn `time.Now()`/device key khỏi đường thực thi. Test tái lập: chạy cùng chuỗi tx trên 4 validator ⇒ state root giống nhau.
### G3 — Bề mặt RPC `eth_*`
- Hoàn thiện để ethers/viem/hardhat/foundry/MetaMask chạy không cần patch: `eth_chainId`, `eth_getTransactionCount (pending)`, `eth_estimateGas`, `eth_gasPrice/maxPriorityFeePerGas/feeHistory`, `eth_getBlockBy*` (có `baseFeePerGas`, `transactions` đúng loại), `eth_getTransactionByHash/Receipt` (hash chuẩn, `type` hợp lệ), `eth_call` (state override), `eth_getLogs`, subscriptions, `eth_sendRawTransaction` lỗi chuẩn (mã lỗi JSON-RPC như go-ethereum: `nonce too low`, `insufficient funds`, `already known`…).
- Chạy `hive rpc-compat`; mục tiêu: các test còn fail đều có lý do được ghi nhận (khác biệt chủ đích, ví dụ không có Merkle-Patricia state root ⇒ `eth_getProof` khác).
- `mtn_*` chỉ giữ các API hạ tầng/quản trị; bỏ mọi `mtn_send*` dành cho người dùng.
### G4 — TCP/client
- TCP nhận **envelope thô** (một đường dẫn duy nhất); bỏ type 0xFF. SDK client = thư viện Ethereum chuẩn; xóa client BLS cho người dùng.
### G5 — Gỡ bỏ code cũ khỏi node mới
- Xóa theo bảng mục 3 (sau khi G1–G4 xanh): BLS user flow, device key, ánh xạ hash, bọc CallData, 0xFF, `tx_signature_mode` ở node mới. Proto: `reserved`, không tái dùng tag. Dọn test/ tài liệu/ `PROJECT_STRUCTURE.md`.
### G6 — Kiểm chứng + cutover chung
- Chạy lại toàn bộ E2E (gate 14, co-attestation 4 validator, cross-chain 26) với giao dịch envelope thuần; thêm bộ tương thích công cụ thật (hardhat/foundry deploy + chuyển + sự kiện + `cast`/`ethers` trên cụm cô lập).
- Đưa vào **runbook cutover chain 991** (đã có) — một lần wipe cho: chain ID, payload proto, định dạng tx. Không phát hành định dạng tx trước cutover.

## 5. Tiêu chí hoàn thành
- Một hash/giao dịch, bằng `keccak(envelope)`; không còn ánh xạ hash.
- Ví/tool Ethereum chuẩn gửi mọi type được hỗ trợ không cần SDK riêng; `hive rpc-compat` + `ethereum/tests` TransactionTests: danh sách fail đều có giải trình.
- Không còn đường BLS của người dùng trong node mới (grep + test chứng minh `mtn_send*`/device key bị gỡ).
- State root nhất quán 4 validator dưới tải; `go test -race`, `build_check.sh` sạch; E2E cũ vẫn xanh.

## 6. Quyết định mở (cần user)
1. **Mô hình phí:** base fee EIP-1559 động hay phí cố định (`MaxGasPrice` hiện tại)? Ảnh hưởng `feeHistory`, ví, và tính tất định.
2. **Blob (0x03) và EIP-7702 (0x04):** đã hỗ trợ, có bật trên mạng thật? Mỗi type mở thêm bề mặt tấn công/ chi phí kiểm thử.
3. **`receiptsRoot`/`transactionsRoot`/`stateRoot` trong header:** giữ NOMT + header riêng (khuyến nghị, chấp nhận `eth_getProof` khác) hay đóng gói MPT? MPT phá thiết kế hiện tại và hiệu năng.
4. **Tx không bảo vệ replay (pre-EIP-155)** có cho phép không (khuyến nghị: cấm).
5. **"Derive key"** là gì chính xác (khóa con theo ứng dụng? khóa công khai kèm tx?) để chọn precompile/calldata (mục 2.3) — chưa đủ thông tin để thiết kế trường.

## 7. Chưa kiểm / rủi ro
- Chưa đọc hết phía Rust (consensus) về cách xử lý byte tx; đổi hash có thể ảnh hưởng cache dedup/ thứ tự (`SubmitOrderGate`) — bắt buộc G0.
- Hiệu năng: ecrecover cho mọi tx tại mempool + exec filter (số liệu cũ: 7060 → 6400 tx/s khi bật BLS batch verify); cần đo secp batch.
- Dữ liệu cũ không đọc được bằng định dạng mới ⇒ chỉ làm trong cutover có wipe; nếu cần giữ lịch sử, phải có đường đọc dữ liệu cũ (không khuyến nghị).
- Tài khoản/hợp đồng đã triển khai theo `creatorPublicKey` BLS (MVM) cần xem xét lại (đã ghi trong `plan_production_launch` G7).
