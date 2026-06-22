# Metanode Performance Dashboard

Chào mừng bạn đến với **Metanode Performance Dashboard**! Đây là giao diện Web hiện đại, gọn nhẹ giúp bạn theo dõi thời gian thực (Real-time telemetry) hiệu năng của các Node trong mạng lưới Metanode và tra cứu dữ liệu On-chain một cách dễ dàng thông qua Universal Explorer.

## 🌟 Các tính năng nổi bật

1. **Real-time Performance Telemetry:**
   - Hiển thị **TPS hiện tại** (Transactions Per Second).
   - Đo lường và phân tách độ trễ (Latency) qua từng giai đoạn xử lý của giao dịch:
     - *Mempool Latency*: Thời gian nằm trong hàng chờ trước khi được tạo Block.
     - *Consensus Latency*: Thời gian đồng thuận qua DAG.
     - *Execution Latency*: Thời gian thực thi Smart Contract qua EVM/MVM.
     - *End-to-End Latency*: Tổng thời gian vòng đời của 1 giao dịch.
2. **Universal Explorer:**
   - Tra cứu đầy đủ các API RPC chuẩn của Metanode và Ethereum:
     - Lấy Block mới nhất hoặc Block theo ID (`eth_blockNumber`, `eth_getBlockByNumber`).
     - Tra cứu Giao dịch và Biên lai giao dịch (`eth_getTransactionByHash`, `eth_getTransactionReceipt`).
     - Lịch sử giao dịch theo địa chỉ (`mtn_getTransactionHistoryByAddress`).
     - Tra cứu thông tin Account, Số dư (Balance), và Số lần gửi (Nonce).
     - Tra cứu thông tin Mạng (Chain ID, Network Version).
3. **Smart Connection:**
   - Tự động ghi nhớ địa chỉ cấu hình RPC Node URL nhờ `localStorage`.
   - Có cơ chế xử lý ngoại lệ (Error Handling) để báo lỗi đỏ mỗi khi Node sập hoặc có vấn đề về kết nối mạng (CORS/502 Bad Gateway).

---

## 🚀 Hướng dẫn khởi chạy

Dashboard được xây dựng dựa trên hệ sinh thái **React** và **Vite** để đem lại tốc độ siêu tốc.

### Bước 1: Yêu cầu cài đặt
Đảm bảo máy chủ hoặc máy tính của bạn đã cài đặt sẵn **Node.js** (Phiên bản v18 trở lên).
Để kiểm tra, hãy chạy lệnh:
```bash
node -v
npm -v
```

### Bước 2: Cài đặt thư viện (Dependencies)
Mở Terminal, di chuyển vào thư mục `dashboard` và chạy lệnh sau để tải về các thư viện cần thiết:
```bash
cd /home/abc/chain-n/metanode/dashboard
npm install
```

### Bước 3: Khởi chạy môi trường Phát triển (Dev Server)
Để bật Web Server, hãy chạy:
```bash
npm run dev
```
Trình duyệt sẽ cung cấp một đường link (mặc định là `http://localhost:5173`). Bạn hãy bấm vào đó để mở giao diện nhé!

### Bước 4: Khởi chạy môi trường Sản phẩm (Production)
Nếu bạn muốn đóng gói và triển khai bản chính thức (tối ưu hóa tốc độ và dung lượng lên mức cao nhất) cho Production:
```bash
npm run build
npm run preview
```
Hoặc bạn có thể mang nội dung sinh ra trong thư mục `dist/` để đặt lên các Web Server khác như Nginx, Apache, Vercel, hoặc Cloudflare Pages.

---

## ⚙️ Cấu hình RPC Node

Giao diện Web mặc định sẽ kết nối tới Node Metanode đang chạy tại địa chỉ:
`http://localhost:8545`

1. Đảm bảo Core Metanode (hoặc `rpc-client-bin`) của bạn đang hoạt động bình thường trên cổng này.
2. Nếu bạn kết nối vào Server từ xa, hãy đổi URL trực tiếp ngay trên góc phải của giao diện Web (Ví dụ: `https://rpc.metanode.co`).
3. **Lưu ý về CORS**: Dashboard của chúng ta là Frontend chạy trên trình duyệt, do vậy RPC Server của Node Metanode **bắt buộc** phải được thiết lập mở CORS `Access-Control-Allow-Origin: *` thì mới có thể nhận dữ liệu thành công.

---

Được thiết kế với phong cách Material Design + Glassmorphism mang hơi hướng công nghệ tương lai. Enjoy building on Metanode! 🚀
