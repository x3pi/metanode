# Kiến trúc Truy vấn và Lưu trữ Dữ liệu Lịch sử

Tài liệu này mô tả chi tiết kiến trúc lưu trữ và truy vấn dữ liệu lịch sử blockchain trong Metanode Execution Engine.

## Chế độ Archive Node (Nút Lưu Trữ Toàn Bộ)

Metanode hỗ trợ cấu hình "Archive Node" (Nút Lưu Trữ) được thiết kế để giữ lại toàn bộ trạng thái lịch sử, các block và logs mà không bao giờ bị xóa bỏ (pruning).

Để chạy một node ở chế độ Archive, bạn cần cấu hình tham số `epochs_to_keep` trong file `config.json` thành `0`:

```json
{
  "epochs_to_keep": 0,
  "ArchiveBaseName": "snapshot_archive"
}
```

**Cơ chế hoạt động:**
Dịch vụ `log_cleaner.go` liên tục giám sát dung lượng ổ cứng của node và tự động dọn dẹp các dữ liệu cũ hơn mức `epochs_to_keep`. Khi đặt giá trị này là `0`, hệ thống sẽ coi đây là một cờ (flag) vô hiệu hóa hoàn toàn tiến trình dọn dẹp. Node sẽ lưu trữ vĩnh viễn toàn bộ các block.

## Các Tầng Lưu Trữ (Storage Mediums)

Metanode áp dụng phương pháp lưu trữ đa tầng (multi-tiered) cho việc lưu trữ cơ sở dữ liệu:

1. **Trie Database (`trie_database/`):**
   Lưu trữ các node của cây MPT (Merkle Patricia Trie) dành cho Smart Contract và các biến trạng thái (state variables).
   
2. **Account State & Stake State DBs:**
   Lưu trữ trực tiếp số dư tài khoản (balance), nonces, và dữ liệu stake của các validator.
   
3. **State Changelog (`StateChangelogDB`):**
   Lưu lại sự thay đổi trạng thái (diff) của từng block. Điều này cho phép hệ thống đảo ngược trạng thái nhanh chóng trong trường hợp xảy ra fork.
   
4. **Lưu trữ Snapshot:**
   Được sử dụng trong quá trình đồng bộ (sync) hoặc phục hồi mạng. Các node chạy ở chế độ Archive sẽ tạo ra các file snapshot lớn hơn rất nhiều do không có dữ liệu lịch sử nào bị xóa đi. Khi một bản lưu trữ (archive) được tạo hoặc tải xuống, nó sử dụng cơ chế chia nhỏ file (split-archive) được định nghĩa trong `file_transfer.go` để xử lý các tệp có dung lượng nhiều Gigabyte.

## Luồng Truy vấn Lịch sử (RPC Gateway)

Khi truy vấn dữ liệu lịch sử (ví dụ: số dư lịch sử, biên lai giao dịch - receipt, hoặc block theo mã hash), hệ thống sẽ xử lý các yêu cầu này chủ yếu thông qua RPC API Gateway (`execution/cmd/rpc/`).

### Truy vấn Trạng thái Trực tiếp (Direct State Queries)
Để loại bỏ sự phụ thuộc vào các bộ nhớ đệm (cache) cũ và đảm bảo tính tất định (determinism), các truy vấn trạng thái lịch sử sẽ quét trực tiếp vào `AccountStateDB` của nền tảng Go:

1. **Tiếp nhận Yêu cầu:** Người dùng gửi một JSON-RPC request (ví dụ: `eth_getTransactionCount` hoặc `mtn_getAccountState`) tới HTTP Proxy Handler.
2. **Đánh chặn qua TCP (TCP Interception):** Proxy (`http_handler.go`) sẽ chặn yêu cầu này lại và gửi một lệnh TCP gọn nhẹ (`GetAccountState`) thông qua connection pool nội bộ tới backend chính của Meta-Node.
3. **Truy cập Cơ sở dữ liệu:** Execution Engine truy cập vào `AccountStateDB` đang hoạt động, lấy ra trạng thái mới nhất và tuyệt đối chính xác (bao gồm nonces và số dư), sau đó tuần tự hóa dữ liệu bằng Protobuf (`pb.AccountState`).
4. **Trả về Kết quả:** Dữ liệu được gửi ngược lại qua luồng TCP về cho proxy, từ đó proxy sẽ định dạng lại thành phản hồi JSON-RPC chuẩn cho client.

### Truy vấn Block & Giao dịch (Block & Transaction Queries)
Đối với các phương thức như `eth_getBlockByHash` hoặc `eth_getTransactionReceipt`:
- Yêu cầu thường được định tuyến từ proxy sang `rpc-server`, tại đây `rpc_block.go` và `rpc_transaction.go` sẽ truy vấn `ChainState` gốc.
- Các module này tương tác với `BlockChainDB` để lấy header và body của block lịch sử từ hệ thống lưu trữ bền vững (persistent storage).

---
> [!NOTE] 
> **Lưu ý về Hiệu năng**
> Chế độ Archive Node đòi hỏi dung lượng lưu trữ lớn hơn rất nhiều. Hãy đảm bảo bạn có đủ không gian ổ cứng NVMe/SSD nếu thiết lập `epochs_to_keep` bằng `0`, vì `StateChangelogDB` sẽ tăng trưởng tuyến tính theo mỗi block được sinh ra.
