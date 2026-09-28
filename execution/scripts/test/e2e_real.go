package main

import (
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

func main() {
	// Connect to parent chain RPC
	client := parentchain.NewHTTPClient("http://127.0.0.1:8547")

	// 1. Generate keys
	senderPriv, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(senderPriv.PublicKey)
	targetPriv, _ := crypto.GenerateKey()
	target := crypto.PubkeyToAddress(targetPriv.PublicKey)

	var pubKey, toPubKey cm.PublicKey
	pubKey[0] = 1
	toPubKey[0] = 2

	log.Printf("Transferring 100 to target on chain 992...")
	start := time.Now()
	_, err := client.SendTransferFloat(
		pubKey, toPubKey, 992,
		sender, target, big.NewInt(100), nil, 0,
		nil, false,
	)
	if err != nil {
		log.Printf("Transfer failed (as expected due to no balance): %v", err)
	} else {
		log.Printf("Transfer succeeded?!")
	}

	log.Printf("Transfer finished on consensus in %v!", time.Since(start))
}
