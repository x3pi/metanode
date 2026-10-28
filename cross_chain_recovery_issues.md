# 📋 DANH SÁCH LỖI & KẾ HOẠCH KHẮC PHỤC CƠ CHẾ CROSS-CHAIN ROLLUP (LƯU TRỮ CHO NHÁNH MỚI)

> **Mục đích tài liệu:** Ghi nhận toàn bộ chi tiết phân tích mã nguồn, bất biến toán học/mật mã, kịch bản lỗi biên (edge cases) và phương án xử lý cho cơ chế Rollup / Recovery giữa Execution Cluster và Parent Chain. Tài liệu này phục vụ cho việc tạo một nhánh Git mới (branch) để refactor và khắc phục sau, không làm ảnh hưởng đến mã nguồn đang chạy ổn định của nhánh hiện tại.

---

## 📌 BỐI CẢNH & HIỆN TRẠNG HỆ THỐNG

- **Trạng thái nhánh hiện tại (Happy Path):**
  - Mạng gồm 10 nodes (4 Parent Chain nodes, 3 Exec 1 nodes, 3 Exec 2 nodes) đang hoạt động bình thường từ Block 0.
  - Toàn bộ 4 bài test thực tế trong `metanode-suite/test-simple/test-rpc/test-chain/cross-chain` đều **PASS 100% trong 22 giây**:
    - `01-parent-chain-health`: PASS (5s)
    - `02-cross-chain-transfer`: PASS (10s) - Số dư tăng thật (+940030000000000 wei) sau 4s-5s; Bảo toàn tổng cung (Conservation): `ok: true`, `diff: 0`.
    - `03-cross-chain-stress`: PASS (1s) - 10/10 giao dịch song song thành công.
    - `04-cross-chain-contract`: PASS (6s) - Deploy Smart Contract, kích hoạt xuyên chuỗi, cập nhật biến đếm thành công.
- **Vấn đề đặt ra:**
  - Trong luồng bình thường, mạng không gặp lỗi. Tuy nhiên, trong các tình huống **mạng sập bất ngờ, crash đột ngột hoặc mất kết nối giữa các cụm** (Failover / Disaster Recovery), cơ chế tự phục hồi hiện tại đang tồn tại 4 lỗi tiềm ẩn về mặt logic và mật mã học.

---

## 🔍 CHI TIẾT CÁC LỖI CẦN XỬ LÝ Ở NHÁNH MỚI

---

### 🔴 LỖI 1: [P1 · Logic Mật Mã] Đổi `SourceSeq` nhưng giữ nguyên `MessageID`

- **Tập tin liên quan:** [worker_send.go](file:///home/abc/nhat/consensus-chain/metanode/execution/pkg/rollup/worker_send.go#L214-L227)
- **Vị trí code:**
  ```go
  if strings.Contains(err.Error(), "wrong nonce") || strings.Contains(err.Error(), "already resolved") {
      ...
      if remoteSeq, sErr := w.client.GetFloatSeq(w.blsKeyPair.PublicKey()); sErr == nil && rec.SourceSeq != remoteSeq {
          _ = w.store.SetFloatSeq(remoteSeq)
          rec.SourceSeq = remoteSeq  // <--- LỖI: Sửa SourceSeq nhưng không tính lại MessageID
          _ = w.store.Put(rec)
      }
      break
  }
  ```
- **Phân tích cơ chế & Nguyên nhân:**
  1. Khi một bản ghi giao dịch được tạo ra ban đầu, `MessageID` được tính toán tất định theo công thức:
     `MessageID = Keccak256(SourcePubKey || DestPubKey || Sender || Target || Value || GasFee || PayloadHash || SourceSeq)`
  2. Trên Parent Chain, khi hàm `TransferFloat` xử lý thành công, Parent Chain cũng lưu bản ghi theo `MessageID` được băm từ `SourceSeq` mới này:
     `parentMessageID = Keccak256(digest(remoteSeq))`
  3. Khi code sửa `rec.SourceSeq = remoteSeq` nhưng giữ nguyên `rec.MessageID` cũ:
     - Record trong store cục bộ vẫn mang khóa `rec.MessageID` cũ.
     - Parent Chain lưu bản ghi dưới `parentMessageID` mới.
  4. Ở pha kiểm tra xác nhận sau đó (`rec.State == StateSendSubmitted`), worker gọi:
     `w.client.GetTransferRecord(rec.MessageID)` (với ID cũ).
  5. Parent Chain không tìm thấy ID cũ này -> Giao dịch bị **kẹt vô tận** ở trạng thái chờ xác nhận, không bao giờ hoàn tất.
- **Phương án giải quyết trong nhánh mới:**
  - Nếu muốn phục hồi sequence khi gặp `wrong nonce`: Phải xóa record cũ khỏi store, tính lại `newMsgID = ComputeMessageID(..., remoteSeq)`, ký lại digest BLS mới, và lưu record dưới key `newMsgID`.
  - Hoặc phương án an toàn nhất: Không tự ý sửa sequence của record đã ký; đánh dấu record đó là `StateFailed` / `StateRevert` và để tầng ứng dụng/user phát hành lại một giao dịch mới hoàn toàn.

---

### 🔴 LỖI 2: [P1 · Consensus Invariant] Hạ `FloatSeq` lúc khởi động gây trùng Nonce (Nonce Reuse)

- **Tập tin liên quan:** [worker_send.go](file:///home/abc/nhat/consensus-chain/metanode/execution/pkg/rollup/worker_send.go#L74-L77)
- **Vị trí code:**
  ```go
  localSeq, _ := w.store.GetNextFloatSeq()
  if localSeq < remoteSeq {
      _ = w.store.SetFloatSeq(remoteSeq)
  } else if localSeq > remoteSeq {
      // <--- LỖI: Tự động ép hạ FloatSeq cục bộ xuống remoteSeq
      _ = w.store.SetFloatSeq(remoteSeq)
  }
  ```
- **Phân tích cơ chế & Nguyên nhân:**
  1. Trạng thái `localSeq > remoteSeq` không nhất thiết là lỗi. Ví dụ: Cụm Exec đã phát 3 giao dịch ra ngoài (`localSeq = 3`), các giao dịch này đang nằm trong hàng đợi hoặc đang truyền trên mạng, Parent Chain mới chỉ commit 1 giao dịch (`remoteSeq = 1`).
  2. Nếu cụm Exec bị restart đúng thời điểm này, code ép hạ `localSeq` về 1.
  3. Giao dịch người dùng tiếp theo submit vào sẽ được cấp `nonce = 1` (trùng với nonce của giao dịch trước đó) -> **Tái sử dụng Nonce (Nonce Reuse)**, gây va chạm băm và xung đột dữ liệu trên Parent Chain.
  4. Ngoài ra, việc `SendWorker` tự tiện ghi `w.store.SetFloatSeq(...)` vào DB cục bộ mà không đi qua sự kiện đồng thuận Raft (`EventProposer`) sẽ làm lệch state giữa các replica trong cùng một cụm shard.
- **Phương án giải quyết trong nhánh mới:**
  - Khôi phục lại cơ chế bảo vệ nguyên bản của tác giả:
    ```go
    } else if localSeq > remoteSeq {
        log.Printf("ERROR: SendWorker: local FloatSeq (%d) is greater than remote (%d). Refusing to auto-fix to prevent forks.", localSeq, remoteSeq)
    }
    ```
  - Mọi thao tác thay đổi `FloatSeq` mang tính chất đồng thuận bắt buộc phải đi qua log entry của Raft state machine.

---

### 🔴 LỖI 3: [P1 · Mất An Toàn Mạng] `QuorumClient.GetFloatSeq()` trả về `(0, nil)` khi mất mạng

- **Tập tin liên quan:** [quorum_client.go](file:///home/abc/nhat/consensus-chain/metanode/execution/pkg/parentchain/quorum_client.go#L635-L646)
- **Vị trí code:**
  ```go
  func (q *QuorumClient) GetFloatSeq(pubKey cm.PublicKey) (uint64, error) {
      if len(q.clients) == 0 {
          return 0, ErrNoNodesConfigured
      }
      for _, c := range q.clients {
          seq, err := c.GetFloatSeq(pubKey)
          if err == nil {
              return seq, nil
          }
      }
      return 0, nil // <--- LỖI NGUY HIỂM: Tất cả client lỗi nhưng vẫn trả về 0 và error = nil
  }
  ```
- **Phân tích cơ chế & Nguyên nhân:**
  1. Khi toàn bộ mạng Parent Chain bị mất kết nối (HTTP 503, timeout, hoặc đang khởi động lại), vòng lặp `for` thất bại với mọi client.
  2. Hàm thay vì trả về lỗi (`return 0, fmt.Errorf(...)`) thì lại trả về `(0, nil)`.
  3. Phía gọi hàm (`SendWorker.Start`) thấy `err == nil` nên tưởng rằng kết nối thành công và Parent Chain đang ở sequence 0.
  4. Kết hợp với Lỗi 2 ở trên: Worker sẽ xóa sạch sequence cục bộ và reset toàn bộ về 0!
  5. Đồng thời, hàm này chỉ lấy kết quả của node đầu tiên phản hồi mà không kiểm tra Quorum BFT đa số (f+1 matching votes) giống như hàm `GetFloat()`.
- **Phương án giải quyết trong nhánh mới:**
  - Bắt buộc trả về lỗi nếu không có client nào phản hồi thành công:
    ```go
    return 0, fmt.Errorf("parentchain: all %d endpoints failed to query float sequence", len(q.clients))
    ```
  - Bổ sung logic gom phiếu Quorum (f+1 node đồng thuận cùng sequence) để chống node giả mạo / node bị lagging.

---

### 🔴 LỖI 4: [P1 · Kẹt State In-flight] `EventProposer` lỗi không dọn dẹp `inFlightSubmits`

- **Tập tin liên quan:** [worker_send.go](file:///home/abc/nhat/consensus-chain/metanode/execution/pkg/rollup/worker_send.go#L234-L248)
- **Vị trí code:**
  ```go
  w.inFlightSubmits.Store(rec.MessageID, true)
  hasProgress = true
  ...
  if w.EventProposer != nil {
      if err := w.EventProposer(event, rec.MessageID, rec.SourceSeq, ...); err != nil {
          log.Printf("SendWorker: failed to propose EventRPCSubmitted for %x: %v", rec.MessageID, err)
      }
      continue // <--- LỖI: w.inFlightSubmits vẫn giữ key rec.MessageID!
  }
  ```
- **Phân tích cơ chế & Nguyên nhân:**
  1. `inFlightSubmits` là bộ nhớ đệm (sync.Map) dùng để ngăn worker gửi trùng một giao dịch đang được xử lý.
  2. Đầu vòng lặp (dòng 164) có đoạn kiểm tra:
     ```go
     if _, inFlight := w.inFlightSubmits.Load(rec.MessageID); inFlight {
         continue // Bỏ qua nếu đang in-flight
     }
     ```
  3. Nếu `w.EventProposer` gặp lỗi tạm thời (hàng đợi Raft đầy, timeout, channel buffer full):
     - Lỗi được in ra log.
     - Vòng lặp `continue` nhưng **không xóa key khỏi `inFlightSubmits`**.
     - Ở các lần lặp tiếp theo, giao dịch này bị dòng 164 chặn vĩnh viễn, rơi vào trạng thái kẹt vô tận (hung in StateSendPending).
- **Phương án giải quyết trong nhánh mới:**
  - Trong khối xử lý lỗi của `EventProposer`, bổ sung lệnh xóa sạch trạng thái:
    ```go
    if err := w.EventProposer(...); err != nil {
        log.Printf("SendWorker: failed to propose EventRPCSubmitted for %x: %v", rec.MessageID, err)
        w.inFlightSubmits.Delete(rec.MessageID) // Cho phép lần lặp sau thử lại
    }
    ```

---

### 🟡 MỤC 5: [P2 · Kiểm Thử & Triển Khai] Phân Định Giữa Chaos Testing Và Live Safe Testing

- **Tập tin liên quan:** [test_real_world_scenarios.go](file:///home/abc/nhat/consensus-chain/metanode/execution/scripts/test/test_real_world_scenarios.go#L563)
- **Vấn đề đặt ra:**
  - Kịch bản 5 trong `test_real_world_scenarios.go` gọi `stopAllParentNodes()` để kiểm tra độ bền của Execution Shard.
- **Phân định rõ ràng:**
  - **Trên môi trường Devnet / Staging**: Việc tắt/bật node là tiêu chuẩn bắt buộc của bài test chịu lỗi (Chaos/Fault-Tolerance Test). Không được tắt bỏ tính năng này trong các kịch bản test tổng thể.
  - **Trên môi trường Production (Live Chain với người dùng thật)**: Tuyệt đối không được chạy các kịch bản tắt node.
- **Phương án giải quyết trong nhánh mới:**
  - Thêm một biến cờ môi trường `SAFE_TEST_LIVE=true` (hoặc cấu hình trong Ansible).
  - Khi cờ này được bật, script test sẽ tự động bỏ qua các bước `stopAllParentNodes()` và `killParentPort()`, chỉ chạy các bài kiểm tra giao dịch nghiệp vụ (transfer, smart contract, đăng ký tài khoản).

---

## 📋 TỔNG KẾT BẢN THAY ĐỔI ĐÃ THÀNH CÔNG Ở NHÁNH HIỆN TẠI (GIỮ NGUYÊN)

Các phần sửa sau đây đã giúp mạng chạy hoàn hảo và cần được giữ nguyên:
1. **[inventory.yml](file:///home/abc/nhat/consensus-chain/metanode/deploy/ansible_clusters/inventory.yml)**: Bổ sung cấu hình `parent_float_accounts` ở Genesis cho Exec 1 và Exec 2.
2. **[mtn_api.go](file:///home/abc/nhat/consensus-chain/metanode/execution/cmd/simple_chain/mtn_api.go)**:
   - Thêm tham số `blsKeyHex` để định tuyến chính xác tới cụm nhận khi tương tác với Smart Contract.
   - Chặn giao dịch tự chuyển cho chính mình (`fromKey == toKey`).
   - Sử dụng `pool.GetPendingNonce(senderAddr, stateNonce)` để lấy đúng nonce lũy kế từ mempool.
3. **[deploy.yml](file:///home/abc/nhat/consensus-chain/metanode/deploy/ansible_clusters/deploy.yml)**: Chặn lệnh restart tuần tự không cần thiết khi chạy `--test-only`.
4. **Bộ test cross-chain**: Tăng lượng chuyển test lên 0.001 token (10^15 wei) để vượt trội hoàn toàn so với phí gas của proposer, xác thực số dư đích tăng rõ ràng (+940030000000000 wei sau 4s-5s).
