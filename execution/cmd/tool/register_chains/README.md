# 🛠️ Metanode Gateway Admin Tool (`register_chains`)

Công cụ dòng lệnh (CLI) hợp nhất để quản trị **Gateway Precompile (`0x1002`)**, phục vụ đăng ký danh bạ, điều phối hạn mức tiền cọc và tra cứu thông tin liên chuỗi.

> ⚠️ **Cập nhật 2026-09-05**: các mô tả "biểu quyết Quản trị của ủy ban" / Timelock ở phiên bản
> README cũ đã LỖI THỜI. Từ 2026-09-04, toàn bộ `GovernanceEngine` (propose/vote/quorum/
> timelock/execute) đã bị xoá hẳn — không còn cơ chế vote nào trong hệ thống nữa. Tool này giờ
> chỉ gọi 2 nhóm hàm: (a) tự ký (self-signed) bằng đúng uỷ ban BLS thật đang sống trên chain
> nguồn — `registerChainViaStake`/`transferAllocationWithCert`/`allocateSupplyWithCert` — cho các
> hành động chỉ ảnh hưởng tài nguyên của chính chain đó; (b) không có hành động nào của tool này
> cần `RecoveryCommittee` (uỷ ban cứu hộ riêng, dùng cho đổi uỷ ban/tuyên bố chết một chain KHÁC).
> Xem `note/eurozone_unified_native_coin_plan.md` mục "CẬP NHẬT (2026-09-04, phiên sau)" để biết
> đầy đủ thiết kế thay thế.

---

## 🚀 1. Biên Dịch Nhanh

```bash
cd execution/cmd/tool/register_chains
go build -o register_chains .
```

> 💡 **Tự động nhận diện cấu hình:** Tool tự động tìm Root Anchor RPC từ `/tmp/private_chains.json`
> (biến môi trường `ROOT_ANCHOR_RPC` ghi đè), và tự động tìm file config `gateway_register.json`
> (biến môi trường `GATEWAY_CONFIG` ghi đè, hoặc cờ `-config` tường minh). Bạn không cần truyền
> lại các cờ đường dẫn dài dòng nếu đang chạy trên máy chủ chứa node.

---

## 📋 2. Các Lệnh Thường Dùng (Copy-Paste)

### 1️⃣ Đăng Ký Danh Bạ & Khóa BLS Cho Chain Mới (action mặc định, không cần `-action`)
> Đọc file config (`-config gateway_register.json`, hoặc tự động dò), gọi thật
> `registerChainViaStake` (cọc tiền thật từ ví `-key`, không cần vote) cho từng chain khai báo
> trong file, đăng ký chéo sang RPC của từng chain khác để chúng biết uỷ ban của nhau, rồi (nếu
> `-fund-genesis`) mint + phân bổ genesis supply.

```bash
./register_chains -config /path/to/gateway_register.json

# Kèm mint + phân bổ genesis supply cho các chain vừa đăng ký:
./register_chains -config /path/to/gateway_register.json -fund-genesis \
  -genesis-supply 400000000000000000000000000 -per-chain-allocation 100000000000000000000000000
```

---

### 2️⃣ Chuyển Hạn Mức Tiền Cọc Giữa 2 Chain (`transfer-alloc`)
> Chuyển hạn mức (PerChainAllocation) đã tồn tại từ chain nguồn sang chain đích, tự ký
> (self-signed) bởi đúng uỷ ban BLS thật của chain nguồn — không cần vote, không cần chờ ai khác.

```bash
# Cú pháp: ./register_chains -action transfer-alloc -from-chain <Nguồn> -to-chain <Đích> -amount-mtn <Số_MTN>

# Ví dụ: Chuyển 20,000,000 MTN hạn mức từ Chain 101 sang Chain 103:
./register_chains -action transfer-alloc -from-chain 101 -to-chain 103 -amount-mtn 20000000
```

---

### 3️⃣ Tra Cứu Hạn Mức Cung Tiền Của Các Chain (`query-alloc`)
> Xem số dư hạn mức tiền cọc còn lại của từng chain để biết chain nào có thể xuất tiền liên chuỗi.

```bash
./register_chains -action query-alloc -chains "991,101,102,103,104"
```

`query-alloc-raw` (kèm đúng 1 chain ID trong `-chains`) in ra đúng 1 dòng số wei thập phân, không
banner/log — dùng cho script tự động (ví dụ `gen_single_chain.py` đối chiếu deterministic-genesis).

---

### 4️⃣ Tra Cứu Danh Bạ & Khóa Validator Của Chain (`query-registry`)
> Kiểm tra trạng thái đã đăng ký trong danh bạ, Epoch hiện tại và danh sách Public Key BLS của từng chain.

```bash
./register_chains -action query-registry -chains "101,102,103,104"
```

`query-genesis-wallet-raw` (kèm đúng 1 chain ID) in ra đúng địa chỉ `GenesisWallet` của chain đó
(hoặc địa chỉ zero nếu chưa đăng ký), không banner/log — cùng mục đích máy-đọc như trên.

---

### 5️⃣ Công Bố / Xác Minh Digest Genesis (`publish-genesis-digest` / `verify-genesis`)
> 2 bước của thiết kế deterministic-genesis (2026-09-04): sau khi đăng ký, mỗi validator tự build
> `genesis.json` từ đúng thông tin đã đăng ký on-chain và NÊN ra kết quả giống hệt nhau bit-for-bit
> — người đăng ký công bố digest của file đó (đúng 1 lần, chỉ `GenesisWallet` mới công bố được);
> bất kỳ ai sau đó dùng `verify-genesis` để đối chiếu genesis.json cục bộ của mình với digest đã
> công bố trước khi tin tưởng/join chain.

```bash
./register_chains -action publish-genesis-digest -chains 101 -genesis-file /path/to/genesis.json
./register_chains -action verify-genesis -chains 101 -genesis-file /path/to/genesis.json
```

---

## 📌 3. Bảng Tóm Tắt Tham Số Cờ Lệnh (CLI Flags)

| Cờ Lệnh | Giá trị mặc định | Giải thích |
| :--- | :--- | :--- |
| `-action` | `register` | `register` \| `transfer-alloc` \| `query-alloc` \| `query-alloc-raw` \| `query-registry` \| `query-genesis-wallet-raw` \| `publish-genesis-digest` \| `verify-genesis` |
| `-config` | *(Tự động dò)* | Đường dẫn file JSON config khai báo các chain cần đăng ký (dùng cho action `register`) |
| `-chains` | `101,102,103,104` | Danh sách Chain ID, ngăn cách bằng dấu phẩy (dùng cho các action `query-*`) |
| `-from-chain` | `101` | Chain nguồn trích hạn mức (dùng cho `transfer-alloc`) |
| `-to-chain` | `103` | Chain đích nhận hạn mức (dùng cho `transfer-alloc`) |
| `-amount-mtn` | `20000000` | Số lượng MTN muốn chuyển (dùng cho `transfer-alloc`) |
| `-amount-wei` | *(rỗng)* | Số wei chính xác muốn chuyển, ghi đè `-amount-mtn` nếu đặt (dùng cho `transfer-alloc`) |
| `-genesis-file` | *(rỗng)* | Đường dẫn `genesis.json` (dùng cho `publish-genesis-digest`/`verify-genesis`) |
| `-fund-genesis` | `false` | Sau khi đăng ký (action `register`), mint + phân bổ genesis supply luôn |
| `-genesis-supply` | *(rỗng)* | Tổng genesis supply cần mint trên Root Anchor, đơn vị wei (dùng với `-fund-genesis`) |
| `-per-chain-allocation` | *(rỗng)* | Số tiền phân bổ cho mỗi chain founding, đơn vị wei (dùng với `-fund-genesis`) |
| `-root-anchor`| *(Tự động dò)* | Địa chỉ RPC của Root Anchor (VD: `http://192.168.1.234:10746`) |
| `-key` | *(Devnet key)* | Khóa ECDSA Private Key trả phí gas + đứng tên cọc tiền/giao dịch |
