# Hướng Dẫn Bảo Mật — MetaNode (Tầng Go)

Tài liệu này mô tả cấu hình bảo mật, các biện pháp gia cố, và quy trình vận hành
tốt nhất cho node MetaNode Go (`mtn-simple-2025`).

## Mục Lục

1. [Quản Lý Bí Mật Qua Biến Môi Trường](#quản-lý-bí-mật-qua-biến-môi-trường)
2. [Bảo Mật Mạng](#bảo-mật-mạng)
3. [Xác Thực](#xác-thực)
4. [Giới Hạn Tốc Độ (Rate Limiting)](#giới-hạn-tốc-độ-rate-limiting)
5. [Chính Sách CORS](#chính-sách-cors)
6. [Bảo Mật IPC / UDS](#bảo-mật-ipc--uds)
7. [Bảo Mật Mã Hóa](#bảo-mật-mã-hóa)
8. [An Toàn Fork](#an-toàn-fork)
9. [Endpoint Debug](#endpoint-debug)

---

## Quản Lý Bí Mật Qua Biến Môi Trường

Các giá trị cấu hình nhạy cảm có thể được truyền qua biến môi trường thay vì
lưu trong `config.json`. **Biến môi trường được ưu tiên hơn** giá trị trong file cấu hình.

| Biến Môi Trường | Khóa Config | Mô Tả |
|---|---|---|
| `META_PRIVATE_KEY` | `private_key` | Khóa riêng BLS của validator |
| `META_REWARD_PRIVATE_KEY` | `reward_sender_private_key` | Khóa riêng ETH cho phân phối phần thưởng |
| `META_SECURE_PASSWORD` | `securepassword` | Mật khẩu API quản trị |

### Cách Sử Dụng

```bash
# Thiết lập bí mật qua môi trường (khuyến nghị cho production)
export META_PRIVATE_KEY="0x..."
export META_REWARD_PRIVATE_KEY="0x..."
export META_SECURE_PASSWORD="mat-khau-manh-cua-ban"

# Khởi động node — giá trị config.json bị ghi đè
./simple_chain --config config.json
```

Sau khi thiết lập biến môi trường, bạn có thể **xóa các giá trị bí mật** tương ứng
trong `config.json` để giảm thiểu rủi ro lộ thông tin.

---

## Bảo Mật Mạng

### Cổng và Giao Thức

| Cổng | Giao Thức | Mục Đích | Xác Thực |
|---|---|---|---|
| `rpc_port` (config) | HTTP/WS/HTTPS | RPC API (MetaMask, ví, dApp) | Không (công khai) |
| `4200` (cố định) | TCP | Giao tiếp P2P block/TX (Go↔Go) | Không |
| `peer_rpc_port` (config) | TCP | RPC khám phá node ngang hàng | Không |
| QUIC | QUIC/TLS 1.2+ | Tải lên chunk file | TLS (InsecureSkipVerify*) |

> **⚠️ Lưu ý**: Kết nối QUIC sử dụng `InsecureSkipVerify: true` vì các node ngang hàng
> dùng chứng chỉ tự ký. Để bảo mật hoàn toàn, triển khai **CA riêng** và phân phối
> chứng chỉ cho tất cả validator.

### Khuyến Nghị

- Chạy node trên **mạng riêng** hoặc qua đường hầm **VPN/WireGuard**
- Sử dụng tường lửa để giới hạn cổng P2P (4200, peer_rpc_port) chỉ cho các IP validator đã biết
- Chỉ mở cổng RPC ra internet công cộng

---

## Xác Thực

### API Quản Trị (Admin)

API quản trị (`admin_login`, `admin_setState`, `admin_createBackup`) sử dụng mật khẩu
lưu trong config là `securepassword` (có thể ghi đè qua `META_SECURE_PASSWORD`).

**Biện pháp bảo mật:**
- So sánh mật khẩu sử dụng **`crypto/subtle.ConstantTimeCompare()`** để chống tấn công timing
- Endpoint quản trị **không** truy cập được qua CORS từ trình duyệt (CORS chỉ áp dụng cho RPC root)

### API RPC

API RPC (`eth_*`, `mtn_*`, `debug_*`) **không yêu cầu xác thực** theo thiết kế (chuẩn Ethereum RPC).
Sử dụng tường lửa để hạn chế truy cập.

---

## Giới Hạn Tốc Độ (Rate Limiting)

Giới hạn tốc độ toàn cục và theo IP được áp dụng cho tất cả request RPC.

| Tham Số | Giá Trị | Mô Tả |
|---|---|---|
| Tốc độ toàn cục | 300.000 req/s | Tối đa trên tất cả client |
| Burst toàn cục | 50.000 | Dung lượng burst |
| Tốc độ theo IP | 10.000 req/s | Tối đa mỗi IP client |
| Burst theo IP | 2.000 | Burst mỗi client |
| Kích thước cache IP | 10.000 mục | Số IP theo dõi tối đa |
| Dọn dẹp cache | 5 phút | Xóa mục IP cũ |

Cấu hình trong `cmd/simple_chain/rpc_rate_limiter.go`.

---

## Chính Sách CORS

CORS `Access-Control-Allow-Origin: *` chỉ được áp dụng cho:
- `/` — Endpoint RPC chính (cần thiết cho MetaMask / ví trình duyệt)
- `/ws` — WebSocket RPC (cần thiết cho API subscription Ethereum)

**Tất cả endpoint khác** (admin, debug, pipeline, metrics) **KHÔNG** có header CORS,
ngăn chặn tấn công cross-site từ trình duyệt.

---

## Bảo Mật IPC / UDS

Giao tiếp giữa Go và Rust sử dụng Unix Domain Socket (UDS).

| Socket | Quyền | Mục Đích |
|---|---|---|
| `/tmp/executor{N}.sock` | Mặc định OS | Rust → Go gửi block |
| `/tmp/metanode-tx-{N}.sock` | `0660` | Go → Rust gửi giao dịch |
| `/tmp/metanode-notification-{N}.sock` | `0660` | Go → Rust thông báo epoch |

Quyền socket giới hạn truy cập cho chủ sở hữu và nhóm. Đảm bảo tiến trình Go và Rust
chạy dưới **cùng user hoặc group**.

---

## Bảo Mật Mã Hóa

| Thành Phần | Thuật Toán | Chi Tiết |
|---|---|---|
| Lưu trữ khóa BLS | AES-256-GCM + Scrypt | N=32768, r=8, p=1 |
| Ký giao dịch | ECDSA secp256k1 | Chuẩn Ethereum |
| Ký block | BLS12-381 | Master ký, Sub xác minh |
| Đường dẫn Xapian DB | Hash Keccak-256 | Tên DB được hash để chống path traversal |

---

## An Toàn Fork

Hệ thống triển khai nhiều lớp phòng chống fork:

1. **Chứng thực trạng thái (State Attestation)**: Chứng thực dựa trên quorum, tự động dừng node khi phát hiện phân kỳ
2. **Thời gian từ consensus**: Tất cả timestamp lấy từ Rust consensus (không dùng đồng hồ local)
3. **Sắp xếp TX xác định**: Giao dịch được sắp xếp theo hash trước khi thực thi
4. **Xóa Excluded Items**: Dữ liệu pool cục bộ được xóa trước khi xử lý block từ consensus
5. **Dừng khẩn cấp**: `os.Exit(1)` / `process::exit(1)` khi phát hiện trạng thái không nhất quán nghiêm trọng

---

## Endpoint Debug

> **⚠️ Cảnh báo**: Endpoint debug tiết lộ dữ liệu vận hành nhạy cảm. Hạn chế truy cập
> trong production bằng tường lửa.

| Endpoint | Loại | Xác Thực | Mô Tả |
|---|---|---|---|
| `debug_traceTransaction` | RPC | Không | Thực thi lại giao dịch với tracing |
| `debug_traceBlock` | RPC | Không | Thực thi lại toàn bộ block với tracing |
| `debug_listLogFiles` | RPC | Không | Liệt kê file log theo epoch |
| `debug_getLogFileContent` | RPC | Không | Đọc nội dung file log |
| `/debug/logs/ws` | HTTP/WS | Không | Stream log thời gian thực |
| `/debug/logs/content` | HTTP GET | Không | Xem trước file log |

**Biện pháp giảm thiểu**: Bind RPC vào `127.0.0.1` trong production, hoặc dùng iptables
để giới hạn truy cập chỉ cho IP quản trị đáng tin cậy.
