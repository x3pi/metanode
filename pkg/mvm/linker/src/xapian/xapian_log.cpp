// linker/src/xapian/xapian_log.cpp

#include "xapian/xapian_log.h"     // Header định nghĩa LogEntry
#include "xapian/xapian_manager.h" // Bao gồm để sử dụng ComprehensiveLog
#include <vector>
#include <string>
#include <cstdint>
#include <stdexcept>    // Cho std::out_of_range, std::overflow_error
#include <arpa/inet.h>  // Cho htonl
#include <netinet/in.h> // Cho ntohl
#include <cstring>      // Cho memcpy
#include <variant>      // Cho std::visit, std::monostate
#include <optional>     // Cho std::optional, std::nullopt
#include <map>          // Cho ComprehensiveLog (nếu nó dùng map, hiện tại không)
#include <utility>      // Cho std::move

// Không gian tên chứa các hàm tiện ích mã hóa/giải mã nhị phân
namespace BinaryEncoding
{
    // --- Các hàm nối dữ liệu vào buffer ---

    // Nối một byte (uint8_t) vào cuối buffer
    void append_uint8(std::vector<uint8_t> &buffer, uint8_t value)
    {
        buffer.push_back(value);
    }

    // Nối một số nguyên 32-bit không dấu (uint32_t) vào buffer theo định dạng Big Endian
    void append_uint32_be(std::vector<uint8_t> &buffer, uint32_t value)
    {
        uint32_t net_value = htonl(value); // Chuyển sang định dạng network (Big Endian)
        const uint8_t *bytes = reinterpret_cast<const uint8_t *>(&net_value);
        buffer.push_back(bytes[0]); // Nối từng byte
        buffer.push_back(bytes[1]);
        buffer.push_back(bytes[2]);
        buffer.push_back(bytes[3]);
    }

    // Nối một giá trị boolean vào buffer dưới dạng một byte (0 hoặc 1)
    void append_bool_u8(std::vector<uint8_t> &buffer, bool value)
    {
        buffer.push_back(static_cast<uint8_t>(value ? 1 : 0));
    }

    // Nối một chuỗi ký tự vào buffer, với độ dài (uint32_t Big Endian) đứng trước nội dung
    void append_length_prefixed_string(std::vector<uint8_t> &buffer, const std::string &str)
    {
        if (str.length() > UINT32_MAX)
        {
            throw std::overflow_error("Độ dài chuỗi vượt quá giới hạn uint32_t");
        }
        uint32_t len = static_cast<uint32_t>(str.length());
        append_uint32_be(buffer, len); // Nối độ dài trước
        // Nối nội dung chuỗi
        for (char c : str)
        {
            buffer.push_back(static_cast<uint8_t>(c));
        }
    }

    // Nối một vector byte khác vào cuối buffer hiện tại
    void append_bytes(std::vector<uint8_t> &buffer, const std::vector<uint8_t> &data_to_append)
    {
        buffer.reserve(buffer.size() + data_to_append.size()); // Tối ưu hóa cấp phát bộ nhớ
        for (uint8_t b : data_to_append)
        {
            buffer.push_back(b);
        }
    }

    // --- Các hàm đọc dữ liệu từ buffer ---

    // Đọc một số nguyên 32-bit không dấu (uint32_t) từ buffer theo định dạng Big Endian
    // Cập nhật vị trí đọc hiện tại (current_pos)
    uint32_t read_uint32_be(const std::vector<uint8_t> &bytes, size_t &current_pos)
    {
        if (current_pos + sizeof(uint32_t) > bytes.size())
        {
            throw std::out_of_range("Đọc uint32 ra ngoài giới hạn buffer");
        }
        uint32_t net_value;
        memcpy(&net_value, bytes.data() + current_pos, sizeof(uint32_t)); // Sao chép byte
        current_pos += sizeof(uint32_t);                                  // Cập nhật vị trí đọc
        return ntohl(net_value);                                          // Chuyển từ network (Big Endian) sang host
    }

    // Đọc một byte (uint8_t) từ buffer và cập nhật vị trí đọc
    uint8_t read_uint8(const std::vector<uint8_t> &bytes, size_t &current_pos)
    {
        if (current_pos >= bytes.size())
        {
            throw std::out_of_range("Đọc uint8 ra ngoài giới hạn buffer");
        }
        return bytes[current_pos++]; // Trả về byte và tăng vị trí đọc
    }

    // Đọc một giá trị boolean (được lưu dưới dạng 1 byte) từ buffer và cập nhật vị trí đọc
    bool read_bool_u8(const std::vector<uint8_t> &bytes, size_t &current_pos)
    {
        if (current_pos >= bytes.size())
        {
            throw std::out_of_range("Đọc bool ra ngoài giới hạn buffer");
        }
        return (bytes[current_pos++] != 0); // Trả về true nếu byte khác 0
    }

    // Đọc một số lượng `length` byte từ buffer và cập nhật vị trí đọc
    std::vector<uint8_t> read_bytes(const std::vector<uint8_t> &bytes, size_t &current_pos, uint32_t length)
    {
        // Kiểm tra tràn số và giới hạn buffer
        if (current_pos > bytes.size() || length > bytes.size() - current_pos)
        {
            throw std::out_of_range("Phép tính đọc byte gây tràn số hoặc ra ngoài giới hạn");
        }
        // Tạo vector kết quả từ phần dữ liệu cần đọc
        std::vector<uint8_t> result(bytes.begin() + current_pos, bytes.begin() + current_pos + length);
        current_pos += length; // Cập nhật vị trí đọc
        return result;
    }

    // Đọc một chuỗi ký tự (có độ dài đứng trước) từ buffer và cập nhật vị trí đọc
    std::string read_length_prefixed_string(const std::vector<uint8_t> &bytes, size_t &current_pos)
    {
        uint32_t len = read_uint32_be(bytes, current_pos); // Đọc độ dài trước
        // Kiểm tra tràn số và giới hạn buffer sau khi biết độ dài
        if (current_pos > bytes.size() || len > bytes.size() - current_pos)
        {
            throw std::out_of_range("Phép tính đọc dữ liệu chuỗi gây tràn số hoặc ra ngoài giới hạn");
        }
        // Tạo chuỗi từ phần dữ liệu tương ứng
        std::string result(reinterpret_cast<const char *>(bytes.data() + current_pos), len);
        current_pos += len; // Cập nhật vị trí đọc
        return result;
    }

} // namespace BinaryEncoding

// Không gian tên cho các cấu trúc và hàm liên quan đến log của Xapian
namespace XapianLog
{
    // Chuyển đổi một LogEntry thành dạng vector byte để lưu trữ hoặc truyền đi
    std::vector<uint8_t> LogEntry::serialize() const
    {
        std::vector<uint8_t> bytes;
        // Nối mã opcode (loại operation) vào đầu
        BinaryEncoding::append_uint8(bytes, static_cast<uint8_t>(this->op));

        // Sử dụng std::visit để xử lý dữ liệu cụ thể tùy theo loại operation trong variant `data`
        std::visit([&bytes](const auto &arg)
                   {
                       using T = std::decay_t<decltype(arg)>; // Xác định kiểu dữ liệu thực tế trong variant
                       // Serialize dữ liệu tương ứng với từng loại operation
                       if constexpr (std::is_same_v<T, NewDocData>)
                       {
                           BinaryEncoding::append_uint32_be(bytes, arg.docid);
                           BinaryEncoding::append_length_prefixed_string(bytes, arg.data);
                       }
                       else if constexpr (std::is_same_v<T, DelDocData>)
                       {
                           BinaryEncoding::append_uint32_be(bytes, arg.docid);
                       }
                       else if constexpr (std::is_same_v<T, AddValueData>)
                       {
                           BinaryEncoding::append_uint32_be(bytes, arg.docid);
                           BinaryEncoding::append_uint32_be(bytes, arg.slot);
                           BinaryEncoding::append_bool_u8(bytes, arg.is_serialised);
                           BinaryEncoding::append_length_prefixed_string(bytes, arg.value);
                       }
                       else if constexpr (std::is_same_v<T, AddTermData>)
                       {
                           BinaryEncoding::append_uint32_be(bytes, arg.docid);
                           BinaryEncoding::append_length_prefixed_string(bytes, arg.term);
                       }
                       else if constexpr (std::is_same_v<T, SetDataData>)
                       {
                           BinaryEncoding::append_uint32_be(bytes, arg.docid);
                           BinaryEncoding::append_length_prefixed_string(bytes, arg.data);
                       }
                       else if constexpr (std::is_same_v<T, IndexTextData>)
                       {
                           BinaryEncoding::append_uint32_be(bytes, arg.docid);
                           BinaryEncoding::append_uint32_be(bytes, arg.wdf_inc);
                           BinaryEncoding::append_length_prefixed_string(bytes, arg.prefix);
                           BinaryEncoding::append_length_prefixed_string(bytes, arg.text);
                       }
                       // Không làm gì nếu là std::monostate (trạng thái rỗng của variant)
                       else if constexpr (std::is_same_v<T, std::monostate>)
                       {
                       }
                       // Các kiểu dữ liệu khác không được xử lý (có thể thêm báo lỗi nếu cần)
                   },
                   this->data); // Áp dụng lambda lên variant `data`

        return bytes; // Trả về vector byte đã serialize
    }

    // Chuyển đổi một vector byte trở lại thành LogEntry (nếu có thể)
    std::optional<LogEntry> LogEntry::deserialize(const std::vector<uint8_t> &bytes)
    {
        if (bytes.empty())
            return std::nullopt; // Trả về rỗng nếu input rỗng

        LogEntry entry;         // Tạo LogEntry để chứa kết quả
        size_t current_pos = 0; // Vị trí đọc hiện tại trong vector byte

        try
        {
            // Đọc mã opcode từ byte đầu tiên
            if (current_pos >= bytes.size())
                throw std::out_of_range("Không thể đọc opcode");
            entry.op = static_cast<Operation>(BinaryEncoding::read_uint8(bytes, current_pos));

            // Dựa vào opcode, đọc và gán dữ liệu tương ứng vào variant `data`
            switch (entry.op)
            {
            case Operation::NEW_DOC:
            {
                NewDocData data;
                data.docid = BinaryEncoding::read_uint32_be(bytes, current_pos);
                data.data = BinaryEncoding::read_length_prefixed_string(bytes, current_pos);
                entry.data = data; // Gán vào variant
                break;
            }
            case Operation::DEL_DOC:
            {
                DelDocData data;
                data.docid = BinaryEncoding::read_uint32_be(bytes, current_pos);
                entry.data = data;
                break;
            }
            case Operation::ADD_VALUE:
            {
                AddValueData data;
                data.docid = BinaryEncoding::read_uint32_be(bytes, current_pos);
                data.slot = BinaryEncoding::read_uint32_be(bytes, current_pos);
                data.is_serialised = BinaryEncoding::read_bool_u8(bytes, current_pos);
                data.value = BinaryEncoding::read_length_prefixed_string(bytes, current_pos);
                entry.data = data;
                break;
            }
            case Operation::ADD_TERM:
            {
                AddTermData data;
                data.docid = BinaryEncoding::read_uint32_be(bytes, current_pos);
                data.term = BinaryEncoding::read_length_prefixed_string(bytes, current_pos);
                entry.data = data;
                break;
            }
            case Operation::SET_DATA:
            {
                SetDataData data;
                data.docid = BinaryEncoding::read_uint32_be(bytes, current_pos);
                data.data = BinaryEncoding::read_length_prefixed_string(bytes, current_pos);
                entry.data = data;
                break;
            }
            case Operation::INDEX_TEXT:
            {
                IndexTextData data;
                data.docid = BinaryEncoding::read_uint32_be(bytes, current_pos);
                data.wdf_inc = BinaryEncoding::read_uint32_be(bytes, current_pos);
                data.prefix = BinaryEncoding::read_length_prefixed_string(bytes, current_pos);
                data.text = BinaryEncoding::read_length_prefixed_string(bytes, current_pos);
                entry.data = data;
                break;
            }
            default:
                // Opcode không xác định hoặc không được xử lý
                // entry.data sẽ giữ giá trị mặc định là std::monostate
                break;
            }
        }
        // Bắt các lỗi có thể xảy ra trong quá trình đọc (ví dụ: đọc ra ngoài giới hạn)
        catch (const std::out_of_range &)
        {
            return std::nullopt;
        }
        catch (const std::exception &)
        {
            return std::nullopt;
        }
        catch (...)
        {
            return std::nullopt;
        }

        // Tùy chọn: Kiểm tra xem đã đọc hết toàn bộ vector byte chưa
        // if (current_pos != bytes.size()) { /* Có dữ liệu thừa */ }

        return entry; // Trả về LogEntry đã được deserialize thành công
    }

    // Chuyển đổi một ComprehensiveLog thành dạng vector byte
    std::vector<uint8_t> ComprehensiveLog::serialize() const
    {
        std::vector<uint8_t> buffer;
        try
        {
            // 0. Serialize tên database trước tiên
            BinaryEncoding::append_length_prefixed_string(buffer, this->db_name);

            // 1. Serialize danh sách các log Xapian document
            // Nối số lượng log entry vào buffer
            BinaryEncoding::append_uint32_be(buffer, static_cast<uint32_t>(xapian_doc_logs.size()));
            // Lặp qua từng log entry, serialize nó, rồi nối độ dài và dữ liệu vào buffer
            for (const auto &entry : xapian_doc_logs)
            {
                std::vector<uint8_t> entry_bytes = entry.serialize();                                // Serialize từng entry
                BinaryEncoding::append_uint32_be(buffer, static_cast<uint32_t>(entry_bytes.size())); // Nối độ dài
                BinaryEncoding::append_bytes(buffer, entry_bytes);                                   // Nối dữ liệu
            }
            // Logic serialize cho các loại log khác (schema, tags) sẽ ở đây nếu có
        }
        catch (const std::exception &e)
        {
            // Nếu có lỗi trong quá trình serialize, ném lại lỗi
            throw;
        }
        return buffer; // Trả về buffer chứa dữ liệu đã serialize
    }

    // Chuyển đổi một vector byte trở lại thành ComprehensiveLog (nếu có thể)
    std::optional<ComprehensiveLog> ComprehensiveLog::deserialize(const std::vector<uint8_t> &data)
    {
        ComprehensiveLog logs;  // Tạo đối tượng để chứa kết quả
        size_t current_pos = 0; // Vị trí đọc hiện tại

        try
        {
            // 0. Deserialize tên database trước
            logs.db_name = BinaryEncoding::read_length_prefixed_string(data, current_pos);

            // 1. Deserialize danh sách log Xapian document
            // Đọc số lượng log entry
            if (current_pos + sizeof(uint32_t) > data.size())
                throw std::out_of_range("Không thể đọc số lượng xapian_doc_logs");
            uint32_t xapian_logs_count = BinaryEncoding::read_uint32_be(data, current_pos);
            logs.xapian_doc_logs.reserve(xapian_logs_count); // Cấp phát trước bộ nhớ

            // Lặp để đọc từng log entry
            for (uint32_t i = 0; i < xapian_logs_count; ++i)
            {
                // Đọc độ dài của log entry tiếp theo
                if (current_pos + sizeof(uint32_t) > data.size())
                    throw std::out_of_range("Không thể đọc kích thước LogEntry");
                uint32_t entry_size = BinaryEncoding::read_uint32_be(data, current_pos);
                // Đọc dữ liệu byte của log entry
                std::vector<uint8_t> entry_bytes = BinaryEncoding::read_bytes(data, current_pos, entry_size);
                // Deserialize dữ liệu byte thành LogEntry
                auto deserialized_entry = XapianLog::LogEntry::deserialize(entry_bytes);
                if (!deserialized_entry)
                {
                    // Lỗi khi deserialize một entry, trả về rỗng
                    return std::nullopt;
                }
                // Thêm entry đã deserialize vào danh sách (sử dụng std::move để tối ưu)
                logs.xapian_doc_logs.push_back(std::move(*deserialized_entry));
            }

            // Logic deserialize cho các loại log khác (schema, tags) sẽ ở đây nếu có

            // Tùy chọn: Kiểm tra xem có dữ liệu thừa sau khi đọc không
            // if (current_pos != data.size()) { /* Cảnh báo dữ liệu thừa */ }
        }
        // Bắt các lỗi có thể xảy ra
        catch (const std::out_of_range &)
        {
            return std::nullopt;
        }
        catch (const std::exception &)
        {
            return std::nullopt;
        }
        catch (...)
        {
            return std::nullopt;
        }

        return logs; // Trả về ComprehensiveLog đã deserialize thành công
    }

    void ComprehensiveLog::removeLogsUntilNearestEndCommand()
    {
        if (xapian_doc_logs.empty())
        {
            return; // Không làm gì nếu vector rỗng
        }
        
        bool found_start = false;
        for (auto it = xapian_doc_logs.rbegin(); it != xapian_doc_logs.rend(); ++it)
        {
            if (it->command_type == XapianLog::CommandType::START)
            {
                // Tìm thấy START, xóa tất cả các log kể từ START này đến cuối
                auto idx = std::distance(it, xapian_doc_logs.rend()) - 1;
                xapian_doc_logs.erase(xapian_doc_logs.begin() + idx, xapian_doc_logs.end());
                found_start = true;
                break;
            }
        }
        
        // Nếu không tìm thấy START (có thể do lỗi), clear toàn bộ để an toàn vì transaction đang bị cancel
        if (!found_start) {
            xapian_doc_logs.clear();
        }
    }
    void ComprehensiveLog::push_back(const LogEntry &entry)
    {
        xapian_doc_logs.push_back(entry);
    }

}
