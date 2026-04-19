import { createPublicClient, http, Hex, webSocket, GetBlockParameters } from 'viem';
import { localhost } from 'viem/chains';

// Tạo client kết nối với rpc
// const client = createPublicClient({
//   chain: localhost,
//   transport: http('http://127.0.0.1:8545'), // URL node local
// });
// const WSS_URL = 'ws://192.168.1.99:8545';
// const WSS_URL = 'ws://127.0.0.1:8545/ws';
// const WSS_URL = 'wss://bsc-rpc.publicnode.com';
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


async function getBlock(blockParam: GetBlockParameters): Promise<void> {
  try {
    const block = await client.getBlock(blockParam);
    console.log('Block Details:', block);
  } catch (error) {
    console.error('Lỗi khi lấy thông tin block:', error);
  }
}

// Lấy thông tin block với nhiều tham số khác nhau
// getBlock({ blockTag: 'latest' }); // Block mới nhất
getBlock({ blockNumber: 1n }); // Block số 10
getBlock({ blockNumber: 2n }); // Block số 10
// getBlock({ blockNumber: 83n }); // Block số 10

// getBlock({ blockHash: '0x679701a4d527f298b9edb7a670ab57677b687303dde0111e58968c3f5103d2ee' as Hex }); 


// --- Ví dụ kết quả trả về ---
// **Kết quả trả về (simple chain):**
// ```
// Block Details: {
//   blockHash: '0x8d0661e4bdcdbd27b1ac84a7a66a3e757ad96fbf92360f79f08954f1de086115',
//   blockNumber: 70389,
//   hash: '0x8d0661e4bdcdbd27b1ac84a7a66a3e757ad96fbf92360f79f08954f1de086115',
//   number: 70389n,
//   stateRoot: '0x337debd80c0e958f48e45fe6d61355db53beaf3f95a8f58cf8d0761715961264',
//   transactions: [],
//   baseFeePerGas: null,
//   blobGasUsed: undefined,
//   difficulty: undefined,
//   excessBlobGas: undefined,
//   gasLimit: undefined,
//   gasUsed: undefined,
//   logsBloom: null,
//   nonce: null,
//   size: undefined,
//   timestamp: undefined,
//   totalDifficulty: null
// }
// ```
// 
// **Kết quả trả về Ethereum (eth):**
// ```
// Block Details: {
//   hash: '0x6d9ceeae8cc4f9da631b100397b2a4162abe3542f4e19e4cddeb226029eb44bc',
//   parentHash: '0x0000000000000000000000000000000000000000000000000000000000000000',
//   sha3Uncles: '0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347',
//   miner: '0x0000000000000000000000000000000000000000',
//   stateRoot: '0x0000000000000000000000000000000000000000000000000000000000000000',
//   transactionsRoot: '0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421',
//   receiptsRoot: '0x0000000000000000000000000000000000000000000000000000000000000000',
//   logsBloom: '0x0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000',
//   difficulty: 0n,
//   number: 0n,
//   gasLimit: 30000000n,
//   gasUsed: 0n,
//   timestamp: 1741151359n,
//   extraData: '0x',
//   mixHash: '0x0000000000000000000000000000000000000000000000000000000000000000',
//   nonce: '0x0000000000000000',
//   baseFeePerGas: 1000000000n,
//   withdrawalsRoot: '0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421',
//   blobGasUsed: 0n,
//   excessBlobGas: 0n,
//   parentBeaconBlockRoot: '0x0000000000000000000000000000000000000000000000000000000000000000',
//   totalDifficulty: 0n,
//   size: 582n,
//   uncles: [],
//   transactions: [],
//   withdrawals: []
// }
// ```
