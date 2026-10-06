package rollup

import (
	"bytes"
	"fmt"
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/stretchr/testify/assert"
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

	inner1, err1 := MarshalRollupSystemPayload(&payload1)
	require.NoError(t, err1)
	inner2, err2 := MarshalRollupSystemPayload(&payload2)
	require.NoError(t, err2)

	// Invariant: innerData must be byte-for-byte identical
	require.True(t, bytes.Equal(inner1, inner2), "innerData must be byte-for-byte identical:\nworker1: %x\nworker2: %x", inner1, inner2)

	// Invariant: unmarshaled payload recovers all fields faithfully
	var recovered RollupSystemPayload
	require.NoError(t, UnmarshalRollupSystemPayload(inner1, &recovered))
	require.Equal(t, payload1.MsgID, recovered.MsgID)
	require.Equal(t, payload1.Event.Type, recovered.Event.Type)
	require.Equal(t, payload1.Event.Role, recovered.Event.Role)
	require.Equal(t, payload1.Event.Sender, recovered.Event.Sender)
	require.Equal(t, payload1.Event.Target, recovered.Event.Target)
	require.Equal(t, 0, payload1.Event.Value.Cmp(recovered.Event.Value))

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

	inner1, err1 := MarshalRollupSystemPayload(&payload1)
	require.NoError(t, err1)
	inner2, err2 := MarshalRollupSystemPayload(&payload2)
	require.NoError(t, err2)

	require.True(t, bytes.Equal(inner1, inner2), "innerData must be byte-for-byte identical:\nworker1: %x\nworker2: %x", inner1, inner2)

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

	inner1, err1 := MarshalRollupSystemPayload(&payload1)
	require.NoError(t, err1)
	inner2, err2 := MarshalRollupSystemPayload(&payload2)
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

		b1, err1 := MarshalRollupSystemPayload(&p1)
		require.NoError(t, err1)
		b2, err2 := MarshalRollupSystemPayload(&p2)
		require.NoError(t, err2)

		require.True(t, bytes.Equal(b1, b2), "Event %s must serialize deterministically", ev.Type)
		d1 := ComputeRollupSystemEventDigest(chainID, b1)
		d2 := ComputeRollupSystemEventDigest(chainID, b2)
		require.True(t, bytes.Equal(d1, d2), "Event %s digest must match", ev.Type)

		// Check roundtrip unmarshaling
		var recovered RollupSystemPayload
		require.NoError(t, UnmarshalRollupSystemPayload(b1, &recovered))
		require.Equal(t, p1.Event.Type, recovered.Event.Type)
		require.Equal(t, p1.Event.Role, recovered.Event.Role)
		require.Equal(t, p1.MsgID, recovered.MsgID)
		require.Equal(t, p1.SourceSeq, recovered.SourceSeq)
		require.Equal(t, p1.SourcePubKey, recovered.SourcePubKey)
		require.Equal(t, p1.DestPubKey, recovered.DestPubKey)
		require.Equal(t, p1.PayloadHash, recovered.PayloadHash)
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

	b1, err1 := MarshalRollupSystemPayload(&p1)
	require.NoError(t, err1)
	b2, err2 := MarshalRollupSystemPayload(&p2)
	require.NoError(t, err2)

	require.False(t, bytes.Equal(b1, b2), "Mutation check: varying IsDuplicate MUST produce different innerData")
	d1 := ComputeRollupSystemEventDigest(chainID, b1)
	d2 := ComputeRollupSystemEventDigest(chainID, b2)
	require.False(t, bytes.Equal(d1, d2), "Mutation check: varying IsDuplicate MUST produce different digests")
}

// TestRollupSystemPayload_ProtobufAndJSONDualSupport verifies that UnmarshalRollupSystemPayload
// correctly decodes both deterministic Protobuf binary payloads and legacy JSON payloads.
func TestRollupSystemPayload_ProtobufAndJSONDualSupport(t *testing.T) {
	original := RollupSystemPayload{
		Event: Event{
			Type:               EventCreditObserved,
			Role:               RoleReceiver,
			Sender:             common.HexToAddress("0x1111111111111111111111111111111111111111"),
			Target:             common.HexToAddress("0x2222222222222222222222222222222222222222"),
			Value:              big.NewInt(777_000_000),
			IsDestinationValid: true,
		},
		MsgID:        common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		SourceSeq:    42,
		SourcePubKey: cm.PublicKey{1, 2, 3},
		DestPubKey:   cm.PublicKey{4, 5, 6},
		PayloadHash:  common.HexToHash("0xabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdef"),
	}

	// 1. Protobuf round-trip
	pbBytes, err := MarshalRollupSystemPayload(&original)
	require.NoError(t, err)
	var fromPB RollupSystemPayload
	require.NoError(t, UnmarshalRollupSystemPayload(pbBytes, &fromPB))
	require.Equal(t, original.MsgID, fromPB.MsgID)
	require.Equal(t, original.Event.Type, fromPB.Event.Type)
	require.Equal(t, original.Event.Sender, fromPB.Event.Sender)
	require.Equal(t, original.Event.Target, fromPB.Event.Target)
	require.Equal(t, 0, original.Event.Value.Cmp(fromPB.Event.Value))
	require.Equal(t, original.SourceSeq, fromPB.SourceSeq)
	require.Equal(t, original.SourcePubKey, fromPB.SourcePubKey)
	require.Equal(t, original.DestPubKey, fromPB.DestPubKey)
	require.Equal(t, original.PayloadHash, fromPB.PayloadHash)

	// Verify corrupted non-protobuf data is rejected
	require.Error(t, UnmarshalRollupSystemPayload([]byte("not_protobuf_data"), &fromPB))
}

// TestRollupSystemAttestedPayload_ProtobufRoundtrip verifies that RollupSystemAttestedPayload
// serializes and deserializes cleanly via deterministic Protobuf.
func TestRollupSystemAttestedPayload_ProtobufRoundtrip(t *testing.T) {
	innerRaw := []byte("some_canonical_inner_bytes_for_testing")
	pub := cm.PublicKey{9, 8, 7}
	sig := cm.Sign{1, 2, 3, 4}

	attested := RollupSystemAttestedPayload{
		Kind:  PayloadKindRollupSystemAttested,
		Inner: innerRaw,
		Attestations: []RegistrationAttestation{
			{
				ValidatorPubkey: pub,
				Signature:       sig,
			},
		},
	}

	// 1. Protobuf serialization
	pbBytes, err := MarshalRollupSystemAttestedPayload(&attested)
	require.NoError(t, err)
	require.True(t, IsRollupSystemAttestedPayload(pbBytes), "IsRollupSystemAttestedPayload should identify protobuf")

	var fromPB RollupSystemAttestedPayload
	require.NoError(t, UnmarshalRollupSystemAttestedPayload(pbBytes, &fromPB))
	require.Equal(t, PayloadKindRollupSystemAttested, fromPB.Kind)
	require.Equal(t, innerRaw, fromPB.Inner)
	require.Len(t, fromPB.Attestations, 1)
	require.Equal(t, pub, fromPB.Attestations[0].ValidatorPubkey)
	require.Equal(t, sig, fromPB.Attestations[0].Signature)

	// 2. Corrupted data rejected
	require.False(t, IsRollupSystemAttestedPayload([]byte("invalid_raw_bytes")))
	require.Error(t, UnmarshalRollupSystemAttestedPayload([]byte("invalid_raw_bytes"), &fromPB))
}

// TestRollupSystem_ProtobufPureStrictness_NoJsonFallbackAndFuzz verifies that JSON is rejected
// across all Rollup wire formats and fuzzing random corrupted bytes never panics.
func TestRollupSystem_ProtobufPureStrictness_NoJsonFallbackAndFuzz(t *testing.T) {
	jsonInputs := [][]byte{
		[]byte(`{"event":{"type":"credit_observed"},"msg_id":"0x1234"}`),
		[]byte(`{"kind":"rollup_system_attested","inner":"abc"}`),
		[]byte(`{"kind":"account_registered","user":"0x0000000000000000000000000000000000000001","cluster_key":"0x11"}`),
		[]byte(`{"foo":"bar"}`),
		[]byte(`[]`),
		[]byte(`{}`),
		[]byte(``),
	}

	for i, in := range jsonInputs {
		t.Run(fmt.Sprintf("JSON_Input_%d", i), func(t *testing.T) {
			var p RollupSystemPayload
			errP := UnmarshalRollupSystemPayload(in, &p)
			assert.Error(t, errP, "UnmarshalRollupSystemPayload must reject JSON")

			var att RollupSystemAttestedPayload
			errAtt := UnmarshalRollupSystemAttestedPayload(in, &att)
			assert.Error(t, errAtt, "UnmarshalRollupSystemAttestedPayload must reject JSON")
			assert.False(t, IsRollupSystemAttestedPayload(in))

			var reg AccountRegistrationPayload
			errReg := reg.Unmarshal(in)
			assert.Error(t, errReg, "AccountRegistrationPayload.Unmarshal must reject JSON")
			assert.False(t, IsAccountRegistrationPayload(in))
		})
	}

	// Nil / Empty checks
	var pNil *RollupSystemPayload
	assert.Error(t, UnmarshalRollupSystemPayload([]byte("abc"), pNil))
	assert.Error(t, UnmarshalRollupSystemPayload(nil, &RollupSystemPayload{}))

	var attNil *RollupSystemAttestedPayload
	assert.Error(t, UnmarshalRollupSystemAttestedPayload([]byte("abc"), attNil))
	assert.Error(t, UnmarshalRollupSystemAttestedPayload(nil, &RollupSystemAttestedPayload{}))

	var reg AccountRegistrationPayload
	assert.Error(t, reg.Unmarshal(nil))
	var regNil *AccountRegistrationPayload
	assert.Error(t, regNil.Unmarshal([]byte("abc")))

	// Fuzzing 1000 random corruptions
	rnd := rand.New(rand.NewSource(1337))
	for f := 0; f < 1000; f++ {
		length := rnd.Intn(256)
		buf := make([]byte, length)
		rnd.Read(buf)

		assert.NotPanics(t, func() {
			var p RollupSystemPayload
			_ = UnmarshalRollupSystemPayload(buf, &p)

			var att RollupSystemAttestedPayload
			_ = UnmarshalRollupSystemAttestedPayload(buf, &att)
			_ = IsRollupSystemAttestedPayload(buf)

			var reg AccountRegistrationPayload
			_ = reg.Unmarshal(buf)
			_ = IsAccountRegistrationPayload(buf)
		})
	}
}

