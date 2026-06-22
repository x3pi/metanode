package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

const (
	defaultLogLevel   = logger.FLAG_INFO
	defaultConfigPath = "config.json"
	defaultDataFile   = "data.json"
	pidFilePath       = "/tmp/tx_sender.pid"
)

var (
	CONFIG_FILE_PATH  string
	DATA_FILE_PATH    string
	LOG_LEVEL         int
	LOOP              bool
	API_URL           string
	AUTO_REGISTER_BLS bool
	ASYNC             bool
	NODE_URL          string
)

// SCData defines a single transaction action (deploy or call)
type SCData struct {
	FromAddress    string   `json:"from_address"`
	Action         string   `json:"action"`
	Input          string   `json:"input"`
	Amount         string   `json:"amount"`
	Address        string   `json:"address"`
	RelatedAddress []string `json:"related_address"`
	StorageHost    string   `json:"storage_host"`
	StorageAddress string   `json:"storage_address"`
	ReplaceAddress []int    `json:"replace_address"`
	Name           string   `json:"name"`
}

func main() {
	flag.IntVar(&LOG_LEVEL, "log-level", defaultLogLevel, "Log level")
	flag.StringVar(&CONFIG_FILE_PATH, "config", defaultConfigPath, "Config path")
	flag.StringVar(&DATA_FILE_PATH, "data", defaultDataFile, "Data file path")
	flag.BoolVar(&LOOP, "loop", false, "Loop continuously (spam mode)")
	flag.BoolVar(&AUTO_REGISTER_BLS, "register-bls", false, "Automatically check and register BLS key if missing")
	flag.StringVar(&API_URL, "api-url", "http://127.0.0.1:8757", "HTTP API URL for eth_call (read_call action)")
	flag.BoolVar(&ASYNC, "async", false, "Send all transactions asynchronously and wait for receipts at the end (groups TXs into one block)")
	flag.StringVar(&NODE_URL, "node", "", "Override config node connection address (e.g. 127.0.0.1:4200)")
	flag.Parse()

	// ═══════════════════════════════════════════════════════════════
	// PID FILE GUARD: Prevent multiple tx_sender instances
	// ═══════════════════════════════════════════════════════════════
	if pidData, err := os.ReadFile(pidFilePath); err == nil {
		if oldPid, err := strconv.Atoi(strings.TrimSpace(string(pidData))); err == nil {
			// Check if the old process is still alive
			if process, err := os.FindProcess(oldPid); err == nil {
				if err := process.Signal(syscall.Signal(0)); err == nil {
					fmt.Printf("❌ Another tx_sender is already running (PID %d)\n", oldPid)
					fmt.Printf("   If this is incorrect, or you want to force start, run the following command to clean up:\n\n")
					fmt.Printf("   kill -9 %d && rm -f %s\n\n", oldPid, pidFilePath)
					os.Exit(1)
				}
			}
		}
	}
	// Write our PID
	if err := os.WriteFile(pidFilePath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		fmt.Printf("⚠️  Could not write PID file: %v\n", err)
	}
	defer os.Remove(pidFilePath)

	// Cleanup PID file on interrupt/termination
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		os.Remove(pidFilePath)
		os.Exit(0)
	}()

	logger.SetConfig(&logger.LoggerConfig{
		Flag:    LOG_LEVEL,
		Outputs: []*os.File{os.Stdout},
	})

	// Load configuration
	configIface, err := c_config.LoadConfig(CONFIG_FILE_PATH)
	if err != nil {
		fmt.Printf("❌ Error loading config: %v\n", err)
		os.Exit(1)
	}
	config := configIface.(*c_config.ClientConfig)

	if NODE_URL != "" {
		config.ParentConnectionAddress = NODE_URL
	}

	// Load transaction data
	datas := loadTransactionData()
	fmt.Printf("📄 Loaded %d transaction(s) from %s\n", len(datas), DATA_FILE_PATH)

	// Validate transaction addresses
	addressError := false
	for i, data := range datas {
		if !validateEthAddress(data.FromAddress, "from_address", i) {
			addressError = true
		}
		if !validateEthAddress(data.Address, "address", i) {
			addressError = true
		}
		if !validateEthAddress(data.StorageAddress, "storage_address", i) {
			addressError = true
		}
		for j, relAddr := range data.RelatedAddress {
			if !validateEthAddress(relAddr, fmt.Sprintf("related_address[%d]", j), i) {
				addressError = true
			}
		}
	}
	if addressError {
		fmt.Println("❌ [CRITICAL] Dừng thực thi do phát hiện địa chỉ không đúng chuẩn Ethereum (Severe Address Format Error).")
		os.Exit(1)
	}

	// Initialize client
	fmt.Printf("🔌 Connecting to node at %s...\n", config.ParentConnectionAddress)
	c, err := client.NewClient(config)
	if err != nil {
		fmt.Printf("❌ Error connecting to node: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ Connected to node")

	if AUTO_REGISTER_BLS {
		// Check if BLS key already registered by querying on-chain nonce
		ecdsaKey, _ := crypto.ToECDSA(config.PrivateKey())
		fromAddr := crypto.PubkeyToAddress(ecdsaKey.PublicKey)
		fmt.Printf("🔑 Checking BLS registration for %s...\n", fromAddr.Hex())

		as, err := c.AccountState(fromAddr)
		if err != nil {
			fmt.Printf("⚠️  Could not check account state: %v\n", err)
		} else if as.Nonce() > 0 {
			fmt.Printf("✅ BLS key already registered (nonce=%d), skipping\n", as.Nonce())
		} else {
			fmt.Println("🔑 Registering BLS key (nonce=0)...")
			chainIdStr := fmt.Sprintf("%d", config.ChainId)
			_, err = c.AddAccountForClient(hex.EncodeToString(config.PrivateKey()), chainIdStr)
			if err != nil {
				fmt.Printf("⚠️  BLS registration: %v\n", err)
			} else {
				fmt.Println("✅ BLS key registration sent. Waiting for confirmation...")
				for waitTotal := 0; waitTotal < 10; waitTotal++ {
					time.Sleep(500 * time.Millisecond)
					asCheck, _ := c.AccountState(fromAddr)
					if asCheck != nil && asCheck.Nonce() > 0 {
						fmt.Printf("✅ BLS key confirmed on-chain! (nonce = %d)\n", asCheck.Nonce())
						break
					}
					fmt.Printf("⏳ Still waiting for BLS key confirmation...\n")
				}
			}
		}
	} else {
		fmt.Println("⏩ Skipping automatic BLS registration (--register-bls=false)")
	}

	// Run transactions
	if LOOP {
		fmt.Println("🔄 Running in loop mode (Ctrl+C to stop)")
		loopCount := 0
		for {
			loopCount++
			fmt.Printf("\n══════════════════════════════════════\n")
			fmt.Printf("  Loop #%d\n", loopCount)
			fmt.Printf("══════════════════════════════════════\n")
			processBatch(c, config, datas)
		}
	} else {
		fmt.Println("\n══════════════════════════════════════")
		fmt.Println("  Sending transactions")
		fmt.Println("══════════════════════════════════════")
		processBatch(c, config, datas)
		fmt.Println("\n✅ All transactions processed")
	}
}

func loadTransactionData() []SCData {
	dat, err := os.ReadFile(DATA_FILE_PATH)
	if err != nil {
		fmt.Printf("❌ Error reading data file '%s': %v\n", DATA_FILE_PATH, err)
		os.Exit(1)
	}
	var datas []SCData
	err = json.Unmarshal(dat, &datas)
	if err != nil {
		fmt.Printf("❌ Error parsing data file: %v\n", err)
		os.Exit(1)
	}
	return datas
}

func validateEthAddress(addr string, fieldName string, index int) bool {
	// For "address" and "storage_address", empty or "0" is acceptable as placeholders.
	if (fieldName == "address" || fieldName == "storage_address") && (addr == "" || addr == "0") {
		return true
	}

	isStandard := strings.HasPrefix(addr, "0x") || strings.HasPrefix(addr, "0X")
	if isStandard {
		isStandard = len(addr) == 42 && common.IsHexAddress(addr)
	}

	if isStandard {
		fmt.Printf("✅ [SUCCESS] Address %s (%s ở index %d) đúng chuẩn địa chỉ Ethereum.\n", addr, fieldName, index)
		return true
	} else {
		fmt.Printf("⚠️  [WARNING] Address %s (%s ở index %d) KHÔNG đúng chuẩn địa chỉ Ethereum!\n", addr, fieldName, index)
		fmt.Printf("🚨 [SEVERE ERROR] Invalid Ethereum address format: %s. Đây là lỗi nghiêm trọng!\n", addr)
		return false
	}
}

func processBatch(c *client.Client, config *c_config.ClientConfig, datas []SCData) {
	var lastDeployedAddress common.Address
	totalStart := time.Now()
	successCount := 0
	failCount := 0

	// Track the nonce locally to avoid querying the out-of-sync Sub-node immediately after a block receipt.
	initialFrom := common.HexToAddress(datas[0].FromAddress)
	as, err := c.AccountState(initialFrom)
	if err != nil {
		fmt.Printf("❌ Failed to fetch initial account state: %v\n", err)
		return
	}
	currentNonce := as.Nonce()
	pendingBalance := as.PendingBalance()

	// Variables for tracking async hashes
	var submittedHashes []common.Hash
	var submittedTxActions []string
	expectedDeployAddresses := make(map[common.Hash]common.Address)

	for i, data := range datas {
		fmt.Printf("\n──────────────────────────────────────\n")
		fmt.Printf("  [%d/%d] %s (%s)\n", i+1, len(datas), data.Name, data.Action)
		fmt.Printf("──────────────────────────────────────\n")

		// Ensure connection is alive before each TX
		if err := ensureConnected(c); err != nil {
			fmt.Printf("  ❌ Cannot connect to node: %v\n", err)
			failCount++
			continue
		}

		fromAddress := common.HexToAddress(data.FromAddress)
		amount, _ := new(big.Int).SetString(data.Amount, 10)
		if amount == nil {
			amount = big.NewInt(0)
		}

		var toAddress common.Address
		var bData []byte
		var deployedAddr common.Address

		// Process input bytes
		inputBytes := common.FromHex(data.Input)

		switch data.Action {
		case "deploy":
			if len(inputBytes) == 0 {
				fmt.Printf("  ❌ Deploy failed: bytecode (input) is empty or invalid hex!\n")
				failCount++
				continue
			}
			deployedAddr = crypto.CreateAddress(fromAddress, currentNonce)
			lastDeployedAddress = deployedAddr
			fmt.Printf("  📋 From:              %s\n", fromAddress.Hex())
			fmt.Printf("  📋 Predicted address:  %s\n", deployedAddr.Hex())
			fmt.Printf("  📋 Nonce:             %d\n", currentNonce)

			storageAddr := common.HexToAddress(data.StorageAddress)
			deployData := transaction.NewDeployData(inputBytes, storageAddr)
			bData, err = deployData.Marshal()
			if err != nil {
				fmt.Printf("  ❌ Marshal deploy data failed: %v\n", err)
				failCount++
				continue
			}
			toAddress = common.Address{} // deploy to 0x0

		case "call":
			if data.Address == "0" || data.Address == "" {
				toAddress = lastDeployedAddress
				fmt.Printf("  📋 Using last deployed: %s\n", toAddress.Hex())
			} else {
				toAddress = common.HexToAddress(data.Address)
			}
			fmt.Printf("  📋 From:    %s\n", fromAddress.Hex())
			fmt.Printf("  📋 To:      %s\n", toAddress.Hex())

			callData := transaction.NewCallData(inputBytes)
			var err error
			bData, err = callData.Marshal()
			if err != nil {
				fmt.Printf("  ❌ Marshal call data failed: %v\n", err)
				failCount++
				continue
			}

		case "read_call":
			// ═══════════════════════════════════════════════════════════════
			// OFF-CHAIN READ: Uses TCP ReadTransaction.
			// Does NOT go on-chain, does NOT increment nonce, does NOT consume gas.
			// ═══════════════════════════════════════════════════════════════
			if data.Address == "0" || data.Address == "" {
				toAddress = lastDeployedAddress
				fmt.Printf("  📋 Using last deployed: %s\n", toAddress.Hex())
			} else {
				toAddress = common.HexToAddress(data.Address)
			}
			fmt.Printf("  📋 From:    %s\n", fromAddress.Hex())
			fmt.Printf("  📋 To:      %s\n", toAddress.Hex())
			fmt.Printf("  📖 Mode:    OFF-CHAIN READ (via TCP)\n")

			// Build CallData proto wrapper
			readCallData := transaction.NewCallData(inputBytes)
			readBData, readMarshalErr := readCallData.Marshal()
			if readMarshalErr != nil {
				fmt.Printf("  ❌ Marshal CallData failed: %v\n", readMarshalErr)
				failCount++
				continue
			}

			// Build related addresses
			readRelatedMap := make(map[common.Address]bool)
			readRelatedMap[fromAddress] = true
			if toAddress != (common.Address{}) {
				readRelatedMap[toAddress] = true
			}
			readRelatedMap[bls.NewKeyPair(config.PrivateKey()).Address()] = true
			for _, v := range data.RelatedAddress {
				readRelatedMap[common.HexToAddress(v)] = true
			}

			var readRelatedAddresses []common.Address
			for addr := range readRelatedMap {
				readRelatedAddresses = append(readRelatedAddresses, addr)
			}

			fmt.Printf("  📤 Sending ReadTransaction (off-chain via TCP)...\n")
			readStart := time.Now()
			receipt, readErr := c.ReadTransaction(
				fromAddress,
				toAddress,
				big.NewInt(0),
				readBData,
				readRelatedAddresses,
				10000000,
				uint64(p_common.MINIMUM_BASE_FEE),
				0,
			)
			readDuration := time.Since(readStart)

			if readErr != nil {
				fmt.Printf("  ❌ read_call FAILED: %v\n", readErr)
				fmt.Printf("  ⏱  Duration: %s\n", readDuration)
				failCount++
				continue
			}

			if receipt == nil {
				fmt.Printf("  ❌ read_call FAILED: receipt is nil\n")
				fmt.Printf("  ⏱  Duration: %s\n", readDuration)
				failCount++
				continue
			}

			fmt.Printf("  ✅ Read result received!\n")
			fmt.Printf("  ⏱  Duration:    %s\n", readDuration)
			returnData := receipt.Return()
			if len(returnData) > 0 {
				fmt.Printf("  📋 Return data: %s\n", hex.EncodeToString(returnData))
				// Try to decode as uint256
				if len(returnData) == 32 {
					val := new(big.Int).SetBytes(returnData)
					fmt.Printf("  📋 Decoded:     %s\n", val.String())
				}
			} else {
				fmt.Printf("  📋 Return data: (empty)\n")
			}

			successCount++
			// read_call does NOT go through the on-chain TX path below
			continue

		default:
			fmt.Printf("  ⚠️  Unknown action: %s, skipping\n", data.Action)
			continue
		}

		// Build related addresses
		relatedMap := make(map[common.Address]bool)
		relatedMap[fromAddress] = true
		if data.Action == "deploy" {
			relatedMap[lastDeployedAddress] = true
		} else if toAddress != (common.Address{}) {
			relatedMap[toAddress] = true
		}
		relatedMap[bls.NewKeyPair(config.PrivateKey()).Address()] = true
		for _, v := range data.RelatedAddress {
			relatedMap[common.HexToAddress(v)] = true
		}

		var relatedAddresses []common.Address
		for addr := range relatedMap {
			relatedAddresses = append(relatedAddresses, addr)
		}

		bRelatedAddresses := make([][]byte, len(relatedAddresses))
		for k, v := range relatedAddresses {
			bRelatedAddresses[k] = v.Bytes()
		}

		lastDeviceKey := common.HexToHash("0000000000000000000000000000000000000000000000000000000000000000")
		newDeviceKey := common.HexToHash("0000000000000000000000000000000000000000000000000000000000000000")

		fmt.Printf("  📤 Sending transaction (locally tracked nonce: %d)...\n", currentNonce)
		txStart := time.Now()

		tx, reqErr := c.GetTransactionController().SendTransaction(
			fromAddress,
			toAddress,
			pendingBalance,
			amount,
			10000000,                          // maxGas
			uint64(p_common.MINIMUM_BASE_FEE), // maxGasPrice
			0,                                 // maxTimeUse
			bData,
			bRelatedAddresses,
			lastDeviceKey,
			newDeviceKey,
			currentNonce,
			config.ChainId,
		)

		if reqErr != nil {
			fmt.Printf("  ❌ Failed to construct TX: %v\n", reqErr)
			failCount++
			continue
		}

		if data.Action == "deploy" {
			expectedDeployAddresses[tx.Hash()] = deployedAddr
		}

		// Update local nonce for sequence on success
		currentNonce++

		// In async mode, we collect the hashes and wait at the end
		if ASYNC {
			fmt.Printf("  ✅ TX sent to node mempool (Hash: %s). Waiting for receipt later...\n", tx.Hash().Hex())
			submittedHashes = append(submittedHashes, tx.Hash())
			submittedTxActions = append(submittedTxActions, data.Action)
			continue
		}

		// Sync mode: Wait for the receipt immediately
		var receipt types.Receipt
		var txErr error
		for attempt := 1; attempt <= 20; attempt++ {
			receipt, txErr = c.FindReceiptByHash(tx.Hash())
			if txErr == nil && receipt != nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		txDuration := time.Since(txStart)

		if txErr != nil || receipt == nil {
			errStr := txErr
			if errStr == nil {
				errStr = fmt.Errorf("receipt is nil")
			}
			fmt.Printf("  ❌ Transaction FAILED after 20 attempts: %v\n", errStr)
			fmt.Printf("  ⏱  Duration: %s\n", txDuration)
			failCount++

			// Refresh sync from network just in case
			as, err := c.AccountState(fromAddress)
			if err == nil {
				currentNonce = as.Nonce()
				pendingBalance = as.PendingBalance()
			}
			time.Sleep(1 * time.Second)
			continue
		}

		// Log receipt details
		status := receipt.Status()
		txHash := receipt.TransactionHash()
		fmt.Printf("  ✅ Receipt received!\n")
		fmt.Printf("  📋 TX Hash:     %s\n", txHash.Hex())
		fmt.Printf("  📋 Status:      %s\n", formatStatus(status))
		fmt.Printf("  📋 Exception:   %s\n", receipt.Exception().String())
		fmt.Printf("  ⏱  Duration:    %s\n", txDuration)

		if status == pb.RECEIPT_STATUS_RETURNED {
			successCount++
			if data.Action == "deploy" {
				expectedAddr := expectedDeployAddresses[tx.Hash()]
				if len(receipt.Return()) == 20 {
					actualAddr := common.BytesToAddress(receipt.Return())
					fmt.Printf("  📋 Actually Deployed to (from receipt): %s\n", actualAddr.Hex())
					fmt.Printf("  📋 Predicted (Metanode & ETH Standard): %s\n", expectedAddr.Hex())
					if actualAddr != expectedAddr {
						fmt.Printf("  ❌ ERROR: Address mismatch! Receipt address does not match predicted address!\n")
					} else {
						fmt.Printf("  ✅ SUCCESS: Address matches predicted calculation!\n")
					}
					lastDeployedAddress = actualAddr
				} else {
					fmt.Printf("  ⚠️  WARNING: Receipt returned data length is %d (expected 20 bytes for contract address). Using predicted address: %s\n", len(receipt.Return()), expectedAddr.Hex())
					lastDeployedAddress = expectedAddr
				}
				fmt.Printf("  ⏳ Waiting 500ms for state propagation...\n")
				time.Sleep(500 * time.Millisecond)

				// Verify deployment
				asContract, _ := c.AccountState(lastDeployedAddress)
				if asContract != nil {
					fmt.Printf("  🔍 Contract Nonce at predicted addr: %v\n", asContract.Nonce())
				} else {
					fmt.Printf("  ❌ Contract AccountState is NIL!\n")
				}
			} else if len(receipt.Return()) > 0 {
				fmt.Printf("  📋 Return data: %s\n", hex.EncodeToString(receipt.Return()))
			}
		} else {
			failCount++
			fmt.Printf("  ⚠️  Transaction not successful (status: %s)\n", formatStatus(status))
			if len(receipt.Return()) > 0 {
				fmt.Printf("  📋 Return data: %s\n", hex.EncodeToString(receipt.Return()))
			}
		}
	}

	if ASYNC && len(submittedHashes) > 0 {
		fmt.Printf("\n──────────────────────────────────────\n")
		fmt.Printf("  ⏳ Async mode: Waiting for %d receipts...\n", len(submittedHashes))
		fmt.Printf("──────────────────────────────────────\n")

		for i, hash := range submittedHashes {
			action := submittedTxActions[i]
			fmt.Printf("  🔍 Waiting for TX %s...\n", hash.Hex())
			startWait := time.Now()

			var receipt types.Receipt
			var err error
			for attempt := 1; attempt <= 40; attempt++ {
				receipt, err = c.FindReceiptByHash(hash)
				if err == nil && receipt != nil {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			waitDuration := time.Since(startWait)

			if err != nil || receipt == nil {
				errStr := err
				if errStr == nil {
					errStr = fmt.Errorf("receipt is nil")
				}
				fmt.Printf("  ❌ Failed to get receipt for %s after 40 attempts: %v\n", hash.Hex(), errStr)
				failCount++
				continue
			}

			status := receipt.Status()
			fmt.Printf("  ✅ Receipt %d/%d received!\n", i+1, len(submittedHashes))
			fmt.Printf("  📋 Status:      %s\n", formatStatus(status))
			fmt.Printf("  📋 Exception:   %s\n", receipt.Exception().String())
			fmt.Printf("  ⏱  Wait time:   %s\n", waitDuration)

			if status == pb.RECEIPT_STATUS_RETURNED {
				successCount++
				if action == "deploy" {
					expectedAddr := expectedDeployAddresses[hash]
					if len(receipt.Return()) == 20 {
						actualAddr := common.BytesToAddress(receipt.Return())
						fmt.Printf("  📋 Actually Deployed to (from receipt): %s\n", actualAddr.Hex())
						fmt.Printf("  📋 Predicted (Metanode & ETH Standard): %s\n", expectedAddr.Hex())
						if actualAddr != expectedAddr {
							fmt.Printf("  ❌ ERROR: Address mismatch! Receipt address does not match predicted address!\n")
						} else {
							fmt.Printf("  ✅ SUCCESS: Address matches predicted calculation!\n")
						}
						lastDeployedAddress = actualAddr
					} else {
						fmt.Printf("  ⚠️  WARNING: Receipt returned data length is %d (expected 20 bytes for contract address). Using predicted address: %s\n", len(receipt.Return()), expectedAddr.Hex())
						lastDeployedAddress = expectedAddr
					}
				} else if len(receipt.Return()) > 0 {
					fmt.Printf("  📋 Return data: %s\n", hex.EncodeToString(receipt.Return()))
				}
			} else {
				failCount++
				fmt.Printf("  ⚠️  Transaction not successful (status: %s)\n", formatStatus(status))
				if len(receipt.Return()) > 0 {
					fmt.Printf("  📋 Return data: %s\n", hex.EncodeToString(receipt.Return()))
				}
			}
		}
	}

	totalDuration := time.Since(totalStart)
	fmt.Printf("\n══════════════════════════════════════\n")
	fmt.Printf("  Summary\n")
	fmt.Printf("══════════════════════════════════════\n")
	fmt.Printf("  ✅ Success: %d\n", successCount)
	fmt.Printf("  ❌ Failed:  %d\n", failCount)
	fmt.Printf("  📊 Total:   %d\n", successCount+failCount)
	fmt.Printf("  ⏱  Time:    %s\n", totalDuration)
}

func ensureConnected(c *client.Client) error {
	ctx := c.GetClientContext()
	parentConn := ctx.ConnectionsManager.ParentConnection()
	if parentConn == nil || !parentConn.IsConnect() {
		fmt.Printf("  🔌 Connection lost, reconnecting...\n")
		if err := c.ReconnectToParent(); err != nil {
			return fmt.Errorf("reconnect failed: %w", err)
		}
		fmt.Printf("  ✅ Reconnected\n")
	}
	return nil
}

func formatStatus(status pb.RECEIPT_STATUS) string {
	switch status {
	case pb.RECEIPT_STATUS_RETURNED:
		return "RETURNED ✅"
	case pb.RECEIPT_STATUS_TRANSACTION_ERROR:
		return "TRANSACTION_ERROR ❌"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", status)
	}
}


