import { createPublicClient, webSocket, http, Chain, PublicClient } from 'viem';
import { localhost } from 'viem/chains'; // Sử dụng mainnet, có thể thay bằng chain khác (sepolia, goerli, ...)

// --- Cấu hình ---
// THAY THẾ BẰNG ENDPOINT WEBSOCKET (WSS) CỦA BẠN
// Ví dụ sử dụng endpoint công khai (có thể không ổn định bằng dịch vụ riêng):
const WSS_URL = 'ws://localhost:8545/ws';
// Hoặc endpoint từ nhà cung cấp dịch vụ (nên dùng):
// const WSS_URL = 'wss://mainnet.infura.io/ws/v3/YOUR_INFURA_PROJECT_ID'; // Ví dụ Infura

// --- Hàm trợ giúp để xử lý BigInt khi chuyển sang JSON ---
// JSON.stringify không hỗ trợ BigInt mặc định
function replacer(key: string, value: any): any {
  if (typeof value === 'bigint') {
    return value.toString(); // Chuyển BigInt thành chuỗi
  }
  return value;
}

// --- Hàm chính để lấy thông tin khối ---
async function fetchLatestBlockInfo(wssUrl: string, chain: Chain) {
  console.log(`Đang kết nối tới WebSocket: ${wssUrl} trên chain ${chain.name}...`);

  let client: PublicClient | null = null;

  try {
    // Tạo một Viem Public Client sử dụng WebSocket transport
    // Public Client dùng cho các hành động chỉ đọc (read-only)
    client = createPublicClient({
      chain: chain, // Chỉ định chain (ví dụ: mainnet)
      transport: webSocket(wssUrl, {
          // Tùy chọn thêm cho WebSocket nếu cần, ví dụ timeout:
          // timeout: 10_000, // 10 giây
          // Tự động kết nối lại khi mất kết nối (mặc định là true)
           retryCount: 3, // Số lần thử kết nối lại
           retryDelay: 1000 // Độ trễ giữa các lần thử (ms)
      }),
      // Có thể cấu hình thêm pollingInterval nếu cần thiết cho 1 số chức năng khác
      // pollingInterval: 4_000,
    });

    console.log('Đã tạo Viem client thành công.');

    // Gọi hàm getBlock để lấy thông tin khối mới nhất ('latest')
    // includeTransactions: true để lấy cả danh sách giao dịch chi tiết trong khối
    console.log("Đang yêu cầu thông tin khối 'latest'...");
    const latestBlock = await client.getBlock({
      blockTag: 'latest', // Lấy khối mới nhất. Có thể dùng 'safe', 'finalized', 'earliest'
      // Hoặc lấy khối cụ thể bằng số: blockNumber: 123456n (n ký hiệu cho BigInt)
      includeTransactions: true, // Lấy thông tin chi tiết giao dịch
    });

    console.log('\n--- Thông tin khối nhận được ---');
    // Sử dụng JSON.stringify với replacer để in ra đối tượng block, xử lý BigInt
    console.log(JSON.stringify(latestBlock, replacer, 2)); // indent 2 cho dễ đọc

    // In ra một vài thông tin cụ thể
    console.log(`\nSố khối (Block Number): ${latestBlock.number?.toString() ?? 'N/A'}`);
    console.log(`Hash khối: ${latestBlock.hash ?? 'N/A'}`);
    console.log(`Timestamp: ${latestBlock.timestamp?.toString() ?? 'N/A'} (Unix)`);
    console.log(`Số lượng giao dịch: ${latestBlock.transactions?.length ?? 0}`);

  } catch (error) {
    console.error('\nĐã xảy ra lỗi:', error);
    if (error instanceof Error && 'code' in error) {
         // Một số lỗi RPC có mã lỗi cụ thể
         console.error(`Mã lỗi (nếu có): ${error.code}`);
    }
  } finally {
       // Rất quan trọng: Đóng kết nối WebSocket khi không cần nữa
       // Viem client không tự động đóng WebSocket transport khi bạn chỉ gọi 1 hàm
       // Để tránh treo process, cần hủy client hoặc transport.
       // Tuy nhiên, `destroy` không có sẵn trực tiếp trên PublicClient.
       // Cách tốt nhất là quản lý vòng đời kết nối cẩn thận hơn trong ứng dụng thực tế.
       // Nếu chỉ chạy script một lần, process sẽ tự thoát và đóng kết nối.
       // Trong ứng dụng chạy dài hạn, bạn cần cơ chế đóng kết nối rõ ràng.
       console.log('\n(Trong ứng dụng thực tế, cần quản lý việc đóng kết nối WebSocket cẩn thận)');
       // Ví dụ cơ bản: Nếu client được tạo, có thể thử truy cập transport để đóng nếu cần,
       // nhưng Viem không cung cấp hàm `destroy()` trực tiếp dễ dàng cho public client.
       // Process sẽ tự thoát sau khi hàm async hoàn thành.
  }
}

// --- Chạy hàm chính ---
if (false) {
    console.warn("CẢNH BÁO: Vui lòng thay thế placeholder như 'YOUR_INFURA_PROJECT_ID' hoặc cung cấp một WSS_URL hợp lệ!");
} else {
    fetchLatestBlockInfo(WSS_URL, localhost)
      .then(() => {
        console.log('\nHoàn thành ví dụ lấy thông tin khối qua WebSocket.');
        // Kết thúc process một cách rõ ràng sau khi hoàn thành
        // process.exit(0); // Bỏ comment nếu muốn ép thoát
      })
      .catch((err) => {
        console.error('Lỗi không mong muốn ở cấp cao nhất:', err);
        process.exit(1); // Thoát với mã lỗi
      });
}

// Để chạy file này:
// 1. Biên dịch: tsc getBlockExample.ts
//    Chạy:     node getBlockExample.js
// 2. Hoặc chạy trực tiếp với ts-node: ts-node getBlockExample.ts