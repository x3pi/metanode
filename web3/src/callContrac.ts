import { createPublicClient, http, parseAbi } from 'viem'
import { mainnet } from 'viem/chains'

// 🔹 Tạo client để kết nối với mạng Ethereum
const client = createPublicClient({
  chain: mainnet,
  transport: http("http://127.0.0.1:8545")
})

// 🔹 Địa chỉ contract ERC-20 (ví dụ: USDC)
const contractAddress = "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"

// 🔹 Định nghĩa ABI (Application Binary Interface)
const abi = parseAbi([
  'function balanceOf(address) view returns (uint256)'
])

// 🔹 Hàm gọi smart contract để lấy số dư của một địa chỉ ví
async function getBalance(walletAddress: `0x${string}`) {
  try {
    const balance: bigint = await client.readContract({
      address: contractAddress,
      abi,
      functionName: 'balanceOf',
      args: [walletAddress]
    })
    console.log(`Balance: ${balance}`)
  } catch (error) {
    console.error("Error calling contract:", error)
  }
}

// 🔹 Gọi hàm và truyền vào địa chỉ ví
getBalance("0xYourWalletAddress")
