# Kiến Trúc Xử Lý Giao Dịch Đồng Thời (Parallel Transaction Execution) trên Metanode

Tài liệu này mô tả chi tiết cơ chế phân luồng xử lý giao dịch (đồng bộ và song song) trong Metanode Core. Mục tiêu là tối đa hóa TPS (Transaction Per Second) bằng cách tận dụng đa luồng (Multi-core CPU) trong khi vẫn đảm bảo tuyệt đối nguyên tắc **Không Fork (Zero-Fork Invariant)** và **Không Xung Đột Trạng Thái (Zero State Drift)**.

---

## 1. Cơ Chế Nhóm Giao Dịch (Transaction Grouping)

Để các giao dịch có thể chạy song song mà không gây ra xung đột ghi (data race) trên cùng một tài khoản (Account State), Metanode sử dụng thuật toán **Union-Find** tại `pkg/grouptxns/grouptxns.go`.

### Nguyên lý hoạt động:
1. **Phân tích địa chỉ liên quan (Related Addresses):** Mỗi giao dịch $T_i$ sẽ khai báo một mảng các địa chỉ liên quan (bao gồm `From`, `To`, và các `Contract` được gọi).
2. **Gộp nhóm (Union-Find):** 
   - Nếu $T_1$ ảnh hưởng đến địa chỉ $A$ và $B$.
   - Nếu $T_2$ ảnh hưởng đến địa chỉ $B$ và $C$.
   - Thuật toán sẽ gộp $T_1$ và $T_2$ thành **cùng một nhóm** (RelativeGroup) vì chúng cùng chia sẻ địa chỉ $B$.
3. **Tính Độc Lập:** Kết quả của bước này là một danh sách các `RelativeGroup`. Giao dịch ở nhóm 1 và nhóm 2 **hoàn toàn không chạm vào bất kỳ tài khoản chung nào**. Do đó, chúng có thể được thực thi song song (Parallel) một cách an toàn tuyệt đối.
4. **Sắp Xếp Thứ Tự Trong Nhóm:** Để đảm bảo tính nhất quán tuyệt đối giữa Leader và Replay/Sync nodes, các giao dịch trong mỗi `RelativeGroup` được sắp xếp theo thuộc tính `ID` tăng dần (tức chỉ số vị trí ban đầu trong block). Điều này bảo toàn chính xác thứ tự thực thi EVM ban đầu.

---

## 2. Phân Chia Luồng Tối Ưu Lạc Quan (Optimistic Block-STM & Union-Find)
Nơi diễn ra: `ProcessTransactionsOptimistic` (trong `tx_processor_optimistic.go`)

Kiến trúc xử lý block sử dụng mô hình kết hợp (Hybrid Model) giữa **Phân Tích Nhóm Tĩnh (Static Grouping)** và **Thực Thi Lạc Quan (Optimistic Block-STM)**. Quá trình chia làm các vòng lặp (Rounds) và pha xác thực (Validation):

### Round 1: Thực Thi Song Song Đầu Tiên (Parallel Speculative Execution)
- Hệ thống duyệt qua danh sách các nhóm tĩnh (`RelativeGroup`) đã được `grouptxns` phân tích từ trước. Thay vì chạy từng giao dịch lẻ, đơn vị thực thi là toàn bộ một **Group**. Điều này giúp các giao dịch chắc chắn conflict (ví dụ: 1000 lệnh chuyển native coin từ cùng 1 ví Funder) sẽ chạy tuần tự với nhau trong cùng 1 group ở 1 worker, ngăn ngừa thảm hoạ conflict ảo gây lãng phí CPU.
- **Preload Tối Ưu:** Toàn bộ địa chỉ liên quan (cả `FromAddress` và `ToAddress` của mọi loại giao dịch) đều được quét và đưa vào State Cache ngay từ đầu, xóa bỏ điểm thắt cổ chai Disk I/O.
- Các worker (giới hạn bằng `runtime.NumCPU()` hoặc max 16) bốc các group ra chạy song song một cách "lạc quan" (Speculative). Chúng ghi nhận Read Set và Write Set (`readAccounts`, `readStorage`, `DirtyAccounts`) thông qua `ValidationStateCache`.

### Pha Xác Thực Tuần Tự (Sequential Validation & Conflict Check)
- Đây là bước chốt chặn **Đảm bảo 100% Zero-Fork Determinism**. Hệ thống duyệt tuần tự mảng gốc `groupsToExecute` (đã được thống nhất thứ tự tĩnh trên toàn mạng).
- Thuật toán `CheckConflict` đối chiếu Read Set của group hiện tại với Write Set của tất cả các group chạy trước nó trong block. Nếu phát hiện đọc dữ liệu cũ (stale read) do group trước đó vừa thay đổi, giao dịch sẽ bị huỷ (Abort) và đưa vào hàng đợi chạy lại (re-queue).
- Các group vượt qua vòng xác thực sẽ được chấp nhận (Accepted), kết quả ghi đè (Write) được áp dụng vào Validation Cache để làm nền tảng cho các vòng sau.

### Các Vòng Lặp Sau & Khắc Phục Xung Đột Bằng Union-Find (Subsequent Rounds)
- Những group bị huỷ ở vòng trước sẽ đi vào quá trình hợp nhất (Union-Find Conflict Resolution). Các group nào xung đột dữ liệu động (ví dụ: cùng chọc vào 1 ô Storage EVM mà tĩnh học không dò ra) sẽ được `Union-Find` gom lại thành một "siêu nhóm" (Meta-group).
- Ở Round tiếp theo, các siêu nhóm này lại được các worker lấy ra chạy song song. Tuy nhiên, các giao dịch *bên trong* siêu nhóm sẽ được thực thi tuần tự hoàn toàn để đảm bảo đọc đúng trạng thái mới nhất từ những giao dịch xung đột.
- Quá trình cứ lặp lại đến khi danh sách `groupsToExecute` trống trơn (100% giao dịch hoàn tất).

### Phase Cuối: Tổng Hợp Đồng Bộ (Deterministic Merge Phase)
- **Thu thập kết quả (Gathering):** Duyệt tuần tự qua mảng kết quả cuối cùng (array order). Gộp tất cả `Transactions`, `Receipts`, và `ExecuteSCResults`.
- **Áp Dụng Trie (IntermediateRoot):** `ValidationStateCache` đẩy dữ liệu Dirty State cuối cùng xuống Global DB. Sau đó tính toán mã băm Merkle `AccountStateDB.IntermediateRoot(true)` và `StakeStateDB.IntermediateRoot(true)` một cách song song.
  > **Tối ưu hóa (Perf Optimization):** `SmartContractDB.LateBindRoots()` bắt buộc được gọi để chốt StateRoot cục bộ của Storage trước khi Trie chính thức hash.

---

## 3. Kiến Trúc Hỗ Trợ Đánh Giá Bug (Guidelines for Bug Fixes)

Khi tiến hành fix bug hoặc tối ưu TPS, hệ thống yêu cầu tuân thủ các quy tắc sau để bảo vệ **Zero-Fork Invariant**:

### 🚫 Quy tắc 1: Không Side-Effect trong Phase Song Song
- **TUYỆT ĐỐI KHÔNG** được gọi các hàm cập nhật trực tiếp state toàn cục (như `db.BatchPut`, `trie.Update`) bên trong luồng xử lý của EVM hoặc `RelativeGroup` worker. 
- Mọi thao tác làm thay đổi State đều phải được đóng gói vào object và đưa về Phase Đồng Bộ (Phase 2).

### 🚫 Quy tắc 2: Không Lock/Unlock tràn lan
- **KHÔNG SỬ DỤNG** `sync.Mutex` hay `sync.RWMutex` bên trong các hàm xử lý EVM để cố gắng đồng bộ hóa. 
- Nếu bạn cần dùng Mutex, điều đó chứng tỏ thuật toán chia nhóm (Union-Find) đã bỏ sót một `RelatedAddress` hoặc thiết kế đang tạo ra thắt cổ chai (bottleneck) không đáng có. 

### 🛡️ Quy tắc 3: Sắp xếp Deterministic
- **Sắp xếp DirtyAddresses:** Trước khi gọi `BatchUpdate` vào Trie (ví dụ `NomtStateTrie`), mảng các địa chỉ `DirtyAddresses` phải được **Sort** (ví dụ `slices.SortFunc`). Nếu không sort, do đặc tính của HashMap (Go `map` iterators are random), thứ tự insert sẽ khác nhau giữa các node dẫn đến lệch `StateRoot` -> **FORK**.
- **Sắp xếp giao dịch trong nhóm (Group Items Sorting):** Các giao dịch trong cùng một nhóm `RelativeGroup` phải được sắp xếp theo thuộc tính `ID` tăng dần (chỉ số vị trí ban đầu trong block). Không được sắp xếp theo `FromAddress`/`Nonce`/`Hash` trong pha replay vì sẽ làm lệch thứ tự thực thi so với thứ tự đề xuất ban đầu của Leader, dẫn đến lệch trạng thái EVM.

### 🛡️ Quy tắc 4: Cập Nhật Trạng Thái Tức Thời (State Sync Caching)
- Khi một Sender có nhiều TXs trong cùng một Group (cùng Nonce list liên tiếp), việc xử lý EVM phải liên tục cập nhật bộ đệm Nonce (`Sync C++ State cache`). Nếu không, EVM sẽ từ chối giao dịch thứ hai do Nonce cũ (Stale Nonce).

### ⚡ Quy tắc 5: Định Tuyến Song Song Giao Dịch Go-Only (BLS Parallel Routing)
- Các nhóm giao dịch thuần Go (Go-Only) không gọi qua C++ MVM/EVM — chẳng hạn như BLS `setBlsPublicKey` với `nonce=0` — hoàn toàn độc lập và không chạm vào `StakeStateDB`. 
- Thay vì đẩy chúng vào Worker 0 (gây thắt cổ chai cực lớn khi spam giao dịch BLS), các nhóm này cần được phân phối theo mô hình **Round-Robin** trên toàn bộ các Worker để đạt hiệu năng đa luồng tối đa mà không gây rủi ro data race.

### ⚙️ Quy tắc 6: Xử Lý Song Song Các Storage Trie Cô Lập (Parallel Trie IntermediateRoot)
- Mỗi Smart Contract sở hữu các phân vùng dữ liệu biệt lập (`TrieDatabase` riêng). Việc chạy hàm cập nhật Merkle Trie `IntermediateRoot()` cho các bảng dữ liệu này cực kỳ tốn CPU và I/O, do đó cần được chạy **song song** thông qua `sync.WaitGroup`.
- Sau khi chạy song song thu được các mã băm kết quả, việc cập nhật `TrieDatabaseMapValue` vào `AccountStateDB` chính bắt buộc phải thực hiện **tuần tự (Deterministic)** trên thread chính để ngăn chặn race condition trên `SmartContractState` map.

---

## 4. Bảng Tra Cứu Các Fork Vector Đã Biết (Known Fork Vectors & Defenses)

Dưới đây là các nguyên nhân cốt lõi từng gây fork trạng thái trong Metanode Core và các cơ chế phòng vệ kiến trúc tương ứng:

| Vector Gây Fork | Root Cause (Nguyên nhân gốc rễ) | Cơ Chế Phòng Vệ Kiến Trúc (Architecture Defense) |
| :--- | :--- | :--- |
| **Không đồng bộ Xapian Lifecycle** | Huỷ `Xapian Manager` sớm hoặc không nhất quán giữa các node khi giao dịch Smart Contract on-chain bị rollback/lỗi. | Quản lý vòng đời `Xapian DB` chặt chẽ, trì hoãn dọn dẹp cho đến khi block kết thúc. |
| **Random insertion order in MPT** | Duyệt qua Go `map` ngẫu nhiên để insert vào Merkle Patricia Trie dẫn đến cấu trúc node trung gian khác nhau. | Sắp xếp khoá (Deterministic Sort) bắt buộc bằng `slices.SortFunc` trước khi update Trie. |
| **Stale cache read during PersistAsync** | Block tiếp theo thực hiện Preload hoặc Read-Only check đọc trúng dữ liệu Trie cũ khi background writer chưa hoàn tất swap Trie. | Dùng cổng chặn `persistReady` channel kết hợp SeqLock `cacheEpoch` để trì hoãn đọc cho đến khi Trie được swap hoàn chỉnh. |
| **Concurrent write to state map** | Chạy song song `IntermediateRoot()` cho các Contract Trie mà cập nhật map `trieDatabaseMap` đồng thời. | Tách pha tính toán Root (Parallel) ra khỏi pha ghi nhận kết quả (Sequential Deterministic). |
| **Duplicate Nonce execution** | Nhiều giao dịch cùng nonce lọt vào block xử lý song song do pool verification không đồng bộ. | Kiểm tra nonce nghiêm ngặt trong `processSingleGroup` trước khi EVM chạy, reject lập tức nếu trùng lặp. |
| **Mismatched execution order** | Khác biệt về thứ tự sắp xếp giao dịch trong nhóm giữa Proposer path (`GroupAndLimitTransactionsOptimized`) và Replay path (`GroupTransactionsDeterministic`) gây lệch EVM state. | Sắp xếp giao dịch trong từng nhóm `RelativeGroup` thống nhất theo `ID` tăng dần (thứ tự xuất hiện ban đầu trong block). |

---

## 5. Tổng Kết

Kiến trúc luồng xử lý giao dịch của Metanode đạt được hiệu suất cao bằng cách:
1. **Biến vấn đề đồng bộ thành song song** bằng thuật toán nhận diện tập hợp giao nhau (Union-Find).
2. **Giam lỏng các hiệu ứng phụ (Side-effects)** vào bộ nhớ cục bộ.
3. **Thực thi các lệnh ghi nặng nề** (Update Trie, Calculate Hash, Disk Flush) ở pha gộp cuối cùng theo tuần tự toán học. 

Việc nắm vững luồng (1) Chạy máy ảo song song và (2) Cập nhật State đồng bộ là chìa khóa để triển khai các cải tiến liên quan tới Transaction Processor hay StateDB mà không vô tình tạo ra Deadlock hay Network Fork.
