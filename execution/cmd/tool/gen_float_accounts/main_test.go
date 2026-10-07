package main

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAndNormalizeBLSPublicKey(t *testing.T) {
	validHex48 := "0x86d5de6f7c9c13cc0d959a553cc0e4853ba5faae45a28da9bddc8ef8e104eb5d3dece8dfaa24f11b4243ec27537e3184"
	norm, err := ValidateAndNormalizeBLSPublicKey(validHex48)
	if err != nil {
		t.Fatalf("expected valid key, got: %v", err)
	}
	if norm != strings.ToLower(validHex48) {
		t.Fatalf("expected %s, got %s", strings.ToLower(validHex48), norm)
	}

	// Without 0x prefix
	rawHex := strings.TrimPrefix(validHex48, "0x")
	norm2, err := ValidateAndNormalizeBLSPublicKey(rawHex)
	if err != nil {
		t.Fatalf("expected valid key without 0x, got: %v", err)
	}
	if norm2 != norm {
		t.Fatalf("expected %s, got %s", norm, norm2)
	}

	// Invalid length (47 bytes)
	invalidLen := "0x" + strings.Repeat("aa", 47)
	if _, err := ValidateAndNormalizeBLSPublicKey(invalidLen); err == nil {
		t.Fatal("expected error on 47-byte key, got nil")
	}

	// Invalid hex/base64
	if _, err := ValidateAndNormalizeBLSPublicKey("not-valid-hex-or-base64!!!"); err == nil {
		t.Fatal("expected error on invalid characters, got nil")
	}
}

func TestSumGenesisAlloc(t *testing.T) {
	tempDir := t.TempDir()
	genPath := filepath.Join(tempDir, "test_genesis.json")

	content := `{
		"alloc": [
			{
				"address": "0x1111111111111111111111111111111111111111",
				"balance": "1000000000000000000000",
				"pending_balance": "500000000000000000000"
			},
			{
				"address": "0x2222222222222222222222222222222222222222",
				"balance": "2500000000000000000000",
				"pending_balance": "0"
			}
		]
	}`

	if err := os.WriteFile(genPath, []byte(content), 0644); err != nil {
		t.Fatalf("write temp genesis: %v", err)
	}

	total, count, err := SumGenesisAlloc(genPath)
	if err != nil {
		t.Fatalf("SumGenesisAlloc failed: %v", err)
	}

	if count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}

	// 1000 + 500 + 2500 = 4000 * 10^18
	expected, _ := new(big.Int).SetString("4000000000000000000000", 10)
	if total.Cmp(expected) != 0 {
		t.Fatalf("expected sum %s, got %s", expected.String(), total.String())
	}
}

func TestProcessClustersAndFormats(t *testing.T) {
	tempDir := t.TempDir()
	gen1 := filepath.Join(tempDir, "gen1.json")
	gen2 := filepath.Join(tempDir, "gen2.json")

	c1 := `{"alloc": [{"address": "0x1", "balance": "100"}]}`
	c2 := `{"alloc": [{"address": "0x2", "balance": "200"}]}`
	_ = os.WriteFile(gen1, []byte(c1), 0644)
	_ = os.WriteFile(gen2, []byte(c2), 0644)

	k1 := "0x" + strings.Repeat("11", 48)
	k2 := "0x" + strings.Repeat("22", 48)

	specs := []ClusterSpec{
		{BLSPublicKey: k1, GenesisPath: gen1},
		{BLSPublicKey: k2, GenesisPath: gen2},
	}

	results, err := ProcessClusters(specs)
	if err != nil {
		t.Fatalf("ProcessClusters failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	jsonOut, err := FormatJSON(results)
	if err != nil {
		t.Fatalf("FormatJSON failed: %v", err)
	}
	if !strings.Contains(jsonOut, `"bls_public_key": "`+k1+`"`) || !strings.Contains(jsonOut, `"balance": "100"`) {
		t.Fatalf("JSON output missing cluster 1: %s", jsonOut)
	}

	yamlOut := FormatYAML(results)
	if !strings.Contains(yamlOut, `parent_float_accounts:`) || !strings.Contains(yamlOut, k1) {
		t.Fatalf("YAML output missing cluster 1: %s", yamlOut)
	}

	// Test PatchParentGenesis
	parentGenPath := filepath.Join(tempDir, "parent_genesis.json")
	initialParent := `{
		"chain_id": 991,
		"validators": [],
		"float_accounts": []
	}`
	_ = os.WriteFile(parentGenPath, []byte(initialParent), 0644)

	if err := PatchParentGenesis(parentGenPath, results); err != nil {
		t.Fatalf("PatchParentGenesis failed: %v", err)
	}

	patchedBytes, _ := os.ReadFile(parentGenPath)
	var patched map[string]interface{}
	_ = json.Unmarshal(patchedBytes, &patched)

	faList, ok := patched["float_accounts"].([]interface{})
	if !ok || len(faList) != 2 {
		t.Fatalf("expected 2 patched float_accounts, got: %v", patched["float_accounts"])
	}
}
