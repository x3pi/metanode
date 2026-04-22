// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/**
 * @title TestFullDBV1
 * @notice Test toàn bộ luồng FullDB precompile (0x106) với data mẫu built-in cho VERSION 1.
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

// ProductData dùng chung cho interface + contract
struct ProductData {
    string name;
    string category;
    string brand;
    uint256 price;
    uint256 discount;
    bool isActive;
    string description;
}

struct SearchResult {
    uint256 docid;
    uint256 rank;
    int256 percent;
    bytes data; // Trở lại dùng chuẩn bytes
}

struct SearchResultsPage {
    uint256 total;
    SearchResult[] results;
}

// ─── Precompile Interface (V1) ────────────────────────────────────────────
interface IFullDBV1 {
    function getOrCreateDb(string memory name) external returns (bool);
    function newDocument(
        string memory dbname,
        bytes memory data
    ) external returns (uint256);
    function getDataDocument(
        string memory dbname,
        uint256 docId
    ) external returns (bytes memory);
    function setDataDocument(
        string memory dbname,
        uint256 docId,
        bytes memory data
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
}

interface IFullDBV1_ReadOnly {
    function querySearch(
        string memory dbname,
        SearchParams memory params
    ) external view returns (SearchResultsPage memory);
}

// ════════════════════════════════════════════════════════════════════════
contract TestFullDBV1 {
    // 0x107 is the address for FULL_DATABASE_ADDRESS_V1.
    IFullDBV1 constant fullDB =
        IFullDBV1(0x0000000000000000000000000000000000000107);

    constructor() {
        fullDB.getOrCreateDb(DB_NAME);
    }

    string constant DB_NAME = "products_test_v1_version1";
    uint8 constant TEXT_WEIGHT = 1;

    // Đối với version 1, data lưu trong DB là ABI-encoded tuples.
    bytes P1_DATA =
        abi.encode(
            ProductData(
                "Iphone 13 Pro",
                "electronics",
                "apple",
                99999,
                84999,
                true,
                "Smart phone cao cap cua Apple, chip A15 Bionic"
            )
        );
    string constant P1_TEXT =
        "Iphone 13 Pro Smart phone cao cap chip A15 Bionic camera Pro";
    string constant P1_PRICE = "99999";
    string constant P1_DISC = "84999";

    bytes P2_DATA =
        abi.encode(
            ProductData(
                "Samsung Galaxy S22",
                "electronics",
                "samsung",
                79900,
                69900,
                true,
                "Dien thoai Android Man hinh Dynamic AMOLED 2X"
            )
        );
    string constant P2_TEXT =
        "Samsung Galaxy S22 Dien thoai Android AMOLED camera 108MP";
    string constant P2_PRICE = "79900";
    string constant P2_DISC = "69900";

    bytes P3_DATA =
        abi.encode(
            ProductData(
                "Macbook Pro 14 inch",
                "electronics",
                "apple",
                199900,
                199900,
                true,
                "Laptop manh me Chip M1 Pro man hinh Liquid Retina XDR"
            )
        );
    string constant P3_TEXT =
        "Macbook Pro 14 inch Laptop manh me Chip M1 Pro Liquid Retina XDR";
    string constant P3_PRICE = "199900";
    string constant P3_DISC = "199900";

    // ─── State lưu kết quả ────────────────────────────────────────────
    uint256[3] public docIds;
    bool public isSetupDone;

    bytes public lastReadData;
    ProductData public lastReadProduct;
    string public lastReadValue;
    string[] public lastReadTerms;
    bool public lastDeleteResult;
    bool public lastCommitResult;
    uint256 public lastQueryTotal;
    SearchResult[] public lastQueryResults;

    // ─── Events ───────────────────────────────────────────────────────
    event Setup_DbCreated(string dbname);
    event Setup_DocCreated(uint256 index, uint256 docId, string title);
    event Read_Data_Ext(uint256 docId, bytes data, ProductData productInfo);
    event Test_Math(uint256 base, uint256 calculated);
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

    event Search_Query(string query, uint256 total, uint256 pageCount);
    event Search_Item_Ext(
        uint256 docid,
        uint256 rank,
        int256 percent,
        bytes data,
        ProductData productInfo
    );

    event Delete_Doc(uint256 docId, bool ok);
    event Debug_DocId(string msg, uint256 docId);

    // ════════════════════════════════════════════════════════════════
    function runStep1_Setup() public {
        isSetupDone = false;
        docIds[0] = 0;
        docIds[1] = 0;
        docIds[2] = 0;
        _setupInternal();
        isSetupDone = true;
    }

    function _setupInternal() private {
        fullDB.getOrCreateDb(DB_NAME);
        emit Setup_DbCreated(DB_NAME);

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

    function _insertProduct(
        bytes memory rawData,
        string memory fullText,
        string memory price,
        string memory discountPrice,
        string memory brand,
        string memory category,
        string memory color,
        string memory filter_tag
    ) private returns (uint256 docId) {
        // Truyền thẳng bytes memory vào precompile
        docId = fullDB.newDocument(DB_NAME, rawData);
        require(docId > 0, "newDocument failed");

        fullDB.indexTextForDocument(DB_NAME, docId, fullText, TEXT_WEIGHT, "T");
        fullDB.indexTextForDocument(DB_NAME, docId, fullText, TEXT_WEIGHT, "");

        fullDB.addTermDocument(
            DB_NAME,
            docId,
            string(abi.encodePacked("B:", brand))
        );
        fullDB.addTermDocument(
            DB_NAME,
            docId,
            string(abi.encodePacked("C:", category))
        );
        fullDB.addTermDocument(
            DB_NAME,
            docId,
            string(abi.encodePacked("CO:", color))
        );
        fullDB.addTermDocument(
            DB_NAME,
            docId,
            string(abi.encodePacked("F:", filter_tag))
        );

        fullDB.addValueDocument(DB_NAME, docId, 0, price, true);
        fullDB.addValueDocument(DB_NAME, docId, 1, discountPrice, true);
    }

    // ════════════════════════════════════════════════════════════════
    function runStep2_ReadBack() public {
        uint256 docId = docIds[0];
        require(docId > 0, "Run step1 first");

        // GET DATA chuẩn bytes, tự gọi abi.decode
        bytes memory rawBytes = fullDB.getDataDocument(DB_NAME, docId);
        ProductData memory product = abi.decode(rawBytes, (ProductData));

        lastReadProduct = product;
        lastReadData = rawBytes;
        uint256 vatTax = (product.price * 10) / 100;
        emit Test_Math(product.price, vatTax);

        emit Read_Data_Ext(docId, lastReadData, product);

        string memory v = fullDB.getValueDocument(DB_NAME, docId, 0, true);
        lastReadValue = v;
        emit Read_Value(docId, 0, v);

        string[] memory t = fullDB.getTermsDocument(DB_NAME, docId);
        delete lastReadTerms;
        for (uint i = 0; i < t.length; i++) lastReadTerms.push(t[i]);
        emit Read_Terms(docId, t.length);
    }

    // ════════════════════════════════════════════════════════════════
    function runStep3_UpdateDoc() public {
        uint256 docId = docIds[0];
        require(docId > 0, "Run step1 first");

        bytes memory newData = abi.encode(
            ProductData(
                "Iphone 13 Pro UPDATED",
                "electronics",
                "apple",
                89999,
                84999,
                false,
                "Flash sale da ket thuc"
            )
        );

        uint256 newDocId = fullDB.setDataDocument(DB_NAME, docId, newData);

        emit Update_SetData(docId, newDocId, newDocId == docId);
        docId = (newDocId > 0) ? newDocId : docId;

        uint256 d = fullDB.addValueDocument(DB_NAME, docId, 2, "4.8", true);
        docId = (d > 0) ? d : docId;

        d = fullDB.addTermDocument(DB_NAME, docId, "F:flashsale");
        docId = (d > 0) ? d : docId;

        docIds[0] = docId;
    }

    // Hàm commit đã được ẩn tự động trong EVM/MVM

    // ════════════════════════════════════════════════════════════════
    function runStep5b_QuerySearch(
        string memory query
    ) public returns (uint256 total, uint256 count) {
        SearchParams memory params = _buildDefaultParams(query);

        SearchResultsPage memory page = fullDB.querySearch(DB_NAME, params);

        lastQueryTotal = page.total;
        delete lastQueryResults;
        for (uint i = 0; i < page.results.length; i++) {
            lastQueryResults.push(page.results[i]);

            // Standard bytes, sử dụng abi.decode
            bytes memory rawBytes = page.results[i].data;
            ProductData memory parsedData;
            if (rawBytes.length > 0) {
                parsedData = abi.decode(rawBytes, (ProductData));
            }

            emit Search_Item_Ext(
                page.results[i].docid,
                page.results[i].rank,
                page.results[i].percent,
                rawBytes,
                parsedData
            );
        }
        emit Search_Query(query, page.total, page.results.length);
        return (page.total, page.results.length);
    }

    // ════════════════════════════════════════════════════════════════
    // TEST ETH_CALL (READ-ONLY) - KHÔNG TỐN GAS
    // ════════════════════════════════════════════════════════════════

    // Cách 1: Gọi qua Interface cục bộ có chữ "view"
    function testCallNormal_Search(string memory query) public view returns (SearchResultsPage memory) {
        SearchParams memory params = _buildDefaultParams(query);
        // Ép kiểu địa chỉ sang Interface ReadOnly
        IFullDBV1_ReadOnly dbReadOnly = IFullDBV1_ReadOnly(address(fullDB));
        return dbReadOnly.querySearch(DB_NAME, params);
    }

    // Cách 2: Gọi bằng low-level .staticcall
    function testCallLowLevel_Search(string memory query) public view returns (SearchResultsPage memory) {
        SearchParams memory params = _buildDefaultParams(query);

        bytes memory payload = abi.encodeWithSignature(
            "querySearch(string,(string,(string,string)[],string[],uint64,uint64,int64,bool,(uint256,string,string)[]))",
            DB_NAME, 
            params
        );

        (bool success, bytes memory returnData) = address(fullDB).staticcall(payload);
        require(success, "Low-level staticcall failed");

        return abi.decode(returnData, (SearchResultsPage));
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
