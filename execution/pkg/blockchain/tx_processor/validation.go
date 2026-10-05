package tx_processor

import (
	"bytes"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"os"

	eth_common "github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
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

func VerifyTransaction(
	tx types.Transaction,
	chainState *blockchain.ChainState,
	preloadedState types.AccountState, // nil = auto-fetch via AccountStateReadOnly
) *transaction.TransactionError {
	if os.Getenv("SKIP_MEMPOOL_SIG_VERIFY") == "true" {
		isExplicitDev := os.Getenv("METANODE_DEVNET") == "true"
		isProd := os.Getenv("NODE_ENV") == "production" ||
			os.Getenv("ENVIRONMENT") == "production" ||
			os.Getenv("METANODE_ENV") == "production"
		if !isExplicitDev || isProd {
			logger.Error("🚨 [SECURITY VIOLATION] SKIP_MEMPOOL_SIG_VERIFY=true invoked without explicit METANODE_DEVNET=true or in production environment! Bypassing is blocked.")
		} else {
			return nil
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
	pol := sigPolicyOf(chainState)

	if as.Nonce() != 0 || tx.ToAddress() != utils.GetAddressSelector(common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		txHash := tx.Hash()

		if tx.Type() == 0xFF {
			if perr := pol.secpProtoError(tx); perr != nil {
				logger.Error("❌ [VERIFY] Type 0xFF tx rejected (%s): txHash=%s chainID=%d", perr.Description, txHash.Hex(), tx.GetChainID())
				return perr
			}
			secpCacheKey := sigCacheKey(tx, nil)
			if !LoadVerifiedSignature(secpCacheKey) {
				if !tx.ValidSecpProtoSign() {
					logger.Error("❌ [VERIFY] Secp256k1 Proto Verification failed: txHash=%s, from=%s", txHash.Hex(), tx.FromAddress().Hex())
					return transaction.InvalidSign
				}
				StoreVerifiedSignature(secpCacheKey)
				count := atomic.AddInt64(&verifiedSignaturesCacheCount, 1)
				if count == maxVerifiedSignaturesCacheSize {
					rotateVerifiedSignatures()
					atomic.StoreInt64(&verifiedSignaturesCacheCount, 0)
				}
			}
		} else if pol.secp && !isNodeBLSIdentity(tx, as) {
			// secp mode: a user tx must carry a valid ETH/secp256k1 signature. No BLS attempt and NO sub-node-lagging
			// bypass: secp-only accounts have no BLS key, so that bypass would skip verification for all of them.
			secpCacheKey := sigCacheKey(tx, nil)
			if !LoadVerifiedSignature(secpCacheKey) {
				if !tx.ValidEthSign() {
					logger.Error("❌ [VERIFY] secp-mode signature verification failed: txHash=%s, from=%s", txHash.Hex(), tx.FromAddress().Hex())
					return transaction.InvalidSign
				}
				StoreVerifiedSignature(secpCacheKey)
				count := atomic.AddInt64(&verifiedSignaturesCacheCount, 1)
				if count == maxVerifiedSignaturesCacheSize {
					rotateVerifiedSignatures()
					atomic.StoreInt64(&verifiedSignaturesCacheCount, 0)
				}
			}
		} else if isSubNodeLagging {
			// Let it pass local verification; assume Master will reject if invalid.
		} else {
			blsCacheKey := sigCacheKey(tx, as.PublicKeyBls())
			if !LoadVerifiedSignature(blsCacheKey) {
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
				StoreVerifiedSignature(blsCacheKey)
				count := atomic.AddInt64(&verifiedSignaturesCacheCount, 1)
				if count == maxVerifiedSignaturesCacheSize {
					rotateVerifiedSignatures()
					atomic.StoreInt64(&verifiedSignaturesCacheCount, 0)
				}
			}
		}
	}

	if as.AccountType() == 1 && tx.ToAddress() != utils.GetAddressSelector(common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		if !tx.ValidSecpSign() {
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
			setBlsCacheKey := sigCacheKey(tx, nil)
			if !LoadVerifiedSignature(setBlsCacheKey) {
				if !tx.ValidSecpSign() {
					return transaction.InvalidSignSecp
				}
				StoreVerifiedSignature(setBlsCacheKey)
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
		// Legacy rule: an account's first tx must bind its BLS key (setBlsPublicKey) before any ordinary tx. In secp
		// mode users never have a BLS key, so the rule would lock every fresh secp-only account out; node (BLS-native)
		// identities keep it.
		secpUser := pol.secp && !isNodeBLSIdentity(tx, as)
		if as.Nonce() == 0 && !isSubNodeLagging && !secpUser {
			return transaction.InvalidAddressMatchForTx0
		}
		if !tx.ValidDeployData() {
			return transaction.InvalidDeployData
		}
		// RollupSystemAddress system-event txs (app.go's eventProposer, dispatched to
		// RollupSystemHandler) are deliberately NOT ETH CallData-wrapped — RollupSystemHandler
		// reads tx.Data() directly since the payload is a plain JSON-marshaled rollupSystemPayload,
		// not an ABI-encoded call. ValidCallData() unconditionally requires a well-formed
		// CallData envelope for any IsCallContract() tx, so without this exemption every such tx
		// was rejected with "invalid call data" the moment it started actually reaching this
		// validator (previously masked because these txs went to a dead pool that TxValidatorPool
		// never processed at all — see app.go's AddTransactionToPool fix).
		if !tx.ValidCallData() && tx.ToAddress() != rollup.RollupSystemAddress {
			return transaction.InvalidCallData
		}

		// EIP-7702: a SetCode tx's own call target may be an address it is
		// delegating IN THIS SAME TX (the "set delegation, then immediately
		// call through it" pattern sponsored-execution relies on) — at
		// admission time that address is still a plain EOA with no code, so
		// the "must already be a smart contract" check below would wrongly
		// reject it. processAuthorizationList (pkg/blockchain/tx_processor/
		// authorization.go) validates the authorization tuples themselves at
		// execution time; if they turn out invalid the call simply hits a
		// no-code address and no-ops, same as calling any other empty EOA.
		if tx.IsCallContract() && tx.GetType() != uint64(e_types.SetCodeTxType) {
			toAddress := tx.ToAddress()
			// RollupSystemAddress has no deployed SmartContractState (it's a barrier-tx marker
			// address dispatched natively by TrueBlockSTM, exactly like VALIDATOR/GATEWAY/
			// PARENT_CHAIN_GATEWAY below), so it must be exempted the same way.
			if toAddress != common.VALIDATOR_CONTRACT_ADDRESS && toAddress != common.GATEWAY_CONTRACT_ADDRESS && toAddress != common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS && toAddress != rollup.RollupSystemAddress {
				toAccount, err := chainState.GetAccountStateDB().AccountStateReadOnly(toAddress)
				if err != nil || toAccount == nil || toAccount.SmartContractState() == nil {
					logger.Warn("❌ [VERIFY] Invalid call to non-existent smart contract: %s (txHash=%s)", tx.ToAddress().Hex(), tx.Hash().Hex())
					return transaction.InvalidCallSmartContractToAccount
				}
			}
		}
	}

	// Thêm kiểm tra kích thước Call Data
	const maxDataSize = 6 * 1024 * 1024
	if len(tx.Data()) > maxDataSize {
		logger.Error("Transaction data size exceeds limit", "hash", tx.Hash().Hex(), "size", len(tx.Data()), "limit", maxDataSize)
		return transaction.InvalidData // Sử dụng lỗi InvalidData hoặc tạo lỗi mới nếu cần
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

	// 4. KIỂM TRA PHÍ & SỐ DƯ (Bỏ qua nếu SubNode chưa đồng bộ đủ state)
	if !isSubNodeLagging {
		// 4.1. Kiểm tra số dư có đủ trả Amount cơ bản không
		if !tx.ValidAmount(as) {
			return transaction.InvalidAmount
		}

		// 4.2. Xác định giao dịch có được miễn phí không
		_, isFree := chainState.GetFreeFeeAddress()[tx.ToAddress()]
		if !isFree {
			_, isFree = chainState.GetFreeFeeAddress()[tx.FromAddress()]
		}
		if tx.GetNonce() == 0 {
			isFree = true
		}

		if !isFree {
			// 4.3. Kiểm tra số dư có đủ trả (MaxFee + Amount) không
			if !tx.ValidMaxFee(as) {
				return transaction.InvalidMaxFee
			}

			// 4.4. Kiểm tra MaxGasPrice > 0 (chống spam cơ bản)
			if !tx.ValidMaxGasPrice(common.MINIMUM_BASE_FEE) {
				return &transaction.TransactionError{
					Code:        transaction.InvalidMaxGasPrice.Code,
					Description: fmt.Sprintf("invalid max gas price, expected at least %d", common.MINIMUM_BASE_FEE),
				}
			}
		}
	}

	if !tx.ValidMaxGas() {
		return transaction.InvalidMaxGas
	}

	// EIP-7702 (F2/F3): MaxGas must at least cover intrinsic gas (base 21000 + 25000 per auth tuple).
	// params.TxGas (21000), not this chain's native TRANSFER_GAS_COST (20000): a SetCode tx that calls a
	// contract is priced by core.IntrinsicGas (21000 base) in the VM, so admitting less would let it in
	// only to fail at execution with "intrinsic gas too low".
	if len(tx.AuthorizationList()) > 0 {
		requiredGas := params.TxGas + uint64(len(tx.AuthorizationList()))*params.CallNewAccountGas
		if tx.MaxGas() < requiredGas {
			return transaction.InvalidMaxGas
		}
	}

	// verify last hash

	// Verify DeviceKey nếu được bật trong cấu hình
	if chainState.GetConfig().VerifyDeviceKey {
		if tx.NewDeviceKey() == as.DeviceKey() && as.Nonce() != 0 {
			return transaction.InvalidNewDeviceKey
		}

		if as != nil && as.DeviceKey() != (eth_common.Hash{}) && crypto.Keccak256Hash(tx.LastDeviceKey().Bytes()) != as.DeviceKey() {
			return transaction.InvalidLastDeviceKey
		}
	}

	return nil
}

// PreVerifySignatures verifies BLS signatures of the given transactions in parallel
// and populates the signature verification cache to eliminate sequential execution bottlenecks.
func PreVerifySignatures(txs []types.Transaction, chainState *blockchain.ChainState) {
	if len(txs) == 0 {
		return
	}

	// GOMAXPROCS(0), not NumCPU(): see native_fast_path.go for why.
	numWorkers := runtime.GOMAXPROCS(0)
	if numWorkers > 128 {
		numWorkers = 128
	}
	if len(txs) < 200 {
		numWorkers = 1 // Sequential is faster for small batches
	}

	accountDB := chainState.GetAccountStateDB()
	pol := sigPolicyOf(chainState)

	verifyFn := func(tx types.Transaction) {
		as, err := accountDB.AccountStateReadOnly(tx.FromAddress())
		if err != nil {
			as = nil
		}
		// Same pure check the consensus-level filter uses; populates the cache on success.
		checkTxSignature(tx, as, pol)
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
