# Runbook: nâng cấp đồng bộ các bản sửa Block-STM (ESTIMATE / write set)

Áp dụng cho PR #132 (mất cộng tiền người nhận + livelock ESTIMATE rò rỉ) và PR #133 (uỷ quyền EIP-7702
+ mọi đường tạm dừng giữ write set). Cả hai là **thay đổi consensus**: kết quả state của một số block
thay đổi so với code cũ. Bản cũ **không tất định** ở các tình huống này (kết quả phụ thuộc lịch chạy goroutine),
nên hai node chạy code khác nhau có thể ra hash khác nhau trên cùng một block.

> Nguyên tắc: **thà pending chứ không fork.** Mọi bước dưới đây ưu tiên dừng lại và kiểm tra hơn là chạy tiếp.

## 0. Điều đã được kiểm chứng và điều CHƯA
Đã đo (cluster local, máy 192.168.1.232, KHÔNG phải cluster thật):
- Stress test trong tiến trình: `TestTrueBlockSTM_HotRecipientNoLostUpdate_Stress`, `TestTrueBlockSTM_EIP7702_*` (0 lỗi sau sửa, hàng chục nghìn vòng, `GOMAXPROCS` 1..16).
- Săn lệch hash giữa 2 tiến trình (C0 spike): 2000 vòng (bản trước merge cuối) và 600 vòng (build từ `dev` `fe218fbc`), 0 lệch.
- `ci.sh run-now --reset`: TPS Blast PASS (~7695 tx/s); bài Chaos Rolling Restart 1 lần FAIL (halt mất payload, không fork) và 1 lần PASS.
- Hash/stateRoot/receiptsRoot/transactionsRoot của mọi block khớp trên 5 node ở cả hai lần.

CHƯA đo: cluster thật (231/230), dữ liệu lịch sử thật, chạy đa máy. Không coi các con số trên là bảo đảm cho môi trường thật.

## 1. Rủi ro riêng của thay đổi consensus này
1. **Nâng cấp không đồng bộ.** Node chạy code mới và node chạy code cũ có thể ra kết quả khác nhau cho cùng một block. **Không nâng cấp cuốn chiếu (rolling) từng node.** Dừng toàn cụm, thay binary trên MỌI node, rồi khởi động lại.
2. **Tái chạy lịch sử.** Nếu một block cũ từng dính lỗi mất cập nhật, code mới sẽ tái tạo ra state khác với state đã commit. Node mới sync từ genesis hoặc replay có thể khác node cũ. Trước khi nâng cấp phải kiểm tra (mục 2, bước 3). Nếu phát hiện lệch ở block lịch sử thì **dừng lại và cần quyết định thiết kế** (ví dụ chỉ kích hoạt từ một block cụ thể), không tự nâng cấp.
3. **Đừng để lẫn phiên bản.** Bài học đã gặp: chỉ deploy `--only-node` để kiểm thử rồi bỏ đó thì các node chưa được deploy tái hiện đúng lỗi vừa sửa. Sau mọi lần deploy thử một node, phải deploy lại toàn cụm.

## 2. Trước khi nâng cấp (pre-flight)
1. Chốt commit sẽ triển khai (phải chứa `21a731b5` và `fe218fbc` trên `dev`). `git log --oneline -1` và ghi lại hash binary.
2. `build_check.sh` trong `consensus/metanode/scripts/` phải sạch (`ALL BUILDS PASSED`, không warning).
3. **Replay đối chiếu trên bản sao dữ liệu thật** (không chạy trên dữ liệu đang phục vụ):
   `execution/cmd/tool/tz_replay_check` phát lại một khoảng block đã commit qua đúng đường Block-STM của node và ghi kết quả từng block ra JSON (`-config`, `-from`, `-to`, `-out`); chế độ `-compare-a/-compare-b` so hai file kết quả. Chạy một lần bằng binary CŨ và một lần bằng binary MỚI trên hai bản sao độc lập của cùng dữ liệu rồi so sánh. **Mọi block lệch = dừng.** Lưu ý: công cụ này được viết để so hai chế độ thực thi (cgo/trustzone) và có bỏ qua một số tx (`CALL_GET_API`); hãy đọc phần comment đầu `main.go` để biết phạm vi chính xác trước khi dựa vào nó, và ghi rõ trong báo cáo phạm vi block đã đối chiếu.
4. Chụp snapshot/backup dữ liệu của ít nhất một node (`CreateBackup` qua admin RPC, hoặc cơ chế snapshot của deploy — xem `DEPLOY_GUIDE.md`) để có đường lui.
5. Xác nhận toàn cụm khỏe: `deploy/ansible/monitors/block_hash_checker` (`go run main.go --watch --interval 5s --config config-m-nodes.json --no-stop-flag`) cho độ cao tăng đều và không có `ERR`.
6. Xác nhận không còn commit nào đang kẹt `CONSENSUS-HALT-TX-PAYLOAD-LOST` (grep log execution). Nếu còn, xử lý trước, đừng nâng cấp khi đang halt.

## 3. Nâng cấp (dừng toàn cụm, thay binary, khởi động lại)
Dùng `deploy/ansible/ansible_deploy.sh` (xem `DEPLOY_GUIDE.md` kịch bản 5 và 5.1 — cập nhật code KHÔNG xoá dữ liệu):
```bash
cd deploy/ansible
./ansible_deploy.sh --start --prebuilt-bin /đường/dẫn/bin   # hoặc: ./ansible_deploy.sh --start   (build tại chỗ)
```
Lệnh này build/đóng gói binary, tắt service, chép binary, khởi động lại **trên toàn bộ các node trong `inventory.yml`**. Không dùng `--only-node` cho bước nâng cấp thật.
Đã đối chiếu với code: `--start` ánh xạ sang action `deploy` với `keep_data=true` (giữ dữ liệu) trong `ansible_deploy.sh`; `deploy.yml` không đặt `serial`, nên mỗi bước (dừng, chép, khởi động) chạy trên tất cả host trước khi sang bước sau (giới hạn bởi `forks` của ansible, mặc định 5 host song song; với cụm lớn hơn hãy kiểm tra `ansible.cfg`). Trước khi để lệnh chạy tiếp, đọc dòng `Keep Data:` mà script in ra và phải thấy `true`. **Tuyệt đối không dùng `--reset-all`, `reset-data` hay `gen-keys`** cho việc nâng cấp này vì chúng xoá dữ liệu.
Sau khi chạy xong:
1. Kiểm tra mọi node cùng phiên bản (so hash file `simple_chain`/`metanode` trên từng máy với hash đã ghi ở bước 1).
2. Theo dõi `block_hash_checker` ít nhất vài chục block: `Heights` tăng đều và **không** có lệch hash/stateRoot giữa các node.

## 4. Sau khi nâng cấp (xác nhận)
1. So `hash`, `stateRoot`, `receiptsRoot`, `transactionsRoot` của mọi block từ block cuối trước nâng cấp đến tip trên tất cả node (`eth_getBlockByNumber`); phải khớp 100%.
2. Gửi một tập giao dịch thật có nhiều người gửi cùng chuyển vào một địa chỉ nóng (đúng kiểu stress đã dùng ở test) và kiểm tra số dư cuối bằng tổng đã gửi.
3. Theo dõi log: không có `CONSENSUS-HALT` mới, không có cảnh báo fork guard.
4. Giữ binary cũ và snapshot ít nhất một chu kỳ vận hành.

## 5. Nếu có dấu hiệu lệch hash hoặc halt
1. **Dừng.** Không restart hàng loạt, không xoá dữ liệu vội.
2. Ghi lại: block đầu tiên lệch, node nào lệch, log quanh block đó.
3. Một node lệch riêng lẻ: xem kịch bản 3 trong `DEPLOY_GUIDE.md` (khôi phục từ snapshot của node SyncOnly).
4. Halt mất payload (`CONSENSUS-HALT-TX-PAYLOAD-LOST`): xem runbook trong `deploy/ansible/monitors/start_monitors.sh` và `admin_attestPayloadLossForCommit`. Đây là quyết định **không hoàn tác** (công nhận giao dịch đã mất vĩnh viễn), phải gọi trên cả 4 node, chỉ do người vận hành xác nhận.
5. Rollback: dừng toàn cụm, đưa binary cũ về **mọi** node, khôi phục dữ liệu từ snapshot đã chụp nếu state đã đi lệch, rồi khởi động lại. Không rollback từng node riêng lẻ.

## 6. Việc còn mở
- Chưa có bản đo trên cluster thật/đa máy.
- Bài Chaos Rolling Restart từng có 1 lần FAIL do halt mất payload (chưa chứng minh là không liên quan; 1 lần chạy lại PASS). Nếu lặp lại thì bisect với `69c2f28b`.
- Bảng audit `ErrEstimateHit` theo từng lời gọi nằm ở `execution/pkg/rollup/NEXT_STEPS_PLAN.md` mục N0.5.
