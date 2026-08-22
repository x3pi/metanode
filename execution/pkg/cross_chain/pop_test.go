package cross_chain

import (
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPop_ValidVerification(t *testing.T) {
	kp := bls.GenerateKeyPair()
	pub := kp.PublicKey()
	pri := kp.PrivateKey()

	popSig := PopSign(pri, pub)

	valid, err := PopVerify(pub.Bytes(), popSig.Bytes())
	require.NoError(t, err)
	assert.True(t, valid)

	entry := ValidatorEntry{
		PubkeyBLS:    pub.Bytes(),
		Stake:        1000,
		PopSignature: popSig.Bytes(),
	}
	assert.NoError(t, ValidateCommitteeEntry(entry))

	// Zero stake should fail
	zeroStakeEntry := ValidatorEntry{
		PubkeyBLS:    pub.Bytes(),
		Stake:        0,
		PopSignature: popSig.Bytes(),
	}
	assert.ErrorIs(t, ValidateCommitteeEntry(zeroStakeEntry), ErrZeroStake)
}

func TestPop_RogueKeyAttackPrevention(t *testing.T) {
	// Legitimate victim validator
	victimKP := bls.GenerateKeyPair()
	victimPub := victimKP.PublicKey()
	victimPri := victimKP.PrivateKey()
	victimPop := PopSign(victimPri, victimPub)

	// Attacker
	attackerKP := bls.GenerateKeyPair()
	attackerPub := attackerKP.PublicKey()
	attackerPri := attackerKP.PrivateKey()

	// 1. Rogue Key Attack Case A: Attacker attempts to register victim's public key with attacker's signature
	attackerPopForVictimPub := PopSign(attackerPri, victimPub)
	rogueEntryA := ValidatorEntry{
		PubkeyBLS:    victimPub.Bytes(),
		Stake:        500,
		PopSignature: attackerPopForVictimPub.Bytes(),
	}
	errA := ValidateCommitteeEntry(rogueEntryA)
	assert.Error(t, errA, "Attacker cannot register victim's pubkey without possessing victim's private key")

	// 2. Rogue Key Attack Case B: Attacker attempts to reuse victim's valid PoP for attacker's public key
	rogueEntryB := ValidatorEntry{
		PubkeyBLS:    attackerPub.Bytes(),
		Stake:        500,
		PopSignature: victimPop.Bytes(),
	}
	errB := ValidateCommitteeEntry(rogueEntryB)
	assert.Error(t, errB, "Reusing another validator's PoP signature for a different public key must fail")

	// 3. Rogue Key Attack Case C: Corrupted/Empty signature
	rogueEntryC := ValidatorEntry{
		PubkeyBLS:    victimPub.Bytes(),
		Stake:        500,
		PopSignature: []byte{1, 2, 3},
	}
	assert.Error(t, ValidateCommitteeEntry(rogueEntryC))
}

func TestPop_ValidateCommittee(t *testing.T) {
	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()
	kp3 := bls.GenerateKeyPair()

	entry1 := ValidatorEntry{
		PubkeyBLS:    kp1.PublicKey().Bytes(),
		Stake:        1000,
		PopSignature: PopSign(kp1.PrivateKey(), kp1.PublicKey()).Bytes(),
	}
	entry2 := ValidatorEntry{
		PubkeyBLS:    kp2.PublicKey().Bytes(),
		Stake:        2000,
		PopSignature: PopSign(kp2.PrivateKey(), kp2.PublicKey()).Bytes(),
	}
	entry3 := ValidatorEntry{
		PubkeyBLS:    kp3.PublicKey().Bytes(),
		Stake:        3000,
		PopSignature: PopSign(kp3.PrivateKey(), kp3.PublicKey()).Bytes(),
	}

	// Valid 3-validator committee
	committee := []ValidatorEntry{entry1, entry2, entry3}
	assert.NoError(t, ValidateCommittee(committee))

	// Empty committee fails
	assert.ErrorIs(t, ValidateCommittee(nil), ErrEmptyCommittee)

	// Duplicate validator fails
	dupEntry := ValidatorEntry{
		PubkeyBLS:    kp1.PublicKey().Bytes(),
		Stake:        500,
		PopSignature: PopSign(kp1.PrivateKey(), kp1.PublicKey()).Bytes(),
	}
	assert.ErrorIs(t, ValidateCommittee([]ValidatorEntry{entry1, entry2, dupEntry}), ErrDuplicatePublicKey)
}
