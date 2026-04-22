# 📚 FlatStateTrie Storage — Tài liệu kiến trúc chi tiết

> Viết cho người chưa từng làm việc với blockchain storage trước đây.

---

## 1. Bức tranh tổng thể — Blockchain lưu gì?

Khi bạn deploy một smart contract lên blockchain, hoặc gửi token cho ai đó, blockchain phải **ghi nhớ** tất cả thứ đó lâu dài. Nói cụ thể, blockchain cần lưu 2 loại dữ liệu chính:

| Loại dữ liệu | Ví dụ | Lưu ở đâu |
|---|---|---|
| **Account State** | Số dư ví, Nonce, Public Key | `AccountStateDB` → PebbleDB (file `account/`) |
| **Smart Contract Storage** | Biến `uint256 value = 2222;` trong contract | `SmartContractDB` → PebbleDB (file `smart_contract/`) |

Hai cái DB này hoàn toàn tách biệt nhau trên ổ cứng. Bài viết này tập trung vào **cả hai**, nhưng đặc biệt vào cơ chế mới **FlatStateTrie với Namespace** cho Smart Contract Storage.

---

## 2. Vấn đề: Tại sao không dùng Ethereum gốc (MPT)?

Ethereum dùng **Merkle Patricia Trie (MPT)** để lưu state. Cấu trúc MPT giống một "cây quyết định" phân nhánh theo từng ký tự hex của key.

```
Root
├── 0x4...
│   ├── 0x8...
│   │   └── 0x9... → [Balance: 100 ETH]   ← Địa chỉ ví 0x489...
│   └── 0xA...
│       └── 0xF... → [Balance: 50 ETH]
```

**Nhược điểm của MPT:** Để đọc 1 số dư, phải đi qua 6-7 nút cây → **6-7 lần đọc ổ cứng**. Khi state lớn (hàng triệu địa chỉ), cực kỳ chậm.

**Giải pháp của MetaNode:** FlatStateTrie — lưu thẳng, đọc O(1).

---

## 3. FlatStateTrie là gì? Cách lưu O(1)

Thay vì cây sâu nhiều tầng, `FlatStateTrie` lưu **trực tiếp Key-Value** vào PebbleDB:

```
PebbleDB (ổ cứng)
┌────────────────────────────────────────────────────────┐
│  Key                          │  Value                 │
│───────────────────────────────│────────────────────────│
│  "fs:0x488900637C9d..."       │  {balance:2000, nonce:5} │  ← Account ví A
│  "fs:0x7d03201fee46..."       │  {balance:500, nonce:1}  │  ← Account ví B
│  "fb:0x00"                    │  0xABCD...32bytes        │  ← Bucket hash 0
│  "fb:0x01"                    │  0x1234...32bytes        │  ← Bucket hash 1
│  ...                          │  ...                     │
└────────────────────────────────────────────────────────┘
```

- Prefix `fs:` = Flat State (dữ liệu thực)
- Prefix `fb:` = Flat Bucket (dữ liệu phụ trợ tính hash)

**Đọc dữ liệu:** `db.Get("fs:" + địa_chỉ_ví)` → **1 lần I/O duy nhất!**

---

## 4. Bài toán Hash: FlatTrie tính Root Hash như thế nào?

Đây là phần phức tạp nhất. Blockchain yêu cầu mỗi block phải có **Root Hash** — một con số đại diện cho toàn bộ state. Nếu bất kỳ byte nào thay đổi, Root Hash phải thay đổi.

Với MPT, Root Hash tính tự nhiên từ cấu trúc cây. Với FlatTrie (không có cây), tác giả MetaNode thiết kế thuật toán riêng:

### 4.1 Thuật toán Bucket Accumulator (MuHash Modulo Prime)

**Bước 1: Chia 256 bucket**
Mỗi entry được gán vào 1 trong 256 bucket dựa trên **byte đầu tiên của key**.

```
Entry "fs:0x48..." → Key byte đầu = 0x48 = 72 → Bucket[72]
Entry "fs:0x7d..." → Key byte đầu = 0x7D = 125 → Bucket[125]
```

**Bước 2: Tính contribution của mỗi entry**

```
contribution = keccak256(key_bytes || value_bytes)
```

**Bước 3: Tính bucket accumulator theo phép nhân modulo số nguyên tố**

```
Bucket[i] = (Bucket[i] × contribution) mod P
```

Với P = 2^256 - 189 (số nguyên tố 256-bit).

**Bước 4: Tính Root Hash**

```
RootHash = keccak256(Bucket[0] || Bucket[1] || ... || Bucket[255])
           = keccak256(tất cả 256 bucket ghép lại, tổng 8192 bytes)
```

### 4.2 Tại sao dùng phép nhân module thay vì cộng?

Khi **thêm** một entry mới: nhân thêm contribution vào bucket.
Khi **xóa/cập nhật** một entry: chia (= nhân với nghịch đảo modular) contribution cũ ra khỏi bucket, rồi nhân contribution mới vào.

```go
// Cập nhật entry:
divModPrime(&bucket, oldContribution)  // remove old
mulModPrime(&bucket, newContribution)  // add new
```

Điều này cho phép **cập nhật tăng dần (incremental)** — chỉ tính lại những entry thay đổi, không cần đọc lại toàn bộ state!

---

## 5. Smart Contract Storage — Vấn đề Namespace

### 5.1 EVM lưu biến Solidity như thế nào?

Khi bạn viết Solidity:

```solidity
contract MyContract {
    uint256 public value;   // → Slot 0 (0x0000...0000)
    address public owner;   // → Slot 1 (0x0000...0001)
    uint256 public count;   // → Slot 2 (0x0000...0002)
}
```

EVM lưu trữ mỗi biến vào một **Slot** (32 bytes key). Slot 0, Slot 1, Slot 2... Mỗi contract đều có Slot 0, Slot 1...

### 5.2 Vấn đề khi dùng FlatTrie cho Smart Contract (cũ — ĐÃ BUG)

Nếu dùng FlatTrie thông thường (không namespace) cho **tất cả contract** trên cùng 1 DB:

```
DB dùng chung (dbSmartContract)
"fs:Slot0" → 2222          ← Contract A ghi (setValue(2222))
"fs:Slot0" → ???           ← Contract B đọc → nhận được 2222 của A! ❌ BUG!
```

Đây là bug "2222 xuyên không" đã được phát hiện và fix.

### 5.3 Giải pháp: Namespace = Địa chỉ Contract (20 bytes)

Mỗi contract có một **namespace riêng** = địa chỉ của nó (20 bytes). Key giờ trở thành:

```
Key = "fs:" + <20 bytes contract address> + <32 bytes slot>
```

**Ví dụ cụ thể:**

```
DB dùng chung (dbSmartContract)
"fs:" + [0x477f...325] + [0x0000...0] → 2222   ← Contract A, Slot 0
"fs:" + [0xE18c...f2] + [0x0000...0] → 0       ← Contract B, Slot 0 (riêng biệt!)
"fs:" + [0x477f...325] + [0x0000...1] → 0xABC  ← Contract A, Slot 1
```

Không bao giờ đụng độ nhau nữa! ✅

### 5.4 Bucket cũng có namespace

Key của bucket accumulator cũng include namespace:

```
"fb:" + <20 bytes contract address> + <1 byte bucket index>
```

Vì vậy **mỗi contract có 256 bucket accumulator riêng**, tính Root Hash (StorageRoot) riêng biệt.

---

## 6. Kiến trúc Go đầy đủ — Ai gọi ai?

```
┌──────────────────────────────────────────────────────────────────────────┐
│                          SMART CONTRACT STORAGE                          │
│                                                                          │
│  SmartContractDB (pkg/smart_contract_db/)                               │
│  ┌────────────────────────────────────────────────────────────────────┐ │
│  │  smartContractStorageTries: map[Address]StateTrie                  │ │
│  │  (RAM cache — mỗi contract 1 FlatStateTrie riêng trong bộ nhớ)   │ │
│  │                                                                    │ │
│  │  StorageValue(address, slot) → FlatStateTrie.Get(slot)            │ │
│  │  SetStorageValue(address, slot, val) → FlatStateTrie.Update(slot) │ │
│  │  loadStorageTrie(address) → tạo/lấy FlatStateTrie của contract    │ │
│  └────────────────────────────────────────────────────────────────────┘ │
│                           │ (namespace = address)                        │
│                           ▼                                              │
│  FlatStateTrie (pkg/trie/flat_state_trie.go)                            │
│  ┌────────────────────────────────────────────────────────────────────┐ │
│  │  namespace: []byte = contract address (20 bytes)                   │ │
│  │  dirty: map[slot → newValue] (RAM, chưa ghi xuống disk)           │ │
│  │  buckets: [256]Hash (bucket accumulator cho Root Hash)            │ │
│  │                                                                    │ │
│  │  Get(slot) → db.Get("fs:" + namespace + slot)                     │ │
│  │  Update(slot, val) → dirty[slot] = val                            │ │
│  │  Commit() → BatchPut tất cả dirty vào PebbleDB                    │ │
│  └────────────────────────────────────────────────────────────────────┘ │
│                           │                                              │
│                           ▼                                              │
│  PebbleDB (ổ cứng — pkg/storage/)                                       │
│  ┌────────────────────────────────────────────────────────────────────┐ │
│  │  "fs:" + addr_A + slot0  → 2222                                   │ │
│  │  "fs:" + addr_A + slot1  → 0xABC                                  │ │
│  │  "fs:" + addr_B + slot0  → 0        ← RIÊNG BIỆT!                │ │
│  │  "fb:" + addr_A + 0x00   → bucket[0] hash của contract A         │ │
│  │  "fb:" + addr_B + 0x00   → bucket[0] hash của contract B         │ │
│  └────────────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## 7. EVM C++ tương tác với Go như thế nào?

EVM (MVM) của MetaNode được viết bằng **C++** nhưng chạy trong process Go qua **CGo** (C-Go bridge). Hai bên giao tiếp qua **exported functions**.

### 7.1 Go export function cho C++ gọi

Trong `mvm_api.go`:

```go
//export GetStorageValue   ← C++ gọi hàm này
func GetStorageValue(mvmId *C.uchar, address *C.uchar, key *C.uchar) (value *C.uchar, success bool) {
    // 1. Convert bytes C → Go
    fAddress := common.BytesToAddress(C.GoBytes(unsafe.Pointer(address), 20))
    bKey := C.GoBytes(unsafe.Pointer(key), 32)
    
    // 2. Tìm MVMApi instance (chứa reference đến SmartContractDB)
    mvmApi := GetMVMApi(fmvmId)
    
    // 3. Gọi xuống SmartContractDB → FlatStateTrie → PebbleDB
    bValue, success := mvmApi.smartContractDb.StorageValue(fAddress, bKey)
    
    // 4. Trả về C++ (C++ sẽ free bộ nhớ này)
    return (*C.uchar)(C.CBytes(bValue)), success
}
```

### 7.2 Luồng đọc đầy đủ: EVM muốn đọc `value` của Contract A

```
[Solidity EVM C++]
  ║   SLOAD(slot=0x0000...0)  ← opcode đọc storage
  ║
  ╠══ gọi C callback ══╗
  ║                    ║
  ║     [Go: GetStorageValue(mvmId, contractA_addr, slot0)]
  ║                    ║
  ║                    ╠══ SmartContractDB.StorageValue(contractA, slot0)
  ║                    ║
  ║                    ╠══ loadStorageTrie(contractA)
  ║                    ║        → check RAM cache (smartContractStorageTries)
  ║                    ║        → nếu miss: tạo FlatStateTrie(namespace=contractA)
  ║                    ║
  ║                    ╠══ FlatStateTrie.Get(slot0)
  ║                    ║        → check dirty map (RAM)
  ║                    ║        → nếu miss: db.Get("fs:" + contractA + slot0)
  ║                    ║        → PebbleDB trả về: 2222
  ║                    ║
  ╠══ trả về 2222 ══════╝
  ║
  ║  tiếp tục thực thi...
```

### 7.3 Luồng ghi đầy đủ: EVM muốn ghi `value = 50` vào Contract A

```
[Solidity EVM C++]
  ║   SSTORE(slot=0x0000...0, value=50)  ← opcode ghi storage
  ║
  ╠══ gọi C callback ══╗
  ║                    ║
  ║     [Go: SetStorageValue(mvmId, contractA_addr, slot0, 50)]
  ║                    ║
  ║                    ╠══ SmartContractDB.SetStorageValue(contractA, slot0, 50)
  ║                    ║
  ║                    ╠══ FlatStateTrie.Update(slot0, 50)
  ║                    ║        → dirty["0x0000...0"] = 50  ← CHỈ GHI VÀO RAM
  ║                    ║        → CHƯA ghi xuống PebbleDB!
  ║
  ╠══ return (ok) ══════╝
  ║
  ║  EVM tiếp tục...
```

**Lưu ý quan trọng:** Dữ liệu chỉ được flush xuống disk khi **block được commit**:

```
Block commit → SmartContractDB.Commit()
             → FlatStateTrie.Commit()
             → BatchPut tất cả dirty entries vào PebbleDB
             → Cập nhật bucket accumulators
             → Trả về StorageRoot mới
```

---

## 8. Account State — Cũng dùng FlatStateTrie nhưng ĐƠN GIẢN HƠN

Với Account State (số dư ví, nonce...), thiết kế đơn giản hơn vì **không cần namespace** — bản thân địa chỉ ví đã là key duy nhất toàn cầu.

```go
// Trong AccountStateDB:
trie.Update(address.Bytes(), accountStateBytes)
// → FlatStateTrie.Update(key=[20 bytes địa chỉ], value=[serialized account])
// → Lưu PebbleDB: "fs:" + địa_chỉ → {balance, nonce, publicKey...}
```

| | Account State | Smart Contract Storage |
|---|---|---|
| Key lưu vào DB | `fs:` + address (20 bytes) | `fs:` + contract (20 bytes) + slot (32 bytes) |
| Namespace | Không cần | Bắt buộc (địa chỉ contract) |
| Bucket key | `fb:` + byte_đầu_của_address | `fb:` + contract + slot_byte_đầu |
| DB instance | `storageAccount` (riêng) | `dbSmartContract` (dùng chung) |

---

## 9. Vòng đời đầy đủ của 1 transaction `setValue(50)`

```
1. Client gửi transaction đến node qua TCP

2. Node nhận → TransactionProcessor xử lý

3. Gọi MVM (C++ EVM):
   mvmApi.Execute(tx)
   
4. C++ biên dịch bytecode Solidity:
   PUSH 50
   PUSH slot0
   SSTORE          ← "ghi 50 vào slot 0"
   
5. C++ callback sang Go:
   SetStorageValue(contractA, slot0, 50)
   
6. Go ghi vào RAM (dirty map):
   FlatStateTrie.dirty["0x0000..0"] = 50
   
7. EVM xong → trả về MVMResult

8. Block Proposer commit block:
   SmartContractDB.Commit()
   FlatStateTrie.Commit():
     a. Đọc old value từ DB: db.Get("fs:" + contractA + slot0) → 0
     b. Tính old contribution: keccak256(slot0 || 0)
     c. Xóa old khỏi bucket: bucket[0] ÷= oldContrib (mod P)
     d. Tính new contribution: keccak256(slot0 || 50)
     e. Thêm new vào bucket: bucket[0] ×= newContrib (mod P)
     f. Ghi xuống PebbleDB (async):
        "fs:" + contractA + slot0 → 50
        "fb:" + contractA + 0x00  → bucket[0]
     g. Tính StorageRoot mới:
        keccak256(bucket[0] || bucket[1] || ... || bucket[255])
   
9. AccountStateDB cập nhật SmartContractState(storageRoot) của contract A

10. BlockHeader lưu AccountStatesRoot mới (bao gồm storage root của A)

11. Block được đồng thuận và broadcast
```

---

## 10. Sơ đồ kiến trúc tổng thể

```
                 ┌──────────────────────────────────────────────────────┐
                 │                METACONSENSUS NODE                     │
                 │                                                        │
  Client TCP ──►│  TransactionProcessor                                  │
                 │         │                                              │
                 │         ▼                                              │
                 │  MVMApi (mvm_api.go)                                   │
                 │  ┌──────────────────────────────────────────────────┐ │
                 │  │  C++ EVM (MVM) ←──CGo──→ Go callbacks           │ │
                 │  │                                                    │ │
                 │  │  SLOAD(slot) ──────────► GetStorageValue()       │ │
                 │  │  SSTORE(slot, val) ────► SetStorageValue()       │ │
                 │  │  BALANCE(addr) ─────────► GlobalStateGet()       │ │
                 │  └──────────────────────────────────────────────────┘ │
                 │         │                        │                     │
                 │         ▼                        ▼                     │
                 │  AccountStateDB         SmartContractDB                │
                 │  FlatStateTrie          FlatStateTrie (namespaced)     │
                 │  (no namespace)         (namespace = contract addr)    │
                 │         │                        │                     │
                 │         ▼                        ▼                     │
                 │  PebbleDB (account/)    PebbleDB (smart_contract/)     │
                 │  "fs:addrA" → data      "fs:contractA+slot0" → 50     │
                 │  "fs:addrB" → data      "fs:contractB+slot0" → 0      │
                 │  "fb:0x48"  → hash      "fb:contractA+0x00" → hash    │
                 └──────────────────────────────────────────────────────┘
```

---

## 11. Tóm tắt: Ưu điểm thiết kế này

| Tiêu chí | MPT (Ethereum) | FlatStateTrie (MetaNode) |
|---|---|---|
| Đọc dữ liệu | O(log N) — 6-7 lần I/O | **O(1) — 1 lần I/O** |
| Tính Root Hash | O(N) — duyệt toàn cây | **O(K) — chỉ entries thay đổi** |
| Cách ly contract | ✅ (hash tree tự nhiên) | ✅ (namespace 20 bytes) |
| Cách ly account | ✅ | ✅ |
| Reorg support | ✅ tốt | ⚠️ cần test thêm |
| Độ phức tạp cài đặt | Cao (trie traversal) | Trung bình (math modular) |
