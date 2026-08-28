# Kế hoạch Thực thi Đầy đủ — 5 Lỗ hổng Đã Xác minh Bằng Code Thật

**Viết:** 2026-08-27. **Dành cho:** agent thực thi kế tiếp (được yêu cầu là Gemini 3.7 Flash) —
tài liệu này viết để 1 agent KHÁC, không có ngữ cảnh hội thoại này, có thể tự đọc và thực thi
toàn bộ mà không cần hỏi lại.

## 0. Bối cảnh & nguồn

File `/home/abc/.gemini/antigravity-ide/brain/1bc0b348-6f4d-4753-bd6b-3dec53888c03/threat_matrix_and_attack_vectors.md`
(Gemini 3.1 Pro, 2026-08-27) đưa ra 1 ma trận mối đe doạ theo tầng (Network/Consensus/FFI/Execution/
Cross-chain). Toàn bộ 17 claim của ma trận đó đã được **verify lại bằng cách đọc code thật** (4
agent riêng biệt, mỗi agent 1 tầng, có trích dẫn file:line) trong phiên làm việc dẫn tới tài liệu
này. Kết quả: **9/17 claim AN TOÀN THẬT** (đã xác minh, không cần làm gì thêm — xem mục 6), và
**5 việc dưới đây là GAP THẬT**, xếp theo mức ưu tiên. Không việc nào trong 5 việc này là suy đoán
— tất cả đều có bằng chứng code cụ thể kèm theo.

**Quan trọng — đọc trước khi bắt đầu bất kỳ việc nào:**
- Đây là dự án blockchain thật (Go execution layer `execution/` + Rust BFT consensus
  `consensus/metanode/`, giao tiếp qua CGO FFI). Một lỗi logic ở đây có thể gây **state fork** —
  hậu quả nghiêm trọng nhất có thể có trong 1 blockchain. Không đoán mò, không "chắc là được" —
  mọi thay đổi phải có test hồi quy thật chứng minh.
- Dự án có nguyên tắc **"Zero-Fork Invariant"** xuyên suốt: KHÔNG BAO GIỜ để 1 node tự ý coi 1
  giao dịch/block là thành công hay thất bại dựa trên timeout/đoán — thà treo (hang) và cần
  restart thủ công còn hơn liều lĩnh tạo ra 2 nhánh trạng thái khác nhau giữa các node. Bất kỳ
  code mới nào thêm vào (đặc biệt Task 2 dưới đây) PHẢI tôn trọng nguyên tắc này: khi không chắc,
  chọn "dừng lại/cảnh báo" chứ không phải "tự đoán rồi tiếp tục".
- Dự án có tiền lệ: **không đoán số ma thuật (magic number)** khi thiếu số liệu vận hành thật —
  xem cách `MinRegistrationStake`/circuit breaker threshold đã làm: thêm field cấu hình, mặc định
  giữ nguyên hành vi cũ, không tự ý chọn 1 con số "hợp lý" mà không giải thích được vì sao.
- Sau MỖI task: chạy `go build ./... && go vet ./... && gofmt -l .` (thư mục `execution/`) và/hoặc
  `cargo build --release && cargo test` (thư mục `consensus/metanode/` — xác nhận đúng thư mục
  cargo workspace trước khi chạy, có thể cần `cd consensus/metanode` hoặc thư mục con). KHÔNG coi
  1 task là xong nếu build/test không chạy qua.
- Mỗi task nên là 1 branch + 1 PR riêng (tạo từ `origin/dev` mới nhất, không phải từ nhánh của
  task khác) — theo đúng quy ước đã dùng trong toàn bộ lịch sử sửa bảo mật của dự án này (xem các
  PR #77-#82 trên GitHub repo `x3pi/metanode` để tham khảo văn phong commit message/PR body).
- KHÔNG tự merge PR — người dùng merge.
- Cập nhật `note/cross_chain_attack_scenario_catalog.md` hoặc tạo mục tương ứng trong chính file
  threat-matrix gốc sau mỗi task, ghi rõ trạng thái đã vá + tên test hồi quy — đây là tài liệu
  "nguồn sự thật" duy nhất về tình trạng bảo mật của dự án, phải luôn khớp với code thật.

---

## 1. Thứ tự ưu tiên

| # | Task | Mức độ | Vì sao thứ tự này |
|---|---|---|---|
| 1 | mTLS consensus gRPC | 🔴 Cao | Cơ chế đã viết sẵn 90%, chỉ bị hardcode tắt — sửa nhanh, rủi ro thấp nếu làm đúng thứ tự rollout |
| 2 | Cross-node state-root attestation | 🔴 Cao, phức tạp nhất | Nghiêm trọng nhất về hậu quả (fork thầm lặng) nhưng cần thiết kế cẩn thận — làm sau Task 1 vì cả 2 đều đụng tầng network Rust, tránh xung đột nhánh |
| 3 | Per-IP rate limit tầng P2P TCP | 🟡 Trung bình-cao | Độc lập, có thể làm song song với Task 1/2 |
| 4 | Phí gas chống dust-account | 🟡 Trung bình-cao | Độc lập (Go execution layer, không đụng Rust) |
| 5 | Bọc `catch_unwind` còn thiếu | 🟢 Thấp | Nhanh, làm cuối cùng hoặc bất kỳ lúc nào rảnh |

Task 3, 4, 5 không phụ thuộc lẫn nhau hay phụ thuộc Task 1/2 — có thể làm song song nếu có nhiều
agent. Task 2 nên làm sau Task 1 (không bắt buộc, nhưng cả hai cùng sửa `tonic_network.rs`/tầng
mạng consensus, làm tuần tự tránh conflict).

---

## 2. TASK 1 — Bật lại mTLS trên cổng gRPC consensus (🔴 Cao)

### Bằng chứng (đã xác minh, đọc trực tiếp)

File `consensus/metanode/meta-consensus/core/src/network/tonic_network.rs`:
- **Phía server** (dòng 1028-1046): 
  ```rust
  let _disable_tls = true; // Hardcode for testing
  let tls_server_config = if true {
      None  // Hardcode disable TLS for testing
  } else {
      Some(meta_tls::create_rustls_server_config_with_client_verifier(
          self.network_keypair.clone().private_key().into_inner(),
          certificate_server_name(&self.context),
          AllowPublicKeys::new(
              self.context.committee.authorities().map(|(_i, a)| a.network_key.to_bytes()).collect(),
          ),
      ))
  };
  ```
  Nhánh `else` (mTLS thật, xác thực client bằng đúng tập public key của committee hiện tại) đã
  viết đầy đủ, chỉ không bao giờ chạy vì `if true`.
- **Phía client** (dòng 472-482), nghiêm trọng hơn — **hoàn toàn KHÔNG có code TLS nào**, kể cả
  code đã comment sẵn:
  ```rust
  let disable_tls = true; // Hardcode for testing
  let addr = if disable_tls { format!("http://{address}") } else { format!("https://{address}") };
  let endpoint = tonic::transport::Channel::from_shared(addr.clone())...
  ```
  Nghĩa là: **mọi kết nối gRPC giữa các node consensus hiện tại đều là plaintext HTTP không xác
  thực**, ai route được tới cổng consensus (`peer_rpc_port`/tương đương) đều kết nối được, không
  cần chứng minh là thành viên committee.

### Hạ tầng đã có sẵn (không cần viết crypto mới)

`crates/meta-tls/src/lib.rs` đã có đủ API cho cả 2 phía:
- Server: `create_rustls_server_config_with_client_verifier(private_key, server_name, allower)` —
  đã dùng ở trên, chỉ cần bỏ hardcode.
- Client: `create_rustls_client_config(target_public_key: Ed25519PublicKey, server_name: String,
  client_key: Option<Ed25519PrivateKey>)` — **đã tồn tại, chưa từng được gọi ở đâu trong
  `tonic_network.rs`**. `target_public_key` ở đây là public key network của ĐÚNG peer đang kết
  nối tới (không phải tập hợp allow-list) — lấy từ
  `self.context.committee.authority(peer).network_key` (biến `authority` đã có sẵn ở dòng 465,
  ngay phía trên đoạn code cần sửa).
- Cả 2 phía đều dùng self-signed certificate sinh trực tiếp từ network keypair của node (xem
  `SelfSignedCertificate::new` trong `crates/meta-tls/src/certgen.rs`) — **không cần hạ tầng CA
  ngoài, không cần cấp phát cert thủ công**, mọi thứ suy ra được từ dữ liệu committee đã có sẵn
  trong `self.context`.

### Việc cần làm

1. **Server side** (`tonic_network.rs` quanh dòng 1028-1046): bỏ hẳn `let _disable_tls = true` và
   `if true { None }`. Instead of gating on a hardcoded bool, luôn xây `tls_server_config` bằng
   nhánh `else` hiện có (biến nó thành nhánh duy nhất, không cần `if/else` nữa nếu quyết định
   không giữ đường thoát tắt TLS — xem lưu ý "Không giữ cờ tắt TLS" bên dưới).
2. **Client side** (`tonic_network.rs` quanh dòng 465-495): thêm code xây `ClientTlsConfig` bằng
   `meta_tls::create_rustls_client_config(authority.network_key.clone(), <server_name khớp với
   phía server>, Some(self.network_keypair.clone().private_key().into_inner()))`, rồi gọi
   `.tls_config(...)` trên `tonic::transport::Channel::from_shared(...)` TRƯỚC khi `.connect()`
   (tham khảo cách `meta_http::Builder` phía server áp `tls_config` ở dòng 1100-1104 để biết tên
   API tương đương phía client của crate `tonic`/`meta_http` đang dùng — có thể cần
   `Endpoint::tls_config()` thay vì raw `Channel`, kiểm tra type thật của `endpoint` trước khi
   sửa). Đổi `format!("http://{address}")` thành luôn `format!("https://{address}")` một khi TLS
   luôn bật.
3. **`certificate_server_name(&self.context)`** — xác nhận hàm này trả về CÙNG 1 giá trị ở cả 2
   phía (client verify tên server phải khớp với tên server tự ký) — đọc định nghĩa hàm này trước
   khi sửa, đừng đoán.
4. **Không giữ cờ tắt TLS mặc định tắt-được** (theo đúng tiền lệ dự án với `ReserveChainID`: "không
   có hành vi cũ an toàn nào đáng giữ lại, hành vi trước khi vá CHÍNH LÀ lỗ hổng"). Nếu vẫn muốn
   giữ 1 cờ cho môi trường devnet cục bộ (nhiều tiến trình 1 máy), đặt theo đúng pattern
   `DevnetGovernanceTimelockSecondsOverride` đã có trong dự án: field cấu hình opt-in, mặc định
   RỖNG/false = mTLS luôn bật, phải set tường minh mới tắt được, và ghi rõ trong doc comment "chỉ
   dùng cho devnet, không bao giờ dùng cho triển khai thật".

### Rủi ro triển khai cần lưu ý (không phải rủi ro code, rủi ro vận hành)

Đây là traffic giữa các validator ĐÃ BIẾT TRƯỚC (committee cố định, không phải mạng công khai) —
không cần rollout dần dần kiểu "gradual rollout" của mainnet công khai. NHƯNG: bật mTLS 2 chiều
đồng nghĩa **mọi node consensus phải deploy code mới CÙNG LÚC** — 1 node cũ (client plaintext) sẽ
không kết nối được tới 1 node mới (server đòi mTLS), và ngược lại. Ghi rõ trong PR description:
"cần deploy đồng loạt tất cả node consensus trong 1 lần bảo trì, không thể rollout dần từng node."

### Tiêu chí hoàn thành

- Test tích hợp: dựng 2 `TonicManager` (hoặc tương đương) trong cùng 1 test, xác nhận: (a) 2 node
  đều dùng key hợp lệ trong committee → kết nối + gửi/nhận message thành công; (b) 1 "node" dùng
  key KHÔNG nằm trong committee cố gắng kết nối tới server → bị từ chối ở tầng TLS handshake
  (không tới được tầng ứng dụng).
- `cargo build --release` + `cargo test` sạch trong `consensus/metanode/`.
- Xác nhận sống (nếu có thể): chạy lại `private_chain_3node/` hoặc cụm devnet đã có sẵn trong repo,
  quan sát log xác nhận các node vẫn connect được nhau bình thường sau khi bật mTLS.

---

## 3. TASK 2 — Cơ chế phát hiện lệch state root giữa các node đang chạy thật (🔴 Cao, EXE-01)

### Bằng chứng (đã xác minh, đọc trực tiếp)

Đây là 1 kiến trúc Sui/Mysticeti-style: Rust consensus chỉ đồng thuận về **thứ tự giao dịch**
(commit_index/GEI), KHÔNG đồng thuận về **kết quả state**. Go thực thi độc lập trên từng node và tự
tính state root SAU KHI Rust đã chốt thứ tự — nghĩa là 1 bug non-determinism ở tầng Go (map
iteration order, race condition, floating point, v.v.) có thể khiến 2 node có CÙNG lịch sử giao
dịch nhưng KHÁC state root, và **hiện tại không có gì runtime phát hiện việc này** ngoài 1 cơ chế
yếu:

1. **Module được thiết kế đúng cho việc này tồn tại nhưng KHÔNG chạy**:
   `consensus/metanode/src/consensus/state_attestation.rs` — struct `AttestationMonitor`, có đủ
   logic cache state root cục bộ, nhận/so state root từ peer, đếm số lần lệch liên tiếp
   (`divergence_counter`). NHƯNG:
   - `consensus/mod.rs` không có dòng `mod state_attestation;` nào — **file này không được biên
     dịch vào binary**, chết hoàn toàn.
   - Hàm `broadcast_attestation()` (dòng 95-98) thân hàm chỉ có 1 dòng log + comment
     `// TODO: Integrate with peer_rpc or peer_discovery to flood this to peers` — chưa từng gửi
     đi đâu cả, kể cả nếu được wire vào.
   - Nhánh xử lý khi phát hiện lệch (dòng 142-145, `if *div_count >= 3`) chỉ **ghi log**
     `error!("🛑 [SAFETY PAUSE] Halting consensus authority...")` — KHÔNG có hành động thật nào
     (không pause, không rollback, không gọi bất kỳ API nào).
2. **Cơ chế đang chạy thật DUY NHẤT** là `PostRecoveryHealthCheck` (file `health_check.rs`, gọi
   từ `consensus/metanode/src/node/setup_consensus/mod.rs:1106-1137`):
   - Chạy **đúng 1 lần**, 30 giây sau khi node khởi động, thử lại tối đa 3 lần cách nhau 30s — sau
     đó **không bao giờ chạy lại** trong suốt vòng đời node.
   - Dung sai ±5 block khi so sánh.
   - **Fail-open**: `result.state_root_match = true; // Best effort check, pass if network fails`
     — nếu không hỏi được peer, coi như "khớp", không coi là dấu hiệu cảnh báo.
   - Kể cả khi phát hiện lệch thật, chỉ log `"Manual investigation required"` — không có hành
     động enforcement nào.

### Việc cần làm — 2 phương án, chọn 1 sau khi điều tra thêm

**Phương án A (khuyến nghị, rủi ro thấp hơn — mở rộng cơ chế đang chạy thay vì hồi sinh module
chết):**
1. Tìm cơ chế RPC query cross-node đã có sẵn và đang hoạt động thật mà `health_check.rs` dùng để
   hỏi state root của peer — theo kết quả điều tra tầng Network (agent khác trong phiên này),
   các file `rpc_queries.rs`/`rpc_queries_epoch.rs` (dòng ~33, 252, 326, 384, 429, 478, 30, 277)
   có các hàm state-query đã wire qua `RpcCircuitBreaker` thật — rất có thể đây chính là kênh
   `health_check.rs` dùng để hỏi peer. Xác nhận bằng cách đọc `health_check.rs` xem nó gọi hàm nào
   để lấy state root của peer.
2. Biến việc gọi đó từ "chạy 1 lần lúc khởi động" thành **1 vòng lặp định kỳ chạy suốt vòng đời
   node** (ví dụ mỗi N block hoặc mỗi N giây — dùng lại đúng interval `ATTESTATION_BLOCK_INTERVAL
   = 10` block đã định nghĩa sẵn trong `state_attestation.rs` làm gợi ý tần suất, không nhất thiết
   phải tái sử dụng file đó).
3. **Đổi fail-open thành fail-safe có ý thức**: khi không hỏi được peer (mạng lỗi), KHÔNG coi là
   "khớp" — chỉ log cảnh báo "không xác minh được" và tiếp tục theo dõi, không dừng node vì 1 lần
   mất mạng đơn lẻ (tôn trọng Zero-Fork Invariant: mất mạng không phải bằng chứng có fork, nhưng
   cũng không phải bằng chứng KHÔNG có fork — đừng quy về true/false, dùng 1 trạng thái thứ 3 kiểu
   `Unknown`/`Unverified` thay vì ép về `true`).
4. Khi phát hiện lệch THẬT (đã hỏi được peer, state root khác nhau, không phải do lỗi mạng) và lặp
   lại đủ số lần để loại trừ khả năng do đang trong cửa sổ ±N block bình thường (race giữa lúc 2
   node chưa cùng tiến độ) → **hành động thật**: tìm hàm nội bộ Rust mà `metanode_pause_consensus`
   (FFI export đã xác nhận tồn tại và đã được bọc `catch_unwind` an toàn, xem
   `consensus/metanode/src/ffi.rs` dòng ~102) gọi vào bên trong — đó là hàm Rust nội bộ cần gọi
   trực tiếp từ đây (không cần đi qua CGO vì đây là code Rust gọi Rust cùng tiến trình). Việc pause
   phải là **dừng lại, không phải rollback tự động** — đúng tinh thần Zero-Fork Invariant: dừng để
   người vận hành can thiệp thủ công, không tự ý đoán cách sửa.

**Phương án B (nếu A không khả thi sau khi điều tra — hồi sinh `state_attestation.rs`):**
1. Thêm `mod state_attestation;` vào `consensus/mod.rs` (hoặc file mod.rs đúng cấp — kiểm tra vị
   trí thật của file so với module cha).
2. Viết thật `broadcast_attestation()` — cần 1 kênh gửi message tới peer. Tầng tonic gRPC
   (`tonic_network.rs`, cũng đang sửa ở Task 1) là ứng viên tự nhiên nếu nó có sẵn 1 kiểu message
   generic broadcast; nếu không, đây là việc thêm 1 RPC method mới vào file `.proto` của consensus
   service — việc lớn hơn, cần đánh giá kỹ trước khi chọn phương án này.
3. Sửa nhánh "chỉ log" (dòng 142-145) thành gọi hàm pause thật, như bước 4 của Phương án A.
4. Gọi `on_block_committed`/`on_attestation_received` từ đúng chỗ block được commit thật (tìm nơi
   Go trả state root về Rust qua FFI sau `execute_block` — đây chính là hook point).

**Không tự ý chọn phương án mà không ghi lại lý do trong PR description** — dù chọn A hay B, giải
thích rõ tại sao trong PR.

### Tiêu chí hoàn thành

- Test hồi quy giả lập: 2 "node" trong test có state root CỐ Ý khác nhau ở cùng 1 block → xác nhận
  cơ chế phát hiện được VÀ hành động pause thật được gọi (không chỉ log).
- Test hồi quy: khi 1 peer không phản hồi (giả lập lỗi mạng) → xác nhận node KHÔNG bị coi là "đã
  xác minh khớp" một cách sai lệch, nhưng cũng KHÔNG bị pause oan (đây là 2 test case riêng biệt,
  cả hai đều phải pass).
- Chạy định kỳ suốt vòng đời node, không chỉ 1 lần lúc khởi động — xác nhận bằng test có mô phỏng
  thời gian trôi qua N chu kỳ.
- `cargo build --release && cargo test` sạch.

---

## 4. TASK 3 — Rate limit theo IP ở tầng P2P TCP (🟡 Trung bình-cao, NET-02)

### Bằng chứng

`execution/pkg/network/server.go`:
- Đã có giới hạn TOÀN CỤC: `connSem := make(chan struct{}, maxConns)` (mặc định 1000, dòng 64-77),
  `RequestChanSize: 500000` (bounded, có drop+`ServerBusy` khi đầy, dòng 230-246).
- **KHÔNG có giới hạn theo từng IP nguồn** — 1 peer có thể mở nhiều kết nối chiếm hết slot toàn
  cục dễ như nhiều peer khác nhau làm vậy.
- `network.CircuitBreaker` (file `handler.go`, dòng 120-130) **cố ý loại trừ** đúng các lệnh quan
  trọng nhất (`BlockDataTopic`, `TransactionsFromSubTopic`, `SendTransaction(s)` — đánh dấu
  `isCritical`, comment "Consensus commands: must never be rejected") — nghĩa là cơ chế breaker
  hiện có không giúp gì chống flood ở đúng loại traffic này (đây là quyết định ĐÚNG cho circuit
  breaker hiện tại — breaker không nên chặn command hợp lệ; nhưng đồng nghĩa cần 1 cơ chế RIÊNG
  cho rate-limit, không phải sửa breaker này).

### Việc cần làm

1. Thêm 1 map đếm kết nối/request theo IP (rút gọn từ `RemoteAddr`, bỏ port) tại `server.go`, gần
   chỗ `connSem` đã có — ví dụ 1 `map[string]int` bảo vệ bằng mutex, hoặc dùng lại pattern
   `RpcRateLimitConfig` đã có sẵn cho tầng JSON-RPC HTTP (`execution/pkg/config/config.go:145-150`
   theo memory nội bộ dự án) làm **tham khảo cấu trúc field** (không phải dùng lại chính field đó
   — tầng P2P TCP là tầng khác, cần field cấu hình riêng, ví dụ `P2PMaxConnectionsPerIP` trong
   `NetworkConfig`/tương đương).
2. Tại điểm accept connection trong `Listen()` (khoảng dòng 260-312): sau khi qua được `connSem`,
   kiểm tra thêm số kết nối hiện tại từ CHÍNH IP đó có vượt ngưỡng không — nếu vượt, đóng kết nối
   ngay, log cảnh báo, **không** chiếm 1 slot của `connSem` (hoặc release ngay lập tức).
3. Cân nhắc thêm rate-limit theo IP ở tầng request/message bên trong 1 kết nối đã mở (không chỉ số
   lượng kết nối) — tuỳ mức độ chi tiết muốn đạt, có thể để việc này là bước 2 riêng nếu bước 1 đã
   đủ giảm rủi ro đáng kể.
4. **Không tự chọn con số ngưỡng** (bao nhiêu kết nối/IP là hợp lý) mà không giải thích — đề xuất
   giá trị mặc định dựa trên số lượng validator/relayer dự kiến kết nối từ CÙNG 1 IP trong triển
   khai thật của dự án này (ví dụ nhiều node cùng chạy trên 1 máy vật lý trong cụm test — tham
   khảo `note/` để hiểu mô hình triển khai thật trước khi chọn số), mặc định đủ cao để không phá
   vỡ cụm devnet nhiều node cùng máy hiện có (`private_chain_3node/`).

### Tiêu chí hoàn thành

- Test: mô phỏng 1 IP mở kết tiếp nhiều kết nối vượt ngưỡng cấu hình → xác nhận các kết nối vượt
  ngưỡng bị từ chối, kết nối trong ngưỡng vẫn hoạt động bình thường.
- Xác nhận cụm devnet nhiều node/1 máy hiện có (`private_chain_3node/`) vẫn chạy được sau khi thêm
  giới hạn (không tự đóng kết nối hợp lệ giữa các node cùng máy) — hoặc nếu ngưỡng mặc định không
  đủ cho cụm test, tài liệu hoá rõ cách chỉnh cấu hình cho môi trường test nhiều node/1 máy.
- `go build/vet/gofmt/test` sạch.

---

## 5. TASK 4 — Phí gas chống dust-account (🟡 Trung bình-cao, EXE-03)

### Bằng chứng

- `execution/pkg/pruning/` chỉ dọn dữ liệu LỊCH SỬ (block/epoch cũ đã qua ngưỡng
  `EpochsToKeep`/`PruneNomtEpoch`) — không đụng tới account đang sống trong trie hiện tại, bất kể
  account đó có hoạt động hay không.
- Không có bất kỳ cơ chế dust/rent/minimum-balance/eviction nào trong toàn bộ `execution/` (đã
  grep xác nhận).
- `TRANSFER_GAS_COST = 20000` (`execution/pkg/common/constant.go:11`) là phí CỐ ĐỊNH cho mọi
  native transfer, dùng ở CẢ 3 nơi: `native_fast_path.go` (dòng 191, 243, 250),
  `true_block_stm.go` (dòng 604, 629, 687, 781), `receipt_helper.go` (dòng 28, 81) — **không phân
  biệt** transfer tới 1 account đã tồn tại hay tạo account HOÀN TOÀN MỚI. Ethereum mainnet tính
  thêm `CallNewAccountGas` (thường 25000) khi transfer tạo account mới — dự án này đã CÓ hằng số
  tương đương (`params.CallNewAccountGas`, đang dùng cho EIP-7702 tại `authorization.go:85`) nhưng
  **chưa áp dụng cho native transfer thường**.

### Việc cần làm

1. Tìm/xác nhận hàm kiểm tra "account đã tồn tại chưa" trong `AccountStateDB` (rất có thể đã có
   sẵn dạng `Exist(address)`/`GetAccount(address) (exists bool)` — tìm trong
   `execution/pkg/account_state_db/`, đừng viết hàm mới nếu đã có).
2. Tại **cả 3 điểm** tính `TRANSFER_GAS_COST` (`native_fast_path.go`, `true_block_stm.go`,
   `receipt_helper.go`) — đây là điểm khó nhất của task này: phải sửa NHẤT QUÁN cả 3 nơi, vì đây
   là 2 pipeline thực thi khác nhau (fast-path cho block toàn-native-transfer, và Block-STM đầy đủ
   cho block hỗn hợp) cộng 1 helper tính receipt dùng chung — lệch nhau giữa 2 pipeline sẽ tạo ra
   **đúng loại bug EXE-01 đang lo** (2 node/2 pipeline tính gas khác nhau → state root khác nhau).
   Thêm gas surcharge = `CallNewAccountGas` (dùng lại đúng field, không tạo constant riêng) khi
   VÀ CHỈ KHI địa chỉ NGƯỜI NHẬN chưa tồn tại trong state trie TRƯỚC giao dịch này.
3. **Tính determinism**: việc kiểm tra "account đã tồn tại chưa" phải được thực hiện tại đúng thời
   điểm account state được đọc để áp dụng transfer (không phải đọc lại sau khi đã tạo — sẽ luôn
   trả về "đã tồn tại", vô nghĩa). Đọc kỹ thứ tự code hiện tại ở cả 3 file trước khi chèn logic
   mới, đừng đoán thứ tự.
4. **Không tự chọn giá trị `CallNewAccountGas` mới** — dùng lại đúng hằng số đã có trong
   `authorization.go:85` (`params.CallNewAccountGas`), đảm bảo nhất quán trong toàn hệ thống.
5. Cân nhắc: task này có nên áp dụng ngay lập tức, hay cần cờ cấu hình bật/tắt cho các chain đã
   deploy từ trước (thay đổi phí gas là thay đổi có thể ảnh hưởng tương thích ngược với genesis
   config/gas budget đã tính sẵn của người dùng hiện tại)? Ghi rõ quyết định + lý do trong PR
   description, theo đúng tinh thần "không đoán mò" của dự án.

### Tiêu chí hoàn thành

- Test hồi quy trên CẢ HAI pipeline (`native_fast_path` và Block-STM đầy đủ): transfer tới account
  mới tính đúng `TRANSFER_GAS_COST + CallNewAccountGas`; transfer tới account đã tồn tại tính đúng
  y hệt `TRANSFER_GAS_COST` như cũ (không phá vỡ hành vi cũ cho trường hợp phổ biến nhất).
  - **Bắt buộc có 1 test so sánh chéo 2 pipeline cho CÙNG 1 kịch bản** (transfer tạo account mới)
    để đảm bảo chúng tính RA CÙNG 1 con số gas — đây là test quan trọng nhất của task này.
- `go build/vet/gofmt/test` sạch, bao gồm `true_block_stm_integration_test.go` hiện có (đã tham
  chiếu `TRANSFER_GAS_COST` trực tiếp ở dòng 240 — kiểm tra test này còn đúng sau khi sửa, sửa lại
  nếu cần chứ đừng làm yếu test để nó pass).

---

## 6. TASK 5 — Bọc `catch_unwind` còn thiếu (🟢 Thấp, FFI-03 residual)

### Bằng chứng

`consensus/metanode/src/ffi.rs`: 5/6 hàm export FFI đã bọc `std::panic::catch_unwind`. Thiếu:
- `metanode_register_callbacks` (dòng ~92-97) — hoàn toàn chưa bọc:
  ```rust
  #[no_mangle]
  pub extern "C" fn metanode_register_callbacks(callbacks: GoCallbacks) {
      if GO_CALLBACKS.set(callbacks).is_err() {
          eprintln!("Warning: metanode_register_callbacks called multiple times");
      }
  }
  ```
- `metanode_start_consensus` (dòng ~297): phần THÂN của thread con đã bọc (dòng ~394-509), nhưng
  đoạn PARSE THAM SỐ ĐẦU VÀO trước khi spawn thread (dòng ~302-318, `CStr::from_ptr(...)
  .to_string_lossy()`) thì KHÔNG nằm trong catch_unwind.

### Việc cần làm

Bọc thân của `metanode_register_callbacks` bằng `std::panic::catch_unwind(std::panic::
AssertUnwindSafe(|| { ... }))`, theo đúng pattern (macro `ffi_catch_unwind!` nếu file này có định
nghĩa/import macro tương tự macro đã dùng trong `nomt_ffi/rust_lib/src/lib.rs:23-32` — kiểm tra
xem có thể tái sử dụng chung 1 macro giữa 2 file FFI này không, tránh viết trùng lặp). Với
`metanode_start_consensus`, mở rộng vùng `catch_unwind` hiện có để bao gồm luôn đoạn parse tham số
đầu vào ở đầu hàm, không chỉ phần thân thread con.

### Tiêu chí hoàn thành

- Test hồi quy tương tự `metanode_submit_transaction_batch_survives_internal_panic` (dòng ~635,
  đã có sẵn làm mẫu) — viết 1 test tương đương cho `metanode_register_callbacks` chứng minh 1
  panic giả lập bên trong không làm crash tiến trình gọi.
- `cargo build --release && cargo test` sạch.

---

## 7. Các claim ĐÃ XÁC MINH AN TOÀN — không cần làm gì (tránh mất công điều tra lại)

Liệt kê ở đây để agent thực thi KHÔNG lãng phí thời gian re-verify những gì đã xác minh kỹ trong
phiên tạo ra tài liệu này:

- **CON-01, CON-02, CON-03, CON-04** (toàn bộ tầng đồng thuận Rust): an toàn thật, đã đọc tận
  logic sắp xếp/commit. Đặc biệt CON-03 (nghiêm trọng nhất theo ma trận gốc): thứ tự commit dùng
  `sort_sub_dag_blocks()` (`commit.rs`) — `(round, author_index)`, không dùng timestamp/hash-map
  iteration ở bất kỳ đâu trong đường commit.
- **FFI-01, FFI-02** (rò rỉ bộ nhớ CGO, dangling pointer): đã mitigate đầy đủ ở cả 2 boundary
  (`nomt_ffi` và `consensus ffi.rs`). Claim gốc của Gemini về "attestCommit qua CGO" bị sai hoàn
  toàn — `attestCommit` không dùng CGO.
- **NET-01** (Eclipse Attack): an toàn thật, xác nhận qua `enough_leader_support()`/
  `reached_quorum()` — không có đường nào tạo commit giả khi bị cô lập.
- **NET-03** (giả thiết "UDS jamming" của Gemini): khung giả thiết sai — kênh RPC nội bộ Go↔Rust
  là gọi hàm trong-process (CGO trực tiếp), không lộ ra mạng để tấn công từ xa như NET-02. Rủi ro
  còn lại (treo node khi Go side chậm) là đánh đổi CHỦ ĐÍCH đã biết của Zero-Fork Invariant, không
  phải gap mới.
- **EXE-02** (goroutine/queue leak): an toàn thật — connection semaphore, worker pool cố định,
  mempool có ngưỡng + eviction, tất cả đã đọc và xác nhận có giới hạn cứng.
- **XCH-01..04**: đã đóng qua các PR bảo mật trước (`cross_chain_attack_scenario_catalog.md` mục
  B/C) — XCH-03 (`MinRegistrationStake`) và XCH-04 (`ReserveChainID`) chính là 2 cơ chế vừa thêm
  trong các PR #80/#82 của cùng dự án này.

---

## 8. Checklist hoàn thành cuối cùng (điền khi xong TOÀN BỘ 5 task)

- [ ] Task 1 — mTLS: PR mở, build/test sạch, test tích hợp reject-non-committee-key pass.
- [ ] Task 2 — State-root attestation: PR mở, build/test sạch, test giả lập lệch state root +
      test fail-safe-khi-mất-mạng đều pass, hành động pause thật được xác nhận gọi (không chỉ log).
- [ ] Task 3 — Per-IP rate limit: PR mở, build/test sạch, cụm devnet nhiều node/1 máy vẫn chạy.
- [ ] Task 4 — Phí dust-account: PR mở, build/test sạch, 2 pipeline (fast-path/Block-STM) tính
      RA CÙNG 1 SỐ gas cho cùng kịch bản.
- [ ] Task 5 — catch_unwind còn thiếu: PR mở, build/test sạch.
- [ ] Cập nhật `note/cross_chain_attack_scenario_catalog.md` HOẶC file threat-matrix gốc, mục
      trạng thái mỗi ID (NET-02, EXE-01, EXE-03, FFI-03) → ✅, trỏ đúng tên test hồi quy.
- [ ] Không PR nào tự merge — báo lại người dùng danh sách PR đang chờ merge.
