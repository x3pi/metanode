# Phân Tích Chi Tiết Logic Thực Thi Cross-Chain Gateway Bằng Lõi MVM (C++)

Tài liệu này giải thích tỉ mỉ từng dòng code bên trong hàm `handle_cross_chain_precompile` nằm ở file `cross_chain_precompile.cpp`, đặc biệt là khi nhận diện được lời gọi hàm `lockAndBridge` hoặc `sendMessage`.

---

## 1. Bắt Khớp Hàm Gọi (Function Selector)

```cpp
static const uint32_t SEL_LOCK_AND_BRIDGE = FunctionSelector::getFunctionSelectorFromString("lockAndBridge(address,uint256)");
static const uint32_t SEL_SEND_MESSAGE = FunctionSelector::getFunctionSelectorFromString("sendMessage(address,bytes,uint256)");

if (selector == SEL_LOCK_AND_BRIDGE || selector == SEL_SEND_MESSAGE) {
    ...
```
**Ý nghĩa:** 
Trong thế giới EVM, khi gọi một hàm, 4 byte đầu tiên của Input Data luôn là mã băm Keccak256 của tên hàm và kiểu dữ liệu tham số.
Thay vì chúng ta phải tự đi tính toán mã băm bằng tay (như `0x796dc9b9`), C++ Engine cung cấp hàm `getFunctionSelectorFromString` rất tiện. Nó tự tính hash string khi biên dịch.
-> Khối `if` này đảm bảo: *"Nếu mày là lệnh gửi tin nhắn hoặc bridge tiền qua chain khác, tao sẽ đứng ra xử lý nội bộ luôn!"*.

## 2. Burn Tiền Nguyên Tử (Quyền Năng Của Thượng Đế)

```cpp
if (value > 0) {
    if (acc.acc.get_balance() < value) {
        return false; // Insufficient balance
    }
    // BURN trực tiếp: Lấy số dư hiện tại trừ đi số tiền gửi đi
    acc.acc.set_balance(acc.acc.get_balance() - value);
    
    // Ghi chép vào sổ cái GlobalState để StateRoot đồng thuận mạng lưới
    gs.add_addresses_sub_balance_change(acc.acc.get_address(), value);
}
```
**Ý nghĩa:**
- Một Smart Contract bình thường bằng Solidity KHÔNG THỂ tự ý làm bốc hơi/hủy (burn) đồng native coin của hệ thống (vì EVM không hỗ trợ hàm `burn()`). Nó chỉ có thể gửi đi chỗ khác (ví dụ địa chỉ `0x00..Dead`).
- Tuy nhiên, vì đoạn code này chạy ở tầng lõi C++ của máy ảo (MVM layer / Precompile), nó có "Quyền thượng đế". Nó can thiệp trực tiếp vào RAM chứa `Context` tài khoản (biến `acc`) và sửa thẳng con số `balance` nhỏ đi nguyên đúng bằng số tiền `value`. Lượng tiền này đi vào Hư vô. Trạng thái State Root của block vẫn hoàn toàn ăn khớp và bảo mật tuyệt đối.

## 3. Phẫu Thuật Gỡ Dữ Liệu ABIData (Input Unpacking)

Vì Solidity encode hàm theo chuẩn ABI (mỗi tham số 32 byte), đoạn code dưới đây dịch ngược bộ nhớ `input` để móc tấc cả tham số ra.

```cpp
uint256_t destId = 0;
uint256_t targetAddr = 0;
std::vector<uint8_t> payload;

// Nếu là lockAndBridge(address recipient, uint256 destinationId)
if (selector == SEL_LOCK_AND_BRIDGE && input.size() >= 68) {
    destId = from_big_endian(input.data() + 36, 32);     // Prama thứ 2 (bỏ 4 byte đầu + 32 byte đầu)
    payload.assign(input.data() + 4, input.data() + 36); // Copy 32 byte đầu tiên để gửi sang
} 
// Nếu là sendMessage(address target, bytes calldata payload, uint256 destinationId)
else if (selector == SEL_SEND_MESSAGE && input.size() >= 100) {
    targetAddr = from_big_endian(input.data() + 4, 32);  // Lấy Target
    destId = from_big_endian(input.data() + 68, 32);     // Lấy destinationId
    
    // Dữ liệu "bytes" có dính độ dài (length offset), phải tính toán offset
    uint32_t offset = static_cast<uint32_t>(from_big_endian(input.data() + 36, 32));
    uint32_t length = static_cast<uint32_t>(from_big_endian(input.data() + 4 + offset, 32));
    // Copy Payload gốc của Contract
    payload.assign(input.data() + 4 + offset + 32, input.data() + 4 + offset + 32 + length);
}
```

## 4. Xây Dựng ABI Encode Cho Event `MessageSent` Trong Lõi C++

Lớp Go Observer lắng nghe `MessageSent`. Thay vì sinh Event bằng Solidity (tốn logic), C++ tự tóm Data và giả mạo một EVM Event Logs đúng cấu trúc hệt như Solidity. Cấu trúc Event ABI gồm: `sourceNationId`, `destNationId`, `isEVM`, `sender`, `target`, `value`, `payload(bytes)`, `timestamp`.

```cpp
std::vector<uint8_t> event_data;
event_data.resize(6 * 32, 0); // Đặt sẵn RAM trống cho 6 tham số kiểu cơ bản
event_data[31] = 1;           // isEVM = true (bool)

uint8_t buf[32] = {0};

// Đổ sender address vào 32 bytes
to_big_endian(acc.acc.get_address(), buf);
memcpy(event_data.data() + 32, buf, 32);

// Đổ target address vào
to_big_endian(targetAddr, buf);
memcpy(event_data.data() + 64, buf, 32);

// Đổ Value tiền
to_big_endian(value, buf);
memcpy(event_data.data() + 96, buf, 32);

// Khai báo Offset của mảng động Payload (Bytes)
uint256_t offset_payload = 6 * 32; 
to_big_endian(offset_payload, buf);
memcpy(event_data.data() + 128, buf, 32);

// Đổ Timestamp vào Event
to_big_endian(timestamp, buf);
memcpy(event_data.data() + 160, buf, 32);

// -- Đuôi mảng động Bytes Payload --
uint256_t payload_len = payload.size();
to_big_endian(payload_len, buf);
event_data.insert(event_data.end(), buf, buf + 32); // Thêm độ dài 
event_data.insert(event_data.end(), payload.begin(), payload.end()); // Thêm Data gốc

// Đệm thêm số (0) cho chẵn 32 byte để ABI Decode không lỗi
size_t remainder = payload.size() % 32;
if (remainder != 0) {
    event_data.resize(event_data.size() + (32 - remainder), 0);
}
```

## 5. Bắn Trực Tiếp Vào Log Handler Và Thoát

```cpp
LogEntry log;
log.address = addr; // Bắn ra từ chính địa chỉ của Gateway 0xB429...

// Signature Topic0: keccak256(MessageSent(...))
uint8_t t0_buf[32] = {0xb0, 0xaa, 0x71, ... 0x8b, 0xc1};
log.topics.push_back(from_big_endian(t0_buf, 32));

// Lấy Source ID thật của Chain
std::vector<uint8_t> source_bin = gs.get_cross_chain_source_id();
uint256_t srcId = from_big_endian(source_bin.data(), 32);

// Index 1, Index 2 cho Event: source id và destination id (vì được Indexed)
log.topics.push_back(srcId);
log.topics.push_back(destId);

// Gán thân dữ liệu đã encode bên trên
log.data = event_data;

// Nạp thẳng vào máy ảo đang chạy
log_handler.handle(std::move(log));

output.clear();
return true; // OK! Solidity sẽ nhảy tiếp hàm sau lệnh Call.
```

**Tổng Kết Của Cú Xóa Này:**
Code C++ này đã thay thế hoàn toàn nhu cầu viết 1 File Solidity dài thòng, tối ưu hóa đến tận kim tiêm và đảm bảo được "tính Nguyên Tử" không thể chối cãi khi Burn Native Asset ngang qua Engine Máy!
