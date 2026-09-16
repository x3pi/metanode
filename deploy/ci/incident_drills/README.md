# Incident Drills

Kịch bản diễn tập sự cố vận hành — khác với `deploy/ci/ci_config.yaml`'s test
matrix (kiểm chứng ứng dụng đúng logic), các script ở đây kiểm chứng **quy
trình phản ứng sự cố thật có hoạt động không**: cảnh báo có tới người trực
không, node hỏng thật có tự phục hồi không, cụm có sống sót khi mất mạng
không.

## Danh sách drill

| Script | Kịch bản | Mức xâm lấn | An toàn chạy khi nào |
| :--- | :--- | :--- | :--- |
| `drill_telegram_alert.sh` | Cảnh báo Telegram có tới người trực không | Thấp — chỉ gửi 1 tin nhắn test, không đụng cluster | Bất cứ lúc nào |
| `drill_disk_full.sh` | Đĩa đầy giữa lúc vận hành | Cao — ghi file thật chiếm dung lượng thật | Chỉ trong cửa sổ bảo trì, không ai dùng máy |
| `drill_node_full_resync.sh` | Node hỏng thật, không dùng snapshot, phải tự đồng bộ lại 100% qua P2P | Cao — xoá dữ liệu thật 1 node | Chỉ trong cửa sổ bảo trì, cụm còn lại phải khoẻ |
| `drill_network_partition.sh` | Mất kết nối mạng giữa 1 host và phần còn lại của cụm | Cao — chặn traffic thật (chỉ port metanode, không đụng SSH) | Chỉ trong cửa sổ bảo trì |

## Nguyên tắc an toàn chung (3 drill "Cao")

- **Mặc định là dry-run.** Không có cờ `--confirm` thì KHÔNG script nào động vào
  disk/process/network thật — chỉ in ra kế hoạch sẽ làm gì.
- **Pre-flight check từ chối chạy nếu không an toàn** — ví dụ `drill_disk_full.sh`
  từ chối nếu disk đã > 80%, `drill_node_full_resync.sh`/`drill_network_partition.sh`
  từ chối nếu cụm không đủ quorum để chịu được việc đó.
- **Dọn dẹp 2 lớp độc lập** (defense in depth): `trap ... EXIT INT TERM` trong
  chính script, CỘNG một watchdog nền độc lập (chạy tách rời, `disown`) tự dọn
  sau một khoảng trần cứng — để nếu phiên SSH điều khiển bị rớt giữa chừng,
  hệ thống vẫn tự phục hồi mà không cần ai can thiệp thủ công.
- **Không chạy khi có người khác đang dùng máy/cụm** — cả 3 drill "Cao" đều ảnh
  hưởng tới trạng thái thật của cụm (disk, dữ liệu node, kết nối mạng), có thể
  làm gián đoạn công việc của người khác nếu chạy khi họ đang thao tác.

## Thứ tự khuyến nghị khi diễn tập lần đầu

1. `drill_telegram_alert.sh` trước tiên — nếu cảnh báo không tới được, mọi drill
   sau đó (dù phát hiện đúng sự cố) cũng vô nghĩa vì không ai biết để xử lý.
2. `drill_node_full_resync.sh` — xác nhận đường P2P tự phục hồi hoạt động, đây
   là "lưới an toàn cuối cùng" khi snapshot không dùng được.
3. `drill_disk_full.sh` — xác nhận cảnh báo tài nguyên hoạt động trước khi cần
   thật (đĩa đầy thường có dấu hiệu báo trước, không như node crash đột ngột).
4. `drill_network_partition.sh` — kịch bản phức tạp nhất, nên làm cuối cùng khi
   đã tin tưởng các cơ chế cơ bản hoạt động đúng.

Sau lần đầu, nên đưa vào lịch định kỳ (vd hàng quý) chứ không chỉ chạy một lần
rồi quên — cấu hình hạ tầng thay đổi (bot token hết hạn, threshold đổi, người
trực đổi) mà không diễn tập lại thì drill cũ không còn phản ánh đúng thực tế.
