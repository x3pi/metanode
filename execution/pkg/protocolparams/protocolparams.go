// Package protocolparams collects the node settings that decide how a block executes or whether it is accepted, and makes
// it possible to prove that every validator of a chain runs with the same ones.
//
// These settings live in each node's config.json, and the code assumes they are identical on every validator (see
// sigPolicy in pkg/blockchain/tx_processor/signature_enforcement.go) without anything enforcing it. This package does not
// change any rule. It only
//   - computes a canonical fingerprint of the settings, per field and overall (P0: compare across a cluster), and
//   - checks the settings against the optional protocol section of genesis.json and reports every difference (P1: a node
//     that disagrees with its chain must not start).
//
// Nothing here may influence execution; it must stay a pure function of (config, genesis).
package protocolparams

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
)

// defaultRaftMaxBatchBytes must equal the raftfeed default (pinned by a test); protocolparams cannot import raftfeed.
const defaultRaftMaxBatchBytes = 4 << 20

// CrossChainParams are the cross_chain settings that the gateway copies into its on-chain state on first use.
type CrossChainParams struct {
	ReserveChainID               uint64 `json:"reserve_chain_id"`
	BondLeverage                 uint64 `json:"bond_leverage"`
	UnbondingPeriodSeconds       uint64 `json:"unbonding_period_seconds"`
	MinSecurityBondToRegisterWei string `json:"min_security_bond_to_register_wei"`
	MinNativeStakeToRegisterWei  string `json:"min_native_stake_to_register_wei"`
	VelocityLimitDisabled        bool   `json:"velocity_limit_disabled"`
}

// Params is the normalized view of the settings. Every value is public chain configuration; none is a secret.
type Params struct {
	// Declared in genesis.protocol and enforced at startup (strict).
	TxSignatureMode  string           `json:"tx_signature_mode"` // "secp" or "default"
	AccountGate      string           `json:"account_gate"`      // "parent_registered" or "off"
	FreeFeeAddresses []string         `json:"free_fee_addresses"`
	CrossChain       CrossChainParams `json:"cross_chain"`

	// Fingerprinted only: they matter, but either come from genesis already or their execution path is not established.
	ChainID              uint64 `json:"chain_id"`
	VerifyDeviceKey      bool   `json:"verify_device_key"`
	StateBackend         string `json:"state_backend"`
	AttestationInterval  uint64 `json:"attestation_interval"`
	EpochDurationSeconds uint64 `json:"epoch_duration_seconds"`
	RaftMaxBatchBytes    int    `json:"raft_max_batch_bytes"`
}

// Mismatch is one declared genesis value that differs from the node's own setting.
type Mismatch struct {
	Field   string `json:"field"`
	Local   string `json:"local"`
	Genesis string `json:"genesis"`
}

func (m Mismatch) String() string {
	return fmt.Sprintf("%s: config.json has %s, genesis protocol section declares %s", m.Field, m.Local, m.Genesis)
}

// Report is what a node publishes (log, RPC) so a cluster can be compared without sharing files.
type Report struct {
	Fingerprint    string            `json:"fingerprint"`     // sha256 over the canonical Params
	Fields         map[string]string `json:"fields"`          // field name -> short hash of its value, to pinpoint a difference
	Params         Params            `json:"params"`          // the values themselves
	GenesisSection bool              `json:"genesis_section"` // true when genesis declares a protocol section (and it matched)
}

// FromConfig normalizes cfg and genesis into Params. Either may be nil.
func FromConfig(cfg *config.SimpleChainConfig, g *config.GenesisConfig) Params {
	var p Params
	p.TxSignatureMode, p.AccountGate = "default", "off"
	p.FreeFeeAddresses = []string{}
	if cfg != nil {
		if cfg.SecpOnlyTxSignatures() {
			p.TxSignatureMode = config.TxSignatureModeSecp
		}
		if cfg.AccountGateParentRegistered() {
			p.AccountGate = config.AccountGateParentRegistered
		}
		// the loader (App.loadFreeFeeAddresses) only accepts 40-character entries, as given in config.json (no 0x)
		for _, a := range cfg.FreeFeeAddresses {
			if len(a) == 40 {
				p.FreeFeeAddresses = append(p.FreeFeeAddresses, strings.ToLower(a))
			}
		}
		p.FreeFeeAddresses = sortedUnique(p.FreeFeeAddresses)
		cc := cfg.CrossChain
		p.CrossChain = CrossChainParams{
			ReserveChainID: cc.ReserveChainID, BondLeverage: cc.BondLeverage, UnbondingPeriodSeconds: cc.UnbondingPeriodSeconds,
			MinSecurityBondToRegisterWei: cc.MinSecurityBondToRegisterWei, MinNativeStakeToRegisterWei: cc.MinNativeStakeToRegisterWei,
			VelocityLimitDisabled: cc.VelocityLimitDisabled,
		}
		p.VerifyDeviceKey = cfg.VerifyDeviceKey
		p.StateBackend = strings.ToLower(cfg.StateBackend)
		if cfg.Raft != nil {
			// an unset limit means the default, so "unset" and "explicitly the default" are the same setting
			p.RaftMaxBatchBytes = cfg.Raft.MaxBatchBytes
			if p.RaftMaxBatchBytes <= 0 {
				p.RaftMaxBatchBytes = defaultRaftMaxBatchBytes
			}
		}
	}
	if g != nil {
		if g.ChainId != nil && g.ChainId.IsUint64() {
			p.ChainID = g.ChainId.Uint64()
		}
		p.AttestationInterval = g.AttestationInterval
		p.EpochDurationSeconds = g.EpochDurationSeconds
	}
	return p
}

func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	sort.Strings(in)
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

type namedField struct {
	name  string
	value interface{}
}

// fields is the one list that defines what is fingerprinted. Add a setting here and it is fingerprinted and compared.
func (p Params) fields() []namedField {
	return []namedField{
		{"tx_signature_mode", p.TxSignatureMode},
		{"account_gate", p.AccountGate},
		{"free_fee_addresses", p.FreeFeeAddresses},
		{"cross_chain.reserve_chain_id", p.CrossChain.ReserveChainID},
		{"cross_chain.bond_leverage", p.CrossChain.BondLeverage},
		{"cross_chain.unbonding_period_seconds", p.CrossChain.UnbondingPeriodSeconds},
		{"cross_chain.min_security_bond_to_register_wei", p.CrossChain.MinSecurityBondToRegisterWei},
		{"cross_chain.min_native_stake_to_register_wei", p.CrossChain.MinNativeStakeToRegisterWei},
		{"cross_chain.velocity_limit_disabled", p.CrossChain.VelocityLimitDisabled},
		{"chain_id", p.ChainID},
		{"verify_device_key", p.VerifyDeviceKey},
		{"state_backend", p.StateBackend},
		{"attestation_interval", p.AttestationInterval},
		{"epoch_duration_seconds", p.EpochDurationSeconds},
		{"raft_max_batch_bytes", p.RaftMaxBatchBytes},
	}
}

// Value returns the value of a fingerprinted field by name, to show what differs between two nodes.
func (p Params) Value(field string) (interface{}, bool) {
	for _, f := range p.fields() {
		if f.name == field {
			return f.value, true
		}
	}
	return nil, false
}

func hashOf(v interface{}) string {
	b, _ := json.Marshal(v) // plain values and string slices cannot fail to marshal
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Fingerprint is the sha256 over every fingerprinted field, in the fixed order of fields().
func (p Params) Fingerprint() string {
	h := sha256.New()
	for _, f := range p.fields() {
		fmt.Fprintf(h, "%s=%s\n", f.name, hashOf(f.value))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Report builds the publishable view of p.
func (p Params) Report(genesisSection bool) Report {
	r := Report{Fingerprint: p.Fingerprint(), Fields: map[string]string{}, Params: p, GenesisSection: genesisSection}
	for _, f := range p.fields() {
		r.Fields[f.name] = hashOf(f.value)[:16]
	}
	return r
}

// DiffFields lists the field names whose value differs between two reports, sorted.
func DiffFields(a, b Report) []string {
	var out []string
	for name, ha := range a.Fields {
		if hb, ok := b.Fields[name]; !ok || ha != hb {
			out = append(out, name)
		}
	}
	for name := range b.Fields {
		if _, ok := a.Fields[name]; !ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func normMode(s string) string {
	if s == config.TxSignatureModeSecp {
		return s
	}
	return "default"
}

func normGate(s string) string {
	if s == config.AccountGateParentRegistered {
		return s
	}
	return "off"
}

func normAddrs(in []string) []string {
	var out []string
	for _, a := range in {
		a = strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(a, "0x"), "0X"))
		if len(a) == 40 {
			out = append(out, a)
		}
	}
	return sortedUnique(out)
}

func mm(field string, local, genesis interface{}) Mismatch {
	return Mismatch{Field: field, Local: fmt.Sprint(local), Genesis: fmt.Sprint(genesis)}
}

// CheckGenesis returns every declared field of the genesis protocol section that differs from p. A nil section declares
// nothing and returns no mismatch.
func CheckGenesis(p Params, s *config.ProtocolSection) []Mismatch {
	if s == nil {
		return nil
	}
	var out []Mismatch
	if s.TxSignatureMode != nil && normMode(*s.TxSignatureMode) != p.TxSignatureMode {
		out = append(out, mm("tx_signature_mode", p.TxSignatureMode, normMode(*s.TxSignatureMode)))
	}
	if s.AccountGate != nil && normGate(*s.AccountGate) != p.AccountGate {
		out = append(out, mm("account_gate", p.AccountGate, normGate(*s.AccountGate)))
	}
	if s.FreeFeeAddresses != nil {
		want := normAddrs(s.FreeFeeAddresses)
		if strings.Join(want, ",") != strings.Join(p.FreeFeeAddresses, ",") {
			out = append(out, mm("free_fee_addresses", p.FreeFeeAddresses, want))
		}
	}
	if c := s.CrossChain; c != nil {
		l := p.CrossChain
		if c.ReserveChainID != nil && *c.ReserveChainID != l.ReserveChainID {
			out = append(out, mm("cross_chain.reserve_chain_id", l.ReserveChainID, *c.ReserveChainID))
		}
		if c.BondLeverage != nil && *c.BondLeverage != l.BondLeverage {
			out = append(out, mm("cross_chain.bond_leverage", l.BondLeverage, *c.BondLeverage))
		}
		if c.UnbondingPeriodSeconds != nil && *c.UnbondingPeriodSeconds != l.UnbondingPeriodSeconds {
			out = append(out, mm("cross_chain.unbonding_period_seconds", l.UnbondingPeriodSeconds, *c.UnbondingPeriodSeconds))
		}
		if c.MinSecurityBondToRegisterWei != nil && *c.MinSecurityBondToRegisterWei != l.MinSecurityBondToRegisterWei {
			out = append(out, mm("cross_chain.min_security_bond_to_register_wei", l.MinSecurityBondToRegisterWei, *c.MinSecurityBondToRegisterWei))
		}
		if c.MinNativeStakeToRegisterWei != nil && *c.MinNativeStakeToRegisterWei != l.MinNativeStakeToRegisterWei {
			out = append(out, mm("cross_chain.min_native_stake_to_register_wei", l.MinNativeStakeToRegisterWei, *c.MinNativeStakeToRegisterWei))
		}
		if c.VelocityLimitDisabled != nil && *c.VelocityLimitDisabled != l.VelocityLimitDisabled {
			out = append(out, mm("cross_chain.velocity_limit_disabled", l.VelocityLimitDisabled, *c.VelocityLimitDisabled))
		}
	}
	return out
}

// Section turns p's strict fields into a genesis protocol section, to be pasted into genesis.json before its digest is
// published (see config.GenesisConfig.Protocol).
func (p Params) Section() *config.ProtocolSection {
	mode, gate := p.TxSignatureMode, p.AccountGate
	cc := p.CrossChain
	return &config.ProtocolSection{
		TxSignatureMode:  &mode,
		AccountGate:      &gate,
		FreeFeeAddresses: append([]string{}, p.FreeFeeAddresses...),
		CrossChain: &config.ProtocolCrossChain{
			ReserveChainID: &cc.ReserveChainID, BondLeverage: &cc.BondLeverage, UnbondingPeriodSeconds: &cc.UnbondingPeriodSeconds,
			MinSecurityBondToRegisterWei: &cc.MinSecurityBondToRegisterWei, MinNativeStakeToRegisterWei: &cc.MinNativeStakeToRegisterWei,
			VelocityLimitDisabled: &cc.VelocityLimitDisabled,
		},
	}
}

// VerifyAgainstGenesis is the startup check. It always returns the node's report. It returns an error only when genesis
// declares a protocol section and this node's config.json disagrees with any declared field: such a node would execute
// blocks by different rules from its chain, so it must not start.
func VerifyAgainstGenesis(cfg *config.SimpleChainConfig, g *config.GenesisData) (Report, error) {
	var gc *config.GenesisConfig
	if g != nil {
		gc = &g.Config
	}
	p := FromConfig(cfg, gc)
	if gc == nil || gc.Protocol == nil {
		return p.Report(false), nil
	}
	if bad := CheckGenesis(p, gc.Protocol); len(bad) > 0 {
		lines := make([]string, len(bad))
		for i, m := range bad {
			lines[i] = "  - " + m.String()
		}
		return p.Report(false), fmt.Errorf("config.json disagrees with the chain's genesis protocol section (%d field(s)); refusing to start because blocks would execute by different rules:\n%s",
			len(bad), strings.Join(lines, "\n"))
	}
	return p.Report(true), nil
}
