package config

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/state"
)

// ──────────────────────────────────────────────
// JoinPathIfNotURL
// ──────────────────────────────────────────────

func TestJoinPathIfNotURL_RelativePath(t *testing.T) {
	result := JoinPathIfNotURL("/base/dir", "subdir/file.db")
	assert.Equal(t, filepath.Join("/base/dir", "subdir/file.db"), result)
}

func TestJoinPathIfNotURL_AbsolutePath(t *testing.T) {
	result := JoinPathIfNotURL("/base/dir", "/absolute/path/file.db")
	assert.Equal(t, filepath.Join("/base/dir", "/absolute/path/file.db"), result)
}

func TestJoinPathIfNotURL_EmptyPath(t *testing.T) {
	result := JoinPathIfNotURL("/base/dir", "")
	assert.Equal(t, "/base/dir", result)
}

// ──────────────────────────────────────────────
// LoadGenesisData
// ──────────────────────────────────────────────

func TestLoadGenesisData_ValidFile(t *testing.T) {
	genesisJSON := map[string]interface{}{
		"config": map[string]interface{}{
			"chainId": 1000,
			"epoch":   5,
		},
		"validators": []interface{}{},
		"alloc":      []interface{}{},
	}

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "genesis.json")

	data, err := json.Marshal(genesisJSON)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filePath, data, 0644))

	genesis, err := LoadGenesisData(filePath)
	require.NoError(t, err)
	require.NotNil(t, genesis)

	assert.Equal(t, big.NewInt(1000), genesis.Config.ChainId)
	assert.Equal(t, 5, genesis.Config.Epoch)
	assert.Empty(t, genesis.Validators)
	assert.Empty(t, genesis.Alloc)
}

// TestLoadGenesisData_RootAnchor used to read a real, machine-local devnet genesis file
// (deploy/systemd/root_anchor_data/genesis.json — gitignored, never committed) via a
// hardcoded absolute path specific to one developer's machine. That made the test
// non-hermetic: it only ever passed by accident, on a machine that happened to have run
// gen_root_anchor_chain.py at that exact path, and failed on any fresh checkout including
// CI's (confirmed: CI's "go-build-test" job failed on this exact test — a real, previously
// undetected gap in the test suite, found when a devnet cleanup on a dev machine removed the
// file this test silently depended on). Rebuilt as a real, self-contained fixture using the
// exact same real BLS pubkey value the original file had, preserving the actual regression
// coverage (PublicKeyBls hex parsing through ToAccountState producing a real 48-byte BLS12-381
// G1 pubkey) without depending on any file outside the test itself.
func TestLoadGenesisData_RootAnchor(t *testing.T) {
	const testAddress = "0x7d8bfbaba9268b59bab9ef8ff3f314d3f5747366"
	const testPublicKeyBls = "0x86d5de6f7c9c13cc0d959a553cc0e4853ba5faae45a28da9bddc8ef8e104eb5d3dece8dfaa24f11b4243ec27537e3184"

	genesisJSON := map[string]interface{}{
		"config": map[string]interface{}{
			"chainId": 9099,
			"epoch":   0,
		},
		"validators": []interface{}{},
		"alloc": []interface{}{
			map[string]interface{}{
				"address":      testAddress,
				"publicKeyBls": testPublicKeyBls,
			},
		},
	}

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "genesis.json")
	data, err := json.Marshal(genesisJSON)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filePath, data, 0644))

	genesis, err := LoadGenesisData(filePath)
	require.NoError(t, err)
	require.NotNil(t, genesis)

	var targetAlloc *state.JsonAccountState
	for _, a := range genesis.Alloc {
		if a.Address == testAddress {
			targetAlloc = &a
			break
		}
	}
	require.NotNil(t, targetAlloc, "account %s must exist in genesis alloc", testAddress)
	assert.Equal(t, testPublicKeyBls, targetAlloc.PublicKeyBls)

	as := targetAlloc.ToAccountState()
	assert.NotEmpty(t, as.PublicKeyBls(), "ToAccountState must parse PublicKeyBls")
	assert.Len(t, as.PublicKeyBls(), 48, "a real BLS12-381 G1 public key must decode to exactly 48 bytes")
}

func TestLoadGenesisData_WithValidators(t *testing.T) {
	genesisJSON := `{
		"config": {
			"chainId": 2000,
			"epoch": 1
		},
		"validators": [
			{
				"name": "validator1",
				"address": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb"
			}
		],
		"alloc": []
	}`

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "genesis.json")
	require.NoError(t, os.WriteFile(filePath, []byte(genesisJSON), 0644))

	genesis, err := LoadGenesisData(filePath)
	require.NoError(t, err)
	require.NotNil(t, genesis)

	assert.Equal(t, big.NewInt(2000), genesis.Config.ChainId)
	require.Len(t, genesis.Validators, 1)
	assert.Equal(t, "validator1", genesis.Validators[0].Name)
	assert.Equal(t, "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb", genesis.Validators[0].Address)
}

func TestLoadGenesisData_MissingFile(t *testing.T) {
	_, err := LoadGenesisData("/nonexistent/path/genesis.json")
	assert.Error(t, err)
}

func TestLoadGenesisData_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "bad.json")
	require.NoError(t, os.WriteFile(filePath, []byte("not valid json{{{"), 0644))

	_, err := LoadGenesisData(filePath)
	assert.Error(t, err)
}

func TestLoadGenesisData_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "empty.json")
	require.NoError(t, os.WriteFile(filePath, []byte("{}"), 0644))

	genesis, err := LoadGenesisData(filePath)
	require.NoError(t, err)
	require.NotNil(t, genesis)
	assert.Nil(t, genesis.Config.ChainId)
}

// ──────────────────────────────────────────────
// Config structs
// ──────────────────────────────────────────────

func TestSimpleChainConfig_JSONParsing(t *testing.T) {
	configJSON := `{
		"debug": true,
		"mode": "single_node",
		"chainId": 1000,
		"service_type": "MASTER",
		"rpc_port": ":8747",
		"genesis_file_path": "genesis.json",
		"state_backend": "flat",
		"Databases": {
			"RootPath": "./data",
			"NodeType": "STORAGE_LOCAL"
		},
		"nodes": {
			"master_address": "0.0.0.0:4201",
			"network_sync_enabled": true
		}
	}`

	var cfg SimpleChainConfig
	err := json.Unmarshal([]byte(configJSON), &cfg)
	require.NoError(t, err)

	assert.True(t, cfg.Debug)

	assert.Equal(t, big.NewInt(1000), cfg.ChainId)
	assert.Equal(t, ":8747", cfg.RpcPort)
	assert.Equal(t, "genesis.json", cfg.GenesisFilePath)
	assert.Equal(t, "flat", cfg.StateBackend)
	assert.Equal(t, "./data", cfg.Databases.RootPath)
	assert.Equal(t, "0.0.0.0:4201", cfg.Nodes.MasterAddress)
	assert.True(t, cfg.Nodes.NetworkSyncEnabled)
}

func TestDatabasesConfig_Defaults(t *testing.T) {
	configJSON := `{
		"RootPath": "./data"
	}`

	var db DatabasesConfig
	err := json.Unmarshal([]byte(configJSON), &db)
	require.NoError(t, err)
	assert.Equal(t, "./data", db.RootPath)
}

func TestTxSignatureMode(t *testing.T) {
	chain := big.NewInt(991)
	for _, tc := range []struct {
		mode    string
		chainID *big.Int
		secp    bool
		wantErr bool
	}{
		{"", nil, false, false},
		{TxSignatureModeBLSLegacy, nil, false, false},
		{TxSignatureModeSecp, chain, true, false},
		{TxSignatureModeSecp, nil, true, true},           // secp replay protection needs a configured chain ID
		{TxSignatureModeSecp, big.NewInt(0), true, true}, // ...and a positive one
		{"SECP", chain, false, true},                     // typos must fail startup, never silently pick another rule set
		{"bls", chain, false, true},
	} {
		c := &SimpleChainConfig{TxSignatureMode: tc.mode, ChainId: tc.chainID}
		if got := c.SecpOnlyTxSignatures(); got != tc.secp {
			t.Errorf("mode %q: SecpOnlyTxSignatures()=%v, want %v", tc.mode, got, tc.secp)
		}
		if err := c.validateTxSignatureMode(); (err != nil) != tc.wantErr {
			t.Errorf("mode %q chain %v: validate err=%v, wantErr=%v", tc.mode, tc.chainID, err, tc.wantErr)
		}
	}
	var nilCfg *SimpleChainConfig
	if nilCfg.SecpOnlyTxSignatures() {
		t.Error("nil config must be legacy")
	}
}

func TestAccountGate(t *testing.T) {
	for _, tc := range []struct {
		gate        string
		sigMode     string
		registered  bool
		wantErr     bool
		description string
	}{
		{"", "", false, false, "empty gate is valid (disabled)"},
		{AccountGateOff, "", false, false, "off gate is valid (disabled)"},
		{AccountGateParentRegistered, TxSignatureModeSecp, true, false, "parent_registered in secp mode is valid"},
		{AccountGateParentRegistered, "", true, true, "parent_registered in empty (legacy) mode must error"},
		{AccountGateParentRegistered, TxSignatureModeBLSLegacy, true, true, "parent_registered in bls_legacy mode must error"},
		{"parent", TxSignatureModeSecp, false, true, "typo/unknown gate must error"},
		{"REGISTERED", TxSignatureModeSecp, false, true, "uppercase/unknown gate must error"},
	} {
		c := &SimpleChainConfig{AccountGate: tc.gate, TxSignatureMode: tc.sigMode}
		if got := c.AccountGateParentRegistered(); got != tc.registered {
			t.Errorf("[%s] AccountGateParentRegistered()=%v, want %v", tc.description, got, tc.registered)
		}
		if err := c.validateAccountGate(); (err != nil) != tc.wantErr {
			t.Errorf("[%s] validateAccountGate() err=%v, wantErr=%v", tc.description, err, tc.wantErr)
		}
	}
	var nilCfg *SimpleChainConfig
	if nilCfg.AccountGateParentRegistered() {
		t.Error("nil config must report false for AccountGateParentRegistered")
	}
}

