# Giao Diện React Đăng Ký BLS Key

Dự án này cung cấp một giao diện người dùng để đăng ký khóa riêng BLS (Boneh–Lynn–Shacham) với một dịch vụ RPC backend. Nó sử dụng MetaMask để kết nối ví và ký tin nhắn.

---

## 🚀 Bắt đầu

Làm theo các hướng dẫn sau để cài đặt và chạy dự án trên máyロー컬 của bạn cho mục đích phát triển và kiểm thử.

### Điều kiện tiên quyết

* **Node.js và npm (hoặc yarn):** Đảm bảo bạn đã cài đặt Node.js, bao gồm cả npm. Bạn có thể tải về từ [nodejs.org](https://nodejs.org/). Hoặc bạn có thể sử dụng yarn.
* **MetaMask:** Một trình duyệt web đã cài đặt tiện ích MetaMask.
* **Dịch vụ Go Backend RPC:** Một phiên bản đang chạy của dịch vụ Go backend RPC mà giao diện này sẽ tương tác. URL mặc định là `http://localhost:8545`.

### Cài đặt & Chạy máy chủ phát triển

1.  **Clone repository (nếu bạn chưa làm):**

2.  **Cài đặt các dependencies (thư viện phụ thuộc):**
    Mở terminal của bạn trong thư mục gốc của dự án và chạy:
    ```bash
    npm install
    ```
    hoặc nếu bạn dùng yarn:
    ```bash
    yarn install
    ```

3.  **Chạy máy chủ phát triển:**
    Sau khi các dependencies được cài đặt, bạn có thể khởi động máy chủ phát triển Vite:
    ```bash
    npm run dev
    ```
    hoặc với yarn:
    ```bash
    yarn dev
    ```
    Lệnh này thường sẽ khởi động ứng dụng trên `http://localhost:5173` (cổng mặc định của Vite, nhưng có thể thay đổi nếu cổng đó đang được sử dụng). Kiểm tra output trên terminal của bạn để biết URL chính xác.

4.  **Mở trên trình duyệt:**
    Mở trình duyệt web của bạn và truy cập vào URL được cung cấp bởi máy chủ phát triển.

---

## ⚙️ Cấu hình

### Thay đổi URL Backend RPC

Giao diện người dùng cần biết URL của dịch vụ Go backend RPC của bạn. Thông tin này được cấu hình thông qua một hằng số trong tệp `src/App.tsx`.

1.  **Xác định vị trí tệp:**
    Mở tệp `src/App.tsx` trong trình soạn thảo mã của bạn.

2.  **Tìm hằng số:**
    Gần đầu tệp, bạn sẽ tìm thấy dòng sau:
    ```typescript
    // URL của backend Go RPC.
    const GO_BACKEND_RPC_URL = 'http://localhost:8545'; // RPC endpoint bạn muốn gọi
    ```

3.  **Sửa đổi URL:**
    Thay đổi chuỗi `'http://localhost:8545'` thành URL thực tế mà dịch vụ Go backend RPC của bạn đang lắng nghe. Ví dụ, nếu backend của bạn đang chạy trên `http://127.0.0.1:8080`, bạn sẽ thay đổi nó thành:
    ```typescript
    const GO_BACKEND_RPC_URL = '[http://127.0.0.1:8080](http://127.0.0.1:8080)'; // RPC endpoint bạn muốn gọi
    ```

4.  **Lưu tệp:**
    Sau khi thực hiện thay đổi, hãy lưu tệp `src/App.tsx`. Nếu máy chủ phát triển đang chạy, nó sẽ tự động tải lại với cấu hình mới.

---

## 🛠️ Điểm nổi bật về Cấu trúc Dự án

* `src/main.tsx`: Điểm vào của ứng dụng React.
* `src/App.tsx`: Component ứng dụng chính, chứa logic để tương tác với MetaMask, ký và giao tiếp với backend.
* `src/index.css`: Các style chung cho toàn bộ ứng dụng.
* `src/App.css`: Các style cụ thể cho component (mặc dù một số style có thể được xử lý bởi các class Tailwind CSS trực tiếp trong `App.tsx`).
* `src/vite-env.d.ts`: Các khai báo TypeScript cho biến môi trường Vite và các đối tượng window toàn cục như `window.ethereum`.

---

## 📜 Tổng quan Chức năng

Ứng dụng cho phép người dùng:
1.  **Kết nối ví MetaMask của họ:** Nếu chưa kết nối, người dùng có thể kết nối ví của mình. Ứng dụng lắng nghe các thay đổi tài khoản.
2.  **Nhập Khóa riêng BLS:** Người dùng nhập khóa riêng BLS của họ ở định dạng thập lục phân (hexadecimal).
3.  **Ký và Gửi:**
    * Ứng dụng xây dựng một tin nhắn chứa khóa riêng BLS và một dấu thời gian hiện tại.
    * Người dùng ký tin nhắn này bằng tài khoản MetaMask đã kết nối của họ.
    * Địa chỉ Ethereum gốc, khóa riêng BLS, dấu thời gian và chữ ký đã tạo sau đó được gửi dưới dạng một yêu cầu JSON-RPC đến `GO_BACKEND_RPC_URL` đã cấu hình.
    * Phương thức được gọi trên backend là `rpc_registerBlsKeyWithSignature`.
4.  **Hiển thị Trạng thái và Lỗi:** Giao diện người dùng cung cấp phản hồi về trạng thái của các hoạt động (kết nối, ký, gửi) và hiển thị bất kỳ lỗi nào gặp phải.

Đảm bảo backend Go của bạn đang chạy và có thể truy cập được tại `GO_BACKEND_RPC_URL` đã chỉ định để ứng dụng hoạt động chính xác.