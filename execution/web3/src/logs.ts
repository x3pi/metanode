import { 
    createPublicClient, 
    http, 
    GetLogsParameters, 
    Hex,               
    parseAbi, 
    decodeEventLog,     
    AbiEvent,
    Log // Import thêm kiểu Log để sử dụng nếu cần
  } from 'viem';
  import { localhost } from 'viem/chains';
  
  // Khởi tạo client kết nối với RPC (Ví dụ: Ganache hoặc Anvil)
  const client = createPublicClient({
    chain: localhost, 
    transport: http('https://rpc-proxy-sequoia.ibe.app:8446'), 
  });
  
  // --- QUAN TRỌNG: Thay thế bằng địa chỉ hợp đồng ERC20 của bạn ---
  const contractAddress = '0x36d15454BFe8Fc3473C7fefa615b9966fc0f2a58'; // <<<=== !!! THAY ĐỊA CHỈ HỢP ĐỒNG CỦA BẠN VÀO ĐÂY !!!
  
  // Kiểm tra nếu địa chỉ hợp đồng chưa được thay thế
//   if (contractAddress === '0x36d15454BFe8Fc3473C7fefa615b9966fc0f2a58') {
//     console.error("Lỗi: Vui lòng thay thế '0x...' bằng địa chỉ hợp đồng ERC20 thực tế của bạn trong code.");
//     if (typeof process !== 'undefined' && process.exit) {
//         process.exit(1); 
//     } else {
//         throw new Error("Địa chỉ hợp đồng chưa được cung cấp.");
//     }
//   }
  
  // Định nghĩa ABI của sự kiện Transfer
  const transferEventAbi = parseAbi([
    'event Transfer(address indexed from, address indexed to, uint256 value)',
  ]);
  
  // Lấy định nghĩa sự kiện cụ thể từ mảng ABI đã parse
  const transferEventDefinition = transferEventAbi[0] as AbiEvent; 
  
  async function getEventLogs(): Promise<void> {
    console.log(`Đang lấy logs sự kiện Transfer cho hợp đồng: ${contractAddress}`);
    try {
      // Định nghĩa các tham số lọc logs
      const logsParams = { 
        address: contractAddress as Hex, 
        event: transferEventDefinition,   
        fromBlock: 4n,                  
      };
  
      // Lấy logs sự kiện từ blockchain
      const logs = await client.getLogs(logsParams); 
  
      console.log(`Tìm thấy ${logs.length} sự kiện Transfer.`);
  
      // In logs đã được giải mã (decoded) ra console để dễ đọc hơn
      if (logs.length > 0) {
          console.log('\n--- Chi tiết Logs Sự Kiện Transfer ---');
          logs.forEach((log, index) => {
              try {
                  // Giải mã dữ liệu và topics của từng log
                  const decodedLog = decodeEventLog({
                      abi: [transferEventDefinition], // ABI chứa định nghĩa sự kiện
                      data: log.data,
                      topics: log.topics,
                  });
                  
                  console.log(`Log #${index + 1}:`);
                  console.log(`  Block: ${log.blockNumber}`);
                  console.log(`  Tx Hash: ${log.transactionHash}`);
  
                  // --- SỬA LỖI TS2339 ---
                  // 1. Kiểm tra eventName có đúng là 'Transfer' không
                  // 2. Kiểm tra xem decodedLog.args có tồn tại không (không phải undefined)
                  if (decodedLog.eventName === 'Transfer' && decodedLog.args) {
                      // 3. Ép kiểu (type assertion) để TypeScript biết cấu trúc của args
                      const args = decodedLog.args as {
                          from: Hex;
                          to: Hex;
                          value: bigint; // uint256 trong Solidity -> bigint trong Viem/TS
                      };
  
                      // 4. Truy cập các thuộc tính một cách an toàn
                      console.log(`  From: ${args.from}`);
                      console.log(`  To: ${args.to}`);
                      console.log(`  Value: ${args.value.toString()}`); // Dùng args đã được ép kiểu
                  } else {
                      // Nếu không phải sự kiện Transfer hoặc không giải mã được args
                       console.log(`  Event Name: ${decodedLog.eventName}`);
                       console.log(`  (Args không tồn tại hoặc không phải sự kiện Transfer)`);
                  }
                  console.log('---------------------------------');
  
              } catch (decodeError) {
                  console.error(`Lỗi khi giải mã log #${index + 1}:`, decodeError);
                  console.log('  Log gốc:', log); 
                   console.log('---------------------------------');
              }
          });
      } else {
          console.log('Không tìm thấy sự kiện Transfer nào trong khoảng block đã chỉ định.');
      }
  
    } catch (error) {
      console.error('Lỗi khi lấy logs sự kiện:', error);
      if (error instanceof Error && error.message.includes('ECONNREFUSED')) {
          console.error("Gợi ý: Đảm bảo node blockchain local (Ganache, Anvil, Hardhat node) đang chạy tại địa chỉ và cổng đã cấu hình (http://127.0.0.1:8545).");
      }
       if (error instanceof Error && error.message.includes('Contract code not found')) {
          console.error(`Gợi ý: Kiểm tra lại địa chỉ hợp đồng '${contractAddress}'. Có thể địa chỉ sai hoặc hợp đồng chưa được triển khai trên mạng lưới bạn đang kết nối.`);
      }
       // Thêm các xử lý lỗi khác nếu cần
       else if (error instanceof Error) {
          console.error(`Lỗi không xác định: ${error.message}`);
       }
    }
  }
  
  getEventLogs();
  