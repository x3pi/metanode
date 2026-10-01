# Discovery: Khảo sát kiến trúc và giao tiếp trước khi triển khai Parent Chain Multi-Node

> Tài liệu thực hiện theo Mục 4 của `note/parent_chain_multinode_plan.md`.
> Ngày lập: 2026-09-30.

---

## 1. Trả lời chi tiết D1 – D14

### D1: Khi Go trả `ExecuteBlockResponse{Success:false}`, Rust làm gì (retry/halt/bỏ qua)? Giới hạn số lần thử?
- **Vị trí code:**
  - `consensus/metanode/src/node/block_delivery.rs:135-238` (`deliver_with_halt_retry`)
  - `consensus/metanode/src/node/executor_client/block_sending.rs:452-505`, `829-848`, `910-922`
  - `execution/executor/ffi_bridge.go:176-291` (`cgo_execute_block`)
- **Hành vi:**
  - `deliver_with_halt_retry` chạy một vòng lặp vô hạn `loop { ... }`.
  - Nếu `send_committed_subdag` trả về lỗi (`Err`), Rust tăng `attempt += 1`, ghi log cảnh báo `🛑🚨 [CONSENSUS-HALT-TX-PAYLOAD-LOST] Failed to deliver commit ... HALTING dispatch on this commit rather than risk a fork by skipping it (Zero-Fork Invariant)`, đánh dấu claim bị kẹt (`mark_stuck`), và `sleep 10s` trước khi thử lại. **Không bao giờ bỏ qua (skip)** và **không có giới hạn số lần thử** (hệ thống dừng dispatch commit đó để bảo vệ Zero-Fork Invariant cho đến khi được phục hồi hoặc can thiệp).
  - Ở tầng FFI `cgo_execute_block` (`ffi_bridge.go:268-291`): khi nhận được response từ Go qua `req.ResponseCh`, response được serialize vào `outPayload`. Nếu channel timeout (`executeBlockResponseTimeout = 30s`), Go log lỗi và trả `Success: false` với `ret = C.bool(false)`.
  - Trong `block_sending.rs:844-848`: nếu `success` là false, Rust gọi `self.record_send_failure().await`, ngắt vòng lặp gửi batch và đưa các block chưa gửi thành công quay lại `send_buffer` (`re-buffered unsent blocks`), không tăng `next_expected_index`.

---

### D2: `GetBlocksRange` được gọi khi nào, khoảng nào; Rust đọc những trường nào của `BlockData`?
- **Vị trí code:**
  - `consensus/metanode/src/node/setup_consensus/fork_guard.rs:170-202`
  - `consensus/metanode/src/node/setup_consensus/startup_sync.rs:176-235`, `638`
  - `consensus/metanode/src/node/peer_go_client.rs:59-75`
  - `consensus/metanode/src/network/peer_rpc/server.rs:698-725`
- **Hành vi & Trường đọc:**
  - `fork_guard.rs` gọi `get_blocks_range(next_check_block, next_check_block)` (1 block mỗi lần) định kỳ để so sánh với quorum peer. Rust đọc:
    - `local_block.block_hash`
    - `local_block.state_root`
  - `startup_sync.rs` gọi `get_blocks_range(local_block, local_block)` trong giai đoạn kiểm tra anti-fork khi khởi động. Rust đọc:
    - `local_b.block_hash`
    - `local_b.state_root`
    - `local_b.timestamp_ms`
    - `local_b.transactions_root`
    - `local_b.receipts_root`
  - `peer_rpc/server.rs` gọi `executor.get_blocks_range(from, actual_to)` để phục vụ peer RPC `/get_blocks`. Server serialize toàn bộ `pb.BlockData` thành bytes (hex) để peer nhận và chuyển sang `sync_blocks()`. Do đó `BlockData` cần chứa:
    - `block_number`, `block_hash`, `parent_hash`, `epoch`, `timestamp_ms`
    - `state_root`, `transactions_root`, `receipts_root`
    - `raw_block_bytes` (chứa toàn bộ payload block phục vụ thực thi lại / xác minh).

---

### D3: Lúc khởi động Rust hỏi Go gì (last block, last GEI, epoch)? Sau restart Rust replay từ đâu? Go phải đặt lại `storage.UpdateLastBlockNumber/UpdateLastGlobalExecIndex` thế nào?
- **Vị trí code:**
  - `consensus/metanode/src/node/executor_client/mod.rs:340-410`
  - `consensus/metanode/src/node/executor_client/rpc_queries.rs:256-335`
  - `consensus/metanode/src/node/recovery.rs:40-100`
  - `execution/executor/unix_socket_handler_epoch_state.go:18-70` (`HandleGetLastBlockNumberRequest`)
  - `execution/pkg/storage/block_number_state.go:93-115`
- **Hành vi:**
  - Lúc khởi động, Rust gửi `GetLastBlockNumberRequest` tới Go. Go trả về `LastBlockNumberResponse`:
    - `last_block_number`
    - `last_global_exec_index`
    - `last_epoch`
    - `is_ready` (`storage.IsBlockchainInitDone()`)
  - Rust khởi tạo:
    - `go_next_expected = last_global_exec_index + 1`
    - `next_block_number = last_block_number + 1`
  - Sau restart: Trong `recovery.rs`, Rust quét commits từ local store bắt đầu từ `go_last_block + 1` để replay tới Go executor thông qua `send_committed_subdag`.
  - Go cần: Khi khởi động node Parent Chain, trước khi gọi `InitFFIBridge`, đọc từ DB bền:
    - `storage.UpdateLastBlockNumber(lastPersistedBlock)`
    - `storage.UpdateLastAssignedBlockNumber(lastPersistedBlock)`
    - `storage.UpdateLastGlobalExecIndex(lastPersistedGEI)`
    - `storage.SetBlockchainInitDone()`

---

### D4: Ngữ nghĩa `SyncBlocks` (`execute_mode`, `preserve_own_commit_index`); ai gọi nó cho node tụt lại?
- **Vị trí code:**
  - `consensus/metanode/src/node/executor_client/block_sync.rs:60-120`
  - `consensus/metanode/src/node/rust_sync_node/sync_loop.rs:232`, `fetch.rs:803`
  - `execution/executor/unix_socket_handler_sync.go:122-140`
  - `execution/executor/unix_socket_handler.go:37` (`CustomSyncBlocksCallback`)
- **Hành vi:**
  - `SyncBlocks` được gọi bởi `rust_sync_node` (hoặc `startup_sync.rs` khi node tụt lại phía sau mạng) sau khi tải các `BlockData` từ peer qua Peer RPC `/get_blocks`.
  - `execute_mode`:
    - `false` (`STORE`): Chỉ lưu các block vào database mà không thực thi qua state machine.
    - `true` (`EXECUTE`): Thực thi block qua state machine (NOMT), tính toán lại state root và so sánh với header root.
  - `preserve_own_commit_index`:
    - Khi `true` (dành cho Validator có DAG riêng): Go không ghi đè `lastHandledCommitIndex` bằng commit index trong header block sync, tránh làm lệch `next_expected_index` của DAG cục bộ.
    - Khi `false` (dành cho SyncOnly node): cho phép cập nhật commit index.
  - Trong Parent Chain: `CustomSyncBlocksCallback` sẽ kiểm tra tính hợp lệ của chuỗi block (gap, conflict), áp dụng block vào state tree và store, và xác minh root.

---

### D5: Cách chain chính dựng `EpochBoundaryData` và `GetValidatorsAtBlock` (nguồn committee, thứ tự, stake)
- **Vị trí code:**
  - `execution/executor/unix_socket_handler_epoch.go:20-60`
  - `execution/executor/unix_socket_handler_validators.go:15-60`
  - `execution/cmd/parent_chain/app.go:59-103`
- **Hành vi:**
  - `CustomGetEpochBoundaryDataCallback`: Trả về `*pb.EpochBoundaryData` gồm: `Epoch`, `EpochStartTimestampMs`, `BoundaryBlock`, `BoundaryGei`, `Validators` (`[]*pb.ValidatorInfo`), và `EpochDurationSeconds`.
  - `CustomGetValidatorsCallback`: Trả về danh sách `[]*pb.ValidatorInfo` tại block được yêu cầu. Mỗi validator gồm: `Address`, `Stake`, `AuthorityKey` (BLS 96 bytes hoặc 48 bytes uncompressed), `ProtocolKey`, `NetworkKey`, `Name`, `P2PAddress`.
  - Thứ tự danh sách `Validators` phải **hoàn toàn giống hệt** giữa mọi node (sắp xếp chuẩn tắc từ `parent_genesis.json`).

---

### D6: Đánh số block: bắt đầu từ 1? Có block `block_number = 0`? Với `is_authoritative_gei=false`, `GlobalExecIndex` có luôn bằng `block_number`?
- **Vị trí code:**
  - `consensus/metanode/proto/executor.proto:13-50`
  - `execution/cmd/parent_chain/processor/block_processor.go:70-76`
- **Hành vi:**
  - Đánh số block bắt đầu từ **1**.
  - Block `block_number = 0` là block khởi tạo ranh giới epoch do Rust gửi lúc ban đầu, không chứa giao dịch thực thi và phải được bỏ qua trong `BlockProcessor` (`if block.BlockNumber == 0 { return }`).
  - Trong Parent Chain, `is_authoritative_gei` là `false` (Rust quản lý GEI). Với các block bình thường (không có phân mảnh commit vượt ngưỡng max tx), `GlobalExecIndex` tăng đồng nhịp cùng `block_number`.

---

### D7: Chuyển epoch của parent chain (`EpochDurationSeconds=86400` đang hard-code); có hỗ trợ đổi committee giữa chừng không?
- **Vị trí code:**
  - `execution/cmd/parent_chain/app.go:84`
- **Hành vi:**
  - Hiện tại `EpochDurationSeconds = 86400` được hard-code. Theo Quyết định Đ14: Giai đoạn đầu **committee cố định**, được tải từ `parent_genesis.json`.
  - Khi epoch kết thúc và chuyển epoch mới, committee giữ nguyên từ file genesis. Không tự động đổi validator giữa chừng. Quy trình thay đổi committee là quy trình vận hành có chủ đích (manual re-genesis/upgrade).

---

### D8: Rust dựng `PeerRpcServer` từ `peer_rpc_addresses` thế nào và gọi Go local ra sao?
- **Vị trí code:**
  - `consensus/metanode/src/network/peer_rpc/server.rs:55-110`, `690-740`
  - `consensus/metanode/src/network/peer_rpc/client.rs:212`
  - `consensus/metanode/src/node/startup.rs:218`
- **Hành vi:**
  - Rust khởi động `PeerRpcServer` lắng nghe trên `peer_rpc_port` (cấu hình trong `node_parent.toml`).
  - Khi các node peer gửi yêu cầu HTTP GET `/get_blocks?from=X&to=Y`, server gọi `executor.get_blocks_range(from, actual_to)` xuống Go Master qua FFI callback `CustomGetBlocksRangeCallback`.
  - Go trả về `*pb.GetBlocksRangeResponse{Blocks: []*pb.BlockData}`. Rust encode thành protobuf bytes (hex) và trả về JSON cho peer.
  - Các node kết nối tới nhau dựa trên danh sách `peer_rpc_addresses` được cấu hình trong `node_parent.toml`.

---

### D9: Dùng `pkg/trie` (NOMT) từ Go cho một namespace không phải account: `trie_factory.go` chọn namespace thế nào; cách sinh proof (`GenerateProof`); cách kiểm proof; chi phí commit; giới hạn key.
- **Vị trí code:**
  - `execution/pkg/trie/trie_factory.go:84-173`, `242-280`
  - `execution/pkg/trie/nomt_state_trie.go:2126-2135`
  - `execution/pkg/nomt_ffi/bridge.go:988-1010`
  - `execution/pkg/nomt_ffi/rust_lib/src/lib.rs:959-1008`
  - Cargo cache: `nomt-core/src/proof/path_proof.rs:66-103`, `185-200`
- **Phân tích:**
  - **Chọn namespace:** `GetOrInitNomtHandle(namespace)` tạo thư mục con `<basePath>/<namespace>` cho cơ sở dữ liệu NOMT riêng biệt. Với Parent Chain, ta dùng namespace riêng (ví dụ `"parent_chain_state"`), cô lập hoàn toàn với `account_state` hay các chain khác.
  - **Giới hạn key:** Key trong NOMT là mảng cố định 32 byte (`[32]byte`), được băm bằng `keccak256(namespace_byte || business_key)` đúng như đặc tả Mục 5.2.
  - **Sinh proof:** Đã có sẵn qua FFI `Handle.GenerateProof(key [32]byte) ([]byte, error)`, bên dưới gọi `session.prove(key_path)` và serialize `nomt::PathProof` bằng `bincode`.
  - **Kiểm proof:** `nomt-core` cung cấp `PathProof::verify::<Blake3Hasher>(&key_path.view_bits(), root)` và `confirm_value(&expected_leaf)` / `confirm_nonexistence(&key_path)`. Phía Go chưa có wrapper FFI để verify proof độc lập mà không cần handle DB. Ta sẽ bổ sung hàm C/Rust FFI `nomt_verify_proof` vào `rust_lib/src/lib.rs` và `bridge.go` để Go kiểm tra Merkle proof offline.

---

### D10: Rào chắn độ bền: mẫu `CommitBlockState` (block DB bền trước NOMT), cách khôi phục khi NOMT lệch tip.
- **Vị trí code:**
  - `execution/pkg/blockchain/block_state_commit.go:95-238`
  - `execution/pkg/rollup/N1_DURABILITY_REPORT.md`
- **Mẫu áp dụng cho Parent Chain:**
  1. Ghi dữ liệu block, header, transactions, receipts, và index mapping vào DB bền vững (LevelDB/PebbleDB) với cờ `Sync=true`.
  2. Cập nhật `storage.UpdateLastBlockNumber(blockNum)`.
  3. Sau khi DB bền đã lưu thành công, mới gọi `Commit` trên cây state NOMT.
  4. Nếu tiến trình bị crash giữa chừng: Khi khởi động lại, đối chiếu tip của DB bền với root của NOMT. Nếu NOMT chưa commit block đó, thực thi lại block từ DB bền để đưa NOMT về đúng tip của DB bền; NOMT không bao giờ được phép đi vượt quá tip của DB bền.

---

### D11: Danh tính người gửi: địa chỉ ↔ khóa BLS/ECDSA (`bls.GetAddressFromPublicKey`), đăng ký khóa, bootstrap tài khoản từ genesis.
- **Vị trí code:**
  - `execution/pkg/parentchain/state.go:89-110`, `RegisterAccount`, `ensureChainRegistry`
  - `execution/pkg/bls/`
- **Quy tắc:**
  - Địa chỉ người gửi (20 bytes) dẫn xuất từ khóa công khai ECDSA (`crypto.PubkeyToAddress`) hoặc BLS (`bls.GetAddressFromPublicKey`).
  - Khóa của các Cluster tham gia được đăng ký trong genesis hoặc qua `ensureChainRegistry` / `RegisterAccount`.
  - Trong `parent_genesis.json`, ta khai báo danh sách các tài khoản/cluster khởi tạo (`initial_accounts`, `initial_clusters`) và nạp trực tiếp vào state tree ở block khởi tạo (genesis).

---

### D12: Miền chữ ký: `TransactionHashData` có gồm `ChainID` không? Xác nhận `ChainID=990` không trùng chain nào.
- **Vị trí code:**
  - `execution/pkg/transaction/transaction.go:693`, `752` (`hashPb.ChainID = t.proto.ChainID`)
  - `execution/pkg/common/constant.go`
  - `OPERATIONS_GUIDE.md:29` (Root Anchor ChainID: `991`)
- **Kết luận:**
  - `TransactionHashData` **có bao gồm** trường `ChainID`. Vì vậy mã băm giao dịch và chữ ký phụ thuộc chặt chẽ vào `ChainID`, ngăn chặn hoàn toàn tấn công replay chéo chain.
  - Qua rà soát toàn bộ kho mã nguồn: ChainID `991` thuộc về Root Anchor (Public Chain), các số 101–104 là exec clusters. ChainID `990` hoàn toàn chưa được sử dụng, an toàn để làm ChainID chuẩn cho Parent Chain.

---

### D13: Thứ tự `transactions` trong `ExecutableBlock` và cách nhóm/sắp giao dịch để giữ nonce tuần tự.
- **Vị trí code:**
  - `execution/cmd/simple_chain/processor/speculative_executor.go:842-876` (`PrepareTransactions`)
- **Quy tắc Đ8 của Parent Chain:**
  - Main chain chỉ sắp theo `TxHash` đơn thuần, điều này có thể làm xáo trộn thứ tự nonce của cùng một người gửi nếu gửi nhiều tx trong cùng một block.
  - Đối với Parent Chain (thực thi tuần tự, Quyết định Đ8):
    1. Loại bỏ các giao dịch trùng `TxHash` (giữ bản đầu tiên xuất hiện).
    2. Sắp xếp danh sách giao dịch theo bộ ba tăng dần: `(FromAddress, Nonce, TxHash)`.
    - Điều này đảm bảo tất cả các giao dịch của cùng một tài khoản luôn được thực thi theo thứ tự nonce tăng dần liên tục, loại bỏ lỗi lệch nonce khi sắp xếp ngẫu nhiên theo hash.

---

### D14: Cách lấy `leader_address`, `commit_digest`, `commit_timestamp_ms` từ `ExecutableBlock`.
- **Vị trí code:**
  - `consensus/metanode/proto/executor.proto:13-60` (`ExecutableBlock`)
  - `execution/cmd/parent_chain/processor/block_processor.go:70-76`
- **Các trường chuẩn tắc giữa các node:**
  - `BlockNumber`: số block chuẩn tắc được Rust đồng thuận chỉ định.
  - `GlobalExecIndex`: chỉ số thực thi toàn cục từ consensus.
  - `CommitIndex`: commit index nội bộ epoch.
  - `Epoch`: số thứ tự epoch.
  - `LeaderAddress`: địa chỉ 20 bytes của leader được xác định bởi BFT DAG.
  - `CommitHash` / `CommitDigest`: mã băm digest của commit DAG được các validator ký xác nhận.
  - `CommitTimestampMs`: timestamp đồng thuận (trung vị có trọng số stake do Linearizer tính toán). Nếu `CommitTimestampMs == 0`, dùng `timestamp_ms` của block trước đó (tuyệt đối không dùng `time.Now()`).

---

## 2. Đề xuất đặc tả giao dịch `DepositToFloat` (Trình duyệt theo WP2 / Mục 6)

Để đóng lỗ hổng G8 (không có chữ ký lúc thực thi, dẫn đến nguy cơ mint float tùy ý), `DepositToFloat` chuyển thành một giao dịch chuẩn tắc:

1. **Phong bì giao dịch:**
   - Là một `pb.Transaction` chuẩn của Metanode:
     - `FromAddress`: Địa chỉ của bên chuyển tiếp (Relayer) hoặc người gửi.
     - `ToAddress`: `0x0000000000000000000000000000000000001003` (`PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS`).
     - `Nonce`: Nonce tuần tự của `FromAddress` trên Parent Chain (lưu trong cây ở namespace `0x0B`).
     - `ChainID`: `990`.
     - `Sign`: Chữ ký ECDSA (`R, S, V`) hoặc BLS của `FromAddress`.
     - `Data`: Mã hóa nhị phân chuẩn tắc của `CallData`.

2. **Cấu trúc `CallData` cho `DepositToFloat`:**
   - `Method`: `"depositToFloat"` (hoặc selector 4 byte `0x...`)
   - `DestKey`: Khóa công khai của cụm đích (cm.PublicKey, 48 bytes BLS).
   - `DestClusterID`: uint64.
   - `Sender`: common.Address (20 bytes).
   - `Target`: common.Address (20 bytes).
   - `Amount`: *big.Int (32 bytes big-endian).
   - `MsgID`: common.Hash (32 bytes).
   - `Cert`: Chữ ký BLS (hoặc ECDSA) của **cụm nguồn (Source Cluster)** trên thông điệp deposit.

3. **Thông điệp ký chứng nhận của cụm nguồn (`DepositFloatMessage`):**
   ```go
   var DepositFloatDomainTag = []byte("DEPOSIT_FLOAT_V1:")

   func ComputeDepositFloatMessage(destKey cm.PublicKey, destClusterID uint64, sender, target common.Address, amount *big.Int, msgID common.Hash) []byte {
       buf := make([]byte, 0, len(DepositFloatDomainTag) + 48 + 8 + 20 + 20 + 32 + 32)
       buf = append(buf, DepositFloatDomainTag...)
       buf = append(buf, destKey[:]...)
       buf = appendUint64BE(buf, destClusterID)
       buf = append(buf, sender.Bytes()...)
       buf = append(buf, target.Bytes()...)
       buf = append(buf, padTo32(amount)...)
       buf = append(buf, msgID.Bytes()...)
       return buf
   }
   ```

4. **Xác thực trong bước thực thi tất định (`ApplyBlock`):**
   - Kiểm tra chữ ký và nonce của `pb.Transaction` (người gửi/relayer).
   - Kiểm tra `Cert` của `DepositToFloat`: Phải khớp với khóa công khai của một cluster đã được đăng ký hợp lệ trong `ChainRegistry` (hoặc cấu hình genesis).
   - Kiểm tra `msgID` chưa từng được xử lý (`store.GetTransferRecord(msgID)` không tìm thấy).
   - Nếu bất kỳ kiểm tra nào thất bại: Giao dịch bị đánh dấu thất bại trong Receipt, trạng thái số dư Float không bị thay đổi, đảm bảo tính nguyên tử 100% trên mọi validator.
