package main

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"math/big"
	"log"
	"strings"
	"time"
	"encoding/hex"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func main() {
	client, err := ethclient.Dial("http://127.0.0.1:10746")
	if err != nil {
		log.Fatal(err)
	}
	chainID, _ := client.ChainID(context.Background())
	
	privateKeyHex := "0x9f61a687fbeac9e11d5cfce0fe2dcec035cb2b21eb9c584d8cf90696ce2fc370"
	privateKey, _ := crypto.HexToECDSA(privateKeyHex[2:])
	from := crypto.PubkeyToAddress(privateKey.PublicKey)
	nonce, _ := client.PendingNonceAt(context.Background(), from)
	
	// ACCOUNT_SETTING_ADDRESS
	hash := crypto.Keccak256([]byte("account"))
	to := common.BytesToAddress(hash[:4])
	
	accountABI, _ := abi.JSON(strings.NewReader(`[{"inputs":[{"internalType":"bytes","name":"publicKey","type":"bytes"}],"name":"setBlsPublicKey","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"}]`))
	
	blsPrivHex := "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b"
	blsPrivBytes, _ := hex.DecodeString(blsPrivHex)
	kp := bls.NewKeyPair(blsPrivBytes)
	dummyBlsKey := kp.BytesPublicKey()
	dataTx, _ := accountABI.Pack("setBlsPublicKey", dummyBlsKey)
	
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		Gas:      3000000,
		GasPrice: big.NewInt(1000000000),
		To:       &to,
		Value:    big.NewInt(0),
		Data:     dataTx,
	})
	
	signedTx, _ := types.SignTx(tx, types.LatestSignerForChainID(chainID), privateKey)
	err = client.SendTransaction(context.Background(), signedTx)
	if err == nil {
		fmt.Printf("Dummy BLS key registered successfully!\n")
	} else {
		fmt.Printf("Failed to register: %v\n", err)
	}
	
	time.Sleep(4 * time.Second)
	
	// Now send a normal tx to trigger the error and reveal the Gateway's BLS key
	tx2 := types.NewTx(&types.LegacyTx{
		Nonce:    nonce + 1,
		Gas:      21000,
		GasPrice: big.NewInt(1000000000),
		To:       &from,
		Value:    big.NewInt(1),
	})
	signedTx2, _ := types.SignTx(tx2, types.LatestSignerForChainID(chainID), privateKey)
	err = client.SendTransaction(context.Background(), signedTx2)
	fmt.Printf("Normal tx error (should reveal Gateway key): %v\n", err)
}
