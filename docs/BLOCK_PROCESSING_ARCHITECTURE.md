# Kiến Trúc Xử Lý Block Sau Consensus

> **Last updated:** 2026-05-30  
> **Scope:** Go Execution Layer — từ khi nhận commit từ Rust Consensus đến khi tạo block final

---

## 1. Tổng Quan Pipeline

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                         RUST CONSENSUS LAYER                                │
│  DAG → Commit → ExecutableBlock(txs, GEI, epoch, timestamp, leader)        │
└──────────────────────┬───────────────────────────────────────────────────────┘
                       │ FFI / channel (dataChan)
                       ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  processRustEpochData (block_processor_network.go)                          │
│  ├─ GEI ordering: đảm bảo sequential theo GlobalExecIndex                  │
│  ├─ BATCH-DRAIN: gom empty commits liên tiếp (5ms window)                  │
│  │   └─ ⚠️ Break ngay khi gặp epoch boundary (FORK-SAFETY FIX May 2026)   │
│  └─ processSingleEpochData: xử lý từng commit                             │
└──────────────────────┬───────────────────────────────────────────────────────┘
                       │
                       ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  ProcessTransactions (tx_validator_pool_core.go)                            │
│  ├─ Step 1: Virtual Execution (dry-run) → thu thập RelatedAddresses        │
│  ├─ Step 2: Union-Find Grouping → tách TX thành nhóm độc lập               │
│  ├─ Step 3: Parallel Group Execution (1 goroutine/group)                    │
│  ├─ Step 4: Merge Results (deterministic order theo group index)            │
│  └─ Step 5: IntermediateRoot (AccountDB ∥ StakeDB)                         │
└──────────────────────┬───────────────────────────────────────────────────────┘
                       │
                       ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  createBlockFromResults (block_processor_processing.go)                     │
│  └─ Block(header: hash, parentHash, stateRoot, epoch, GEI, receiptsRoot)   │
└──────────────────────┬───────────────────────────────────────────────────────┘
                       │
                       ▼
               commitChannel (async persist to DB)
```

**Nguyên tắc cốt lõi:** Mỗi commit từ Rust = chính xác 1 block trong Go. Thứ tự block được bảo đảm bởi `GlobalExecIndex` (GEI) — tất cả nodes xử lý cùng GEI → cùng block → cùng hash.

---

## 2. Phân Loại Giao Dịch

### 2.1 Bảng tổng hợp

| Loại                  | ToAddress               | Cần EVM? | RelatedAddresses              | Xử lý bởi                            |
|-----------------------|-------------------------|----------|-------------------------------|---------------------------------------|
| Native Transfer       | User address            | ❌       | `{from, to}`                  | `ProcessNonceOnly` (nonce + balance)  |
| Account Setting       | `ACCOUNT_SETTING_ADDR`  | ❌       | `{from, to}`                  | Native handler (set BLS key, type)    |
| BLS Registration      | `VALIDATOR_CONTRACT`    | ❌       | `{from}` (contract filtered)  | `ValidatorHandler.HandleTransaction`  |
| EVM Contract Call     | Contract address        | ✅       | `{from, to, storage_slots…}`  | `VmProcessor.ExecuteTransaction`      |
| EVM Deploy            | `0x0` (empty)           | ✅       | `{from, predicted_addr}`      | `VmProcessor.ExecuteTransactionDeploy`|
| Cross-Chain batchSubmit| `0x1002`               | ✅/❌    | `{from, to, targets…}`        | Vote accumulation → EVM khi đủ 2/3   |
| Xapian Storage        | Xapian contract         | ✅       | `{from, contract, sub_contracts…}` | C++ MVM + Xapian DB            |

---

## 3. Cơ Chế Phân Nhóm (Grouping) & Đảm Bảo An Toàn Xử Lý Song Song

Hệ thống sử dụng **Union-Find Grouping** dựa trên `RelatedAddresses` để quyết định giao dịch nào chạy song song, giao dịch nào chạy tuần tự. 

**Quy tắc bất biến:**
- **Chung địa chỉ (chia sẻ trạng thái)** → Cùng nhóm → Chạy TUẦN TỰ (Sequential).
- **Không chung địa chỉ (độc lập trạng thái)** → Khác nhóm → Chạy SONG SONG (Parallel).

Dưới đây là phân tích chi tiết cách chia nhóm và tính an toàn cho từng loại giao dịch cụ thể:

### 3.1. Thiết lập tài khoản (Account Setting) & Đăng ký BLS (Validator)

Đây là các giao dịch gọi đến các hợp đồng hệ thống (`ACCOUNT_SETTING_ADDR`, `VALIDATOR_CONTRACT`).

- **Đặc điểm:** Các hợp đồng này KHÔNG lưu trạng thái chung (global state). Giao dịch gửi đến đây thực chất chỉ cập nhật metadata trên chính tài khoản của người gửi (ví dụ: gán BLS public key vào tài khoản của sender, đổi type của sender).
- **Cách chia nhóm:** 
  Hệ thống sử dụng cơ chế **Native Contract Address Filtering**:
  ```go
  // Các địa chỉ này bị loại khỏi mảng dùng để gom nhóm
  nativeParallelAddrs := map[common.Address]struct{}{
      accountSettingAddr:         {},
      VALIDATOR_CONTRACT_ADDRESS: {},
  }
  ```
  **Trường hợp 1: KHÁC SENDER (Song song)**
  Nếu User A và User B cùng gửi đăng ký BLS:
  - TX1 (User A): Địa chỉ hợp đồng bị filter, `RelatedAddresses` = `[A]`
  - TX2 (User B): Địa chỉ hợp đồng bị filter, `RelatedAddresses` = `[B]`
  - Giao nhau: `[A] ∩ [B] = {}` (rỗng) ➔ **KHÁC NHÓM** ➔ **Chạy song song an toàn**. (Nếu không có filter này, Union-Find sẽ gom cả hai vào cùng nhóm vì chung địa chỉ `VALIDATOR_CONTRACT`, gây thắt cổ chai hiệu năng).

  **Trường hợp 2: CÙNG SENDER (Tuần tự - Tránh xung đột tuyệt đối)**
  Nếu User A gửi 2 giao dịch: TX1 (đăng ký BLS) và TX2 (gọi Smart Contract hoặc lại đăng ký BLS):
  - TX1 (User A): `RelatedAddresses` = `[A]`
  - TX2 (User A): `RelatedAddresses` bao gồm `[A, ...]`
  - Giao nhau: Cả hai đều chứa địa chỉ của người gửi `[A]` ➔ **CÙNG NHÓM** ➔ **Chạy tuần tự**.
- **Tính an toàn (Parallel Safety): ✅ RẤT AN TOÀN**. 
  Bởi vì địa chỉ người gửi (`FromAddress`) LUÔN LUÔN được giữ lại trong `RelatedAddresses`, mọi giao dịch của CÙNG MỘT SENDER chắc chắn sẽ bị gom vào cùng một nhóm. Điều này đảm bảo chúng luôn được thực thi tuần tự theo đúng thứ tự Nonce, loại bỏ hoàn toàn rủi ro xung đột trạng thái (race condition) trên chính tài khoản đó.

### 3.2. Gọi Xapian Contract (Full-text Search DB)

Xapian contract là các hợp đồng thông minh lưu trữ dữ liệu off-chain vào cơ sở dữ liệu Xapian thông qua C++ MVM.

- **Đặc điểm:** Mỗi hợp đồng Xapian tương ứng với một instance database riêng biệt dưới tầng C++.
- **Cách chia nhóm:** Thu thập thông qua **Virtual Execution (Dry-run)**.
  - User A gọi Xapian_Contract_1 ➔ `RelatedAddresses = [A, Xapian_Contract_1]`
  - User B gọi Xapian_Contract_2 ➔ `RelatedAddresses = [B, Xapian_Contract_2]`
  - Giao nhau = `{}` ➔ **KHÁC NHÓM** ➔ **Chạy song song**.
  - Trái lại, nếu User C cũng gọi Xapian_Contract_1 ➔ `[C, Xapian_Contract_1] ∩ [A, Xapian_Contract_1] = {Xapian_Contract_1}` ➔ **CÙNG NHÓM** ➔ **Chạy tuần tự**.
- **Tính an toàn (Parallel Safety): ✅ RẤT AN TOÀN**.
  - Các lệnh gọi đến **cùng** một Xapian contract bị ép chạy tuần tự ➔ Tránh việc 2 luồng cùng ghi vào 1 database Xapian ở tầng C++, loại bỏ data corruption.
  - Các lệnh gọi đến **khác** Xapian contract chạy song song ➔ Tận dụng multi-core vì mỗi contract dùng một file database riêng.

### 3.3. Triển khai Hợp đồng (Deploy Contract)

- **Đặc điểm:** Tạo ra một tài khoản hợp đồng mới.
- **Cách chia nhóm:** Địa chỉ hợp đồng được tính toán trước (predicted address) dựa vào sender và nonce.
  - `RelatedAddresses = [Sender, Predicted_Contract_Address]`
- **Tính an toàn (Parallel Safety): ✅ AN TOÀN**.
  - Nếu User A deploy 2 hợp đồng liên tiếp ➔ Chung địa chỉ A ➔ Tuần tự.
  - User A và User B cùng deploy hợp đồng ➔ Địa chỉ A khác B, predicted address cũng khác nhau hoàn toàn ➔ Song song. Hai hợp đồng mới không thể xung đột không gian lưu trữ.

### 3.4. Chuyển Native Coin (Transfer)

- **Đặc điểm:** Cập nhật số dư (balance) của 2 tài khoản.
- **Cách chia nhóm:** 
  - `RelatedAddresses = [Sender, Receiver]`
  - A chuyển cho B: `[A, B]`
  - C chuyển cho D: `[C, D]`
  - Giao nhau = `{}` ➔ **Khác nhóm** ➔ Song song.
  - Nếu A chuyển cho B và C chuyển cho B: `[A, B] ∩ [C, B] = {B}` ➔ **Cùng nhóm** ➔ Tuần tự.
- **Tính an toàn (Parallel Safety): ✅ AN TOÀN**.
  Không bao giờ có chuyện 2 goroutine cùng lúc cố gắng cộng/trừ số dư của tài khoản B. Mọi giao dịch dính dáng tới B đều xếp hàng xử lý từng cái một.

### 3.5. Gọi Hợp đồng thông minh (Smart Contract Call)

- **Đặc điểm:** Tương tác phức tạp, có thể gọi chéo (cross-contract call) sang nhiều hợp đồng khác nhau (A gọi B, B gọi C).
- **Cách chia nhóm:** 
  Dựa hoàn toàn vào **Virtual Execution (Dry-run)** chạy trước khi gom nhóm:
  - Máy ảo EVM chạy giả lập giao dịch. Mọi thao tác đọc/ghi storage (SLOAD, SSTORE) hay gọi hợp đồng khác (CALL, DELEGATECALL) đều bị ghi nhận lại địa chỉ.
  - Trả về danh sách ĐẦY ĐỦ các contract bị "chạm" tới: `RelatedAddresses = [Sender, Contract_A, Contract_B, Contract_C]`.
- **Tính an toàn (Parallel Safety): ✅ AN TOÀN**.
  Nhờ có "bản đồ" địa chỉ thu thập từ Dry-run, Union-Find đảm bảo: Chỉ cần 2 giao dịch có khả năng chạm vào CÙNG MỘT hợp đồng (dù là gọi trực tiếp hay gián tiếp), chúng sẽ bị ép vào cùng một nhóm và chạy tuần tự. Điều này loại trừ hoàn toàn race condition trên state của EVM.

---

## 4. Tóm Lược 6 Quy Tắc An Toàn (Rule Vàng)

1. **Cùng sender** → Luôn tuần tự (Union-Find gom qua shared `FromAddress` / quản lý Nonce chặt chẽ).
2. **Cùng contract (EVM, Xapian)** → Luôn tuần tự (Union-Find gom qua shared contract address).
3. **Khác sender VÀ không chạm chung bất kỳ contract nào** → Chạy song song an toàn tuyệt đối.
4. **Native dispatch (BLS, AccountSetting)** → Chạy song song giữa các sender khác nhau (nhờ cơ chế Address Filtering).
5. **KHÔNG BAO GIỜ** dùng timeout/timer để quyết định thứ tự xử lý hay gom nhóm → Đảm bảo tính Deterministic (tất định) 100% trên toàn mạng lưới.
6. **Thà pending chứ không fork** — nếu chưa chắc chắn, chờ thêm data từ peers.

---

## 5. File Reference

| File | Chức năng |
|------|-----------|
| `processor/block_processor_network.go` | Nhận commit từ Rust, BATCH-DRAIN, GEI ordering |
| `processor/block_processor_sync.go` | processSingleEpochData, epoch boundary detection |
| `processor/block_processor_processing.go` | createBlockFromResults |
| `processor/tx_validator_pool_core.go` | ProcessTransactions, Union-Find grouping, native filtering |
| `processor/transaction_virtual_processor.go` | Virtual Execution (dry-run cho RelatedAddresses) |
| `processor/transaction_processor.go` | VmProcessor dispatch (EVM call/deploy/native) |
| `pkg/grouptxns/grouptxns.go` | GroupTransactionsDeterministic (Union-Find algorithm) |
| `pkg/blockchain/tx_processor/tx_processor.go` | processGroupsConcurrently, parallel execution |
| `pkg/blockchain/chain_state.go` | Epoch state, CheckAndUpdateEpochFromBlock |
