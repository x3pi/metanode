import { createPublicClient, http } from 'viem';
import { localhost } from 'viem/chains';

// Tạo client kết nối với rpc
const client = createPublicClient({
  chain: localhost,
  transport: http('http://127.0.0.1:8545'), // URL node local
});

interface TransactionCountOptions {
  address: `0x${string}`;
  blockTag?: 'latest' | 'earliest' | 'pending';
}

async function getTransactionCount(options: TransactionCountOptions): Promise<void> {
  try {
    const transactionCount = await client.getTransactionCount({
      address: options.address,
      blockTag: options.blockTag,
    });
    console.log(`Transaction Count: ${transactionCount}`);
  } catch (error) {
    console.error('Lỗi khi lấy số lượng giao dịch:', error);
  }
}

// Địa chỉ ví mặc định của Anvil/Hardhat
getTransactionCount({ address: '0x5AE1e723973577AcaB776ebC4be66231fc57b370', blockTag: 'latest' });