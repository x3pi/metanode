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
	seen := make(map[cm.PublicKey]bool)
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
		// Distinct keys only: validators sharing one BLS key (the single-cluster-key deployment) are one signer, so
		// counting them separately would inflate N and the f+1 threshold beyond what can ever be signed.
		k := cm.PubkeyFromBytes(pub)
		if seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
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
		return true, fmt.Sprintf("validator %s on-chain PublicKeyBls (%s) does not match node attestation key (%s): attestations will stay PENDING forever", matchedAddr.Hex(), maskKeyHex(pub), maskKeyHex(attestPubKey.Bytes()))
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
		return true, fmt.Sprintf("validator %s attestation key %s is not in active committee (active committee size %d)", matchedAddr.Hex(), maskKeyHex(attestPubKey.Bytes()), len(keys))
	}

	return true, ""
}

// VerifyNodeCommitteeKeyLight is the periodic-health variant of VerifyNodeCommitteeKey. It uses point lookups only
// (GetValidator + account state) and NEVER enumerates the validator trie: GetAllValidators walks NOMT and must not run
// concurrently with the background commit of a block (see the note in ChainState on the boundary-block race), which a
// timer goroutine cannot guarantee. It does not check the top-N committee cap; the exact check runs once at startup.
// err != nil means the state could not be read this time: callers keep the previous status instead of flipping it.
func VerifyNodeCommitteeKeyLight(chainState *blockchain.ChainState, nodeAddr, cfgAddr common.Address, attestPubKey cm.PublicKey) (isValidator bool, warning string, err error) {
	if chainState == nil || chainState.GetStakeStateDB() == nil || chainState.GetAccountStateDB() == nil {
		return false, "", nil
	}
	stakeDB := chainState.GetStakeStateDB()
	candidates := []common.Address{nodeAddr}
	if cfgAddr != (common.Address{}) && cfgAddr != nodeAddr {
		candidates = append(candidates, cfgAddr)
	}
	for _, addr := range candidates {
		v, verr := stakeDB.GetValidator(addr)
		if verr != nil {
			return false, "", fmt.Errorf("GetValidator(%s): %w", addr.Hex(), verr)
		}
		if v == nil {
			continue
		}
		if v.IsJailed() {
			return true, fmt.Sprintf("validator %s is JAILED in stake state: attestations will not be accepted by peers", addr.Hex()), nil
		}
		if total := v.TotalStakedAmount(); total == nil || total.Sign() <= 0 {
			return true, fmt.Sprintf("validator %s has zero or non-positive stake (%v): attestations will not be accepted by peers", addr.Hex(), total), nil
		}
		as, aerr := chainState.GetAccountStateDB().AccountState(addr)
		if aerr != nil {
			return false, "", fmt.Errorf("AccountState(%s): %w", addr.Hex(), aerr)
		}
		if as == nil {
			return true, fmt.Sprintf("validator %s has no account state in database", addr.Hex()), nil
		}
		pub := as.PublicKeyBls()
		if len(pub) != len(cm.PublicKey{}) {
			return true, fmt.Sprintf("validator %s on-chain account has missing or invalid PublicKeyBls (len %d, expected 48)", addr.Hex(), len(pub)), nil
		}
		if !bytes.Equal(pub, attestPubKey.Bytes()) {
			return true, fmt.Sprintf("validator %s on-chain PublicKeyBls (%s) does not match node attestation key (%s): attestations will stay PENDING forever", addr.Hex(), maskKeyHex(pub), maskKeyHex(attestPubKey.Bytes())), nil
		}
		return true, "", nil
	}
	return false, "", nil
}

// maskKeyHex masks public keys to avoid dumping full key material in warnings (e.g. 0x1234...cdef).
func maskKeyHex(key []byte) string {
	if len(key) == 0 {
		return "none"
	}
	if len(key) <= 8 {
		return fmt.Sprintf("0x%x", key)
	}
	return fmt.Sprintf("0x%x...%x", key[:4], key[len(key)-4:])
}
