# Validator Test Suite

Công cụ test tự động cho validator lifecycle: register/deregister Node 4, stop/resume nodes, kiểm tra fork.

## Cách chạy

```bash
# Build
cd cmd/tool/validator_test_suite
go build -o validator_test_suite .

# Chạy 1 round (mặc định)
./validator_test_suite

# Chạy nhiều rounds
./validator_test_suite --loop=3 --loop-interval=5m

# Chạy vô hạn (Ctrl+C để dừng)
./validator_test_suite --loop=0 --loop-interval=5m
```

## Flags

| Flag | Default | Mô tả |
|------|---------|-------|
| `--master-rpc` | `http://localhost:8747` | Master node RPC |
| `--node4-rpc` | `http://localhost:10748` | Node 4 RPC |
| `--tx-rpc` | `http://localhost:8545` | RPC gửi transactions |
| `--scripts-dir` | auto-detect | Đường dẫn `mtn-consensus/metanode/scripts/node/` |
| `--private-key` | Node 4 key | Private key hex |
| `--check-interval` | `5s` | Khoảng cách kiểm tra hash |
| `--stake` | `1000000000000000000000` | Stake amount (wei) |
| `--skip-phase` | "" | Bỏ qua phases (ví dụ: `"4,5"`) |
| `--loop` | `1` | Số rounds (0 = vô hạn) |
| `--loop-interval` | `5m` | Thời gian chờ giữa rounds |

## 5 Phases

| Phase | Mô tả | Thời gian |
|-------|-------|-----------|
| 1 | **Baseline** — Kiểm tra hệ thống ổn định | 30s |
| 2 | **Deregister Node 4** — Undelegate + Deregister + đợi epoch | ~2m10s |
| 3 | **Stop/Resume Node 4** — Tắt 30s rồi bật lại, đợi catch-up | ~1m50s |
| 4 | **Re-register Node 4** — Register + Delegate + đợi epoch | ~2m5s |
| 5 | **Stop/Resume Node 0 (Master)** — Tắt 30s rồi bật lại | ~1m50s |

Tổng 1 round: **~8m30s**

## ⚠️ Lưu ý quan trọng — Tránh node bị stuck

### Vấn đề: Node 0 bị kẹt epoch sau khi restart

Khi chạy loop liên tục, Phase 5 restart Node 0 (Master) nhiều lần. Nếu `loop-interval` quá ngắn, Node 0 **chưa kịp catch-up epoch** trước khi round mới bắt đầu restart lại. Sau vài lần:

1. Go Master bị restart liên tục → mỗi lần load epoch từ DB (epoch cũ)
2. Rust advance epoch nhưng **chậm hơn các node khác** vì phải replay
3. Node 0 kẹt ở epoch N trong khi các node khác đã ở epoch N+4
4. Consensus cần quorum (≥3/5 nodes cùng epoch) → Node 0 một mình → **stuck vĩnh viễn**
5. TX socket lock timeout → giao dịch không thực thi được

### Khuyến nghị

```bash
# ✅ AN TOÀN: Loop interval đủ dài (≥5 phút, khuyến nghị 10 phút)
./validator_test_suite --loop=0 --loop-interval=10m

# ✅ AN TOÀN: Chạy ít rounds
./validator_test_suite --loop=3 --loop-interval=5m

# ✅ AN TOÀN: Bỏ Phase 5 nếu chỉ test validator lifecycle
./validator_test_suite --loop=0 --skip-phase=5 --loop-interval=3m

# ⚠️ NGUY HIỂM: Loop interval ngắn + có Phase 5
./validator_test_suite --loop=0 --loop-interval=1m   # KHÔNG NÊN

# ⚠️ NGUY HIỂM: Chạy quá nhiều rounds liên tục với Phase 5
./validator_test_suite --loop=20 --loop-interval=3m  # RỦI RO CAO
```

### Khi node đã bị stuck

Nếu thấy log lặp lại `Lock timeout (1s) - epoch transition likely in progress`:

```bash
# Kiểm tra epoch mismatch
grep "Invalid block" mtn-consensus/metanode/logs/node_0/rust.log | tail -5
# Nếu thấy "expected X, actual Y" → node bị stuck

# Giải pháp: Restart toàn bộ cluster
cd mtn-consensus/metanode/scripts/node
./stop_all.sh
sleep 10
./start_all.sh
```

### Quy tắc chung

| Quy tắc | Giá trị |
|---------|---------|
| `loop-interval` tối thiểu nếu có Phase 5 | **5 phút** |
| `loop-interval` khuyến nghị | **10 phút** |
| Số rounds tối đa liên tục an toàn | **5-10 rounds** |
| Sau mỗi 10 rounds nên | Kiểm tra logs hoặc restart cluster |
| Nếu thấy fork | Dừng test, restart cluster, chạy lại |
