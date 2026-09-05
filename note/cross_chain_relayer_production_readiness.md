# 📘 CROSS-CHAIN RELAYER — ĐÁNH GIÁ KIẾN TRÚC & TÀI LIỆU VẬN HÀNH PRODUCTION

> Viết 2026-09-05, sau khi rà soát toàn bộ `execution/pkg/cross_chain/relayer_daemon/` +
> `execution/cmd/tool/cross_chain_relayer/` cho mức độ sẵn sàng production. Tài liệu này vừa là
> bản đánh giá kiến trúc, vừa là runbook vận hành thực tế (yêu cầu triển khai, chạy nhiều instance,
> giám sát/cảnh báo, và các giới hạn còn tồn đọng).

---

## 1. Relayer là gì, chạy ở đâu trong hệ thống

`RelayerDaemon` (`execution/pkg/cross_chain/relayer_daemon/daemon.go`) là tiến trình nền theo dõi
các message cross-chain đang chờ (`outbound()` trên 1 chain nguồn), gom chúng thành 1 commit
(`batchOutboundCommit`), chờ đủ chữ ký BLS quorum từ Root Anchor, rồi tự gửi `attestCommit` +
`claimMessage` lên chain đích để hoàn tất chuyển tiền/message. Binary thực thi là
`cross_chain_relayer` (`execution/cmd/tool/cross_chain_relayer/main.go`).

Đây là tiến trình **off-chain, không nằm trong tập validator** — nó chỉ ký giao dịch bằng key
riêng của chính nó và gửi qua RPC công khai, giống hệt bất kỳ client JSON-RPC nào khác.

---

## 2. Điều kiện cần để chạy 1 Relayer

| Điều kiện | Chi tiết |
|---|---|
| **Key riêng có số dư thật** | `relayer_key` phải có đủ native coin trên **mọi chain nó gửi giao dịch tới** (Root Anchor nếu dùng Reserve routing, + mọi chain đích) để trả gas. Hết số dư = giao dịch không gửi được, message bị kẹt vô thời hạn không có cảnh báo (đã vá, xem mục 4.2). |
| **RPC tới mọi chain liên quan** | Cần 1 JSON-RPC endpoint sống cho Root Anchor và mỗi private chain được cấu hình trong `chains`/`nodes`. |
| **Chain đã đăng ký trên Root Anchor** | `GetChainRegistry` phải trả `exists=true` với `Committee` khác rỗng — nếu không `pollAndAggregateCommitCert` sẽ lỗi ngay. |
| **Tổng stake ủy ban > 0** | `totalStake == 0` → lỗi tường minh, không rơi vào vòng lặp treo. |
| **Tối thiểu 2 chain cấu hình** | `main.go` chặn cứng nếu `len(chainRPCs) < 2` (cần cả nguồn lẫn đích để relay được gì đó). |
| **`ReserveChainID` (tùy chọn)** | Tự động dò từ `eth_chainId` của chính Root Anchor RPC nếu không set — chỉ cần set tay khi Root Anchor không phải Reserve. |

---

## 3. Có hỗ trợ chạy nhiều Relayer song song không?

**Có — ở tầng contract/protocol** (đã có sẵn từ trước), **và giờ đã có ở tầng vận hành/tooling**
(bổ sung 2026-09-05).

### 3.1. Tầng contract — permissionless, idempotent, có động lực kinh tế (không đổi)
* Gateway dispatch (`gateway_handler.go`) **không kiểm tra `tx.FromAddress()`** cho
  `attestCommit`/`claimMessage`/`batchOutboundCommit` — quyền hạn đến hoàn toàn từ việc xác minh
  chữ ký BLS quorum bên trong, không phải danh tính người gửi.
* `GatewayEngine.attestCommitInternal` có write-once guard thật (`AttestedCommits[key]`) — 1 relayer
  thứ 2 gọi lại `attestCommit` cho cùng 1 commit sẽ nhận về đúng kết quả cũ, vô hại.
* `ClaimMessage` khóa `g.mu` và kiểm tra `MessageStatus` atomically — chỉ 1 trong N relayer đua
  nhau claim cùng 1 message thắng, các bên còn lại nhận `ErrAlreadyClaimed`, không double-spend.
* Bên thắng nhận `message.Tip` vào `RelayerBalances[relayer]` (rút bằng `withdrawRelayerTip`) — đây
  là động lực kinh tế để nhiều relayer độc lập cùng cạnh tranh chạy dịch vụ.
* **2026-09-05: claim thật sự này giờ có bài test tự động** —
  `TestRelayerDaemon_TwoConcurrentInstances_NoDoubleProcessing` (`daemon_test.go`) dựng 2
  `RelayerDaemon` với 2 key khác nhau, cho cả hai gọi `RelayBatch` đồng thời lên cùng 1
  `GatewayEngine`, rồi assert: mỗi message settle đúng 1 lần ở `Success`, tổng tip được cộng dồn
  đúng bằng giá trị 2 message (không nhân đôi, không mất). Trước bản vá này, tính an toàn khi đua
  chỉ được xác nhận bằng cách đọc code, chưa từng có test thực thi song song.

### 3.2. Tầng vận hành/tooling — trước đây là khoảng trống, giờ đã có
Trước 2026-09-05: `deploy/ansible_private_chains/run_relayer_tmux.sh` hard-code
`SESSION_NAME="relayer"`, không có cách chạy instance thứ 2 mà không tự tay sửa script; và ngay cả
nếu chạy tay 2 tiến trình, `stop_relayer()`'s `pkill -9 -f "cross_chain_relayer"` sẽ **giết luôn cả
2 instance** bất kể instance nào được yêu cầu dừng.

Đã vá — xem mục 5 (Runbook chạy nhiều Relayer).

---

## 4. Các lỗ hổng production-readiness đã tìm thấy & đã vá (2026-09-05)

| Mức độ | Vấn đề | Đã vá bằng |
|---|---|---|
| 🔴 Nghiêm trọng | `sendToChain` hard-code `GasPrice = 1 Gwei`, không có fee estimation | `rootanchor.Client.SuggestGasPrice` (eth_gasPrice) + `resolveGasPrice` trong `gas_price.go`, có cache TTL, bump %, và trần an toàn (mục 4.1) |
| 🟠 Cao | Không có metrics/health/HTTP endpoint nào | `metrics.go`: `/metrics` (Prometheus) + `/health` (JSON), bật qua `StartMetricsServer` (mục 4.2) |
| 🟠 Cao | Toàn bộ state (`processedMessages`, `attestedCommits`, `nonces`, ...) chỉ nằm trong RAM | Không phải lỗi an toàn (write-once guard on-chain đã bảo vệ) — vẫn còn là giới hạn đã biết, xem mục 6 |
| 🟡 Trung bình | `main.go` spawn watcher cho MỌI cặp (nguồn, đích) — O(N²) | Chưa vá (mục 6 — quyết định không tối ưu sớm khi chưa có dữ liệu thật cho thấy cần) |
| 🟡 Trung bình | `WatchChainPair` luôn ngủ đúng `PollInterval` dù lỗi liên tục | `backoffDuration` trong `metrics.go`: exponential backoff có trần `MaxPollBackoff` (mục 4.3) |

### 4.1. Gas pricing động (`gas_price.go`)
`resolveGasPrice(ctx, chainID, client)` thay hoàn toàn hằng số `1_000_000_000` cũ:
1. Dùng lại giá đã cache nếu còn mới (`GasPriceCacheTTL`, mặc định 5s — tránh gọi `eth_gasPrice`
   cho từng giao dịch trong 1 đợt gửi liên tiếp của `RelayBatch`).
2. Gọi `eth_gasPrice` thật của chain đích.
3. Lỗi hoặc giá trị ≤0 → dùng `FallbackGasPriceWei` (mặc định đúng 1 Gwei cũ, để hành vi lỗi giữ
   nguyên như trước bản vá).
4. `GasPriceBumpPercent` (>100) nếu muốn ưu tiên tốc độ vào block trong lúc phí biến động.
5. `MaxGasPriceWei` — trần an toàn cứng, chặn 1 RPC hỏng/độc hại đề xuất giá phi lý rút cạn ví.

Cấu hình qua `DaemonConfig` (JSON) hoặc flag CLI: `-gas-price-bump-percent`, `-max-gas-price-gwei`.

### 4.2. Metrics + Health endpoint (`metrics.go`)
Bật bằng `-metrics-addr :9090` (mặc định bật sẵn khi chạy `main.go` trực tiếp; script tmux có logic
riêng — xem mục 5).

* **`GET /metrics`** — chuẩn Prometheus text format, gồm:
  * `relayer_messages_relayed_total{source_chain,dest_chain}` — đếm message relay thành công.
  * `relayer_watch_errors_total{source_chain,dest_chain}` — đếm lỗi mỗi tick watch loop.
  * `relayer_last_successful_poll_timestamp_seconds{source_chain,dest_chain}` — dùng cho alert
    "không thấy poll thành công trong N phút".
  * `relayer_consecutive_errors{source_chain,dest_chain}` — chuỗi lỗi liên tiếp hiện tại.
  * `relayer_gas_price_wei{chain_id}` — giá gas vừa dùng gần nhất.
  * `relayer_wallet_balance_wei{chain_id}` — số dư native của chính relayer, refresh định kỳ
    (mặc định 30s, chỉnh bằng `-balance-check-interval-s`) — **đây là chỉ số quan trọng nhất để
    alert hết gas**.
* **`GET /health`** — JSON, trả HTTP 503 + `status:"degraded"` nếu bất kỳ cặp chain nào có
  `consecutive_errors >= 5`; kèm địa chỉ relayer, thời gian uptime, danh sách chain đã cấu hình,
  chi tiết từng cặp đang theo dõi, và số dư ví theo từng chain.

### 4.3. Backoff (`metrics.go::backoffDuration`)
`WatchChainPair` giờ nhân đôi thời gian chờ sau mỗi lỗi liên tiếp (`base * 2^(n-1)`), trần ở
`MaxPollBackoff` (mặc định 30s, chỉnh bằng `-max-poll-backoff-s`). Một chain node chết không còn bị
long-poll dồn dập vô ích; log vẫn ghi `Warn` mỗi lần lỗi (không câm lặng).

### 4.4. Test đua 2-instance (`daemon_test.go`)
Xem mục 3.1 — `TestRelayerDaemon_TwoConcurrentInstances_NoDoubleProcessing`.

---

## 5. Runbook: chạy nhiều Relayer song song trên cùng 1 máy

Dùng `deploy/ansible_private_chains/run_relayer_tmux.sh <action> [instance_name]`.
`instance_name` mặc định là `default` — hành vi y hệt trước khi có bản vá này (session tmux tên
`relayer`, dùng `gateway_register.json`, log `relayer.log`).

```bash
# Instance mặc định (như cũ, không đổi)
./run_relayer_tmux.sh start
./run_relayer_tmux.sh status
./run_relayer_tmux.sh stop

# Thêm 1 instance thứ 2 -- BẮT BUỘC có file cấu hình riêng với relayer_key KHÁC
cp gateway_register.json gateway_register.relayer2.json
# ... sửa relayer_key trong gateway_register.relayer2.json thành 1 key khác, có số dư riêng ...
RELAYER_METRICS_ADDR=:9091 ./run_relayer_tmux.sh start relayer2
./run_relayer_tmux.sh status relayer2
./run_relayer_tmux.sh stop relayer2   # chỉ dừng relayer2, KHÔNG đụng instance default
```

Các điểm quan trọng script đã tự kiểm tra:
* **Guard trùng key**: nếu `gateway_register.<tên>.json` có `relayer_key` trùng với instance mặc
  định, script từ chối khởi động và giải thích rõ lý do (đua nonce giữa 2 tiến trình độc lập).
* **`stop_relayer()` chỉ nhắm đúng 1 instance**: trước bản vá, `pkill -9 -f cross_chain_relayer`
  giết TẤT CẢ relayer đang chạy trên máy bất kể instance nào được yêu cầu dừng — nay đã scope theo
  đúng đường dẫn `--config` của từng instance.
* **Cổng metrics phải khác nhau**: instance `default` mặc định `:9090`; mọi instance khác **mặc
  định TẮT hẳn metrics** trừ khi set `RELAYER_METRICS_ADDR` tường minh — tránh đụng cổng âm thầm.
* Biến môi trường `RELAYER_EXTRA_ARGS` cho phép truyền thêm flag bất kỳ (vd
  `RELAYER_EXTRA_ARGS="-gas-price-bump-percent 110 -max-gas-price-gwei 50"`).

---

## 6. Giới hạn còn tồn đọng (đã biết, chưa vá — quyết định có chủ đích)

* **State chỉ ở RAM**: `processedMessages`, `attestedCommits`, `watchedPairs`, `nonces` mất khi
  restart. Không phải lỗi an toàn — write-once guard on-chain (`AttestedCommits`, `MessageStatus`)
  và nonce tự dò lại qua `GetPendingTransactionCount` khi cache trống đã đủ để không double-spend
  hay kẹt vĩnh viễn — nhưng KHÔNG có tính năng "xem lại lịch sử đã relay" qua các lần restart. Nếu
  cần, giải pháp tương lai là ghi 1 log file append-only hoặc SQLite nhẹ, không cần thay đổi tầng
  an toàn.
* **O(N²) watcher trong `main.go`**: mọi cặp `(src, dst)` trong danh sách chain cấu hình đều được
  spawn 1 goroutine + vòng lặp poll RPC riêng. Với vài chục chain đây sẽ là vài trăm/nghìn vòng lặp
  RPC. Chưa vá vì chưa có dữ liệu thực tế nào cho thấy đây là vấn đề ở quy mô hiện tại (khớp với
  quyết định "không đoán mò giới hạn khi chưa có số liệu" đã áp dụng cho các cấu hình tương tự
  trong dự án) — nếu triển khai với số lượng chain lớn, nên đo trước khi tối ưu, và cân nhắc thêm 1
  flag `-pairs=src1:dst1,src2:dst2,...` để chỉ định tường minh thay vì tích Descartes toàn bộ.

---

## 7. Tóm tắt đánh giá kiến trúc cho production

Trước 2026-09-05: kiến trúc **đúng và an toàn ở tầng giao thức** (permissionless, idempotent, có
động lực kinh tế cho nhiều relayer cạnh tranh) nhưng **chưa sẵn sàng vận hành production** — thiếu
fee estimation động (rủi ro giao dịch kẹt/mất phí thật), thiếu hoàn toàn khả năng quan sát
(metrics/health), và tooling triển khai chỉ hỗ trợ đúng 1 instance kèm 1 lỗi ẩn (`pkill` phạm vi
rộng) sẽ phá vỡ chính khả năng chạy nhiều relayer nếu ai đó thử làm thủ công.

Sau bản vá 2026-09-05: cả 5 khoảng trống đã xác định đều đã được xử lý (2 vá thật ở logic gửi giao
dịch/backoff, 1 tính năng quan sát mới hoàn toàn, 1 công cụ triển khai đa-instance mới hoàn toàn có
kèm test tự động cho đúng tính chất an toàn mà thiết kế giao thức đã hứa). Còn 2 giới hạn có chủ đích
để lại (mục 6), không chặn triển khai production ở quy mô hiện tại của dự án.
