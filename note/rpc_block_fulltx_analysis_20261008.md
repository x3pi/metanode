# Báo Cáo Phân Tích Kỹ Thuật: Chuẩn Hoá Đối Tượng Giao Dịch `eth_getBlockByNumber/Hash(fullTx=true)` Theo Chuẩn Ethereum

**Tài liệu tham chiếu:** `note/plan_rpc_block_fulltx_reports_ttl_20261008.md`  
**Ngày lập:** 2026-10-08  
**Tác giả:** Metanode Core Engineering Team  
**Trạng thái:** HOÀN THÀNH PHÂN TÍCH — Chuẩn bị triển khai mã nguồn  

---

## 1. Đặt Vấn Đề (Problem Statement)

Trong quá trình kiểm thử tương thích với các thư viện Web3 tiêu chuẩn (ethers.js v6, viem, web3.py, Foundry `cast`), hệ thống phát hiện sự bất đồng nhất nghiêm trọng giữa hai đường API trả về đối tượng giao dịch:
1. **Truy vấn đơn lẻ (`eth_getTransactionByHash`, `eth_getTransactionByBlockNumberAndIndex`, `eth_getTransactionByBlockHashAndIndex`):**
   - Trả về đối tượng qua struct `RPCTransaction` (`execution/cmd/simple_chain/backend.go:59`), được tạo bởi hàm dùng chung `newCommittedRPCTransaction` (`execution/cmd/simple_chain/rpc_transaction.go:37`).
   - Chứa đầy đủ các trường chuẩn Ethereum theo go-ethereum (EIP-2718, EIP-2930, EIP-1559, EIP-4844, EIP-7702), bao gồm `blockHash`, `blockNumber`, `type`, `maxFeePerGas`, `maxPriorityFeePerGas`, `accessList`, `yParity`, v.v.
2. **Truy vấn khối kèm giao dịch đầy đủ (`eth_getBlockByNumber(fullTx=true)`, `eth_getBlockByHash(fullTx=true)`):**
   - Trả về danh sách giao dịch qua hàm `MarshalBlockToMapWithGas` (`execution/cmd/simple_chain/rpc_block.go:96`).
   - Hàm này tự dựng map thủ công (`txMap := make(map[string]interface{})`, dòng 202-234) với chỉ 14 trường thô, **hoàn toàn thiếu** các trường trọng yếu như `blockNumber`, `blockHash`, `type`, `maxFeePerGas`, `maxPriorityFeePerGas`, `accessList`, `yParity`.
   - Hệ quả: Khi client (ví dụ `ethers.provider.getBlock(n, true)`) phân tích giao dịch, do thiếu trường `type`, client coi toàn bộ giao dịch là Legacy (Type 0) và báo lỗi khi giải mã hoặc không ánh xạ được giao dịch với block chứa nó. Ngoài ra, trong log xác minh Pebble DB trước đây cũng hiển thị `"Target Block: #0"` vì trường `blockNumber` bị thiếu trong block fullTx.

---

## 2. Kiểm Toán Toàn Bộ Các Nơi Trả Đối Tượng Giao Dịch (Caller Sites Audit)

Đã rà soát toàn bộ các tệp RPC trong `execution/cmd/simple_chain/`:

| Tên phương thức RPC | Vị trí mã nguồn | Cơ chế sinh dữ liệu | Tập trường trả về | Đánh giá tương thích |
| :--- | :--- | :--- | :--- | :--- |
| `eth_getTransactionByHash` | `rpc_transaction.go:123` | `newCommittedRPCTransaction` (`:259`) | Đủ 22 trường chuẩn Ethereum | ✅ Chuẩn Geth |
| `eth_getTransactionByBlockNumberAndIndex` | `rpc_block.go:499` | `newCommittedRPCTransaction` (`:559`) | Đủ 22 trường chuẩn Ethereum | ✅ Chuẩn Geth |
| `eth_getTransactionByBlockHashAndIndex` | `rpc_block.go:563` | `newCommittedRPCTransaction` (`:600`) | Đủ 22 trường chuẩn Ethereum | ✅ Chuẩn Geth |
| `eth_getBlockByNumber(fullTx=true)` | `rpc_block.go:252` | `MarshalBlockToMapWithGas` (`:96, 202`) | Map thủ công (14 trường) | ❌ **Lỗi: Thiếu 8 trường chuẩn** |
| `eth_getBlockByHash(fullTx=true)` | `rpc_block.go:415` | `MarshalBlockToMapWithGas` (`:96, 202`) | Map thủ công (14 trường) | ❌ **Lỗi: Thiếu 8 trường chuẩn** |
| `eth_newPendingTransactions(fullTx)` | `rpc_subscription.go:42` | Chỉ notify `ethHash` (dòng 69) | Chưa hỗ trợ streaming fullTx | ℹ️ Stream hash only |
| `GetSystemTransactionsByBlockNumber` | `rpc_block.go:387` | Map giải mã BCS (`decoded`) | System-specific custom fields | ℹ️ Endpoint nội bộ Metanode |

---

## 3. Bảng Đối Chiếu Chi Tiết Từng Trường (Field-by-Field Comparison)

So sánh giữa chuẩn go-ethereum (`RPCTransaction`), hàm `newCommittedRPCTransaction` hiện tại, và hàm `MarshalBlockToMapWithGas` hiện tại:

| Tên trường JSON | Kiểu dữ liệu | Chuẩn Geth | `newCommittedRPCTransaction` | `MarshalBlockToMapWithGas` (Hiện tại) | Trạng thái cần sửa |
| :--- | :--- | :---: | :---: | :---: | :---: |
| `blockHash` | `common.Hash` | Có | Có (`&blockHash`) | **THIẾU** | 🔴 Bổ sung từ `block.Header().Hash()` |
| `blockNumber` | `*hexutil.Big` | Có | Có (`blockNumber`) | **THIẾU** | 🔴 Bổ sung từ `block.Header().BlockNumber()` |
| `from` | `common.Address` | Có | Có (`tx.FromAddress()`) | Có (`tx.FromAddress()`) | 🟢 Khớp |
| `gas` | `hexutil.Uint64` | Có | Có (`tx.MaxGas()`) | Có (`tx.MaxGas()`) | 🟢 Khớp |
| `gasPrice` | `*hexutil.Big` | Có | Có (`tx.EffectiveGasPrice()`) | Có (`tx.MaxGasPrice()`) | 🟡 Sửa thành `EffectiveGasPrice()` |
| `maxFeePerGas` | `*hexutil.Big` | Có (Type 2/3/4) | Có (`tx.GasFeeCap()`) | **THIẾU** | 🔴 Bổ sung từ `tx.GasFeeCap()` |
| `maxPriorityFeePerGas` | `*hexutil.Big` | Có (Type 2/3/4) | Có (`tx.GasTipCap()`) | **THIẾU** | 🔴 Bổ sung từ `tx.GasTipCap()` |
| `maxFeePerBlobGas` | `*hexutil.Big` | Có (Type 3) | Có (`tx.MaxFeePerBlobGas()`) | **THIẾU** | 🔴 Bổ sung từ `tx.MaxFeePerBlobGas()` |
| `hash` | `common.Hash` | Có | Có (`ethHash`) | Có (`ethHash`) | 🟢 Khớp |
| `input` | `hexutil.Bytes` | Có | Có (`tx.CallData().Input()`) | Có (`tx.CallData().Input()`) | 🟢 Khớp |
| `nonce` | `hexutil.Uint64` | Có | Có (`tx.GetNonce()`) | Có (`tx.GetNonce()`) | 🟢 Khớp |
| `to` | `*common.Address` | Có | Có (deployed addr nếu deploy) | Có (`tx.ToAddress()` = zero addr) | 🟡 Sửa deployed contract address |
| `transactionIndex` | `*hexutil.Uint64` | Có | Có (`txIndex`) | Có (từ receipt nếu có) | 🟢 Khớp (chuẩn hoá thành pointer) |
| `value` | `*hexutil.Big` | Có | Có (`tx.Amount()`) | Có (`tx.Amount()`) | 🟢 Khớp |
| `type` | `hexutil.Uint64` | Có | Có (`tx.GetType()`) | **THIẾU** | 🔴 Bổ sung từ `tx.GetType()` |
| `accessList` | `*types.AccessList` | Có (Type 1/2/3/4) | Có (`tx.EthAccessList()`) | **THIẾU** | 🔴 Bổ sung từ `tx.EthAccessList()` |
| `chainId` | `*hexutil.Big` | Có | Có (`tx.GetChainID()`) | Có (`tx.GetChainID()`) | 🟢 Khớp |
| `blobVersionedHashes` | `[]common.Hash` | Có (Type 3) | Có (`tx.BlobVersionedHashes()`) | **THIẾU** | 🔴 Bổ sung từ `tx.BlobVersionedHashes()` |
| `authorizationList` | `[]Authorization` | Có (Type 4) | Có (`tx.EthAuthorizationList()`) | **THIẾU** | 🔴 Bổ sung từ `tx.EthAuthorizationList()` |
| `v` | `*hexutil.Big` | Có | Có (`v`) | Có (`v`) | 🟢 Khớp |
| `r` | `*hexutil.Big` | Có | Có (`r`) | Có (`r`) | 🟢 Khớp |
| `s` | `*hexutil.Big` | Có | Có (`s`) | Có (`s`) | 🟢 Khớp |
| `yParity` | `*hexutil.Uint64` | Có (Type ≥ 1) | Có (`yParity`) | **THIẾU** | 🔴 Bổ sung từ `v` khi Type ≥ 1 |
| **`groupId`** (Custom) | `*hexutil.Uint64` | Không có (Metanode) | Chưa có | Có (`info.groupIndex`) | 🔵 **Bảo toàn trường mở rộng** |

---

## 4. Phân Tích Nguồn Gốc Thiếu Sót & Dữ Liệu `groupId`

1. **Vì sao `blockNumber` và `blockHash` bị thiếu trong `MarshalBlockToMapWithGas`:**
   - Trong quá trình phát triển trước đây, khi lập trình viên triển khai `MarshalBlockToMapWithGas`, họ chỉ ánh xạ các trường lấy trực tiếp từ interface `mt_types.Transaction` mà không truyền thông tin ngữ cảnh của block cha (`block.Header().Hash()`, `block.Header().BlockNumber()`).
   - Đây là sự thiếu sót vô tình (omission), không phải quyết định thiết kế có chủ đích.
2. **Nguồn dữ liệu `groupId` và `transactionIndex`:**
   - Khi một block được xử lý qua pipeline thực thi song song (Block-STM / Native Fast Path), hàm `tx_processor` gán `groupIndex` và `blockTxIndex` vào Receipt tương ứng.
   - Các giá trị này được lưu bền vững vào Merkle trie của Receipt (`block.Header().ReceiptRoot()`).
   - Khi `MarshalBlockToMapWithGas` chạy, nó mở `rcpDb := receipt.NewReceiptsFromRoot(...)` và đọc `rcp.GroupIndex()` cũng như `rcp.TransactionIndex()`.
   - Trường `groupId` được công cụ giám sát nội bộ `block_hash_checker` (`deploy/ansible/monitors/block_hash_checker/main.go:1304`) sử dụng để đối chiếu tính nhất quán giữa các validator. Do đó, việc bảo toàn trường `groupId` là **bắt buộc**.

---

## 5. Phương Án Kỹ Thuật Hợp Nhất (Unified Architecture Solution)

Để loại bỏ hoàn toàn sự trôi dạt (drift) dữ liệu giữa các API RPC và tuân thủ nguyên tắc Single Source of Truth:

1. **Mở rộng struct `RPCTransaction` (`execution/cmd/simple_chain/backend.go:59`):**
   - Thêm trường:
     ```go
     GroupID *hexutil.Uint64 `json:"groupId,omitempty"`
     ```
   - Trường này dùng tag `omitempty`, chỉ xuất hiện trong JSON khi giá trị khác `nil`. Các client Web3 chuẩn (ethers, viem, web3.py) tự động bỏ qua các trường không xác định trong JSON spec.
2. **Cập nhật hàm `newCommittedRPCTransaction` (`execution/cmd/simple_chain/rpc_transaction.go:37`):**
   - Thêm tham số hoặc hàm bao `newCommittedRPCTransactionWithGroup(tx, blockHash, blockNumber, txIndex, ethHash, groupIndex *uint64) *RPCTransaction`.
   - Đảm bảo logic tính `ethHash`, `to` (contract deployment address), `gasPrice` (`EffectiveGasPrice()`), `yParity`, và các trường EIP-1559/EIP-2930/EIP-4844/EIP-7702 được áp dụng thống nhất cho mọi nơi gọi.
3. **Refactor `MarshalBlockToMapWithGas` (`execution/cmd/simple_chain/rpc_block.go:96`):**
   - Thay thế toàn bộ đoạn code tự tạo `txMap` (dòng 202-234) bằng việc gọi trực tiếp `newCommittedRPCTransactionWithGroup`.
   - `blockHash`: lấy từ `block.Header().Hash()`.
   - `blockNumber`: lấy từ `block.Header().BlockNumber()`.
   - `txIndex`: lấy từ `info.transactionIndex` (hoặc index vòng lặp `i` làm fallback an toàn).
   - `groupId`: lấy từ `info.groupIndex`.
   - Đưa trực tiếp `*RPCTransaction` vào mảng `transactions = append(transactions, rpcTx)`.
4. **Hiệu năng & Phức tạp tính toán ($O(1)$ Overhead):**
   - Thông tin `blockHash` và `blockNumber` đã có sẵn trong bộ nhớ của biến `block`.
   - Việc gọi `newCommittedRPCTransactionWithGroup` chỉ là các phép toán trên struct trong RAM, không phát sinh thêm bất kỳ truy vấn I/O hay đọc đĩa nào.
   - Không làm thay đổi hành vi của `fullTx = false` (vẫn chỉ trả danh sách chuỗi hex hash).
