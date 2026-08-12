package tx_processor

import (
	"fmt"
	"math/big"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/types"
)

// blobFeeOrReject returns the EIP-4844 blob fee to charge tx (0, nil for any
// non-blob tx), burned rather than credited to the leader — unlike ordinary
// execution gas, blob-gas fees have no equivalent of miner tip in the spec.
// Returns an error if tx's MaxFeePerBlobGas is below blockBlobBaseFee, the
// same treatment as insufficient balance (reject, don't execute).
func blobFeeOrReject(tx types.Transaction, blockBlobBaseFee *big.Int) (*big.Int, error) {
	n := len(tx.BlobVersionedHashes())
	if n == 0 {
		return big.NewInt(0), nil
	}
	if tx.MaxFeePerBlobGas().Cmp(blockBlobBaseFee) < 0 {
		return nil, fmt.Errorf("insufficient max fee per blob gas: have %s, want at least %s",
			tx.MaxFeePerBlobGas(), blockBlobBaseFee)
	}
	blobGasUsed := mt_common.BLOB_GAS_PER_BLOB * uint64(n)
	return new(big.Int).Mul(new(big.Int).SetUint64(blobGasUsed), blockBlobBaseFee), nil
}

// blockBlobBaseFee is a small wrapper around block.BlobBaseFeeAt so call
// sites don't need to import pkg/block directly just for this one call, and
// so the ms-timestamp conversion (blockTime here is in SECONDS, see
// ProcessTransactions' doc comment) lives in exactly one place.
func blockBlobBaseFee(lastBlockHeader types.BlockHeader, blockTimeSec uint64) *big.Int {
	return block.BlobBaseFeeAt(lastBlockHeader, blockTimeSec*1000)
}
