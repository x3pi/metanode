package tx_processor

import (
	"os"
	"runtime"
	"sync"

	eth_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
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

	verify := func(k int) {
		tx := groups[refs[k].g].Items[refs[k].i].Tx
		as, err := accountDB.AccountStateReadOnly(tx.FromAddress())
		if err != nil {
			as = nil
		}
		valid[k] = checkTxSignature(tx, as)
	}

	workers := runtime.GOMAXPROCS(0)
	if workers > 128 {
		workers = 128
	}
	if total < 200 || workers < 2 {
		for k := range refs {
			verify(k)
		}
	} else {
		var wg sync.WaitGroup
		chunk := (total + workers - 1) / workers
		for start := 0; start < total; start += chunk {
			end := start + chunk
			if end > total {
				end = total
			}
			wg.Add(1)
			go func(s, e int) {
				defer wg.Done()
				for k := s; k < e; k++ {
					verify(k)
				}
			}(start, end)
		}
		wg.Wait()
	}

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
