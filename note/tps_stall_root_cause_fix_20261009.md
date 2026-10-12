# Gốc rễ việc chain "dừng giữa chừng" dưới tải bão hòa và bản sửa — 2026-10-09

**Phạm vi:** đường nhận giao dịch qua TCP (`SendRawTransaction(s)`) của `simple_chain`, dùng chung cho chế độ Raft và BFT. Không đụng consensus, thực thi EVM, hay thứ tự/hash khối.
**Evidence:** `note/evidence/tps_root_cause_fix_20261009/` (JSON/log từng lượt đo và các script). Kết quả đo của các lượt mô tả bên dưới đều là số đo thật, kèm binary build từ cây mã tương ứng.

## 1. Triệu chứng

Khi bơm tải không giới hạn, chain ngừng tạo block giữa chừng và một phần giao dịch không bao giờ lên chain:
- Raft sustained: dừng ở giây ~15 (chế độ "~3,9k tx/s") hoặc ~40; 19–25% tx đã gửi không lên chain.
- BFT sustained: mất khoảng 1% rồi dừng vĩnh viễn (baseline: 869.000 gửi → 860.000 lên chain, dừng ở block 171).

## 2. Chuỗi bằng chứng

| Bước | Quan sát (đo thật) |
|---|---|
| Khoảng trống nonce ở ingress | Với một ví, nonce 1–214 có ADD, rồi **thiếu 215–306, 336–427, ...**; các khoảng có ADD (1–214, 307–335, 428–454, 595–623, ...) trùng chính xác với các cửa sổ mạch ngắt quá tải **tắt**, các khoảng thiếu trùng với cửa sổ **bật** (lượt trace S11, chế độ RPC). |
| Pool toàn tx "future" | Sau khoảng trống, log `ProcessPoolSub` cho `validTxs=0, futureTxs≈allTxs` (40.000–200.000) kéo dài đến hết; `FutureTxTimeout=5 phút` nên không tự hết. |
| Đối chứng: tắt mạch ngắt | Binary không có mạch ngắt (C): client bơm 3,5 triệu tx, **không còn bị cắt ở 250.000**, 0 lỗi `system overloaded`. Baseline A: 3/3 lượt có đúng **1** lần mạch ngắt bật + **1** lỗi `system overloaded` + client dừng ở 246.000–262.000 tx. |
| Raft không phải thủ phạm | Với binary có bộ đếm: Raft nhận 195 proposal (382.043 tx), áp dụng đủ và đồng nhất trên 3 node, hàng đợi propose luôn rỗng. Phần còn lại của tx đã gửi **chưa từng tới Raft**. |
| Mempool đầy → evict/reject | Sau khi sửa mạch ngắt, log hiện 318.622 tx bị từ chối `transaction pool is full` và hàng chục lần `Evicting 1000 lowest-fee transactions`; sửa phần này đưa tỷ lệ lên chain từ 27% lên 63% → 73% → 85% → 89%. |
| Panic làm sập node | `panic: send on closed channel` trong `Connection.SendMessage` (`connection.go:446`, từ `BroadCastReceipts`) làm n0 sập giữa lượt đo khi client ngắt kết nối. |
| Đuôi hàng đợi bị bỏ lúc EOF | `NET-STATS` cho thấy server chỉ nhận ~34 batch/s trong khi client báo đã gửi 4,4 triệu tx; phần dư nằm trong kênh yêu cầu của kết nối (500.000 batch) và bị bỏ khi `HandleConnection` thoát lúc client đóng kết nối (1,65 triệu lên chain / 4,4 triệu đã gửi). |

## 3. Gốc rễ

Đường TCP nhận giao dịch coi "quá tải" là lý do để **bỏ** giao dịch, trong khi client là *fire-and-forget* (`secp_tps_blast` đọc và bỏ mọi phản hồi, SDK cũng không nhận ack theo batch). Mỗi giao dịch bị bỏ để lại một khoảng trống nonce cho ví của nó; mọi giao dịch sau đó của ví nằm trong pool ở trạng thái "future" vĩnh viễn (5 phút), pool đầy tx không rút được, mạch ngắt kẹt bật, và block ngừng được tạo. Các điểm bỏ cụ thể:

1. **Mạch ngắt quá tải** (`pendingOverloaded`, pool > 150.000): `ProcessRawTransaction(s)FromClient` **ngắt kết nối và bỏ batch**, không trả lỗi có thể thử lại. Với client chỉ có 1 kết nối, client dừng hẳn.
2. **Giới hạn mempool 200.000**: `addTransaction(s)ToPoolInternal` loại tx phí thấp nhất hoặc từ chối `pool is full`. Số đếm pool dao động ~40.000 vì forwarder giữ tạm một batch đã rút rồi trả lại phần chưa gửi, nên kiểm tra chỗ trống có thể thấy "còn chỗ" rồi ngay sau đó thấy đầy.
3. **`HandleConnection`**: kênh trung tâm đầy → bỏ yêu cầu; và khi client đóng kết nối (EOF) thì thoát ngay, bỏ cả hàng đợi đã nhận.
4. **`readLoop`**: kênh yêu cầu của kết nối đầy quá 30 giây → bỏ yêu cầu. Kênh này lớn 500.000 batch nên TCP không bao giờ đóng cửa sổ; bộ nhớ phồng thay vì client bị chặn.
5. **Panic `send on closed channel`** (`Connection.SendMessage`) khi gửi biên nhận cho kết nối vừa đóng: làm sập tiến trình.

Lỗi phụ nhưng thật, cùng đợt:
- **Hồi quy `608b5195`** trong `tx_batch_forwarder_core.go`: sau khi đổi sang vòng lặp retry, `AdvanceNoncesCacheForForwarded`, trace và thống kê "đã chuyển tiếp" bị đẩy xuống sau `break`, nên chỉ chạy khi gửi **thất bại**. Hệ quả đo được: ~92% phân loại là "future" (7,1 triệu "future" so với 612 nghìn "valid" trong lượt trace); sau sửa, `validTxs=allTxs, futureTxs=0`. Ảnh hưởng mọi chế độ (cả BFT). Sửa này **chưa đo được lợi ích TPS riêng**.
- **Công cụ blast** tăng `acc.Nonce` trước khi gửi, nên một lần `SendBatch` lỗi tiêu mất nonce và tạo khoảng trống; đã sửa để gửi lại đúng chunk đó.

## 4. Thay đổi

| File | Thay đổi |
|---|---|
| `execution/pkg/network/admission.go` (mới) | `SetTxAdmissionGate` + `isTxSubmissionCommand` (chỉ `SendRawTransaction(s)`). |
| `execution/pkg/network/server.go` | `HandleConnection`: giao tx **không bỏ** (đợi cổng admission rồi gửi vào kênh trung tâm có chặn); tách `dispatch`; khi đối tác đóng kết nối (EOF) **giao nốt các yêu cầu đã nhận** trước khi dọn. |
| `execution/pkg/network/connection.go` | `readLoop` không bỏ lệnh tx (chặn đọc socket = backpressure) và đặt lại hạn đọc sau khi chờ; `SendMessage` `recover` panic gửi vào channel đã đóng → `ErrDisconnected`; kênh yêu cầu mỗi kết nối cấu hình được. |
| `execution/pkg/network/config.go` | `ConnRequestChanSize` (0 = như cũ). |
| `execution/cmd/simple_chain/app.go` | Server client-facing dùng `ConnRequestChanSize = 256`. |
| `execution/cmd/simple_chain/processor/processors.go` | `overloadGate` biến cờ quá tải thành cổng chờ (sự kiện), nối vào `network.SetTxAdmissionGate`. |
| `execution/cmd/simple_chain/processor/transaction_processor.go` | Bỏ nhánh "Disconnect + trả lỗi" khi quá tải ở hai handler TCP; `waitForPoolRoom` (chờ còn chỗ, chừa `poolAdmissionHeadroom = 40.000`) + `admitMu` làm cặp "chờ chỗ + thêm" nguyên tử; dùng biến thể thêm không evict. Đường RPC giữ nguyên (trả lỗi tường minh). |
| `execution/cmd/simple_chain/processor/tx_validator_pool_core.go` | Thêm `AddAdmittedTransaction(s)ToPool` (bỏ bước evict-or-reject vì đã kiểm soát ở admission). |
| `execution/cmd/simple_chain/processor/tx_batch_forwarder_core.go` | Đưa bước ghi nhận sau khi gửi thành công ra khỏi vòng retry (khôi phục ý định ban đầu). |
| `execution/cmd/tool/secp_tps_blast/main.go` | Gửi lại đúng chunk khi `SendBatch` lỗi, không tiêu nonce. |
| Test mới | `overload_gate_test.go`, `admission_test.go`, `connection_send_closed_test.go` (test này **fail** khi chưa sửa, pass sau khi sửa). |
| `PROJECT_STRUCTURE.md` | Cập nhật. |

`consensus/metanode/scripts/build_check.sh`: **ALL BUILDS PASSED (5/5)**. `go test -race` cho `pkg/network` và các test liên quan của `processor`: pass.

## 5. Kết quả kiểm chứng (đo thật; "rút cạn" = chờ chain xử lý hết backlog rồi so tx lên chain với tx client đã gửi)

| Kịch bản | Baseline (binary gốc) | Sau sửa (binary cuối) |
|---|---|---|
| Raft, bơm 60 s, TCP | gửi 242.000 → **240.000** lên chain, **dừng vĩnh viễn** (client bị cắt ở giây ~15) | gửi 1.315.000 → **1.315.000 (100%)** trong 103 s; gửi 1.441.000 → **1.441.000 (100%)** trong 117 s |
| Raft, bơm 20 s ×3 | dừng ở ~250.000 (ba lượt giống nhau) | 878.848 / 861.000 / 801.000 → **100% / 100% / 100%** trong 56–63 s |
| BFT, bơm 20 s | 869.000 → 860.000 (**98,96%**, dừng vĩnh viễn) | 2.588.000 → **2.588.000 (100%)** trong 192 s |

- Tất cả các lượt sau sửa: zero-fork PASS, root nhất quán, 3 node Raft cùng số entry đã apply.
- Thông lượng end-to-end khi rút cạn (từ lúc bắt đầu bơm đến khi tx cuối lên chain, đã đếm đủ): Raft ≈ **12,3–12,8k tx/s** (60 s) và ≈ 13,9k (20 s); BFT ≈ **13,5k tx/s**.
- Client giờ bị TCP làm chậm thật sự (Raft 20 s: 0,8–0,9 triệu tx thay vì 1,5 triệu), thay vì được nhận hết rồi bị mất.

## 6. Giới hạn và rủi ro còn lại

- **Chờ không có thời hạn** ở admission khi pool đầy: nếu pool toàn "future" (khoảng trống nonce do nguồn khác) thì ingest sẽ chờ tới khi `FutureTxTimeout` (5 phút) dọn futures. Đó là hành vi an toàn hơn mất giao dịch, nhưng worker sẽ bị giữ; chưa có kiểm thử cho trường hợp này.
- Hạn đọc 90 s của `readLoop` đã được đặt lại sau khi chờ, nhưng nhánh này **chưa được kiểm thử** (cần chờ >90 s liên tục).
- `ConnRequestChanSize = 256` ảnh hưởng mọi lệnh trên server client-facing; chưa kiểm thử với tải đọc/P2P hỗn hợp.
- Đường RPC (`eth_sendRawTransaction`) giữ cơ chế từ chối tường minh khi quá tải; client RPC phải tự gửi lại theo đúng thứ tự nonce.
- Các số TPS ở trên chạy trên một máy chung nhiều node, binary build lại từ cây mã; chưa chạy trên nhiều máy hay với SDK thật.
- Bản sửa forwarder được giữ vì khôi phục đúng ý định có ghi chú trong mã, nhưng **tác dụng riêng lên TPS chưa đo được**.
- Cần review kỹ thay đổi trước khi merge vào `main`: chúng nằm ở đường ingress dùng chung cho cả BFT lẫn Raft.

## 7. Đo lại toàn bộ ma trận bằng binary cuối (49 lượt + 4 lượt đối chiếu)

Binary Q = cây mã đã commit (`ece1e0b4`), blast = cây mã hiện tại. Giao thức rút cạn: bơm, rồi chờ chain xử lý hết và so tx trên chain với tx đã gửi. "e2e" = từ lúc gửi tx đầu đến khi tx cuối lên chain (nhịp poll 2 giây, nên **chỉ đáng tin ở lượt dài**). Mọi lượt: zero-fork PASS, root nhất quán.

| Cấu hình | Binary | Lượt | Hoàn tất (100% lên chain) | Thông lượng |
|---|---|---|---|---|
| Raft sustained 60 s | **Q (đã sửa)** | 5 | **5/5** | e2e trung vị **12.790** tx/s (12.053–13.831) |
| Raft sustained 60 s | A (gốc) | 3 | **0/3** (dừng ở 210.000–250.000 tx) | không đo được |
| BFT sustained 20 s | **Q** | 3 | **3/3** | e2e trung vị **13.490** tx/s (13.419–14.283) |
| BFT sustained 20 s | A | 3 | **0/3** (799.000–885.000 tx, dừng vĩnh viễn) | không đo được |
| Raft burst 20k / 25k / 30k | Q | 5 mỗi mức | 5/5 mỗi mức | gồm thời gian bơm, trung vị: **12.486 / 13.446 / 13.827** tx/s |
| Raft burst 25k | A | 3 | 3/3 | gồm thời gian bơm, trung vị 13.217 tx/s (burst nhỏ không chạm ngưỡng quá tải) |
| BFT burst 25k | Q | 5 | 5/5 | gồm thời gian bơm, trung vị **7.426** tx/s |

Độ trễ ở tốc độ bơm cố định (60 s, 3 lượt mỗi ô; mọi tx lên chain):

| Chế độ | Tốc độ bơm | Binary | P50 | P99 |
|---|---|---|---|---|
| BFT | 5.000 | Q | 1.856 ms | 2.028 ms |
| BFT | 8.000 | Q | 1.871 ms | 2.162 ms |
| Raft | 5.000 | Q | 413 / 408 / 413 ms | 524–536 ms |
| Raft | 8.000 | Q | 412 / 328 / 414 ms | 434–621 ms |
| Raft | 5.000 | A (gốc) | 326 / 328 / 417 ms | 519–529 ms |

Nhận xét:
- **Bản sửa biến 0/6 lượt hoàn tất thành 8/8** ở hai kịch bản bão hòa (Raft sustained 60 s, BFT sustained 20 s). Ở burst nhỏ (≤30k) cả hai binary đều trọn vẹn nên không có khác biệt.
- **BFT không có hồi quy**: độ trễ ở tốc độ cố định giữ ~1,86 s như lần đo trước.
- **Độ trễ Raft ở tốc độ cố định hôm nay là ~330–410 ms cho cả binary gốc A lẫn Q**, trong khi lần đo trước (cùng binary A, cùng công cụ blast bản cũ) cho ~103 ms. Mình đã kiểm tra lại bằng chính binary blast cũ nhưng vẫn ra 326 ms, và máy không có tải nền đáng kể. Khác biệt so với lần đo cũ **chưa được giải thích**, nhưng không do bản sửa.
- Số "end-to-end" ở burst ngắn (2–5 giây) nhiễu vì nhịp poll nên không dùng; dùng số gồm thời gian bơm.
