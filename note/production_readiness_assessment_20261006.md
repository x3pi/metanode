# Đánh giá sẵn sàng production — account gate + co-attestation (2026-10-06)

**Kết luận: chưa production-ready cho mạng thật; đủ điều kiện cho testnet/pilot nhiều máy có giám sát.** Lý do nằm ở mục "Chặn" bên dưới.

## Đã kiểm chứng (output thật, worktree sạch, binary rebuild, commit `1081ba60`)
| Hạng mục | Kết quả |
|---|---|
| Account gate E2E (secp + parent_registered, 2 cụm, replay, race, forged event) | 14/14 PASS |
| Độ bền hàng đợi đăng ký, kill -9 (có/không parent) | PASS (3 lần trước + 1 lần sau chuyển proto) |
| Co-attestation đăng ký, cụm 4 validator Mysticeti (A–E: quorum, 1 node down, 2 node down không fork, validator giả, kill -9, state root từng block) | 23/23 PASS |
| Credit cross-chain thật qua envelope (A–F, tombstone, double-credit, validator giả, fail-closed) | 26/26 PASS (2 lần trước + 1 lần sau chuyển proto) |
| `go test -race` (rollup, tx_processor, simple_chain), `build_check.sh` | sạch |
| Wire format | proto, giải mã yêu cầu độ dài chính xác (mutation-checked) |

## Lỗi đã tìm và sửa trong đợt review
1. Tombstone ghi trước dispatch ⇒ sự kiện bị từ chối mất vĩnh viễn (`b097049d`).
2. Fail-open khi đọc committee lỗi (`b097049d`).
3. Digest attestation dùng chain ID 991 cứng (`f9abb586`).
4. **NOMT vendor bị tắt toàn bộ fsync** (sao từ checkout cargo sửa tay) — khôi phục (`bd71000a`).
5. Decoder proto zero-pad/cắt ngắn trường độ dài cố định ⇒ malleability (`1081ba60`).
6. Script test: reuse binary cũ, pkill theo mẫu, đường dẫn scratch lạ.

## CHẶN — phải xong trước khi lên mạng thật
- **B1. Hiệu năng sau thay đổi NOMT/commit chưa đo.** `NomtStateTrie.Commit` chờ `commitWg` trước mỗi session mới, `Session.Finish` giữ lock, fsync NOMT đã trở lại. Cần benchmark tải (mốc cũ ~6400 tx/s, `note/` về sig-enforcement) và đo độ trễ commit; nếu tụt đáng kể thì tối ưu có kiểm soát (không tắt fsync).
- **B2. Chưa chạy trên nhiều máy thật.** Mọi E2E là 1 máy, cổng loopback. Cần thử mạng thật (độ trễ, mất gói, tắt máy) trên cụm test riêng (không phải 231/230 trừ khi user cho phép).
- **B3. Đổi định dạng payload + đổi chain ID 991 ⇒ cutover đồng thời toàn mạng.** Chưa có runbook (P1-1) và chưa thử cutover trên cụm cô lập từ template ansible thật (P1-2).
- **B4. Điều kiện cấu hình validator:** `PublicKeyBls` của địa chỉ validator PHẢI = pub(`Databases.BLSPrivateKey`), nếu không đăng ký/credit PENDING mãi (an toàn nhưng treo). Đã có log `[COMMITTEE-KEY-*]` nhưng chưa có metric/alert, chưa có kiểm tra ở bước deploy ansible.
- **B5. P0-4 chưa đóng hẳn:** #104 mới có chuỗi vault giả trong inventory mẫu; #103 chưa kiểm trên deploy thật (template chỉ bật bypass khi `skip_mempool_sig_verify=true`, đã thêm `METANODE_ENV=production`).

## Rủi ro còn lại (chấp nhận được nếu ghi rõ, nên xử lý sớm)
- `GetAllValidators` (top 21) gọi trong lúc thực thi tx hệ thống; phụ thuộc NOMT, đã fail-closed khi lỗi.
- Hàng đợi/attestation lưu trong contract storage của `RollupSystemAddress` (không nằm trong state root header; lệch sẽ lộ qua cờ `ParentRegistered`/số dư, không lộ trực tiếp).
- Thiếu metric: PENDING age, attestation đang chờ, chữ ký bị từ chối, lệch chain ID.
- Legacy chain (`bls_legacy`) chưa có chain-binding cho tx không phải 0xFF (P1-3); MVM `creatorPublicKey` cho deploy từ tài khoản secp chưa xác nhận bằng test deploy thật (P1-4).
- Tải: chưa thử 10k đăng ký đồng thời, parent mất 10 phút, restart giữa tải (P1-6).
- Các commit chưa push; `portal/` có file sửa dở của người khác.

## Thứ tự đề xuất
B1 (benchmark) → B4 (kiểm tra khoá ở deploy + metric) → B3 (runbook + thử cutover từ template ansible) → B2 (cụm nhiều máy riêng) → B5 → P1-3/4/5/6.


## Review đợt 2026-10-06 (commit 4f1b2feb, fc4c2b3c, 456538a0 của agent khác) — đã sửa
1. **Pre-flight khóa BLS là mã chết:** tool `bls_pubkey` không được build/phân phối ở bất kỳ role nào nên bước kiểm tra luôn bị bỏ qua; ngoài ra khóa riêng nằm trên dòng lệnh và log ansible. Đã chuyển sang chạy trên controller với tool do role `build` dựng, khóa qua stdin + `no_log`, FAIL khi thiếu tool/`bls_pubkey`. Kiểm bằng ansible thật: khớp ⇒ pass; lệch ⇒ fail; `-vvv` không lộ khóa (0 lần xuất hiện); thiếu `bls_pubkey` ⇒ fail. (Genesis từng rơi về dùng chính khóa riêng làm public key khi thiếu `bls_pubkey`.)
2. **Kiểm tra định kỳ gọi `GetAllValidators` mỗi 30 giây** từ goroutine timer — đúng kiểu duyệt NOMT mà `chain_state.go` cảnh báo sẽ race với commit nền. Thêm `VerifyNodeCommitteeKeyLight` (chỉ tra cứu điểm), lỗi đọc thì giữ trạng thái cũ; kiểm đúng chuẩn đầy đủ chỉ chạy lúc khởi động.
3. **Gauge pending attestation trôi** (cập nhật trong đường thực thi, có thể chạy lặp khi speculative, về 0 khi restart) ⇒ thay bằng 2 counter `..._sets_stored_total` / `..._sets_completed_total`.
4. **Committee đếm trùng khóa:** các validator chung một khóa BLS (kiểu ansible hiện tại) làm phình N và ngưỡng f+1 tới mức không bao giờ đạt. Provider nay đếm khóa phân biệt (test + mutation).
5. **Điều kiện chặn bí mật devnet** dùng `or` nên chặn cả triển khai devnet khi chỉ một trong hai biến là devnet; đổi thành "production trừ khi khai báo devnet" (kiểm 5 trường hợp bằng ansible thật).
6. **Runbook cutover** dùng tên unit/đường dẫn/cổng/playbook/message proto không tồn tại (`metanode-exec-cluster`, `/opt/metanode/exec/data`, `site.yml`, cổng 8545, `bls_pubkey -priv`, `CreditAttestationRequest`…) — nguy hiểm vì chứa lệnh `rm -rf`. Đã viết lại theo `deploy_clusters.sh`/inventory/`backend.go` thật; vẫn CHƯA diễn tập.
7. `deploy_clusters.sh`: nhánh dự phòng đọc mật khẩu bỏ qua giá trị Jinja `{{ ... }}`. `reset_clusters.sh`: bỏ mật khẩu sudo ghi cứng, yêu cầu biến môi trường `SUDO_PASS`.
- Kiểm trực tiếp trên node thật (cụm cô lập): `/health` `committee_key=ok`, `/readiness` 200, `master_validator_committee_key_valid 1`; cố ý đặt sai `Databases.BLSPrivateKey` ⇒ `committee_key=mismatch`, `/readiness` 503, metric 0.
- **Còn mở (cần user):** mật khẩu sudo dev `1234@abcd` vẫn xuất hiện ở nhiều file được theo dõi (`OPERATIONS_GUIDE.md`, `consensus/metanode/scripts/node/*`, `execution/cmd/tool/tps_blast/*`, `migrate-to-btrfs-lvm.sh`) và trong lịch sử git — cần đổi mật khẩu ở máy thật và quyết định có làm sạch lịch sử không.
