// Package ceremony implements the Root Anchor genesis ceremony (Milestone D of
// note/cross_chain_root_anchor_architecture.md, section 1.3 #5 / 5.2.1 / 14
// P1.1-P1.2): each founding private chain generates its own validator key
// material locally and publishes a "founding_entry.json" containing only
// public data; a coordinator gathers >= 4 of those and assembles a genesis.json
// for the new Root Anchor network.
//
// This package is the single source of truth for both the founding_entry.json
// and assembled genesis.json schemas, shared by the
// execution/cmd/tool/founding_entry and execution/cmd/tool/assemble_root_anchor
// CLI tools so the two never drift apart.
package ceremony

// SchemaVersion is the founding_entry.json format version this package reads/writes.
const SchemaVersion = 1

// DelegatorStake mirrors one entry of genesis.json's validators[].delegator_stakes.
// This field is NOT part of pkg/proto.Validator (the protobuf message) — the Go
// execution layer recovers it via a second raw-JSON pass at genesis load time
// (execution/cmd/simple_chain/app_blockchain.go:1069-1099).
type DelegatorStake struct {
	Address string `json:"address"`
	Amount  string `json:"amount"`
}

// GenesisValidatorEntry mirrors the fields the Go execution layer expects in
// genesis.json's "validators[]" (pkg/proto.Validator, JSON-tagged, see
// execution/pkg/proto/validator.pb.go), plus delegator_stakes (see above).
type GenesisValidatorEntry struct {
	Address                    string           `json:"address"`
	Hostname                   string           `json:"hostname"`
	Description                string           `json:"description"`
	Website                    string           `json:"website"`
	Image                      string           `json:"image"`
	CommissionRate             uint64           `json:"commission_rate"`
	MinSelfDelegation          string           `json:"min_self_delegation"`
	AccumulatedRewardsPerShare string           `json:"accumulated_rewards_per_share"`
	DelegatorStakes            []DelegatorStake `json:"delegator_stakes"`
	TotalStakedAmount          string           `json:"total_staked_amount"`
	NetworkKey                 string           `json:"network_key"`   // base64, Ed25519 pub, 32B
	AuthorityKey               string           `json:"authority_key"` // base64, BLS min-sig (fastcrypto) pub, 96B — Rust consensus committee key
	ProtocolKey                string           `json:"protocol_key"`  // base64, Ed25519 pub, 32B
	PrimaryAddress             string           `json:"primary_address"`
	WorkerAddress              string           `json:"worker_address"`
	P2PAddress                 string           `json:"p2p_address"`
}

// GenesisAllocEntry mirrors state.JsonAccountState (execution/pkg/state/account_state.go).
type GenesisAllocEntry struct {
	Address        string `json:"address"`
	Balance        string `json:"balance"`
	PendingBalance string `json:"pending_balance"`
	LastHash       string `json:"last_hash"`
	DeviceKey      string `json:"device_key"`
	PublicKeyBls   string `json:"publicKeyBls"` // hex, 0x-prefixed, blst min-pk (G1) pub, 48B — Go execution key
	AccountType    int32  `json:"accountType"`
}

// CrossChainEntry is the entry consumed by cross_chain.NewRootAnchorCommittee
// (execution/pkg/cross_chain/root_anchor.go) — Section 1.3 #5 committee
// aggregation. PubkeyBLS is deliberately the SAME min-pk (G1) key as
// GenesisAllocEntry.PublicKeyBls: both are execution-layer (Go) BLS keys, the
// single scheme chosen for ChainRegistry per the Milestone D decision (Rust's
// min-sig types/pop.rs is reconciled later, in Milestone C).
type CrossChainEntry struct {
	PubkeyBLS    string `json:"pubkey_bls"`    // hex, 0x-prefixed, 48B
	PopSignature string `json:"pop_signature"` // hex, 0x-prefixed, 96B
	Stake        uint64 `json:"stake"`
}

// FoundingEntry is the ONLY artifact a founding-chain operator publishes.
// Every field is public information; no private key material appears here —
// enforced by a regression test (TestBuildFoundingEntry_NoPrivateKeyLeakage).
type FoundingEntry struct {
	SchemaVersion    int                   `json:"schema_version"`
	ChainID          uint64                `json:"chain_id"`
	ChainName        string                `json:"chain_name"`
	GenesisValidator GenesisValidatorEntry `json:"genesis_validator"`
	GenesisAlloc     GenesisAllocEntry     `json:"genesis_alloc"`
	CrossChain       CrossChainEntry       `json:"cross_chain"`
}
