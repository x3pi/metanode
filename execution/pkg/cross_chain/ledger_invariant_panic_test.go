package cross_chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A non-reserve chain's local SupplyLedger copy is credited by ClaimMessage without any matching genesis supply, so
// sum(PerChainAllocation) > GenesisTotalSupply is a normal state there. GrantAllocation / TransferAllocation used to
// panic on that state, and a panic in a barrier handler takes down every validator that executes the block. They must
// return an error and leave the ledger untouched instead.
func TestLedger_InvariantAlreadyBrokenReturnsErrorInsteadOfPanicking(t *testing.T) {
	for name, op := range map[string]func(*GlobalSupplyLedger) error{
		"GrantAllocation":    func(l *GlobalSupplyLedger) error { return l.GrantAllocation(7, big.NewInt(10)) },
		"TransferAllocation": func(l *GlobalSupplyLedger) error { return l.TransferAllocation(8, 7, big.NewInt(10)) },
	} {
		t.Run(name, func(t *testing.T) {
			l := &GlobalSupplyLedger{GenesisTotalSupply: big.NewInt(0), PerChainAllocation: map[uint64]*big.Int{8: big.NewInt(500)}}
			var err error
			require.NotPanics(t, func() { err = op(l) })
			assert.ErrorIs(t, err, ErrInvariantViolation)
			assert.Equal(t, 0, l.PerChainAllocation[8].Cmp(big.NewInt(500)), "a rejected operation must not change the ledger")
			assert.Nil(t, l.PerChainAllocation[7], "a rejected operation must not create an allocation")
			assert.Equal(t, 0, l.GenesisTotalSupply.Sign(), "a rejected operation must not change the genesis supply")
		})
	}
}

// Real route to that state: a commit issued by the reserve is attested without a ceiling debit, then claimed on a chain
// whose ledger copy has no genesis supply. The reserve's genesis-mint cert is public calldata, so replaying it on that
// non-reserve chain must be rejected, not crash the node (and not mint a local genesis supply).
func TestGateway_ClaimThenAllocateSupplyWithCert_DoesNotPanic(t *testing.T) {
	kp := bls.GenerateKeyPair()
	ledger, err := NewGlobalSupplyLedger(big.NewInt(0), map[uint64]*big.Int{})
	require.NoError(t, err)
	engine := NewGatewayEngine(102, map[uint64]ChainRegistry{
		101: {ChainID: 101, Epoch: 1, Committee: []ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000}}},
	}, ledger)
	engine.ReserveChainID = 101

	msg := CrossChainMessage{
		MessageID: common.HexToHash("0xC1A1"), SourceChainID: 101, DestChainID: 102, Sequence: 1, HopCount: 1,
		Sender: common.HexToAddress("0x1111"), Target: common.HexToAddress("0x2222"), AssetID: big.NewInt(0),
		Value: big.NewInt(500), Tip: big.NewInt(0), GasFee: big.NewInt(0),
	}
	root, layers, aggAmounts, aggIndex, err := BuildCommitTree([]CrossChainMessage{msg})
	require.NoError(t, err)
	sig := bls.Sign(kp.PrivateKey(), ComputeCommitRootAttestMessage(root))
	_, err = engine.AttestReserveIssuedCommit(101, root, aggAmounts["0"], big.NewInt(0), GetMerkleProof(layers, aggIndex["0"]),
		QuorumCert{Epoch: 1, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}})
	require.NoError(t, err)
	_, err = engine.ClaimMessage(msg, GetMerkleProof(layers, 0), root, common.HexToAddress("0x9999"), 0)
	require.NoError(t, err)
	require.Equal(t, 0, ledger.GetAllocation(102).Cmp(big.NewInt(500)), "claim credits the local allocation")

	amount := big.NewInt(1_000_000)
	mintSig := bls.Sign(kp.PrivateKey(), ComputeAllocateSupplyMessage(101, amount))
	cert := QuorumCert{Epoch: 1, AggregateSignature: mintSig.Bytes(), SignerBitmap: []byte{0x01}}
	require.NotPanics(t, func() { err = engine.AllocateSupplyWithCert(101, amount, cert) })
	assert.ErrorIs(t, err, ErrOnlyReserveMayMint, "the genesis mint must only run on the reserve chain itself")
	assert.Equal(t, 0, ledger.GenesisTotalSupply.Sign(), "a rejected mint must leave the genesis supply at 0")

	// and on the reserve chain itself an already-broken ledger is an error, not a panic
	engine.LocalChainID = 101
	require.NotPanics(t, func() { err = engine.AllocateSupplyWithCert(101, amount, cert) })
	assert.ErrorIs(t, err, ErrInvariantViolation)
	assert.Equal(t, 0, ledger.GenesisTotalSupply.Sign())
}

// A forfeited bond is credited to the reserve's allocation; that must not break sum(allocations) <= genesis, or every later
// allocation move on the reserve fails (and, before the panic->error fix, crashed the node).
func TestForfeitBond_KeepsLedgerInvariant(t *testing.T) {
	ledger, err := NewGlobalSupplyLedger(big.NewInt(10_000), map[uint64]*big.Int{101: big.NewInt(6000), 102: big.NewInt(4000)})
	require.NoError(t, err)
	engine := NewGatewayEngine(101, map[uint64]ChainRegistry{102: {ChainID: 102, Epoch: 1}}, ledger)
	engine.ReserveChainID = 101
	engine.ensureSecurityBond()
	engine.SecurityBond.Bond[102] = big.NewInt(500)
	engine.forfeitBond(102)

	assert.True(t, ledger.VerifyInvariant(), "forfeiting a bond must keep sum(allocations) <= genesis supply")
	assert.Equal(t, 0, ledger.GetAllocation(101).Cmp(big.NewInt(6500)))
	assert.Equal(t, 0, ledger.GenesisTotalSupply.Cmp(big.NewInt(10_500)))
	// and the reserve can still move allocation afterwards
	require.NoError(t, ledger.TransferAllocation(101, 102, big.NewInt(100)))
}
