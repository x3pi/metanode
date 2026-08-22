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
	ErrZeroStake          = errors.New("validator stake cannot be zero")
	ErrEmptyCommittee     = errors.New("committee cannot be empty")
	ErrDuplicatePublicKey = errors.New("duplicate validator public key detected in committee")
	ErrPopVerifyFailed    = errors.New("BLS Proof-of-Possession verification failed")
)

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
