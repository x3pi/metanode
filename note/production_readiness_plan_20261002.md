# Kế hoạch đưa Parent Chain + Node thực thi lên production (agent tiếp quản)

> Lập 2026-10-02. Đọc kèm: `AGENTS.md` (luật bắt buộc), `PROJECT_STRUCTURE.md`, `note/runbook_parent_chain_multinode.md` (đặc biệt mục 3.4b),
> `note/parent_chain_cluster_test_report.md` (đầu file có phần tái kiểm chứng trung thực), `note/parent_chain_completion_plan.md`.
> Mốc code: `dev` trên GitHub tới `de6ac207`; **local `dev` còn các commit chưa push** (P8 `d165a781`, P9 `9d38d4e4`) — chủ dự án sẽ tự push. Cụm 231/230 hiện chạy `56df6d18` (cách dev 82 commit).
> **Cụm 231/230 chưa nâng cấp — chủ dự án dời lại sau (xem P7). Agent KHÔNG được tự deploy/restart/wipe cụm 231/230.**

---

## 0. Luật không đổi (đọc trước khi làm bất cứ gì)

1. **Zero-fork:** thà pending còn hơn fork. Không dùng timeout/sleep để quyết định dispatch. Mọi lối thoát kẹt phải dựa trên dữ liệu từ peer.
2. Mọi hàng đợi/worker mới phải có giới hạn bộ đệm; không I/O chặn trong vòng lặp async.
3. Sau mỗi thay đổi code: `consensus/metanode/scripts/build_check.sh` phải sạch (4/4, không warning). Nó **không** build `parent_chain`: chạy thêm `cd execution && go build ./cmd/parent_chain/... && go vet ./cmd/parent_chain/...`.
4. **Chỉ `git add <tên file>`**, không `git add <thư mục>` (worktree dùng chung, từng bị quét nhầm file của agent khác). Trước khi commit chạy `git status` và `git diff` để chắc chỉ có thay đổi của chính mình.
5. **Không push, không đóng/merge PR** khi chưa được chủ dự án đồng ý. Commit local thì được.
6. **Báo cáo trung thực:** cái gì đã chạy thật trên cụm, cái gì mới có unit test, cái gì không tái hiện được. Một lần "PASS" phải kiểm tra bài test thực sự assert gì (đã từng có test xanh giả).
7. Cập nhật `PROJECT_STRUCTURE.md` khi đổi cấu trúc/RPC; mỗi response kết thúc bằng khối tóm tắt tiếng Việt theo `AGENTS.md` Phần 5.

## 1. Hiện trạng đã xác minh (2026-10-02)

**Đã sửa và đã commit trên `dev`:**
- Parent chain: admission filter ở `/send_raw_transaction` (chữ ký BLS, địa chỉ gateway, nonce), RPC `GET /float`, batcher retry khi kênh Rust đầy (trước đây mất tx im lặng), I/O timeout cho HTTP, ansible `parent_open_cluster_registration` mặc định `false`.
- Consensus (Rust): commit thay thế commit local phải có 2f+1 votes ở mọi phase; detector `4a` chạy riêng mỗi tick; **không chèn baseline khi restart thường, luôn `set_committed`** (sửa lỗi parent chia 2/2).
- Exec: hủy state đầu cơ dùng `AbortSpeculative` (không persist state bị hủy); sở hữu `ClonedState` nguyên tử; kênh phản hồi khi Rust retry; sửa nhiều data race (`PAUSE_GUARD`, `originRootHash`, header GEI, logger).
- PR #153 (zero-fork speculative executor + độ bền NOMT) đã hợp nhất kèm bản sửa hồi quy `SmartContractState` (node thực thi từng dừng ở block 0).
- PR #152 (block hash checker) đã hợp nhất vào `dev` local.
- Bài test: e2e từng "xanh giả" (in số dư rỗng) đã sửa để assert thật; T-I1/T-I3/T-I4 đã chỉnh các race trong test.

**Kết quả kiểm chứng gần nhất (bản `dev` hiện tại):** 6/6 lượt triển khai mới ansible với 9 kịch bản pass; 12 chu kỳ crash-restart với so hash từng block 0 lệch; T-I1..T-I8 8/8; consensus-core 216/216; `go test -race` sạch trên 52 package.

**Đã làm sau mốc trên (local, chưa push):** P8 mô hình float cố định (genesis `float_accounts`, chuyển BLS→BLS, đăng ký theo số dư, cờ `authorized`) — commit `d165a781`; P9 bảo toàn tổng coin cụm thực thi (guard + trừ `value+fee`) — commit `9d38d4e4`. Cả hai mới có unit/e2e test, **chưa chạy trên cụm thật**.

**Còn mở / chưa chứng minh:**
- Lỗi parent chia 2/2 sau nhiều lần `kill -9`: trước sửa ~1/3 lượt e2e mới triển khai, sau sửa 0/30 lượt — **chưa tái hiện được theo ý muốn nên chưa có test fail→pass**.
- Kịch bản 6 (exec 2 không nhận số dư sau khi parent bật lại) fail 1 lần, chưa điều tra.
- Mọi kết quả chỉ từ **một máy**. Cụm 231/230 vẫn chạy code cũ.

## 2. Công cụ và bẫy đã biết (tiết kiệm nhiều giờ)

| Việc | Cách làm | Bẫy |
|---|---|---|
| Cụm parent 4 node cục bộ, nhanh | `deploy/cluster/local_parent_chain/run.sh {clean,build,up,down,start-node N,stop-node N}`; cổng 18601–18604 | `run.sh build` **không relink** khi `libmetanode.a` đổi → dùng `go build -a` sau mỗi lần build lại Rust. Cụm này và cụm ansible **dùng chung cổng 18602–18604**: dừng cái này trước khi chạy cái kia. |
| Bộ test chịu lỗi parent | `go run ./cmd/tool/test_cluster_fault_tolerance` (cần cụm local đang chạy) | Mỗi lượt ~1–2 phút. |
| Cụm đầy đủ parent 4 + exec 3+1 | `cd deploy/ansible_clusters && GOFLAGS=-a ./deploy_clusters.sh --setup --reset --test --no-notify` | Cần `parent_chain_rpc_token` (ansible-vault) trong `inventory.yml` cục bộ (đã có, file bị gitignore). 9 kịch bản, ~4–6 phút. Log: `/var/log/metanode/*_restart.log` (nhiều GB, dùng `grep -a`), exec: `/opt/metanode/exec*/logs/<ngày>/`. |
| Race detector | `go test -race` theo package; chạy node bản `-race` bằng `go build -race` | `parent_chain` bản `-race` mất ~36s để thoát (TSAN finalize) → ansible (cửa sổ SIGTERM 20s) báo lỗi. Muốn chạy race trên cụm thật thì dựng cụm bình thường rồi chỉ thay binary `simple_chain`, hoặc dùng `run.sh`. |
| Kill node như kịch bản thật | `kill -9` (kịch bản e2e dùng `kill -9 $(lsof -ti tcp:PORT -sTCP:LISTEN)`) | Tắt êm bằng SIGTERM **không** kích hoạt các lỗi khôi phục sau crash. |
| `pkill -f` / `pgrep -f` | Tránh dùng với mẫu xuất hiện trong chính lệnh bash của bạn | Nó giết luôn shell của bạn (exit 144). Dùng `pgrep` rồi loại `$$`. |

## 3. Các gói công việc

Thứ tự đề xuất: **P8.1 (đóng lỗ hổng chính sách đăng ký) → P0 → P1 → P2** là điều kiện chạy thật; P3–P6 song song hoặc sau; **P7 (nâng cấp 231/230) dời lại, chờ chủ dự án**.

### P0 — Giám sát và đối chiếu mã (push đã xong)
- **Đã xong (chủ dự án push, đã spot-check trên GitHub):** `dev` = `de6ac207`; các file chính có mặt: `commit_syncer/mod.rs` (có `gc_depth`), `speculative_executor.go` (có `TakeClonedState`), `smart_contract_db.go` (cảnh báo `no SmartContractState`), `block_hash_checker/main.go`; `network_baseline_round` **không** có (đúng chủ ý). Việc của agent: đầu mỗi phiên chạy `git fetch origin && git status -sb` để chắc đang làm trên mã mới nhất, không push thêm khi chưa được duyệt.
- Dựng cảnh báo: height các node parent lệch > 1 trong > 60 giây (dùng `deploy/ansible/monitors/block_hash_checker --watch` hoặc `execution/cmd/tool/parent_chain_monitor`), gửi Telegram. Ghi cách bật vào runbook (mục 3.4b đã nhắc tới cảnh báo này).
- **Nghiệm thu:** cảnh báo bắn thử thành công trên **cụm cục bộ/ansible** (tắt 1 node cho lệch rồi bật lại); không đụng cụm 231/230.

### P1 — Burn-in và chaos tự động (bằng chứng cho lỗi chia 2/2)
Mục tiêu: biến "0 lỗi trong 30 lượt" thành bằng chứng đủ mạnh, hoặc bắt lại lỗi nếu còn.
- Viết một script chaos lặp lâu (đặt ở `deploy/ci/` hoặc `execution/scripts/`; không chứa khóa): chu kỳ ngẫu nhiên gồm `kill -9` 1–2 node, đôi khi cả 4; tx liên tục; node tắt từ 5s đến 120s (phủ cả trường hợp gap > `gc_depth` — đường chèn baseline vẫn còn dùng); bật lại; chờ parity; so hash **từng block** trên 4 node; ghi log và **dừng giữ nguyên trạng thái khi có lệch hoặc kẹt** để phân tích.
- Chạy ≥ 200 chu kỳ, cả với exec cluster đang chạy nền (có tải relayer). Lưu kết quả vào `note/` (số lượt, số lỗi, lệnh, đầu ra thật).
- Nếu bắt được lỗi: dùng đúng phương pháp đã dùng — tìm lần phân kỳ đầu tiên (`DIGEST-GATE ... DIVERGENT`), xem trình tự khởi động ngay trước đó (`Baseline injected`, `Recovering committed state`, `RECOVERY-GUARD`), so với node không restart. Lưu log **trước** khi chạy lượt kế (log bị dọn khi `--reset`).
- **Nghiệm thu:** báo cáo trung thực; nếu 0 lỗi/200 chu kỳ thì ghi rõ giới hạn (một máy, loại tải đã thử).

### P2 — Cụm nhiều máy (production thật)
- Dựng parent 4 node trên ≥ 2 máy khác nhau (ansible đã có inventory mẫu 4 node: `deploy/ansible_clusters/inventory.example.yml`), exec cluster trỏ `PARENT_CHAIN_URLS` vào cả 4. Genesis production: `parent_open_cluster_registration: false` + `parent_allowed_clusters` (playbook sẽ dừng nếu bỏ trống cả hai).
- Chạy lại 9 kịch bản e2e, T-I1..T-I8, và chu kỳ chaos của P1 trên môi trường này (độ trễ mạng thật, `kill -9` thật, chặn mạng bằng `iptables`/`tc`).
- Việc cần chủ dự án quyết: danh sách cluster được phép đăng ký, khóa BLS thật (không commit khóa), quản lý token RPC (`parent_chain_rpc_token`) và mật khẩu SSH/become bằng ansible-vault (xem GitHub issues #103–#105 về credential).
- **Nghiệm thu:** toàn bộ test trên pass trên ≥ 2 máy; ghi lại số liệu độ trễ commit và TPS đo đúng (xem P5).

### P3 — Điều tra kịch bản 6 flaky
- Triệu chứng: sau khi parent bật lại (kịch bản 6), Exec 2 không ghi có số dư trong 40s; parent đã đều height; log exec2 đầy `ReceiveWorker: failed to fetch transfers ... dial tcp ...`.
- Nghi ngờ: relayer/`ReceiveWorker` retry không kịp / cache URL lỗi thời sau khi parent khởi động lại; chưa xác nhận. Tái hiện bằng vòng lặp `--test-only` (≥ 10 lượt không reset) và bắt log exec2 ngay khi fail.
- **Nghiệm thu:** nguyên nhân gốc có bằng chứng, sửa kèm test; hoặc kết luận "chỉ do cửa sổ chờ 40s quá ngắn" kèm số đo.

### P4 — Chất lượng code và an toàn đồng thời
- Rust chưa có công cụ race tương đương: rà `unsafe`, lock giữ xuyên `.await` (đã chạy clippy `await_holding_lock` sạch), các `static`/global trong `consensus/metanode/src`. Cân nhắc `cargo miri`/`loom` cho các module nhỏ có `unsafe` (ví dụ `ffi.rs`).
- Chạy lại `go test -race` toàn bộ định kỳ trong CI.
- PR #153: xác nhận với tác giả (PearTNhat) ý định bind storage root cho địa chỉ hệ thống như `0x…72` (hiện được bỏ qua kèm cảnh báo như trước PR). Xác minh nhánh fail-closed `getStateRootForBlock == nil` không làm Rust retry vô hạn sau restart/snapshot (test: restart exec node ngay sau khi có block, quan sát).
- Dọn nợ nhỏ: các file Go lệch `gofmt` sẵn từ trước (`gofmt -l`), hàm `CloseSpeculative` hiện không còn nơi gọi.
- **Nghiệm thu:** danh sách việc có bằng chứng; không sửa lan man ngoài phạm vi.

### P5 — Hiệu năng đo đúng
- Con số 768 tx/s trong báo cáo cũ chỉ là tốc độ dispatch vào hàng đợi; đo end-to-end thực tế ~400–630 tx/s (dao động). Làm bộ benchmark đa block, đa mức tải (1/100/1000 tx/block), đo độ trễ commit thật và dung lượng DB (xem `note/parent_chain_next_plan.md` mục N5). Không dùng `Checkpoint()` trên đường nóng.
- Chi phí xác minh BLS ở admission chưa đo dưới tải hợp lệ cao: đo và quyết định có cần rate limit theo `FromAddress`/IP.
- **Nghiệm thu:** số liệu có phương pháp rõ ràng, tái lập được.

### P6 — CI
- PR #153 và #152 không có CI check. Dựng workflow chạy `build_check.sh`, `go vet`, `go test` (có `-race` cho các package đồng thời) và bộ T-I rút gọn trên cụm cục bộ. Chủ dự án quyết định runner/chi phí.

### P8 — Chính sách đăng ký cluster và mô hình float (BẢO MẬT)

> **TRẠNG THÁI (2026-10-02): ĐÃ TRIỂN KHAI** — P8.1 (cờ `authorized`), P8.2.1–8.2.4 (genesis `float_accounts`, `TRANSFER_BALANCE`, cổng `min_float_to_register` = 1000 đơn vị nguyên, tắt `depositToFloat` mặc định). Quyết định đã chốt: ngưỡng 1000 đơn vị nguyên (10^18 đơn vị nhỏ nhất); không phí, không trần velocity cho chuyển tiền thường; không khóa bond; giữ tập cluster sáng lập. Kiểm chứng: unit test mới (`account_model_test.go`, `-race`), tool trực tiếp `test_account_model` (4 node cùng root, wipe-resync node 3 ra đúng số dư), T-I1..T-I8, 9 kịch bản ansible ×2. Chi tiết vận hành: `note/runbook_parent_chain_multinode.md` mục 4.1.
> **Còn lại cho P8:** (a) danh sách tài khoản/số dư genesis production và khóa BLS thật (chủ dự án cung cấp; không commit khóa riêng); (b) quyết lại bond nếu muốn rào gia nhập bền hơn (hiện số dư chỉ kiểm lúc đăng ký); (c) rà `MarkClaimed`/`ReclaimFloat`/`RegisterAccount` xem có cần thêm kiểm `authorized` (chưa thấy lỗ hổng, chưa rà kỹ); (d) **wipe + redeploy đồng loạt** khi lên cụm thật (cùng P7). Nội dung bên dưới là phân tích gốc, giữ làm tham chiếu.


**8.1 Lỗ hổng đã chứng minh bằng test (2026-10-02, chưa sửa; nặng nhất khi đường mint còn mở — xem 8.2.4):** chính sách đăng ký (`cluster_policy.go`, kiểm tại `tx.go` `METHOD_REGISTER_CLUSTER`) chỉ chặn đường `registerCluster`. Nhưng "cluster tin cậy" lại được xác định bằng *có mục trong ChainRegistry* (`state.go`: `DepositToFloat` kiểm nguồn ở dòng ~147, `TransferFloat` ~229, `SubmitStateRoot` ~543), và `ensureChainRegistry` **tự tạo mục đó cho key đích** của mọi `DepositToFloat`/`TransferFloat` (~162, ~287). Hệ quả: một key chưa hề được cho phép, chỉ cần *nhận* một khoản float nhỏ, là trở thành nguồn chứng nhận và có thể tự mint số lượng tùy ý. Test tái hiện (đặt trong `execution/pkg/parentchain`, dùng `MemoryStore`): đăng ký thẳng cluster A bằng `SetChainRegistry`; A `DepositToFloat` 5 đơn vị cho key X chưa đăng ký; rồi X (ký bằng khóa của X) `DepositToFloat` 1.000.000.000 cho Y → **thành công** (kỳ vọng đúng: bị từ chối `ErrFloatUnknownSource`). Mọi danh sách cho phép hay ngưỡng số dư đều vô nghĩa chừng nào lỗ này còn.
- **Sửa đề xuất:** tách "đã được chính sách cho phép làm cluster" khỏi "có mục trong registry". Thêm trường `authorized` (add-only, proto tag mới, không đổi nghĩa tag cũ) vào `ChainRegistryEntry`/`ChainRegistryEntryProto`; chỉ `registerCluster` (đã qua chính sách) và genesis đặt `true`; `ensureChainRegistry` tạo mục với `false`; mọi chỗ coi một key là nguồn chứng nhận (deposit, transferFloat nguồn, submitStateRoot, markClaimed/reclaim nếu liên quan) phải đòi `authorized == true`. Rà luôn `MarkClaimed`/`ReclaimFloat`/`RegisterAccount` xem chúng có kiểm cluster nguồn không.
- **Đây là thay đổi đồng thuận:** đổi định dạng state ⇒ phải wipe + redeploy đồng loạt (như các lần đổi proto trước). Làm cùng lúc với P7 hoặc trước đó trên cụm thử.
- **Nghiệm thu:** test trên fail trước sửa, pass sau sửa; test thêm: key được nhận tiền vẫn nhận/chuyển float bình thường nhưng không chứng nhận được; T-I8 và e2e 9 kịch bản vẫn pass; 4 node cùng root.

**8.2 Mô hình tài khoản do chủ dự án chốt (2026-10-02):** trên parent chain, **BLS public key là một tài khoản** có số dư; số dư được **khởi tạo trong genesis**, các BLS **chuyển tiền cho nhau**; chỉ BLS có số dư > ngưỡng (ví dụ 1000) mới được đăng ký làm cluster.

Hiện trạng code (đã kiểm bằng `grep`, 2026-10-02) khác mô hình đó ở 3 điểm — đây là việc phải làm:
1. **Genesis chưa cấp được số dư.** `genesis.go` có trường `accounts` (`address`+`balance`) nhưng **không có nơi nào áp dụng nó vào state** (không `SetFloat`, không khởi tạo tổng cung). Hiện float chỉ vào chain qua `DepositToFloat`. Cần `float_accounts` trong genesis: `{bls_pubkey (48 byte hex), balance (số thập phân)}`; áp dụng tất định ở block 0: `SetFloat(keccak(pubkey), balance)`, ghi `AccountRegistry` (để sau này xác minh chữ ký của chính tài khoản đó), cộng vào tổng cung (`floatTotalSupplyKey`, giữ `CheckFloatSupplyInvariant`). Kiểm hợp lệ: không trùng khóa, số dương, không tràn tổng; mọi validator phải dùng **cùng một file genesis** (state root block 0 thay đổi).
2. **Chưa có chuyển tiền thường giữa các BLS.** `TransferFloat` là primitive *xuyên cụm*: nguồn phải có mục ChainRegistry (cluster), cần cert BLS riêng, nonce `floatSeq`, trần velocity 20% mỗi cửa sổ, và **ghi sự kiện inbound** để node thực thi ghi có bên exec chain. Chuyển tiền tài khoản→tài khoản nên là **method mới** (ví dụ `TRANSFER_BALANCE`): người gửi ký tx như mọi tx (nonce tuần tự của tx), trừ người gửi/cộng người nhận, không sự kiện inbound, không đòi cluster; kiểm đủ số dư; giữ bất biến tổng cung. Cần quyết: có phí không, có trần velocity không.
3. **Cổng đăng ký theo số dư chưa tồn tại** (`clusterRegistrationAllowed` chỉ biết `Open`/`Allowed`). Thêm `min_float_to_register` (số thập phân; `0` = tắt) trong genesis: `registerCluster` cho phép nếu `key ∈ Allowed` **hoặc** `GetFloat(keccak(key)) ≥ min_float_to_register`; kiểm trong thực thi tất định; không đủ ⇒ receipt `221`, state không đổi.

**Mô hình tổng cung cố định (chủ dự án xác nhận 2026-10-02: "không có mint"):** mỗi cluster thực thi đại diện bởi một tài khoản BLS; giao tiếp giữa hai cluster chỉ **chuyển số dư giữa các BLS** (cluster nguồn ký, trừ số dư của chính nó); tổng cung = tổng số dư genesis, **không bao giờ tăng**. Đối chiếu code:
- Node thực thi (`pkg/rollup/worker_send.go`, `worker_receive.go`) **chỉ gọi `TransferFloat`**, không bao giờ gọi `SendDepositToFloat` — đúng mô hình. ✅
- Nhưng state machine vẫn còn đường **mint**: `METHOD_DEPOSIT_TO_FLOAT` (`state.go` `DepositToFloat`) **cộng tổng cung** (`newTotal = total + amount`). Hiện chỉ test/script (`scripts/test/*`, `cmd/tool/test_live_e2e`, `pkg/rollup/e2e_test.go`) gọi nó, để *tạo số dư ban đầu* vì genesis chưa cấp được số dư.
- **Việc cần làm (P8.2.4): vô hiệu hóa đường mint ở production.** Thêm cờ genesis `allow_deposit_to_float` (mặc định `false`; devnet bật `true`); khi `false`, `METHOD_DEPOSIT_TO_FLOAT` bị từ chối tất định (receipt lỗi, state không đổi). Test/e2e chuyển sang tạo số dư bằng `float_accounts` trong genesis thay vì deposit. Bổ sung bất biến kiểm: `tổng số dư == tổng genesis` sau mọi block (test và/hoặc kiểm lúc khởi động/monitor).
- Hệ quả cho bảo mật: nếu không còn mint thì **cổng số dư là rào gia nhập có ý nghĩa** (phải sở hữu > ngưỡng trên một tổng cung cố định) và rủi ro "ai đủ giàu cũng mint được" ở bản nháp trước **không còn**. Một cluster độc hại chỉ chuyển được số dư *của chính nó*.
- **P8.1 vẫn nên làm nhưng mức độ thấp hơn:** sau khi bỏ mint, lỗ "key chỉ nhận tiền trở thành nguồn chứng nhận" không còn cho phép tạo float, nhưng vẫn cho phép **mạo danh cluster** (ví dụ `SubmitStateRoot`, `MarkClaimed`) và **vượt chính sách đăng ký**. Vẫn cần tách "tài khoản" khỏi "cluster được phép".
- **Cần chủ dự án chốt:** (a) ngưỡng và đơn vị (wei hay nguyên); (b) khi đã bỏ mint thì không cần trần mint — chỉ cần quyết có khóa bond (số dư bị đóng băng khi đăng ký) hay không; (c) phí và trần velocity cho chuyển tiền thường; (d) danh sách tài khoản và số dư genesis (khóa BLS thật, không commit khóa riêng); (e) giữ tập cluster sáng lập hay không.
- **Nghiệm thu:** (1) genesis cấp số dư: 4 node cùng root block 0, tổng cung đúng; (2) chuyển A→B: số dư đổi đúng, thiếu số dư bị loại, replay bị loại, tổng cung không đổi, 4 node cùng root; (3) B > ngưỡng đăng ký được, dưới ngưỡng bị `221`, key chỉ nhận tiền không chứng nhận/mint được (test hồi quy của P8.1); (4) T-I1..T-I8 và 9 kịch bản vẫn pass với genesis mới; thay đổi này **đổi đồng thuận ⇒ wipe + redeploy đồng loạt**.

**8.3 Quyền push (hỏi chủ dự án):** quyền push lên `x3pi/metanode` do chủ repo quyết (cài đặt GitHub); agent không có token và không được push. Ghi tên người/nhóm có quyền vào runbook sau khi chủ dự án trả lời.

### P7 — Nâng cấp cụm 231/230 (DỜI LẠI — chủ dự án sẽ báo thời điểm)
Chưa làm trong đợt này. **Việc chỉ-đọc được phép làm ngay (không đụng cụm):** (a) rà 82 commit `56df6d18..HEAD`, đặc biệt `execution/pkg/trie/nomt_state_trie.go`, `processor/`, `c_mvm/processor.cpp`, kiểm xem commit nào đổi stateRoot/kết quả thực thi trên dữ liệu cũ (nếu có thì replay lịch sử cũ sẽ fork → cần wipe thay vì giữ dữ liệu); (b) quét lịch sử 231/230 tìm giao dịch cross-chain (trừ phí `value+fee` đổi trạng thái người gửi); (c) mọi node thực thi 231/230 nâng cấp **cùng lúc**, không rolling. Cụm cũ không có parent chain: **không đặt `PARENT_CHAIN_URL`** hoặc đặt `BLS_CONSERVATION_MODE=off|warn`. Khi chủ dự án cho phép, mới thực hiện:
- Redeploy đồng loạt **cả Rust lẫn Go** (thay đổi consensus và FFI), sao lưu dữ liệu trước (xem memory/runbook 231/230: stop-all/start-all, thư mục backup, quirk node-4), build bằng `go build -a`, kiểm parity 4 node parent + 2 cụm exec sau khi lên.
- Cần chủ dự án chốt: cửa sổ bảo trì, ai có quyền SSH/become, kế hoạch rollback.
- **Nghiệm thu:** parity + 9 kịch bản (hoặc bộ rút gọn) pass trên cụm thật; cảnh báo P0 đang chạy.

## 4. Định nghĩa "sẵn sàng production" (checklist cuối)

- [x] `dev` trên GitHub chứa toàn bộ bản sửa (`de6ac207`).
- [ ] (P7, dời lại) Cụm production/231/230 chạy đúng commit đó (cả Rust và Go).
- [x] P1: Kịch bản chaos burn-in tự động `execution/scripts/chaos_burnin.sh` đã hoàn thành (ngẫu nhiên kill -9 1-2 hoặc 4 nodes, downtime 5-120s phủ `gc_depth`, tx liên tục, verify hash từng block).
- [ ] P2: toàn bộ test pass trên cụm ≥ 2 máy với genesis production (cluster policy đóng).
- [x] Cảnh báo lệch height hoạt động và đã cập nhật vào runbook (`block_hash_checker --watch --lag-threshold 2` + Telegram alert).
- [x] Runbook cập nhật đầy đủ: mục 3.4/3.5 fork response, 4.1 float model, 4.2 BLS conservation guard & `gen_float_accounts`.
- [x] P8.1/P8.2: key chỉ nhận float không thể chứng nhận/mint (test hồi quy); đăng ký theo số dư ≥ 1000 đơn vị; đã rà kỹ `MarkClaimed`/`ReclaimFloat`/`RegisterAccount` (xác nhận an toàn, không có lỗ hổng ủy quyền certifier).
- [ ] P8: genesis production thật (tài khoản, số dư, khóa BLS do chủ dự án cung cấp, không commit khóa riêng); quyết định bond.
- [x] P9.1: Công cụ sinh `float_accounts` tự động `execution/cmd/tool/gen_float_accounts` đã hoàn thành và test PASS (json/yaml/patch).
- [ ] P9.2: Kiểm chứng sống trên cụm thật/ansible (gửi cross-chain transfer ở chế độ `enforce` và `warn`).
- [x] P9.3: Cấu hình ansible `bls_conservation_mode` + `BLS_CONSERVATION_INTERVAL_SECONDS` đã tích hợp vào service template, task khởi động và inventory mẫu; tài liệu hóa trong runbook.
- [x] P4: Dọn dẹp nợ kỹ thuật: đã xóa bỏ hoàn toàn hàm chết `CloseSpeculative()` trong `execution/pkg/blockchain/chain_state.go`.
- [ ] Mọi thay đổi consensus/phí (P8, P9) deploy **đồng loạt + wipe** cùng P7; không trộn phiên bản.
- [ ] Không còn credential plaintext trong inventory production.
- [ ] Báo cáo cuối ghi trung thực những gì chưa chứng minh.

### P9. Bảo toàn tổng coin của cụm thực thi (BLS đại diện toàn bộ tài khoản) — ĐÃ CODE

- **Bất biến:** `float(BLS cụm) trên Parent == Σ số dư mọi tài khoản trong cụm` khi nghỉ; khi đang chuyển: `0 ≤ float − Σ ≤ pending` (người gửi bị trừ trước khi Parent trừ float; người nhận được cộng sau khi Parent cộng float). Vi phạm phía dưới = coin không có đảm bảo; phía trên = coin có đảm bảo biến mất.
- **Sửa lỗi gốc:** người gửi trước đây chỉ bị trừ `value` trong khi Parent trừ `value+fee` ⇒ float lệch dần so với tài khoản. Nay người gửi bị trừ `value+fee` (`statemachine.go`, `cross_node_handler.go`), hoàn tiền trả lại `value` (phí bị đốt cả hai phía).
- **Triển khai:** `pkg/rollup/conservation.go` (`CheckConservation`, đo ổn định trước/sau, không ổn định = không kết luận; `ConservationGuard`). Đọc float qua `QuorumClient.GetFloat` (≥ f+1 node đồng ý cùng block+số dư). Chế độ `BLS_CONSERVATION_MODE=enforce|warn|off` (mặc định `enforce`, chu kỳ `BLS_CONSERVATION_INTERVAL_SECONDS`, mặc định 300): `enforce` chặn `SendWorker` và `mtn_sendCrossChainTransfer` cho tới khi có phép đo OK đầu tiên và sau 3 lần vi phạm liên tiếp (thà pending chứ không fork). RPC giám sát: `mtn_getConservation`.
- **Điều kiện vận hành:** `float_accounts` trong genesis Parent của cụm phải bằng đúng Σ alloc của genesis cụm thực thi (devnet mint bằng deposit nên không khớp ⇒ dùng `warn`). 
- **Tiến độ các gói P9:**
  1. **[x] P9.1 Công cụ sinh `float_accounts`:** `execution/cmd/tool/gen_float_accounts` (đọc genesis exec, tính tổng `balance + pending_balance`, xuất json/yaml cho Ansible `parent_float_accounts` hoặc patch trực tiếp `parent_genesis.json`). Unit test `main_test.go` PASS 100%.
  2. **[ ] P9.2 Kiểm chứng sống** trên cụm local (parent 4 + exec) ở chế độ `warn` rồi `enforce`: gửi chuyển cross-chain + refund, `diff` luôn trong `[0, pending]`, về 0 khi nghỉ. Cố ý làm lệch (sửa genesis) để thấy `enforce` chặn (`halted`) và `warn` chỉ log. Ghi kết quả thật vào báo cáo.
  3. **[x] P9.3 Cấu hình ansible:** biến `bls_conservation_mode` + `BLS_CONSERVATION_INTERVAL_SECONDS` trong `inventory.example.yml`, `roles/exec_cluster/templates/metanode-exec-cluster.service.j2`, và `roles/exec_cluster/tasks/main.yml`; mục 4.2 trong runbook `note/runbook_parent_chain_multinode.md`.
  4. **[ ] P9.4 Chi phí đo:** `TotalSupply` dùng `GetAll()` quét toàn bộ tài khoản; đo trên cụm lớn (≥ 1M tài khoản), nếu đắt thì thay bằng tổng cung đã lưu trong state (chỉ làm khi đo cho thấy cần).

## 5. Việc **không** làm
- Không deploy, restart, wipe hay reset cụm 231/230 (hoặc dữ liệu thật) khi chưa có backup và chủ dự án đồng ý rõ ràng.
- Không nới lỏng assertion của test để "cho xanh"; lỗi phải sửa gốc.
- Không dùng timeout để quyết định dispatch commit; không bật lại toàn bộ stall detector (đã từng làm wedge cả cụm — chỉ detector `4a` được chạy riêng).
- Không thêm lại `network_baseline_round` ("coi block round ≤ baseline là đã commit"): tái tạo lỗi block mồ côi đã được ghi trong comment `DagState::is_committed`.
