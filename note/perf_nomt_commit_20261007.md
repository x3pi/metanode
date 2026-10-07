# Báo Cáo Đo Đạc Microbenchmark NOMT Commit & Phục Hồi Crash Tiến Trình — 2026-10-07

## 1. Mục Tiêu & Phương Pháp Đo Đạc

1. **Microbenchmark:** Chạy `go test -bench=NomtCommit -benchmem -count=5 ./pkg/trie/...` để định lượng thời gian commit trie ở cấp độ đơn vị qua 5 lần lặp độc lập, so sánh mô hình BlockPipeline (`commitWg` + `CommitAsync`) và Synchronous Commit.
   *(Lưu ý đính chính: tham số cuối của `nomt_ffi.Open(..., false)` trong code benchmark là `preallocate bool`, KHÔNG phải tham số bật/tắt fsync. Fsync được quản lý độc lập bên trong NOMT engine).*
2. **Khôi phục sự cố sập tiến trình (Crash-Process Recovery under Load):** Chạy `test_crash_recovery_nomt.sh` đủ 10 vòng `kill -9` ngẫu nhiên trên các validator của cụm cô lập trong lúc đang blast giao dịch SECP256k1 EIP-1559, kiểm chứng:
   - Không node nào thoát với mã lỗi 78 (Startup Data Integrity Check).
   - Không xuất hiện tệp cờ lỗi `/tmp/MTN_INTEGRITY_FAILED`.
   - Các node khởi động lại tự động bắt kịp chiều cao block của cụm.
   - Parity 100% Zero-Fork (Block Hash + State Root) giữa toàn bộ 4 validator sau khi node tái gia nhập.

---

## 2. Kết Quả Microbenchmark (NOMT Commit)

Lệnh thực thi:
```bash
cd execution && go test -bench=NomtCommit -benchmem -count=5 ./pkg/trie/...
```

### 2.1 Bảng số liệu chi tiết (5 lần đo)

| Kịch bản | Lần đo | Số ops | Thời gian (ns/op) | Thời gian (ms/op) | Bộ nhớ (B/op) | Allocs/op |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: |
| **BlockPipeline (Keys=100)** | 1 | 354 | 3,251,281 | 3.25 ms | 131,714 B | 1,093 |
| | 2 | 309 | 3,463,575 | 3.46 ms | 156,251 B | 1,093 |
| | 3 | 326 | 3,436,890 | 3.44 ms | 164,937 B | 1,093 |
| | 4 | 320 | 3,377,975 | 3.38 ms | 152,369 B | 1,093 |
| | 5 | 344 | 3,655,662 | 3.66 ms | 156,878 B | 1,093 |
| **Trung bình Pipeline 100** | — | — | **3,437,076** | **3.44 ms** | **152,430 B** | **1,093** |
| | | | | | | |
| **BlockPipeline (Keys=1000)**| 1 | 100 | 11,473,883 | 11.47 ms | 1,524,092 B | 8,424 |
| | 2 | 100 | 11,652,542 | 11.65 ms | 1,515,403 B | 8,422 |
| | 3 | 100 | 10,897,039 | 10.90 ms | 1,524,240 B | 8,422 |
| | 4 | 100 | 10,812,841 | 10.81 ms | 1,513,642 B | 8,423 |
| | 5 | 100 | 11,547,039 | 11.55 ms | 1,539,494 B | 8,424 |
| **Trung bình Pipeline 1000**| — | — | **11,276,668**| **11.28 ms** | **1,523,374 B** | **8,423** |
| | | | | | | |
| **Sync Commit (Keys=100)** | 1 | 219 | 4,803,721 | 4.80 ms | 228,525 B | 1,689 |
| | 2 | 274 | 4,462,208 | 4.46 ms | 173,745 B | 1,689 |
| | 3 | 244 | 4,250,568 | 4.25 ms | 212,699 B | 1,689 |
| | 4 | 261 | 4,250,886 | 4.25 ms | 223,793 B | 1,689 |
| | 5 | 259 | 4,542,533 | 4.54 ms | 207,412 B | 1,689 |
| **Trung bình Sync 100** | — | — | **4,461,983** | **4.46 ms** | **209,235 B** | **1,689** |
| | | | | | | |
| **Sync Commit (Keys=1000)** | 1 | 64 | 17,638,482 | 17.64 ms | 2,159,904 B | 14,357 |
| | 2 | 74 | 18,493,891 | 18.49 ms | 2,136,674 B | 14,360 |
| | 3 | 62 | 18,214,102 | 18.21 ms | 2,244,694 B | 14,359 |
| | 4 | 68 | 17,518,374 | 17.52 ms | 2,170,752 B | 14,357 |
| | 5 | 68 | 18,762,462 | 18.76 ms | 2,120,315 B | 14,359 |
| **Trung bình Sync 1000** | — | — | **18,125,462**| **18.13 ms** | **2,166,468 B** | **14,358** |

### 2.2 Đánh giá hiệu năng Pipeline vs Sync
- Với 100 keys/block: Mô hình Pipeline (`3.44 ms`) nhanh hơn mô hình Sync (`4.46 ms`) **23.0%**.
- Với 1,000 keys/block: Mô hình Pipeline (`11.28 ms`) nhanh hơn mô hình Sync (`18.13 ms`) **37.8%**.
- **Cơ chế:** Mô hình pipeline `commitWg` chuyển việc chờ flush đĩa sang goroutine nền (`CommitAsync`), cho phép luồng thực thi block kế tiếp tiếp tục xử lý logic mà không bị block hoàn toàn trong thời gian I/O. Khi block kế tiếp đến điểm commit, nó chỉ cần đợi `commitWg.Wait()` nếu đợt flush trước chưa xong.

---

## 3. Kết Quả Kiểm Thử Crash-Recovery 10 Vòng (Kill -9 Dưới Tải)

Lệnh thực thi:
```bash
./execution/scripts/test/test_crash_recovery_nomt.sh /tmp/gate_4val_p06
```

Cụm kiểm thử: 4 validator Mysticeti độc lập (`val0`: 31646, `val1`: 31647, `val2`: 31648, `val3`: 31649).
Tải nền: 1,000 transactions Secp256k1 EIP-1559 mỗi vòng được inject qua kết nối TCP.

| Round | Node bị Kill -9 | PID bị Kill | Trạng thái sau Restart | Exit Code | Sentinel File | Block kiểm tra | Parity Check (Hash & StateRoot) | Kết quả |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| 1 | `val1` | 2873794 | Phục hồi thành công (PID 2881423) | 0 (no 78) | Không có | #17 (`0x11`) | Hash: `0x5005a3d852a0092b...`<br>Root: `0x43b847a1c65caeb9...` | ✅ PASS |
| 2 | `val2` | 2873945 | Phục hồi thành công (PID 2883935) | 0 (no 78) | Không có | #19 (`0x13`) | Hash: `0x14e2724081ee6f98...`<br>Root: `0x191bf330b80934d4...` | ✅ PASS |
| 3 | `val3` | 2874347 | Phục hồi thành công (PID 2886104) | 0 (no 78) | Không có | #21 (`0x15`) | Hash: `0x6012885da6cd6ca3...`<br>Root: `0x301a8794ea008459...` | ✅ PASS |
| 4 | `val1` | 2881423 | Phục hồi thành công (PID 2888270) | 0 (no 78) | Không có | #22 (`0x16`) | Hash: `0xc102ccd3385666bf...`<br>Root: `0x2fcfc15872c6f3c2...` | ✅ PASS |
| 5 | `val2` | 2883935 | Phục hồi thành công (PID 2890696) | 0 (no 78) | Không có | #23 (`0x17`) | Hash: `0xbcba1cce1339fb14...`<br>Root: `0x3d98dfed2c9df0cc...` | ✅ PASS |
| 6 | `val3` | 2886104 | Phục hồi thành công (PID 2893018) | 0 (no 78) | Không có | #24 (`0x18`) | Hash: `0x1880d0db2190a441...`<br>Root: `0x3cf2473e91da3f6c...` | ✅ PASS |
| 7 | `val1` | 2888270 | Phục hồi thành công (PID 2895724) | 0 (no 78) | Không có | #26 (`0x1a`) | Hash: `0x0d489da211196804...`<br>Root: `0x585568a9dff24171...` | ✅ PASS |
| 8 | `val2` | 2890696 | Phục hồi thành công (PID 2898032) | 0 (no 78) | Không có | #28 (`0x1c`) | Hash: `0x06e4043b7852c431...`<br>Root: `0x13222341c56ad52f...` | ✅ PASS |
| 9 | `val3` | 2893018 | Phục hồi thành công (PID 2900156) | 0 (no 78) | Không có | #29 (`0x1d`) | Hash: `0x24cfd3f70b684f98...`<br>Root: `0x76bf41a0001c7422...` | ✅ PASS |
| 10 | `val1` | 2895724 | Phục hồi thành công (PID 2902291) | 0 (no 78) | Không có | #31 (`0x1f`) | Hash: `0xc312a8e7763d98e1...`<br>Root: `0x2b304a2a34a66d2d...` | ✅ PASS |

### 3.1 Chi tiết kiểm tra tính toàn vẹn khi khởi động lại
Mỗi node sau khi bị `kill -9` trong lúc đang ghi NOMT, khi khởi động lại đều trải qua quy trình kiểm tra toàn vẹn khởi động 5 bước (`startup_integrity_check.go`):
1. `CHECK 1/5`: Tải `LastBlock` thành công từ BlockDB.
2. `CHECK 2/5`: Xác minh liên kết chuỗi cha-con (`parentHash`) cho toàn bộ các block đã lưu.
3. `CHECK 3/5`: Ánh xạ số thứ tự block (`BlockNumber→Hash`) khớp chính xác.
4. `CHECK 4/5`: Gốc cây trạng thái tài khoản NOMT (`NOMT account_state root`) khớp 100% với `AccountStatesRoot` ghi trong block header.
5. `CHECK 5/5`: Bộ đếm trạng thái toàn cục (`GEI`, `CommitIndex`, `lastBlockNumber`) đồng bộ.

Kết quả:
- **10/10 vòng** đều vượt qua toàn bộ 5/5 bước kiểm tra mà không có bất kỳ lỗi nào.
- Tệp cờ `/tmp/MTN_INTEGRITY_FAILED` không hề được tạo ra.
- Không có tiến trình nào thoát với mã lỗi 78.
- Parity so sánh Block Hash và StateRoot giữa toàn bộ 4 node đều trùng khớp 100%.

---

## 4. Kết Luận & Phạm Vi Đã Kiểm Chứng
1. **Khôi phục crash tiến trình (Process Crash):** Cơ chế `commitWg` và pipeline commit đảm bảo node có thể chịu được `kill -9` đột ngột trong khi đang nhận tải, khởi động lại sạch sẽ và tiếp tục đồng bộ trạng thái chính xác.
2. **Hiệu năng microbenchmark:** Ở mức độ trie đơn thuần, pipeline giảm thời gian commit từ 4.46ms xuống 3.44ms (100 keys) và từ 18.13ms xuống 11.28ms (1,000 keys).
3. **Bất biến Zero-Fork:** 10/10 lần phục hồi sự cố đều duy trì sự đồng thuận tuyệt đối về block hash và state root trên cả 4 validator.

---

## 5. Giới Hạn Của Các Thí Nghiệm & Việc Chưa Làm

### 5.1 Giới hạn kỹ thuật
1. **`kill -9` không chứng minh được độ bền fsync phần cứng / mất điện:**
   - Lệnh `kill -9` chỉ kết thúc tiến trình người dùng (user space process). Hệ điều hành Linux kernel vẫn duy trì page cache trong RAM và tiếp tục ghi đĩa bình thường sau khi tiến trình chết.
   - Do đó, bài test `kill -9` chỉ kiểm chứng được khả năng phục hồi logic khi crash tiến trình, KHÔNG thay thế được bài kiểm thử mất điện đột ngột (power-loss / sudden reboot).
2. **Microbenchmark không tương đương với TPS toàn hệ thống:**
   - Số liệu 3.44 ms / 11.28 ms là thời gian commit của riêng trie bộ nhớ.
   - TPS toàn hệ thống phụ thuộc vào pipeline mạng, thẩm định chữ ký Secp256k1, thực thi MVM/EVM, đồng thuận Mysticeti và ghi BlockDB/LevelDB. Microbenchmark trie không đủ để tuyên bố năng lực TPS tổng thể nếu không có benchmark end-to-end.

### 5.2 Các hạng mục chưa làm trong báo cáo này (chuyển sang các giai đoạn tiếp theo)
1. **Chưa có bằng chứng syscall fsync:** Chưa có trace `strace` / `bpftrace` chứng minh các syscall `fsync` / `fdatasync` (hoặc io_uring) thực sự được gọi trên đường production (thực hiện ở Giai đoạn 2).
2. **Chưa thực hiện đo B1 end-to-end so sánh 3 cấu hình:** Chưa so sánh đầy đủ 3 build (A: commit `ec6560ce`, B: HEAD, C: HEAD bỏ `commitWg.Wait()`) dưới tải `secp_tps_blast` trên cùng máy (thực hiện ở Giai đoạn 3).
3. **Chưa kiểm thử mất điện thực tế (Power-loss simulation):** Chưa chạy trên môi trường có cắt điện/đĩa ảo no-cache (thực hiện ở Giai đoạn 4).
