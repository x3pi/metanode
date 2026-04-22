Blockchain hỗ trợ môi trường "Self-Debugging" cho mọi loại Smart Contract.

1. Artifact Registry
(Kho tri thức xác thực cho Smart Contract Debug & Replay)
1.1. Mục tiêu
Artifact Registry là nền tảng cốt lõi của toàn bộ hệ thống Self-Debugging.
Nó đóng vai trò như một “Compiler Memory” của Blockchain, cho phép hệ thống:
Giải mã lỗi REVERT chính xác (Error / Custom Error / Panic)
Ánh xạ Bytecode → Source Code (Source Mapping)
Phân tích Call Tree (External + Internal)
Decode Storage Slot → Tên biến thực tế
Replay giao dịch trong môi trường Local với độ chính xác tuyệt đối
⚠️ Nguyên tắc thiết kế quan trọng
Mọi thông tin debug chỉ được tin cậy nếu nó được xác thực là khớp 100% với Bytecode đang chạy on-chain.

1.2. Thời điểm và cơ chế đồng bộ Artifact
Thời điểm
Artifact bắt buộc phải được đăng ký ngay sau khi Smart Contract được deploy.
Cơ chế
CI/CD Pipeline hoặc Backend Smart Contract Service chịu trách nhiệm push Artifact
Registry không tin tưởng dữ liệu đầu vào và sẽ tự xác thực lại
Luồng chuẩn:
Compile → Deploy → Push Artifact → Verify → Activate

Artifact chỉ được đưa vào trạng thái ACTIVE sau khi verify thành công.

1.3. Danh sách Artifact bắt buộc (Artifact Set)
1. ABI
Dùng để:
Decode function call
Decode Custom Error
Decode Event
Yêu cầu:
ABI phải đúng compiler output
Không cho phép ABI thủ công

2. Source Code
Toàn bộ file .sol gốc
Giữ nguyên:
Thứ tự file
Comment
Line ending
Dùng cho:
Hiển thị lỗi
Source Mapping
Internal Call Boundary

3. Source Map
Mapping giữa:
Program Counter (PC) ↔ Bytecode ↔ Source Code Offset


Bắt buộc để:
Xác định dòng code gây lỗi
Sinh Stack Trace kiểu Web2

4. Storage Layout (Bắt buộc – Không tùy chọn)
File metadata mô tả:
Slot index
Offset
Kiểu dữ liệu
Tên biến
Mapping / Struct layout
Ví dụ:
slot 5 → mapping(address => uint256) balances

👉 Thành phần này là điều kiện bắt buộc để:
Decode Storage Dump
Hiển thị Storage Delta (Before / After)
Debug lỗi logic phức tạp (DeFi, AMM, Oracle)

5. Compiler Metadata
Để đảm bảo determinism, Registry bắt buộc lưu:
solc version (exact)
optimizer enabled / runs
evmVersion
library linking address
viaIR flag

1.4. Định danh Artifact (Artifact Identity)
Vấn đề
Cùng source code nhưng:
compiler khác
optimizer khác
library khác
→ sinh Bytecode khác
👉 Không được index Artifact chỉ theo Contract Address

Định nghĩa Artifact ID
artifact_id = keccak256(
  bytecode_hash +
  solc_version +
  optimizer_settings +
  evm_version +
  linked_libraries
)

Cơ chế index
Registry phải hỗ trợ tra cứu theo:
ContractAddress → artifact_id
BytecodeHash → artifact_id
artifact_id → full artifact set
➡️ Cho phép:
Debug proxy
Debug clone
Debug CREATE2

1.5. Xác thực Artifact (Verification – Bắt buộc)
Registry không tin tưởng artifact được push lên.
Quy trình xác thực
Registry compile lại source code bằng metadata đã cung cấp
Sinh Bytecode
So sánh với Bytecode on-chain
Kết quả
✅ Match 100% → Artifact được activate
❌ Mismatch → Artifact bị reject
⚠️ Không cho phép:
Debug bằng artifact chưa verify
Source map không khớp bytecode

1.6. Phạm vi áp dụng Artifact
Artifact có thể được gắn cho:
Contract triển khai trực tiếp
Proxy (logic contract)
Library
Precompiled extension (nếu có)
Registry phải cho phép 1 transaction sử dụng nhiều artifact khác nhau trong cùng Call Tree.

1.7. Vai trò của Artifact Registry trong hệ sinh thái
Thành phần
Registry cung cấp
RPC Decoder
ABI, Error signature
Call Tracer
Function name, Source boundary
Source Mapper
PC → Line / Column
Dump State
Storage Layout
Local Replay
Deterministic execution context

👉 Nếu thiếu Registry → toàn bộ hệ thống debug sụp đổ

1.8. Nguyên tắc thiết kế cốt lõi
Artifact là read-only sau khi verify
Không có Artifact → chỉ trả Hex thô
Debug chỉ tốt khi Artifact đúng
Registry là single source of truth

2. Universal Error Decoder
(Tầng RPC giải mã lỗi thống nhất cho toàn bộ Smart Contract)
2.1. Mục tiêu
Universal Error Decoder là một tầng Middleware nằm trước hoặc bên trong RPC, có nhiệm vụ:
Chuyển đổi dữ liệu REVERT thô (Hex) thành lỗi có ý nghĩa
Chuẩn hoá lỗi cho mọi Smart Contract, không cần sửa code Frontend
Cung cấp thông tin lỗi đủ chi tiết để:
Hiển thị cho người dùng
Phân tích nguyên nhân ở Backend
Ánh xạ tới Source Code (kết hợp Mục 4)
⚠️ Nguyên tắc thiết kế
RPC không trả về Hex thô nếu có đủ Artifact để giải mã.

2.2. Phạm vi lỗi được hỗ trợ
Universal Error Decoder bắt buộc hỗ trợ đầy đủ 4 nhóm lỗi sau:

2.2.1. Standard Error – Error(string)
Nguồn gốc:
require(condition, "message")
revert("message")
Định dạng ABI:
0x08c379a0 + abi.encode(string)

Xử lý:
Decode string UTF-8
Giữ nguyên nội dung message

2.2.2. Custom Error (Solidity ≥ 0.8.4)
Nguồn gốc:
error InsufficientBalance(uint256 available, uint256 required);
revert InsufficientBalance(a, b);

Cơ chế giải mã:
Lấy 4-byte selector đầu tiên
Tra ABI từ Artifact Registry
Match selector → error definition
Decode parameters theo ABI
⚠️ Yêu cầu:
Artifact phải verify
Không fallback đoán mò

2.2.3. Panic Error – Panic(uint256) (BẮT BUỘC)
Nguồn gốc:
Arithmetic overflow / underflow
Division by zero
Assert failure
Array out-of-bounds
Định dạng:
0x4e487b71 + uint256(code)

Bảng mapping chuẩn
Code
Ý nghĩa
0x01
assert(false)
0x11
Arithmetic overflow / underflow
0x12
Division by zero
0x21
Invalid enum value
0x22
Storage byte array incorrectly encoded
0x31
Empty array pop
0x32
Array index out of bounds

RPC phải trả về message dễ hiểu, không phải chỉ code.

2.2.4. Low-level / Unknown Revert
Áp dụng khi:
Không có Artifact
Selector không match ABI
Revert từ precompile / assembly
Xử lý:
Giữ nguyên revert data (hex)
Gắn cờ decoded = false

2.3. Quy trình giải mã lỗi (Decoding Pipeline)
Luồng xử lý chuẩn
EVM REVERT
   ↓
Extract revert data
   ↓
Detect error type (selector)
   ↓
Fetch Artifact (ABI)
   ↓
Decode parameters
   ↓
Normalize JSON output

Thứ tự ưu tiên
Panic(uint256)
Custom Error
Error(string)
Unknown
⚠️ Không được decode sai thứ tự.

2.4. Chuẩn hoá Output JSON
RPC không trả lỗi dạng string, mà trả JSON có cấu trúc thống nhất.
Schema chuẩn
{
  "decoded": true,
  "error_type": "custom",
  "error_name": "InsufficientBalance",
  "error_signature": "InsufficientBalance(uint256,uint256)",
  "arguments": {
    "available": "100",
    "required": "150"
  },
  "message": "InsufficientBalance(available=100, required=150)",
  "contract": {
    "address": "0x...",
    "name": "Pool"
  }
}


Panic Error Example
{
  "decoded": true,
  "error_type": "panic",
  "panic_code": "0x11",
  "message": "Arithmetic overflow or underflow",
  "contract": {
    "address": "0x...",
    "name": "Token"
  }
}


Unknown Error Example
{
  "decoded": false,
  "error_type": "unknown",
  "raw_revert_data": "0x1234abcd..."
}


2.5. Hỗ trợ đa hợp đồng (Call Context Awareness)
Trong giao dịch phức tạp:
Revert có thể xảy ra ở hợp đồng con
RPC bắt buộc trả về:
Address hợp đồng gây lỗi
Function context (nếu decode được)
Call depth
Ví dụ:
{
  "call_depth": 3,
  "contract_address": "0xPool",
  "function": "swap",
  "error": { ... }
}

➡️ Frontend có thể hiển thị đúng nơi lỗi xảy ra, không phải Router.

2.6. Tích hợp với Source Mapping & Call Trace
Universal Error Decoder không hoạt động độc lập.
Nó phải:
Gắn pc (Program Counter) tại thời điểm revert
Truyền thông tin này cho:
Mục 3: Call Tree
Mục 4: Source Mapping
{
  "pc": 742,
  "source_map_available": true
}


2.7. Chính sách fallback & an toàn
Không có Artifact → KHÔNG decode Custom Error
ABI không match → trả Unknown
Không đoán, không suy luận
⚠️ Debug sai còn nguy hiểm hơn không debug.

2.8. Vị trí triển khai trong Node
Nếu dùng Geth hoặc EVM tương đương:
Hook kỹ thuật
core/vm/interpreter.go
Bắt REVERT opcode
Lưu revert data + pc
internal/ethapi/api.go
Decode ABI
Chuẩn hoá JSON RPC response

2.9. Nguyên tắc thiết kế cốt lõi
Decode đúng tuyệt đối, không decode nếu không chắc
Artifact là điều kiện tiên quyết
Output phải có cấu trúc
Frontend không cần hiểu ABI

3. Hierarchical Call Tree & Trace
(Truy vết luồng thực thi đa hợp đồng – External & Internal)
3.1. Mục tiêu
Hierarchical Call Tree cho phép hệ thống:
Hiển thị toàn bộ luồng gọi hàm của một giao dịch
Xác định chính xác hợp đồng / hàm / chặng gây ra lỗi
Cung cấp ngữ cảnh thực thi cho:
Universal Error Decoder (Mục 2)
Source Mapping & Stack Trace (Mục 4)
Local Replay (Mục 5)
⚠️ Nguyên tắc thiết kế
Một lỗi chỉ có ý nghĩa khi biết nó xảy ra trong ngữ cảnh nào.

3.2. Phạm vi Call cần được truy vết
Hệ thống bắt buộc phải bao phủ cả hai loại:
3.2.1. External Call (Opcode-level)
Phát sinh từ các opcode:
CALL
DELEGATECALL
STATICCALL
CALLCODE (legacy)
Đây là ranh giới giữa các hợp đồng.

3.2.2. Internal Call (Source-level – BẮT BUỘC)
Phát sinh từ:
Function gọi function
Modifier
Inherited function
⚠️ Lưu ý
Internal call không có opcode CALL → nếu không synthesize thì:
Call Tree sẽ “phẳng”
Debug logic phức tạp là bất khả thi

3.3. Mô hình Node trong Call Tree
Mỗi Call Tree được biểu diễn dưới dạng cây phân cấp JSON.
3.3.1. Node chuẩn
{
  "node_id": "3.2.1",
  "call_type": "external | internal",
  "opcode": "CALL | DELEGATECALL | JUMP",
  "contract": {
    "address": "0x...",
    "name": "Pool"
  },
  "function": {
    "name": "swap",
    "signature": "swap(uint256,uint256)"
  },
  "call_depth": 3,
  "status": "success | revert",
  "gas": {
    "provided": 120000,
    "used": 53210
  },
  "children": []
}


3.4. Phân biệt các loại External Call (RẤT QUAN TRỌNG)
3.4.1. CALL
Code & Storage: callee
Context: tách biệt

3.4.2. DELEGATECALL
Code: callee
Storage & msg.sender: caller
👉 Node bắt buộc ghi rõ:
{
  "execution_context": {
    "code_address": "0xLogic",
    "storage_address": "0xProxy"
  }
}

Nếu không → debug proxy sai hoàn toàn.

3.4.3. STATICCALL
Không cho phép ghi storage
Quan trọng để debug Oracle / View logic

3.5. Synthesize Internal Call Node (Source-level)
3.5.1. Nguyên lý
Dựa trên:
Source Map
Function boundaries
PC jump pattern
RPC phải tự tạo Virtual Node khi:
PC đi vào vùng source của function khác
Nhưng không có CALL opcode

3.5.2. Ví dụ
function swap() {
  _updatePrice();
}

function _updatePrice() internal {
  require(x > 0);
}

Call Tree:
swap()
 └── _updatePrice()   ← internal node
     └── REVERT


3.5.3. Internal Node Schema
{
  "call_type": "internal",
  "function": {
    "name": "_updatePrice",
    "visibility": "internal"
  },
  "source_range": "Pool.sol:120-180"
}


3.6. Gắn lỗi vào Call Node
Khi xảy ra REVERT:
Lỗi phải gắn vào node sâu nhất
Node cha không được overwrite lỗi
Ví dụ:
Router.swap()        → success
  Factory.getPool() → success
    Pool.swap()     → revert ❌

Frontend hiển thị:
Pool.swap bị lỗi
Router chỉ là caller

3.7. Tích hợp với Universal Error Decoder
Mỗi node có thể mang error object:
{
  "error": {
    "decoded": true,
    "error_type": "custom",
    "error_name": "InsufficientBalance"
  }
}

➡️ Error Decoder không tồn tại độc lập, mà là thuộc tính của Call Node.

3.8. Gas Profiling theo Call Node
Mỗi node bắt buộc có:
Gas provided
Gas used
Gas refunded (nếu có)
➡️ Cho phép:
Phân tích tắc nghẽn gas
Tối ưu hoá DeFi path

3.9. Thứ tự thực thi & thời gian
RPC nên gắn thêm:
execution_index
start_pc / end_pc
Cho phép:
Tái hiện timeline thực thi
Đồng bộ với Source Mapping

3.10. Output chuẩn cho Frontend
RPC phải trả về 1 object duy nhất:
{
  "tx_hash": "0x...",
  "root_call": { ...call tree... }
}

Frontend:
Render dạng cây
Click từng node để xem:
Function
Gas
Error
Source Code

3.11. Hiệu năng & chế độ hoạt động
Chế độ
Mode
Mô tả
light
External call only
full
External + Internal
debug
Full + PC + source

➡️ Mainnet mặc định: light
➡️ Debug tool / Replay: full hoặc debug

3.12. Vị trí hook trong EVM / Node
Geth (ví dụ)
core/vm/interpreter.go
Hook opcode CALL / DELEGATECALL
Track PC, gas, depth
core/vm/evm.go
Track execution context
internal/ethapi/api.go
Assemble Call Tree JSON

3.13. Nguyên tắc thiết kế cốt lõi
Call Tree phải đúng về ngữ nghĩa, không chỉ opcode
Internal call là bắt buộc
Delegatecall phải phân tách code vs storage
Error gắn vào node gây lỗi
Call Tree là xương sống của debug

4. Source Mapping & Stack Trace
(Ánh xạ lỗi EVM → dòng mã Solidity & dựng Stack Trace)
4.1. Mục tiêu
Source Mapping & Stack Trace cho phép hệ thống:
Xác định chính xác dòng code Solidity gây lỗi
Dựng Stack Trace nhiều tầng xuyên qua:
External call
Internal function
Delegatecall / Proxy
Mang lại trải nghiệm debug tương đương:
JavaScript stack trace
Java exception trace
⚠️ Nguyên tắc thiết kế
Một lỗi chỉ thực sự “được hiểu” khi Dev nhìn thấy dòng code gây ra nó.

4.2. Dữ liệu đầu vào bắt buộc
Source Mapping chỉ hoạt động khi đầy đủ Artifact.
Nguồn dữ liệu
Nguồn
Cung cấp
Artifact Registry
Source Code, Source Map
Call Tree
Call depth, execution context
EVM
Program Counter (PC)
Error Decoder
Revert / Panic metadata

⚠️ Thiếu bất kỳ thành phần nào → fallback về raw PC.

4.3. Source Map – vai trò & cấu trúc
4.3.1. Khái niệm
Source Map ánh xạ:
PC → Instruction → (file, start_offset, length)

Cho phép:
Truy ngược bytecode → vị trí source

4.3.2. Đơn vị ánh xạ
Hệ thống không map theo opcode, mà theo:
Instruction index
PC tại thời điểm runtime
Lý do:
Compiler tối ưu hoá
Inlining
via-IR

4.4. Thuật toán ánh xạ PC → Source
Quy trình chuẩn
Nhận PC tại thời điểm:
REVERT
INVALID
Panic
Tra Source Map từ Artifact Registry
Lấy:
File index
Character offset
Length
Convert offset → (line, column)

Output chuẩn
{
  "file": "Pool.sol",
  "line": 142,
  "column": 17,
  "source_snippet": "require(x > 0, \"INVALID_PRICE\");"
}


4.5. Xử lý nhiều loại lỗi
4.5.1. REVERT có message / custom error
Ánh xạ PC của opcode REVERT
Stack trace kết thúc tại dòng gây revert

4.5.2. Panic / INVALID / OOG
Panic: map tại opcode gây panic
INVALID: map opcode INVALID
Out-of-Gas:
Map instruction cuối cùng thành công
Gắn cờ approximate = true

4.6. Dựng Stack Frame
4.6.1. Stack Frame chuẩn
Mỗi frame đại diện cho một call context.
{
  "depth": 3,
  "contract": {
    "address": "0x...",
    "name": "Pool"
  },
  "function": "swap",
  "call_type": "external | internal | delegatecall",
  "source": {
    "file": "Pool.sol",
    "line": 142,
    "column": 17
  }
}


4.6.2. Thứ tự Stack
Frame 0: nơi lỗi xảy ra
Frame N: entry point (tx sender)
Giống Java / JS:
at Pool.swap (Pool.sol:142)
at Router.execute (Router.sol:88)
at TxSender


4.7. Internal Function & Modifier Mapping
Vấn đề
Modifier
Inlined function
Multiple require trong 1 function
Giải pháp
Dùng Source Map segment-level
Nếu nhiều segment cùng line:
Lấy segment gần PC nhất
Hiển thị:
Function name
Modifier (nếu có)

4.8. Delegatecall & Proxy Stack Trace
Quy tắc
Code location → logic contract
Storage context → proxy contract
Stack Frame ví dụ
{
  "contract": {
    "code": "Logic.sol",
    "storage": "Proxy.sol"
  },
  "function": "upgradeTo",
  "source": "Logic.sol:55"
}

➡️ Tránh hiểu sai lỗi proxy.

4.9. Tích hợp với Call Tree
Stack Trace được dựng từ Call Tree, không dựng độc lập.
Quy trình:
Call Tree Node (deepest)
   ↓
Collect PC per node
   ↓
Map source per node
   ↓
Assemble stack frames

➡️ Đảm bảo:
Stack trace phản ánh đúng luồng gọi
Không lẫn context

4.10. Output chuẩn cho Frontend
RPC trả về:
{
  "error": { ...decoded error... },
  "stack_trace": [
    {
      "function": "swap",
      "location": "Pool.sol:142"
    },
    {
      "function": "execute",
      "location": "Router.sol:88"
    }
  ]
}

Frontend:
Click từng frame
Jump tới source code
Highlight dòng lỗi

4.11. Hiệu năng & chế độ hoạt động
Mode
Source Mapping
light
Tắt
full
PC → line
debug
Full + snippet

➡️ Mainnet mặc định: full khi có lỗi
➡️ Replay / Local: debug

4.12. Vị trí hook trong Node
Geth (ví dụ)
core/vm/interpreter.go
Track PC per instruction
core/vm/jump_table.go
Hook INVALID / REVERT
internal/ethapi/api.go
Map PC → Source

4.13. Nguyên tắc thiết kế cốt lõi
Source Map là nguồn sự thật duy nhất
Stack Trace phản ánh đúng Call Tree
Không đoán khi thiếu dữ liệu
Sai mapping nguy hiểm hơn không mapping


5. Dump State & Local Replay
(Tái hiện giao dịch on-chain trong môi trường debug cục bộ)
5.1. Mục tiêu
Dump State & Local Replay cho phép Dev:
Tái hiện chính xác 100% một giao dịch lỗi trên Mainnet
Debug không tốn gas, không ảnh hưởng mạng thật
Quan sát:
Call Tree
Stack Trace
Storage biến động
Console log nội bộ
Thử nghiệm bản fix bằng hot-swap bytecode
⚠️ Nguyên tắc cốt lõi
Replay phải quyết định (deterministic). Kết quả replay phải giống Mainnet, hoặc fail có kiểm soát.

5.2. API Dump State (debug_dumpState)
5.2.1. Mục đích
Thu thập toàn bộ bối cảnh tối thiểu cần thiết để replay giao dịch lỗi.

5.2.2. Đầu vào
{
  "tx_hash": "0x...",
  "block_number": 12345678
}


5.2.3. Payload Dump (BẮT BUỘC)
A. Block Environment (Determinism Guard)
{
  "block_env": {
    "number": 12345678,
    "timestamp": 1700000000,
    "basefee": "0x...",
    "coinbase": "0x...",
    "gaslimit": "0x...",
    "chainid": 1
  }
}

➡️ Toàn bộ opcode:
TIMESTAMP, NUMBER, BASEFEE, COINBASE, GASLIMIT
phải trả về giá trị này khi replay

B. Transaction Context
{
  "tx": {
    "from": "0x...",
    "to": "0x...",
    "value": "0x...",
    "data": "0x...",
    "gas": 500000,
    "gas_price": "0x..."
  }
}


C. Account State (Theo Demand)
Với mỗi account tham gia call tree:
{
  "address": "0x...",
  "nonce": 12,
  "balance": "0x...",
  "code": "0x...",
  "storage": {
    "0x00": "0x...",
    "0x01": "0x..."
  }
}

⚠️ Storage dump:
Ưu tiên lazy
Nhưng account gây lỗi phải đầy đủ

5.3. Node Local – Fork & Replay Mode
5.3.1. Fork Mode
Node local chạy ở chế độ:
node --fork --state=state_dump.json

Đặc điểm:
Không sync chain
Không mining
Không gossip

5.3.2. State Resolution Strategy
Trường hợp
Hành vi
Account có trong dump
Đọc local
Chưa có
eth_getProof từ RPC
Không cho phép
Fail (strict mode)


5.4. Deterministic Execution Layer (BẮT BUỘC)
Node replay phải override các nguồn không quyết định:
Opcode
Giá trị
TIMESTAMP
dump.block_env.timestamp
NUMBER
dump.block_env.number
BASEFEE
dump.block_env.basefee
COINBASE
dump.block_env.coinbase
GASPRICE
dump.tx.gas_price

➡️ Nếu không override → replay vô nghĩa

5.5. Replay Execution
Chạy replay
my-cli replay --trace --debug

Node:
Thực thi lại tx
Sinh:
Call Tree
Error Decode
Stack Trace
Storage Delta

5.6. Storage Delta Tracking (RẤT QUAN TRỌNG)
5.6.1. Mục tiêu
Hiển thị:
Biến nào đã thay đổi → ở call nào → trước & sau

5.6.2. Cơ chế
Tại mỗi Call Node:
Snapshot storage trước khi vào
Snapshot sau khi thoát

5.6.3. Decode Storage
Kết hợp với:
Storage Layout (Mục 1)
Ví dụ:
{
  "variable": "price",
  "slot": "0x05",
  "before": "100",
  "after": "80"
}


5.7. Console Log & Breakpoint Ảo
5.7.1. Console Log (0x6c)
Chỉ bật ở replay / debug
Không tiêu gas
Ghi log string / value
DEBUG.log("price", price);


5.7.2. Virtual Breakpoint (0x6d)
Cho phép:
Dừng tại line cụ thể
Dump memory / stack
Chỉ tồn tại ở replay mode.

5.8. Hot-Swap Bytecode (Fix Validation)
Mục tiêu
Thử fix mà không redeploy
Không cần private key

Cơ chế
my-cli replay --replace-code Pool.sol

Ghi đè bytecode tại address
Storage giữ nguyên

5.9. Impersonation (Sender Override)
Replay cho phép:
--impersonate 0xUser

Bỏ qua chữ ký
Cho phép test edge case

5.10. Replay Result Output
{
  "status": "success",
  "error": null,
  "call_tree": { ... },
  "stack_trace": [ ... ],
  "storage_delta": [ ... ]
}

➡️ Nếu replay vẫn revert → fix chưa đúng.

5.11. Quy trình làm việc cho Dev
Frontend báo tx lỗi
dump tx_hash
replay --trace
Xem lỗi & storage delta
Sửa code
replay --replace-code
Thành công → deploy fix

5.12. Nguyên tắc thiết kế cốt lõi
Replay phải deterministic
Không ảnh hưởng Mainnet
Storage delta là chìa khoá debug logic
Console & breakpoint chỉ tồn tại ở debug
Replay phải tin cậy hơn test giả lập


6. SOP & Phối hợp 3 bên
(Quy trình vận hành hệ thống Self-Debugging cho Smart Contract)
6.1. Mục tiêu
Mục 6 định nghĩa:
Ranh giới trách nhiệm rõ ràng giữa 3 bên
Quy trình chuẩn từ lúc phát hiện lỗi → phân tích → fix → deploy
Cách vận hành hệ thống debug an toàn, không ảnh hưởng Mainnet
⚠️ Nguyên tắc cốt lõi
Debug là năng lực của hệ thống, không phải nỗ lực cá nhân.

6.2. Các vai trò chính
6.2.1. Team Blockchain (Protocol / Node)
Chịu trách nhiệm cho hạ tầng debug
Vận hành Artifact Registry
Nâng cấp RPC:
Universal Error Decoder
Call Tree & Trace
Source Mapping
Cung cấp API:
debug_dumpState
debug_replay (local)
Đảm bảo:
Deterministic execution
Hiệu năng & an toàn Mainnet
❌ Không viết logic Smart Contract
❌ Không tham gia sửa bug nghiệp vụ

6.2.2. Backend Smart Contract Team
Chủ sở hữu logic nghiệp vụ
Viết & deploy Smart Contract
Đẩy Artifact vào Registry sau deploy
Đảm bảo Artifact verify thành công
Sử dụng:
Custom Error
Console debug (0x6c) khi cần
Phân tích lỗi & sửa code
❌ Không can thiệp Node / RPC
❌ Không debug trực tiếp trên Mainnet

6.2.3. Frontend / Product / QA
Người phát hiện & kích hoạt debug
Nhận lỗi từ người dùng
Gửi tx_hash vào hệ thống debug
Hiển thị:
Error đã decode
Stack Trace
Call Tree
Không cần hiểu ABI / Solidity
❌ Không decode lỗi thủ công
❌ Không phân tích Bytecode

6.3. Trạng thái Debug của hệ thống
Hệ thống debug có 3 trạng thái:
Mode
Mô tả
Production
Debug tối thiểu, không console
Debug
Decode lỗi + source
Replay
Full debug + can thiệp

➡️ Mainnet mặc định: Production
➡️ Chỉ bật Debug / Replay theo yêu cầu

6.4. Quy trình chuẩn khi xảy ra lỗi (End-to-End SOP)
Bước 1 – Phát hiện lỗi (Frontend)
Giao dịch thất bại
RPC trả về:
Error đã decode
Contract + function
Frontend gắn:
Tx hash
User action

Bước 2 – Kích hoạt Debug (Backend SC)
my-cli debug 0xTxHash

Kết quả:
Call Tree
Stack Trace
Source location

Bước 3 – Dump State (Team Blockchain tool)
my-cli dump 0xTxHash

Sinh state_dump.json
Đảm bảo deterministic context

Bước 4 – Local Replay (Backend SC)
my-cli replay --trace --debug

Tái hiện lỗi
Xem:
Storage delta
Console log
Breakpoint

Bước 5 – Sửa lỗi & kiểm chứng
my-cli replay --replace-code Contract.sol

Replay pass
Không còn revert

Bước 6 – Deploy & Verify
Deploy bản fix
Push Artifact mới
Registry verify → ACTIVE

Bước 7 – Đóng vòng lặp
Frontend xác nhận hành vi đúng
Đánh dấu issue resolved

6.5. Artifact Lifecycle & Trách nhiệm
Giai đoạn
Trách nhiệm
Compile
Backend SC
Deploy
Backend SC
Push Artifact
Backend SC
Verify Artifact
Registry
Activate
Registry
Use for Debug
RPC

⚠️ Artifact không verify → không debug nâng cao.

6.6. Chính sách an toàn & kiểm soát rủi ro
6.6.1. Mainnet Safety
Console (0x6c) OFF
Breakpoint (0x6d) OFF
Dump State cần quyền

6.6.2. Access Control
debug_dumpState:
Chỉ nội bộ / whitelist
replay:
Chỉ local / CI

6.6.3. Audit Trail
Mọi hành động debug được log:
Ai dump
Khi nào
Tx nào

6.7. SLA & trách nhiệm phản hồi
Loại sự cố
Thời gian phản hồi
Revert logic
< 1h
Panic / OOG
< 30 phút
Node / RPC lỗi
Ngay lập tức


6.8. Nguyên tắc phối hợp cốt lõi
Frontend không đoán lỗi
Backend không debug mù
Blockchain Team không chạm logic
Artifact là hợp đồng chung
Replay là nguồn sự thật

6.9. Kết nối các Mục 1–6
Mục
Vai trò
1
Nguồn tri thức
2
Giải mã lỗi
3
Ngữ cảnh
4
Stack Trace
5
Phòng thí nghiệm
6
Vận hành

➡️ Thiếu Mục 6 → hệ thống mạnh nhưng không dùng được

6.10. Kết luận
Mục 6 biến hệ thống debug từ:
“có kỹ thuật”
thành
“có thể vận hành, mở rộng và chịu trách nhiệm”
7. Security & Abuse Model
(Mô hình an toàn & phòng chống lạm dụng cho hệ thống Self-Debugging)
7.1. Mục tiêu
Mục này định nghĩa threat model chính thức cho toàn bộ hệ thống debug, nhằm:
Ngăn lộ thông tin nhạy cảm (logic, state, chiến lược DeFi)
Ngăn lạm dụng hạ tầng debug để reverse engineering
Đảm bảo tuân thủ yêu cầu security / legal / compliance
Cho phép vận hành debug trên Mainnet một cách có kiểm soát
⚠️ Nguyên tắc cốt lõi
Debug mạnh hơn production → bảo mật phải cao hơn production.

7.2. Phân loại Threats (Threat Taxonomy)
7.2.1. Threat A – State Exfiltration (Rất nghiêm trọng)
Mô tả
API debug_dumpState cho phép truy xuất:
Storage đầy đủ
Balance
Code
Với DeFi, đây có thể là:
Chiến lược định giá
Oracle buffer
MEV defense logic
State chưa public
Hệ quả
Lộ chiến lược kinh doanh
Front-running / sandwich
Vi phạm NDA / compliance

7.2.2. Threat B – Replay-based Reverse Engineering
Mô tả
Replay + Hot-swap bytecode cho phép:
Thử nghiệm nhiều biến thể
Suy ra invariant nội bộ
Phân tích edge-case chưa public
Hệ quả
Đối thủ clone logic
Phá hoại advantage cạnh tranh
Legal risk (IP leakage)

7.2.3. Threat C – Artifact Poisoning
Mô tả
Attacker push artifact:
ABI giả
Source map sai
Nếu Registry tin → debug hiển thị sai
Hệ quả
Dev debug sai
Fix sai bug
Có thể che giấu backdoor
⚠️ Đây là threat logic, nguy hiểm hơn bug kỹ thuật.

7.2.4. Threat D – Infrastructure Abuse / DoS
Mô tả
Dump storage lớn
Replay tx phức tạp liên tục
Gây quá tải node debug

7.3. Security Controls – Biện pháp bắt buộc

7.3.1. Artifact Signing & Trust Chain (BẮT BUỘC)
Nguyên tắc
Artifact không chỉ verify bằng bytecode, mà còn phải:
Xác định ai phát hành
Có thể audit trách nhiệm
Cơ chế đề xuất
Developer Key (ED25519 / secp256k1)
        ↓
Sign Artifact Manifest
        ↓
Registry verify:
  - Signature hợp lệ
  - Key được whitelist

Artifact Manifest (ví dụ)
{
  "artifact_id": "0xabc...",
  "contract": "Pool",
  "compiler": "solc 0.8.21",
  "signed_by": "0xDevKey",
  "signature": "0x..."
}

Quy tắc
Artifact không có chữ ký → reject
Mỗi chữ ký gắn trách nhiệm pháp lý
👉 Rất quan trọng để thuyết phục legal & audit

7.3.2. Dump State Access Control & Rate Limit
Chính sách truy cập
API
Quyền
debug_dumpState
internal + role-based
debug_replay
local only
debug_hot_swap
local only

Rate limit bắt buộc
debug_dumpState:
  - max N tx / hour / project
  - max storage size / dump

Storage Scope Rule
Default: lazy dump
Full dump chỉ cho contract gây lỗi
Các contract khác: on-demand

7.3.3. Disable Debug cho Private / Sensitive TX
Các loại TX bị cấm dump
Private mempool tx
MEV-protected tx
Tx flagged confidential=true
{
  "tx_hash": "...",
  "debug_allowed": false,
  "reason": "private_mempool"
}

👉 Tránh xung đột với:
Searcher
Builder
Compliance

7.3.4. Audit Log bắt buộc (Hash-Chained)
Mọi hành động debug phải log:
Ai
Khi nào
Dump tx nào
Replay bao nhiêu lần
Hash-chain log
log[n].hash = keccak256(log[n-1].hash + log[n].data)

Thuộc tính
Không sửa được
Có thể export cho audit
👉 Cực kỳ quan trọng khi có dispute / investigation

7.4. Nguyên tắc Security cốt lõi
Debug là privileged operation
Không có “debug miễn phí”
Artifact = hợp đồng trách nhiệm
Replay không bao giờ public

7. Performance & Cost Envelope
(Ràng buộc hiệu năng & chi phí hệ thống debug)
7.1. Mục tiêu
Đảm bảo debug không ảnh hưởng RPC chính
Cho phép infra team dự toán chi phí
Tránh “spec đẹp nhưng không deploy được”

7.2. Ràng buộc bắt buộc (Hard Limits)
Thành phần
Giới hạn
Artifact size
≤ 5 MB / contract
ABI lookup
O(1)
SourceMap lookup
O(1)
Call tree depth
≤ 64
Full debug tx
≤ 200 ms
Dump storage
Lazy + depth-limited

⚠️ Vượt ngưỡng → degrade mode

7.3. Degrade & Fallback Policy (BẮT BUỘC)
Các mức degrade
Trường hợp
Hành vi
Artifact quá lớn
Disable source snippet
Call tree quá sâu
External-only
Storage quá lớn
Disable storage delta
RPC quá tải
Trả raw revert

👉 Không bao giờ block RPC chính

7.4. Cache Strategy
Artifact cache theo artifact_id
SourceMap preload
ABI selector map in-memory
👉 Giảm latency decode xuống <10ms

7.5. Nguyên tắc hiệu năng
Debug là best-effort
RPC chính luôn ưu tiên
Không có debug nào được phép làm chậm block production

8. Developer Experience (DX) – Phụ lục BẮT BUỘC
8.1. Mục tiêu
Biến hệ thống debug từ:
“Chỉ người xây node mới dùng được”
thành:
“Dev bình thường cũng debug được”

8.2. CLI Workflow Chuẩn
my-cli debug <tx_hash>
   ↓
Universal Error Decoder
   ↓
Call Tree + Stack Trace
   ↓
VSCode mở đúng file + line


8.3. End-to-End Example (rút gọn)
my-cli debug 0xTx

Output:
❌ Pool.swap()
   ↳ require(x > 0)
   Pool.sol:142

my-cli dump 0xTx
my-cli replay --trace

my-cli replay --replace-code Pool.sol

✅ Replay success

8.4. Sample JSON Response (thực tế, không abstract)
{
  "error": {
    "error_type": "panic",
    "message": "Arithmetic overflow"
  },
  "stack_trace": [
    "Pool.swap (Pool.sol:142)",
    "Router.execute (Router.sol:88)"
  ]
}


8.5. Mapping với Tooling hiện có
Tool
Tích hợp
Foundry
map stack trace
Hardhat
import artifact
VSCode
jump-to-line
CI
auto replay


8.6. Nguyên tắc DX
Dev không cần biết ABI
Dev không đọc bytecode
Debug phải giống Web2
Không phụ thuộc “expert nội bộ”

