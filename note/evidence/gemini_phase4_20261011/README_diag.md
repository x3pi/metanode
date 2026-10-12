# Bản chẩn đoán NOMT (không commit vào mã sản phẩm)

- `nomt_diag.patch`: patch áp lên HEAD `c359fa2a` (hoặc mới hơn nếu `consensus/vendor/nomt` không đổi) gồm `bitbox/writeout.rs`, `io/linux.rs`, `io/mod.rs`, `lib.rs` (thêm `test_io`) và file mới `diag.rs`.
- `io_stress.rs`: test stress cô lập, đặt vào `consensus/vendor/nomt/nomt/tests/io_stress.rs` sau khi áp patch.
- Dựng lại: `git worktree add /tmp/wt_diag <commit> && cd /tmp/wt_diag && git apply <đường dẫn>/nomt_diag.patch && cp <đường dẫn>/io_stress.rs consensus/vendor/nomt/nomt/tests/`.
- Chạy stress: `cd consensus/vendor/nomt/nomt && cargo test --release --test io_stress -- --nocapture`.
- Bản chẩn đoán chỉ in bộ đếm khi `write_ht` chờ > 30 s; trong các lần chạy bình thường không có bộ đếm nào được ghi.
