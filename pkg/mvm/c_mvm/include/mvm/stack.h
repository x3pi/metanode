// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once
#include "bigint.h" // Giả định file này định nghĩa uint256_t và các hàm liên quan

#include <deque>
#include <fmt/format_header_only.h> // Đảm bảo thư viện fmt có sẵn
#include <fmt/ostream.h>
#include <ostream>
#include <cstddef> // Cho std::size_t

namespace mvm
{
  /**
   * Stack used by Processor
   */
  class Stack
  {
  private:
    std::deque<uint256_t> st;

  public:
    // Giới hạn kích thước tối đa của stack theo chuẩn EVM
    static constexpr std::size_t MAX_SIZE = 1024;

    Stack() = default;

    /**
     * @brief Lấy và xóa phần tử trên cùng của stack.
     * @throw Exception nếu stack rỗng (underflow).
     * @return Giá trị uint256_t trên cùng.
     */
    uint256_t pop();

    /**
     * @brief Lấy và xóa phần tử trên cùng, chuyển đổi thành uint64_t.
     * @throw Exception nếu stack rỗng hoặc giá trị lớn hơn uint64_t::max.
     * @return Giá trị uint64_t trên cùng.
     */
    uint64_t pop64();

    /**
     * @brief Thêm một phần tử vào đỉnh stack.
     * @param val Giá trị uint256_t để thêm.
     * @throw Exception nếu stack đã đầy (overflow).
     * @throw std::runtime_error nếu cấp phát bộ nhớ thất bại.
     */
    void push(const uint256_t& val);

    /**
     * @brief Lấy kích thước hiện tại của stack.
     * @return Số phần tử trên stack.
     */
    uint64_t size() const; // Nên trả về std::size_t cho nhất quán? Nhưng giữ uint64_t theo code gốc

    /**
     * @brief Hoán đổi phần tử trên cùng (st[0]) với phần tử thứ i (st[i]).
     * Chỉ số i tính từ 1 (SWAP1 tương ứng với i=1).
     * @param i Chỉ số của phần tử cần hoán đổi (1 <= i < size()).
     * @throw Exception nếu chỉ số i không hợp lệ.
     */
    void swap(uint64_t i);

    /**
     * @brief Sao chép phần tử thứ a và đẩy bản sao lên đỉnh stack.
     * Chỉ số a tính từ 0 (DUP1 sao chép st[0], DUP2 sao chép st[1],...).
     * @param a Chỉ số của phần tử cần sao chép (0 <= a < size()).
     * @throw Exception nếu chỉ số a không hợp lệ hoặc stack đã đầy.
     * @throw std::runtime_error nếu cấp phát bộ nhớ thất bại.
     */
    void dup(uint64_t a);

    /**
     * @brief Hỗ trợ ghi stack ra ostream (ví dụ: cout).
     */
    friend std::ostream& operator<<(std::ostream& os, const Stack& s);

    /**
     * @brief Lấy tham chiếu const đến deque nội bộ (chủ yếu cho debug/test).
     */
    const std::deque<uint256_t>& getData() const { return st; }
  };
} // namespace mvm