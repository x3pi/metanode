# Kế hoạch giai đoạn 3: kiểm chứng thật và hoàn thiện Parent Chain

> Lập 2026-10-01 cho agent thực hiện (Gemini). Tài liệu tự đủ. Đọc theo thứ tự: mục 0 (luật) → mục 1 (hiện trạng, đã xác minh) → mục 3 (gói việc).
> Tài liệu nền: `AGENTS.md`, `note/parent_chain_multinode_plan.md` (quyết định Đ1–Đ14, đặc tả tất định), `note/parent_chain_completion_plan.md` (giai đoạn 2), `note/parent_chain_cluster_test_report.md`, `note/runbook_parent_chain_multinode.md`.

## 0. Luật bắt buộc

1. **Trung thực tuyệt đối về kết quả.** Chỉ ghi "PASS" khi chính bạn đã chạy lệnh và thấy đầu ra. Công cụ test từng in "✅ PASSED" trong khi không làm gì (T-I4 chỉ đọc một cờ rồi in "Verified"). Với mỗi test: đọc mã của nó để biết nó **thật sự kiểm tra gì**, và ghi vào báo cáo đúng phạm vi (lệnh, đầu ra thô, số liệu). Test chưa làm thì ghi **CHƯA LÀM**, không ghi PASS.
2. **Zero-fork (AGENTS.md mục 2.5):** thà pending còn hơn fork. Cấm `sleep`/timeout/`Duration` làm cơ sở quyết định dispatch hay thoát deadlock; thoát bằng dữ liệu từ peer (so root, đồng bộ block). Sleep trong **công cụ test** để chờ là được, trong **logic node** thì không.
3. Mọi queue/worker mới có giới hạn bộ đệm; không I/O chặn trong vòng lặp async; thay đổi kiểu dùng chung phải phân tích blast radius (`grep`/codegraph) trước.
4. Sau mỗi thay đổi code: `consensus/metanode/scripts/build_check.sh` sạch, **và** `cd execution && go build ./cmd/... && go vet` các gói đụng tới, **và** `go test -race` các gói đụng tới. `build_check.sh` **không** build `parent_chain` và các tool.
5. Code comment tiếng Anh. Commit bằng **tên file** (`git add <file>`), không `git add <thư mục>` hay `git add -A` (worktree dùng chung, từng quét nhầm file rác). Mỗi commit một việc, thông điệp nêu rõ cái gì đã chạy thật. **Không push `dev`** khi chưa được chủ dự án đồng ý.
6. Chỉ `gofmt` file mới hoặc file đã sạch ở HEAD (nhiều file cũ chưa gofmt; format hàng loạt tạo diff nhiễu).
7. Kết thúc **mỗi phản hồi** bằng khối tóm tắt tiếng Việt theo mẫu "📋 Tóm tắt thay đổi" ở `AGENTS.md` mục 5. Cập nhật `PROJECT_STRUCTURE.md` khi thêm module/tool.
8. **Môi trường local dev, dữ liệu bỏ được** (chủ dự án chốt): được dừng/wipe/chạy lại từ đầu mọi cụm. Nhưng **không dừng tiến trình mà bạn không tự khởi động khi chưa hỏi** (người dùng đã từ chối một lệnh `kill` hàng loạt): trước khi dừng cụm khác, liệt kê tiến trình và hỏi.
9. **Cổng:** cụm cục bộ `deploy/cluster/local_parent_chain/` dùng HTTP 18601–18604, mạng 19001–19004, peer RPC 19501–19504, metrics 19601–19604. Cụm triển khai ở `/opt/metanode/parent_chain{,_1,_2,_3}` **trùng** cổng 1860x/1950x ⇒ không chạy song song (gây `AddrInUse`, node lệch). Luôn `ss -ltnp | grep -E ':1860|:1900|:1950'` trước khi dựng cụm.
10. Thư mục `deploy/cluster/*` bị `.gitignore` chặn (chứa khóa). Đừng ép commit; tool/script dùng chung đặt trong `execution/cmd/tool/` hoặc `execution/scripts/`. Công cụ test đang gọi `deploy/cluster/local_parent_chain/run.sh` (không có trong git): nếu cần tái lập được thì chuyển bản không chứa khóa vào repo (xem N9).
11. Cấm commit file nhị phân build (đã từng lọt một binary 34MB `test_live_e2e`); kiểm `git status` trước khi commit. Không in/ghi khóa riêng, token vào file hoặc log.

## 1. Hiện trạng đã xác minh (2026-10-01)

**Đã có và đã chạy thật** (commit gần nhất `4c8c68a3`; 12 commit local chưa push):
- Parent chain là chain nhiều validator: giao dịch là `pb.Transaction` ký BLS/ECDSA, kiểm chữ ký/nonce/chainID **trong thực thi tất định**; `CallData`, receipt, header, record, giá trị state là **protobuf tất định** (`pkg/proto/parent_chain.proto`); state là cây NOMT dùng lại lớp `pkg/trie` của simple_chain; `txs_root`/`receipts_root` là cây Merkle nhị phân; `ApplyBlock` nguyên tử, idempotent; genesis bắt buộc (`-genesis`), không còn committee hard-code; `depositToFloat` yêu cầu `Cert` của cluster nguồn (đóng lỗ hổng mint float); `QuorumClient` đọc ≥ f+1 node + kiểm proof; metrics, runbook, tool giám sát.
- Test tự động pass (`-race`): `pkg/parentchain`, `cmd/parent_chain/processor`, `pkg/proto`, `pkg/nomt_ffi`, `pkg/rollup`. `build_check.sh` 4/4.
- Cụm 4 node cục bộ chạy; công cụ `execution/cmd/tool/test_cluster_fault_tolerance` chạy T-I1..T-I8. Kết quả live trung thực nhất: **7/8 PASS**; mất quorum thì dừng, bật lại thì tiếp tục; wipe node rồi resync khớp root; deposit giả Cert bị loại tất định.

**Chưa chứng minh / còn lệch (đây là phạm vi của kế hoạch này):**

| Mã | Vấn đề | Bằng chứng |
|---|---|---|
| **G1** | **T-I4 chưa kiểm live.** Không có test nào làm hỏng dữ liệu một node rồi thấy nó bị phát hiện/cách ly. Chưa biết mã có thật sự phát hiện được "DB bị sửa" hay không (chỉ có unit test `TestBlockProcessor_ForkConflictDetection` cho block replay khác nội dung). | `execution/cmd/tool/test_cluster_fault_tolerance/main.go` hàm `testTI4` |
| **G2** | **Node nói dối chưa được test live.** T-I7 chỉ dừng 1 node (offline), chưa có node trả dữ liệu giả cho `QuorumClient`. | `testTI7`; `pkg/parentchain/quorum_client_test.go` chỉ có unit |
| **G3** | **Chưa chạy e2e cross-chain thật giữa node thực thi và cụm parent mới.** Exec node đã có `QuorumClient` (`PARENT_CHAIN_URLS`, `cmd/simple_chain/app.go:271-296`), nhưng chưa ai dựng cụm exec trỏ vào cụm parent 4 node và chạy deposit/transfer/claim/reclaim thật. Ghi chú trong bộ nhớ: genesis các node exec có thể thiếu đăng ký khóa BLS riêng của node. | — |
| **G4** | **Số liệu hiệu năng không đáng tin.** Benchmark chỉ 1000 tx/3 block; báo cáo ghi "thời gian cam kết 0.001s" (vô lý), throughput 431 rồi 768 tx/s giữa hai lần chạy. Tài liệu giai đoạn 2 (`parent_chain_completion_plan.md` bảng H6/H10) còn ghi "100% PASS" và "431.45 tx/s" **không khớp** báo cáo đã sửa (7/8). | `note/parent_chain_cluster_test_report.md`, `note/parent_chain_completion_plan.md` dòng ~35, ~39 |
| **G5** | **Còn mã legacy JSON:** `httpClient.Send*` vẫn POST JSON `ParentChainTx` tới `/tx` (`pkg/parentchain/http_rpc.go` ~dòng 102–260); còn `encoding/json` ở `tx.go`, `http_rpc.go`. Cần xác định ai còn dùng (`execution/scripts/test/*.go`, test), rồi xóa hoặc chuyển. | `grep -n '"/tx"' pkg/parentchain/*.go` |
| **G6** | **Chưa test độ bền khi sập đột ngột** (SIGKILL giữa commit, mất điện): rào chắn "ghi block DB bền trước NOMT" của parent chain chưa được ép lỗi thật. Kế hoạch có T-U7 (crash từng bước). | `pkg/parentchain/block_exec.go`, mẫu `pkg/blockchain/block_state_commit.go` |
| **G7** | **Chưa soak/churn:** chưa chạy dài với node chết/sống liên tục dưới tải. Phát hiện gần đây: restart/resync từng làm lệch bộ đếm `commit_index` (đã sửa ở `7e13d249`) — loại lỗi này chỉ lộ khi churn. | commit `7e13d249` |
| **G8** | **Rust `fork_guard`/`health_check` chưa được chứng minh đọc dữ liệu Go mới** (D2 trong discovery chỉ là đọc code, chưa quan sát log lúc chạy). | `consensus/metanode/src/node/setup_consensus/fork_guard.rs` |
| **G9** | **Công cụ test phụ thuộc file không có trong git** (`run.sh` ở thư mục bị ignore) ⇒ người khác không tái lập được. | `runClusterScript` trong `test_cluster_fault_tolerance/main.go` |
| **G10** | **Triển khai ansible N node chưa được xác nhận chạy thật** (template đã sửa; một cụm đã được dựng ở `/opt/metanode/parent_chain*` bởi agent khác nhưng chưa có biên bản). | `deploy/ansible_clusters/` |

## 2. Thứ tự

`N1` (sửa tài liệu sai) → `N2`,`N3` (bằng chứng an toàn, độc lập nhau) → `N4` (e2e cross-chain, lớn nhất) → `N5` (đo hiệu năng đúng) → `N6` (độ bền/soak) → `N7`,`N8`,`N9` (dọn dẹp, ansible, tái lập) → `N10` (báo cáo cuối, hỏi trước khi push).
Làm tuần tự, mỗi gói một hoặc vài commit; chạy lại `build_check.sh` + test liên quan trước mỗi commit.

## 3. Gói công việc

### N1 — Sửa tài liệu sai/phóng đại (G4)  [nhỏ, làm đầu]
- `note/parent_chain_completion_plan.md`: bảng H6 ghi "100% PASS", H10 ghi "431.45 tx/giây", H1–H13 ghi "ĐÃ XONG" ⇒ đối chiếu từng dòng với bằng chứng thật; sửa H6 thành "7/8 live, T-I4 chưa live", H10 thành "chưa đo đúng, xem N5", và mọi dòng khác chỉ giữ "ĐÃ XONG" nếu có test/chạy thật tương ứng (ghi tên test).
- `note/parent_chain_cluster_test_report.md`: dòng "thời gian cam kết 0.001s" vô lý ⇒ xác định cách đo sai, sửa cách đo hoặc xóa số; bỏ câu "tái lập 100%" nếu không đúng.
- **Nghiệm thu:** mỗi khẳng định "đã xong/PASS" trong hai file có dẫn tới lệnh/test chạy được; không còn số liệu hiệu năng chưa đo đúng.

### N2 — T-I4 live: làm hỏng dữ liệu một node và chứng minh bị phát hiện (G1)
1. Đọc mã để biết **cơ chế phát hiện thật sự**: `ErrBlockConflict`/`forkDetected` trong `cmd/parent_chain/processor/block_processor.go`, `block_exec.go`, và (Rust) `fork_guard.rs`. Ghi vào báo cáo: những loại hỏng nào được phát hiện, ở bước nào.
2. Thiết kế tấn công thật (ít nhất hai loại), trên cụm 4 node đang chạy dưới tải nhẹ:
   - (a) dừng node-3, **sửa dữ liệu đã lưu** (ví dụ ghi một `state_root` hoặc `block_hash` sai vào bản ghi block cuối trong `parentchain_db` bằng một tool nhỏ dùng đúng API lưu trữ; hoặc làm sai một giá trị trong NOMT), khởi động lại, gửi giao dịch mới;
   - (b) cho node-3 áp một block có nội dung khác block đã lưu cùng số (`ErrBlockConflict`).
3. Kỳ vọng (zero-fork): node bị hỏng **không âm thầm tiếp tục**: `fork_detected=true` (`/status` và metric) hoặc từ chối khởi động/từ chối áp block; không ghi thêm state; 3 node còn lại **không bị ảnh hưởng** và vẫn tiến.
4. **Nếu mã chưa phát hiện được loại hỏng (a)** (ví dụ chỉ so khi replay block): đây là lỗ hổng thật. Sửa bằng cách tự kiểm lúc khởi động: so `state_root` thực của NOMT với `state_root` trong header block cuối đã lưu; lệch ⇒ đặt `fork_detected`, **không** áp block mới, **không** dùng timeout; nối với đường khôi phục (`AlignWithExpectedRoot`/changelog nếu phù hợp) hoặc yêu cầu wipe+resync có chủ đích. Thêm unit test cho đúng đường này.
5. Cập nhật `testTI4` thành test **thật** (tự làm hỏng, tự kiểm, tự dọn: wipe + resync node đó về parity) và báo cáo đầu ra thô.
- **Nghiệm thu:** `go run ./cmd/tool/test_cluster_fault_tolerance -test T-I4` thật sự làm hỏng dữ liệu, thấy phát hiện, rồi khôi phục; kèm test đơn vị cho đường sửa (nếu có).

### N3 — Node nói dối với QuorumClient, live (G2)
- Dựng một proxy HTTP nhỏ (trong `execution/cmd/tool/`, giới hạn cổng/bộ đệm) đứng trước **một** node, tráo dữ liệu: `status.state_root`, `block hash`, `proof` (cả proof hợp lệ nhưng cho giá trị sai, và proof bị cắt/đổi byte).
- Cấu hình `QuorumClient` với 4 endpoint (1 qua proxy nói dối): kỳ vọng đọc **đúng**. Với 2 endpoint nói dối: kỳ vọng **trả lỗi** (không trả giá trị sai). Thử cả trường hợp 2 node nói dối **cùng một đáp án giả** (kẻ tấn công phối hợp): phải lỗi vì không đủ f+1 khớp thật.
- Kiểm cả đường **ghi**: `SendRawTransaction` qua node nói dối (nuốt/đổi giao dịch) ⇒ client phải thử node kế hoặc báo lỗi, không báo thành công giả; kiểm tra bằng cách đọc lại receipt qua quorum.
- **Nghiệm thu:** bảng kết quả live (số node nói dối × kết quả mong đợi/thực tế) trong báo cáo; mở rộng `testTI7` hoặc thêm `T-I9` trong tool.

### N4 — E2E cross-chain thật giữa node thực thi và parent chain mới (G3)  [lớn nhất]
- Dựng một cụm exec nhỏ (tối thiểu 2 cụm exec để có chuyển chéo; dùng cụm cục bộ có sẵn trong repo hoặc đã chạy ở `/opt/metanode/exec1_r*`, `exec2` nếu cho phép dùng; **hỏi trước khi dừng/cấu hình lại cụm đang chạy**), cấu hình `PARENT_CHAIN_URLS=http://127.0.0.1:18601,...,18604`.
- Đăng ký đầy đủ: cluster (khóa BLS) trên parent chain bằng giao dịch ký, tài khoản người dùng (`registerAccount`), nạp genesis phù hợp. Kiểm tra lỗ hổng đã flag trong bộ nhớ: **khóa BLS riêng của exec node có được đăng ký ở genesis hay không**; nếu thiếu, sửa genesis/công cụ sinh genesis, ghi rõ.
- Chạy luồng thật: **deposit → transfer chéo cluster → claim → reclaim/refund** (xem các script mẫu `execution/scripts/test/e2e_cross_cluster.go`, `e2e_real.go`, `stress_concurrent_transfers.go`; cập nhật chúng khỏi mã legacy). Sau mỗi bước đọc **số dư float qua `QuorumClient` có proof**.
- Bất biến phải kiểm: tổng cung float không đổi ngoài deposit/withdraw hợp lệ (`CheckFloatSupplyInvariant`); `msgID` chỉ áp một lần; deposit không `Cert` hợp lệ không tạo tiền.
- Kịch bản chịu lỗi kèm theo: dừng 1 node parent giữa luồng (3/4) ⇒ luồng vẫn hoàn tất; dừng 2 node ⇒ luồng **dừng chờ** (pending) và tự chạy tiếp khi bật lại, **không** mất/tạo tiền.
- **Nghiệm thu:** script e2e tái lập được trong repo, đầu ra thật (số dư trước/sau mỗi bước, hash giao dịch, block) ghi trong `note/parent_chain_e2e_report.md`; liệt kê bug tìm thấy và đã sửa (mỗi bug có test hồi quy).

### N5 — Đo hiệu năng đúng cách (G4)
- Phương pháp: tăng tải 1 → 100 → 1.000 → 5.000 → 10.000 tx (và duy trì tải 60 giây), **nhiều block**, giao dịch đa dạng (đăng ký, deposit hợp lệ, transfer). Đo từ phía node: thời gian từ nhận tới receipt (dùng `timestamp_ms` của block và thời điểm gửi), tx/giây theo cửa sổ 1 giây (p50/p95), số tx/block, độ trễ commit mỗi block (log hoặc metric), kích thước DB tăng mỗi 10.000 tx, CPU/RAM node (`ps`/`pidstat`). Ghi rõ **cách đo** và giới hạn (4 node trên 1 máy, trộn tải).
- Kiểm tra giả thuyết đã nêu: ghi bền `Sync` mỗi block + NOMT. Nếu là nút thắt: chỉ commit NOMT khi block có giao dịch **được phép nếu** root của block rỗng vẫn giống hệt trên mọi node (có test); **không** dùng `Checkpoint()` trên đường nóng.
- **Nghiệm thu:** bảng số liệu có phương pháp trong `note/parent_chain_cluster_test_report.md` (hoặc file riêng), thay các số cũ; không dùng số nào chưa tự đo.

### N6 — Độ bền khi sập đột ngột và churn dài (G6, G7)
- **Sập giữa commit:** test tự động ép SIGKILL ở các điểm của chu trình `ApplyBlock` (trước ghi block DB, sau ghi block DB trước NOMT, sau NOMT trước dấu tiến độ…): có thể dùng hook thử nghiệm bằng build tag (`//go:build` chỉ cho test, không vào binary release) hoặc injection ở unit test cho `ApplyBlock`. Kỳ vọng: mở lại thì hoặc trọn block hoặc không gì; **NOMT không bao giờ vượt tip bền**; không mất/ trùng giao dịch (so `txs_root`).
- **Churn dài:** 10–20 phút, tải đều, mỗi ~30 giây SIGKILL một node ngẫu nhiên (không vượt f=1 cùng lúc), bật lại sau khi dừng. Cuối: dừng tải, đợi đồng bộ, **4 node cùng `state_root`/`block_hash`**, không `fork_detected`, không giao dịch mất/trùng, log không có panic lặp (đếm `panicked`, `AddrInUse`). Ghi mọi bất thường và nguyên nhân gốc (đừng nới điều kiện test).
- **Nghiệm thu:** test đơn vị cho từng điểm crash + báo cáo churn có số liệu (số lần kill, thời gian bắt kịp trung bình/max).

### N7 — Dọn mã legacy (G5)
- `grep -rn "ParentChainTx\|\"/tx\"\|SendDepositToFloat\|httpClient" execution --include=*.go` để lập danh sách người dùng còn lại. Chuyển script/test còn gọi `httpClient.Send*` sang `QuorumClient`/giao dịch ký, rồi **xóa** đường JSON legacy ở client; ở server, xóa `/tx` JSON (hoặc giữ sau cờ mặc định tắt nếu còn test cần, ghi rõ). Mục tiêu: `encoding/json` trong `pkg/parentchain` và `cmd/parent_chain` chỉ còn ở genesis (cấu hình) và lớp hiển thị RPC đọc.
- **Nghiệm thu:** build `./cmd/... ./scripts/...` (các script có build tag cần build với tag tương ứng), test pass, `grep` như trên sạch.

### N8 — Xác nhận triển khai ansible N node thật (G10)
- Dựng cụm parent 4 node từ `deploy/ansible_clusters/` với inventory mẫu trên **localhost/cổng riêng** (không đụng cổng cụm khác; nếu cụm `/opt/metanode/parent_chain*` còn chạy thì hỏi người dùng trước). Kiểm: file `-genesis` sinh/đưa đúng, `node_id`/`peer_rpc_addresses` đúng từng node, 4 node lên cùng block/`state_root`, `/status`, `/metrics` đúng; chạy lại playbook lần 2 (idempotent, không đổi gì); wipe + chạy lại từ đầu.
- Tìm hạn chế: mật khẩu SSH/vault (đã có issue #103–#105 trên GitHub, **đừng làm lại**, chỉ không làm tệ hơn); không ghi khóa vào git.
- **Nghiệm thu:** biên bản (lệnh, đầu ra, node status) trong `note/`; sửa template nếu thấy lỗi thật.

### N9 — Làm cho test tái lập được và dọn repo (G8, G9)
- Đưa bản **không chứa khóa** của bộ dựng cụm cục bộ vào repo (ví dụ `deploy/parent_chain_cluster/run.sh` + script sinh khóa/genesis sinh khóa mới mỗi lần, khóa ghi vào thư mục bị ignore), để người khác chạy `T-I1..T-I8` mà không cần thư mục ignore. `test_cluster_fault_tolerance` đọc đường dẫn script qua cờ/biến môi trường.
- Quan sát thật `fork_guard`/`health_check` (G8): bật log, gây lệch (liên hệ N2), ghi lại dòng log chứng minh Rust có đọc `state_root`/`block_hash` từ Go qua `GetBlocksRange` và phản ứng thế nào.
- **Nghiệm thu:** tài liệu "chạy test từ đầu" trong `note/runbook_parent_chain_multinode.md` được thử làm theo từng bước bởi chính bạn trên cây sạch.

### N10 — Báo cáo cuối và push
- Cập nhật `parent_chain_completion_plan.md`, `PROJECT_STRUCTURE.md` (đúng mức đã kiểm chứng), bảng G1–G10 với trạng thái (đã sửa/đã chứng minh live/còn mở) và bằng chứng.
- Liệt kê commit local chưa push và **hỏi chủ dự án** trước khi push. Lưu ý: nhiều commit đổi hành vi đồng thuận (EIP-7702, gas thống nhất, định dạng giao dịch parent chain) ⇒ khi lên môi trường chung phải nâng cấp mọi node cùng lúc và wipe dữ liệu parent chain.

## 4. Định nghĩa "xong"

- [ ] T-I4 live thật (làm hỏng dữ liệu → phát hiện → khôi phục) và node nói dối live (N2, N3), kèm test đơn vị cho mọi sửa đổi.
- [ ] E2E cross-chain thật giữa exec cluster và parent 4 node, bất biến tổng cung giữ, chịu mất 1 node, dừng an toàn khi mất 2 node (N4).
- [ ] Số liệu hiệu năng có phương pháp, đa block, đa mức tải (N5); tài liệu không còn khẳng định chưa kiểm chứng (N1).
- [ ] Crash test từng điểm và churn dài pass, 4 node cùng root cuối (N6).
- [ ] Không còn đường JSON legacy trong `pkg/parentchain`/`cmd/parent_chain` ngoài genesis và hiển thị RPC (N7).
- [ ] Ansible 4 node dựng được và idempotent, có biên bản (N8); bộ test tái lập được từ cây sạch (N9).
- [ ] `build_check.sh` sạch, `go build ./cmd/...`, `go vet`, `go test -race` các gói đụng tới pass; mọi commit chỉ gồm file đúng chủ đề, không binary/khóa.

## 5. Điều cần hỏi chủ dự án (đừng tự quyết)

1. Có được dùng/cấu hình lại các cụm exec đang chạy ở `/opt/metanode` cho N4 không, hay dựng cụm exec riêng?
2. Có được dừng cụm parent ở `/opt/metanode/parent_chain*` cho N6/N8 không (trùng cổng với cụm cục bộ)?
3. Nếu N2 cho thấy phải thêm tự kiểm lúc khởi động (đổi hành vi khởi động node): xác nhận hành vi mong muốn khi phát hiện lệch (dừng hẳn, hay chờ wipe+resync thủ công).
4. Khi nào push `dev`.

## 6. Việc để sau (ngoài phạm vi)

Chữ ký tổng hợp BLS trên header (client tin một nguồn); phí/gas; đổi committee tự động và epoch có đổi validator; cắt tỉa block cũ/snapshot đồng thuận (xem `parent_chain_multinode_plan.md` mục 10).
