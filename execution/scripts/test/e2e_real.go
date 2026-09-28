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

	log.Printf("Registering target %s to toPubKey %x...", target.Hex(), toPubKey[:4])
	
	// Create cluster sig for testing (we just use a dummy signature since the parentchain mock/node 
	// currently uses blst.Sign without full verification if we don't have the real private key, 
	// actually for devnet we just pass a fake 96-byte sig for now, or if it fails, we will see).
	// Wait, RegisterAccount verifies BOTH UserSig and ClusterSig!
	// We need to properly generate UserSig.
	
	digest := parentchain.ComputeRegisterAccountMessage(target, toPubKey)
	userSig, _ := crypto.Sign(crypto.Keccak256(digest), targetPriv)
	
	var clusterSig cm.Sign // 96 bytes zero for now, might fail if devnet requires real BLS
	
	msgID, err := client.SendRegisterAccount(target, toPubKey, userSig, clusterSig)
	if err != nil {
		log.Printf("RegisterAccount failed: %v", err)
		// We'll continue anyway to see if lookup fails
	} else {
		log.Printf("RegisterAccount submitted: %x", msgID)
	}

	log.Printf("Querying Account Registry for %s...", target.Hex())
	fetchedPubKey, found, err := client.GetAccountRegistry(target)
	if err != nil {
		log.Printf("GetAccountRegistry err: %v", err)
	} else if found {
		log.Printf("FOUND Account Registry! Address %s belongs to Cluster %x", target.Hex(), fetchedPubKey[:4])
	} else {
		log.Printf("NOT FOUND in Account Registry (maybe consensus dropped our invalid sig)")
	}

	log.Printf("Transferring 100 to target on chain 992...")
	start := time.Now()
	_, err = client.SendTransferFloat(
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
