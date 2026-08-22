package cross_chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTestValidator(stake uint64) ValidatorEntry {
	kp := bls.GenerateKeyPair()
	pub := kp.PublicKey()
	pri := kp.PrivateKey()
	popSig := PopSign(pri, pub)
	return ValidatorEntry{
		PubkeyBLS:    pub.Bytes(),
		Stake:        stake,
		PopSignature: popSig.Bytes(),
	}
}

func TestEpochSync_P3_1_CommitteeUpdateLifecycleDoD(t *testing.T) {
	registry := make(map[uint64]*ChainRegistry)
	v1 := makeTestValidator(1000)

	registry[101] = &ChainRegistry{
		ChainID:          101,
		Committee:        []ValidatorEntry{v1},
		Epoch:            5,
		QuorumThreshold:  667,
		GatewayContract:  common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		StateRoot:        common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		ArchivalEndpoint: "http://archive.test",
		RegisteredAt:     1000,
	}

	v2 := makeTestValidator(2000)
	v3 := makeTestValidator(3000)

	newStateRoot := common.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999")
	cert := QuorumCert{
		Epoch:              5,
		AggregateSignature: make([]byte, 48),
		SignerBitmap:       []byte{0x0F},
	}

	// 1. Success Case: Epoch 5 -> 6
	updateValid := CommitteeUpdate{
		SourceChainID:   101,
		NewEpoch:        6,
		NewCommittee:    []ValidatorEntry{v2, v3},
		QuorumThreshold: 3334,
		StateRoot:       newStateRoot,
		Cert:            cert,
	}
	err := ApplyCommitteeUpdate(registry, updateValid, true)
	require.NoError(t, err)

	reg := registry[101]
	assert.Equal(t, uint64(6), reg.Epoch)
	assert.Equal(t, newStateRoot, reg.StateRoot)
	assert.Equal(t, 2, len(reg.Committee))
	assert.Equal(t, uint64(3334), reg.QuorumThreshold)

	// 2. Non-sequential Reject: Epoch 6 -> 8 (skipping 7)
	updateSkip := CommitteeUpdate{
		SourceChainID:   101,
		NewEpoch:        8,
		NewCommittee:    []ValidatorEntry{v2, v3},
		QuorumThreshold: 3334,
		StateRoot:       newStateRoot,
		Cert: QuorumCert{
			Epoch:              6,
			AggregateSignature: make([]byte, 48),
			SignerBitmap:       []byte{0x0F},
		},
	}
	errSkip := ApplyCommitteeUpdate(registry, updateSkip, true)
	assert.ErrorIs(t, errSkip, ErrNonSequentialEpoch)

	// 3. Invalid Cert Reject
	updateBadCert := CommitteeUpdate{
		SourceChainID:   101,
		NewEpoch:        7,
		NewCommittee:    []ValidatorEntry{v2, v3},
		QuorumThreshold: 3334,
		StateRoot:       newStateRoot,
		Cert: QuorumCert{
			Epoch:              6,
			AggregateSignature: make([]byte, 48),
			SignerBitmap:       []byte{0x0F},
		},
	}
	errBadCert := ApplyCommitteeUpdate(registry, updateBadCert, false)
	assert.ErrorIs(t, errBadCert, ErrInvalidQuorumCert)
}

func TestEpochSync_P3_2_AccountMerkleTreeProofAndTamperGuard(t *testing.T) {
	a1 := AccountLeaf{
		Account: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		Balance: big.NewInt(500),
	}
	a2 := AccountLeaf{
		Account: common.HexToAddress("0x2222222222222222222222222222222222222222"),
		Balance: big.NewInt(1500),
	}
	a3 := AccountLeaf{
		Account: common.HexToAddress("0x3333333333333333333333333333333333333333"),
		Balance: big.NewInt(3000),
	}
	a4 := AccountLeaf{
		Account: common.HexToAddress("0x4444444444444444444444444444444444444444"),
		Balance: big.NewInt(5000),
	}

	accounts := []AccountLeaf{a1, a2, a3, a4}
	root, proofs, err := BuildAccountMerkleTree(accounts)
	require.NoError(t, err)

	// 1. Valid Proof Verification for all 4 accounts
	for i, acc := range accounts {
		assert.True(t, VerifyAccountMerkleProof(acc, proofs[i], root), "account %d proof must be valid", i)
	}

	// 2. Tamper Guard: Attacker tampers balance
	tamperedA1 := AccountLeaf{
		Account: a1.Account,
		Balance: big.NewInt(50000),
	}
	assert.False(t, VerifyAccountMerkleProof(tamperedA1, proofs[0], root), "tampered balance must fail")

	// 3. Tamper Guard: Attacker changes address
	tamperedAddr := AccountLeaf{
		Account: common.HexToAddress("0x9999999999999999999999999999999999999999"),
		Balance: big.NewInt(500),
	}
	assert.False(t, VerifyAccountMerkleProof(tamperedAddr, proofs[0], root), "tampered address must fail")
}
