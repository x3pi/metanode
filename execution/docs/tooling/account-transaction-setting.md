# Hướng dẫn thay đổi loại tài khoản (Account Type) bằng cách gọi hợp đồng thông minh

## 1. Giao dịch lần đầu: Thiết lập BLS Public Key

Khi tài khoản **chưa có BLS Public Key**, cần thực hiện một giao dịch để gọi hàm `setBlsPublicKey` từ hợp đồng tại địa chỉ:

```
0x00000000000000000000000000000000D844bb55
```

### Interface của hợp đồng

```solidity
interface AccountSetting {
    function setBlsPublicKey(bytes calldata publicKey) external returns (bool);
    function setAccountType(uint8 _accountType) external returns (bool);
}
```

### ABI của hợp đồng

```json
[
    {
        "inputs": [
            {
                "internalType": "uint8",
                "name": "_accountType",
                "type": "uint8"
            }
        ],
        "name": "setAccountType",
        "outputs": [
            {
                "internalType": "bool",
                "name": "",
                "type": "bool"
            }
        ],
        "stateMutability": "nonpayable",
        "type": "function"
    },
    {
        "inputs": [
            {
                "internalType": "bytes",
                "name": "publicKey",
                "type": "bytes"
            }
        ],
        "name": "setBlsPublicKey",
        "outputs": [
            {
                "internalType": "bool",
                "name": "",
                "type": "bool"
            }
        ],
        "stateMutability": "nonpayable",
        "type": "function"
    }
]
```

> Giao dịch này được ký bằng chữ ký **secp256k1** (tương thích với Ethereum). Rồi chuyển đổi các trường tương ứng tham khảo `docs/transaction/CreateTransaction.md`. Tool golang có thể chạy : `cmd/client/add_account_tx_0`

---

## 2. Thay đổi loại tài khoản sau khi đã có BLS Public Key

Sau khi đã thiết lập **BLS Public Key**, bạn có thể gọi hàm `setAccountType` để thay đổi loại tài khoản.

Giao dịch này có thể được thực hiện qua **MetaMask** hoặc thông qua **Coin Tool**.

### Ví dụ file `data.json` dùng với Coin Tool

```json
[
    {
        "from_address": "0x91976d3dbbc91320e9c4f79ac33e404f973ff5f7",
        "action": "call",
        "address": "0x00000000000000000000000000000000D844bb55",
        "input": "0xf2f5646f0000000000000000000000000000000000000000000000000000000000000001",
        "amount": "0",
        "name": "AccountSetting - setAccountType"
    }
]
```

**Giải thích:**
- `from_address`: địa chỉ ví thực hiện giao dịch.
- `address`: địa chỉ hợp đồng thông minh.
- `input`: dữ liệu ABI đã mã hóa để gọi hàm `setAccountType(uint8)` với giá trị `1` hoặc `0`.
- `amount`: giá trị ETH gửi kèm (0 trong trường hợp này).
- `name`: mô tả giao dịch.

---
