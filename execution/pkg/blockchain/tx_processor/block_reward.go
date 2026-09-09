package tx_processor

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// RewardBlockLeader pays a block's total gas fee to its leader (the validator who proposed
// it), split between the validator's own commission and its delegators' pro-rata share --
// applied every block instead of batched at epoch boundary.
//
// Rationale (per-block, not per-epoch): with N delegators, actually paying each one out
// every block would be O(N) per block. Both Cosmos SDK's F1 fee-distribution algorithm and
// Polkadot's era-points model avoid that by updating a single accumulated-reward-per-share
// ratio each block (O(1), touches only this block's leader) and letting each delegator
// settle lazily whenever they choose to withdraw -- exactly the AccumulatedRewardsPerShare /
// rewardDebt ledger validator_state.go already implements. Batching at epoch boundary instead
// would still need to loop every active validator inside the epoch-transition state
// transition (AdvanceEpochWithBoundary), which is deterministic and fork-sensitive on its own
// (see its "single write path" / lock-contention warnings) -- there is no upside to adding an
// O(validators) settlement spike there, so it is done here, per block, per leader, instead.
//
// creditFn applies the actual balance mutation: native_fast_path.go's lock-free path passes
// AddPendingBalance (its merge phase already defers all balance writes that way);
// true_block_stm.go's single-threaded commit tail passes AddBalance directly. Both call sites
// run after their respective parallel phases have finished, so no extra locking is needed here.
func RewardBlockLeader(
	chainState *blockchain.ChainState,
	leaderAddr common.Address,
	totalGasFee *big.Int,
	creditFn func(addr common.Address, amount *big.Int) error,
) {
	if totalGasFee == nil || totalGasFee.Sign() <= 0 || leaderAddr == (common.Address{}) {
		return
	}

	payDirect := func() {
		if err := creditFn(leaderAddr, totalGasFee); err != nil {
			logger.Warn("⚠️ [BLOCK REWARD] credit leader %s failed: %v", leaderAddr.Hex(), err)
		}
	}

	stakeStateDB := chainState.GetStakeStateDB()
	if stakeStateDB == nil {
		payDirect()
		return
	}

	validator, err := stakeStateDB.GetValidator(leaderAddr)
	if err != nil || validator == nil || validator.TotalStakedAmount() == nil || validator.TotalStakedAmount().Sign() <= 0 {
		// Leader is not a registered/staked validator (e.g. tests, or a non-staking chain) --
		// nothing to split, keep the old behavior so gas fee is never lost.
		payDirect()
		return
	}

	stakerShare, err := stakeStateDB.DistributeRewardsToValidator(leaderAddr, totalGasFee)
	if err != nil || stakerShare == nil {
		logger.Warn("⚠️ [BLOCK REWARD] DistributeRewardsToValidator(%s) failed, paying gas fee directly: %v", leaderAddr.Hex(), err)
		payDirect()
		return
	}

	commission := new(big.Int).Sub(totalGasFee, stakerShare)
	if commission.Sign() > 0 {
		if err := creditFn(leaderAddr, commission); err != nil {
			logger.Warn("⚠️ [BLOCK REWARD] credit commission to leader %s failed: %v", leaderAddr.Hex(), err)
		}
	}
	if stakerShare.Sign() > 0 {
		// Real custody backing every delegator's AccumulatedRewardsPerShare claim -- withdrawReward
		// (validation_transaction.go's _withdrawRewards) pays out of this same contract balance.
		if err := creditFn(mt_common.VALIDATOR_CONTRACT_ADDRESS, stakerShare); err != nil {
			logger.Warn("⚠️ [BLOCK REWARD] credit staker share to validation contract failed: %v", err)
		}
	}
}
