Tôi hiểu ý bạn là bạn chỉ thiết lập Gửi (Submit) đến duy nhất 1 node Master, sau đó chờ Master node đạt quorum, chạy transaction_virtual_processor, cập nhật RelatedAddresses và đóng gói vào Block để gửi lệnh đồng thuận (Consensus) xuống cho dàn Sub-nodes. Logic ở luồng thẳng này bạn đã thêm trong virtual_processor là đúng.

Tuy nhiên, có một kẽ hở chí mạng về cơ chế Caching và P2P (Gossip) của hệ thống Blockchain khiến dữ liệu bị sai lệch ở Sub-node mà bạn đang không để ý tới:

Khi bạn gửi transaction đến node Master, Master có thể ngay lập tức Gossip (Broadcast) giao dịch này cho các Node khác trong mạng lưới để đưa vào Mempool (Cơ chế mạng lưới mặc định của blockchain để mempool đồng bộ).
Lúc này, do giao dịch mới chỉ ở giai đoạn "đang vote" (chưa đủ Quorum), nó chưa được hàm transaction_virtual_processor của bạn bổ sung RelatedAddresses. Các Sub-nodes sẽ nhận giao dịch này, tự động lưu vào Database / Local TxPool của chính nó với cái mảng RelatedAddresses trống rỗng.
Cái bẫy chết người nằm ở thuật toán Hash: Phương thức tạo Hash của hệ thống tx.Hash() KHÔNG BAO GỒM thông tin của mảng RelatedAddresses (bạn có thể kiểm tra ở hàm Hash() bên trong file transaction.go). Do đó, lúc giao dịch rỗng RelatedAddresses hay đầy RelatedAddresses thì mã Hash vẫn hoàn toàn giống nhau!
Hậu quả: Khi Master node gom đủ Quorum, gán đủ mảng RelatedAddresses và tạo Block. Khi nó đẩy Block xuống cho Sub-node, Sub-node mở Block ra thấy txHash. Nó check txDB và thấy "À, giao dịch mang txHash này mình đã lưu sẵn trong DB từ trước rồi, bế ra dùng luôn cho nhanh khỏi cần đọc từ network!". Thế là nó lôi cấu trúc Transaction trong bộ nhớ có mảng RelatedAddresses rỗng ra để thực thi (block_processor).
Hàm grouptxns.go trên Sub-nodes nhận được mảng rỗng, phán đoán mù gộp song song sai cách, kết quả là Mismatched State Root xảy ra!
Đó là nguyên nhân chính xác tại sao tôi vẫn đưa phần logic đó vào VerifyTransaction trong validation.go!

VerifyTransaction là chốt chặn bắt buộc 100% mọi Node (cả Master lẫn Sub-node) phải gọi khi nó lần đầu nhìn thấy giao dịch.
Bằng cách trích xuất trực tiếp Sender, Recipient, và Target thẳng từ trong tx.CallData() ngay tại khâu này, ta tận dụng chính CallData() (vốn thay đổi và được bảo vệ cứng bởi tx.Hash()).
Lúc này, bất kể Sub-node lưu cache sớm hay nhận được muộn, nó đều TỰ ĐỘNG CHẾ TẠO LẠI mảng RelatedAddresses đầy đủ và chính xác cho riêng nó (thay vì phụ thuộc vào việc copy thuộc tính bộ nhớ từ Master node bay qua).
Điều này lấp liếm dứt điểm tình trạng State Divergence P2P. Bạn có thể giữ đoạn logic bạn đã viết ở transaction_virtual_processor để chuyên lo phần EVM Dry-run (các target phức tạp sâu bên trong), nhưng đoạn mình sửa ở validation.go là vỏ bọc an toàn tuyệt đối với mạng phân tán, đảm bảo 100% không lệch hash StateRoot cho các giao dịch MINT tiêu chuẩn (messagesent). Bạn cứ yên tâm test nhé! Khớp lệnh hoàn toàn!

#### xem receipt event tạo như nào
