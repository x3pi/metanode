# Báo Cáo Thực Nghiệm Client Thật Cho `eth_getBlockByNumber(fullTx=true)`

- **Ngày thực hiện:** 2026-10-08
- **Môi trường:** Cluster cô lập 4 validators (Mysticeti 4val) tại cổng RPC 31646
- **Mục tiêu:** Kiểm tra tương thích chuẩn Ethereum JSON-RPC (EIP-2718, EIP-2930, EIP-1559) với các client Web3 thật: ethers.js v6, viem, web3.py, và foundry cast.

## 1. Kết Quả Kiểm Thử Thực Tế

| Bộ công cụ / Client | Loại giao dịch kiểm tra | Trạng thái | Ghi chú | Bằng chứng |
| :--- | :--- | :---: | :--- | :---: |
| **Ethers.js v6** (`v6.17.0`) | EIP-1559 (Type 2) | **PASS** | `provider.getBlock(n, true)` giải mã thành công `prefetchedTransactions`, `blockNumber` và `blockHash` khớp | evidence:rpc_block_fulltx_live_e2e |
| **Viem** (`v2.57.3`) | EIP-1559 (Type 2) | **PASS** | `client.getBlock({includeTransactions: true})` vượt qua schema validation nghiêm ngặt | evidence:rpc_block_fulltx_live_e2e |
| **Web3.py** (`v7.16.0`) | Legacy (Type 0), EIP-2930 (Type 1), EIP-1559 (Type 2) | **PASS** | `w3.eth.get_block(n, full_transactions=True)` giải mã đủ trường chuẩn | evidence:rpc_block_fulltx_live_e2e |
| **Foundry (`cast`)** (`v1.8.5`) | Khối chứa giao dịch đầy đủ | **PASS** | `cast block <n> --full` in cấu trúc giao dịch có `type: 2`, `blockNumber` | evidence:rpc_block_fulltx_live_e2e |

## 2. Đối Chiếu Tính Đồng Nhất Trường Chuẩn (Zero Drift)

| Phương thức so sánh | Đối tượng giao dịch | Kết quả đối chiếu | Bằng chứng |
| :--- | :--- | :--- | :---: |
| `eth_getBlockByNumber(n, true)` vs `eth_getTransactionByHash(h)` | Legacy (Type 0) | Trùng khớp các trường chuẩn Ethereum (`blockHash`, `blockNumber`, `from`, `gas`, `gasPrice`, `hash`, `input`, `nonce`, `to`, `transactionIndex`, `value`, `type`, `chainId`, `v`, `r`, `s`) | evidence:rpc_block_fulltx_live_e2e |
| `eth_getBlockByNumber(n, true)` vs `eth_getTransactionByHash(h)` | EIP-2930 (Type 1) | Trùng khớp các trường chuẩn Ethereum kèm `accessList` và `yParity` | evidence:rpc_block_fulltx_live_e2e |
| `eth_getBlockByNumber(n, true)` vs `eth_getTransactionByHash(h)` | EIP-1559 (Type 2) | Trùng khớp các trường chuẩn Ethereum kèm `maxFeePerGas`, `maxPriorityFeePerGas`, `accessList`, `yParity` | evidence:rpc_block_fulltx_live_e2e |
| Trường mở rộng `groupId` | Mọi loại giao dịch | Bảo toàn cho các công cụ giám sát nội bộ | evidence:rpc_block_fulltx_live_e2e |
