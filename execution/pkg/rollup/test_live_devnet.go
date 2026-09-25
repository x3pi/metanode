package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ERC20 minimal ABI for transfer
const erc20ABI = `[{"constant":false,"inputs":[{"name":"_to","type":"address"},{"name":"_value","type":"uint256"}],"name":"transfer","outputs":[{"name":"","type":"bool"}],"payable":false,"stateMutability":"nonpayable","type":"function"}]`

// ERC20 minimal bytecode (creates a token with 1000 supply for deployer)
// This is a simple ERC20 bytecode.
const erc20Bin = "608060405234801561001057600080fd5b50600080546001600160a01b031916331790556103e86001819055336000908152602081905260409020556101188061004a6000396000f3fe6080604052348015600f57600080fd5b506004361060285760003560e01c8063a9059cbb14602d575b600080fd5b6056600480360381019080803573ffffffffffffffffffffffffffffffffffffffff16906020019092919080359060200190929190505050606e565b604051808215151515815260200191505060405180910390f35b600033600090815260208190526040812054821115609357600080fd5b3360009081526020819052604080822080548545039055848573ffffffffffffffffffffffffffffffffffffffff16815282208054840190555050506001939250505056fea2646970667358221220473e6da80153549646c039750b3fbf461cfa8a2abfe7ab5ffba7e70404323d8c64736f6c63430008070033"

func main() {
	client, err := ethclient.Dial("http://127.0.0.1:10746")
	if err != nil {
		log.Fatalf("Failed to connect to devnet: %v", err)
	}

	privateKeyHex := "0x9f61a687fbeac9e11d5cfce0fe2dcec035cb2b21eb9c584d8cf90696ce2fc370"
	privateKey, err := crypto.HexToECDSA(privateKeyHex[2:])
	if err != nil {
		log.Fatal(err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		log.Fatal("error casting public key to ECDSA")
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	fmt.Printf("Sender address: %s\n", fromAddress.Hex())

	nonce, err := client.PendingNonceAt(context.Background(), fromAddress)
	if err != nil {
		log.Fatal(err)
	}

	chainID, err := client.ChainID(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Chain ID: %v\n", chainID)

	// 1. Send EIP-1559 tx
	toAddress := common.HexToAddress("0x47Bc6bCBAeDC731Fd2832df68CEd61053aCC02B0")
	tx1559 := types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: big.NewInt(1000000000), // 1 Gwei
		GasFeeCap: big.NewInt(1000000000), // 1 Gwei
		Gas:       21000,
		To:        &toAddress,
		Value:     big.NewInt(10000000000000000), // 0.01 ETH
	})

	signedTx, err := types.SignTx(tx1559, types.LatestSignerForChainID(chainID), privateKey)
	if err != nil {
		log.Fatal(err)
	}

	err = client.SendTransaction(context.Background(), signedTx)
	if err != nil {
		log.Fatalf("SendTransaction EIP-1559 failed: %v", err)
	}
	fmt.Printf("EIP-1559 Tx sent: %s\n", signedTx.Hash().Hex())

	// Wait for tx to be mined
	time.Sleep(3 * time.Second)
	
	txOut, isPending, err := client.TransactionByHash(context.Background(), signedTx.Hash())
	if err != nil {
		log.Fatalf("TransactionByHash failed: %v", err)
	}
	if isPending {
		log.Fatalf("EIP-1559 Tx is still pending")
	}
	
	v, r, s := txOut.RawSignatureValues()
	fmt.Printf("EIP-1559 Tx verified from block! V=%v, R=%v, S=%v\n", v, r, s)
	
	// 2. Deploy ERC20 Token and test Revert
	nonce++
	fmt.Println("Deploying ERC20 token...")
	
	auth, err := bind.NewKeyedTransactorWithChainID(privateKey, chainID)
	if err != nil {
		log.Fatal(err)
	}
	auth.Nonce = big.NewInt(int64(nonce))
	auth.Value = big.NewInt(0)
	auth.GasLimit = uint64(3000000)
	auth.GasPrice = big.NewInt(1000000000)

	txDeploy := types.NewContractCreation(nonce, big.NewInt(0), 3000000, big.NewInt(1000000000), common.FromHex(erc20Bin))
	signedTxDeploy, _ := types.SignTx(txDeploy, types.LatestSignerForChainID(chainID), privateKey)
	err = client.SendTransaction(context.Background(), signedTxDeploy)
	if err != nil {
		log.Fatalf("Deploy token failed: %v", err)
	}
	fmt.Printf("ERC20 deploy tx sent: %s\n", signedTxDeploy.Hash().Hex())
	time.Sleep(4 * time.Second)
	
	receipt, err := client.TransactionReceipt(context.Background(), signedTxDeploy.Hash())
	if err != nil {
		log.Fatalf("TransactionReceipt for deploy failed: %v", err)
	}
	tokenAddr := receipt.ContractAddress
	fmt.Printf("ERC20 Token deployed at: %s\n", tokenAddr.Hex())

	// Transfer exceeding balance (1000 + 1)
	parsedABI, _ := abi.JSON(strings.NewReader(erc20ABI))
	data, _ := parsedABI.Pack("transfer", toAddress, big.NewInt(1001))
	nonce++
	
	txRevert := types.NewTransaction(nonce, tokenAddr, big.NewInt(0), 100000, big.NewInt(1000000000), data)
	signedTxRevert, _ := types.SignTx(txRevert, types.LatestSignerForChainID(chainID), privateKey)
	err = client.SendTransaction(context.Background(), signedTxRevert)
	if err != nil {
		log.Fatalf("Send revert tx failed: %v", err)
	}
	fmt.Printf("ERC20 Revert tx sent: %s\n", signedTxRevert.Hash().Hex())
	time.Sleep(4 * time.Second)
	
	receiptRevert, err := client.TransactionReceipt(context.Background(), signedTxRevert.Hash())
	if err != nil {
		log.Fatalf("TransactionReceipt for revert failed: %v", err)
	}
	fmt.Printf("ERC20 Revert Receipt Status: %v\n", receiptRevert.Status)
	fmt.Printf("Check metanode node-0 logs or internal blocks to verify Exception == 5\n")
}
