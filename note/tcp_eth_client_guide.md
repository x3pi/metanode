# Hướng dẫn Tích hợp Client TCP & Mobile: Giao dịch Chuẩn Ethereum (EIP-2718)

Tài liệu này hướng dẫn cách gửi giao dịch chuẩn Ethereum (EIP-2718: Legacy EIP-155, EIP-2930, EIP-1559, EIP-4844) qua cổng TCP và RPC của Metanode trong chế độ `tx_signature_mode="secp"`.

---

## 1. Tổng quan Kiến trúc Ingress

Trong chế độ `tx_signature_mode="secp"`:
1. **TCP Native Ingress**:
   - `SendRawTransaction`: Nhận trực tiếp byte envelope EIP-2718 thô (legacy RLP hoặc `type || payload`).
   - `SendRawTransactions`: Nhận mảng các envelopes đóng gói theo chuẩn RLP: `rlp.EncodeToBytes([][]byte{env1, env2, ...})`.
   - Node giải mã bằng `types.Transaction.UnmarshalBinary` của go-ethereum và chuyển đổi sang MetaTx nội bộ.
2. **Khóa bảo vệ Ingress**:
   - Cấm giao dịch pre-EIP-155 (không có chain ID).
   - Kiểm tra chain ID phải khớp với chain ID của mạng (ví dụ: `991`).
   - Chống chữ ký malleable: $s \le N/2$ theo EIP-2.
   - Các lệnh proto cũ (`SendTransaction`, `SendTransactions`, `SendTransactionWithDeviceKey`) và Type `0xFF` bị từ chối với mã lỗi `18 (InvalidSign)`.
3. **Ánh xạ Hash và Receipt**:
   - Trước khi đưa vào pool, node đăng ký ánh xạ `SetEthHashMapblsHash(ethTx.Hash(), metaTx.Hash())`.
   - Khi client tra cứu qua RPC (`eth_getTransactionReceipt` hoặc `eth_getTransactionByHash`), client sử dụng trực tiếp `ethHash` chuẩn Ethereum (`ethTx.Hash()`).
   - Phản hồi TCP `TransactionSuccess` trả về hash giao dịch để xác nhận việc nhận giao dịch vào mempool.

---

## 2. Gửi qua TCP Client (Go)

### 2.1 Sử dụng `ConnectionClient`

```go
import (
    "math/big"
    "time"
    e_types "github.com/ethereum/go-ethereum/core/types"
    "github.com/ethereum/go-ethereum/crypto"
    "github.com/meta-node-blockchain/meta-node/pkg/connection_manager/connection_client"
)

// 1. Tạo và ký giao dịch EIP-1559 chuẩn Ethereum
chainID := big.NewInt(991)
signer := e_types.LatestSignerForChainID(chainID)

txData := &e_types.DynamicFeeTx{
    ChainID:   chainID,
    Nonce:     nonce,
    GasTipCap: big.NewInt(10000),
    GasFeeCap: big.NewInt(200000),
    Gas:       21000,
    To:        &recipientAddress,
    Value:     amountWei,
    Data:      data,
}

signedTx, err := e_types.SignTx(e_types.NewTx(txData), signer, privateKey)
if err != nil {
    log.Fatalf("Ký tx thất bại: %v", err)
}

// 2. Lấy envelope bytes thô (0x02 || rlp_payload)
rawEnvelope, err := signedTx.MarshalBinary()
if err != nil {
    log.Fatalf("Marshal binary thất bại: %v", err)
}

// 3. Gửi qua ConnectionClient
// client là *connection_client.ConnectionClient đã kết nối TCP tới node
err = client.SendRawTransaction(rawEnvelope)
if err != nil {
    log.Fatalf("Gửi raw tx qua TCP thất bại: %v", err)
}

ethHash := signedTx.Hash()
fmt.Printf("Giao dịch đã gửi thành công, ethHash: %s\n", ethHash.Hex())
```

### 2.2 Gửi Batch Nhiều Giao Dịch (`SendRawTransactions`)

```go
import (
    "github.com/ethereum/go-ethereum/rlp"
)

var envelopes [][]byte
for _, tx := range signedTxs {
    raw, _ := tx.MarshalBinary()
    envelopes = append(envelopes, raw)
}

batchRLP, err := rlp.EncodeToBytes(envelopes)
if err != nil {
    log.Fatalf("Encode RLP batch thất bại: %v", err)
}

err = client.SendRawTransactions(batchRLP)
if err != nil {
    log.Fatalf("Gửi batch thất bại: %v", err)
}
```

---

## 3. Hướng dẫn Dapp & Mobile (Android / iOS / Web)

### 3.1 Khuyến nghị Kiến trúc
- **Mobile (Android/iOS) và Web Dapps:** **Nên sử dụng RPC `eth_sendRawTransaction`** qua HTTP hoặc WebSocket.
- Không cần tự cài đặt socket TCP hay framing nhị phân của Metanode trên mobile.
- Mọi thư viện Ethereum tiêu chuẩn (web3j trên Android/JVM, ethers/viem trên JS/React Native, web3.swift trên iOS) đều tương thích 100%.

### 3.2 Ví dụ Android (web3j)

```java
import org.web3j.crypto.Credentials;
import org.web3j.crypto.RawTransaction;
import org.web3j.crypto.TransactionEncoder;
import org.web3j.protocol.Web3j;
import org.web3j.protocol.core.methods.response.EthSendTransaction;
import org.web3j.protocol.http.HttpService;
import org.web3j.utils.Numeric;

Web3j web3 = Web3j.build(new HttpService("http://192.168.1.232:8545"));
Credentials credentials = Credentials.create("YOUR_PRIVATE_KEY_HEX");

long chainId = 991L;
BigInteger nonce = web3.ethGetTransactionCount(credentials.getAddress(), DefaultBlockParameterName.PENDING)
                       .send().getTransactionCount();

// Lưu ý: Đặt gasPrice/maxFeePerGas >= MINIMUM_BASE_FEE (100,000 wei)
BigInteger maxPriorityFeePerGas = BigInteger.valueOf(10_000L);
BigInteger maxFeePerGas = BigInteger.valueOf(200_000L);
BigInteger gasLimit = BigInteger.valueOf(21_000L);

RawTransaction rawTx = RawTransaction.createEtherTransaction(
    chainId,
    nonce,
    gasLimit,
    "0xRecipientAddress...",
    BigInteger.valueOf(1_000_000_000_000_000_000L), // 1 ETH
    maxPriorityFeePerGas,
    maxFeePerGas
);

byte[] signedMessage = TransactionEncoder.signMessage(rawTx, chainId, credentials);
String hexValue = Numeric.toHexString(signedMessage);

EthSendTransaction response = web3.ethSendRawTransaction(hexValue).send();
if (response.hasError()) {
    System.err.println("Gửi tx thất bại: " + response.getError().getMessage());
} else {
    String ethTxHash = response.getTransactionHash();
    System.out.println("Tx Hash: " + ethTxHash);
}
```

### 3.3 Ví dụ Ethers.js / Viem (Node.js & React Native)

```typescript
import { ethers } from "ethers";

const provider = new ethers.JsonRpcProvider("http://192.168.1.232:8545");
const wallet = new ethers.Wallet("0xYOUR_PRIVATE_KEY", provider);

// Gửi giao dịch EIP-1559 tiêu chuẩn
const tx = await wallet.sendTransaction({
    to: "0xRecipientAddress...",
    value: ethers.parseEther("0.1"),
    maxPriorityFeePerGas: 10000n,
    maxFeePerGas: 200000n,
});

console.log("Tx Hash:", tx.hash);
const receipt = await tx.wait();
console.log("Receipt status:", receipt.status);
```

---

## 4. Các Điểm Cần Lưu Ý
1. **Chain ID**: Phải luôn khớp với chain ID của genesis (ví dụ: `991`). Giao dịch pre-EIP-155 hoặc sai chain ID sẽ bị từ chối ngay lập tức.
2. **Phí gas**: Hiện tại chain sử dụng mô hình phí phẳng với `MINIMUM_BASE_FEE = 100000`. Khi gửi tx, `maxFeePerGas` (hoặc `gasPrice` cho legacy) phải $\ge 100,000$.
3. **Tra cứu Receipt**: Dùng `eth_getTransactionReceipt(ethHash)` bình thường trên RPC. Node tự động tra cứu qua bảng ánh xạ `ethHash → metaHash`.
