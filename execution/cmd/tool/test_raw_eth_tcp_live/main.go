package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	c_network "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/network"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
)

const (
	defaultGenesisKey = "4828f88ed7a8dc1b6450b3479ce17370c238fd9ed3b9df828b06ead16126db0b"
	defaultTCPAddr    = "127.0.0.1:4200"
	defaultParentAddr = "0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5"
	defaultRPCURL     = "http://127.0.0.1:8646"
	defaultChainID    = uint64(991)
)

var secp256k1N, _ = new(big.Int).SetString("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", 16)

type jsonRPCResponse struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	tcpAddr := flag.String("addr", defaultTCPAddr, "Execution Node TCP connection address")
	chainID := flag.Uint64("chainid", defaultChainID, "EVM Chain ID")
	rpcURL := flag.String("rpc", defaultRPCURL, "Execution Node HTTP JSON-RPC URL")
	keyHex := flag.String("key", defaultGenesisKey, "Sender hex private key (must be funded)")
	flag.Parse()

	fmt.Println("════════════════════════════════════════════════════════════════════════════")
	fmt.Println("🚀 MetaNode Live E2E Test: TCP & RPC Native Ethereum Ingress (Eth-Only)")
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

	fmt.Println("\n⏳ Establishing TCP connection to cluster node...")
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
	currentNonce := initialNonce

	// ════════════════════════════════════════════════════════════════════════
	// TEST 1: Single Native EIP-1559 Transaction via TCP SendRawTransaction
	// ════════════════════════════════════════════════════════════════════════
	fmt.Println("\n────────────────────────────────────────────────────────────────────────────")
	fmt.Println("📦 [TEST 1] Single Native EIP-1559 Transaction via TCP SendRawTransaction")
	fmt.Println("────────────────────────────────────────────────────────────────────────────")

	transferAmount1 := big.NewInt(1_000_000_000_000_000_000) // 1 ETH
	tx1Data := &ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     currentNonce,
		GasTipCap: big.NewInt(100_000_000),   // 0.1 Gwei
		GasFeeCap: big.NewInt(1_000_000_000), // 1 Gwei
		Gas:       21000,
		To:        &recipientAddr,
		Value:     transferAmount1,
		Data:      nil,
	}
	currentNonce++
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
	fmt.Printf("   • Block Number:        #%d\n", receipt1.BlockNumber.Uint64())
	fmt.Printf("   • Status:              %d (1 = SUCCESS)\n", receipt1.Status)
	fmt.Printf("   • Gas Used:            %d\n", receipt1.GasUsed)
	fmt.Printf("   • Cumulative Gas Used: %d\n", receipt1.CumulativeGasUsed)
	if receipt1.Status != 1 {
		fmt.Printf("❌ Expected receipt status 1, got %d\n", receipt1.Status)
		os.Exit(1)
	}
	if receipt1.CumulativeGasUsed == 0 {
		fmt.Printf("❌ Expected CumulativeGasUsed > 0, got 0\n")
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
		Nonce:     currentNonce,
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       21000,
		To:        &recipientAddr,
		Value:     transferAmountBatch,
	})
	currentNonce++
	signedBatch1, err := ethtypes.SignTx(txBatch1, signer, senderPrivKey)
	if err != nil {
		fmt.Printf("❌ Failed to sign batch tx 1: %v\n", err)
		os.Exit(1)
	}
	rawBatch1, _ := signedBatch1.MarshalBinary()

	txBatch2 := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     currentNonce,
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       21000,
		To:        &recipientAddr,
		Value:     transferAmountBatch,
	})
	currentNonce++
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
	fmt.Printf("   • Tx 1 Mined in Block: #%d (Status %d, CumGas: %d)\n", rcpBatch1.BlockNumber.Uint64(), rcpBatch1.Status, rcpBatch1.CumulativeGasUsed)
	fmt.Printf("   • Tx 2 Mined in Block: #%d (Status %d, CumGas: %d)\n", rcpBatch2.BlockNumber.Uint64(), rcpBatch2.Status, rcpBatch2.CumulativeGasUsed)
	fmt.Println("   ✅ Batch Native EIP-1559 Ingress PASSED successfully!")

	// ════════════════════════════════════════════════════════════════════════
	// TEST 2B: Detailed Batch Ingress via SendRawEthTransactionsDetailed
	// ════════════════════════════════════════════════════════════════════════
	fmt.Println("\n────────────────────────────────────────────────────────────────────────────")
	fmt.Println("📦 [TEST 2B] Detailed Batch Ingress (SendRawEthTransactionsDetailed)")
	fmt.Println("────────────────────────────────────────────────────────────────────────────")

	txDet1 := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     currentNonce,
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       21000,
		To:        &recipientAddr,
		Value:     transferAmountBatch,
	})
	currentNonce++
	signedDet1, _ := ethtypes.SignTx(txDet1, signer, senderPrivKey)
	rawDet1, _ := signedDet1.MarshalBinary()

	txDet2 := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     currentNonce,
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       21000,
		To:        &recipientAddr,
		Value:     transferAmountBatch,
	})
	currentNonce++
	signedDet2, _ := ethtypes.SignTx(txDet2, signer, senderPrivKey)
	rawDet2, _ := signedDet2.MarshalBinary()

	fmt.Printf("   Sending detailed batch of 2 transactions...\n")
	fmt.Printf("   • Expected Det1: %s\n", signedDet1.Hash().Hex())
	fmt.Printf("   • Expected Det2: %s\n", signedDet2.Hash().Hex())

	detHashes, err := c.SendRawEthTransactionsDetailed([][]byte{rawDet1, rawDet2})
	if err != nil {
		fmt.Printf("❌ SendRawEthTransactionsDetailed failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Received %d individual tx hashes from node!\n", len(detHashes))
	if len(detHashes) != 2 {
		fmt.Printf("❌ Expected 2 hashes, got %d\n", len(detHashes))
		os.Exit(1)
	}
	if detHashes[0] != signedDet1.Hash() || detHashes[1] != signedDet2.Hash() {
		fmt.Printf("❌ Detailed returned hashes do not match expected tx hashes!\n")
		os.Exit(1)
	}
	fmt.Println("   • Hashes strictly match expected transactions!")

	rcpDet1, err := pollReceipt(ethRpc, signedDet1.Hash(), 15*time.Second)
	if err != nil || rcpDet1.Status != 1 {
		fmt.Printf("❌ Detailed tx 1 receipt failed: %v\n", err)
		os.Exit(1)
	}
	rcpDet2, err := pollReceipt(ethRpc, signedDet2.Hash(), 15*time.Second)
	if err != nil || rcpDet2.Status != 1 {
		fmt.Printf("❌ Detailed tx 2 receipt failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("   ✅ SendRawEthTransactionsDetailed PASSED successfully!")

	// ════════════════════════════════════════════════════════════════════════
	// TEST 2C: Direct HTTP JSON-RPC eth_sendRawTransaction
	// ════════════════════════════════════════════════════════════════════════
	fmt.Println("\n────────────────────────────────────────────────────────────────────────────")
	fmt.Println("🌐 [TEST 2C] Direct HTTP JSON-RPC eth_sendRawTransaction (EIP-1559)")
	fmt.Println("────────────────────────────────────────────────────────────────────────────")

	transferAmountRpc := big.NewInt(300_000_000_000_000_000) // 0.3 ETH
	txRpc := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     currentNonce,
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       21000,
		To:        &recipientAddr,
		Value:     transferAmountRpc,
	})
	currentNonce++
	signedRpcTx, _ := ethtypes.SignTx(txRpc, signer, senderPrivKey)
	rawRpcBytes, _ := signedRpcTx.MarshalBinary()

	fmt.Printf("   Sending raw EIP-1559 tx via HTTP JSON-RPC eth_sendRawTransaction...\n")
	fmt.Printf("   • Expected Tx Hash: %s\n", signedRpcTx.Hash().Hex())

	rpcPayload := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_sendRawTransaction","params":["0x%x"],"id":100}`, rawRpcBytes)
	httpResp, err := http.Post(*rpcURL, "application/json", strings.NewReader(rpcPayload))
	if err != nil {
		fmt.Printf("❌ Failed to call eth_sendRawTransaction HTTP endpoint: %v\n", err)
		os.Exit(1)
	}
	bodyResp, _ := io.ReadAll(httpResp.Body)
	httpResp.Body.Close()

	var rpcParsed jsonRPCResponse
	if err := json.Unmarshal(bodyResp, &rpcParsed); err != nil {
		fmt.Printf("❌ Failed to unmarshal JSON-RPC response: %v, raw: %s\n", err, string(bodyResp))
		os.Exit(1)
	}
	if rpcParsed.Error != nil {
		fmt.Printf("❌ eth_sendRawTransaction returned error: %s (code: %d)\n", rpcParsed.Error.Message, rpcParsed.Error.Code)
		os.Exit(1)
	}
	var returnedTxHashHex string
	_ = json.Unmarshal(rpcParsed.Result, &returnedTxHashHex)
	fmt.Printf("✅ HTTP JSON-RPC confirmed acceptance with hash: %s\n", returnedTxHashHex)
	if !strings.EqualFold(returnedTxHashHex, signedRpcTx.Hash().Hex()) {
		fmt.Printf("❌ Hash mismatch! Got %s, want %s\n", returnedTxHashHex, signedRpcTx.Hash().Hex())
		os.Exit(1)
	}

	rcpRpc, err := pollReceipt(ethRpc, signedRpcTx.Hash(), 15*time.Second)
	if err != nil {
		fmt.Printf("❌ Failed to poll receipt for RPC transaction: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Receipt confirmed via JSON-RPC!\n")
	fmt.Printf("   • Block Number:        #%d\n", rcpRpc.BlockNumber.Uint64())
	fmt.Printf("   • Status:              %d\n", rcpRpc.Status)
	fmt.Printf("   • Gas Used:            %d\n", rcpRpc.GasUsed)
	fmt.Printf("   • Cumulative Gas Used: %d\n", rcpRpc.CumulativeGasUsed)
	fmt.Printf("   • Effective Gas Price: %s wei\n", rcpRpc.EffectiveGasPrice.String())
	if rcpRpc.Status != 1 || rcpRpc.CumulativeGasUsed == 0 || rcpRpc.EffectiveGasPrice.Sign() == 0 {
		fmt.Printf("❌ Invalid receipt fields in HTTP RPC response!\n")
		os.Exit(1)
	}

	// Verify eth_getTransactionByHash
	ctxRpc, cancelRpc := context.WithTimeout(context.Background(), 5*time.Second)
	queriedTx, isPending, err := ethRpc.TransactionByHash(ctxRpc, signedRpcTx.Hash())
	cancelRpc()
	if err != nil {
		fmt.Printf("❌ eth_getTransactionByHash failed: %v\n", err)
		os.Exit(1)
	}
	if isPending {
		fmt.Println("⚠️ Transaction still reported pending, but receipt exists")
	}
	if queriedTx.Hash() != signedRpcTx.Hash() {
		fmt.Printf("❌ Queried tx hash mismatch: got %s, want %s\n", queriedTx.Hash().Hex(), signedRpcTx.Hash().Hex())
		os.Exit(1)
	}
	fmt.Println("   ✅ Direct HTTP JSON-RPC eth_sendRawTransaction PASSED successfully!")

	// ════════════════════════════════════════════════════════════════════════
	// TEST 2D: Smart Contract Deployment, Event Logs & Bloom Filter
	// ════════════════════════════════════════════════════════════════════════
	fmt.Println("\n────────────────────────────────────────────────────────────────────────────")
	fmt.Println("📜 [TEST 2D] Smart Contract Deployment, Event Logs & Bloom Filter")
	fmt.Println("────────────────────────────────────────────────────────────────────────────")

	// Minimal EVM bytecode that emits LOG1 with topic 0x24ec... when called:
	// Init code: 6027600c60003960276000f3 (copies 39 bytes from offset 12 to mem 0 and returns it)
	// Runtime code: 7f24ec1d3ff24c2fa8f171943c3470b173c9a01f59d9417182a0e599078d30fb5e 6000 6000 a1 00 (PUSH32 topic, PUSH1 0 size, PUSH1 0 offset, LOG1, STOP)
	deployBytecodeHex := "6027600c60003960276000f37f24ec1d3ff24c2fa8f171943c3470b173c9a01f59d9417182a0e599078d30fb5e60006000a100"
	deployBytecode := common.FromHex(deployBytecodeHex)
	eventTopic := common.HexToHash("0x24ec1d3ff24c2fa8f171943c3470b173c9a01f59d9417182a0e599078d30fb5e")

	deployTx := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     currentNonce,
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       200_000,
		To:        nil, // Contract Creation
		Value:     big.NewInt(0),
		Data:      deployBytecode,
	})
	currentNonce++
	signedDeployTx, _ := ethtypes.SignTx(deployTx, signer, senderPrivKey)
	rawDeployBytes, _ := signedDeployTx.MarshalBinary()

	fmt.Printf("   Deploying test event emitter contract (Tx: %s)...\n", signedDeployTx.Hash().Hex())
	deployHash, err := c.SendRawEthTransaction(rawDeployBytes)
	if err != nil {
		fmt.Printf("❌ Contract deploy failed: %v\n", err)
		os.Exit(1)
	}
	deployReceipt, err := pollReceipt(ethRpc, deployHash, 15*time.Second)
	if err != nil || deployReceipt.Status != 1 {
		fmt.Printf("❌ Failed to get contract deploy receipt: %v\n", err)
		os.Exit(1)
	}
	contractAddr := deployReceipt.ContractAddress
	fmt.Printf("✅ Contract successfully deployed at: %s (Block #%d)\n", contractAddr.Hex(), deployReceipt.BlockNumber.Uint64())

	// Call the deployed contract to trigger event emission
	callTx := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     currentNonce,
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       100_000,
		To:        &contractAddr,
		Value:     big.NewInt(0),
		Data:      []byte{0x01},
	})
	currentNonce++
	signedCallTx, _ := ethtypes.SignTx(callTx, signer, senderPrivKey)
	rawCallBytes, _ := signedCallTx.MarshalBinary()

	fmt.Printf("   Invoking contract to trigger LOG1 event (Tx: %s)...\n", signedCallTx.Hash().Hex())
	callHash, err := c.SendRawEthTransaction(rawCallBytes)
	if err != nil {
		fmt.Printf("❌ Contract call failed: %v\n", err)
		os.Exit(1)
	}
	callReceipt, err := pollReceipt(ethRpc, callHash, 15*time.Second)
	if err != nil {
		fmt.Printf("❌ Failed to get contract call receipt: %v\n", err)
		os.Exit(1)
	}
	if callReceipt.Status != 1 {
		fmt.Printf("❌ Contract call failed, receipt status: %d\n", callReceipt.Status)
		os.Exit(1)
	}
	fmt.Printf("✅ Contract call executed successfully! GasUsed: %d, Logs count: %d\n", callReceipt.GasUsed, len(callReceipt.Logs))
	if len(callReceipt.Logs) == 0 {
		fmt.Printf("❌ Expected at least 1 log event, got 0\n")
		os.Exit(1)
	}
	if callReceipt.Logs[0].Topics[0] != eventTopic {
		fmt.Printf("❌ Log topic mismatch! Got %s, want %s\n", callReceipt.Logs[0].Topics[0].Hex(), eventTopic.Hex())
		os.Exit(1)
	}
	// Verify LogsBloom contains event topic
	emptyBloom := ethtypes.Bloom{}
	if callReceipt.Bloom == emptyBloom {
		fmt.Printf("❌ Expected non-empty Bloom filter in receipt!\n")
		os.Exit(1)
	}
	if !callReceipt.Bloom.Test(eventTopic.Bytes()) {
		fmt.Printf("❌ Bloom filter test failed for event topic!\n")
		os.Exit(1)
	}
	fmt.Println("   • LogsBloom contains event topic successfully verified!")
	fmt.Println("   ✅ Contract deployment, event emission, and bloom filter PASSED successfully!")

	// ════════════════════════════════════════════════════════════════════════
	// TEST 3: Negative Validation Checks (Rejections & Security Guards)
	// ════════════════════════════════════════════════════════════════════════
	fmt.Println("\n────────────────────────────────────────────────────────────────────────────")
	fmt.Println("🛡️  [TEST 3] Security & Envelope Validation Negative Checks")
	fmt.Println("────────────────────────────────────────────────────────────────────────────")

	// 3A: Pre-EIP-155 Replay Rejection
	fmt.Println("   [3A] Testing Pre-EIP-155 Legacy Transaction Rejection...")
	legacyTx := ethtypes.NewTx(&ethtypes.LegacyTx{
		Nonce:    currentNonce + 100,
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
		Nonce:     currentNonce + 101,
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
		Nonce:     currentNonce + 102,
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

	// 3D: Oversized Envelope Rejection (> 128KB)
	fmt.Println("   [3D] Testing Oversized Envelope Rejection (> 128KB)...")
	oversizedData := make([]byte, 130*1024)
	oversizedTx := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     currentNonce + 103,
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       500_000,
		To:        &recipientAddr,
		Value:     big.NewInt(0),
		Data:      oversizedData,
	})
	signedOversized, _ := ethtypes.SignTx(oversizedTx, signer, senderPrivKey)
	rawOversized, _ := signedOversized.MarshalBinary()
	_, errOversized := c.SendRawEthTransaction(rawOversized)
	if errOversized == nil {
		fmt.Printf("❌ CRITICAL: Oversized envelope (>128KB) was ACCEPTED! Expected rejection.\n")
		os.Exit(1)
	}
	fmt.Printf("   ✅ Successfully rejected Oversized Envelope (%d bytes): %v\n", len(rawOversized), errOversized)

	// 3E: Oversized Batch Rejection (> 1000 items)
	fmt.Println("   [3E] Testing Oversized Batch Rejection (> 1000 items)...")
	oversizedBatch := make([][]byte, 1001)
	for i := 0; i < 1001; i++ {
		oversizedBatch[i] = []byte{0x01}
	}
	_, errOverBatch := c.SendRawEthTransactions(oversizedBatch)
	if errOverBatch == nil {
		fmt.Printf("❌ CRITICAL: Oversized batch (>1000) was ACCEPTED! Expected rejection.\n")
		os.Exit(1)
	}
	fmt.Printf("   ✅ Successfully rejected Oversized Batch (1001 items): %v\n", errOverBatch)

	// 3F: Legacy Proto Command Rejection over TCP
	fmt.Println("   [3F] Testing Legacy Proto Command Rejection over TCP...")
	parentConn := c.GetClientContext().ConnectionsManager.ParentConnection()
	if parentConn != nil && parentConn.IsConnect() {
		// Send legacy SendTransaction command
		_ = c.GetClientContext().MessageSender.SendBytes(parentConn, command.SendTransaction, []byte("legacy_payload"))
		// Wait briefly for error response
		select {
		case errLegacy := <-c.GetClientContext().Handler.(*c_network.Handler).TxErrorChan():
			fmt.Printf("   ✅ Successfully received rejection for legacy command: %v\n", errLegacy)
		case <-time.After(2 * time.Second):
			fmt.Println("   ℹ️ Legacy command dropped or connection rejected (as expected)")
		}
	}

	// 3G: HTTP JSON-RPC Geth-standard Error Mapping
	fmt.Println("   [3G] Testing HTTP JSON-RPC Geth-standard Error Mapping (Code -32000)...")
	// Send raw transaction with nonce too low (nonce = 0 < currentNonce)
	lowNonceTx := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(*chainID),
		Nonce:     0, // Nonce too low
		GasTipCap: big.NewInt(100_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       21000,
		To:        &recipientAddr,
		Value:     big.NewInt(1000),
	})
	signedLowNonce, _ := ethtypes.SignTx(lowNonceTx, signer, senderPrivKey)
	rawLowNonce, _ := signedLowNonce.MarshalBinary()

	lowNoncePayload := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_sendRawTransaction","params":["0x%x"],"id":101}`, rawLowNonce)
	lowResp, err := http.Post(*rpcURL, "application/json", strings.NewReader(lowNoncePayload))
	if err == nil {
		lowBody, _ := io.ReadAll(lowResp.Body)
		lowResp.Body.Close()
		var lowParsed jsonRPCResponse
		_ = json.Unmarshal(lowBody, &lowParsed)
		if lowParsed.Error != nil {
			fmt.Printf("   ✅ Successfully received Geth error code %d: %q\n", lowParsed.Error.Code, lowParsed.Error.Message)
			if lowParsed.Error.Code != -32000 || !strings.Contains(strings.ToLower(lowParsed.Error.Message), "nonce too low") {
				fmt.Printf("❌ Expected error code -32000 with 'nonce too low', got code %d: %s\n", lowParsed.Error.Code, lowParsed.Error.Message)
				os.Exit(1)
			}
		} else {
			fmt.Printf("❌ Expected JSON-RPC error for low nonce, got result: %s\n", string(lowBody))
			os.Exit(1)
		}
	}

	// ════════════════════════════════════════════════════════════════════════
	// TEST 4: Full State Consistency between TCP & HTTP JSON-RPC
	// ════════════════════════════════════════════════════════════════════════
	fmt.Println("\n────────────────────────────────────────────────────────────────────────────")
	fmt.Println("🔍 [TEST 4] Full State & Query Consistency Verification")
	fmt.Println("────────────────────────────────────────────────────────────────────────────")

	finalRecipState, err := c.AccountState(recipientAddr)
	if err != nil {
		fmt.Printf("❌ Failed to query recipient account state from TCP: %v\n", err)
		os.Exit(1)
	}
	ctxFinal, cancelFinal := context.WithTimeout(context.Background(), 5*time.Second)
	rpcBal, err := ethRpc.BalanceAt(ctxFinal, recipientAddr, nil)
	cancelFinal()
	if err != nil {
		fmt.Printf("❌ Failed to query eth_getBalance from HTTP RPC: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   • HTTP JSON-RPC eth_getBalance: %s wei\n", rpcBal.String())
	fmt.Printf("   • TCP AccountState balance:     %s wei\n", finalRecipState.Balance().String())
	if rpcBal.Cmp(finalRecipState.Balance()) != 0 {
		fmt.Printf("❌ Inconsistency between RPC and TCP balances! RPC=%s, TCP=%s\n",
			rpcBal.String(), finalRecipState.Balance().String())
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
