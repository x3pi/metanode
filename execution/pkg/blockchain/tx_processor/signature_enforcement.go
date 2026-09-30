package tx_processor

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	eth_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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

// checkTxSignature is a pure function of (tx, sender account state): every node evaluating the same
// tx against the same pre-block state gets the same verdict, so it is safe to use as a consensus-level
// execution filter (no clocks, no peer-local data other than the cache, which only memoizes this result).
// It mirrors the signature rules of VerifyTransaction (BLS key if registered, ETH secp256k1 otherwise or
// as fallback; AccountType 1 additionally requires the ETH signature).
func checkTxSignature(tx types.Transaction, as types.AccountState) bool {
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
	if len(blsKey) > 0 {
		ok = transaction.NewVerifyTransactionRequest(tx.Hash(), common.PubkeyFromBytes(blsKey), tx.Sign()).Valid()
	}
	if !ok {
		ok = tx.ValidEthSign()
	}
	if ok && accountType == 1 && tx.ToAddress() != utils.GetAddressSelector(common.ACCOUNT_SETTING_ADDRESS_SELECT) && !tx.ValidEthSign() {
		ok = false
	}
	if ok {
		StoreVerifiedSignature(key)
	}
	return ok
}

// FilterInvalidSignatures drops every tx whose signature does not verify against the sender's
// pre-block account state. Committed blocks used to be executed WITHOUT re-checking signatures (the
// mempool of the proposing node was the only gate), so a single Byzantine proposer could include
// forged txs that every honest node then executed. Dropping (not failing) mirrors what the native
// fast path already does for a rejected tx: no receipt, no state change, no nonce bump — identical on
// every node. Group order and per-group item order are preserved; emptied groups are removed.
func FilterInvalidSignatures(chainState *blockchain.ChainState, groups []grouptxns.RelativeGroup) []grouptxns.RelativeGroup {
	if sigVerifyBypassedForDevnet() {
		return groups
	}
	total := 0
	for _, g := range groups {
		total += len(g.Items)
	}
	if total == 0 {
		return groups
	}

	type ref struct{ g, i int }
	refs := make([]ref, 0, total)
	for gi, g := range groups {
		for ii := range g.Items {
			refs = append(refs, ref{gi, ii})
		}
	}
	valid := make([]bool, total)
	accountDB := chainState.GetAccountStateDB()

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

	// Phase 1: cache hits are accepted; plain BLS txs (registered key, AccountType 0, not yet cached) are
	// queued for batch verification; everything else (ETH-signed, no key yet, AccountType 1, ...) takes
	// the exact per-tx path.
	type pending struct {
		k   int
		key eth_common.Hash
		pub []byte
	}
	queued := make([]*pending, total)
	parallel(total, 64, func(k int) {
		tx := groups[refs[k].g].Items[refs[k].i].Tx
		as, err := accountDB.AccountStateReadOnly(tx.FromAddress())
		if err != nil {
			as = nil
		}
		if as != nil && len(as.PublicKeyBls()) > 0 && as.AccountType() == 0 {
			key := sigCacheKey(tx, as.PublicKeyBls())
			if LoadVerifiedSignature(key) {
				valid[k] = true
			} else {
				queued[k] = &pending{k: k, key: key, pub: as.PublicKeyBls()}
			}
			return
		}
		valid[k] = checkTxSignature(tx, as)
	})

	// Phase 2: batch-verify the queued BLS txs in chunks (one shared final exponentiation, random linear
	// combination => a bad/cancelling signature cannot hide). A chunk that fails falls back to the exact
	// per-tx check, so every tx's verdict is identical to non-batched verification (deterministic).
	toBatch := make([]*pending, 0, total)
	for _, p := range queued {
		if p != nil {
			toBatch = append(toBatch, p)
		}
	}
	const batchChunk = 128
	nChunks := (len(toBatch) + batchChunk - 1) / batchChunk
	parallel(nChunks, 2, func(c int) {
		lo := c * batchChunk
		hi := lo + batchChunk
		if hi > len(toBatch) {
			hi = len(toBatch)
		}
		part := toBatch[lo:hi]
		pubs := make([][]byte, len(part))
		sigs := make([][]byte, len(part))
		msgs := make([][]byte, len(part))
		for i, p := range part {
			tx := groups[refs[p.k].g].Items[refs[p.k].i].Tx
			h := tx.Hash()
			pubs[i], sigs[i], msgs[i] = p.pub, tx.Sign().Bytes(), h.Bytes()
		}
		if bls.VerifyBatch(pubs, sigs, msgs) {
			for _, p := range part {
				valid[p.k] = true
				StoreVerifiedSignature(p.key)
			}
			return
		}
		for _, p := range part {
			tx := groups[refs[p.k].g].Items[refs[p.k].i].Tx
			as, err := accountDB.AccountStateReadOnly(tx.FromAddress())
			if err != nil {
				as = nil
			}
			valid[p.k] = checkTxSignature(tx, as)
		}
	})

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
