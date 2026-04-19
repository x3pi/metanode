import re

path = "/home/abc/chain-n/mtn-simple-2025/cmd/simple_chain/processor/tx_batch_forwarder_core.go"
with open(path, "r") as f:
    content = f.read()

# 1. Remove txsender import
content = content.replace('\n\t"github.com/meta-node-blockchain/meta-node/pkg/txsender"', '')
content = content.replace('txSender             *txsender.ChannelBasedSender', '')

# 2. Add executor import
content = content.replace('"github.com/meta-node-blockchain/meta-node/pkg/transaction"', '"github.com/meta-node-blockchain/meta-node/pkg/transaction"\n\t"github.com/meta-node-blockchain/meta-node/executor"')

# 3. Remove UDS Fallback logic and TxSender creation (from line 56 to 173)
# It span from "// CHANNEL-BASED ARCHITECTURE: Sử dụng UDS thay vì TCP" to "setEmptyBlock := false"
start_str = "// CHANNEL-BASED ARCHITECTURE: Sử dụng UDS thay vì TCP cho Go-sub → Rust"
end_str = "\n\t\tsetEmptyBlock := false"
idx_start = content.find(start_str)
idx_end = content.find(end_str)
if idx_start != -1 and idx_end != -1:
    content = content[:idx_start] + "// LOCALHOST OPTIMIZATION: Tăng concurrent sends (removed for FFI)" + content[idx_end:]


# 4. Replace SendBatch logic
old_send_logic = """nel (non-blocking) hoặc TCP fallback
der != nil {
ua channel sender
der.SendBatch(bTransaction); err != nil {
sactions might be lost if not handled
("⚠️  [TX FLOW] Failed to queue batch [%d/%d] (%d txs) to channel: error=%v (will re-add to pool)",
um, totalBatches, len(batchTxs), err)
saction pool for retry in next tick
sactionProcessor.transactionPool.AddTransactions(batchTxs)
Pool
ge batchTxs {
sactionProcessor.pendingTxManager.UpdateStatus(tx.Hash(), StatusInPool)
tinue
d {
fo("✅ [TX FLOW] Queued batch [%d/%d]: %d txs via UDS",
um, totalBatches, len(batchTxs))
OTE: pendingTxManager.Remove() loop removed — UDS path never
dingTxManager.Add(), so Remove() was always a no-op
c.Map lookups per test run).
e stats: track TXs forwarded to Rust
eStats.IncrTxsForwarded(int64(len(batchTxs)))
SENSUS TIMER: stamp when last batch enters Rust ──────
dBatchTimeNano.Store(time.Now().UnixNano())
dBatchTxCount.Store(int64(totalTxs))
gTCPFallback {
hưng sender bị nil (tất cả validator UDS cũng fail)
stead of dropping TX permanently
("⚠️  [TX FORWARD] All validator UDS paths unavailable, re-adding batch [%d/%d] (%d txs) to pool",
um, totalBatches, len(batchTxs))
sactionProcessor.transactionPool.AddTransactions(batchTxs)
tinue
nel sender chưa sẵn sàng và chưa ở TCP fallback mode
stead of silently dropping TX
("⚠️  [TX FLOW] Channel sender not ready, re-adding batch [%d/%d] (%d txs) to pool for retry",
um, totalBatches, len(batchTxs))
sactionProcessor.transactionPool.AddTransactions(batchTxs)
tinue
ew_send_logic = """ channel
g(p_common.ServiceTypeMaster) || bf.serviceType == string(p_common.ServiceTypeWrite) {
sactionBatch(bTransaction); !success {
("⚠️  [TX FLOW] Failed to submit batch [%d/%d] (%d txs) to FFI (will re-add to pool)",
um, totalBatches, len(batchTxs))
sactionProcessor.transactionPool.AddTransactions(batchTxs)
ge batchTxs {
sactionProcessor.pendingTxManager.UpdateStatus(tx.Hash(), StatusInPool)
tinue
d {
fo("✅ [TX FLOW] Queued batch [%d/%d]: %d txs via FFI",
um, totalBatches, len(batchTxs))
eStats.IncrTxsForwarded(int64(len(batchTxs)))
dBatchTimeNano.Store(time.Now().UnixNano())
dBatchTxCount.Store(int64(totalTxs))
tent = content.replace(old_send_logic, new_send_logic)

# 5. Fix TCP Path (Single node / Readonly fallback)
old_tcp = """odes forward to Master when UDS unavailable, OR Single node)
OTE: ServiceTypeWrite now uses UDS path above. Only fall back to TCP if txSender is nil.
g(p_common.ServiceTypeWrite) && bf.txSender == nil && !usingTCPFallback) || bf.serviceType == string(p_common.ServiceTypeReadonly) || bf.chainState.GetConfig().Mode == p_common.MODE_SINGLE {"""

content = content.replace(old_tcp, """LY Readonly Nodes or Single mode
g(p_common.ServiceTypeReadonly) || bf.chainState.GetConfig().Mode == p_common.MODE_SINGLE {""")

with open(path, "w") as f:
    f.write(content)

