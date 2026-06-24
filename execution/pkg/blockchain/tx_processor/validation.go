package tx_processor

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	eth_common "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

type shardedSignatureCache struct {
	shards [256]sync.Map
}

func (s *shardedSignatureCache) getShard(key interface{}) *sync.Map {
	var hash byte
	switch k := key.(type) {
	case string:
		if len(k) >= 4 {
			hash = k[0] ^ k[1] ^ k[2] ^ k[3]
		} else if len(k) >= 2 {
			hash = k[0] ^ k[1]
		} else if len(k) == 1 {
			hash = k[0]
		}
	case int64:
		hash = byte(k)
	case int:
		hash = byte(k)
	case eth_common.Hash:
		hash = k[0]
	default:
		// Fallback hash
	}
	return &s.shards[hash]
}

func (s *shardedSignatureCache) Load(key interface{}) (value interface{}, ok bool) {
	return s.getShard(key).Load(key)
}

func (s *shardedSignatureCache) Store(key, value interface{}) {
	s.getShard(key).Store(key, value)
}

func (s *shardedSignatureCache) Clear() {
	for i := range s.shards {
		s.shards[i].Clear()
	}
}

type rotatingSignatureCache struct {
	active *shardedSignatureCache
	old    *shardedSignatureCache
}

var (
	currentRotatingCache         atomic.Pointer[rotatingSignatureCache]
	verifiedSignaturesCacheCount int64 // atomic counter for cache size
)

func init() {
	currentRotatingCache.Store(&rotatingSignatureCache{
		active: new(shardedSignatureCache),
		old:    new(shardedSignatureCache),
	})
}

func LoadVerifiedSignature(key interface{}) bool {
	rc := currentRotatingCache.Load()
	if rc == nil {
		return false
	}
	if _, ok := rc.active.Load(key); ok {
		return true
	}
	if rc.old != nil {
		if _, ok := rc.old.Load(key); ok {
			return true
		}
	}
	return false
}

func StoreVerifiedSignature(key interface{}) {
	rc := currentRotatingCache.Load()
	if rc != nil {
		rc.active.Store(key, true)
	}
}

func rotateVerifiedSignatures() {
	rc := currentRotatingCache.Load()
	if rc != nil {
		newRc := &rotatingSignatureCache{
			active: new(shardedSignatureCache),
			old:    rc.active,
		}
		currentRotatingCache.Store(newRc)
	}
}

const (
	// Maximum cache entries before forced reset (safety valve)
	maxVerifiedSignaturesCacheSize = 500_000
	// Periodic cleanup interval
	signatureCacheCleanupInterval = 10 * time.Minute
)

// StartSignatureCacheCleanup starts a background goroutine that periodically
// resets the verifiedSignaturesCache to prevent unbounded memory growth.
// The cache is a performance optimization only — resetting it just means
// signatures will be re-verified (which is safe and correct).
func StartSignatureCacheCleanup(stopCh <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(signatureCacheCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				count := atomic.LoadInt64(&verifiedSignaturesCacheCount)
				if count > 0 {
					rotateVerifiedSignatures()
					atomic.StoreInt64(&verifiedSignaturesCacheCount, 0)
					logger.Info("🧹 [MEMORY] Periodic rotation of verifiedSignaturesCache (%d entries)", count)
				}
			case <-stopCh:
				return
			}
		}
	}()
}

func callDataToAccountType(callData []byte) (pb.ACCOUNT_TYPE, *transaction.TransactionError) {

	if len(callData) != 36 {
		return pb.ACCOUNT_TYPE_REGULAR_ACCOUNT, transaction.InvalidData
	}
	bytesFSelect := utils.GetFunctionSelector("setAccountType(int256)")
	// Kiểm tra 4 byte đầu tiên phải bằng "0x61e1270b"
	if !bytes.Equal(callData[:4], bytesFSelect) {
		return pb.ACCOUNT_TYPE_REGULAR_ACCOUNT, transaction.InvalidData
	}
	// Lấy 4 byte sau để xác định kiểu tài khoản
	num := int32(binary.BigEndian.Uint32(callData[len(callData)-4:]))

	switch num {
	case 0:
		return pb.ACCOUNT_TYPE_REGULAR_ACCOUNT, nil
	case 1:
		return pb.ACCOUNT_TYPE_READ_WRITE_STRICT, nil
	default:
		return pb.ACCOUNT_TYPE_REGULAR_ACCOUNT, transaction.InvalidData
	}
}

func VerifyTransaction(
	tx types.Transaction,
	chainState *blockchain.ChainState,
	preloadedState types.AccountState, // nil = auto-fetch via AccountStateReadOnly
) *transaction.TransactionError {
	isCrossChainBatchSubmit := false
	if tx.ToAddress() == common.CROSS_CHAIN_CONTRACT_ADDRESS {
		ccHandler, errHandler := cross_chain_handler.GetCrossChainHandler()
		if errHandler == nil && ccHandler != nil {
			isCrossChainBatchSubmit = ccHandler.IsBatchSubmitTx(tx.CallData().Input())
		}
	}

	var as types.AccountState
	if preloadedState != nil {
		// PERFORMANCE: Use pre-loaded state from batch caller (avoids sync.Map lookup)
		as = preloadedState
	} else {
		// Standard path: fetch from AccountStateDB
		var err error
		as, err = chainState.GetAccountStateDB().AccountStateReadOnly(tx.FromAddress())
		if err != nil {
			// Retry once after yielding — transient trie contention under high TPS
			runtime.Gosched()
			as, err = chainState.GetAccountStateDB().AccountStateReadOnly(tx.FromAddress())
		}
		if err != nil {
			accountSettingAddr := utils.GetAddressSelector(common.ACCOUNT_SETTING_ADDRESS_SELECT)
			if tx.GetNonce() == 0 && tx.ToAddress() == accountSettingAddr {
				as = state.NewAccountState(tx.FromAddress())
			} else {
				logger.Error("❌ [VERIFY] AccountStateReadOnly failed for %s after retry: %v (txHash=%s, nonce=%d)",
					tx.FromAddress().Hex(), err, tx.Hash().Hex(), tx.GetNonce())
				return transaction.InvalidData
			}
		}
	}
	if tx.GetNonce() < as.Nonce() {
		if !NomtAheadReplayMode.Load() {
			logger.Warn("tx.GetNonce() < as.Nonce(): ", tx.GetNonce(), as.Nonce())
			return transaction.InvalidNonce
		} else {
			logger.Warn("🛡️ [NOMT-AHEAD-REPLAY-VALIDATION] Bypassing nonce < state nonce check: tx=%s (From=%s, tx.Nonce=%d, state.Nonce=%d)",
				tx.Hash().Hex()[:16]+"...", tx.FromAddress().Hex(), tx.GetNonce(), as.Nonce())
		}
	}

	// ════════════════════════════════════════════════════════════════
	// SUB-NODE SYNC OPTIMIZATION (Fix Code 66 Invalid Sign)
	// Sub nodes may have incomplete account state: the AccountBatch
	// replication might replicate nonce but NOT PublicKeyBls.
	// Two scenarios where we must bypass strict BLS verification:
	//   1. Sub node has NO state at all: as.Nonce()==0, tx.GetNonce()>0
	//   2. Sub node has nonce but missing BLS key: as.Nonce()>0, PublicKeyBls empty
	// In both cases, forward the TX to Master/Rust for proper verification.
	// On Master nodes, PublicKeyBls is always populated for registered accounts,
	// so this bypass never fires on Master (which is correct).
	// ════════════════════════════════════════════════════════════════
	isSubNodeLagging := len(as.PublicKeyBls()) == 0 && (tx.GetNonce() > 0 || as.Nonce() > 0)

	if as.Nonce() != 0 || tx.ToAddress() != utils.GetAddressSelector(common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		txHash := tx.Hash()

		if isSubNodeLagging {
			// logger.Warn("⚠️ [BLS-LAG-DEBUG] account=%s | as.Nonce=%d | tx.Nonce=%d | blsKeyLen=%d | stateIsPreloaded=%v | tx=%s",
			// 	tx.FromAddress().String(),
			// 	as.Nonce(),
			// 	tx.GetNonce(),
			// 	len(as.PublicKeyBls()),
			// 	preloadedState != nil,
			// 	tx.Hash().Hex()[:16]+"...",
			// )
			// Let it pass local verification; assume Master will reject if invalid.
		} else {
			if !isCrossChainBatchSubmit {
				if !LoadVerifiedSignature(txHash) {
					request := transaction.NewVerifyTransactionRequest(
						tx.Hash(),
						common.PubkeyFromBytes(as.PublicKeyBls()),
						tx.Sign(),
					)
					if !request.Valid() {
						logger.Error("BLS Verification Failed!")
						logger.Error("  txHashHex: %s", txHash.Hex())
						logger.Error("  FromAddress: %s", tx.FromAddress().Hex())
						logger.Error("  ToAddress: %s", tx.ToAddress().Hex())
						logger.Error("  SenderPubKey: %x", as.PublicKeyBls())
						logger.Error("  SenderSign: %x", tx.Sign().Bytes())
						logger.Error("  Hash() of TX according to SubNode: %x", tx.Hash().Bytes())
						if !tx.ValidEthSign() {
							logger.Error("  ETH Verification also failed!")
							return transaction.InvalidSign
						}
					}
					// Only cache on successful validation
					StoreVerifiedSignature(txHash)
					count := atomic.AddInt64(&verifiedSignaturesCacheCount, 1)
					if count == maxVerifiedSignaturesCacheSize {
						rotateVerifiedSignatures()
						atomic.StoreInt64(&verifiedSignaturesCacheCount, 0)
					}
				}
			}
		}
	}

	if as.AccountType() == 1 && tx.ToAddress() != utils.GetAddressSelector(common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		if !tx.ValidEthSign() {
			return transaction.RequiresTwoSignatures
		}
	}

	if tx.ToAddress() == utils.GetAddressSelector(common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		dataInput := tx.CallData().Input()

		if len(dataInput) < 4 {
			return transaction.InvalidData
		}

		selector := dataInput[:4]
		isSetBls := bytes.Equal(selector, utils.GetFunctionSelector("setBlsPublicKey(bytes)"))
		isSetType := bytes.Equal(selector, utils.GetFunctionSelector("setAccountType(uint8)"))

		switch {
		case as.Nonce() == 0 && isSetBls:
			txHash := tx.Hash()
			if !LoadVerifiedSignature(txHash) {
				if !tx.ValidEthSign() {
					return transaction.InvalidSignSecp
				}
				StoreVerifiedSignature(txHash)
				count := atomic.AddInt64(&verifiedSignaturesCacheCount, 1)
				if count == maxVerifiedSignaturesCacheSize {
					rotateVerifiedSignatures()
					atomic.StoreInt64(&verifiedSignaturesCacheCount, 0)
				}
			}
			_, err := UnpackSetBlsPublicKeyInput(dataInput)
			if err != nil {
				return transaction.InvalidData
			}
			if len(as.PublicKeyBls()) != 0 {
				return transaction.PublicKeyExists
			}
		case as.Nonce() != 0 && isSetType:
			_, err := UnpackSetAccountTypeInput(dataInput)
			if err != nil {
				return transaction.InvalidData
			}
		default:
			return transaction.InvalidData
		}
	} else {
		if as.Nonce() == 0 && !isCrossChainBatchSubmit && !isSubNodeLagging {
			return transaction.InvalidAddressMatchForTx0
		}
		if !tx.ValidDeployData() {
			return transaction.InvalidDeployData
		}
		if !tx.ValidCallData() {
			return transaction.InvalidCallData
		}

		if tx.IsCallContract() {
			toAccount, err := chainState.GetAccountStateDB().AccountStateReadOnly(tx.ToAddress())
			if err != nil || toAccount == nil || toAccount.SmartContractState() == nil {
				logger.Warn("❌ [VERIFY] Invalid call to non-existent smart contract: %s (txHash=%s)", tx.ToAddress().Hex(), tx.Hash().Hex())
				return transaction.InvalidCallSmartContractToAccount
			}
		}
	}

	// Thêm kiểm tra kích thước Call Data
	const maxDataSize = 6 * 1024 * 1024
	if len(tx.Data()) > maxDataSize {
		logger.Error("Transaction data size exceeds limit", "hash", tx.Hash().Hex(), "size", len(tx.Data()), "limit", maxDataSize)
		return transaction.InvalidData // Sử dụng lỗi InvalidData hoặc tạo lỗi mới nếu cần
	}

	if tx.ToAddress() == tx.FromAddress() && tx.GetNonce() != 0 {
		_, erR := callDataToAccountType(tx.Data())
		if erR != nil {
			return erR
		}
	}

	if !tx.ValidChainID(chainState.GetConfig().ChainId.Uint64()) {
		return transaction.InvalidChainId
	}

	// validTx0, errCode := tx.ValidTx0(as, chainState.GetConfig().ChainId.String())

	// if !validTx0 {
	// 	return transaction.CodeToError[errCode]
	// }

	// verify pending use
	if !tx.ValidPendingUse(as) {
		return transaction.InvalidPendingUse
	}

	// nếu mà là giao dịch chuyển native token
	// thì maxGas sẽ là định phí
	// chỉ cho phép lơn hơn 10 lần phí cố định
	// ngược lại nếu là smart contract thì phí lơn hơn tối đa là 10.000 lần
	_, isFree := chainState.GetFreeFeeAddress()[tx.ToAddress()]
	if !isFree {
		_, isFree = chainState.GetFreeFeeAddress()[tx.FromAddress()]
	}
	if tx.GetNonce() == 0 || isCrossChainBatchSubmit {
		isFree = true
	}
	if !isSubNodeLagging && !tx.ValidAmount(as) {
		return transaction.InvalidAmount
	}

	if !isFree && !isSubNodeLagging && !tx.ValidMaxFee(as) {
		return transaction.InvalidMaxFee
	}

	// kiểm tra số dư có đủ cho max price
	// maxFee := tx.MaxFee()
	// if !isFree && maxFee.Cmp(big.NewInt(common.MINIMUM_BASE_FEE)) < 0 {
	// 	logger.Error("maxFee", maxFee)
	// 	return transaction.InvalidAmount
	// }

	// if !isFree && !tx.ValidAmountSpend(as, maxFee) {
	// 	logger.Error("Error when execute transaction code 120003: maxFee")
	// 	logger.Error("Error when execute transaction code 120003: detail as", as.Balance(), as.PendingBalance())
	// 	logger.Error("Error when execute transaction code 120003: detail mf", maxFee, tx.Amount())
	// 	return transaction.InvalidMaxGasPrice
	// }

	// if (!isFree && tx.ValidMaxGas() ) {
	// 	return transaction.InvalidMaxGas
	// }

	// verify last hash

	// Debug
	// neu newDeviceKey ma bang voi as.DeviceKey() thi bao loi
	// if tx.NewDeviceKey() == as.DeviceKey() && as.Nonce() != 0 {
	// 	return transaction.InvalidNewDeviceKey
	// }

	// // // verify device key
	// if !tx.ValidDeviceKey(as) {
	// 	return transaction.InvalidLastDeviceKey
	// }

	return nil
}

// PreVerifySignatures verifies BLS signatures of the given transactions in parallel
// and populates the signature verification cache to eliminate sequential execution bottlenecks.
func PreVerifySignatures(txs []types.Transaction, chainState *blockchain.ChainState) {
	if len(txs) == 0 {
		return
	}

	numWorkers := runtime.NumCPU()
	if numWorkers > 128 {
		numWorkers = 128
	}
	if len(txs) < 200 {
		numWorkers = 1 // Sequential is faster for small batches
	}

	accountDB := chainState.GetAccountStateDB()

	verifyFn := func(tx types.Transaction) {
		txHash := tx.Hash()
		if LoadVerifiedSignature(txHash) {
			return
		}

		as, err := accountDB.AccountStateReadOnly(tx.FromAddress())
		if err != nil || as == nil || len(as.PublicKeyBls()) == 0 {
			return
		}

		request := transaction.NewVerifyTransactionRequest(
			txHash,
			common.PubkeyFromBytes(as.PublicKeyBls()),
			tx.Sign(),
		)
		if request.Valid() {
			StoreVerifiedSignature(txHash)
		}
	}

	if numWorkers <= 1 {
		for _, tx := range txs {
			verifyFn(tx)
		}
		return
	}

	var wg sync.WaitGroup
	chunkSize := (len(txs) + numWorkers - 1) / numWorkers
	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		if start >= len(txs) {
			break
		}
		end := start + chunkSize
		if end > len(txs) {
			end = len(txs)
		}

		wg.Add(1)
		go func(slice []types.Transaction) {
			defer wg.Done()
			for _, tx := range slice {
				verifyFn(tx)
			}
		}(txs[start:end])
	}
	wg.Wait()
}
