# State attestation: route mở cho mọi client, không xác thực, có thể làm node tự dừng (2026-10-21)

Trạng thái: **đã sửa** trong `execution/cmd/simple_chain/processor/block_processor_attestation.go`; test: `attestation_auth_test.go`.

## Phát hiện
- [ĐỌC CODE] `routes.go:84` gắn `ProcessStateAttestation` cho **mọi** kết nối TCP; không có kiểm tra loại kết nối hay xác thực trong handler.
- [ĐỌC CODE] Chữ ký BLS chỉ được kiểm tra "nếu có" (`len(sig) > 0 && len(pubkey) > 0`); attestation không chữ ký vẫn đi tiếp vào bộ phát hiện fork.
- [ĐỌC CODE] `addAttestation` đếm theo chuỗi `NodeAddress` do người gửi tự viết và **không loại trùng**: một client gửi N bản được tính là N node.
- [ĐỌC CODE] `countConnectedPeers()` đếm các kết nối **tự khai** loại `master`/`child_node` qua `InitConnection` (`ProcessInitConnection` không xác thực loại). Kẻ tấn công tự mở 2 kết nối khai `master` để vượt điều kiện `totalPeers >= 2`.
- [ĐỌC CODE] Khi đạt ngưỡng (`disagree >= 2/3 * (1 + totalPeers)`), sau 10 giây node tính lại hash cục bộ; hash giả luôn khác nên đi tới `fatal.Exit("CONFIRMED FORK ...")`.
- Hệ quả (suy ra từ các điểm trên, chưa chạy end-to-end vì sẽ làm chết tiến trình test): một client ẩn danh có thể dừng một validator từ xa; làm với mọi validator thì dừng cả mạng (liveness, không phải fork). Đã ghi nhận: không ảnh hưởng tính đúng đắn của state.
- [ĐO] Trước khi sửa, 3 test mới FAIL: 50 attestation không chữ ký với 50 `NodeAddress` giả đều được ghi nhận; chữ ký đúng từ khóa không phải validator được ghi nhận; một validator gửi 5 lần dưới địa chỉ giả mạo được đếm 5 lần và hash mâu thuẫn thứ hai cũng được ghi nhận. Sau khi sửa: PASS.
- Các key `state_att:<block>:peer:<NodeAddress>` ghi xuống đĩa cũng theo chuỗi người gửi chọn (tăng dung lượng không giới hạn); sau khi sửa chỉ validator đã xác thực mới được ghi.

## Sửa
1. `authenticateAttestation`: bắt buộc chữ ký + khóa công khai; địa chỉ suy ra từ khóa (`Keccak(pub)[12:]`) phải là validator trong stake DB (kiểm tra rẻ trước), rồi mới xác minh BLS; `NodeAddress` được gán lại từ khóa đã xác minh.
2. `addAttestation`: một định danh chỉ một phiếu cho mỗi block (phiếu đầu thắng).

## Giới hạn / chưa xác minh
- [ĐỌC CODE] Trong `cmd/simple_chain` không còn chỗ nào dựng kết nối master/child giữa các validator (libp2p đã gỡ), nên attestation giữa các validator trung thực thực tế không được trao đổi; chi phí vận hành của bản sửa gần như bằng 0. [CHƯA ĐO] trên cụm thật.
- Bản sửa yêu cầu địa chỉ suy ra từ khóa BLS ký (`config.Databases.BLSPrivateKey`) là địa chỉ validator trong stake DB. Cấu hình mẫu `cmd/simple_chain/config.json` không thỏa điều này (địa chỉ suy ra khác địa chỉ validator trong genesis); nếu một triển khai thật bật trao đổi attestation, cần đặt hai thứ này khớp nhau hoặc mở rộng điều kiện.
- Đây là cơ chế phát hiện ngoài consensus: không đổi state, không cần triển khai đồng loạt.
