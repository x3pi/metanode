#ifndef XAPIAN_SEARCH_H
#define XAPIAN_SEARCH_H

#include <xapian.h>
#include <string>
#include <vector>
#include <map>
#include <optional>
#include <utility>
#include <cstdint>
#include <limits>
#include <nlohmann/json.hpp>
#include <sstream> // Thêm dòng này
#include <string>
#include <iostream>
#include <mvm/util.h>

// --- Struct dùng cho tham số ABI (nếu cần) ---
struct CppPrefixEntry
{
    std::string key;
    std::string value;
};

struct CppRangeFilter
{
    uint64_t slot;
    std::string startSerialised;
    std::string endSerialised;
};

struct CppSearchParams
{
    std::string queries;
    std::vector<CppPrefixEntry> prefixMap;
    std::string stemmerLang;
    std::vector<std::string> stopWords;
    uint64_t offset;
    uint64_t limit;
    uint64_t sortByValueSlot;
    bool sortAscending;
    std::vector<CppRangeFilter> rangeFilters;
    static constexpr uint64_t NO_SORT_SLOT = std::numeric_limits<uint64_t>::max();
};
// --- Kết thúc struct ABI ---

// --- Struct dùng nội bộ và cho kết quả ---
struct SearchResult
{
    Xapian::docid docid;
    unsigned int rank;
    int percent;
    std::string data;
};

struct DecodedQuerySearchArgs
{
    std::string dbname;
    CppSearchParams params;
};

// Struct cho tham số range của hàm search nội bộ
struct RangeFilter
{
    Xapian::valueno slot;
    std::string start_serialised; // Đổi lại tên cho nhất quán nếu muốn, nhưng giữ nguyên cũng được
    std::string end_serialised;   // Đổi lại tên cho nhất quán nếu muốn, nhưng giữ nguyên cũng được
};
// --- Kết thúc struct nội bộ ---

class XapianSearcher
{
public:
    explicit XapianSearcher(const std::string &db_path);
    ~XapianSearcher() = default;
    void dumpIndex();

    // --- Phương thức tìm kiếm gốc (Chữ ký cuối cùng) ---
    std::pair<std::vector<SearchResult>, Xapian::doccount> search(
        const std::vector<std::string> &queries,
        Xapian::Query::op combine_op = Xapian::Query::OP_AND,
        Xapian::Query::op default_op = Xapian::Query::OP_AND,
        const std::map<std::string, std::string> &prefix_map = {},
        const std::optional<std::string> &stemmer_lang = std::nullopt,
        const std::optional<std::vector<std::string>> &stop_words = std::nullopt,
        Xapian::doccount offset = 0,
        Xapian::doccount limit = 10,
        const std::optional<Xapian::valueno> &sort_by_value_slot = std::nullopt,
        bool sort_ascending = true,
        const std::vector<RangeFilter> &range_filters = {}, // Tham số thứ 11 LÀ vector<RangeFilter>
        uint256_t blockNumber =0
    );
    static constexpr const char *LOGICAL_ID_GENERATED_PREFIX = "uuid:";

    // --- Hàm tìm kiếm qua ABI ---
    std::pair<std::vector<SearchResult>, Xapian::doccount> searchABI(
        const std::vector<uint8_t> &abi_encoded_params);
    std::vector<uint8_t> encodeSearchResultsPage(uint64_t total_count, const std::vector<SearchResult> &results);
    std::vector<uint8_t> internalEncodeResultsArrayContent(const std::vector<SearchResult> &results);

private:
    Xapian::Database db;
};
std::string getDbNameFromABI(const std::vector<uint8_t> &call_data);

// Khai báo hàm decode (để .cpp có thể định nghĩa và gọi)
CppSearchParams decodeSearchParams(const std::vector<uint8_t> &abi_bytes);
// Định nghĩa to_json vẫn giữ nguyên
inline void to_json(nlohmann::json &j, const SearchResult &sr)
{
    j = nlohmann::json{
        {"docid", sr.docid},
        {"rank", sr.rank},
        {"percent", sr.percent},
        {"data", sr.data}};
}

// Sửa lại from_json
inline void from_json(const nlohmann::json &j, SearchResult &sr)
{
    sr.docid = j["docid"];
    sr.rank = j["rank"];
    sr.percent = j["percent"];
    sr.data = j["data"];
}

inline std::map<std::string, std::string> convertJsonToMap(const nlohmann::json &jsonArray)
{
    std::map<std::string, std::string> prefixMap;

    try
    {
        for (const auto &item : jsonArray)
        {
            if (item.find("key") != item.end() && item.find("value") != item.end())
            {
                prefixMap[item.at("key").get<std::string>()] = item.at("value").get<std::string>();
            }
        }
    }
    catch (const nlohmann::json::exception &e)
    {
        std::cerr << "JSON error: " << e.what() << std::endl;
    }

    return prefixMap;
}

inline std::optional<std::vector<std::string>> convertJsonToStopWordsList(const nlohmann::json &jsonArray)
{
    if (jsonArray.is_null() || !jsonArray.is_array() || jsonArray.empty())
    {
        return std::nullopt; // Trả về nullopt nếu không có dữ liệu hợp lệ
    }

    std::vector<std::string> stopWordsList;
    try
    {
        for (const auto &item : jsonArray)
        {
            if (item.is_string())
            {
                stopWordsList.push_back(item.get<std::string>());
            }
        }
        if (stopWordsList.empty())
        {
            return std::nullopt; // Nếu mảng JSON không chứa chuỗi hợp lệ, trả về nullopt
        }
    }
    catch (const nlohmann::json::exception &e)
    {
        std::cerr << "JSON error: " << e.what() << std::endl;
        return std::nullopt;
    }

    return stopWordsList;
}

// Hàm chuyển đổi giá tiền từ string sang double
inline double parsePrice(const std::string &priceStr)
{
    try
    {
        // Xử lý chuỗi "ser_price_XX"
        size_t pos = priceStr.find_last_of("_");
        if (pos != std::string::npos)
        {
            std::string numStr = priceStr.substr(pos + 1);
            return std::stod(numStr);
        }
    }
    catch (...)
    {
        // Xử lý lỗi nếu cần
    }
    return 0.0;
}
inline uint64_t hex_to_uint64_range(const std::string &hex_str)
{
    uint64_t result = 0;
    std::istringstream(hex_str) >> std::hex >> result;
    return result;
}
// Hàm chuyển đổi từ JSON sang vector<RangeFilter>
inline std::vector<RangeFilter> convertJsonToRangeFilters(const nlohmann::json &jsonData)
{
    std::vector<RangeFilter> rangeFilters;

    try
    {
        if (jsonData.find("rangeFilters") != jsonData.end())
        {
            for (const auto &tuple : jsonData["rangeFilters"])
            {
                // Tạo RangeFilter trước
                RangeFilter filter;

                // Gán slot
                filter.slot = hex_to_uint64_range(tuple["slot"]);

                // Xử lý begin
                std::string beginStr = tuple["begin"].get<std::string>();
                filter.start_serialised = (beginStr.empty() || beginStr == "") ? "" : Xapian::sortable_serialise(std::stod(beginStr));
                std::cerr << "beginStr " << beginStr << std::endl;

                // Xử lý end
                std::string endStr = tuple["end"].get<std::string>();
                filter.end_serialised = (endStr.empty() || endStr == "") ? "" : Xapian::sortable_serialise(std::stod(endStr));
                std::cerr << "endStr " << endStr << std::endl;

                // Thêm vào vector
                rangeFilters.push_back(filter);
            }
        }
    }
    catch (const std::exception &e)
    {
        std::cerr << "Error converting JSON to RangeFilters: " << e.what() << std::endl;
    }

    return rangeFilters;
}
#endif // XAPIAN_SEARCH_H