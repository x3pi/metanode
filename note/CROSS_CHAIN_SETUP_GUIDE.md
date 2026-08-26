# Hướng Dẫn Setup & Chạy Giao Dịch Cross-Chain (Client + Relayer)

Tài liệu này hướng dẫn chi tiết quy trình thiết lập mạng lưới đa chuỗi, đăng ký danh bạ chữ ký số BLS (`ChainRegistry`) trên Gateway Precompile (`0x1002`), vận hành Relayer Daemon chạy ngầm và thực hiện giao dịch cross-chain từ phía Client.

---

## 📊 Bảng Thông Tin Môi Trường & Cổng RPC

| Mạng | Chain ID | Địa chỉ RPC JSON-RPC | Vai trò |
| :--- | :---: | :--- | :--- |
| **Root Anchor** | `991` | `http://127.0.0.1:10746` (hoặc `9099`) | Hub bảo mật & quản lý trần cung toàn cầu |
| **Private Chain A** | `101` | `http://127.0.0.1:8546` | Chuỗi nguồn gửi giao dịch |
| **Private Chain B** | `102` | `http://127.0.0.1:8547` | Chuỗi đích nhận tài sản / thực thi contract |

> **Khóa ECDSA Relayer (Devnet):** `3f7a0514531a1485edc4270f06dbed62da4974c3b5bbd54a4534060514b8023d` (Địa chỉ: `0x4b51d69B903C136654D168d0d500dA58AFdc5b60`)

---

## 🚀 Quy Trình Vận Hành 5 Bước

### Bước 1: Khởi động Root Anchor (Public Chain)
Mở **Terminal 1**, khởi động Root Anchor:

```bash
cd ~/nhat/consensus-chain/metanode/deploy/ansible
./ansible_deploy.sh --reset-all
```
> *Đảm bảo RPC Root Anchor phản hồi tại `http://127.0.0.1:10746`.*

---

### Bước 2: Khởi động 2 Private Chains (101 & 102)
Mở **Terminal 2**, khởi động 2 Private Chains:

```bash
cd ~/nhat/consensus-chain/metanode/deploy/systemd
./setup_2_private_chains.sh --clean
```
> *Lệnh này khởi động Chain 101 (`http://127.0.0.1:8546`) và Chain 102 (`http://127.0.0.1:8547`).*

---

### Bước 3: Đăng ký Chain & Ủy ban BLS vào Gateway (`register_chains`)
Để các chuỗi có thể xác thực chữ ký Quorum Certificate của nhau, cần đăng ký danh bạ committee (`ChainRegistry` với BLS Proof-of-Possession) và hạn mức ban đầu lên Gateway (`0x1002`) của **cả 3 chuỗi**.

Mở **Terminal 3**, chạy lần lượt 3 lệnh:

```bash
cd ~/nhat/consensus-chain/metanode/execution

# 1. Đăng ký trên Chain 101
go run ./cmd/tool/register_chains \
    --key "3f7a0514531a1485edc4270f06dbed62da4974c3b5bbd54a4534060514b8023d" \
    --root-anchor "http://127.0.0.1:8546" \
    --chains "101,102" \
    --chains-dir "../deploy/systemd/private_chains_data"

# 2. Đăng ký trên Chain 102
go run ./cmd/tool/register_chains \
    --key "3f7a0514531a1485edc4270f06dbed62da4974c3b5bbd54a4534060514b8023d" \
    --root-anchor "http://127.0.0.1:8547" \
    --chains "101,102" \
    --chains-dir "../deploy/systemd/private_chains_data"

# 3. Đăng ký trên Root Anchor (Chain 991)
go run ./cmd/tool/register_chains \
    --key "3f7a0514531a1485edc4270f06dbed62da4974c3b5bbd54a4534060514b8023d" \
    --root-anchor "http://127.0.0.1:10746" \
    --chains "101,102" \
    --chains-dir "../deploy/systemd/private_chains_data"
```

---

### Bước 4: Khởi chạy Relayer Daemon ngầm (Zero-Trust)

Relayer chạy hoàn toàn độc lập, **không cần và không chạm vào bất kỳ file private key nào của Validator**:

```bash
cd ~/nhat/consensus-chain/metanode/execution/cmd/tool/cross_chain_relayer

# Khởi chạy Relayer Daemon ngầm (-key là ví ECDSA của Relayer trả gas, hoàn toàn không cần key Validator):
go run main.go \
    -key "3f7a0514531a1485edc4270f06dbed62da4974c3b5bbd54a4534060514b8023d" \
    -root-anchor "http://127.0.0.1:10746" \
    -chains "101=http://127.0.0.1:8546,102=http://127.0.0.1:8547" \
    -poll-interval-ms 100
```
# HOẶC Chạy ngầm (nohup background):
# nohup go run main.go -key "3f7a0514531a1485edc4270f06dbed62da4974c3b5bbd54a4534060514b8023d" -root-anchor "http://127.0.0.1:10746" -chains "101=http://127.0.0.1:8546,102=http://127.0.0.1:8547" -poll-interval-ms 100 > relayer.log 2>&1 &

---

### Bước 5: Chạy kịch bản Client (`02-client-only-transfer`)
Client hoạt động thuần túy như một người dùng/ứng dụng dApp: chỉ gọi hàm `Gateway.outbound()` trên Chain 101 và đợi số dư hoặc trạng thái Smart Contract cập nhật trên Chain 102.

Mở **Terminal 5**, chạy kịch bản:

```bash
cd ~/nhat/consensus-chain/metanode-suite/test-simple/test-rpc/test-blockstm/cross-chain/02-client-only-transfer

go run .
```

#### 🎯 Luồng thực thi tự động:
1. **Phần 1 - Chuyển tiền (Native Transfer):** Client nộp lệnh chuyển 500 MTN từ Chain 101 -> Chain 102.
   - Relayer phát hiện event `outbound()` trên Chain 101.
   - Relayer nộp `attestCommit` lên Root Anchor & Chain 102.
   - Relayer nộp `claimMessage` lên Chain 102 ➔ Số dư ví người nhận trên Chain 102 tăng thêm 500 MTN.
2. **Phần 2 - Gọi Smart Contract (Contract Call):** Client nộp lệnh gọi hàm `increment()` từ Chain 101 sang contract `TestCounter` trên Chain 102.
   - Relayer tự động chuyển tiếp và thực thi lệnh trên Chain 102 ➔ Biến đếm `Counter` trên Chain 102 tăng từ `0` lên `1`.
3. Client xác nhận cả 2 điều kiện thành công và in thông báo hoàn tất:
   ```
   🎉 BINGOOOO! TIỀN ĐÃ MINT BÊN CHAIN B THÀNH CÔNG! (+500.0000 MTN)
   🎉 BINGOOOO! SMART CONTRACT CHAIN B ĐÃ NHẬN LỆNH TỪ CHAIN A VÀ THỰC THI THÀNH CÔNG! (Counter = 1)
   ✅ HOÀN TẤT KỊCH BẢN CLIENT!
   ```
