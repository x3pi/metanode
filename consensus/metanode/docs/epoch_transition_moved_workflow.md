# Tài liệu Đặc tả: Quy trình Chuyển giao Kỷ nguyên (Epoch Transition) trong Rust Consensus

Quá trình chuyển giao Kỷ nguyên (Epoch Transition) là một trong những cơ chế phức tạp và quan trọng nhất của hệ thống MetaNode. Nó xảy ra khi mạng lưới đồng thuận kết thúc một chu kỳ (Epoch N) và bước sang chu kỳ mới (Epoch N+1).

Luồng xử lý chính được định nghĩa tại hàm `transition_to_epoch_from_system_tx` trong file `src/node/transition/epoch_transition.rs`.

Dưới đây là chi tiết từng bước mà Rust sẽ thực hiện, kèm theo mã nguồn dẫn chứng.

---

## Bước 1: Kiểm tra an toàn và Khóa trạng thái (Guarding)

Trước khi thực hiện, Rust phải đảm bảo rằng không có một tiến trình chuyển giao nào khác đang chạy chéo nhau, đồng thời kiểm tra xem Kỷ nguyên yêu cầu có hợp lệ không.

```rust
// File: src/node/transition/epoch_transition.rs

// Chặn các trường hợp đã qua Epoch này hoặc đang chạy
if node.current_epoch >= new_epoch && !is_sync_only { return Ok(()); }
if node.current_epoch > new_epoch { return Ok(()); }

// Bật khóa (lock) để ngăn các tác vụ khác can thiệp
if node.coordination_hub.swap_epoch_transitioning(true) {
    warn!("⚠️ Full epoch transition already in progress, skipping.");
    return Ok(());
}

// Sử dụng Guard pattern để tự động nhả khóa nếu có lỗi (panic)
let _guard = Guard(node.coordination_hub.get_is_transitioning_ref());
```

---

## Bước 2: Chốt mốc thời gian và Tìm Hội đồng mới

Rust đi tìm danh sách những người xác thực (Validator) cho kỷ nguyên tiếp theo và thiết lập một mốc thời gian chốt sổ (Timestamp) bắt buộc. Mốc thời gian này sẽ được ép buộc sang phía Go để đảm bảo không bị lệch (Fork) giữa các node.

```rust
let committee_source = crate::node::committee_source::CommitteeSource::discover(config).await?;

let provisional_timestamp = boundary_timestamp_ms;
info!("ℹ️ Sending exact timestamp_ms={} to Go for epoch {}", provisional_timestamp, new_epoch);

// Cập nhật epoch và timestamp chuẩn cho provider
node.system_transaction_provider
    .update_epoch(new_epoch, provisional_timestamp)
    .await;
```

---

## Bước 3 & 4: Tạm dừng tạo Block, Xả dữ liệu và Đồng bộ với Go

Đây là bước phối hợp quan trọng nhất giữa Rust và Go. Rust ngừng việc đồng thuận cũ, đẩy hết các block còn kẹt trong buffer sang Go, và kiên nhẫn chờ đợi đến khi Go xác nhận đã xử lý xong block cuối cùng của Kỷ nguyên cũ.

```rust
// Hàm stop_authority_and_poll_go thực hiện các việc:
// 1. exec_client.flush_buffer().await -> Đẩy nốt block
// 2. node.authority.take().stop() -> Ngừng hẳn Consensus cũ
// 3. poll_go_until_synced() -> Chờ Go cập nhật xong Global Exec Index (GEI)
let synced_index = stop_authority_and_poll_go(
    node, 
    new_epoch, 
    &executor_client, 
    &committee_source
).await?;
```

---

## Bước 5: Dọn dẹp trạng thái và Bộ nhớ (Disk/Memory Cleanup)

Sau khi chốt sổ Kỷ nguyên cũ, hệ thống dọn dẹp các tệp tin trên ổ cứng của các kỷ nguyên quá cũ để tiết kiệm dung lượng, đồng thời reset lại các biến đếm cho Kỷ nguyên mới.

```rust
// Xóa file rác trên ổ cứng
if config.epochs_to_keep > 0 {
    cleanup_old_epochs(node, new_epoch, config.epochs_to_keep);
}

// Reset trạng thái đếm
node.current_epoch = new_epoch;
node.current_commit_index.store(0, Ordering::SeqCst);
node.last_global_exec_index = effective_synced;

// Xóa cache các giao dịch đã commit trong RAM
{
    let mut hashes = node.committed_transaction_hashes.lock().await;
    hashes.clear();
}
```

---

## Bước 6: Thông báo tiến cấp Kỷ nguyên cho Go (Advance Go Epoch)

Lúc này Rust ra lệnh trực tiếp cho Executor (Go) qua FFI/RPC để yêu cầu Go cũng tiến hành đóng gói trạng thái và chuyển sang Kỷ nguyên mới.

```rust
let go_boundary = executor_client.get_last_block_number().await?.0;

// Gửi lệnh qua Go: Advance Epoch
if let Err(e) = executor_client
    .advance_epoch(
        new_epoch,
        provisional_timestamp,
        go_boundary,
        effective_synced,
    )
    .await
{
    warn!("⚠️ Failed for epoch {}: {}", new_epoch, e);
}
```

---

## Bước 7: Xác định vai trò mới và Khởi tạo Hệ thống

Rust tải danh sách Ủy ban (Committee) chính thức. Nó kiểm tra xem `PublicKey` của node hiện tại có nằm trong danh sách không để quyết định node sẽ tiếp tục làm **Validator** (Tạo block) hay bị hạ cấp xuống **SyncOnly** (Chỉ đồng bộ).

```rust
// Cập nhật committee
let (committee, epoch_timestamp_to_use, eth_addresses) = committee_source
    .fetch_committee_with_timestamp(&config.executor_send_socket_path, new_epoch)
    .await?;

// Kiểm tra và đổi NodeMode (Validator / SyncOnly)
node.check_and_update_node_mode(&committee, config, true).await?;

// Khởi tạo các module đồng thuận mới dựa trên Role
if matches!(node.node_mode, NodeMode::Validator) {
    setup_validator_consensus(
        node, new_epoch, effective_synced, epoch_timestamp_to_use, db_path, committee, config
    ).await?;
} else {
    setup_synconly_sync(
        node, new_epoch, effective_synced, epoch_timestamp_to_use, committee, config
    ).await?;
}
```

---

## Bước 8 & 9: Phục hồi giao dịch treo và Xác minh chéo

Bước cuối cùng là dọn dẹp các giao dịch của người dùng bị mắc kẹt do quá trình chuyển Kỷ nguyên diễn ra (đưa chúng lại vào hàng chờ của Kỷ nguyên mới), tắt cờ trạng thái `is_transitioning`, và chốt hạ bằng việc kiểm tra tính nhất quán giữa Rust và Go một lần cuối.

```rust
// Phục hồi Pending Transactions
let _ = recover_epoch_pending_transactions(node).await;

// Nhả khóa (Bây giờ hệ thống có thể nhận block và txs mới)
node.coordination_hub.set_epoch_transitioning(false);
let _ = node.submit_queued_transactions().await;

// Reset trạng thái cấu hình nội bộ
node.reset_reconfig_state().await;

// Kiểm tra chéo (Verify) tính nhất quán lần cuối cùng
verify_epoch_consistency(node, new_epoch, epoch_timestamp_to_use, &executor_client).await?;
```

---
**Tổng kết:** Quy trình này đảm bảo việc dừng các tiến trình sinh block một cách dứt khoát, đồng bộ hóa tuyệt đối giữa trạng thái C++ / Rust và Go, dọn dẹp dữ liệu thừa, phân bổ lại quyền lực (Roles) trên mạng lưới và khởi động một chu trình đồng thuận mới một cách hoàn hảo mà không làm sập mạng.
