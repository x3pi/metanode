package block

import (
	"math/big"

	"github.com/ethereum/go-ethereum/consensus/misc/eip4844"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"

	"github.com/meta-node-blockchain/meta-node/types"
)

// NextExcessBlobGas computes the EIP-4844 excess-blob-gas field for a new block
// from its parent header, per consensus/misc/eip4844.CalcExcessBlobGas. Every
// node must recompute this deterministically from parent state — it is never
// trusted from an incoming value. Both the local block-build path
// (cmd/simple_chain/processor) and the peer-sync path (executor) call this
// same function so they can never derive it differently.
//
// Uses params.AllDevChainProtocolChanges (all forks active from genesis) since
// this chain has no hardfork-activation mechanism of its own — same choice as
// cmd/simple_chain/transaction_args.go's setCancunFeeDefaults.
func NextExcessBlobGas(parentHeader types.BlockHeader, timestampMs uint64) uint64 {
	parentExcess := parentHeader.ExcessBlobGas()
	parentUsed := parentHeader.BlobGasUsed()
	return eip4844.CalcExcessBlobGas(
		params.AllDevChainProtocolChanges,
		&e_types.Header{ExcessBlobGas: &parentExcess, BlobGasUsed: &parentUsed},
		timestampMs/1000,
	)
}

// BlobBaseFeeAt returns the EIP-4844 blob base fee (price per unit of blob
// gas) for a block built on top of parentHeader at timestampMs, per
// consensus/misc/eip4844.CalcBlobFee. This is what a blob tx's
// MaxFeePerBlobGas must be at least, and what actually gets charged per unit
// of blob gas used — see Transaction.MaxFeePerBlobGas.
func BlobBaseFeeAt(parentHeader types.BlockHeader, timestampMs uint64) *big.Int {
	excess := NextExcessBlobGas(parentHeader, timestampMs)
	return eip4844.CalcBlobFee(params.AllDevChainProtocolChanges, &e_types.Header{ExcessBlobGas: &excess})
}
