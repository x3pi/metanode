# Stress Test: Đăng ký BLS 10,000 Accounts — Hướng dẫn & Kết quả

## Tổng quan

Test đăng ký BLS Public Key cho **10,000 account mới** trong 1 lần blast duy nhất.
Mục tiêu: đo TPS thực tế, tỉ lệ thành công, phân tích bottleneck từng component.

---

## 1. Kiến trúc

```
┌─────────────┐  SendBytes   ┌──────────┐    UDS     ┌──────────┐  dataChan  ┌──────────┐
│  tps_blast  │ ──────────►  │  Go-Sub  │ ────────►  │   Rust   │ ────────►  │ Go-Master│
│  (1 TCP)    │ fire-forget  │ Node 0   │  10 workers│ mtn-consensus│ consensus  │ Execution│
└─────────────┘              └──────────┘            └──────────┘            └──────────┘
```

- **Cluster**: 5 nodes mtn-consensus, Chain ID: 991
- **Test machine**: Single machine, all nodes local

## 2. Chạy test

### 2.1 Khởi động cluster

```bash
# Fresh start (clean data, giữ keys)
bash /path/to/mtn-consensus/metanode/scripts/node/run_all.sh
# Chờ ~30s cho cluster ổn định
```

### 2.2 Build & chạy blast

```bash
cd cmd/tool/tps_blast
go build -o tps_blast .
./tps_blast -count 10000 -batch 500 -sleep 10
```

| Flag | Mô tả | Mặc định |
|:---|:---|:---|
| `-count` | Số giao dịch | 1000 |
| `-batch` | TX mỗi batch | 500 |
| `-sleep` | Delay giữa batch (ms) | 20 |

### 2.3 Quy trình bên trong tool

1. Tạo 10,000 keypair secp256k1 → `blast_accounts.json`
2. Pre-build tất cả signed TX offline (~6.5s)
3. Blast 10K TX qua `SendBytes` fire-and-forget (~300ms)
4. Chờ 60s cho chain xử lý
5. Verify concurrent (50 goroutines, ~330ms) — **KHÔNG retry**
6. Report kết quả → `blast_results.json`

### 2.4 Giám sát real-time

```bash
# Go-Master blocks
bash scripts/logs/go-master.sh 0 -f | grep "committed.*tx_count=[1-9]"

# Go-Sub forwarding
bash scripts/logs/go-sub.sh 0 -f | grep "TxsProcessor2"
```

---

## 3. Kết quả (2026-02-21)

### 3.1 Tổng quan

```
═══════════════════════════════════════════════════
  📊 BLS REGISTRATION RESULTS (ONE-SHOT)
═══════════════════════════════════════════════════
  📤 Total TXs sent:       10,000
  🚀 Injection TPS:        33,563 tx/s
  ⏱️  Injection time:       298ms
  ─────────────────────────────────────────────────
  ✅ BLS registered:       10,000/10,000 (100.0%)
  ❌ Not registered:       0
  ⚠️  Verify errors:        0
  ─────────────────────────────────────────────────
  📊 Processing TPS:       ~294 tx/s (thực tế từ block log)
  ⏱️  Verify time:          331ms
═══════════════════════════════════════════════════
```

### 3.2 Thời gian từng component

| Component | Thời gian | TPS | Bottleneck? |
|:---|:---|:---|:---|
| **Injection** (client → go-sub) | 298ms | 33,563 tx/s | ❌ |
| **Go-Sub** (nhận + forward UDS) | ~1s | ~10,000 tx/s | ❌ |
| **Rust Consensus** (ordering) | instant | N/A | ❌ |
| **Go-Master** (execute + commit) | **~34s** | **~294 tx/s** | ✅ **BOTTLENECK** |
| **Verification** (50 goroutines) | 331ms | ~30,000 acc/s | ❌ |

### 3.3 Timeline chi tiết

```
10:13:01  Go-Sub nhận 10K TX, forward qua UDS (batches 21→67→104 TX)
10:13:02  Go-Sub gửi xong → Rust, Rust tạo consensus blocks
10:13:02  Go-Master: Block #224 (678 TX, merged 3 Rust blocks)
10:13:03  Go-Master: Block #228 (869 TX)
10:13:09  Go-Master: Block #239 (1000 TX) ← block lớn nhất
10:13:15  Go-Master: Block #251 (766 TX)
10:13:36  Go-Master: Block cuối → xong 10K TX
          ──────────────────────────────
          26 blocks, ~385 TX/block avg, 34s tổng
```

### 3.4 Block size distribution

| TX/block | Số blocks | Tổng TX |
|:---|:---|:---|
| 800–1000 | 3 | ~2,600 |
| 500–799 | 5 | ~3,200 |
| 200–499 | 14 | ~3,900 |
| < 200 | 4 | ~300 |
| **Tổng** | **26** | **10,000** |

---

## 4. Fix đã áp dụng: Parallel Execution

### 4.1 Vấn đề trước fix

Tất cả BLS TX có `ToAddress = ACCOUNT_SETTING_ADDRESS_SELECT` (address chung).
`GroupAndLimitTransactionsOptimized` gom tất cả vào **1 group duy nhất** → 1 goroutine → tuần tự.

**Trước fix**: ~40 TX/block, ~40 TPS

### 4.2 Fix

**File**: `pkg/transaction/transaction.go` — hàm `RelatedAddresses()`

Loại bỏ virtual selector addresses (`ACCOUNT_SETTING_ADDRESS_SELECT`, `IDENTIFIER_STAKE`)
khỏi danh sách related addresses. Các address này chỉ dùng để route TX, không phải shared state.

```go
// Trước: tất cả BLS TX share ToAddress → 1 group
relatedAddresses[len(t.proto.RelatedAddresses)] = t.ToAddress()

// Sau: loại bỏ selector → mỗi TX có group riêng → parallel
if toAddr != accountSettingSelector && toAddr != stakeSelector {
    relatedAddresses = append(relatedAddresses, toAddr)
}
```

### 4.3 Kết quả sau fix

| Metric | Trước | Sau | Cải thiện |
|:---|:---|:---|:---|
| TX/block | ~40 | 270–1000 | **~15x** |
| Processing TPS | ~40 | ~294 | **~7x** |
| Success rate | Cần retry | 100% one-shot | ✅ |

---

## 5. Bottleneck còn lại

Go-Master execution mất ~1-4s/block vì:

1. **`ProcessTransactions()`** — execute TX (đã parallel theo group)
2. **`IntermediateRoot()`** — hash toàn bộ dirty trie nodes, **O(n)** với n dirty accounts
3. **`commitWorker`** — LevelDB write (single-threaded)

Hướng tối ưu tiếp:
- Batch `IntermediateRoot()` cho nhiều TX hơn
- Tăng UDS workers (10 → 50+) để Rust nhận TX nhanh hơn
- Giảm Rust consensus block frequency để mỗi Rust block lớn hơn

---

## 6. File liên quan

| File | Mô tả |
|:---|:---|
| `main.go` | Source code `tps_blast` |
| `config.json` | Cấu hình kết nối node |
| `blast_accounts.json` | 10K accounts (key, address, status) |
| `blast_results.json` | Kết quả JSON lần blast gần nhất |
| `check/check_loop.go` | Script check từng account |

---

*Test: 2026-02-21 — Cluster: 5 nodes mtn-consensus local, Chain ID: 991*
