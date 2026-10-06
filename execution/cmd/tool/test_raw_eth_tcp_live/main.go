package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
)

const (
	defaultGenesisKey = "4828f88ed7a8dc1b6450b3479ce17370c238fd9ed3b9df828b06ead16126db0b"
	defaultTCPAddr    = "127.0.0.1:4200"
	defaultParentAddr = "0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5"
	defaultRPCURL     = "http://127.0.0.1:8646"
	defaultChainID    = uint64(991)
)

var secp256k1N, _ = new(big.Int).SetString("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", 16)

func main() {
	tcpAddr := flag.String("addr", defaultTCPAddr, "Execution Node TCP connection address")
	chainID := flag.Uint64("chainid", defaultChainID, "EVM Chain ID")
	rpcURL := flag.String("rpc", defaultRPCURL, "Execution Node HTTP JSON-RPC URL")
	keyHex := flag.String("key", defaultGenesisKey, "Sender hex private key (must be funded)")
	flag.Parse()

	fmt.Println("════════════════════════════════════════════════════════════════════════════")
	fmt.Println("🚀 MetaNode Live E2E Test: TCP Native Ethereum Ingress (EIP-2718 / EIP-1559)")
	fmt.Println("════════════════════════════════════════════════════════════════════════════")
	fmt.Printf("• TCP Target Address: %s\n", *tcpAddr)
	fmt.Printf("• HTTP JSON-RPC URL:  %s\n", *rpcURL)
	fmt.Printf("• Chain ID:           %d\n", *chainID)

	// 1. Parse sender private key
	senderPrivKey, err := crypto.HexToECDSA(*keyHex)
	if err != nil {
		fmt.Printf("❌ Failed to parse sender private key: %v\n", err)
		os.Exit(1)
	}
	senderAddr := crypto.PubkeyToAddress(senderPrivKey.PublicKey)
	fmt.Printf("• Sender Address:     %s\n", senderAddr.Hex())

	// 2. Connect to HTTP RPC
	ethRpc, err := ethclient.Dial(*rpcURL)
	if err != nil {
		fmt.Printf("❌ Failed to connect to HTTP RPC at %s: %v\n", *rpcURL, err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	currBlock, err := ethRpc.BlockNumber(ctx)
	cancel()
	if err != nil {
		fmt.Printf("❌ Failed to query eth_blockNumber from RPC: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("• Current Block Height: #%d\n", currBlock)

	// 3. Connect to TCP Node
	cfg := &c_config.ClientConfig{
		Version_:                "0.0.1.0",
		PrivateKey_:             *keyHex,
		ParentConnectionAddress: *tcpAddr,
		ParentAddress:           defaultParentAddr,
		ChainId:                 *chainID,
		ParentConnectionType:    "client",
	}

	fmt.Println("\n⏳ [1/4] Establishing TCP connection to cluster node...")
	c, err := client.NewClient(cfg)
	if err != nil {
		fmt.Printf("❌ Failed to connect to TCP node at %s: %v\n", *tcpAddr, err)
		os.Exit(1)
	}
	defer c.Close()
	time.Sleep(1 * time.Second) // Handshake settle
	fmt.Println("✅ TCP connection successfully established!")

	// 4. Query sender on-chain account state via TCP
	senderState, err := c.AccountState(senderAddr)
	if err != nil {
		fmt.Printf("❌ Failed to query sender account state: %v\n", err)
		os.Exit(1)
	}
	initialNonce := senderState.Nonce()
	fmt.Printf("• Sender on-chain nonce:   %d\n", initialNonce)
	fmt.Printf("• Sender on-chain balance: %s wei\n", senderState.Balance().String())

	// Generate a fresh random recipient
	recipientPrivKey, err := crypto.GenerateKey()
	if err != nil {
		fmt.Printf("❌ Failed to generate recipient key: %v\n", err)
		os.Exit(1)
	}
	recipientAddr := crypto.PubkeyToAddress(recipientPrivKey.PublicKey)
	fmt.Printf("• Fresh Recipient Address: %s\n", recipientAddr.Hex())

	signer := ethtypes.LatestSignerForChainID(new(big.Int).SetUint64(*chainID))

	// ════════════════════════════════════════════════════════════════════════
	// TEST 1: Single Native EIP-1559 Transaction via TCP SendRawTransaction
	// ════════════════════════════════════════════════════════════════════════
	fmt.Println("\n────────────────────────────────────────────────────────────────────────────")
	fmt.Println("📦 [TEST 1] Single Native EIP-1559 Transaction via TCP SendRawTransaction")
	fmt.Println("────────────────────────────────────────────────────────────────────────────")

	transferAmount1 := big.NewInt(1_000_000_000_000_000_000) // 1 ETH
	tx1Data := &ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     initialNonce,
		GasTipCap: big.NewInt(100_000_000),   // 0.1 Gwei
		GasFeeCap: big.NewInt(1_000_000_000), // 1 Gwei
		Gas:       21000,
		To:        &recipientAddr,
		Value:     transferAmount1,
		Data:      nil,
	}
	signedTx1, err := ethtypes.SignTx(ethtypes.NewTx(tx1Data), signer, senderPrivKey)
	if err != nil {
		fmt.Printf("❌ Failed to sign EIP-1559 tx: %v\n", err)
		os.Exit(1)
	}
	rawEthTx1, err := signedTx1.MarshalBinary()
	if err != nil {
		fmt.Printf("❌ Failed to marshal EIP-1559 envelope binary: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("   Sending raw EIP-2718 binary (%d bytes) over TCP...\n", len(rawEthTx1))
	fmt.Printf("   Expected Eth Tx Hash: %s\n", signedTx1.Hash().Hex())

	start1 := time.Now()
	respHash1, err := c.SendRawEthTransaction(rawEthTx1)
	if err != nil {
		fmt.Printf("❌ SendRawEthTransaction failed: %v\n", err)
		os.Exit(1)
	}
	dur1 := time.Since(start1)
	fmt.Printf("✅ Node confirmed TransactionSuccess in %v!\n", dur1)
	fmt.Printf("   Confirmed Hash: %s\n", respHash1.Hex())
	if respHash1 != signedTx1.Hash() {
		fmt.Printf("❌ Response hash mismatch! Got %s, want %s\n", respHash1.Hex(), signedTx1.Hash().Hex())
		os.Exit(1)
	}

	fmt.Println("   Waiting for block inclusion and querying receipt via HTTP RPC...")
	receipt1, err := pollReceipt(ethRpc, signedTx1.Hash(), 15*time.Second)
	if err != nil {
		fmt.Printf("❌ Failed to fetch receipt: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Receipt found on-chain!\n")
	fmt.Printf("   • Block Number: #%d\n", receipt1.BlockNumber.Uint64())
	fmt.Printf("   • Status:       %d (1 = SUCCESS)\n", receipt1.Status)
	fmt.Printf("   • Gas Used:     %d\n", receipt1.GasUsed)
	if receipt1.Status != 1 {
		fmt.Printf("❌ Expected receipt status 1, got %d\n", receipt1.Status)
		os.Exit(1)
	}

	// Verify recipient balance
	recipState1, err := c.AccountState(recipientAddr)
	if err != nil {
		fmt.Printf("❌ Failed to query recipient account state: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   • Recipient on-chain balance: %s wei\n", recipState1.Balance().String())
	if recipState1.Balance().Cmp(transferAmount1) != 0 {
		fmt.Printf("❌ Recipient balance mismatch! Got %s, want %s\n", recipState1.Balance().String(), transferAmount1.String())
		os.Exit(1)
	}
	fmt.Println("   ✅ Single Native EIP-1559 Ingress PASSED successfully!")

	// ════════════════════════════════════════════════════════════════════════
	// TEST 2: Batch Native EIP-1559 Transactions via TCP SendRawTransactions
	// ════════════════════════════════════════════════════════════════════════
	fmt.Println("\n────────────────────────────────────────────────────────────────────────────")
	fmt.Println("📦 [TEST 2] Batch Native EIP-1559 Transactions via TCP SendRawTransactions")
	fmt.Println("────────────────────────────────────────────────────────────────────────────")

	transferAmountBatch := big.NewInt(500_000_000_000_000_000) // 0.5 ETH each
	txBatch1 := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     initialNonce + 1,
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       21000,
		To:        &recipientAddr,
		Value:     transferAmountBatch,
	})
	signedBatch1, err := ethtypes.SignTx(txBatch1, signer, senderPrivKey)
	if err != nil {
		fmt.Printf("❌ Failed to sign batch tx 1: %v\n", err)
		os.Exit(1)
	}
	rawBatch1, _ := signedBatch1.MarshalBinary()

	txBatch2 := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     initialNonce + 2,
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       21000,
		To:        &recipientAddr,
		Value:     transferAmountBatch,
	})
	signedBatch2, err := ethtypes.SignTx(txBatch2, signer, senderPrivKey)
	if err != nil {
		fmt.Printf("❌ Failed to sign batch tx 2: %v\n", err)
		os.Exit(1)
	}
	rawBatch2, _ := signedBatch2.MarshalBinary()

	batchPayload := [][]byte{rawBatch1, rawBatch2}
	fmt.Printf("   Sending batch of %d raw transactions over TCP...\n", len(batchPayload))
	fmt.Printf("   • Tx 1 Hash: %s\n", signedBatch1.Hash().Hex())
	fmt.Printf("   • Tx 2 Hash: %s\n", signedBatch2.Hash().Hex())

	startBatch := time.Now()
	batchHash, err := c.SendRawEthTransactions(batchPayload)
	if err != nil {
		fmt.Printf("❌ SendRawEthTransactions failed: %v\n", err)
		os.Exit(1)
	}
	durBatch := time.Since(startBatch)
	fmt.Printf("✅ Node confirmed Batch TransactionSuccess in %v!\n", durBatch)
	fmt.Printf("   Batch Group Hash: %s\n", batchHash.Hex())

	fmt.Println("   Waiting for block inclusion and querying receipts via HTTP RPC...")
	rcpBatch1, err := pollReceipt(ethRpc, signedBatch1.Hash(), 15*time.Second)
	if err != nil {
		fmt.Printf("❌ Failed to fetch batch receipt 1: %v\n", err)
		os.Exit(1)
	}
	rcpBatch2, err := pollReceipt(ethRpc, signedBatch2.Hash(), 15*time.Second)
	if err != nil {
		fmt.Printf("❌ Failed to fetch batch receipt 2: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Both batch receipts found on-chain!\n")
	fmt.Printf("   • Tx 1 Mined in Block: #%d (Status %d)\n", rcpBatch1.BlockNumber.Uint64(), rcpBatch1.Status)
	fmt.Printf("   • Tx 2 Mined in Block: #%d (Status %d)\n", rcpBatch2.BlockNumber.Uint64(), rcpBatch2.Status)

	expectedTotal := new(big.Int).Add(transferAmount1, new(big.Int).Mul(transferAmountBatch, big.NewInt(2)))
	recipState2, err := c.AccountState(recipientAddr)
	if err != nil {
		fmt.Printf("❌ Failed to query recipient account state after batch: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   • Cumulative Recipient on-chain balance: %s wei (expected: %s)\n",
		recipState2.Balance().String(), expectedTotal.String())
	if recipState2.Balance().Cmp(expectedTotal) != 0 {
		fmt.Printf("❌ Balance mismatch! Got %s, want %s\n", recipState2.Balance().String(), expectedTotal.String())
		os.Exit(1)
	}
	fmt.Println("   ✅ Batch Native EIP-1559 Ingress PASSED successfully!")

	// ════════════════════════════════════════════════════════════════════════
	// TEST 3: Negative Validation Checks (Rejections & Security Guards)
	// ════════════════════════════════════════════════════════════════════════
	fmt.Println("\n────────────────────────────────────────────────────────────────────────────")
	fmt.Println("🛡️  [TEST 3] Security & Envelope Validation Negative Checks")
	fmt.Println("────────────────────────────────────────────────────────────────────────────")

	// 3A: Pre-EIP-155 Replay Rejection
	fmt.Println("   [3A] Testing Pre-EIP-155 Legacy Transaction Rejection...")
	legacyTx := ethtypes.NewTx(&ethtypes.LegacyTx{
		Nonce:    initialNonce + 100,
		GasPrice: big.NewInt(1_000_000_000),
		Gas:      21000,
		To:       &recipientAddr,
		Value:    big.NewInt(1000),
	})
	homesteadSigner := ethtypes.HomesteadSigner{}
	pre155Tx, err := ethtypes.SignTx(legacyTx, homesteadSigner, senderPrivKey)
	if err != nil {
		fmt.Printf("❌ Failed to sign homestead tx: %v\n", err)
		os.Exit(1)
	}
	rawPre155, _ := pre155Tx.MarshalBinary()
	_, errPre155 := c.SendRawEthTransaction(rawPre155)
	if errPre155 == nil {
		fmt.Printf("❌ CRITICAL: Pre-EIP-155 transaction was ACCEPTED! Expected rejection.\n")
		os.Exit(1)
	}
	fmt.Printf("   ✅ Successfully rejected Pre-EIP-155 tx: %v\n", errPre155)

	// 3B: Chain ID Mismatch Rejection
	fmt.Println("   [3B] Testing Chain ID Mismatch Rejection (ChainID=1 on ChainID=991)...")
	wrongChainSigner := ethtypes.LatestSignerForChainID(big.NewInt(1))
	wrongChainTx := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID:   big.NewInt(1),
		Nonce:     initialNonce + 101,
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       21000,
		To:        &recipientAddr,
		Value:     big.NewInt(1000),
	})
	signedWrongChain, err := ethtypes.SignTx(wrongChainTx, wrongChainSigner, senderPrivKey)
	if err != nil {
		fmt.Printf("❌ Failed to sign wrong chain tx: %v\n", err)
		os.Exit(1)
	}
	rawWrongChain, _ := signedWrongChain.MarshalBinary()
	_, errWrongChain := c.SendRawEthTransaction(rawWrongChain)
	if errWrongChain == nil {
		fmt.Printf("❌ CRITICAL: Wrong Chain ID transaction was ACCEPTED! Expected rejection.\n")
		os.Exit(1)
	}
	fmt.Printf("   ✅ Successfully rejected Wrong Chain ID tx: %v\n", errWrongChain)

	// 3C: Malleable Signature Rejection (s > N/2)
	fmt.Println("   [3C] Testing Malleable Signature Rejection (s > N/2)...")
	validTx := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     initialNonce + 102,
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       21000,
		To:        &recipientAddr,
		Value:     big.NewInt(1000),
	})
	signedValid, err := ethtypes.SignTx(validTx, signer, senderPrivKey)
	if err != nil {
		fmt.Printf("❌ Failed to sign valid tx: %v\n", err)
		os.Exit(1)
	}
	v, r, s := signedValid.RawSignatureValues()
	malleableS := new(big.Int).Sub(secp256k1N, s) // s_malleable = N - s
	malleableTx, err := signedValid.WithSignature(signer, encodeRSV(r, malleableS, v))
	if err == nil {
		rawMalleable, errMarshal := malleableTx.MarshalBinary()
		if errMarshal == nil {
			_, errMalleable := c.SendRawEthTransaction(rawMalleable)
			if errMalleable == nil {
				fmt.Printf("❌ CRITICAL: Malleable signature was ACCEPTED! Expected rejection.\n")
				os.Exit(1)
			}
			fmt.Printf("   ✅ Successfully rejected Malleable signature tx: %v\n", errMalleable)
		}
	} else {
		fmt.Printf("   ℹ️ Note: Go-ethereum signer rejected malleable signature during construction: %v\n", err)
	}

	// ════════════════════════════════════════════════════════════════════════
	// TEST 4: Full State Consistency between TCP & HTTP JSON-RPC
	// ════════════════════════════════════════════════════════════════════════
	fmt.Println("\n────────────────────────────────────────────────────────────────────────────")
	fmt.Println("🔍 [TEST 4] Full State & Query Consistency Verification")
	fmt.Println("────────────────────────────────────────────────────────────────────────────")

	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	rpcBal, err := ethRpc.BalanceAt(ctx, recipientAddr, nil)
	cancel()
	if err != nil {
		fmt.Printf("❌ Failed to query eth_getBalance from HTTP RPC: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   • HTTP JSON-RPC eth_getBalance: %s wei\n", rpcBal.String())
	fmt.Printf("   • TCP AccountState balance:     %s wei\n", recipState2.Balance().String())
	if rpcBal.Cmp(recipState2.Balance()) != 0 {
		fmt.Printf("❌ Inconsistency between RPC and TCP balances! RPC=%s, TCP=%s\n",
			rpcBal.String(), recipState2.Balance().String())
		os.Exit(1)
	}
	fmt.Println("   ✅ Perfect state consistency between TCP Native Ingress and HTTP JSON-RPC!")

	fmt.Println("\n════════════════════════════════════════════════════════════════════════════")
	fmt.Println("🎉 ALL LIVE CLUSTER END-TO-END TESTS PASSED SUCCESSFULLY! (100% Zero-Fork)")
	fmt.Println("════════════════════════════════════════════════════════════════════════════")
}

func pollReceipt(client *ethclient.Client, txHash common.Hash, timeout time.Duration) (*ethtypes.Receipt, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		receipt, err := client.TransactionReceipt(ctx, txHash)
		cancel()
		if err == nil && receipt != nil {
			return receipt, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout waiting for receipt of tx %s after %v", txHash.Hex(), timeout)
}

func encodeRSV(r, s, v *big.Int) []byte {
	sig := make([]byte, 65)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)
	sig[64] = byte(v.Uint64())
	return sig
}
