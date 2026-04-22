// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/**
 * @title TestSetDataDocument
 * @notice Contract dùng để test hàm setDataDocument của FullDB.
 *         Phát ra events chi tiết để debug kết quả trả về (docId mới hoặc cũ).
 *
 * NOTE: Interface setDataDocument đã được cập nhật trả về uint256 (docId)
 *       thay vì bool, khớp với C++ backend mới.
 *
 * Cách dùng trên Remix:
 *   1. Deploy TestSetDataDocument
 *   2. Gọi testSetData() với dbname, docId cũ, data mới
 *   3. Xem events trong transaction log để debug
 */

// Interface cập nhật: setDataDocument trả về uint256 (docId)
interface FullDBV2 {
    function getOrCreateDb(string memory name) external returns (bool);
    function newDocument(string memory dbname, string memory data) external returns (uint256);
    function getDataDocument(string memory dbname, uint256 docId) external returns (string memory);

    // ← Đã đổi return type từ bool → uint256 (docId thực tế chứa data mới)
    function setDataDocument(string memory dbname, uint256 docId, string memory data) external returns (uint256);

    function commit(string memory dbname) external returns (bool);
}

contract TestSetDataDocument {
    FullDBV2 public fullDB = FullDBV2(0x0000000000000000000000000000000000000106);

    // ─── State variables để quan sát kết quả ───────────────────────────────
    uint256 public lastCreatedDocId;       // docId tạo mới (newDocument)
    uint256 public lastSetDataDocId;       // docId trả về từ setDataDocument
    string  public lastReadDataBefore;     // data đọc TRƯỚC khi setData
    string  public lastReadDataAfter;      // data đọc SAU khi setData
    bool    public lastCommitStatus;

    // ─── Events để debug ────────────────────────────────────────────────────

    /// @dev Phát ra khi tạo document mới
    event DocumentCreated(
        string  dbname,
        uint256 docId,
        string  initialData
    );

    /// @dev Phát ra data đọc được TRƯỚC khi update
    event DataBeforeUpdate(
        string  dbname,
        uint256 docId,
        string  data
    );

    /// @dev Phát ra kết quả gọi setDataDocument
    event SetDataCalled(
        string  dbname,
        uint256 inputDocId,       // docId truyền vào
        uint256 returnedDocId,    // docId thực tế được trả về (cùng block → giống inputDocId, khác block → docId mới)
        string  newData,
        bool    isSameDocId       // true nếu returnedDocId == inputDocId
    );

    /// @dev Phát ra data đọc được từ docId ĐƯỢC TRẢ VỀ sau setData
    event DataAfterUpdate(
        string  dbname,
        uint256 returnedDocId,
        string  data
    );

    /// @dev Phát ra để so sánh nhanh kết quả
    event TestSummary(
        uint256 originalDocId,
        uint256 returnedDocId,
        bool    docIdChanged,
        string  dataBefore,
        string  dataAfter,
        bool    updateSuccess
    );

    // ─── Hàm test đơn giản ─────────────────────────────────────────────────

    /**
     * @notice Tạo document mới → đọc data → gọi setDataDocument → đọc lại data.
     *         Toàn bộ quá trình được phát ra qua events.
     * @param dbname Tên database
     * @param initialData Data ban đầu khi tạo document
     * @param updatedData Data mới muốn update
     */
    function testFullFlow(
        string memory dbname,
        string memory initialData,
        string memory updatedData
    ) public returns (uint256 returnedDocId) {
        // Bước 1: Tạo hoặc mở DB
        fullDB.getOrCreateDb(dbname);

        // Bước 2: Tạo document mới
        uint256 newDocId = fullDB.newDocument(dbname, initialData);
        lastCreatedDocId = newDocId;
        emit DocumentCreated(dbname, newDocId, initialData);

        // Bước 3: Đọc data TRƯỚC khi update
        string memory dataBefore = fullDB.getDataDocument(dbname, newDocId);
        lastReadDataBefore = dataBefore;
        emit DataBeforeUpdate(dbname, newDocId, dataBefore);

        // Bước 4: Gọi setDataDocument
        returnedDocId = fullDB.setDataDocument(dbname, newDocId, updatedData);
        lastSetDataDocId = returnedDocId;
        bool isSame = (returnedDocId == newDocId);
        emit SetDataCalled(dbname, newDocId, returnedDocId, updatedData, isSame);

        // Bước 5: Đọc data SAU khi update (dùng docId được trả về)
        string memory dataAfter = fullDB.getDataDocument(dbname, returnedDocId);
        lastReadDataAfter = dataAfter;
        emit DataAfterUpdate(dbname, returnedDocId, dataAfter);

        // Bước 6: Commit
        bool commitOk = fullDB.commit(dbname);
        lastCommitStatus = commitOk;

        // Bước 7: Tóm tắt
        emit TestSummary(
            newDocId,
            returnedDocId,
            !isSame,        // docIdChanged
            dataBefore,
            dataAfter,
            returnedDocId > 0
        );

        return returnedDocId;
    }

    /**
     * @notice Test setDataDocument trên một docId đã có sẵn (không tạo mới).
     *         Dùng khi bạn đã biết docId và muốn test update nhiều lần.
     * @param dbname Tên database
     * @param existingDocId DocId đã tồn tại
     * @param newData Data mới muốn update
     */
    function testSetData(
        string memory dbname,
        uint256 existingDocId,
        string memory newData
    ) public returns (uint256 returnedDocId) {
        // Đọc data hiện tại trước khi update
        string memory dataBefore = fullDB.getDataDocument(dbname, existingDocId);
        lastReadDataBefore = dataBefore;
        emit DataBeforeUpdate(dbname, existingDocId, dataBefore);

        // Gọi setDataDocument
        returnedDocId = fullDB.setDataDocument(dbname, existingDocId, newData);
        lastSetDataDocId = returnedDocId;
        bool isSame = (returnedDocId == existingDocId);
        emit SetDataCalled(dbname, existingDocId, returnedDocId, newData, isSame);

        // Đọc data SAU khi update từ docId được trả về
        string memory dataAfter = fullDB.getDataDocument(dbname, returnedDocId);
        lastReadDataAfter = dataAfter;
        emit DataAfterUpdate(dbname, returnedDocId, dataAfter);

        // Commit
        bool commitOk = fullDB.commit(dbname);
        lastCommitStatus = commitOk;

        // Tóm tắt
        emit TestSummary(
            existingDocId,
            returnedDocId,
            !isSame,
            dataBefore,
            dataAfter,
            returnedDocId > 0
        );

        return returnedDocId;
    }

    // ─── Events bổ sung ──────────────────────────────────────────────────────
    event DbStatus(string dbname, bool ok);
    event NewDocumentResult(string dbname, uint256 docId);
    event CommitResult(string dbname, bool ok);

    /**
     * @notice Tạo document mới (tương tự createSampleProductDatabase).
     *         Gọi transaction này TRƯỚC, ghi lại docId, rồi mới gọi testSetDataOnly.
     * @param dbname Tên database
     * @param data   Data ban đầu của document
     * @return docId ID của document vừa tạo (0 = lỗi)
     */
    function createTestDocument(
        string memory dbname,
        string memory data
    ) public returns (uint256 docId) {
        // Bước 1: Tạo hoặc mở DB
        bool dbOk = fullDB.getOrCreateDb(dbname);
        emit DbStatus(dbname, dbOk);   // ← xem DB tạo có ok không

        // Bước 2: Tạo document
        docId = fullDB.newDocument(dbname, data);
        emit NewDocumentResult(dbname, docId);  // ← xem docId là bao nhiêu

        // Bước 3: Commit
        bool commitOk = fullDB.commit(dbname);
        emit CommitResult(dbname, commitOk);    // ← xem commit có ok không

        lastCreatedDocId = docId;
        lastCommitStatus = commitOk;

        emit DocumentCreated(dbname, docId, data);
    }

    /**
     * @notice Chỉ test setDataDocument trên docId đã tạo từ trước.
     *         Gọi transaction này RIÊNG (block khác) sau createTestDocument.
     * @param dbname        Tên database
     * @param existingDocId DocId được lấy từ createTestDocument
     * @param newData       Data muốn update
     */
    function testSetDataOnly(
        string memory dbname,
        uint256 existingDocId,
        string memory newData
    ) public returns (uint256 returnedDocId) {
        // Đọc data hiện tại
        string memory dataBefore = fullDB.getDataDocument(dbname, existingDocId);
        lastReadDataBefore = dataBefore;
        emit DataBeforeUpdate(dbname, existingDocId, dataBefore);

        // Gọi setDataDocument
        returnedDocId = fullDB.setDataDocument(dbname, existingDocId, newData);
        lastSetDataDocId = returnedDocId;
        bool isSame = (returnedDocId == existingDocId);
        emit SetDataCalled(dbname, existingDocId, returnedDocId, newData, isSame);

        // Đọc data sau update (dùng docId được trả về)
        string memory dataAfter = fullDB.getDataDocument(dbname, returnedDocId);
        lastReadDataAfter = dataAfter;
        emit DataAfterUpdate(dbname, returnedDocId, dataAfter);

        // Commit
        bool commitOk = fullDB.commit(dbname);
        lastCommitStatus = commitOk;

        // Tổng kết
        emit TestSummary(
            existingDocId,
            returnedDocId,
            !isSame,
            dataBefore,
            dataAfter,
            returnedDocId > 0
        );
    }


    function readData(
        string memory dbname,
        uint256 docId
    ) public returns (string memory data) {
        data = fullDB.getDataDocument(dbname, docId);
        emit DataAfterUpdate(dbname, docId, data);
        return data;
    }
}
