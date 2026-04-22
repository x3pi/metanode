# Phân Tích Dữ Liệu Khởi Động Rust - Chi Tiết Theo Từng Bước

## Tổng Quan

Tài liệu này phân tích chi tiết từng bước quá trình khởi động của Rust executor trong hệ thống Go-Rust hybrid, tập trung vào:
- Dữ liệu cần thiết cho từng bước
- Nguồn gốc của dữ liệu (từ Go, từ file, hay hardcoded)
- Luồng xử lý giữa Go và Rust

## Kiến Trúc Tổng Quan

```
Rust Client (socket-rust/) <--- Unix Socket ---> Go Server (executor/)
    ├── main.rs (khởi động)
    ├── server.rs (kết nối socket)
    └── client_handler.rs (xử lý request/response)
```

---

## Bước 1: Rust main() Khởi Động

### Mã Nguồn
```rust
fn main() {
    // Đọc socket paths từ environment variable
    let socket_paths_str = env::var("RUST_SOCKET_PATHS").unwrap_or_else(|_| {
        eprintln!("ERROR: Environment variable RUST_SOCKET_PATHS is required");
        eprintln!("Usage: RUST_SOCKET_PATHS=\"/tmp/rust-go.sock_1,/tmp/rust-go.sock_2\" ./rust-executor");
        eprintln!("Example: RUST_SOCKET_PATHS=\"/tmp/rust-go.sock_1\" ./rust-executor");
        process::exit(1);
    });

    // Parse socket paths từ string (comma-separated)
    let socket_paths: Vec<String> = socket_paths_str
        .split(',')
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
        .collect();

    if socket_paths.is_empty() {
        eprintln!("ERROR: No socket paths provided in RUST_SOCKET_PATHS");
        eprintln!("Usage: RUST_SOCKET_PATHS=\"/tmp/rust-go.sock_1,/tmp/rust-go.sock_2\" ./rust-executor");
        process::exit(1);
    }
    // ...
}
```

### Dữ liệu Cần Thiết
| Dữ liệu | Giá Trị | Nguồn Gốc | Mô Tả |
|---------|---------|------------|---------|
| `RUST_SOCKET_PATHS` | Environment variable | **Bắt buộc config** | Danh sách đường dẫn socket ngăn cách bởi dấu phẩy |

### Đặc Điểm
- **Bắt buộc config**: Không có giá trị mặc định, phải cung cấp `RUST_SOCKET_PATHS`
- **Environment-driven**: Đọc từ biến môi trường thay vì hardcoded
- **Validation**: Kiểm tra và validate input trước khi chạy
- **Flexible**: Hỗ trợ số lượng socket bất kỳ (không giới hạn 2 như trước)
- **Error handling**: Thoát với error code nếu thiếu config

### Cách Sử Dụng

```bash
# Cấu hình 1 socket
export RUST_SOCKET_PATHS="/tmp/rust-go.sock_1"
./target/debug/socket

# Cấu hình nhiều socket (comma-separated)
export RUST_SOCKET_PATHS="/tmp/rust-go.sock_1,/tmp/rust-go.sock_2,/tmp/rust-go.sock_3"
./target/debug/socket

# Inline command
RUST_SOCKET_PATHS="/tmp/rust-go.sock_1" ./target/debug/socket
```

### Lỗi Nếu Thiếu Config

```bash
$ ./target/debug/socket
ERROR: Environment variable RUST_SOCKET_PATHS is required
Usage: RUST_SOCKET_PATHS="/tmp/rust-go.sock_1,/tmp/rust-go.sock_2" ./rust-executor
Example: RUST_SOCKET_PATHS="/tmp/rust-go.sock_1" ./rust-executor
```

---

## Bước 2: Thiết Lập Kết Nối Unix Socket

### Mã Nguồn
```rust
pub fn start_connector(socket_path: &'static str) {
    loop {
        println!("[Rust Client] Đang thử kết nối tới {}...", socket_path);
        match UnixStream::connect(socket_path) {
            Ok(mut stream) => {
                // Xử lý kết nối thành công
            }
            Err(e) => {
                eprintln!("[Rust Client] Không thể kết nối tới {}: {}. Thử lại sau 2 giây.", socket_path, e);
                thread::sleep(Duration::from_secs(2));
            }
        }
    }
}
```

### Dữ liệu Cần Thiết
| Dữ liệu | Kiểu | Nguồn Gốc | Mô Tả |
|---------|------|------------|---------|
| Socket file | Unix domain socket | Go server tạo | File socket thực tế trên filesystem |

### Quá Trình Tạo Socket File (Go Side)

**File:** `executor/unix_sokcet.go`

```go
func (se *SocketExecutor) listenAndServe() error {
    // Xóa socket file cũ nếu tồn tại
    if _, err := os.Stat(se.socketPath); err == nil {
        if err := os.Remove(se.socketPath); err != nil {
            return fmt.Errorf("không thể xóa socket file cũ: %w", err)
        }
    }

    // Tạo listener mới
    listener, err := net.Listen("unix", se.socketPath)
    if err != nil {
        return fmt.Errorf("không thể lắng nghe trên socket %s: %w", se.socketPath, err)
    }
    // ...
}
```

**Đặc điểm:**
- Go server chủ động tạo và quản lý socket files
- Socket files được đặt tại `/tmp/rust-go.sock_1` và `/tmp/rust-go.sock_2`
- Rust client chỉ cần biết đường dẫn và kết nối

---

## Bước 3: Gửi StatusRequest (Kiểm tra kết nối)

### Mã Nguồn Rust
```rust
pub fn handle_status_request(
    stream: &mut UnixStream,
    socket_path: &str,
) -> Result<(), Box<dyn std::error::Error>> {
    let wrapped_request = Request {
        payload: Some(request::Payload::StatusRequest(StatusRequest {})),
    };
    // Gửi và nhận response...
}
```

### Dữ liệu Cần Thiết
| Dữ liệu | Kiểu | Nguồn Gốc | Mô Tả |
|---------|------|------------|---------|
| Request | `StatusRequest {}` | Hardcoded trong code | Request rỗng, chỉ kiểm tra kết nối |

### Dữ Liệu Nhận Được
| Dữ Liệu | Giá Trị | Nguồn Gốc | Mô Tả |
|----------|---------|------------|---------|
| `status_message` | `"Server is running smoothly"` | Hardcoded trong Go | Thông báo trạng thái server |
| `uptime_seconds` | `9001` | Hardcoded trong Go | Thời gian uptime (giả lập) |

### Xử Lý Trong Go
```go
case *pb.Request_StatusRequest:
    logger.Info("[Go Server] Nhận được yêu cầu StatusRequest")
    status := &pb.ServerStatus{
        StatusMessage: "Server is running smoothly",
        UptimeSeconds: 9001,
    }
    wrappedResponse = &pb.Response{
        Payload: &pb.Response_ServerStatus{
            ServerStatus: status,
        },
    }
```

---

## Bước 4: Gửi BlockRequest (Lấy dữ liệu validators)

### Mã Nguồn Rust
```rust
pub fn handle_block_request(
    stream: &mut UnixStream,
    socket_path: &str,
    block_number: u64,  // <- Dữ liệu đầu vào
) -> Result<(), Box<dyn std::error::Error>> {
    println!("[{}] Gửi BlockRequest cho block {}", socket_path, block_number);

    let block_req = BlockRequest { block_number };
    let wrapped_request = Request {
        payload: Some(request::Payload::BlockRequest(block_req)),
    };
    // ...
}
```

### Dữ Liệu Cần Thiết
| Dữ Liệu | Kiểu | Nguồn Gốc | Mô Tả |
|----------|------|------------|---------|
| `block_number` | `u64` | Khởi tạo từ 0 trong Rust | Số block cần lấy validators (bắt đầu từ 0) |

### Dữ Liệu Nhận Được
| Dữ Liệu | Nguồn Gốc | Mô Tả |
|----------|------------|---------|
| `ValidatorList` | Go database | Danh sách tất cả validators tại block đó |

### Quá Trình Lấy Validators Trong Go

**File:** `executor/unix_socket_handler.go`

```go
func (rh *RequestHandler) HandleBlockRequest(request *pb.BlockRequest) (*pb.ValidatorList, error) {
    blockNumber := request.GetBlockNumber()

    // 1. Lấy block hash từ blockchain
    blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)

    // 2. Lấy block data từ storage
    blockData, err := rh.chainState.GetBlockDatabase().GetBlockByHash(blockHash)

    // 3. Tạo ChainState tại block đó
    chainStateNew, err := blockchain.NewChainState(rh.storageManager, blockDatabase, blockData.Header(), rh.chainState.GetConfig(), rh.chainState.GetFreeFeeAddress())

    // 4. Lấy tất cả validators từ stake state DB
    validators, err := chainStateNew.GetStakeStateDB().GetAllValidators()

    // 5. Map sang protobuf format
    validatorList := &pb.ValidatorList{}
    for _, dbValidator := range validators {
        val := &pb.Validator{
            Address: dbValidator.Address().Hex(),
            Name: dbValidator.Name(),
            // ... các trường khác
        }
        validatorList.Validators = append(validatorList.Validators, val)
    }

    return validatorList, nil
}
```

---

## Luồng Dữ Liệu Tổng Quan

```mermaid
graph TD
    A[Rust main()] --> B[Hardcoded socket paths]
    B --> C[Connect to Unix socket]
    C --> D[Socket files created by Go]

    D --> E[Send StatusRequest]
    E --> F[Go returns hardcoded status]

    F --> G[Send BlockRequest with block_number=0]
    G --> H[Go queries database]

    H --> I[Go returns ValidatorList]
    I --> J[Rust processes validators]

    H --> K[Block hash from blockchain]
    K --> L[Block data from storage]
    L --> M[Chain state recreation]
    M --> N[Validators from stake DB]
```

---

## Cấu Trúc Dữ Liệu Validator

### Validator Message (Protobuf)
```protobuf
message Validator {
    string address = 1;
    string name = 2;
    string description = 3;
    string website = 4;
    bool is_jailed = 5;
    uint64 commission_rate = 8;
    string min_self_delegation = 9;
    string total_staked_amount = 15;
    string primary_address = 14;
    string worker_address = 16;
    string p2p_address = 17;
    string pubkey_bls = 18;
    string pubkey_secp = 19;
}
```

### Nguồn Dữ Liệu Cho Các Trường
| Trường | Nguồn Gốc | Mô Tả |
|--------|------------|---------|
| `address` | `dbValidator.Address().Hex()` | Địa chỉ validator từ database |
| `name` | `dbValidator.Name()` | Tên validator |
| `pubkey_bls` | `dbValidator.PubKeyBls()` | Public key BLS cho signing |
| `total_staked_amount` | `dbValidator.TotalStakedAmount().String()` | Tổng stake (dạng string) |
| `p2p_address` | `dbValidator.P2PAddress()` | Địa chỉ P2P network |

---

## Các Loại Request/Response Khác

Ngoài StatusRequest và BlockRequest, hệ thống còn hỗ trợ:

1. **GetActiveValidatorsRequest** - Lấy validators active cho epoch transition
2. **GetValidatorsAtBlockRequest** - Lấy validators tại block cụ thể (cho snapshot)
3. **GetLastBlockNumberRequest** - Lấy block number cuối cùng (cho init)
4. **GetCurrentEpochRequest** - Lấy epoch hiện tại (Sui-style)
5. **GetEpochStartTimestampRequest** - Lấy timestamp bắt đầu epoch
6. **AdvanceEpochRequest** - Chuyển sang epoch mới

---

## Kết Luận

### Tóm Tắt Nguồn Gốc Dữ Liệu

1. **Từ Environment Variables:**
   - `RUST_SOCKET_PATHS` - đường dẫn socket (bắt buộc)

2. **Hardcoded trong Rust:**
   - Block number khởi tạo (0)
   - Request structures

3. **Tạo bởi Go server:**
   - Socket files tại các đường dẫn được config
   - Server status response
   - Database connections

4. **Từ Go database:**
   - Block data
   - Validator information
   - Chain state
   - Stake state

### Điểm Quan Trọng

- **Rust không cần file config**: Tất cả thông tin kết nối được hardcoded
- **Go chủ động tạo socket**: Rust chỉ cần biết đường dẫn
- **Database-driven**: Dữ liệu validators thực tế từ Go database
- **Incremental loading**: Rust load validators theo từng block
- **Fault tolerant**: Retry logic khi kết nối thất bại

---

*Tài liệu được tạo dựa trên phân tích mã nguồn Go-Rust hybrid system*"