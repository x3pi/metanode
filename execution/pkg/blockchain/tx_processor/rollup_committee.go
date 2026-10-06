package tx_processor

import (
	"bytes"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// LiveCommitteeProvider derives the active validator committee (BLS keys) from the chain state the tx executes
// against, so every replica computes the same set for the same block: non-jailed validators with positive stake
// whose account has a registered 48-byte BLS public key.
type LiveCommitteeProvider struct {
	chainState *blockchain.ChainState
}

// NewLiveCommitteeProvider creates a LiveCommitteeProvider for the given chain state.
func NewLiveCommitteeProvider(chainState *blockchain.ChainState) *LiveCommitteeProvider {
	return &LiveCommitteeProvider{chainState: chainState}
}

func newLiveCommitteeProvider(chainState *blockchain.ChainState) *LiveCommitteeProvider {
	return NewLiveCommitteeProvider(chainState)
}

func (p *LiveCommitteeProvider) GetActiveCommitteeBLSKeys() ([]cm.PublicKey, error) {
	stakeDB := p.chainState.GetStakeStateDB()
	if stakeDB == nil {
		// A chain without a stake DB has no committee (callers that require one treat an empty committee as an error).
		return nil, nil
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

// VerifyNodeCommitteeKey checks whether a node's configured attestation key matches its on-chain validator identity.
// It returns isValidator=true if either nodeAddr or cfgAddr is listed in the validator set.
// If isValidator is true and there is any discrepancy (key mismatch, zero stake, jailed, or key missing from active committee),
// warning contains a detailed human-readable message. If everything is healthy or the node is not a validator, warning is empty.
func VerifyNodeCommitteeKey(chainState *blockchain.ChainState, nodeAddr, cfgAddr common.Address, attestPubKey cm.PublicKey) (isValidator bool, warning string) {
	if chainState == nil {
		return false, ""
	}
	stakeDB := chainState.GetStakeStateDB()
	if stakeDB == nil {
		return false, ""
	}
	validators, err := stakeDB.GetAllValidators()
	if err != nil {
		return false, fmt.Sprintf("failed to get validators: %v", err)
	}

	var matchedAddr common.Address
	found := false
	for _, v := range validators {
		addr := v.Address()
		if addr == nodeAddr || (cfgAddr != (common.Address{}) && addr == cfgAddr) {
			found = true
			matchedAddr = addr
			if v.IsJailed() {
				return true, fmt.Sprintf("validator %s is JAILED in stake state: attestations will not be accepted by peers", addr.Hex())
			}
			if total := v.TotalStakedAmount(); total == nil || total.Sign() <= 0 {
				return true, fmt.Sprintf("validator %s has zero or non-positive stake (%v): attestations will not be accepted by peers", addr.Hex(), total)
			}
			break
		}
	}
	if !found {
		return false, ""
	}
	isValidator = true

	accDB := chainState.GetAccountStateDB()
	if accDB == nil {
		return true, fmt.Sprintf("validator %s: account state DB unavailable", matchedAddr.Hex())
	}
	as, err := accDB.AccountState(matchedAddr)
	if err != nil || as == nil {
		return true, fmt.Sprintf("validator %s has no account state in database", matchedAddr.Hex())
	}
	pub := as.PublicKeyBls()
	if len(pub) != len(cm.PublicKey{}) {
		return true, fmt.Sprintf("validator %s on-chain account has missing or invalid PublicKeyBls (len %d, expected 48)", matchedAddr.Hex(), len(pub))
	}
	if !bytes.Equal(pub, attestPubKey.Bytes()) {
		return true, fmt.Sprintf("validator %s on-chain PublicKeyBls (%x) does not match node attestation key (%x): attestations will stay PENDING forever", matchedAddr.Hex(), pub, attestPubKey.Bytes())
	}

	provider := NewLiveCommitteeProvider(chainState)
	keys, err := provider.GetActiveCommitteeBLSKeys()
	if err != nil {
		return true, fmt.Sprintf("failed to get active committee keys: %v", err)
	}
	inCommittee := false
	for _, k := range keys {
		if k == attestPubKey {
			inCommittee = true
			break
		}
	}
	if !inCommittee {
		return true, fmt.Sprintf("validator %s attestation key %x is not in active committee (active committee size %d)", matchedAddr.Hex(), attestPubKey[:6], len(keys))
	}

	return true, ""
}
