package parentchain

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestGenesis_ValidAndInvalid(t *testing.T) {
	tmpDir := t.TempDir()

	validJSON := `{
		"chain_id": 991,
		"epoch_duration_seconds": 86400,
		"validators": [
			{
				"name": "node-0",
				"address": "0x7e615e4a500ab42b7bb3fdbb62fbb8bd10385fc5",
				"stake": "1000000000000000000",
				"authority_key": "kUYrYvf/fDUygF8+nIdNATAAlnQU3BZSD3aGuHNoAZQv3OJOIZKW+Uw+UbH/1LWCAlbyWnQra9vUSDJfFVIxlV4XlraaNkLsZSb3HMJJQK3qEc1L20Yqb5YM8uGRXvnB",
				"protocol_key": "fN/BNA8PFyjE3hclyjxnkYgjFlR6M27jpbocq7X847Y=",
				"network_key": "jZ/kDNNPBsZXUD28FcxMLLZ+vCZCbEJoUvdB8zgRvug=",
				"p2p_address": "/ip4/127.0.0.1/tcp/19001"
			},
			{
				"name": "node-1",
				"address": "0x2b5ad5c4795c026514f8317c7a215e218dccd6cf",
				"stake": "1000000000000000000",
				"authority_key": "iRQSDQ6sV7V5jQVsevn8QpS11kG/0tCrN8lSuPqEcb1fDjbtRyzPFgRzY3SM8KAYEycufZ61UBQXMFKEPqAQ41hl4IOed47OQK/NZsQyuccw53CV0F89gaTELIFO/jx9",
				"protocol_key": "ngmCtN3emEfN6PD/oOtB4YoBaHRFn1M3GzhdtdnZzLw=",
				"network_key": "LFHUv5KJyfpRHkJrLGTItE8+LQI1y8su7PVrId8Dqzs=",
				"p2p_address": "/ip4/127.0.0.1/tcp/19002"
			},
			{
				"name": "node-2",
				"address": "0x6813eb9362372eef6200f3b1dbc3f819671cba69",
				"stake": "1000000000000000000",
				"authority_key": "hK+5uAd/qeLvGo98NE7OCFwYioigpG0uYXn5ocETYSrUMLbCkC+PPH6E4AtRgo5jF7aDhhUmZPX1jAppMs2g9aJzAXQVMNo7Hj5sJq5/r5ogz9HyHsMPvDKvi3HBIuq5",
				"protocol_key": "SJMLmNrKGJHkextl8SN2px1Nq1LBsj6BX6nsnb8gU8Y=",
				"network_key": "ursUG3M+DRgSL1q6oKQY0FM1EEOla67/EzAw+u75iHM=",
				"p2p_address": "/ip4/127.0.0.1/tcp/19003"
			},
			{
				"name": "node-3",
				"address": "0x1ef3613697cf40e54f45d602ee3458c425f38144",
				"stake": "1000000000000000000",
				"authority_key": "lexRcMNpxBeR/rHNqC3d2+8FOPbDzhXj1mYAdkimbQuCr1WZtSD4Z1sOS0jumEFiFovhoOugnMDIxmOPEZLg7QbzDYGjmpd2+j4ARJTsm70Ophw/q5XmPlzaJL8R0lJO",
				"protocol_key": "2EhvdGj7yPTg2fPLQOy6ytlApvgpUhgc1nxzsPEkGCA=",
				"network_key": "tmkvDNdrvCX0F6Da6n6zr36cQDej3PUirrbUYOceq4w=",
				"p2p_address": "/ip4/127.0.0.1/tcp/19004"
			}
		]
	}`

	validPath := filepath.Join(tmpDir, "valid_genesis.json")
	if err := os.WriteFile(validPath, []byte(validJSON), 0644); err != nil {
		t.Fatalf("failed to write valid genesis: %v", err)
	}

	g, err := LoadGenesis(validPath)
	if err != nil {
		t.Fatalf("LoadGenesis failed on valid json: %v", err)
	}
	if g.ChainID != 991 {
		t.Fatalf("expected chainID 991, got %d", g.ChainID)
	}
	if len(g.Validators) != 4 {
		t.Fatalf("expected 4 validators, got %d", len(g.Validators))
	}

	protoValidators, err := g.ToProtoValidators()
	if err != nil {
		t.Fatalf("ToProtoValidators failed: %v", err)
	}
	if len(protoValidators) != 4 {
		t.Fatalf("expected 4 proto validators, got %d", len(protoValidators))
	}

	// Test Invalid cases
	invalidCases := []struct {
		name string
		json string
	}{
		{
			name: "wrong_chain_id",
			json: `{"chain_id": 1337, "validators": []}`,
		},
		{
			name: "empty_validators",
			json: `{"chain_id": 991, "validators": []}`,
		},
		{
			name: "invalid_stake",
			json: `{
				"chain_id": 991,
				"validators": [
					{"name":"n0","address":"0x1111111111111111111111111111111111111111","stake":"0","protocol_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 32)) + `","network_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 32)) + `","authority_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 48)) + `","p2p_address":"addr"}
				]
			}`,
		},
		{
			name: "duplicate_address",
			json: `{
				"chain_id": 991,
				"validators": [
					{"name":"n0","address":"0x1111111111111111111111111111111111111111","stake":"1000","protocol_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 32)) + `","network_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 32)) + `","authority_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 48)) + `","p2p_address":"addr"},
					{"name":"n1","address":"0x1111111111111111111111111111111111111111","stake":"1000","protocol_key":"` + base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012")) + `","network_key":"` + base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012")) + `","authority_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 48)) + `","p2p_address":"addr"},
					{"name":"n2","address":"0x2222222222222222222222222222222222222222","stake":"1000","protocol_key":"` + base64.StdEncoding.EncodeToString([]byte("abcdefghijabcdefghijabcdefghijab")) + `","network_key":"` + base64.StdEncoding.EncodeToString([]byte("abcdefghijabcdefghijabcdefghijab")) + `","authority_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 48)) + `","p2p_address":"addr"},
					{"name":"n3","address":"0x3333333333333333333333333333333333333333","stake":"1000","protocol_key":"` + base64.StdEncoding.EncodeToString([]byte("klmnopqrstklmnopqrstklmnopqrstkl")) + `","network_key":"` + base64.StdEncoding.EncodeToString([]byte("klmnopqrstklmnopqrstklmnopqrstkl")) + `","authority_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 48)) + `","p2p_address":"addr"}
				]
			}`,
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(tmpDir, tc.name+".json")
			_ = os.WriteFile(p, []byte(tc.json), 0644)
			_, err := LoadGenesis(p)
			if err == nil {
				t.Fatalf("expected error for case %s, got nil", tc.name)
			}
		})
	}
}

func TestParentChainIDConfigurable(t *testing.T) {
	old := ParentChainID
	defer SetParentChainID(old)
	if DefaultParentChainID != 991 {
		t.Fatalf("default parent chain ID must equal the shared exec chain ID 991, got %d", DefaultParentChainID)
	}
	SetParentChainID(4242)
	if ParentChainID != 4242 {
		t.Fatal("SetParentChainID did not take effect")
	}
}
