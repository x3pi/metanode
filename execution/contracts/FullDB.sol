// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/**
 @title PublicfullDB
 @notice Hợp đồng này cung cấp một giao diện công khai để tương tác với hợp đồng FullDB (tại địa chỉ 0x0000000000000000000000000000000000000106).
 Nó cho phép người dùng quản lý cơ sở dữ liệu (CSDL), các trường (fields), thẻ (tags), tài liệu (documents) và thực hiện tìm kiếm.
 @dev Hướng dẫn sử dụng cơ bản:
 
 1. Tạo hoặc Mở CSDL:
 - Gọi hàm `getOrCreateDb(string memory name)` với tên CSDL mong muốn.
 - Hàm sẽ trả về `true` nếu thành công và tên CSDL sẽ được lưu vào biến `dbName`.
 - Trạng thái thành công cũng được lưu vào biến `status`.
 2. Quản lý Tài liệu (Documents):
 - Dùng `newDocument(dbname, data)` để tạo tài liệu mới. Hàm trả về ID tài liệu (`docId`) và lưu vào biến `id`. `data` là dữ liệu gốc của tài liệu.
 - Dùng `getDataDocument(dbname, docId)` để lấy dữ liệu gốc của tài liệu. Kết quả được lưu vào `returnString`.
 - Dùng `setDataDocument(dbname, docId, data)` để cập nhật dữ liệu gốc.
 - Dùng `indexTextForDocument(dbname, docId, text, weight, prefix)` để lập chỉ mục văn bản cho tài liệu, giúp tìm kiếm sau này. `prefix` dùng để đánh dấu văn bản thuộc về trường nào (ví dụ: "T" cho title, "C" cho category).
 - Dùng `addValueDocument(dbname, docId, slot, data, isSerialise)` để thêm giá trị vào một "slot" của tài liệu (thường dùng cho sắp xếp hoặc lọc theo khoảng giá trị, ví dụ: giá tiền).
 - Dùng `getValueDocument(dbname, docId, slot, isSerialise)` để lấy giá trị từ một slot. Kết quả được lưu vào `returnString`.
 - Dùng `getTermsDocument(dbname, docId)` để lấy danh sách các term (từ khóa) đã được lập chỉ mục cho tài liệu. Kết quả được lưu vào `arrayString`.
 - Dùng `deleteDocument(dbname, docId)` để xóa tài liệu.

 3. Tìm kiếm:
 - Dùng `querySearch(dbname, params)` để thực hiện tìm kiếm nâng cao.
 - `params` là một struct `SearchParams` chứa các thông tin như:
 - `queries`: Chuỗi truy vấn tìm kiếm.
 - `prefixMap`: Bộ lọc theo prefix (ví dụ: tìm sản phẩm có category là "electronics").
 - `stopWords`: Các từ dừng (bỏ qua khi tìm kiếm).
 - `offset`, `limit`: Phân trang kết quả.
 - `sortByValueSlot`, `sortAscending`: Sắp xếp kết quả theo giá trị trong một slot.
 - `rangeFilters`: Bộ lọc theo khoảng giá trị (ví dụ: lọc sản phẩm theo khoảng giá).
 - Kết quả trả về là một struct `SearchResultsPage` chứa tổng số kết quả và danh sách kết quả cho trang hiện tại.
 - Kết quả trang hiện tại cũng được lưu vào biến `searchResults` (dạng mảng `SearchResult[]`).
 - Tổng số kết quả được lưu vào `lastQueryResults.total`.
 - Các sự kiện `QuerySearchResults` và `SearchResultLogged` được phát ra để theo dõi kết quả tìm kiếm off-chain.

 4. Tạo dữ liệu mẫu:
 - Gọi hàm `createSampleProductDatabase(dbname)` để tạo một CSDL mẫu tên `dbname` với các trường, thẻ và dữ liệu sản phẩm ví dụ.
 - Hàm này tự động gọi các hàm thêm trường, thẻ và tài liệu cần thiết.

 Lưu ý:
 - Hầu hết các hàm ghi (thêm, sửa, xóa) đều trả về `bool` và cập nhật biến `status`.
 - Các hàm đọc dữ liệu thường lưu kết quả vào các biến public tương ứng (`returnString`, `lastQueriedField`, `lastQueriedTag`, `id`, `arrayString`, `searchResults`, `lastQueryResults`).

 5. Ví dụ tham số `params` cho hàm `querySearch` trên Remix:

 Ví dụ 1: Tìm title chứa "pro"
 ["title:pro",[
 ["title", "T"],
 ["T", "T"],
 ["category", "C"],
 ["C", "C"],
 ["brand", "B"],
 ["B", "B"],
 ["color", "CO"],
 ["CO", "CO"],
 ["filter", "F"],
 ["F", "F"]
 ], ["the", "of"], 0, 10, 0, true, []]

 Ví dụ 2: Brand 'apple' AND Category 'electronics' hoặc phức tạp hơn "(T:thun OR T:jean) AND (F:men OR F:casual)"
 (Điền chuỗi query tương ứng vào tham số đầu tiên)
 ["B:apple C:electronics",[ // hoặc ["(T:thun OR T:jean) AND (F:men OR F:casual)", ...]
 ["title", "T"],
 ["T", "T"],
 ["category", "C"],
 ["C", "C"],
 ["brand", "B"],
 ["B", "B"],
 ["color", "CO"],
 ["CO", "CO"],
 ["filter", "F"],
 ["F", "F"]
 ], ["the", "of"], 0, 10, 0, true, []]

 Ví dụ 3: Lọc sản phẩm giá gốc từ $500.00 đến $1000.00 (dùng 1 range filter)
 ["",[
 ["title", "T"],
 ["T", "T"],
 ["category", "C"],
 ["C", "C"],
 ["brand", "B"],
 ["B", "B"],
 ["color", "CO"],
 ["CO", "CO"],
 ["filter", "F"],
 ["F", "F"]
 ], ["the", "of"], 0, 10, 0, true, [[0, "500.00", "1000.00"]]]

 Ví dụ 4: Tìm mô tả/nội dung chứa 'man hinh'
 ["man hinh",[
 ["title", "T"],
 ["T", "T"],
 ["category", "C"],
 ["C", "C"],
 ["brand", "B"],
 ["B", "B"],
 ["color", "CO"],
 ["CO", "CO"],
 ["filter", "F"],
 ["F", "F"]
 ], ["the", "of"], 0, 10, 0, true, []]

 Ví dụ 5: Tìm kiếm "Electronics", giá gốc $400-$900 VÀ giá KM <= $800 (sắp xếp theo giá KM)
 ["C:electronics", [
 ["C","C"],
 ["F","F"]
 ],
 ["the", "of"], 0, 10, 1, true, [
 [0, "400", "900"],
 [1, "", "800"]
 ]
 ]

 Tìm kiếm cho cách trường được thêm bằng addTermDocument như 'color' hoặc 'filter' thay bằng nhập gái trị cần nhập mã code của Tag
 */



struct Field {
  string name;
  string code;
}

struct Tag {
  string name;
  string code;
}

struct ProductData {
  string title;
  string category;
  string brand;
  string price;
  string discountPrice;
  string description;
  string content;
  string[] colors;
  string[] filters;
}

/**
 * @notice Mục nhập cho prefix map trong SearchParams.
 */
struct PrefixEntry {
  string key;
  string value;
}

/**
 * @notice Đại diện cho một bộ lọc theo khoảng giá trị (range).
 * Tương ứng với struct RangeFilter trong C++.
 */
struct RangeFilter {
  // Tương ứng Xapian::valueno slot
  uint slot;
  // Tương ứng std::string start_serialised
  string startSerialised;
  // Tương ứng std::string end_serialised
  string endSerialised;
}

/**
 * @title SearchParams
 * @notice Cấu trúc dữ liệu đại diện cho các tham số tìm kiếm,
 * tương ứng với phiên bản hàm XapianSearcher::search hỗ trợ nhiều range filter.
 */

struct SearchParams {
  string queries;

  PrefixEntry[] prefixMap;


  string[] stopWords;

  uint64 offset;

  uint64 limit;

  int64 sortByValueSlot;

  bool sortAscending;  // true tăng dần, false giảm dần

  RangeFilter[] rangeFilters;
}

struct SearchResult {
  uint256 docid;
  uint256 rank;
  int256 percent;
  string data;
}

struct SearchResultsPage {
  uint256 total;  // Tổng số kết quả tìm thấy (không phải chỉ trong trang này)
  SearchResult[] results;  // Mảng kết quả cho trang hiện tại
}

// Struct for Add Product Input
struct ProductInputData {
     string title;
     string category;
     string brand;
     string price;
     string discountPrice;
     string description;
     string content;
     string[] colors; // Pass names, contract will look up codes
    string[] filters; // Pass names, contract will look up codes
}

interface FullDB {
  function getOrCreateDb(string memory name) external returns(bool);

  function newDocument(string memory dbname, string memory data)
      external returns(uint256);
  function getDataDocument(string memory dbname, uint256 docId)
      external returns(string memory);
  function setDataDocument(string memory dbname, uint256 docId,
                           string memory data) external returns(uint256);
  function deleteDocument(string memory dbname, uint256 docId)
      external returns(bool);
  function addTermDocument(string memory dbname, uint256 docId,
                           string memory term) external returns(bool);
  function indexTextForDocument(string memory dbname, uint256 docId,
                                string memory text, uint8 weight,
                                string memory prefix) external returns(bool);
  function addValueDocument(string memory dbname, uint256 docId, uint256 slot,
                            string memory data, bool isSerialise)
      external returns(bool);
  function getValueDocument(string memory dbname, uint256 docId, uint256 slot,
                            bool isSerialise) external returns(string memory);
  function getTermsDocument(string memory dbname, uint256 docId)
      external returns(string[] memory);
  function search(string memory dbname, string memory query)
      external returns(string memory);
  function querySearch(string memory dbname, SearchParams memory params)
      external returns(SearchResultsPage memory);

  function commit(string memory dbname)
      external returns(bool);
}

contract PublicfullDB {
  FullDB public fullDB = FullDB(0x0000000000000000000000000000000000000106);

  bool public status;
  Field public lastQueriedField;
  Tag public lastQueriedTag;
  string public dbName;
  string public returnString;
  uint256 public id;
  string[] public arrayString;
  SearchResultsPage public lastQueryResults;
  SearchResult[] public searchResults;

  // Constants to reduce stack usage
  string constant private P_TITLE = "T";
  string constant private P_CATEGORY = "C";
  string constant private P_BRAND = "B";
  string constant private P_COLOR = "CO";
  string constant private P_FILTER = "F";
  uint16 constant private PRICE_SLOT = 0;
  uint16 constant private DISCOUNT_PRICE_SLOT = 1;
  uint8 constant private TEXT_WEIGHT = 1;

  function getOrCreateDb(string memory name) public returns(bool) {
    bool result = fullDB.getOrCreateDb(name);
    if (result) dbName = name;
    status = result;
    return result;
  }


  function commit(string memory name) public returns(bool) {
    bool result = fullDB.commit(name);
    status = result;
    return result;
  }




  // Document

  function newDocument(string memory dbname,
                       string memory data) public returns(uint256) {
    uint256 result = fullDB.newDocument(dbname, data);
    id = result;
    return result;
  }

  function getDataDocument(string memory dbname,
                           uint256 docId) public returns(string memory) {
    string memory result = fullDB.getDataDocument(dbname, docId);
    returnString = result;
    return result;
  }

  function setDataDocument(string memory dbname, uint256 docId,
                           string memory data) public returns(uint256) {
    uint256 returnedDocId = fullDB.setDataDocument(dbname, docId, data);
    id = returnedDocId;       // lưu docId thực tế (cũ hoặc mới)
    status = (returnedDocId > 0);
    return returnedDocId;
  }

  function indexTextForDocument(string memory dbname, uint256 docId,
                                string memory text, uint8 wdf_inc,
                                string memory prefix) public returns(bool) {
    bool result =
        fullDB.indexTextForDocument(dbname, docId, text, wdf_inc, prefix);
    status = result;
    return result;
  }

  function addValueDocument(string memory dbname, uint256 docId, uint256 slot,
                            string memory data, bool isSerialise)
      external returns(bool) {
    bool result =
        fullDB.addValueDocument(dbname, docId, slot, data, isSerialise);
    status = result;
    return result;
  }

  function deleteDocument(string memory dbname,
                          uint256 docId) public returns(bool) {
    bool result = fullDB.deleteDocument(dbname, docId);
    status = result;
    return result;
  }

  function getValueDocument(string memory dbname, uint256 docId, uint256 slot,
                            bool isSerialise) public returns(string memory) {
    returnString = fullDB.getValueDocument(dbname, docId, slot, isSerialise);
    return returnString;
  }

  function getTermsDocument(string memory dbname,
                            uint256 docId) public returns(string[] memory) {
    arrayString = fullDB.getTermsDocument(dbname, docId);
    return arrayString;
  }

  event QuerySearchResults(uint256 totalResults, uint256 resultsCount);
  event SearchResultLogged(uint256 docid, uint256 rank, uint256 percent, string data);

  function querySearch(
      string memory dbname,
      SearchParams memory params) public returns(SearchResultsPage memory) {
    SearchResultsPage memory currentPage = fullDB.querySearch(dbname, params);
    lastQueryResults.total = currentPage.total;
    emit QuerySearchResults(currentPage.total, currentPage.results.length);
    
    delete searchResults; // Xóa toàn bộ mảng trước khi gán lại các phần tử mới
    for (uint256 i = 0; i < currentPage.results.length; i++) {
      SearchResult memory result = currentPage.results[i];
      uint256 percentValue =
          (result.percent >= 0) ? uint256(result.percent) : 0;

      searchResults.push(SearchResult({
        docid : result.docid,
        rank : result.rank,
        percent : result.percent,
        data : result.data
      }));

      emit SearchResultLogged(result.docid, result.rank, percentValue, result.data);
    }

    return currentPage;
  }

  // REFACTORED: Split into multiple functions to avoid stack too deep
  function createSampleProductDatabase(string memory _dbname) public returns(bool) {
    require(fullDB.getOrCreateDb(_dbname), "Database creation/opening failed");
    dbName = _dbname;
    
    // Setup fields
    // Create and index products
    _createAndIndexProducts(_dbname);
    
    status = true;
    return true;
  }
  

    

  
  // Helper function to create and index products
  function _createAndIndexProducts(string memory _dbname) private {
    // Process products one at a time to reduce stack usage
    _processProduct1(_dbname);
    _processProduct2(_dbname);
    _processProduct3(_dbname);
    _processProduct4(_dbname);
    _processProduct5(_dbname);
    _processProduct6(_dbname);
  }
  
  // Process individual products to avoid having a large array in memory
  function _processProduct1(string memory _dbname) private {
    string[] memory colors = new string[](3);
    colors[0] = "black";
    colors[1] = "gold";
    colors[2] = "silver";
    
    string[] memory filters = new string[](3);
    filters[0] = "discount";
    filters[1] = "bestseller";
    filters[2] = "new";
    
    _indexProduct(
      _dbname,
      "Iphone 13 Pro",
      "electronics",
      "apple",
      "999.99",
      "899.99",
      "Smart phone cao cap cua Apple",
      "Man hinh OLED 6.1 inch, chip A15 Bionic, camera Pro",
      colors,
      filters
    );
  }
  
  function _processProduct2(string memory _dbname) private {
    string[] memory colors = new string[](3);
    colors[0] = "black";
    colors[1] = "white";
    colors[2] = "green";
    
    string[] memory filters = new string[](2);
    filters[0] = "bestseller";
    filters[1] = "android";
    
    _indexProduct(
      _dbname,
      "Samsung Galaxy S22",
      "electronics",
      "samsung", 
      "799.00",
      "799.00",
      "Dien thoai Android hang dau",
      "Man hinh Dynamic AMOLED 2X, camera 108MP",
      colors,
      filters
    );
  }
  
  function _processProduct3(string memory _dbname) private {
    string[] memory colors = new string[](2);
    colors[0] = "space gray";
    colors[1] = "silver";
    
    string[] memory filters = new string[](2);
    filters[0] = "new";
    filters[1] = "professional";
    
    _indexProduct(
      _dbname,
      "Macbook Pro 14 inch",
      "electronics",
      "apple",
      "1999.00",
      "1999.00",
      "Laptop manh me cho chuyen gia",
      "Chip M1 Pro, man hinh Liquid Retina XDR",
      colors,
      filters
    );
  }
  
  function _processProduct4(string memory _dbname) private {
    string[] memory colors = new string[](2);
    colors[0] = "black";
    colors[1] = "silver";
    
    string[] memory filters = new string[](2);
    filters[0] = "discount";
    filters[1] = "audio";
    
    _indexProduct(
      _dbname,
      "Sony WH-1000XM5",
      "electronics",
      "sony",
      "399.99",
      "349.99",
      "Tai nghe chong on xuat sac",
      "Chat luong am thanh Hi-Res, chong on thong minh",
      colors,
      filters
    );
  }
  
  function _processProduct5(string memory _dbname) private {
    string[] memory colors = new string[](4);
    colors[0] = "black";
    colors[1] = "white";
    colors[2] = "blue";
    colors[3] = "gray";
    
    string[] memory filters = new string[](3);
    filters[0] = "discount";
    filters[1] = "casual";
    filters[2] = "men";
    
    _indexProduct(
      _dbname,
      "Ao thun nam Cool",
      "fashion",
      "coolmate",
      "29.99",
      "19.99",
      "Ao thun cotton thoang mat cho nam",
      "Chat lieu 100% cotton, tham hut mo hoi tot",
      colors,
      filters
    );
  }
  
  function _processProduct6(string memory _dbname) private {
    string[] memory colors = new string[](2);
    colors[0] = "blue";
    colors[1] = "black";
    
    string[] memory filters = new string[](3);
    filters[0] = "bestseller";
    filters[1] = "women";
    filters[2] = "denim";
    
    _indexProduct(
      _dbname,
      "Quan Jean Nu",
      "fashion",
      "levis",
      "89.50",
      "89.50",
      "Quan jean nu ong dung",
      "Vai denim ben dep, form chuan",
      colors,
      filters
    );
  }
  
  // Core indexing function that handles a single product
  function _indexProduct(
    string memory _dbname,
    string memory title,
    string memory category,
    string memory brand,
    string memory price,
    string memory discountPrice,
    string memory description,
    string memory content,
    string[] memory colors,
    string[] memory filters
  ) internal returns (uint256 docId)  {
    // Create document
       // Tạo JSON ở ngoài để tránh stack too deep
    string memory jsonData = _buildProductJson(
        title,
        category,
        brand,
        price,
        discountPrice,
        description,
        content,
        colors,
        filters
    );
    docId = fullDB.newDocument(_dbname, jsonData);
    require(docId > 0, "FullDB: Failed to create new document");
    
    // Index text fields
    _indexTextFields(_dbname, docId, title, category, brand, description, content);
    
    // Index colors and filters
    _indexArrayTerms(_dbname, docId, colors, P_COLOR);
    _indexArrayTerms(_dbname, docId, filters, P_FILTER);
    
    // Add value fields
    if (bytes(price).length > 0) {
      fullDB.addValueDocument(_dbname, docId, PRICE_SLOT, price, true);
    }
    
    if (bytes(discountPrice).length > 0) {
      fullDB.addValueDocument(_dbname, docId, DISCOUNT_PRICE_SLOT, discountPrice, true);
    }
    return docId;

  }

  function escapeJsonString(string memory s)
      internal pure returns(string memory) {
    bytes memory sBytes = bytes(s);
    bytes memory result = new bytes(sBytes.length * 2);  // Max possible length
    uint resultIdx = 0;
    for (uint i = 0; i < sBytes.length; i++) {
      // Only escape double quotes and backslashes for this basic example
      if (sBytes[i] == '"') {
        result[resultIdx++] = '\\';
        result[resultIdx++] = '"';
      } else if (sBytes[i] == '\\') {
        result[resultIdx++] = '\\';
        result[resultIdx++] = '\\';
      } else {
        result[resultIdx++] = sBytes[i];
      }
    }
    // Resize result array
    bytes memory finalResult = new bytes(resultIdx);
    for (uint i = 0; i < resultIdx; i++) {
      finalResult[i] = result[i];
    }
    return string(finalResult);
  }


function _buildProductJson(
    string memory title,
    string memory category,
    string memory brand,
    string memory price,
    string memory discountPrice,
    string memory description,
    string memory content,
    string[] memory colors,
    string[] memory filters
) internal pure returns (string memory) {
    return string(abi.encodePacked(
        "{",
        '"title":"', title, '",',
        '"category":"', category, '",',
        '"brand":"', brand, '",',
        '"price":"', price, '",',
        '"discountPrice":"', discountPrice, '",',
        '"description":"', description, '",',
        '"content":"', content, '",',
        '"colors":', buildJsonStringArray(colors), ',',
        '"filters":', buildJsonStringArray(filters),
        "}"
    ));
}

function buildJsonStringArray(string[] memory items) internal pure returns (string memory) {
    bytes memory result = "[";
    for (uint i = 0; i < items.length; i++) {
        result = abi.encodePacked(result, '"', escapeJsonString(items[i]), '"');
        if (i < items.length - 1) {
            result = abi.encodePacked(result, ',');
        }
    }
    result = abi.encodePacked(result, "]");
    return string(result);
}

    // Hàm ví dụ để gọi và lấy chuỗi JSON
    function getProductJsonExample() public pure returns (string memory) {
        string[] memory colors = new string[](2);
        colors[0] = "Red";
        colors[1] = "Blue with \"quotes\""; // Ví dụ chuỗi có dấu ngoặc kép

        string[] memory filters = new string[](1);
        filters[0] = "Size L";

        return _buildProductJson(
            "Awesome T-Shirt",
            "Clothing",
            "MyBrand",
            "25.99", // Giá dạng chuỗi
            "19.99", // Giá giảm giá dạng chuỗi
            "A very \"nice\" t-shirt with details.", // Mô tả có dấu ngoặc kép
            "Made from 100% cotton. Handle with care\\backslash.", // Nội dung có dấu ngoặc kép và backslash
            colors,
            filters
        );
    }
  
  // Helper for indexing text fields
  function _indexTextFields(
    string memory _dbname,
    uint256 docId,
    string memory title,
    string memory category,
    string memory brand,
    string memory description,
    string memory content
  ) internal {
    if (bytes(title).length > 0) {
      fullDB.indexTextForDocument(_dbname, docId, title, TEXT_WEIGHT, P_TITLE);
    }

    if (bytes(title).length > 0) {
      fullDB.indexTextForDocument(_dbname, docId, title, TEXT_WEIGHT, "");
    }
    
    if (bytes(category).length > 0) {
      fullDB.indexTextForDocument(_dbname, docId, category, TEXT_WEIGHT, P_CATEGORY);
    }
    
    if (bytes(brand).length > 0) {
      fullDB.indexTextForDocument(_dbname, docId, brand, TEXT_WEIGHT, P_BRAND);
    }
    
    if (bytes(description).length > 0) {
      fullDB.indexTextForDocument(_dbname, docId, description, TEXT_WEIGHT, "");
    }
    
    if (bytes(content).length > 0) {
      fullDB.indexTextForDocument(_dbname, docId, content, TEXT_WEIGHT, "");
    }
  }
  
  // Helper for indexing array terms with prefix
  function _indexArrayTerms(
    string memory _dbname,
    uint256 docId,
    string[] memory terms,
    string memory prefix
  ) internal {
    for (uint j = 0; j < terms.length; j++) {
      if (bytes(terms[j]).length > 0) {
        // Cần lấy code của tag không phải name để gán cho Term
        fullDB.addTermDocument(
          _dbname, 
          docId, 
          string(abi.encodePacked(prefix, ":",  terms[j]))
        );
      }
    }
  }

     /**
    * @notice Adds a new product document and indexes its fields.
    * @param _dbname The name of the database.
    * @param _productData The product details to add.
    * @return docId The ID of the newly created document.
    * @dev Requires the database to exist. Relies on internal helpers for indexing.
    * Color and Filter names in _productData will be looked up to get their codes for indexing.
    */
   function addProduct(string memory _dbname, ProductInputData memory _productData)
       public
       returns (uint256 docId)
   {
       // Basic check: Ensure we are operating on the intended DB if dbName is set
       // You might want more robust DB existence checks depending on your full contract logic
       // require(bytes(dbName).length > 0 && keccak256(abi.encodePacked(dbName)) == keccak256(abi.encodePacked(_dbname)), "Target DB mismatch");

       // Call the internal indexing function
       // Note: _indexProduct itself creates the document via newDocument
       docId = _indexProduct(
           _dbname,
           _productData.title,
           _productData.category,
           _productData.brand,
           _productData.price,
           _productData.discountPrice,
           _productData.description,
           _productData.content,
           _productData.colors,
           _productData.filters
       );

       // Update status based on whether a valid docId was returned
       // Assuming _indexProduct ensures docId > 0 on success
       status = (docId > 0);
       id = docId; // Optionally update the public id variable

       // Event emission could be added here if needed
       // emit ProductAdded(docId, _productData.title);

       return docId;
   }

}