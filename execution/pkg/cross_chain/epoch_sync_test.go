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

// TestComputeCommitteeUpdateDigest_OrderIndependent is Milestone C's core determinism guarantee:
// every validator must derive the exact same digest from the exact same logical committee, no
// matter what order they happened to enumerate its members in (e.g. GetAllValidators' own sort
// order could differ subtly from another node's), or their individual signatures could never
// aggregate into one valid QuorumCert.
func TestComputeCommitteeUpdateDigest_OrderIndependent(t *testing.T) {
	v1 := makeTestValidator(1000)
	v2 := makeTestValidator(2000)
	v3 := makeTestValidator(3000)
	stateRoot := common.HexToHash("0xABCDABCDABCDABCDABCDABCDABCDABCDABCDABCDABCDABCDABCDABCDABCDABCD")
	accountTreeRoot := common.HexToHash("0x1234123412341234123412341234123412341234123412341234123412341234")

	orderA := []ValidatorEntry{v1, v2, v3}
	orderB := []ValidatorEntry{v3, v1, v2}
	orderC := []ValidatorEntry{v2, v3, v1}

	digestA := ComputeCommitteeUpdateDigest(101, 5, orderA, stateRoot, accountTreeRoot)
	digestB := ComputeCommitteeUpdateDigest(101, 5, orderB, stateRoot, accountTreeRoot)
	digestC := ComputeCommitteeUpdateDigest(101, 5, orderC, stateRoot, accountTreeRoot)

	assert.Equal(t, digestA, digestB, "digest must not depend on input slice order")
	assert.Equal(t, digestA, digestC, "digest must not depend on input slice order")

	// Sanity: changing any input actually changes the digest (the function isn't just returning
	// a constant).
	assert.NotEqual(t, digestA, ComputeCommitteeUpdateDigest(102, 5, orderA, stateRoot, accountTreeRoot), "sourceChainID must affect the digest")
	assert.NotEqual(t, digestA, ComputeCommitteeUpdateDigest(101, 6, orderA, stateRoot, accountTreeRoot), "newEpoch must affect the digest")
	assert.NotEqual(t, digestA, ComputeCommitteeUpdateDigest(101, 5, orderA, common.Hash{}, accountTreeRoot), "stateRoot must affect the digest")
	assert.NotEqual(t, digestA, ComputeCommitteeUpdateDigest(101, 5, orderA, stateRoot, common.Hash{}), "accountTreeRoot must affect the digest")
	assert.NotEqual(t, digestA, ComputeCommitteeUpdateDigest(101, 5, []ValidatorEntry{v1, v2}, stateRoot, accountTreeRoot), "committee membership must affect the digest")

	// The original slice must not be mutated by sorting internally (callers may reuse it).
	assert.Equal(t, []ValidatorEntry{v1, v2, v3}, orderA, "ComputeCommitteeUpdateDigest must not mutate its input slice")
}

func TestBuildAccountSnapshot_DeterministicAndVerifiable(t *testing.T) {
	alice := AccountLeaf{Account: common.HexToAddress("0x1111111111111111111111111111111111111111"), Balance: big.NewInt(100)}
	bob := AccountLeaf{Account: common.HexToAddress("0x2222222222222222222222222222222222222222"), Balance: big.NewInt(200)}
	charlie := AccountLeaf{Account: common.HexToAddress("0x3333333333333333333333333333333333333333"), Balance: big.NewInt(300)}

	// Test order independence
	order1 := []AccountLeaf{alice, bob, charlie}
	order2 := []AccountLeaf{charlie, alice, bob}

	root1, proofMap1, err1 := BuildAccountSnapshot(order1)
	assert.NoError(t, err1)

	root2, proofMap2, err2 := BuildAccountSnapshot(order2)
	assert.NoError(t, err2)

	assert.Equal(t, root1, root2, "snapshot root must be canonically sorted")
	assert.Equal(t, proofMap1[alice.Account], proofMap2[alice.Account])

	// Verify proof directly
	assert.True(t, VerifyAccountMerkleProof(alice, proofMap1[alice.Account], root1))
	assert.True(t, VerifyAccountMerkleProof(bob, proofMap1[bob.Account], root1))
	assert.True(t, VerifyAccountMerkleProof(charlie, proofMap1[charlie.Account], root1))
}

func TestBuildAccountSnapshot_LargeScale(t *testing.T) {
	const count = 5000
	accounts := make([]AccountLeaf, count)
	for i := 0; i < count; i++ {
		var addrBytes [20]byte
		addrBytes[0] = byte(i >> 24)
		addrBytes[1] = byte(i >> 16)
		addrBytes[2] = byte(i >> 8)
		addrBytes[3] = byte(i)
		accounts[i] = AccountLeaf{
			Account: common.BytesToAddress(addrBytes[:]),
			Balance: big.NewInt(int64(i * 100)),
		}
	}

	root, proofMap, err := BuildAccountSnapshot(accounts)
	assert.NoError(t, err)
	assert.NotEqual(t, common.Hash{}, root)
	assert.Equal(t, count, len(proofMap))

	// Verify sample proofs across the tree
	for _, idx := range []int{0, 1, count / 2, count - 2, count - 1} {
		leaf := accounts[idx]
		proof, ok := proofMap[leaf.Account]
		assert.True(t, ok)
		assert.True(t, VerifyAccountMerkleProof(leaf, proof, root))
	}
}

func BenchmarkBuildAccountSnapshot_10kAccounts(b *testing.B) {
	const count = 10000
	accounts := make([]AccountLeaf, count)
	for i := 0; i < count; i++ {
		var addrBytes [20]byte
		addrBytes[0] = byte(i >> 24)
		addrBytes[1] = byte(i >> 16)
		addrBytes[2] = byte(i >> 8)
		addrBytes[3] = byte(i)
		accounts[i] = AccountLeaf{
			Account: common.BytesToAddress(addrBytes[:]),
			Balance: big.NewInt(int64(i * 100)),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = BuildAccountSnapshot(accounts)
	}
}
