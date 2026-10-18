package tx_processor

import (
	"bytes"
	"fmt"
	"math/big"
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

type sigCacheShard struct {
	mu sync.RWMutex
	m  map[eth_common.Hash]struct{}
}

// shardedSignatureCache uses a value-typed map[eth_common.Hash]struct{} per shard.
// Because neither [32]byte nor struct{} contains pointers, the Go runtime allocates all map buckets
// in noscan spans, eliminating interface{} boxing and removing signature cache entries from GC mark scanning.
type shardedSignatureCache struct {
	shards [256]sigCacheShard
}

func (s *shardedSignatureCache) Load(key eth_common.Hash) bool {
	sh := &s.shards[key[0]]
	sh.mu.RLock()
	m := sh.m
	if m == nil {
		sh.mu.RUnlock()
		return false
	}
	_, ok := m[key]
	sh.mu.RUnlock()
	return ok
}

func (s *shardedSignatureCache) Store(key eth_common.Hash) {
	sh := &s.shards[key[0]]
	sh.mu.Lock()
	if sh.m == nil {
		sh.m = make(map[eth_common.Hash]struct{}, 64)
	}
	sh.m[key] = struct{}{}
	sh.mu.Unlock()
}

func (s *shardedSignatureCache) Clear() {
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		sh.m = nil
		sh.mu.Unlock()
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

func LoadVerifiedSignature(key eth_common.Hash) bool {
	rc := currentRotatingCache.Load()
	if rc == nil {
		return false
	}
	if rc.active.Load(key) {
		return true
	}
	if rc.old != nil && rc.old.Load(key) {
		return true
	}
	return false
}

func StoreVerifiedSignature(key eth_common.Hash) {
	rc := currentRotatingCache.Load()
	if rc != nil {
		rc.active.Store(key)
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

func bumpVerifiedSignatureCacheCount() {
	count := atomic.AddInt64(&verifiedSignaturesCacheCount, 1)
	if count >= maxVerifiedSignaturesCacheSize {
		if atomic.CompareAndSwapInt64(&verifiedSignaturesCacheCount, count, 0) {
			rotateVerifiedSignatures()
		}
	}
}

const (
	// Maximum cache entries before forced reset (safety valve)
	maxVerifiedSignaturesCacheSize = 2_500_000
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
			os.Getenv("METANODE_ENV") == "production" ||
			(chainState != nil && chainState.GetConfig() != nil && chainState.GetConfig().ChainId != nil && chainState.GetConfig().ChainId.Cmp(big.NewInt(991)) == 0)
		if !isExplicitDev || isProd {
			logger.Error("🚨 [SECURITY VIOLATION] SKIP_MEMPOOL_SIG_VERIFY=true invoked without explicit METANODE_DEVNET=true or on production chain (991)! Bypassing is blocked.")
		} else {
			return nil
		}
	}

	// P0-9: Enforce strict envelope binding at mempool admission
	envelopeBoundOK := false
	if tx != nil && len(tx.RawEnvelope()) > 0 {
		if IsVerifiedEnvelopeBound(tx) {
			envelopeBoundOK = true
		} else {
			if err := transaction.ValidateEnvelopeBinding(tx); err != nil {
				logger.Error("❌ [VERIFY] envelope binding failed: txHash=%s, err=%v", tx.Hash().Hex(), err)
				return transaction.ErrEnvelopeBindingMismatch
			}
			envelopeBoundOK = true
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

	pol := sigPolicyOf(chainState)

	// Rollup system events are applied from their payload alone, so only BLS-native node identities may submit them.
	// System txs are produced by the master node's own workers, which always hold the node identity's state.
	if tx.ToAddress() == rollup.RollupSystemAddress && !isNodeBLSIdentity(tx, as) {
		return transaction.UnauthorizedSystemSender
	}

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
				bumpVerifiedSignatureCacheCount()
			}
		} else if pol.secp && !isNodeBLSIdentity(tx, as) {
			// secp mode: a user tx must carry a valid ETH/secp256k1 signature.
			secpCacheKey := sigCacheKey(tx, nil)
			if !LoadVerifiedSignature(secpCacheKey) {
				if envelopeBoundOK && len(tx.RawEnvelope()) > 0 {
					// ValidateEnvelopeBinding above already fully verified secp signature and envelope fields
				} else if !tx.ValidEthSign() {
					logger.Error("❌ [VERIFY] secp-mode signature verification failed: txHash=%s, from=%s", txHash.Hex(), tx.FromAddress().Hex())
					return transaction.InvalidSign
				}
				StoreVerifiedSignature(secpCacheKey)
				if len(tx.RawEnvelope()) > 0 {
					// ValidEthSign ran the envelope binding too, so the block-time filter may skip it on a hit.
					StoreVerifiedSignature(boundSigKey(secpCacheKey))
				}
				bumpVerifiedSignatureCacheCount()
			}
		} else if len(as.PublicKeyBls()) > 0 {
			blsCacheKey := sigCacheKey(tx, as.PublicKeyBls())
			if !LoadVerifiedSignature(blsCacheKey) {
				request := transaction.NewVerifyTransactionRequest(
					tx.Hash(),
					common.PubkeyFromBytes(as.PublicKeyBls()),
					tx.Sign(),
				)
				if !request.Valid() {
					logger.Error("BLS Verification Failed! txHash=%s from=%s", txHash.Hex(), tx.FromAddress().Hex())
					if envelopeBoundOK && len(tx.RawEnvelope()) > 0 {
						// Valid envelope binding already passed above
					} else if !tx.ValidEthSign() {
						return transaction.InvalidSign
					}
				}
				// Only cache on successful validation
				StoreVerifiedSignature(blsCacheKey)
				bumpVerifiedSignatureCacheCount()
			}
		} else {
			if envelopeBoundOK && len(tx.RawEnvelope()) > 0 {
				// Valid envelope binding already passed above
			} else if !tx.ValidEthSign() {
				return transaction.InvalidSign
			}
		}
	}

	if regErr := pol.senderRegisteredError(tx, as); regErr != nil {
		logger.Warn("❌ [VERIFY] Sender not registered on parent chain: from=%s, txHash=%s", tx.FromAddress().Hex(), tx.Hash().Hex())
		return regErr
	}

	loadRecipientState := func(addr eth_common.Address) types.AccountState {
		if chainState == nil || chainState.GetAccountStateDB() == nil {
			return nil
		}
		recAs, err := chainState.GetAccountStateDB().AccountStateReadOnly(addr)
		if err != nil {
			return nil
		}
		return recAs
	}
	if recErr := pol.recipientRegisteredError(tx, loadRecipientState); recErr != nil {
		logger.Warn("❌ [VERIFY] Recipient/owner rejected by account gate (%s): txHash=%s", recErr.Description, tx.Hash().Hex())
		return recErr
	}

	// Payload data size limit check (DoS prevention)
	const maxDataSize = 6 * 1024 * 1024
	if len(tx.Data()) > maxDataSize {
		logger.Error("Transaction data size exceeds limit", "hash", tx.Hash().Hex(), "size", len(tx.Data()), "limit", maxDataSize)
		return transaction.InvalidData
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
				bumpVerifiedSignatureCacheCount()
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
		if as.Nonce() == 0 && !secpUser {
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

	// 4. KIỂM TRA PHÍ & SỐ DƯ
	// 4.1. Gas limits first: cheap uint64 comparisons reject malformed txs before any big.Int balance math.
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

	// 4.2. Kiểm tra số dư có đủ trả Amount cơ bản không
	if !tx.ValidAmount(as) {
		return transaction.InvalidAmount
	}

	// 4.3. Xác định giao dịch có được miễn phí không
	_, isFree := chainState.GetFreeFeeAddress()[tx.ToAddress()]
	if !isFree {
		_, isFree = chainState.GetFreeFeeAddress()[tx.FromAddress()]
	}
	// Only the BLS-key registration is free at nonce 0 (see isFreeRegistrationTx); every other nonce-0 tx pays.
	if isFreeRegistrationTx(tx) {
		isFree = true
	}

	if !isFree {
		// 4.4. Kiểm tra số dư có đủ trả (MaxFee + Amount) không
		if !tx.ValidMaxFee(as) {
			return transaction.InvalidMaxFee
		}

		// 4.5. Kiểm tra MaxGasPrice > 0 (chống spam cơ bản)
		if !tx.ValidMaxGasPrice(common.MINIMUM_BASE_FEE) {
			return &transaction.TransactionError{
				Code:        transaction.InvalidMaxGasPrice.Code,
				Description: fmt.Sprintf("invalid max gas price, expected at least %d", common.MINIMUM_BASE_FEE),
			}
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
