import { createPublicClient, http, formatEther, Hex } from 'viem';
import { localhost } from 'viem/chains';
const WSS_URL = 'https://rpc-proxy-sequoia.ibe.app:8446';

// Tạo client kết nối với rpc
const client = createPublicClient({
  chain: localhost,
  transport: http(WSS_URL), // URL node local
});

async function getBalance(address: Hex): Promise<void> {
  try {
    const balance = await client.getBalance({ address });
    console.log(`Balance: ${formatEther(balance)} ETH`);
  } catch (error) {
    console.error('Lỗi khi lấy số dư:', error);
  }
}

// Địa chỉ ví mặc định của Anvil/Hardhat
getBalance('0x5AE1e723973577AcaB776ebC4be66231fc57b370');