package tx_processor

import (
	"fmt"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// liveCommitteeProvider derives the active validator committee (BLS keys) from the chain state the tx executes
// against, so every replica computes the same set for the same block: non-jailed validators with positive stake
// whose account has a registered 48-byte BLS public key.
type liveCommitteeProvider struct {
	chainState *blockchain.ChainState
}

func newLiveCommitteeProvider(chainState *blockchain.ChainState) *liveCommitteeProvider {
	return &liveCommitteeProvider{chainState: chainState}
}

func (p *liveCommitteeProvider) GetActiveCommitteeBLSKeys() ([]cm.PublicKey, error) {
	stakeDB := p.chainState.GetStakeStateDB()
	if stakeDB == nil {
		return nil, fmt.Errorf("stake state DB unavailable")
	}
	validators, err := stakeDB.GetAllValidators()
	if err != nil {
		return nil, fmt.Errorf("GetAllValidators: %w", err)
	}
	var keys []cm.PublicKey
	for _, v := range validators {
		if v.IsJailed() {
			continue
		}
		if total := v.TotalStakedAmount(); total == nil || total.Sign() <= 0 {
			continue
		}
		as, err := p.chainState.GetAccountStateDB().AccountState(v.Address())
		if err != nil || as == nil {
			continue
		}
		pub := as.PublicKeyBls()
		if len(pub) != len(cm.PublicKey{}) {
			continue
		}
		keys = append(keys, cm.PubkeyFromBytes(pub))
	}
	return keys, nil
}
