# Vận hành cụm `raft` (C4): đổi leader, thêm / thay replica

Công cụ: `execution/cmd/tool/rollup_cluster` (`go build -o rollup-cluster ./cmd/tool/rollup_cluster`). Mọi lệnh đổi trạng thái có `--dry-run` (in các bước, không đổi gì) và **từ chối chạy** khi điều kiện an toàn không thoả. Kênh điều khiển là cổng nội bộ (`forward_bind_address`) có HMAC dùng chung `forward_secret_file`; mỗi thao tác được **kiểm lại ở phía node** nên client bỏ qua bước kiểm cũng không phá được cụm.

`cluster.json`: `{"secret_file": "…/raft_forward.key", "members": [{"id":"n0","raft_addr":"h:7100","admin_addr":"h:7200"}, …]}`

## Cấu hình cần có
- `raft.forward_port_offset` (ví dụ 100): cổng nội bộ của mọi thành viên = cổng Raft + offset, nên replica thêm sau **không** cần sửa cấu hình của node cũ. Không đặt thì chỉ các peer khai báo sẵn trong `peers[].forward_address` được biết.
- Replica thêm sau: `raft.join_existing_chain=true` (không `bootstrap`), **cùng** `private_key`/`sequencer_address` (cùng khoá ký) và cùng secret.

## `check` (chỉ đọc)
`rollup-cluster -config cluster.json check` → JSON: leader/term, từng replica (voter?, `applied_index`, `lag_entries`, `last_block`, `lag_blocks`, failed, draining), `checkpoint_block`, `state_root_consistent` (so hash header block checkpoint giữa các replica), `key_consistent` (cùng địa chỉ ký), `quorum_ok`, `problems`. Thoát mã 3 nếu có vấn đề.

## Chuyển leader chủ động
`… transfer-leader --to n1 [--dry-run]`. Điều kiện: đích là voter, sống, không `failed`, chênh ≤ 64 entry **và** ≤ 64 block đã thực thi; cụm còn quorum. Các bước: drain leader cũ (ngừng nhận batch mới, đợi mọi thứ đã nhận được áp dụng) → chuyển → đợi đích thành leader → leader cũ chạy tiếp như follower (`Submit` của nó chuyển tiếp). Đo thật: chuyển giữa tải 36 000 tx, không mất tx nào.

## Thêm replica (trạng thái không nằm trong snapshot Raft — snapshot Raft chỉ có bộ đếm)
Thứ tự an toàn (mọi bước là lệnh của `rollup-cluster`, trừ bước khởi động replica):
0. `hold-snapshots --on on` — tạm dừng **snapshot tự động / thu gọn log** trên mọi thành viên (tự nhả sau 30 phút). Cần vì leader chỉ có thể phục vụ replica mới bằng log **sau snapshot mới nhất**: state sao chép phải mới hơn snapshot đó, nếu snapshot đi trước trong lúc bạn sao chép thì replica mới bị từ chối (fail closed) và phải làm lại.
1. Lấy state, **một trong hai**:
   - **`fetch-state --from <cổng nội bộ của một replica ĐANG CHẠY> --to-data <root_path mới>`** (không cần dừng peer): donor tạm dừng thực thi trong thời gian ngắn để chụp snapshot nguyên tử (Pebble checkpoint + NOMT native snapshot + changelog + blob store + Xapian), tiếp tục chạy rồi băm từng file; manifest có MAC của donor; tệp thưa (NOMT: hàng chục GB logic, vài trăm MB dữ liệu) chỉ truyền các đoạn có dữ liệu và ghi ra thưa; mỗi file được kiểm hash trước khi đặt vào chỗ, chạy lại được sau gián đoạn (file đúng hash được giữ, file thừa của lần cũ bị xoá). Donor **từ chối** nếu snapshot thiếu một thư mục cơ sở dữ liệu mà root đang chạy có, hoặc nếu bố cục lạ (không im lặng bỏ dữ liệu).
     - **Ổ đĩa:** cần filesystem hỗ trợ **reflink** (btrfs/xfs) để tạm dừng chỉ vài trăm ms; không có reflink thì donor **từ chối** trừ khi đặt `raft.state_transfer_allow_copy=true` (khi đó snapshot là bản sao đầy đủ và thực thi bị dừng suốt thời gian sao chép — đo trên ext4 với DB thử nghiệm: ~0,65 s dừng, 3 s toàn bộ). Thư mục tạm là `<root_path>.state_transfer` (cùng filesystem với root).
   - hoặc dừng một peer khoẻ (cụm vẫn còn quorum), `prepare-replica --from-data … --to-data … --from-admin … --secret-file …` (từ chối nếu peer còn trả lời hoặc đích không rỗng), khởi động lại peer.
2. Khởi động replica mới với `join_existing_chain=true` và thư mục Raft riêng (rỗng), **id mới**.
3. `add-replica --id n3 --raft-addr h:7103 --admin-addr h:7203 [--dry-run]`: thêm như **non-voter** → đợi tới khi nó áp dụng trong 64 entry của commit index **và thực thi** trong 64 block của leader → so hash block tip với leader (khác ⇒ từ chối: trạng thái không phải của chuỗi này) → nâng lên voter.
4. `hold-snapshots --on off`.
- Replica **rỗng** không có state chỉ vào được khi log của leader còn đủ từ entry 1; nếu log đã thu gọn thì công cụ **từ chối** và gợi ý các bước trên (ép ở mức node ⇒ replica tự dừng: Restore từ chối vì DB chưa có block snapshot nói).
- Bắt đầu với state Raft rỗng mà DB đã có block **mà không** đặt `join_existing_chain` ⇒ node từ chối khởi động.
- Replica tắt lâu hơn cửa sổ `trailing_logs` không tự bắt kịp: khi bật lại, leader gửi snapshot mới hơn DB của nó và replica **tự dừng** (fail closed). Dựng lại theo mục "Dựng lại replica" — không phát lại từ log.

## Dựng lại replica (tụt hậu quá cửa sổ log, hỏng dữ liệu…)
**Không** xoá thư mục Raft rồi dùng lại id cũ: replica quên lịch sử phiếu bầu/term của mình có thể bỏ phiếu lần thứ hai trong cùng term và phá tính an toàn của Raft. Cách đúng: `remove-replica --id <cũ>` rồi làm đúng quy trình "Thêm replica" với **id mới**. Đã chạy thật: replica dựng từ state truyền qua mạng bị kill, sau đó tụt quá cửa sổ log → tự dừng đúng thiết kế → xoá + thêm `n8` bằng `fetch-state` ⇒ 4 replica cùng 348 000 tx, cùng hash/stateRoot.

## Thay replica chết vĩnh viễn
`remove-replica --id n1` **trước** (được phép khi voter sống còn ≥ đa số của cụm sau khi bỏ; nếu không ⇒ từ chối "mất quorum"), rồi thêm replica mới như trên. Thứ tự này giữ quorum khi dừng một peer để sao chép. Xoá leader cần `--transfer-first`. Đo thật: giết leader n1 giữa tải (24 000 tx), xoá n1, sao chép từ n3, thêm n4 ⇒ 4 replica cùng 152 000 tx, cùng hash/stateRoot.

## Khoá ký (mã hoá tại chỗ)
Mọi replica dùng **cùng** khoá (`private_key`, địa chỉ = `raft.sequencer_address`, node tự thoát khi lệch). `check` báo `key_consistent=false` nếu các replica báo địa chỉ ký khác nhau. Khoá không đi qua mạng (mỗi node đọc từ cấu hình cục bộ).

**Không để khoá dạng rõ trong `config.json`:** `pkg/keyvault` (scrypt N=2^15 + AES-256-GCM) lưu mỗi bí mật thành chuỗi `enc:v1:…`; node giải mã **một lần lúc khởi động, trong bộ nhớ**. Mật khẩu **không** nằm trong `config.json`: lấy từ biến môi trường `META_KEY_PASSWORD`, hoặc từ file (mode 0600, trường `key_password_file` hoặc `META_KEY_PASSWORD_FILE`; file mở cho group/others bị từ chối). Không có mật khẩu / sai mật khẩu ⇒ node **không khởi động** (không chạy với khoá hỏng).
- Mã hoá cả file: `encrypt_secret config -in config.json -out config.enc.json -password-file /etc/metanode/keypw [-require]` (cmd/tool/encrypt_secret) — mã hoá mọi bí mật còn dạng rõ (`private_key`, `Databases.BLSPrivateKey`, `gateway_bls_key`, `reward_sender_private_key`, `securepassword`, `master_password`, `app_pepper`, `pk_admin_file_storage`, `bls_admin_storage`, `cross_chain.root_anchor_submitter_private_key_hex`), không ghi đè file có sẵn, bí mật đọc từ stdin (không vào lịch sử shell). `-require` đặt `require_encrypted_keys=true`: node từ chối khởi động nếu còn bí mật dạng rõ.
- Từng giá trị: `printf '%s' "$KEY" | encrypt_secret encrypt -password-file pw` → dán chuỗi `enc:v1:…` vào trường; kiểm bằng `decrypt`. Biến `META_PRIVATE_KEY`… cũng có thể mang chuỗi `enc:v1:…`.
- Cùng một khoá trên mọi replica ⇒ có thể dùng cùng mật khẩu hoặc mỗi node một mật khẩu (mỗi bản mã có salt/nonce riêng). **Giới hạn:** bảo vệ khoá khi nghỉ và trong bản sao lưu cấu hình; kẻ đọc được cả nguồn mật khẩu lẫn cấu hình trên máy đang chạy vẫn lấy được khoá. Đặt file mật khẩu ở nơi khác `config.json`/sao lưu (ví dụ `/etc`, quyền 0600, hoặc trình quản lý bí mật đẩy vào biến môi trường).

## Giới hạn
- Một máy thử: chưa chạy trên nhiều máy thật; `fetch-state` chưa thử trên btrfs/xfs (reflink) vì máy thử không có quyền ghi vào phân vùng btrfs — đường không-reflink (`state_transfer_allow_copy`) đã chạy thật, đường reflink mới có kiểm tra phát hiện và từ chối/chấp nhận. Kênh nội bộ là HTTP thường có HMAC (xác thực, không mã hoá): dùng trong mạng tin cậy.
- `state_root_consistent` là so hash header (đã gồm các state root) tại block checkpoint chung, không so từng state root riêng.
