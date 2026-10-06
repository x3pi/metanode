# DEPRECATED: Legacy RPC Client Proxy

This directory (`cmd/rpc-client`) is deprecated as part of the ETH-only production architecture (see `note/adr_eth_only_production_decisions.md` and `note/plan_eth_only_production_readiness.md`).

MetaMask, ethers.js, viem, Web3j, and all dApps connect directly to `simple_chain`'s standard Ethereum JSON-RPC endpoint (`:8646` / `:31646`) and raw EIP-2718 TCP ingress.

Do NOT deploy or use this proxy in production.
