package rollup

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

const (
	AttestationDomainRollupSystemEvent = "ROLLUP_SYS_EVENT_ATTEST_V1"
	PayloadKindRollupSystemAttested    = "rollup_system_attested"
)

var deterministicMarshal = proto.MarshalOptions{Deterministic: true}

// requireLen enforces the exact fixed size of a wire field. The encoders always emit full-size fields, so a decoder
// must never zero-pad, truncate or re-interpret a different length: that would let two different byte strings decode to
// the same value (malleability) or to a different key than the one that was signed.
func requireLen(name string, b []byte, n int) error {
	if len(b) != n {
		return fmt.Errorf("invalid %s length %d, want %d", name, len(b), n)
	}
	return nil
}

func validateAttestationsProto(atts []*pb.RollupAttestationProto) error {
	for i, a := range atts {
		if a == nil {
			metrics.RollupSignaturesRejectedTotal.WithLabelValues("invalid_length").Inc()
			return fmt.Errorf("attestation %d is nil", i)
		}
		if err := requireLen("attestation validator_pubkey", a.ValidatorPubkey, len(cm.PublicKey{})); err != nil {
			metrics.RollupSignaturesRejectedTotal.WithLabelValues("invalid_length").Inc()
			return err
		}
		if err := requireLen("attestation signature", a.Signature, len(cm.Sign{})); err != nil {
			metrics.RollupSignaturesRejectedTotal.WithLabelValues("invalid_length").Inc()
			return err
		}
	}
	return nil
}

// RollupSystemPayload is the canonical inner payload for rollup system events across all validators.
type RollupSystemPayload struct {
	Event        Event        `json:"event"`
	MsgID        common.Hash  `json:"msg_id"`
	SourceSeq    uint64       `json:"source_seq"`
	SourcePubKey cm.PublicKey `json:"source_pub_key"`
	DestPubKey   cm.PublicKey `json:"dest_pub_key"`
	PayloadHash  common.Hash  `json:"payload_hash"`
}

// EventToProto converts a rollup Event to its protobuf representation.
func EventToProto(ev *Event) *pb.RollupEventProto {
	if ev == nil {
		return nil
	}
	p := &pb.RollupEventProto{
		Type:               string(ev.Type),
		Role:               uint32(ev.Role),
		Sender:             ev.Sender.Bytes(),
		Target:             ev.Target.Bytes(),
		ParentBlockTime:    ev.ParentBlockTime,
		ParentConfirmTime:  ev.ParentConfirmTime,
		Timeout:            ev.Timeout,
		ParentTxHash:       ev.ParentTxHash.Bytes(),
		Outcome:            uint32(ev.Outcome),
		IsDuplicate:        ev.IsDuplicate,
		IsDestinationValid: ev.IsDestinationValid,
	}
	if ev.Value != nil {
		p.Value = ev.Value.Bytes()
	}
	if ev.GasFee != nil {
		p.GasFee = ev.GasFee.Bytes()
	}
	return p
}

// EventFromProto populates a rollup Event from its protobuf representation.
func EventFromProto(p *pb.RollupEventProto) Event {
	if p == nil {
		return Event{}
	}
	var ev Event
	ev.Type = EventType(p.Type)
	ev.Role = Role(p.Role)
	if len(p.Sender) > 0 {
		ev.Sender = common.BytesToAddress(p.Sender)
	}
	if len(p.Target) > 0 {
		ev.Target = common.BytesToAddress(p.Target)
	}
	if len(p.Value) > 0 {
		ev.Value = new(big.Int).SetBytes(p.Value)
	}
	if len(p.GasFee) > 0 {
		ev.GasFee = new(big.Int).SetBytes(p.GasFee)
	}
	ev.ParentBlockTime = p.ParentBlockTime
	ev.ParentConfirmTime = p.ParentConfirmTime
	ev.Timeout = p.Timeout
	if len(p.ParentTxHash) > 0 {
		ev.ParentTxHash = common.BytesToHash(p.ParentTxHash)
	}
	ev.Outcome = Outcome(p.Outcome)
	ev.IsDuplicate = p.IsDuplicate
	ev.IsDestinationValid = p.IsDestinationValid
	return ev
}

// ToProto converts RollupSystemPayload to its protobuf message.
func (p *RollupSystemPayload) ToProto() *pb.RollupSystemPayloadProto {
	if p == nil {
		return nil
	}
	return &pb.RollupSystemPayloadProto{
		Event:        EventToProto(&p.Event),
		MsgId:        p.MsgID.Bytes(),
		SourceSeq:    p.SourceSeq,
		SourcePubKey: p.SourcePubKey[:],
		DestPubKey:   p.DestPubKey[:],
		PayloadHash:  p.PayloadHash.Bytes(),
	}
}

// FromProto converts a protobuf message into RollupSystemPayload.
func (p *RollupSystemPayload) FromProto(pbPayload *pb.RollupSystemPayloadProto) {
	if pbPayload == nil {
		return
	}
	p.Event = EventFromProto(pbPayload.Event)
	if len(pbPayload.MsgId) > 0 {
		p.MsgID = common.BytesToHash(pbPayload.MsgId)
	}
	p.SourceSeq = pbPayload.SourceSeq
	if len(pbPayload.SourcePubKey) == len(p.SourcePubKey) {
		copy(p.SourcePubKey[:], pbPayload.SourcePubKey)
	}
	if len(pbPayload.DestPubKey) == len(p.DestPubKey) {
		copy(p.DestPubKey[:], pbPayload.DestPubKey)
	}
	if len(pbPayload.PayloadHash) > 0 {
		p.PayloadHash = common.BytesToHash(pbPayload.PayloadHash)
	}
}

// MarshalRollupSystemPayload encodes payload deterministically as Protobuf.
func MarshalRollupSystemPayload(p *RollupSystemPayload) ([]byte, error) {
	if p == nil {
		return nil, errors.New("cannot marshal nil RollupSystemPayload")
	}
	return deterministicMarshal.Marshal(p.ToProto())
}

// UnmarshalRollupSystemPayload decodes data into RollupSystemPayload from deterministic Protobuf format.
func UnmarshalRollupSystemPayload(data []byte, p *RollupSystemPayload) error {
	if len(data) == 0 {
		return errors.New("empty data for RollupSystemPayload")
	}
	if p == nil {
		return errors.New("nil target RollupSystemPayload")
	}
	var pbPayload pb.RollupSystemPayloadProto
	if err := proto.Unmarshal(data, &pbPayload); err != nil {
		return fmt.Errorf("failed to unmarshal RollupSystemPayload protobuf: %w", err)
	}
	if pbPayload.Event == nil {
		return errors.New("missing Event in RollupSystemPayload protobuf")
	}
	for _, f := range []struct {
		name string
		b    []byte
		n    int
	}{
		{"msg_id", pbPayload.MsgId, common.HashLength},
		{"payload_hash", pbPayload.PayloadHash, common.HashLength},
		{"source_pub_key", pbPayload.SourcePubKey, len(cm.PublicKey{})},
		{"dest_pub_key", pbPayload.DestPubKey, len(cm.PublicKey{})},
		{"event.sender", pbPayload.Event.Sender, common.AddressLength},
		{"event.target", pbPayload.Event.Target, common.AddressLength},
		{"event.parent_tx_hash", pbPayload.Event.ParentTxHash, common.HashLength},
	} {
		if err := requireLen(f.name, f.b, f.n); err != nil {
			return fmt.Errorf("rollup system payload: %w", err)
		}
	}
	p.FromProto(&pbPayload)
	return nil
}

// RollupSystemAttestedPayload wraps any rollup system event payload (such as RollupSystemPayload)
// with committee co-attestations.
type RollupSystemAttestedPayload struct {
	Kind         string                    `json:"kind"`
	Inner        []byte                    `json:"inner"`
	Attestations []RegistrationAttestation `json:"attestations,omitempty"`
}

// ToProto converts RollupSystemAttestedPayload to its protobuf message.
func (p *RollupSystemAttestedPayload) ToProto() *pb.RollupSystemAttestedPayloadProto {
	if p == nil {
		return nil
	}
	var attestations []*pb.RollupAttestationProto
	for _, a := range p.Attestations {
		attestations = append(attestations, &pb.RollupAttestationProto{
			ValidatorPubkey: a.ValidatorPubkey[:],
			Signature:       a.Signature[:],
		})
	}
	return &pb.RollupSystemAttestedPayloadProto{
		Kind:         p.Kind,
		Inner:        p.Inner,
		Attestations: attestations,
	}
}

// FromProto converts a protobuf message into RollupSystemAttestedPayload.
func (p *RollupSystemAttestedPayload) FromProto(pbPayload *pb.RollupSystemAttestedPayloadProto) {
	if pbPayload == nil {
		return
	}
	p.Kind = pbPayload.Kind
	p.Inner = pbPayload.Inner
	p.Attestations = nil
	for _, a := range pbPayload.Attestations {
		var pub cm.PublicKey
		var sig cm.Sign
		copy(pub[:], a.ValidatorPubkey)
		copy(sig[:], a.Signature)
		p.Attestations = append(p.Attestations, RegistrationAttestation{
			ValidatorPubkey: pub,
			Signature:       sig,
		})
	}
}

// MarshalRollupSystemAttestedPayload encodes attested payload deterministically as Protobuf.
func MarshalRollupSystemAttestedPayload(p *RollupSystemAttestedPayload) ([]byte, error) {
	if p == nil {
		return nil, errors.New("cannot marshal nil RollupSystemAttestedPayload")
	}
	return deterministicMarshal.Marshal(p.ToProto())
}

// UnmarshalRollupSystemAttestedPayload decodes data into RollupSystemAttestedPayload from deterministic Protobuf format.
func UnmarshalRollupSystemAttestedPayload(data []byte, p *RollupSystemAttestedPayload) error {
	if len(data) == 0 {
		return errors.New("empty data for RollupSystemAttestedPayload")
	}
	if p == nil {
		return errors.New("nil target RollupSystemAttestedPayload")
	}
	var pbPayload pb.RollupSystemAttestedPayloadProto
	if err := proto.Unmarshal(data, &pbPayload); err != nil {
		return fmt.Errorf("failed to unmarshal RollupSystemAttestedPayload protobuf: %w", err)
	}
	if pbPayload.Kind != PayloadKindRollupSystemAttested {
		return fmt.Errorf("unexpected payload kind %q, expected %q", pbPayload.Kind, PayloadKindRollupSystemAttested)
	}
	if err := validateAttestationsProto(pbPayload.Attestations); err != nil {
		return fmt.Errorf("rollup system attested payload: %w", err)
	}
	p.FromProto(&pbPayload)
	return nil
}

// ComputeRollupSystemEventDigest computes the deterministic keccak256 digest:
// keccak256("ROLLUP_SYS_EVENT_ATTEST_V1" || chainID (8 bytes BE) || keccak256(inner))
func ComputeRollupSystemEventDigest(chainID uint64, inner []byte) []byte {
	var data []byte
	data = append(data, []byte(AttestationDomainRollupSystemEvent)...)
	var chainIDBytes [8]byte
	binary.BigEndian.PutUint64(chainIDBytes[:], chainID)
	data = append(data, chainIDBytes[:]...)
	innerHash := crypto.Keccak256(inner)
	data = append(data, innerHash...)
	return crypto.Keccak256(data)
}

func RollupSystemPendingKey(digest []byte) common.Hash {
	return common.BytesToHash(crypto.Keccak256([]byte("ROLLUP_SYS_PENDING_V1"), digest))
}

func RollupSystemTombstoneKey(digest []byte) common.Hash {
	return common.BytesToHash(crypto.Keccak256([]byte("ROLLUP_SYS_TOMBSTONE_V1"), digest))
}

// HasPendingRollupSystemAttestation checks if the event is already applied (tombstone)
// or if the specified validator has already submitted an attestation that is pending on chain.
func HasPendingRollupSystemAttestation(db SmartContractDB, chainID uint64, inner []byte, validator cm.PublicKey) bool {
	if db == nil {
		return false
	}
	digest := ComputeRollupSystemEventDigest(chainID, inner)
	// If already applied, no need to propose or attest again
	tombstoneKey := RollupSystemTombstoneKey(digest)
	if raw, ok := db.StorageValue(RollupSystemAddress, tombstoneKey); ok && len(raw) > 0 {
		return true
	}
	pendingKey := RollupSystemPendingKey(digest)
	for _, a := range NewDBAttestationStore(db).Load(pendingKey) {
		if a.ValidatorPubkey == validator {
			return true
		}
	}
	return false
}

// IsRollupSystemAttestedPayload reports whether data is a protobuf payload with kind "rollup_system_attested".
func IsRollupSystemAttestedPayload(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	var pbPayload pb.RollupSystemAttestedPayloadProto
	if err := proto.Unmarshal(data, &pbPayload); err == nil && pbPayload.Kind == PayloadKindRollupSystemAttested {
		return true
	}
	return false
}

// ApplyAttestedSystemEvent processes an attested rollup system event payload against state and contract storage.
// It verifies validator signatures, accumulates them across transactions until >= f+1 signatures are collected,
// writes a tombstone to prevent duplicate application, and then calls dispatcher to apply the inner event.
func ApplyAttestedSystemEvent(
	store Store,
	stateDB AccountStateDB,
	contractDB SmartContractDB,
	committee CommitteeProvider,
	chainID uint64,
	data []byte,
	dispatcher func(store Store, stateDB AccountStateDB, inner []byte) error,
) error {
	if stateDB == nil {
		return errors.New("rollup system attestation: stateDB is nil")
	}

	var payload RollupSystemAttestedPayload
	if err := UnmarshalRollupSystemAttestedPayload(data, &payload); err != nil {
		return fmt.Errorf("rollup system attestation: invalid payload: %w", err)
	}
	if payload.Kind != PayloadKindRollupSystemAttested {
		return fmt.Errorf("rollup system attestation: unexpected payload kind %q", payload.Kind)
	}
	if len(payload.Inner) == 0 {
		return errors.New("rollup system attestation: empty inner payload")
	}

	digest := ComputeRollupSystemEventDigest(chainID, payload.Inner)
	tombstoneKey := RollupSystemTombstoneKey(digest)

	// Idempotent: if already applied, return success immediately (no duplicate execution/credit)
	if contractDB != nil {
		if raw, ok := contractDB.StorageValue(RollupSystemAddress, tombstoneKey); ok && len(raw) > 0 {
			return nil
		}
	}

	if committee == nil {
		// Non-committee / testing mode: verify any provided signatures and execute directly
		seen := make(map[cm.PublicKey]bool)
		for _, att := range payload.Attestations {
			if seen[att.ValidatorPubkey] {
				return fmt.Errorf("rollup system attestation: duplicate attestation from validator %x", att.ValidatorPubkey[:6])
			}
			seen[att.ValidatorPubkey] = true
			if !bls.VerifySign(att.ValidatorPubkey, att.Signature, digest) {
				return fmt.Errorf("rollup system attestation: invalid BLS signature from validator %x", att.ValidatorPubkey[:6])
			}
		}
		if err := dispatcher(store, stateDB, payload.Inner); err != nil {
			return err
		}
		if contractDB != nil {
			contractDB.SetStorageValue(RollupSystemAddress, tombstoneKey, []byte{1})
		}
		return nil
	}

	keys, err := committee.GetActiveCommitteeBLSKeys()
	if err != nil {
		metrics.RollupCommitteeReadErrorsTotal.Inc()
		return fmt.Errorf("rollup system attestation: failed to get active committee: %w", err)
	}
	if len(keys) == 0 {
		return errors.New("rollup system attestation: committee is empty")
	}
	committeeSet := make(map[cm.PublicKey]bool, len(keys))
	for _, k := range keys {
		committeeSet[k] = true
	}
	// Quorum threshold: f = (N - 1) / 3, required = f + 1
	required := (len(keys)-1)/3 + 1

	if len(payload.Attestations) == 0 {
		return errors.New("rollup system attestation: no attestation in payload")
	}

	pendingKey := RollupSystemPendingKey(digest)
	var merged []RegistrationAttestation
	have := make(map[cm.PublicKey]bool)
	var attStore AttestationStore
	if contractDB != nil {
		attStore = NewDBAttestationStore(contractDB)
		for _, a := range attStore.Load(pendingKey) {
			if committeeSet[a.ValidatorPubkey] && !have[a.ValidatorPubkey] {
				have[a.ValidatorPubkey] = true
				merged = append(merged, a)
			}
		}
	}

	newInPayload := make(map[cm.PublicKey]bool)
	changed := false
	for _, att := range payload.Attestations {
		if !committeeSet[att.ValidatorPubkey] {
			metrics.RollupSignaturesRejectedTotal.WithLabelValues("non_committee").Inc()
			return fmt.Errorf("rollup system attestation: attestation from non-committee validator %x", att.ValidatorPubkey[:6])
		}
		if newInPayload[att.ValidatorPubkey] {
			metrics.RollupSignaturesRejectedTotal.WithLabelValues("duplicate").Inc()
			return fmt.Errorf("rollup system attestation: duplicate attestation from validator %x", att.ValidatorPubkey[:6])
		}
		newInPayload[att.ValidatorPubkey] = true
		if !bls.VerifySign(att.ValidatorPubkey, att.Signature, digest) {
			metrics.RollupSignaturesRejectedTotal.WithLabelValues("invalid_signature").Inc()
			return fmt.Errorf("rollup system attestation: invalid BLS signature from validator %x", att.ValidatorPubkey[:6])
		}
		if !have[att.ValidatorPubkey] {
			have[att.ValidatorPubkey] = true
			merged = append(merged, att)
			changed = true
		}
	}

	if len(merged) >= required {
		// Dispatch FIRST. If the inner event is rejected (e.g. a stale-state race that the worker retries later) nothing
		// is tombstoned or cleared, so the event is not lost: the next attestation (or the retry by the validator whose
		// share was not recorded) reaches the same quorum and dispatches again. Only a successful dispatch tombstones.
		if err := dispatcher(store, stateDB, payload.Inner); err != nil {
			return err
		}
		if contractDB != nil {
			contractDB.SetStorageValue(RollupSystemAddress, tombstoneKey, []byte{1})
			if attStore != nil {
				attStore.Clear(pendingKey)
			}
		}
		return nil
	}

	if contractDB == nil {
		return fmt.Errorf("rollup system attestation: insufficient attestations: got %d, required %d (committee size %d)", len(merged), required, len(keys))
	}

	// Insufficient signatures: save accumulated and remain PENDING
	if changed && attStore != nil {
		attStore.Save(pendingKey, merged)
	}
	return nil
}
