import { createPublicClient, http,webSocket } from 'viem';
import { localhost } from 'viem/chains';

// Tạo client kết nối với rpc
const WSS_URL = 'wss://rpc-proxy-sequoia.ibe.app:8446';

const client = createPublicClient({
  chain: localhost, // Chỉ định chain (ví dụ: mainnet)
  transport: webSocket(WSS_URL, {
      // Tùy chọn thêm cho WebSocket nếu cần, ví dụ timeout:
      // timeout: 10_000, // 10 giây
      // Tự động kết nối lại khi mất kết nối (mặc định là true)
       retryCount: 3, // Số lần thử kết nối lại
       retryDelay: 1000 // Độ trễ giữa các lần thử (ms)
  }),
  // Có thể cấu hình thêm pollingInterval nếu cần thiết cho 1 số chức năng khác
  // pollingInterval: 4_000,
});

// Theo dõi sự thay đổi của số block mới nhất
// ... existing code ...
// Theo dõi sự thay đổi của số block mới nhất
let blockCount = 0; // Biến đếm số lần cập nhật block
const unwatch = client.watchBlockNumber({ // gán unwatch cho hàm watchBlockNumber
  onBlockNumber: (blockNumber: bigint) => {
    console.log(`Block Number Updated: ${blockNumber}`);
    blockCount++;
    if (blockCount >= 5000) {
      unwatch(); // Dừng theo dõi sau 5 lần
    }
  },
  onError: (error) => {
    console.error('An error occurred while watching block number:', error);
  },
  poll: true, // Kích hoạt chế độ polling
  pollingInterval: 300, // Kiểm tra mỗi 3 giây
});