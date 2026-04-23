// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/**
 @title PublicfullDB1
 @notice Hợp đồng này cung cấp một giao diện công khai để tương tác với hợp đồng FullDB1 (tại địa chỉ 0x0000000000000000000000000000000000000107).
 Nó cho phép người dùng quản lý cơ sở dữ liệu (CSDL), các trường (fields), thẻ (tags), tài liệu (documents) và thực hiện tìm kiếm.
 Mọi tương tác dữ liệu document được thực hiện thông qua struct ProductData thay cho chuỗi JSON.
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

// Cấu trúc dùng cho Output / Frontend
struct SearchResult {
  uint256 docid;
  uint256 rank;
  int256 percent;
  ProductData data;
}

struct SearchResultsPage {
  uint256 total;
  SearchResult[] results;
}

// Cấu trúc thô giao tiếp với Core C++ (Sử dụng bytes)
struct SearchResultCore {
  uint256 docid;
  uint256 rank;
  int256 percent;
  bytes data;
}

struct SearchResultsPageCore {
  uint256 total;
  SearchResultCore[] results;
}

interface FullDB1 {
  function getOrCreateDb(string memory name) external returns(bool);

  function newDocument(string memory dbname, bytes memory data)
      external returns(uint256);
  function getDataDocument(string memory dbname, uint256 docId)
      external returns(bytes memory);
  function setDataDocument(string memory dbname, uint256 docId,
                           bytes memory data) external returns(uint256);
  function deleteDocument(string memory dbname, uint256 docId)
      external returns(bool);
  function addTermDocument(string memory dbname, uint256 docId,
                           string memory term) external returns(uint256);
  function indexTextForDocument(string memory dbname, uint256 docId,
                                string memory text, uint8 weight,
                                string memory prefix) external returns(uint256);
  function addValueDocument(string memory dbname, uint256 docId, uint256 slot,
                            string memory data, bool isSerialise)
      external returns(uint256);
  function getValueDocument(string memory dbname, uint256 docId, uint256 slot,
                            bool isSerialise) external returns(string memory);
  function getTermsDocument(string memory dbname, uint256 docId)
      external returns(string[] memory);
  function search(string memory dbname, string memory query)
      external returns(string memory);
  function querySearch(string memory dbname, SearchParams memory params)
      external returns(SearchResultsPageCore memory);
}

contract PublicfullDB1 {
  FullDB1 public fullDB = FullDB1(0x0000000000000000000000000000000000000107);

  bool public status;
  Field public lastQueriedField;
  Tag public lastQueriedTag;
  string public dbName;
  string public returnString;
  ProductData public returnProductData;
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

  // Hàm commit đã được ẩn tự động trong EVM/MVM

  // Document

  function newDocument(string memory dbname,
                       ProductData memory data) public returns(uint256) {
    uint256 result = fullDB.newDocument(dbname, abi.encode(data));
    id = result;
    return result;
  }

  function getDataDocument(string memory dbname,
                           uint256 docId) public returns(ProductData memory) {
    bytes memory raw = fullDB.getDataDocument(dbname, docId);
    ProductData memory result = abi.decode(raw, (ProductData));
    returnProductData = result;
    return result;
  }

  function setDataDocument(string memory dbname, uint256 docId,
                           ProductData memory data) public returns(uint256) {
    uint256 resultId = fullDB.setDataDocument(dbname, docId, abi.encode(data));
    status = (resultId > 0);
    id = resultId;
    return resultId;
  }

  function indexTextForDocument(string memory dbname, uint256 docId,
                                string memory text, uint8 wdf_inc,
                                string memory prefix) public returns(uint256) {
    uint256 resultId =
        fullDB.indexTextForDocument(dbname, docId, text, wdf_inc, prefix);
    status = (resultId > 0);
    id = resultId;
    return resultId;
  }

  function addValueDocument(string memory dbname, uint256 docId, uint256 slot,
                            string memory data, bool isSerialise)
      external returns(uint256) {
    uint256 resultId =
        fullDB.addValueDocument(dbname, docId, slot, data, isSerialise);
    status = (resultId > 0);
    id = resultId;
    return resultId;
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
  event SearchResultLogged(uint256 docid, uint256 rank, uint256 percent, ProductData data);

  function querySearch(
      string memory dbname,
      SearchParams memory params) public returns(SearchResultsPage memory) {
    SearchResultsPageCore memory currentPage = fullDB.querySearch(dbname, params);
    lastQueryResults.total = currentPage.total;
    emit QuerySearchResults(currentPage.total, currentPage.results.length);
    
    delete searchResults;
    for (uint256 i = 0; i < currentPage.results.length; i++) {
      SearchResultCore memory result = currentPage.results[i];
      ProductData memory pdata = abi.decode(result.data, (ProductData));
      uint256 percentValue =
          (result.percent >= 0) ? uint256(result.percent) : 0;

      searchResults.push(SearchResult({
        docid : result.docid,
        rank : result.rank,
        percent : result.percent,
        data : pdata
      }));

      emit SearchResultLogged(result.docid, result.rank, percentValue, pdata);
    }

    return currentPage;
  }

  function createSampleProductDatabase(string memory _dbname) public returns(bool) {
    require(fullDB.getOrCreateDb(_dbname), "Database creation/opening failed");
    dbName = _dbname;
    
    _createAndIndexProducts(_dbname);
    
    status = true;
    return true;
  }
  
  function _createAndIndexProducts(string memory _dbname) private {
    _processProduct1(_dbname);
    _processProduct2(_dbname);
    _processProduct3(_dbname);
    _processProduct4(_dbname);
    _processProduct5(_dbname);
    _processProduct6(_dbname);
  }
  
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
    ProductData memory productData = ProductData({
        title: title,
        category: category,
        brand: brand,
        price: price,
        discountPrice: discountPrice,
        description: description,
        content: content,
        colors: colors,
        filters: filters
    });

    docId = fullDB.newDocument(_dbname, abi.encode(productData));
    require(docId > 0, "FullDB: Failed to create new document");
    
    _indexTextFields(_dbname, docId, title, category, brand, description, content);
    
    _indexArrayTerms(_dbname, docId, colors, P_COLOR);
    _indexArrayTerms(_dbname, docId, filters, P_FILTER);
    
    if (bytes(price).length > 0) {
      fullDB.addValueDocument(_dbname, docId, PRICE_SLOT, price, true);
    }
    
    if (bytes(discountPrice).length > 0) {
      fullDB.addValueDocument(_dbname, docId, DISCOUNT_PRICE_SLOT, discountPrice, true);
    }
    return docId;
  }

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
  
  function _indexArrayTerms(
    string memory _dbname,
    uint256 docId,
    string[] memory terms,
    string memory prefix
  ) internal {
    for (uint j = 0; j < terms.length; j++) {
      if (bytes(terms[j]).length > 0) {
        fullDB.addTermDocument(
          _dbname,
          docId,
          string(abi.encodePacked(prefix, ":",  terms[j]))
        );
      }
    }
  }

   function addProduct(string memory _dbname, ProductData memory _productData)
       public
       returns (uint256 docId)
   {
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

       status = (docId > 0);
       id = docId;

       return docId;
   }

}
