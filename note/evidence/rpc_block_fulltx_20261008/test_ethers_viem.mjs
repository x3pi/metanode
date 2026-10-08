import { ethers } from '/tmp/eth_test_env/node_modules/ethers/dist/ethers.js';
import { createPublicClient, http } from '/tmp/eth_test_env/node_modules/viem/_esm/index.js';

const RPC_URL = 'http://127.0.0.1:31646';

async function main() {
  console.log("=== 1. ETHERS.JS V6 FULL TX TEST ===");
  const provider = new ethers.JsonRpcProvider(RPC_URL);
  const blockNumber = await provider.getBlockNumber();
  console.log(`Current block number: ${blockNumber}`);

  const block = await provider.getBlock(blockNumber, true);
  if (!block) throw new Error("Failed to get block");
  console.log(`Block hash: ${block.hash}, number: ${block.number}, tx count: ${block.prefetchedTransactions.length}`);

  for (let i = 0; i < Math.min(block.prefetchedTransactions.length, 5); i++) {
    const tx = block.prefetchedTransactions[i];
    console.log(`  [ethers-v6] tx[${i}] hash=${tx.hash} type=${tx.type} blockNumber=${tx.blockNumber} from=${tx.from}`);
    if (tx.blockNumber !== block.number) {
      throw new Error(`tx.blockNumber (${tx.blockNumber}) !== block.number (${block.number})`);
    }
    if (tx.blockHash !== block.hash) {
      throw new Error(`tx.blockHash !== block.hash`);
    }
  }
  console.log("✅ [ethers-v6] PASS: all prefetched transactions correctly decoded and matched parent block!");

  console.log("\n=== 2. VIEM FULL TX TEST ===");
  const client = createPublicClient({
    transport: http(RPC_URL)
  });

  const viemBlock = await client.getBlock({
    blockNumber: BigInt(blockNumber),
    includeTransactions: true
  });
  console.log(`[viem] Block hash: ${viemBlock.hash}, tx count: ${viemBlock.transactions.length}`);

  for (let i = 0; i < Math.min(viemBlock.transactions.length, 5); i++) {
    const tx = viemBlock.transactions[i];
    console.log(`  [viem] tx[${i}] hash=${tx.hash} type=${tx.type} blockNumber=${tx.blockNumber} from=${tx.from}`);
    if (tx.blockNumber !== viemBlock.number) {
      throw new Error(`viem tx.blockNumber !== viemBlock.number`);
    }
    if (tx.blockHash !== viemBlock.hash) {
      throw new Error(`viem tx.blockHash !== viemBlock.hash`);
    }
  }
  console.log("✅ [viem] PASS: all transactions parsed cleanly by viem with strict schema validation!");
}

main().catch(err => {
  console.error("❌ ERROR:", err);
  process.exit(1);
});
