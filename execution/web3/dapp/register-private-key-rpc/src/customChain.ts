// src/customChain.ts
import { type Chain } from 'viem';
const GO_BACKEND_RPC_URL =  window.location.origin;
// const GO_BACKEND_RPC_URL =  "http://localhost:8545";

// Replace with your actual Chain ID 991 details
export const chain991 = {
  id: 991,
  name: 'My Chain 991', // Give your network a descriptive name
  nativeCurrency: {
    name: 'My Native Token',
    symbol: 'MNT',
    decimals: 18,
  },
  rpcUrls: {
    default: { http: [GO_BACKEND_RPC_URL] },
    public: { http: [GO_BACKEND_RPC_URL] },
  },
  // Optional: Add block explorer if you have one
  // blockExplorers: {
  //   default: { name: 'MyExplorer', url: 'http://localhost:4000' },
  // },
} as const satisfies Chain;