# Ethereum Key Tool

Công cụ này dùng để tạo (generate) một cặp khóa Ethereum mới hoặc khôi phục (recover) địa chỉ từ một khóa cá nhân (private key) đã có sẵn.

## Hướng dẫn sử dụng

### 1. Tạo cặp khóa mới (Generate Key)

Để tạo ngẫu nhiên một private key mới và nhận thư mục địa chỉ (address) tương ứng, hãy chạy mã nguồn mà không cần truyền thêm tham số nào.

```bash
go run key_eth.go
```

**Kết quả:**
```
=== ETH Generate New Key Pair ===
ETH_PRIVATE_KEY: 0x33dc8c1d4ca9d1bac9a81a2a373a20fcc4c1eb7c024556a2c4cede0d14b6919d
ETH_ADDRESS:     0xBa7c5001Ae4f8351d84Bc1615237715a8D2209b2
```

### 2. Khôi phục địa chỉ từ Private Key (Recover Key)

Để lấy ra (recover) địa chỉ (address) từ một private key đã có sẵn, hãy truyền chuỗi hex của private key đó (có thể bao gồm hoặc không bao gồm tiền tố `0x`) vào làm tham số dòng lệnh.

```bash
go run key_eth.go <chuỗi_private_key_của_bạn>
```

**Ví dụ:**
```bash
go run key_eth.go 0x33dc8c1d4ca9d1bac9a81a2a373a20fcc4c1eb7c024556a2c4cede0d14b6919d
```

hoặc không dùng `0x`:
```bash
go run key_eth.go 28f0ad246c39b9b5a32692e4f9364e29c3df3cdd6ca6d88fcb40e9dc6bc6c511
```

**Kết quả:**
```
=== ETH Recover from Private Key ===
ETH_PRIVATE_KEY: 0x33dc8c1d4ca9d1bac9a81a2a373a20fcc4c1eb7c024556a2c4cede0d14b6919d
ETH_ADDRESS:     0xBa7c5001Ae4f8351d84Bc1615237715a8D2209b2
```

## Thư viện sử dụng
Công cụ thực thi chuẩn bảo mật chuẩn đường cong elliptic (ECDSA) lấy từ thư viện `github.com/ethereum/go-ethereum/crypto`, đảm bảo 100% việc tạo khóa và sinh địa chỉ tương thích với hệ sinh thái Ethereum.
