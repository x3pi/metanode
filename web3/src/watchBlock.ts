import { createPublicClient, http } from 'viem';
import { localhost } from 'viem/chains';

// Tạo client kết nối với rpc
const client = createPublicClient({
  chain: localhost,
  transport: http('ws://localhost:8545'), // URL node local
  // transport: http('wss://rpc-proxy-sequoia.ibe.app:8446'), // URL node local
});

// Theo dõi sự thay đổi của block và lấy thông tin chi tiết
let blockCount = 0;
let unwatch = client.watchBlocks({
  onBlock: (block) => {
    console.log('New Block:', block);
    blockCount++;
    if (blockCount >= 5) {
      unwatch(); // Dừng theo dõi sau 5 lần
    }
  },
  poll: true, // Kích hoạt chế độ polling
  pollingInterval: 3000, // Kiểm tra mỗi 3 giây
});