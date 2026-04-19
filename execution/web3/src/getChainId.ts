import { createPublicClient, http,webSocket } from 'viem';
import { localhost } from 'viem/chains';

// Tạo client kết nối với RPC
// const client = createPublicClient({
//   chain: localhost,
//   transport: http('http://127.0.0.1:8545'), // URL node local
// });

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


async function getChainId(): Promise<void> {
  try {
    const chainId = await client.getChainId();
    console.log(`Chain ID: ${chainId}`);
  } catch (error) {
    console.error('Lỗi khi lấy Chain ID:', error);
  }
}

// Gọi hàm lấy Chain ID
getChainId();
