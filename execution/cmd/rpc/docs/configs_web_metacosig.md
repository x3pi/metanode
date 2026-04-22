
---
## 1.1. Cấu hình FE
file `/web3/register-private-key-rpc/src/constants/customChain.ts`, cập nhật `GO_BACKEND_RPC_URL` và `WSS_RPC` với địa chỉ RPC và WSS tương ứng ip rpc của bạn:

```typescript
export const GO_BACKEND_RPC_URL = "http://<ip-rpc>:8545";
export const WSS_RPC = "ws://<ip-rpc>:8545";
```
abi được đặt cấu hình ở `metaCoSign/web3/dapp/register-private-key-rpc/src/constants/contracts.ts`

## 2. Hướng dẫn chạy App

### Bước 1: Chạy RPC Client (Backend)

Di chuyển vào thư mục `rpc-client` và chạy lệnh, logs nằm ở `cmd/rpc-client/logs` log theo ngày:

```bash
cd ./cmd/rpc-client
go run .
```

### Bước 2: Chạy Giao diện METACOSIGN (Frontend)

Di chuyển vào thư mục dự án frontend:

```bash
cd ./web3/dapp/register-private-key-rpc
```

Cài đặt dependencies (nếu chưa cài):
```bash
yarn install
```

Khởi chạy ứng dụng ở chế độ development:
```bash
yarn dev
```
