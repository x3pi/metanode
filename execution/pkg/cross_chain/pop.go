package cross_chain

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// BLSPopDomain is the domain separation tag for BLS Proof-of-Possession (Section 1.3 #4 & 5.4).
var BLSPopDomain = []byte("BLS_POP_METANODE_ROOT_ANCHOR_V1:")

var (
	ErrZeroStake              = errors.New("validator stake cannot be zero")
	ErrEmptyCommittee         = errors.New("committee cannot be empty")
	ErrDuplicatePublicKey     = errors.New("duplicate validator public key detected in committee")
	ErrPopVerifyFailed        = errors.New("BLS Proof-of-Possession verification failed")
	ErrInvalidQuorumThreshold = errors.New("quorum threshold below the BFT safety floor")
)

// MinSafeQuorumThresholdBasisPoints is the minimum QuorumThreshold (out of 10000 basis points)
// this codebase will ever accept for a ChainRegistry entry — 6667 basis points = 2/3, the same
// BFT safety floor used everywhere else in this project (root_anchor.go's BftQuorumThreshold,
// GovernanceEngine.QuorumThreshold's ceil(2N/3)). VerifyQuorumCertAgainstRegistry (gateway.go)
// treats registry.QuorumThreshold as the fraction of a committee's TOTAL STAKE required to sign
// before a QuorumCert verifies — a value below 2/3 would let a cert verify without Byzantine
// fault tolerance, i.e. a minority (even a single low-stake signer) could forge a "valid" quorum
// for that chain's attestCommit()/vote() going forward.
const MinSafeQuorumThresholdBasisPoints = 6667

// ValidateQuorumThreshold enforces the BFT safety floor on a ChainRegistry entry's
// QuorumThreshold. Zero means "unset" — VerifyQuorumCertAgainstRegistry falls back to computing
// a real stake-weighted 2/3 threshold itself, which is always safe, so zero always passes. Any
// nonzero value must be a real, safe fraction: between the 2/3 floor and 100%.
func ValidateQuorumThreshold(basisPoints uint64) error {
	if basisPoints == 0 {
		return nil
	}
	if basisPoints < MinSafeQuorumThresholdBasisPoints || basisPoints > 10000 {
		return fmt.Errorf("%w: %d basis points (must be 0 (default) or between %d (2/3) and 10000)",
			ErrInvalidQuorumThreshold, basisPoints, MinSafeQuorumThresholdBasisPoints)
	}
	return nil
}

// PopSign generates a Proof-of-Possession signature over (BLSPopDomain || pubkey).
func PopSign(privKey cm.PrivateKey, pubKey cm.PublicKey) cm.Sign {
	msg := append(append([]byte{}, BLSPopDomain...), pubKey.Bytes()...)
	return bls.Sign(privKey, msg)
}

// PopVerify verifies a BLS Proof-of-Possession signature for a public key.
func PopVerify(pubkeyBytes []byte, popSigBytes []byte) (bool, error) {
	if len(pubkeyBytes) == 0 || len(popSigBytes) == 0 {
		return false, ErrPopVerifyFailed
	}

	msg := append(append([]byte{}, BLSPopDomain...), pubkeyBytes...)
	pubKey := cm.PubkeyFromBytes(pubkeyBytes)
	sig := cm.SignFromBytes(popSigBytes)

	valid := bls.VerifySign(pubKey, sig, msg)
	if !valid {
		return false, ErrPopVerifyFailed
	}

	return true, nil
}

// ValidateCommitteeEntry checks that a validator has stake > 0 and a valid Proof-of-Possession.
func ValidateCommitteeEntry(entry ValidatorEntry) error {
	if entry.Stake == 0 {
		return ErrZeroStake
	}

	valid, err := PopVerify(entry.PubkeyBLS, entry.PopSignature)
	if err != nil || !valid {
		return fmt.Errorf("%w: for validator with pubkey 0x%x", ErrPopVerifyFailed, entry.PubkeyBLS)
	}

	return nil
}

// ValidateCommittee validates all validator entries in a committee registration on ChainRegistry:
// - Non-empty committee
// - Valid PoP signature for each validator (preventing rogue-key attacks)
// - Unique public keys
func ValidateCommittee(committee []ValidatorEntry) error {
	if len(committee) == 0 {
		return ErrEmptyCommittee
	}

	seenKeys := make([][]byte, 0, len(committee))
	for _, entry := range committee {
		if err := ValidateCommitteeEntry(entry); err != nil {
			return err
		}

		for _, seen := range seenKeys {
			if bytes.Equal(seen, entry.PubkeyBLS) {
				return ErrDuplicatePublicKey
			}
		}
		seenKeys = append(seenKeys, entry.PubkeyBLS)
	}

	return nil
}
