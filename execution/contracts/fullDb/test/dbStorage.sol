// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

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

// ============================================================
//  INTERFACE FULLDB
// ============================================================

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

// ============================================================
//  DBSTORAGE — Deploy 1 lần, giữ mãi mãi
//  Tất cả contract khác gọi qua đây
// ============================================================

/**
 * @title DBStorage
 * @notice Contract trung tâm quản lý tất cả DB.
 *         Deploy 1 lần duy nhất. Không bao giờ redeploy.
 *
 * Danh sách DB:
 *   "hevo_categories"  — category sản phẩm
 *   "hevo_products"    — sản phẩm
 *   "hevo_users"       — người dùng
 *   "hevo_user_bikes"  — xe đăng ký của user
 *
 * Quyền truy cập:
 *   - owner: có thể thêm/xóa authorized contracts
 *   - authorized contracts: có thể gọi các hàm write/read
 */
contract DBStorage {
    IFullDB private fullDB =
        IFullDB(0x0000000000000000000000000000000000000106);

    address public owner;

    // Danh sách contract được phép gọi vào DBStorage
    mapping(address => bool) public authorized;

    // Tên các DB
    string public constant DB_CATEGORIES = "hevo_categories";
    string public constant DB_PRODUCTS = "hevo_products";
    string public constant DB_USERS = "hevo_users";
    string public constant DB_BIKES = "hevo_user_bikes";
    string public constant DB_DETAIL_BIKES = "hevo_detail_bikes";
    string public constant DB_HEVO_TYPE = "hevo_types_bikes";

    event Authorized(address indexed contractAddr);
    event Revoked(address indexed contractAddr);

    modifier onlyOwner() {
        require(msg.sender == owner, "DBStorage: not owner");
        _;
    }

    modifier onlyAuthorized() {
        require(authorized[msg.sender], "DBStorage: not authorized");
        _;
    }

    // ---- Constructor: tạo tất cả DB ngay khi deploy ----
    constructor() {
        owner = msg.sender;

        // Tạo tất cả DB một lần duy nhất
        require(
            fullDB.getOrCreateDb(DB_CATEGORIES),
            "Init DB_CATEGORIES failed"
        );
        require(fullDB.getOrCreateDb(DB_PRODUCTS), "Init DB_PRODUCTS failed");
        require(fullDB.getOrCreateDb(DB_USERS), "Init DB_USERS failed");
        require(fullDB.getOrCreateDb(DB_BIKES), "Init DB_BIKES failed");
        require(
            fullDB.getOrCreateDb(DB_DETAIL_BIKES),
            "Init DB_DETAIL_BIKE failed"
        );
        require(
            fullDB.getOrCreateDb(DB_HEVO_TYPE),
            " Init DB_HEVO_TYPES failed"
        );
    }

    // ---- Quản lý quyền truy cập ----

    /**
     * @notice Cấp quyền cho contract được gọi DBStorage
     * @param contractAddr Địa chỉ contract (HevoProductDB, UserRacingDB, ...)
     */
    function authorize(address contractAddr) external onlyOwner {
        require(contractAddr != address(0), "Invalid address");
        authorized[contractAddr] = true;
        emit Authorized(contractAddr);
    }

    /**
     * @notice Thu hồi quyền của contract
     */
    function revoke(address contractAddr) external onlyOwner {
        authorized[contractAddr] = false;
        emit Revoked(contractAddr);
    }
    //  WRITE FUNCTIONS — chỉ authorized contract gọi được

    function newDocument(
        string memory dbname,
        string memory data
    ) external onlyAuthorized returns (uint256) {
        fullDB.getOrCreateDb(dbname);
        return fullDB.newDocument(dbname, data);
    }

    function setDataDocument(
        string memory dbname,
        uint256 docId,
        string memory data
    ) external onlyAuthorized returns (uint256) {
        fullDB.getOrCreateDb(dbname);
        return fullDB.setDataDocument(dbname, docId, data);
    }

    function deleteDocument(
        string memory dbname,
        uint256 docId
    ) external onlyAuthorized returns (bool) {
        fullDB.getOrCreateDb(dbname);
        return fullDB.deleteDocument(dbname, docId);
    }

    function addTermDocument(
        string memory dbname,
        uint256 docId,
        string memory term
    ) external onlyAuthorized returns (uint256) {
        fullDB.getOrCreateDb(dbname);
        return fullDB.addTermDocument(dbname, docId, term);
    }

    function indexTextForDocument(
        string memory dbname,
        uint256 docId,
        string memory text,
        uint8 weight,
        string memory prefix
    ) external onlyAuthorized returns (uint256) {
        fullDB.getOrCreateDb(dbname);
        return fullDB.indexTextForDocument(dbname, docId, text, weight, prefix);
    }

    function addValueDocument(
        string memory dbname,
        uint256 docId,
        uint256 slot,
        string memory data,
        bool isSerialise
    ) external onlyAuthorized returns (uint256) {
        fullDB.getOrCreateDb(dbname);
        return fullDB.addValueDocument(dbname, docId, slot, data, isSerialise);
    }

    function commit(
        string memory dbname
    ) external onlyAuthorized returns (bool) {
        fullDB.getOrCreateDb(dbname);
        return fullDB.commit(dbname);
    }

    function getDataDocument(
        string memory dbname,
        uint256 docId
    ) external returns (string memory) {
        return fullDB.getDataDocument(dbname, docId);
    }

    function getTermsDocument(
        string memory dbname,
        uint256 docId
    ) external returns (string[] memory) {
        // Ensure DB is opened for searcher context
        fullDB.getOrCreateDb(dbname);
        return fullDB.getTermsDocument(dbname, docId);
    }

    function querySearch(
        string memory dbname,
        SearchParams memory params
    ) external returns (SearchResultsPage memory) {
        // Some FullDB deployments require db to be opened per-caller
        fullDB.getOrCreateDb(dbname);
        return fullDB.querySearch(dbname, params);
    }

    function searchRaw(
        string memory dbname,
        string memory query
    ) external returns (string memory) {
        fullDB.getOrCreateDb(dbname);
        return fullDB.search(dbname, query);
    }
}