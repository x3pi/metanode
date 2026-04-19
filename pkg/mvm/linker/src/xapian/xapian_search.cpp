#include "xapian_search.h"
#include <abi/encoding_utils.h>
// #include "abi/encoding_utils.h" // Bỏ comment nếu bạn tách ra file riêng
#include <cmath>   // Cần cho getPaddedSize nếu dùng ceil
#include <cstring> // Cần cho memcpy
#include <iomanip>
#include <iostream>
#include <limits>
#include <map>
#include <memory>
#include <numeric>
#include <optional>
#include <stdexcept>
#include <string>
#include <vector>

void printHexTest(const std::vector<uint8_t> &bytes) {
  for (uint8_t byte : bytes) {
    std::cout << std::hex << std::setw(2) << std::setfill('0')
              << static_cast<int>(byte);
  }
  std::cout << std::dec << std::endl;
}

// --- Hàm lấy dbname từ ABI ---
std::string getDbNameFromABI(const std::vector<uint8_t> &call_data) {
  const size_t MIN_CALLDATA_SIZE_FOR_DBNAME_OFFSET = 0 + 32;
  if (call_data.size() < MIN_CALLDATA_SIZE_FOR_DBNAME_OFFSET) {
    throw std::runtime_error(
        "Invalid call data: too short to contain dbname offset.");
  }

  // Offset của dbname nằm ngay sau function selector (4 bytes)
  const size_t dbname_offset_ptr = 0;

  try {
    return encoding::readStringDynamic(call_data, dbname_offset_ptr);
  } catch (const std::out_of_range &e) {
    throw std::runtime_error(
        std::string("Failed to get dbname from ABI (out_of_range): ") +
        e.what());
  } catch (const std::exception &e) {
    throw std::runtime_error(std::string("Failed to get dbname from ABI: ") +
                             e.what());
  }
}

DecodedQuerySearchArgs
decodeQuerySearchCallData(const std::vector<uint8_t> &call_data) {
  if (call_data.size() < 0 + 32 + 32) { // Selector + 2 offsets
    throw std::runtime_error(
        "Invalid call data: too short for selector and offsets.");
  }

  DecodedQuerySearchArgs result;

  // Bỏ qua 4 byte selector
  size_t current_offset = 0;

  // Đọc offset của dbname
  uint64_t dbname_offset_ptr = current_offset;
  current_offset += 32;

  // Đọc offset của params
  uint64_t params_struct_offset_ptr = current_offset;
  uint64_t params_struct_start_offset =
      encoding::readUint256AsUint64(call_data, params_struct_offset_ptr);
  current_offset += 32;
  std::cerr << "params_struct_offset_ptr : " << params_struct_offset_ptr
            << std::endl;

  std::cerr << "params_struct_start_offset : " << params_struct_start_offset
            << std::endl;

  // Decode dbname (dùng offset đọc từ vị trí 4)
  result.dbname = encoding::readStringDynamic(call_data, 0);

  // Decode params (dùng offset đọc từ vị trí 36 và truyền offset bắt đầu của
  // struct)
  result.params = decodeSearchParams(call_data);

  return result;
}

// Constructor (Giữ nguyên)
XapianSearcher::XapianSearcher(const std::string &db_path) {
  try {
    db = Xapian::Database(db_path);
    std::cerr << "Mở database_____ '" << db_path << std::endl;
  } catch (const Xapian::Error &e) {
    std::cerr << "Lỗi mở database '" << db_path << "': " << e.get_msg()
              << std::endl;
    throw;
  }
}

// Phương thức tìm kiếm gốc
std::pair<std::vector<SearchResult>, Xapian::doccount> XapianSearcher::search(
    const std::vector<std::string> &queries, Xapian::Query::op combine_op,
    Xapian::Query::op default_op,
    const std::map<std::string, std::string> &prefix_map,
    const std::optional<std::string> &stemmer_lang,
    const std::optional<std::vector<std::string>> &stop_words,
    Xapian::doccount offset, Xapian::doccount limit,
    const std::optional<Xapian::valueno> &sort_by_value_slot,
    bool sort_ascending, const std::vector<RangeFilter> &range_filters,
    uint256_t blockNumber) {
  std::vector<SearchResult> results;
  Xapian::doccount estimated_total = 0;
  unsigned flags = Xapian::QueryParser::FLAG_DEFAULT |
                   Xapian::QueryParser::FLAG_WILDCARD |
                   Xapian::QueryParser::FLAG_BOOLEAN |
                   Xapian::QueryParser::FLAG_BOOLEAN_ANY_CASE;

  try {
    Xapian::QueryParser qp;
    qp.set_database(db);
    qp.set_default_op(default_op);
    qp.set_stemming_strategy(Xapian::QueryParser::STEM_SOME);

    std::unique_ptr<Xapian::SimpleStopper> stopper_ptr;
    if (stemmer_lang) {
      try {
        std::cerr << "Cảnh báo: stemmer_lang " << std::endl;
        Xapian::Stem stemmer(*stemmer_lang);
        qp.set_stemmer(stemmer);
        if (stop_words && !stop_words->empty()) {
          stopper_ptr = std::make_unique<Xapian::SimpleStopper>();
          for (const auto &word : *stop_words)
            stopper_ptr->add(word);
          qp.set_stopper(stopper_ptr.get());
        }
      } catch (const Xapian::Error &e) {
        std::cerr << "Cảnh báo: Không thể tạo stemmer/stopper: " << e.get_msg()
                  << std::endl;
      }
    }

    for (const auto &pair : prefix_map) {
      qp.add_prefix(pair.first, pair.second);
    }

    std::vector<Xapian::Query> parsed_queries;
    for (const auto &q_str : queries) {
      if (!q_str.empty()) {
        try {
          parsed_queries.push_back(qp.parse_query(q_str, flags));
        } catch (const Xapian::QueryParserError &e) {
          std::cerr << "Lỗi phân tích query '" << q_str << "': " << e.get_msg()
                    << std::endl;
        }
      }
    }

    Xapian::Query keyword_query =
        parsed_queries.empty()
            ? Xapian::Query::MatchAll
            : (parsed_queries.size() == 1
                   ? parsed_queries[0]
                   : Xapian::Query(combine_op, parsed_queries.begin(),
                                   parsed_queries.end()));

    Xapian::Query final_query = keyword_query;
    std::vector<Xapian::Query> value_range_queries;
    value_range_queries.reserve(range_filters.size() +
                                1); // thêm 1 cho slot 254

    // Các range filter bình thường
    for (const auto &filter : range_filters) {
      value_range_queries.emplace_back(Xapian::Query::OP_VALUE_RANGE,
                                       filter.slot, filter.start_serialised,
                                       filter.end_serialised);
    }

    // Tạo điều kiện lọc slot 254 >= blockNumber hoặc không tồn tại
    std::string block_serialised =
        Xapian::sortable_serialise(mvm::uint256_to_double(blockNumber));
    Xapian::Query slot254_ge_block =
        Xapian::Query(Xapian::Query::OP_VALUE_GE, 254, block_serialised);

    Xapian::Query has_slot254 =
        Xapian::Query(Xapian::Query::OP_VALUE_RANGE,
                      254,   // Số hiệu slot
                      "\0",  // Giá trị byte thấp nhất có thể
                      "\xFF" // Giá trị byte cao nhất có thể
        );

    Xapian::Query slot254_is_null = Xapian::Query(
        Xapian::Query::OP_AND_NOT,
        Xapian::Query::MatchAll, // Lấy tất cả tài liệu làm cơ sở
        has_slot254 // Loại trừ những tài liệu có giá trị ở slot 254
    );
    Xapian::Query slot254_filter =
        Xapian::Query(Xapian::Query::OP_OR, slot254_ge_block, slot254_is_null);

    value_range_queries.push_back(slot254_filter);

    Xapian::Query combined_range_query =
        (value_range_queries.size() == 1)
            ? value_range_queries[0]
            : Xapian::Query(Xapian::Query::OP_AND, value_range_queries.begin(),
                            value_range_queries.end());

    final_query = Xapian::Query(Xapian::Query::OP_FILTER, final_query,
                                combined_range_query);
    std::cerr << "final_query:  " << final_query.get_description() << std::endl;

    Xapian::Enquire enquire(db);
    enquire.set_query(final_query);

    if (sort_by_value_slot) {
      try {
        enquire.set_sort_by_value_then_relevance(*sort_by_value_slot,
                                                 sort_ascending);
      } catch (const Xapian::InvalidArgumentError &e) {
        std::cerr << "Lỗi sắp xếp: " << e.get_msg() << std::endl;
      }
    }

    Xapian::MSet mset = enquire.get_mset(offset, limit);
    estimated_total = mset.get_matches_estimated();

    for (Xapian::MSetIterator i = mset.begin(); i != mset.end(); ++i) {

      try {
        Xapian::Document doc = i.get_document();
        std::string val253_str = doc.get_value(253);
        std::string val254_str = doc.get_value(254);

        double val253 = -1.0;
        double val254 = -1.0;

        try {
          if (!val253_str.empty()) {
            val253 = Xapian::sortable_unserialise(val253_str);
          }
          if (!val254_str.empty()) {
            val254 = Xapian::sortable_unserialise(val254_str);
          }
        } catch (const Xapian::Error &e) {
          std::cerr << "Cảnh báo unserialise slot 253/254: " << e.get_msg()
                    << std::endl;
        }

        std::cerr << "[DOCID: " << *i << "] Rank: " << i.get_rank()
                  << ", Percent: " << i.get_percent() << ", Slot253: " << val253
                  << ", Slot254: " << val254 << ", Data: " << doc.get_data()
                  << std::endl;

        results.push_back({*i, i.get_rank(), i.get_percent(), doc.get_data()});
      } catch (const Xapian::Error &e) {
        std::cerr << "Lỗi lấy document ID " << *i << ": " << e.get_msg()
                  << std::endl;
      }
    }
  } catch (const Xapian::Error &e) {
    std::cerr << "Lỗi Xapian trong quá trình search: " << e.get_msg()
              << std::endl;
    throw;
  }
  std::cerr << "Estimated_total: " << estimated_total << std::endl;
  return {results, estimated_total};
}

CppSearchParams decodeSearchParams(const std::vector<uint8_t> &encodedData) {
  CppSearchParams params;
  size_t currentOffset = 0;

  // 1. Giải mã 'queries' (string)
  size_t queriesOffset =
      encoding::readUint256AsUint64(encodedData, currentOffset);
  currentOffset += 32;
  size_t queriesLength =
      encoding::readUint256AsUint64(encodedData, queriesOffset);
  params.queries = encoding::readStringFromData(encodedData, queriesOffset + 32,
                                                queriesLength);

  // 2. Giải mã 'prefixMap' (PrefixEntry[])
  size_t prefixMapOffset =
      encoding::readUint256AsUint64(encodedData, currentOffset);
  currentOffset += 32;
  size_t prefixMapLength =
      encoding::readUint256AsUint64(encodedData, prefixMapOffset);
  size_t prefixMapDataOffset = prefixMapOffset + 32;

  for (size_t i = 0; i < prefixMapLength; ++i) {
    CppPrefixEntry entry;

    // Giải mã 'key' (string)
    size_t keyOffset =
        encoding::readUint256AsUint64(encodedData, prefixMapDataOffset);
    size_t keyLength = encoding::readUint256AsUint64(encodedData, keyOffset);
    entry.key =
        encoding::readStringFromData(encodedData, keyOffset + 32, keyLength);
    prefixMapDataOffset += 32;

    // Giải mã 'value' (string)
    size_t valueOffset =
        encoding::readUint256AsUint64(encodedData, prefixMapDataOffset);
    size_t valueLength =
        encoding::readUint256AsUint64(encodedData, valueOffset);
    entry.value = encoding::readStringFromData(encodedData, valueOffset + 32,
                                               valueLength);
    prefixMapDataOffset += 32;

    params.prefixMap.push_back(entry);
  }

  // 3. Giải mã 'stemmerLang' (string)
  size_t stemmerLangOffset =
      encoding::readUint256AsUint64(encodedData, currentOffset);
  currentOffset += 32;
  size_t stemmerLangLength =
      encoding::readUint256AsUint64(encodedData, stemmerLangOffset);
  params.stemmerLang = encoding::readStringFromData(
      encodedData, stemmerLangOffset + 32, stemmerLangLength);

  // 4. Giải mã 'stopWords' (string[])
  size_t stopWordsOffset =
      encoding::readUint256AsUint64(encodedData, currentOffset);
  currentOffset += 32;
  size_t stopWordsLength =
      encoding::readUint256AsUint64(encodedData, stopWordsOffset);
  size_t stopWordsDataOffset = stopWordsOffset + 32;

  for (size_t i = 0; i < stopWordsLength; ++i) {
    size_t wordOffset =
        encoding::readUint256AsUint64(encodedData, stopWordsDataOffset);
    size_t wordLength = encoding::readUint256AsUint64(encodedData, wordOffset);
    params.stopWords.push_back(
        encoding::readStringFromData(encodedData, wordOffset + 32, wordLength));
    stopWordsDataOffset += 32;
  }

  // 5. Giải mã 'offset' (uint64)
  params.offset = encoding::readUint256AsUint64(encodedData, currentOffset);
  currentOffset += 32;

  // 6. Giải mã 'limit' (uint64)
  params.limit = encoding::readUint256AsUint64(encodedData, currentOffset);
  currentOffset += 32;

  // 7. Giải mã 'sortByValueSlot' (uint)
  params.sortByValueSlot =
      encoding::readUint256AsUint64(encodedData, currentOffset);
  currentOffset += 32;

  // 8. Giải mã 'sortAscending' (bool)
  params.sortAscending = encoding::readBoolPadded(encodedData, currentOffset);
  currentOffset += 32;

  // 9. Giải mã 'rangeFilters' (RangeFilter[])
  size_t rangeFiltersOffset =
      encoding::readUint256AsUint64(encodedData, currentOffset);
  currentOffset += 32;
  size_t rangeFiltersLength =
      encoding::readUint256AsUint64(encodedData, rangeFiltersOffset);
  size_t rangeFiltersDataOffset = rangeFiltersOffset + 32;

  for (size_t i = 0; i < rangeFiltersLength; ++i) {
    CppRangeFilter filter;

    // Giải mã 'slot' (uint)
    filter.slot =
        encoding::readUint256AsUint64(encodedData, rangeFiltersDataOffset);
    rangeFiltersDataOffset += 32;

    // Giải mã 'startSerialised' (string)
    size_t startOffset =
        encoding::readUint256AsUint64(encodedData, rangeFiltersDataOffset);
    size_t startLength =
        encoding::readUint256AsUint64(encodedData, startOffset);
    filter.startSerialised = encoding::readStringFromData(
        encodedData, startOffset + 32, startLength);
    rangeFiltersDataOffset += 32;

    // Giải mã 'endSerialised' (string)
    size_t endOffset =
        encoding::readUint256AsUint64(encodedData, rangeFiltersDataOffset);
    size_t endLength = encoding::readUint256AsUint64(encodedData, endOffset);
    filter.endSerialised =
        encoding::readStringFromData(encodedData, endOffset + 32, endLength);
    rangeFiltersDataOffset += 32;

    params.rangeFilters.push_back(filter);
  }

  return params;
}
// void XapianSearcher::dumpIndex()
// {
//     try
//     {

//         for (Xapian::TermIterator it = db.allterms_begin(); it !=
//         db.allterms_end(); ++it)
//         {
//             std::cout << "Term: " << *it << ", Số lần xuất hiện: " <<
//             it.get_termfreq() << std::endl;
//         }
//     }
//     catch (const Xapian::Error &e)
//     {
//         std::cerr << "Lỗi khi liệt kê từ khóa: " << e.get_msg() << std::endl;
//     }
// }
void XapianSearcher::dumpIndex() {
  try {
    std::cout << "\n========== [DUMP XAPIAN INDEX] ==========" << std::endl;

    // 1. In toàn bộ từ khóa đang có trong CSDL
    std::cout << "--- TẤT CẢ TỪ KHÓA (TERMS) ---" << std::endl;
    for (Xapian::TermIterator it = db.allterms_begin(); it != db.allterms_end();
         ++it) {
      std::cout << "Term: " << *it << " | Tần suất: " << it.get_termfreq()
                << std::endl;
    }

    // 2. In toàn bộ chi tiết của từng Document
    std::cout << "\n--- TẤT CẢ TÀI LIỆU (DOCUMENTS) ---" << std::endl;
    Xapian::doccount last_docid = db.get_lastdocid();
    std::cout << "Tong so Document (last_docid): " << last_docid << std::endl;

    for (Xapian::docid i = 1; i <= last_docid; ++i) {
      try {
        Xapian::Document doc = db.get_document(i);
        std::cout << "\n>> DocID: " << i << std::endl;

        std::cout << "  [Data]: " << doc.get_data() << std::endl;

        std::cout << "  [Terms]: ";
        for (Xapian::TermIterator it = doc.termlist_begin();
             it != doc.termlist_end(); ++it) {
          std::cout << *it << " ";
        }
        std::cout << std::endl;

        std::cout << "  [Values]: ";
        for (Xapian::ValueIterator vit = doc.values_begin();
             vit != doc.values_end(); ++vit) {
          std::cout << "(Slot " << vit.get_valueno() << ": " << *vit << ") ";
        }
        std::cout << std::endl;

      } catch (const Xapian::DocNotFoundError &) {
        std::cout << ">> DocID: " << i << " [DA BI XOA (DocNotFoundError)]"
                  << std::endl;
      }
    }
    std::cout << "========================================\n" << std::endl;
  } catch (const Xapian::Error &e) {
    std::cerr << "Lỗi khi dump Index: " << e.get_msg() << std::endl;
  }
}

// *** Triển khai hàm searchABI ***
std::pair<std::vector<SearchResult>, Xapian::doccount>
XapianSearcher::searchABI(const std::vector<uint8_t> &abi_encoded_params) {
  // 1. Decode ABI call data
  DecodedQuerySearchArgs decoded_args;
  try {
    // *** Gọi hàm decode cấp cao ***
    decoded_args = decodeQuerySearchCallData(abi_encoded_params);
  } catch (const std::exception &e) {
    throw std::runtime_error(
        std::string("searchABI: Failed to decode call data: ") + e.what());
  }

  // Lấy ra CppSearchParams đã decode
  const CppSearchParams &params = decoded_args.params;

  // 2. Chuyển đổi CppSearchParams -> tham số cho hàm search gốc
  // (Logic chuyển đổi giữ nguyên như câu trả lời trước)
  std::vector<std::string> search_queries;
  if (!params.queries.empty()) {
    search_queries.push_back(params.queries);
  }
  Xapian::Query::op search_combine_op = Xapian::Query::OP_AND;
  Xapian::Query::op search_default_op = Xapian::Query::OP_AND;

  std::map<std::string, std::string> search_prefix_map;
  for (const auto &entry : params.prefixMap) {
    search_prefix_map[entry.key] = entry.value;
  }

  std::optional<std::string> search_stemmer_lang =
      params.stemmerLang.empty() ? std::nullopt
                                 : std::make_optional(params.stemmerLang);

  std::optional<std::vector<std::string>> search_stop_words =
      params.stopWords.empty() ? std::nullopt
                               : std::make_optional(params.stopWords);

  Xapian::doccount search_offset = static_cast<Xapian::doccount>(params.offset);
  Xapian::doccount search_limit = static_cast<Xapian::doccount>(params.limit);
  bool search_sort_ascending = params.sortAscending;

  std::optional<Xapian::valueno> search_sort_slot =
      (params.sortByValueSlot == CppSearchParams::NO_SORT_SLOT)
          ? std::nullopt
          : std::make_optional(
                static_cast<Xapian::valueno>(params.sortByValueSlot));

  std::vector<RangeFilter> search_range_filters;
  search_range_filters.reserve(params.rangeFilters.size());
  for (const auto &cpp_filter : params.rangeFilters) {
    search_range_filters.push_back(
        {static_cast<Xapian::valueno>(cpp_filter.slot),
         cpp_filter.startSerialised, cpp_filter.endSerialised});
  }

  // 3. Gọi hàm search gốc
  // *** Lưu ý: Tham số dbname từ decoded_args.dbname chưa được sử dụng ở đây
  // ***
  // *** Hàm search gốc không nhận dbname. Cần điều chỉnh nếu logic
  // XapianSearcher ***
  // *** phụ thuộc vào dbname (ví dụ: mở DB khác nhau dựa trên tên) ***
  std::cout << "Thông tin: Đang tìm kiếm trên DB (tên '" << decoded_args.dbname
            << "' từ ABI chưa được dùng trực tiếp trong hàm search này)"
            << std::endl;

  return this->search(search_queries, search_combine_op, search_default_op,
                      search_prefix_map, search_stemmer_lang, search_stop_words,
                      search_offset, search_limit, search_sort_slot,
                      search_sort_ascending, search_range_filters);
}

std::vector<uint8_t> XapianSearcher::internalEncodeResultsArrayContent(
    const std::vector<SearchResult> &results) {
  std::vector<uint8_t> encoded_content;
  // Ước tính kích thước có thể không chính xác hoàn toàn nhưng giúp tối ưu
  encoded_content.reserve(32 +
                          results.size() * 180); // Điều chỉnh ước tính một chút

  // === Phần 1: Mã hóa số lượng phần tử trong mảng ===
  // Ghi 32 byte chứa số lượng phần tử (N)
  encoding::appendUint256(encoded_content, results.size());

  // === Phần 2: Tính toán và mã hóa các offset đến dữ liệu của từng phần tử ===
  // Theo quy tắc ABI cho mảng động chứa kiểu động:
  // [độ dài mảng N] [offset đến elem 0] [offset đến elem 1] ... [offset đến
  // elem N-1] [dữ liệu elem 0] [dữ liệu elem 1] ... Offset được tính từ đầu của
  // phần mã hóa mảng này (tức là từ sau độ dài N).

  // Tính toán vị trí bắt đầu của dữ liệu phần tử đầu tiên (elem 0)
  // Nó nằm sau slot độ dài (32 bytes) và N slot offset (N * 32 bytes)
  size_t N = results.size();
  size_t data_start_offset = N * 32; // = 0x20 + N * 0x20

  if (N == 0) {
    // Không có phần tử, chỉ cần trả về độ dài mảng là 0
    return encoded_content;
  }

  // Tính toán kích thước của từng phần tử được mã hóa trước để xác định các
  // offset
  std::vector<size_t> encoded_element_sizes(N);
  for (size_t i = 0; i < N; ++i) {
    // Kích thước phần cố định của struct SearchResult:
    // docid (32) + rank (32) + percent (32) + offset_to_data (32) = 128 bytes
    // (0x80)
    size_t fixed_part_size = 4 * 32; // 128 bytes

    size_t string_len = results[i].data.length();
    size_t padded_len = ((string_len + 31) / 32) * 32;
    size_t dynamic_part_size = 32 + padded_len; // length prefix + padded data

    encoded_element_sizes[i] = fixed_part_size + dynamic_part_size;
  }

  // Ghi các offset vào encoded_content
  // Offset đầu tiên là vị trí bắt đầu dữ liệu (data_start_offset)
  encoding::appendUint256(encoded_content, data_start_offset);

  size_t current_offset = data_start_offset;
  for (size_t i = 1; i < N; ++i) {
    // Offset của phần tử i = offset của phần tử (i-1) + kích thước mã hóa của
    // phần tử (i-1)
    current_offset += encoded_element_sizes[i - 1];
    encoding::appendUint256(encoded_content, current_offset);
  }
  std::cerr << "results length " << results.size() << std::endl;

  // === Phần 3: Mã hóa dữ liệu của từng phần tử SearchResult ===
  // Phần này cần khớp với cách tính size ở trên
  for (const auto &result : results) {
    // Phần cố định của Struct:
    // Thứ tự: docid, rank, percent, offset_to_string_data
    encoding::appendUint256(encoded_content, result.docid);
    encoding::appendUint256(encoded_content, result.rank);
    encoding::appendInt256(encoded_content, result.percent);
    // Offset đến phần dữ liệu string (độ dài + data bytes + padding)
    // Tính từ đầu của struct này. Phần cố định có 4 slot = 128 bytes.
    encoding::appendUint256(encoded_content, 128); // Offset = 0x80

    // Phần động của Struct (string data hoặc bytes):
    size_t data_length = result.data.length();

    // ABI encoding for string/bytes: length prefix + data + padding
    encoding::appendUint256(encoded_content, data_length);

    encoded_content.insert(
        encoded_content.end(),
        reinterpret_cast<const uint8_t *>(result.data.data()),
        reinterpret_cast<const uint8_t *>(result.data.data()) + data_length);

    // Pad to 32-byte boundary
    size_t current_size = encoded_content.size();
    size_t padded_size = ((current_size + 31) / 32) * 32;
    encoded_content.resize(padded_size, 0);
  }

  return encoded_content;
}

/**
 * @brief Encodes the entire SearchResultsPage struct according to ABI rules.
 * The struct corresponds to the tuple (uint256, SearchResult[]).
 *
 * @param total_count The total number of results found (static uint256 part).
 * @param results The vector of SearchResult objects for the current page
 * (dynamic array part).
 * @return ABI encoded byte vector representing the SearchResultsPage tuple.
 */
std::vector<uint8_t> XapianSearcher::encodeSearchResultsPage(
    uint64_t total_count, const std::vector<SearchResult> &results) {
  std::vector<uint8_t> encoded_page;
  // Ước tính kích thước
  encoded_page.reserve(64 + results.size() * 200); // Ước tính

  // --- Phần đầu (Head) của tuple (SearchResultsPage) ---
  // Bao gồm các kiểu tĩnh và offset đến các kiểu động.
  // Tuple này có dạng (uint256, SearchResult[]), gồm 1 tĩnh, 1 động.
  // Kích thước phần đầu = (1 tĩnh + 1 động) * 32 bytes = 64 bytes.
  const size_t head_size = 64;

  // --- Mã hóa phần đầu ---

  // --- SỬA LỖI: Xóa dòng không hợp lệ này ---
  // encoding::appendUint256(encoded_page, 0x20); // <--- Dòng này sai logic
  // ABI, cần xóa

  // 1. Thành phần tĩnh đầu tiên - total_count (uint256)
  encoding::appendUint256(encoded_page, total_count);

  // 2. Thành phần động thứ hai - results (SearchResult[])
  // Ghi offset đến vị trí bắt đầu của dữ liệu mảng results.
  // Dữ liệu mảng sẽ bắt đầu ngay sau phần đầu (head) của tuple.
  encoding::appendUint256(encoded_page,
                          head_size); // offset = 64 bytes = 0x40

  // --- Mã hóa phần đuôi (Tail) ---
  // Chứa dữ liệu của các thành phần động (ở đây chỉ có mảng results)

  // 3. Mã hóa nội dung của mảng động (results)
  std::vector<uint8_t> encoded_results =
      internalEncodeResultsArrayContent(results);

  // 4. Nối dữ liệu mảng đã mã hóa vào sau phần đầu
  encoded_page.insert(encoded_page.end(), encoded_results.begin(),
                      encoded_results.end());

  return encoded_page;
}
