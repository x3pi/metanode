# Báo Cáo Review Pull Request #23 (Metanode)

> [!CAUTION]
> **Khuyến nghị:** Reject (từ chối) Pull Request này ngay lập tức. Nếu merge, toàn bộ Execution layer của Metanode sẽ bị treo, tụt TPS (Transactions Per Second) cực kỳ nghiêm trọng, và mất hoàn toàn khả năng concurrency của hệ thống Block-STM.

Báo cáo này liệt kê 3 vấn đề kiến trúc và logic chí mạng được đưa vào qua PR #23.

---

## 1. Nút thắt cổ chai I/O cực nặng (Disk Fsync per Transaction)

Trong file `execution/pkg/mvm/linker/src/xapian/xapian_registry.cpp`, PR đã thêm lệnh `db.commit()` vào luồng ghi đệm cho từng transaction:

```cpp
// [FIX] BẮT BUỘC gọi db.commit() để lưu thay đổi xuống đĩa
try {
    std::unique_lock<std::shared_mutex> comp_lock(manager_ptr->changes_mutex);
    manager_ptr->db.commit();  // <--- LỖI CHÍ MẠNG
    manager_ptr->read_db = Xapian::Database(mvm::createFullPath(manager_ptr->address, manager_ptr->getDbName()).string());
```

> [!WARNING]
> Hàm `db.commit()` của Xapian sẽ ép hệ điều hành thực hiện lệnh `fsync` (ghi dữ liệu từ RAM xuống ổ đĩa cứng vật lý một cách đồng bộ). 

Khi xét trong chu trình của Go (phía execution engine), hàm `commitBufferForTxHash` được gọi thông qua CGO (`CommitXapianTxBuffer`) **TRONG VÒNG LẶP CHO TỪNG TRANSACTION** khi tiến hành commit một block (`block_processor_commit.go`, dòng 84). 

- **Tác động thực tế:** Nếu một block chứa 1,000 smart contract transactions có thao tác Xapian, node sẽ thực hiện fsync 1,000 lần liên tiếp. Điều này sẽ làm thời gian xử lý một block tăng từ vài mili-giây lên đến hàng giây hoặc hàng phút.
- **Cách fix đúng:** Lệnh `db.commit()` chỉ được phép gọi **1 lần duy nhất cho toàn bộ block** (sau khi đã xử lý xong toàn bộ transactions) thông qua một background thread hoặc tại cuối chu trình commit.

---

## 2. Phá vỡ hoàn toàn cơ chế song song của Block-STM (Serialization of Reads)

Trong `execution/pkg/mvm/linker/src/xapian/xapian_manager.cpp`, PR đã thay đổi cách lấy lock ở toàn bộ các hàm đọc như `get_data`, `get_value`, `get_terms`, `get_document`:

```diff
-  std::sh# Báo Cáo Review Pull Request #23 (Metanode)

> [!CAUTION]
> **Khuyến nghị:** Reject (từ chối) Pull Request này ngay lập tức. Nếu merge, toàn bộ Execution layer của Metanode sẽ bị treo, tụt TPS (Transactions Per Second) cực kỳ nghiêm trọng, và mất hoàn toàn khả năng concurrency của hệ thống Block-STM.

Báo cáo này liệt kê 3 vấn đề kiến trúc và logic chí mạng được đưa vào qua PR #23.

---

## 1. Nút thắt cổ chai I/O cực nặng (Disk Fsync per Transaction)

Trong file `execution/pkg/mvm/linker/src/xapian/xapian_registry.cpp`, PR đã thêm lệnh `db.commit()` vào luồng ghi đệm cho từng transaction:

```cpp
// [FIX] BẮT BUỘC gọi db.commit() để lưu thay đổi xuống đĩa
try {
    std::unique_lock<std::shared_mutex> comp_lock(manager_ptr->changes_mutex);
    manager_ptr->db.commit();  // <--- LỖI CHÍ MẠNG
    manager_ptr->read_db = Xapian::Database(mvm::createFullPath(manager_ptr->address, manager_ptr->getDbName()).string());
```

> [!WARNING]
> Hàm `db.commit()` của Xapian sẽ ép hệ điều hành thực hiện lệnh `fsync` (ghi dữ liệu từ RAM xuống ổ đĩa cứng vật lý một cách đồng bộ). 

Khi xét trong chu trình của Go (phía execution engine), hàm `commitBufferForTxHash` được gọi thông qua CGO (`CommitXapianTxBuffer`) **TRONG VÒNG LẶP CHO TỪNG TRANSACTION** khi tiến hành commit một block (`block_processor_commit.go`, dòng 84). 

- **Tác động thực tế:** Nếu một block chứa 1,000 smart contract transactions có thao tác Xapian, node sẽ thực hiện fsync 1,000 lần liên tiếp. Điều này sẽ làm thời gian xử lý một block tăng từ vài mili-giây lên đến hàng giây hoặc hàng phút.
- **Cách fix đúng:** Lệnh `db.commit()` chỉ được phép gọi **1 lần duy nhất cho toàn bộ block** (sau khi đã xử lý xong toàn bộ transactions) thông qua một background thread hoặc tại cuối chu trình commit.

---

## 2. Phá vỡ hoàn toàn cơ chế song song của Block-STM (Serialization of Reads)

Trong `execution/pkg/mvm/linker/src/xapian/xapian_manager.cpp`, PR đã thay đổi cách lấy lock ở toàn bộ các hàm đọc như `get_data`, `get_value`, `get_terms`, `get_document`:

```diff
-  std::shared_lock<std::shared_mutex> read_lock(changes_mutex);
+  std::unique_lock<std::shared_mutex> read_lock(changes_mutex);
```

> [!IMPORTANT]
> `std::shared_lock` cho phép nhiều luồng đọc đồng thời (Concurrent Reads) - nền tảng của cơ chế thực thi song song Block-STM. Việc ép chuyển sang `std::unique_lock` biến lock này thành *mutually exclusive* (độc quyền).

- **Tác động thực tế:** Bất kỳ hai (hay nhiều) smart contracts nào được thực thi song song mà có hành động đọc dữ liệu từ Xapian (search, get doc, get value) đều sẽ bị lock lẫn nhau, buộc phải chạy tuần tự từng cái một. Điều này giết chết hoàn toàn hiệu năng xử lý đa luồng của Block-STM.

---

## 3. Sửa lỗi sai logic (Vô nghĩa và gây hại) trong `resolveVirtualDocId`

PR đổi cách tìm document ID từ việc dùng `Xapian::Enquire` trên `read_db` sang việc check trực tiếp posting list trên `db` ghi:

```cpp
// Use the WritableDatabase 'db' instead of 'read_db' so that newly 
// added (but uncommitted) documents can be resolved in the same transaction.
Xapian::PostingIterator it = db.postlist_begin(term);
```

> [!NOTE]
> Người viết PR nghĩ rằng làm thế này để có thể thấy được doc "mới thêm vào ở transaction hiện tại nhưng chưa commit".

Tuy nhiên, cấu trúc này là sai hoàn toàn vì:
1. Nếu bạn kiểm tra lại hàm `new_document()`, trong quá trình chạy transaction đang xử lý (`txHash != nullptr`), document **mới được lưu vào bộ đệm RAM (`tx_buffers`)**, hoàn toàn chưa hề được gọi `db.add_document()`.
2. Do đó, việc query trực tiếp vào `db` cũng sẽ **không bao giờ** tìm thấy document mới sinh ra trong transaction đó.
3. Vì việc truy cập thẳng vào `db` (WritableDatabase của Xapian) không an toàn luồng, tác giả đã bị "ép" phải đổi sang dùng `unique_lock` ở mục số 2, gây ra lỗi cổ chai hệ thống.

- **Cách fix đúng:** Nếu muốn tìm được document mới chưa commit trong cùng một transaction hiện tại, bắt buộc phải duyệt tìm ngược lại trong log đệm `tx_buffers` của chính TX đó. Quá trình đọc database vẫn phải dùng `read_db` và `shared_lock` như cũ.ck(changes_mutex);
+  std::unique_lock<std::shared_mutex> read_lock(changes_mutex);
```

> [!IMPORTANT]
> `std::shared_lock` cho phép nhiều luồng đọc đồng thời (Concurrent Reads) - nền tảng của cơ chế thực thi song song Block-STM. Việc ép chuyển sang `std::unique_lock` biến lock này thành *mutually exclusive* (độc quyền).

- **Tác động thực tế:** Bất kỳ hai (hay nhiều) smart contracts nào được thực thi song song mà có hành động đọc dữ liệu từ Xapian (search, get doc, get value) đều sẽ bị lock lẫn nhau, buộc phải chạy tuần tự từng cái một. Điều này giết chết hoàn toàn hiệu năng xử lý đa luồng của Block-STM.

---

## 3. Sửa lỗi sai logic (Vô nghĩa và gây hại) trong `resolveVirtualDocId`

PR đổi cách tìm document ID từ việc dùng `Xapian::Enquire` trên `read_db` sang việc check trực tiếp posting list trên `db` ghi:

```cpp
// Use the WritableDatabase 'db' instead of 'read_db' so that newly 
// added (but uncommitted) documents can be resolved in the same transaction.
Xapian::PostingIterator it = db.postlist_begin(term);
```

> [!NOTE]
> Người viết PR nghĩ rằng làm thế này để có thể thấy được doc "mới thêm vào ở transaction hiện tại nhưng chưa commit".

Tuy nhiên, cấu trúc này là sai hoàn toàn vì:
1. Nếu bạn kiểm tra lại hàm `new_document()`, trong quá trình chạy transaction đang xử lý (`txHash != nullptr`), document **mới được lưu vào bộ đệm RAM (`tx_buffers`)**, hoàn toàn chưa hề được gọi `db.add_document()`.
2. Do đó, việc query trực tiếp vào `db` cũng sẽ **không bao giờ** tìm thấy document mới sinh ra trong transaction đó.
3. Vì việc truy cập thẳng vào `db` (WritableDatabase của Xapian) không an toàn luồng, tác giả đã bị "ép" phải đổi sang dùng `unique_lock` ở mục số 2, gây ra lỗi cổ chai hệ thống.

- **Cách fix đúng:** Nếu muốn tìm được document mới chưa commit trong cùng một transaction hiện tại, bắt buộc phải duyệt tìm ngược lại trong log đệm `tx_buffers` của chính TX đó. Quá trình đọc database vẫn phải dùng `read_db` và `shared_lock` như cũ.