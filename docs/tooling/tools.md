# 📦 Tài liệu hướng dẫn sử dụng các Tool

> Danh sách đầy đủ các tool trong `cmd/tool/`, kèm hướng dẫn sử dụng và đánh giá mức độ cần thiết.

---

## Mục lục

| # | Tool | Mức độ | Mô tả ngắn |
|---|------|--------|-------------|
| 1 | [register_validator](#1-register_validator) | ✅ **Cần thiết** | Đăng ký validator mới + delegate stake |
| 2 | [deregister_validator](#2-deregister_validator) | ✅ **Cần thiết** | Hủy đăng ký validator |
| 3 | [query_validators](#3-query_validators) | ✅ **Cần thiết** | Truy vấn thông tin validators |
| 4 | [block_hash_checker](#4-block_hash_checker) | ✅ **Cần thiết** | So sánh block hash giữa các node |
| 5 | [watch_block](#5-watch_block) | ✅ **Cần thiết** | Giám sát block mới realtime |
| 6 | [libp2p_generate_key](#6-libp2p_generate_key) | ✅ **Cần thiết** | Tạo libp2p private key |
| 7 | [peer_id](#7-peer_id) | ✅ **Cần thiết** | Lấy Peer ID từ private key |
| 8 | [gen_bls](#8-gen_bls) | ✅ **Cần thiết** | Tạo BLS public key |
| 9 | [verify_key.go](#9-verify_keygo) | ✅ **Cần thiết** | Verify ECDSA key → address |
| 10 | [tx_sender](#10-tx_sender) | ✅ **Cần thiết** | Gửi transaction (deploy/call), hỗ trợ spam |
| 11 | [spam_rpc](#11-spam_rpc) | ⚠️ **Giữ lại** | Stress test RPC endpoint |
| 12 | [file](#12-file) | ⚠️ **Giữ lại** | Benchmark file transfer |
| 13 | [verify_abi](#13-verify_abi) | ⚠️ **Giữ lại** | Kiểm tra ABI handler |
| 14 | [add_validator_node4](#14-add_validator_node4) | 🗑️ **Bỏ được** | Trùng với register_validator |
| 15 | [register_validator_example](#15-register_validator_example) | 🗑️ **Bỏ được** | Phiên bản cũ, dùng base64 |
| 16 | [rpc_example](#16-rpc_example) | 🗑️ **Bỏ được** | Trùng với query_validators |
| 17 | [ethtx](#17-ethtx) | 🗑️ **Bỏ được** | Code test tính hash EIP-155 |
| 18 | [txType](#18-txtype) | 🗑️ **Bỏ được** | Ví dụ các loại transaction |
| 19 | [txproto](#19-txproto) | 🗑️ **Bỏ được** | Chỉ in 1 dòng log, trống |
| 20 | [test](#20-test) | 🗑️ **Bỏ được** | Test nhanh eth_chainId |
| 21 | [ants](#21-ants) | 🗑️ **Bỏ được** | CPU stress test, không liên quan |
| 22 | [hash_calculator](#22-hash_calculator) | 🗑️ **Bỏ được** | Thư mục trống |

---

## Tool cần thiết (GIỮ LẠI)

---

### 1. register_validator

**Mục đích**: Đăng ký một validator mới trên blockchain và delegate stake cho validator đó.

**Cách sử dụng**:

```bash
# Đăng ký với cấu hình mặc định (Node 4)
cd cmd/tool/register_validator
go run . 

# Đăng ký với tham số tùy chỉnh
go run . \
  --key "private_key_hex" \
  --name "my-validator" \
  --primary "/ip4/1.2.3.4/tcp/9004" \
  --worker "1.2.3.4:9004" \
  --p2p "/ip4/1.2.3.4/tcp/9004" \
  --desc "My Validator Node" \
  --commission 1000 \
  --stake "1000000000000000000000" \
  --authority-key "base64_bls_key" \
  --protocol-key "base64_ed25519_key" \
  --network-key "base64_ed25519_key" \
  --hostname "my-node"

# Chỉ delegate thêm stake (validator đã đăng ký)
go run . --delegate-only --stake "5000000000000000000000"

# Chế độ dry-run (chỉ xem calldata, không gửi)
go run . --dry-run
```

**Flags**:
| Flag | Mặc định | Mô tả |
|------|----------|-------|
| `--key` | Node 4 key | Private key (hex, không có 0x) |
| `--name` | `node-4` | Tên validator |
| `--primary` | `/ip4/127.0.0.1/tcp/9004` | Địa chỉ P2P chính |
| `--worker` | `127.0.0.1:9004` | Địa chỉ worker |
| `--p2p` | `/ip4/127.0.0.1/tcp/9004` | Địa chỉ P2P |
| `--desc` | Node 4 description | Mô tả |
| `--commission` | `1000` (10%) | Commission rate |
| `--stake` | `1000000000000000000000` | Stake amount (wei) |
| `--authority-key` | Node 4 BLS key | BLS authority key (base64) |
| `--protocol-key` | Node 4 protocol key | Ed25519 protocol key (base64) |
| `--network-key` | Node 4 network key | Ed25519 network key (base64) |
| `--hostname` | `node-4` | Tên host |
| `--dry-run` | `false` | Chỉ encode calldata |
| `--delegate-only` | `false` | Chỉ delegate stake |
| `--min-delegation` | `0` | Min self-delegation (wei) |

**Yêu cầu**: RPC endpoint tại `http://localhost:8545`

---

### 2. deregister_validator

**Mục đích**: Hủy đăng ký validator. Tự động rút stake trước khi hủy nếu cần.

**Cách sử dụng**:

```bash
cd cmd/tool/deregister_validator

# Hủy đăng ký với private key mặc định (Node 4)
go run .

# Hủy với private key khác
go run . --key "your_private_key_hex"

# Dry-run (chỉ xem, không gửi)
go run . --dry-run

# Bỏ qua bước undelegate (nếu đã rút stake trước đó)
go run . --skip-undelegate
```

**Flags**:
| Flag | Mặc định | Mô tả |
|------|----------|-------|
| `--key` | Node 4 key | Private key (hex) |
| `--dry-run` | `false` | Chỉ encode calldata |
| `--skip-undelegate` | `false` | Bỏ qua bước rút stake |

**Quy trình**:
1. Kiểm tra validator có đang đăng ký không
2. Kiểm tra stake → tự động rút hết nếu còn (`undelegate`)
3. Gọi `deregisterValidator()` trên smart contract
4. Xác nhận validator đã bị xóa khỏi danh sách

---

### 3. query_validators

**Mục đích**: Truy vấn thông tin chi tiết toàn bộ validators: address, stake, keys, delegation, rewards, balance.

**Cách sử dụng**:

```bash
cd cmd/tool/query_validators

# Truy vấn trạng thái mới nhất
go run .

# Truy vấn trạng thái tại block cụ thể
go run . --block 5000
```

**Flags**:
| Flag | Mặc định | Mô tả |
|------|----------|-------|
| `--block` | `-1` (latest) | Block number để truy vấn |

**Thông tin hiển thị**:
- Tổng số validators (`getValidatorCount`)
- Chi tiết từng validator: Name, Hostname, Stake, Keys, Commission, Owner
- Epoch info (timestamp, block number)
- Balance of account
- Delegation info (staked amount, reward debt)
- Pending rewards

**Yêu cầu**: RPC endpoint tại `http://localhost:8545`

---

### 4. block_hash_checker

**Mục đích**: So sánh block hash giữa nhiều node để phát hiện fork/lệch dữ liệu. Hỗ trợ quét 1 lần hoặc giám sát liên tục.

**Cách sử dụng**:

```bash
cd cmd/tool/block_hash_checker

# Quét 1 lần từ block 1 đến 5000
go run . --nodes "master=http://localhost:8747,node4=http://localhost:10748" \
         --from 1 --to 5000

# Quét từ block 1 đến block mới nhất
go run . --nodes "master=http://localhost:8747,node4=http://localhost:10748" \
         --from 1

# Giám sát liên tục (Watch mode)
go run . --watch \
         --nodes "master=http://localhost:8747,node4=http://localhost:10748" \
         --interval 10s --check-last 10

# Tùy chỉnh batch size và timeout
go run . --nodes "n1=http://host1:8545,n2=http://host2:8545" \
         --from 1 --to 10000 --batch 100 --timeout 10s
```

**Flags**:
| Flag | Mặc định | Mô tả |
|------|----------|-------|
| `--nodes` | *bắt buộc* | Danh sách node: `"name=url,name2=url2"` |
| `--from` | `1` | Block bắt đầu |
| `--to` | `0` (latest) | Block kết thúc |
| `--batch` | `50` | Số block kiểm tra song song mỗi batch |
| `--timeout` | `5s` | Timeout mỗi RPC call |
| `--watch` | `false` | Chế độ giám sát liên tục |
| `--interval` | `10s` | Khoảng thời gian giữa mỗi lần check (watch mode) |
| `--check-last` | `5` | Số block gần nhất cần check mỗi cycle (watch mode) |

**Output**: Kết quả CSV tại `mismatches_<from>_<to>.csv` nếu phát hiện lệch.

---

### 5. watch_block

**Mục đích**: Lắng nghe block mới theo thời gian thực qua WebSocket subscription (`newHeads`).

**Cách sử dụng**:

```bash
cd cmd/tool/watch_block
go run .
```

**Output mẫu**:
```
⏳ Đang lắng nghe block mới...
📦 Raw: {"hash":"0x...","number":"0x1234",...}
🧱 Block #4660 | Hash: 0x... | Difficulty: 0 | GasLimit: 1000000000 | Timestamp: 1738...
```

**Yêu cầu**: WebSocket endpoint tại `ws://localhost:8545`

---

### 6. libp2p_generate_key

**Mục đích**: Tạo cặp khóa Ed25519 cho libp2p, trả về private key dạng Base64 (protobuf format) dùng trong file cấu hình.

**Cách sử dụng**:

```bash
cd cmd/tool/libp2p_generate_key
go run .
```

**Output mẫu**:
```
Private Key (Base64 - Protobuf Format): CAES...
** Hãy sử dụng chuỗi này cho trường 'private_key' trong file cấu hình của bạn. **
```

---

### 7. peer_id

**Mục đích**: Lấy Peer ID từ libp2p private key (Base64), hoặc tạo cặp key mới kèm Peer ID.

**Cách sử dụng**:

```bash
cd cmd/tool/peer_id

# Lấy Peer ID từ private key có sẵn
go run main.go "BASE64_PRIVATE_KEY_STRING"

# Tạo key + Peer ID mới (chạy file generate.go)
# Lưu ý: peer_id có 2 file main, cần chọn đúng file
```

**Output mẫu**:
```
Peer ID: 12D3KooW...
```

> ⚠️ **Lưu ý**: Thư mục này có 2 file Go với 2 hàm `main()`, không thể build cùng lúc. Nên tách `generate.go` ra thành tool riêng hoặc hợp nhất thành 1 file với subcommand.

---

### 8. gen_bls

**Mục đích**: Tạo BLS public key từ private key hex. Hiện hardcode private key của Node 4.

**Cách sử dụng**:

```bash
cd cmd/tool/gen_bls
go run .
```

**Output mẫu**:
```
BLS_PUBLIC_KEY: 0x...
```

> ⚠️ **Lưu ý**: Private key hiện đang hardcode (`6c8489...`). Nên sửa để nhận private key từ argument.

---

### 9. verify_key.go

**Mục đích**: Xác minh ECDSA private key và hiển thị Ethereum address tương ứng.

**Cách sử dụng**:

```bash
cd cmd/tool
go run verify_key.go
```

**Output mẫu**:
```
Private Key: 6c8489...
Address: 0xa87c6FD018Da82a52158B0328D61BAc29b556e86
```

> ⚠️ **Lưu ý**: Private key hardcode. Nên sửa để nhận từ argument.

---

### 10. tx_sender

**Mục đích**: Gửi các giao dịch (deploy/call smart contract) trực tiếp lên mạng thông qua TCP Client. Tự động kiểm tra và đăng ký BLS key (nếu chưa có). Hỗ trợ chế độ spam loop.

**Cách sử dụng**:

```bash
cd cmd/tool/tx_sender

# Gửi 1 batch transaction (theo cấu hình data.json)
go run .

# Gửi liên tục (Spam mode)
go run . -loop

# Dùng file cấu hình và dữ liệu tùy chỉnh
go run . -config path/to/config.json -data path/to/data.json -log-level 3
```

**Cấu hình (`config.json`)**:
```json
{
    "version": "0.0.1.0",
    "private_key": "your_private_key_hex",
    "parent_connection_address": "127.0.0.1:4200", 
    "parent_address": "node_manager_address",
    "chain_id": 991,
    "parent_connection_type": "client"
}
```
*(Lưu ý: TCP Client kết nối trực tiếp vào `go-sub` port 4200 hoặc port đã cấu hình)*

**Dữ liệu (`data.json`)**: Chứa danh sách các action cần thực hiện (deploy contract, call contract).
```json
[
  {
    "name": "Deploy Token",
    "action": "deploy",
    "from_address": "0xYourAddress",
    "amount": "0",
    "input": "hex_compiled_bytecode"
  },
  {
    "name": "Call Increment",
    "action": "call",
    "from_address": "0xYourAddress",
    "address": "0", 
    "amount": "1000000",
    "input": "hex_calldata"
  }
]
```
> **Đặc biệt**: Trong action `call`, nếu `address: "0"`, tool sẽ tự động gọi vào địa chỉ contract vừa được deploy ở bước trước đó.

**Tính năng**:
1. Đọc file config và khởi tạo TCP Client.
2. Kiểm tra tài khoản trên on-chain. Nếu `nonce == 0` (chưa đăng ký BLS), sẽ tự động gọi `AddAccountForClient` kết hợp với `chain_id`.
3. Khả năng Deploy contract và Call contract với danh sách liên hoàn.
4. Lắng nghe timeout và nhận TX Receipt trực tiếp trả về từ mạng.

---

## Tool bổ trợ (GIỮ LẠI cho dev/test)

---

### 11. spam_rpc

**Mục đích**: Stress test RPC endpoint — đo throughput (TPS) và kiểm tra tính chính xác dữ liệu trả về.

**Cách sử dụng**:

```bash
cd cmd/tool/spam_rpc
go run .
```

**Cấu hình** (sửa trong code):
| Biến | Giá trị | Mô tả |
|------|---------|-------|
| `targetURL` | `http://127.0.0.1:8545` | RPC endpoint |
| `duration` | `30s` | Thời gian test |
| `concurrency` | `1000` | Số worker song song |
| `expectedAddress` | `0x781e...` | Address kỳ vọng trong response |

**Output**: Tổng requests, TPS, số lỗi, số data mismatch.

---

### 11. file

**Mục đích**: Benchmark đọc/ghi file theo chunk song song — đo tốc độ xử lý IO.

**Cách sử dụng**:

```bash
cd cmd/tool/file

# Copy file với benchmark
go run . --input /path/to/source --output /path/to/dest

# Chỉ benchmark đọc (không ghi)
go run . --input /path/to/source

# Tùy chỉnh chunk size và parallelism
go run . --input /path/to/large_file --chunk-size 4194304 --parallel 8

# Hiển thị báo cáo tốc độ mỗi giây
go run . --input /path/to/file --report-interval 1s
```

**Flags**:
| Flag | Mặc định | Mô tả |
|------|----------|-------|
| `--input` | *bắt buộc* | File nguồn |
| `--output` | *(trống)* | File đích (bỏ trống = chỉ đọc) |
| `--chunk-size` | `1MB` | Kích thước chunk (bytes) |
| `--parallel` | `4` | Số luồng song song |
| `--report-interval` | `0` | Khoảng báo cáo tạm thời |

---

### 12. verify_abi

**Mục đích**: Kiểm tra nhanh xem `GetValidatorHandler` ABI có khởi tạo thành công không.

**Cách sử dụng**:

```bash
cd cmd/tool/verify_abi
go run .
```

---

## Tool nên XÓA (🗑️)

---

### 13. add_validator_node4

**Lý do xóa**: ❌ Trùng chức năng hoàn toàn với `register_validator`. Sử dụng TCP client thay vì JSON-RPC, giá trị hardcode cho Node 4, không linh hoạt.

**Thay thế bằng**: `register_validator`

---

### 14. register_validator_example

**Lý do xóa**: ❌ Phiên bản cũ/đơn giản của `register_validator`. Dùng ABI 9 tham số (thiếu keys), hardcode private key, gửi tx bằng **base64** thay vì hex chuẩn. Không có verification.

**Thay thế bằng**: `register_validator`

---

### 15. rpc_example

**Lý do xóa**: ❌ Chức năng trùng hoàn toàn với `query_validators`. `query_validators` đầy đủ hơn (có flags, thêm delegation/rewards).

**Thay thế bằng**: `query_validators`

---

### 16. ethtx

**Lý do xóa**: ❌ Code test 1 lần — tính hash EIP-155 legacy transaction. Hardcode dữ liệu, không có flag/argument, không có ứng dụng thực tế.

---

### 17. txType

**Lý do xóa**: ❌ Ví dụ reference các loại Ethereum transaction (Legacy, EIP-2930, EIP-1559). 3 file Go với hardcode placeholder (`YOUR_ETHEREUM_RPC_URL`). File `lagacy.go` có package name sai (`package lagacy` thay vì `package main`), không thể build.

---

### 18. txproto

**Lý do xóa**: ❌ File gần như trống, chỉ in 1 dòng log `"txproto"`. Không có chức năng gì.

---

### 19. test

**Lý do xóa**: ❌ Script test nhanh `eth_chainId` — hardcode URL đến `rpc-proxy-sequoia.ibe.app:8446`, dùng `ioutil.ReadAll` (deprecated). Không có giá trị sử dụng lại.

---

### 20. ants

**Lý do xóa**: ❌ CPU stress test (đẩy CPU lên 100%) bằng goroutine pool. Không liên quan đến blockchain hay project. Có thể gây hại nếu chạy nhầm.

---

### 21. hash_calculator

**Lý do xóa**: ❌ Thư mục trống, không có file nào.

---

## Tóm tắt đề xuất

### Giữ lại (9 tool):
```
register_validator/
deregister_validator/
query_validators/
block_hash_checker/
watch_block/
libp2p_generate_key/
peer_id/
gen_bls/
verify_key.go
```

### Giữ cho dev/test (3 tool):
```
spam_rpc/
file/
verify_abi/
```

### Xóa (9 tool):
```bash
rm -rf cmd/tool/add_validator_node4
rm -rf cmd/tool/register_validator_example
rm -rf cmd/tool/rpc_example
rm -rf cmd/tool/ethtx
rm -rf cmd/tool/txType
rm -rf cmd/tool/txproto
rm -rf cmd/tool/test
rm -rf cmd/tool/ants
rm -rf cmd/tool/hash_calculator
```
