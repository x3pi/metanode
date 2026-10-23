package protocolparams

import (
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
)

func baseConfig() *config.SimpleChainConfig {
	return &config.SimpleChainConfig{
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
		FreeFeeAddresses: []string{
			"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		StateBackend: "nomt",
		CrossChain:   config.CrossChainConfig{ReserveChainID: 991, BondLeverage: 5, UnbondingPeriodSeconds: 3600, MinSecurityBondToRegisterWei: "100", MinNativeStakeToRegisterWei: "200"},
		Raft:         &config.RaftConfig{MaxBatchBytes: 4 << 20},
	}
}

func baseGenesis() *config.GenesisConfig {
	return &config.GenesisConfig{ChainId: big.NewInt(991), AttestationInterval: 10, EpochDurationSeconds: 900}
}

// every fingerprinted setting, with a mutation that changes only it
var mutations = map[string]func(c *config.SimpleChainConfig, g *config.GenesisConfig){
	"tx_signature_mode": func(c *config.SimpleChainConfig, g *config.GenesisConfig) { c.TxSignatureMode = "" },
	"account_gate":      func(c *config.SimpleChainConfig, g *config.GenesisConfig) { c.AccountGate = "" },
	"free_fee_addresses": func(c *config.SimpleChainConfig, g *config.GenesisConfig) {
		c.FreeFeeAddresses = c.FreeFeeAddresses[:1]
	},
	"cross_chain.reserve_chain_id":         func(c *config.SimpleChainConfig, g *config.GenesisConfig) { c.CrossChain.ReserveChainID++ },
	"cross_chain.bond_leverage":            func(c *config.SimpleChainConfig, g *config.GenesisConfig) { c.CrossChain.BondLeverage++ },
	"cross_chain.unbonding_period_seconds": func(c *config.SimpleChainConfig, g *config.GenesisConfig) { c.CrossChain.UnbondingPeriodSeconds++ },
	"cross_chain.min_security_bond_to_register_wei": func(c *config.SimpleChainConfig, g *config.GenesisConfig) {
		c.CrossChain.MinSecurityBondToRegisterWei = "101"
	},
	"cross_chain.min_native_stake_to_register_wei": func(c *config.SimpleChainConfig, g *config.GenesisConfig) {
		c.CrossChain.MinNativeStakeToRegisterWei = "201"
	},
	"cross_chain.velocity_limit_disabled": func(c *config.SimpleChainConfig, g *config.GenesisConfig) { c.CrossChain.VelocityLimitDisabled = true },
	"chain_id":                            func(c *config.SimpleChainConfig, g *config.GenesisConfig) { g.ChainId = big.NewInt(992) },
	"verify_device_key":                   func(c *config.SimpleChainConfig, g *config.GenesisConfig) { c.VerifyDeviceKey = true },
	"state_backend":                       func(c *config.SimpleChainConfig, g *config.GenesisConfig) { c.StateBackend = "mpt" },
	"attestation_interval":                func(c *config.SimpleChainConfig, g *config.GenesisConfig) { g.AttestationInterval = 11 },
	"epoch_duration_seconds":              func(c *config.SimpleChainConfig, g *config.GenesisConfig) { g.EpochDurationSeconds = 901 },
	"raft_max_batch_bytes":                func(c *config.SimpleChainConfig, g *config.GenesisConfig) { c.Raft.MaxBatchBytes = 8 << 20 },
}

func TestEveryFieldIsFingerprintedAndAMutationExistsForIt(t *testing.T) {
	// a setting added to Params but forgotten in fields() would silently escape the fingerprint
	leaves := 0
	rt := reflect.TypeOf(Params{})
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).Type.Kind() == reflect.Struct {
			leaves += rt.Field(i).Type.NumField()
		} else {
			leaves++
		}
	}
	got := baseParams().fields()
	if len(got) != leaves {
		t.Fatalf("Params has %d settings but fields() lists %d: add the new setting to fields()", leaves, len(got))
	}
	for _, f := range got {
		if mutations[f.name] == nil {
			t.Errorf("no mutation test for fingerprinted field %q", f.name)
		}
	}
}

func baseParams() Params { return FromConfig(baseConfig(), baseGenesis()) }

func TestFingerprintIsStableAndIgnoresLocalOperationalSettings(t *testing.T) {
	a := baseParams().Fingerprint()
	c := baseConfig()
	c.RpcPort, c.LogPath, c.BackupPath, c.ConnectionAddress, c.MinGasPrice = ":9999", "/other/logs", "/other/backup", "0.0.0.0:1", 77
	c.RpcRateLimit.GlobalRate = 1
	c.Pruning = config.PruningConfig{}
	c.SnapshotEnabled = true
	if b := FromConfig(c, baseGenesis()).Fingerprint(); a != b {
		t.Fatal("ports, paths, rate limits and snapshot settings are local policy and must not change the fingerprint")
	}
	if baseParams().Fingerprint() != a {
		t.Fatal("fingerprint is not deterministic")
	}
}

func TestFreeFeeAddressesAreNormalized(t *testing.T) {
	c1, c2 := baseConfig(), baseConfig()
	c1.FreeFeeAddresses = []string{"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	c2.FreeFeeAddresses = []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "not-an-address"}
	if FromConfig(c1, baseGenesis()).Fingerprint() != FromConfig(c2, baseGenesis()).Fingerprint() {
		t.Fatal("order, case, duplicates and entries the loader ignores must not change the fingerprint")
	}
}

func TestEachSettingChangesTheFingerprintAndOnlyItsOwnField(t *testing.T) {
	base := baseParams().Report(false)
	for name, mutate := range mutations {
		c, g := baseConfig(), baseGenesis()
		mutate(c, g)
		got := FromConfig(c, g).Report(false)
		if got.Fingerprint == base.Fingerprint {
			t.Errorf("%s: fingerprint did not change", name)
		}
		if d := DiffFields(base, got); len(d) != 1 || d[0] != name {
			t.Errorf("%s: DiffFields = %v, want exactly [%s]", name, d, name)
		}
	}
}

func TestGenesisCheck(t *testing.T) {
	p := baseParams()
	if bad := CheckGenesis(p, nil); bad != nil {
		t.Fatalf("no section declares nothing, got %v", bad)
	}
	if bad := CheckGenesis(p, p.Section()); len(bad) != 0 {
		t.Fatalf("a section generated from the same settings must match, got %v", bad)
	}
	if bad := CheckGenesis(p, &config.ProtocolSection{}); len(bad) != 0 {
		t.Fatalf("an empty section declares nothing, got %v", bad)
	}

	str := func(s string) *string { return &s }
	u64 := func(v uint64) *uint64 { return &v }
	boolp := func(v bool) *bool { return &v }
	cases := map[string]*config.ProtocolSection{
		"tx_signature_mode":                             {TxSignatureMode: str("default")},
		"account_gate":                                  {AccountGate: str("off")},
		"free_fee_addresses":                            {FreeFeeAddresses: []string{}},
		"cross_chain.reserve_chain_id":                  {CrossChain: &config.ProtocolCrossChain{ReserveChainID: u64(1)}},
		"cross_chain.bond_leverage":                     {CrossChain: &config.ProtocolCrossChain{BondLeverage: u64(1)}},
		"cross_chain.unbonding_period_seconds":          {CrossChain: &config.ProtocolCrossChain{UnbondingPeriodSeconds: u64(1)}},
		"cross_chain.min_security_bond_to_register_wei": {CrossChain: &config.ProtocolCrossChain{MinSecurityBondToRegisterWei: str("1")}},
		"cross_chain.min_native_stake_to_register_wei":  {CrossChain: &config.ProtocolCrossChain{MinNativeStakeToRegisterWei: str("1")}},
		"cross_chain.velocity_limit_disabled":           {CrossChain: &config.ProtocolCrossChain{VelocityLimitDisabled: boolp(true)}},
	}
	for field, sec := range cases {
		bad := CheckGenesis(p, sec)
		if len(bad) != 1 || bad[0].Field != field {
			t.Errorf("declaring a different %s: got %v", field, bad)
		}
	}

	// addresses may be written with 0x and in any case
	sec := &config.ProtocolSection{FreeFeeAddresses: []string{"0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	if bad := CheckGenesis(p, sec); len(bad) != 0 {
		t.Fatalf("0x prefix and case must not matter, got %v", bad)
	}
}

func TestVerifyAgainstGenesis(t *testing.T) {
	cfg, g := baseConfig(), &config.GenesisData{Config: *baseGenesis()}

	rep, err := VerifyAgainstGenesis(cfg, g)
	if err != nil || rep.GenesisSection {
		t.Fatalf("a genesis without a protocol section must start (GenesisSection=false): rep=%v err=%v", rep.GenesisSection, err)
	}

	g.Config.Protocol = baseParams().Section()
	rep, err = VerifyAgainstGenesis(cfg, g)
	if err != nil || !rep.GenesisSection {
		t.Fatalf("matching section: rep=%v err=%v", rep.GenesisSection, err)
	}

	other := baseConfig()
	other.AccountGate, other.CrossChain.VelocityLimitDisabled = "", true
	_, err = VerifyAgainstGenesis(other, g)
	if err == nil {
		t.Fatal("a node whose config disagrees with the genesis protocol section must not start")
	}
	for _, want := range []string{"account_gate", "cross_chain.velocity_limit_disabled", "2 field(s)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

func TestGenesisFileCompatibility(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.json")
	if err := os.WriteFile(old, []byte(`{"config":{"chainId":991,"epoch":0},"validators":[],"alloc":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := config.LoadGenesisData(old)
	if err != nil || g.Config.Protocol != nil {
		t.Fatalf("a genesis written before the protocol section existed must load unchanged: %v %+v", err, g)
	}
	withSection := filepath.Join(dir, "new.json")
	if err := os.WriteFile(withSection, []byte(`{"config":{"chainId":991,"epoch":0,"protocol":{"tx_signature_mode":"secp","free_fee_addresses":[],"cross_chain":{"bond_leverage":5}}},"validators":[],"alloc":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err = config.LoadGenesisData(withSection)
	if err != nil || g.Config.Protocol == nil || *g.Config.Protocol.TxSignatureMode != "secp" ||
		g.Config.Protocol.FreeFeeAddresses == nil || len(g.Config.Protocol.FreeFeeAddresses) != 0 || *g.Config.Protocol.CrossChain.BondLeverage != 5 {
		t.Fatalf("section did not decode as declared (a declared empty list must stay non-nil): %v %+v", err, g.Config.Protocol)
	}
}

func TestUnsetRaftBatchLimitIsTheDefault(t *testing.T) {
	if defaultRaftMaxBatchBytes != raftfeed.MaxBatchBytes() {
		t.Fatalf("protocolparams default %d != raftfeed default %d: keep them equal", defaultRaftMaxBatchBytes, raftfeed.MaxBatchBytes())
	}
	unset, explicit := baseConfig(), baseConfig()
	unset.Raft.MaxBatchBytes = 0
	explicit.Raft.MaxBatchBytes = defaultRaftMaxBatchBytes
	if FromConfig(unset, baseGenesis()).Fingerprint() != FromConfig(explicit, baseGenesis()).Fingerprint() {
		t.Fatal("unset and explicitly-default max_batch_bytes behave identically and must fingerprint identically")
	}
}

// The fingerprint must carry the limit raftfeed really applies, for every kind of configured value.
func TestRaftBatchLimitIsTheEffectiveOne(t *testing.T) {
	saved := config.ConfigApp
	defer func() { config.ConfigApp = saved }()
	for _, configured := range []int{0, -1, 1, 1 << 20, defaultRaftMaxBatchBytes, maxRaftBatchBytes - 1, maxRaftBatchBytes, maxRaftBatchBytes + 1, 200 << 20} {
		cfg := baseConfig()
		cfg.Raft.MaxBatchBytes = configured
		config.ConfigApp = cfg
		if got, want := FromConfig(cfg, baseGenesis()).RaftMaxBatchBytes, raftfeed.MaxBatchBytes(); got != want {
			t.Fatalf("configured %d: fingerprint carries %d but raftfeed applies %d", configured, got, want)
		}
	}
	over, capped := baseConfig(), baseConfig()
	over.Raft.MaxBatchBytes = 200 << 20
	capped.Raft.MaxBatchBytes = maxRaftBatchBytes
	if FromConfig(over, baseGenesis()).Fingerprint() != FromConfig(capped, baseGenesis()).Fingerprint() {
		t.Fatal("a limit above the forwarding cap behaves like the cap and must fingerprint like it")
	}
}

func TestOnlyTheRaftLimitIsOperational(t *testing.T) {
	for _, f := range baseParams().fields() {
		if got, want := Operational(f.name), f.name == "raft_max_batch_bytes"; got != want {
			t.Fatalf("Operational(%q) = %v, want %v", f.name, got, want)
		}
	}
}
