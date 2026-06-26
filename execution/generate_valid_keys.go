package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

type Account struct {
	Index       int    `json:"index"`
	PrivateKey  string `json:"private_key"`
	Address     string `json:"address"`
}

func main() {
	var keys []Account
	count := 50000

	for i := 0; i < count; i++ {
		kp := bls.GenerateKeyPair()
		privBytes := kp.BytesPrivateKey()
		
		ecdsaKey, err := crypto.ToECDSA(privBytes)
		if err != nil {
			// This shouldn't happen, but just in case
			i--
			continue
		}
		addr := crypto.PubkeyToAddress(ecdsaKey.PublicKey)

		keys = append(keys, Account{
			Index:      i,
			PrivateKey: hex.EncodeToString(privBytes),
			Address:    addr.Hex(),
		})
		
		if (i+1)%5000 == 0 {
			fmt.Printf("Generated %d keys...\n", i+1)
		}
	}

	newData, _ := json.MarshalIndent(keys, "", "  ")
	ioutil.WriteFile("../../metanode-suite/test_tps/gen_spam_keys/generated_keys.json", newData, 0644)
	fmt.Println("Wrote 50000 valid BLS keys to generated_keys.json")
}
