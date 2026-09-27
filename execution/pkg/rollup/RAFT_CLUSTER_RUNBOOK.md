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

## Thêm replica (trạng thái KHÔNG được truyền qua mạng: snapshot Raft chỉ có bộ đếm)
1. Dừng **một** peer khoẻ (cụm vẫn phải còn quorum), `rollup-cluster prepare-replica --from-data <root_path của peer> --to-data <root_path mới> --from-admin <cổng nội bộ của peer> --secret-file …` (từ chối nếu peer còn trả lời hoặc đích không rỗng; `cp -a --reflink=auto`, không chép thư mục Raft của peer). Khởi động lại peer.
2. Khởi động replica mới với `join_existing_chain=true` và thư mục Raft riêng (rỗng).
3. `… add-replica --id n3 --raft-addr h:7103 --admin-addr h:7203 [--dry-run]`: thêm như **non-voter** → đợi tới khi nó áp dụng trong 64 entry của commit index **và thực thi** trong 64 block của leader → so hash block tip của nó với leader (khác ⇒ từ chối: trạng thái không phải của chuỗi này) → nâng lên voter.
- Replica **rỗng** (không sao chép) chỉ vào được khi log của leader còn đủ từ entry 1 (chưa bị thu gọn): nó tự phát lại toàn bộ. Nếu log đã thu gọn (`first_log_index > 1` và snapshot phủ block cao hơn DB của nó) công cụ **từ chối**; ép thêm ở mức node thì replica **tự dừng** (Restore từ chối vì DB chưa có block snapshot nói) và cụm không bị ảnh hưởng.
- Bắt đầu với state Raft rỗng mà DB đã có block **mà không** đặt `join_existing_chain` ⇒ node từ chối khởi động.

## Thay replica chết vĩnh viễn
`remove-replica --id n1` **trước** (được phép khi voter sống còn ≥ đa số của cụm sau khi bỏ; nếu không ⇒ từ chối "mất quorum"), rồi thêm replica mới như trên. Thứ tự này giữ quorum khi dừng một peer để sao chép. Xoá leader cần `--transfer-first`. Đo thật: giết leader n1 giữa tải (24 000 tx), xoá n1, sao chép từ n3, thêm n4 ⇒ 4 replica cùng 152 000 tx, cùng hash/stateRoot.

## Khoá ký (mã hoá tại chỗ)
Mọi replica dùng **cùng** khoá (`private_key`, địa chỉ = `raft.sequencer_address`, node tự thoát khi lệch). `check` báo `key_consistent=false` nếu các replica báo địa chỉ ký khác nhau. Khoá không đi qua mạng (mỗi node đọc từ cấu hình cục bộ).

**Không để khoá dạng rõ trong `config.json`:** `pkg/keyvault` (scrypt N=2^15 + AES-256-GCM) lưu mỗi bí mật thành chuỗi `enc:v1:…`; node giải mã **một lần lúc khởi động, trong bộ nhớ**. Mật khẩu **không** nằm trong `config.json`: lấy từ biến môi trường `META_KEY_PASSWORD`, hoặc từ file (mode 0600, trường `key_password_file` hoặc `META_KEY_PASSWORD_FILE`; file mở cho group/others bị từ chối). Không có mật khẩu / sai mật khẩu ⇒ node **không khởi động** (không chạy với khoá hỏng).
- Mã hoá cả file: `encrypt_secret config -in config.json -out config.enc.json -password-file /etc/metanode/keypw [-require]` (cmd/tool/encrypt_secret) — mã hoá mọi bí mật còn dạng rõ (`private_key`, `Databases.BLSPrivateKey`, `gateway_bls_key`, `reward_sender_private_key`, `securepassword`, `master_password`, `app_pepper`, `pk_admin_file_storage`, `bls_admin_storage`, `cross_chain.root_anchor_submitter_private_key_hex`), không ghi đè file có sẵn, bí mật đọc từ stdin (không vào lịch sử shell). `-require` đặt `require_encrypted_keys=true`: node từ chối khởi động nếu còn bí mật dạng rõ.
- Từng giá trị: `printf '%s' "$KEY" | encrypt_secret encrypt -password-file pw` → dán chuỗi `enc:v1:…` vào trường; kiểm bằng `decrypt`. Biến `META_PRIVATE_KEY`… cũng có thể mang chuỗi `enc:v1:…`.
- Cùng một khoá trên mọi replica ⇒ có thể dùng cùng mật khẩu hoặc mỗi node một mật khẩu (mỗi bản mã có salt/nonce riêng). **Giới hạn:** bảo vệ khoá khi nghỉ và trong bản sao lưu cấu hình; kẻ đọc được cả nguồn mật khẩu lẫn cấu hình trên máy đang chạy vẫn lấy được khoá. Đặt file mật khẩu ở nơi khác `config.json`/sao lưu (ví dụ `/etc`, quyền 0600, hoặc trình quản lý bí mật đẩy vào biến môi trường).

## Giới hạn
- Một máy thử: chưa chạy trên nhiều máy thật. `add-replica` cần sao chép thư mục dữ liệu ngoài băng (ổ btrfs/xfs có reflink cho nhanh).
- `state_root_consistent` là so hash header (đã gồm các state root) tại block checkpoint chung, không so từng state root riêng.
