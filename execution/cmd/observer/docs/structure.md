# Cấu Trúc Giao Dịch Cross-Chain

## Thiết lập kênh

**Chain A:**
- Kênh 1 : `SM-A1-B1`
- Kênh 2 : `SM-A2-B2`
- Ví: `W-A1`, `W-A2`

**Chain B:**
- Kênh 1 : `SM-B1-A1`
- Kênh 2 : `SM-B2-A2`
- Ví: `W-B1`, `W-B2`

---

## Chuyển tiền xuyên chain: Chain A → B

### Chain A tạo giao dịch

#### Kênh 1

**Gọi hàm chuyển tiền ở contract A:**

```json
Tx1 : {
    "from": "W-A1",
    "to": "SM-A1-B1",
    "value": 100,
    "data": { "recipient": "WC1" }
}
```

**Gọi contract xuyên chain:**

```json
Tx2 : {
    "from": "W-A2",
    "to": "SM-A1-B1",
    "value": 100,
    "relativeAddress": [],
    "data": { "target": "SM1" }
}
```

#### Kênh 2

**Gọi hàm chuyển tiền ở contract A:**

```json
Tx3 : {
    "from": "W-A2",
    "to": "SM-A1-B1",
    "value": 100,
    "data": { "recipient": "WC1" }
}
```

**Gọi hàm chuyển tiền ở contract A:**

```json
Tx4 : {
    "from": "W-A2",
    "to": "SM-A2-B2",
    "value": 200,
    "relativeAddress": [],
    "data": { "target": "SM1" }
}
```

---

### Chain B — Thực hiện giao dịch từ A yêu cầu

**Xử lý giao dịch Tx1 từ A yêu cầu:**

```json
TxB1 : {
    "from": "W-B1",
    "to": "SM-B1-A1",
    "relativeAddress": ["WC1"],
    "value": 100,
    "data": "encodeFunctionData(transfer, [100])"
}
```

**Xử lý giao dịch Tx2 từ A yêu cầu:**

```json
TxB2 : {
    "from": "W-A2",
    "to": "SM-A1-B1",
    "value": 100,
    "relativeAddress": ["SC1"],
    "data": "targetContract"
}
```

**Xử lý giao dịch Tx3 từ A yêu cầu:**

```json
TxB3 : {
    "from": "W-B1",
    "to": "SM-B2-A2",
    "relativeAddress": ["WC1"],
    "data": "encodeFunctionData(transfer, [100])"
}
```

**Xử lý giao dịch Tx4 từ A yêu cầu:**

```json
TxB4 : {
    "from": "W-A2",
    "to": "SM-A1-B1",
    "value": 100,
    "relativeAddress": ["SC1"],
    "data": "targetContract"
}
```

---

### Sau khi B xử lý xong — Chain A nhận event và tiến hành confirm

**Giao dịch thành công:**

```json
Tx1 : {
    "from": "W-A1",
    "to": "SM-A1-B1",
    "value": 100,
    "data": "confirm(id)"
}
```

**Giao dịch thất bại:**

```json
Tx2 : {
    "from": "W-A1",
    "to": "SM-A1-B1",
    "value": 100,
    "relativeAddress": ["SC1"],
    "data": "confirm(id)"
}
```

```json
Tx4 : {
    "from": "W-A1",
    "to": "SM-A2-B2",
    "value": 100,
    "relativeAddress": ["SC1"],
    "data": "confirm(id)"
}
```
