# 🔍 Audit: mvmId Lifecycle — Rò rỉ bộ nhớ & Lệch hash

> **Ngày audit:** 2026-05-28
> **Phạm vi:** Toàn bộ code liên quan đến `GetOrCreateMVMApi`, `ClearMVMApi`, `ProtectMVMApi`, `UnprotectMVMApi`, `MVM_cancelTransaction` trong Go và C++
> **Mục tiêu:** Tìm rò rỉ bộ nhớ, giải phóng sai, lệch hash

---

## 📋 Mục lục

1. [Tổng quan kiến trúc mvmId](#1-tổng-quan-kiến-trúc-mvmid)
2. [Bảng trace toàn bộ lifecycle](#2-bảng-trace-toàn-bộ-lifecycle)
3. [Phân tích chi tiết từng flow](#3-phân-tích-chi-tiết-từng-flow)
4. [🐛 BUG TÌM THẤY](#4--bug-tìm-thấy)
5. [✅ Các flow ĐÃ AN TOÀN](#5--các-flow-đã-an-toàn)
6. [Khuyến nghị](#6-khuyến-nghị)

---

## 1. Tổng quan kiến trúc mvmId

### Các tầng resource cần giải phóng khi clear một mvmId

```
┌──────────────────────────────────────────────────────────┐
│  Go Layer                                                │
│  ├── apiInstances (sync.Map)   → MVMApi struct           │
│  ├── protectedApiInstances     → protection flag         │
│  └── apiInstanceCount          → atomic counter          │
├──────────────────────────────────────────────────────────┤
│  C++ Layer (giải phóng bởi MVM_cancelTransaction)        │
│  ├── XapianRegistry            → managers per mvmId      │
│  │   └── XapianManager[]       → uncommitted DB changes  │
│  └── State::instances          → in-memory state cache   │
└──────────────────────────────────────────────────────────┘
```

### ClearMVMApi logic (mvm_api.go:316-341)

```go
func ClearMVMApi(mvmId common.Address) {
    // 1. Skip nếu protected
    if _, protected := protectedApiInstances.Load(mvmId); protected {
        return  // ← KHÔNG clear!
    }
    // 2. Xóa khỏi Go map
    instance, loaded := apiInstances.LoadAndDelete(mvmId)
    // 3. LUÔN gọi C++ cleanup (kể cả khi Go map không có entry)
    C.MVM_cancelTransaction(mvmId)  // ← Cancel Xapian + clear State
    // 4. Giảm counter
    if loaded {
        apiInstanceCount.Add(-1)
    }
}
```

---

## 2. Bảng trace toàn bộ lifecycle

### Legend

| Ký hiệu | Nghĩa |
|----------|--------|
| ✅ | An toàn — đã clear đúng |
| ⚠️ | Cần chú ý — có rủi ro tiềm ẩn |
| 🐛 | BUG — rò rỉ hoặc sai logic |

### Master Execution (isCache=true)

| File | Hàm | Create | Protect | Clear | C++ Cancel | Verdict |
|------|-----|--------|---------|-------|------------|---------|
| `vm_processor.go:116` | `executeSmartContract` (write) | `GetOrCreateMVMApi` | `ProtectMVMApi` (L114) + `defer UnprotectMVMApi` (L119) | **KHÔNG** — giữ lại | Chờ commit | ✅ |
| `vm_processor.go:274` | `deploySmartContract` | `GetOrCreateMVMApi` | `ProtectMVMApi` (L114) + `defer UnprotectMVMApi` (L119) | `ClearMVMApi` (L274) | Qua ClearMVMApi | ✅ |
| `vm_processor.go:345` | `readOnlyCall` | `GetOrCreateMVMApi` (L99) | KHÔNG | `ClearMVMApi` (L345) | Qua ClearMVMApi | ✅ |
| `vm_processor.go:686` | `sendNative` | Dùng chung mvmE | `ProtectMVMApi` (L114) | `UnprotectMVMApi` (L686), **KHÔNG ClearMVMApi** | **KHÔNG** | ⚠️ **Xem BUG #1** |
| `vm_processor.go:704` | `ExecuteNonceOnly` (isCache=false) | `GetOrCreateMVMApi` (L629) | `ProtectMVMApi` (L626) + `defer UnprotectMVMApi` (L632) | `ClearMVMApi` (L705) khi `!isCache` | Qua ClearMVMApi | ✅ |
| `block_processor_commit.go:132` | commitWorker | KHÔNG — dùng existing | `UnprotectMVMApi` (L112/123) | `ClearMVMApi` (L132) | Qua ClearMVMApi | ✅ |

### Virtual Execution (Sub node)

| File | Hàm | Create | Protect | Clear | C++ Cancel | Verdict |
|------|-----|--------|---------|-------|------------|---------|
| `transaction_virtual_processor.go` | `ProcessSingleTransactionVirtual` (DELETED) | - | - | - | - | ✅ |
| `transaction_virtual_processor.go:427/441` | batchSubmit dry-run | `GetOrCreateMVMApi` (via vmP) | KHÔNG | `ClearMVMApi` (L427/441) | Qua ClearMVMApi | ✅ |

### Off-chain Execution

| File | Hàm | Create | Protect | Clear | C++ Cancel | Verdict |
|------|-----|--------|---------|-------|------------|---------|
| `transaction_processor_offchain.go:129` | `executeTransactionOffChainWithState` | `GetOrCreateMVMApi` (L129) | KHÔNG | `ClearMVMApi` (L186) | Qua ClearMVMApi | ✅ |
| `transaction_processor_offchain.go:291` | `executeTransactionOffChain` | `GetOrCreateMVMApi` (L291) | KHÔNG | `ClearMVMApi` (L347) | Qua ClearMVMApi | ✅ |
| `transaction_processor_offchain.go:402` | `ProcessTransactionDebug` | Dùng `tx.ToAddress()` as mvmId | KHÔNG | Via `ExecuteTransactionWithMvmIdDebug` → `ClearMVMApi` (L113) | Qua ClearMVMApi | ✅ |

### Cross-Chain

| File | Hàm | Create | Protect | Clear | C++ Cancel | Verdict |
|------|-----|--------|---------|-------|------------|---------|
| `cross_chain_outbound.go:86` | `handleLockAndBridge` | `GetOrCreateMVMApi` (L86) | KHÔNG | **KHÔNG CLEAR** | **KHÔNG** | 🐛 **Xem BUG #2** |
| `cross_chain_outbound.go:212` | `handleSendMessage` | `GetOrCreateMVMApi` (L212) | KHÔNG | **KHÔNG CLEAR** | **KHÔNG** | 🐛 **Xem BUG #2** |
| `cross_chain_inbound.go:39` | `executeMintForInbound` | `GetOrCreateMVMApi` (L39) | KHÔNG | **KHÔNG CLEAR** | **KHÔNG** | 🐛 **Xem BUG #2** |
| `cross_chain_inbound.go:222` | `executeConfirmation` | `GetOrCreateMVMApi` (L222) | KHÔNG | **KHÔNG CLEAR** | **KHÔNG** | 🐛 **Xem BUG #2** |

### Debug

| File | Hàm | Create | Protect | Clear | C++ Cancel | Verdict |
|------|-----|--------|---------|-------|------------|---------|
| `vm_processor_debug.go:102` | `ExecuteTransactionWithMvmIdDebug` | `GetOrCreateMVMApi` (L102) | KHÔNG | `ClearMVMApi` (L113) | Qua ClearMVMApi | ✅ |
| `vm_processor_debug.go:303` | `ExecuteTransactionWithMvmIdSub` | `GetOrCreateMVMApi` (L303) | KHÔNG (caller protect) | Caller clear | Caller clear | ✅ |
| `vm_processor_debug.go:629` | `ExecuteNonceOnly` debug path | `GetOrCreateMVMApi` (L629) | `ProtectMVMApi` (L626) | `ClearMVMApi` (L705) | Qua ClearMVMApi | ✅ |

### GC / Bulk Cleanup

| File | Hàm | Trigger | Logic |
|------|-----|---------|-------|
| `mvm_api.go:248` | `RemoveOldApiInstances` | Khi `apiInstanceCount > 50000` | Sort by createdAt, xóa cũ nhất, skip protected | ✅ |
| `mvm_api.go:343` | `ClearAllMVMApi` | Manual call | Range qua map, skip protected | ✅ |
| `vm_processor_state.go:416` | `updateStateDB` revert | Khi TX revert | `ClearMVMApi(mvmId)` | ✅ |

---

## 3. Phân tích chi tiết từng flow

### Flow 1: Master Execute (happy path — write TX)

```
1. processSingleGroup → NewVmProcessor(mvmId = contractAddress)
2. ExecuteTransactionWithMvmId
   → ProtectMVMApi(mvmId)                    ← Bảo vệ khỏi GC
   → GetOrCreateMVMApi(mvmId)                ← Tạo/reuse
   → defer UnprotectMVMApi(mvmId)            ← Gỡ bảo vệ khi hàm return
   → executeSmartContract(isCache=true)
     → mvmE.Execute(...)                     ← C++ thực thi, Xapian commit
     → updateStateDB(...)                    ← Go apply balance/nonce/storage
     → KHÔNG ClearMVMApi                     ← ✅ Giữ lại để CommitFullDb
3. block_processor_commit.go → commitWorker
   → CommitFullDb()                          ← Flush Xapian xuống disk
   → UnprotectMVMApi(mvmId)                  ← Gỡ bảo vệ
   → ClearMVMApi(mvmId)                      ← ✅ Dọn dẹp Go + C++
   → RemoveOldApiInstances()                 ← GC thêm nếu > 50k
```

**Verdict: ✅ AN TOÀN** — Protect/Unprotect/Clear flow hoàn chỉnh.

### Flow 2: Master Deploy

```
1. deploySmartContract
   → Deploy(...)                             ← C++ deploy
   → updateStateDB(...)                      ← Go apply
   → ClearMVMApi(mvmId)                      ← ✅ Clear ngay sau deploy
```

**Verdict: ✅ AN TOÀN** — Deploy dùng mvmId tạm, clear ngay.

### Flow 3: ReadOnly

```
1. mvmIdReadOnly = random address (prefix 0xFC)
2. GetOrCreateMVMApi(mvmIdReadOnly)           ← Tạo mới
3. mvmE.Call(readOnly=true, commit=true)      ← C++ call
4. ClearMVMApi(readOnlyMvmId)                 ← ✅ Clear ngay
```

**Verdict: ✅ AN TOÀN**

---

## 4. 🐛 BUG TÌM THẤY

### BUG #1: `sendNative` — Unprotect nhưng KHÔNG Clear (Rò rỉ bộ nhớ nhẹ)

**File:** [vm_processor.go:686-688](../execution/pkg/blockchain/vm_processor/vm_processor.go#L686-L688)

```go
// sendNative() cuối hàm:
mvm.UnprotectMVMApi(currentMvmId)
// mvm.ClearMVMApi(currentMvmId)   ← ĐÃ BỊ COMMENT OUT!
return rs, nil
```

**Vấn đề:**
- `sendNative` là hàm xử lý **regular transaction** (chuyển coin, không phải smart contract call).
- Nó tạo `MVMApi`, dùng xong, gọi `UnprotectMVMApi` nhưng **KHÔNG gọi `ClearMVMApi`**.
- Instance còn nằm trong `apiInstances` map → **rò rỉ bộ nhớ (Go side)**.
- C++ side: `MVM_cancelTransaction` **KHÔNG được gọi** → Nếu có Xapian operation → **dirty state tồn tại**.

**Mức độ nghiêm trọng:** ⚠️ **TRUNG BÌNH**
- Với regular TX (chuyển coin), C++ side không có Xapian operations nên không gây lệch hash.
- Nhưng Go `MVMApi` instance bị leak → tích tụ bộ nhớ.
- `RemoveOldApiInstances()` sẽ dọn khi > 50k, nhưng đây là band-aid, không phải fix.

**Tuy nhiên**, nhìn lại flow:
- `sendNative` được gọi từ `ExecuteTransactionWithMvmId` khi `isCache=true` → instance sẽ được `commitWorker` clear sau đó qua `block_processor_commit.go:132`.
- Khi `isCache=false` (không qua commit path), dòng code comment-out sẽ gây leak thật.

**Nhưng kiểm tra caller:**
```go
// vm_processor.go:121
if tx.IsRegularTransaction() || tx.ToAddress() == ACCOUNT_SETTING {
    rs, err := vmP.sendNative(execCtx, tx, mvmE, isCache)
    return rs, err
}
```
→ `sendNative` luôn được gọi từ `ExecuteTransactionWithMvmId`, nơi mà khi `isCache=true`, `ProtectMVMApi + defer UnprotectMVMApi` đã được gọi trước đó (L114+119). CommitWorker sẽ clear.

→ Khi `isCache=false` (Virtual Execution): `sendNative` return → `ExecuteTransactionWithMvmId` return → caller (Virtual Executor) sẽ gọi `ClearMVMApi`.

**Kết luận BUG #1:** ⚠️ **Không nghiêm trọng trực tiếp** vì caller luôn clear, nhưng code **không tự chủ** (phụ thuộc caller), vi phạm nguyên tắc "resource owner cleans up". Nếu tương lai có caller mới quên clear → leak thật.

---

### BUG #2: Cross-chain handlers KHÔNG bao giờ clear mvmId (Rò rỉ bộ nhớ + Rủi ro lệch hash)

**Files:**
- [cross_chain_outbound.go:86](../execution/pkg/cross_chain_handler/cross_chain_outbound.go#L86) — `handleLockAndBridge`
- [cross_chain_outbound.go:212](../execution/pkg/cross_chain_handler/cross_chain_outbound.go#L212) — `handleSendMessage`
- [cross_chain_inbound.go:39](../execution/pkg/cross_chain_handler/cross_chain_inbound.go#L39) — `executeMintForInbound`
- [cross_chain_inbound.go:222](../execution/pkg/cross_chain_handler/cross_chain_inbound.go#L222) — `executeConfirmation`

**Pattern lặp lại ở cả 4 hàm:**

```go
mvmE := mvm.GetOrCreateMVMApi(mvmId, chainState.GetSmartContractDB(), chainState.GetAccountStateDB(), true)
// ... sử dụng mvmE ...
// ❌ KHÔNG CÓ mvm.ClearMVMApi(mvmId) ở cuối!
```

**Vấn đề:**
1. **Rò rỉ bộ nhớ Go:** `MVMApi` instance được tạo nhưng không bao giờ clear → tích tụ trong `apiInstances` map.
2. **Rò rỉ C++ state:** `MVM_cancelTransaction` không được gọi → XapianRegistry có thể giữ dirty managers cho mvmId này.
3. **Rủi ro lệch hash:** Nếu cùng mvmId được reuse sau đó (vì cross-chain dùng `mvmId` truyền từ caller — thường là `tx.ToAddress()` = `CROSS_CHAIN_CONTRACT_ADDRESS`), dirty Xapian logs từ lần trước sẽ **cộng dồn** → `MapFullDbHash` khác nhau giữa các node → **FORK**.

**Tuy nhiên**, phân tích sâu hơn:
- Cross-chain `mvmId` thường = `CROSS_CHAIN_CONTRACT_ADDRESS` (0x1002) — địa chỉ cố định.
- Các hàm này **luôn dùng `ProcessNativeMintBurn`** → C++ gọi `execute()` → `processResult()` sẽ **tự commit/cancel** Xapian trong C++ side.
- Vấn đề chỉ là **Go side không clear MVMApi instance** → rò rỉ bộ nhớ Go struct.

**Nhưng kiểm tra caller path:**
```
HandleTransaction() → handleLockAndBridge(mvmId)
                    → executeMintForInbound(mvmId)
                    → executeConfirmation(mvmId)
```
`HandleTransaction` nằm trong `processGroup` flow của Master → mvmId = `CROSS_CHAIN_CONTRACT_ADDRESS` → instance này sẽ được `commitWorker` clear ở `block_processor_commit.go:132`.

**Kết luận BUG #2:** ⚠️ **Rò rỉ bộ nhớ nhẹ** — MVMApi instance cho `CROSS_CHAIN_CONTRACT_ADDRESS` không được clear bởi chính cross-chain handlers, mà phụ thuộc vào `commitWorker`. Nếu cross-chain TX bị revert trước khi đến commitWorker, hoặc nếu có error path nào skip commit, thì instance sẽ leak.

**Thực tế:** Không gây lệch hash vì C++ side tự commit/cancel trong `processResult()`. Nhưng thiếu defensive cleanup.

---

### BUG #3: `ProcessNativeMintBurn` KHÔNG clear mvmId (Pattern lặp lại)

**File:** [vm_processor.go:480-588](../execution/pkg/blockchain/vm_processor/vm_processor.go#L480-L588)

```go
func (vmP *VmProcessor) ProcessNativeMintBurn(...) (types.ExecuteSCResult, error) {
    // ... thực thi ...
    // ❌ KHÔNG CÓ ClearMVMApi ở cuối hàm!
    // ❌ KHÔNG CÓ UnprotectMVMApi!
    return rs, nil
}
```

**Vấn đề:** `ProcessNativeMintBurn` nhận `mvmE` đã tạo từ caller, không tự clear. Điều này **phụ thuộc** vào caller phải clear — nhưng tất cả caller (cross-chain handlers) cũng **KHÔNG clear** (BUG #2).

**Kết luận:** Cùng BUG #2 — phụ thuộc `commitWorker` để clear.

---

### BUG #4: Early return paths thiếu ClearMVMApi (RESOLVED/DEFUNCT)

**Vấn đề:** Đã được giải quyết triệt để. Hàm `ProcessSingleTransactionVirtual` đã bị xóa bỏ hoàn toàn khỏi codebase.

```go
// Nếu TX revert:
if exRs.ReceiptStatus() == pb.RECEIPT_STATUS_THREW || ... {
    return nil, fmt.Errorf("..."), exRs.Return()
    // ❌ KHÔNG CÓ ClearMVMApi(mvmId)!
    // ❌ KHÔNG CÓ UnprotectMVMApi(mvmId)!
}
// Nếu err != nil:
if err != nil {
    return nil, err, nil
    // ❌ KHÔNG CÓ ClearMVMApi(mvmId)!
}
// Nếu exRs == nil:
if exRs == nil {
    return nil, fmt.Errorf("..."), nil
    // ❌ KHÔNG CÓ ClearMVMApi(mvmId)!
}

// Chỉ clear ở happy path (L189-190):
mvm.UnprotectMVMApi(mvmId)
mvm.ClearMVMApi(mvmId)
```

**Vấn đề:**
- `ProtectMVMApi(mvmId)` được gọi ở L112.
- Nếu TX revert (L142) hoặc error (L168) hoặc exRs nil (L172) → return sớm → **KHÔNG gọi `UnprotectMVMApi` và `ClearMVMApi`**.
- Instance bị **PROTECTED + không clear** = **permanent leak** (GC `RemoveOldApiInstances` skip protected instances!).

**Mức độ nghiêm trọng:** 🐛 **NGHIÊM TRỌNG**
- Protected instance **KHÔNG BAO GIỜ** được clear bởi GC (`RemoveOldApiInstances` skip protected).
- C++ `MVM_cancelTransaction` không được gọi → dirty Xapian state.
- Nếu cùng mvmId (= random address) được reuse (rất khó vì random) → không lệch hash.
- Nhưng **bộ nhớ leak vĩnh viễn** cho mỗi failed virtual TX.

**Tần suất:** Mỗi khi một virtual TX fail (revert, error, nil result) → +1 leaked entry.

---

## 5. ✅ Các flow ĐÃ AN TOÀN

| Flow | Lý do an toàn |
|------|---------------|
| **Master execute (write, isCache=true)** | Protect → Execute → commitWorker clears + RemoveOld |
| **Master deploy** | ClearMVMApi ngay sau deploy |
| **ReadOnly** | Random mvmId + ClearMVMApi ngay sau Call |
| **Off-chain (both variants)** | Random mvmId + ClearMVMApi ngay sau khi xong |
| **Debug** | Random mvmId + ClearMVMApi ngay |
| **Virtual batchSubmit dry-run** | ClearMVMApi per itemMvmId |
| **updateStateDB revert** | ClearMVMApi khi TX throw (L416) |
| **GC mechanism** | `RemoveOldApiInstances` sort by age, xóa unprotected oldest |

---

## 6. Khuyến nghị

### 🔴 Priority 1 — Fix BUG #4 (Virtual Execution early return leak)

**File:** `transaction_virtual_processor.go`

Thêm `defer` cleanup ngay sau `ProtectMVMApi`:

```go
mvm.ProtectMVMApi(mvmId)
defer func() {
    mvm.UnprotectMVMApi(mvmId)
    mvm.ClearMVMApi(mvmId)
}()
```

Hoặc sử dụng pattern `defer cleanup` tại chỗ ProtectMVMApi (L112), xóa bỏ explicit clear ở L189-190.

### 🟡 Priority 2 — Thêm defensive ClearMVMApi cho cross-chain handlers

**Files:** `cross_chain_outbound.go`, `cross_chain_inbound.go`

Mặc dù hiện tại `commitWorker` đang clear, nên thêm defensive cleanup:

```go
defer mvm.ClearMVMApi(mvmId)
```

ở cuối mỗi hàm handler, hoặc ở caller `HandleTransaction`.

### 🟢 Priority 3 — Uncomment ClearMVMApi trong sendNative

**File:** `vm_processor.go:687`

```diff
- // mvm.ClearMVMApi(currentMvmId)
+ if !isCache {
+     mvm.ClearMVMApi(currentMvmId)
+ }
```

Giống pattern đã dùng ở `executeSmartContract` (L473-475) và `ExecuteNonceOnly` (L704-706).

### 🔵 Priority 4 — Monitoring

Thêm metric cho `apiInstanceCount` và `protectedApiInstances` count để phát hiện leak sớm.

---

## Tổng kết

| Loại vấn đề | Số lượng | Nghiêm trọng nhất |
|-------------|----------|--------------------|
| Rò rỉ bộ nhớ (Go side) | 3 | BUG #4 — protected leak vĩnh viễn |
| Rò rỉ C++ state | 1 | BUG #4 — MVM_cancelTransaction bị skip |
| Lệch hash trực tiếp | 0 | Không tìm thấy fork risk trực tiếp |
| Thiếu defensive cleanup | 4 hàm | Cross-chain handlers |

> **Kết luận:** Code hiện tại **KHÔNG có bug gây fork trực tiếp** — C++ side (`processResult`) tự commit/cancel Xapian đúng. Vấn đề chính là **rò rỉ bộ nhớ Go** do thiếu `ClearMVMApi` ở error paths, đặc biệt BUG #4 gây **permanent protected leak** khi virtual TX fail.
