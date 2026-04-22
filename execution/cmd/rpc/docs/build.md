# Build Configuration

## Environment Variables

### Development Mode
Khi develop local, sử dụng các biến sau trong `src/constants/customChain.ts`:

```typescript
export const WSS_RPC = "ws://192.168.1.234:8545";
const GO_BACKEND_RPC_URL = "http://192.168.1.234:8545";
```

### Production Mode
Khi deploy lên production, sử dụng các biến sau:

```typescript
const GO_BACKEND_RPC_URL = window.location.origin;
export const WSS_RPC = window.location.origin.replace(/^http/, "ws");
```

> **Note**: Production mode tự động chuyển đổi protocol:
> - `http://` → `ws://`
> - `https://` → `wss://`

## Build & Deploy

1. **Build project:**
   ```bash
   yarn build
   ```

2. **Deploy:**
   - Folder `dist` sẽ được tạo sau khi build
   - Copy folder `dist` vào folder `cmd/rpc-client`
   - Truy cập qua: `http://localhost:8545/register-bls-key/`