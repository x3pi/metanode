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

### 2.3 Gửi Batch Chi Tiết Nhận Danh Sách Hashes (`SendRawEthTransactionsDetailed`)

```go
// Gửi danh sách envelopes thô và nhận mảng []common.Hash tương ứng từng transaction
hashes, err := client.SendRawEthTransactionsDetailed(envelopes)
if err != nil {
    log.Fatalf("Gửi batch chi tiết thất bại: %v", err)
}
for i, h := range hashes {
    fmt.Printf("Tx %d Hash: %s\n", i, h.Hex())
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

## 4. Giới Hạn Ingress & Bảng Kích Thước (Production Limits)

| Tham số / Giới hạn | Ngưỡng tối đa | Cơ chế cưỡng chế | Hành vi khi vượt quá |
| :--- | :--- | :--- | :--- |
| **Standard Tx Envelope** | `128 KB` (131,072 bytes) | `MaxStandardTxEnvelopeSize` | Trả mã lỗi 76 (`ErrExceedsMaxEnvelopeSize`), drop ngay tại TCP readloop |
| **Blob Tx Envelope (4844)** | `1 MB` (1,048,576 bytes) | `MaxRawEthTxEnvelopeSize` | Trả mã lỗi 76 (`ErrExceedsMaxEnvelopeSize`) |
| **TCP Batch Size** | `1,000` giao dịch / request | `MaxBatchTxCount` | Trả mã lỗi 75 (`ErrExceedsMaxBatchSize`), từ chối cả batch |
| **Contract InitCode Size** | `48 KB` (49,152 bytes) | EIP-3860 `MaxInitCodeSize` | Trả mã lỗi 86 (`ErrMaxInitCodeSizeExceeded`) |
| **Access List Size** | `1,024` tuples | `MaxAccessListTuples` | Trả mã lỗi `InvalidTransaction` |
| **TCP Ingress Rate Limit** | `50,000` req/s (burst 5,000) | Token Bucket Rate Limiter | Trả message `ServerBusy` (non-blocking) |
| **TCP Read Rate Limit** | `500,000` req/s (burst 50,000) | Token Bucket Rate Limiter | Trả message `ServerBusy` (non-blocking) |

---

## 5. Bảng Mã Lỗi Ingress (Error Codes Reference)

Khi một giao dịch gửi qua TCP hoặc RPC bị từ chối, node sẽ trả về mã lỗi có kiểu theo bảng chuẩn sau:

| Mã lỗi | Tên lỗi (Go Sentinel) | Mô tả & Nguyên nhân | HTTP/JSON-RPC Code |
| :--- | :--- | :--- | :--- |
| **18** | `InvalidSign` / `InvalidSignSecp` | Chữ ký không hợp lệ, hoặc gọi nhầm lệnh BLS legacy / type 0xFF | `-32000` (invalid sender / signature) |
| **34** | `InvalidChainId` | Chain ID trong tx không khớp với Chain ID của node (`991`) | `-32000` (invalid chain ID) |
| **71** | `ErrDecodeRawEth` | Lỗi giải mã RLP envelope hoặc cấu trúc bytes sai chuẩn EIP-2718 | `-32000` (decode raw eth error) |
| **72** | `ErrPreEIP155` | Giao dịch pre-EIP-155 (không có chain ID, không được bảo vệ chống replay) | `-32000` (only replay-protected txs supported) |
| **73** | `ErrMalleableSignature` | Chữ ký dẻo: giá trị $s > N/2$ (vi phạm EIP-2) | `-32000` (malleable signature) |
| **74** | `ErrSenderRecovery` | Không thể phục hồi địa chỉ người gửi từ chữ ký secp256k1 | `-32000` (invalid sender) |
| **75** | `ErrExceedsMaxBatchSize` | Batch TCP vượt quá giới hạn 1,000 giao dịch | `-32000` (batch exceeds max size) |
| **76** | `ErrExceedsMaxEnvelopeSize`| Kích thước envelope vượt quá 128 KB (hoặc 1 MB cho blob) | `-32000` (envelope exceeds max size) |
| **77** | `ErrInvalidEnvelope` | RawEnvelope rỗng hoặc không phân tích được khi kiểm tra ràng buộc P0-9 | `-32000` (invalid envelope) |
| **86** | `ErrMaxInitCodeSizeExceeded`| Kích thước initcode khi deploy contract vượt quá 49,152 bytes (EIP-3860) | `-32000` (max initcode size exceeded) |
| **81** | `ErrNonceTooLow` | Nonce của giao dịch nhỏ hơn nonce hiện tại on-chain | `-32000` (nonce too low) |
| **82** | `ErrNonceTooHigh` | Nonce của giao dịch tạo khoảng trống (nonce gap) | `-32000` (nonce too high) |
| **83** | `ErrInsufficientFunds` | Số dư tài khoản không đủ trả `gasLimit * gasPrice + value` | `-32000` (insufficient funds) |
| **84** | `ErrAlreadyKnown` | Giao dịch cùng hash đã có sẵn trong mempool | `-32000` (already known) |
| **85** | `ErrReplacementUnderpriced`| Thay thế giao dịch cùng nonce nhưng gas price không tăng đủ tối thiểu 10% | `-32000` (replacement transaction underpriced) |
| **90** | `ErrInvalidBlobProof` | Xác minh bằng chứng KZG thất bại (KZG proof verification failed) cho giao dịch EIP-4844 blob | `-32000` (invalid blob proof) |

---

## 6. Ma Trận Hỗ Trợ JSON-RPC API

Node `simple_chain` cung cấp trực tiếp JSON-RPC chuẩn Ethereum tương thích 100% với MetaMask, Ethers.js, Viem, Foundry (`cast`/`forge`), Web3j:

### 6.1 RPC Methods Được Hỗ Trợ (Supported)
- **Giao dịch**: `eth_sendRawTransaction`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `eth_getTransactionCount`, `eth_estimateGas`, `eth_call`.
- **Khối & Chuỗi**: `eth_blockNumber`, `eth_getBlockByNumber`, `eth_getBlockByHash`, `eth_chainId`, `net_version`.
- **Trạng thái & Tài khoản**: `eth_getBalance`, `eth_getCode`, `eth_getStorageAt`.
- **Phí**: `eth_gasPrice` (trả về F = 100,000 wei), `eth_feeHistory`, `eth_maxPriorityFeePerGas` (gợi ý 0 hoặc 1 wei).
- **Log & Filter**: `eth_getLogs`, `eth_newFilter`, `eth_getFilterChanges`, `eth_uninstallFilter`.
- **Subscriptions (WebSocket)**: `eth_subscribe` (`newHeads`, `logs`), `eth_unsubscribe`.

### 6.2 RPC Methods Đã Bị Loại Bỏ / Không Hỗ Trợ (Deprecated / Removed)
- `eth_sendRawTransactionWithDeviceKey` — **ĐÃ XÓA** (chuyển sang `eth_sendRawTransaction`).
- `mtn_getDeviceKey` — **ĐÃ XÓA** (không còn mô hình device key).
- Các endpoint quản lý khóa cá nhân trên node (`eth_accounts`, `eth_sign`, `personal_sign` không giữ private key — client tự ký offline và gửi envelope qua `eth_sendRawTransaction`).

---

## 7. Các Điểm Cần Lưu Ý
1. **Chain ID**: Phải luôn khớp với chain ID của genesis (ví dụ: `991`). Giao dịch pre-EIP-155 hoặc sai chain ID sẽ bị từ chối ngay lập tức.
2. **Mô hình phí v1 (ADR D2)**:
   - Phí phẳng: `F = MINIMUM_BASE_FEE = 100,000 wei`.
   - Đối với EIP-1559: Giá hiệu dụng là `min(maxFeePerGas, F + maxPriorityFeePerGas)`.
   - Ví ngoài (Ethers/Viem) nên đặt `maxPriorityFeePerGas: 0` hoặc để mặc định, `maxFeePerGas: >= 100,000`. Phần chênh lệch giữa `maxFeePerGas` và giá hiệu dụng **không bị trừ**, người dùng chỉ trả đúng lượng gas tiêu thụ nhân với giá hiệu dụng thực tế.
3. **Tra cứu Receipt**: Dùng `eth_getTransactionReceipt(ethHash)` bình thường trên RPC. Node trả về đúng trạng thái `status: "0x1"` khi thành công và `effectiveGasPrice`.
