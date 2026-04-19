# Xapian DB — Luồng Hoạt Động & Commit Dữ Liệu

> **Phạm vi**: `pkg/mvm/linker/src/xapian/` + `pkg/mvm/linker/src/my_extension/xapian_handlers.cpp`
> **Ngôn ngữ**: C++17
> **Mục đích**: Full-text search engine tích hợp vào MVM, cho phép smart contract lưu trữ và truy vấn index văn bản trực tiếp on-chain.

---

## 1. Kiến Trúc Tổng Quan

```
Smart Contract (MVM bytecode)
        |
        |  XAPIAN_* opcode call (ABI-encoded)
        v
+-----------------------------------------------------+
|         MyExtension::FullDatabase()                 |   xapian_handlers.cpp
|  Dispatch theo opCode (4 byte đầu tiên)             |
+-----------------------------------------------------+
        |
        |  getInstance() / operationXxx()
        v
+-----------------------------------------------------+
|              XapianManager                          |   xapian_manager.cpp
|  WritableDB (Xapian::WritableDB)                    |
|  comprehensive_log (staged change log, in-memory)   |
+-----------------------------------------------------+
        |
        |  registerManager()
        v
+-----------------------------------------------------+
|              XapianRegistry                         |   xapian_registry.cpp
|  TBB concurrent_hash_map<mvmId to [managers]>       |
|  (anh xa transaction ID to danh sach manager)       |
+-----------------------------------------------------+
        |
        |  commitChangesForMvmId() -> saveAllAndCommit() -> db.commit()
        v
+-----------------------------------------------------+
|           Xapian WritableDatabase (Disk)            |
|           path: <data_dir>/<address>/<dbname>/      |
+-----------------------------------------------------+
```

### Các thành phần chính

| File | Vai trò |
|------|---------|
| `xapian_handlers.cpp` | Dispatch opcode từ MVM, gọi XapianManager |
| `xapian_manager.cpp` | Thực thi thao tác, quản lý staged log, commit |
| `xapian_registry.cpp` | Map từ `mvmId` sang danh sách manager (per-transaction) |
| `xapian_log.cpp` | Định nghĩa và serialize/deserialize `LogEntry` |
| `xapian_search.cpp` | Query-time searcher (read-only, không qua Manager) |

---

## 2. Lifecycle Của Một XapianManager

### 2.1 Tạo Instance (Singleton per DB path)

```cpp
// XapianManager::getInstance()
std::filesystem::path db_path = mvm::createFullPath(addr, db_name);
// path = "<data_dir>/<address_hex>/<db_name>/"

// Tra cứu trong TBB concurrent_hash_map
if (!isReset && instances.find(accessor, db_path_str)) {
    accessor->second->touch();  // Cap nhat last_access_time
    return accessor->second;    // Tra ve instance da co
}

// Tao moi neu chua co
accessor->second = std::make_shared<XapianManager>(db_name, addr);
// Constructor: db(path, Xapian::DB_CREATE_OR_OPEN)
```

**Key point**: Mỗi `(address, db_name)` pair -> 1 instance duy nhất trong `XapianManager::instances` (static TBB map). Instance dùng chung across transactions nhưng được protect bởi `db_mutex`.

### 2.2 Đăng Ký Với MVM Transaction

```cpp
registry.registerManager(this->mvmId, manager);
```

`XapianRegistry` ánh xạ `mvmId -> [manager1, manager2, ...]`. Một transaction có thể dùng nhiều DB. Một manager chỉ thuộc 1 mvmId tại một thời điểm.

---

## 3. Luồng Ghi Dữ Liệu (Write Path)

### 3.1 Tổng Quan Write Flow

```
MVM opcode call
    |
    +-- decode ABI input
    |
    +-- getInstance(dbname, address, isReset)  ->  XapianManager singleton
    |
    +-- registerManager(mvmId, manager)        ->  registry map
    |
    +-- manager->operation(docId, data, blockNumber)
    |       +-- lock(db_mutex)
    |       +-- begin_transaction() neu chua started
    |       +-- thuc hien thao tac Xapian (add/replace document)
    |       +-- lock(changes_mutex)
    |       +-- push LogEntry vao comprehensive_log
    |
    +-- return encoded result (docId)
```

### 3.2 Chi Tiết Từng Opcode Ghi

#### XAPIAN_GET_OR_CREATE_DB
```
Input:  dbname (string)
Output: 1 (uint256, success flag)

Flow:
1. createFullPath(address, dbname) -> physical path
2. std::filesystem::create_directories() neu chua ton tai
3. XapianManager::getInstance() -> tao hoac lay instance
4. registry.registerManager(mvmId, manager)
5. Tra ve 1 (success)
```

Day la buoc BAT BUOC dau tien truoc khi goi cac opcode ghi khac.

---

#### XAPIAN_NEW_DOCUMENT
```
Input:  dbname (string), data (string)
Output: docId (uint256)

Flow:
1. getInstance(dbname, address, isReset)
2. lock db_mutex
3. if !has_started -> db.begin_transaction()
4. Tao Xapian::Document moi:
   - doc.set_data(data)                         <- raw data
   - doc.add_value(253, serialise(blockNumber)) <- "created_at"
   (Note: UUID generation was disabled for strict state determinism)
5. docId = db.add_document(doc)
6. lock changes_mutex -> push LogEntry{NEW_DOC, docId, data}
7. return docId
```

Slot 253 = created_at blockNumber (serialized dang Xapian sortable float).

---

#### XAPIAN_ADD_TERM_DOCUMENT
```
Input:  dbname, docId (uint256), term (string)
Output: docId (uint256)

Flow:
1. getInstance() -> lock db_mutex -> begin_transaction() neu can
2. old_doc = db.get_document(did)
3. existing_blockNb = old_doc.get_value(253)

Case A: existing_blockNb == current_blockNb  (cung block)
   -> old_doc.add_term(term)
   -> db.replace_document(did, old_doc)
   -> return did

Case B: khac block (versioned update)
   -> new_doc = clone_document(old_doc)  <- copy toan bo term, value, data
   -> new_doc.add_term(term)
   -> new_doc.add_value(253, blockNb)    <- set created_at = current block
   -> old_doc.add_value(254, blockNb)   <- danh dau old version deleted_at
   -> db.replace_document(did, old_doc)
   -> new_did = db.add_document(new_doc)
   -> return new_did

4. push LogEntry{ADD_TERM, did, term}
```

CRITICAL - Versioning Model: Khi thay doi document o block khac, he thong KHONG xoa old version
ma tao phien ban moi va danh dau old version bang slot 254 = deleted_at.
Dieu nay dam bao query tai bat ky blockNumber nao deu tra ve dung data.

---

#### XAPIAN_INDEX_TEXT_DOCUMENT
```
Input:  dbname, docId, text (string), weight (uint8), prefix (string)
Output: docId (uint256)

Flow giong ADD_TERM nhung dung Xapian::TermGenerator:
- TermGenerator.set_document(doc)
- TermGenerator.index_text(text, wdf_inc, prefix)
  -> tu dong tokenize, stem, them nhieu term vao document

Versioning logic: giong het ADD_TERM
```

---

#### XAPIAN_SET_DATA_DOCUMENT
```
Input:  dbname, docId, data (string)
Output: docId (uint256)

Flow:
- Versioning logic giong ADD_TERM
- Cung block -> old_doc.set_data(new_data); db.replace_document()
- Khac block -> clone, set_data tren clone, mark old with slot 254
```

---

#### XAPIAN_ADD_VALUE_DOCUMENT
```
Input:  dbname, docId, slot (uint256), data (string), isSerialise (bool)
Output: docId (uint256)

Flow:
- isSerialise=true -> Xapian::sortable_serialise(stod(value)) truoc khi luu
- Versioning logic giong ADD_TERM
- Slots 253, 254 la RESERVED (created_at, deleted_at)
```

---

#### XAPIAN_DELETE_DOCUMENT
```
Input:  dbname, docId (uint256)
Output: bool (uint256)

Flow:
1. getInstance() -> lock db_mutex -> begin_transaction() neu can
2. doc = db.get_document(did)
3. Soft delete: doc.add_value(254, serialise(blockNumber))
   <- danh dau "deleted at this block"
4. db.replace_document(did, doc)
   <- KHONG xoa cung, document van con trong DB
5. push LogEntry{DEL_DOC, docId}
```

Xapian khong xoa vat ly record. Document chi bi an boi filter slot 254 <= query_blockNumber.

---

## 4. Cơ Chế Versioning (Document History)

```
Block 100: new_document(data="foo") -> docId=1
   DB: [DocID=1 | data="foo" | slot253=100 | slot254=empty]

Block 102: add_term(docId=1, term="hello")  <- khac block!
   DB:
   [DocID=1 | data="foo" | slot253=100 | slot254=102]  <- old version
   [DocID=2 | data="foo" | slot253=102 | term="hello"] <- new version

Block 102: index_text(docId=2, text="world") <- cung block 102!
   DB (in-place update):
   [DocID=1 | slot254=102]
   [DocID=2 | slot253=102 | terms=["hello","world"]]  <- updated in-place
```

### Quy tắc Visibility khi query tại blockNumber B:
- Document VISIBLE: slot253 <= B AND (slot254 > B OR slot254 la empty)
- Document INVISIBLE: slot253 > B (chua ton tai) hoac slot254 <= B (da bi xoa)

---

## 5. Luồng COMMIT — Từ Smart Contract Đến Disk

### 5.1 Opcode XAPIAN_COMMIT (smart contract gọi)
```
Input:  dbname (string)
Output: status (uint256)

Flow trong xapian_handlers.cpp:
1. getInstance(dbname, address, isReset) -> manager
2. registry.registerManager(mvmId, manager)
3. hash = manager->getChangeHash()   <- tinh hash cua staged changes
4. log  = manager->getChangeLogs()   <- lay log entries
5. status = registry.commitChangesForMvmId(mvmId)
6. return status
```

### 5.2 registry.commitChangesForMvmId(mvmId)

```cpp
bool XapianRegistry::commitChangesForMvmId(unsigned char *mvmId) {
    managers = getManagersForMvmId(mvmId);

    for (manager : managers) {
        manager->saveAllAndCommit();  // <- core commit
    }

    unregisterAllManagersForMvmId(mvmId);
    // Xoa registration sau commit de tranh memory leak
    return all_succeeded;
}
```

### 5.3 manager->saveAllAndCommit() -> commit_changes()

```cpp
bool XapianManager::commit_changes() {
    lock(changes_mutex);

    if (comprehensive_log.xapian_doc_logs.empty()) {
        return true;  // Khong co gi de commit
    }

    db.commit();  // <- XAPIAN FLUSH TO DISK (diem duy nhat!)

    comprehensive_log.xapian_doc_logs.clear();
    // Xoa staged log sau commit thanh cong

    return true;
}
```

### 5.4 Toàn Bộ COMMIT Flow

```
Smart Contract
    | XAPIAN_COMMIT(dbname)
    v
FullDatabase() [xapian_handlers.cpp]
    |
    +-- XapianManager::getInstance()
    +-- registry.registerManager()
    +-- manager->getChangeHash()    -- tinh Keccak256 cua staged log
    +-- manager->getChangeLogs()    -- lay ban copy log
    |
    +-- registry.commitChangesForMvmId(mvmId)
            |
            +-- getManagersForMvmId(mvmId)
            |
            +-- for each manager:
            |       manager->saveAllAndCommit()
            |           +-- commit_changes()
            |               +-- lock(changes_mutex)
            |               +-- if log empty -> return true (no-op)
            |               +-- db.commit()  -------> DISK
            |               +-- clear(comprehensive_log)
            |
            +-- unregisterAllManagersForMvmId(mvmId)
```

---

## 6. Transaction Lifecycle (Begin / Commit / Cancel)

### 6.1 db.begin_transaction() — Khi nào?

Moi operation write dau tien khi has_started == false se tu dong goi:
```cpp
if (this->has_started == false) {
    db.begin_transaction();
    // has_started = true duoc set trong log entry
}
```

### 6.2 commitTransaction(mvmId) — MVM transaction commit

```cpp
void XapianRegistry::commitTransaction(unsigned char *mvmId) {
    for (manager : getManagersForMvmId(mvmId)) {
        if (manager->has_started) {
            manager->mvmCommitTransaction();  // db.commit_transaction()
            manager->has_started = false;
        }
    }
}
```

### 6.3 cancelTransaction(mvmId) — MVM rollback

```cpp
void XapianRegistry::cancelTransaction(unsigned char *mvmId) {
    for (manager : getManagersForMvmId(mvmId)) {
        if (manager->has_started) {
            manager->removeLogsUntilNearestEndCommand();  // trim log
            manager->mvmCancelTransaction();              // db.cancel_transaction()
            manager->has_started = false;
        }
    }
}
```

### 6.4 revertUncommittedChanges() — Hard revert

```cpp
bool XapianManager::revertUncommittedChanges() {
    lock(changes_mutex);
    comprehensive_log.xapian_doc_logs.clear();

    db.close();
    db = Xapian::WritableDatabase();  // clear object
    db = Xapian::WritableDatabase(    // mo lai tu disk (state cuoi commit)
        mvm::createFullPath(address, db_name).string(),
        Xapian::DB_OPEN
    );
    return true;
}
```

### 6.5 Sơ Đồ Transaction States

```
[new write opcode]
      |
      v
has_started = false?
   YES -> db.begin_transaction() -> has_started = true
      |
      v
[thuc hien thay doi] [push vao comprehensive_log]
      |
      +-- XAPIAN_COMMIT ---------> db.commit() -> DISK; log.clear()
      |
      +-- cancelTransaction() ---> db.cancel_transaction()
      |                            removeLogsUntilNearestEnd()
      |                            has_started = false
      |
      +-- revertUncommitted() ---> db.close() + reopen; log.clear()
```

---

## 7. Change Log System (comprehensive_log)

### 7.1 Cấu Trúc LogEntry

```cpp
struct LogEntry {
    Operation op;       // NEW_DOC | DEL_DOC | ADD_VALUE | ADD_TERM | SET_DATA | INDEX_TEXT
    CommandType command_type;  // START | END | NORMAL
    std::variant<
        std::monostate,
        NewDocData,     // {docid, data}
        DelDocData,     // {docid}
        AddValueData,   // {docid, slot, value, is_serialised}
        AddTermData,    // {docid, term}
        SetDataData,    // {docid, data}
        IndexTextData   // {docid, text, wdf_inc, prefix}
    > data;

    std::vector<uint8_t> serialize() const;
    static std::optional<LogEntry> deserialize(const std::vector<uint8_t>&);
};
```

### 7.2 Binary Serialization Format (Big Endian)

```
[1 byte: op code]
[N bytes: operation-specific data]

NewDoc:     [4B: docid] [4B: data_len] [N: data_bytes]
DelDoc:     [4B: docid]
AddValue:   [4B: docid] [4B: slot] [1B: is_serialised] [4B: val_len] [N: val]
AddTerm:    [4B: docid] [4B: term_len] [N: term]
SetData:    [4B: docid] [4B: data_len] [N: data]
IndexText:  [4B: docid] [4B: wdf_inc] [4B: prefix_len] [N: prefix] [4B: text_len] [N: text]
```

### 7.3 Change Hash (State Fingerprint)

```cpp
std::array<uint8_t, 32> XapianManager::getChangeHash() {
    for (entry : comprehensive_log.xapian_doc_logs) {
        bytes = entry.serialize();
        combined_bytes.append(bytes);
    }
    return keccak_256(combined_bytes);
}

// Registry tong hop hash theo tung address
std::map<mvm::Address, std::array<uint8_t,32>>
XapianRegistry::getGroupHashForMvmId(mvmId) {
    groups = groupManagersByAddress(getManagersForMvmId(mvmId));
    for ([addr, managers_in_group] : groups) {
        concat_hashes = {};
        for (m : managers_in_group)
            concat_hashes += m->getComprehensiveStateHash();  // keccak cua log + tags
        group_hash[addr] = keccak_256(concat_hashes);
    }
}
```

---

## 8. Luồng READ / Query

### 8.1 XAPIAN_QUERY_SEARCH — Bypass Manager!

```
Input: dbname, queries, prefixMap, offset, limit, sort, rangeFilters

Flow:
1. fullPath = createFullPath(address, dbName)
2. XapianSearcher searcher(fullPath)
   <- mo DB o che do READ-ONLY truc tiep tu disk!
3. results = searcher.search(queries, OP_AND, ...)
   filter: slot253 <= blockNumber AND (slot254 empty OR slot254 > blockNumber)
4. return encodeSearchResultsPage(total, results)
```

QUAN TRONG: XapianSearcher mo DB truc tiep tu filesystem path, khong qua WritableDatabase cua Manager.
- Search chi thay data da COMMIT xuong disk (db.commit() da duoc goi)
- Data chua commit (trong Xapian write buffer) KHONG VISIBLE voi search

### 8.2 Read Qua Manager (get_data, get_value, get_terms, get_document)

Cac opcode GET_DATA, GET_VALUE, GET_TERMS, GET_DOCUMENT di qua XapianManager::db (WritableDatabase)
-> CO THE thay uncommitted data.

Visibility check (tat ca read operations):
- slot254 empty OR slot254 > blockNumber  -> document con song
- slot253 empty OR slot253 <= blockNumber -> document da ton tai

---

## 9. Thread Safety

| Resource | Mutex | Scope |
|----------|-------|-------|
| Xapian::WritableDatabase db | db_mutex | toan bo operation doc/ghi |
| comprehensive_log | changes_mutex | push_back va read logs |
| XapianManager::instances | TBB concurrent_hash_map (lock-free) | getInstance |
| XapianRegistry::m_mvmId_to_managers | TBB concurrent_hash_map accessor | register/unregister |
| last_access_time | access_mutex | touch() / is_idle_for() |

---

## 10. Background Cleaner Thread

```cpp
// Chay trong namespace an danh (auto-start khi library load)
std::thread cleaner_thread([] {
    while (cleaner_running.load()) {
        sleep(1 minute);

        // Tim instance idle > 5 phut va khong co external references
        for (instance : XapianManager::instances) {
            if (instance.is_idle_for(5 min) && use_count <= 2) {
                keys_to_erase.push_back(instance.key);
            }
        }

        // Phase 2: destroy
        for (key : keys_to_erase) {
            XapianManager::destroyInstance(key);
            // db.close() + instances.erase()
        }
    }
});

// RAII: tu dong stop khi shutdown
struct CleanerStopper { ~CleanerStopper() { stopCleanerThread(); } } stopper;
```

---

## 11. Log Replay (Sub-node Sync)

```cpp
// Dung de apply lai log tu Master -> Sub node
bool XapianManager::replay_log(const vector<LogEntry>& log) {
    lock(db_mutex);
    lock(changes_mutex);

    for (entry : log) {
        std::visit([this](auto& data) {
            if (NEW_DOC):    db.replace_document(docid, doc_with_data)
            if (DEL_DOC):    db.delete_document(docid)   // hard delete khi replay
            if (ADD_VALUE):  fetch doc -> add_value -> replace
            if (ADD_TERM):   fetch doc -> add_term -> replace
            if (SET_DATA):   fetch doc -> set_data -> replace
            if (INDEX_TEXT): fetch doc -> TermGenerator.index_text -> replace
        }, entry.data);
    }
    // Chu y: replay KHONG goi commit()
    // caller phai goi commit_changes() sau do
}
```

---

## 12. Điểm Quan Trọng và Gotchas

**[IMPORTANT]** Commit chi xay ra khi smart contract goi XAPIAN_COMMIT. Cac opcode write khac chi ghi vao Xapian write buffer + staged log, CHUA flush xuong disk.

**[WARNING]** Search (XAPIAN_QUERY_SEARCH) dung XapianSearcher (read-only) — chi thay data da committed. Neu contract goi NEW_DOCUMENT roi ngay QUERY_SEARCH trong cung 1 transaction (chua commit), search se KHONG thay document moi.

**[NOTE]** Versioning model tao nhieu physical documents cho cung 1 logical entity. Moi modification o block khac tao 1 Xapian document moi. DB co the lon nhanh neu document bi update nhieu lan.

**[CAUTION]** Slot 253 va 254 la RESERVED. Slot 253 = created_at_block, Slot 254 = deleted_at_block. Smart contract KHONG duoc dung 2 slot nay.

**[TIP]** Trong cung 1 block, update se in-place. Dieu nay giup giam so luong physical documents khi nhieu thao tac xay ra trong cung block.

---

## 13. Tóm Tắt Flow End-to-End

```
[Block N bat dau]
        |
        | Smart contract goi XAPIAN operations:
        |
        +-- GET_OR_CREATE_DB("mydb")
        |     +-- XapianManager::getInstance() -> singleton created/fetched
        |     +-- registry.registerManager(mvmId, manager)
        |
        +-- NEW_DOCUMENT("mydb", data)
        |     +-- begin_transaction() [lan dau]
        |     +-- db.add_document(doc)       <- in Xapian write buffer
        |     +-- log.push(NEW_DOC entry)    <- staged log
        |
        +-- ADD_TERM("mydb", docId, "keyword")
        |     +-- version check -> in-place OR new version
        |     +-- db.replace_document(...)   <- in Xapian write buffer
        |     +-- log.push(ADD_TERM entry)
        |
        +-- INDEX_TEXT("mydb", docId, "full text", weight, prefix)
        |     +-- TermGenerator tokenize -> multiple terms
        |     +-- db.replace_document(...)   <- in Xapian write buffer
        |     +-- log.push(INDEX_TEXT entry)
        |
        +-- COMMIT("mydb")                   <- Smart contract commits!
              |
              +-- registry.commitChangesForMvmId(mvmId)
              |     +-- manager->saveAllAndCommit()
              |           +-- db.commit()    <--- FLUSH TO DISK
              |           +-- log.clear()
              |     +-- unregisterAllManagers()
              |
              | [Tu day QUERY_SEARCH thay data moi]
              v
        [Block N hoan tat]
```
