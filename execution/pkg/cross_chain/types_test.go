package cross_chain

import (
	"encoding/json"
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTypes_RoundTripJSON(t *testing.T) {
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	hash := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	// 1. ValidatorEntry
	val := ValidatorEntry{
		PubkeyBLS:    []byte{1, 2, 3, 4},
		Stake:        1000,
		PopSignature: []byte{5, 6, 7, 8},
	}
	valData, err := json.Marshal(val)
	require.NoError(t, err)
	var valDeser ValidatorEntry
	require.NoError(t, json.Unmarshal(valData, &valDeser))
	assert.Equal(t, val, valDeser)

	// 2. ChainRegistry
	reg := ChainRegistry{
		ChainID:          101,
		Committee:        []ValidatorEntry{val},
		Epoch:            5,
		QuorumThreshold:  667,
		GatewayContract:  addr,
		StateRoot:        hash,
		ArchivalEndpoint: "https://archive.metanode.network",
		RegisteredAt:     1700000000,
	}
	regData, err := json.Marshal(reg)
	require.NoError(t, err)
	var regDeser ChainRegistry
	require.NoError(t, json.Unmarshal(regData, &regDeser))
	assert.Equal(t, reg, regDeser)

	// 3. CrossChainMessage
	msg := CrossChainMessage{
		MessageID:     hash,
		SourceChainID: 101,
		DestChainID:   102,
		Sequence:      42,
		HopCount:      1,
		Sender:        addr,
		Target:        addr,
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(1000000),
		Payload:       []byte{0xaa, 0xbb, 0xcc},
		Tip:           big.NewInt(100),
		Ordered:       false,
	}
	msgData, err := json.Marshal(msg)
	require.NoError(t, err)
	var msgDeser CrossChainMessage
	require.NoError(t, json.Unmarshal(msgData, &msgDeser))
	assert.Equal(t, msg.MessageID, msgDeser.MessageID)
	assert.Equal(t, msg.SourceChainID, msgDeser.SourceChainID)
	assert.Equal(t, msg.DestChainID, msgDeser.DestChainID)
	assert.Equal(t, msg.Sequence, msgDeser.Sequence)
	assert.Equal(t, msg.HopCount, msgDeser.HopCount)
	assert.Equal(t, msg.Sender, msgDeser.Sender)
	assert.Equal(t, msg.Target, msgDeser.Target)
	assert.Equal(t, msg.AssetID.String(), msgDeser.AssetID.String())
	assert.Equal(t, msg.Value.String(), msgDeser.Value.String())
	assert.Equal(t, msg.Payload, msgDeser.Payload)
	assert.Equal(t, msg.Tip.String(), msgDeser.Tip.String())
	assert.Equal(t, msg.Ordered, msgDeser.Ordered)

	// 4. QuorumCert
	cert := QuorumCert{
		Epoch:              5,
		AggregateSignature: []byte{0x12, 0x34},
		SignerBitmap:       []byte{0xff, 0x01},
	}
	certData, err := json.Marshal(cert)
	require.NoError(t, err)
	var certDeser QuorumCert
	require.NoError(t, json.Unmarshal(certData, &certDeser))
	assert.Equal(t, cert, certDeser)

	// 5. MerkleProof
	proof := MerkleProof{
		LeafIndex: 3,
		Siblings:  []common.Hash{hash, hash},
	}
	proofData, err := json.Marshal(proof)
	require.NoError(t, err)
	var proofDeser MerkleProof
	require.NoError(t, json.Unmarshal(proofData, &proofDeser))
	assert.Equal(t, proof, proofDeser)

	// 6. AssetEntry
	asset := AssetEntry{
		AssetID:           big.NewInt(1),
		HomeChainID:       101,
		CanonicalContract: addr,
		WrappedContracts:  map[uint64]common.Address{102: addr},
		Active:            true,
	}
	assetData, err := json.Marshal(asset)
	require.NoError(t, err)
	var assetDeser AssetEntry
	require.NoError(t, json.Unmarshal(assetData, &assetDeser))
	assert.Equal(t, asset.AssetID.String(), assetDeser.AssetID.String())
	assert.Equal(t, asset.HomeChainID, assetDeser.HomeChainID)
	assert.Equal(t, asset.CanonicalContract, assetDeser.CanonicalContract)
	assert.Equal(t, asset.WrappedContracts, assetDeser.WrappedContracts)
	assert.Equal(t, asset.Active, assetDeser.Active)

	// 7. Channel
	channel := Channel{
		SourceChainID:         101,
		DestChainID:           102,
		Ordered:               true,
		NextSequence:          10,
		LastProcessedSequence: 9,
		StatusByMessageID: map[common.Hash]MessageStatus{
			hash: MessageStatusSuccess,
		},
	}
	channelData, err := json.Marshal(channel)
	require.NoError(t, err)
	var channelDeser Channel
	require.NoError(t, json.Unmarshal(channelData, &channelDeser))
	assert.Equal(t, channel, channelDeser)

	// 8. AttestedCommit
	commit := AttestedCommit{
		SourceChainID: 101,
		CommitRoot:    hash,
		Epoch:         5,
		FundedAmount:  big.NewInt(5000),
		ClaimedAmount: big.NewInt(2000),
	}
	commitData, err := json.Marshal(commit)
	require.NoError(t, err)
	var commitDeser AttestedCommit
	require.NoError(t, json.Unmarshal(commitData, &commitDeser))
	assert.Equal(t, commit.SourceChainID, commitDeser.SourceChainID)
	assert.Equal(t, commit.CommitRoot, commitDeser.CommitRoot)
	assert.Equal(t, commit.Epoch, commitDeser.Epoch)
	assert.Equal(t, commit.FundedAmount.String(), commitDeser.FundedAmount.String())
	assert.Equal(t, commit.ClaimedAmount.String(), commitDeser.ClaimedAmount.String())

	// 9. GovernanceProposal was removed 2026-09-04 along with the whole GovernanceEngine
	// propose/vote/execute machinery -- see UpdateCommitteePayload's doc comment in types.go.

	// 10. AccountLeaf
	leaf := AccountLeaf{
		Account: addr,
		Balance: big.NewInt(100000),
	}
	leafData, err := json.Marshal(leaf)
	require.NoError(t, err)
	var leafDeser AccountLeaf
	require.NoError(t, json.Unmarshal(leafData, &leafDeser))
	assert.Equal(t, leaf.Account, leafDeser.Account)
	assert.Equal(t, leaf.Balance.String(), leafDeser.Balance.String())
}

func TestGlobalSupplyLedger_BasicOperations(t *testing.T) {
	totalSupply := big.NewInt(1000000000)
	allocations := map[uint64]*big.Int{
		0: big.NewInt(700000000), // Reserve
		1: big.NewInt(200000000), // Chain 1
		2: big.NewInt(100000000), // Chain 2
	}

	ledger, err := NewGlobalSupplyLedger(totalSupply, allocations)
	require.NoError(t, err)
	assert.True(t, ledger.VerifyInvariant())

	// Valid transfer from chain 1 to chain 2
	err = ledger.TransferAllocation(1, 2, big.NewInt(50000000))
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(150000000), ledger.GetAllocation(1))
	assert.Equal(t, big.NewInt(150000000), ledger.GetAllocation(2))
	assert.True(t, ledger.VerifyInvariant())

	// Over-allocation attempt: try to transfer 200M when only 150M available
	err = ledger.TransferAllocation(1, 2, big.NewInt(200000000))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInsufficientAllocation)
	assert.True(t, ledger.VerifyInvariant())

	// Same chain transfer
	err = ledger.TransferAllocation(1, 1, big.NewInt(1000))
	require.ErrorIs(t, err, ErrSameChainTransfer)
}

func TestGlobalSupplyLedger_FuzzInvariant(t *testing.T) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	totalSupply := big.NewInt(10000000000)
	numChains := uint64(5)

	initialAllocs := map[uint64]*big.Int{
		0: big.NewInt(6000000000),
		1: big.NewInt(1000000000),
		2: big.NewInt(1000000000),
		3: big.NewInt(1000000000),
		4: big.NewInt(1000000000),
	}

	ledger, err := NewGlobalSupplyLedger(totalSupply, initialAllocs)
	require.NoError(t, err)
	assert.True(t, ledger.VerifyInvariant())

	// Run 10,000 randomized operations
	for i := 0; i < 10000; i++ {
		fromChain := uint64(r.Intn(int(numChains)))
		toChain := uint64(r.Intn(int(numChains)))

		fromAlloc := ledger.GetAllocation(fromChain)

		var transferAmount *big.Int
		if r.Float32() < 0.7 && fromAlloc.Sign() > 0 {
			// Random amount up to available balance
			transferAmount = big.NewInt(r.Int63n(fromAlloc.Int64()) + 1)
		} else {
			// Potential over-allocation attempt
			transferAmount = new(big.Int).Add(fromAlloc, big.NewInt(int64(r.Intn(100000)+1)))
		}

		transferErr := ledger.TransferAllocation(fromChain, toChain, transferAmount)

		if fromChain == toChain {
			assert.ErrorIs(t, transferErr, ErrSameChainTransfer)
		} else if transferAmount.Cmp(fromAlloc) > 0 {
			assert.ErrorIs(t, transferErr, ErrInsufficientAllocation)
		} else {
			assert.NoError(t, transferErr)
		}

		// CRITICAL INVARIANT: Total supply MUST remain identical under all outcomes
		require.True(t, ledger.VerifyInvariant(), "Supply ledger invariant violated at step %d", i)
		assert.Equal(t, 0, ledger.SumAllocations().Cmp(totalSupply))
	}
}

// TestEncodeDecodeRelayPayload is the regression test for the 2-hop A -> Reserve -> B value &
// CONTRACT_CALL routing marker (2026-08-28/2026-08-29, note/cross_chain_stake_and_value_flow.md).
func TestEncodeDecodeRelayPayload(t *testing.T) {
	t.Run("round trip, no inner payload (plain value relay)", func(t *testing.T) {
		for _, chainID := range []uint64{1, 101, 999, 18446744073709551615} {
			payload := EncodeRelayPayload(chainID, nil)
			gotChainID, gotInner, ok := DecodeRelayPayload(payload)
			require.True(t, ok)
			assert.Equal(t, chainID, gotChainID)
			assert.Nil(t, gotInner, "no inner payload was encoded, decode must not synthesize one")
		}
	})

	t.Run("round trip with a real inner payload (CONTRACT_CALL relay)", func(t *testing.T) {
		inner := []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01, 0x02, 0x03, 0x04}
		payload := EncodeRelayPayload(103, inner)
		gotChainID, gotInner, ok := DecodeRelayPayload(payload)
		require.True(t, ok)
		assert.Equal(t, uint64(103), gotChainID)
		assert.Equal(t, inner, gotInner)
	})

	t.Run("empty payload (every pre-existing message) is not a relay marker", func(t *testing.T) {
		_, _, ok := DecodeRelayPayload(nil)
		assert.False(t, ok)
		_, _, ok = DecodeRelayPayload([]byte{})
		assert.False(t, ok)
	})

	t.Run("arbitrary contract-call payload is not mistaken for a relay marker", func(t *testing.T) {
		_, _, ok := DecodeRelayPayload([]byte{0xde, 0xad, 0xbe, 0xef})
		assert.False(t, ok)
		// Same length as a real header-only relay payload, but wrong prefix -- must not
		// false-positive.
		wrongPrefix := make([]byte, len(EncodeRelayPayload(1, nil)))
		copy(wrongPrefix, "NOTRELAY01:")
		_, _, ok = DecodeRelayPayload(wrongPrefix)
		assert.False(t, ok)
	})

	t.Run("truncated marker (shorter than the fixed header) is rejected, not misread", func(t *testing.T) {
		full := EncodeRelayPayload(12345, nil)
		_, _, ok := DecodeRelayPayload(full[:len(full)-1])
		assert.False(t, ok)
	})
}
