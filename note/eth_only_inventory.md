# Kiểm kê giao dịch (W1.0 Inventory): Phân loại Producer & Tham chiếu Legacy

Ngày lập: 2026-10-06  
Dựa trên commit: `affc5580` (sau mốc `legacy-simple-chain-bls-v1` tại `a25cb135`)  
Tài liệu tham chiếu: `note/plan_eth_only_node_completion.md`, `AGENTS.md`, `PROJECT_STRUCTURE.md`

---

## 1. Bảng phân loại Transaction Producers (Hệ thống vs Người dùng)

Quy tắc tối thượng: **Giao dịch hệ thống của node (BLS identity) KHÔNG ĐƯỢC LÀM HỎNG.** Chỉ cổng công khai của người dùng (TCP/RPC) bị khóa về EIP-2718 secp256k1 thuần.

| STT | Tên Producer / Nơi tạo | Loại | Khóa ký / Danh tính | Đường vào thực thi | Quyết định xử lý |
|:---:|:---|:---:|:---:|:---|:---|
| 1 | **Rollup System Events** (`app.go:580,664`): `sendWorker`, `recvWorker`, `reclaimWorker`, `regWorker` | Hệ thống (Node) | BLS node identity (`attestKey.PrivateKey()`) gửi đến `RollupSystemAddress` | Nội bộ: gọi trực tiếp `app.transactionProcessor.AddTransactionToPool(tx)` | **GIỮ NGUYÊN 100%**. Được xác thực bởi `isNodeBLSIdentity(tx, as)` trong `validation.go` & `rollup_system_handler.go`. |
| 2 | **Root-Anchor Submitter** (`pkg/cross_chain/rootanchor/client.go:279,412`) | Hệ thống (Cross-chain) | ECDSA secp256k1 (envelope EIP-1559/EIP-2718) | Gửi qua RPC chuẩn `eth_sendRawTransaction` | **GIỮ NGUYÊN 100%**. Đã là chuẩn Ethereum envelope. |
| 3 | **Committee & Message Attestation Workers** (`pkg/blockchain/tx_processor/*_worker.go`) | Hệ thống (Relayer) | ECDSA secp256k1 trên Parent Chain | Gửi transaction tới gateway contract trên Parent Chain | **GIỮ NGUYÊN 100%**. Không liên quan đến ingress của node con. |
| 4 | **TCP SendRawTransaction** (`processor/transaction_processor.go:708`) | Người dùng | ECDSA secp256k1 (envelope EIP-2718: Legacy, EIP-2930, EIP-1559, EIP-4844) | TCP command `SendRawTransaction` | **GIỮ & HOÀN THIỆN (W2)**: Chuẩn hóa converter dùng chung, giới hạn envelope, mã lỗi rõ ràng. |
| 5 | **TCP SendRawTransactions** (`processor/transaction_processor.go:738`) | Người dùng | ECDSA secp256k1 (batch envelopes RLP `[][]byte`) | TCP command `SendRawTransactions` | **GIỮ & HOÀN THIỆN (W2)**: Báo lỗi từng tx trong batch, trả danh sách hash đã nhận. |
| 6 | **TCP SendTransaction** (`processor/transaction_processor.go:360`) | Người dùng (Legacy) | BLS user signature / Proto `types.Transaction` | TCP command `SendTransaction` | **XÓA (W1)**: Đã bị chặn trong secp mode; gỡ bỏ hoàn toàn khỏi handler, route, command. |
| 7 | **TCP SendTransactions** (`processor/transaction_processor.go:512`) | Người dùng (Legacy) | BLS user signature / Proto batch | TCP command `SendTransactions` | **XÓA (W1)**: Đã bị chặn trong secp mode; gỡ bỏ hoàn toàn. |
| 8 | **TCP SendTransactionWithDeviceKey** (`processor/transaction_processor.go:415`) | Người dùng (Legacy) | BLS user signature + DeviceKey proto | TCP command `SendTransactionWithDeviceKey` | **XÓA (W1)**: Đã bị chặn trong secp mode; gỡ bỏ hoàn toàn. |
| 9 | **RPC eth_sendRawTransaction (1 tham số)** (`rpc_transaction.go:630`) | Người dùng | ECDSA secp256k1 (hex envelope EIP-2718) | HTTP/WS JSON-RPC | **GIỮ & TỐI ƯU (W3)**: Dùng chung converter canonical với TCP; bỏ wrap BLS/device key; trả lỗi chuẩn geth. |
| 10 | **RPC eth_sendRawTransaction (3 tham số)** (`rpc_transaction.go:291`) | Người dùng (Legacy) | `(input, inputEth, pubKeyBls)` nhận proto trực tiếp | HTTP JSON-RPC | **XÓA (W1/W3)**: Vi phạm chuẩn Ethereum. |
| 11 | **RPC eth_sendTransaction** (`rpc_transaction.go:266`) | Người dùng (Legacy) | Nhận proto trong `Data` | HTTP JSON-RPC | **XÓA (W1/W3)**: Không làm gì, không chuẩn. |
| 12 | **RPC eth_getSendRawTransaction** (`rpc_transaction.go:477`) | Người dùng (Legacy) | Dựng tx proto với device key rỗng | HTTP JSON-RPC | **XÓA (W1/W3)**. |
| 13 | **RPC eth_sendRawTransactionWithDeviceKey** (`mtn_api.go:204`) | Người dùng (Legacy) | DeviceKey proto | HTTP JSON-RPC | **XÓA (W1/W3)**. |
| 14 | **RPC mtn_registerBlsKeyWithSignature & mtn_getDeviceKey** (`mtn_api.go`) | Người dùng (Legacy) | Đăng ký khóa BLS & truy vấn DeviceKey | HTTP JSON-RPC | **XÓA (W1/W3)**. |
| 15 | **Gateway cũ (`execution/cmd/rpc`) & `cmd/rpc-client`** | Cầu nối cũ (Legacy) | Re-sign BLS, PKS (kho khóa BLS), top-up native coin | Standalone gateway binary | **GỠ BỎ (W5)**: Dapp kết nối trực tiếp RPC của node `simple_chain`. |

---

## 2. Kiểm kê các thành phần lõi cần dọn dẹp (Core & Types)

1. **Nhánh Type `0xFF` (`SignSecpProto`, `ValidSecpProtoSign`):**
   - Vị trí: `execution/pkg/transaction/transaction.go:1212`, `execution/pkg/blockchain/tx_processor/tx_validator_pool_core.go:258`, `validation.go:240-270`.
   - Hành vi: Trước đây dùng để bọc proto tx với chữ ký secp qua type `0xFF`. Trong mô hình EIP-2718 thuần, tx người dùng luôn là envelope chuẩn (type 0x00, 0x01, 0x02, 0x03). Type `0xFF` cho user là mã chết cần gỡ bỏ.
   - Lưu ý an toàn: Giữ nguyên logic cho `isNodeBLSIdentity` kiểm tra giao dịch hệ thống đến `RollupSystemAddress`.

2. **Cờ `TxSignatureMode` & `validateTxSignatureMode`:**
   - Vị trí: `execution/cmd/simple_chain/config/config.go`, `execution/pkg/blockchain/tx_processor/signature_enforcement.go`.
   - Hành vi mới: Node luôn là Ethereum-only (secp). Cấu hình cũ có `"tx_signature_mode": "secp"` tiếp tục hoạt động trong suốt; nếu gặp cấu hình `"bls_legacy"` thì cảnh báo hoặc từ chối rõ ràng vì nhánh code BLS người dùng đã được lưu giữ ở tag `legacy-simple-chain-bls-v1`.

3. **`GatewayBLSKey` và `blsKeyStore` trong RPC:**
   - Vị trí: `execution/cmd/simple_chain/rpc_transaction.go:658-665, 716-732`.
   - Hành vi mới: `eth_sendRawTransaction` nhận envelope EIP-2718 trực tiếp, giải mã qua converter canonical và đẩy thẳng vào mempool, không re-sign hay gán bất kỳ khóa BLS nào.

4. **Kiểm tra tham chiếu deploy/ansible/systemd:**
   - Trong `deploy/ansible_clusters/roles/exec_cluster/templates/exec_config.json.j2`: `"tx_signature_mode": "secp"`.
   - Không còn systemd service nào phụ thuộc vào binary `rpc-client` độc lập; các cụm devnet và Ansible đều chạy trực tiếp binary `simple_chain`.

---

## 3. Lộ trình triển khai tiếp theo
- **W2**: Hoàn thiện TCP (Một converter canonical dùng chung cho cả TCP & RPC, batch reporting chi tiết từng tx, giới hạn envelope/batch tường minh, mã lỗi tường minh).
- **W3**: Hoàn thiện RPC (Unify `eth_sendRawTransaction`, chuẩn hóa mã lỗi geth, đồng bộ phí phẳng `MINIMUM_BASE_FEE`, sửa trường block/receipt `gasLimit`, `cumulativeGasUsed`, `logsBloom`).
- **W1**: Xóa bỏ mã chết legacy (3 lệnh TCP cũ, RPC methods legacy, nhánh `0xFF` user, wrap BLS/device key).
- **W5/W6**: Chạy E2E test thực tế trên cluster với giao dịch EIP-1559, kiểm tra receipt, state root.
