# Kế hoạch sửa thiết kế CLI và triển khai Ansible

> Last updated: 2026-09-18
> Trạng thái: kế hoạch triển khai; chưa thay đổi script hoặc chạy deploy.
> Phạm vi chính: public chain trong `deploy/ansible/`; cập nhật các caller liên quan trong CI và metanode-suite.
> Tham chiếu kiến trúc: `PROJECT_STRUCTURE.md`.

## 1. Mục tiêu và giới hạn

Chốt ý nghĩa của từng lệnh, phạm vi node và tác động lên dữ liệu trước khi tách playbook. Giữ kiến trúc hiện có, tái sử dụng roles và triển khai theo từng giai đoạn có thể kiểm chứng độc lập.

Các yêu cầu bắt buộc:

- Thứ tự flag không làm thay đổi kết quả.
- Một lần gọi chỉ có một action; tham số sai phải bị từ chối trước mọi tác động.
- Start/restart/stop không build, phân phối binary, sinh keys hoặc thay genesis.
- Deploy giữ keys, genesis và dữ liệu; restore/reset phải có phạm vi rõ ràng.
- Không sửa consensus, digest verification hoặc cơ chế dispatch commit.
- Không dùng timeout để cho phép commit chưa verified. Timeout vận hành chỉ được kết thúc kiểm tra và báo lỗi.
- Không coi việc service đang chạy là bằng chứng cluster đã đồng bộ hoặc không fork.
- Không tự động mở firewall, reset dữ liệu hoặc cập nhật monitors trong một lệnh không yêu cầu các việc đó.

Không thuộc giai đoạn đầu: di chuyển source monitors, thiết kế framework deploy mới, tự động rolling upgrade, thay đổi CLI private-chain.

## 2. Những bất cập đã xác nhận

| Hiện trạng | Hệ quả | Hướng sửa |
|---|---|---|
| Public wrapper nhận `--reset-all`, không nhận `--setup` | Plan cũ dùng lệnh không hợp lệ | Không dùng `--setup` cho public chain |
| `--start` ghi lại `KEEP_DATA=true` | `--clean --start` khác `--start --clean` | Parse xong mới validate và xác định hành vi |
| Nhiều action ghi đè nhau | `--stop --start` âm thầm chọn start | Từ chối nhiều action |
| `--restore-node N` không đặt target node | Restore một node nhưng stop/deploy cả cụm | Restore luôn chọn đúng một node |
| `--reset-all --only-node N` vẫn sinh cấu hình toàn cụm | Local keys/genesis và remote state lệch phạm vi | Cấm reset-all theo node |
| Start hiện tại thực chất build và deploy | Tên lệnh gây hiểu nhầm | Tách deploy và service start |
| Wrapper dừng monitors toàn cụm | Thao tác một node ảnh hưởng quan sát toàn cụm | Tách lifecycle monitors |
| Parser inventory còn phục vụ CI và validation | Xóa file sẽ mất chức năng hoặc bỏ qua preflight | Chuyển caller và validation trước khi loại bỏ |
| Nhiều đường dẫn phụ thuộc `playbook_dir` | Di chuyển playbook có thể sai project root/scripts | Giữ entrypoint cùng cấp ở giai đoạn đầu |

Các nguồn chính: `ansible_deploy.sh`, `deploy.yml`, `roles/local_build/tasks/main.yml`, `roles/snapshot_restore/tasks/main.yml`, `group_vars/all.yml`, `deploy/ci/ci_runner.py`.

## 3. Hợp đồng CLI mới

Giữ tên wrapper `ansible_deploy.sh`, dùng subcommand làm cú pháp chuẩn:

```text
./ansible_deploy.sh <command> [options]
```

Các ví dụ trong tài liệu là cú pháp mục tiêu, chưa phải lệnh được hỗ trợ ở phiên bản hiện tại.

### 3.1. Lệnh và tác động

| Lệnh | Phạm vi bắt buộc | Hành vi | Keys/genesis và dữ liệu |
|---|---|---|---|
| `build` | Cục bộ (local), không cần inventory | Biên dịch và công bố artifact tại `deploy/bin/`; chỉ ghi build cache và output local | Không đổi keys/genesis hoặc dữ liệu node |
| `deploy` | `--node N` hoặc `--all` | Chuẩn bị artifact (tự build hoặc từ `--bin-dir`), kiểm tra, dừng node đích, cập nhật, khởi động và kiểm tra | Giữ nguyên identity, genesis và dữ liệu |
| `start` | `--node N` hoặc `--all` | Bật service đã cài | Giữ nguyên |
| `stop` | `--node N` hoặc `--all` | Dừng service node đích | Giữ nguyên |
| `restart` | `--node N` hoặc `--all` | Khởi động lại service node đích | Giữ nguyên |
| `restore` | Chỉ `--node N` | Khôi phục từ `--snapshot-url URL`, khởi động và kiểm tra | Giữ keys/genesis; thay dữ liệu node đích |
| `reset-data` | Chỉ `--node N` | Dừng node, xóa dữ liệu node, khởi động lại để sync từ peers | Giữ keys/genesis |
| `reset-all` | Toàn inventory, không nhận selector | Khởi tạo lại chain và deploy toàn cụm | Sinh lại validator keys/cấu hình, reset dữ liệu; giữ custom alloc và chính sách RecoveryCommittee hiện có |
| `gen-keys` | Toàn inventory, chỉ local | Tạo bộ keys/genesis phục vụ bootstrap | Mặc định từ chối ghi đè bộ đã tồn tại; ghi đè phải nêu rõ `--overwrite` |
| `open-ports` | `--node N` hoặc `--all` | Áp dụng firewall theo cấu hình đã có | Không đổi service/data |
| `monitors deploy/start/stop/restart/status` | `--scope local\|all` | Quản lý monitors trên controller hoặc nhóm monitoring hosts | Không tác động lifecycle node |

Quyết định bổ sung:

- Không có command: hiện help và trả lỗi usage, không mặc định deploy.
- `--node` và `--all` loại trừ nhau. Không dùng `--limit` thay cho node selector vì một host có thể chứa nhiều node.
- `deploy` yêu cầu keys/genesis đã chuẩn bị và tương thích cluster. Thiếu thì báo lỗi, không tự sinh.
- `reset-data` chỉ phục hồi node của chain đang tồn tại; không phải cách bootstrap chain mới và không reset cả cụm.
- `reset-all` là workflow phá hủy toàn cụm: yêu cầu opt-in `--yes-reset-all`; không dùng xác nhận tương tác trong CI.
- `restore` và `reset-data` phải hiển thị node, host và đường dẫn dữ liệu trước khi thực thi.
- Không ghi lại `.last_deployed_commit` khi chỉ start/stop/restart/open-ports. Với binary prebuilt, không nhận commit của checkout hiện tại làm provenance nếu chưa xác minh artifact.

### 3.2. Options và validation

| Option | Áp dụng | Quy tắc |
|---|---|---|
| `--inventory PATH` | Mọi command trừ build | Mặc định inventory hiện tại; đường dẫn phải tồn tại |
| `--node N`, `--all` | Các lệnh trong bảng trên | Node phải tồn tại duy nhất trong inventory |
| `--bin-dir DIR` | deploy/reset-all/gen-keys/monitors deploy | Chọn artifact prebuilt rõ ràng; kiểm tra thành phần đúng theo command |
| `--fast`, `--debug-cpp` | build; deploy/reset-all khi build từ source | Không kết hợp prebuilt; không nhận ở service commands; semantics theo mục 3.3 |
| `--snapshot-url URL` | restore | Bắt buộc, không chấp nhận rỗng; xác minh nguồn và metadata trước bước phá hủy |
| `--btrfs-size SIZE` | deploy/reset-all | Validate kích thước và shared storage trước khi dừng node |
| `--open-ports` | deploy/reset-all | Tùy chọn rõ ràng để cấu hình firewall kèm theo deploy; tái sử dụng workflow firewall |
| `--overwrite` | gen-keys | Cho phép thay bộ local; không được hiểu là an toàn để deploy vào chain đang chạy |
| `--yes-reset-all` | reset-all | Opt-in cho reset toàn cụm |
| `--dry-run` | Mọi command | Chỉ in action, targets, paths, nguồn artifact và các bước sẽ chạy |

`--dry-run` là chế độ lập kế hoạch của wrapper: không build, tạo keys, ghi config, gửi Telegram, SSH hoặc gọi playbook có mutation. Nó không xác nhận remote readiness và không được quảng bá là kiểm chứng đầy đủ. Ansible check mode, nếu dùng khi kiểm thử, phải được đánh giá riêng cho các task shell/script.

Quy tắc parser được chốt như sau:

- Cú pháp mới bắt buộc command đứng đầu, rồi đến options; riêng monitors có thêm subcommand ngay sau `monitors`.
- Options sau command được đảo thứ tự mà không đổi hành vi. Không hỗ trợ global options đứng trước command, ngoại trừ `-h/--help` để hiện help chung.
- Legacy flags được nhận diện và chuẩn hóa qua adapter riêng ở mục 4. Không trộn subcommand mới với action flag cũ; ví dụ `deploy --start` phải báo lỗi.
- Option có giá trị phải nhận đúng một giá trị hợp lệ. Từ chối option lặp lại để tránh quy tắc ngầm “giá trị cuối thắng”.
- Command nhận option ngoài bảng cho phép phải báo lỗi; không bỏ qua im lặng. `build` từ chối inventory, target selector, prebuilt và các tùy chọn remote.
- Parser dùng `while/case` và Bash array cho argv; không dùng `eval` để chạy lệnh. Extra vars truyền bằng JSON có kiểu dữ liệu rõ ràng, tránh ghép chuỗi quote thủ công.
- Parse và validate cú pháp hoàn tất trước notification, chmod binary, ghi file hoặc gọi remote. Validation inventory/artifact tiếp theo chỉ đọc dữ liệu; remote preflight chạy sau khi xác định targets, trước mutation remote.

### 3.3. Build và hợp đồng artifact

- `build` không đọc inventory/Vault, không resolve targets, không SSH, không gửi Telegram và không ghi `.last_deployed_commit`. Nó vẫn chạy được khi chưa có inventory hay bộ keys local.
- `build` và nhánh build-from-source của deploy/reset-all dùng chung implementation biên dịch; không sao chép các lệnh Go/Rust/FFI sang wrapper hoặc một role khác.
- Giữ semantics `--fast` hiện có: Rust dùng debug profile thay vì release; không mô tả là chỉ bỏ các bước dư thừa. Mặc định không có flag là release. `--debug-cpp` giữ cách bật debug C++ linker của builder hiện tại và được ghi rõ trong output.
- Output chuẩn của bước build là `deploy/bin/`, tối thiểu có cặp `metanode` và `simple_chain` từ cùng lần build thành công. Artifact không chứa validator keys hay genesis của cluster.
- Kèm metadata nhỏ ghi source commit, trạng thái dirty, OS/architecture, Rust profile, debug-cpp và checksum từng binary. Commit chỉ là provenance; không coi commit của checkout là bằng chứng binary có sẵn được build từ commit đó.
- Build trong staging riêng. Chỉ công bố bộ output hoàn chỉnh sau khi kiểm tra thành công; lỗi hoặc bị ngắt phải giữ bộ output trước đó dùng được. Serialize việc publish/read bộ artifact dùng chung bằng lock local; không ghi đè từng binary đang được một deploy khác lấy để đóng gói.
- `deploy --bin-dir DIR` không biên dịch: kiểm tra artifact rồi đóng gói và phân phối. Không sửa quyền hay nội dung trong thư mục đầu vào của người dùng. Thiếu binary/helper cần cho workflow phải báo lỗi trước stop, không fallback sang compile.
- Prebuilt legacy chưa có metadata được ghi provenance/profile là `unknown`, không tự gán release hay commit hiện tại. Nếu có metadata thì kiểm tra checksum và target compatibility; checksum sai phải dừng.
- Kiểm tra helper theo workflow: node deploy cần cặp binary node; gen-keys cần keytool và các helper RecoveryCommittee mà workflow thực sự sử dụng; monitors deploy có bộ binary riêng. Không bắt mọi command cần toàn bộ tools.

Builder hiện tại `deploy/systemd/build_release.sh` xuất `metanode-deploy/bin/` và `metanode-deploy.tar.gz`. Khi implement phải tách bước build khỏi đóng gói: công bố binary chuẩn tại `deploy/bin/`, sau đó bước package dùng bộ đã chọn để tạo layout/tarball hiện có cho downstream. Giữ entrypoint builder cũ tương thích với callers đã xác minh, không bỏ tarball đang được node_setup sử dụng. Cập nhật cả call sites và đường dẫn, không chỉ đổi mô tả trong CLI.

Ví dụ cú pháp mục tiêu:

```bash
./ansible_deploy.sh build --fast
./ansible_deploy.sh deploy --node 2 --bin-dir /path/to/metanode/deploy/bin
./ansible_deploy.sh restart --node 2
```

## 4. Tương thích với CLI cũ

Giữ adapter trong wrapper để nhận legacy flags, phát cảnh báo deprecation và chuẩn hóa về cùng mô hình command/targets. Không duy trì hai implementation workflow.

| Cú pháp cũ | Ánh xạ hoặc hành vi chuyển đổi |
|---|---|
| `--start [--only-node N] [--fast]` | `deploy --node N [--fast]`; không có selector thì deploy toàn cụm (cần đảm bảo ánh xạ chính xác tổ hợp cờ này do CI phụ thuộc vào nó) |
| `--restart`, `--stop` | Service command tương ứng; không selector giữ phạm vi toàn cụm của caller cũ |
| `--restore-node N --snapshot-url U` | Restore đúng N; nếu có `--only-node M` khác N thì từ chối |
| `--start --restore-node N ...` | Một workflow restore N; không triển khai toàn cụm |
| `--reset-all --only-node N` | Từ chối; không giữ hành vi nguy hiểm |
| `--reset-all` | Reset toàn cụm; caller phải được cập nhật opt-in `--yes-reset-all` |
| `--gen-keys` | Local generation; nếu đã có bộ keys phải nêu rõ `--overwrite` |
| `--open-ports` đơn lẻ | Firewall command; giữ target cũ |
| `--bin-dir D`, `--prebuilt-bin D`, `--use-prebuilt D` | Chuẩn hóa thành `--bin-dir D` |
| `--skip-build` hoặc prebuilt không có path | Chỉ dùng thư mục mặc định đã công bố `deploy/bin`, không dò sang repo khác |
| `--all-monitors`, `--monitor-all` | Yêu cầu chuyển thành lệnh monitors riêng; không ngầm mở rộng deploy |
| `--clean`, kể cả kết hợp start | Từ chối kèm hướng dẫn reset-data hoặc reset-all tùy ý định |
| Không truyền action | Help/lỗi usage; cập nhật caller từng dựa vào default start |

Việc bỏ `--clean`, yêu cầu reset opt-in và tách monitors là breaking changes có chủ đích. Cập nhật caller trong cùng giai đoạn trước khi bật parser mới. Không âm thầm ánh xạ `--start --clean` sang reset-all vì sẽ làm thay keys/genesis.

Workflow bootstrap giữ bộ keys/genesis chuẩn bị riêng: `gen-keys` rồi `deploy` vào đích chưa có dữ liệu. Nếu đích đang có chain state khác thì dừng và yêu cầu xử lý rõ ràng, không tự xóa để tiếp tục.

## 5. Phạm vi node, dữ liệu và khả năng tiến triển

Xác định topology đầy đủ và tập node được chọn riêng biệt:

- Topology inventory dùng để hiểu peers/roles; node selector chỉ quyết định tập mutation.
- Resolve node ID thành host và node path trước các thao tác remote.
- Node không tồn tại hoặc không có service/config cần thiết phải báo lỗi, không âm thầm lọc ra rồi trả success.
- Target một node không được sinh lại keys/genesis toàn cụm, dừng node khác hoặc sửa shared storage làm mất mount của node khác.
- Kiểm tra nguồn snapshot, điều kiện node được restore và tương thích chain trước khi stop/xóa dữ liệu. Nếu metadata hiện có chưa đủ để kiểm chứng, ghi rõ thiếu gì và dừng thay vì giả định hợp lệ.
- Giữ guard cấm restore snapshot producer hiện hành cho đến khi có thiết kế thay thế được kiểm chứng.
- Start/stop/restart không được phụ thuộc vào việc controller còn lưu thư mục keys.

Deploy toàn cụm có thể gây downtime; kế hoạch này không cam kết zero-downtime. Không mặc định thêm `serial: 1` rồi coi là bảo đảm quorum: một host có thể chứa nhiều validators. Rolling upgrade cần thiết kế riêng theo voting power, tương thích phiên bản và trạng thái sync.

Deploy thất bại phải trả nonzero và nêu giai đoạn/node lỗi. Không tự reset data, đổi keys hoặc hạ binary để cố cứu deploy. Rollback binary chỉ được thực hiện sau khi xác minh tương thích format dữ liệu; rollback code Ansible không thể hoàn tác dữ liệu đã reset.

## 6. Tổ chức playbook và roles

### 6.1. Trách nhiệm từng lớp

| Thành phần | Trách nhiệm | Không được làm ngầm |
|---|---|---|
| Wrapper Bash | Parse/validate CLI, chuẩn hóa legacy, chọn workflow, in plan và trả exit status | Chạy chuỗi SSH riêng, clean data, kill monitors hoặc sinh keys ngoài workflow |
| Playbook | Resolve inventory/targets, assert preconditions, điều phối thứ tự các bước local/remote | Mở rộng targets hoặc đổi action để xử lý lỗi |
| Roles/task files | Thực hiện bước cụ thể như stop, copy, restore, start; dùng chung khi thực sự cần | Tự suy đoán action từ tổ hợp biến mặc định |
| Builder/package script | Build Go/Rust/FFI và đóng gói artifact theo hợp đồng 3.3 | Đụng inventory, service remote hoặc identity cluster |

`build` là ngoại lệ dispatch local: wrapper gọi builder trực tiếp, không cần Ansible playbook riêng. Deploy/reset-all gọi cùng builder qua task local tương ứng. Business validation inventory chỉ có một implementation dùng chung, không viết lại bằng regex trong Bash. Các assertion bảo vệ reset/restore cũng phải có trong workflow Ansible để gọi trực tiếp playbook không bỏ qua guard của wrapper.

### 6.2. Workflow và tái sử dụng

Giai đoạn đầu giữ entrypoint cùng cấp với `deploy.yml` trong `deploy/ansible/`, tránh thay đổi `playbook_dir` đồng thời với thay đổi hành vi CLI.

Tách theo workflow:

- `deploy.yml`: kiểm tra topology/artifact → chuẩn bị local → remote preflight → stop targets → cập nhật → start → kiểm tra.
- `service.yml`: start/stop/restart; dùng biến giới hạn `service_action`.
- `restore.yml`: validate một target và nguồn → stop → restore → start → kiểm tra.
- `reset_data.yml`: validate một target và storage → stop → clean dữ liệu → start.
- `reset_all.yml`: validate toàn cụm và opt-in → chuẩn bị artifact/bộ cấu hình mới → preflight → stop → reset → deploy → kiểm tra.
- `gen_keys.yml`: chỉ local generation.
- `open_ports.yml`: chỉ firewall.
- `monitors.yml`: chỉ monitors lifecycle/config.

Dùng task imports hoặc roles hiện có để chia sẻ các bước thực sự dùng chung. Không tạo role chỉ để bọc một dòng, không ép mỗi command phải có một role mới.

Điều chỉnh role hiện có:

- `local_build`: tách task build artifact khỏi task sinh keys/genesis; command nào cần bước nào gọi bước đó.
- Task package chỉ dùng artifact đã chọn; prebuilt không được chạy compiler, kể cả helper RecoveryCommittee. Build-only không gọi task sinh keys hay package cấu hình cluster.
- `systemd_services`: tách cài/copy config khỏi start service. Deploy phải stop trước khi thay binary rồi start lại; service command không chạy task cài đặt.
- `stop_services`: chỉ dừng node đích; chuyển logic stop monitors ra workflow riêng.
- `node_setup`, `clean_data`, `snapshot_restore`: kiểm tra phạm vi node và tài nguyên dùng chung.
- Preflight snapshot storage phải giữ thứ tự trước stop/clean.
- Giữ các bước node_exporter, firewall và monitoring config có chủ đích; không bỏ sót khi tách deploy.yml.

Nếu sau này đưa entrypoint vào `playbooks/`, thực hiện ở thay đổi riêng: sửa project root, script/template paths, roles_path, filter_plugins và kiểm tra việc load group_vars/hostvars. Wrapper phải tìm cấu hình ổn định khi chạy từ cwd bất kỳ.

## 7. Inventory và rpc_nodes.json

Mục tiêu là thống nhất cách hiểu inventory, không phải xóa Python bằng mọi giá.

- Dùng dữ liệu inventory đã được Ansible resolve cho workflow Ansible; giữ hỗ trợ Vault.
- Giữ `parse_inventory.py` đến khi đã chuyển hết caller, validation và kiểm thử schema.
- Không thay parser bằng template chỉ lọc RPC/SyncOnly.
- Chuẩn hóa node ID, host-level flags và danh sách per-node trong một nơi dùng chung khi cần; chỉ thêm custom filter nếu logic thực sự khó biểu diễn/kiểm thử bằng task thông thường.
- Giữ validation duplicate node ID, role flags và cấu hình local connection, đồng thời xét môi trường controller local hợp lệ.
- Giữ preflight kết nối cho CI bằng đường gọi thay thế rõ ràng; thiếu helper phải báo lỗi thay vì bỏ qua kiểm tra.

Hợp đồng JSON cần giữ:

| Trường | Nội dung |
|---|---|
| `nodes` | Endpoint của toàn bộ nodes |
| `roles` | Vai trò tương ứng của từng node |
| `state_history_nodes`, `rpc_nodes`, `ws_nodes` | Tập endpoint theo RPC capability hiện hành |
| `tcp_nodes` | Endpoint TCP theo node |
| `ssh` | Metadata SSH theo schema hiện hành; không thêm mật khẩu/token |

Giữ key `m<N>`, quy tắc port và quyền `0600` trong giai đoạn tương thích. Ghi atomic và không export secrets từ hostvars. Khi targeted deploy, file topology vẫn mô tả toàn inventory, không chỉ node đích.

`/tmp/rpc_nodes.json` là đường dẫn dùng chung giữa nhiều consumer: chưa đổi trong giai đoạn đầu, ghi rõ giới hạn một cluster context tại một thời điểm. Chỉ chuyển sang output riêng theo inventory sau khi CI/suite/monitors nhận được đường dẫn cấu hình tương ứng.

## 8. Monitors

Chuyển lifecycle sang systemd ở giai đoạn riêng, giữ source tại vị trí hiện có trước.

- Xác định monitoring hosts riêng; `--scope all` là toàn nhóm monitoring, không suy ra mọi node host.
- Build binary một lần trên controller cho kiến trúc đích được hỗ trợ, kiểm tra kiến trúc trước khi phân phối.
- Khai báo user, working directory, config path, log/state path và restart policy trong unit.
- `monitors deploy` cài đặt/cập nhật unit và binary; start/stop/restart chỉ thay trạng thái service.
- Cấu hình `monitors_enabled` quyết định autostart; mặc định false để không tự bật lúc reboot trong quá trình migration.
- Node deploy không kill monitors toàn cụm; nếu cần suppress cảnh báo, thiết kế maintenance scope rõ ràng.
- Migration tiến trình cũ phải xác định PID/owner/path chính xác và bảo đảm chỉ một instance chạy; không xóa toàn bộ process cleanup trước khi xử lý tiến trình legacy.
- Khi config/binary đổi, restart service có chủ đích; `state: started` đơn thuần không đủ để áp dụng cập nhật.
- Giữ compatibility entrypoint cho caller cũ hoặc cập nhật hết caller trước khi bỏ `start_monitors.sh`.

Di chuyển source sang `tools/monitors/` là thay đổi tùy chọn sau cùng. Trước khi làm phải kiểm tra go.mod, relative paths, dashboard/static assets, scripts và consumers. Không coi việc đổi thư mục là điều kiện để sửa CLI.

## 9. Blast radius và caller phải cập nhật

| Thành phần | Nội dung cần rà soát/cập nhật |
|---|---|
| `deploy/ansible/ansible_deploy.sh` | Parser, adapter legacy, validation, dispatch, notification, exit code |
| `deploy/systemd/build_release.sh` và callers | Tách build/package, output chuẩn, metadata, publish artifact và tương thích tarball |
| `deploy/ansible/deploy.yml`, roles, group_vars, ansible.cfg | Workflow và đường dẫn; giới hạn tác động |
| `deploy/ansible/parse_inventory.py`, `check_snapshot_node.py` | Validation/schema/Vault và callers |
| `deploy/ansible/auto_rebuild_deploy.sh` | Default action và `--start --fast` |
| `deploy/ci/ci_config.yaml`, `ci_runner.py` | reset/restart command, preflight |
| `deploy/ci/prepare_tps_chain.sh`, `verify_multi_reset_stability.sh` | Reset opt-in và semantics |
| `deploy/test/test_remote_deploy.sh` | gen-keys, prebuilt, start/clean bootstrap |
| `metanode-suite/scripts/` và các caller khác trong suite | CLI, rpc_nodes.json, monitor entrypoints |
| Runbook, README và `PROJECT_STRUCTURE.md` | Cú pháp mới, migration, module map khi thay cấu trúc |

Khi bắt đầu implementation, tìm lại toàn repo và suite vì danh sách trên không thay thế impact analysis. Không thay đổi public CLI và private-chain CLI cùng một patch; ghi rõ sự khác nhau trong tài liệu.

## 10. Thứ tự thực hiện và tiêu chí hoàn thành

Các giai đoạn là thứ tự phát triển, không mặc nhiên là các bản phát hành độc lập. Chỉ bật một command khi parser, workflow, caller migration và kiểm chứng hành vi của chính command đó đã hoàn tất. Không nối `start` mới vào luồng `start` cũ còn build/copy. Nếu workflow chưa sẵn sàng thì command phải trả lỗi rõ ràng trước side effect; không fallback sang action khác. Thay đổi caller chỉ phát hành cùng command đã sẵn sàng.

### Giai đoạn 1 — CLI và compatibility

- Tách parse/validate khỏi execution để kiểm thử không chạm inventory thật hoặc remote.
- Implement command model và validation theo bảng; thêm legacy adapter.
- Cập nhật caller bị breaking change cùng đợt; rà soát cả lời gọi không có action.
- Chưa đổi thư mục source monitors.
- Hoàn thành phần parser khi options sau command không phụ thuộc thứ tự và mọi caller đã biết có mapping hoặc lỗi migration rõ ràng; chưa đủ điều kiện phát hành command nếu giai đoạn workflow tương ứng chưa xong.

### Giai đoạn 2 — Workflow và phạm vi mutation

- Tách service actions khỏi deploy/build.
- Hoàn thiện build-only, output artifact và nhánh prebuilt theo mục 3.3; dùng chung builder, không compile hai lần trong cùng deploy.
- Tách restore/reset theo đúng phạm vi và giữ preflight trước thao tác phá hủy.
- Không dừng monitors hoặc rewrite keys ngoài action tương ứng.
- Hoàn thành khi task selection xác nhận command chỉ chạy các bước được phép.

### Giai đoạn 3 — Inventory và compatibility JSON

- Dùng resolved inventory, chuyển validation/caller từng bước.
- Kiểm tra schema JSON và endpoint parity bằng fixtures.
- Chỉ xóa parser cũ khi không còn caller và preflight thay thế đã được kiểm chứng.

### Giai đoạn 4 — Systemd monitors

- Chuyển lifecycle, config, migration tiến trình legacy và caller.
- Kiểm chứng stop/start/restart cùng behavior khi binary/config đổi.
- Cập nhật PROJECT_STRUCTURE.md khi thêm entrypoint/role quan trọng.

### Giai đoạn 5 — Dọn cấu trúc tùy chọn

Chỉ di chuyển source hoặc playbooks khi có lợi ích rõ ràng sau các giai đoạn trên. Không gộp thêm thay đổi hành vi.

## 11. Kế hoạch kiểm chứng

### Kiểm chứng tự động không triển khai remote

- `bash -n` và ShellCheck cho scripts thay đổi nếu công cụ có sẵn.
- Ansible syntax check cho entrypoints với fixture inventory; không load secrets thật.
- Test parser/dispatcher bằng stub để kiểm tra argv và chắc chắn invalid/dry-run không gọi build, SSH, Telegram hoặc mutation.
- Kiểm tra task selection và đường dẫn từ repo root, thư mục Ansible và cwd ngoài repo.
- Kiểm tra parser inventory/JSON bằng fixture; không cần chạy full CI để chứng minh syntax hoặc mapping flags.
- Sau khi sửa code, chạy `consensus/metanode/scripts/build_check.sh` theo AGENTS.md và xử lý lỗi/cảnh báo; nếu môi trường chặn thì báo rõ phần chưa xác minh. Chỉ sửa tài liệu không cần build.

Ma trận tối thiểu:

| Trường hợp | Kết quả yêu cầu |
|---|---|
| Không action, nhiều action, thiếu option value | Lỗi usage trước side effect |
| Option lặp, option sai command, trộn `deploy --start` | Lỗi usage; không chọn giá trị/action cuối |
| Options trước command mới hoặc ở sai vị trí monitors subcommand | Lỗi usage, hướng dẫn cú pháp chuẩn |
| Flag hợp lệ ở các thứ tự khác nhau | Cùng action/targets/options |
| Build không có inventory/keys; build có `--inventory` hoặc `--node` | Trường hợp đầu vẫn build local; trường hợp sau bị từ chối |
| Build-only | Không Ansible deploy/SSH/Vault/Telegram/gen-keys; không ghi deployed commit |
| Build mặc định và `--fast --debug-cpp` | Profile/options đúng trong lệnh build và metadata |
| Build lỗi/bị ngắt; build và deploy đọc output đồng thời | Không công bố hoặc đóng gói bộ binary trộn giữa hai lần build |
| Deploy prebuilt, kể cả thiếu helper | Không gọi compiler; thiếu thành phần bắt buộc thì báo lỗi trước stop |
| Artifact có checksum sai hoặc thiếu metadata legacy | Checksum sai bị chặn; legacy ghi unknown, không suy đoán provenance |
| Gọi trực tiếp reset/restore playbook với target/opt-in sai | Assertion chặn trước mutation |
| Command có parser nhưng workflow chưa sẵn sàng | Báo chưa hỗ trợ, không gọi workflow cũ có tác động khác |
| `--clean` ở mọi vị trí | Cùng lỗi migration, không vô tình giữ/xóa data |
| Target không tồn tại hoặc duplicate ID | Lỗi rõ ràng |
| Restore thiếu URL, target xung đột, snapshot producer | Bị chặn |
| Reset-all có node selector hoặc thiếu opt-in | Bị chặn |
| Start/restart/stop | Không build/copy/gen-keys/clean/firewall |
| Gen-keys đã tồn tại, không overwrite | Bị chặn |
| Prebuilt thiếu artifact hoặc build flag xung đột | Bị chặn trước stop |
| Target một node cùng host với node khác | Chỉ node đích bị mutation |
| Local keys thiếu khi service remote đã cài | Service command vẫn chọn đúng target |
| Inventory có host/per-node role flags, Vault, custom SSH port | Resolve đúng, không leak secrets |
| Targeted deploy xuất JSON | Topology đầy đủ và schema đúng |
| Dry-run | Không mutation, không remote/notification |
| Ansible/build lỗi | Exit nonzero; không báo deploy thành công |
| Chạy stop/open-ports/restart | Không ghi provenance như vừa deploy binary |

### Nghiệm thu tích hợp do người vận hành chạy

Trên cluster test dùng dữ liệu có thể bỏ, chọn riêng từng workflow:

1. Bootstrap bằng bộ keys chuẩn bị trước, deploy và đối chiếu identity/genesis.
2. Deploy lại một node, xác nhận không đổi keys/genesis và không ảnh hưởng node khác.
3. Service start/stop/restart một node; xác nhận không build hoặc copy.
4. Restore một node và reset-data một node; kiểm tra peers/những node cùng host không bị reset.
5. Reset-all với opt-in; kiểm tra custom alloc và chính sách RecoveryCommittee được giữ theo hợp đồng.
6. Cố ý gây lỗi artifact/preflight: node chưa bị dừng hoặc xóa dữ liệu.
7. Kiểm tra service readiness và tiến triển block; so sánh hash tại cùng height sau khi sync, không suy ra an toàn chỉ từ process status.
8. Chạy các case CI/suite liên quan với inventory test rõ ràng; không dùng `./ci.sh run-now` không giới hạn như một bước kiểm tra vô hại.
9. Kiểm tra migration monitors không tạo instance kép và caller legacy được xử lý.
10. Build một bộ artifact, kiểm tra metadata rồi deploy prebuilt bộ đó; xác nhận không compile lại, binary đích khớp checksum và tarball/layout cũ vẫn dùng được.

Chỉ công bố hoàn thành implementation khi phân biệt rõ: kiểm tra tĩnh đã pass, build đã pass và những workflow runtime nào đã được người vận hành nghiệm thu.
