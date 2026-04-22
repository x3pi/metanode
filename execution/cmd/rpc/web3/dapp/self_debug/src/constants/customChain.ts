// src/customChain.ts
import { type Chain } from "viem";
// export const GO_BACKEND_RPC_URL = window.location.origin;
// export const WS_BASE = window.location.origin.replace(/^http/, "ws");
// export const WSS_RPC = `${WS_BASE}/interceptor`;
export const WSS_RPC = "ws://192.168.1.234:8545";
export const GO_BACKEND_RPC_URL = "http://192.168.1.234:8545";
// const GO_BACKEND_RPC_URL = "https://rpc-proxy-sequoia.iqnb.com:8446";
// export const WSS_RPC = "wss://rpc-proxy-sequoia.iqnb.com:8446";

// Replace with your actual Chain ID 991 details
export const chain991 = {
  id: 991,
  name: "My Chain 991", // Give your network a descriptive name
  nativeCurrency: {
    name: "My Native Token",
    symbol: "MNT",
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
