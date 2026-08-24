# 📘 METANODE RUNBOOK — QUY TRÌNH CỨU HỘ KHI CHAIN CHẾT (CHAIN-DEATH RECOVERY - P8)

> **Tài liệu đặc tả vận hành khẩn cấp (Emergency Runbook)** theo chuẩn kiến trúc Root Anchor (Mục 10.8 & Kịch bản T3.c trong `cross_chain_root_anchor_architecture.md`).

---

## 🎯 1. Mục đích & Nguyên Tắc Cốt Lõi

Khi một Private Chain thành viên trong hệ sinh thái gặp sự cố nghiêm trọng không thể phục hồi (chết hẳn do thảm họa hạ tầng, tấn công 51%, hoặc Byzantine takeover chiếm đa số validator):
* **Bảo vệ người dùng 100%:** Người dùng có tài sản trên Chain bị chết có quyền rút lại toàn bộ số dư native coin hợp pháp về một Chain an toàn hoặc Reserve Hub.
* **Bất biến Zero-Fork & Bảo toàn tổng cung:** Chỉ giải ngân đúng theo trạng thái `LastAnchoredStateRoot` đã được Root Anchor Hub chứng thực gần nhất.
* **Chống Double-Claim & Proof giả:** Kiểm tra chặt chẽ Merkle Proof và mapping `deadChainClaimed[chainId][account]`.

---

## 🔄 2. Quy Trình 4 Pha Khôi Phục Khẩn Cấp (Emergency Lifecycle)

```text
┌────────────────────────────────────────────────────────────────────────────────────────┐
│                        QUY TRÌNH 4 PHA CHAIN-DEATH RECOVERY                            │
└────────────────────────────────────────────────────────────────────────────────────────┘

  [ Pha 1: Phát hiện ]      [ Pha 2: Quản trị ]       [ Pha 3: Bằng chứng ]     [ Pha 4: Giải ngân ]
  Root Anchor phát hiện ──➔ Đề xuất & Biểu quyết ──➔ Người dùng tạo      ──➔ Nộp Claim & Nhận lại
  Chain mất liveness        ≥ 2/3 Chain đồng ý       Merkle Proof           tiền trên Chain an toàn
  (Timeout > 72h)           DeclareDead(ChainID)     từ LastStateRoot       (Khóa chống Claim lần 2)
```

---

### 🚨 Pha 1: Phát Hiện & Đánh Giá Sự Cố (Detection & Triage)
1. Hệ thống giám sát P7 phát hiện Chain không gửi bất kỳ Block Commit hoặc Heartbeat nào trong hơn $72\text{h}$ (hoặc nhận báo cáo sự cố nghiêm trọng từ uỷ ban vận hành).
2. Phát tín hiệu cảnh báo mức `CRITICAL: ChainLivenessLost (ChainID: X)`.

### 🗳️ Pha 2: Biểu Quyết Quản Trị On-Chain (Governance Declare Dead)
1. Một validator/chain thành viên tạo đề xuất cứu hộ:
   * `proposalId = keccak256("DECLARE_DEAD", deadChainId, timestamp)`
   * `kind = KindDeclareDead (UnregisterChain)`
   * `payload = ABI_Encode(deadChainId)`
2. Các chain active tham gia bỏ phiếu:
   * **Ngưỡng biểu quyết:** $\ge 2/3$ tổng số chain active đồng thuận ($1\text{ chain} = 1\text{ phiếu}$, không tính theo stake để tránh lũng đoạn).
3. Khi đạt đủ quorum $\ge 2/3$:
   * Kích hoạt hàm `DeclareDead(deadChainId)`.
   * Root Anchor chuyển trạng thái `DeadChains[deadChainId] = true`.
   * **Đóng băng vĩnh viễn:** Từ chối mọi Commit mới từ Chain này và cô lập quỹ `PerChainAllocation[deadChainId]`.

### 📜 Pha 3: Trích Xuất Dữ Liệu & Tạo Merkle Proof
1. Người dùng lấy `LastAnchoredStateRoot` của Chain bị chết từ Root Anchor.
2. Trích xuất tài khoản của mình từ state snapshot gần nhất:
   * `AccountLeaf = { account: 0x..., balance: 1000 MTN }`
   * `accountLeafHash = keccak256(account, balance)`
3. Tạo `MerkleProof` chứng minh `accountLeafHash` nằm trong `LastAnchoredStateRoot`.

### 💰 Pha 4: Nộp Claim & Nhận Hoàn Tiền Cứu Hộ
1. Người dùng gửi giao dịch đến `GatewayPrecompile` hoặc `RootAnchorEngine`:
   ```go
   gateway.ClaimDeadChainBalance(deadChainID, account, balance, proof, accountLeafHash)
   ```
2. Gateway thực thi kiểm tra 4 lớp an ninh:
   * ✅ **Lớp 1 (Trạng thái Chain):** `DeadChains[deadChainID] == true` (Chain đã có quyết nghị Declare Dead).
   * ✅ **Lớp 2 (Chống Double Claim):** `DeadChainClaimed[deadChainID:account] == false`.
   * ✅ **Lớp 3 (Tính toàn vẹn mật mã):** `VerifyMerkleProof(accountLeafHash, proof, LastAnchoredStateRoot) == true`.
   * ✅ **Lớp 4 (Bảo toàn tổng cung):** `balance <= PerChainAllocation[deadChainID]`.
3. Khi tất cả điều kiện thỏa mãn:
   * Đánh dấu `DeadChainClaimed[deadChainID:account] = true`.
   * Khấu trừ `PerChainAllocation[deadChainID] -= balance`.
   * Giải ngân số dư native coin về ví của người dùng trên Chain an toàn (hoặc Reserve).

---

## 🛡️ 3. Ma Trận Đối Kháng & Xử Lý Lỗi (Adversarial Defenses)

| Tình huống tấn công / lỗi | Cơ chế phòng thủ | Kết quả |
| :--- | :--- | :---: |
| **Claim khi Chain chưa bị Declare Dead** | Kiểm tra `DeadChains[deadChainID]` | ❌ Revert `ErrChainNotDead` |
| **Tấn công Double-Claim (Nộp lại proof cũ lần 2)** | Kiểm tra `DeadChainClaimed[claimKey]` | ❌ Revert `ErrDeadChainAlreadyClaimed` |
| **Làm giả số dư trong Leaf hoặc Proof sai** | `VerifyMerkleProof(leafHash, proof, stateRoot)` | ❌ Revert `ErrInvalidMerkleProof` |
| **Yêu cầu số tiền vượt quá Allocation của Chain chết** | `SupplyLedger.GetAllocation(deadChainID)` | ❌ Revert `ErrInsufficientAllocation` |

---

## 📋 4. Checklist Diễn Tập Khẩn Cấp (Drill Checklist - DoD T3.c)

- [x] Mô phỏng 1 chain testnet ngừng hoạt động hoàn toàn.
- [x] Biểu quyết on-chain governance $\ge 2/3$ đạt điều kiện tuyên bố `Dead`.
- [x] Tạo Merkle Proof tài khoản từ `LastAnchoredStateRoot`.
- [x] Thực thi `ClaimDeadChainBalance` thành công và giải ngân tài sản chính xác.
- [x] Thử nghiệm tấn công Replay / Double-Claim bị chặn đứng 100%.
