# Thiết kế đề xuất: Chặn backlog ở lớp nhận giao dịch (Tx Ingress Backpressure) — 2026-10-15

**Phạm vi tài liệu:** Chỉ phân tích mã nguồn hiện tại và đề xuất thiết kế theo yêu cầu của Việc 2 trong `note/plan_backlog_gc_followup_20261015.md:53-55` `[ĐỌC CODE]`. Không sửa mã nguồn node trong phạm vi việc này `[ĐỌC CODE]`.

---

## 1. Bối cảnh vấn đề

- Ở phiên bản Go cũ (`f97a2fbf`, trước commit `ece1e0b4` và `1cea5f0e`), khi tải gửi vượt tốc độ xử lý, lớp mạng/mempool làm rơi hoặc từ chối gói giao dịch khiến client bị chặn hoặc đứt kết nối sớm (~0,86 triệu tx/lượt ở BFT sustained 60s), làm backlog thực tế nhỏ nhưng gây mất giao dịch và khuyết nonce (nonce gap) `[TRÍCH: note/node_ab_old_vs_new_report_20261014.md; note/tps_ingress_rootcause_report_20261013.md]`.
- Sau chuỗi bản vá `ece1e0b4` (backpressure không mất tx) và `1cea5f0e` (xả hết hàng đợi `requestChan` khi reader kết thúc vì bất kỳ lỗi nào), toàn bộ giao dịch client gửi qua TCP đều được bảo toàn tới validator (`sent == received == committed`, 0 mất tx trên 33/33 lượt kiểm tra) `[TRÍCH: note/tps_ingress_rootcause_report_20261013.md]`.
- Tuy nhiên, khi client bơm liên tục khối lượng lớn (1–4 triệu tx ở chế độ sustained), validator nhận vào nhanh hơn tốc độ tạo block và commit: tại mức 4 triệu tx, thời gian bơm chỉ mất ~27–55 giây nhưng pha xả backlog kéo dài ~300–350 giây, đẩy heap Go sống từ ~0,99 GB (ở 1 triệu tx) lên ~2,04–2,34 GB (ở 4 triệu tx), kéo `runtime.gcDrain` từ ~11% lên ~29–30% CPU và làm TPS đầu-cuối giảm từ ~13,1k tx/s (0,5 triệu tx) xuống ~10,0k tx/s (4 triệu tx) `[TRÍCH: note/tps_by_volume_report_20261014.md:1; note/node_profile_backlog_raft_report_20261014.md:1]`.

---

## 2. Phân tích hành vi hiện tại trong mã nguồn (`[ĐỌC CODE]`)

### 2.1. (a) Giới hạn mềm theo kích thước mempool (`MaxMempoolSize`) và Admission Gate

Hiện tại hệ thống đã có **hai lớp chặn mềm** ở phía Go:

1. **Lớp 1 — `TxAdmissionGate` tại `SocketServer.HandleConnection` (`execution/pkg/network/admission.go:8-36`, `execution/pkg/network/server.go:238-250`):**
   - Trong `execution/cmd/simple_chain/processor/constants.go:82-89`, các ngưỡng được đặt cố định:
     - `MaxMempoolSize = 200000` (`constants.go:85`) `[ĐỌC CODE]`.
     - `HighWatermark = 150000` (`constants.go:88`) `[ĐỌC CODE]`.
     - `LowWatermark = 100000` (`constants.go:89`) `[ĐỌC CODE]`.
   - Goroutine `startSystemLoadMonitor` (`execution/cmd/simple_chain/processor/processors.go:26-98`) đăng ký hàm `waitNotOverloaded` vào `p_network.SetTxAdmissionGate(waitNotOverloaded)` (`processors.go:29`) và kiểm tra định kỳ mỗi `200ms` (`processors.go:39`) theo công thức:
     $$\text{currentLoad} = \text{poolSize} + \text{resultChanSize} + \text{virtualChainSize}$$
     trong đó `poolSize = transactionProcessor.GetPoolSize()` (`transactionPool.CountTransactions()`), `resultChanSize = len(resultChan)`, `virtualChainSize = virtualChain.GetTotalTransactions()` (`processors.go:64-67`) `[ĐỌC CODE]`.
   - Khi `currentLoad > HighWatermark` (`150.000`), `overloadGate.setOverloaded(true)` đóng cổng; mọi goroutine `HandleConnection` đang xử lý lệnh `SendTransaction` / `SendTransactions` / `SendRawTransaction` / `SendRawTransactions` (`server.go:18-26, 238-243`) sẽ block tại `gate(s.ctx)` cho tới khi `currentLoad < LowWatermark` (`100.000`) thì `overloadGate.setOverloaded(false)` mới `close(ch)` để đánh thức đồng thời các kết nối đang chờ (`overload_gate.go:29-56`) `[ĐỌC CODE]`.

2. **Lớp 2 — `waitForPoolRoom` tại `TransactionProcessor` (`execution/cmd/simple_chain/processor/transaction_processor.go:392-395, 485-511, 682-685`):**
   - Trước khi đưa lô giao dịch đã xác thực chữ ký vào `TransactionPool`, `processTransactionsBatch` và `SendRawTransactions` khóa `tp.admitMu.Lock()` rồi gọi `tp.waitForPoolRoom(len(validTxs))` (`transaction_processor.go:393-394, 683-684`) `[ĐỌC CODE]`.
   - `waitForPoolRoom(n)` (`transaction_processor.go:485-511`) đặt ngưỡng `highWater = MaxMempoolSize - 25000` (`175.000` tx tại `transaction_processor.go:494`) và ngủ lặp `5ms` (`transaction_processor.go:509`) nếu `tp.transactionPool.CountTransactions() + n >= highWater` `[ĐỌC CODE]`.
   - Khi đã có chỗ, `AddAdmittedTransactionsToPool` (`tx_validator_pool_core.go:639-641`) gọi `addTransactionsToPool(..., enforceCap = false)` để **không** gọi `EvictLowestGasPrice(MaxMempoolSize)` (`tx_validator_pool_core.go:643-689`), nhờ đó không bao giờ đuổi (evict) giao dịch đã nhận `[ĐỌC CODE]`.

### 2.2. Vì sao hai lớp chặn hiện tại vẫn để lọt backlog 4 triệu tx vào node? (`[ĐỌC CODE]`)

Đọc đường đi của giao dịch sau khi vào `TransactionPool` tại `execution/cmd/simple_chain/processor/tx_batch_forwarder_core.go:55-297` cho thấy nguyên nhân gốc rễ:

- Goroutine `TxBatchForwarder.StartForwardingLoop` (`tx_batch_forwarder_core.go:55`) quét `TransactionPool` mỗi `1–10ms` (`tx_batch_forwarder_core.go:57, 114-122`), rút tối đa `maxPoolDrainPerTick = targetBlockSize * 5 = 40.000` tx (`tx_batch_forwarder_core.go:66, 87, 157`), lấy tối đa `targetBlockSize = 8.000` tx hợp lệ mỗi nhịp (`tx_batch_forwarder_core.go:180-185`) và đẩy ngay sang Rust qua FFI `executor.SubmitTransactionBatch` (hoặc `raftfeed.Submit` ở chế độ Raft) theo từng batch `2.000` tx (`tx_batch_forwarder_core.go:20-28, 60, 230`) `[ĐỌC CODE]`.
- Ngay khi `ProcessTransactionsInPoolSub` rút giao dịch ra khỏi `TransactionPool` để đẩy sang Rust FFI, **`transactionPool.CountTransactions()` giảm xuống ngay lập tức**, trong khi các giao dịch đó **chưa được đồng thuận hay thực thi xong** mà chuyển sang nằm chờ ở:
  1. Hàng đợi đề xuất block / DAG phía Rust (`SubmitTransactionBatch`) hoặc hàng đợi Raft (`raftfeed`) `[ĐỌC CODE]`.
  2. Các cấu trúc dữ liệu giữ trạng thái chờ trên heap Go: `bc.txsCache` (`blockchain.go:232-246`), `ReceiptTracker.txHashConnectionMap` (`receipt_tracker.go:29`), và `shardedSignatureCache` (`validation.go:28-149`) `[ĐỌC CODE]`.
- Do cả `startSystemLoadMonitor` (`processors.go:64`: chỉ đo `poolSize + resultChanSize + virtualChainSize`) lẫn `waitForPoolRoom` (`transaction_processor.go:501`: chỉ đo `transactionPool.CountTransactions()`) **không tính số giao dịch đã đẩy sang Rust nhưng chưa được commit (`inFlightDownstream = TxsForwarded - TxsCommitted`)**, nên miễn là `TxBatchForwarder` rút kịp `TransactionPool` sang Rust thì `poolSize` luôn dưới `100.000` `[ĐỌC CODE]`. Hệ quả là `overloadGate` và `waitForPoolRoom` gần như không kích hoạt khi Rust nhận nhanh hơn tốc độ thực thi block, để mặc tổng backlog toàn node tích tụ lên tới 1–4 triệu tx `[ĐỌC CODE]`.
- Trong khi đó, bộ đếm nguyên tử `GlobalPipelineStats` (`execution/cmd/simple_chain/processor/pipeline_stats.go:16`, được cập nhật tại `tx_batch_forwarder_core.go:279` qua `IncrTxsForwarded`) đã theo dõi sẵn số giao dịch đẩy sang Rust, nhưng chưa được nối vào công thức `currentLoad` của `startSystemLoadMonitor` hay `waitForPoolRoom` `[ĐỌC CODE]`.

---

## 2.3. (b) Cơ chế phản hồi ngược (TCP Flow Control hiện có)

Chuỗi phản hồi ngược từ validator về client qua TCP hiện hoạt động theo 4 bước nối tiếp (`execution/pkg/network/connection.go:150-358`, `execution/pkg/network/server.go:167-250`) `[ĐỌC CODE]`:

```
[Client TCP write()] 
   ──► [Kernel TCP Recv-Q (validator)]
   ──► [Connection.readLoop (connection.go:165)]
   ──► [Connection.requestChan (buffer = InboundRequestChanSize = 256, app.go:219)]
   ──► [SocketServer.HandleConnection (server.go:167)]
         ├─► 1) gate(s.ctx) (admission.go:28 — chặn khi overloadGate đóng)
         └─► 2) s.requestChan <- request (server.go:247 — buffer = ConnRequestChanSize = 10000)
               ──► [Worker pool (server.go:96) ──► TransactionProcessor.admitMu + waitForPoolRoom]
```

- Khi `gate(s.ctx)` chặn (hoặc khi các worker của `SocketServer` đều đang chờ tại `admitMu` / `waitForPoolRoom` làm đầy `s.requestChan`), vòng lặp `for req := range conn.RequestChan()` trong `SocketServer.HandleConnection` (`server.go:167`) dừng rút phần tử khỏi `conn.requestChan` của kết nối đó `[ĐỌC CODE]`.
- Sau khi `conn.requestChan` (dung lượng `256` message đối với server nhận tx từ client, cấu hình tại `execution/cmd/simple_chain/app.go:219` và `execution/pkg/network/connection.go:88-90`) đầy, `Connection.readLoop` chặn tại phép gửi kênh `c.requestChan <- req` (`connection.go:349`) và ngừng gọi `c.realConn.Read( lengthBuf )` (`connection.go:180`) `[ĐỌC CODE]`.
- Khi `readLoop` ngừng đọc khỏi socket, bộ đệm nhận TCP của nhân Linux (`Recv-Q`) đầy dần rồi quảng bá `TCP ZeroWindow` về phía client; lúc này lệnh `conn.Write()` ở phía client tự động block ở tầng nhân OS (hoặc trả về `EAGAIN`/timeout tùy cách client đặt `SetWriteDeadline`) mà **validator không làm rơi bất kỳ byte nào** `[ĐỌC CODE]`.
- **Điểm cần lưu ý về `IdleReadTimeout` (`execution/pkg/network/connection.go:18-19, 178`):** `readLoop` chỉ đặt `SetReadDeadline(time.Now().Add(IdleReadTimeout))` (`IdleReadTimeout = 5 * time.Minute`) **trước mỗi lần gọi `Read`** (`connection.go:178`) `[ĐỌC CODE]`. Khi `readLoop` đang chặn ở `c.requestChan <- req` (`connection.go:349`), nó không ở trong hàm `Read`, nên không bị nổ timeout đọc trong lúc chờ `requestChan`; tuy nhiên ngay khi `c.requestChan` vơi đi 1 phần tử và `readLoop` quay lại đầu vòng lặp để gọi `Read`, nếu phía client đã ngừng gửi dữ liệu quá 5 phút thì lần `Read` tiếp theo mới hết hạn, và bản vá `1cea5f0e` (`server.go:173-180`) đã đảm bảo mọi phần tử còn nằm trong `c.requestChan` đều được xả hết trước khi đóng kết nối `[ĐỌC CODE]`.

---

## 3. So sánh các phương án thiết kế chặn backlog

Dưới đây là bảng đối chiếu 3 phương án thiết kế (không triển khai mã trong bước này) `[ĐỌC CODE]`:

| Tiêu chí | Phương án A: Mở rộng `currentLoad` của Admission Gate bao gồm cả `inFlightDownstream` (Khuyến nghị) | Phương án B: Chặn cứng tại `TxBatchForwarder` theo độ sâu hàng đợi Rust/FFI | Phương án C: Trả mã lỗi `ServerBusy` / từ chối nhanh ở lớp ứng dụng khi đầy |
| :--- | :--- | :--- | :--- |
| **Cơ chế kỹ thuật** | Tính `inFlight = poolSize + (txsForwarded - txsCommitted)` trong `startSystemLoadMonitor` (`processors.go:64`) và/hoặc `waitForPoolRoom` (`transaction_processor.go:501`); đóng `overloadGate` khi tổng số tx chưa commit toàn node vượt `HighWatermark` (ví dụ `200.000–300.000` tx) và mở lại ở `LowWatermark` (`100.000–150.000` tx) `[ĐỌC CODE]`. | Giữ nguyên ngưỡng ở `TransactionPool`, nhưng giới hạn số tx đang chờ trong Rust (`txsForwarded - txsCommitted <= K`) tại `TxBatchForwarder.StartForwardingLoop` (`tx_batch_forwarder_core.go:98`) để `TransactionPool` tự đầy lên tới `175.000` và kích hoạt `waitForPoolRoom` + `overloadGate` gián tiếp `[ĐỌC CODE]`. | Khi `currentLoad > HighWatermark`, `HandleConnection` không block mà gửi trả gói `ServerBusy` (`server.go:268`) hoặc đóng/bỏ qua batch giao dịch mới gửi tới `[ĐỌC CODE]`. |
| **Bộ nhớ Heap Go giữ cho tx chờ** | **Thấp nhất:** giao dịch dư thừa nằm dưới dạng byte thô trong buffer TCP của client / kernel OS thay vì được giải mã thành `Transaction`, `txsCache`, `txHashConnectionMap` trên heap Go `[TÍNH]`. | **Trung bình:** vẫn phải nhận và giải mã `175.000` tx vào `TransactionPool` + `txsCache` + `txHashConnectionMap` trước khi `waitForPoolRoom` chặn `[ĐỌC CODE]`. | Thấp về bộ nhớ nhưng **vi phạm yêu cầu không làm mất giao dịch** của client fire-and-forget `[ĐỌC CODE]`. |
| **Ảnh hưởng tới client fire-and-forget** | **Không mất tx, không tạo khoảng trống nonce:** TCP flow control làm chậm tốc độ `Write()` của client khớp với tốc độ commit của chuỗi `[ĐỌC CODE]`. | **Không mất tx, không tạo khoảng trống nonce:** tương tự Phương án A nhưng phản ứng trễ hơn sau khi mempool Go đã chứa đầy `175.000` tx `[ĐỌC CODE]`. | **Gây hỏng chuỗi nonce (Nonce Gap):** client fire-and-forget đã tăng bộ đếm nonce cục bộ cho các batch tiếp theo; bỏ 1 batch ở giữa khiến toàn bộ các tx có nonce cao hơn của cùng sender bị kẹt ở trạng thái `future nonce` trong `TransactionPool` `[ĐỌC CODE]`. |
| **Tuân thủ Zero-Fork (`AGENTS.md` Phần 2.5)** | **An toàn với Zero-Fork:** chỉ điều tiết tốc độ đọc socket TCP ở rìa ngoài (ingress) của từng validator trước khi tx vào mempool; không can thiệp vào thứ tự tx trong block đã đề xuất, không đụng tới logic đồng thuận DAG/CommitVoteMonitor hay thực thi EVM/MVM `[ĐỌC CODE]`. | **An toàn với Zero-Fork:** chỉ điều tiết tốc độ rút từ `TransactionPool` của validator cục bộ sang Rust để tạo block mới; việc thực thi các CommittedSubDag từ đồng thuận vẫn chạy trên kênh độc lập `[ĐỌC CODE]`. | Không gây fork state root giữa các validator, nhưng gây dừng tạo block có ích do toàn bộ mempool ngập giao dịch khuyết nonce `[ĐỌC CODE]`. |
| **Tính tiến triển (Liveness / No Deadlock)** | **Đảm bảo tiến triển nếu tách bạch kênh đọc:** `gate(s.ctx)` chỉ chặn lệnh `isTxSubmissionCommand(cmd)` (`server.go:238`); các lệnh đọc trạng thái/nonce (`GetAccountState`, `InitConnection`), kênh P2P đồng thuận Rust, và luồng thực thi block (`ProcessCommittedBlock`) hoàn toàn không đi qua `gate`, nên số `txsCommitted` luôn tăng và kéo `inFlight` xuống dưới `LowWatermark` để mở lại cổng `[ĐỌC CODE]`. | **Đảm bảo tiến triển**, nhưng giữ ~175k tx trong `TransactionPool` lâu hơn làm tăng chi phí quét `TransactionsWithAggSign` mỗi nhịp `[ĐỌC CODE]`. | **Mất tính tiến triển ứng dụng:** các ví bị rơi 1 batch sẽ không thể thực thi bất kỳ giao dịch nào tiếp theo cho tới khi gửi bù đúng nonce bị mất `[ĐỌC CODE]`. |

---

## 4. Phân tích chuyên sâu mục (c): Client Fire-and-Forget, Zero-Fork và Tính Tiến Triển

### 4.1. Ảnh hưởng tới client fire-and-forget và rủi ro khoảng trống nonce (Nonce Gap)
- Các công cụ tải cao và ví gửi lô qua giao thức TCP thô (`SendTransactions` / `SendRawTransactions` tại `execution/pkg/network/server.go:18-26`) hoạt động theo mô hình **fire-and-forget trên luồng gửi**: client ký sẵn một chuỗi nonce liên tiếp $n, n+1, n+2, \dots$ và đẩy liên tục các batch vào socket TCP mà không chờ biên nhận của batch trước rồi mới ký batch sau `[ĐỌC CODE]`.
- Nếu validator **bỏ (drop) hoặc từ chối (reject)** một batch chứa nonce $n$ khi hàng đợi đầy:
  1. Các batch tiếp theo trên cùng kết nối (chứa nonce $n+1, n+2, \dots$) vẫn tiếp tục đi vào `TransactionPool` khi hàng đợi vơi bớt `[ĐỌC CODE]`.
  2. Tại `TransactionPool`, hàm lọc theo thứ tự nonce (`ProcessTransactionsInPoolSub` tại `tx_validator_pool_core.go`) thấy tài khoản đang ở state nonce $n$ nhưng chỉ có các giao dịch từ $n+1$ trở lên, nên xếp toàn bộ chúng vào nhóm **future nonces** và trả ngược lại vào `TransactionPool` ở mọi chu kỳ `[ĐỌC CODE]`.
  3. Hệ quả là tài khoản đó bị kẹt vĩnh viễn cho tới khi có giao dịch mang nonce $n$, đồng thời làm `TransactionPool` tích tụ các giao dịch không thể đưa vào block `[ĐỌC CODE]`.
- Vì vậy, nguyên tắc thiết kế bắt buộc là **tuyệt đối không bỏ giao dịch ở lớp nhận (`server.go:234-250`)**: chỉ được dùng cơ chế **tạm dừng đọc trên kết nối gửi tx (`gate(s.ctx)`)** để nhân TCP tự giữ dữ liệu theo đúng thứ tự byte trên đường ống dẫn `[ĐỌC CODE]`.

### 4.2. Ảnh hưởng tới bất biến Zero-Fork (`AGENTS.md` Phần 2.5)
- Trong kiến trúc Metanode, bất biến Zero-Fork đòi hỏi mọi validator chỉ thực thi `CommittedSubDag` sau khi đã được đồng thuận (hoặc đạt `PeerAttestResult::Ok` với $2f+1$ phiếu bầu digest), và thực thi tất cả giao dịch trong `CommittedSubDag` theo thứ tự tất định giống hệt nhau giữa mọi node `[ĐỌC CODE]`.
- Cơ chế chặn backlog ở lớp nhận tx (`TxAdmissionGate` tại `execution/pkg/network/server.go:238-243`) nằm **hoàn toàn ở phía trước `TransactionPool` cục bộ** của từng validator (trước khi giao dịch được đóng gói vào batch để đưa sang Rust DAG hoặc Raft log) `[ĐỌC CODE]`.
- Một khi giao dịch đã được một validator đóng gói vào block đề xuất trong DAG/Raft, việc đồng bộ và thực thi block đó giữa các validator đi qua mạng P2P của tầng đồng thuận (Rust P2P / Raft transport) và callback FFI thực thi block, **hoàn toàn không đi qua `SocketServer` TCP của client** (`server.go:238`) `[ĐỌC CODE]`.
- Do đó, việc validator A mở/đóng `overloadGate` sớm hay muộn hơn validator B vài mili-giây chỉ ảnh hưởng tới tốc độ validator A nhận thêm giao dịch *mới* từ client vào mempool cục bộ, **không ảnh hưởng tới tập giao dịch hay thứ tự giao dịch trong bất kỳ block đã cam kết nào**, và không sử dụng bất kỳ cơ chế timeout/sleep nào trong đường đồng thuận/dispatch commit `[ĐỌC CODE]`.

### 4.3. Tính tiến triển (Liveness / Không Deadlock) và các điều kiện biên cần bảo vệ
Để việc đóng `overloadGate` theo `inFlightDownstream` không bao giờ gây deadlock (treo vĩnh viễn không mở lại được cổng), thiết kế phải thỏa mãn 3 bất biến sau `[ĐỌC CODE]`:

1. **Đường xả (commit/drain path) hoàn toàn độc lập với đường nhận (ingress path):**
   - `gate(s.ctx)` trong `server.go:238-243` chỉ áp dụng cho `isTxSubmissionCommand(cmd)` (`SendTransaction`, `SendTransactions`, `SendRawTransaction`, `SendRawTransactions` tại `server.go:18-26`) `[ĐỌC CODE]`.
   - Luồng nhận block đã đồng thuận từ Rust về Go (`ProcessCommittedBlock`), luồng ghi NOMT/DB, và các lệnh đọc RPC/TCP (`InitConnection`, `GetAccountState`, `GetNonce`) không bị chặn bởi `gate`, nên hệ thống luôn tiếp tục thực thi block để giảm số tx đang bay (`inFlight`) về dưới `LowWatermark` `[ĐỌC CODE]`.
2. **Cách tính `inFlight` không được bị lệch vĩnh viễn (Counter Drift) khi có giao dịch bị loại ở bước tạo block:**
   - Nếu tính `inFlight = TxsForwarded - TxsCommitted` đơn thuần bằng hiệu hai bộ đếm tích lũy, cần xét trường hợp một giao dịch đã được `TxBatchForwarder` cộng vào `TxsForwarded` (`tx_batch_forwarder_core.go:279`) nhưng sau đó bị loại hoặc trùng lặp mà không tăng `TxsCommitted` tương ứng `[ĐỌC CODE]`.
   - Nếu bộ đếm bị lệch dương ($\text{drift} > \text{LowWatermark}$), `inFlight` sẽ không bao giờ giảm xuống dưới `LowWatermark` ngay cả khi toàn bộ hàng đợi thực tế đã rỗng, gây **deadlock cổng nhận (admission gate stuck closed)** `[TÍNH]`.
   - **Giải pháp chống deadlock:**
     - Thay vì chỉ dựa vào hiệu hai bộ đếm đơn điệu có thể trôi (drift), cần lấy **số lượng giao dịch đang chờ thực tế (gauge)** từ các cấu trúc đang giữ chúng (ví dụ: `pool.CountTransactions() + len(resultChan) + virtualChain.GetTotalTransactions() + rustPendingTxCount`), **hoặc** bổ sung điều kiện tự hồi phục (self-healing reset): khi `poolSize == 0` và không có block mới chứa tx trong $K$ chu kỳ liên tiếp (hoặc hàng đợi Rust báo rỗng), đồng bộ lại `inFlightDownstream = 0` `[ĐỌC CODE]`.
3. **Không để client bị ngắt kết nối oan khi chờ backpressure lâu:**
   - Ở phía client (như `secp_tps_blast` hoặc SDK), khi validator áp dụng TCP backpressure, `conn.Write()` trên TCP có thể chờ lâu hơn nếu đặt `WriteDeadline` quá ngắn (ví dụ vài giây) `[ĐỌC CODE]`. Client cần giữ kết nối hoặc thử gửi lại cùng một batch (giữ nguyên các nonce trong batch đó, như bản sửa trong commit `ece1e0b4` tại `execution/cmd/tool/secp_tps_blast/main.go`) thay vì bỏ batch và tăng nonce tiếp `[TRÍCH: commit ece1e0b4]`.

---

## 5. Phương án khuyến nghị và câu hỏi cần chủ dự án quyết định

### 5.1. Phương án khuyến nghị (KISS / YAGNI)
Khuyến nghị **Phương án A (kết hợp bảo vệ chống lệch bộ đếm)**:
1. Giữ nguyên kiến trúc `TxAdmissionGate` (`execution/pkg/network/admission.go`) và `overloadGate` (`execution/cmd/simple_chain/processor/overload_gate.go`) sẵn có — không thêm kênh, worker hay giao thức mới `[ĐỌC CODE]`.
2. Bổ sung vào `currentLoad` trong `startSystemLoadMonitor` (`execution/cmd/simple_chain/processor/processors.go:64`) thành phần đo số giao dịch đang chờ phía sau `TransactionPool` (số giao dịch đã forward sang Rust/Raft nhưng chưa được thực thi xong tại `BlockProcessor`), kèm cơ chế neo về `0` khi hàng đợi thực tế đã cạn để không bao giờ kẹt cổng `[ĐỌC CODE]`.
3. Cho phép cấu hình `HighWatermark` và `LowWatermark` qua biến môi trường (ví dụ `META_INGRESS_HIGH_WATERMARK`, `META_INGRESS_LOW_WATERMARK`, mặc định giữ nguyên hoặc đặt `300.000` / `150.000` toàn node) để có thể bật/tắt trong thí nghiệm A/B mà không cần biên dịch lại binary `[ĐỌC CODE]`.

### 5.2. Câu hỏi cần chủ dự án quyết định trước khi triển khai
1. **Ngưỡng tổng backlog toàn validator (`HighWatermark` / `LowWatermark`):** Chủ dự án muốn giới hạn tổng số giao dịch đang chờ trên mỗi validator (tính cả phần đã đẩy sang Rust/Raft nhưng chưa commit) ở mức bao nhiêu (ví dụ: `HighWatermark = 250.000`, `LowWatermark = 150.000` tương đương ~1,0 triệu tx trên toàn cụm 4 validator, hay một ngưỡng khác) `[CHƯA ĐO]`?
2. **Áp dụng cho cả cổng JSON-RPC HTTP (`eth_sendRawTransaction`) hay chỉ cổng TCP thô?** Hiện tại `TxAdmissionGate` mới chỉ gắn ở `SocketServer.HandleConnection` (`execution/pkg/network/server.go:238-243`), trong khi đường JSON-RPC HTTP (`rpc_transaction.go`) chưa gọi `loadTxAdmissionGate()`; chủ dự án có muốn áp dụng cùng cổng chặn mềm này cho HTTP RPC không `[ĐỌC CODE]`?
3. **Thứ tự ưu tiên với Việc 1 (thu gọn đối tượng heap Go cho mỗi tx chờ):** Chủ dự án muốn triển khai Việc 1 trước (thu gọn cấu trúc và giảm cấp phát trong `execute_sc_result.go:18`, `validation.go:46`, `blockchain.go:232` để node chịu được backlog hàng triệu tx nhẹ nhàng hơn), rồi mới quyết định có cần bật giới hạn tổng backlog ở Việc 2 hay không `[CHƯA ĐO]`?

---

## Giới hạn

- Tài liệu này chỉ phân tích trên mã nguồn tại commit `f85bf683f223b6dbcb60bf1b5ebd4a355a342da6` và các số liệu đã có trong kho bằng chứng (`note/tps_by_volume_report_20261014.md`, `note/node_profile_backlog_raft_report_20261014.md`); chưa thực hiện sửa mã hay chạy phép đo A/B cho Phương án A `[ĐỌC CODE]`.
- Hiệu quả thực tế của việc đặt `HighWatermark` toàn pipeline lên độ trễ P99 của phía client phụ thuộc vào hành vi timeout ghi (`WriteDeadline`) của từng loại client bên ngoài `secp_tps_blast` `[CHƯA ĐO]`.

## Chưa làm

- Chưa sửa mã `execution/cmd/simple_chain/processor/processors.go` hay `execution/pkg/network/admission.go` (đúng theo yêu cầu "chỉ tài liệu, không sửa mã" của Việc 2 trong `note/plan_backlog_gc_followup_20261015.md:53-55`) `[ĐỌC CODE]`.
- Chưa đo thực nghiệm ngưỡng `HighWatermark`/`LowWatermark` tối ưu trên cụm 4 validator BFT ở tải 4 triệu tx `[CHƯA ĐO]`.
