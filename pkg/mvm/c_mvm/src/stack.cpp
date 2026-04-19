// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#include "mvm/stack.h"

#include "mvm/exception.h" // Giả định file này định nghĩa Exception và ET
#include "mvm/util.h"      // Giả định file này định nghĩa to_hex_string

#include <algorithm>
#include <limits>
#include <stdexcept> // Cần cho std::runtime_error
#include <string>    // Cần cho to_string

// using namespace std; // Có thể bỏ nếu không muốn dùng toàn cục

namespace mvm
{
  using ET = Exception::Type;

  uint256_t Stack::pop()
  {
    // Kiểm tra stack rỗng trước khi pop
    if (st.empty())
      throw Exception(ET::outOfBounds, "Stack underflow on pop");

    uint256_t val = st.front();
    st.pop_front();
    return val;
  }

  uint64_t Stack::pop64()
  {
    // Gọi pop() đã có kiểm tra underflow
    const auto val = pop();

    // Kiểm tra xem giá trị có nằm trong phạm vi uint64_t không
    if (val > std::numeric_limits<uint64_t>::max())
      throw Exception(
        ET::outOfBounds,
        "Value on stack (" + to_hex_string(val) + ") exceeds uint64_t maximum");

    return static_cast<uint64_t>(val);
  }

  void Stack::push(const uint256_t& val)
  {
    // Kiểm tra giới hạn stack TRƯỚC KHI push
    // Sử dụng >= MAX_SIZE để an toàn hơn, mặc dù == MAX_SIZE là đủ logic
    if (size() >= MAX_SIZE)
      throw Exception(
        ET::outOfBounds,
        "Stack overflow on push (size: " + std::to_string(size()) +
          ", limit: " + std::to_string(MAX_SIZE) + ")");

    try
    {
      st.push_front(val);
    }
    catch (const std::bad_alloc&)
    {
      // Xử lý lỗi cấp phát bộ nhớ
      throw std::runtime_error("bad_alloc while pushing onto stack");
    }
  }

  uint64_t Stack::size() const
  {
    // Trả về kích thước hiện tại của deque
    return st.size();
  }

  void Stack::swap(uint64_t i)
  {
    // SWAPi hoán đổi st[0] và st[i]. Chỉ số i phải >= 1 và < size().
    // SWAP0 không tồn tại.
    if (i == 0 || i >= size())
      throw Exception(
        ET::outOfBounds,
        "Stack swap index out of range (index: " + std::to_string(i) + ", size: " + std::to_string(size()) +
          ")");

    // Thực hiện hoán đổi
    std::swap(st[0], st[i]);
  }

  // === HÀM DUP ĐÃ CẬP NHẬT ===
  void Stack::dup(uint64_t a)
  {
    // DUPa sao chép st[a] lên đỉnh. Chỉ số 'a' phải >= 0 và < size().
    // DUP1 -> a=0, DUP2 -> a=1, ...
    if (a >= size())
      throw Exception(
        ET::outOfBounds,
        "Stack dup index out of range (index: " + std::to_string(a) + ", size: " + std::to_string(size()) + ")");

    // <<< THAY ĐỔI CHÍNH: Kiểm tra giới hạn stack TRƯỚC KHI push bản sao >>>
    // Sử dụng >= MAX_SIZE để an toàn hơn
    if (size() >= MAX_SIZE)
      throw Exception(
        ET::outOfBounds,
        "Stack overflow on dup (size: " + std::to_string(size()) +
          ", limit: " + std::to_string(MAX_SIZE) + ")");
    // <<< KẾT THÚC THAY ĐỔI CHÍNH >>>

    // Bây giờ mới thực hiện push bản sao
    try
    {
        // st[a] là phần tử cần sao chép (0-based index trong deque)
        st.push_front(st[a]);
    }
    catch (const std::bad_alloc&)
    {
      // Xử lý lỗi cấp phát bộ nhớ
      throw std::runtime_error("bad_alloc while duplicating onto stack");
    }
  }
  // === KẾT THÚC HÀM DUP ===

  // Operator<< để in stack ra stream (ví dụ: std::cout)
  std::ostream& operator<<(std::ostream& os, const Stack& s)
  {
    os << "Stack (size=" << s.size() << ", MAX_SIZE=" << Stack::MAX_SIZE << "):" << std::endl;
    int i = 0;
    // In từ đỉnh stack (chỉ số 0) xuống dưới
    for (const auto& elem : s.st)
    {
        // fmt::format yêu cầu fmt/format_header_only.h và fmt/ostream.h
        // Giả định to_hex_string(elem) hoạt động đúng
        os << fmt::format("  [{:03}]: {}", i++, to_hex_string(elem)) << std::endl;
    }
    if (s.st.empty()) {
        os << "  (empty)" << std::endl;
    }
    return os;
  }
} // namespace mvm