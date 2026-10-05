# Báo cáo Kiểm thử Co-Attestation E2E Cụm 4 Validator (2026-10-05)

## 📌 Tổng quan mục tiêu
Theo yêu cầu **P0-1** trong `note/plan_production_readiness_20261005.md`, triển khai kiểm thử co-attestation thật trên cụm độc lập $\ge 4$ validator chạy đồng thuận Mysticeti (không dùng Raft 1 node), kiểm tra cơ chế cộng dồn chữ ký BLS ($f+1=2$ trên $N=4, f=1$) qua 5 kịch bản bắt buộc:
1. **Kịch bản A (Co-attestation Quorum)**: Đăng ký tài khoản $\implies$ `CONFIRMED` chỉ sau khi $\ge 2$ validator đã chứng thực; tài khoản chưa đăng ký bị chặn với `"not registered"`; sau khi confirmed, nạp tiền và giao dịch thành công (receipt status 1).
2. **Kịch bản B (Tolerance - 1 Node Down)**: Tắt 1 validator (`val3`, còn $3 \ge 2f+1=3$) $\implies$ hệ thống tiếp tục đồng thuận, đăng ký người dùng mới thành công, giao dịch thành công; bật lại `val3` $\implies$ catchup đầy đủ, khớp 100% block hash và state root.
3. **Kịch bản C (Quarantine - 2 Nodes Down, Zero-Fork)**: Tắt 2 validator (`val2`, `val3`, còn $2 < 3$) $\implies$ đồng thuận tạm dừng (zero new blocks), đăng ký giữ trạng thái `PENDING`, tuyệt đối **KHÔNG FORK** (hash `val0` == hash `val1`); bật lại cả 2 node $\implies$ phục hồi quorum, đồng thuận tiếp diễn, đăng ký chuyển thành `CONFIRMED`, giao dịch thành công.
4. **Kịch bản D (Byzantine Fake Event)**: Một validator Byzantine gửi sự kiện đăng ký giả kèm attestation đơn lẻ cho user chưa từng đăng ký ở Parent Chain $\implies$ các validator trung thực không chứng thực $\implies$ tài khoản **KHÔNG BAO GIỜ** thành `ParentRegistered`, giao dịch bị từ chối; chữ ký giả mạo từ khoá ngoài committee bị từ chối.
5. **Kịch bản E (Crash Recovery - kill -9 mid-traffic)**: `kill -9` đột ngột validator `val1` giữa luồng đăng ký $\implies$ 3 node còn lại tiếp tục đồng thuận và confirm; bật lại `val1` $\implies$ đồng bộ bắt kịp; kiểm toán toàn bộ block-by-block từ block #1 đến latest: **100% khớp block hash và state root trên cả 4 node**.

---

## 🛠️ Công cụ & Môi trường triển khai
1. **Môi trường độc lập (Port range 31xxx)**:
   - File cấu hình sinh tự động: `execution/scripts/test/gate_e2e/gen_env.py` (hỗ trợ cờ `--validators 4`).
   - Khởi tạo 4 validator riêng biệt: `val0`, `val1`, `val2`, `val3`.
   - Mỗi validator có: địa chỉ EVM riêng, khoá BLS riêng (`Databases.BLSPrivateKey`), và public key min-pk 48-byte được ghi nhận trong `alloc[addr].publicKeyBls` của `genesis.json`.
   - Cụm Parent Chain: HTTP `:31601`, P2P `:31001`.
   - Các cổng execution:
     - `val0`: RPC `:31646`, Conn `:31200`, P2P `:31100`, Peer RPC `:31300`.
     - `val1`: RPC `:31647`, Conn `:31201`, P2P `:31101`, Peer RPC `:31301`.
     - `val2`: RPC `:31648`, Conn `:31202`, P2P `:31102`, Peer RPC `:31302`.
     - `val3`: RPC `:31649`, Conn `:31203`, P2P `:31103`, Peer RPC `:31303`.
2. **Quản lý vòng đời tiến trình**:
   - `execution/scripts/test/gate_e2e/run_env.sh`: bổ sung các lệnh điều khiển chi tiết:
     - `start_node <valX>`: khởi động tiến trình validator cụ thể và đợi RPC sẵn sàng.
     - `stop_node <valX>`: gửi SIGTERM, đợi thoát graceful hoặc SIGKILL.
     - `kill_node <valX>`: gửi trực tiếp SIGKILL (`kill -9`) để mô phỏng crash phần cứng đột ngột.
3. **Bộ driver kiểm thử tự động**:
   - `execution/cmd/tool/e2e_coattest_4val/main.go`: Thực thi toàn bộ 23 bước kiểm tra đối với 5 kịch bản.
   - `execution/scripts/test/gate_e2e/test_coattest_4val.sh`: Script wrapper tự động chạy từ khâu gen env, start, test, so khớp state root, đến clean stop.

---

## 📊 Kết quả kiểm thử: 3 lần chạy liên tiếp (3 Consecutive Runs)

### 🔹 Lần chạy 1 (Run 1: `/tmp/gate_4val_run1`)
```
=================================================================
🏁 SUITE RESULTS: 23 steps executed
   PASS: 23 | FAIL: 0
=================================================================
✅ A1   unregistered user transfer is refused before registration (3ms)
✅ A2   register user on val0 and observe transition to CONFIRMED (>=2 attestations) (3.034s)
✅ A3   fund confirmed user and verify transfer succeeds with receipt status 1 (2.121s)
✅ A4   state parity across all 4 validators after Scenario A — Block #7 StateRoot: 0x4adcf40e6c0c9679... (14ms)
✅ B1   stop validator val3 — val3 stopped successfully (2.092s)
✅ B2   register new user uB while val3 is down (consensus advances) (2.717s)
✅ B3   fund and execute transaction from uB with 1 validator offline (2.12s)
✅ B3b  state parity across the 3 active validators before restart — Block #13 StateRoot: 0x59bffcf9... (10ms)
✅ B4   restart val3 and verify catchup to latest block (4.172s)
✅ B5   state parity across all 4 validators after Scenario B — Block #13 StateRoot: 0x59bffcf9... (16ms)
✅ C1   stop 2 validators: val2 and val3 (3.164s)
✅ C2   submit registration uC and verify consensus pauses with zero fork (4.011s)
✅ C3   restart val2 and val3; verify quorum restores and consensus advances (5.228s)
✅ C4   fund and execute transaction from uC (2.119s)
✅ C5   state parity across all 4 validators after Scenario C — Block #18 StateRoot: 0x550744ce... (18ms)
✅ D1   single validator injects fake attestation for unverified user (only 1 attestation) (3.508s)
✅ D2   verify fake user uD NEVER becomes ParentRegistered on any node (915ms)
✅ D3   forged attestation with non-committee key is rejected by handler (3.51s)
✅ D4   state parity across all 4 validators after Scenario D — Block #21 StateRoot: 0x04c40efb... (19ms)
✅ E1   kill -9 val1 while registering uE; quorum remains alive and confirms (5.516s)
✅ E2   restart val1 and verify catchup to latest block (28.737s)
✅ E3   exhaustive block-by-block hash and stateRoot audit across all 4 nodes: Blocks #1-#26 match 100% (111ms)
✅ E4   all registered users CONFIRMED on all 4 nodes; fake user unconfirmed everywhere (11ms)
```

### 🔹 Lần chạy 2 (Run 2: `/tmp/gate_4val_run2`)
```
=================================================================
🏁 SUITE RESULTS: 23 steps executed
   PASS: 23 | FAIL: 0
=================================================================
✅ A1..A4: PASS (Block #7 StateRoot: 0x48ef715b0b83d288...)
✅ B1..B5: PASS (Block #14 StateRoot: 0x3755f62032970001...)
✅ C1..C5: PASS (Block #20 StateRoot: 0x612c974141bf8645...)
✅ D1..D4: PASS (Block #23 StateRoot: 0x08f5f83f9489babb...)
✅ E1..E4: PASS (Blocks #1-#28 audited: 100% hash & stateRoot identical on all 4 nodes; 0 mismatches)
```

### 🔹 Lần chạy 3 (Run 3: `/tmp/gate_4val_run3`)
```
=================================================================
🏁 SUITE RESULTS: 23 steps executed
   PASS: 23 | FAIL: 0
=================================================================
✅ A1..A4: PASS (Block #7 StateRoot: 0x5c4834badba6b8f2...)
✅ B1..B5: PASS (Block #13 StateRoot: 0x2f74f7baa64c2d5c...)
✅ C1..C5: PASS (Block #18 StateRoot: 0x0cd3ebee865d1357...)
✅ D1..D4: PASS (Block #21 StateRoot: 0x369f7d55f4e1868d...)
✅ E1..E4: PASS (Blocks #1-#27 audited: 100% hash & stateRoot identical on all 4 nodes; 0 mismatches)
```

---

## 🔍 Phân tích các tiêu chí bất biến (Invariants Verification)

1. **Bất biến Zero-Fork (Zero-Fork Invariant)**:
   - Trong Kịch bản C, khi tắt 2 validator (số node hoạt động = $2 < 3 = 2f+1$):
     - Hệ thống tạm dừng đề xuất block mới, không có bất kỳ block phân kỳ nào được tạo ra.
     - Hash tại block hiện tại của `val0` và `val1` hoàn toàn trùng khớp từng byte.
     - Không có cơ chế timeout nào tự tiện bypass để ép commit; mọi commit pending được giữ nguyên.
     - Khi bật lại `val2` và `val3`, quorum khôi phục ngay lập tức và tiến trình tiếp diễn trơn tru.
2. **Bất biến Ngưỡng Co-attestation ($f+1$)**:
   - Với $N=4$ validator, $f = \lfloor(4-1)/3\rfloor = 1 \implies$ ngưỡng tối thiểu để kích hoạt trạng thái là $f+1 = 2$ chữ ký hợp lệ từ các thành viên committee khác nhau.
   - Khi một sự kiện chỉ nhận 1 chữ ký (Kịch bản D hoặc giai đoạn trung gian): payload được ghi nhận vào `AttestationStore`, nhưng trạng thái `ParentRegistered` của user vẫn là `false`.
   - User không thể thực hiện giao dịch cho đến khi có đủ $\ge 2$ attestations.
3. **Khả năng kháng tấn công Byzantine (Byzantine Resistance)**:
   - Một validator Byzantine giả mạo sự kiện không thể lừa được hệ thống vì các validator trung thực chỉ ký attestation khi sự kiện đó thực sự xuất hiện trong log của Parent Chain.
   - Kẻ tấn công dùng khoá ngoài committee gửi giao dịch hệ thống sẽ bị hàm `ApplyAttested` từ chối ngay lập tức (`attestation from non-committee validator`).
4. **Phục hồi sau Crash (Crash Recovery & State Parity)**:
   - Validator bị `kill -9` trong lúc có luồng giao dịch/đăng ký dở dang sau khi khởi động lại đã tự động nạp lại trạng thái từ PebbleDB/NOMT, kết nối lại mạng P2P Mysticeti, và đồng bộ đuổi kịp các node còn lại.
   - Kiểm toán block-by-block từng block từ #1 đến block mới nhất chứng minh **tất cả các node có hash và stateRoot hoàn toàn trùng khớp 100%** (0 sai lệch).
