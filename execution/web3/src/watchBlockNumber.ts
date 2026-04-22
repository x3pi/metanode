import { createPublicClient, http } from 'viem';
import { localhost } from 'viem/chains';

// Tạo client kết nối với rpc
const client = createPublicClient({
  chain: localhost,
  transport: http('http://127.0.0.1:8545'), // URL node local
});

// Theo dõi sự thay đổi của số block mới nhất
let blockCount = 0; // Biến đếm số lần cập nhật block
let unwatch = client.watchBlockNumber({
  onBlockNumber: (blockNumber: bigint) => {
    console.log(`Block Number Updated: ${blockNumber}`);
    blockCount++;
    if (blockCount >= 5) {
      unwatch(); // Dừng theo dõi sau 5 lần
    }
  },
  poll: true, // Kích hoạt chế độ polling
  pollingInterval: 3000, // Kiểm tra mỗi 3 giây
});
