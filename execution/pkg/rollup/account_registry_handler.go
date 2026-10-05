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

// Apply executes an account registration payload against the state database.
// It is idempotent (already registered accounts are no-op successes).
// If a CommitteeProvider is configured, it enforces that >= f+1 distinct active committee
// members have signed the deterministic attestation digest.
func (h *AccountRegistryHandler) Apply(stateDB AccountStateRegistryDB, data []byte) error {
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

	// Committee attestation verification
	if h.committeeProvider != nil {
		committee, err := h.committeeProvider.GetActiveCommitteeBLSKeys()
		if err != nil {
			return fmt.Errorf("account registry: failed to get active committee: %w", err)
		}
		if len(committee) == 0 {
			return errors.New("account registry: committee is empty")
		}

		// Quorum threshold: f = (N - 1) / 3, required = f + 1
		n := len(committee)
		f := (n - 1) / 3
		required := f + 1

		if len(payload.Attestations) < required {
			return fmt.Errorf("account registry: insufficient attestations: got %d, required %d (committee size %d)",
				len(payload.Attestations), required, n)
		}

		digest := ComputeAccountRegistrationAttestDigest(h.attestChainID(), payload.User, payload.ClusterKey, payload.ParentSeq)
		seen := make(map[cm.PublicKey]bool)
		committeeSet := make(map[cm.PublicKey]bool, len(committee))
		for _, key := range committee {
			committeeSet[key] = true
		}

		validCount := 0
		for _, att := range payload.Attestations {
			if !committeeSet[att.ValidatorPubkey] {
				return fmt.Errorf("account registry: attestation from non-committee validator %x", att.ValidatorPubkey[:6])
			}
			if seen[att.ValidatorPubkey] {
				return fmt.Errorf("account registry: duplicate attestation from validator %x", att.ValidatorPubkey[:6])
			}
			seen[att.ValidatorPubkey] = true

			if !bls.VerifySign(att.ValidatorPubkey, att.Signature, digest) {
				return fmt.Errorf("account registry: invalid BLS signature from validator %x", att.ValidatorPubkey[:6])
			}
			validCount++
		}

		if validCount < required {
			return fmt.Errorf("account registry: insufficient valid attestations: got %d, required %d", validCount, required)
		}
	} else if len(payload.Attestations) > 0 {
		// When no committee provider is configured, verify any attached signatures against the payload digest
		digest := ComputeAccountRegistrationAttestDigest(h.attestChainID(), payload.User, payload.ClusterKey, payload.ParentSeq)
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
	}

	// Idempotent: already registered is a no-op success
	if stateDB.GetParentRegistered(payload.User) {
		return nil
	}

	stateDB.SetParentRegistered(payload.User, true)
	return nil
}
