// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "./DBStorage.sol";
import {
    SearchResultsPage,
    SearchParams,
    PrefixEntry,
    RangeFilter,
    SearchResult
} from "./DBStorage.sol";

interface IFullDBRaw {
    function getTermsDocument(
        string memory dbname,
        uint256 docId
    ) external returns (string[] memory);

    function search(
        string memory dbname,
        string memory query
    ) external returns (string memory);
}

// ============================================================
//  HEVO PRODUCT DB  — ĐÃ SỬA LỖI INDEXING
//  Tuân theo đúng pattern của PublicfullDB
// ============================================================

contract HevoProductDB {
    IFullDBRaw private constant fullDBRaw =
        IFullDBRaw(0x0000000000000000000000000000000000000106);
    enum STATUS_BIKE {
        RESTING,
        CHARGING,
        RUNNING
    }
    enum MODE_BIKE {
        ECO,
        NORMAL,
        SPORT
    }

    struct BikeTelemetry {
        uint32 seq;
        uint32 timestamp;
        int256 throttlePercent; // scale 1e6
        bool brakeActive;
        int256 latitude; // scale 1e8
        int256 longitude; // scale 1e8
        int256 speedKmh; // scale 1e6
        int256 altitude; // scale 1e6
        int256 heading; // scale 1e6
        uint8 satellites;
        bool fixValid;
        uint32 ageMs;
        int256 accelX; // scale 1e6
        int256 accelY;
        int256 accelZ;
        int256 gyroX; // scale 1e6
        int256 gyroY;
        int256 gyroZ;
        int256 accelMagnitude; // scale 1e6
        int256 temperature; // scale 1e6
        bool fallDetected;
        bool sensorOk;
        bool personSeated;
        int256 batterySoc; // scale 1e6
        int256 batteryVoltage; // scale 1e6
        bool fingerprintMatched;
        uint16 fingerprintId;
        uint16 fingerprintConfidence;
        uint8 state;
        uint8 faultCode;
        int256 motorSpeedReq; // scale 1e6
        bool motorEnableReq;
    }

    struct HevoProduct {
        uint256 hevoId;
        uint256 merchantId;
        uint256 branchId;
        uint256 categoryId;
        string name;
        string slug;
        string description;
        string images;
        string productType;
        uint8 status;
        bool isFeatured;
        uint256 price;
        uint256 stock;
        STATUS_BIKE bikestatus;
        uint256 pin;
        uint256 speed;
        uint256 time_used;
        uint256 distance;
        MODE_BIKE mode;
        string licensePlate;
    }

    struct HevoState {
        uint256 productId;
        MODE_BIKE mode;
        STATUS_BIKE bikeStatus;
        uint256 pin;
        uint256 distance;
        uint256 timeUsed;
        bool exists;
        bool isActive;
    }

    // ── Prefix constants (giống PublicfullDB) ──────────────────
    // Khi indexTextForDocument dùng prefix "N" thì query phải dùng "N:keyword"
    // Khi searchProducts truyền PrefixEntry("N","N") nghĩa là:
    //   query "name:keyword" → engine map "name" → prefix "N" → tìm "Nkeyword" trong index
    string private constant P_NAME = "N"; // name
    string private constant P_TYPE = "T"; // productType
    string private constant P_LP = "LP"; // licensePlate (term only)
    string private constant P_CAT = "C"; // category term
    string private constant P_MERCH = "M"; // merchant term
    string private constant P_STATUS = "S"; // status term

    // Value slots
    uint256 private constant SLOT_CATEGORY = 1;
    uint256 private constant SLOT_STATUS = 2;
    uint256 private constant SLOT_CREATED_AT = 3;
    uint256 private constant SLOT_PRICE = 4; // isSerialise=true cho range filter

    int256 constant SCALE_6 = 1_000_000;
    int256 constant SCALE_8 = 100_000_000;

    DBStorage public db;
    uint256 public categoryCounter;
    uint256 public productCounter;
    uint256 public productDetailCounter;

    event CategoryAdded(uint256 indexed id, string name, uint256 docId);
    event CategoryData(string data);
    event CategoryUpdated(uint256 indexed categoryDocId);
    event ProductAdded(uint256 indexed id, string name, uint256 docId);
    event ProductUpdated(uint256 indexed productDocId);
    event ProductDeleted(uint256 indexed productDocId);
    event ProductData(string data);
    event SearchCategoriesResult(uint256 total, SearchResult[] results);
    event Test(uint256 a, uint256 b);
    event IndexDebug(
        string dbName,
        uint256 docId,
        uint256 indexWithPrefix,
        uint256 indexFullText,
        uint256 termId,
        bool committed
    );
    event ProductTelemetryAdded(
        uint256 indexed productId,
        uint256 indexed telemetryId,
        uint256 docId
    );
    event ModeChanged(uint256 indexed hevoId, MODE_BIKE mode);
    event BikeStatusChanged(uint256 indexed hevoId, STATUS_BIKE bikeStatus);
    event DistanceUpdated(uint256 indexed hevoId, uint256 distance);
    event TimeUsedUpdated(uint256 indexed hevoId, uint256 timeUsed);
    event OnOffUpdated(uint256 indexed hevoId, bool onOff);
    event CategoryTerm(uint256 indexed docId, string[] result);
    event SearchTerm(string data);

    mapping(uint256 => uint256) public categoryDocIds;
    mapping(uint256 => uint256) public productDocIds;
    mapping(uint256 => uint256) public productDetailIds;
    mapping(uint256 => HevoState) public hevoStates;

    constructor(address _dbStorage) {
        require(_dbStorage != address(0), "E1");
        db = DBStorage(_dbStorage);
    }

    // ============================================================
    //  CATEGORY
    // ============================================================

    function addCategory(
        uint256 parentId,
        string memory name,
        string memory slug
    ) public returns (uint256 id) {
        id = ++categoryCounter;

        string memory json = string(
            abi.encodePacked(
                '{"id":',
                _u(id),
                ',"parent_id":',
                _u(parentId),
                ',"name":"',
                name,
                '","slug":"',
                slug,
                '","created_at":',
                _u(block.timestamp),
                ',"deleted_at":0}'
            )
        );

        uint256 docId = db.newDocument(db.DB_CATEGORIES(), json);
        require(docId > 0, "E2");
        categoryDocIds[id] = docId;

        // ── Index text trước, commit SAU CÙNG ──────────────────
        // [SỬA] Giống PublicfullDB: indexText cho name với prefix "N"
        //        VÀ không có prefix (full-text)
        uint256 itPrefix = db.indexTextForDocument(
            db.DB_CATEGORIES(),
            docId,
            name,
            1,
            P_NAME
        );
        require(itPrefix > 0, "E3");
        uint256 itFull = db.indexTextForDocument(
            db.DB_CATEGORIES(),
            docId,
            name,
            1,
            ""
        );
        require(itFull > 0, "E4");

        // Term để filter chính xác theo ID
        uint256 atd = db.addTermDocument(
            db.DB_CATEGORIES(),
            docId,
            string(abi.encodePacked("ID:", _u(id)))
        );
        require(atd > 0, "E5");

        // [SỬA] commit SAU KHI đã index xong tất cả
        // bool committed = db.commit(db.DB_CATEGORIES());
        // require(committed, "E6");

        emit IndexDebug(
            db.DB_CATEGORIES(),
            docId,
            itPrefix,
            itFull,
            atd,
            true
        );
        emit Test(docId, atd);
        emit CategoryAdded(id, name, docId);
    }
    // // ============================================================
    //  INTERNAL HELPERS
    // ============================================================

    function _u(uint256 v) internal pure returns (string memory) {
        if (v == 0) return "0";
        uint256 tmp = v;
        uint256 len;
        while (tmp != 0) {
            len++;
            tmp /= 10;
        }
        bytes memory b = new bytes(len);
        while (v != 0) {
            b[--len] = bytes1(uint8(48 + (v % 10)));
            v /= 10;
        }
        return string(b);
    }

    function _i256(int256 value) internal pure returns (string memory) {
        if (value == 0) return "0";
        bool negative = value < 0;
        uint256 absValue = negative ? uint256(-value) : uint256(value);
        return
            negative
                ? string(abi.encodePacked("-", _u(absValue)))
                : _u(absValue);
    }

    // ============================================================
    //  DEBUG HELPERS
    // ============================================================

    function debugGetCategoryTerms(
        uint256 categoryId
    ) external returns (string[] memory) {
        uint256 docId = categoryDocIds[categoryId];
        require(docId > 0, "E7");
        string[] memory result = db.getTermsDocument(db.DB_CATEGORIES(), docId);
        emit CategoryTerm(docId, result);
        return result;
    }
     function searchCategories(
        string memory keyword,
        uint64 offset,
        uint64 limit
    ) public returns (SearchResultsPage memory page) {
        // [SỬA] prefixMap: ("N","N") cho phép query dạng "N:keyword"
        //        key = alias người dùng gõ, value = prefix trong index
        PrefixEntry[] memory pm = new PrefixEntry[](2);
        pm[0] = PrefixEntry("name", P_NAME); // "name:abc" → tìm Nabc
        pm[1] = PrefixEntry(P_NAME, P_NAME); // "N:abc"    → tìm Nabc

        string[] memory sw = new string[](0);
        RangeFilter[] memory rf = new RangeFilter[](0);

        page = db.querySearch(
            db.DB_CATEGORIES(),
            // sortByValueSlot = -1 → C++ decode thành UINT64_MAX = NO_SORT_SLOT (không sort)
            // Không dùng 0 vì C++ hiểu là "sort by slot 0" (chưa index trong categories)
            SearchParams(keyword, pm, sw, offset, limit, -1, true, rf)
        );
        emit SearchCategoriesResult(page.total, page.results);
    }
    function debugSearchRaw(
        string memory dbname,
        string memory query
    ) external returns (string memory) {
        string memory data = fullDBRaw.search(dbname, query);
        emit SearchTerm(data);
        return data;
    }
}