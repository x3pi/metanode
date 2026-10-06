package rollup

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

const (
	SystemPayloadKindAccountRegistered = "account_registered"
	AttestationDomainAccountRegistered = "ACCT_REG_ATTEST_V1"
)

// RegistrationAttestation carries a single validator's BLS signature over the event digest.
type RegistrationAttestation struct {
	ValidatorPubkey cm.PublicKey
	Signature       cm.Sign
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

// AccountRegistrationPayload is the canonical payload sent in a system transaction to RollupSystemAddress
// when an account registration event from Parent Chain is relayed to the execution cluster.
type AccountRegistrationPayload struct {
	Kind         string
	User         common.Address
	ClusterKey   cm.PublicKey
	ParentSeq    uint64
	Attestations []RegistrationAttestation
}

// ToProto converts AccountRegistrationPayload to protobuf representation.
func (p *AccountRegistrationPayload) ToProto() *pb.AccountRegistrationPayloadProto {
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
	return &pb.AccountRegistrationPayloadProto{
		Kind:         p.Kind,
		User:         p.User.Bytes(),
		ClusterKey:   p.ClusterKey[:],
		ParentSeq:    p.ParentSeq,
		Attestations: attestations,
	}
}

// FromProto populates AccountRegistrationPayload from protobuf message.
func (p *AccountRegistrationPayload) FromProto(pbPayload *pb.AccountRegistrationPayloadProto) {
	if pbPayload == nil {
		return
	}
	p.Kind = pbPayload.Kind
	if len(pbPayload.User) > 0 {
		p.User = common.BytesToAddress(pbPayload.User)
	}
	if len(pbPayload.ClusterKey) == len(p.ClusterKey) {
		copy(p.ClusterKey[:], pbPayload.ClusterKey)
	}
	p.ParentSeq = pbPayload.ParentSeq
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

// MarshalProto encodes AccountRegistrationPayload as deterministic Protobuf.
func (p *AccountRegistrationPayload) MarshalProto() ([]byte, error) {
	if p == nil {
		return nil, errors.New("cannot marshal nil AccountRegistrationPayload")
	}
	return deterministicMarshal.Marshal(p.ToProto())
}

// Unmarshal decodes data into AccountRegistrationPayload from deterministic Protobuf format.
func (p *AccountRegistrationPayload) Unmarshal(data []byte) error {
	if len(data) == 0 {
		return errors.New("empty data for AccountRegistrationPayload")
	}
	if p == nil {
		return errors.New("nil target AccountRegistrationPayload")
	}
	var pbPayload pb.AccountRegistrationPayloadProto
	if err := proto.Unmarshal(data, &pbPayload); err != nil {
		return fmt.Errorf("failed to unmarshal AccountRegistrationPayload protobuf: %w", err)
	}
	if pbPayload.Kind != SystemPayloadKindAccountRegistered {
		return fmt.Errorf("unexpected payload kind %q, expected %q", pbPayload.Kind, SystemPayloadKindAccountRegistered)
	}
	if len(pbPayload.ClusterKey) != len(p.ClusterKey) {
		return fmt.Errorf("account registry: cluster_key must be exactly %d bytes, got %d", len(p.ClusterKey), len(pbPayload.ClusterKey))
	}
	if err := requireLen("user", pbPayload.User, common.AddressLength); err != nil {
		return fmt.Errorf("account registry: %w", err)
	}
	if err := validateAttestationsProto(pbPayload.Attestations); err != nil {
		return fmt.Errorf("account registry: %w", err)
	}
	p.FromProto(&pbPayload)
	return nil
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

// IsAccountRegistrationPayload checks whether the given byte slice is a Protobuf payload with kind="account_registered".
func IsAccountRegistrationPayload(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	var pbPayload pb.AccountRegistrationPayloadProto
	if err := proto.Unmarshal(data, &pbPayload); err == nil && pbPayload.Kind == SystemPayloadKindAccountRegistered {
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
	if err := payload.Unmarshal(data); err != nil {
		return fmt.Errorf("account registry: invalid payload: %w", err)
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
		metrics.RollupCommitteeReadErrorsTotal.Inc()
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
			metrics.RollupSignaturesRejectedTotal.WithLabelValues("non_committee").Inc()
			return fmt.Errorf("account registry: attestation from non-committee validator %x", att.ValidatorPubkey[:6])
		}
		if newInPayload[att.ValidatorPubkey] {
			metrics.RollupSignaturesRejectedTotal.WithLabelValues("duplicate").Inc()
			return fmt.Errorf("account registry: duplicate attestation from validator %x", att.ValidatorPubkey[:6])
		}
		newInPayload[att.ValidatorPubkey] = true
		if !bls.VerifySign(att.ValidatorPubkey, att.Signature, digest) {
			metrics.RollupSignaturesRejectedTotal.WithLabelValues("invalid_signature").Inc()
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
	if len(atts) == 0 {
		s.Clear(key)
		return
	}
	raw, ok := s.db.StorageValue(RollupSystemAddress, key)
	if !ok || len(raw) == 0 {
		metrics.RollupAttestationPendingTotal.Inc()
	}
	buf := make([]byte, 0, len(atts)*attestEntrySize)
	for _, a := range atts {
		buf = append(buf, a.ValidatorPubkey[:]...)
		buf = append(buf, a.Signature[:]...)
	}
	s.db.SetStorageValue(RollupSystemAddress, key, buf)
}

func (s *dbAttestationStore) Clear(key common.Hash) {
	raw, ok := s.db.StorageValue(RollupSystemAddress, key)
	if ok && len(raw) > 0 {
		metrics.RollupAttestationPendingTotal.Dec()
	}
	s.db.SetStorageValue(RollupSystemAddress, key, nil)
}
