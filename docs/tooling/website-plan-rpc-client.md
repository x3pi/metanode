## Website dựa trên rpc-client: 2 vai trò (User thường, Admin), 2 luồng ký (co-sign & tự ký)

Tài liệu developer mô tả mục tiêu, kiến trúc, xác thực bằng ví (MetaMask/WalletConnect), hai vai trò người dùng, luồng thực thi L1, mô hình 2 chữ ký ETH+BLS, ECDH với bên thứ ba, và tích hợp với `cmd/rpc-client`.


### 1) Mục tiêu (Layer 1)

- Tránh server giả lập giao dịch.
- Kỹ thuật: 1 account, 2 chữ ký (ETH + BLS).
- Tất cả giao dịch cần co-sign 2 chữ ký mới chấp nhận.
- Nếu user không ký, mọi giao dịch server đẩy đều bị từ chối on-chain.
- Mọi giao dịch hiển thị trên explorer.
- Bảo mật dữ liệu với bên thứ ba bằng ECDH (server không thấy nội dung).


### 2) Loại người dùng và đăng nhập ví

- User thường:
  - Đăng nhập bằng ví (MetaMask/WalletConnect).
  - Đăng ký tài khoản on-chain (mapping địa chỉ ↔ profile).
  - Xem lịch sử giao dịch của chính mình.
  - Thực hiện giao dịch theo Luồng 1 (co-sign) mặc định; tùy chọn Luồng 2 nếu được cấp quyền BLS.
- Admin:
  - Đăng nhập bằng ví.
  - Xem danh sách các user do mình quản lý (quan hệ admin→user lưu on-chain/off-chain có xác thực).
  - Xem lịch sử giao dịch của các user được ủy quyền.
  - Cấu hình policy: nạp ME, bật `accountType = 2`, đăng ký/thu hồi BLS pubkey, hạn mức.


### 3) Tổng quan kiến trúc

- Website (Frontend):
  - Kết nối ví trình duyệt; ký ETH (EIP-712/tx).
  - Luồng 1: gửi yêu cầu ký hộ BLS tới server.
  - Luồng 2: ký BLS cục bộ (nếu được cấp và bật).
  - UI lịch sử giao dịch (theo địa chỉ), UI quản trị (Admin).
- RPC Client (@rpc-client, tham chiếu `cmd/rpc-client`):
  - Tạo tham số giao dịch, canonical message, quản lý private key ETH (nếu chạy desktop client), call RPC.
  - Bridge sang Web (TypeScript) nếu cần để dùng trong frontend.
- RPC Server ký hộ:
  - Xác thực địa chỉ ví (session liên kết chữ ký).
  - Ký BLS theo policy co-sign, broadcast nếu hợp lệ.
  - API cho Admin quản trị accountType, BLS pubkey, nạp ME.
- L1 + Smart Contract:
  - Ràng buộc `accountType = 2` yêu cầu đủ ETH+BLS.
  - Xác minh quyền Admin với user và các hành động quản trị.
  - Sổ cái cho explorer.


### 4) Luồng thực thi L1 (bắt buộc 2 chữ ký)

- Hợp đồng/logic L1 kiểm tra:
  - `sig_eth` khớp địa chỉ user.
  - `sig_bls` khớp BLS pubkey đã đăng ký cho account/policy.
  - `accountType = 2` bắt buộc đủ chữ ký co-sign.
  - Nonce/deadline chống replay, role-based access cho một số hàm.


### 5) Hai luồng giao dịch

- Luồng 1 (co-sign, server tài trợ phí nếu cần):
  - User ký ETH trên message chuẩn (EIP-712/tx).
  - Gửi lên RPC server ký hộ BLS; server xác thực quyền→ký BLS→broadcast.
  - Server nạp ME và bật `accountType = 2` cho user thuộc phạm vi quản lý.
  - Ví dụ: khách thường của Vietcombank; bank tài trợ phí và đồng ký.
- Luồng 2 (tự ký, user tự trả phí):
  - User giữ cả khóa ETH + BLS.
  - User gọi hàm contract trực tiếp, tự trả phí.
  - Ví dụ: khách VIP tự quản trị ví, số dư lớn.


### 6) Vai trò & quyền

- User thường:
  - Quyền thực thi giao dịch thuộc phạm vi tài khoản của mình.
  - Luồng mặc định: 1 (co-sign). Có thể dùng Luồng 2 nếu đã thiết lập BLS cục bộ.
  - Xem lịch sử giao dịch của chính mình.
- Admin:
  - Quản lý danh sách user (theo domain tổ chức).
  - Xem lịch sử của user trong phạm vi quản lý.
  - Thao tác quản trị: đăng ký/thu hồi BLS pubkey, bật `accountType = 2`, nạp ME, đặt hạn mức và policy ký hộ.


### 7) Mã hóa đầu-cuối với bên thứ ba (ECDH)

- Hai phía (user ↔ đối tác/SM) trao đổi public key, tạo shared secret (ECDH), dẫn xuất khóa phiên (HKDF), mã hóa AES‑GCM.
- Server chỉ chuyển tiếp ciphertext; explorer lưu hash/commitment nếu cần đối soát.
- Frontend hỗ trợ tạo/nhập public key và mã hóa/giải mã cục bộ.


### 8) API đề xuất (REST/JSON-RPC)

- POST /auth/nonce → GET /auth/proof: đăng nhập bằng ví (ký nonce).
- GET /me/profile: lấy profile, roles, accountType, blsPub.
- GET /me/txs?page=:paging: lịch sử giao dịch theo địa chỉ.
- POST /cosign/bls: { message, sig_eth, meta{nonce,chainId,deadline,gas} } → { sig_bls, tx_hash }.
- POST /tx/broadcast: { raw_tx | fields + sig_eth + sig_bls } → { tx_hash }.
- GET /admin/users: danh sách user quản lý.
- GET /admin/users/:addr/txs: lịch sử giao dịch user.
- POST /admin/account/:addr/config: bật `accountType=2`, set blsPub, hạn mức.
- POST /admin/fund/:addr: nạp ME/ETH (tuỳ chain).
- POST /ecdh/pubkey: đăng ký/trao đổi public key.

Gợi ý ánh xạ vào `cmd/rpc-client`: tái dùng cấu trúc trong `transaction_args.go`, quản lý khoá trong `privatekey_store.go`, logic RPC ở `main.go` (trích xuất thành module).


### 9) UI chính

- Đăng nhập bằng ví; trang tổng quan tài khoản (accountType, BLS, số dư).
- Tạo & ký giao dịch: Luồng 1 (Ký ETH → Gửi ký hộ BLS), Luồng 2 (Ký ETH+BLS).
- Explorer mini: danh sách giao dịch, trạng thái, liên kết explorer.
- ECDH: quản lý khóa, mã hóa/giải mã mẫu.
- Admin: danh sách user, lịch sử từng user, cấu hình account/policy.


### 10) Quy trình triển khai

- P1: Đăng nhập bằng ví + EIP‑712 + RPC ký hộ BLS + contract kiểm 2 chữ ký.
- P2: Website Luồng 1 (co-sign) + explorer mini + trang Admin cơ bản.
- P3: Website Luồng 2 (tự ký BLS) + tự trả phí.
- P4: ECDH end‑to‑end + công cụ trao đổi khóa.


### 11) Tiêu chí chấp nhận

- Giao dịch chỉ thành công khi có đủ ETH + BLS.
- Thiếu chữ ký user → server không thể đẩy giao dịch thành công.
- User thường xem được lịch sử của mình; Admin xem được lịch sử user thuộc phạm vi quản lý.
- Explorer hiển thị đầy đủ; ECDH che giấu nội dung với server.


### 12) Ghi chú kỹ thuật

- Khuyến nghị EIP‑712 cho message canonical; ràng buộc nonce/deadline chống replay.
- BLS triển khai đồng nhất với verifier on-chain (WASM/Native).
- API ký hộ đặt rate‑limit/audit; bắt buộc xác thực ví trước khi ký.
- Cân nhắc module TypeScript hoá `rpc-client` cho frontend.


