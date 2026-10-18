package tx_processor

import (
	"bytes"

	eth_common "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// isFeeExemptAddr reports whether tx is to or from a configured free-fee address. Such txs are exempt from the fee
// floor (mempool admission) and are charged no fee at execution, in every pipeline.
func isFeeExemptAddr(tx types.Transaction, free map[eth_common.Address]struct{}) bool {
	if len(free) == 0 {
		return false
	}
	if _, ok := free[tx.ToAddress()]; ok {
		return true
	}
	_, ok := free[tx.FromAddress()]
	return ok
}

// isFeeExempt is isFeeExemptAddr against the chain's configured free-fee set.
func isFeeExempt(chainState *blockchain.ChainState, tx types.Transaction) bool {
	return isFeeExemptAddr(tx, chainState.GetFreeFeeAddress())
}

// isFreeRegistrationTx is the one nonce-0 tx that is free by protocol: the BLS-key registration sent to the account
// setting address (the account has no balance yet). A nonce-0 tx of any other kind pays like everyone else; exempting all
// nonce-0 txs let an unlimited number of fresh zero-balance keys flood the pool and the chain at price 0.
//
// "Registration" means a well-formed setBlsPublicKey(bytes) call carrying a 48-byte key -- the same shape the executor
// accepts. Calldata that would just be rejected must not ride the exemption: a proposer could otherwise put price-0
// garbage sent to the account setting address from fresh keys into a block for free.
func isFreeRegistrationTx(tx types.Transaction) bool {
	if tx.GetNonce() != 0 || tx.ToAddress() != utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		return false
	}
	data := tx.CallData().Input()
	if len(data) < 4 || !bytes.Equal(data[:4], utils.GetFunctionSelector("setBlsPublicKey(bytes)")) {
		return false
	}
	key, err := UnpackSetBlsPublicKeyInput(data)
	return err == nil && len(key) == 48
}

// accountHasState reports whether as is a real, pre-existing account. AccountStateDB.AccountState creates an empty entry
// for an address it has never seen, so "non-nil" proves nothing; an account counts as existing once it holds a balance, has
// sent a tx, registered a BLS key or carries code.
func accountHasState(as types.AccountState) bool {
	return as != nil && (as.Nonce() > 0 || as.TotalBalance().Sign() > 0 || len(as.PublicKeyBls()) > 0 || as.SmartContractState() != nil)
}

// feeFloorOK reports whether tx pays at least the chain's minimum gas price. It applies exactly the rule the mempool
// admission gate (VerifyTransaction, step 4) applies, so a committed block cannot carry a tx that no honest node would
// have admitted:
//   - txs to/from a free-fee address are exempt,
//   - the nonce-0 BLS registration to the account setting address is exempt,
//   - everything else must satisfy tx.ValidMaxGasPrice(MINIMUM_BASE_FEE) (which has its own native-reward exemption).
//
// It is a pure function of (tx, the chain's static free-fee set): no clocks, no caches, no peer data.
func feeFloorOK(tx types.Transaction, free map[eth_common.Address]struct{}) bool {
	if isFreeRegistrationTx(tx) || isFeeExemptAddr(tx, free) {
		return true
	}
	return tx.ValidMaxGasPrice(mt_common.MINIMUM_BASE_FEE)
}

// FilterBelowFeeFloor drops every tx that pays less than the minimum gas price. Before, the floor was enforced only at
// mempool admission, so a Byzantine proposer could put zero-fee txs into a committed block and every honest node
// executed them for free. Like FilterInvalidSignatures it DROPS (no receipt, no state change, no nonce bump), runs on
// every execution path before the native fast path / Block-STM split, and keeps group order and per-group item order, so
// both pipelines apply one rule. Honours the same devnet bypass switches as the signature filter (never active on the
// production chain, see sigVerifyBypassedForDevnet).
func FilterBelowFeeFloor(chainState *blockchain.ChainState, groups []grouptxns.RelativeGroup) []grouptxns.RelativeGroup {
	if sigVerifyBypassedForDevnet() || execFilterDisabledForDevnetBenchmark() {
		return groups
	}
	free := chainState.GetFreeFeeAddress()
	out := make([]grouptxns.RelativeGroup, 0, len(groups))
	dropped := 0
	for _, g := range groups {
		kept := make([]grouptxns.Item, 0, len(g.Items))
		for _, item := range g.Items {
			if feeFloorOK(item.Tx, free) {
				kept = append(kept, item)
				continue
			}
			dropped++
			if dropped <= 5 {
				logger.Warn("❌ [FEE-FLOOR] dropping tx %s (From: %s): gas price below minimum %d in committed block",
					item.Tx.Hash().Hex(), item.Tx.FromAddress().Hex(), mt_common.MINIMUM_BASE_FEE)
			}
		}
		if len(kept) == 0 {
			continue
		}
		ng := g
		ng.Items = kept
		out = append(out, ng)
	}
	if dropped > 5 {
		logger.Warn("❌ [FEE-FLOOR] dropped %d txs below the minimum gas price (only first 5 logged)", dropped)
	}
	return out
}
