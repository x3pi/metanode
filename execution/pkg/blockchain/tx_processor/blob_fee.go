package tx_processor

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/types"
)

// computeBlobBudgetRejections walks txs in the block's fixed, deterministic
// order and returns the set of blob tx hashes that would push the block's
// cumulative blob gas over MAX_BLOBS_PER_BLOCK*BLOB_GAS_PER_BLOB — mirroring
// mainnet Ethereum's per-block blob-gas cap, which this chain otherwise has no
// hard enforcement of (only the economic pressure of a fast-rising blob base
// fee). A rejected tx's blob gas does NOT count toward the running total, so
// later blob txs within budget are still accepted — matches
// blobFeeOrReject's/computeBlobGasUsed's contract that a budget-rejected tx
// contributes zero blob gas to the block (it never got space), as opposed to
// a tx that's included but reverts for an unrelated reason (which still
// occupies its blob gas, per computeBlobGasUsed's existing "regardless of
// success" rule).
//
// Must be called with the SAME tx order used elsewhere for this block (the
// original group order, not a kind-partitioned/re-ordered one) — every call
// site does that independently but deterministically, so they agree.
func computeBlobBudgetRejections(txs []types.Transaction) map[common.Hash]struct{} {
	const maxBlockBlobGas = mt_common.MAX_BLOBS_PER_BLOCK * mt_common.BLOB_GAS_PER_BLOB
	var rejected map[common.Hash]struct{}
	var used uint64
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		n := len(tx.BlobVersionedHashes())
		if n == 0 {
			continue
		}
		txBlobGas := mt_common.BLOB_GAS_PER_BLOB * uint64(n)
		if used+txBlobGas > maxBlockBlobGas {
			if rejected == nil {
				rejected = make(map[common.Hash]struct{})
			}
			rejected[tx.Hash()] = struct{}{}
			continue
		}
		used += txBlobGas
	}
	return rejected
}

// blobFeeOrReject returns the EIP-4844 blob fee to charge tx (0, nil for any
// non-blob tx), burned rather than credited to the leader — unlike ordinary
// execution gas, blob-gas fees have no equivalent of miner tip in the spec.
// Returns an error if tx's MaxFeePerBlobGas is below blockBlobBaseFee, or if
// tx is in blobBudgetRejected (see computeBlobBudgetRejections) — both get
// the same treatment as insufficient balance (reject, don't execute, nonce
// still advances at the call site).
func blobFeeOrReject(tx types.Transaction, blockBlobBaseFee *big.Int, blobBudgetRejected map[common.Hash]struct{}) (*big.Int, error) {
	n := len(tx.BlobVersionedHashes())
	if n == 0 {
		return big.NewInt(0), nil
	}
	if _, over := blobBudgetRejected[tx.Hash()]; over {
		return nil, fmt.Errorf("block blob-gas budget exceeded (max %d blobs/block)", mt_common.MAX_BLOBS_PER_BLOCK)
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
