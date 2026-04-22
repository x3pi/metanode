// src/customChain.ts
// export const GO_BACKEND_RPC_URL = window.location.origin;
// export const WSS_RPC = window.location.origin.replace(/^http/, "ws");
export const WSS_RPC = "ws://192.168.1.234:8545";
export const WSS_RPC_INTERCEPTOR = "ws://192.168.1.234:8545/interceptor";
export const GO_BACKEND_RPC_URL = "http://192.168.1.234:8545";
// const GO_BACKEND_RPC_URL = "https://rpc-proxy-sequoia.iqnb.com:8446";
// export const WSS_RPC = "wss://rpc-proxy-sequoia.iqnb.com:8446";
export const privateKey = "3f425fa96b85f8ece78f2a10350fa7af4643a4cdee02f36369833f45b0e003a7"
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
} ;
