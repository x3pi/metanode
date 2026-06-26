package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"

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

	keyMap := make(map[string]string)
	for _, k := range keys {
		keyMap[strings.ToLower(k.Address)] = k.PrivateKey
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
	updated := 0
	for _, allocRaw := range alloc {
		acc := allocRaw.(map[string]interface{})
		addr := acc["address"].(string)
		
		if privKey, ok := keyMap[strings.ToLower(addr)]; ok {
			privKeyBytes, _ := hex.DecodeString(privKey)
			kp := bls.NewKeyPair(privKeyBytes)
			pubKey := kp.BytesPublicKey()
			
			acc["publicKeyBls"] = "0x" + hex.EncodeToString(pubKey)
			updated++
		}
	}

	fmt.Printf("Updated %d accounts in genesis.\n", updated)

	newData, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		panic(err)
	}

	if err := ioutil.WriteFile("../deploy/systemd/genesis.json", newData, 0644); err != nil {
		panic(err)
	}
	fmt.Println("Wrote updated genesis.json")
}
