# Tích Hợp Arbitrum Nitro Với Metanode (L1)

Tài liệu này tổng hợp toàn bộ các vấn đề đã gặp phải, nguyên nhân gốc rễ và các bước sửa lỗi trong quá trình cấu hình để Arbitrum Nitro chạy được Rollup trên mạng lưới L1 là **Metanode**.

---

## 1. Lỗi `Timestamp: 0` và Lỗi Reorg Init Message trên Nitro

### Hiện tượng
Khi chạy Nitro node, liên tục xuất hiện log báo lỗi không thể đồng bộ khối Genesis từ L1:
```text
cannot reorg out init message ... db-header="... BlockNumber:11 Timestamp:0 ..."
```

### Nguyên nhân
* Lớp thực thi EVM (MVM) của Metanode bị thiếu giá trị truyền vào cho `block.timestamp`.
* Hệ quả là khi hợp đồng `SequencerInbox.sol` và `Bridge.sol` của Arbitrum phát ra sự kiện `MessageDelivered`, tham số `blockTimestamp` bên trong event này bị ghi nhận là `0`.
* Khi Nitro đọc log này, nó phát hiện block Genesis có `timestamp = 0`, dẫn tới lỗi Reorg liên tục.

### Cách khắc phục
Sửa mã nguồn của Metanode tại file `metanode/execution/pkg/blockchain/tx_processor/true_block_stm.go`. Cập nhật hàm `Process` để truyền đúng tham số `blockTime` (đã được parse từ header của Rust) vào hàm khởi tạo EVM:

```go
// Trong true_block_stm.go
vmP := vm_processor.NewVmProcessor(chainState, mvmId, false, blockTime, leaderAddr)
```
*(Việc này đảm bảo `block.timestamp` trong Smart Contract luôn đồng bộ chính xác với Block Timestamp của mạng L1).*

---

## 2. Lỗi Nitro Node bị kẹt ở State cũ (Cache)

### Hiện tượng
Dù đã sửa thành công lỗi Timestamp trên mã nguồn Metanode và deploy lại contract, Nitro vẫn tiếp tục báo lỗi `Timestamp: 0`.

### Nguyên nhân
Nitro sử dụng thư mục cục bộ (LevelDB) để lưu trữ trạng thái của chuỗi Rollup. Khi L1 Metanode được wipe (xóa sạch) và chạy lại, Nitro vẫn còn lưu giữ thông tin của Genesis Block cũ (bị lỗi).

### Cách khắc phục
Bắt buộc phải xóa sạch database cục bộ của Nitro trước khi khởi động lại docker-compose:
```bash
# Xóa thư mục chứa state của chuỗi L2
rm -rf /home/abc/nhat/con-chain-v2/orbit-setup-script/config/metanode-chat-l2

# Xóa container cũ và dựng lại
docker compose stop nitro
docker compose rm -f nitro
docker compose up -d nitro
```

---

## 3. Lỗi "latest L1 block is old" (Sequencer ngưng hoạt động)

### Hiện tượng
Sau khi Nitro đọc thành công Block Genesis, nó ngưng không chịu nhận block mới và liên tục spam lỗi:
```text
ERROR [07-29|08:18:56.821] latest L1 block is old   l1Block=22 l1Timestamp=... age=6m13s
```

### Nguyên nhân
* Cơ chế bảo vệ của Arbitrum Nitro Sequencer: Nếu block L1 cuối cùng có độ trễ lớn hơn ~5 phút so với đồng hồ hệ thống (Wall-clock time), Sequencer sẽ **tạm dừng mọi hoạt động** (tránh bị chia cắt mạng - split brain).
* Metanode của bạn là mạng Private, **chỉ đào block mới khi có giao dịch**. Do đó, nếu không có ai gửi giao dịch trong 5 phút, timestamp của L1 sẽ trở nên "cũ" đối với Nitro.

### Cách khắc phục
Cần có cơ chế "Automine" (tự động đào block trống) trên L1. Trong quá trình test, tôi đã viết một script tự động gửi giao dịch rỗng (dummy tx) mỗi 5 giây vào Metanode để ép mạng này liên tục tạo block mới.

**Script `automine.ts`:**
```typescript
import { ethers } from "ethers";

async function main() {
    const provider = new ethers.providers.JsonRpcProvider("http://127.0.0.1:8545");
    const wallet = new ethers.Wallet("PRIVATE_KEY", provider);
    
    while (true) {
        try {
            const nonce = await provider.getTransactionCount(wallet.address);
            const tx = await wallet.sendTransaction({
                to: wallet.address,
                value: 0,
                nonce: nonce,
            });
            await tx.wait(1);
        } catch (e: any) { }
        await new Promise(r => setTimeout(r, 5000));
    }
}
main();
```

---

## 4. Giao dịch Nạp tiền (Deposit) L1 -> L2 không hiển thị số dư

### Hiện tượng
Khi chạy script `test_deposit.ts`, giao dịch `depositEth` thành công trên L1 (Metanode) nhưng số dư trên mạng L2 (Nitro) mãi mãi là `0.0 ETH` (bị timeout sau khi chờ).

### Nguyên nhân
Trong file cấu hình của Orbit Setup (`orbit-setup-script/config/nodeConfig.json`), có cấu hình cố ý tắt trình điều phối Sequencer:
```json
"dangerous": {
  "no-sequencer-coordinator": true
}
```
Khi cờ này bật, Nitro Sequencer sẽ **không quét Delayed Inbox**. Giao dịch nạp tiền ETH từ L1 thực chất được gửi vào Delayed Inbox, do Sequencer không quét nên giao dịch này không bao giờ được "đúc" (mint) thành công trên L2.

### Đề xuất Khắc phục
Nếu bạn muốn luồng `test_deposit.ts` chạy thành công toàn vẹn:
1. Xóa bỏ cấu hình `"no-sequencer-coordinator": true` trong `nodeConfig.json`.
2. Bật và cấu hình kết nối Redis đầy đủ cho `Sequencer Coordinator` để nó có quyền quét Delayed Inbox và tự động phân phát block L2 chứa tiền nạp.

---
**Kết luận:** Kiến trúc của Metanode **hoàn toàn tương thích** để chạy Arbitrum Nitro Rollup. Các vấn đề hiện tại chỉ liên quan đến cơ chế tự động sinh block của Private Chain và cấu hình môi trường test cục bộ của Nitro.
