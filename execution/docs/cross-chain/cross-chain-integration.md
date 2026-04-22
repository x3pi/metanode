# 📋 Tài Liệu Tích Hợp: Cross-Chain BLS Embassy Signature Accumulation

## Tổng quan

Hệ thống sử dụng **TX `Type` field** (proto field 16) thay vì `ReadOnly` để đánh dấu
TX embassy "sig-only" vs "execution", kết hợp với scan progress lưu trên
`CrossChainConfigRegistry` để recovery sau crash.

---

## Bước 1: Thêm TX Type cho Cross-Chain

### 1.1 Định nghĩa constants (pkg/common hoặc pkg/cross_chain)

```go
// pkg/common/cross_chain_types.go
const (
    // TX_TYPE_CROSS_CHAIN_SIG_ACK: embassy gửi sig, chưa đủ 2/3
    // → vào block, nonce++, KHÔNG thực thi state change
    TX_TYPE_CROSS_CHAIN_SIG_ACK uint64 = 100

    // TX_TYPE_CROSS_CHAIN_EXECUTE: đủ 2/3 sig → thực thi mint/call
    TX_TYPE_CROSS_CHAIN_EXECUTE uint64 = 101
)
```

### 1.2 transaction_virtual_processor.go

```go
if tx.ToAddress() == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS {
    payload := decodeCrossChainSigPayload(tx.Data())

    // 1. Verify BLS sig của embassy này
    embassyPubKey := getEmbassyPubKey(payload.EmbassyIndex) // từ CrossChainConfigRegistry
    if !bls.Verify(embassyPubKey, payload.MessageId[:], payload.BLSSig) {
        return nil, fmt.Errorf("invalid BLS sig from embassy %d", payload.EmbassyIndex), nil
    }

    // 2. Accumulate
    tp.sigAccum.mu.Lock()
    defer tp.sigAccum.mu.Unlock()

    pending := tp.sigAccum.getOrCreate(payload.MessageId, payload.Packet)
    pending.Sigs[payload.EmbassyIndex] = payload.BLSSig

    totalEmbassies := getEmbassyCount() // từ registry contract
    threshold := (totalEmbassies*2 + 2) / 3 // ceiling(2N/3)

    if len(pending.Sigs) < threshold {
        // Chưa đủ → SIG_ACK: vào block, không execute
        updatedTx := tx
        updatedTx.SetType(TX_TYPE_CROSS_CHAIN_SIG_ACK) // ← dùng Type thay ReadOnly
        updatedTx.AddRelatedAddress(tx.FromAddress())
        return updatedTx, nil, []byte(fmt.Sprintf("sig_ack:%d/%d", len(pending.Sigs), threshold))
    }

    // Đủ 2/3 → EXECUTE
    aggSig := bls.Aggregate(collectSigs(pending.Sigs))
    delete(tp.sigAccum.pending, payload.MessageId)

    updatedTx := tx
    updatedTx.SetType(TX_TYPE_CROSS_CHAIN_EXECUTE) // ← execute type
    updatedTx.SetData(encodeFinalExecPayload(payload.Packet, aggSig))
    updatedTx.AddRelatedAddress(tx.FromAddress())
    return updatedTx, nil, nil
}
```

### 1.3 tx_processor.go

```go
if tx.ToAddress() == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS {
    txType := tx.GetType()

    switch txType {
    case TX_TYPE_CROSS_CHAIN_SIG_ACK:
        // Chưa đủ sig → không làm gì, TX vào block bình thường
        // → receipt RETURNED, returnData = "sig_ack:N/M"
        // → nonce của embassy tăng lên (TX đã confirmed vào block)
        return buildReturnedReceipt(tx, tx.Data()), nil

    case TX_TYPE_CROSS_CHAIN_EXECUTE:
        // Đủ 2/3 sig → thực thi
        data := decodeFinalExecPayload(tx.Data())
        packet := data.Packet

        switch {
        case packet.Target == (common.Address{}):
            // ASSET_TRANSFER → mint cho recipient
            recipient := abi.Decode(packet.Payload) // address
            recipientState, _ := chainState.GetAccountStateDB().AccountState(recipient)
            recipientState.AddBalance(packet.Value)

        case packet.Target != (common.Address{}):
            // CONTRACT_CALL → gọi contract với value
            result := evm.Call(packet.Sender, packet.Target, packet.Payload, packet.Value)
            if !result.Success {
                // emit MessageReceived FAILED → embassy scan → refund on source chain
            }
        }
        // emit MessageReceived SUCCESS event vào receipt
        return buildSuccessReceiptWithEvent(tx, "MessageReceived", packet.MessageId), nil
    }
}
```

---

## Bước 2: Sửa CrossChainConfigRegistry — Dùng "Embassy" Thống Nhất

### 2.1 Xóa ambassadorAddresses, thay bằng embassyAddresses

Hiện tại contract có `embassyList` (bytes publicKey) nhưng cần thêm `embassyAddresses` (address)
vì scan progress dùng address (msg.sender) làm key:

```solidity
// Thêm vào CrossChainConfigRegistry.sol
/// @notice ETH address của từng embassy (tương ứng với embassyList)
address[] public embassyAddresses;

/// @notice Mapping: address → có phải embassy không
mapping(address => bool) public isEmbassyAddress;

/// @notice Khi addEmbassy, đăng ký thêm address
function addEmbassyWithAddress(
    bytes calldata _publicKey,
    address _embassyAddress
) external onlyOwner {
    // ... existing addEmbassy logic ...
    isEmbassyAddress[_embassyAddress] = true;
    embassyAddresses.push(_embassyAddress);
}

/// @notice batchUpdateScanProgress — 1 TX cập nhật nhiều chainId
/// Embassy chạy N goroutine scan song song → gom vào 1 TX duy nhất
function batchUpdateScanProgress(
    uint256[] calldata sourceNationIds,
    uint256[] calldata lastScannedBlocks
) external {
    require(isEmbassyAddress[msg.sender], "Not an embassy");
    // ... cập nhật scanProgress[keccak256(msg.sender, nationId)] ...
}

/// @notice Block thấp nhất embassy đã scan → điểm bắt đầu khi restart
function getMinScanBlock(uint256 sourceNationId) external view returns (uint256)
```

---

## Bước 3: Flow Embassy Gửi TX

### 3.1 Embassy build payload

```go
// Mỗi embassy tự gửi TX riêng, KHÔNG cần P2P
type CrossChainSigPayload struct {
    MessageId    [32]byte
    Packet       CrossChainPacket  // sourceId, destId, sender, target, value, payload, ts
    BLSSig       []byte            // BLS sig của embassy này
    EmbassyIndex uint8             // vị trí trong embassyList (để verify)
}

func (obs *Observer) sendSigTx(event *MessageSentEvent) {
    msgId := computeMessageId(event)
    sig   := bls.Sign(obs.privateKey, msgId[:])

    payload := CrossChainSigPayload{
        MessageId:    msgId,
        Packet:       buildPacket(event),
        BLSSig:       sig,
        EmbassyIndex: obs.embassyIndex,
    }

    // Dùng wallet pool để TX không conflict nonce với nhau
    // wallet index = deterministic từ messageId + embassyIndex
    walletIdx := (binary.BigEndian.Uint64(msgId[:8]) % uint64(poolSize)) +
                 uint64(obs.embassyIndex) * uint64(poolSize)
    wallet := obs.walletPool[walletIdx]

    obs.chainClient.SendTransaction(
        From:    wallet.Address,
        To:      CROSS_CHAIN_CONTRACT_ADDRESS,
        Data:    encode(payload),
    )
}
```

### 3.2 Embassy cập nhật scan progress (1 TX / batch)

```go
// Chạy sau mỗi chu kỳ scan (ví dụ mỗi 30s hoặc sau N blocks)
func (obs *Observer) updateScanProgress() {
    // Collect kết quả từ tất cả goroutine scan song song
    progress := map[uint64]uint64{}
    for _, chain := range obs.remoteChains {
        progress[chain.NationId] = obs.lastScannedBlock[chain.NationId]
    }

    // Batch update → 1 TX duy nhất, 1 nonce từ parent_address
    nationIds := []uint256{}
    blocks    := []uint256{}
    for nationId, block := range progress {
        nationIds = append(nationIds, nationId)
        blocks    = append(blocks, block)
    }

    // Gọi batchUpdateScanProgress trên CrossChainConfigRegistry
    obs.registryClient.BatchUpdateScanProgress(nationIds, blocks)
}
```

---

## Bước 4: Startup Recovery — Recover sigAccum từ Blockchain

```go
// Khi chain B khởi động lại, virtual processor recover sigAccum:
func (tp *TransactionProcessor) RecoverSigAccumulator() {
    // 1. Lấy min scan block của tất cả embassies từ registry
    // (đây là giới hạn dưới: events trước block này đã được xử lý chắc chắn)
    minBlock := tp.queryMinScanBlock(sourceNationId) // read từ CrossChainConfigRegistry

    // 2. Scan lại blocks từ minBlock → latestBlock
    latestBlock := storage.GetLastBlockNumber()
    executedSet := map[common.Hash]bool{}

    for blockNum := minBlock; blockNum <= latestBlock; blockNum++ {
        block := storage.GetBlock(blockNum)
        for _, tx := range block.Transactions() {
            if tx.To() != CROSS_CHAIN_CONTRACT_ADDRESS { continue }

            receipt := storage.GetReceipt(tx.Hash())
            payload := decodeCrossChainSigPayload(tx.Data())

            // Phân biệt dựa vào TX Type:
            switch tx.GetType() {
            case TX_TYPE_CROSS_CHAIN_EXECUTE:
                // TX này đã thực thi → messageId đã xong
                executedSet[payload.MessageId] = true

            case TX_TYPE_CROSS_CHAIN_SIG_ACK:
                // TX này là sig-only → recover sig vào accumulator
                if !executedSet[payload.MessageId] {
                    tp.sigAccum.addSig(
                        payload.MessageId,
                        payload.EmbassyIndex,
                        payload.BLSSig,
                        payload.Packet,
                    )
                }
            }
        }
    }
    logger.Info("🔄 [CrossChain] Recovered sigAccum: %d pending messages", len(tp.sigAccum.pending))
}
```

---

## Bước 5: Tổng Hợp Tất Cả Flows

### 5.1 Happy path (không crash)

```
User lockAndBridge trên Chain A (block 100)
  ↓
Embassy-1 scan GetLogs (Chain A) → detect MessageSent
Embassy-2 scan GetLogs (Chain A) → detect MessageSent
Embassy-3 scan GetLogs (Chain A) → detect MessageSent
  ↓ (3 goroutines độc lập, không P2P)
Embassy-1 send TX{sigPayload, sig1} → Chain B
  └→ virtual: sigAccum[msgId]={1} → Type=SIG_ACK → block, nonce++, receipt ack
Embassy-2 send TX{sigPayload, sig2} → Chain B
  └→ virtual: sigAccum[msgId]={1,2} → Type=SIG_ACK → block, nonce++, receipt ack
Embassy-3 send TX{sigPayload, sig3} → Chain B
  └→ virtual: sigAccum[msgId]={1,2,3} → ĐỦ! → aggregate → Type=EXECUTE
  └→ EVM: mint/call → receipt SUCCESS
  └→ emit MessageReceived(msgId, SUCCESS)
  ↓
Embassy scan MessageReceived trên Chain B → confirm success
  ↓
Batch update scan progress trên Chain B registry (1 TX / embassy)
```

### 5.2 Crash recovery

```
Chain B crash tại bất kỳ thời điểm nào
  ↓
Chain B restart → RecoverSigAccumulator():
  query getMinScanBlock(sourceNationId) từ registry → block 480
  scan blocks 480→latestBlock
    → TX_TYPE_CROSS_CHAIN_EXECUTE events → executedSet
    → TX_TYPE_CROSS_CHAIN_SIG_ACK events → rebuild sigAccum
  sigAccum restored ✅
  ↓
Embassies retry loop (mỗi 60s):
  "messageId X confirmed chưa?" → check MessageReceived event on Chain B
  → chưa → gửi lại sig TX → accumulate → execute ✅
```

---

## Bước 6: Checklist Tích Hợp

- [ ] **pkg/common**: Thêm `TX_TYPE_CROSS_CHAIN_SIG_ACK = 100`, `TX_TYPE_CROSS_CHAIN_EXECUTE = 101`
- [ ] **CrossChainConfigRegistry.sol**: Thêm `addEmbassyWithAddress`, `batchUpdateScanProgress`, `getMinScanBlock`
- [ ] **transaction_virtual_processor.go**: Thêm case `CROSS_CHAIN_CONTRACT_ADDRESS` với sig accumulation
- [ ] **tx_processor.go**: Thêm switch case `TX_TYPE_CROSS_CHAIN_SIG_ACK` / `TX_TYPE_CROSS_CHAIN_EXECUTE`
- [ ] **TransactionProcessor struct**: Thêm `sigAccum *SigAccumulator`
- [ ] **block_processor hoặc startup**: Gọi `RecoverSigAccumulator()` khi khởi động
- [ ] **Observer**: `sendSigTx()` + `updateScanProgress()` + retry loop
- [ ] **Observer config**: `embassy_index`, `wallet_pool_size`

---

## Data Structures

```go
// SigAccumulator — in-memory, rebuilt từ blocks khi restart
type SigAccumulator struct {
    mu      sync.Mutex
    pending map[common.Hash]*PendingMessage
}

type PendingMessage struct {
    Packet      CrossChainPacket
    Sigs        map[uint8][]byte  // embassyIndex → blsSig
    ReceivedAt  time.Time
}

// CrossChainSigPayload — data trong TX từ embassy
type CrossChainSigPayload struct {
    MessageId    [32]byte
    Packet       CrossChainPacket
    BLSSig       []byte
    EmbassyIndex uint8
}

// CrossChainExecPayload — data trong TX khi đủ sig (TYPE_EXECUTE)
type CrossChainExecPayload struct {
    Packet  CrossChainPacket
    AggSig  []byte  // BLS aggregate signature
}
```
