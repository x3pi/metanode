# Harness đo TPS e2e (Raft + BFT) — dùng cho kế hoạch giai đoạn 2

Nguồn: các script do Claude dùng ngày 2026-10-10 (copy nguyên văn từ scratchpad của phiên đó).

## Phải chỉnh trước khi chạy
- Trong `full_measure.py` và `bft_m_check.py` và `e2e3.py`, biến `S = Path("/tmp/claude-.../scratchpad")` trỏ vào thư mục tạm của phiên cũ. Đổi thành thư mục làm việc của bạn, ví dụ `/tmp/gemini_verify/`, trong đó có:
  - `bins/` chứa: `secp_tps_blast` (build từ `execution/cmd/tool/secp_tps_blast`), và các binary `simple_chain_<TAG>` bạn tự build (xem kế hoạch). Tên `simple_chain_<TAG>` là tham số `binname` của `e2e3.py`.
  - `meas_out/` (tự tạo): nơi ghi JSON/log/timeline.
- Phụ thuộc có sẵn trên máy này: `/tmp/gate_4val_clean_template` (template cụm BFT 4 validator), `execution/scripts/test/gate_e2e/run_env.sh`, `execution/scripts/test/evidence/run_raft_peak_search.py`, `/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json`.
  Nếu `/tmp/gate_4val_clean_template` không còn, KHÔNG tự bịa: dừng và báo.
- `full_measure.py::bft_start` có dòng `shutil.copy(BINS/"simple_chain_A", BINS/"simple_chain")` (chỉ dùng khi gọi trực tiếp); `bft_m_check.py::start_with(binname)` mới là hàm `e2e3.py bft` dùng — nó copy `simple_chain_<binname>` thành `simple_chain` rồi khởi động cụm.

## Cách dùng
```
python3 -I e2e3.py raft <tag> simple_chain_<TAG> '["-duration","60","-batch","1000"]'
python3 -I e2e3.py bft  <tag> simple_chain_<TAG> '["-duration","60","-batch","1000"]'
```
Kết quả: `meas_out/<tag>_e2e.json` (submitted, on_chain, complete, e2e_tps), `<tag>_timeline.json` (tx lên chain theo thời gian, poll song song), `logs_<tag>/` (BFT: log từng validator).
`e2e_tps = submitted / giây từ lúc bắt đầu gửi đến khi đủ tx trên chain`. `complete=false` nghĩa là chuỗi không nhận đủ tx → lần chạy FAIL, không được đưa vào trung vị.

## Cạm bẫy đã biết
1. Go link `consensus/metanode/target/release/libmetanode.a` nhưng cargo ghi vào `/home/abc/chain-n/metanode/target/release/`. Sau khi sửa Rust phải `cp -p` thư viện sang, rồi `touch execution/executor/ffi_bridge.go execution/pkg/nomt_ffi/bridge.go` trước `go build`.
2. Hai lần chạy nối tiếp phải đợi cụm cũ tắt hẳn (`e2e3.py` đã `sleep`), nếu không số liệu nhiễm.
3. Máy dùng chung, load average thường ~20: ghi `uptime` trước/sau từng lần chạy.
