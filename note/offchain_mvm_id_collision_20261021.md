# Off-chain eth_call: MVM instance id could be shared by concurrent identical calls (2026-10-21)

Trạng thái: **đã sửa**. Phạm vi: chỉ đường off-chain (eth_call / estimateGas), không đụng thực thi on-chain, không đổi consensus.

## Phát hiện
- [ĐỌC CODE] `executeTransactionOffChainWithState` (đường `eth_call` với block lịch sử, `rpc_state.go:207`) tạo id instance MVM từ `sha256(txHash ‖ giây ‖ rand.Intn(1000))`. Hàm của đường "latest" (`ExecuteTransactionOffChain`) đã dùng cách mạnh hơn: `UnixNano` + `rand.Int63` + vòng lặp kiểm tra; bản lịch sử bị bỏ sót.
- [TÍNH] Xác suất ít nhất một cặp trùng id khi n lời gọi giống hệt nhau chạy trong cùng một giây (`1 − Π(1 − k/1000)`): n=10 → 0,044; n=20 → 0,174; n=40 → 0,546; n=60 → 0,836; n=100 → 0,994. Máy chủ có 200 worker đọc và tới 100 lượt thực thi off-chain song song.
- [ĐỌC CODE] Khi trùng id, `GetOrCreateMVMApi` trả lại cùng một `*MVMApi` và ghi đè `accountStateDb`/`smartContractDb` của lần chạy đang diễn ra; lần chạy nào xong trước sẽ `ClearMVMApi` instance của lần kia.
- [ĐỌC CODE] Bảy callback của phần mở rộng Xapian (`extension.go`: `getOrCreateSimpleDb`, `deleteDb`, `getSimpledb`, `setSimpledb`, `getNextKeys`, `getAllSimpledb`, `searchByValue`) gọi `GetMVMApi` rồi truy cập `mvmApi.accountStateDb` mà không kiểm tra `nil`; các callback chính trong `mvm_api.go` (6 chỗ) đều có kiểm tra. Callback chạy trong cgo nên nil-dereference ở đó làm chết tiến trình.
- Hệ quả (suy ra, chưa tái hiện end-to-end vì cần tranh chấp thời gian thật): `eth_call` lịch sử đồng thời giống hệt nhau có thể cho kết quả sai/lỗi ngẫu nhiên, và nếu thực thi gọi phần mở rộng Xapian thì có thể làm chết node.
- [ĐO] Test callback Xapian trên mã cũ FAIL (nil-dereference ở các callback); sau khi sửa PASS.
- [ĐO] **Không tái hiện được hậu quả quan sát được của việc trùng id.** `TestReadTxExec_ConcurrentIdenticalHistoricalCalls_AllSucceedIdentically` (100 goroutine × 30 lời gọi lịch sử giống hệt nhau) chạy trên mã cũ cho cùng một kết quả mọi lần: 3/3 lần với initcode chỉ ghi bộ nhớ và 5/5 lần với initcode gọi `BALANCE` 300 lần (mỗi lần là một callback Go). Có thể callback đọc state khi mất instance chỉ trả lỗi mà EVM bỏ qua, nên kết quả không đổi. Vì vậy phần "kết quả sai/chết node" chỉ là suy luận từ code; bản sửa vẫn đúng và rẻ, nhưng đừng xem nó là lỗi đã được tái hiện.

## Sửa
1. `newOffChainMVMID`: một hàm dùng chung cho cả hai đường (UnixNano + `rand.Int63` + kiểm tra `GetMVMApi == nil`, giữ tiền tố `0xFD`).
2. Bảy callback Xapian trả `errMVMApiNotFound` thay vì dereference `nil`.

## Giới hạn
- Hai test chặn hồi quy: `TestNewOffChainMVMID_UniqueForIdenticalConcurrentCalls` (tính duy nhất của id; không phải test "fail trước, pass sau" vì id cũ nằm inline) và `TestXapianExtensionCallbacks_MissingInstanceIsAnErrorNotAPanic` (fail trước, pass sau).
- Vòng kiểm tra "có instance trùng không" vẫn là kiểm-rồi-tạo (TOCTOU) nhưng id có 63 bit ngẫu nhiên cộng thời gian nano nên xác suất trùng là không đáng kể.
