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

## 2. Phân Chia Luồng Đồng Bộ (Sync) và Song Song (Parallel)

Kiến trúc xử lý block (được đặt trong `ProcessTransactions`) chia làm 2 giai đoạn (Phase) rõ rệt:

### Phase 1: Thực Thi Song Song (Parallel Execution Phase)
Nơi diễn ra: `executeGroupsParallel` (trong `tx_processor.go`)

- **Worker Pool:** Metanode sử dụng một pool các goroutines thay vì tạo mới liên tục (Zero-allocation parallel processing qua `sync.Pool`). Các `RelativeGroup` độc lập được đẩy vào các channel để các worker xử lý.
- **Thực thi cô lập (Isolated Execution):** 
  - Trong quá trình chạy EVM, các worker chỉ **đọc** (Read) dữ liệu từ State Trie (trie chung).
  - Khi cần **ghi** (Write), thay vì ghi đè trực tiếp lên DB gây lock contention, dữ liệu mới sẽ được ghi vào một bản ghi tạm thời (`DirtyAccounts` / `ValidatorState` nội bộ của group).
- **Nguyên lý song song (Địa chỉ liên quan quyết định):** Việc chạy song song hay tuần tự **hoàn toàn phụ thuộc vào địa chỉ liên quan (Related Addresses) của giao dịch chứ không phụ thuộc vào loại giao dịch (Transfer hay EVM)**:
  - Bất kỳ giao dịch nào (Transfer, Smart Contract, hay System) nếu không chia sẻ địa chỉ liên quan với các giao dịch khác sẽ được Union-Find xếp vào các nhóm khác nhau và chạy song song.
  - Ngược lại, nếu có chung địa chỉ liên quan (ví dụ: cùng địa chỉ gửi `From`, nhận `To`, hoặc ví gọi hợp đồng), chúng sẽ bị gộp vào cùng một nhóm để thực thi tuần tự nhằm bảo toàn tính toàn vẹn trạng thái.
  - **Trường hợp ngoại lệ (Địa chỉ hệ thống dùng chung / Dispatch point):** Các địa chỉ ảo đóng vai trò định tuyến hoặc đăng ký hệ thống nhưng không thay đổi trạng thái nội bộ của chính địa chỉ đó (ví dụ: `ACCOUNT_SETTING_ADDRESS_SELECT` dùng đăng ký BLS, hay `VALIDATOR_CONTRACT_ADDRESS` dùng stake) sẽ được **loại bỏ (exclude) khỏi mảng gom nhóm của Union-Find**. Điều này ngăn việc toàn bộ giao dịch hệ thống bị dồn về một luồng tuần tự duy nhất, cho phép chúng chạy song song an toàn (do chỉ thay đổi trạng thái trên tài khoản riêng của sender).
- **Xử lý đặc biệt trong pha song song:**
  - **Giao dịch Read-Only:** Được tách vào các nhóm riêng biệt để tối ưu hóa đọc song song.
  - **Giao dịch Cross-Chain / System (Ví dụ: SetBlsPublicKey):** Áp dụng kỹ thuật "Batch Mutation". Việc cập nhật DB thực tế được trì hoãn và tổng hợp ghi nhận ở Phase 2 (Dirty State Merge).

### Phase 2: Tổng Hợp Đồng Bộ (Deterministic Merge Phase)
Nơi diễn ra: Nửa sau của `ProcessTransactions` (Sau `wg.Wait()`)

Đây là pha **bắt buộc chạy đồng bộ (Synchronous)** để đảm bảo State Root sinh ra là hoàn toàn giống nhau (Deterministic) trên mọi node.

- **Thu thập kết quả (Gathering):** Duyệt tuần tự qua mảng kết quả của các nhóm (array order). Gộp tất cả `Transactions`, `Receipts`, và `ExecuteSCResults`.
- **Apply Dirty States:** Các trạng thái (State) bị thay đổi trong quá trình tính toán song song (nằm ở `gRs.DirtyAccounts`) giờ mới được cập nhật thực sự vào `AccountStateDB` và `StakeStateDB`.
- **Cập nhật Trie (IntermediateRoot):** Sau khi các thay đổi được map vào bộ nhớ, `AccountStateDB.IntermediateRoot(true)` và `StakeStateDB.IntermediateRoot(true)` sẽ tính toán mã băm Merkle (Hash).
  > **Tối ưu hóa (Perf Optimization):** Mặc dù bước tổng hợp là đồng bộ, nhưng việc tính toán mã băm cho AccountTrie và StakeTrie có thể được chạy song song với nhau (`irWg.Add(2)`), do chúng lưu trữ ở hai cơ sở dữ liệu/trie hoàn toàn độc lập.

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
