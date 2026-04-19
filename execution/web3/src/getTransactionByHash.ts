import { createPublicClient, http, Hex, GetBlockParameters } from 'viem';
import { localhost } from 'viem/chains';

// Tạo client kết nối với RPC
const client = createPublicClient({
  chain: localhost,
  transport: http('http://127.0.0.1:8545'), // URL node local
});

// Hàm lấy thông tin giao dịch theo transaction hash
async function getTransactionByHash(txHash: Hex): Promise<void> {
  try {
    const transaction = await client.getTransaction({ hash: txHash });
    console.log('Transaction Details:', transaction);
  } catch (error) {
    console.error('Lỗi khi lấy thông tin giao dịch:', error);
  }
}


// Lấy thông tin giao dịch theo hash
getTransactionByHash('0x3f90b1b3b0ac133927b62ede065644725b22e481b73a7e7658957295e5d5e4f2' as Hex);


// Kết quả trả về simple chain

// Transaction Details: {
//   blockHash: '0x4e82023118385675709ac702c752862ff340c657fc9ab5df4ec8ab575708bec9',
//   blockNumber: 71771n,
//   from: '0x5ae1e723973577acab776ebc4be66231fc57b370',
//   gas: 0n,
//   gasPrice: undefined,
//   hash: '0x870872aa67f8c27543f6fef140a64112c614a086093d98ace0c6ba708bd8afe4',
//   input: '0x',
//   nonce: 8,
//   to: '0x0240461f1830bec69d9939f923960cf9fc7ed356',
//   transactionIndex: 0,
//   value: 1000000000000000000n,
//   type: 'legacy',
//   v: undefined,
//   r: null,
//   s: null,
//   chainId: undefined,
//   typeHex: '0x0'
// }


// Kết quả trả về eth chain

// Transaction Details: {
//   type: 'eip1559',
//   chainId: 31337,
//   nonce: 1,
//   gas: 21001n,
//   maxFeePerGas: 1875175000n,
//   maxPriorityFeePerGas: 1875175000n,
//   to: '0x0240461f1830bec69d9939f923960cf9fc7ed356',
//   value: 1000000000000000000n,
//   accessList: [],
//   input: '0x',
//   r: '0xe4396c77b8769c0244a36629083af025097e85ccae333ad63db79a773008602d',
//   s: '0x32c8bb7a534f431d39b464a9d4db2ea865e170ea21b8fd3c4aca34e60bbf3e82',
//   yParity: 0,
//   v: 0n,
//   hash: '0xd3b8da548fcfc04048184a5f3c12fc81c5dc7f485e3dbad567076f7735ee8b2d',
//   blockHash: '0xe24f83ffd42847ce00786929c23d899d60a377409511680e6d4b384acbab799d',
//   blockNumber: 2n,
//   transactionIndex: 0,
//   from: '0x5ae1e723973577acab776ebc4be66231fc57b370',
//   gasPrice: 2750350000n,
//   typeHex: '0x2'
// }