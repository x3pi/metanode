# Khởi chạy & Thiết lập Môi trường

Để mô phỏng một ván cờ Caro giữa 2 mạng máy chủ, hãy mở 2 cửa sổ Terminal độc lập trỏ vào cùng thư mục `tictactoe-cli` này.

### Yêu cầu tiên quyết
Bạn phải đang chạy:
1. MetaNode Blockchain (Bật `run.sh` lên).
2. Observer Node (Để bắt tín hiệu Event và gọi Bridge qua Gateway).

---

## 💻 Terminal 1: Đóng vai Player X (Mạng 1)
Bước này giả lập người chơi thứ nhất đang mở ví trên **Chain 1**.

1. Mở cửa sổ Terminal 1, trỏ vào thu mục:
   ```bash
   cd cmd/observer/tool/tictactoe-cli
   ```
2. Khởi động Tool:
   ```bash
   # Nếu .env mặc định của bạn đã trỏ vào HTTP_URL của Network 1 (vd: Node ở Port 8545)
   go run main.go
   ```
3. Bấm `1` để Deploy Smart Contract bàn cờ Caro.
4. **⚠️ Lưu ý:** Lấy giấy bút lưu lại dòng có ghi `Đã deploy thành công tại địa chỉ: 0x...` (Đây là địa chỉ game của bạn trên Chain 1). Tool sẽ tự động khóa địa chỉ này để tương tác trong các ván tới.

---

## 💻 Terminal 2: Đóng vai Player O (Mạng 2)
Bước này giả lập người chơi số 2 đang mở ví tại **Chain 2**.

1. Cùng lúc đó, tại Terminal 2, trỏ vào cùng thư mục:
   ```bash
   cd cmd/observer/tool/tictactoe-cli
   ```
2. Khởi động tool ép trỏ sang máy chủ RPC của Chain 2 (ví dụ Port của Node mạng khác hoặc sub-node). *Thay `192.168.1.100:9001` bằng địa chỉ thật của Chain bạn*:
   ```bash
   HTTP_URL=http://<IP-Chain-2>:<Port> go run main.go
   ```
3. Bấm `1` để Deploy Smart Contract tại Chain 2.
4. **⚠️ Lưu ý:** Cũng copy lại cái địa chỉ `0x...` vừa sinh ra bên này (Đây là địa chỉ của hệ thống mạng 2).

---

## ⚔️ Bắt đầu Trận Chiến Xuyên Blockchain

Bây giờ bạn đã có 2 bản Cop py Contract nằm ở 2 thế giới tách biệt! Đã đến lúc đồng bộ chúng với nhau qua Observer.

**Bước 1: Player X (Terminal 1) gửi thư thách đấu:**
- Tại cửa sổ Terminal 1, bấm `3` (Khởi tạo Game).
- Màn hình hỏi `Chain ID đối thủ`: Nhập chính xác Chain ID của cái Mạng thứ 2.
- Màn hình hỏi `Địa chỉ Contract tại chain đối thủ`: Dán cái địa chỉ `0x...` mà bạn copy được ở bước Deploy của Terminal 2 vào đây.
- Nhấn Enter. Hệ thống sẽ phát nổ súng tạo sự kiện gửi đi!
- Qua Terminal 2 bấm `5` để theo dõi bàn cờ. Nếu thấy chữ `Lock đối thủ: <Địa chỉ của Chain 1>` hiện lên thì chúc mừng! Observer đã ném thông điệp sang thành công.

**Bước 2: X-O Đối Trận:**
- Do Player X luôn đi trước, tại **Terminal 1**, bấm `4`. Gõ phím `4` để tích 1 chữ X vào đúng vị trí Trung Tâm của bàn Caro 3x3. Chờ 3 giây...
- Tại **Terminal 2**, bấm quyền `4` thử ăn gian gõ dấu O xem sao? Do trái trình tự, Contract xịn của MetaNode sẽ chửi ngay: "Revert: Không phải lượt của O!".
- Sang **Terminal 2**, bấm `5` (Xem bàn cờ), bạn sẽ tận mắt thấy dấu chữ X đã nổi lềnh bềnh ở ô chính giữa dù bạn chưa hề thao tác bằng tay mạng này!
- Giờ tới lượt thật, Terminal 2 hãy bấm `4` và gõ số `0` (Góc trái trên).

Cứ luân phiên như vậy đánh cho đến khi có một hàng chéo / dọc / ngang 3 con liền kề nhau được hình thành. Contract sẽ tự nhận diện Auto Win hoặc Tie và đóng băng mọi nước cờ còn lại bằng cách set `winner`! 🏆
