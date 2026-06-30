// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

/**
 * @title Tantivy
 * @dev Thư viện giao tiếp với Tantivy Search Engine bằng kiến trúc Hashed Address (Địa chỉ động).
 *      Bảo mật tuyệt đối: Mỗi Database có một địa chỉ riêng biệt được băm từ (Address Contract Mẹ + Tên DB).
 *      Chỉ có Contract mẹ mới có quyền Gọi/Ghi vào địa chỉ này.
 */
library Tantivy {
    enum Action { SEARCH, INSERT, DELETE }

    /**
     * @dev Tính toán địa chỉ duy nhất của Database thuộc sở hữu của Contract gọi nó.
     */
    function getDbAddress(string memory dbName) internal view returns (address) {
        // Hashing: keccak256(Contract Address + Database Name)
        return address(uint160(uint256(keccak256(abi.encodePacked(address(this), dbName)))));
    }

    function search(string memory dbName, string memory query) internal view returns (uint256[] memory) {
        address dbAddr = getDbAddress(dbName);
        // Payload: [Hành động] + [Tên DB] + [Tham số]
        bytes memory payload = abi.encode(Action.SEARCH, dbName, query);
        
        (bool success, bytes memory result) = dbAddr.staticcall(payload);
        require(success, "Tantivy Search failed");
        
        // Nếu Host chưa khởi tạo, gọi vào tài khoản rỗng sẽ trả về mảng rỗng
        if (result.length == 0) return new uint256[](0);
        return abi.decode(result, (uint256[]));
    }

    function insert(string memory dbName, uint256 id, string memory metadata) internal {
        address dbAddr = getDbAddress(dbName);
        bytes memory payload = abi.encode(Action.INSERT, dbName, id, metadata);
        
        (bool success, ) = dbAddr.call(payload);
        require(success, "Tantivy Insert failed");
    }

    function remove(string memory dbName, uint256 id) internal {
        address dbAddr = getDbAddress(dbName);
        bytes memory payload = abi.encode(Action.DELETE, dbName, id);
        
        (bool success, ) = dbAddr.call(payload);
        require(success, "Tantivy Delete failed");
    }
}

/**
 * @title TantivyStore
 * @dev Hợp đồng mẫu minh họa cửa hàng quản lý sản phẩm sử dụng Tantivy (Multi-DB).
 */
contract TantivyStore {
    using Tantivy for string;

    // Database riêng cho các sản phẩm
    string constant DB_PRODUCTS = "products";
    string constant DB_CUSTOMERS = "customers";

    struct Product {
        uint256 id;
        string name;
        string description;
        uint256 price;
    }

    uint256 public nextProductId = 1;
    mapping(uint256 => Product) public products;

    event ProductAdded(uint256 indexed id, string name);
    event ProductUpdated(uint256 indexed id, string name);
    event ProductRemoved(uint256 indexed id);

    // ==========================================
    // QUẢN LÝ SẢN PHẨM (CRUD & SEARCH)
    // ==========================================

    // 1. CREATE - Thêm sản phẩm mới
    function addProduct(string memory _name, string memory _description, uint256 _price) public {
        uint256 productId = nextProductId++;
        products[productId] = Product(productId, _name, _description, _price);

        // Ghi vào Tantivy Database "products"
        // metadata là chuỗi JSON hoặc text gộp để phục vụ tìm kiếm toàn văn bản
        string memory metadata = string(abi.encodePacked(_name, " | ", _description));
        DB_PRODUCTS.insert(productId, metadata);

        emit ProductAdded(productId, _name);
    }

    // 2. READ / SEARCH - Tìm kiếm sản phẩm
    function searchProducts(string memory _query) public view returns (Product[] memory) {
        // Tìm kiếm trên Tantivy Database "products"
        uint256[] memory productIds = DB_PRODUCTS.search(_query);
        
        Product[] memory foundProducts = new Product[](productIds.length);
        for (uint256 i = 0; i < productIds.length; i++) {
            foundProducts[i] = products[productIds[i]];
        }
        return foundProducts;
    }

    // 3. UPDATE - Cập nhật sản phẩm
    function updateProduct(uint256 _id, string memory _newName, string memory _newDescription, uint256 _newPrice) public {
        require(products[_id].id != 0, "Product not found");
        
        products[_id].name = _newName;
        products[_id].description = _newDescription;
        products[_id].price = _newPrice;

        // Cập nhật lại trong Tantivy bằng cách ghi đè (Insert lại với cùng ID)
        string memory metadata = string(abi.encodePacked(_newName, " | ", _newDescription));
        DB_PRODUCTS.insert(_id, metadata);

        emit ProductUpdated(_id, _newName);
    }

    // 4. DELETE - Xóa sản phẩm
    function removeProduct(uint256 _id) public {
        require(products[_id].id != 0, "Product not found");
        delete products[_id];

        // Xóa khỏi Tantivy Database "products"
        DB_PRODUCTS.remove(_id);
        
        emit ProductRemoved(_id);
    }
}
