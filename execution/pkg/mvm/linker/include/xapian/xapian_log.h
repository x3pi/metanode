// linker/include/xapian/xapian_log.h
#ifndef XAPIAN_LOG_H
#define XAPIAN_LOG_H

#include <cstdint>
#include <string>
#include <vector>
#include <variant>
#include <optional>
#include <map> // Cần cho ComprehensiveLog

namespace XapianLog
{

    // Định nghĩa các loại hành động
    enum class Operation : uint8_t
    {
        UNKNOWN = 0,
        NEW_DOC = 1,
        DEL_DOC = 2,
        ADD_VALUE = 3,
        ADD_TERM = 4,
        SET_DATA = 5,
        INDEX_TEXT = 6
        // Thêm các hành động khác nếu cần
    };
    enum class CommandType : uint8_t
    {
        NORMAL = 0,
        START = 1,
        END = 2
    };
    // Định nghĩa cấu trúc cho từng hành động
    struct NewDocData
    {
        std::string docid;
        std::string data;
    };

    struct DelDocData
    {
        std::string docid;
    };

    struct AddValueData
    {
        std::string docid;
        uint32_t slot;
        std::string value;
        bool is_serialised;
    };

    struct AddTermData
    {
        std::string docid;
        std::string term;
    };

    struct SetDataData
    {
        std::string docid;
        std::string data;
    };

    struct IndexTextData
    {
        std::string docid;
        std::string text;
        uint32_t wdf_inc;
        std::string prefix;
    };

    // Sử dụng std::variant để lưu trữ một trong các cấu trúc trên
    using LogData = std::variant<
        std::monostate,
        NewDocData,
        DelDocData,
        AddValueData,
        AddTermData,
        SetDataData,
        IndexTextData>;

    // Cấu trúc LogEntry chính
    struct LogEntry
    {
        Operation op;
        LogData data;
        bool is_begin_transaction = true;
        CommandType command_type = CommandType::NORMAL;
        double blockNumber_double = 0;
        // --- Khai báo phương thức ---
        std::vector<uint8_t> serialize() const;
        static std::optional<LogEntry> deserialize(const std::vector<uint8_t> &bytes);
    };

    // --- Định nghĩa Struct ComprehensiveLog ---
    // (Struct này chứa LogEntry nên có thể đặt cùng file header hoặc file riêng)
    struct ComprehensiveLog
    {
        std::string db_name;
        std::vector<XapianLog::LogEntry> xapian_doc_logs;
        std::vector<uint8_t> serialize() const;
        static std::optional<ComprehensiveLog> deserialize(const std::vector<uint8_t> &data);
        void removeLogsUntilNearestEndCommand();
        void push_back(const LogEntry& entry);
    };

} // namespace XapianLog

#endif // XAPIAN_LOG_H