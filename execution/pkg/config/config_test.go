package config

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			"NodeType": "STORAGE_LOCAL",
			"AccountState": {
				"Path": "account_state",
				"ListenAddress": "0.0.0.0:5000"
			}
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
	assert.Equal(t, STORAGE_LOCAL, cfg.Databases.NodeType)
	assert.Equal(t, "account_state", cfg.Databases.AccountState.Path)
	assert.Equal(t, "0.0.0.0:4201", cfg.Nodes.MasterAddress)
	assert.True(t, cfg.Nodes.NetworkSyncEnabled)
}

func TestDatabasesConfig_Defaults(t *testing.T) {
	configJSON := `{
		"RootPath": "./data",
		"NodeType": "STORAGE_REMOTE"
	}`

	var db DatabasesConfig
	err := json.Unmarshal([]byte(configJSON), &db)
	require.NoError(t, err)
	assert.Equal(t, STORAGE_REMOTE, db.NodeType)
	assert.Equal(t, "./data", db.RootPath)
}

func TestNodeType_Constants(t *testing.T) {
	assert.Equal(t, NodeType("STORAGE_REMOTE"), STORAGE_REMOTE)
	assert.Equal(t, NodeType("STORAGE_CLIENT"), STORAGE_CLIENT)
	assert.Equal(t, NodeType("STORAGE_LOCAL"), STORAGE_LOCAL)
}
