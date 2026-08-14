package mvm

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// ExecutionEngine is the seam between the Go host and the transaction-
// execution core (today: mvm+Xapian via cgo, see *MVMApi below). It exists
// so a future implementation — e.g. one that talks to the same mvm+Xapian
// core running as a TrustZone TA over a session API instead of over cgo —
// can be swapped in without touching any caller.
//
// This is step B5 of note/tee_core_packaging_plan.md: a pure, behavior-
// preserving extraction. *MVMApi already implements every method below
// (see the compile-time assertion at the bottom of this file); no call site
// has been changed yet, and no C++ code changed. The interface's method set
// intentionally mirrors *MVMApi's exported methods exactly — it is not an
// aspirational redesign of the boundary (that's B1-B4 in the same plan,
// which do change what crosses the boundary and need their own shadow-mode
// verification before landing).
type ExecutionEngine interface {
	// Instance identity & wiring — set once per mvmId before executing.
	GetKey() common.Address
	SetSmartContractDb(smartContractDb SmartContractDB)
	SmartContractDatas() SmartContractDB
	SetAccountStateDb(accountStateDb AccountStateDB)
	AccountStateDb() AccountStateDB

	// Per-transaction context, set immediately before Call/Execute/Deploy/etc.
	SetRelatedAddresses(addresses []common.Address)
	GetCurrentRelatedAddresses() []common.Address
	InRelatedAddress(address common.Address) bool
	AddRelatedAddress(address common.Address)
	SetBlobContext(blobVersionedHashes [][]byte, blobBaseFee *uint256.Int)
	SetCrossChainContext(sender common.Address, sourceChainId uint64)
	ClearCrossChainContext()

	// Execution entry points.
	Call(
		bSender []byte,
		bContractAddress []byte,
		bInput []byte,
		amount *big.Int,
		gasPrice uint64,
		gasLimit uint64,
		blockPrevrandao uint64,
		blockGasLimit uint64,
		blockTime uint64,
		blockBaseFee uint64,
		blockNumber uint64,
		blockCoinbase common.Address,
		mvmId common.Address,
		readOnly bool,
		bTxHash []byte,
		relatedAddresses []common.Address,
		isDebug bool,
		isOffChain bool,
	) *MVMExecuteResult

	Execute(
		bSender []byte,
		bContractAddress []byte,
		bInput []byte,
		amount *big.Int,
		gasPrice uint64,
		gasLimit uint64,
		blockPrevrandao uint64,
		blockGasLimit uint64,
		blockTime uint64,
		blockBaseFee uint64,
		blockNumber uint64,
		blockCoinbase common.Address,
		mvmId common.Address,
		bTxHash []byte,
		relatedAddresses []common.Address,
		isDebug bool,
		isCache bool,
	) *MVMExecuteResult

	ExecuteBatch(
		inputs []ExecuteBatchInput,
		blockPrevrandao uint64,
		blockGasLimit uint64,
		blockTime uint64,
		blockBaseFee uint64,
		blockNumber uint64,
		blockCoinbase common.Address,
		mvmId common.Address,
	) []*MVMExecuteResult

	Deploy(
		bSender []byte,
		bContractConstructor []byte,
		amount *big.Int,
		gasPrice uint64,
		gasLimit uint64,
		blockPrevrandao uint64,
		blockGasLimit uint64,
		blockTime uint64,
		blockBaseFee uint64,
		blockNumber uint64,
		blockCoinbase common.Address,
		mvmId common.Address,
		bTxHash []byte,
		isDebug bool,
		isCache bool,
		isOffChain bool,
	) *MVMExecuteResult

	SendNative(
		bSender []byte,
		bContractAddress []byte,
		amount *big.Int,
		gasPrice uint64,
		gasLimit uint64,
		blockPrevrandao uint64,
		blockGasLimit uint64,
		blockTime uint64,
		blockBaseFee uint64,
		blockNumber uint64,
		blockCoinbase common.Address,
		mvmId common.Address,
		isCache bool,
	) *MVMExecuteResult

	ProcessNativeMintBurn(
		bFrom []byte,
		bTo []byte,
		amount *big.Int,
		operationType uint64, // 0: mint, 1: burn
		gasPrice uint64,
		gasLimit uint64,
		blockPrevrandao uint64,
		blockGasLimit uint64,
		blockTime uint64,
		blockBaseFee uint64,
		blockNumber uint64,
		blockCoinbase common.Address,
		mvmId common.Address,
		isCache bool,
	) *MVMExecuteResult

	NoncePlusOne(
		bSender []byte,
		gasPrice uint64,
		gasLimit uint64,
		blockPrevrandao uint64,
		blockGasLimit uint64,
		blockTime uint64,
		blockBaseFee uint64,
		blockNumber uint64,
		blockCoinbase common.Address,
		mvmId common.Address,
		isCache bool,
	) *MVMExecuteResult

	// GetExecuteResult returns the last result recorded on this instance
	// (some call sites read it back separately from the call above, e.g.
	// after a panic recovery).
	GetExecuteResult() *MVMExecuteResult
}

// Compile-time assertion: today's cgo-backed implementation must keep
// satisfying ExecutionEngine. If this line fails to compile, either
// *MVMApi lost a method the interface expects, or the interface drifted
// out of sync with *MVMApi — fix the mismatch before anything else, since
// every other B5 consumer assumes this holds.
var _ ExecutionEngine = (*MVMApi)(nil)
