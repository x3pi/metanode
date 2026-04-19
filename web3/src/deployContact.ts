import { createPublicClient, createWalletClient, http, Hex, parseAbi } from 'viem';
import { localhost } from 'viem/chains';
import { privateKeyToAccount } from 'viem/accounts';

// Connect to local Ethereum node
const client = createPublicClient({
    chain: localhost,
    transport: http('http://127.0.0.1:8545'),
});

const privateKey = '0xcee4c644f964bb3ce7a322db844c50708745e1941990574d358af282c25144fc'; // Replace with actual private key
const account = privateKeyToAccount(privateKey);
const walletClient = createWalletClient({
    chain: { ...localhost, id: 991 },
    transport: http('http://127.0.0.1:8545'),
    account,
});

// Contract bytecode for 1_Storage.sol
const bytecode: Hex = '0x608060405234801561000f575f80fd5b5061031d8061001d5f395ff3fe608060405234801561000f575f80fd5b506004361061003f575f3560e01c80632e64cec1146100435780636057361d14610061578063c7559da414610091575b5f80fd5b61004b6100c1565b6040516100589190610131565b60405180910390f35b61007b60048036038101906100769190610178565b6100c9565b6040516100889190610131565b60405180910390f35b6100ab60048036038101906100a69190610178565b6100f0565b6040516100b8919061022d565b60405180910390f35b5f8054905090565b5f6001826100d7919061027a565b5f819055506001826100e9919061027a565b9050919050565b60608160405160200161010391906102cd565b6040516020818303038152906040529050919050565b5f819050919050565b61012b81610119565b82525050565b5f6020820190506101445f830184610122565b92915050565b5f80fd5b61015781610119565b8114610161575f80fd5b50565b5f813590506101728161014e565b92915050565b5f6020828403121561018d5761018c61014a565b5b5f61019a84828501610164565b91505092915050565b5f81519050919050565b5f82825260208201905092915050565b5f5b838110156101da5780820151818401526020810190506101bf565b5f8484015250505050565b5f601f19601f8301169050919050565b5f6101ff826101a3565b61020981856101ad565b93506102198185602086016101bd565b610222816101e5565b840191505092915050565b5f6020820190508181035f83015261024581846101f5565b905092915050565b7f4e487b71000000000000000000000000000000000000000000000000000000005f52601160045260245ffd5b5f61028482610119565b915061028f83610119565b92508282019050808211156102a7576102a661024d565b5b92915050565b5f819050919050565b6102c76102c282610119565b6102ad565b82525050565b5f6102d882846102b6565b6020820191508190509291505056fea26469706673582212205cedd935670ff9c960e2a2d9ec394c1c03a888d8e8900843c4d94358db2eb71964736f6c63430008140033';

// Define ABI
const abi = parseAbi([
    'function retrieve() view returns (uint256)',
    'function store(uint256 num) returns (uint256)'
]);

// Function to monitor a transaction until it's confirmed
async function monitorTransaction(txHash: `0x${string}`): Promise<any> {
    console.log(`Monitoring transaction: ${txHash}`);
    
    return new Promise((resolve, reject) => {
        const unwatch = client.watchBlockNumber({
            onBlockNumber: async (blockNumber) => {
                try {
                    console.log(`New block mined: ${blockNumber}`);
                    
                    const receipt = await client.getTransactionReceipt({ hash: txHash });
                    
                    if (receipt) {
                        // Always clean up the watcher when we have a receipt
                        unwatch();
                        
                        if (receipt.status === 'success') {
                            console.log(`Transaction successful at block ${receipt.blockNumber}!`);
                            resolve(receipt);
                        } else {
                            console.log(`Transaction failed at block ${blockNumber}.`);
                            reject(new Error(`Transaction failed: ${txHash}`));
                        }
                    }
                    // If no receipt is found, continue watching for new blocks
                } catch (error) {
                    unwatch();
                    reject(error);
                }
            },
            onError: (error) => {
                unwatch();
                reject(error);
            }
        });
    });
}

// Function to deploy the contract
async function deployContract(): Promise<`0x${string}`> {
    try {
        // Deploy contract
        const txHash = await walletClient.deployContract({ 
            bytecode, 
            abi, 
            account, 
            args: [], 
            gas: 3000000n 
        });
        console.log(`Contract Deployment Hash: ${txHash}`);
        
        // Wait for deployment transaction
        const receipt = await monitorTransaction(txHash);
        
        if (!receipt.contractAddress) {
            throw new Error('Contract address not found in transaction receipt');
        }
        
        console.log(`Contract deployed at: ${receipt.contractAddress}`);
        return receipt.contractAddress;
    } catch (error) {
        console.error('Error deploying contract:', error);
        throw error;
    }
}

// Function to interact with the contract
async function interactWithContract(contractAddress: `0x${string}`): Promise<void> {
    try {
        // Call store function
        const storeTxHash = await walletClient.writeContract({
            address: contractAddress,
            abi,
            functionName: "store",
            args: [99n],
            gas: 100000n,
            account
        });
        console.log(`Transaction for store function: ${storeTxHash}`);
        
        // Wait for store transaction
        await monitorTransaction(storeTxHash);
        
        // Read stored value
        const storedValue = await client.readContract({
            address: contractAddress,
            abi,
            functionName: "retrieve",
            args: []
        });
        
        console.log(`Value stored in contract: ${storedValue}`);
    } catch (error) {
        console.error("Error interacting with contract:", error);
    }
}

// Main execution function
async function main() {
    try {
        const contractAddress = await deployContract();
        await interactWithContract(contractAddress);
        console.log("Contract interaction completed successfully");
    } catch (error) {
        console.error("Process failed:", error);
    }
}

main();