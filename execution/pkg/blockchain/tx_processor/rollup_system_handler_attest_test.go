package tx_processor

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

func TestRollupSystemHandler_CoAttestationEnforcedIn4ValidatorCommittee(t *testing.T) {
	bls.Init()
	cs := setupTestChainState(t)

	// Create 4 validators in committee
	var vals []*bls.KeyPair
	for i := 0; i < 4; i++ {
		kp := bls.GenerateKeyPair()
		vals = append(vals, kp)

		node := state.NewAccountState(kp.Address())
		node.AddBalance(big.NewInt(1_000_000_000_000_000_000))
		node.SetPublicKeyBls(kp.PublicKey().Bytes())
		cs.GetAccountStateDB().SetState(node)

		addTestCommitteeValidator(t, cs, kp)
	}
	flushTestStake(t, cs)

	h := newSystemHandler()
	target := common.HexToAddress("0x0000000000000000000000000000000000009999")
	creditEvents := forgedCreditEvents(target)

	// 1. A single validator attempts to send an UN-ATTESTED system event in a 4-validator committee => REJECTED!
	rawUnattested, _ := rollup.MarshalRollupSystemPayload(&rollup.RollupSystemPayload{Event: creditEvents[0], MsgID: common.HexToHash("0x1111"), SourceSeq: 1})
	unattestedTx := transaction.NewTransaction(vals[0].Address(), rollup.RollupSystemAddress, big.NewInt(0), 21000, 1_000_000_000, 0, rawUnattested, nil,
		common.Hash{}, common.Hash{}, 0, 1).(*transaction.Transaction)
	rcp, _, err := h.HandleTransaction(context.Background(), cs, unattestedTx, rollup.RollupSystemAddress, false, 0)
	require.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcp.Status(), "unattested event must be rejected in multi-validator committee")
	assert.Contains(t, string(rcp.Return()), "co-attestation required")

	// 2. Co-attestation flow across the 3 legs of credit (CreditObserved, RPCSubmitted, ClaimedConfirmed)
	for legIdx, ev := range creditEvents {
		inner, _ := rollup.MarshalRollupSystemPayload(&rollup.RollupSystemPayload{Event: ev, MsgID: common.HexToHash("0x2222"), SourceSeq: 1})
		chainID := cs.GetConfig().ChainId.Uint64()
		digest := rollup.ComputeRollupSystemEventDigest(chainID, inner)

		// Helper to build attested tx
		makeAttestedTx := func(val *bls.KeyPair, nonce uint64) *transaction.Transaction {
			sig := bls.Sign(val.PrivateKey(), digest)
			attPayload := rollup.RollupSystemAttestedPayload{
				Kind:  rollup.PayloadKindRollupSystemAttested,
				Inner: inner,
				Attestations: []rollup.RegistrationAttestation{
					{
						ValidatorPubkey: val.PublicKey(),
						Signature:       sig,
					},
				},
			}
			raw, _ := rollup.MarshalRollupSystemAttestedPayload(&attPayload)
			return transaction.NewTransaction(val.Address(), rollup.RollupSystemAddress, big.NewInt(0), 21000, 1_000_000_000, 0, raw, nil,
				common.Hash{}, common.Hash{}, nonce, 1).(*transaction.Transaction)
		}

		// (a) Validator 0 submits its attestation (1 signature < 2 required => PENDING)
		tx0 := makeAttestedTx(vals[0], uint64(legIdx*2+1))
		rcp0, _, err := h.HandleTransaction(context.Background(), cs, tx0, rollup.RollupSystemAddress, false, 0)
		require.NoError(t, err)
		assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcp0.Status(), "single attestation tx must succeed and consume nonce")

		// If this is the 3rd leg (ClaimedConfirmed), verify that Target is NOT credited yet!
		if ev.Type == rollup.EventClaimedConfirmed {
			targetAcc, _ := cs.GetAccountStateDB().AccountState(target)
			bal := big.NewInt(0)
			if targetAcc != nil {
				bal = targetAcc.Balance()
			}
			assert.Equal(t, int64(0), bal.Int64(), "single validator must NOT be able to mint or credit balance")
		}

		// (b) Validator 1 submits its attestation (2 signatures >= 2 required => DISPATCHED!)
		tx1 := makeAttestedTx(vals[1], uint64(legIdx*2))
		rcp1, _, err := h.HandleTransaction(context.Background(), cs, tx1, rollup.RollupSystemAddress, false, 0)
		require.NoError(t, err)
		assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcp1.Status())

		// (c) Validator 2 submits late => tombstone hit, no-op success
		tx2 := makeAttestedTx(vals[2], uint64(legIdx*2))
		rcp2, _, err := h.HandleTransaction(context.Background(), cs, tx2, rollup.RollupSystemAddress, false, 0)
		require.NoError(t, err)
		assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcp2.Status())
	}

	// Final verification: after all 3 legs were co-attested by 2 validators, target is credited!
	got, err := cs.GetAccountStateDB().AccountState(target)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, forgedMint.String(), got.Balance().String(), "target balance must be credited exactly once after co-attestation")
}

type failingCommittee struct{}

func (failingCommittee) GetActiveCommitteeBLSKeys() ([]cm.PublicKey, error) {
	return nil, errors.New("committee unreadable")
}

// If the committee cannot be read (stake DB error) an unattested system event must be rejected, never applied:
// failing open would let a single Byzantine validator bypass co-attestation whenever the read fails, and replicas
// whose read failed would disagree with those whose read succeeded.
func TestRollupSystemHandler_UnattestedEventRejectedWhenCommitteeUnreadable(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	called := false
	h := &RollupSystemHandler{
		dispatcher:   systemDispatcher(func(rollup.Store, AccountStateAccessor, []byte) error { called = true; return nil }),
		committeeFor: func(*blockchain.ChainState) rollup.CommitteeProvider { return failingCommittee{} },
	}
	nodeKP := bls.GenerateKeyPair()
	ns := state.NewAccountState(nodeKP.Address())
	ns.AddBalance(big.NewInt(1_000_000_000_000_000))
	ns.SetPublicKeyBls(nodeKP.PublicKey().Bytes())
	cs.GetAccountStateDB().SetState(ns)
	rawEvent, _ := rollup.MarshalRollupSystemPayload(&rollup.RollupSystemPayload{Event: rollup.Event{Type: rollup.EventCreditObserved}})
	tx := transaction.NewTransaction(nodeKP.Address(), rollup.RollupSystemAddress, big.NewInt(0), 21000, 1_000_000_000, 0,
		rawEvent, nil, common.Hash{}, common.Hash{}, 0, 1)
	rcp, _, err := h.HandleTransaction(context.Background(), cs, tx, rollup.RollupSystemAddress, false, 0)
	require.NoError(t, err)
	require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcp.Status())
	require.False(t, called, "the inner event must not be dispatched")
}
