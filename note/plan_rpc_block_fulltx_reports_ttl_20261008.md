# Kế hoạch: sửa `eth_getBlockByNumber(fullTx=true)` theo chuẩn Ethereum, làm sạch báo cáo, chạy thí nghiệm TTL — 2026-10-08

Dành cho agent thực hiện. Nền: `dev` local gồm các commit `4c645c86`, `b553e34a`, `a9b50168`, `95928882` (chưa push).
Đọc trước: `AGENTS.md`, `PROJECT_STRUCTURE.md`, `note/plan_fix_nomt_reports_evidence_20261007.md` (mục 0), `note/plan_fuzz_coverage_drift_ttl_20261007.md` (mục 0),
`note/p1_5_compatibility_matrix.md`, `execution/cmd/simple_chain/{rpc_block.go,rpc_transaction.go,backend.go}` (đặc biệt `RPCTransaction` ở `backend.go:59`, `MarshalBlockToMapWithGas` ở `rpc_block.go:96`, `GetTransactionByBlockNumberAndIndex` ở `rpc_block.go:499`),
`execution/scripts/test/evidence/{verify_evidence.py,fuzz_mutation_check.py,mutation_check.sh,run_ttl_experiment.py}`, `note/perf_rss_investigation_20261007.md`, `note/design_bounded_memory_indexes_20261007.md`.

> **Vì sao có kế hoạch này (reviewer đã tự xác nhận):**
> 1. **Lỗi tương thích Ethereum thật:** `MarshalBlockToMapWithGas` (`rpc_block.go:~195-230`) dựng mỗi tx của `eth_getBlockByNumber(…, true)` bằng map tay chỉ gồm
>    `hash, from, to, value, input, nonce, gas, gasPrice, chainId, v, r, s, groupId, transactionIndex`. **Thiếu** `blockNumber`, `blockHash`, `type`, `maxFeePerGas`, `maxPriorityFeePerGas`, `accessList`, `yParity`
>    (và `maxFeePerBlobGas`, `blobVersionedHashes`, `authorizationList` cho tx loại 3/4). Log xác minh Pebble cũng lộ ra: "Target Block: #0" trong khi receipt báo block #8681.
>    Trong khi đó `GetTransactionByHash`/`GetTransactionByBlockNumberAndIndex` dùng struct `RPCTransaction` (đủ trường, theo geth). Hai đường trả dữ liệu KHÁC NHAU cho cùng một tx → client (ethers v6, viem, web3.py) có thể phân loại sai loại tx hoặc lỗi.
> 2. **Độ phủ kiểm tra bằng bằng chứng đã tốt hơn nhiều** (reviewer đo bằng đột biến độc lập, bỏ biến thể tương đương: B1 ~94–96%, durability 100%, RSS ~93–95%), NHƯNG:
>    - tiêu đề commit `4c645c86` và `mutation_check.log` còn ghi "100% catch rate" với **seed 11 là seed reviewer đã dùng lần trước** (cấm theo kế hoạch trước);
>    - ma trận khuyến nghị cấu hình trong báo cáo RSS bị miễn kiểm tra (`unchecked`) nhưng chứa các số ghi **"ĐÃ ĐO THỰC TẾ"** (TPS ~6,107; RSS ~9.8 GB/cụm…): đổi `9.8`→`3.8`, `6.0`→`0.0` vẫn exit 0.
> 3. **Mô tả báo cáo chưa chính xác:** mục kết luận ghi drift RSS "đã được kiểm chứng và giải thích minh bạch" — thực tế mới *ghi nhận*, nguyên nhân CHƯA xác định (INCONCLUSIVE). "Cụm sạch / genesis mới" thực ra là bản sao từ template có state sẵn
>    (sau 200 tx block cao #8681; mỗi node ~2,050 MB đĩa và ~1.8–2.1 GB RSS ngay sau khởi động) — báo cáo phải nói rõ.
> 4. **Thí nghiệm TTL ≥ 45 phút CHƯA chạy** (có `run_ttl_experiment.py`, không có log/kết quả).

---

## 0. QUY TẮC CHỐNG BÁO CÁO GIẢ (tuyệt đối bắt buộc — kế thừa, KHÔNG nới lỏng)

1. **Không có file thô trong repo (hoặc sha256 + `stored_at` trong manifest) => không có con số.** Không gõ tay, không làm tròn, không dựng lại số từ báo cáo cũ.
2. **Ba trạng thái PASS / FAIL / INCONCLUSIVE.** Thiếu dữ liệu/không đạt tiêu chí ghi trước => INCONCLUSIVE/FAIL. Ngưỡng ghi trước, không chỉnh sau khi thấy kết quả.
3. **Cấm "dạy theo đề".** Không dùng lại các seed đã công bố (11, 31, 777, 2026) làm bằng chứng nghiệm thu; reviewer sẽ chọn seed mới. Cấm các cụm "AIRTIGHT", "100% caught", "100% coverage" trong tiêu đề commit, log và báo cáo — chỉ ghi số đo thật (k/n, seed, danh sách lọt).
4. **Mọi PASS phải có điều khiển âm chạy thật, lưu log** (kiểm tra thật sự bắt được lỗi cố ý).
5. **Không so sánh khác điều kiện;** xen kẽ thứ tự; nêu cờ/cấu hình; nêu rõ cluster dựng từ template nào và state ban đầu.
6. **Cấm khẳng định tuyệt đối** ("100%", "hoàn toàn", "tuyệt đối", "chứng minh", "không hề", "0%") thiếu `evidence:<id>` + phạm vi đo ngay cạnh — kể cả trong tiêu đề commit, log, tài liệu thiết kế.
7. **Tự kiểm chéo cuối:** chạy lại độc lập ≥ 1 lượt mỗi hạng mục quan trọng bằng đúng lệnh trong manifest; nêu rõ cái KHÔNG chạy lại được.
8. **Quy tắc vận hành:**
   - Zero-fork (AGENTS.md 2.5): không timeout/sleep để quyết định dispatch commit; thiếu bằng chứng => PENDING.
   - Cô lập: chỉ cluster local cổng 31xxx trong scratchpad; KHÔNG đụng 231/230, `/opt/metanode`, tiến trình của user (máy có ~9 tiến trình `/opt/metanode`; đo theo PID env test, không dùng `ps -C simple_chain`); dừng env bằng `run_env.sh <BASE> stop`.
   - Không push, không merge, không đóng issue, không sửa `portal/`, không đụng mật khẩu/token; commit local theo TÊN FILE (không `git add <dir>`; `*.py` gitignore => `git add -f`); cuối commit `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
   - TUYỆT ĐỐI không tắt fsync NOMT; thí nghiệm sửa hằng số/vendor chỉ ở scratchpad (KHÔNG commit) và phải hoàn nguyên + chứng minh bằng `git diff` rỗng.
   - Sau mỗi thay đổi code: `gofmt`, `go vet`, `go test -race` cho package đã sửa, `cd consensus/metanode/scripts && ./build_check.sh` sạch (0 lỗi/0 warning).
   - DỪNG và hỏi user nếu: cần đổi đường commit/consensus; cần `sudo`/máy ngoài; thí nghiệm dài sẽ chạy chồng với tiến trình của user; thay đổi RPC có thể ảnh hưởng thực thi.
   - Code comment tiếng Anh; cuối mỗi phản hồi có khối "📋 Tóm tắt thay đổi" tiếng Việt (AGENTS.md Phần 5); cập nhật `PROJECT_STRUCTURE.md` khi đổi cấu trúc.

---

## GIAI ĐOẠN 1 — Sửa `eth_getBlockByNumber/Hash(fullTx=true)` trả tx đủ trường theo chuẩn Ethereum (làm trước)
Đây là sửa lớp RPC (chỉ đọc), KHÔNG được ảnh hưởng thực thi/consensus. Nếu phát hiện ngược lại thì DỪNG và hỏi user.
### 1.1 Phân tích trước khi sửa (ghi vào `note/rpc_block_fulltx_analysis_20261008.md`)
1. Liệt kê MỌI đường trả đối tượng giao dịch: `GetTransactionByHash`, `GetTransactionByBlockNumberAndIndex`, `GetTransactionByBlockHashAndIndex`, `GetBlockByNumber`, `GetBlockByHash`, `eth_pendingTransactions` (nếu có), subscription `newPendingTransactions` (full), `eth_getBlockReceipts`… Với mỗi đường: nó dựng tx bằng `RPCTransaction` hay map tay; trường nào có/thiếu (bảng so sánh, kèm `file:dòng`).
2. Chuẩn tham chiếu: cấu trúc `eth_getBlockByNumber(fullTx=true)` của go-ethereum (`blockHash`, `blockNumber`, `from`, `gas`, `gasPrice`, `maxFeePerGas`/`maxPriorityFeePerGas` cho type 2/3/4, `hash`, `input`, `nonce`, `to`, `transactionIndex`, `value`, `type`, `accessList` (type 1/2/3/4), `chainId`, `v`, `r`, `s`, `yParity` (type ≥1), blob/authorization fields cho type 3/4). Nêu rõ `gasPrice` của tx type 2 theo geth = effective gas price (xem `EffectiveGasPrice()` trong repo, ADR D2) và đối chiếu với `GetTransactionByHash` đang trả.
3. Xác định nguồn dữ liệu `groupId`/`transactionIndex` (receipt trie) và vì sao `blockNumber/blockHash` đang bị bỏ (có lý do thiết kế không?).
### 1.2 Sửa
1. Dùng CHUNG một hàm dựng `RPCTransaction` (hoặc hàm dùng chung được rút ra từ `GetTransactionByBlockNumberAndIndex`) cho cả `fullTx=true`, để hai đường luôn trả cùng một tập trường. Giữ các trường mở rộng hiện có (`groupId`) nếu client nội bộ đang dùng (grep các tool/portal/dashboard dùng `groupId`); không xoá trường đang có.
2. Bảo đảm: `blockNumber`, `blockHash`, `transactionIndex` điền từ block đang marshal (không gọi thêm lần đọc DB O(N) mỗi tx — dùng dữ liệu đã có trong vòng lặp/ `blockGasCache`; đo trước/sau cho block 1.000 và 5.000 tx).
3. Không đổi hành vi `fullTx=false`.
### 1.3 Kiểm thử (bắt buộc, có điều khiển âm)
1. Test đơn vị bảng cho từng loại tx (Legacy, EIP-2930, EIP-1559, EIP-4844, EIP-7702) và cả tx hệ thống/BLS node-identity: mọi trường chuẩn có mặt, đúng giá trị, đúng kiểu hex (kiểm bằng JSON schema nhỏ hoặc so với `RPCTransaction` từ `GetTransactionByHash` cho cùng tx: hai kết quả phải KHỚP trên tập trường chuẩn).
2. Điều khiển âm: bỏ `blockNumber` khỏi bản sửa (ở scratchpad) → test phải đỏ.
3. Test hiệu năng: `MarshalBlockToMapWithGas` với block 5.000 tx — không tăng đáng kể (ghi tiêu chí ghi trước, ví dụ ≤ 20% so với trước); log số đo thật.
4. Kiểm thử client thật trên cluster cô lập (cổng 31xxx), theo cách đã dùng ở `note/p1_5_compatibility_matrix.md`: ethers v6 `provider.getBlock(n, true)`, viem `getBlock({includeTransactions:true})`, web3.py `get_block(n, True)`, `cast block`: parse thành công, `tx.blockNumber == block.number`, `tx.type` đúng, `tx.hash` khớp receipt. Lưu output thô vào `note/evidence/rpc_block_fulltx_20261008/` kèm manifest; client nào không chạy được (thiếu Node/Python lib) → ghi INCONCLUSIVE cho client đó, KHÔNG bịa.
5. `go test -race ./cmd/simple_chain` (≈120 s), `build_check.sh` sạch.
**Xong khi:** các đường trả tx cho cùng một tx cho cùng tập trường chuẩn; test + điều khiển âm xanh/đỏ đúng; có kết quả client thật (hoặc INCONCLUSIVE có lý do); không có thay đổi nào ảnh hưởng thực thi.

## GIAI ĐOẠN 2 — Làm sạch báo cáo và công cụ kiểm tra
1. **Bỏ khẳng định tuyệt đối khỏi tiêu đề/ log:** sửa `mutation_check.sh` và `note/evidence/mutation_check.log` (không còn "100%/AIRTIGHT"); log ghi seed, số token, k/n bắt được, danh sách lọt. Không đổi lịch sử git (không `--amend` commit đã có); thêm commit mới sửa phần file.
2. **Ma trận khuyến nghị cấu hình RSS:** tách thành hai bảng: (a) "Đã đo" — mỗi số có `evidence:<id>` + extractor đọc từ CSV/log (TPS ~6,107, RSS ~9.8 GB/cụm…), đưa ra khỏi `unchecked`; (b) "Suy luận, chưa đo" — giữ nhãn rõ ràng và có thể giữ trong `unchecked`. Thêm test chứng minh đổi `9.8`→`3.8` ở bảng (a) làm công cụ exit ≠ 0.
3. **Sửa mô tả drift:** "đã ghi nhận; nguyên nhân CHƯA xác định (INCONCLUSIVE)", thay cho "giải thích minh bạch".
4. **Nói rõ nguồn gốc "cụm sạch":** mọi báo cáo hiệu năng dùng cluster từ template (`TEMPLATE_BASE`, `/tmp/gate_4val_clean_template` hoặc tương đương) phải ghi: số tài khoản genesis, block ban đầu, kích thước đĩa mỗi node, RSS baseline sau khởi động (lấy từ `rss_drift_diagnostic.log`/CSV). Bỏ cụm "genesis mới/hoàn toàn sạch" nếu không đúng.
5. **Khoảng dung sai của công cụ:** ghi tường minh trong `verify_evidence.py` + tài liệu: sai số tương đối cho phép (ví dụ 0.5%) và hệ quả (thay đổi nhỏ hơn dung sai không bị bắt); thử giảm dung sai cho các cột đã in đủ số chữ số (ví dụ 3 chữ số thập phân → so khớp đúng đến chữ số in) và đo lại độ phủ bằng đột biến ngẫu nhiên.
6. **Đo độ phủ độc lập với seed mới:** viết vào log kết quả `fuzz_mutation_check.py --all` với ≥ 3 seed MỚI do agent chọn (không phải 11/31/777/2026); danh sách lọt phải được giải thích (dung sai/ngoài bảng) hoặc khắc phục.
**Xong khi:** không còn cụm khẳng định tuyệt đối trong commit/log/báo cáo; ma trận RSS tách đo vs suy luận và kiểm tra được; mô tả drift/cluster chính xác; fuzz với seed mới ghi số thật.

## GIAI ĐOẠN 3 — Chạy thí nghiệm TTL ≥ 45 phút (đã có runner, CHƯA chạy)
1. **Hỏi user trước khi chạy** nếu máy đang có tiến trình `/opt/metanode` hoặc phiên khác (thí nghiệm kéo dài ~1 giờ, tốn CPU/RAM).
2. Lượt thử ngắn 3–5 phút để kiểm tra runner (lưu log thử nhưng KHÔNG dùng làm kết quả chính). Ghi tiêu chí ghi trước vào báo cáo TRƯỚC khi chạy chính thức: tải cố định; ≥ 45 phút (> `mappingCacheTTL`=30 phút + ≥ 3 chu kỳ prune); "bão hoà" khi hệ số góc `HeapAlloc` sau GC ở cửa sổ ≥ 15 phút sau mốc 30 phút ≤ 5% hệ số góc 30 phút đầu (kèm khoảng tin cậy từ `stats_util.py`); ngược lại "chưa bão hoà".
3. Chạy chính thức; lưu chuỗi thời gian: trích ≤ 10 MB trong git (CSV theo phút + log), bản đầy đủ ngoài git với sha256 trong manifest `external`. Bật pprof cho lượt này (`ENABLE_DEBUG_PPROF=true`) và nêu rõ trong báo cáo.
4. Đối chứng TTL ngắn (tuỳ chọn, scratchpad, không commit); nếu không làm: "chưa có đối chứng TTL".
5. Cập nhật `note/perf_rss_investigation_20261007.md` (đổi mô tả "unbounded" thành theo kết quả: "chặn theo TTL"/"chưa bão hoà"/"chưa kết luận") và mục 2.3 của `note/design_bounded_memory_indexes_20261007.md`. Mọi số có extractor; `verify_evidence.py --strict` exit 0.
**Xong khi:** có log ≥ 45 phút trong evidence và kết luận 3 trạng thái theo tiêu chí ghi trước; hoặc ghi rõ INCONCLUSIVE/chưa chạy kèm lý do (đừng bịa).

## GIAI ĐOẠN 4 — Tổng kết, tự kiểm chéo, commit
1. Cập nhật báo cáo theo mẫu: Tiêu chí (ghi trước) → Phương pháp (lệnh y nguyên) → Kết quả (bảng có `evidence:<id>`) → Giới hạn → Chưa làm → "Thay đổi so với bản trước".
2. Chạy `python3 -I execution/scripts/test/evidence/verify_evidence.py --strict note/evidence/<từng-thư-mục>`: tất cả exit 0. Chạy `fuzz_mutation_check.py --all` với seed mới; ghi k/n thật.
3. Tự kiểm chéo (mục 0.7): chạy lại ≥ 1 lượt B1, 1 lượt GOGC, 1 test RPC (client thật); ghi đối chiếu số.
4. Cập nhật `PROJECT_STRUCTURE.md` nếu thêm module/script/test mới.
5. Commit theo giai đoạn (mỗi giai đoạn ≥ 1 commit, theo TÊN FILE). Tiêu đề commit KHÔNG chứa "100%". Không push.
6. Báo cáo cuối cho user: danh sách commit; mỗi hạng mục: lệnh + đường dẫn evidence + trạng thái PASS/FAIL/INCONCLUSIVE; việc chưa làm; rủi ro; những gì KHÔNG chạy lại được. Nêu rõ seed đã dùng để reviewer chạy seed khác.

---

## KHÔNG làm (cần user)
Đổi mật khẩu sudo dev trong repo/lịch sử git; thu hồi token GitHub; VM/máy riêng cho power-loss thật và B2; mọi lệnh `sudo`/`drop_caches`/đụng thiết bị khối trên máy của user nếu chưa có văn bản cho phép;
cutover chain 991; bật pipeline commit Rust→Go; **viết/sửa code giới hạn bộ nhớ index khi chưa được user duyệt thiết kế** (`note/design_bounded_memory_indexes_20261007.md` vẫn DRAFT chờ duyệt); chạy thí nghiệm TTL đè lên tiến trình của user khi chưa hỏi; push/merge/đóng issue.

## Tiêu chí hoàn thành toàn kế hoạch
- `eth_getBlockByNumber/Hash(fullTx=true)` trả tx cùng tập trường chuẩn với `eth_getTransactionByHash`; test + điều khiển âm; kết quả client thật hoặc INCONCLUSIVE có lý do; không ảnh hưởng thực thi/consensus.
- Không còn khẳng định tuyệt đối trong tiêu đề commit/log/báo cáo; ma trận RSS tách "đã đo" (kiểm tra được) và "suy luận"; mô tả drift/cluster chính xác.
- Độ phủ đo bằng ≥ 3 seed mới, kết quả thật được ghi kèm danh sách lọt đã giải thích.
- Có log TTL ≥ 45 phút (hoặc INCONCLUSIVE có lý do) và tài liệu được cập nhật theo kết quả.
- Báo cáo cuối nêu rõ mọi mục INCONCLUSIVE thay vì cố làm cho "xanh".
