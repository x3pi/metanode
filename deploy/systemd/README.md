# 🚀 Metanode `deploy/systemd/` — Index & Quickstart

Đây là thư mục script cho triển khai devnet/production qua systemd (chạy trực tiếp process,
không dùng Ansible — xem `deploy/ansible/` cho triển khai nhiều máy tự động qua playbook).

**Hướng dẫn đầy đủ, luôn coi là nguồn chuẩn** (tài liệu này chỉ là index, không lặp lại nội
dung của 2 tài liệu sau):
- **Từng bước, có xác thực từng bước**: `note/deployment_runbook_step_by_step.md`
- **Tài liệu tham chiếu đầy đủ** (kiến trúc, các bug đã vá, checklist production):
  `note/production_deployment_guide.md`

⚠️ Mọi đường dẫn ví dụ trong các script/log dưới đây là tương đối tới thư mục này
(`deploy/systemd/`) — không hardcode theo máy cụ thể nào.

---

## 1. Luồng hiện tại (khuyến nghị) — Root Anchor + 4 Private Chains + Relayer

```bash
pip3 install web3 eth-account eth-keys --break-system-packages   # 1 lần

bash setup_root_anchor.sh --clean          # 4-validator Root Anchor (chain 9099)
bash setup_4_private_chains.sh --clean     # chain 101/102/103/104
bash register_private_chains_t2.sh         # đăng ký chéo qua bootstrapFoundingChains()
bash start_relayer_daemon.sh               # RelayerDaemon tự động attest/claim
```

Dừng: `bash stop.sh` (dừng cả private chains + root anchor cũ trên máy này).

## 2. Script index — script nào dùng, script nào KHÔNG dùng

| Script | Vai trò | Ghi chú |
|---|---|---|
| `setup_root_anchor.sh` | Dựng cụm 4-validator Root Anchor thật | Luồng hiện tại, dùng đầu tiên |
| `setup_4_private_chains.sh` | Dựng 4 private chain (101-104) | Luồng hiện tại |
| `setup_2_private_chains.sh` | Dựng 2 private chain (101-102) | Biến thể nhẹ hơn cho smoke test nhanh, không dùng Root Anchor/cross-chain — không phải luồng chính |
| `register_private_chains_t2.sh` | Đăng ký chéo 4 chain qua `bootstrapFoundingChains()` | **Dùng bản `.sh` này** |
| ~~`register_private_chains_t2.py`~~ | ~~Bản Python cũ, dùng `propose()`~~ | **Đã xoá (2026-08-27)** — lỗi thời từ trước khi `.sh` được sửa đúng; `propose()` không bao giờ thành công trên registry rỗng. Xem git history nếu cần tham khảo lại. |
| `start_relayer_daemon.sh` | Chạy `cross_chain_relayer` (RelayerDaemon) | Đọc khoá từ `RELAYER_KEY` env/`-key`, KHÔNG dùng khoá devnet mặc định cho vai trò thật |
| `gen_root_anchor_chain.py` | Sinh genesis + config N-node cho Root Anchor | Có kiểm tra port-collision tự động (xem comment `peer_rpc_port` trong file) |
| `gen_single_chain.py` | Sinh genesis + config cho 1 private chain | Lưu ý: khoá submitter devnet mặc định KHÔNG có BLS pubkey đăng ký sẵn trên private chain (khác Root Anchor) — dùng khoá trong `dev_accounts.json` của chính chain đó thay vì khoá hardcode nếu cần gửi giao dịch từ genesis |
| `gen_validator_entry.py` | Sinh khoá BLS+ETH + entry genesis cho 1 validator (ceremony thật) | Dùng cho vận hành viên thật, khác với `_rehearsal_gen_node_configs.py` |
| `_rehearsal_gen_node_configs.py` | Helper nội bộ cho `rehearse_root_anchor_ceremony.sh` | KHÔNG dùng cho ceremony thật — chỉ để diễn tập cục bộ, xem docstring đầu file |
| `rehearse_root_anchor_ceremony.sh` | Diễn tập ceremony Root Anchor cục bộ (4 operator giả lập) | Xem `note/runbook_root_anchor_genesis_ceremony.md` |
| `register_chains` | Binary Go (build từ `execution/cmd/tool/register_chains`) | Không commit vào git (`.gitignore`), tự build khi script cần |
| `cross_chain_relayer` | Binary Go RelayerDaemon (build từ `execution/cmd/tool/cross_chain_relayer`) | Không commit vào git |
| `stop.sh` | Dừng toàn bộ private chains + root anchor trên máy này | |
| `install.sh` / `build_release.sh` | Cài đặt systemd service / đóng gói bản release standalone | Dùng cho triển khai production 1 node qua systemd, không phải devnet đa-chain |
| `setup_and_run.sh` | Luồng cũ (1 single chain) | Tiền thân trước khi có multi-chain — vẫn hoạt động, không phải trọng tâm hiện tại |
| `cluster/`, `docs/` | Bộ script/tài liệu cluster btrfs + systemd riêng (Sui-style multi-node) | Xem `docs/install.md` trong thư mục con |

## 3. Cổng kết nối mặc định (đổi qua `--port-offset`/`--rpc-port-base` nếu chạy nhiều cụm)

Xem chi tiết đầy đủ + lý do tách port trong comment của `gen_root_anchor_chain.py`
(`peer_rpc_port` phải khác `network_port` — bug thật đã tìm+vá 2026-08-25, có kiểm tra tự động
chặn regressions). Bảng dưới chỉ liệt kê nhanh:

| Cổng | Việc dùng |
|---|---|
| `rpc_port` (mặc định 9099 + i) | JSON-RPC (Go execution layer) |
| `network_port` (19200 + i) | gRPC consensus P2P thật (Rust `tonic_network`) |
| `peer_rpc_port` (29200 + i) | HTTP diagnostic server riêng (Rust `PeerRpcServer`) — KHÁC `network_port` |
| `metrics_port` (12100 + offset + i) | Prometheus exporter |

## 4. Log thời gian thực

Log THẬT theo từng giao dịch/block nằm ở `logs/execution/<YYYY-MM-DD>/execution.log` của mỗi
node — **không phải** `node-0.log`/`node.log` (các file đó chỉ có log khởi động, đã có người
tốn thời gian tra nhầm file này, xem `note/deployment_runbook_step_by_step.md` mục Troubleshooting).
