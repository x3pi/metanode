package main

import (
	"math/big"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
)

// Genesis accounts may be flagged ParentRegistered ONLY when the account gate is on: otherwise the genesis state of a
// chain that does not use the gate would change (every alloc account would encode an extra field), altering the
// genesis root and breaking startup integrity checks and compatibility with older binaries.
func TestGenesisAccountsRegistered_OnlyWhenGateEnabled(t *testing.T) {
	cases := []struct {
		name string
		app  *App
		want bool
	}{
		{"nil config", &App{}, false},
		{"legacy chain", &App{config: &config.SimpleChainConfig{ChainId: big.NewInt(991)}}, false},
		{"secp mode, gate off", &App{config: &config.SimpleChainConfig{ChainId: big.NewInt(991), TxSignatureMode: config.TxSignatureModeSecp}}, false},
		{"secp mode, gate on", &App{config: &config.SimpleChainConfig{ChainId: big.NewInt(991), TxSignatureMode: config.TxSignatureModeSecp, AccountGate: config.AccountGateParentRegistered}}, true},
	}
	for _, c := range cases {
		if got := c.app.genesisAccountsRegistered(); got != c.want {
			t.Errorf("%s: genesisAccountsRegistered()=%v, want %v", c.name, got, c.want)
		}
	}
}
