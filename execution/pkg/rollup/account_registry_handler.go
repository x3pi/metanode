package rollup

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

const (
	SystemPayloadKindAccountRegistered = "account_registered"
)

// AccountRegistrationPayload is the JSON payload sent in a system transaction to RollupSystemAddress
// when an account registration event from Parent Chain is relayed to the execution cluster.
type AccountRegistrationPayload struct {
	Kind       string         `json:"kind"`
	User       common.Address `json:"user"`
	ClusterKey cm.PublicKey   `json:"cluster_key"`
	ParentSeq  uint64         `json:"parent_seq"`
}

func (p *AccountRegistrationPayload) UnmarshalJSON(data []byte) error {
	type rawPayload struct {
		Kind          string          `json:"kind"`
		User          common.Address  `json:"user"`
		ClusterKeyRaw json.RawMessage `json:"cluster_key"`
		ParentSeq     uint64          `json:"parent_seq"`
	}
	var raw rawPayload
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Kind = raw.Kind
	p.User = raw.User
	p.ParentSeq = raw.ParentSeq

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
		Kind       string         `json:"kind"`
		User       common.Address `json:"user"`
		ClusterKey string         `json:"cluster_key"`
		ParentSeq  uint64         `json:"parent_seq"`
	}{
		Kind:       p.Kind,
		User:       p.User,
		ClusterKey: hex.EncodeToString(p.ClusterKey[:]),
		ParentSeq:  p.ParentSeq,
	})
}

// AccountStateRegistryDB is the state interface needed to query and set the ParentRegistered flag.
type AccountStateRegistryDB interface {
	GetParentRegistered(addr common.Address) bool
	SetParentRegistered(addr common.Address, registered bool)
}

// AccountRegistryHandler applies parent-chain account registration events on the execution cluster.
type AccountRegistryHandler struct {
	clusterPubKey cm.PublicKey
}

// NewAccountRegistryHandler creates a new handler bound to the cluster's public key.
func NewAccountRegistryHandler(clusterPubKey cm.PublicKey) *AccountRegistryHandler {
	return &AccountRegistryHandler{
		clusterPubKey: clusterPubKey,
	}
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
// It rejects payloads with mismatched cluster keys, invalid JSON, or missing user addresses.
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

	// Idempotent: already registered is a no-op success
	if stateDB.GetParentRegistered(payload.User) {
		return nil
	}

	stateDB.SetParentRegistered(payload.User, true)
	return nil
}
