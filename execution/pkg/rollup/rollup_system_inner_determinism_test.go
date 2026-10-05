package rollup

import (
	"bytes"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/stretchr/testify/require"
)

// TestRollupSystemInnerDeterminism_ReceiveWorker verifies that two independent ReceiveWorker instances
// observing the exact same Parent Chain TransferEvent produce byte-for-byte identical innerData JSON,
// even if one worker's local store already contains a cached/stale record while the other does not.
func TestRollupSystemInnerDeterminism_ReceiveWorker(t *testing.T) {
	chainID := uint64(991)
	senderAddr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	targetAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	amount := big.NewInt(5_000_000_000)
	msgID := common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	sourceKey := cm.PublicKey{1, 2, 3}
	destKey := cm.PublicKey{4, 5, 6}
	payloadHash := common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	eventFromParent := &parentchain.TransferEvent{
		MsgID:        msgID,
		Sender:       senderAddr,
		Target:       targetAddr,
		Amount:       amount,
		SourceSeq:   42,
		SourcePubKey: sourceKey,
		DestPubKey:   destKey,
		PayloadHash:  payloadHash,
	}

	// Worker 1: fresh empty store
	scDB1 := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store1 := NewDBStore(scDB1)
	stateDB1 := newMockAccountStateDB()
	client1 := &mockParentChainClient{}
	worker1 := NewReceiveWorker(store1, stateDB1, client1, bls.GenerateKeyPair())
	var payload1 RollupSystemPayload
	worker1.EventProposer = func(event Event, mID common.Hash, seq uint64, sKey cm.PublicKey, dKey cm.PublicKey, pHash common.Hash) error {
		payload1 = RollupSystemPayload{
			Event:        event,
			MsgID:        mID,
			SourceSeq:    seq,
			SourcePubKey: sKey,
			DestPubKey:   dKey,
			PayloadHash:  pHash,
		}
		return nil
	}

	// Worker 2: store already has a previous record for this msgID (simulating local cache/restart)
	scDB2 := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store2 := NewDBStore(scDB2)
	require.NoError(t, store2.Put(&MessageRecord{
		MessageID: msgID,
		Role:      RoleReceiver,
		State:     StateMarkedClaimedPendingCredit,
	}))
	stateDB2 := newMockAccountStateDB()
	client2 := &mockParentChainClient{}
	worker2 := NewReceiveWorker(store2, stateDB2, client2, bls.GenerateKeyPair())
	var payload2 RollupSystemPayload
	worker2.EventProposer = func(event Event, mID common.Hash, seq uint64, sKey cm.PublicKey, dKey cm.PublicKey, pHash common.Hash) error {
		payload2 = RollupSystemPayload{
			Event:        event,
			MsgID:        mID,
			SourceSeq:    seq,
			SourcePubKey: sKey,
			DestPubKey:   dKey,
			PayloadHash:  pHash,
		}
		return nil
	}

	// Both workers process the incoming parent transfer
	worker1.handleIncomingTransfer(eventFromParent)
	worker2.handleIncomingTransfer(eventFromParent)

	inner1, err1 := json.Marshal(payload1)
	require.NoError(t, err1)
	inner2, err2 := json.Marshal(payload2)
	require.NoError(t, err2)

	// Invariant: innerData must be byte-for-byte identical
	require.True(t, bytes.Equal(inner1, inner2), "innerData must be byte-for-byte identical:\nworker1: %s\nworker2: %s", string(inner1), string(inner2))

	// Invariant: attestation digests must match identically
	digest1 := ComputeRollupSystemEventDigest(chainID, inner1)
	digest2 := ComputeRollupSystemEventDigest(chainID, inner2)
	require.True(t, bytes.Equal(digest1, digest2), "attestation digests must match: %x vs %x", digest1, digest2)
}

// TestRollupSystemInnerDeterminism_ReclaimWorker verifies that two independent ReclaimWorker instances
// checking reclaim eligibility at different wall clock times produce byte-for-byte identical innerData.
func TestRollupSystemInnerDeterminism_ReclaimWorker(t *testing.T) {
	chainID := uint64(991)
	msgID := common.HexToHash("0x1234567890123456789012345678901234567890123456789012345678901234")
	senderAddr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	targetAddr := common.HexToAddress("0x4444444444444444444444444444444444444444")
	val := big.NewInt(10_000_000)
	sourceKey := cm.PublicKey{10, 20}
	destKey := cm.PublicKey{30, 40}
	payloadHash := common.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999")

	clientMock := &mockParentChainClient{
		claimedOutcome: parentchain.FloatOutcomeNone,
	}

	record := &MessageRecord{
		MessageID:    msgID,
		Role:         RoleSender,
		State:        StateSentConfirmed,
		Sender:       senderAddr,
		Target:       targetAddr,
		Value:        val,
		SourceSeq:    101,
		SourcePubKey: sourceKey,
		DestPubKey:   destKey,
		PayloadHash:  payloadHash,
	}

	scDB1 := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store1 := NewDBStore(scDB1)
	require.NoError(t, store1.Put(record))
	worker1 := NewReclaimWorker(store1, newMockAccountStateDB(), clientMock, bls.GenerateKeyPair())
	var payload1 RollupSystemPayload
	worker1.EventProposer = func(event Event, mID common.Hash, seq uint64, sKey cm.PublicKey, dKey cm.PublicKey, pHash common.Hash) error {
		payload1 = RollupSystemPayload{
			Event:        event,
			MsgID:        mID,
			SourceSeq:    seq,
			SourcePubKey: sKey,
			DestPubKey:   dKey,
			PayloadHash:  pHash,
		}
		return nil
	}

	scDB2 := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store2 := NewDBStore(scDB2)
	require.NoError(t, store2.Put(record))
	worker2 := NewReclaimWorker(store2, newMockAccountStateDB(), clientMock, bls.GenerateKeyPair())
	var payload2 RollupSystemPayload
	worker2.EventProposer = func(event Event, mID common.Hash, seq uint64, sKey cm.PublicKey, dKey cm.PublicKey, pHash common.Hash) error {
		payload2 = RollupSystemPayload{
			Event:        event,
			MsgID:        mID,
			SourceSeq:    seq,
			SourcePubKey: sKey,
			DestPubKey:   dKey,
			PayloadHash:  pHash,
		}
		return nil
	}

	// Trigger worker 1
	worker1.checkAndReclaim(record)
	// Simulate delay between worker ticks on different validator nodes
	time.Sleep(10 * time.Millisecond)
	// Trigger worker 2
	worker2.checkAndReclaim(record)

	inner1, err1 := json.Marshal(payload1)
	require.NoError(t, err1)
	inner2, err2 := json.Marshal(payload2)
	require.NoError(t, err2)

	require.True(t, bytes.Equal(inner1, inner2), "innerData must be byte-for-byte identical:\nworker1: %s\nworker2: %s", string(inner1), string(inner2))

	digest1 := ComputeRollupSystemEventDigest(chainID, inner1)
	digest2 := ComputeRollupSystemEventDigest(chainID, inner2)
	require.True(t, bytes.Equal(digest1, digest2), "attestation digests must match: %x vs %x", digest1, digest2)
}

// TestRollupSystemInnerDeterminism_SendWorker verifies that two independent SendWorker instances
// processing the same record produce byte-for-byte identical innerData and attestation digests.
func TestRollupSystemInnerDeterminism_SendWorker(t *testing.T) {
	chainID := uint64(991)
	msgID := common.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555")
	senderAddr := common.HexToAddress("0x7777777777777777777777777777777777777777")
	targetAddr := common.HexToAddress("0x8888888888888888888888888888888888888888")
	val := big.NewInt(25_000_000)
	sourceKey := cm.PublicKey{1, 3, 5}
	destKey := cm.PublicKey{2, 4, 6}
	payloadHash := common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")

	record := &MessageRecord{
		MessageID:    msgID,
		Role:         RoleSender,
		State:        StateLocalAppliedPendingSend,
		Sender:       senderAddr,
		Target:       targetAddr,
		Value:        val,
		SourceSeq:    55,
		SourcePubKey: sourceKey,
		DestPubKey:   destKey,
		PayloadHash:  payloadHash,
	}

	scDB1 := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store1 := NewDBStore(scDB1)
	require.NoError(t, store1.Put(record))
	client1 := &mockParentChainClient{}
	worker1 := NewSendWorker(store1, client1, bls.GenerateKeyPair(), destKey, 2)
	var payload1 RollupSystemPayload
	worker1.EventProposer = func(event Event, mID common.Hash, seq uint64, sKey cm.PublicKey, dKey cm.PublicKey, pHash common.Hash) error {
		payload1 = RollupSystemPayload{
			Event:        event,
			MsgID:        mID,
			SourceSeq:    seq,
			SourcePubKey: sKey,
			DestPubKey:   dKey,
			PayloadHash:  pHash,
		}
		return nil
	}

	scDB2 := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store2 := NewDBStore(scDB2)
	require.NoError(t, store2.Put(record))
	client2 := &mockParentChainClient{}
	worker2 := NewSendWorker(store2, client2, bls.GenerateKeyPair(), destKey, 2)
	var payload2 RollupSystemPayload
	worker2.EventProposer = func(event Event, mID common.Hash, seq uint64, sKey cm.PublicKey, dKey cm.PublicKey, pHash common.Hash) error {
		payload2 = RollupSystemPayload{
			Event:        event,
			MsgID:        mID,
			SourceSeq:    seq,
			SourcePubKey: sKey,
			DestPubKey:   dKey,
			PayloadHash:  pHash,
		}
		return nil
	}

	worker1.processPending()
	worker2.processPending()

	inner1, err1 := json.Marshal(payload1)
	require.NoError(t, err1)
	inner2, err2 := json.Marshal(payload2)
	require.NoError(t, err2)

	require.True(t, bytes.Equal(inner1, inner2), "innerData must be byte-for-byte identical")
	digest1 := ComputeRollupSystemEventDigest(chainID, inner1)
	digest2 := ComputeRollupSystemEventDigest(chainID, inner2)
	require.True(t, bytes.Equal(digest1, digest2), "attestation digests must match")
}

// TestRollupSystemInnerDeterminism_AllEventTypes verifies that across all event types,
// identical input data generates byte-for-byte identical innerData and attestation digests.
func TestRollupSystemInnerDeterminism_AllEventTypes(t *testing.T) {
	chainID := uint64(991)
	events := []Event{
		{
			Type:               EventCreditObserved,
			Role:               RoleReceiver,
			Sender:             common.HexToAddress("0x1111111111111111111111111111111111111111"),
			Target:             common.HexToAddress("0x2222222222222222222222222222222222222222"),
			Value:              big.NewInt(1_000_000),
			IsDestinationValid: true,
		},
		{
			Type:    EventClaimedConfirmed,
			Role:    RoleReceiver,
			Sender:  common.HexToAddress("0x1111111111111111111111111111111111111111"),
			Target:  common.HexToAddress("0x2222222222222222222222222222222222222222"),
			Value:   big.NewInt(1_000_000),
			Outcome: OutcomeCredited,
		},
		{
			Type: EventRPCSubmitted,
			Role: RoleReceiver,
		},
		{
			Type: EventRPCSubmitted,
			Role: RoleSender,
		},
		{
			Type: EventParentConfirmed,
			Role: RoleSender,
		},
		{
			Type:              EventReclaimEligible,
			Role:              RoleSender,
			Value:             big.NewInt(500_000),
			ParentBlockTime:   1700000060,
			ParentConfirmTime: 1700000000,
			Timeout:           60,
		},
		{
			Type:   EventReclaimWon,
			Role:   RoleSender,
			Sender: common.HexToAddress("0x1111111111111111111111111111111111111111"),
			Value:  big.NewInt(500_000),
		},
		{
			Type:   EventRefundObserved,
			Role:   RoleSender,
			Sender: common.HexToAddress("0x1111111111111111111111111111111111111111"),
			Value:  big.NewInt(500_000),
		},
		{
			Type: EventRefundConfirmed,
			Role: RoleReceiver,
		},
	}

	for i, ev := range events {
		p1 := RollupSystemPayload{
			Event:        ev,
			MsgID:        common.HexToHash("0x1122334455667788990011223344556677889900112233445566778899001122"),
			SourceSeq:    uint64(i + 1),
			SourcePubKey: cm.PublicKey{byte(i), 1},
			DestPubKey:   cm.PublicKey{byte(i), 2},
			PayloadHash:  common.HexToHash("0xaabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"),
		}
		p2 := RollupSystemPayload{
			Event:        ev,
			MsgID:        common.HexToHash("0x1122334455667788990011223344556677889900112233445566778899001122"),
			SourceSeq:    uint64(i + 1),
			SourcePubKey: cm.PublicKey{byte(i), 1},
			DestPubKey:   cm.PublicKey{byte(i), 2},
			PayloadHash:  common.HexToHash("0xaabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"),
		}

		b1, err1 := json.Marshal(p1)
		require.NoError(t, err1)
		b2, err2 := json.Marshal(p2)
		require.NoError(t, err2)

		require.True(t, bytes.Equal(b1, b2), "Event %s must serialize deterministically", ev.Type)
		d1 := ComputeRollupSystemEventDigest(chainID, b1)
		d2 := ComputeRollupSystemEventDigest(chainID, b2)
		require.True(t, bytes.Equal(d1, d2), "Event %s digest must match", ev.Type)
	}
}

// TestRollupSystemInnerDeterminism_MutationCatchesMismatch demonstrates mutation testing:
// if a node-local non-deterministic field (e.g. wall-clock time or local duplicate flag)
// is varied between nodes, the test detects the mismatch and rejects it.
func TestRollupSystemInnerDeterminism_MutationCatchesMismatch(t *testing.T) {
	chainID := uint64(991)
	p1 := RollupSystemPayload{
		Event: Event{
			Type:        EventCreditObserved,
			IsDuplicate: false, // Node 1: clean store
		},
		MsgID: common.HexToHash("0x1111"),
	}
	p2 := RollupSystemPayload{
		Event: Event{
			Type:        EventCreditObserved,
			IsDuplicate: true, // Node 2: contaminated/local store
		},
		MsgID: common.HexToHash("0x1111"),
	}

	b1, _ := json.Marshal(p1)
	b2, _ := json.Marshal(p2)

	require.False(t, bytes.Equal(b1, b2), "Mutation check: varying IsDuplicate MUST produce different innerData")
	d1 := ComputeRollupSystemEventDigest(chainID, b1)
	d2 := ComputeRollupSystemEventDigest(chainID, b2)
	require.False(t, bytes.Equal(d1, d2), "Mutation check: varying IsDuplicate MUST produce different digests")
}
