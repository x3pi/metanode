// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

// Structs required for searching with FullDB
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

// FullDB Interface
interface FullDB {
    function getOrCreateDb(string memory name) external returns (bool);
    function newDocument(string memory dbname, string memory data) external returns (uint256);
    function getDataDocument(string memory dbname, uint256 docId) external returns (string memory);
    function setDataDocument(string memory dbname, uint256 docId, string memory data) external returns (bool);
    function deleteDocument(string memory dbname, uint256 docId) external returns (bool);
    function addTermDocument(string memory dbname, uint256 docId, string memory term) external returns (bool);
    function indexTextForDocument(string memory dbname, uint256 docId, string memory text, uint8 weight, string memory prefix) external returns (bool);
    function addValueDocument(string memory dbname, uint256 docId, uint256 slot, string memory data, bool isSerialise) external returns (bool);
    function getValueDocument(string memory dbname, uint256 docId, uint256 slot, bool isSerialise) external returns (string memory);
    function getTermsDocument(string memory dbname, uint256 docId) external returns (string[] memory);
    function search(string memory dbname, string memory query) external returns (string memory);
    function querySearch(string memory dbname, SearchParams memory params) external returns (SearchResultsPage memory);
    function commit(string memory dbname) external returns (bool);
}

/**
 * @title JobContractWithFullDB
 * @dev A contract for creating, assigning, and completing jobs, with reward and claim functionality stored in FullDB.
 */
contract JobContractWithFullDB {
    FullDB public fullDB = FullDB(0x0000000000000000000000000000000000000106);

    // Enums for Job properties
    enum JobStatus { NEW, COMPLETED }
    enum JobType { HASH_MINING, VIDEO_ADS }

    // Struct to hold core job data on-chain for quick access and validation.
    struct JobCoreData {
        string jobId;
        address creator;
        address assignee;
        JobType jobType;
        string data;
        uint256 reward;
        uint64 createdAt;
    }
    
    // Structs for detailed events
    struct JobInfo {
        uint256 docId;
        string jobId;
        address creator;
        address assignee;
        JobType jobType;
        JobStatus status;
        string data;
        uint256 reward;
        uint64 createdAt;
        uint64 completedAt;
    }

    struct ClaimInfo {
        uint256 claimDocId;
        uint256 claimId;
        address claimant;
        uint256 amount;
        uint256 timestamp;
        bytes32 txHash;
    }

    // State variables
    bool public status;
    string public dbName;
    uint256 public lastDocId;
    string public lastFetchedDocument;
    SearchParams public lastQueryParams;
    SearchResultsPage public lastQueryResults;
    SearchResult[] public searchResults;
    uint256 private _nextClaimId;

    // Mappings
    mapping(uint256 => JobCoreData) private jobDataStore;
    mapping(address => uint256) private pendingBalances;

    // Constants
    string constant private P_STATUS = "S";
    string constant private P_TYPE = "T";
    string constant private P_CREATOR = "CR";
    string constant private P_ASSIGNEE = "AS";
    string constant private P_CLAIMANT = "CL";
    uint16 constant private REWARD_SLOT = 0;
    uint16 constant private CREATED_AT_SLOT = 1;
    uint16 constant private COMPLETED_AT_SLOT = 2;
    uint8 constant private TEXT_WEIGHT = 1;

    // Sample video links
    string[] private videoLinks;

    // Detailed events
    event JobStateUpdated(JobInfo job);
    event RewardClaimed(address indexed claimant, ClaimInfo claim);
    event ClaimHistorySent(address indexed user, SearchResultsPage results);

    event QuerySearchResults(uint256 totalResults, uint256 resultsCount);
    event SearchResultLogged(uint256 docid, uint256 rank, uint256 percent, string data);
    event BalanceUpdated(address indexed user, uint256 newBalance);
    
 constructor(string memory _initialDbName) {
        require(bytes(_initialDbName).length > 0, "Database name cannot be empty");
        dbName = _initialDbName;

        videoLinks.push("https://www.youtube.com/watch?v=dQw4w9WgXcQ");
        videoLinks.push("https://www.youtube.com/watch?v=xvFZjo5PgG0");
        videoLinks.push("https://www.youtube.com/watch?v=3JZ_D3ELwOQ");
        videoLinks.push("https://www.youtube.com/watch?v=L_LUpnjgPso");
        
        initializeDatabase();
    }

    receive() external payable {}

    function initializeDatabase() public {
        require(bytes(dbName).length > 0, "Database name not set");
        status = fullDB.getOrCreateDb(dbName);
        require(status, "Database creation/opening failed");
    }

    function completeJob(uint256 docId, uint256 nonce) public returns (uint256 newDocId) {
        require(status, "Database not initialized.");
        JobCoreData memory oldData = jobDataStore[docId];
        
        require(oldData.assignee == msg.sender, "Only the assignee can complete the job");
        require(oldData.creator != address(0), "Job not found or already completed");

        if (oldData.jobType == JobType.HASH_MINING) {
            bytes32 proof = keccak256(abi.encodePacked(oldData.data, nonce));
            require(proof[0] == 0x00 && proof[1] == 0x00 && (proof[2] & 0xF0) == 0x00, "Invalid proof of work: Hash does not have 5 leading zeros");
        }
        
        address completer = msg.sender;
        pendingBalances[completer] += oldData.reward;
        emit BalanceUpdated(completer, pendingBalances[completer]);

        bool deleted = fullDB.deleteDocument(dbName, docId);
        require(deleted, "Failed to delete old document");
        delete jobDataStore[docId];

        uint64 completedAt = uint64(block.timestamp);

        newDocId = _indexJob(oldData.jobId, oldData.creator, completer, oldData.jobType, JobStatus.COMPLETED, oldData.data, oldData.createdAt, completedAt, oldData.reward);
        require(newDocId > 0, "Failed to create completed document");
        
        jobDataStore[newDocId] = JobCoreData(oldData.jobId, oldData.creator, completer, oldData.jobType, oldData.data, oldData.reward, oldData.createdAt);

        lastDocId = newDocId;

        emit JobStateUpdated(JobInfo({
            docId: newDocId,
            jobId: oldData.jobId,
            creator: oldData.creator,
            assignee: completer,
            jobType: oldData.jobType,
            status: JobStatus.COMPLETED,
            data: oldData.data,
            reward: oldData.reward,
            createdAt: oldData.createdAt,
            completedAt: completedAt
        }));

        return newDocId;
    }

    function claimReward(uint256 _amount) public {
        uint256 userBalance = pendingBalances[msg.sender];
        require(userBalance >= _amount, "Insufficient pending balance");
        require(_amount > 0, "Claim amount must be positive");
        
        pendingBalances[msg.sender] = userBalance - _amount;

        (bool sent, ) = msg.sender.call{value: _amount}("");
        require(sent, "Failed to send Ether");

        (bool success, bytes memory data) = address(400).staticcall("");
        require(success, "Custom Chain: Failed to get transaction hash");
        require(data.length == 32, "Custom Chain: Invalid data length");
        
        bytes32 currentTxHash;
        assembly { currentTxHash := mload(add(data, 32)) }

        uint256 claimId = _nextClaimId;
        _nextClaimId++;

        string memory claimJson = _buildClaimJson(claimId, msg.sender, _amount, block.timestamp, currentTxHash);
        uint256 claimDocId = fullDB.newDocument(dbName, claimJson);
        require(claimDocId > 0, "Failed to create claim document in FullDB");

        fullDB.addTermDocument(dbName, claimDocId, string(abi.encodePacked(P_CLAIMANT, ":", _addressToString(msg.sender))));

        ClaimInfo memory claim = ClaimInfo({
            claimDocId: claimDocId,
            claimId: claimId,
            claimant: msg.sender,
            amount: _amount,
            timestamp: block.timestamp,
            txHash: currentTxHash
        });
        emit RewardClaimed(msg.sender, claim);
    }
    
    function getMyPendingBalance() public view returns (uint256) {
        return pendingBalances[msg.sender];
    }

    // *** HÀM ĐÃ ĐƯỢC CẬP NHẬT VỚI THAM SỐ PHÂN TRANG ***
// *** HÀM ĐÃ ĐƯỢC SỬA LẠI ĐỂ PHÁT EVENT (KHÔNG KHUYẾN KHÍCH) ***
    function getMyClaimHistory(uint64 offset, uint64 limit) public { // Bỏ `view` và `returns`
        require(limit > 0 && limit <= 100, "Limit must be between 1 and 100");

        string memory queryString = string(abi.encodePacked(P_CLAIMANT, ":", _addressToString(msg.sender)));

        PrefixEntry[] memory prefixMap = new PrefixEntry[](1);
        prefixMap[0] = PrefixEntry({ key: P_CLAIMANT, value: string(abi.encodePacked(P_CLAIMANT, ":")) });

        SearchParams memory params = SearchParams({
            queries: queryString,
            prefixMap: prefixMap,
            stopWords: new string[](0),
            offset: offset,
            limit: limit,
            sortByValueSlot: -1, 
            sortAscending: false,
            rangeFilters: new RangeFilter[](0)
        });

        // Gọi hàm tìm kiếm để lấy kết quả
        SearchResultsPage memory results = fullDB.querySearch(dbName, params);

        // Phát event chứa kết quả
        emit ClaimHistorySent(msg.sender, results);
    }

    function getDocumentById(uint256 docId) public returns (string memory data) {
        require(status, "Database not initialized.");
        data = fullDB.getDataDocument(dbName, docId);
        lastFetchedDocument = data;
        return data;
    }

    function queryJobs(SearchParams memory params) public returns (SearchResultsPage memory) {
        require(status, "Database not initialized.");
        lastQueryParams = params;
        
        SearchResultsPage memory currentPage = fullDB.querySearch(dbName, params);
        
        lastQueryResults.total = currentPage.total;
        emit QuerySearchResults(currentPage.total, currentPage.results.length);

        delete searchResults;
        for (uint i = 0; i < currentPage.results.length; i++) {
            searchResults.push(currentPage.results[i]);
        }
        
        return currentPage;
    }

function getOrAssignJob() public returns (uint256 docId) {
    require(status, "Database not initialized. Call initializeDatabase() first.");

    PrefixEntry[] memory prefixMap = new PrefixEntry[](4);
    prefixMap[0] = PrefixEntry({ key: P_STATUS, value: string(abi.encodePacked(P_STATUS, ":")) });
    prefixMap[1] = PrefixEntry({ key: P_ASSIGNEE, value: string(abi.encodePacked(P_ASSIGNEE, ":")) });
    prefixMap[2] = PrefixEntry({ key: P_CREATOR, value: string(abi.encodePacked(P_CREATOR, ":")) });
    prefixMap[3] = PrefixEntry({ key: P_TYPE, value: string(abi.encodePacked(P_TYPE, ":")) });

    // --- PHẦN SỬA ĐỔI BẮT ĐẦU ---

    // Bước 1: Tìm job đã được gán cho msg.sender và vẫn đang ở trạng thái NEW.
    string memory userSpecificQuery = string(abi.encodePacked(
        P_ASSIGNEE, ":", _addressToString(msg.sender), " ", P_STATUS, ":new"
    ));
    
    SearchParams memory userParams = SearchParams({
        queries: userSpecificQuery, prefixMap: prefixMap, stopWords: new string[](0),
        offset: 0, limit: 1, sortByValueSlot: -1, 
        sortAscending: false, rangeFilters: new RangeFilter[](0)
    });

    SearchResultsPage memory existingJobForSender = fullDB.querySearch(dbName, userParams);

    // Bước 2: Nếu tìm thấy, trả về job đó ngay lập tức mà không thay đổi gì.
    if (existingJobForSender.total > 0) {
        docId = existingJobForSender.results[0].docid;
        JobCoreData storage existingJob = jobDataStore[docId];

        emit JobStateUpdated(JobInfo({
            docId: docId,
            jobId: existingJob.jobId,
            creator: existingJob.creator,
            assignee: msg.sender,
            jobType: existingJob.jobType,
            status: JobStatus.NEW,
            data: existingJob.data,
            reward: existingJob.reward,
            createdAt: existingJob.createdAt,
            completedAt: 0 
        }));

        return docId;
    }

    // --- KẾT THÚC PHẦN SỬA ĐỔI ---


    // Bước 3: Nếu không tìm thấy job cũ, thực hiện logic gốc: tìm job mới hoặc tạo mới.
    string memory generalQuery = string(abi.encodePacked(
        P_STATUS, ":new ", P_ASSIGNEE, ":", _addressToString(address(0))
    ));

    SearchParams memory generalParams = SearchParams({
        queries: generalQuery, prefixMap: prefixMap, stopWords: new string[](0),
        offset: 0, limit: 1, sortByValueSlot: -1, 
        sortAscending: false, rangeFilters: new RangeFilter[](0)
    });

    SearchResultsPage memory availableJobs = fullDB.querySearch(dbName, generalParams);

    if (availableJobs.total > 0) {
        docId = availableJobs.results[0].docid;
        JobCoreData storage jobToAssign = jobDataStore[docId];
        
        require(jobToAssign.creator != address(0), "Job from DB not found in contract state");

        jobToAssign.assignee = msg.sender;

        fullDB.deleteDocument(dbName, docId);
        uint256 newDocId = _indexJob(jobToAssign.jobId, jobToAssign.creator, msg.sender, jobToAssign.jobType, JobStatus.NEW, jobToAssign.data, jobToAssign.createdAt, 0, jobToAssign.reward);
        require(newDocId == docId, "New docId mismatch after re-indexing");
        
        emit JobStateUpdated(JobInfo({
            docId: docId,
            jobId: jobToAssign.jobId,
            creator: jobToAssign.creator,
            assignee: msg.sender,
            jobType: jobToAssign.jobType,
            status: JobStatus.NEW,
            data: jobToAssign.data,
            reward: jobToAssign.reward,
            createdAt: jobToAssign.createdAt,
            completedAt: 0
        }));

        return docId;
    }

    // Nếu không có job nào, tạo một job hoàn toàn mới
    uint256 newJobNumericId = lastDocId + 1;
    string memory newJobId = string(abi.encodePacked("auto-job-", _uintToString(newJobNumericId)));
    uint256 random = uint256(keccak256(abi.encodePacked(block.timestamp, msg.sender, lastDocId, block.prevrandao, block.number)));
    JobType newJobType = JobType(random % 2);
    
    string memory newJobData;
    if (newJobType == JobType.VIDEO_ADS) {
        newJobData = videoLinks[random % videoLinks.length];
    } else {
        newJobData = _bytes32ToString(keccak256(abi.encodePacked(newJobId, block.timestamp)));
    }
    
    uint256 newJobReward = 1 ether;
    address creator = address(this);
    address assignee = msg.sender;
    uint64 createdAt = uint64(block.timestamp);

    docId = _indexJob(newJobId, creator, assignee, newJobType, JobStatus.NEW, newJobData, createdAt, 0, newJobReward);
    require(docId > 0, "Failed to create new auto-assigned job");

    jobDataStore[docId] = JobCoreData({
        jobId: newJobId, creator: creator, assignee: assignee,
        jobType: newJobType, data: newJobData, reward: newJobReward, createdAt: createdAt
    });

    lastDocId = docId;

    emit JobStateUpdated(JobInfo({
        docId: docId,
        jobId: newJobId,
        creator: creator,
        assignee: assignee,
        jobType: newJobType,
        status: JobStatus.NEW,
        data: newJobData,
        reward: newJobReward,
        createdAt: createdAt,
        completedAt: 0
    }));

    return docId;
}
    // === INTERNAL & UTILITY FUNCTIONS ===

    function _indexJob(
        string memory jobId, address creator, address assignee, JobType jobType,
        JobStatus jobStatus, string memory data, uint64 createdAt,
        uint64 completedAt, uint256 reward
    ) internal returns (uint256 docId) {
        string memory jsonData = _buildJobJson(jobId, creator, assignee, jobType, jobStatus, data, createdAt, completedAt, reward);
        docId = fullDB.newDocument(dbName, jsonData);
        if (docId == 0) return 0;

        if (bytes(data).length > 0) {
            fullDB.indexTextForDocument(dbName, docId, data, TEXT_WEIGHT, "");
        }
        fullDB.addTermDocument(dbName, docId, string(abi.encodePacked(P_STATUS, ":", _jobStatusToString(jobStatus))));
        fullDB.addTermDocument(dbName, docId, string(abi.encodePacked(P_TYPE, ":", _jobTypeToString(jobType))));
        fullDB.addTermDocument(dbName, docId, string(abi.encodePacked(P_CREATOR, ":", _addressToString(creator))));
        
        fullDB.addTermDocument(dbName, docId, string(abi.encodePacked(P_ASSIGNEE, ":", _addressToString(assignee))));
        
        fullDB.addValueDocument(dbName, docId, REWARD_SLOT, _uintToString(reward), false);
        fullDB.addValueDocument(dbName, docId, CREATED_AT_SLOT, _uintToString(createdAt), false);
        if (completedAt > 0) {
            fullDB.addValueDocument(dbName, docId, COMPLETED_AT_SLOT, _uintToString(completedAt), false);
        }
        return docId;
    }

    function _buildJobJson(
        string memory jobId, address creator, address assignee, JobType jobType,
        JobStatus _status, string memory data, uint64 createdAt,
        uint64 completedAt, uint256 reward
    ) internal pure returns (string memory) {
        return string(abi.encodePacked(
            "{",
            '"id":"', jobId, '",',
            '"creator_address":"', _addressToString(creator), '",',
            '"assignee_address":"', _addressToString(assignee), '",',
            '"job_type":"', _jobTypeToString(jobType), '",',
            '"status":"', _jobStatusToString(_status), '",',
            '"data":"', data, '",',
            '"created_at":', _uintToString(createdAt), ',',
            '"completed_at":', _uintToString(completedAt), ',',
            '"reward":', _uintToString(reward),
            "}"
        ));
    }

    function _buildClaimJson(
        uint256 claimId,
        address claimant,
        uint256 amount,
        uint256 timestamp,
        bytes32 txHash
    ) internal pure returns (string memory) {
        return string(abi.encodePacked(
            "{",
            '"type":"claim",',
            '"claim_id":', _uintToString(claimId), ',',
            '"claimant_address":"', _addressToString(claimant), '",',
            '"amount":', _uintToString(amount), ',',
            '"timestamp":', _uintToString(timestamp), ',',
            '"tx_hash":"', _bytes32ToString(txHash), '"',
            "}"
        ));
    }

    function _jobStatusToString(JobStatus _status) internal pure returns (string memory) {
        if (_status == JobStatus.NEW) return "new";
        if (_status == JobStatus.COMPLETED) return "completed";
        return "";
    }

    function _jobTypeToString(JobType jobType) internal pure returns (string memory) {
        if (jobType == JobType.HASH_MINING) return "hash_mining";
        if (jobType == JobType.VIDEO_ADS) return "video_ads";
        return "";
    }
    
    function _addressToString(address _addr) internal pure returns (string memory) {
        bytes32 _bytes = bytes32(uint256(uint160(_addr)));
        bytes memory HEX = "0123456789abcdef";
        bytes memory _string = new bytes(42);
        _string[0] = '0';
        _string[1] = 'x';
        for (uint i = 0; i < 20; i++) {
            _string[2 + i * 2] = HEX[uint8(_bytes[i + 12] >> 4)];
            _string[3 + i * 2] = HEX[uint8(_bytes[i + 12] & 0x0f)];
        }
        return string(_string);
    }
    
    function _bytes32ToString(bytes32 _bytes) internal pure returns (string memory) {
        bytes memory HEX = "0123456789abcdef";
        bytes memory _string = new bytes(66);
        _string[0] = '0';
        _string[1] = 'x';
        for (uint i = 0; i < 32; i++) {
            _string[2 + i * 2] = HEX[uint8(_bytes[i] >> 4)];
            _string[3 + i * 2] = HEX[uint8(_bytes[i] & 0x0f)];
        }
        return string(_string);
    }

    function _uintToString(uint256 _i) internal pure returns (string memory) {
        if (_i == 0) return "0";
        uint256 j = _i;
        uint256 len;
        while (j != 0) {
            len++;
            j /= 10;
        }
        bytes memory bstr = new bytes(len);
        uint256 k = len;
        while (_i != 0) {
            k = k - 1;
            uint8 temp = (48 + uint8(_i - (_i / 10) * 10));
            bytes1 b1 = bytes1(temp);
            bstr[k] = b1;
            _i /= 10;
        }
        return string(bstr);
    }
}