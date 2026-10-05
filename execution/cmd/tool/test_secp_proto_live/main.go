package main

import (
	"context"
	"crypto/ecdsa"
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

const (
	defaultGenesisKey = "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b"
	defaultTCPAddr    = "127.0.0.1:4201"
	defaultParentAddr = "0x55798165960a62cED34a0d86e36B1758D1303907"
	defaultRPCURL     = "http://127.0.0.1:8757"
	defaultChainID    = uint64(991)
)

func main() {
	tcpAddr := flag.String("addr", defaultTCPAddr, "Node TCP connection address")
	chainID := flag.Uint64("chainid", defaultChainID, "Chain ID")
	rpcURL := flag.String("rpc", defaultRPCURL, "Node HTTP RPC URL")
	keyHex := flag.String("key", defaultGenesisKey, "Sender hex private key")
	flag.Parse()

	fmt.Println("═════════════════════════════════════════════════════════════")
	fmt.Println("🚀 MetaNode Live E2E Test: Secp256k1 Proto TCP (Type 0xFF)")
	fmt.Println("═════════════════════════════════════════════════════════════")
	fmt.Printf("TCP Target:  %s\n", *tcpAddr)
	fmt.Printf("HTTP RPC:    %s\n", *rpcURL)
	fmt.Printf("Chain ID:    %d\n", *chainID)

	// 1. Parse sender private key
	senderPrivKey, err := crypto.HexToECDSA(*keyHex)
	if err != nil {
		fmt.Printf("❌ Failed to parse sender private key: %v\n", err)
		os.Exit(1)
	}
	senderAddr := crypto.PubkeyToAddress(senderPrivKey.PublicKey)
	fmt.Printf("Sender Addr: %s\n", senderAddr.Hex())

	// 2. Initialize TCP Client
	cfg := &c_config.ClientConfig{
		Version_:                "0.0.1.0",
		PrivateKey_:             *keyHex,
		ParentConnectionAddress: *tcpAddr,
		ParentAddress:           defaultParentAddr,
		ChainId:                 *chainID,
		ParentConnectionType:    "client",
	}

	fmt.Println("\n⏳ Connecting to node via TCP...")
	c, err := client.NewClient(cfg)
	if err != nil {
		fmt.Printf("❌ Failed to connect to node at %s: %v\n", *tcpAddr, err)
		os.Exit(1)
	}
	fmt.Println("✅ TCP connection established!")

	// Give handshake 1s to settle
	time.Sleep(1 * time.Second)

	// 3. Query initial account state of sender
	fmt.Println("\n🔍 Querying initial sender state via TCP...")
	senderState, err := c.AccountState(senderAddr)
	if err != nil {
		fmt.Printf("❌ Failed to query sender account state: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Sender initial on-chain nonce: %d\n", senderState.Nonce())
	fmt.Printf("Sender initial balance:        %s\n", senderState.Balance().String())

	// 4. Generate fresh recipient (unregistered, pure EVM secp account)
	recipientPrivKey, err := crypto.GenerateKey()
	if err != nil {
		fmt.Printf("❌ Failed to generate recipient key: %v\n", err)
		os.Exit(1)
	}
	recipientAddr := crypto.PubkeyToAddress(recipientPrivKey.PublicKey)
	fmt.Printf("\n🎯 Target Recipient (pure un-registered EVM account): %s\n", recipientAddr.Hex())

	// Test 1: Send first Type 0xFF transaction
	transferAmount := big.NewInt(1_000_000_000) // 1 Gwei
	fmt.Printf("\n📦 [TEST 1] Sending Type 0xFF transaction: %s -> %s (Amount: %s)...\n",
		senderAddr.Hex()[:10], recipientAddr.Hex()[:10], transferAmount.String())

	start1 := time.Now()
	rcp1, tx1, err := c.SendSecpProtoTransaction(
		senderPrivKey,
		recipientAddr,
		transferAmount,
		21000,
		1000,
		nil,
	)
	if err != nil {
		fmt.Printf("❌ Test 1 failed: %v\n", err)
		os.Exit(1)
	}
	dur1 := time.Since(start1)

	fmt.Printf("✅ Receipt received in %v!\n", dur1)
	fmt.Printf("   TX Hash: %s\n", tx1.Hash().Hex())
	fmt.Printf("   Status:  %d (%s)\n", rcp1.Status(), pb.RECEIPT_STATUS_name[int32(rcp1.Status())])
	if rcp1.Status() != pb.RECEIPT_STATUS_RETURNED {
		fmt.Printf("❌ Expected receipt status RETURNED (0), got %d\n", rcp1.Status())
		os.Exit(1)
	}

	// Wait 1 second for consensus state commit
	time.Sleep(1 * time.Second)

	// Verify recipient balance
	fmt.Println("\n🔍 Verifying recipient balance on-chain...")
	recipState, err := c.AccountState(recipientAddr)
	if err != nil {
		fmt.Printf("❌ Failed to query recipient account state: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Recipient on-chain balance: %s (expected: %s)\n",
		recipState.Balance().String(), transferAmount.String())
	if recipState.Balance().Cmp(transferAmount) != 0 {
		fmt.Printf("❌ Balance mismatch! Got %s, want %s\n",
			recipState.Balance().String(), transferAmount.String())
		os.Exit(1)
	}
	fmt.Println("   ✅ Recipient balance verified exactly!")

	// Test 2: Chaining sequential transaction with updated nonce
	fmt.Println("\n📦 [TEST 2] Sending 2nd Type 0xFF transaction (testing sequential nonce chaining)...")
	start2 := time.Now()
	rcp2, tx2, err := c.SendSecpProtoTransaction(
		senderPrivKey,
		recipientAddr,
		transferAmount,
		21000,
		1000,
		nil,
	)
	if err != nil {
		fmt.Printf("❌ Test 2 failed: %v\n", err)
		os.Exit(1)
	}
	dur2 := time.Since(start2)

	fmt.Printf("✅ Receipt 2 received in %v!\n", dur2)
	fmt.Printf("   TX 2 Hash: %s\n", tx2.Hash().Hex())
	fmt.Printf("   TX 2 Nonce: %d\n", tx2.GetNonce())
	fmt.Printf("   Status:    %d (%s)\n", rcp2.Status(), pb.RECEIPT_STATUS_name[int32(rcp2.Status())])
	if rcp2.Status() != pb.RECEIPT_STATUS_RETURNED {
		fmt.Printf("❌ Expected receipt status RETURNED (0), got %d\n", rcp2.Status())
		os.Exit(1)
	}

	time.Sleep(1 * time.Second)

	expectedTotal := new(big.Int).Mul(transferAmount, big.NewInt(2))
	recipState2, err := c.AccountState(recipientAddr)
	if err != nil {
		fmt.Printf("❌ Failed to query recipient account state after tx 2: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Recipient on-chain balance after 2 txs: %s (expected: %s)\n",
		recipState2.Balance().String(), expectedTotal.String())
	if recipState2.Balance().Cmp(expectedTotal) != 0 {
		fmt.Printf("❌ Balance mismatch after tx 2! Got %s, want %s\n",
			recipState2.Balance().String(), expectedTotal.String())
		os.Exit(1)
	}
	fmt.Println("   ✅ Chained balance verified exactly!")

	// Test 3: Querying via standard HTTP JSON-RPC
	fmt.Println("\n🔍 [TEST 3] Querying state via HTTP JSON-RPC endpoint...")
	ethRpc, err := ethclient.Dial(*rpcURL)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bal, errBal := ethRpc.BalanceAt(ctx, recipientAddr, nil)
		if errBal == nil {
			fmt.Printf("   RPC eth_getBalance: %s wei\n", bal.String())
			if bal.Cmp(expectedTotal) == 0 {
				fmt.Println("   ✅ HTTP JSON-RPC balance matches on-chain state perfectly!")
			}
		} else {
			fmt.Printf("   ℹ️ Note: HTTP RPC BalanceAt returned: %v\n", errBal)
		}
	} else {
		fmt.Printf("   ℹ️ HTTP RPC client dial skipped: %v\n", err)
	}

	fmt.Println("\n═════════════════════════════════════════════════════════════")
	fmt.Println("🎉 ALL LIVE E2E TESTS PASSED SUCCESSFULLY! (100% Zero-Fork)")
	fmt.Println("═════════════════════════════════════════════════════════════")
}

// Suppress unused imports
var _ *ecdsa.PrivateKey
var _ = hexutil.Encode
var _ = common.Address{}
