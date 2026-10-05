package rollup

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

const (
	SystemPayloadKindAccountRegistered = "account_registered"
	AttestationDomainAccountRegistered = "ACCT_REG_ATTEST_V1"
)

// RegistrationAttestation carries a single validator's BLS signature over the event digest.
type RegistrationAttestation struct {
	ValidatorPubkey cm.PublicKey `json:"validator_pubkey"`
	Signature       cm.Sign      `json:"signature"`
}

func (a *RegistrationAttestation) UnmarshalJSON(data []byte) error {
	var raw struct {
		ValidatorPubkeyRaw json.RawMessage `json:"validator_pubkey"`
		SignatureRaw       json.RawMessage `json:"signature"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	pub, err := parseClusterKey(raw.ValidatorPubkeyRaw)
	if err != nil {
		return fmt.Errorf("invalid validator_pubkey: %w", err)
	}
	sig, err := parseSignature(raw.SignatureRaw)
	if err != nil {
		return fmt.Errorf("invalid signature: %w", err)
	}
	a.ValidatorPubkey = pub
	a.Signature = sig
	return nil
}

func (a RegistrationAttestation) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		ValidatorPubkey string `json:"validator_pubkey"`
		Signature       string `json:"signature"`
	}{
		ValidatorPubkey: hex.EncodeToString(a.ValidatorPubkey[:]),
		Signature:       hex.EncodeToString(a.Signature[:]),
	})
}

func parseSignature(raw json.RawMessage) (cm.Sign, error) {
	var sig cm.Sign
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		b, err := hex.DecodeString(strings.TrimPrefix(str, "0x"))
		if err != nil {
			return sig, fmt.Errorf("invalid signature hex: %w", err)
		}
		if len(b) != len(sig) {
			return sig, fmt.Errorf("invalid signature length %d, want %d", len(b), len(sig))
		}
		copy(sig[:], b)
		return sig, nil
	}
	var arr []int
	if err := json.Unmarshal(raw, &arr); err == nil {
		if len(arr) != len(sig) {
			return sig, fmt.Errorf("invalid signature length %d, want %d", len(arr), len(sig))
		}
		for i, v := range arr {
			if v < 0 || v > 255 {
				return sig, fmt.Errorf("invalid signature byte %d at index %d", v, i)
			}
			sig[i] = byte(v)
		}
		return sig, nil
	}
	return sig, fmt.Errorf("invalid signature format")
}

// ComputeAccountRegistrationAttestDigest computes the deterministic keccak256 digest for committee co-attestation:
// keccak256("ACCT_REG_ATTEST_V1" || chainID || user || clusterKey || parentSeq)
func ComputeAccountRegistrationAttestDigest(chainID uint64, user common.Address, clusterKey cm.PublicKey, parentSeq uint64) []byte {
	var data []byte
	data = append(data, []byte(AttestationDomainAccountRegistered)...)
	var chainIDBytes [8]byte
	binary.BigEndian.PutUint64(chainIDBytes[:], chainID)
	data = append(data, chainIDBytes[:]...)
	data = append(data, user.Bytes()...)
	data = append(data, clusterKey[:]...)
	var seqBytes [8]byte
	binary.BigEndian.PutUint64(seqBytes[:], parentSeq)
	data = append(data, seqBytes[:]...)
	return crypto.Keccak256(data)
}

// AccountRegistrationPayload is the JSON payload sent in a system transaction to RollupSystemAddress
// when an account registration event from Parent Chain is relayed to the execution cluster.
type AccountRegistrationPayload struct {
	Kind         string                    `json:"kind"`
	User         common.Address            `json:"user"`
	ClusterKey   cm.PublicKey              `json:"cluster_key"`
	ParentSeq    uint64                    `json:"parent_seq"`
	Attestations []RegistrationAttestation `json:"attestations,omitempty"`
}

func (p *AccountRegistrationPayload) UnmarshalJSON(data []byte) error {
	type rawPayload struct {
		Kind          string                    `json:"kind"`
		User          common.Address            `json:"user"`
		ClusterKeyRaw json.RawMessage           `json:"cluster_key"`
		ParentSeq     uint64                    `json:"parent_seq"`
		Attestations  []RegistrationAttestation `json:"attestations,omitempty"`
	}
	var raw rawPayload
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Kind = raw.Kind
	p.User = raw.User
	p.ParentSeq = raw.ParentSeq
	p.Attestations = raw.Attestations

	if len(raw.ClusterKeyRaw) > 0 {
		key, err := parseClusterKey(raw.ClusterKeyRaw)
		if err != nil {
			return err
		}
		p.ClusterKey = key
	}
	return nil
}

// parseClusterKey accepts a hex string (with or without 0x) or an array of 48 byte values, and requires EXACTLY 48
// bytes: a shorter or longer value must never be silently zero-padded or truncated into a different key.
func parseClusterKey(raw json.RawMessage) (cm.PublicKey, error) {
	var key cm.PublicKey
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		b, err := hex.DecodeString(strings.TrimPrefix(str, "0x"))
		if err != nil {
			return key, fmt.Errorf("invalid cluster_key hex: %w", err)
		}
		if len(b) != len(key) {
			return key, fmt.Errorf("invalid cluster_key length %d, want %d", len(b), len(key))
		}
		copy(key[:], b)
		return key, nil
	}
	var arr []int
	if err := json.Unmarshal(raw, &arr); err == nil {
		if len(arr) != len(key) {
			return key, fmt.Errorf("invalid cluster_key length %d, want %d", len(arr), len(key))
		}
		for i, v := range arr {
			if v < 0 || v > 255 {
				return key, fmt.Errorf("invalid cluster_key byte %d at index %d", v, i)
			}
			key[i] = byte(v)
		}
		return key, nil
	}
	return key, fmt.Errorf("invalid cluster_key format")
}

func (p AccountRegistrationPayload) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		Kind         string                    `json:"kind"`
		User         common.Address            `json:"user"`
		ClusterKey   string                    `json:"cluster_key"`
		ParentSeq    uint64                    `json:"parent_seq"`
		Attestations []RegistrationAttestation `json:"attestations,omitempty"`
	}{
		Kind:         p.Kind,
		User:         p.User,
		ClusterKey:   hex.EncodeToString(p.ClusterKey[:]),
		ParentSeq:    p.ParentSeq,
		Attestations: p.Attestations,
	})
}

// AccountStateRegistryDB is the state interface needed to query and set the ParentRegistered flag.
type AccountStateRegistryDB interface {
	GetParentRegistered(addr common.Address) bool
	SetParentRegistered(addr common.Address, registered bool)
}

// NOTE: committee enforcement is NOT active in production yet: no CommitteeProvider is wired in simple_chain, and the
// RegistrationWorker attaches only its OWN attestation (no signature exchange between validators). Wiring a provider
// for a committee with f+1 > 1 before that exchange exists would leave every registration PENDING forever.
// CommitteeProvider returns the active committee validator BLS public keys.
type CommitteeProvider interface {
	GetActiveCommitteeBLSKeys() ([]cm.PublicKey, error)
}

// AccountRegistryHandler applies parent-chain account registration events on the execution cluster.
type AccountRegistryHandler struct {
	clusterPubKey     cm.PublicKey
	chainID           uint64
	committeeProvider CommitteeProvider
}

// NewAccountRegistryHandler creates a new handler bound to the cluster's public key.
func NewAccountRegistryHandler(clusterPubKey cm.PublicKey) *AccountRegistryHandler {
	return &AccountRegistryHandler{clusterPubKey: clusterPubKey}
}

// SetCommitteeProvider sets the committee provider for quorum verification (>= f+1).
func (h *AccountRegistryHandler) SetCommitteeProvider(cp CommitteeProvider) {
	h.committeeProvider = cp
}

// attestChainID is the chain ID bound into the attestation digest: an explicit SetChainID value, otherwise the
// configured shared chain ID (parentchain.ParentChainID), i.e. the same value the RegistrationWorker signs with.
func (h *AccountRegistryHandler) attestChainID() uint64 {
	if h.chainID != 0 {
		return h.chainID
	}
	return parentchain.ParentChainID
}

// SetChainID sets the expected chain ID used for attestation digest calculation.
func (h *AccountRegistryHandler) SetChainID(id uint64) {
	h.chainID = id
}

// ClusterPublicKey returns the cluster key configured for this handler.
func (h *AccountRegistryHandler) ClusterPublicKey() cm.PublicKey {
	return h.clusterPubKey
}

// IsAccountRegistrationPayload checks whether the given byte slice is a JSON payload with kind="account_registered".
func IsAccountRegistrationPayload(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &probe); err == nil && probe.Kind == SystemPayloadKindAccountRegistered {
		return true
	}
	return false
}

// Apply executes an account registration payload without a pending-attestation store: every attestation needed must
// already be inside the payload. Kept for callers/tests that have no state store; production goes through ApplyAttested.
func (h *AccountRegistryHandler) Apply(stateDB AccountStateRegistryDB, data []byte) error {
	return h.ApplyAttested(stateDB, nil, h.committeeProvider, data)
}

// AttestationStore persists the validator attestations collected so far for one registration event (keyed by the event
// digest). It must live in replicated, deterministically executed state so every replica sees the same set.
type AttestationStore interface {
	Load(key common.Hash) []RegistrationAttestation
	Save(key common.Hash, atts []RegistrationAttestation)
	Clear(key common.Hash)
}

// ApplyAttested executes an account registration payload against the state database.
//
// With a committee it enforces the f+1 co-attestation rule: the event is applied only once >= f+1 DISTINCT committee
// members have signed its digest. Each validator submits its own system tx carrying its own attestation (it signs only
// events it verified itself against the Parent Chain quorum); signatures accumulate in store across blocks until the
// threshold is reached, so no off-chain signature exchange is needed and no timing is involved: an event that lacks
// signatures simply stays PENDING. With committee == nil (tests / non-enforcing use) any attached signatures are only
// checked for validity. The call is idempotent (already registered and repeated attestations are no-op successes).
func (h *AccountRegistryHandler) ApplyAttested(stateDB AccountStateRegistryDB, store AttestationStore, committee CommitteeProvider, data []byte) error {
	if stateDB == nil {
		return errors.New("account registry: stateDB is nil")
	}

	var payload AccountRegistrationPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("account registry: invalid json payload: %w", err)
	}
	if payload.Kind != SystemPayloadKindAccountRegistered {
		return fmt.Errorf("account registry: unexpected payload kind %q", payload.Kind)
	}
	if payload.User == (common.Address{}) {
		return errors.New("account registry: empty user address")
	}
	if payload.ClusterKey != h.clusterPubKey {
		return fmt.Errorf("account registry: cluster key mismatch: got %x, want %x", payload.ClusterKey[:6], h.clusterPubKey[:6])
	}

	// Idempotent: already registered is a no-op success
	if stateDB.GetParentRegistered(payload.User) {
		return nil
	}

	digest := ComputeAccountRegistrationAttestDigest(h.attestChainID(), payload.User, payload.ClusterKey, payload.ParentSeq)

	if committee == nil {
		seen := make(map[cm.PublicKey]bool)
		for _, att := range payload.Attestations {
			if seen[att.ValidatorPubkey] {
				return fmt.Errorf("account registry: duplicate attestation from validator %x", att.ValidatorPubkey[:6])
			}
			seen[att.ValidatorPubkey] = true
			if !bls.VerifySign(att.ValidatorPubkey, att.Signature, digest) {
				return fmt.Errorf("account registry: invalid BLS signature from validator %x", att.ValidatorPubkey[:6])
			}
		}
		stateDB.SetParentRegistered(payload.User, true)
		return nil
	}

	keys, err := committee.GetActiveCommitteeBLSKeys()
	if err != nil {
		return fmt.Errorf("account registry: failed to get active committee: %w", err)
	}
	if len(keys) == 0 {
		return errors.New("account registry: committee is empty")
	}
	committeeSet := make(map[cm.PublicKey]bool, len(keys))
	for _, k := range keys {
		committeeSet[k] = true
	}
	// Quorum threshold: f = (N - 1) / 3, required = f + 1
	required := (len(keys)-1)/3 + 1

	if len(payload.Attestations) == 0 {
		return errors.New("account registry: no attestation in payload")
	}
	pendingKey := attestationPendingKey(digest)
	var merged []RegistrationAttestation
	have := make(map[cm.PublicKey]bool)
	if store != nil {
		for _, a := range store.Load(pendingKey) {
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
			return fmt.Errorf("account registry: attestation from non-committee validator %x", att.ValidatorPubkey[:6])
		}
		if newInPayload[att.ValidatorPubkey] {
			return fmt.Errorf("account registry: duplicate attestation from validator %x", att.ValidatorPubkey[:6])
		}
		newInPayload[att.ValidatorPubkey] = true
		if !bls.VerifySign(att.ValidatorPubkey, att.Signature, digest) {
			return fmt.Errorf("account registry: invalid BLS signature from validator %x", att.ValidatorPubkey[:6])
		}
		if !have[att.ValidatorPubkey] {
			have[att.ValidatorPubkey] = true
			merged = append(merged, att)
			changed = true
		}
	}

	if len(merged) >= required {
		stateDB.SetParentRegistered(payload.User, true)
		if store != nil {
			store.Clear(pendingKey)
		}
		return nil
	}
	if store == nil {
		return fmt.Errorf("account registry: insufficient attestations: got %d, required %d (committee size %d)", len(merged), required, len(keys))
	}
	// Not enough yet: stay PENDING (success receipt, no registration) and remember the signatures collected so far.
	if changed {
		store.Save(pendingKey, merged)
	}
	return nil
}

func attestationPendingKey(digest []byte) common.Hash {
	return common.BytesToHash(crypto.Keccak256([]byte("ACCT_REG_PENDING_V1"), digest))
}

// HasPendingAttestation reports whether validator's attestation for this event is already recorded on chain (waiting
// for the remaining validators), so its worker does not keep re-submitting it.
func HasPendingAttestation(db SmartContractDB, chainID uint64, user common.Address, clusterKey cm.PublicKey, parentSeq uint64, validator cm.PublicKey) bool {
	key := attestationPendingKey(ComputeAccountRegistrationAttestDigest(chainID, user, clusterKey, parentSeq))
	for _, a := range NewDBAttestationStore(db).Load(key) {
		if a.ValidatorPubkey == validator {
			return true
		}
	}
	return false
}

const attestEntrySize = len(cm.PublicKey{}) + len(cm.Sign{})

// dbAttestationStore keeps pending attestations in the rollup system address's contract storage, the same replicated
// storage that already holds the rollup message records.
type dbAttestationStore struct{ db SmartContractDB }

// NewDBAttestationStore returns an AttestationStore over contract storage at RollupSystemAddress.
func NewDBAttestationStore(db SmartContractDB) AttestationStore { return &dbAttestationStore{db: db} }

func (s *dbAttestationStore) Load(key common.Hash) []RegistrationAttestation {
	raw, ok := s.db.StorageValue(RollupSystemAddress, key)
	if !ok || len(raw) == 0 || len(raw)%attestEntrySize != 0 {
		return nil
	}
	out := make([]RegistrationAttestation, 0, len(raw)/attestEntrySize)
	for off := 0; off < len(raw); off += attestEntrySize {
		var a RegistrationAttestation
		copy(a.ValidatorPubkey[:], raw[off:off+len(a.ValidatorPubkey)])
		copy(a.Signature[:], raw[off+len(a.ValidatorPubkey):off+attestEntrySize])
		out = append(out, a)
	}
	return out
}

func (s *dbAttestationStore) Save(key common.Hash, atts []RegistrationAttestation) {
	buf := make([]byte, 0, len(atts)*attestEntrySize)
	for _, a := range atts {
		buf = append(buf, a.ValidatorPubkey[:]...)
		buf = append(buf, a.Signature[:]...)
	}
	s.db.SetStorageValue(RollupSystemAddress, key, buf)
}

func (s *dbAttestationStore) Clear(key common.Hash) {
	s.db.SetStorageValue(RollupSystemAddress, key, nil)
}
