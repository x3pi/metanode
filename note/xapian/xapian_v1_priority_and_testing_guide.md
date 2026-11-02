# Hướng Dẫn Ưu Tiên và Kiểm Thử Xapian Precompiles: V1 (0x107) vs V0 (0x106)

Tài liệu này ghi chú chi tiết về định hướng kiến trúc, lý do ưu tiên kiểm thử **V1 (0x107)**, lộ trình đối với **V0 (0x106)**, và danh mục các test case chuẩn mẫu để các lập trình viên sau này có thể kế thừa và mở rộng.

---

## 1. Tổng Quan Kiến Trúc Precompile

MetaNode cung cấp hai cổng Precompile để EVM tương tác với Search Engine Xapian:

| Tiêu chí | V1 (FullDatabaseV1) | V0 (FullDatabase - Legacy) |
|---|---|---|
| **EVM Address** | `0x107` (Decimal: 263) | `0x106` (Decimal: 262) |
| **Kiểu dữ liệu Payload** | `bytes` (Mảng byte nhị phân thô) | `string` (Chuỗi ký tự UTF-8) |
| **Mục đích sử dụng** | **Chuẩn Production** cho dApps và Smart Contracts | **Bản Legacy**, ít dùng, đang trong kế hoạch loại bỏ |
| **Khả năng chứa dữ liệu** | Tùy ý: ABI structs, JSON, hash, media, null bytes | Bị giới hạn bởi chuỗi UTF-8, dễ hỏng nếu có byte 0x00 |
| **Solidity Interface** | `IFullDBV1` (xem `test-db-xapiant-v2.sol`) | `IFullDB` cũ |
| **Trạng thái hỗ trợ** | Ưu tiên số 1, phát triển và tối ưu liên tục | Duy trì tương thích ngược tạm thời |

---

## 2. Vì Sao Phải Ưu Tiên Test Case V1?

1. **Thực tế dApp dùng `bytes`**:
   Các smart contract trong sản phẩm (như quản lý sản phẩm, tài liệu phi tập trung, metadata NFT/RWA) đều đóng gói dữ liệu phức tạp thành ABI-encoded structs (ví dụ `ProductData`) rồi gọi `fullDB.newDocument(DB_NAME, rawData)`. Chỉ có V1 (`0x107`) nhận `bytes` mới xử lý chính xác và an toàn.
2. **Loại bỏ lỗi ép kiểu chuỗi**:
   V0 nhận `string`, nếu truyền nhị phân có thể sinh ra lỗi parse ABI hoặc cắt cụt khi gặp ký tự kết thúc chuỗi.
3. **Đồng bộ với cơ chế Block-STM và Journal Rollback**:
   Cả hai precompile đều dùng chung backend `XapianManager` và chia sẻ In-Memory Version Chaining. Tuy nhiên, việc kiểm thử trực tiếp trên cổng V1 (`0x107`) đảm bảo rằng flow thực tế của smart contract production hoạt động hoàn hảo 100%.

---

## 3. Danh Mục 10 Kịch Bản Kiểm Thử Chuẩn Mẫu (V1 Dedicated Test Suite)

Tất cả 10 test case đã được hiện thực hóa và kiểm chứng thành công trong [xapian_revert_test.go](file:///home/abc/nhat/consensus-chain/metanode/execution/pkg/blockchain/tx_processor/xapian_revert_test.go):

1. **`TestXapianV1_NestedRevert_SubCallRevertDiscardsWrites`**:
   - Khung gọi con (child frame) ghi binary bytes vào 0x107 rồi REVERT.
   - Toàn bộ log ghi của khung con bị hủy bỏ hoàn toàn khỏi buffer, không rò rỉ ra block commit.
2. **`TestXapianV1_NestedRevert_SubCallSuccessCommitsWrites`**:
   - Khung gọi con ghi binary bytes vào 0x107 và RETURN thành công.
   - Khung cha thành công, các bản ghi được commit vào đĩa chính xác.
3. **`TestXapianV1_SiblingSubCallsOneSucceedsOneReverts`**:
   - Khung cha gọi 2 con độc lập: Child 1 thành công, Child 2 REVERT.
   - Bản ghi của Child 1 được lưu, Child 2 bị dọn dẹp sạch sẽ.
4. **`TestXapianV1_ParentAndChild_ChildRevertPreservesParent`**:
   - Cha ghi Doc 1, gọi Con (Con ghi Doc 2 rồi REVERT), Cha bắt lỗi và ghi Doc 3.
   - Commit giữ lại Doc 1 và Doc 3 của Cha, loại bỏ Doc 2 của Con.
5. **`TestXapianV1_MultiLevelDeepRevert`**:
   - Chuỗi lồng sâu 4 cấp: A -> B -> C -> D. D ghi và REVERT, C bắt lỗi và ghi tiếp, A/B thành công.
   - Bản ghi của C được commit nguyên vẹn, D bị rollback chuẩn xác theo LIFO.
6. **`TestXapianV1_TopLevelTxRevertClearsAll`**:
   - Giao dịch mức cao nhất ghi dữ liệu vào 0x107 rồi REVERT toàn bộ tx.
   - Runtime tự động kích hoạt `ClearXapianTxBuffer`, buffer sạch hoàn toàn (0 logs).
7. **`TestXapianV1_ReadOnlyOpInterleavedWithWrites`**:
   - Thao tác đọc (read-only `getOrCreateDb`) xen kẽ với ghi (`newDocument`) và sub-call revert.
   - Đảm bảo read-only không tạo snapshot thừa và không làm sai lệch vị trí rollback stack.
8. **`TestXapianV1_StaticCallWriteProtection`**:
   - Hợp đồng gọi qua opcode `STATICCALL` cố tình ghi vào 0x107 bị chặn ngay lập tức với exception `ErrWriteProtection`. Thao tác đọc qua `STATICCALL` vẫn hoạt động bình thường.
9. **`TestXapianV1_MultipleWritersSameBlock`**:
   - Nhiều giao dịch độc lập (Tx A, Tx B, Tx C) cùng cập nhật và đọc một tài liệu trong cùng 1 block.
   - In-Memory Version Chaining cho phép Tx C đọc được giá trị binary mới nhất của Tx A mà không bị đọc giá trị cũ từ đĩa.
10. **`TestXapianV1_MultiWriterWithInterleavedRevert`**:
    - Chuỗi ghi trong cùng block: Tx A ghi thành công, Tx B ghi đè nhưng bị REVERT, Tx C đọc dữ liệu.
    - Tx C đọc đúng giá trị của Tx A, mắt xích của Tx B bị bỏ qua an toàn.

---

## 4. Hướng Dẫn Cho Lập Trình Viên Khi Viết Test Mới Cho V1

Khi thêm tính năng hoặc sửa lỗi liên quan đến Xapian EVM:

### 4.1. Sử dụng các hàm Helper V1 có sẵn

Trong [xapian_revert_test.go](file:///home/abc/nhat/consensus-chain/metanode/execution/pkg/blockchain/tx_processor/xapian_revert_test.go), sử dụng các helper:

```go
// 1. Tạo calldata gọi newDocument với kiểu bytes
newDocData := makeXapianV1NewDocData(t, "db_name", []byte("raw_binary_content"))

// 2. Tạo calldata gọi setDataDocument với kiểu bytes
setData := makeXapianV1SetDataData(t, "db_name", docId, []byte("updated_binary_content"))

// 3. Tạo calldata gọi getDataDocument trả về kiểu bytes
getData := makeXapianV1GetDataData(t, "db_name", docId)

// 4. Tạo calldata gọi addTermDocument
addTerm := makeXapianV1AddTermData(t, "db_name", docId, "tag_term")

// 5. Bytecode helper để gọi precompile 0x107 trong EVM assembly
confScript(
    callXapianV1Op(0, newDocData),
    []any{0, 0, "RETURN"},
)
```

### 4.2. Mẫu Dispatcher Contract chuyển tiếp đến 0x107

Để kiểm thử tương tác giữa nhiều giao dịch hoặc gọi lồng nhau, dùng hợp đồng chuyển tiếp chuẩn:

```go
sharedContractV1 := common.HexToAddress("0x1000000000000000000000000000000000000188")
forwardCodeV1 := confScript(
    "CALLDATASIZE", 0, 0, "CALLDATACOPY",
    512, 1024, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 100000, "CALL", "POP",
    "RETURNDATASIZE", 1024, "RETURN",
)
seedContract(t, cs, sharedContractV1, big.NewInt(0), forwardCodeV1)
```

### 4.3. Cách chạy kiểm thử

Để chạy kiểm thử nhanh toàn bộ các test case V1:

```bash
cd execution
go test -v -count=1 ./pkg/blockchain/tx_processor -run "TestXapianV1_.*"
```

---

## 5. Kết Luận & Định Hướng Tiếp Theo

- Toàn bộ logic lõi của Xapian hiện tại đã được bảo vệ đầy đủ bằng 10 kịch bản kiểm thử V1 chuyên sâu.
- Các nhà phát triển mới khi tiếp cận hệ thống cần **tập trung 100% vào cổng V1 (0x107)**.
- Bộ test V0 được giữ lại ở chế độ bảo trì để đảm bảo không gãy các hợp đồng cũ (nếu có), nhưng không khuyến khích bổ sung test case mới trên V0.
