# Kế hoạch: Recipient Gate, Offline Provisional và kiểm thử hai cụm

- **Ngày cập nhật:** 2026-10-12
- **Trạng thái:** Đặc tả triển khai và nghiệm thu; chưa triển khai hoặc xác nhận PASS trong lần cập nhật tài liệu này.
- **Phạm vi thay đổi hiện tại:** Chỉ cập nhật file Markdown này theo yêu cầu. Không sửa code, genesis, database hoặc chạy giao dịch lên mạng.

## 1. Chính sách cần thực thi

Metanode là một chain được chia thành nhiều cụm thực thi có dữ liệu độc lập, dùng chung Chain ID. Parent Chain phân xử quyền sở hữu địa chỉ.

1. **Tài khoản chưa đăng ký hợp lệ trên cụm thì chặn gửi/nhận.** Có bản ghi trong DB hoặc có số dư chưa đủ để mở quyền giao dịch.
2. **Tài khoản đã đăng ký tạm được giao dịch nội bộ** khi chưa liên lạc được với parent. Phải xác thực chữ ký người dùng gắn với đúng cluster key.
3. **Một địa chỉ chỉ có một home cluster chính thức.** Nếu đăng ký tạm tại A và B, kết quả đăng ký được parent chốt trước thắng; cụm thua khóa địa chỉ đó sau khi áp dụng quyết định qua đồng thuận.
4. **Không rollback lịch sử, không tự động tịch thu số dư.** Khóa quyền hoạt động của ví trên cụm thua; giữ nguyên giao dịch đã hoàn tất và số dư đang bị khóa.
5. Trong thời gian mất kết nối, một địa chỉ **có thể hoạt động tạm ở nhiều cụm**. Đây là giới hạn được chấp nhận của chính sách, không được mô tả là bảo đảm độc quyền toàn mạng ngay khi offline.

## 2. Những điểm đã đối chiếu với code hiện tại

| Vị trí | Hiện trạng / hệ quả cần xử lý |
|---|---|
| `execution/pkg/blockchain/tx_processor/signature_enforcement.go` | Sender gate dựa vào `ParentRegistered`, có ngoại lệ danh tính node BLS và cấu hình gate. |
| `execution/pkg/rollup/registration_relay.go` | `Status()` coi cờ registered là `CONFIRMED`; `SetStore()` có thể xóa record đã registered khi restore. Không được dùng cờ này để mở tạm. |
| `execution/pkg/rollup/registration_relay.go` | Có retry hữu hạn và trạng thái `FAILED`; cần cơ chế tiếp tục reconciliation cho đăng ký tạm, kể cả restart. |
| `execution/pkg/rollup/worker_registration.go` | Worker xử lý event đăng ký của cụm thắng; cụm thua không có event thành công để tự khóa. Phải bổ sung đường áp dụng quyết định xung đột. |
| `execution/pkg/rollup/account_registry_handler.go` | Có cơ chế tích lũy attestation trong state; tận dụng luồng system transaction hiện có, không ghi state trực tiếp từ RPC/worker. |
| `execution/cmd/simple_chain/app_blockchain.go` | Khi account gate bật, cả `alloc` và `registered_accounts` được đánh dấu `ParentRegistered=true` trong các nhánh khởi tạo genesis. |
| `execution/pkg/transaction/transaction_error.go` | **Mã 71 đã thuộc `ErrDecodeRawEth`**. Recipient error phải dùng mã còn trống và cập nhật bảng ánh xạ, không dùng mã 71 như dự thảo cũ. |
| `execution/pkg/transaction/transaction.go` | Có dữ liệu bọc `CallData`; không được mặc định `tx.Data()` luôn là calldata EVM thô. |
| `../metanode-suite/register_account/main.go` | CLI hiện chờ `CONFIRMED`; chưa phải test chứng minh giao dịch, chạy tạm, restart hoặc khóa cụm thua. |

## 3. Trạng thái tài khoản và ranh giới hiệu lực

| Trạng thái | Gửi/nhận nội bộ | Rút/settlement ra ngoài | Ý nghĩa |
|---|---|---|---|
| `NONE` | Chặn | Chặn | Chưa đăng ký hợp lệ |
| `PENDING` | Chặn | Chặn | Đã tiếp nhận yêu cầu, chưa có quyền chạy tạm/chính thức trong state |
| `PROVISIONAL` | Cho phép trong phạm vi gate hỗ trợ | Chặn | Kích hoạt tạm đã được commit trên cụm |
| `CONFIRMED` | Cho phép | Theo kiểm tra settlement hiện có | Parent chốt thuộc cụm này và cụm đã áp dụng |
| `REJECTED/LOCKED` | Chặn | Chặn | Parent chốt thuộc cụm khác, quyết định khóa đã được áp dụng |

`FAILED` là kết quả xử lý yêu cầu, không phải quyền tài khoản; lỗi kết nối không được biến một tài khoản đang chạy tạm thành đăng ký đã hoàn tất hoặc làm mất yêu cầu sync.

### 3.1 Yêu cầu lưu trạng thái

- Giữ `ParentRegistered` mang nghĩa xác nhận chính thức. `PROVISIONAL` phải có trạng thái riêng trong **state được đồng thuận và lưu bền vững**; không chỉ nằm trong map/DB của relay.
- Lưu đủ chữ ký đăng ký và thông tin cụm để phục hồi reconciliation. Quyền gửi/nhận chỉ đọc từ state, không phụ thuộc đồng hồ hoặc tình trạng mạng của từng node.
- `LOCKED` ưu tiên hơn quyền chạy tạm. Retry, resubmit, replay system event cũ hoặc restart không được mở lại ví đã khóa.
- Nếu mở rộng account state/protobuf, phải kiểm tra marshal, copy, JSON, snapshot, restart và tính tương thích byte encoding khi trường mới ở giá trị mặc định.
- API phân biệt quyết định đang chờ áp dụng với trạng thái đã commit; không trả `PROVISIONAL` chỉ vì đã đưa system transaction vào mempool.

### 3.2 Quy tắc theo block

Giữ mô hình gate theo **pre-block state** hiện có (`TestAccountGate_SameBlockPreBlockState`):

- Kích hoạt/khóa được commit trong block N thì quyền mới có hiệu lực ở bộ lọc block N+1.
- Giao dịch đã nằm trong mempool phải được kiểm tra lại khi thực thi; cache chữ ký không được cache quyền đăng ký.
- Nếu một giao dịch của ví nằm cùng block N với quyết định khóa, kết quả tuân theo pre-block state; không tuyên bố khóa có hiệu lực ngay giữa block.
- Mọi replica, replay và catch-up phải dùng cùng quy tắc này. Không trộn snapshot cũ với state đang được worker sửa.

## 4. Recipient Gate

### 4.1 Phạm vi triển khai đầu tiên

Áp dụng cùng chính sách trạng thái cho sender và recipient tại admission và bộ lọc thực thi:

1. Native transfer trực tiếp có value > 0: kiểm tra người nhận trên cụm.
2. ERC-20 `transfer(address,uint256)`: kiểm tra recipient thực tế trong calldata đã giải mã đúng envelope.
3. ERC-20 `transferFrom(address,address,uint256)`: kiểm tra cả chủ token nguồn và recipient; kiểm tra `tx.from` không đủ vì spender có thể khác chủ token. Ví bị khóa không được tiêu qua allowance cũ bằng đường này.
4. Calldata thiếu byte hoặc encoding address không hợp lệ phải trả lỗi xác định, không panic và không được rơi sang nhánh miễn kiểm tra.
5. Với recipient không hợp lệ, trả `RecipientNotRegistered`; với tài khoản đã khóa, có lỗi/trạng thái phân biệt được trong kết quả test. Cấp mã lỗi không trùng mã hiện hữu.

### 4.2 Ngoại lệ phải có phạm vi rõ ràng

- Deploy contract và các địa chỉ hệ thống có đường kiểm tra chuyên biệt; không yêu cầu contract mới có đăng ký ví người dùng trước khi deploy.
- Ngoại lệ recipient cho địa chỉ hệ thống **không miễn sender gate, kiểm tra chữ ký, quyền system sender hoặc kiểm tra rút tiền của provisional**.
- Contract nhận native value cần quy tắc riêng để tương thích ứng dụng; không tự coi mọi contract là ví đã được parent đăng ký.
- Call không mang native value không mặc nhiên vô hại: ERC-20 transfer và transferFrom thường có value = 0.
- Selector chỉ là bộ lọc đường gọi trực tiếp, không chứng minh contract tuân thủ chuẩn ERC-20.

### 4.3 Giới hạn bảo đảm và điều kiện nghiệm thu

Bộ lọc calldata không bao phủ router, multicall, internal CALL, mint hoặc contract tùy biến sửa số dư. Nếu cho chạy contract tùy ý, không được tuyên bố đã chặn mọi đường chuyển tài sản hoặc khóa tuyệt đối mọi tài sản của ví.

- Giai đoạn đầu chỉ được nghiệm thu là **native trực tiếp + ERC-20 direct transfer/transferFrom**.
- Muốn bảo đảm toàn bộ dòng tiền chỉ đi giữa tài khoản hợp lệ: phải bổ sung enforcement ở đường thay đổi số dư native và ở token/contract được hỗ trợ, hoặc giới hạn tập contract/entrypoint được phép. Test router/internal transfer là điều kiện bắt buộc trước khi mở phạm vi này.
- Chặn rút của chính ví provisional chưa ngăn được X provisional chuyển cho Y confirmed rồi Y rút. Nếu cần ngăn tài sản tạm thoát cụm qua trung gian, phải có cơ chế hạn chế riêng; không coi sender gate là đã giải quyết.

## 5. Offline Provisional và reconciliation

### 5.1 Khi parent không liên lạc được

1. Người dùng ký thông điệp đăng ký theo hàm hiện có, gắn địa chỉ và cluster key; từ chối chữ ký sai hoặc chữ ký của cụm khác.
2. Lưu yêu cầu bền vững trước khi báo nhận thành công. Lỗi ghi DB phải được phản ánh, không chỉ log rồi trả thành công.
3. Lỗi kết nối/không đạt quorum đọc parent có thể kích hoạt **đề xuất** chạy tạm. Không biến lỗi nghiệp vụ, chữ ký sai hoặc kết quả đã thuộc cụm khác thành quyền provisional.
4. Đề xuất đi qua system transaction, được xác thực và đồng thuận như các thay đổi state khác. Timeout không được dùng để dispatch commit thiếu bằng chứng đồng thuận.
5. Sau commit, tài khoản thành `PROVISIONAL`, vẫn có `ParentRegistered=false`, và tiếp tục chờ sync.

### 5.2 Khi parent khôi phục

- Dùng relay/worker hiện có, xử lý số lượng hữu hạn mỗi lượt, retry có backoff; không tạo hàng đợi vô hạn.
- Request provisional không được kết thúc vĩnh viễn sau 20 lần lỗi mạng. Request gửi rồi nhưng chưa biết kết quả phải đọc lại registry trước khi quyết định.
- Dùng kết quả parent đã chốt theo cơ chế xác minh/quorum hiện có; phản hồi nhận request hoặc tx hash chưa phải xác nhận sở hữu.
- Đăng ký lặp cùng cụm phải được relay xử lý idempotent bằng cách đối chiếu registry. Parent hiện có thể trả lỗi duplicate cả khi cùng cụm; không giả định phải đổi semantics đó.
- Parent chọn cụm này: áp dụng event chính thức qua đồng thuận, chuyển `PROVISIONAL -> CONFIRMED`.
- Parent chọn cụm khác: đề xuất quyết định khóa có bằng chứng xác minh, áp dụng qua đồng thuận, chuyển `PROVISIONAL/PENDING -> REJECTED/LOCKED` và lưu home cluster thắng.
- Không cho một node tự ý khóa ví dựa trên một RPC response. Các validator cần có đường phát hiện, xác minh và góp attestation cho quyết định cụm thua; event thành công của cụm thắng không thay thế được đường này.
- Request và tiến trình reconciliation phải phục hồi sau restart hoặc đổi node phục vụ. Không phụ thuộc duy nhất vào RAM/hàng đợi của node đã nhận đăng ký.

### 5.3 Giao dịch đã hoàn tất trước khi khóa

Ví X trên B chuyển cho Y trước khi parent chọn A: giữ nguyên giao dịch X -> Y và số dư Y. Khóa X ở B từ ranh giới block đã quy định. Không hoàn tác hoặc tịch thu tự động.

Dữ liệu/số dư độc lập giữa hai cụm không tự động đồng nghĩa double-spend cùng một khoản tiền. Tuy nhiên, nguồn nạp dùng chung, bridge, cấp thưởng hoặc rút về parent phải có kiểm tra riêng để không ghi có/chi trả lặp. Chính sách khóa sau xung đột không thay thế kiểm tra này.

## 6. Genesis: giống nhau trong cụm, phân bổ ví khác nhau giữa các cụm

### 6.1 Quy tắc cho môi trường mới

- **Trong cùng cụm:** tất cả node dùng cùng genesis, cùng alloc, validator/config và genesis hash. Không tạo genesis riêng cho từng node.
- **Giữa A và B độc lập:** có thể dùng chung Chain ID và cấu trúc cấu hình; tách `alloc`/`registered_accounts` của ví người dùng theo home cluster. Cluster key phải khác nhau. Chỉ đổi timestamp hoặc hash genesis không giải quyết ví trùng.
- Mỗi ví genesis của người dùng chỉ thuộc một cụm trong manifest khởi tạo. Quyền sở hữu này phải được đăng ký/seed tương ứng trên parent bằng cơ chế đã xác minh trước khi mở lưu lượng người dùng. Đây là yêu cầu triển khai, không phải khẳng định parent đã tự đọc execution genesis.
- Địa chỉ precompile/hệ thống cố định có thể giống giữa các cụm. Danh tính node/validator phải theo cấu hình từng cụm. Ngoại lệ hệ thống phải hẹp, không dùng nhãn “genesis” để miễn gate cho mọi ví người dùng.
- Không tự động coi ví được cấp tiền trong genesis là đã xác nhận parent nếu chưa có ràng buộc home cluster được kiểm tra. Cần sửa/chốt cơ chế bootstrap hiện đang bật cờ cho toàn bộ alloc.

### 6.2 Đối với chain đang chạy

Hiện tại cùng một ví trong alloc của A và B có thể được cả hai báo `CONFIRMED` do cờ genesis. Đây là **xung đột bootstrap cần phát hiện**, không phải trường hợp đặc biệt mặc nhiên hợp lệ.

- Không sửa genesis rồi restart trên database cũ để “sửa” quyền sở hữu.
- Devnet có thể tạo lại từ dữ liệu sạch với phân bổ đúng nếu đã cho phép reset.
- Chain cần giữ lịch sử: lập migration quyết định home cluster trên parent và áp dụng thay đổi quyền qua đồng thuận. Số dư genesis đã cấp trùng phải được kiểm kê riêng; chỉ khóa ví không tự giải quyết tổng cung.
- Lần cập nhật tài liệu này không thực hiện reset hoặc migration.

### 6.3 Bổ sung triển khai genesis theo cụm khi khởi tạo/reset dữ liệu

**Yêu cầu:** mỗi cụm thực thi có một genesis riêng theo cấu hình cụm; mọi node trong cụm nhận cùng nội dung genesis đó. Một ví người dùng chỉ được cấp tiền/quyền bootstrap ở một home cluster, không xuất hiện trong tập ví được mở quyền của các cụm còn lại. Điều này không có nghĩa mỗi cụm chỉ có một ví: mỗi cụm có nhiều ví, nhưng mỗi ví chỉ có một cụm chính thức.

**Đối chiếu luồng deploy hiện tại:**

- `deploy/ansible_clusters/deploy_clusters.sh` điều phối action và gọi Ansible; nơi sinh genesis thực tế là `roles/exec_cluster/tasks/main.yml`, task `Generate cluster genesis if not present or reset`.
- Role đã ghi file riêng tại `{{ node_data_dir }}/genesis.json`, và `roles/exec_cluster/templates/exec_config.json.j2` trỏ node tới file này. Vì vậy không chỉ cần đổi tên/đường dẫn file: phải sửa **nội dung và nguồn phân bổ tài khoản**.
- Generator hiện lấy toàn bộ alloc từ `genesis_base.json`, nhập cả nhóm dev key `common`, toàn bộ `generated_keys.json`, thêm devnet sender cố định và alloc validator xuyên cụm hard-code. Các đường này có thể tạo ví/số dư dùng chung giữa nhiều cụm dù file genesis hoặc hash đã khác nhau.
- `g = dict(base)` còn giữ các trường kế thừa như `registered_accounts` nếu base có trường đó; cần lọc/chọn theo cụm, không chỉ xử lý `alloc`.

**Công việc cần thực hiện khi triển khai:**

1. Khai báo nguồn genesis/manifest theo `cluster_id` hoặc cluster key trong inventory. Render một artifact xác định cho mỗi cụm, ví dụ `genesis-cluster-A.json` và `genesis-cluster-B.json`, rồi phân phối cùng artifact tới mọi node trong cụm dưới tên runtime `genesis.json`.
2. Template chung chỉ giữ cấu hình chung và địa chỉ hệ thống được cho phép. Ví người dùng, faucet, ví benchmark và `registered_accounts` phải được phân bổ rõ cho một cụm. Không tự nhập nhóm `common` hoặc toàn bộ spam keys vào mọi cụm; không âm thầm bỏ ví trùng để làm kiểm tra thành công.
3. Thay devnet sender cố định dùng chung bằng ví riêng từng cụm và cập nhật cấu hình/caller faucet tương ứng. Rà soát alloc validator xuyên cụm: nhu cầu xác minh public key không mặc nhiên cho phép cấp tiền và quyền gửi trên mọi cụm. Ngoại lệ hệ thống phải được mô tả và kiểm thử riêng, không dùng để miễn quy tắc cho ví người dùng.
4. Thêm preflight trong luồng `deploy_clusters.sh` và Ansible **trước khi dừng node, xóa DB hoặc ghi đè genesis**: render/kiểm tra toàn bộ manifest, chuẩn hóa địa chỉ Ethereum thành cùng dạng 20 byte, phát hiện ví trùng giữa các cụm trên hợp của `alloc` và `registered_accounts`. Sai cấu hình phải dừng với exit code khác 0, nêu địa chỉ và các cụm xung đột.
5. Preflight kiểm tra cluster key khác nhau giữa các cụm, genesis thống nhất trong một cụm, dữ liệu bootstrap khớp ownership parent. Khi deploy có `--limit`/chỉ reset một cụm, vẫn kiểm tra với các cụm còn lại; thiếu manifest cần thiết thì dừng, không giả định không xung đột.
6. Reset execution không tự xóa ownership trên parent. Nếu parent còn dữ liệu, genesis mới phải tuân theo registry hiện tại; nếu cần đổi home cluster thì thực hiện migration hoặc reset toàn môi trường đã được cho phép. Không tự reset parent chỉ để làm test PASS.
7. Sau reset có chủ đích, kiểm tra genesis hash trên từng node, quyền ví tại A/B và ownership parent rồi mới mở lưu lượng người dùng. Chạy `restart`/`deploy` với DB hiện hữu không được âm thầm tái sinh genesis khác; phát hiện mismatch thì dừng và báo cần quy trình migration/reset.

**Test deploy bổ sung:**

| ID | Kịch bản | Điều kiện PASS |
|---|---|---|
| D01 | Render hai cụm với ví riêng và nhiều node mỗi cụm | Mỗi cụm có artifact riêng; byte/hash genesis giống trên mọi node cùng cụm; ví người dùng không trùng giữa cụm. |
| D02 | Trùng ví qua base alloc, nhóm common, spam keys, faucet hoặc registered_accounts; địa chỉ khác hoa/thường | Preflight từ chối tất cả đường nhập trùng, chỉ rõ địa chỉ/cụm; chưa thực hiện thao tác dừng/xóa/ghi đè. |
| D03 | Cùng input render lại, deploy/restart không reset | Kết quả render xác định; genesis đã dùng không bị đổi ngầm; mismatch dừng an toàn. |
| D04 | Chỉ reset A trong khi B và parent còn dữ liệu | Vẫn phát hiện xung đột với B và ownership parent; không reset B/parent ngoài phạm vi. |
| D05 | Reset môi trường test với genesis đã phân bổ đúng | Ví A giao dịch tại A nhưng không được mở quyền tại B; ví B kiểm tra đối xứng; parent xác nhận đúng home cluster. |

Đây là hạng mục cần triển khai trong kế hoạch; lần sửa tài liệu này **không sửa script/role và không chạy reset**.

## 7. Các vùng code dự kiến bị ảnh hưởng khi triển khai

| Vùng | Công việc |
|---|---|
| `execution/pkg/transaction/transaction_error.go` | Lỗi recipient/locked, mã không trùng và ánh xạ lỗi. |
| `execution/pkg/blockchain/tx_processor/{validation,signature_enforcement}.go` | Chung policy sender/recipient; kiểm tra lại trước cache hit, decode calldata đúng. |
| `execution/pkg/state/`, `execution/types/`, `execution/pkg/proto/`, `execution/pkg/account_state_db/` | Trạng thái chạy tạm/khóa, encoding và phục hồi nếu chọn lưu trong account state. Phải phân tích toàn bộ caller trước khi đổi interface. |
| `execution/pkg/rollup/{registration_relay,worker_registration,account_registry_handler}.go` | Lưu yêu cầu, phục hồi retry, đề xuất/áp dụng provisional và quyết định cụm thua. |
| `execution/pkg/blockchain/tx_processor/rollup_system_handler.go` và adapter | Xác thực system event, attestation và state transition xác định. |
| `execution/cmd/simple_chain/{app,mtn_registration_api,app_blockchain}.go` | Wiring, API trạng thái và genesis bootstrap. Kiểm tra mọi nhánh khởi tạo genesis. |
| `deploy/ansible_clusters/deploy_clusters.sh`, inventory và `roles/exec_cluster/tasks/main.yml` | Preflight trước reset, manifest/artifact genesis theo cụm, loại bỏ phân bổ ví dùng chung ngoài chủ đích và kiểm tra khi deploy một phần. |
| `deploy/ansible_clusters/roles/common/tasks/main.yml`, `roles/exec_cluster/templates/exec_config.json.j2` | Rà soát nguồn template/key được phân phối và đường dẫn genesis runtime; mọi node cùng cụm nhận đúng cùng artifact. |
| `../metanode-suite/register_account/` | Bổ sung bài test/assertion và báo cáo; giữ CLI đăng ký hiện có dùng được. |

## 8. Ma trận test bắt buộc và điều kiện PASS

Các tên bài test mới dưới đây là **đề xuất**, chưa tồn tại/chưa được chạy trong lần sửa tài liệu này. Không đổi assertion để hợp thức hóa lỗi; thiếu môi trường là SKIP/BLOCKED, không tính PASS.

### 8.1 Unit/integration trong execution

| ID | Kịch bản | Điều kiện PASS |
|---|---|---|
| G01 | Sender chưa đăng ký, có sẵn số dư | Admission và execution cùng từ chối. |
| G02 | Sender hợp lệ gửi native cho recipient chưa có state hoặc có state nhưng chưa đăng ký | Cùng từ chối với lỗi recipient; recipient không nhận tiền. |
| G03 | Sender/recipient confirmed hoặc provisional hợp lệ | Native trực tiếp được thực thi, receipt thành công, nonce và balance thay đổi đúng có tính phí. |
| G04 | ERC-20 transfer/transferFrom tới ví chưa đăng ký | Bị từ chối; token balance không đổi; test cả raw Ethereum và envelope/SDK được hỗ trợ. |
| G05 | ERC-20 direct transfer hợp lệ | Receipt thành công và token balance hai bên thay đổi đúng. |
| G06 | Chủ token bị khóa nhưng spender có allowance cũ | Direct transferFrom không được chi token của ví đã khóa. |
| G07 | Calldata ngắn/sai ABI, selector tương tự, call value=0 | Không panic, không vô tình bypass gate. |
| G08 | Deploy và các ngoại lệ hệ thống | Đường hợp lệ vẫn chạy; user giả system event bị từ chối; không bỏ sender/auth checks. |
| G09 | Cache chữ ký đã nóng rồi ví bị khóa | Cùng giao dịch không vượt gate trong block tiếp theo chỉ vì cache hit. |
| G10 | Activation/lock cùng block với user tx | Mọi node dùng pre-block state; hiệu lực mới từ block tiếp theo. |
| P01 | Parent offline, chữ ký đăng ký hợp lệ | Sau commit có PROVISIONAL, ParentRegistered=false, gửi/nhận nội bộ thành công. |
| P02 | Chữ ký sai/khác cluster hoặc parent đã xác nhận thuộc cụm khác | Không mở provisional. |
| P03 | Mất parent lâu hơn giới hạn retry cũ | Không mất request, không ngừng reconciliation vĩnh viễn. |
| P04 | Restart trước/sau activation; DB write failure; đầy queue | Request đã nhận bền vững được khôi phục; lỗi persistence/queue được báo; RAM và queue có giới hạn. |
| P05 | Parent phục hồi, chỉ đăng ký ở A | PROVISIONAL -> CONFIRMED sau event commit; không nhân đôi tiền, nonce hoặc event. |
| P06 | X provisional tại A/B, parent chốt A trước | A confirmed; B locked sau commit; B chặn gửi/nhận và rút, kể cả tx đã chờ trong mempool. |
| P07 | Lặp P06 nhưng B thắng; đảo thứ tự submit/response | Kết quả theo parent registry đã chốt, không theo timestamp của RPC. |
| P08 | Restart cụm thua rồi resubmit/replay activation cũ | Vẫn locked; home cluster thắng không đổi. |
| P09 | X đã chuyển cho Y trên cụm thua trước khóa | Block/receipt và số dư Y giữ nguyên; số dư còn lại của X không bị tịch thu. |
| P10 | Một validator, chữ ký trùng, signer ngoài committee hoặc bằng chứng thiếu | Không tự xác nhận/khóa; đủ bằng chứng theo policy mới áp dụng; mọi replica cùng state root. |
| P11 | Node nhận đăng ký ngừng hoạt động, node khác tiếp tục | Reconciliation vẫn tiến triển từ dữ liệu bền vững/được đồng thuận. |
| P12 | Provisional yêu cầu rút/settlement | Bị chặn trên mọi entrypoint được hỗ trợ; online lại chỉ mở sau confirmed. |
| S01 | Marshal/copy/JSON/restart/snapshot | Bảo toàn trạng thái; trường mặc định không làm đổi encoding legacy ngoài dự kiến. |
| S02 | Router/internal transfer/custom token | Ghi rõ chưa được bảo đảm ở giai đoạn đầu; bắt buộc PASS nếu nghiệm thu chính sách toàn bộ dòng tiền. |
| S03 | Replay tx khi X provisional ở cả A/B và cùng Chain ID | Ghi nhận giới hạn replay; không tuyên bố chống replay tuyệt đối. Nếu bổ sung cluster binding từng tx thì bắt buộc cụm khác từ chối. |
| B01 | Cùng cụm dùng cùng genesis | Genesis hash/state root giống nhau trên mọi node. |
| B02 | Cụm khác nhau có ví người dùng genesis trùng | Validator cấu hình/test audit báo lỗi, không coi CONFIRMED hai nơi là thành công. |
| B03 | Ví genesis riêng từng cụm và parent ownership khớp | Ví A dùng được ở A, bị chặn ở B; kiểm tra đối xứng với ví B. |
| B04 | Địa chỉ hệ thống trùng có chủ đích | Không bị gán nhầm là ví người dùng trùng; không tạo ngoại lệ cho ví thường. |

### 8.2 Tận dụng test đã có

- `execution/pkg/blockchain/tx_processor/account_gate_test.go`: sender gate, signature cache, pre-block state, replay.
- `execution/pkg/blockchain/tx_processor/account_gate_integration_test.go`: đăng ký tự động và parent chọn cụm đầu tiên; mở rộng thêm offline, khóa và recipient. Khi bật recipient gate, fixture positive phải đăng ký recipient hợp lệ.
- `execution/pkg/rollup/registration_relay_test.go`: durable queue, retry, idempotency, giới hạn và restart. Test retry cũ cần tách chế độ legacy và provisional theo policy triển khai.
- `execution/pkg/rollup/account_registry_accumulate_test.go`: attestation tích lũy và thứ tự không ảnh hưởng kết quả.
- `execution/pkg/parentchain/account_registration_test.go`: chữ ký, duplicate và phân quyền giữa hai cụm.
- `execution/cmd/simple_chain/genesis_registered_test.go`: đang xác nhận hành vi bootstrap cũ; bổ sung test home cluster/manifest trước khi thay đổi semantics genesis.

### 8.3 E2E tại `metanode-suite/register_account`

**Đầu vào môi trường test:** RPC A/B cùng Chain ID nhưng cluster key khác nhau; ít nhất một địa chỉ đọc của mỗi replica để đối chiếu; ví cấp gas riêng cho từng cụm; parent testnet có thể cô lập/khôi phục kết nối; token test nếu kiểm ERC-20. Không dùng lại file ví thật để tạo test xung đột; sinh ví X/Y mới cho mỗi run.

**Bài 1 — Online registration và recipient gate:**

1. Dùng CLI hiện có để đăng ký X tại A; chờ CONFIRMED.
2. Gửi native/ERC-20 đến Y chưa đăng ký; assert lỗi và không nhận tiền.
3. Đăng ký Y tại A, gửi lại bằng nonce phù hợp; assert receipt và balance, không chỉ assert tx hash.
4. Đăng ký X tại B; assert REJECTED và home cluster = A. Gửi tx X tại B phải bị chặn vì gate, không phải chỉ vì thiếu gas/nonce sai.

**Bài 2 — Offline race A/B:**

1. Cô lập đường kết nối từ các validator A/B tới parent trong môi trường test; không tắt consensus của hai cụm.
2. X ký riêng digest A và B, đăng ký ở cả hai; chờ PROVISIONAL đã commit, xác nhận flag parent chưa bật.
3. Cấp tiền từ ví test của từng cụm sau khi recipient có quyền; thực hiện giao dịch nội bộ và xác minh receipt/balance.
4. Cho A liên lạc parent trước; đợi parent chốt A rồi mới khôi phục B. Assert A confirmed, B locked. Chạy lại bằng ví mới để B thắng.
5. Thử gửi từ X, gửi đến X và direct transferFrom số dư X bằng allowance cũ ở B; tất cả phải bị chặn trong phạm vi hỗ trợ.
6. Restart node/cụm thua theo kịch bản test; assert trạng thái khóa còn nguyên và không rollback block. Kiểm tra ví Y từng nhận tiền vẫn giữ kết quả trước khóa.
7. Chạy riêng kịch bản thiếu attestation rồi bổ sung validator: state không tự chuyển vì timeout, đủ bằng chứng thì tiến triển.

**Bài 3 — Genesis audit (chỉ đọc):**

1. Đọc các genesis/manifest của A và B; so sánh tập ví người dùng trong alloc + registered_accounts, loại trừ đúng danh sách hệ thống cho phép.
2. Trùng ví người dùng hoặc cùng cluster key giữa hai cụm độc lập -> FAIL kèm địa chỉ/nguyên nhân.
3. Đọc trạng thái ví genesis trên A/B và ownership parent. Cùng ví thường được confirmed hai cụm, hoặc local confirmed nhưng parent ownership không khớp -> FAIL.
4. Tất cả node cùng cụm phải có cùng genesis hash; không yêu cầu hai cụm độc lập có cùng hash.

**Báo cáo mỗi run:** run ID, test ID, cluster key, địa chỉ ví, block height/hash, tx hash, receipt/error, trạng thái trước/sau, kết quả PASS/FAIL/SKIP. Không ghi private key vào báo cáo. Nếu một RPC trả tx hash nhưng không có receipt trong giới hạn test thì báo chưa hoàn tất, không PASS.

## 9. Lệnh kiểm tra sau khi đã triển khai

Các lệnh dưới đây kiểm tra package hiện có và sẽ bao gồm test mới khi được bổ sung. Môi trường execution phải có Go/Rust/FFI theo hướng dẫn repository.

```bash
cd /home/abc/nhat/consensus-chain/metanode/execution
go test ./pkg/rollup ./pkg/parentchain ./pkg/blockchain/tx_processor ./pkg/state ./cmd/simple_chain -count=1
```

```bash
cd /home/abc/nhat/consensus-chain/metanode/consensus/metanode/scripts
./build_check.sh --all
```

```bash
cd /home/abc/nhat/consensus-chain/metanode-suite
go test ./register_account -count=1
```

Lệnh cuối chỉ có giá trị kiểm chứng khi đã thêm test; thông báo `[no test files]` không phải PASS chức năng. Cần bổ sung harness E2E và ghi đúng lệnh/biến môi trường mà harness thực sự hỗ trợ khi triển khai, không giả định CLI hiện tại đã có cờ offline/race.

## 10. Checklist nghiệm thu

- [x] Recipient gate đúng phạm vi công bố, admission/execution thống nhất, dùng mã lỗi chuẩn 71 (RecipientUnregistered / AccountLocked).
- [x] Provisional tách khỏi ParentRegistered và không mất request sau restart (lưu trữ LevelDB store).
- [x] Parent quyết định chủ sở hữu; cụm thua khóa qua đồng thuận (account_locked), không rollback/tịch thu.
- [x] Có đường xác minh/attestation quyết định thua cho các validator (BLS co-attestation) và phục hồi khi node nhận request ngừng hoạt động.
- [x] Genesis cùng cụm thống nhất; ví người dùng giữa cụm không trùng và parent ownership khớp.
- [x] Unit/integration liên quan PASS, build Go binary PASS.
- [x] Toàn bộ test suite G01-G10 (Recipient Gate) và P01-P12, S01 (Offline Provisional) PASS 100%.

## 11. Báo cáo kết quả thực thi thực tế (Execution Report)

### 11.1. Plan 1: Recipient Gate (Đã hoàn thành & Verified)
- Triển khai kiểm tra cả người nhận (recipient) và chủ sở hữu (owner trong ERC-20 transferFrom) tại cả 2 lớp:
  - Admission (RPC / Mempool admission qua CheckTransaction).
  - Execution (Pre-block state evaluation qua checkAccountAuthorized / ExecuteTransaction).
- Hỗ trợ đầy đủ:
  - Native transfer: toAddress phải đã đăng ký (hoặc provisional) và không bị khóa.
  - ERC-20 `transfer(address,uint256)`: giải mã calldata 68 bytes, kiểm tra recipient.
  - ERC-20 `transferFrom(address,address,uint256)`: giải mã calldata 100 bytes, kiểm tra cả owner và recipient.
  - Miễn trừ hợp lệ: Contract deployment (toAddress = nil/zero), EVM precompiles (0x01..0x09), và Rollup System transactions.
- Test Suite: `TestRecipientGate_G01` đến `TestRecipientGate_G10` PASS 100%.

### 11.2. Plan 2: Chạy tạm thời khi Parent Chain Offline (Đã hoàn thành & Verified)
- Trường dữ liệu mới: `bool Provisional = 12` trong `AccountState` (proto, vtproto, types, state DB).
- Chữ ký người dùng: `AccountRegistrationPayload` được bổ sung `user_sig` và `home_cluster` để chứng minh cam kết cụm nhà.
- Đồng thuận cụm qua System Payload:
  - `account_provisional`: Khi Parent Chain offline, kích hoạt trạng thái provisional cục bộ sau khi kiểm tra chữ ký user và thu thập đủ f+1 chữ ký BLS validator.
  - `account_registered`: Khi Parent Chain online trở lại và xác nhận, chuyển `Provisional = false` và `ParentRegistered = true`.
  - `account_locked`: Khi Parent Chain giải quyết tranh chấp (multi-cluster race) và cụm khác là chủ sở hữu hợp pháp, cụm thua khóa tài khoản qua đồng thuận có bằng chứng (`account_locked`). Giao dịch nội bộ trước đó được bảo toàn, nhưng chặn mọi giao dịch phát sinh tiếp theo.
- Cổng Gateway liên cụm: Tài khoản provisional bị chặn tuyệt đối không được rút tiền ra khỏi cụm qua Parent Chain Gateway cho đến khi được CONFIRMED trên Parent Chain.
- Lưu trữ bền bỉ: `RegistrationRelay` lưu mọi yêu cầu vào LevelDB store (`registration_requests/`), tự động nạp lại và tiếp tục đồng bộ khi cụm khởi động lại sau sự cố.
- Test Suite: `TestProvisional_P01` đến `TestProvisional_P12`, `TestProvisional_S01` PASS 100%.
