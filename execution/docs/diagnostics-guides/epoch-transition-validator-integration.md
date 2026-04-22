# Epoch Transition - Tích hợp Validator từ Go State

## Tổng quan

Luồng này cho phép epoch transition lấy dữ liệu validators từ Go state thay vì hardcode, đảm bảo committee được build từ state thực tế của blockchain.

## Đã hoàn thành

### 1. ✅ Thêm GetActiveValidatorsRequest vào validator.proto

**File**: `pkg/proto/validator.proto`

- Thêm message `GetActiveValidatorsRequest` để request active validators
- Thêm vào `Request` oneof: `GetActiveValidatorsRequest get_active_validators_request = 3`

### 2. ✅ Thêm Handler trong Go

**File**: `executor/unix_socket_handler.go`

- Thêm function `HandleGetActiveValidatorsRequest()` để:
  - Lấy tất cả validators từ `stakeStateDB.GetAllValidators()`
  - Lọc chỉ lấy active validators (không jailed, có stake > 0)
  - Map sang protobuf `ValidatorList`

**File**: `executor/unix_sokcet.go`

- Thêm case xử lý `Request_GetActiveValidatorsRequest` trong `handleConnection()`

### 3. ⚠️ Cần regenerate protobuf code

**Lệnh**:
```bash
cd mtn-simple-2025/pkg/proto
protoc --go_out=. --go_opt=paths=source_relative validator.proto
```

**Sau khi regenerate**, uncomment code trong:
- `executor/unix_socket_handler.go` (function `HandleGetActiveValidatorsRequest`)
- `executor/unix_sokcet.go` (case `Request_GetActiveValidatorsRequest`)

## Cần làm tiếp

### 3. Thêm function trong Rust ExecutorClient

**File**: `mtn-consensus/metanode/src/executor_client.rs`

Cần thêm function để:
1. Kết nối đến Go socket (`/tmp/rust-go.sock_2` hoặc socket tương tự)
2. Gửi `GetActiveValidatorsRequest`
3. Nhận `ValidatorList` response
4. Parse và trả về danh sách validators

**Ví dụ**:
```rust
pub async fn get_active_validators(&self) -> Result<Vec<ValidatorInfo>> {
    // Connect to Go socket
    // Send GetActiveValidatorsRequest
    // Receive ValidatorList response
    // Parse and return
}
```

### 4. Sửa epoch transition để build committee từ Go state

**File**: `mtn-consensus/metanode/src/node.rs` (function `transition_to_epoch`)

Thay vì dùng `proposal.new_committee` (hardcode), cần:
1. Gọi `executor_client.get_active_validators()` để lấy validators từ Go
2. Map validators từ Go sang Rust `Committee` format:
   - `address` → `Authority.address`
   - `total_staked_amount` → `Authority.stake`
   - `p2p_address` → `Authority.address` (network address)
   - `pubkey_bls` → `Authority.authority_key`
   - `pubkey_secp` → `Authority.protocol_key` và `network_key`
3. Build `Committee` mới từ validators
4. Tính `total_stake`, `quorum_threshold`, `validity_threshold`

### 5. Map validator data sang Committee format

**Cấu trúc Committee** (từ `committee_node_2.json`):
```json
{
  "epoch": 11,
  "total_stake": 4,
  "quorum_threshold": 3,
  "validity_threshold": 2,
  "authorities": [
    {
      "stake": 1,
      "address": "/ip4/127.0.0.1/tcp/9000",
      "hostname": "node-0",
      "authority_key": "...",
      "protocol_key": "...",
      "network_key": "..."
    }
  ]
}
```

**Mapping từ Go Validator**:
- `total_staked_amount` (string) → `stake` (u64, normalized)
- `p2p_address` → `address` (Multiaddr format)
- `pubkey_bls` → `authority_key`
- `pubkey_secp` → `protocol_key` và `network_key` (có thể cần split hoặc derive)

## Lưu ý quan trọng

1. **Fork-safety**: Tất cả nodes phải lấy cùng danh sách validators từ Go state tại cùng một điểm (barrier commit index)

2. **Deterministic**: Validators phải được sort theo một tiêu chí nhất định (ví dụ: address hoặc stake) để đảm bảo tất cả nodes build cùng committee

3. **Stake normalization**: Cần normalize stake amount (có thể chia cho một số để có stake nhỏ hơn, hoặc dùng stake trực tiếp)

4. **Key derivation**: Cần xác định cách map `pubkey_secp` sang `protocol_key` và `network_key` (có thể cần derive từ cùng một key hoặc lưu riêng trong validator state)

## Testing

1. Đăng ký validators trong Go state
2. Trigger epoch transition
3. Verify committee.json được tạo với validators từ Go state
4. Verify tất cả nodes có cùng committee.json sau transition

