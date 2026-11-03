# Kiến Trúc & Luồng Hoạt Động Hệ Thống Ví Chuyển Tiền Hộ (HelpPay Sponsor Wallet Pool)

Tài liệu này mô tả chi tiết toàn bộ kiến trúc, luồng xử lý dữ liệu, cơ chế đồng bộ và quản trị của hệ thống chuyển tiền hộ (Gas Sponsorship / HelpPay) trên blockchain.

---

## 1. Tổng Quan & Bài Toán Nghiệp Vụ

### 1.1. Bài toán
Khi người dùng mới, ví đối tác hoặc ví con trong hệ sinh thái tương tác với mạng lưới:
* Số dư native coin bằng 0 hoặc nhỏ hơn phí gas cần thiết để thực thi giao dịch.
* Nếu không có cơ chế hỗ trợ, giao dịch gửi lên qua cổng RPC (`eth_sendRawTransaction`) hoặc TCP (`ProcessRawTransactionFromClient`) sẽ bị từ chối ngay lập tức do không đủ tiền trả gas.

### 1.2. Giải pháp: Hệ thống HelpPay Sponsor Wallet Pool
* Tự động phát hiện khi tài khoản người gửi có số dư nhỏ hơn ngưỡng quy định (`min_balance`).
* Tự động điều phối một ví bảo trợ (sponsor wallet) trong pool chuyển một lượng coin (`extra_amount`) sang ví người dùng trước khi nạp giao dịch chính vào mempool.
* **Đặc tính kỹ thuật cốt lõi**:
  1. **Non-blocking giữa các ví**: Nhiều ví bảo trợ chạy song song độc lập. Ví nào rảnh sẽ bốc việc xử lý ngay.
  2. **Quản lý Nonce tuần tự**: Mỗi ví bảo trợ tự quản lý nonce và chờ giao dịch nạp tiền được đóng block rồi mới nhận việc tiếp theo, triệt tiêu hoàn toàn lỗi xung đột nonce.
  3. **Khử trùng lặp tại cùng một thời điểm (In-flight Deduplication)**: Khi một tài khoản gửi liên tiếp 10 giao dịch cùng lúc, hệ thống chỉ kích hoạt đúng 1 lần nạp tiền duy nhất.
  4. **Tự động chuyển tiếp (Failover Re-queue)**: Khi một ví bảo trợ cạn tiền, hệ thống tự động đẩy việc sang ví khác còn tiền. Nếu toàn bộ ví cạn tiền, hệ thống báo lỗi chi tiết địa chỉ ví và số dư để nạp thêm.
  5. **Đồng bộ cụm Raft (Realtime Cluster Sync)**: Thêm/xóa ví hoặc bật/tắt trên 1 node bất kỳ sẽ tự động phát sóng đồng bộ tức thì sang tất cả các node còn lại trong cụm Raft.
  6. **Bảo mật đa Admin (Multi-Admin Authentication - Fail-Closed)**: Toàn bộ các thao tác thêm ví, xóa ví, xem danh sách ví, bật/tắt hoặc cập nhật cấu hình đều bắt buộc có chữ ký số ECDSA (EIP-191) của ví Admin hoặc mật khẩu quản trị `admin_password`. Nếu node chưa cấu hình Admin thì từ chối (Fail-Closed).
  7. **Tự phục hồi & Kéo bù cấu hình khi node sống lại (Startup Catch-up Sync & Atomic Persistence)**:
     - Ghi file đĩa nguyên tử (Atomic Write qua Temp File + `os.Rename`) chống hỏng file JSON khi sập nguồn hoặc crash đột ngột.
     - Khi node chết sống lại, ngoài việc nạp file cục bộ, node tự động gọi `FetchHelpPayClusterState()` (HTTP GET `/raft/v1/admin/helppay_sync`) sang các peer đang sống với cơ chế retry backoff để kéo bù mọi ví và cấu hình đã thay đổi trong lúc nó offline.

### 1.3. Chính sách Private Chain & Quyết định Nghiệp vụ (Đã được Leader phê duyệt)
> [!NOTE]
> **Quy định đối với mạng Private Chain nội bộ**:
> * Hệ thống consensus-chain này được thiết kế và triển khai phục vụ **mạng riêng tư (Private Chain / Consortium)** trong hệ sinh thái nội bộ, không mở công khai ra Internet công cộng cho người lạ tương tác.
> * **Cơ chế nạp tự động cho mọi ví mới**: Nhằm mục tiêu tối ưu hóa trải nghiệm người dùng nội bộ và tự động kích hoạt ví cho các dApp/microservices của đối tác mà không cần thủ tục cấp phát faucet thủ công, **bất kỳ địa chỉ mới nào gửi giao dịch hợp lệ đều được tài trợ 0.1 ETH**.
> * **Quyết định nghiệp vụ**: Thiết kế này đã được **Leader phê duyệt chính thức**. Các AI Agent và kỹ sư phát triển tiếp theo cần ghi nhớ thiết kế này là có chủ đích và không được tự ý chặn hoặc áp đặt các rào cản faucet làm gián đoạn trải nghiệm của mạng nội bộ.

---

## 2. Các Thành Phần Chính Trong Hệ Thống

```
+---------------------------------------------------------------------------------------------------+
|                                      CLIENT / DAPP / OPERATOR                                     |
+---------------------------------------------------------------------------------------------------+
             | (1. Gửi Raw Tx)                                      | (Quản trị Admin RPC)
             v                                                      v
+-----------------------------+                           +-----------------------------+
|    CỔNG TIẾP NHẬN RAW TX    |                           |     JSON-RPC / ADMIN API    |
| - TCP: transaction_processor|                           | - helppay_toggle / setConfig|
| - RPC: rpc_transaction.go   |                           | - helppay_add/removeWallet  |
+-----------------------------+                           +-----------------------------+
             |                                                           |
             | Gọi: CheckAndTopUp(fromAddr)                              | Ký Admin / Xác thực
             v                                                           v
+---------------------------------------------------------------------------------------------------+
|                                      HELP-PAY MANAGER (Singleton)                                 |
|                                                                                                   |
|  1. Kiểm tra IsEnabled() và IsSponsorWallet(fromAddr)                                             |
|  2. Kiểm tra Số dư on-chain: GetBalance(fromAddr) < minBalance                                    |
|  3. Khử trùng lặp theo người nhận: inflightMu.LoadOrStore(recipient)                              |
|  4. Đẩy tác vụ vào hàng đợi phân phối: sponsorJobQueue chan *SponsorGasTask                       |
+---------------------------------------------------------------------------------------------------+
                                            |
                         +------------------+------------------+
                         |                                     |
                         v                                     v
         +-------------------------------+     +-------------------------------+
         |    WORKER VÍ BẢO TRỢ 1        |     |    WORKER VÍ BẢO TRỢ 2 (N...) |
         |  - Lắng nghe sponsorJobQueue  |     |  - Lắng nghe sponsorJobQueue  |
         |  - Nonce độc lập              |     |  - Nonce độc lập              |
         |  - Chờ Block Confirmation     |     |  - Chờ Block Confirmation     |
         +-------------------------------+     +-------------------------------+
                         |                                     |
                         +------------------+------------------+
                                            |
                                            v (Gửi tx chuyển tiền nạp gas)
+---------------------------------------------------------------------------------------------------+
|                                         MEMPOOL & CONSENSUS                                       |
| - Transaction Pool (Mempool) -> Đóng Block -> Cập nhật Account State DB                           |
+---------------------------------------------------------------------------------------------------+
                                            |
                                            v (Broadcast đồng bộ khi cấu hình thay đổi)
+---------------------------------------------------------------------------------------------------+
|                                   KÊNH ĐỒNG BỘ NỘI BỘ RAFT (HMAC)                                 |
| - Node A (BroadcastHelpPaySync) === HTTP POST (HMAC) ===> Node B, Node C, Node D (ApplySync)      |
+---------------------------------------------------------------------------------------------------+
```

### Chi tiết các file mã nguồn:
| Thành phần | Vị trí file | Trách nhiệm |
| :--- | :--- | :--- |
| **Data Types** | [types.go](file:///home/abc/nhat/consensus-chain/metanode/execution/pkg/helppay/types.go) | Định nghĩa `SponsorGasTask`, `WalletWorker`, `SyncPayload`, `PersistedConfig` |
| **Core Manager** | [manager.go](file:///home/abc/nhat/consensus-chain/metanode/execution/pkg/helppay/manager.go) | Quản lý pool ví, worker goroutines, hàng đợi `sponsorJobQueue`, deduplication, kiểm tra chữ ký admin, lưu đĩa |
| **TCP Ingress** | [transaction_processor.go](file:///home/abc/nhat/consensus-chain/metanode/execution/cmd/simple_chain/processor/transaction_processor.go) | Kiểm tra và gọi `CheckAndTopUp` trước khi nhận giao dịch qua TCP |
| **RPC Ingress** | [rpc_transaction.go](file:///home/abc/nhat/consensus-chain/metanode/execution/cmd/simple_chain/rpc_transaction.go) | Kiểm tra và gọi `CheckAndTopUp` trước khi nhận giao dịch qua `eth_sendRawTransaction` |
| **Public/Admin RPC** | [helppay_api.go](file:///home/abc/nhat/consensus-chain/metanode/execution/cmd/simple_chain/helppay_api.go) & [admin_api.go](file:///home/abc/nhat/consensus-chain/metanode/execution/cmd/simple_chain/admin_api.go) | Cung cấp các hàm RPC `helppay_*` và `admin_*` cho dApp/operator |
| **Cluster Sync** | [admin.go](file:///home/abc/nhat/consensus-chain/metanode/execution/pkg/rollup/raftfeed/admin.go) | Kênh HTTP nội bộ `/raft/v1/admin/helppay_sync` có xác thực HMAC giữa các node |
| **Configuration** | [config.go](file:///home/abc/nhat/consensus-chain/metanode/execution/pkg/config/config.go) | Định nghĩa cấu hình `HelpPayConfig`, `AdminAddresses` trong node |
| **Management Tool**| [manage_help_pay](file:///home/abc/nhat/consensus-chain/metanode-suite/private-chain/manage_help_pay) | CLI script hỗ trợ quản lý pool ví, bật/tắt qua chữ ký và test tự động |
| **Ansible Deployment**| [inventory.yml](file:///home/abc/nhat/consensus-chain/metanode/deploy/ansible_clusters/inventory.yml) & [exec_config.json.j2](file:///home/abc/nhat/consensus-chain/metanode/deploy/ansible_clusters/roles/exec_cluster/templates/exec_config.json.j2) | Tự động sinh cấu hình `help_pay` và `admin_addresses` cho toàn bộ cụm node khi triển khai |

---

## 3. Luồng Hoạt Động Chi Tiết (Step-by-Step Execution Flow)

### 3.1. Luồng Người Dùng Gửi Giao Dịch & Tự Động Nạp Tiền

1. **Tiếp nhận giao dịch**:
   * Người dùng gửi raw tx qua TCP hoặc JSON-RPC HTTP.
   * Node giải mã giao dịch, lấy được địa chỉ người gửi: `fromAddr := tx.FromAddress()`.
2. **Kiểm tra điều kiện (Pre-check)**:
   * Nếu `m.IsEnabled() == false` (tính năng bị tắt hoặc chưa có ví nào trong pool) -> Bỏ qua, xử lý tx bình thường.
   * Nếu `fromAddr` chính là một trong các ví bảo trợ (`IsSponsorWallet`) -> Bỏ qua (tránh đệ quy vô tận).
   * Node đọc số dư on-chain hiện tại: `balance := m.GetBalance(fromAddr)`.
   * Nếu `balance >= minBalance` (người dùng đã đủ tiền) -> Bỏ qua, tiếp tục nạp tx vào mempool.
3. **Khử trùng lặp (In-flight Deduplication)**:
   * Nếu `balance < minBalance`:
   * Hệ thống khóa mutex riêng cho địa chỉ `fromAddr` thông qua `sync.Map` (`m.inflightMu`).
   * Các giao dịch tiếp theo của cùng `fromAddr` gửi đến trong lúc giao dịch nạp tiền đang chạy sẽ bị chặn chờ mutex, sau đó tái kiểm tra số dư và đi qua mà không bị nạp tiền lần 2.
4. **Phân phối công việc vào hàng đợi (`sponsorJobQueue`)**:
   * Tạo tác vụ `task := &SponsorGasTask{ Recipient: fromAddr, Amount: m.ExtraAmount(), ResultCh: make(chan error, 1) }`.
   * Đẩy `task` vào `m.sponsorJobQueue`.
5. **Worker thực thi chuyển tiền**:
   * Worker goroutine của ví bảo trợ đang rảnh bốc `task` ra.
   * Đọc số dư ví bảo trợ: Nếu số dư < `Amount`, worker thực hiện quy trình **Failover Re-queue** (xem mục 3.2).
   * Lấy nonce tiếp theo của ví bảo trợ từ Account State DB: `nonce := as.Nonce()`.
   * Ký giao dịch Ethereum chuyển `Amount` native coin từ ví bảo trợ sang `Recipient` bằng private key nội bộ của worker.
   * Nạp giao dịch chuyển tiền vào node mempool (`m.txPoolAdder.AddTransactionToPool`).
6. **Chờ xác nhận đóng Block (Block Commitment)**:
   * Worker chờ cho đến khi hash của giao dịch nạp tiền được đóng thành công vào một block (`blockNum <= storage.GetLastBlockNumber()`).
   * Tăng biến đếm `totalSent` và cập nhật `lastUsed`.
   * Báo thành công qua `task.ResultCh <- nil`.
7. **Tiếp tục xử lý giao dịch gốc của người dùng**:
   * Sau khi nhận được tín hiệu thành công từ worker, số dư của `fromAddr` đã đủ tiền trả gas.
   * Giao dịch gốc của người dùng được nạp thẳng vào mempool và đưa vào khối tiếp theo mà không bị lỗi thiếu gas.

---

### 3.2. Luồng Xử Lý Khi Ví Bảo Trợ Thiếu Tiền (Failover & Re-queue)

Khi worker `w` nhận tác vụ nhưng số dư của ví `w.address` không đủ:
1. **Kiểm tra số lần thử (`task.attempts`)**:
   * Nếu số lần thử < tổng số ví đang có trong pool:
     * Tăng `task.attempts++`.
     * Ghi log cảnh báo `logger.Warn("⚠️ [HelpPay] Sponsor wallet %s balance low, re-queueing to another wallet")`.
     * Đẩy ngược `task` vào `m.sponsorJobQueue` để worker của ví khác còn tiền bốc ra xử lý.
2. **Khi tất cả ví trong pool đều cạn tiền**:
   * Nếu đã thử qua tất cả các ví mà không ví nào đủ tiền:
   * Node tự động thu thập danh sách toàn bộ các ví bảo trợ kèm số dư chi tiết của từng ví.
   * Trả về lỗi định dạng chuẩn ra client:
     ```text
     all sponsor wallets have insufficient balance: [0xWalletA... (balance: 1000 wei), 0xWalletB... (balance: 0 wei)], needed 50000000000000000 wei for top-up
     ```
   * Cổng TCP/RPC trả mã lỗi `-32603` chứa danh sách các ví này để quản trị viên biết chính xác ví nào cần nạp thêm tiền.

---

### 3.3. Luồng Đồng Bộ Cụm Raft (Cluster Synchronization)

Khi cụm blockchain chạy nhiều node (ví dụ: Node 0, Node 1, Node 2...):

1. **Gọi thay đổi trên 1 Node bất kỳ**:
   * Admin gọi RPC `helppay_addWallet`, `helppay_removeWallet`, hoặc `helppay_toggle` trên **Node 0**.
2. **Cập nhật Local & Ghi đĩa**:
   * Node 0 cập nhật cấu hình trong bộ nhớ RAM.
   * Node 0 ghi đè vào file cục bộ `<RootPath>/helppay_wallets.json`.
3. **Phát sóng Broadcast qua kênh nội bộ Raft**:
   * Node 0 đóng gói payload JSON dạng `SyncPayload`:
     ```json
     {
       "action": "add_wallet",
       "private_key": "0x...",
       "address": "0x...",
       "sender_node_id": "node0"
     }
     ```
   * Node 0 gửi HTTP POST có chữ ký HMAC bí mật tới endpoint nội bộ `/raft/v1/admin/helppay_sync` của Node 1 và Node 2.
4. **Áp dụng đồng bộ trên các Node Replicas**:
   * Node 1 và Node 2 nhận request, xác thực chữ ký HMAC thành công.
   * Bộ lọc `ApplySync` kiểm tra `action` và tự động thêm/xóa ví hoặc bật/tắt tương ứng, sau đó lưu xuống file `helppay_wallets.json` của từng node.
   * Cả cụm Raft đạt trạng thái đồng nhất 100% trong vòng vài mili-giây.

---

### 3.4. Cơ Chế Xác Thực Ví Admin (Admin Authentication - Fail-Closed)

Để đảm bảo an toàn tuyệt đối cho hệ thống và quỹ bảo trợ:
1. **Cấu hình ví Admin trong `config.json` (Ví dụ minh họa)**:
   ```json
   "help_pay": {
     "enabled": true,
     "admin_addresses": [
       "0x0000000000000000000000000000000000000000"
     ]
   }
   ```
2. **Nguyên tắc Fail-Closed**:
   * Nếu node **chưa cấu hình ví Admin** trong `admin_addresses`, toàn bộ các lệnh thay đổi cấu hình, thêm ví, xóa ví hoặc truy vấn danh sách ví qua public RPC `helppay_*` sẽ **bị từ chối ngay lập tức** (`operation refused (fail-closed)`).
3. **Ký số EIP-191 & Chống Replay Attack**:
   * Mọi hàm quản trị trên `helppay_*` đều nhận cặp tham số: `(adminAddress, signature, timestamp, ...)`.
   * Định dạng message cần ký (toàn bộ tham số hành động đều được bind chặt vào chuỗi ký):
     - `helppay:add_wallet:<targetAddress>:<timestamp>` (bind địa chỉ ví cần thêm để chống giả mạo key)
     - `helppay:remove_wallet:<targetAddress>:<timestamp>`
     - `helppay:get_wallets:<timestamp>`
     - `helppay:toggle:<enabled>:<timestamp>`
     - `helppay:set_config:<enabled>:<minBal>:<extraAmt>:<timestamp>`
   * Client lấy Unix timestamp hiện tại và dùng Private Key của ví Admin ký thông điệp theo chuẩn Ethereum personal_sign.
4. **Node xác minh**:
   * Kiểm tra độ lệch thời gian: `|now - timestamp| <= 120 giây` (nếu lệch quá 2 phút thì từ chối).
   * **Chống Replay (One-Time Execution)**: Lưu vết hash của chữ ký vào cache bộ nhớ (thời gian sống 2 phút); từ chối ngay lập tức nếu một chữ ký bị tái sử dụng (`signature already consumed`).
   * Dùng hàm `crypto.SigToPub` phục hồi địa chỉ người ký từ chữ ký và thông điệp.
   * Kiểm tra địa chỉ phục hồi có nằm trong danh sách `admin_addresses` hay không.
   * **Bảo vệ chống trùng lặp đồng thời (`inflightMu`)**: Thiết kế chuyên biệt cho App-Chain tối ưu trải nghiệm Zero-Gas Onboarding. Cơ chế khóa deduplication per-recipient (`inflightMu`) đảm bảo nếu client gửi nhiều transaction đồng thời thì chỉ có duy nhất 1 lệnh nạp 0.1 ETH được thực thi, tự động dừng khi số dư ví đã đạt ngưỡng `>= min_balance`.
5. **Kênh Admin nội bộ (`admin_*`)**:
   * Dành cho Operator thông qua CLI/Curl cục bộ có xác thực `admin_password` (Constant-Time Compare) và chặn Origin từ trình duyệt (chống CSWSH/CSRF).

---

## 4. Cấu Trúc File Lưu Trữ Động (`helppay_wallets.json`)

* **Vị trí**: Nằm trong thư mục dữ liệu database của node:
  `<Databases.RootPath>/helppay_wallets.json`
* **Quyền hạn file**: `0600` (`-rw-------`), chỉ duy nhất tiến trình node được phép đọc/ghi.
* **Cơ chế Ghi nguyên tử (Atomic Write)**:
  - Khi lưu cấu hình, node ghi toàn bộ nội dung JSON ra một file tạm ẩn:
    `<Databases.RootPath>/.helppay_wallets.json.tmp.<nanoseconds>`
  - Sau khi ghi hoàn tất, node thực hiện gọi `os.Rename()` ghi đè nguyên tử lên file chính.
  - Cơ chế này đảm bảo file JSON không bao giờ bị rỗng, dở dang hay hỏng cú pháp (corrupt) kể cả khi tiến trình bị tắt đột ngột (`kill -9`) hoặc máy chủ mất nguồn điện.
* **Cơ chế Kéo bù khi khởi động (Startup Catch-up Sync)**:
  - Khi một node bị gián đoạn (chết/offline), các node khác trong cụm vẫn tiếp tục hoạt động và có thể nhận thêm ví mới qua RPC/CLI.
  - Khi node offline khởi động lại, hàm `Start()` của `Manager` sẽ khởi tạo worker từ file đĩa cục bộ, đồng thời khởi chạy tiến trình nền `startStartupCatchUp()` gửi yêu cầu HTTP GET tới `/raft/v1/admin/helppay_sync` của các peer đang hoạt động trong cụm.
  - Node tự động gộp (merge) danh sách ví mới, đồng bộ trạng thái `enabled`, `min_balance`, `extra_amount`, kích hoạt worker mới và ghi đè nguyên tử lại file `helppay_wallets.json` trên đĩa.
* **Cấu trúc dữ liệu minh họa (Example JSON)**:
  > *Lưu ý: Các ví và khóa dưới đây chỉ là dữ liệu ví dụ minh họa.*
  ```json
  {
    "enabled": true,
    "min_balance": "10000000000000000",
    "extra_amount": "100000000000000000",
    "wallets": [
      {
        "address": "0x0000000000000000000000000000000000000001",
        "private_key": "0000000000000000000000000000000000000000000000000000000000000001"
      },
      {
        "address": "0x0000000000000000000000000000000000000002",
        "private_key": "0000000000000000000000000000000000000000000000000000000000000002"
      }
    ]
  }
  ```

---

## 5. Hướng Dẫn Sử Dụng Tool Quản Lý `manage_help_pay`

Thư mục công cụ: `metanode-suite/private-chain/manage_help_pay/`

```bash
# 1. Xem danh sách ví trong pool, số dư, trạng thái và thống kê
go run main.go list

# 2. Bật tính năng chuyển tiền hộ (dùng ví Admin ký chữ ký EIP-191)
go run main.go toggle on
# hoặc:
go run main.go enable

# 3. Tắt tính năng chuyển tiền hộ
go run main.go toggle off
# hoặc:
go run main.go disable

# 4. Thêm ví chuyển hộ mới (truyền private key để node tự động ký chuyển tiền)
go run main.go add <private_key_hex>

# 5. Xóa ví chuyển hộ khỏi pool
go run main.go remove <wallet_address>

# 6. Xem cấu hình hiện tại (ngưỡng nạp, ví admin)
go run main.go config

# 7. Cập nhật cấu hình ngưỡng nạp (min_balance) và số tiền nạp (extra_amount)
go run main.go set-config true 10000000000000000 100000000000000000

# 8. Chạy test tự động quy trình nạp tiền hộ (End-to-End Test)
go run main.go test-topup
```

---

## 6. Danh Mục RPC API Hỗ Trợ

| Namespace & Phương thức | Tham số | Ý nghĩa |
| :--- | :--- | :--- |
| `helppay_toggle` | `adminAddr, sig, timestamp, enabled` | Bật/tắt chuyển tiền hộ (bắt buộc chữ ký Admin EIP-191) |
| `helppay_setEnabled` | `adminAddr, sig, timestamp, enabled` | Alias của `helppay_toggle` |
| `helppay_getAdmins` | Không có | Lấy danh sách ví Admin được phân quyền |
| `helppay_getWallets` | `adminAddr, sig, timestamp` | Lấy danh sách các ví trong pool (bắt buộc chữ ký Admin) |
| `helppay_addWallet` | `adminAddr, sig, timestamp, privateKeyHex` | Thêm ví bảo trợ mới và đồng bộ Raft (bắt buộc chữ ký Admin) |
| `helppay_removeWallet` | `adminAddr, sig, timestamp, addressHex` | Xóa ví bảo trợ khỏi pool và đồng bộ Raft (bắt buộc chữ ký Admin) |
| `helppay_getConfig` | Không có | Lấy cấu hình `HelpPayConfig` hiện tại (metadata công khai) |
| `helppay_setConfig` | `adminAddr, sig, timestamp, enabled, minBal, extraAmt` | Cập nhật cấu hình có xác thực chữ ký Admin |
| `helppay_setConfigWithAdmin`| `adminAddr, sig, timestamp, enabled, minBal, extraAmt` | Cập nhật cấu hình có xác thực chữ ký Admin |

> [!NOTE]
> Các phương thức `admin_*` dùng mật khẩu text thuần cũ đã được loại bỏ hoàn toàn để chuẩn hóa 100% sang chuẩn chữ ký mật mã ECDSA (EIP-191) qua namespace `helppay_*`.

---

## 7. Các Tối Ưu An Toàn & Quyết Định Kiến Trúc Đã Được Phê Duyệt

Các AI Agent và kỹ sư phát triển tiếp theo cần nắm rõ các quyết định này để tránh lặp lại các vấn đề đã được giải quyết:

### 7.1. Bảo Vệ Ví Có Đủ Tiền (Funded Wallet Fast-Path - Zero Overhead)
- **Vấn đề**: Ví có số dư `>= min_balance` tự trả gas, không cần bảo trợ. Nếu bắt tất cả ví phải đi qua hàng đợi concurrency hoặc goroutine của HelpPay sẽ làm nghẽn giao dịch của người dùng thật.
- **Giải pháp**: Kiểm tra `NeedsTopUp(fromAddr)` ngay đầu cổng vào (TCP & RPC). Nếu ví đủ tiền, trả về ngay lập tức (`0ms`, không spawn goroutine, không giữ slot concurrency).

### 7.2. Tầng Tiền Kiểm Tra Rẻ Tiền (Cheap Pre-Check Trước Khi Nạp Tiền)
Trước khi phân bổ slot ví sponsor và spawn goroutine nạp gas, hệ thống thực hiện các bước pre-check không tốn tài nguyên:
1. **Gas Limit hợp lệ**: `21.000 <= tx.Gas() <= 30.000.000`.
2. **Chống Rút Cạn Sponsor (Anti-Drain Tx Amount)**: HelpPay **chỉ tài trợ gas** (`extra_amount`), **không tài trợ giá trị chuyển khoản** (`tx.Amount()`). Nếu ví không có tiền mà gửi giao dịch chuyển `tx.Amount() > 0` (với `balance + extra_amount < tx.Amount()`), giao dịch bị từ chối ngay lập tức mà không mất 1 wei gas tài trợ.
3. **Mempool Nonce Bound**: `stateNonce <= tx.Nonce() <= stateNonce + 5`. Cho phép người dùng gửi chuỗi 5 giao dịch liên tiếp vào mempool mà không bị từ chối oan, đồng thời chặn triệt để tấn công flood nonce vô lý.

### 7.3. Giới Hạn Tải Động & Cảnh Báo Telegram (Dynamic Concurrency Limiter)
- **Giới hạn slot**: `MaxInflight = ActiveWalletCount * 20` (chia sẻ chung cho cả cổng RPC và TCP qua `helppay.Manager`).
- **Xử lý quá tải**: Khi đạt giới hạn 20 slot/ví sponsor, yêu cầu mới bị từ chối an toàn với mã lỗi rõ ràng và tự động bắn cảnh báo Telegram (có cooldown 60s để chống spam cảnh báo).

### 7.4. Chống Tấn Công Memory Bloat & Brute-Force Admin API
- Trong `helppay_api.go`, chữ ký Admin `VerifyAdminSignature` được kiểm tra tính hợp lệ **TRƯỚC** khi lưu vào cache chống replay `usedSignatures`. Kẻ tấn công gửi chữ ký rác sẽ bị từ chối ngay ở bước verify mà không thể làm phình bộ nhớ RAM.
- **Rate limiting brute-force**: Nếu 1 IP gửi sai chữ ký quá 5 lần trong 60 giây, IP đó bị tạm khóa 5 phút và gửi cảnh báo khẩn qua Telegram.

### 7.5. Cấu Hình Telegram Chuẩn Hóa Qua Biến Môi Trường (Không Quét Fallback)
- Xóa bỏ cơ chế quét fallback nhiều thư mục để tránh đọc nhầm file rác / file cũ.
- Chỉ đọc từ biến môi trường chuẩn: `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, `TELEGRAM_THREAD_ID`.
- Ansible (`security.env.j2`) tự động trích xuất cấu hình Telegram từ `inventory.yml` trên máy deploy và render vào `/opt/metanode/node-<id>/config/security.env`. Systemd tự nạp vào tiến trình khi khởi động.
- **Lọc Log Level**: Chỉ các cảnh báo quan trọng (`logger.Telegram` và `logger.Fatal`) mới được gửi về Telegram theo mặc định, tránh làm loãng kênh chat bởi các log lỗi mạng/client thông thường.

