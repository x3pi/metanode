import { createPublicClient, http, webSocket, createWalletClient } from 'viem';
import { localhost } from 'viem/chains';
import { privateKeyToAccount } from 'viem/accounts';

// Create a public client connection to local node (Hardhat, Anvil, Geth)
// const client = createPublicClient({
//     chain: localhost,
//     transport: http('http://127.0.0.1:8545'), // Local node URL
// });

const WSS_URL = 'ws://localhost:8747';

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

// Create wallet client to perform transactions
const privateKey = '0xcee4c644f964bb3ce7a322db844c50708745e1941990574d358af282c25144fc'; // Actual private key with 0x prefix
const account = privateKeyToAccount(privateKey);
const walletClient = createWalletClient({
    chain: { ...localhost, id: 991 },
    transport: http('http://127.0.0.1:8545'),
    account,
});

// Function to monitor a transaction until it's confirmed
async function monitorTransaction(txHash: `0x${string}`): Promise<boolean> {
    console.log(`Monitoring transaction: ${txHash}`);
    
    return new Promise((resolve, reject) => {
        const unwatch = client.watchBlockNumber({
            onBlockNumber: async (blockNumber: bigint) => {
                try {
                    console.log(`New block mined: ${blockNumber}`);
                    
                    const receipt = await client.getTransactionReceipt({
                        hash: txHash,
                    });
                    
                    if (receipt) {
                        // Always clean up the watcher when we have a receipt
                        unwatch();
                        
                        if (receipt.status === 'success') {
                            console.log(`Transaction successful at block ${receipt.blockNumber}!`);
                            console.log(`Gas used: ${receipt.gasUsed}`);
                            resolve(true);
                        } else {
                            console.log(`Transaction failed at block ${blockNumber}.`);
                            reject(new Error(`Transaction failed: ${txHash}`));
                        }
                    }
                    // If no receipt is found, continue watching for new blocks
                } catch (error) {
                    console.error('Error while checking transaction receipt:', error);
                    unwatch();
                    reject(error);
                }
            },
            onError: (error) => {
                console.error('Error in block watcher:', error);
                unwatch();
                reject(error);
            },
            poll: true,
            pollingInterval: 3000, // Check every 3 seconds
        });
    });
}

// Send transaction and monitor it
async function sendTransaction(): Promise<void> {
    let txHash: `0x${string}`;
    
    try {
        // Send the transaction
        txHash = await walletClient.sendTransaction({
            to: '0x0240461F1830bEc69D9939F923960cF9fC7eD356', // Recipient address
            value: 1n * 10n**18n, // Convert to Wei
            gas: 21000n, // Gas limit
        });
        
        console.log(`Transaction sent: ${txHash}`);
        
        // Monitor the transaction
        const success = await monitorTransaction(txHash);
        
        if (success) {
            console.log('Transaction process completed successfully');
        }
    } catch (error) {
        console.error('Transaction process failed:', error instanceof Error ? error.message : error);
    }
}

// Execute the main function
sendTransaction().then(() => {
    console.log('Process completed');
}).catch((error) => {
    console.error('Process failed:', error);
});