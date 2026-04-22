import { createPublicClient, http, Hex, parseAbi, decodeEventLog, GetTransactionReceiptReturnType } from 'viem';
import { localhost } from 'viem/chains';

// ABI của sự kiện EmployeeAdded
const abi = parseAbi([
  'event EmployeeAdded(uint256 id, string name)',
]);

// Tạo client kết nối với RPC
const client = createPublicClient({
  chain: localhost,
  transport: http('http://127.0.0.1:8545'), // URL node local
});

// Hàm lấy thông tin giao dịch theo transaction hash và decode log
async function getTransactionReceiptByHash(txHash: Hex): Promise<void> {
    try {
        // Lấy transaction receipt từ blockchain
        const transactionReceipt: GetTransactionReceiptReturnType = await client.getTransactionReceipt({ hash: txHash });
        console.log('Transaction Receipt:', transactionReceipt);
        
        // Lọc logs có liên quan đến sự kiện EmployeeAdded
        const logs = transactionReceipt.logs.map(log => {
            console.log(log.topics)
            try {
                return decodeEventLog({
                    abi,
                    data: log.data,
                    topics: log.topics, // Chắc chắn topics không rỗng
                  });
            } catch (err) {
                return null; // Bỏ qua log nếu không decode được
            }
        }).filter(log => log !== null);

        console.log('Decoded Event Logs:', logs);
                console.log('Decoded Event Logs:', transactionReceipt.logs[0].topics);

    } catch (error) {
        console.error('Lỗi khi lấy thông tin giao dịch:', error);
    }
}

// Lấy thông tin giao dịch theo hash
getTransactionReceiptByHash('0xf834d26a33456a7faeaa910c44d3d8f2a7aa4c10f100bf585707ea4aa3d8af60' as Hex);
