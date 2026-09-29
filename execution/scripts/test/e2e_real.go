//go:build ignore

package main

import (
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
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
	
	blsPrivHex := "5fb8d1ceadf4059adca5c106dbd91452be8c433b2c38c5dd85c50f1c7da4c85c" // exec1 devnet BLS private key
	blsPriv, clusterPubKey, _ := bls.GenerateKeyPairFromSecretKey(blsPrivHex)
	
	digest := parentchain.ComputeRegisterAccountMessage(target, clusterPubKey)
	userSig, _ := crypto.Sign(crypto.Keccak256(digest), targetPriv)
	
	clusterSig := bls.Sign(blsPriv, digest)
	
	log.Printf("[DEBUG] e2e UserAddress: %s", target.Hex())
	log.Printf("[DEBUG] e2e floatIdentityKey: %x", clusterPubKey[:4])
	log.Printf("[DEBUG] e2e digest: %x", digest)
	log.Printf("[DEBUG] e2e sig: %x", clusterSig[:4])
	
	msgID, err := client.SendRegisterAccount(target, clusterPubKey, userSig, clusterSig)
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

	log.Printf("Depositing 500 to target on chain 992...")
	start := time.Now()
	_, err = client.SendDepositToFloat(
		clusterPubKey, 992,
		sender, target, big.NewInt(500),
	)
	if err != nil {
		log.Printf("Deposit failed: %v", err)
	} else {
		log.Printf("Deposit succeeded! Sent to Parent Chain.")
	}
	log.Printf("Waiting 3s for Deposit to be processed by Rollup...")
	time.Sleep(3 * time.Second)

	log.Printf("Transferring 100 to sender on chain 992 (Target withdrawing)...")
	// Target is sending to Sender, so we need Target's BLS signature? 
	// Wait, we need to sign with the Cluster's BLS key because the Float Account is owned by the Cluster.
	// But in this test, we are just triggering `TransferFloat`, which requires a signature from `clusterPubKey`.
	payloadHash := crypto.Keccak256Hash(nil)
	digest2 := parentchain.ComputeTransferFloatMessage(clusterPubKey, pubKey, target, sender, big.NewInt(100), big.NewInt(0), payloadHash, 0)
	clusterSig2 := bls.Sign(blsPriv, digest2)
	
	_, err = client.SendTransferFloat(
		clusterPubKey, pubKey, 992,
		target, sender, big.NewInt(100), big.NewInt(0), 0,
		clusterSig2[:], false,
	)
	if err != nil {
		log.Printf("Transfer failed: %v", err)
	} else {
		log.Printf("Transfer succeeded!")
	}

	log.Printf("Transfer finished on consensus in %v!", time.Since(start))
}
