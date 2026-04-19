## Thiết kế luồng (User thường, Admin) — Website dựa trên rpc-client

### Sơ đồ tổng quan vai trò

```mermaid
flowchart LR
  subgraph Web[Website Frontend]
    U[User thường<br/>MetaMask/WalletConnect]
    A[Admin<br/>MetaMask/WalletConnect]
  end
  subgraph S[RPC Server ký hộ]
    COSIGN[/Co-sign BLS/]
    ADMINAPI[/Admin APIs/]
  end
  subgraph L1[Blockchain L1 + Smart Contract]
    ACCT2[accountType=2<br/>Require ETH+BLS]
    VERIFY[Verify sig_eth + sig_bls]
  end
  subgraph EXP[Explorer]
    TXS[Txs, Receipts, Events]
  end

  U -->|Ký ETH| COSIGN
  COSIGN -->|Ký BLS + Broadcast| L1
  A --> ADMINAPI
  ADMINAPI -->|Config accountType=2, BLS pubkey, Fund| L1
  L1 --> EXP
  U -->|Xem lịch sử (địa chỉ)| EXP
  A -->|Xem lịch sử user quản lý| EXP
```

### Luồng 1 — User thường co-sign (server ký hộ BLS)

```mermaid
sequenceDiagram
  participant User(Web)
  participant RPC as RPC Server
  participant L1 as Smart Contract (L1)
  participant EXP as Explorer

  User->>User: Tạo message (EIP-712) + Ký ETH (sig_eth)
  User->>RPC: POST /cosign/bls {message, sig_eth, meta}
  RPC->>RPC: Xác thực ví + Policy co-sign
  RPC->>L1: Ký BLS (sig_bls) + Broadcast
  L1-->>RPC: tx_hash
  RPC-->>User: {sig_bls, tx_hash}
  L1-->>EXP: Giao dịch, receipt, events
  User->>EXP: Xem lịch sử giao dịch
```

### Luồng 2 — User VIP tự ký (ETH+BLS), tự trả phí

```mermaid
sequenceDiagram
  participant User(Web)
  participant L1 as Smart Contract (L1)
  participant EXP as Explorer

  User->>User: Tạo message (EIP-712)
  User->>User: Ký ETH + Ký BLS cục bộ
  User->>L1: Broadcast tx (đã đủ 2 chữ ký)
  L1-->>User: tx_hash
  L1-->>EXP: Giao dịch, receipt, events
  User->>EXP: Xem lịch sử giao dịch
```

### Phân hệ Admin

```mermaid
flowchart TB
  A[Admin đăng nhập ví] --> AU[Auth: ký nonce]
  AU --> CFG[Quản trị tài khoản user]
  CFG -->|Bật accountType=2| L1[(L1)]
  CFG -->|Đăng ký/thu hồi BLS pubkey| L1
  CFG -->|Nạp ME/ETH| L1
  A --> HIST[Xem lịch sử user quản lý]
  HIST --> EXP[Explorer]
  L1 --> EXP
```

Ghi chú:
- Bắt buộc 2 chữ ký (ETH + BLS) ở `accountType=2`.
- Nếu thiếu chữ ký user, tx server đẩy bị bác bỏ.
- Explorer là nguồn sự thật cho lịch sử giao dịch.


