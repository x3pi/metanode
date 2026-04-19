# MessageReceived Event Handling - Confirmation & Refund Flow

## Tổng quan

Hệ thống đã được cập nhật để:

1. **Listen cả 2 events**: `MessageSent` và `MessageReceived` trong cùng một listener
2. **Unified message queue**: Xử lý cả 2 loại event trong cùng một worker
3. **Auto-confirmation**: Khi message thành công, tự động confirm (mark as processed)
4. **Auto-refund**: Khi message thất bại, tự động gọi `handleFailedMessage` để hoàn tiền

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│ CrossChainListener                                              │
├─────────────────────────────────────────────────────────────────┤
│ - messageQueue: chan messageEvent (unified queue)               │
│ - messageCache: sync.Map (cache original message info)          │
│                                                                 │
│ Events:                                                         │
│   1. MessageSent    → Cache info → Queue → Process             │
│   2. MessageReceived → Queue → Check cache → Confirm/Refund    │
└─────────────────────────────────────────────────────────────────┘
```

## Data Structures

### messageEvent

```go
type messageEvent struct {
    eventType string                 // "MessageSent" or "MessageReceived"
    eventData map[string]interface{}
    txHash    common.Hash
    topic0    common.Hash
}
```

### cachedMessageInfo

```go
type cachedMessageInfo struct {
    MessageId [32]byte
    Sender    common.Address
    Amount    uint64 // msg.value from original transaction
    MsgType   uint8  // MessageType (ASSET_TRANSFER or CONTRACT_CALL)
}
```

## Flow Diagram

### 1. MessageSent Event (Chain A → Chain B)

```
┌─────────────────────────────────────────────────────────────────┐
│ STEP 1: User sends message on Chain A                          │
└─────────────────────────────────────────────────────────────────┘
User calls: sendMessage(destNationId, target, payload) payable
    ↓
Contract emits: MessageSent(messageId, sourceId, destId, nonce, ...)
    ↓
    │
    ▼
┌─────────────────────────────────────────────────────────────────┐
│ STEP 2: Observer listens MessageSent                           │
└─────────────────────────────────────────────────────────────────┘
CrossChainListener.handleMessageSentEvent()
    ↓
Parse event → MessageSent struct
    ↓
Cache message info:
    messageCache.Store(messageId, &cachedMessageInfo{
        MessageId: messageId,
        Sender:    sender,
        Amount:    msg.value,  // TODO: Get from tx receipt
        MsgType:   msgType,
    })
    ↓
Add to queue:
    messageQueue <- messageEvent{
        eventType: "MessageSent",
        eventData: eventData,
        txHash:    txHash,
        topic0:    topic0,
    }
    ↓
    │
    ▼
┌─────────────────────────────────────────────────────────────────┐
│ STEP 3: Worker processes MessageSent                           │
└─────────────────────────────────────────────────────────────────┘
worker() receives event
    ↓
Switch on eventType:
    case "MessageSent":
        processor.HandleCrossChainMessage()
            ↓
            Check nonce ordering
            ↓
            If nonce == expected:
                Call receiveMessage() on Chain B
            Else:
                Add to pending pool
                Query missing events
    ↓
    │
    ▼
┌─────────────────────────────────────────────────────────────────┐
│ STEP 4: Chain B processes message                              │
└─────────────────────────────────────────────────────────────────┘
receiveMessage(packet, signature, sourceBlockNumber)
    ↓
Validate nonce, anti-replay
    ↓
_processMessage()
    ↓
    ├─ Try execute (unlock/contract call)
    │   ├─ SUCCESS → Emit MessageReceived(SUCCESS, "...")
    │   └─ FAILED  → Emit MessageReceived(FAILED, error_reason)
```

### 2. MessageReceived Event (Chain B → Chain A)

```
┌─────────────────────────────────────────────────────────────────┐
│ STEP 1: Chain B emits MessageReceived                          │
└─────────────────────────────────────────────────────────────────┘
Contract emits: MessageReceived(messageId, sourceId, nonce, 
                                msgType, status, remark)
    ↓
    │
    ▼
┌─────────────────────────────────────────────────────────────────┐
│ STEP 2: Observer listens MessageReceived                       │
└─────────────────────────────────────────────────────────────────┘
CrossChainListener.handleDecodedEvent()
    ↓
case "MessageReceived":
    Add to queue:
        messageQueue <- messageEvent{
            eventType: "MessageReceived",
            eventData: eventData,
        }
    ↓
    │
    ▼
┌─────────────────────────────────────────────────────────────────┐
│ STEP 3: Worker processes MessageReceived                       │
└─────────────────────────────────────────────────────────────────┘
worker() receives event
    ↓
Switch on eventType:
    case "MessageReceived":
        processMessageReceived(eventData)
            ↓
            Parse event → MessageReceived struct
            ↓
            Check if already processed:
                isProcessed = IsMessageProcessed(messageId)
                if isProcessed:
                    Skip (already confirmed/refunded)
                    return
            ↓
            ├─ If status == SUCCESS:
            │   ↓
            │   Log confirmation
            │   (Optional: Call confirm function on contract)
            │   ✅ Done
            │
            └─ If status == FAILED:
                ↓
                Get cached info:
                    cachedInfo = messageCache.Load(messageId)
                    if not found:
                        Error: Cannot refund without cached info
                        return
                ↓
                Extract: sender, amount, msgType
                ↓
                Call handleFailedMessage on Chain A:
                    HandleFailedMessage(
                        messageId,
                        sender,
                        msgType,
                        amount,
                    )
                ↓
                Remove from cache:
                    messageCache.Delete(messageId)
                ↓
                ✅ Refund completed
```

### 3. Refund Flow (Chain A)

```
┌─────────────────────────────────────────────────────────────────┐
│ handleFailedMessage() on Chain A                               │
└─────────────────────────────────────────────────────────────────┘
Validate:
    - sender != zero address
    - amount > 0
    ↓
Decrease totalLocked:
    totalLocked -= amount
    ↓
Refund to sender:
    sender.call{value: amount}("")
    ↓
Emit MessageFailed:
    MessageFailed(messageId, sourceId, 0, sender, "refunded")
    ↓
    ✅ User receives refund
```

## Key Functions

### Go Functions

#### `handleMessageSentEvent(eventData, txHash, topic0)`

1. Parse `MessageSent` event
2. Cache message info (messageId → sender, amount, msgType)
3. Add to message queue with eventType="MessageSent"

#### `processMessageReceived(eventData)`

1. Parse `MessageReceived` event
2. Check if already processed (avoid duplicate)
3. If SUCCESS: Log confirmation
4. If FAILED:
   - Get cached message info
   - Call `HandleFailedMessage` on source chain
   - Remove from cache

#### `worker()`

- Unified worker for both event types
- Switch on `eventType`:
  - "MessageSent" → `processor.HandleCrossChainMessage()`
  - "MessageReceived" → `processMessageReceived()`

### Contract Functions

#### `isMessageProcessed(messageId) view returns (bool)`

- Check if message has been confirmed/refunded
- Used to prevent duplicate processing

#### `handleFailedMessage(messageId, sender, msgType, amount)`

- Called by Embassy when MessageReceived with FAILED status
- Refunds `amount` to `sender`
- Emits `MessageFailed` event

## Cache Management

### When to Cache

- **On MessageSent**: Cache original message info for potential refund

### What to Cache

```go
cachedMessageInfo{
    MessageId: [32]byte,  // Unique message identifier
    Sender:    address,   // Who to refund
    Amount:    uint64,    // How much to refund
    MsgType:   uint8,     // ASSET_TRANSFER or CONTRACT_CALL
}
```

### When to Remove from Cache

- **On successful refund**: After `HandleFailedMessage` completes
- **On confirmation**: (Optional) After successful processing
- **On timeout**: (Future) After X time without MessageReceived

## Error Handling

### Scenario 1: MessageReceived but no cached info

```
ERROR: Cannot find cached message info for MessageId=0x123..., cannot refund
```

**Solution**: Ensure MessageSent is processed before MessageReceived

### Scenario 2: Message already processed

```
INFO: Message already processed (confirmed/refunded), skipping: MessageId=0x123...
```

**Solution**: This is normal, duplicate event is safely ignored

### Scenario 3: Refund transaction fails

```
ERROR: Failed to handle failed message: insufficient funds
```

**Solution**: Ensure source chain contract has sufficient balance

## Configuration

### Message Queue Size

```go
messageQueue: make(chan messageEvent, 100)
```

- Default: 100 events
- Increase if experiencing dropped events

### Cache Cleanup

- **Current**: Manual cleanup on successful refund
- **Future**: Add periodic cleanup for old entries (e.g., > 24h)

## Testing Scenarios

### Test 1: Successful Message

1. Send message on Chain A
2. Process on Chain B → SUCCESS
3. Verify: MessageReceived with status=SUCCESS
4. Verify: No refund triggered

### Test 2: Failed Message with Refund

1. Send message with value on Chain A
2. Process on Chain B → FAILED
3. Verify: MessageReceived with status=FAILED
4. Verify: handleFailedMessage called
5. Verify: Sender receives refund
6. Verify: Cache entry removed

### Test 3: Duplicate MessageReceived

1. Process MessageReceived (FAILED)
2. Trigger refund
3. Receive duplicate MessageReceived
4. Verify: Skipped (already processed)

### Test 4: MessageReceived without cached info

1. Restart observer (cache cleared)
2. Receive MessageReceived (FAILED)
3. Verify: Error logged, no refund attempted

## Future Improvements

1. **Persistent Cache**: Store cached message info in database instead of memory
2. **Transaction Receipt Parsing**: Extract `msg.value` from transaction receipt
3. **Batch Refunds**: Support refunding multiple failed messages in one transaction
4. **Timeout Handling**: Auto-refund if MessageReceived not received within X time
5. **Metrics**: Track success/failure rates, refund amounts, etc.
6. **Retry Logic**: Retry failed refund transactions with exponential backoff

## Security Considerations

1. **Cache Poisoning**: Validate all cached data before use
2. **Duplicate Processing**: Always check `isMessageProcessed` before refund
3. **Amount Validation**: Verify refund amount matches original locked amount
4. **Sender Validation**: Ensure refund goes to original sender only
5. **Access Control**: Only Embassy (owner) can call `handleFailedMessage`

## Monitoring & Logging

### Key Log Messages

#### MessageSent

```
INFO: Nhận sự kiện MessageSent, đưa vào hàng chờ xử lý: Nonce=1, From=0x123..., To=0x456...
```

#### MessageReceived - Success

```
INFO: 📨 Received MessageReceived event: MessageId=0x789..., Status=0, MsgType=0, Remark=Asset transfer successful
INFO: ✅ Message processed successfully on destination chain, sending confirmation...
INFO: ✅ Confirmation completed for MessageId=0x789...
```

#### MessageReceived - Failed

```
WARN: 📨 Received MessageReceived event: MessageId=0xabc..., Status=1, MsgType=1, Remark=Contract call failed
WARN: ❌ Message failed on destination chain, triggering refund...
WARN:    Reason: Contract call failed
INFO: 🔄 Calling handleFailedMessage on source chain...
INFO:    Sender: 0xdef..., Amount: 1000000000000000000, MsgType: 1
INFO: ✅ Refund completed for MessageId=0xabc...
```

### Metrics to Track

- Total MessageSent events processed
- Total MessageReceived events (SUCCESS vs FAILED)
- Total refunds triggered
- Total refund amount
- Average confirmation time
- Cache hit/miss rate
