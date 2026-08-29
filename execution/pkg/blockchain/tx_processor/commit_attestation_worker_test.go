package tx_processor

import (
	"context"
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain/rootanchor"
)

func TestCommitAttestationWorker_SingleValidatorLifecycle(t *testing.T) {
	rootAnchorCS, _, _, _ := newPersistentTestChainState(t)

	const localChainID = 888
	const epoch = 1

	kp := bls.GenerateKeyPair()
	entry := cross_chain.ValidatorEntry{PubkeyBLS: kp.PublicKey().Bytes(), Stake: 1000}

	// commitRoot is the eventual AttestCommit call's own AggregateValueLeaf hash (proof = no
	// siblings, Section 2.3.1) — the worker/bulletin-board flow below only needs it to be a stable
	// identifier throughout, not any particular bit pattern.
	commitRoot := cross_chain.HashAggregateValueLeaf(cross_chain.AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: big.NewInt(100)})

	raEngine, err := loadGatewayEngine(rootAnchorCS)
	require.NoError(t, err)
	raEngine.ChainRegistry[localChainID] = cross_chain.ChainRegistry{
		ChainID:         localChainID,
		Committee:       []cross_chain.ValidatorEntry{entry},
		Epoch:           epoch,
		QuorumThreshold: 6667,
	}
	require.NoError(t, saveGatewayEngine(rootAnchorCS, raEngine))

	rootAnchorChainID := big.NewInt(9099)
	srv := newRootAnchorTestServer(t, rootAnchorCS, rootAnchorChainID)
	defer srv.Close()

	client, err := rootanchor.NewClient([]string{srv.URL}, nil)
	require.NoError(t, err)

	submitterKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	submitterHex := hex.EncodeToString(crypto.FromECDSA(submitterKey))

	validatorAddr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	worker := NewCommitAttestationWorker(
		nil, client, localChainID, validatorAddr,
		hex.EncodeToString(kp.BytesPrivateKey()),
		submitterHex,
	)
	worker.SetPollConfig(10*time.Millisecond, 20)

	// Step 1: Submit share
	err = worker.SubmitMyShare(context.Background(), localChainID, epoch, commitRoot)
	require.NoError(t, err)

	// Step 2: Poll and aggregate into QuorumCert
	cert, err := worker.PollAndAggregate(context.Background(), localChainID, epoch, commitRoot)
	require.NoError(t, err)
	require.NotNil(t, cert)
	assert.Equal(t, uint64(epoch), cert.Epoch)
	assert.Equal(t, []byte{0x01}, []byte(cert.SignerBitmap))

	// Step 3: Verify produced QuorumCert against GatewayEngine.AttestCommit
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10_000), map[uint64]*big.Int{localChainID: big.NewInt(10_000)})
	require.NoError(t, err)
	destEngine := cross_chain.NewGatewayEngine(999, map[uint64]cross_chain.ChainRegistry{
		localChainID: {
			ChainID:   localChainID,
			Committee: []cross_chain.ValidatorEntry{entry},
			Epoch:     epoch,
		},
	}, ledger)
	destEngine.ReserveChainID = 999 // C8 fix: destEngine plays Reserve, attesting chain 888's commit

	attested, err := destEngine.AttestCommit(localChainID, commitRoot, big.NewInt(100), big.NewInt(0), cross_chain.MerkleProof{}, *cert)
	require.NoError(t, err)
	require.NotNil(t, attested)
	assert.Equal(t, big.NewInt(100), attested.FundedAmount)
}

func TestCommitAttestationWorker_MultiValidatorQuorum(t *testing.T) {
	rootAnchorCS, _, _, _ := newPersistentTestChainState(t)

	const localChainID = 999
	const epoch = 5

	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()
	kp3 := bls.GenerateKeyPair()

	// 3 validators: 40 + 40 + 20 = 100 stake. Quorum (2/3) is 67.
	v1 := cross_chain.ValidatorEntry{PubkeyBLS: kp1.PublicKey().Bytes(), Stake: 40}
	v2 := cross_chain.ValidatorEntry{PubkeyBLS: kp2.PublicKey().Bytes(), Stake: 40}
	v3 := cross_chain.ValidatorEntry{PubkeyBLS: kp3.PublicKey().Bytes(), Stake: 20}

	commitRoot := cross_chain.HashAggregateValueLeaf(cross_chain.AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: big.NewInt(500)})

	raEngine, err := loadGatewayEngine(rootAnchorCS)
	require.NoError(t, err)
	raEngine.ChainRegistry[localChainID] = cross_chain.ChainRegistry{
		ChainID:         localChainID,
		Committee:       []cross_chain.ValidatorEntry{v1, v2, v3},
		Epoch:           epoch,
		QuorumThreshold: 6667,
	}
	require.NoError(t, saveGatewayEngine(rootAnchorCS, raEngine))

	srv := newRootAnchorTestServer(t, rootAnchorCS, big.NewInt(9099))
	defer srv.Close()

	client, err := rootanchor.NewClient([]string{srv.URL}, nil)
	require.NoError(t, err)

	submitterKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	submitterHex := hex.EncodeToString(crypto.FromECDSA(submitterKey))

	worker1 := NewCommitAttestationWorker(
		nil, client, localChainID, common.HexToAddress("0x1111"),
		hex.EncodeToString(kp1.BytesPrivateKey()),
		submitterHex,
	)
	worker1.SetPollConfig(10*time.Millisecond, 5)

	worker2 := NewCommitAttestationWorker(
		nil, client, localChainID, common.HexToAddress("0x2222"),
		hex.EncodeToString(kp2.BytesPrivateKey()),
		submitterHex,
	)
	worker2.SetPollConfig(10*time.Millisecond, 20)

	// Step 1: Validator 1 submits share (40 stake). Threshold is 67.
	err = worker1.SubmitMyShare(context.Background(), localChainID, epoch, commitRoot)
	require.NoError(t, err)

	// Worker 1 tries to aggregate -> should timeout / fail because stake 40 < 67
	_, err = worker1.PollAndAggregate(context.Background(), localChainID, epoch, commitRoot)
	require.Error(t, err, "Expected quorum not reached with only validator 1 (40/100)")

	// Step 2: Validator 2 submits share (40 stake). Total stake = 80 >= 67.
	err = worker2.SubmitMyShare(context.Background(), localChainID, epoch, commitRoot)
	require.NoError(t, err)

	// Worker 2 aggregates -> quorum reached!
	cert, err := worker2.PollAndAggregate(context.Background(), localChainID, epoch, commitRoot)
	require.NoError(t, err)
	require.NotNil(t, cert)
	assert.Equal(t, uint64(epoch), cert.Epoch)
	// Bits 0 and 1 must be set: 1 | 2 = 3 (0x03)
	assert.Equal(t, []byte{0x03}, []byte(cert.SignerBitmap))

	// Step 3: Destination engine verifies real multi-sig QuorumCert
	ledger2, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10_000), map[uint64]*big.Int{localChainID: big.NewInt(10_000)})
	require.NoError(t, err)
	destEngine := cross_chain.NewGatewayEngine(1000, map[uint64]cross_chain.ChainRegistry{
		localChainID: {
			ChainID:   localChainID,
			Committee: []cross_chain.ValidatorEntry{v1, v2, v3},
			Epoch:     epoch,
		},
	}, ledger2)
	destEngine.ReserveChainID = 1000 // C8 fix: destEngine plays Reserve, attesting localChainID's commit

	attested, err := destEngine.AttestCommit(localChainID, commitRoot, big.NewInt(500), big.NewInt(0), cross_chain.MerkleProof{}, *cert)
	require.NoError(t, err)
	require.NotNil(t, attested)
	assert.Equal(t, big.NewInt(500), attested.FundedAmount)
}

func TestCommitAttestationWorker_NegativeCases(t *testing.T) {
	rootAnchorCS, _, _, _ := newPersistentTestChainState(t)

	const localChainID = 555
	const epoch = 2

	kpValid := bls.GenerateKeyPair()
	kpRogue := bls.GenerateKeyPair()

	validEntry := cross_chain.ValidatorEntry{PubkeyBLS: kpValid.PublicKey().Bytes(), Stake: 100}

	commitRoot := common.HexToHash("0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC")

	raEngine, err := loadGatewayEngine(rootAnchorCS)
	require.NoError(t, err)
	raEngine.ChainRegistry[localChainID] = cross_chain.ChainRegistry{
		ChainID:         localChainID,
		Committee:       []cross_chain.ValidatorEntry{validEntry},
		Epoch:           epoch,
		QuorumThreshold: 6667,
	}
	require.NoError(t, saveGatewayEngine(rootAnchorCS, raEngine))

	srv := newRootAnchorTestServer(t, rootAnchorCS, big.NewInt(9099))
	defer srv.Close()

	client, err := rootanchor.NewClient([]string{srv.URL}, nil)
	require.NoError(t, err)

	submitterKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	submitterHex := hex.EncodeToString(crypto.FromECDSA(submitterKey))

	// Attack 1: Non-member (kpRogue) attempts to submit a share -> REJECTED
	rogueWorker := NewCommitAttestationWorker(
		nil, client, localChainID, common.HexToAddress("0x9999"),
		hex.EncodeToString(kpRogue.BytesPrivateKey()),
		submitterHex,
	)
	err = rogueWorker.SubmitMyShare(context.Background(), localChainID, epoch, commitRoot)
	assert.Error(t, err, "Non-member share submission must be rejected")

	// Attack 2: Valid member submits for wrong epoch (epoch+1) -> REJECTED
	validWorker := NewCommitAttestationWorker(
		nil, client, localChainID, common.HexToAddress("0x1111"),
		hex.EncodeToString(kpValid.BytesPrivateKey()),
		submitterHex,
	)
	err = validWorker.SubmitMyShare(context.Background(), localChainID, epoch+1, commitRoot)
	assert.Error(t, err, "Epoch mismatch share submission must be rejected")

	// Valid 1: Valid member submits for correct epoch -> ACCEPTED
	err = validWorker.SubmitMyShare(context.Background(), localChainID, epoch, commitRoot)
	require.NoError(t, err)

	// Attack 3: Same member tries to submit duplicate share -> REJECTED
	err = validWorker.SubmitMyShare(context.Background(), localChainID, epoch, commitRoot)
	assert.Error(t, err, "Duplicate share submission must be rejected")
}
