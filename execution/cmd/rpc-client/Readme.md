# RPC Reverse Proxy

Dự án này là một reverse proxy được viết bằng Go, đóng vai trò trung gian cho các lệnh gọi RPC và WebSocket tới một máy chủ RPC Ethereum (hoặc tương thích). Nó cũng cung cấp các điểm cuối tùy chỉnh để quản lý khóa BLS và phục vụ một giao diện người dùng tĩnh để đăng ký khóa BLS.

## Mục lục

- [Điều kiện tiên quyết](#điều-kiện-tiên-quyết)
- [Cấu hình](#cấu-hình)
- [Cài đặt](#cài-đặt)
- [Xây dựng (Build)](#xây-dựng-build)
- [Chạy dự án](#chạy-dự-án)
- [Tính năng](#tính-năng)
  - [Reverse Proxy](#reverse-proxy)
  - [WebSocket Proxy](#websocket-proxy)
  - [Xử lý RPC tùy chỉnh](#xử-lý-rpc-tùy-chỉnh)
  - [Lưu trữ khóa cá nhân (Private Key Store)](#lưu-trữ-khóa-cá-nhân-private-key-store)
  - [Phục vụ giao diện tĩnh](#phục-vụ-giao-diện-tĩnh)
- [Cấu trúc thư mục](#cấu-trúc-thư-mục-quan-trọng)

## Điều kiện tiên quyết

- **Go**: Phiên bản 1.18 trở lên.
- **Một máy chủ RPC Ethereum (hoặc tương thích)**: Proxy cần một URL máy chủ RPC để chuyển tiếp các yêu cầu.

## Cấu hình

Proxy được cấu hình thông qua một tệp JSON. Một tệp mẫu có tên `config-rpc.json` được cung cấp.

```json
{
  "rpc_server_url": "http://localhost:8646",
  "wss_server_url": "ws://localhost:8646/ws",
  "private_key": "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
  "server_port": ":8545",
  "chain_id": 991,
  "https_port": ":8446",
  "cert_file": "certificate.pem",
  "key_file": "private.key",
  "master_password": "your_strong_master_password_here",
  "app_pepper": "your_unique_secret_pepper_here"
}