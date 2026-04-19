# Khắc Phục Lỗi Genesis Block Mismatch Trong Epoch Transition

## Vấn Đề

### Triệu chứng
```
Invalid block received from [0]: Ancestor B0([0],1rTr) not found among genesis blocks!
Invalid block received from [1]: Ancestor B0([1],SqWt) not found among genesis blocks!
Invalid block received from [2]: Ancestor B0([2],TTn2) not found among genesis blocks!
```

### Nguyên nhân gốc rễ

**Không phải lỗi khi restart** - hệ thống chạy tốt sau khi restart. Vấn đề xảy ra **TRONG QUÁ TRÌNH CHUYỂN ĐỔI EPOCH** khi hệ thống đang chạy.

#### Cơ chế Epoch Transition trong mtn-consensus

1. **Khi bắt đầu epoch mới** (ví dụ: Epoch 0 → Epoch 1, Epoch 1 → Epoch 2):
   - Mỗi validator node phải tạo **genesis blocks mới** cho epoch đó
   - Các genesis blocks này phải có **hash giống hệt nhau** trên tất cả các node
   - Hash được tính dựa trên: committee structure (thứ tự validators), epoch timestamp, và các tham số khác

2. **Vấn đề thứ tự validator**:
   - Khi Rust gọi Go để lấy danh sách validators cho epoch mới, Go trả về danh sách từ database
   - Database không đảm bảo thứ tự cố định (phụ thuộc vào internal storage)
   - **Node A** nhận: `[validator-1, validator-2, validator-3, validator-4]`
   - **Node B** nhận: `[validator-2, validator-1, validator-4, validator-3]`
   - → Cùng tập validators nhưng **thứ tự khác** → **hash khác** → **không đồng bộ được**

## Các Handler bị ảnh hưởng

### 1. `HandleGetValidatorsAtBlockRequest` ✅ (Đã có sorting)
- **Mục đích**: Lấy validators tại một block cụ thể
- **Sử dụng khi**: Epoch Monitor kiểm tra ai trong committee
- **Status**: Đã có sorting (lines 209 và 338)

### 2. `HandleGetActiveValidatorsRequest` ❌ (THIẾU sorting - ĐÃ SỬA)
- **Mục đích**: Lấy active validators cho epoch transition
- **Sử dụng khi**: Chuyển đổi epoch (critical!)
- **Vấn đề**: KHÔNG có sorting → mỗi node có thể nhận thứ tự khác nhau
- **Ảnh hưởng**: **Đây là nguyên nhân chính** gây ra genesis mismatch trong epoch transition

### 3. `HandleBlockRequest` ⚠️ (THIẾU sorting - ĐÃ SỬA để phòng ngừa)
- **Mục đích**: Trả về ValidatorList (legacy API)
- **Sử dụng khi**: Query validator info
- **Ảnh hưởng**: Có thể không critical, nhưng đã thêm sorting để đảm bảo tính nhất quán

## Giải pháp đã triển khai

### Patch 1: HandleGetActiveValidatorsRequest (CRITICAL)
```go
// Lấy tất cả validators từ state hiện tại
validators, err := rh.chainState.GetStakeStateDB().GetAllValidators()
if err != nil {
    return nil, fmt.Errorf("could not get all validators from stake DB: %w", err)
}

// CRITICAL: Sort validators by address (public key) to ensure consistent ordering across all nodes
// This prevents different nodes from having different committee orders during epoch transitions
sort.Slice(validators, func(i, j int) bool {
    return validators[i].Address().Hex() < validators[j].Address().Hex()
})

// Lọc chỉ lấy active validators (không jailed, có stake > 0)
validatorInfoList := &pb.ValidatorInfoList{}
```

### Patch 2: HandleBlockRequest (PREVENTIVE)
```go
validators, err := chainStateNew.GetStakeStateDB().GetAllValidators()
if err != nil {
    return nil, fmt.Errorf("could not get all validators from stake DB: %w", err)
}

// CRITICAL: Sort validators by address to ensure consistent ordering
sort.Slice(validators, func(i, j int) bool {
    return validators[i].Address().Hex() < validators[j].Address().Hex()
})

// Map the database validators to protobuf validators.
validatorList := &pb.ValidatorList{}
```

## Cách triển khai

### Bước 1: Rebuild Go binary (ĐÃ HOÀN THÀNH)
```bash
cd /home/abc/chain-n/mtn-simple-2025
go build -o simple_chain ./cmd/simple_chain
```

### Bước 2: Restart các Go nodes
**QUAN TRỌNG**: Chỉ cần restart Go processes, KHÔNG cần xóa data vì:
- Restart ban đầu đã ổn (validators được sắp xếp đúng)
- Vấn đề chỉ xảy ra trong quá trình runtime epoch transition
- Binary mới sẽ đảm bảo thứ tự nhất quán từ epoch transition tiếp theo

```bash
# Option A: Restart toàn bộ hệ thống (nếu dùng orchestration script)
pkill -f "simple_chain"
# Sau đó chạy lại script khởi động của bạn

# Option B: Restart từng Go process riêng lẻ (nếu chạy manual)
# Tìm và kill process cũ
ps aux | grep simple_chain
kill <PID>

# Chạy lại với binary mới
cd /home/abc/chain-n/mtn-simple-2025
# Chạy lệnh khởi động Go Master và Sub nodes của bạn
```

### Bước 3: Verify
Sau khi restart, hệ thống sẽ:
1. **Chạy bình thường** cho đến epoch transition tiếp theo
2. **Khi chuyển epoch**, tất cả nodes sẽ nhận validators theo **cùng thứ tự**
3. **Genesis blocks** cho epoch mới sẽ có **hash giống nhau**
4. **Không còn lỗi** "Ancestor not found among genesis blocks"

## Kiểm tra log để xác nhận

### Log thành công khi epoch transition
```
INFO: Handling GetActiveValidatorsRequest for epoch transition
INFO: Returning active validators for epoch transition, count=4
```

### Tất cả nodes sẽ log cùng thứ tự validators
```
📤 [GO→RUST] ValidatorInfo[0]: address=/ip4/127.0.0.1/tcp/9000, stake=1000000, name=node-0
📤 [GO→RUST] ValidatorInfo[1]: address=/ip4/127.0.0.1/tcp/9001, stake=1000000, name=node-1
📤 [GO→RUST] ValidatorInfo[2]: address=/ip4/127.0.0.1/tcp/9002, stake=1000000, name=node-2
📤 [GO→RUST] ValidatorInfo[3]: address=/ip4/127.0.0.1/tcp/9003, stake=1000000, name=node-3
```

**Quan trọng**: Thứ tự này phải **giống hệt** trên tất cả các nodes!

## Tham khảo

- Knowledge Base: `mtn_consensus_metanode_operations/artifacts/genesis_lifecycle.md` - Phần "Epoch-Boundary Genesis"
- Incident Log: `[2026-01-30] Node 1 Synchronization Stall & Genesis Mismatch`
- Root Cause: Non-Deterministic Validator Ordering trong epoch transitions
