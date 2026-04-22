// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/**
 * @title TestFullDB
 * @notice Test toàn bộ luồng FullDB precompile (0x106) với data mẫu built-in.
 *
 * ┌─────────────────────────────────────────────────────────────────────┐
 * │  WORKFLOW NHANH (không cần nhập gì):                               │
 * │  1. runStep1_Setup()        → tạo DB + insert 3 sản phẩm mẫu      │
 * │  2. runStep2_ReadBack()     → đọc data/value/terms của docId[0]   │
 * │  3. runStep3_UpdateDoc()    → setData + addValue + addTerm         │
 * │  4. runStep4_IndexMore()    → indexText thêm cho docId[0]          │
 * │  5. runStep5_Search()       → simple search + querySearch          │
 * │  6. runStep6_SearchRange()  → querySearch với range filter giá     │
 * │  7. runStep7_DeleteAndCommit() → xóa docId[0]            │
 * │                                                                     │
 * │  Hoặc chỉ cần: runAllInOne() → chạy toàn bộ 1 lần                │
 * └─────────────────────────────────────────────────────────────────────┘
 *
 * State variables lưu kết quả:
 *   docIds[0..2]      → các docId đã tạo
 *   lastReadData      → data đọc về
 *   lastReadValue     → value slot 0 đọc về
 *   lastReadTerms     → terms list đọc về
 *   lastSearchRaw     → kết quả search() raw
 *   lastQueryTotal    → tổng kết quả querySearch
 *   lastQueryResults  → danh sách kết quả trang hiện tại
 */

// ─── Interface structs ───────────────────────────────────────────────────
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

// ─── Precompile Interface ────────────────────────────────────────────────
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
    function search(
        string memory dbname,
        string memory query
    ) external returns (string memory);
    function querySearch(
        string memory dbname,
        SearchParams memory params
    ) external returns (SearchResultsPage memory);
    function commit(string memory dbname) external returns (bool);
}

// ════════════════════════════════════════════════════════════════════════
contract TestFullDB {
    IFullDB constant fullDB =
        IFullDB(0x0000000000000000000000000000000000000106);
    constructor() {
        fullDB.getOrCreateDb(DB_NAME);
    }
    // ─── Built-in sample data ──────────────────────────────────────────
    string constant DB_NAME = "products_test_v1";
    uint8 constant TEXT_WEIGHT = 1;

    // Product 1 – Iphone 13 Pro
    string constant P1_DATA =
        '{"title":"Iphone 13 Pro","category":"electronics","brand":"apple","price":"999.99","discountPrice":"849.99","description":"Smart phone cao cap cua Apple, chip A15 Bionic"}';
    string constant P1_TEXT =
        "Iphone 13 Pro Smart phone cao cap chip A15 Bionic camera Pro";
    string constant P1_PRICE = "999.99";
    string constant P1_DISC = "849.99";

    // Product 2 – Samsung Galaxy S22
    string constant P2_DATA =
        '{"title":"Samsung Galaxy S22","category":"electronics","brand":"samsung","price":"799.00","discountPrice":"699.00","description":"Dien thoai Android Man hinh Dynamic AMOLED 2X"}';
    string constant P2_TEXT =
        "Samsung Galaxy S22 Dien thoai Android AMOLED camera 108MP";
    string constant P2_PRICE = "799.00";
    string constant P2_DISC = "699.00";

    // Product 3 – Macbook Pro
    string constant P3_DATA =
        '{"title":"Macbook Pro 14 inch","category":"electronics","brand":"apple","price":"1999.00","discountPrice":"1999.00","description":"Laptop manh me Chip M1 Pro man hinh Liquid Retina XDR"}';
    string constant P3_TEXT =
        "Macbook Pro 14 inch Laptop manh me Chip M1 Pro Liquid Retina XDR";
    string constant P3_PRICE = "1999.00";
    string constant P3_DISC = "1999.00";

    // ─── State lưu kết quả ────────────────────────────────────────────
    uint256[3] public docIds; // docId của 3 sản phẩm mẫu
    bool public isSetupDone;

    string public lastReadData;
    string public lastReadValue;
    string[] public lastReadTerms;
    bool public lastDeleteResult;
    bool public lastCommitResult;
    string public lastSearchRaw;
    uint256 public lastQueryTotal;
    SearchResult[] public lastQueryResults;

    // ─── Events ───────────────────────────────────────────────────────
    event Setup_DbCreated(string dbname);
    event Setup_DocCreated(uint256 index, uint256 docId, string title);
    event Setup_Committed(bool ok);

    event Read_Data(uint256 docId, string data);
    event Read_Value(uint256 docId, uint256 slot, string value);
    event Read_Terms(uint256 docId, uint256 termCount);

    event Update_SetData(
        uint256 inputDocId,
        uint256 returnedDocId,
        bool sameDoc
    );
    event Update_AddValue(
        uint256 inputDocId,
        uint256 returnedDocId,
        uint256 slot,
        string value,
        bool sameDoc
    );
    event Update_AddTerm(
        uint256 inputDocId,
        uint256 returnedDocId,
        string term,
        bool sameDoc
    );
    event Update_IndexText(
        uint256 inputDocId,
        uint256 returnedDocId,
        string prefix,
        bool sameDoc
    );

    event Search_Raw(string query, string result);
    event Search_Query(string query, uint256 total, uint256 pageCount);
    event Search_Item(uint256 docid, uint256 rank, int256 percent, string data);

    event Delete_Doc(uint256 docId, bool ok);
    event Commit_Done(bool ok);

    event Debug_DocId(string msg, uint256 docId);

    // ════════════════════════════════════════════════════════════════
    // ① SETUP: Tạo DB + insert 3 sản phẩm mẫu
    // Gọi 1 lần duy nhất khi khởi động
    // ════════════════════════════════════════════════════════════════
    function runStep1_Setup() public {
        // Auto-reset nếu đã setup rồi (tiện hơn phải gọi Reset riêng)
        isSetupDone = false;
        docIds[0] = 0;
        docIds[1] = 0;
        docIds[2] = 0;
        _setupInternal();
    }

    /// Reset và setup lại từ đầu (alias cho runStep1_Setup)
    function runStep1_Reset() public {
        isSetupDone = false;
        docIds[0] = 0;
        docIds[1] = 0;
        docIds[2] = 0;
        _setupInternal();
    }

    function _setupInternal() private {
        // Tạo/mở DB
        fullDB.getOrCreateDb(DB_NAME);
        emit Setup_DbCreated(DB_NAME);

        // Insert product 1
        docIds[0] = _insertProduct(
            P1_DATA,
            P1_TEXT,
            P1_PRICE,
            P1_DISC,
            "apple",
            "electronics",
            "black",
            "bestseller"
        );
        emit Setup_DocCreated(0, docIds[0], "Iphone 13 Pro");

        // Insert product 2
        docIds[1] = _insertProduct(
            P2_DATA,
            P2_TEXT,
            P2_PRICE,
            P2_DISC,
            "samsung",
            "electronics",
            "white",
            "android"
        );
        emit Setup_DocCreated(1, docIds[1], "Samsung Galaxy S22");

        // Insert product 3
        docIds[2] = _insertProduct(
            P3_DATA,
            P3_TEXT,
            P3_PRICE,
            P3_DISC,
            "apple",
            "electronics",
            "silver",
            "professional"
        );
        emit Setup_DocCreated(2, docIds[2], "Macbook Pro 14");
    }

    /// Helper: Insert 1 sản phẩm đầy đủ (data, text, terms, values)
    function _insertProduct(
        string memory jsonData,
        string memory fullText,
        string memory price,
        string memory discountPrice,
        string memory brand, // term B:brand
        string memory category, // term C:category
        string memory color, // term CO:color
        string memory filter_tag // term F:filter
    ) private returns (uint256 docId) {
        // Tạo document
        docId = fullDB.newDocument(DB_NAME, jsonData);
        emit Debug_DocId("After newDocument", docId);
        require(docId > 0, "newDocument failed");

        // Index text: title prefix T, general
        uint256 d;
        d = fullDB.indexTextForDocument(
            DB_NAME,
            docId,
            fullText,
            TEXT_WEIGHT,
            "T"
        );
        docId = (d > 0) ? d : docId;
        d = fullDB.indexTextForDocument(
            DB_NAME,
            docId,
            fullText,
            TEXT_WEIGHT,
            ""
        );
        emit Debug_DocId("After indexTextForDocument", d);
        docId = (d > 0) ? d : docId;

        // addTerm: brand, category, color, filter
        d = fullDB.addTermDocument(
            DB_NAME,
            docId,
            string(abi.encodePacked("B:", brand))
        );
        docId = (d > 0) ? d : docId;
        d = fullDB.addTermDocument(
            DB_NAME,
            docId,
            string(abi.encodePacked("C:", category))
        );
        emit Debug_DocId("After addTermDocument C", d);
        docId = (d > 0) ? d : docId;
        d = fullDB.addTermDocument(
            DB_NAME,
            docId,
            string(abi.encodePacked("CO:", color))
        );
        docId = (d > 0) ? d : docId;
        d = fullDB.addTermDocument(
            DB_NAME,
            docId,
            string(abi.encodePacked("F:", filter_tag))
        );
        docId = (d > 0) ? d : docId;

        // addValue: slot 0 = price, slot 1 = discount
        d = fullDB.addValueDocument(DB_NAME, docId, 0, price, true);
        docId = (d > 0) ? d : docId;
        d = fullDB.addValueDocument(DB_NAME, docId, 1, discountPrice, true);
        emit Debug_DocId("After addValueDocument slot 1", d);
        docId = (d > 0) ? d : docId;
    }

    // ════════════════════════════════════════════════════════════════
    // ② READ BACK: Đọc data/value/terms của docId[0]
    // ════════════════════════════════════════════════════════════════
    function runStep2_ReadBack() public {
        uint256 docId = docIds[0];
        require(docId > 0, "Run step1 first");

        string memory d = fullDB.getDataDocument(DB_NAME, docId);
        lastReadData = d;
        emit Read_Data(docId, d);

        string memory v = fullDB.getValueDocument(DB_NAME, docId, 0, true);
        lastReadValue = v;
        emit Read_Value(docId, 0, v);

        string[] memory t = fullDB.getTermsDocument(DB_NAME, docId);
        delete lastReadTerms;
        for (uint i = 0; i < t.length; i++) lastReadTerms.push(t[i]);
        emit Read_Terms(docId, t.length);
    }

    // ════════════════════════════════════════════════════════════════
    // ③ UPDATE: setData + addValue mới + addTerm mới cho docId[0]
    // ════════════════════════════════════════════════════════════════
    function runStep3_UpdateDoc() public {
        uint256 docId = docIds[0];
        require(docId > 0, "Run step1 first");

        // setData
        string
            memory updatedData = '{"title":"Iphone 13 Pro UPDATED","category":"electronics","brand":"apple","price":"899.99","note":"flash sale"}';
        uint256 d = fullDB.setDataDocument(DB_NAME, docId, updatedData);
        emit Update_SetData(docId, d, d == docId);
        docId = (d > 0) ? d : docId;

        // addValue slot 2 (rating)
        d = fullDB.addValueDocument(DB_NAME, docId, 2, "4.8", true);
        emit Update_AddValue(docId, d, 2, "4.8", d == docId);
        docId = (d > 0) ? d : docId;

        // addTerm thêm tag flash sale
        d = fullDB.addTermDocument(DB_NAME, docId, "F:flashsale");
        emit Update_AddTerm(docId, d, "F:flashsale", d == docId);
        docId = (d > 0) ? d : docId;

        // Cập nhật lại docIds[0] với docId mới nhất
        docIds[0] = docId;
    }

    // ════════════════════════════════════════════════════════════════
    // ④ INDEX MORE: indexText bổ sung cho docId[0]
    // ════════════════════════════════════════════════════════════════
    function runStep4_IndexMore() public {
        uint256 docId = docIds[0];
        require(docId > 0, "Run step1 first");

        string memory extraText = "flash sale giam gia manh dien thoai Apple";

        uint256 d = fullDB.indexTextForDocument(
            DB_NAME,
            docId,
            extraText,
            TEXT_WEIGHT,
            "T"
        );
        emit Update_IndexText(docId, d, "T", d == docId);
        docId = (d > 0) ? d : docId;

        d = fullDB.indexTextForDocument(
            DB_NAME,
            docId,
            extraText,
            TEXT_WEIGHT,
            ""
        );
        emit Update_IndexText(docId, d, "", d == docId);
        docId = (d > 0) ? d : docId;

        docIds[0] = docId;
    }

    // ════════════════════════════════════════════════════════════════
    // ⑤ SEARCH: simple search + querySearch
    // ════════════════════════════════════════════════════════════════

    /// Simple search (trả về raw JSON string)
    function runStep5a_Search(string memory query) public {
        // XAPIAN_SEARCH (search method) hiện chưa implement trong code C++ my_extension.cpp
        // Do đó luôn trả về rỗng. Chúng tôi chuyển hướng nó gọi sang querySearch (đã được implement mới)
        runStep5b_QuerySearch(query);
    }

    /// querySearch với prefix map đầy đủ, sắp xếp theo giá tăng dần
    function runStep5b_QuerySearch(
        string memory query
    ) public returns (uint256 total, uint256 count) {
        SearchParams memory params = _buildDefaultParams(query);
        SearchResultsPage memory page = fullDB.querySearch(DB_NAME, params);

        lastQueryTotal = page.total;
        delete lastQueryResults;
        for (uint i = 0; i < page.results.length; i++) {
            lastQueryResults.push(page.results[i]);
            emit Search_Item(
                page.results[i].docid,
                page.results[i].rank,
                page.results[i].percent,
                page.results[i].data
            );
        }
        emit Search_Query(query, page.total, page.results.length);
        return (page.total, page.results.length);
    }

    // ════════════════════════════════════════════════════════════════
    // ⑥ SEARCH RANGE: lọc theo khoảng giá (slot 0)
    // ════════════════════════════════════════════════════════════════
    function runStep6_SearchRange(
        string memory minPrice,
        string memory maxPrice
    ) public returns (uint256 total, uint256 count) {
        PrefixEntry[] memory prefixMap = _buildPrefixMap();
        string[] memory stopWords = _buildStopWords();

        RangeFilter[] memory ranges = new RangeFilter[](1);
        ranges[0] = RangeFilter(0, minPrice, maxPrice);

        SearchParams memory params = SearchParams({
            queries: "",
            prefixMap: prefixMap,
            stopWords: stopWords,
            offset: 0,
            limit: 20,
            sortByValueSlot: 0,
            sortAscending: true,
            rangeFilters: ranges
        });

        SearchResultsPage memory page = fullDB.querySearch(DB_NAME, params);
        lastQueryTotal = page.total;
        delete lastQueryResults;
        for (uint i = 0; i < page.results.length; i++) {
            lastQueryResults.push(page.results[i]);
            emit Search_Item(
                page.results[i].docid,
                page.results[i].rank,
                page.results[i].percent,
                page.results[i].data
            );
        }
        string memory label = string(
            abi.encodePacked("price:", minPrice, "-", maxPrice)
        );
        emit Search_Query(label, page.total, page.results.length);
        return (page.total, page.results.length);
    }

    // ════════════════════════════════════════════════════════════════
    // ⑦ DELETE + COMMIT
    // ════════════════════════════════════════════════════════════════
    function runStep7_DeleteAndCommit() public {
        uint256 docId = docIds[0];
        require(docId > 0, "Run step1 first");

        bool ok = fullDB.deleteDocument(DB_NAME, docId);
        lastDeleteResult = ok;
        emit Delete_Doc(docId, ok);
    }

    // ════════════════════════════════════════════════════════════════
    // ⑧ ALL IN ONE: chạy toàn bộ pipeline (setup → search → delete)
    // ════════════════════════════════════════════════════════════════
    function runAllInOne() public {
        // Reset state
        isSetupDone = false;

        // Step 1: Setup
        _setupInternal();

        // Step 2: Read back docId[0]
        {
            uint256 docId = docIds[0];

            // Xả RAM -> HDD trước khi cho phép hàm Read truy xuất snapshot
            string memory d = fullDB.getDataDocument(DB_NAME, docId);
            lastReadData = d;
            emit Read_Data(docId, d);

            string memory v = fullDB.getValueDocument(DB_NAME, docId, 0, true);
            lastReadValue = v;
            emit Read_Value(docId, 0, v);

            string[] memory t = fullDB.getTermsDocument(DB_NAME, docId);
            delete lastReadTerms;
            for (uint i = 0; i < t.length; i++) lastReadTerms.push(t[i]);
            emit Read_Terms(docId, t.length);
        }

        // Step 3: Update docId[0]
        {
            uint256 docId = docIds[0];
            string
                memory newData = '{"title":"Iphone 13 Pro UPDATED","note":"flash sale"}';
            uint256 d = fullDB.setDataDocument(DB_NAME, docId, newData);
            emit Update_SetData(docId, d, d == docId);
            docId = (d > 0) ? d : docId;

            d = fullDB.addTermDocument(DB_NAME, docId, "F:flashsale");
            emit Update_AddTerm(docId, d, "F:flashsale", d == docId);
            docId = (d > 0) ? d : docId;

            d = fullDB.indexTextForDocument(
                DB_NAME,
                docId,
                "flash sale Apple giam gia",
                TEXT_WEIGHT,
                ""
            );
            emit Update_IndexText(docId, d, "", d == docId);
            docId = (d > 0) ? d : docId;

            docIds[0] = docId;
        }
        // Step 6: Delete docId[0]
        // {
        //     bool ok = fullDB.deleteDocument(DB_NAME, docIds[0]);
        //     lastDeleteResult = ok;
        //     emit Delete_Doc(docIds[0], ok);
        // }
    }

    // ════════════════════════════════════════════════════════════════
    // ⑨ QUICK DEBUG TEST: Insert -> Commit -> Get (Để chắc chắn)
    // ════════════════════════════════════════════════════════════════
    function testDebugFullCycleGet() public {
        // Tái tạo hoặc mở
        fullDB.getOrCreateDb(DB_NAME);

        // Tạo document nhanh
        string memory data = '{"test":"hello xapian"}';
        uint256 dId = fullDB.newDocument(DB_NAME, data);
        require(dId > 0, "Insert err");

        fullDB.addValueDocument(DB_NAME, dId, 0, "500", true);

        // Thử Get
        string memory fetchedData = fullDB.getDataDocument(DB_NAME, dId);
        string memory fetchedValue = fullDB.getValueDocument(
            DB_NAME,
            dId,
            0,
            true
        );

        emit Read_Data(dId, fetchedData);
        emit Read_Value(dId, 0, fetchedValue);
    }

    // ════════════════════════════════════════════════════════════════
    // ⑩ TEST LEAK CHO ETH_CALL
    // ════════════════════════════════════════════════════════════════
    function testLeak_Create() public returns (uint256) {
        fullDB.getOrCreateDb("test_leak_db");
        uint256 dId = fullDB.newDocument(
            "test_leak_db",
            "BI_MAT_KHONG_DUOC_LUU"
        );
        return dId;
    }

    function testLeak_Read(uint256 dId) public returns (string memory) {
        return fullDB.getDataDocument("test_leak_db", dId);
    }

    // ════════════════════════════════════════════════════════════════
    // HELPERS đọc state
    // ════════════════════════════════════════════════════════════════
    function getDocIds() public view returns (uint256, uint256, uint256) {
        return (docIds[0], docIds[1], docIds[2]);
    }

    function getLastReadTermsLength() public view returns (uint256) {
        return lastReadTerms.length;
    }

    function getLastReadTerm(uint256 idx) public view returns (string memory) {
        return lastReadTerms[idx];
    }

    function getLastQueryResultsLength() public view returns (uint256) {
        return lastQueryResults.length;
    }

    function getLastQueryResult(
        uint256 idx
    )
        public
        view
        returns (
            uint256 docid,
            uint256 rank,
            int256 percent,
            string memory data
        )
    {
        SearchResult memory r = lastQueryResults[idx];
        return (r.docid, r.rank, r.percent, r.data);
    }

    // ════════════════════════════════════════════════════════════════
    // PRIVATE HELPERS
    // ════════════════════════════════════════════════════════════════
    function _buildPrefixMap() private pure returns (PrefixEntry[] memory) {
        PrefixEntry[] memory m = new PrefixEntry[](10);
        m[0] = PrefixEntry("title", "T");
        m[1] = PrefixEntry("T", "T");
        m[2] = PrefixEntry("category", "C");
        m[3] = PrefixEntry("C", "C");
        m[4] = PrefixEntry("brand", "B");
        m[5] = PrefixEntry("B", "B");
        m[6] = PrefixEntry("color", "CO");
        m[7] = PrefixEntry("CO", "CO");
        m[8] = PrefixEntry("filter", "F");
        m[9] = PrefixEntry("F", "F");
        return m;
    }

    function _buildStopWords() private pure returns (string[] memory) {
        string[] memory sw = new string[](3);
        sw[0] = "the";
        sw[1] = "of";
        sw[2] = "and";
        return sw;
    }

    function _buildDefaultParams(
        string memory query
    ) private pure returns (SearchParams memory) {
        RangeFilter[] memory ranges = new RangeFilter[](0);
        return
            SearchParams({
                queries: query,
                prefixMap: _buildPrefixMap(),
                stopWords: _buildStopWords(),
                offset: 0,
                limit: 20,
                sortByValueSlot: 0, // sắp theo price tăng dần
                sortAscending: true,
                rangeFilters: ranges
            });
    }
}
