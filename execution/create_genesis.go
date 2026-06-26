package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

type Account struct {
	Index       int    `json:"index"`
	PrivateKey  string `json:"private_key"`
	Address     string `json:"address"`
}

func main() {
	keysData, err := ioutil.ReadFile("../../metanode-suite/test_tps/gen_spam_keys/generated_keys.json")
	if err != nil {
		panic(err)
	}

	var keys []Account
	if err := json.Unmarshal(keysData, &keys); err != nil {
		panic(err)
	}

	genesisData, err := ioutil.ReadFile("../deploy/systemd/genesis.json")
	if err != nil {
		panic(err)
	}

	var genesis map[string]interface{}
	if err := json.Unmarshal(genesisData, &genesis); err != nil {
		panic(err)
	}

	alloc := genesis["alloc"].([]interface{})
	var newAlloc []interface{}

	for _, allocRaw := range alloc {
		acc := allocRaw.(map[string]interface{})
		bal, ok := acc["balance"].(string)
		if !ok || bal != "2000000000000000000000000000000" {
			newAlloc = append(newAlloc, allocRaw)
		}
	}

	validCount := 0
	for _, k := range keys {
		privKeyBytes, _ := hex.DecodeString(k.PrivateKey)
		kp := bls.NewKeyPair(privKeyBytes)
		if kp == nil {
			continue // Invalid BLS key, skip
		}
		pubKey := kp.BytesPublicKey()

		newAlloc = append(newAlloc, map[string]interface{}{
			"address":         k.Address,
			"balance":         "2000000000000000000000000000000",
			"pending_balance": "0",
			"last_hash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
			"device_key":      "0x0000000000000000000000000000000000000000000000000000000000000000",
			"publicKeyBls":    "0x" + hex.EncodeToString(pubKey),
		})
		validCount++
	}

	genesis["alloc"] = newAlloc

	fmt.Printf("Genesis now has %d accounts (%d valid BLS keys).\n", len(newAlloc), validCount)

	newData, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		panic(err)
	}

	if err := ioutil.WriteFile("../deploy/systemd/genesis.json", newData, 0644); err != nil {
		panic(err)
	}
	fmt.Println("Wrote updated genesis.json to systemd")
}
