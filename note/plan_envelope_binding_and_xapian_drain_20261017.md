# Kế hoạch giao agent: kiểm chứng và (nếu đáng) giảm chi phí kiểm tra envelope lặp, tăng gom lô Xapian, hoàn tất bộ đo A/B 4M (2026-10-17)

**Người giao:** agent trước (Claude), theo yêu cầu của chủ dự án. **Người thực hiện:** agent khác. **Quy tắc tối cao:** `AGENTS.md` Phần 2.5 (Zero-Fork) và Phần 2.6 (chống bịa số liệu). Không có file thô thì không có số. Việc nào không chạy được: ghi `[CHƯA ĐO]` kèm lý do, không điền số.

## 0. Hiện trạng đã kiểm tra (đọc trước, đừng làm lại)

Mình (agent trước) đã mở lại các file profile bằng `/tmp/p06_bins/pprof` và đối chiếu mã trên `origin/dev` (`09b4d706`). Mọi số dưới đây có thể tái tạo bằng lệnh trong cột "Cách kiểm lại".

Thư mục bằng chứng: `note/evidence/keccak_blockcache_xapian_batch_20261017/ab_4m/` (gọi tắt `$D`), lượt `ab_4m_next_p1_run1_val0_*`.

| Phát hiện | Nhãn | Cách kiểm lại |
|---|---|---|
| CPU tổng 274,69 s; `ValidateEnvelopeBindingsParallel.func1` 44,20 s (16,09%); `validateDirectEnvelopeBinding` 41,35 s (15,05%); `core/types.Sender` 36,39 s; `secp256k1_ext_ecdsa_recover` 33,02 s (12,02%) | [ĐO] | `pprof -top -cum $D/..._cpu30.pb.gz` |
| `_Cfunc_database_index_documents_batch_packed` 31,83 s (11,59%); `_Cfunc_database_index_document` (gọi đơn) 9,54 s (3,47%) | [ĐO] | cùng file |
| **Người gọi `ValidateEnvelopeBinding`:** 39,05 s từ `ValidateEnvelopeBindingsParallel` (chuẩn bị khối), 3,87 s từ `evalTxSignature`, **chỉ 0,36 s từ `VerifyTransaction` (admission)** | [ĐO] | `pprof -peek 'transaction.ValidateEnvelopeBinding$'` |
| Heap `inuse_objects`: `convertRawEth` (cả cây con) 5.756.383 object (62,31%) và 330,57 MB (36,60% `inuse_space`); riêng `NewTransactionFromEth` 2.211.841 object (23,94%) và 189,06 MB (20,93%); `Transaction.UnmarshalBinary` 2.026.239 object (21,93%) | [ĐO] | `pprof -sample_index=inuse_objects -top -cum $D/..._heap.pb.gz` |
| Allocs: `validateProtoEnvelopeBinding` 8.437,48 MB (6,52% `alloc_space`); `ValidateEnvelopeBindingsParallel.func1` 7.273,16 MB (5,62%) | [ĐO] | `pprof -sample_index=alloc_space -top -cum $D/..._allocs.pb.gz` |
| `defaultNumShards = 32`, `maxWorkerBatchDrain = 64` | [ĐỌC CODE] | `execution/pkg/explorer/service.go:25`, `:271` |
| Bộ chuyển đổi raw-ETH **đang dùng thật** là `App.ConvertRawEthTxToMetaTx` (`execution/cmd/simple_chain/eth_tx_converter.go:28`), không phải `defaultRawEthConverter` (chỉ là dự phòng, `processor/transaction_processor.go:248`) | [ĐỌC CODE] | `app.go:869` gọi `SetRawEthConverter(app.ConvertRawEthTxToMetaTx)` |
| `NewTransactionFromEth` gọi lại `MarshalBinary()` để dựng `RawEnvelope` (`execution/pkg/transaction/transaction.go:~171-176`) | [ĐỌC CODE] | đọc hàm |
| Admission (`VerifyTransaction`) **đã** ghi `boundSigKey` sau `ValidEthSign` ở nhánh secp (`execution/pkg/blockchain/tx_processor/validation.go:~250-258`) và bỏ qua binding nếu `IsVerifiedEnvelopeBound` (`:~179`) | [ĐỌC CODE] | đọc hàm |
| Bộ 4M mới có **1 cặp** (`p1`): TPS đầu-cuối `prev` 15.096, `next` 15.763, loss 0 cả hai; **n = 1 mỗi nhánh, không có thống kê** | [ĐO]/[TRÍCH] | `$D/ab_4m_{prev,next}_p1_summary.csv` |

### Chỉnh lại các nhận định trong ghi chú đề xuất trước (đã đối chiếu, không thể giữ nguyên)
1. "Quên `RecordVerifiedEnvelopeBound` ở mempool" **không đúng một phần**: admission đã ghi cache bound ở nhánh secp, và `ValidateEnvelopeBinding` từ admission chỉ tốn 0,36 s so với 39,05 s ở chuẩn bị khối [ĐO]. Phần lớn chi phí nằm ở tx mà validator này **chưa từng thấy ở admission** (đến từ DAG của validator khác). Validator **không được tin** kết quả verify của peer (Zero-Fork, Byzantine), nên phần này là chi phí bắt buộc, không phải "lặp thừa". Lợi ích của việc ghi cache ở ingress chỉ áp dụng cho tx nhận trực tiếp.
2. "5.756.383 object thuộc `NewTransactionFromEth` + `UnmarshalBinary`" **sai chỗ gán**: con số đó là cả cây con `convertRawEth` (62,31%). `NewTransactionFromEth` là 23,94%, `UnmarshalBinary` 21,93% [ĐO].
3. "8.437,48 MB là `ValidateEnvelopeBindingsParallel`" **lệch tên hàm**: con số thuộc `validateProtoEnvelopeBinding`; `ValidateEnvelopeBindingsParallel.func1` là 7.273,16 MB [ĐO].
4. Nhánh `!pol.secp` ở `validation.go` (thiếu cache `ValidEthSign`) là đường **chết**: `config.go` từ chối `bls_legacy`, nên `secp` luôn bật ở cấu hình hợp lệ. **Không khôi phục.**
5. Hai việc dọn sau PR #173 (xóa `&& as.AccountType() == 0`; đưa `ValidMaxGas`/EIP-7702 lên trước `ValidAmount`) **đã xong** và đã merge (`f67af8e6` trên `origin/dev`). Đừng làm lại.
6. Commit `09b4d706` **đã nằm trên `origin/dev`**; không còn việc "push 09b4d706".
7. Giả thuyết Poisson ("khoảng 27% shard chỉ có 1 doc") là [TÍNH] lý thuyết giả định hàng đợi luôn đầy ≥ 64 job và băm đều; chưa có số đo phân bố thật. Xem Việc 3.

## 1. Quy tắc bắt buộc (mọi việc)
- Đọc `AGENTS.md`, `PROJECT_STRUCTURE.md`. Trước khi sửa mã node: phân tích blast radius (codegraph hoặc `grep -rn`), ghi ở đầu báo cáo. Sau sửa: `consensus/metanode/scripts/build_check.sh` sạch lỗi và cảnh báo (kiểm exit code + log).
- Nhãn `[ĐO]` (kèm đường dẫn file thô) / `[TÍNH]` (kèm công thức) / `[ĐỌC CODE]` (kèm `file:dòng`) / `[TRÍCH]` / `[GIẢ THUYẾT]` / `[CHƯA ĐO]`. Không từ tuyệt đối khi không có `evidence:<id>`.
- **Preregistration:** trước mọi lượt đo, ghi `PREREGISTERED.md` (giả thuyết, số cặp, tiêu chí đạt/không, thống kê) **và** commit hash + `sha256sum` của hai bộ binary. Nếu hai binary giống hệt nhau hoặc không khớp commit → dừng, kết quả vô hiệu. Không đổi tiêu chí sau khi thấy số.
- A/B chỉ hợp lệ khi **ghép cặp, xen kẽ ngẫu nhiên có seed, cùng một đợt**; luôn có đối chứng A/A. Số TPS luôn kèm khối lượng tx và chế độ đo. Không cherry-pick: báo cả kết quả không đạt (`p ≥ 0,05`) hoặc xấu đi.
- Cô lập: chỉ cổng 31xxx; không đụng `/opt/metanode`, cụm 231/230, tiến trình người khác; không `sudo`; không `pkill -f`. Kiểm `ss -ltn | grep -c ":31[0-9][0-9][0-9]"` = 0 trước mỗi lượt; `MemAvailable` ≥ 20 GB, `/tmp` trống ≥ 100 GB.
- Binary phải là **bản sao thật** (không symlink); `cp -a` cho dữ liệu NOMT; profile dùng `/tmp/p06_bins/pprof` (`go tool pprof` lỗi trên máy này).
- Không push/merge/tạo PR khi chưa có lệnh của chủ dự án. `git add` theo tên file (cây làm việc dùng chung). Commit có dòng `Co-Authored-By` theo hướng dẫn phiên.
- Sau khi xong: manifest bằng `execution/scripts/test/evidence/make_manifest.py`; `verify_evidence.py <dir> --report <report.md> --strict` và `claims_lint.py <report.md>` phải qua; cập nhật `note/CLAIMS_LEDGER.md` đúng trạng thái. Báo cáo có mục "Giới hạn" và "Chưa làm".
- Mỗi phản hồi kết thúc bằng khối "📋 Tóm tắt thay đổi" tiếng Việt (`AGENTS.md` Phần 5).

## 2. Công cụ có sẵn
- Runner: `execution/scripts/test/evidence/run_tps_counted.py` (`--mode sustained`, `--runs`, `--tag`, `--bins`, `--out`, `--max-txs`, `--batch`, `--profile-at`). Driver mẫu: `note/evidence/keccak_blockcache_xapian_batch_20261017/driver_ab.py` (đã có kiểm tài nguyên và dọn log lớn).
- Thống kê: `execution/scripts/test/evidence/stats_util.py` (`welch_t_test`, `t_two_tailed_p`, `t_critical`).
- Build: `cd execution && CGO_ENABLED=1 go build -o <dir>/simple_chain ./cmd/simple_chain` (thư viện Rust đã build ở `target/release`).

## Việc 0 — Hoàn tất bộ đo 4M đang dở (làm trước, không sửa mã node)
Bộ `ab_4m` chỉ có cặp 1 (n = 1 mỗi nhánh), nên chưa kết luận được gì về `prev` so với `next`.
1. Đọc `PREREGISTERED.md` hiện có trong thư mục bằng chứng; **giữ nguyên tiêu chí**. Nếu thiếu `sha256sum` hoặc binary không khớp commit: lập lại binary từ commit đã ghi và ghi lại trước khi chạy.
2. Chạy nốt cặp 2–4 ở 4 triệu tx với cùng seed/thứ tự đã đăng ký (xem `order.txt`). Bắt buộc mọi lượt `loss == 0` và `parity_status` bắt đầu bằng `OK:`; lượt nào không thỏa thì báo, không loại.
3. Báo cáo: trung bình, SD, t-test ghép cặp, và đối chiếu với mục tiêu đã đăng ký (ví dụ `NewExecuteSCResult` heap giảm về ≤ 50 MB / < 500k object).

## Việc 1 — Giai đoạn A: đo trước khi sửa envelope binding (chỉ đọc + thêm bộ đếm tạm)
**Câu hỏi:** bao nhiêu phần `ValidateEnvelopeBinding` ở chuẩn bị khối là tx đã được admission cục bộ ghi `bound`, và bao nhiêu là tx từ validator khác?
1. Thêm bộ đếm tạm (không đổi hành vi; chỉ trên nhánh thí nghiệm): ở `ValidateEnvelopeBindingsParallel` đếm `IsVerifiedEnvelopeBound(tx)` đúng/sai trên mỗi khối. Chạy 1 lượt sustained 4 triệu tx, ghi tổng đúng/sai. Nếu tỉ lệ "sai" gần bằng phần tx không đi qua admission cục bộ thì xác nhận chi phí là bắt buộc.
2. Ghi rõ client đo (`secp_tps_blast`) gửi tới validator nào (một hay cả bốn) vì điều này quyết định tỉ lệ tx "cục bộ".
3. **Cổng quyết định:** nếu phần tx *cục bộ mà vẫn bị verify lại* nhỏ (đề xuất ngưỡng, có thể chỉnh trước khi đo: < 5% thời gian của `ValidateEnvelopeBindingsParallel`), **dừng** hạng mục sửa mã ở Giai đoạn B và báo lại "không đáng". Không sửa chỉ vì giả thuyết.

## Việc 1 — Giai đoạn B: chỉ làm nếu qua cổng ở trên (sửa mã node, cần chủ dự án duyệt thiết kế)
Ứng viên, theo thứ tự rủi ro tăng dần. Mỗi ứng viên: blast radius, rủi ro Zero-Fork, test đơn vị, A/B.
- **B1. Tránh `MarshalBinary()` lặp khi dựng `RawEnvelope`** (`NewTransactionFromEth`). **Rủi ro hash:** theo ADR D3 hash giao dịch = `keccak256(RawEnvelope)`. Dùng nguyên `rawEth` của client chỉ an toàn nếu `rawEth` **bằng đúng byte** `ethTx.MarshalBinary()` (RLP không chuẩn có thể giải mã được nhưng mã hóa lại khác). Yêu cầu: test cho mọi loại tx (legacy, 2930, 1559, 4844 không sidecar, 7702) chứng minh bằng nhau, và một test với RLP không chuẩn chứng minh hành vi **không đổi** (vẫn ra hash cũ). Nếu không chứng minh được thì bỏ B1.
- **B2. Ghi `RecordVerifiedEnvelopeBound` ở ingress** sau khi tx vừa dựng từ `ethTx`. **Rủi ro:** cache `boundSigKey` là mô hình hóa một hàm thuần dùng trong bộ lọc đồng thuận; một dương tính giả ở validator này mà không ở validator khác = lệch verdict = fork. Chỉ được ghi khi **chứng minh bằng test** rằng `ValidateEnvelopeBinding(metaTx) == nil` cho mọi tx do `NewTransactionFromEth` dựng (so sánh trực tiếp với kết quả gọi thật, mọi loại tx), và không bao giờ ghi nếu `GetReadOnly()`. Bắt buộc đọc lại `IsVerifiedEnvelopeBound` / `boundSigKey` / chiến lược quay vòng cache (`rotateVerifiedSignatures`) để đảm bảo không có đường ghi sai.
- Không làm: khôi phục nhánh `!pol.secp` (đường chết, xem mục 0).
- A/B (ghi `PREREGISTERED.md` trước): `bins_base` so với `bins_opt`, ≥ 8 cặp xen kẽ ở 4 triệu tx (+ A/A 4 cặp), tiêu chí (chỉnh được *trước* khi chạy): mọi lượt gửi = nhận = commit và parity OK; CPU `ValidateEnvelopeBindingsParallel` + `convertRawEth` giảm có ý nghĩa thống kê; TPS không giảm. Nếu không đạt thì báo "không đạt".

## Việc 2 — Đo phân bố gom lô Xapian rồi mới quyết định `maxWorkerBatchDrain` (64 → 256 là giả thuyết)
Trần lợi ích: nhánh gọi đơn `_Cfunc_database_index_document` là 9,54 s (3,47% CPU) trong 1 profile [ĐO]; thay đổi chỉ có thể cải thiện phần này, và chỉ một phần của nó là chi phí CGo.
1. **Đo, chưa sửa:** thêm bộ đếm tạm (nhánh thí nghiệm) trong `doIndexTransactionsBatch` (`execution/pkg/explorer/service.go`): số job mỗi lần drain, số tài liệu mỗi shard mỗi lần, tỉ lệ shard có đúng 1 tài liệu (nhánh gọi đơn). So kết quả thực với dự đoán Poisson ở mục 0.7.
2. Chỉ nếu tỉ lệ gọi đơn thực sự cao: thử `maxWorkerBatchDrain` = 128 và 256. Đánh đổi cần nêu: giữ khóa shard lâu hơn, độ trễ index tăng, bộ nhớ batch tăng, tương tác với gom commit 64 khối. Đây chỉ là Explorer (không nằm trong đường đồng thuận) nên rủi ro fork thấp, nhưng phải giữ nguyên nội dung chỉ mục (test `explorer_test.go` / `xapian_test.go` phải qua).
3. A/B ≥ 8 cặp 4 triệu tx (+ A/A), `PREREGISTERED.md` trước. Tiêu chí: giảm CPU nhánh gọi đơn và/hoặc tổng CGo, TPS không giảm. Báo cả khi không khác biệt.

## Việc 3 — Dọn dẹp tài liệu bằng chứng
- Cập nhật `note/CLAIMS_LEDGER.md` dòng 23 theo kết quả Việc 0 (không đánh `RE-VERIFIED` khi chưa đủ cặp).
- Không đổi tên/xóa thư mục bằng chứng đã được manifest hoặc ledger tham chiếu.

## Câu hỏi cho chủ dự án (agent thực hiện hỏi trước khi làm)
1. Có chấp nhận dừng ở Giai đoạn A nếu cổng quyết định cho thấy lợi ích nhỏ (khả năng cao theo dữ liệu hiện có)?
2. Có duyệt thiết kế B1/B2 (chạm đường băm/đồng thuận) hay chỉ cho phép tối ưu Explorer (Việc 2)?
3. Nâng cấp node có thay đổi bộ lọc chữ ký (như PR #173) cần nâng đồng thời mọi validator: B2 nếu làm cũng thuộc diện này.
