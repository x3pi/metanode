# Giao tiếp giữa Solidity và Tantivy qua Dynamic Hashed Address (Địa chỉ động)

Để đảm bảo tính bảo mật và khả năng cô lập dữ liệu tuyệt đối (Data Isolation) trong một mạng lưới Blockchain hàng triệu DApp, Metanode không sử dụng một địa chỉ tĩnh (`0x1000`) cho mọi Database. 

Thay vào đó, Metanode TEE áp dụng kiến trúc **Hashed Dynamic Address** (Địa chỉ động băm). 
Mỗi Database của Tantivy sẽ được gán cho một "Địa chỉ Ảo" duy nhất, được băm từ địa chỉ của Smart Contract Mẹ và Tên của Database.

---

## 1. Cơ chế Bảo mật và Ánh xạ

- **Công thức tạo địa chỉ DB:** `address(uint160(uint256(keccak256(abi.encodePacked(address(this), dbName)))))`
- **Quyền sở hữu (Ownership):** Bởi vì địa chỉ này được băm cùng với `address(this)` (Địa chỉ của Smart Contract đang gọi), nên **KHÔNG MỘT CONTRACT NÀO KHÁC** có thể tính ra cùng một địa chỉ Database. 
- Lớp EVM (Rust) bên ngoài khi nhận được một lệnh `CALL`, nó sẽ lấy `dbName` truyền trong Payload, kết hợp với `msg.sender` để tính lại mã Hash. Nếu khớp với địa chỉ đích, nó tự động hiểu đó là một lệnh gọi Tantivy hợp lệ và cho phép Đọc/Ghi.

> [!IMPORTANT]
> - Chỉ có Contract mẹ mới có thể Gọi/Ghi vào đúng Database của mình. Hacker có gọi thẳng vào địa chỉ đó cũng sẽ bị Node Rust chặn lại vì `hash(hacker_address, dbName) != target_address`.

---

## 2. Thư viện chuẩn `Tantivy.sol`

Bằng cách bọc logic trong một Thư viện (Library), Smart Contract của người dùng sẽ gọn gàng và giống như Lập trình Hướng đối tượng (OOP).

```solidity
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

library Tantivy {
    enum Action { SEARCH, INSERT, DELETE }

    function getDbAddress(string memory dbName) internal view returns (address) {
        return address(uint160(uint256(keccak256(abi.encodePacked(address(this), dbName)))));
    }

    function search(string memory dbName, string memory query) internal view returns (uint256[] memory) {
        address dbAddr = getDbAddress(dbName);
        bytes memory payload = abi.encode(Action.SEARCH, dbName, query);
        
        (bool success, bytes memory result) = dbAddr.staticcall(payload);
        require(success, "Search failed");
        
        if (result.length == 0) return new uint256[](0); // Database chưa có dữ liệu
        return abi.decode(result, (uint256[]));
    }

    function insert(string memory dbName, uint256 id, string memory metadata) internal {
        address dbAddr = getDbAddress(dbName);
        bytes memory payload = abi.encode(Action.INSERT, dbName, id, metadata);
        
        (bool success, ) = dbAddr.call(payload);
        require(success, "Insert failed");
    }

    function remove(string memory dbName, uint256 id) internal {
        address dbAddr = getDbAddress(dbName);
        bytes memory payload = abi.encode(Action.DELETE, dbName, id);
        
        (bool success, ) = dbAddr.call(payload);
        require(success, "Delete failed");
    }
}
```

---

## 3. Ứng dụng thực tế: Cửa hàng Sản phẩm (CRUD)

Dưới đây là một ví dụ Smart Contract hoàn chỉnh quản lý Sản phẩm của một cửa hàng (Thêm, Sửa, Xóa, Tìm kiếm), sử dụng cơ chế băm địa chỉ Tantivy.

```solidity
contract TantivyStore {
    using Tantivy for string;

    // Định nghĩa Database duy nhất cho sản phẩm
    string constant DB_PRODUCTS = "products";

    struct Product {
        uint256 id;
        string name;
        string description;
        uint256 price;
    }

    mapping(uint256 => Product) public products;
    uint256 public nextProductId = 1;

    // 1. CREATE (Thêm sản phẩm)
    function addProduct(string memory _name, string memory _desc, uint256 _price) public {
        uint256 id = nextProductId++;
        products[id] = Product(id, _name, _desc, _price);
        
        // Gộp metadata để đánh index Full-text Search
        string memory metadata = string(abi.encodePacked(_name, " | ", _desc));
        
        // Ghi vào Tantivy Database "products"
        DB_PRODUCTS.insert(id, metadata);
    }

    // 2. UPDATE (Sửa sản phẩm)
    function updateProduct(uint256 _id, string memory _name, string memory _desc, uint256 _price) public {
        require(products[_id].id != 0, "Product not found");
        
        products[_id].name = _name;
        products[_id].description = _desc;
        products[_id].price = _price;
        
        string memory metadata = string(abi.encodePacked(_name, " | ", _desc));
        
        // Ghi đè vào Tantivy (bằng cách insert lại cùng ID)
        DB_PRODUCTS.insert(_id, metadata);
    }

    // 3. DELETE (Xóa sản phẩm)
    function removeProduct(uint256 _id) public {
        require(products[_id].id != 0, "Product not found");
        
        delete products[_id];
        
        // Xóa khỏi Tantivy
        DB_PRODUCTS.remove(_id);
    }

    // 4. SEARCH (Tìm kiếm sản phẩm)
    function searchProducts(string memory _query) public view returns (Product[] memory) {
        // Tìm kiếm trên Tantivy Database "products"
        uint256[] memory productIds = DB_PRODUCTS.search(_query);
        
        // Giải mã kết quả (Fetch từ trạng thái State của Solidity)
        Product[] memory foundProducts = new Product[](productIds.length);
        for (uint256 i = 0; i < productIds.length; i++) {
            foundProducts[i] = products[productIds[i]];
        }
        
        return foundProducts;
    }
}
```

## 💡 Kết luận

Kiến trúc **Hashed Address** biến Tantivy thành một hạ tầng vô hạn. EVM không cần phải theo dõi (Tracking) địa chỉ nào là của Tantivy. Tất cả đều diễn ra thông qua Thuật toán Mã hóa Băm (Cryptographic Hash) một cách Deterministic và Phi tập trung. Tốc độ thực thi Cực nhanh, Bảo mật Cực cao, Cực kỳ tinh gọn!
