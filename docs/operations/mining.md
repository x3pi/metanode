# Hướng dẫn Mining

Tài liệu này hướng dẫn cách cấu hình và sử dụng các API liên quan đến mining.

## 1. Cấu hình ban đầu

Để chạy trên một sub node với chức năng mining, bạn cần thêm các tham số sau vào file `config.json`:

```json
{
  "mining_db_path":"./sample/simple/data-write-2/other/mining",
  "is_mining": true,
  "client_rpc_url": "http://localhost:8545",  // là link client rpc
  "reward_sender_private_key": "cee4c644f964bb3ce7a322db844c50708745e1941990574d358af282c25144fc", // private secp ví trả thưởng 
  "reward_sender_address": "0x5AE1e723973577AcaB776ebC4be66231fc57b370", //Địa chỉ ví trả thưởng
}
```


***Lưu ý: nối chạy mining cần 1 rpc client để chuyển tiền thưởng nên cần chạy sau rpc client. Ví trả thưởng cần có tiền và có thể chuyển được qua rpc client***

## 2. Cấu trúc lệnh curl cơ bản

Mọi yêu cầu đến API Mining đều có cấu trúc chung như sau:

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "METHOD_NAME",
    "params": [PARAM1, PARAM2, ...],
    "id": 1
}'
```

**Trong đó:**

- `METHOD_NAME`: Tên phương thức API bạn muốn gọi (ví dụ: `mtn_getJob`, `mtn_completeJob`).
- `PARAMS`: Các tham số đầu vào cho phương thức.
- `id`: Một ID yêu cầu duy nhất.

## 3. Các ví dụ về truy vấn Mining API

Dưới đây là các lệnh `curl` hoàn chỉnh cho từng phương thức của API Mining.

### 3.1. Lấy thông tin Job (`mtn_getJob`)

Phương thức này cho phép một địa chỉ miner yêu cầu một job mới hoặc lấy thông tin job hiện có.

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_getJob",
    "params": ["0xYOUR_MINER_ADDRESS"],
    "id": 1
}'
```

**Tham số:**

- `0xYOUR_MINER_ADDRESS`: Địa chỉ ví của miner yêu cầu job.

**Phản hồi ví dụ:**

```json
{
    "jsonrpc": "2.0",
    "result": {
        "JobID": "some_unique_job_id",
        "Assignee": "0xYOUR_MINER_ADDRESS",
        "JobType": "validate_block", // Hoặc "video_ads"
        "Data": "0xABCDEF...", // Dữ liệu của job, ví dụ: block number cho validate_block
        "Status": "new",
        "CreatedAt": "2023-10-27T10:00:00Z"
    },
    "id": 1
}
```

### 3.2. Hoàn thành Job (`mtn_completeJob`)

Phương thức này được sử dụng để thông báo rằng một job đã được hoàn thành, đồng thời cung cấp bằng chứng hoàn thành (nếu có).

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_completeJob",
    "params": ["0xYOUR_MINER_ADDRESS", "YOUR_JOB_ID", "0xHASH_EXECUTE_SC_RESULTS"],
    "id": 1
}'
```

nếu là ads vide job thì tham số hash truyền như nào cũng được không được xử lý và hoàn thành job luôn

còn job loại `validate_block` thì cần tìm hash của mảng kết quả evm trả về  gửi lên. Ví dụ cho miner client chạy qua 1 storage online nằm ở `cmd/client/miner`

**Tham số:**

- `0xYOUR_MINER_ADDRESS`: Địa chỉ ví của miner đã hoàn thành job.
- `YOUR_JOB_ID`: ID của job được lấy từ `mtn_getJob`.
- `0xHASH_EXECUTE_SC_RESULTS`: (Chỉ áp dụng cho `JobType: validate_block`) Hash của kết quả thực thi smart contract của block được yêu cầu xác thực. Với `JobType: video_ads`, tham số này có thể là `null` hoặc một giá trị mặc định (ví dụ: `0x000...`).

**Phản hồi ví dụ:**

```json
{
    "jsonrpc": "2.0",
    "result": true,
    "id": 1
}
```

Hoặc lỗi nếu job không hợp lệ hoặc hash không khớp.

### 3.3. Lấy lịch sử giao dịch theo địa chỉ (`mtn_getTransactionHistoryByAddress`)

Phương thức này cho phép bạn truy vấn lịch sử các giao dịch (bao gồm cả giao dịch phần thưởng mining) liên quan đến một địa chỉ cụ thể.

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_getTransactionHistoryByAddress",
    "params": ["0xTARGET_ADDRESS", 0, 10],
    "id": 1
}'
```

**Tham số:**

- `0xTARGET_ADDRESS`: Địa chỉ ví bạn muốn xem lịch sử giao dịch.
- `OFFSET`: Vị trí bắt đầu lấy kết quả (dùng để phân trang, ví dụ: 0 là bắt đầu từ kết quả đầu tiên).
- `LIMIT`: Số lượng kết quả tối đa muốn nhận.

**Phản hồi ví dụ:**

```json
{
    "jsonrpc": "2.0",
    "result": {
        "total": 50,
        "records": [
            {
                "txID": "0xabc123...",
                "blockNumber": 12345,
                "sender": "0x...",
                "recipient": "0x...",
                "amount": "1000000000000000000", // Ví dụ: 1 IQN
                "txType": "reward", // Hoặc "transfer", "call_contract", v.v.
                "timestamp": "2023-10-27T10:05:00Z",
                "status": "success"
            },
            // ... các giao dịch khác
        ]
    },
    "id": 1
}
```

### 3.4. Lấy lịch sử giao dịch theo Job ID (`mtn_getTransactionHistoryByJobID`)

Phương thức này cho phép bạn truy vấn thông tin chi tiết về giao dịch phần thưởng được tạo ra từ việc hoàn thành một job mining cụ thể.

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_getTransactionHistoryByJobID",
    "params": ["YOUR_JOB_ID"],
    "id": 1
}'
```

**Tham số:**

- `YOUR_JOB_ID`: ID của job bạn muốn xem lịch sử giao dịch phần thưởng.

**Phản hồi ví dụ:**

```json
{
    "jsonrpc": "2.0",
    "result": {
        "record": {
            "txID": "0xdef456...",
            "blockNumber": 12346,
            "sender": "0x0000000000000000000000000000000000000000", // Thường là địa chỉ 0x0 cho reward
            "recipient": "0xYOUR_MINER_ADDRESS",
            "amount": "5000000000000000000", // Ví dụ: 5 IQN
            "txType": "reward",
            "timestamp": "2023-10-27T10:10:00Z",
            "status": "success",
            "jobID": "YOUR_JOB_ID"
        }
    },
    "id": 1
}
```

```


# Hỗ trợ call tool qua các command

Phần này mô tả các command được sử dụng để tương tác với API Mining thông qua call tool.

**Các command:**

*   `SetCompleteJob`: Command này được sử dụng để thiết lập thông tin về một job đã hoàn thành.
*   `CompleteJob`: Command này được sử dụng để thông báo rằng một job đã được hoàn thành và gửi kết quả lên server.
*   `GetTxRewardHistoryByAddress`: Command này được sử dụng để truy vấn lịch sử giao dịch phần thưởng theo địa chỉ ví.
*   `TxRewardHistoryByAddress`: Command này trả về lịch sử giao dịch phần thưởng theo địa chỉ ví.
*   `GetTxRewardHistoryByJobID`: Command này được sử dụng để truy vấn thông tin chi tiết về giao dịch phần thưởng được tạo ra từ việc hoàn thành một job mining cụ thể.
*   `TxRewardHistoryByJobID`: Command này trả về thông tin chi tiết về giao dịch phần thưởng theo Job ID.



proto Request và Response  nở file:

/home/pi/Videos/Code/mtn-simple-jul2/pkg/proto/mining.proto


```proto
// pkg/proto/mining.proto
syntax = "proto3";

package mining;
option go_package = "/proto";

// Common types
message Hash {
  bytes value = 1; // 32 bytes
}

message Address {
  bytes value = 1; // 20 bytes
}

// Request/Response for GetJob
message GetJobRequest {
  Address address = 1;
}

message Job {
  string job_id = 1;
  string creator = 2;
  string assignee = 3;
  string job_type = 4;
  string status = 5;
  string data = 6;
  double reward = 7;
  int64 created_at = 8;
  int64 completed_at = 9;
  string tx_hash = 10;
}

message GetJobResponse {
  Job job = 1;
}

// Request/Response for CompleteJob
message CompleteJobRequest {
  Address address = 1;
  string job_id = 2;
  Hash hash_execute = 3;
}

message CompleteJobResponse {
  bool success = 1;
}

// Request/Response for GetTransactionHistoryByAddress
message GetTransactionHistoryByAddressRequest {
  Address address = 1; // Yêu cầu địa chỉ là một message Address
  int32 offset = 2;
  int32 limit = 3;
}

message TransactionRecord {
  string tx_id = 1;
  string job_id = 2;
  string sender = 3;
  string recipient = 4;
  double amount = 5;
  int64 timestamp = 6;
  string status = 7;
}

message GetTransactionHistoryByAddressResponse {
  uint64 total = 1;
  repeated TransactionRecord records = 2;
}

// Request/Response for GetTransactionHistoryByJobID
message GetTransactionHistoryByJobIDRequest {
  string job_id = 1;
}

message GetTransactionHistoryByJobIDResponse {
  TransactionRecord record = 1;
}
```



