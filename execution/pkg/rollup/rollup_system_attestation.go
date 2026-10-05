package rollup

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

const (
	AttestationDomainRollupSystemEvent = "ROLLUP_SYS_EVENT_ATTEST_V1"
	PayloadKindRollupSystemAttested    = "rollup_system_attested"
)

// RollupSystemAttestedPayload wraps any rollup system event payload (such as RollupSystemPayload)
// with committee co-attestations.
type RollupSystemAttestedPayload struct {
	Kind         string                    `json:"kind"`
	Inner        json.RawMessage           `json:"inner"`
	Attestations []RegistrationAttestation `json:"attestations,omitempty"`
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

// IsRollupSystemAttestedPayload reports whether data is a JSON payload with kind "rollup_system_attested".
func IsRollupSystemAttestedPayload(data []byte) bool {
	var p struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &p); err == nil && p.Kind == PayloadKindRollupSystemAttested {
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
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("rollup system attestation: invalid json payload: %w", err)
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
			return fmt.Errorf("rollup system attestation: attestation from non-committee validator %x", att.ValidatorPubkey[:6])
		}
		if newInPayload[att.ValidatorPubkey] {
			return fmt.Errorf("rollup system attestation: duplicate attestation from validator %x", att.ValidatorPubkey[:6])
		}
		newInPayload[att.ValidatorPubkey] = true
		if !bls.VerifySign(att.ValidatorPubkey, att.Signature, digest) {
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
