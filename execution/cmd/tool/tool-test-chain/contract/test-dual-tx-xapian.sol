// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/**
 * @title DualTxXapianTest
 * @notice Contract dùng để kiểm chứng vấn đề "2 tx cùng mvmId trong 1 block chỉ commit 1 lần".
 *
 * ═══════════════════════════════════════════════════════════════════
 * VẤN ĐỀ CẦN KIỂM CHỨNG:
 *   Trong block_processor_commit.go, nếu 2 giao dịch có cùng ToAddress
 *   (cùng mvmId = contract address), chỉ giao dịch đầu tiên được xử lý:
 *
 *      if processedMvmIds[mvmId] { continue }  ← giao dịch 2 bị skip!
 *      processedMvmIds[mvmId] = true
 *      mvmAPI.CommitFullDb()  ← chỉ commit data của tx1
 *
 *   → Câu hỏi: data của giao dịch tx2 (write riêng vào FullDB) có bị mất không?
 *
 * ═══════════════════════════════════════════════════════════════════
 * CÁCH SỬ DỤNG (2 bước):
 *
 *   BƯỚC 1 - SETUP: Gọi hàm này 1 lần để init DB (1 tx riêng):
 *     → initDb()
 *
 *   BƯỚC 2 - TEST: Gửi 2 tx VÀO CÙNG 1 BLOCK (nonce liên tiếp, không chờ receipt):
 *     → tx1: insertDoc(1001, "data_tu_TX1_Iphone_Pro")
 *     → tx2: insertDoc(1002, "data_tu_TX2_Samsung_Galaxy")
 *
 *   BƯỚC 3 - VERIFY: Đọc lại sau khi block được commit:
 *     → readDoc(1001)  → phải trả về "data_tu_TX1_Iphone_Pro"
 *     → readDoc(1002)  → phải trả về "data_tu_TX2_Samsung_Galaxy"
 *                        ↑ Nếu trả về rỗng "" → BUG CONFIRMED!
 *
 * ═══════════════════════════════════════════════════════════════════
 */

// Structs cần cho querySearch (không dùng trong test này nhưng để interface đầy đủ)
struct PrefixEntry {
    string key;
    string value;
}
struct RangeFilter {
    uint slot;
    string startSerialised;
    string endSerialised;
}
struct SearchParams {
    string queries;
    PrefixEntry[] prefixMap;
    string[] stopWords;
    uint64 offset;
    uint64 limit;
    int64 sortByValueSlot;
    bool sortAscending;
    RangeFilter[] rangeFilters;
}
struct SearchResult {
    uint256 docid;
    uint256 rank;
    int256 percent;
    string data;
}
struct SearchResultsPage {
    uint256 total;
    SearchResult[] results;
}

interface IFullDB {
    function getOrCreateDb(string memory name) external returns (bool);
    function newDocument(
        string memory dbname,
        string memory data
    ) external returns (uint256);
    function getDataDocument(
        string memory dbname,
        uint256 docId
    ) external returns (string memory);
    function setDataDocument(
        string memory dbname,
        uint256 docId,
        string memory data
    ) external returns (uint256);
    function deleteDocument(
        string memory dbname,
        uint256 docId
    ) external returns (bool);
    function addTermDocument(
        string memory dbname,
        uint256 docId,
        string memory term
    ) external returns (uint256);
    function indexTextForDocument(
        string memory dbname,
        uint256 docId,
        string memory text,
        uint8 weight,
        string memory prefix
    ) external returns (uint256);
    function addValueDocument(
        string memory dbname,
        uint256 docId,
        uint256 slot,
        string memory data,
        bool isSerialise
    ) external returns (uint256);
    function getValueDocument(
        string memory dbname,
        uint256 docId,
        uint256 slot,
        bool isSerialise
    ) external returns (string memory);
    function getTermsDocument(
        string memory dbname,
        uint256 docId
    ) external returns (string[] memory);
    function querySearch(
        string memory dbname,
        SearchParams memory params
    ) external returns (SearchResultsPage memory);
    function commit(string memory dbname) external returns (bool);
}

contract DualTxXapianTest {
    IFullDB constant fullDB =
        IFullDB(0x0000000000000000000000000000000000000106);

    string constant DB_NAME = "dual_tx_test_v1";

    // Lưu docId thực sự được xapian assign (auto-increment, không phải input docId)
    // mapping: slot → docId thực
    // slot 0 = reserved cho doc từ tx1, slot 1 = reserved cho doc từ tx2
    mapping(uint256 => uint256) public realDocIds;

    // Events để trace
    event DbInitialized(string dbName);
    event DocInserted(uint256 inputSlot, uint256 realDocId, string data);
    event DocRead(uint256 realDocId, string data, bool isEmpty);
    event VerifyResult(string label, bool passed);

    // ═══════════════════════════════════════════════════════════════
    // BƯỚC 1: Init DB (gọi 1 lần trong 1 tx riêng)
    // ═══════════════════════════════════════════════════════════════
    function initDb() external {
        fullDB.getOrCreateDb(DB_NAME);
        emit DbInitialized(DB_NAME);
    }

    // ═══════════════════════════════════════════════════════════════
    // BƯỚC 2: Mỗi tx gọi hàm này 1 lần
    //   slot: 0 = tx1, 1 = tx2 (để phân biệt)
    //   data: chuỗi JSON sẽ lưu vào FullDB
    // ═══════════════════════════════════════════════════════════════
    function insertDoc(uint256 slot, string calldata data) external {
        uint256 docId = fullDB.newDocument(DB_NAME, data);
        require(docId > 0, "newDocument failed");

        // Index thêm để dễ search sau
        fullDB.indexTextForDocument(DB_NAME, docId, data, 1, "");

        // Lưu mapping slot → docId thực
        realDocIds[slot] = docId;

        emit DocInserted(slot, docId, data);
    }

    // ═══════════════════════════════════════════════════════════════
    // BƯỚC 3: Đọc lại để verify (gọi sau khi block đã commit)
    // ═══════════════════════════════════════════════════════════════
    function readDoc(uint256 slot) external returns (string memory data) {
        uint256 docId = realDocIds[slot];
        require(docId > 0, "No docId for this slot");

        data = fullDB.getDataDocument(DB_NAME, docId);
        bool isEmpty = bytes(data).length == 0;

        emit DocRead(docId, data, isEmpty);
        return data;
    }

    // ═══════════════════════════════════════════════════════════════
    // VERIFY ALL: Kiểm tra cả 2 slot đều có data
    // ═══════════════════════════════════════════════════════════════
    function verifyBothSlots(
        string calldata expectedData0,
        string calldata expectedData1
    ) external returns (bool slot0Ok, bool slot1Ok) {
        uint256 docId0 = realDocIds[0];
        uint256 docId1 = realDocIds[1];

        // Đọc data thực tế
        string memory actual0 = docId0 > 0
            ? fullDB.getDataDocument(DB_NAME, docId0)
            : "";
        string memory actual1 = docId1 > 0
            ? fullDB.getDataDocument(DB_NAME, docId1)
            : "";

        // So sánh (dùng keccak256 vì Solidity không có string ==)
        slot0Ok = (keccak256(bytes(actual0)) ==
            keccak256(bytes(expectedData0)));
        slot1Ok = (keccak256(bytes(actual1)) ==
            keccak256(bytes(expectedData1)));

        emit DocRead(docId0, actual0, bytes(actual0).length == 0);
        emit DocRead(docId1, actual1, bytes(actual1).length == 0);

        emit VerifyResult("slot0_tx1_data", slot0Ok);
        emit VerifyResult("slot1_tx2_data", slot1Ok);

        return (slot0Ok, slot1Ok);
    }

    // Helper: lấy realDocId
    function getDocId(uint256 slot) external view returns (uint256) {
        return realDocIds[slot];
    }
}
