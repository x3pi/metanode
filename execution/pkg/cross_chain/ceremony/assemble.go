package ceremony

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

// GenesisConfigOut mirrors execution/pkg/config.GenesisConfig's JSON shape
// exactly (field names/tags), so the assembled genesis.json round-trips
// through config.LoadGenesisData without modification.
type GenesisConfigOut struct {
	ChainId              *big.Int `json:"chainId"`
	Epoch                int      `json:"epoch"`
	EpochTimestampMs     uint64   `json:"epoch_timestamp_ms,omitempty"`
	AttestationInterval  uint64   `json:"attestation_interval,omitempty"`
	EpochDurationSeconds uint64   `json:"epoch_duration_seconds,omitempty"`
}

// GenesisOutput is the assembled genesis.json for the new Root Anchor network.
// TotalStake/QuorumThreshold/ValidityThreshold are informational only — the Go
// loader (execution/pkg/config/genesis.go GenesisData) does not read them —
// included only for operational parity with existing genesis-main.json files.
type GenesisOutput struct {
	Config            GenesisConfigOut        `json:"config"`
	Validators        []GenesisValidatorEntry `json:"validators"`
	Alloc             []GenesisAllocEntry     `json:"alloc"`
	TotalStake        uint64                  `json:"total_stake"`
	QuorumThreshold   uint64                  `json:"quorum_threshold"`
	ValidityThreshold uint64                  `json:"validity_threshold"`
}

// AssembleOptions are the coordinator-supplied parameters for the new Root
// Anchor network, independent of any single founding chain.
type AssembleOptions struct {
	ChainID              uint64 // the NEW Root Anchor network's own chain ID (must not collide with any founding chain's ID)
	EpochTimestampMs     uint64
	EpochDurationSeconds uint64
	AttestationInterval  uint64
	MaxStakeCapPercent   uint8 // 0 => cross_chain.DefaultMaxStakeCapPercent (33)
}

// AssembleResult bundles everything the coordinator needs to publish.
type AssembleResult struct {
	Genesis   *GenesisOutput
	Committee *cross_chain.RootAnchorCommittee
	Digest    [32]byte
}

// Assemble validates >= 4 founding_entry.json submissions and deterministically
// builds the Root Anchor genesis.json + committee. It is the single point where
// ceremony-level checks (Milestone D, gaps #3/#4 of the plan) are enforced —
// duplicate identities, port collisions, malformed keys — on top of the
// already-tested invariants in cross_chain.NewRootAnchorCommittee (>= 4 chains,
// PoP, stake cap, duplicate chain IDs).
func Assemble(entries []FoundingEntry, opts AssembleOptions) (*AssembleResult, error) {
	if opts.ChainID == 0 {
		return nil, fmt.Errorf("AssembleOptions.ChainID (new Root Anchor chain ID) is required and must be non-zero")
	}
	if err := validateNoCollisions(entries, opts); err != nil {
		return nil, err
	}

	chainNames := make(map[uint64]string)
	chainValidatorsCC := make(map[uint64][]cross_chain.ValidatorEntry)
	var chainOrder []uint64

	for _, e := range entries {
		if name, ok := chainNames[e.ChainID]; ok {
			if name != e.ChainName {
				return nil, fmt.Errorf("founding chain %d has inconsistent chain_name across entries: %q vs %q", e.ChainID, name, e.ChainName)
			}
		} else {
			chainNames[e.ChainID] = e.ChainName
			chainOrder = append(chainOrder, e.ChainID)
		}

		ccEntry, err := toCrossChainValidatorEntry(e.CrossChain)
		if err != nil {
			return nil, fmt.Errorf("entry for chain %d hostname %q: %w", e.ChainID, e.GenesisValidator.Hostname, err)
		}
		chainValidatorsCC[e.ChainID] = append(chainValidatorsCC[e.ChainID], ccEntry)
	}

	foundingChains := make([]cross_chain.FoundingChainConfig, 0, len(chainOrder))
	for _, chainID := range chainOrder {
		validators := chainValidatorsCC[chainID]
		var totalStake uint64
		for _, v := range validators {
			totalStake += v.Stake
		}
		foundingChains = append(foundingChains, cross_chain.FoundingChainConfig{
			ChainID:    chainID,
			Name:       chainNames[chainID],
			Validators: validators,
			TotalStake: totalStake,
		})
		if chainID == opts.ChainID {
			return nil, fmt.Errorf("Root Anchor chain_id %d collides with founding chain %q's own chain_id — pick a distinct chain ID for the new network", opts.ChainID, chainNames[chainID])
		}
	}

	committee, err := cross_chain.NewRootAnchorCommittee(foundingChains, opts.MaxStakeCapPercent)
	if err != nil {
		return nil, fmt.Errorf("committee aggregation failed: %w", err)
	}

	sortedValidators, sortedAlloc, err := buildSortedGenesisArrays(entries)
	if err != nil {
		return nil, err
	}

	genesis := &GenesisOutput{
		Config: GenesisConfigOut{
			ChainId:              new(big.Int).SetUint64(opts.ChainID),
			Epoch:                0,
			EpochTimestampMs:     opts.EpochTimestampMs,
			AttestationInterval:  opts.AttestationInterval,
			EpochDurationSeconds: opts.EpochDurationSeconds,
		},
		Validators: sortedValidators,
		Alloc:      sortedAlloc,
		// Informational parity fields only (see GenesisOutput doc comment) —
		// computed via cross_chain.RootAnchorCommittee's canonical BFT formula,
		// which may be off by one from older ad-hoc genesis-main.json values.
		TotalStake:        committee.TotalStake,
		QuorumThreshold:   committee.BftQuorumThreshold(),
		ValidityThreshold: committee.MaxFaultyStake() + 1,
	}

	digestBytes, err := CanonicalGenesisBytes(genesis)
	if err != nil {
		return nil, err
	}

	return &AssembleResult{
		Genesis:   genesis,
		Committee: committee,
		Digest:    Digest(digestBytes),
	}, nil
}

func toCrossChainValidatorEntry(cc CrossChainEntry) (cross_chain.ValidatorEntry, error) {
	pub, err := decodeHex0x(cc.PubkeyBLS, 48)
	if err != nil {
		return cross_chain.ValidatorEntry{}, fmt.Errorf("cross_chain.pubkey_bls: %w", err)
	}
	sig, err := decodeHex0x(cc.PopSignature, 96)
	if err != nil {
		return cross_chain.ValidatorEntry{}, fmt.Errorf("cross_chain.pop_signature: %w", err)
	}
	return cross_chain.ValidatorEntry{
		PubkeyBLS:    pub,
		Stake:        cc.Stake,
		PopSignature: sig,
	}, nil
}

// buildSortedGenesisArrays sorts validators+alloc together by the SAME key
// used at runtime committee-build time
// (execution/executor/unix_socket_handler_validators.go:185-198): bytes of
// AuthorityKey (min-sig, base64-decoded), tiebreak eth address, tiebreak
// p2p_address. This mirrors the Go/Rust ordering so the on-disk genesis.json
// is already in canonical order (the runtime re-sorts anyway, but a
// mismatched on-disk order would defeat visual/manual diffing between
// operators during the D3 verify step).
func buildSortedGenesisArrays(entries []FoundingEntry) ([]GenesisValidatorEntry, []GenesisAllocEntry, error) {
	type pair struct {
		v       GenesisValidatorEntry
		a       GenesisAllocEntry
		authKey []byte
	}
	pairs := make([]pair, 0, len(entries))
	for _, e := range entries {
		authKey, err := base64.StdEncoding.DecodeString(e.GenesisValidator.AuthorityKey)
		if err != nil {
			return nil, nil, fmt.Errorf("hostname %q: invalid authority_key base64: %w", e.GenesisValidator.Hostname, err)
		}
		pairs = append(pairs, pair{v: e.GenesisValidator, a: e.GenesisAlloc, authKey: authKey})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		cmp := bytes.Compare(pairs[i].authKey, pairs[j].authKey)
		if cmp != 0 {
			return cmp < 0
		}
		if pairs[i].v.Address != pairs[j].v.Address {
			return pairs[i].v.Address < pairs[j].v.Address
		}
		return pairs[i].v.P2PAddress < pairs[j].v.P2PAddress
	})

	validators := make([]GenesisValidatorEntry, len(pairs))
	alloc := make([]GenesisAllocEntry, len(pairs))
	for i, pr := range pairs {
		validators[i] = pr.v
		alloc[i] = pr.a
	}
	return validators, alloc, nil
}

// validateNoCollisions performs the ceremony-level checks the existing
// gen_validator_entry.py auto-merge does NOT do (Milestone D plan gap #3):
// duplicate identities and port/address collisions across ALL submitted
// entries, plus key-length/schema sanity.
func validateNoCollisions(entries []FoundingEntry, opts AssembleOptions) error {
	if len(entries) == 0 {
		return fmt.Errorf("no founding_entry.json submissions provided")
	}

	seenAddress := make(map[string]string) // eth address -> hostname
	seenHostname := make(map[string]bool)
	seenAuthorityKey := make(map[string]string) // base64 -> hostname
	seenProtocolKey := make(map[string]string)
	seenNetworkKey := make(map[string]string)
	seenP2PAddr := make(map[string]string)
	seenPrimaryAddr := make(map[string]string)
	seenWorkerAddr := make(map[string]string)

	for _, e := range entries {
		hostname := e.GenesisValidator.Hostname
		if e.SchemaVersion != SchemaVersion {
			return fmt.Errorf("hostname %q: unsupported schema_version %d (expected %d)", hostname, e.SchemaVersion, SchemaVersion)
		}
		if e.ChainID == 0 {
			return fmt.Errorf("hostname %q: chain_id must be non-zero", hostname)
		}
		if hostname == "" {
			return fmt.Errorf("entry for chain %d has an empty hostname", e.ChainID)
		}
		if e.ChainID == opts.ChainID {
			return fmt.Errorf("hostname %q declares founding chain_id %d which collides with the Root Anchor's own new chain_id — pick a distinct chain ID", hostname, e.ChainID)
		}

		addr := strings.ToLower(e.GenesisValidator.Address)
		if addr == "" || !strings.HasPrefix(addr, "0x") || len(addr) != 42 {
			return fmt.Errorf("hostname %q: invalid eth address %q", hostname, e.GenesisValidator.Address)
		}
		if addr != strings.ToLower(e.GenesisAlloc.Address) {
			return fmt.Errorf("hostname %q: genesis_validator.address %q != genesis_alloc.address %q", hostname, e.GenesisValidator.Address, e.GenesisAlloc.Address)
		}

		if err := requireKeyLen(e.GenesisValidator.AuthorityKey, true, 96, "authority_key"); err != nil {
			return fmt.Errorf("hostname %q: %w", hostname, err)
		}
		if err := requireKeyLen(e.GenesisValidator.ProtocolKey, true, 32, "protocol_key"); err != nil {
			return fmt.Errorf("hostname %q: %w", hostname, err)
		}
		if err := requireKeyLen(e.GenesisValidator.NetworkKey, true, 32, "network_key"); err != nil {
			return fmt.Errorf("hostname %q: %w", hostname, err)
		}
		if err := requireKeyLen(e.GenesisAlloc.PublicKeyBls, false, 48, "publicKeyBls"); err != nil {
			return fmt.Errorf("hostname %q: %w", hostname, err)
		}
		if err := requireKeyLen(e.CrossChain.PubkeyBLS, false, 48, "cross_chain.pubkey_bls"); err != nil {
			return fmt.Errorf("hostname %q: %w", hostname, err)
		}
		if err := requireKeyLen(e.CrossChain.PopSignature, false, 96, "cross_chain.pop_signature"); err != nil {
			return fmt.Errorf("hostname %q: %w", hostname, err)
		}
		if strings.ToLower(e.GenesisAlloc.PublicKeyBls) != strings.ToLower(e.CrossChain.PubkeyBLS) {
			return fmt.Errorf("hostname %q: genesis_alloc.publicKeyBls != cross_chain.pubkey_bls (must be the same min-pk key)", hostname)
		}

		if prev, ok := seenAddress[addr]; ok {
			return fmt.Errorf("duplicate eth address %s: hostnames %q and %q", addr, prev, hostname)
		}
		seenAddress[addr] = hostname
		if seenHostname[hostname] {
			return fmt.Errorf("duplicate hostname %q", hostname)
		}
		seenHostname[hostname] = true
		if prev, ok := seenAuthorityKey[e.GenesisValidator.AuthorityKey]; ok {
			return fmt.Errorf("duplicate authority_key: hostnames %q and %q", prev, hostname)
		}
		seenAuthorityKey[e.GenesisValidator.AuthorityKey] = hostname
		if prev, ok := seenProtocolKey[e.GenesisValidator.ProtocolKey]; ok {
			return fmt.Errorf("duplicate protocol_key: hostnames %q and %q", prev, hostname)
		}
		seenProtocolKey[e.GenesisValidator.ProtocolKey] = hostname
		if prev, ok := seenNetworkKey[e.GenesisValidator.NetworkKey]; ok {
			return fmt.Errorf("duplicate network_key: hostnames %q and %q", prev, hostname)
		}
		seenNetworkKey[e.GenesisValidator.NetworkKey] = hostname
		if prev, ok := seenP2PAddr[e.GenesisValidator.P2PAddress]; ok {
			return fmt.Errorf("p2p_address collision %q: hostnames %q and %q", e.GenesisValidator.P2PAddress, prev, hostname)
		}
		seenP2PAddr[e.GenesisValidator.P2PAddress] = hostname
		if prev, ok := seenPrimaryAddr[e.GenesisValidator.PrimaryAddress]; ok {
			return fmt.Errorf("primary_address collision %q: hostnames %q and %q", e.GenesisValidator.PrimaryAddress, prev, hostname)
		}
		seenPrimaryAddr[e.GenesisValidator.PrimaryAddress] = hostname
		if prev, ok := seenWorkerAddr[e.GenesisValidator.WorkerAddress]; ok {
			return fmt.Errorf("worker_address collision %q: hostnames %q and %q", e.GenesisValidator.WorkerAddress, prev, hostname)
		}
		seenWorkerAddr[e.GenesisValidator.WorkerAddress] = hostname
	}

	return nil
}

func requireKeyLen(value string, isBase64 bool, wantLen int, fieldName string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", fieldName)
	}
	var raw []byte
	var err error
	if isBase64 {
		raw, err = base64.StdEncoding.DecodeString(value)
	} else {
		raw, err = decodeHex0x(value, -1)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", fieldName, err)
	}
	if len(raw) != wantLen {
		return fmt.Errorf("%s: expected %d bytes, got %d", fieldName, wantLen, len(raw))
	}
	return nil
}

func decodeHex0x(s string, wantLen int) ([]byte, error) {
	s = strings.TrimPrefix(s, "0x")
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid hex: %w", err)
	}
	if wantLen >= 0 && len(raw) != wantLen {
		return nil, fmt.Errorf("expected %d bytes, got %d", wantLen, len(raw))
	}
	return raw, nil
}

// LoadFoundingEntry reads and schema-validates one founding_entry.json file.
func LoadFoundingEntry(path string) (FoundingEntry, error) {
	var fe FoundingEntry
	raw, err := os.ReadFile(path)
	if err != nil {
		return fe, err
	}
	if err := json.Unmarshal(raw, &fe); err != nil {
		return fe, fmt.Errorf("parsing %s: %w", path, err)
	}
	return fe, nil
}

// LoadFoundingEntriesFromDir loads every *.json file directly inside dir as a
// FoundingEntry (non-recursive).
func LoadFoundingEntriesFromDir(dir string) ([]FoundingEntry, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches) // deterministic input order (Assemble's own sort makes output order-independent regardless, but this keeps error messages reproducible)
	entries := make([]FoundingEntry, 0, len(matches))
	for _, m := range matches {
		fe, err := LoadFoundingEntry(m)
		if err != nil {
			return nil, err
		}
		entries = append(entries, fe)
	}
	return entries, nil
}
