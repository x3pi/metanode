// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

contract Demo {

    // Định nghĩa cấu trúc dữ liệu SearchResult
    struct SearchResult {
        uint256 docid;
        uint256 rank;
        int256 percent;
        string data;
    }

    // Mảng lưu trữ các kết quả tìm kiếm
    SearchResult[] public searchResults;

    // Hàm thêm một kết quả vào mảng
    function addSearchResult(uint256 docid, uint256 rank, int256 percent, string memory data) public {
        delete searchResults;
        // Thêm một kết quả mới vào mảng searchResults
        searchResults.push(SearchResult({
            docid: docid,
            rank: rank,
            percent: percent,
            data: data
        }));
    }

    // Hàm xóa tất cả các kết quả trong mảng
    function clearSearchResults() public {
        // Xóa tất cả phần tử trong mảng bằng cách gọi delete
        delete searchResults;
    }

    // Hàm lấy số lượng kết quả tìm kiếm
    function getSearchResultsCount() public view returns (uint256) {
        return searchResults.length;
    }

    // Hàm lấy thông tin kết quả tìm kiếm tại chỉ số i
    function getSearchResult(uint256 i) public view returns (SearchResult memory) {
        require(i < searchResults.length, "Index out of bounds");
        return searchResults[i];
    }
}
