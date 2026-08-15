package vm_processor

import (
	"github.com/ethereum/go-ethereum/core"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

// computeIntrinsicGas returns the minimum gas a transaction must pay before
// any EVM execution begins: the base 21000 (53000 for contract creation),
// the per-byte calldata cost (EIP-2028), the flat per-entry access-list cost
// (EIP-2930), the EIP-7702 authorization-tuple cost, and (for deploys)
// EIP-3860's init-code word cost. Reuses go-ethereum's own core.IntrinsicGas
// instead of reimplementing the formula by hand, so this matches mainnet
// Ethereum's accounting exactly.
//
// isHomestead/isEIP2028/isEIP3860 are passed true unconditionally: this
// chain has no hardfork-activation mechanism (see
// params.AllDevChainProtocolChanges used elsewhere in this codebase) —
// every EIP core.IntrinsicGas can gate is always active here.
func computeIntrinsicGas(tx types.Transaction, data []byte, isContractCreation bool) (uint64, error) {
	return core.IntrinsicGas(data, tx.EthAccessList(), tx.EthAuthorizationList(), isContractCreation, true, true, true)
}

// vmGasBudget returns the gas budget to actually hand the VM (maxGas minus
// the intrinsic cost the tx must pay up front) and the intrinsic cost
// itself. When maxGas can't even cover the intrinsic cost, the budget
// clamps to 0 — the VM call still happens (see applyIntrinsicGas) so the
// sender's nonce still gets consumed via mvm's own increment-on-every-path
// behavior (including its exception handlers), exactly like a normal
// mid-execution out-of-gas failure. A zero-budget call to an account that
// happens to have empty code would otherwise return a trivial "success"
// without ever running dispatch()'s gas check, so the maxGas<intrinsicGas
// case must still be caught and reported explicitly — see applyIntrinsicGas.
func vmGasBudget(intrinsicGas, maxGas uint64) uint64 {
	if maxGas < intrinsicGas {
		return 0
	}
	return maxGas - intrinsicGas
}

// applyIntrinsicGas folds the intrinsic cost into mvmResult after a VM call
// made with vmGasBudget's reduced budget. If maxGas didn't even cover the
// intrinsic cost, the call is forced to a hard failure consuming the whole
// gas limit (mirrors go-ethereum's ErrIntrinsicGas — the tx never gets to
// spend a single unit of gas on real execution) while preserving whatever
// state-change maps mvm already populated (in particular MapNonce: mvm's
// run() always increments the sender's nonce, even from its exception
// handlers, so this still consumes the nonce and prevents replay).
// Otherwise, the intrinsic cost — never charged inside the VM's own
// gas-tracked budget, since vmGasBudget already subtracted it — is added
// back so gasUsed correctly reflects the tx's true total cost.
func applyIntrinsicGas(mvmResult *mvm.MVMExecuteResult, intrinsicGas, maxGas uint64) {
	if mvmResult == nil {
		return
	}
	if maxGas < intrinsicGas {
		mvmResult.Status = pb.RECEIPT_STATUS_TRANSACTION_ERROR
		mvmResult.Exception = pb.EXCEPTION_ERR_OUT_OF_GAS
		mvmResult.Exmsg = "intrinsic gas too low"
		mvmResult.Return = nil
		mvmResult.GasUsed = maxGas
		return
	}
	mvmResult.GasUsed += intrinsicGas
}
