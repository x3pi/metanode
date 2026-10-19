# Đánh giá hệ thống MetaNode: điểm mạnh và điểm yếu (2026-10-19)

Tài liệu này là bản đánh giá tổng thể tại commit `dev` ngày 2026-10-19. Nó không thay thế `CLAIMS_LEDGER.md`: mọi số hiệu năng ở đây được trích từ ledger, không đo lại.

Nhãn bằng chứng (theo `AGENTS.md` Part 2.6):

- `[ĐO]`: số lấy từ lệnh chạy hoặc từ `CLAIMS_LEDGER.md` (kèm nguồn).
- `[ĐỌC CODE]`: quan sát tĩnh từ mã nguồn hoặc tài liệu.
- `[GIẢ THUYẾT]`: suy luận chưa kiểm chứng.
- `[CHƯA ĐO]`: chưa có điều kiện đo.

---

## 1. Hệ thống là gì

Một L1 kiểu consortium (validator có đăng ký, BFT) với execution tương thích Ethereum:

- **Consensus:** Rust Mysticeti DAG-BFT (`consensus/`), có thêm chế độ Raft (`pkg/rollup/raftfeed`).
- **Execution:** Go, thực thi song song kiểu Block-STM, nhóm giao dịch tất định bằng Union-Find (`execution/`).
- **State:** NOMT (Merkle trie, FFI) cùng PebbleDB; máy ảo MVM và full-text Xapian bằng C++.
- **Kết nối:** một tiến trình duy nhất, Rust và Go nối với nhau qua CGo FFI.
- **Mở rộng:** Parent Chain (đăng ký chain/account), rollup Raft, cross-chain.

Quy mô `[ĐO]` (đếm dòng các file được git theo dõi, không tính `vendor/`, `node_modules/`, `*.pb.go`): Go ≈ 266.818 dòng, Rust ≈ 117.181, C++ ≈ 25.670, Solidity ≈ 9.580, Python ≈ 14.356.

---

## 2. Điểm mạnh

### 2.1. Triết lý an toàn nhất quán: "thà pending chứ không fork"
- `[ĐỌC CODE]` `AGENTS.md` Part 2.5: cấm dùng timeout để quyết định dispatch commit; commit chỉ được chuyển sang Go khi có bằng chứng 2f+1 digest từ peer (`CommitVoteMonitor`, `PeerAttestResult`).
- Cách làm này đã được dùng để tìm và sửa lỗi thật: 3 lần fork thật trên cụm live đã được sửa và verify (ghi trong memory dự án, mục "Phuong an A").
- Có chaos test và kill -9.

### 2.2. Một tiến trình, không IPC giữa consensus và execution
- `[ĐỌC CODE]` Rust gọi Go qua FFI callback; đã bỏ mô hình Master/Sub (`execution/EXECUTION_GUIDE.md`).
- `[GIẢ THUYẾT]` Độ trễ commit → execution thấp hơn mô hình hai tiến trình. Chưa đo so sánh.

### 2.3. Thực thi song song tất định
- `[ĐỌC CODE]` Ba đường thực thi: `native_fast_path.go` (block thuần native transfer), `true_block_stm.go` (`TrueBlockSTM`), `block_stm.go` (`ProcessTransactionsOptimistic`).
- `[ĐO]` Ledger dòng 1–2: BFT burst 25k ≈ 7,7–8,0k tx/s (n=7); BFT sustained 60 s ≈ 14,07k tx/s, tái lập được với mã cũ (13,4k đầu-cuối).
- `[ĐO]` Ledger dòng 12: Raft burst ≈ 13,1k tx/s.
- Tất cả là một máy, 4 validator cùng máy (xem 3.1).

### 2.4. Tương thích Ethereum khá sâu
- `[ĐỌC CODE]` Hỗ trợ EIP-1559, 2930, 4844, 7702; envelope secp256k1; `hash = keccak256(raw_envelope)` sau cutover (ADR D3); JSON-RPC chuẩn và `eth_getProof`.
- Đợt hardening EVM riêng (EIP-4844/7702, intrinsic gas, state journal, SSTORE/refund/CREATE2/SELFDESTRUCT) đã được commit và verify live (memory dự án).

### 2.5. State có xác thực
- `[ĐỌC CODE]` NOMT cho state root và Merkle proof; fallback Pebble khi NOMT lỗi.
- `[ĐO]` Ledger dòng 10: test Go của fallback pass (phạm vi test, chưa chạy nhiều máy).

### 2.6. Quy trình bằng chứng nghiêm túc
- `[ĐO]` `CLAIMS_LEDGER.md` có 20 dòng RE-VERIFIED, 3 EXPLAINED, 1 RESOLVED, 3 OPEN.
- Có preregistration, hash binary, evidence bundle kiểm bằng `execution/scripts/test/evidence/verify_evidence.py`.
- Quy trình đã bắt được số bị thổi phồng: công cụ đo cũ cao hơn số đếm thật khoảng 4,9% ở sustained (dòng 3); số 129.345 và 74.067 tx/s bị thu hẹp thành micro-benchmark (dòng 4).

### 2.7. Phạm vi kiểm thử rộng
- `[ĐO]` 1760 hàm `func Test*` trong Go, 563 thuộc tính `#[test]`/`#[tokio::test]` trong Rust (không tính vendor). Đây là số đếm, không phải tỷ lệ pass.
- `[ĐO]` CI (`go-ci.yml`) chạy khoảng 12,5 phút và pass trên 3 PR dependabot ngày 2026-10-19 (check-run `go-build-test`).

### 2.8. Cơ chế vận hành
- `[ĐỌC CODE]` Ansible đa cụm, snapshot/restore, watchdog `SIGABRT` kèm supervisor, runbook, bất biến bảo toàn float cross-chain với halt-not-guess (`note/runbook_parent_chain_multinode.md`).

---

## 3. Điểm yếu

### 3.1. Mọi số liệu đều từ một máy
- `[ĐO]` Ledger, mục "Giới hạn": "Một máy, 4 validator (BFT) hoặc 3 node (Raft) cùng máy".
- `[CHƯA ĐO]` Độ trễ mạng thật, nhiều máy, chạy dài (soak), A/B trên môi trường thật.
- `[ĐỌC CODE]` Cụm 3 node không có khả năng chịu lỗi BFT (cần n ≥ 4; `note/bft_fault_tolerance_node_count.md`).

### 3.2. Phức tạp đa ngôn ngữ và ranh giới CGo
- `[ĐO]` 16 file Go dùng `import "C"` (đếm bằng `git grep`).
- `[ĐO]` Ledger dòng 19: `runtime.cgocall` chiếm 29,99% CPU ở 4M tx; 99,33% của nó nằm ở `pkg/goxapian` (64,73%) và `crypto/secp256k1` (34,60%).
- `[ĐỌC CODE]` Lịch sử có SIGABRT, freeze `parking_lot`, panic do con trỏ nil qua FFI, deadlock NOMT session (memory dự án).

### 3.3. Một số nguyên nhân gốc chưa được chứng minh
- `[ĐO]` Ledger dòng 8 (OPEN): nguyên nhân treo NOMT chưa xác định chắc chắn.
- `[ĐO]` Ledger dòng 15 (OPEN): validator tự thoát với `nomt_ffi: failed to commit payload`; 1 lần quan sát, không tái hiện trong các lượt chạy lại.
- `[ĐO]` Dòng 9 và 13 là NOT RE-VERIFIED. Dòng 9 (`recv_timeout(100 ms)`) chỉ là bản vá phòng ngừa, chưa có bằng chứng can thiệp.
- Watchdog tự thoát giảm tác hại nhưng không loại bỏ nguyên nhân.

### 3.4. Hiệu năng giảm khi backlog lớn, GC của Go là điểm nghẽn
- `[ĐO]` Ledger dòng 16: sustained giảm 13,1k (0,5M tx) → 12,2k (1M) → 11,5k (2M) → 10,0k (4M), Spearman −0,91. GC chiếm 11% CPU ở 1M tx và 29–37% ở 4M tx.
- `[ĐO]` Đã có các tối ưu được kiểm chứng (dòng 18–23). Phạm vi vẫn là một máy.
- `[ĐO]` Dòng "Chưa làm" của ledger: Xapian `_Cfunc_database_index_documents_batch_packed` còn 31,83 s CPU ở 4M tx, `[CHƯA ĐO]` khả năng giảm thêm.

### 3.5. Nhiều đường thực thi phải giữ tương đương nhau
- `[ĐỌC CODE]` Ba pipeline lặp lại luật nonce, phí và gas.
- Hai vòng audit (round 2 và 3, tháng 10/2026) tìm ra các lỗi thật thuộc loại "lệch giữa các đường", đã sửa: receipt báo gas chưa bị trừ làm leader nhận thưởng từ hư không; nonce không được tiêu cho tx account-setting lỗi; `senderExisted` luôn đúng vì `AccountState()` không bao giờ trả `nil`.
- `[GIẢ THUYẾT]` Có thể còn lỗi cùng loại chưa được tìm ra. Chưa có differential test giữa ba đường.

### 3.6. Bề mặt tấn công
- `[ĐỌC CODE]` Giao thức TCP peer không xác thực (`InitConnection`) vẫn mở.
- Đã đóng trong cùng đợt: `mtn_sendCrossChainTransfer` (khóa devnet công khai), `/pipeline/reset`, `eth_verifyHistoricalRoot`, `eth_getProof` lịch sử không giới hạn. Việc các lỗi này tồn tại cho thấy bề mặt RPC từng thiếu rà soát.
- `[ĐO]` Đếm bằng grep: 263 `.unwrap()`/`.expect(` trong `consensus/metanode/src`, 19 `unsafe`, 81 `panic(` trong Go không phải test, 91 TODO/FIXME. Chưa phân loại bao nhiêu trong đó thật sự nguy hiểm.

### 3.7. Thiết kế kinh tế và quản trị còn đơn giản
- `[ĐỌC CODE]` ADR D2: phí phẳng `MINIMUM_BASE_FEE = 100000` wei (`execution/pkg/common/constant.go`), không có fee market, phí chuyển hết cho leader, không đốt.
- Phase 5 (kinh tế an ninh, slashing) còn mở theo thiết kế (`note/cross_chain/shard_design_ton_real.md`). Chuỗi chết mà không có double-sign thì tiền bị khóa (đã chấp nhận).
- Có khái niệm free-fee address do cấu hình, là một điểm tin cậy tập trung.

### 3.8. Lịch sử state và nâng cấp
- `[ĐỌC CODE]` NOMT chỉ giữ state mới nhất. Proof cho block cũ phải dựng lại từ changelog với chi phí tỉ lệ số account (`eth_getProof` lịch sử). Đã giới hạn đồng thời và kiểm tra root nhưng chi phí dựng lại không đổi.
- Pruning và self-heal bằng Checkpoint bị hoãn vì Checkpoint từng gây treo hệ thống (memory dự án).
- Mọi lần đổi luật thực thi trong đợt này đều yêu cầu deploy đồng loạt tất cả validator; cụm 231/230 có dữ liệu cũ không replay được.
- `[CHƯA ĐO]` Chưa kiểm tra có cơ chế kích hoạt theo block height hay không.

### 3.9. Quy trình phát triển
- `[ĐỌC CODE]` `dev` nhận commit trực tiếp (admin bypass), không qua review PR.
- Lịch sử từng có audit pack giả (đã sửa) và benchmark do agent báo sai (memory dự án).
- Tài liệu từng bị lỗi thời nặng: ngày 2026-10-19 đã gỡ hơn 8.700 dòng tài liệu cũ (commit `3c7ed581`).
- Một số test CI còn flaky (raftfeed).
- `[GIẢ THUYẾT]` Phụ thuộc nhiều vào một nhóm nhỏ, có rủi ro bus factor.

---

## 4. Mức độ phù hợp
`[GIẢ THUYẾT]` Phù hợp mạng consortium hoặc doanh nghiệp, hoặc rollup sequencer với committee nhỏ, đã biết danh tính. Chưa phù hợp mạng public không cần cấp phép, vì chưa có kinh tế an ninh, slashing và đo đạc quy mô lớn.

## 5. Thứ tự ưu tiên trước production (đề xuất)
1. Chạy `build_check.sh` và kiểm chứng nhiều máy cùng soak test dài (3.1).
2. Xác thực giao thức TCP peer (3.6).
3. Đóng hai lỗi OPEN (ledger dòng 8 và 15) bằng thí nghiệm can thiệp thay vì dựa vào watchdog (3.3).
4. Thêm differential test giữa ba đường thực thi: cùng input, so state root (3.5).
5. Chốt cơ chế nâng cấp luật theo block height và kế hoạch xử lý dữ liệu cũ (3.8).

## 6. Giới hạn của đánh giá này
- Dựa trên phần code và tài liệu tôi đọc được, nên có thể bỏ sót điểm yếu ở phần chưa đọc (MVM C++, nhiều crate Rust).
- Số đếm (dòng code, test, `unwrap`) chỉ cho thấy quy mô, không đo chất lượng.
- Số TPS trích từ ledger là một máy, không ngoại suy cho nhiều máy.
- Các mục `[GIẢ THUYẾT]` cần đo hoặc kiểm tra thêm trước khi dùng để ra quyết định.

## 7. Cách tái lập số đếm
```bash
# Dòng code theo ngôn ngữ (thay <ext> bằng go|rs|cpp|sol|py)
git ls-files "*.<ext>" | grep -v -E '(^|/)vendor/|node_modules|\.pb\.go$' | xargs cat | wc -l
# Hàm test
git grep -h -E '^func Test' -- '*_test.go' | wc -l
git grep -h -E '#\[(tokio::)?test' -- '*.rs' ':!consensus/vendor' | wc -l
# Rủi ro tĩnh
git grep -h -E '\.unwrap\(\)|\.expect\(' -- consensus/metanode/src | wc -l
git grep -h 'panic(' -- 'execution/*.go' ':!*_test.go' | wc -l
git grep -l 'import "C"' -- 'execution/*.go' | wc -l
```
