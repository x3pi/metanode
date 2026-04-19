# Giao dịch

## 1. Thay đổi định nghĩa giao dịch

### Loại bỏ các trường không cần thiết:

- `PendingUse`
- `CommissionSign`

### Bổ sung các trường liên quan tới giao dịch tương thích Ethereum:

```protobuf
// Bổ sung cho giao dịch eth
uint64 ChainID = 15;
uint64 Type = 16;
bytes R = 17;
bytes S = 18;
bytes V = 19;

// Thêm cho EIP-1559/EIP-2930
bytes GasTipCap = 20;
bytes GasFeeCap = 21;
repeated AccessTuple AccessList = 22;
```

> Định nghĩa proto trong file: `pkg/proto/transaction.proto`

---

## 2. Chuyển đổi giao dịch Ethereum

Có thể tham khảo module: `pkg/transaction`

### Trường tương ứng khi chuyển đổi từ giao dịch Ethereum:

| Trường Ethereum | Trường nội bộ         |
|------------------|------------------------|
| `type`           | `Type`                 |
| `r`              | `R`                    |
| `s`              | `S`                    |
| `v`              | `V`                    |

### Đối với loại **EthLegacy**:

| Trường Ethereum | Trường nội bộ     |
|------------------|--------------------|
| `value`          | `Amount`           |
| `gas`            | `MaxGas`           |
| `gasPrice`       | `MaxGasPrice`      |

### Đối với **EIP-1559/EIP-2930**:

Bổ sung thêm các trường:

- `GasTipCap`
- `GasFeeCap`
- `AccessList`

---

### Chú ý trường data cần build thành object `DeployData` hoặc `CallData` 
