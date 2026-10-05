package tx_processor

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	eth_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// sigCacheKey binds a verified-signature cache entry to (tx hash, signature bytes, BLS key it was
// verified against). Keying by tx hash alone would let a tx copy carrying a forged signature ride on
// the cache entry of the genuine one.
func sigCacheKey(tx types.Transaction, blsKey []byte) eth_common.Hash {
	buf := make([]byte, 0, 32+len(tx.Sign().Bytes())+len(blsKey))
	h := tx.Hash()
	buf = append(buf, h[:]...)
	buf = append(buf, tx.Sign().Bytes()...)
	buf = append(buf, blsKey...)
	return crypto.Keccak256Hash(buf)
}

// sigPolicy is the per-chain signature rule set, derived from the node config (identical on every validator of a
// chain). It keeps every signature verdict a pure function of (tx, sender state, policy).
type sigPolicy struct {
	chainID uint64 // configured chain ID (0 = unset)
	secp    bool   // tx_signature_mode == "secp": users sign only with secp256k1
}

func sigPolicyOf(cs *blockchain.ChainState) sigPolicy {
	if cs == nil || cs.GetConfig() == nil {
		return sigPolicy{}
	}
	cfg := cs.GetConfig()
	p := sigPolicy{secp: cfg.SecpOnlyTxSignatures()}
	if cfg.ChainId != nil {
		p.chainID = cfg.ChainId.Uint64()
	}
	return p
}

// isNodeBLSIdentity reports whether the sender is a BLS-native identity: an account whose address IS the address
// derived from its own registered BLS public key (what a node/cluster key pair produces). Such accounts have no
// secp key, so in secp mode they are the only senders still allowed to carry a BLS tx signature.
func isNodeBLSIdentity(tx types.Transaction, as types.AccountState) bool {
	if as == nil {
		return false
	}
	pub := as.PublicKeyBls()
	return len(pub) > 0 && tx.FromAddress() == bls.GetAddressFromPublicKey(pub)
}

// blsAllowed: whether a BLS signature may authenticate this tx. Always in legacy mode; only for node identities in
// secp mode (user txs there must be secp-signed).
func (p sigPolicy) blsAllowed(tx types.Transaction, as types.AccountState) bool {
	return !p.secp || isNodeBLSIdentity(tx, as)
}

// secpProtoError returns nil when a Type 0xFF tx is admissible under the policy, else the rejection reason.
// Type 0xFF exists only in secp mode and only for the configured chain (anti cross-chain replay; an unset chain ID
// fails closed). Every other tx type returns nil.
func (p sigPolicy) secpProtoError(tx types.Transaction) *transaction.TransactionError {
	if tx.Type() != 0xFF {
		return p.chainBindingError(tx)
	}
	if !p.secp {
		return transaction.InvalidSign
	}
	if p.chainID == 0 || tx.GetChainID() != p.chainID {
		return transaction.InvalidChainId
	}
	return nil
}

// chainBindingError makes the consensus-level filter enforce what mempool admission (VerifyTransaction's
// ValidChainID) already enforces for every tx type: in secp mode a tx must carry THIS chain's ID. Without it a tx
// signed for another chain (valid under ValidEthSign, which recovers the sender with the tx's own chain ID) could be
// included by a Byzantine proposer, or admitted through a verification-skipping path, and be executed by every
// honest node. Legacy chains keep their existing verdicts (no change to history); secp chains are new.
func (p sigPolicy) chainBindingError(tx types.Transaction) *transaction.TransactionError {
	if p.secp && (p.chainID == 0 || tx.GetChainID() != p.chainID) {
		return transaction.InvalidChainId
	}
	return nil
}

// checkTxSignature is a pure function of (tx, sender account state): every node evaluating the same
// tx against the same pre-block state gets the same verdict, so it is safe to use as a consensus-level
// execution filter (no clocks, no peer-local data other than the cache, which only memoizes this result).
// It mirrors the signature rules of VerifyTransaction (BLS key if registered, ETH secp256k1 otherwise or
// as fallback; AccountType 1 additionally requires the ETH signature).
func checkTxSignature(tx types.Transaction, as types.AccountState, pol sigPolicy) bool {
	if pol.secpProtoError(tx) != nil {
		return false
	}
	var blsKey []byte
	accountType := int32(0)
	if as != nil {
		blsKey = as.PublicKeyBls()
		accountType = int32(as.AccountType())
	}
	key := sigCacheKey(tx, blsKey)
	if LoadVerifiedSignature(key) {
		return true
	}

	ok := false
	if tx.Type() != 0xFF && len(blsKey) > 0 && pol.blsAllowed(tx, as) {
		ok = transaction.NewVerifyTransactionRequest(tx.Hash(), common.PubkeyFromBytes(blsKey), tx.Sign()).Valid()
	}
	if !ok {
		ok = tx.ValidSecpSign()
	}
	if ok && accountType == 1 && tx.ToAddress() != utils.GetAddressSelector(common.ACCOUNT_SETTING_ADDRESS_SELECT) && !tx.ValidSecpSign() {
		ok = false
	}
	if ok {
		StoreVerifiedSignature(key)
	}
	return ok
}

// sigStats reports how a verification pass was served (for throughput investigations).
type sigStats struct{ cacheHits, batched, individual int64 }

// batchChunk is the size of one random-linear-combination batch. On a failing chunk we BISECT instead of
// re-verifying every member one by one, so a single bad signature costs ~O(log n) batch calls rather than n
// individual verifications (bounds the amplification an attacker gets from sending bad signatures).
const (
	batchChunk    = 128
	bisectMinSize = 8 // at or below this, verify individually (exact ETH-fallback semantics included)
)

// verifySignatures computes, for every tx, the exact verdict checkTxSignature would give, but verifies plain
// BLS txs (registered key, AccountType 0) with blst batch verification, in parallel. stateOf(i) returns the
// sender's account state for txs[i] (nil = load from accountDB). Successful checks populate the
// verified-signature cache. The result depends only on (tx, sender state): batching never changes a verdict.
func verifySignatures(accountDB *account_state_db.AccountStateDB, txs []types.Transaction, stateOf func(i int) types.AccountState, pol sigPolicy) ([]bool, sigStats) {
	total := len(txs)
	valid := make([]bool, total)
	var st sigStats
	if total == 0 {
		return valid, st
	}

	workers := runtime.GOMAXPROCS(0)
	if workers > 128 {
		workers = 128
	}
	// parallel runs fn(k) for k in [0,n) with dynamic scheduling (atomic work counter) on up to `workers`
	// goroutines (inline for small n), so uneven items (batch chunks) still balance across cores.
	parallel := func(n int, minParallel int, fn func(k int)) {
		if n < minParallel || workers < 2 {
			for k := 0; k < n; k++ {
				fn(k)
			}
			return
		}
		var next int64 = -1
		var wg sync.WaitGroup
		w := workers
		if w > n {
			w = n
		}
		for i := 0; i < w; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					k := int(atomic.AddInt64(&next, 1))
					if k >= n {
						return
					}
					fn(k)
				}
			}()
		}
		wg.Wait()
	}

	loadState := func(i int) types.AccountState {
		if stateOf != nil {
			if as := stateOf(i); as != nil {
				return as
			}
		}
		as, err := accountDB.AccountStateReadOnly(txs[i].FromAddress())
		if err != nil {
			return nil
		}
		return as
	}

	// Phase 1: cache hits are accepted; plain BLS txs not yet cached are queued for batch verification;
	// everything else (ETH-signed, no key yet, AccountType 1, ...) takes the exact per-tx path.
	type pending struct {
		i   int
		key eth_common.Hash
		pub []byte
	}
	queued := make([]*pending, total)
	parallel(total, 64, func(i int) {
		as := loadState(i)
		if as != nil && len(as.PublicKeyBls()) > 0 && as.AccountType() == 0 && txs[i].Type() != 0xFF && pol.blsAllowed(txs[i], as) {
			key := sigCacheKey(txs[i], as.PublicKeyBls())
			if LoadVerifiedSignature(key) {
				valid[i] = true
				atomic.AddInt64(&st.cacheHits, 1)
			} else {
				queued[i] = &pending{i: i, key: key, pub: as.PublicKeyBls()}
			}
			return
		}
		valid[i] = checkTxSignature(txs[i], as, pol)
		atomic.AddInt64(&st.individual, 1)
	})

	toBatch := make([]*pending, 0, total)
	for _, p := range queued {
		if p != nil {
			toBatch = append(toBatch, p)
		}
	}

	// Phase 2: batch-verify in chunks; bisect failing chunks.
	var verifyPart func(part []*pending)
	verifyPart = func(part []*pending) {
		if len(part) == 0 {
			return
		}
		if len(part) > bisectMinSize {
			pubs := make([][]byte, len(part))
			sigs := make([][]byte, len(part))
			msgs := make([][]byte, len(part))
			for j, p := range part {
				h := txs[p.i].Hash()
				pubs[j], sigs[j], msgs[j] = p.pub, txs[p.i].Sign().Bytes(), h.Bytes()
			}
			if bls.VerifyBatch(pubs, sigs, msgs) {
				atomic.AddInt64(&st.batched, int64(len(part)))
				for _, p := range part {
					valid[p.i] = true
					StoreVerifiedSignature(p.key)
				}
				return
			}
			mid := len(part) / 2
			verifyPart(part[:mid])
			verifyPart(part[mid:])
			return
		}
		for _, p := range part {
			valid[p.i] = checkTxSignature(txs[p.i], loadState(p.i), pol)
			atomic.AddInt64(&st.individual, 1)
		}
	}
	nChunks := (len(toBatch) + batchChunk - 1) / batchChunk
	parallel(nChunks, 2, func(c int) {
		lo := c * batchChunk
		hi := lo + batchChunk
		if hi > len(toBatch) {
			hi = len(toBatch)
		}
		verifyPart(toBatch[lo:hi])
	})
	return valid, st
}

// PrewarmSignatureCache batch-verifies txs' signatures and fills the verified-signature cache so the
// per-tx VerifyTransaction calls that follow hit the cache. It never rejects anything itself (a tx that
// fails here simply misses the cache and is rejected by VerifyTransaction as before). senderStates may be
// nil / incomplete.
func PrewarmSignatureCache(chainState *blockchain.ChainState, txs []types.Transaction, senderStates map[eth_common.Address]types.AccountState) {
	if len(txs) == 0 || sigVerifyBypassedForDevnet() {
		return
	}
	flat := make([]types.Transaction, 0, len(txs))
	for _, tx := range txs {
		if tx != nil {
			flat = append(flat, tx)
		}
	}
	verifySignatures(chainState.GetAccountStateDB(), flat, func(i int) types.AccountState {
		if senderStates == nil {
			return nil
		}
		return senderStates[flat[i].FromAddress()]
	}, sigPolicyOf(chainState))
}

// FilterInvalidSignatures drops every tx whose signature does not verify against the sender's
// pre-block account state. Committed blocks used to be executed WITHOUT re-checking signatures (the
// mempool of the proposing node was the only gate), so a single Byzantine proposer could include
// forged txs that every honest node then executed. Dropping (not failing) mirrors what the native
// fast path already does for a rejected tx: no receipt, no state change, no nonce bump — identical on
// every node. Group order and per-group item order are preserved; emptied groups are removed.
func FilterInvalidSignatures(chainState *blockchain.ChainState, groups []grouptxns.RelativeGroup) []grouptxns.RelativeGroup {
	if sigVerifyBypassedForDevnet() || execFilterDisabledForDevnetBenchmark() {
		return groups
	}
	total := 0
	for _, g := range groups {
		total += len(g.Items)
	}
	if total == 0 {
		return groups
	}
	startFilter := time.Now()
	flat := make([]types.Transaction, 0, total)
	for _, g := range groups {
		for _, item := range g.Items {
			flat = append(flat, item.Tx)
		}
	}
	valid, st := verifySignatures(chainState.GetAccountStateDB(), flat, nil, sigPolicyOf(chainState))

	dropped := 0
	out := make([]grouptxns.RelativeGroup, 0, len(groups))
	k := 0
	for _, g := range groups {
		kept := make([]grouptxns.Item, 0, len(g.Items))
		for _, item := range g.Items {
			if valid[k] {
				kept = append(kept, item)
			} else {
				dropped++
				logger.Warn("❌ [SIG-ENFORCE] dropping tx %s (From: %s): invalid signature in committed block", item.Tx.Hash().Hex(), item.Tx.FromAddress().Hex())
			}
			k++
		}
		if len(kept) == 0 {
			continue
		}
		ng := g
		ng.Items = kept
		out = append(out, ng)
	}
	// Cost visibility for throughput investigations: this runs on the block critical path of EVERY validator.
	if elapsed := time.Since(startFilter); elapsed > 20*time.Millisecond || dropped > 0 {
		logger.Info("🔏 [SIG-ENFORCE] %d txs in %v (cache_hit=%d batch_verified=%d individual=%d dropped=%d)",
			total, elapsed, st.cacheHits, st.batched, st.individual, dropped)
	}
	if dropped > 0 {
		logger.Warn("❌ [SIG-ENFORCE] dropped %d/%d txs with invalid signatures", dropped, total)
	}
	return out
}

// sigVerifyBypassedForDevnet reports the explicit devnet-benchmark bypass (same guard as VerifyTransaction:
// SKIP_MEMPOOL_SIG_VERIFY=true only honoured with METANODE_DEVNET=true and never in production).
func sigVerifyBypassedForDevnet() bool {
	if os.Getenv("SKIP_MEMPOOL_SIG_VERIFY") != "true" {
		return false
	}
	isProd := os.Getenv("NODE_ENV") == "production" ||
		os.Getenv("ENVIRONMENT") == "production" ||
		os.Getenv("METANODE_ENV") == "production"
	return os.Getenv("METANODE_DEVNET") == "true" && !isProd
}

// execFilterDisabledForDevnetBenchmark lets a devnet operator A/B the throughput cost of THIS filter alone
// (mempool verification stays on): METANODE_DEVNET_SKIP_EXEC_SIG_FILTER=true, honoured only with
// METANODE_DEVNET=true and never in production. Not for real networks: it re-opens the Byzantine-proposer
// forged-tx hole the filter closes.
func execFilterDisabledForDevnetBenchmark() bool {
	if os.Getenv("METANODE_DEVNET_SKIP_EXEC_SIG_FILTER") != "true" {
		return false
	}
	isProd := os.Getenv("NODE_ENV") == "production" ||
		os.Getenv("ENVIRONMENT") == "production" ||
		os.Getenv("METANODE_ENV") == "production"
	return os.Getenv("METANODE_DEVNET") == "true" && !isProd
}
