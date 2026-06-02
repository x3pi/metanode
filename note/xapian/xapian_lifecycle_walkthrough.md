# 🔍 Vòng Đời của Xapian trong Hệ Thống Blockchain Metanode

> **Tài liệu này mô tả chi tiết từng bước** khi một giao dịch (transaction) sử dụng Xapian full-text search database: từ khi Smart Contract gọi precompile, qua lớp FFI (Go → C++), đến khi dữ liệu được commit xuống disk hoặc revert nếu giao dịch lỗi, và cuối cùng là cơ chế cleanup tự động.

---

## 📋 Tổng Quan Kiến Trúc

```mermaid
graph TD
    A["🔗 Solidity Smart Contract<br/>(test-db-xapiant-v2.sol)"] -->|"Gọi precompile 0x107"| B["⚙️ MVM (MetaNode Virtual Machine)<br/>EVM + Custom Opcodes"]
    B -->|"FFI (CGo)"| C["🔌 MyExtension::FullDatabase()<br/>(xapian_handlers.cpp)"]
    C -->|"ABI Decode → gọi method"| D["📦 XapianManager<br/>(xapian_manager.cpp)"]
    D -->|"begin_transaction()"| E["💾 Xapian::WritableDatabase<br/>(On-disk B-tree)"]
    
    F["📋 XapianRegistry<br/>(xapian_registry.cpp)"] -->|"Quản lý instance<br/>theo mvmId"| D
    
    G["✅ TX Thành công"] -->|"CommitFullDb()"| H["commitTransaction()"]
    I["❌ TX Thất bại"] -->|"RevertFullDb()"| J["cancelTransaction()"]
    
    H --> K["db.commit_transaction()"]
    J --> L["db.cancel_transaction()"]
    
    M["🧹 Cleaner Thread<br/>(Background)"] -->|"Mỗi 1 phút"| N["destroyInstance()<br/>Idle > 5 phút"]
```

---

## 🚀 Phase 1: Smart Contract Khởi Tạo (Solidity)

Bắt đầu từ hàm [runStep1_Setup](file:///home/abc/nhat/con-chain-v2/metanode/execution/cmd/tool/tool-test-chain/contract/test-db-xapiant-v2.sol#L234-L241):

```solidity
// test-db-xapiant-v2.sol, line 234
function runStep1_Setup() public {
    isSetupDone = false;
    docIds[0] = 0;
    docIds[1] = 0;
    docIds[2] = 0;
    _setupInternal();   // ← Bước chính
    isSetupDone = true;
}
```

Hàm [_setupInternal](file:///home/abc/nhat/con-chain-v2/metanode/execution/cmd/tool/tool-test-chain/contract/test-db-xapiant-v2.sol#L243-L282) thực hiện 3 bước:

### Bước 1.1: Tạo/Mở Database

```solidity
// line 244
fullDB.getOrCreateDb(DB_NAME);  // DB_NAME = "products_test_v1_version1"
```

> `fullDB` là interface `IFullDBV1` trỏ đến precompile address `0x107`.
> Khi EVM gặp lệnh gọi đến address `0x107`, nó **không chạy bytecode** mà chuyển xuống **C++ handler**.

### Bước 1.2: Insert Product (3 lần)

```solidity
// line 247-256: Insert sản phẩm "iPhone 13 Pro"
docIds[0] = _insertProduct(
    P1_DATA,       // ABI-encoded ProductData struct
    P1_TEXT,       // "Iphone 13 Pro Smart phone cao cap..."
    P1_PRICE,      // "99999"
    P1_DISC,       // "84999"
    "apple",       // brand
    "electronics", // category
    "black",       // color
    "bestseller"   // filter_tag
);
```

Hàm [_insertProduct](file:///home/abc/nhat/con-chain-v2/metanode/execution/cmd/tool/tool-test-chain/contract/test-db-xapiant-v2.sol#L284-L324) gọi 6 precompile calls:

| # | Solidity Call | Mục đích |
|---|--------------|----------|
| 1 | `fullDB.newDocument(DB_NAME, rawData)` | Tạo document mới, lưu data thô (ABI-encoded) |
| 2 | `fullDB.indexTextForDocument(DB_NAME, docId, fullText, 1, "T")` | Index text với prefix "T" (title) |
| 3 | `fullDB.indexTextForDocument(DB_NAME, docId, fullText, 1, "")` | Index text không prefix (full-text) |
| 4 | `fullDB.addTermDocument(DB_NAME, docId, "B:apple")` | Thêm term phân loại brand |
| 5 | `fullDB.addTermDocument(DB_NAME, docId, "C:electronics")` | Thêm term phân loại category |
| 6 | `fullDB.addValueDocument(DB_NAME, docId, 0, "99999", true)` | Thêm value vào slot 0 (price, serialize) |

---

## ⚙️ Phase 2: FFI Bridge — Từ EVM đến C++

Khi EVM gặp lời gọi đến precompile `0x107`, nó kích hoạt C++ handler [MyExtension::FullDatabase()](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/my_extension/xapian_handlers.cpp#L51-L95):

```cpp
// xapian_handlers.cpp, line 51
mvm::Code MyExtension::FullDatabase(mvm::Code input, mvm::Address address,
                                     bool isReset, uint256_t blockNumber) {
    // 1. Extract opcode từ 4 byte đầu
    uint32_t opCode = (input[0] << 24) | (input[1] << 16) | (input[2] << 8) | input[3];
    
    // 2. Off-chain check: nếu là read-only node, fake success cho write ops
    if (this->isOffChain && isWriteOp) {
        return encodeArgument(uint256Abi, "0x1");  // Giả success
    }
    
    // 3. Dispatch theo opcode...
}
```

### Ví dụ: `XAPIAN_GET_OR_CREATE_DB` (opcode match)

```cpp
// xapian_handlers.cpp, line 98-148
if (opCode == mvm::FunctionSelector::XAPIAN_GET_OR_CREATE_DB) {
    // 1. Parse ABI → lấy tên database
    parseABI(input, selector, extracted_str);  // extracted_str = "products_test_v1_version1"
    
    // 2. Tạo thư mục trên disk
    std::filesystem::path fullPath = mvm::createFullPath(address, extracted_str);
    //    → /xapian_base_path/<contract_address>/products_test_v1_version1/
    
    // 3. Lấy hoặc tạo XapianManager instance (singleton per db_path)
    auto manager = XapianManager::getInstance(extracted_str, address, isReset);
    
    // 4. ⭐ ĐĂNG KÝ vào Registry (chỉ on-chain, không off-chain)
    if (!this->isOffChain) {
        registry.registerManager(this->mvmId, manager);
    }
    
    return encodeArgument(uint256Abi, "0x1");  // Trả về true
}
```

> [!IMPORTANT]
> **`registry.registerManager(mvmId, manager)`** — Đây là bước quan trọng nhất!
> Nó liên kết `XapianManager` với `mvmId` (địa chỉ contract + txHash). Nhờ vậy, khi TX kết thúc, hệ thống biết **manager nào cần commit/revert**.

> [!NOTE]
> **FORK-SAFETY**: Đối với các thao tác read-only (ví dụ: `GET_DOCUMENT`, `GET_DATA_DOCUMENT`, v.v.), `manager` sẽ **không được đăng ký** vào Registry. Việc đăng ký sẽ làm `comprehensive_log` của manager tham gia vào quá trình tính hash của giao dịch (ảnh hưởng state hash), có thể gây ra fork giữa các node. Các thao tác ghi (Write) vẫn gọi `registerManager` bình thường.

---

## 📦 Phase 3: XapianManager — Ghi Dữ Liệu Có Versioning

### 3.1 Tạo Document Mới

Trong phiên bản mới, tất cả các hàm ghi/xóa dữ liệu (`new_document`, `add_term`, `index_text`, v.v.) từ `xapian_handlers.cpp` đều truyền thêm `mvmId` xuống `XapianManager`. Điều này giúp hệ thống lưu trữ context giao dịch chính xác hơn.

[new_document()](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/xapian/xapian_manager.cpp#L252-L302):

```cpp
// xapian_manager.cpp, line 252
Xapian::docid XapianManager::new_document(const std::string &data, uint256_t blockNumber, unsigned char* mvmId) {
    touch();  // Cập nhật last_access_time (cho cleaner thread)
    std::lock_guard<std::shared_mutex> lock(changes_mutex);  // Thread-safe

    // ① Bắt đầu transaction nếu chưa có
    bool just_started = false;
    if (!this->has_started) {
        db.begin_transaction();     // ← XAPIAN TRANSACTION BẮT ĐẦU
        this->has_started = true;
        just_started = true;
    }

    // ② Tạo document với data thô + block number
    Xapian::Document doc;
    doc.set_data(data);
    doc.add_value(253, Xapian::sortable_serialise(blockNumber));  // Slot 253 = created_at

    // ③ Thêm vào database → nhận docid
    Xapian::docid id = db.add_document(doc);

    // ④ Thêm deterministic logical ID (FORK-SAFETY)
    auto localId = db_name + "_" + std::to_string(id);
    doc.add_term(LOGICAL_ID_GENERATED_PREFIX + localId);
    db.replace_document(id, doc);

    // ⑤ Ghi log vào comprehensive_log (dùng để tính hash consensus)
    XapianLog::LogEntry entry;
    entry.op = XapianLog::Operation::NEW_DOC;
    comprehensive_log.push_back(entry);

    return id;  // Ví dụ: trả về docid=1, 2, 3
}
```

### 3.2 Versioning — Cơ Chế "Copy on Write"

Khi update document **ở block khác** với block tạo ra nó, Xapian tạo **phiên bản mới** thay vì ghi đè.

Ví dụ [add_value()](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/xapian/xapian_manager.cpp#L355-L452):

```cpp
// xapian_manager.cpp, line 403-421
if (existing_blockNb_serialised == blockNb_serialised) {
    // ✅ CÙNG BLOCK: Update tại chỗ (in-place)
    old_doc.add_value(slot, value_to_add);
    db.replace_document(did, old_doc);
    result_id = did;     // Trả về docid CŨ
}
else {
    // 🔀 KHÁC BLOCK: Tạo version mới
    Xapian::Document new_version_doc = clone_document(old_doc);
    new_version_doc.add_value(slot, value_to_add);
    new_version_doc.add_value(253, blockNb_serialised);  // Slot 253 = created_at (mới)

    old_doc.add_value(254, blockNb_serialised);           // Slot 254 = deleted_at (cũ)
    db.replace_document(did, old_doc);                    // Cập nhật bản cũ
    result_id = db.add_document(new_version_doc);         // Thêm bản mới → docid MỚI
}
```

> [!NOTE]
> **Slot 253** = `created_at_block` — block number mà document version này được tạo.  
> **Slot 254** = `deleted_at_block` — block number mà document version này bị "soft delete".  
> Đọc dữ liệu luôn kiểm tra 2 slot này để xác định document có **active** tại block number hiện tại hay không.

> [!TIP]
> **Tối ưu hóa hiệu năng**: Lời gọi `dump_all_documents(blockNumber)` đã được **loại bỏ** khỏi tất cả các thao tác ghi (trước đây gọi sau mỗi lệnh write) nhằm khắc phục tình trạng suy giảm TPS nghiêm trọng (độ phức tạp O(N^2)).

---

## 🔀 Phase 4: Điều Phối Thực Thi Song Song (Parallel Execution) ở Tầng Go

Trong `tx_processor.go` (hàm `processGroupsConcurrently`), các nhóm giao dịch (Groups) được thực thi **song song** trên nhiều worker (goroutines). Việc này đặt ra một bài toán lớn: làm sao để Xapian ở tầng C++ biết được thay đổi nào thuộc về giao dịch của luồng (group) nào, từ đó commit/revert chính xác mà không gây xung đột (data race) hay chia rẽ trạng thái (fork)?

### 4.1 Sinh `mvmId` Độc Nhất (Deterministic) Cho Từng Nhóm

Trước khi đưa vào Worker Pool chạy song song, tầng Go sinh ra một mã định danh `mvmId` hoàn toàn riêng biệt và deterministic cho mỗi nhóm:

```go
var ethAddressBytes [20]byte
ethAddressBytes[0] = 0xFE  // Đảm bảo không trùng với contract address
copy(ethAddressBytes[1:16], lastBlockHeader.LastBlockHash().Bytes()[:15])
binary.BigEndian.PutUint32(ethAddressBytes[16:], uint32(groupID))
mvmId := common.Address(ethAddressBytes)
```

> [!NOTE]
> Prefix `0xFE` kết hợp với `LastBlockHash` và `GroupID` đảm bảo `mvmId` này là **duy nhất** cho mỗi nhóm và **hoàn toàn giống nhau** trên toàn bộ các node trong mạng lưới, giúp đạt chuẩn deterministic.

### 4.2 Cách Ly Dữ Liệu Bằng `mvmId` (Isolation)

Khi các nhóm được đưa vào thực thi song song, `mvmId` độc nhất này được truyền qua EVM xuống tận tầng C++ (các hàm ghi/xóa ở **Phase 3**).
- **Worker A chạy Nhóm 0**: Gọi xuống Xapian kèm `mvmId_0`. Xapian sẽ log tất cả thay đổi của nhóm này vào danh sách quản lý của `mvmId_0`.
- **Worker B chạy Nhóm 1**: Gọi xuống Xapian kèm `mvmId_1`. Xapian sẽ log tất cả thay đổi của nhóm này vào danh sách quản lý của `mvmId_1`.

Nhờ cơ chế này, `XapianRegistry` phân loại minh bạch được những thay đổi nào thuộc về group nào đang chạy. Các thao tác ghi đồng thời (concurrency) không bị trộn lẫn vào nhau, bảo đảm Fork-Safety và Thread-Safety tuyệt đối cho dữ liệu.

---

## 🔒 Phase 5: Transaction Commit/Revert

Sau khi MVM thực thi xong toàn bộ Smart Contract code, hệ thống quyết định commit hay revert dựa trên ngữ cảnh và trạng thái lỗi.

### 5.1 Xử Lý Exception Tại MVM (C++)

Nếu trong quá trình gọi precompile có lỗi xảy ra (ví dụ: revert từ smart contract), `MVM` (trong [processor.cpp](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/c_mvm/src/processor.cpp)) sẽ ưu tiên bắt các lỗi này và trích xuất nguyên bản `exception message` từ `returnData` của nested call thay vì dùng chuỗi lỗi mặc định:

```cpp
if (result.ex == ET::ErrExecutionReverted) {
    // ...
    // ✅ Ưu tiên sử dụng exception message từ returnData (từ nested call)
    if (!ctxt->returnData.empty()) {
        std::string original_msg(ctxt->returnData.begin(), ctxt->returnData.end());
        result.exmsg = original_msg;
    } else {
        result.exmsg = ex_.what();
    }
}
```
Điều này đảm bảo thông báo lỗi (panic/revert) từ bên trong C++ (như Xapian handler) được đưa đẩy chính xác lên tầng Go, giúp việc debug giao dịch dễ dàng hơn.

### 5.2 Luồng Quyết Định Commit/Revert (Tầng Go)

Xem [block_processor_commit.go](file:///home/abc/nhat/con-chain-v2/metanode/execution/cmd/simple_chain/processor/block_processor_commit.go#L72-L116):

```go
// block_processor_commit.go, line 72-106
if job.ProcessResults != nil {
    committedMvmIds := make(map[common.Address]struct{}, 64)

    for _, tx := range job.ProcessResults.Transactions {
        if (isCall || isDeploy) && !tx.GetReadOnly() {
            mvmId := tx.ToAddress()
            mvmAPI := mvm.GetMVMApi(mvmId)

            mvmRs := mvmAPI.GetExecuteResult()
            if mvmRs.Status == pb.RECEIPT_STATUS_THREW || 
               mvmRs.Status == pb.RECEIPT_STATUS_HALTED {
                // ❌ TX THẤT BẠI → REVERT
                mvmAPI.RevertFullDb()
            } else {
                // ✅ TX THÀNH CÔNG → COMMIT
                mvmAPI.CommitFullDb()
            }
            
            committedMvmIds[mvmId] = struct{}{}
        }
    }
    
    // 🧹 Dọn dẹp MVMApi instances đã xử lý xong
    for mvmId := range committedMvmIds {
        mvm.ClearMVMApi(mvmId)
    }
}
```

### 5.3 CommitFullDb() — FFI Go → C++

[mvm_api.go, line 922-931](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/mvm_api.go#L922-L931):

```go
func (a *MVMApi) CommitFullDb() bool {
    mvmId := a.key
    cBBmvmId := C.CBytes(mvmId.Bytes())
    defer C.free(unsafe.Pointer(cBBmvmId))
    status := C.commit_full_db((*C.uchar)(cBBmvmId))  // ← Gọi sang C++
    return status != 0
}
```

C++ Registry xử lý: [commitTransaction()](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/xapian/xapian_registry.cpp#L506-L532)

```cpp
void XapianRegistry::commitTransaction(unsigned char *mvmId) {
    auto managers = getManagersForMvmId(mvmId);
    for (const auto &manager_ptr : managers) {
        if (manager_ptr && manager_ptr->has_started) {
            manager_ptr->db.commit_transaction();   // ← FLUSH TO DISK
            manager_ptr->has_started = false;
        }
    }
}
```

### 5.4 RevertFullDb() — Hủy Tất Cả Thay Đổi

[cancelTransaction()](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/xapian/xapian_registry.cpp#L476-L503):

```cpp
void XapianRegistry::cancelTransaction(unsigned char *mvmId) {
    auto managers = getManagersForMvmId(mvmId);
    for (const auto &manager_ptr : managers) {
        if (manager_ptr && manager_ptr->has_started) {
            manager_ptr->removeLogsUntilNearestEndCommand();
            manager_ptr->db.cancel_transaction();   // ← ROLLBACK, data CŨ được khôi phục
            manager_ptr->has_started = false;
        }
    }
}
```

> [!TIP]
> **Xapian transaction** hoạt động giống database transaction:
> - `begin_transaction()` → Bắt đầu ghi buffered
> - `commit_transaction()` → Flush tất cả thay đổi xuống B-tree trên disk
> - `cancel_transaction()` → Discard tất cả, database quay về trạng thái trước `begin_transaction()`

---

## 🔐 Phase 6: Hash Consensus — Đảm Bảo Tất Cả Node Giống Nhau

Trước khi commit, mỗi node tính **hash của tất cả thay đổi Xapian** để so sánh với node khác:

### 6.1 Tính Hash Cho Từng Manager

[getComprehensiveStateHash()](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/xapian/xapian_manager.cpp#L929-L948):

```cpp
std::array<uint8_t, 32u> XapianManager::getComprehensiveStateHash() {
    // 1. Hash của tất cả LogEntry (new_doc, add_term, add_value, ...)
    std::array<uint8_t, 32u> manager_log_hash = this->getChangeHash();
    
    // 2. Hash của tag changes (nếu có)
    std::array<uint8_t, 32u> tags_hash = this->getCombinedTagsChangeHash();
    
    // 3. Nối lại và hash lần nữa
    return mvm::keccak_256(concatenated_data);
}
```

### 6.2 Nhóm Theo Contract Address

[getGroupHashForMvmId()](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/xapian/xapian_registry.cpp#L266-L319):

```mermaid
graph LR
    A["mvmId<br/>(txHash → contract)"] --> B["managers[]"]
    B --> C["Group by<br/>contract address"]
    C --> D["Contract 0xABC<br/>manager1, manager2"]
    C --> E["Contract 0xDEF<br/>manager3"]
    D --> F["hash(log1 + log2)"]
    E --> G["hash(log3)"]
    F --> H["Keccak256"]
    G --> H
```

> [!WARNING]
> **FORK-SAFETY**: Nếu hash không khớp giữa các node → block bị reject, node chờ CertifiedCommit từ consensus. **Thà pending chứ không fork.**

---

## 🧹 Phase 7: Cleanup — Dọn Dẹp Tài Nguyên

### 7.1 Cleaner Thread (Background — Mỗi 1 Phút)

[xapian_manager.cpp, line 956-1008](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/xapian/xapian_manager.cpp#L956-L1008):

```cpp
// Luồng chạy nền — khởi tạo tĩnh khi chương trình start
std::thread cleaner_thread([] {
    while (cleaner_running.load()) {
        std::this_thread::sleep_for(std::chrono::minutes(1));  // Ngủ 1 phút
        
        // Giai đoạn 1: Tìm instances idle > 5 phút
        for (auto it = XapianManager::instances.begin(); ...) {
            if (it->second->is_idle_for(std::chrono::minutes(5))) {
                // Chỉ xóa nếu không ai giữ reference (use_count <= 2)
                if (temp_ptr.use_count() <= 2) {
                    keys_to_erase.push_back(it->first);
                }
            }
        }
        
        // Giai đoạn 2: Hủy instance
        for (const std::string &key : keys_to_erase) {
            XapianManager::destroyInstance(key);  // close DB + xóa khỏi map
        }
    }
});
```

### 7.2 destroyInstance() — Đóng DB Tường Minh

[xapian_manager.cpp, line 1144-1194](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/xapian/xapian_manager.cpp#L1144-L1194):

```cpp
bool XapianManager::destroyInstance(const std::string &db_path_str) {
    // 1. Xóa khỏi instances map (giảm reference count)
    {
        std::unique_lock<std::shared_mutex> write_lock(instances_mutex);
        auto it = instances.find(db_path_str);
        instance_ptr = it->second;
        instances.erase(it);
    }
    
    // 2. Đóng Xapian database handle
    instance_ptr->db.close();
    
    // 3. shared_ptr tự động delete khi use_count = 0
    return true;
}
```

### 7.3 Registry Cleanup — Sau Mỗi TX

[xapian_registry.cpp, line 431](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/xapian/xapian_registry.cpp#L394-L434):

```cpp
bool XapianRegistry::commitChangesForMvmId(unsigned char *mvmId) {
    // ... commit all managers ...
    
    // [FIX] Xóa khỏi registry sau commit
    // → manager (static instance) có thể gán cho TX tiếp theo
    unregisterAllManagersForMvmId(mvmId);
    return all_succeeded;
}
```

---

## 📊 Tổng Kết: Timeline Một Giao Dịch Xapian

```mermaid
sequenceDiagram
    participant SC as Solidity Contract
    participant EVM as MVM (EVM)
    participant FFI as C++ Handler
    participant XM as XapianManager
    participant XDB as Xapian Database
    participant REG as XapianRegistry
    participant BP as BlockProcessor
    
    SC->>EVM: runStep1_Setup()
    EVM->>FFI: FullDatabase(XAPIAN_GET_OR_CREATE_DB)
    FFI->>XM: getInstance("products_test_v1_version1")
    XM->>XDB: Xapian::WritableDatabase(CREATE_OR_OPEN)
    FFI->>REG: registerManager(mvmId, manager)
    FFI-->>EVM: return true
    
    loop For each product (x3)
        EVM->>FFI: FullDatabase(XAPIAN_NEW_DOCUMENT)
        FFI->>XM: new_document(rawData, blockNumber, mvmId)
        XM->>XDB: begin_transaction() (chỉ lần đầu)
        XM->>XDB: add_document(doc) → docid
        XM->>XM: comprehensive_log.push_back(NEW_DOC)
        FFI-->>EVM: return docid
        
        EVM->>FFI: FullDatabase(XAPIAN_INDEX_TEXT_DOCUMENT)
        FFI->>XM: index_text(docid, text, weight, prefix)
        XM->>XDB: replace_document(docid, updated_doc)
        XM->>XM: comprehensive_log.push_back(INDEX_TEXT)
        FFI-->>EVM: return docid
        
        Note over FFI,XM: + addTermDocument x4<br/>+ addValueDocument x2
    end
    
    EVM-->>BP: TX kết thúc → ExecuteResult
    
    alt TX Thành Công (Status = SUCCESS)
        BP->>EVM: CommitFullDb(mvmId)
        EVM->>REG: commitTransaction(mvmId)
        REG->>XM: db.commit_transaction()
        XM->>XDB: 💾 FLUSH TO DISK
        REG->>REG: unregisterAllManagersForMvmId(mvmId)
    else TX Thất Bại (Status = THREW/HALTED)
        BP->>EVM: RevertFullDb(mvmId)
        EVM->>REG: cancelTransaction(mvmId)
        REG->>XM: db.cancel_transaction()
        XM->>XDB: 🔄 ROLLBACK
        REG->>REG: unregisterAllManagersForMvmId(mvmId)
    end
    
    Note over XM: ⏰ Background: Cleaner thread<br/>mỗi 1 phút kiểm tra<br/>idle > 5 phút → destroyInstance()
```

---

## 🗂️ Tổng Hợp File Liên Quan

| Layer | File | Vai trò |
|-------|------|---------|
| **Solidity** | [test-db-xapiant-v2.sol](file:///home/abc/nhat/con-chain-v2/metanode/execution/cmd/tool/tool-test-chain/contract/test-db-xapiant-v2.sol) | Smart contract test, gọi precompile `0x107` |
| **C++ Handler** | [xapian_handlers.cpp](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/my_extension/xapian_handlers.cpp) | ABI decode → dispatch opcode → gọi XapianManager |
| **C++ Core** | [xapian_manager.cpp](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/xapian/xapian_manager.cpp) | CRUD + versioning + logging + transaction |
| **C++ Registry** | [xapian_registry.cpp](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/linker/src/xapian/xapian_registry.cpp) | Quản lý lifecycle per-mvmId, commit/revert/hash |
| **Go FFI** | [mvm_api.go](file:///home/abc/nhat/con-chain-v2/metanode/execution/pkg/mvm/mvm_api.go#L922-L941) | `CommitFullDb()` / `RevertFullDb()` — cầu nối Go↔C++ |
| **Go Commit** | [block_processor_commit.go](file:///home/abc/nhat/con-chain-v2/metanode/execution/cmd/simple_chain/processor/block_processor_commit.go#L72-L116) | Quyết định commit/revert dựa trên TX status |

---

> [!CAUTION]
> **Không bao giờ ghi trực tiếp vào Xapian DB mà không qua XapianManager!**
> - Mọi write đều phải đi qua `new_document()`, `add_term()`, `add_value()`, v.v.
> - Mỗi write đều được ghi vào `comprehensive_log` để tính hash consensus.
> - Bỏ qua log → hash khác nhau giữa các node → **FORK**.
