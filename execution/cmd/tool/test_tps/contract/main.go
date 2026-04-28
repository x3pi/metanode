package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

type accountInfo struct {
	Index      int    `json:"index"`
	PrivateKey string `json:"private_key"`
	Address    string `json:"address"`
}

type contractConfig struct {
	BytecodeAlt string `json:"bytescode"`
	Bytecode    string `json:"bytecode"`
	RPC         string `json:"rpc"`
	ChainID     int64  `json:"chain_id"`
}

type deployedContract struct {
	ContractAddress string `json:"contract_address"`
	DeployerAddress string `json:"deployer_address"`
	KeyIndex        int    `json:"key_index"`
	DeployTxHash    string `json:"deploy_tx_hash"`
	DeployNonce     uint64 `json:"deploy_nonce"`
	DeployedAt      string `json:"deployed_at"`
}

type setValueResult struct {
	ContractAddress string `json:"contract_address"`
	CallerAddress   string `json:"caller_address"`
	Value           uint64 `json:"value"`
	Nonce           uint64 `json:"nonce"`
	TxHash          string `json:"tx_hash"`
	SentAt          string `json:"sent_at"`
}

type rpcClient struct {
	endpoint string
	http     *http.Client
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newRPCClient(endpoint string) *rpcClient {
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 100
	t.MaxConnsPerHost = 100
	t.MaxIdleConnsPerHost = 100
	return &rpcClient{
		endpoint: endpoint,
		http: &http.Client{
			Timeout:   20 * time.Second,
			Transport: t,
		},
	}
}

func (c *rpcClient) call(method string, params ...interface{}) (json.RawMessage, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Post(c.endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rpc status: %d", resp.StatusCode)
	}
	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", out.Error.Code, out.Error.Message)
	}
	return out.Result, nil
}

func (c *rpcClient) gasPrice() (*big.Int, error) {
	result, err := c.call("eth_gasPrice")
	if err != nil {
		return nil, err
	}
	var hexGas string
	if err := json.Unmarshal(result, &hexGas); err != nil {
		return nil, err
	}
	hexGas = strings.TrimPrefix(hexGas, "0x")
	if hexGas == "" {
		return big.NewInt(0), nil
	}
	v, ok := new(big.Int).SetString(hexGas, 16)
	if !ok {
		return nil, fmt.Errorf("cannot parse gas price: %s", hexGas)
	}
	return v, nil
}

func (c *rpcClient) estimateGas(callObj map[string]interface{}) (uint64, error) {
	result, err := c.call("eth_estimateGas", callObj)
	if err != nil {
		return 0, err
	}
	var hexGas string
	if err := json.Unmarshal(result, &hexGas); err != nil {
		return 0, err
	}
	hexGas = strings.TrimPrefix(hexGas, "0x")
	if hexGas == "" {
		return 0, fmt.Errorf("empty gas estimate")
	}
	v, ok := new(big.Int).SetString(hexGas, 16)
	if !ok {
		return 0, fmt.Errorf("cannot parse gas estimate: %s", hexGas)
	}
	return v.Uint64(), nil
}

func (c *rpcClient) getTransactionCount(addr string) (uint64, error) {
	result, err := c.call("eth_getTransactionCount", addr, "latest")
	if err != nil {
		return 0, err
	}
	raw := string(result)
	if raw == "null" || raw == "" {
		return 0, nil
	}
	var hexNonce string
	if err := json.Unmarshal(result, &hexNonce); err == nil {
		hexNonce = strings.TrimPrefix(hexNonce, "0x")
		if hexNonce == "" {
			return 0, nil
		}
		v, ok := new(big.Int).SetString(hexNonce, 16)
		if !ok {
			return 0, fmt.Errorf("cannot parse hex nonce: %s", hexNonce)
		}
		return v.Uint64(), nil
	}
	var intNonce uint64
	if err := json.Unmarshal(result, &intNonce); err == nil {
		return intNonce, nil
	}
	return 0, fmt.Errorf("unexpected nonce payload: %s", raw)
}

func (c *rpcClient) sendRawTransaction(rawTx []byte) (string, error) {
	rawHex := "0x" + hex.EncodeToString(rawTx)
	result, err := c.call("eth_sendRawTransaction", rawHex, nil, nil)
	if err != nil {
		return "", err
	}
	var txHash string
	if err := json.Unmarshal(result, &txHash); err != nil {
		return "", fmt.Errorf("cannot decode tx hash: %w", err)
	}
	return txHash, nil
}

func (c *rpcClient) getReceipt(txHash string) (map[string]interface{}, error) {
	result, err := c.call("eth_getTransactionReceipt", txHash)
	if err != nil {
		return nil, err
	}
	if string(result) == "null" {
		return nil, nil
	}
	var receipt map[string]interface{}
	if err := json.Unmarshal(result, &receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

func loadJSON(path string, out interface{}) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func saveJSON(path string, in interface{}) error {
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

func normalizePrivKey(pk string) string {
	pk = strings.TrimSpace(pk)
	pk = strings.TrimPrefix(pk, "0x")
	return pk
}

func normalizeAddress(addr string) string {
	return strings.ToLower(strings.TrimSpace(addr))
}

func readBytecode(path string) ([]byte, error) {
	var cfg contractConfig
	if err := loadJSON(path, &cfg); err != nil {
		return nil, err
	}
	hexCode := strings.TrimSpace(cfg.Bytecode)
	if hexCode == "" {
		hexCode = strings.TrimSpace(cfg.BytecodeAlt)
	}
	if hexCode == "" {
		return nil, fmt.Errorf("missing bytecode/bytescode in %s", path)
	}
	hexCode = strings.TrimPrefix(hexCode, "0x")
	return hex.DecodeString(hexCode)
}

func mustABI(path string) abi.ABI {
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	parsed, err := abi.JSON(bytes.NewReader(b))
	if err != nil {
		panic(err)
	}
	return parsed
}

func loadToolConfig(path string) (*contractConfig, error) {
	var cfg contractConfig
	if err := loadJSON(path, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func resolveRPCAndChainID(cfg *contractConfig) (string, int64, error) {
	if cfg.RPC == "" {
		return "", 0, fmt.Errorf("rpc url is empty in config")
	}
	if cfg.ChainID == 0 {
		return "", 0, fmt.Errorf("chain_id is empty in config")
	}
	return cfg.RPC, cfg.ChainID, nil
}

func uniqueContracts(in []deployedContract) []deployedContract {
	seen := make(map[string]bool)
	out := make([]deployedContract, 0, len(in))
	for _, c := range in {
		addr := normalizeAddress(c.ContractAddress)
		if addr == "" || addr == "0x0000000000000000000000000000000000000000" {
			continue
		}
		if seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, c)
	}
	return out
}

func loadDeployedContracts(path string) ([]deployedContract, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []deployedContract{}, nil
		}
		return nil, err
	}
	var out []deployedContract
	if err := loadJSON(path, &out); err != nil {
		return nil, err
	}
	return uniqueContracts(out), nil
}

func waitForReceipt(ctx context.Context, rpc *rpcClient, txHash string, every time.Duration) (map[string]interface{}, error) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		receipt, err := rpc.getReceipt(txHash)
		if err == nil && receipt != nil {
			return receipt, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func autoDeployGasLimit(rpc *rpcClient, from string, data []byte, fallback uint64) uint64 {
	gas, err := rpc.estimateGas(map[string]interface{}{
		"from": from,
		"data": "0x" + hex.EncodeToString(data),
	})
	if err != nil || gas == 0 {
		return fallback
	}
	gas = uint64(float64(gas) * 1.2)
	if gas < fallback {
		return fallback
	}
	return gas
}

func autoCallGasLimit(rpc *rpcClient, from string, to string, data []byte, fallback uint64) uint64 {
	gas, err := rpc.estimateGas(map[string]interface{}{
		"from": from,
		"to":   to,
		"data": "0x" + hex.EncodeToString(data),
	})
	if err != nil || gas == 0 {
		return fallback
	}
	gas = uint64(float64(gas) * 1.2)
	if gas < fallback {
		return fallback
	}
	return gas
}

func main() {
	var (
		mode            string
		configFile      string
		keysFile        string
		bytecodeFile    string
		deployedOutFile string
		setResultFile   string
		walletCount     int
		contractCount   int
		workers         int
		setStartValue   uint64
		waitReceipt     bool
		receiptTimeout  int
	)

	flag.StringVar(&mode, "mode", "deploy", "Mode: deploy|setvalue")
	flag.StringVar(&configFile, "config", "./config.json", "Tool config JSON with rpc and chain_id")
	flag.StringVar(&keysFile, "keys", "../gen_spam_keys/generated_keys.json", "Keys JSON file")
	flag.StringVar(&bytecodeFile, "bytecode-config", "./config.json", "Bytecode config JSON")
	flag.StringVar(&deployedOutFile, "deployed-file", "./deployed_contracts.json", "Persisted deployed contract list")
	flag.StringVar(&setResultFile, "setvalue-result", "./setvalue_results.json", "SetValue tx result file")
	flag.IntVar(&walletCount, "wallet-count", 10, "How many wallets to use for deploy")
	flag.IntVar(&contractCount, "contract-count", 10, "How many contracts to use for setvalue")
	flag.IntVar(&workers, "workers", 20, "Parallel workers")
	flag.Uint64Var(&setStartValue, "set-start", 1, "Initial value used by first setValue call")
	flag.BoolVar(&waitReceipt, "wait-receipt", true, "Wait for tx receipt and update deployed contract address by receipt")
	flag.IntVar(&receiptTimeout, "receipt-timeout", 120, "Receipt timeout seconds")
	flag.Parse()

	if workers <= 0 {
		workers = 1
	}
	if walletCount <= 0 {
		logger.Error("wallet-count must be > 0")
		os.Exit(1)
	}
	if contractCount <= 0 {
		logger.Error("contract-count must be > 0")
		os.Exit(1)
	}

	var keys []accountInfo
	if err := loadJSON(keysFile, &keys); err != nil {
		logger.Error("cannot read keys file %s: %v", keysFile, err)
		os.Exit(1)
	}
	if len(keys) == 0 {
		logger.Error("keys file is empty")
		os.Exit(1)
	}

	cfg, err := loadToolConfig(configFile)
	if err != nil {
		logger.Error("cannot read config file %s: %v", configFile, err)
		os.Exit(1)
	}
	rpcURL, chainID, err := resolveRPCAndChainID(cfg)
	if err != nil {
		logger.Error("invalid config: %v", err)
		os.Exit(1)
	}

	rpc := newRPCClient(rpcURL)
	gasPriceWei, err := rpc.gasPrice()
	if err != nil {
		gasPriceWei = big.NewInt(0)
	}

	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "deploy":
		if walletCount > len(keys) {
			walletCount = len(keys)
		}
		if err := runDeploy(rpc, chainID, keys[:walletCount], bytecodeFile, deployedOutFile, gasPriceWei, waitReceipt, receiptTimeout, workers); err != nil {
			logger.Error("deploy failed: %v", err)
			os.Exit(1)
		}
	case "setvalue":
		if err := runSetValue(rpc, chainID, keys, "./abi.json", deployedOutFile, setResultFile, contractCount, workers, setStartValue, gasPriceWei, waitReceipt, receiptTimeout); err != nil {
			logger.Error("setvalue failed: %v", err)
			os.Exit(1)
		}
	default:
		logger.Error("invalid mode. Use deploy or setvalue")
		os.Exit(1)
	}
}

func runDeploy(
	rpc *rpcClient,
	chainID int64,
	keys []accountInfo,
	bytecodeFile string,
	deployedOutFile string,
	gasPriceWei *big.Int,
	waitReceipt bool,
	receiptTimeout int,
	workers int,
) error {
	bytecode, err := readBytecode(bytecodeFile)
	if err != nil {
		return err
	}

	logger.Info("========================================")
	logger.Info("Deploy mode")
	logger.Info("RPC: %s", rpc.endpoint)
	logger.Info("Wallets: %d", len(keys))
	logger.Info("Workers: %d", workers)
	if waitReceipt {
		logger.Info("Mode note: waiting for receipt after send, so output may appear in batches")
	}
	logger.Info("========================================")

	type deployJob struct {
		Idx int
		Acc accountInfo
	}
	type deployResult struct {
		Record deployedContract
		Err    error
	}

	jobs := make(chan deployJob)
	results := make(chan deployResult, len(keys))
	var started int64
	var finished int64

	workerFn := func() {
		for j := range jobs {
			jobNo := atomic.AddInt64(&started, 1)
			acc := j.Acc
			logger.Info("deploy-start %d/%d key_index=%d address=%s", jobNo, len(keys), acc.Index, acc.Address)
			privHex := normalizePrivKey(acc.PrivateKey)
			pk, err := crypto.HexToECDSA(privHex)
			if err != nil {
				results <- deployResult{Err: fmt.Errorf("key[%d] invalid private key: %w", acc.Index, err)}
				continue
			}

			from := crypto.PubkeyToAddress(pk.PublicKey)
			nonce, err := rpc.getTransactionCount(from.Hex())
			if err != nil {
				results <- deployResult{Err: fmt.Errorf("key[%d] cannot get nonce: %w", acc.Index, err)}
				continue
			}
			gasLimit := autoDeployGasLimit(rpc, from.Hex(), bytecode, 600000)
			logger.Info("deploy-build key_index=%d from=%s nonce=%d gas=%d", acc.Index, from.Hex(), nonce, gasLimit)

			tx := types.NewTx(&types.LegacyTx{
				Nonce:    nonce,
				GasPrice: gasPriceWei,
				Gas:      gasLimit,
				To:       nil,
				Value:    big.NewInt(0),
				Data:     bytecode,
			})

			signedTx, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(chainID)), pk)
			if err != nil {
				results <- deployResult{Err: fmt.Errorf("key[%d] sign failed: %w", acc.Index, err)}
				continue
			}
			rawTx, err := signedTx.MarshalBinary()
			if err != nil {
				results <- deployResult{Err: fmt.Errorf("key[%d] marshal tx failed: %w", acc.Index, err)}
				continue
			}

			txHash, err := rpc.sendRawTransaction(rawTx)
			if err != nil {
				results <- deployResult{Err: fmt.Errorf("key[%d] send tx failed: %w", acc.Index, err)}
				continue
			}
			logger.Info("deploy-sent key_index=%d tx=%s", acc.Index, txHash)

			rec := deployedContract{
				ContractAddress: crypto.CreateAddress(from, nonce).Hex(),
				DeployerAddress: from.Hex(),
				KeyIndex:        acc.Index,
				DeployTxHash:    txHash,
				DeployNonce:     nonce,
				DeployedAt:      time.Now().UTC().Format(time.RFC3339),
			}

			if waitReceipt {
				logger.Info("deploy-wait key_index=%d tx=%s", acc.Index, txHash)
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(receiptTimeout)*time.Second)
				receipt, rErr := waitForReceipt(ctx, rpc, txHash, 2*time.Second)
				cancel()
				if rErr == nil {
					if cAddrAny, ok := receipt["contractAddress"]; ok {
						if cAddr, ok := cAddrAny.(string); ok && common.IsHexAddress(cAddr) {
							rec.ContractAddress = common.HexToAddress(cAddr).Hex()
						} else {
							logger.Warn("deploy receipt missing contractAddress for tx=%s", txHash)
						}
					} else {
						logger.Warn("deploy receipt wait finished with non-fatal error for tx=%s: %v", txHash, rErr)
					}
				}
			}

			atomic.AddInt64(&finished, 1)
			results <- deployResult{Record: rec}
			logger.Info("deploy-done %d/%d contract=%s", atomic.LoadInt64(&finished), len(keys), rec.ContractAddress)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerFn()
		}()
	}

	go func() {
		for i, acc := range keys {
			jobs <- deployJob{Idx: i, Acc: acc}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	newRecords := make([]deployedContract, 0, len(keys))
	errCount := 0
	for r := range results {
		if r.Err != nil {
			errCount++
			logger.Error("[deploy-error] %v", r.Err)
			continue
		}
		newRecords = append(newRecords, r.Record)
		logger.Info("[deploy-ok] %s -> %s | tx=%s", r.Record.DeployerAddress, r.Record.ContractAddress, r.Record.DeployTxHash)
	}

	existing, err := loadDeployedContracts(deployedOutFile)
	if err != nil {
		return err
	}
	all := append(existing, newRecords...)
	all = uniqueContracts(all)
	sort.Slice(all, func(i, j int) bool {
		return all[i].ContractAddress < all[j].ContractAddress
	})
	if err := saveJSON(deployedOutFile, all); err != nil {
		return err
	}

	logger.Info("========================================")
	logger.Info("Deploy finished. success=%d failed=%d", len(newRecords), errCount)
	logger.Info("Saved unique deployed contracts: %d -> %s", len(all), deployedOutFile)
	logger.Info("========================================")

	if len(newRecords) == 0 {
		return fmt.Errorf("all deploy tx failed")
	}
	return nil
}

func runSetValue(
	rpc *rpcClient,
	chainID int64,
	keys []accountInfo,
	abiFile string,
	deployedFile string,
	setResultFile string,
	contractCount int,
	workers int,
	startValue uint64,
	gasPriceWei *big.Int,
	waitReceipt bool,
	receiptTimeout int,
) error {
	parsedABI := mustABI(abiFile)
	if _, ok := parsedABI.Methods["setValue"]; !ok {
		return fmt.Errorf("method setValue not found in ABI")
	}

	allContracts, err := loadDeployedContracts(deployedFile)
	if err != nil {
		return err
	}
	if len(allContracts) == 0 {
		return fmt.Errorf("no contracts found in %s", deployedFile)
	}
	if contractCount > len(allContracts) {
		contractCount = len(allContracts)
	}
	selected := allContracts[:contractCount]

	keyByIndex := make(map[int]accountInfo)
	keyByAddress := make(map[string]accountInfo)
	for _, k := range keys {
		keyByIndex[k.Index] = k
		keyByAddress[normalizeAddress(k.Address)] = k
	}

	type nonceState struct {
		mu   sync.Mutex
		next uint64
	}
	nonceBySigner := make(map[string]*nonceState)
	for _, c := range selected {
		var acc accountInfo
		var ok bool
		if acc, ok = keyByIndex[c.KeyIndex]; !ok {
			acc, ok = keyByAddress[normalizeAddress(c.DeployerAddress)]
		}
		if !ok {
			return fmt.Errorf("cannot map key for deployer=%s key_index=%d", c.DeployerAddress, c.KeyIndex)
		}
		signerAddr := normalizeAddress(acc.Address)
		if _, exists := nonceBySigner[signerAddr]; exists {
			continue
		}
		nonce, nErr := rpc.getTransactionCount(common.HexToAddress(acc.Address).Hex())
		if nErr != nil {
			return fmt.Errorf("cannot get nonce for signer %s: %w", acc.Address, nErr)
		}
		nonceBySigner[signerAddr] = &nonceState{next: nonce}
	}

	logger.Info("========================================")
	logger.Info("SetValue mode")
	logger.Info("RPC: %s", rpc.endpoint)
	logger.Info("Contracts requested: %d", contractCount)
	logger.Info("Unique contracts selected: %d", len(selected))
	logger.Info("Workers: %d", workers)
	if waitReceipt {
		logger.Info("Mode note: waiting for receipt after each setValue tx")
	}
	logger.Info("========================================")

	type setJob struct {
		Idx int
		Rec deployedContract
	}
	type setRes struct {
		Result setValueResult
		Err    error
	}

	jobs := make(chan setJob)
	results := make(chan setRes, len(selected))
	var started int64
	var finished int64

	workerFn := func() {
		for j := range jobs {
			jobNo := atomic.AddInt64(&started, 1)
			rec := j.Rec
			logger.Info("setvalue-start %d/%d contract=%s", jobNo, len(selected), rec.ContractAddress)

			var acc accountInfo
			var ok bool
			if acc, ok = keyByIndex[rec.KeyIndex]; !ok {
				acc, ok = keyByAddress[normalizeAddress(rec.DeployerAddress)]
			}
			if !ok {
				results <- setRes{Err: fmt.Errorf("cannot find signer for contract %s", rec.ContractAddress)}
				continue
			}

			privHex := normalizePrivKey(acc.PrivateKey)
			pk, err := crypto.HexToECDSA(privHex)
			if err != nil {
				results <- setRes{Err: fmt.Errorf("invalid key index=%d: %w", acc.Index, err)}
				continue
			}
			from := crypto.PubkeyToAddress(pk.PublicKey)
			fromNorm := normalizeAddress(from.Hex())
			state := nonceBySigner[fromNorm]
			if state == nil {
				results <- setRes{Err: fmt.Errorf("missing nonce state for signer %s", from.Hex())}
				continue
			}

			state.mu.Lock()
			nonce := state.next
			state.next++
			state.mu.Unlock()

			valueArg := new(big.Int).SetUint64(startValue + uint64(j.Idx))
			data, err := parsedABI.Pack("setValue", valueArg)
			if err != nil {
				results <- setRes{Err: fmt.Errorf("abi pack failed for %s: %w", rec.ContractAddress, err)}
				continue
			}
			gasLimit := autoCallGasLimit(rpc, from.Hex(), rec.ContractAddress, data, 120000)
			logger.Info("setvalue-build contract=%s from=%s nonce=%d gas=%d value=%d", rec.ContractAddress, from.Hex(), nonce, gasLimit, valueArg.Uint64())

			to := common.HexToAddress(rec.ContractAddress)
			tx := types.NewTx(&types.LegacyTx{
				Nonce:    nonce,
				GasPrice: gasPriceWei,
				Gas:      gasLimit,
				To:       &to,
				Value:    big.NewInt(0),
				Data:     data,
			})
			signed, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(chainID)), pk)
			if err != nil {
				results <- setRes{Err: fmt.Errorf("sign failed for %s: %w", rec.ContractAddress, err)}
				continue
			}
			raw, err := signed.MarshalBinary()
			if err != nil {
				results <- setRes{Err: fmt.Errorf("marshal failed for %s: %w", rec.ContractAddress, err)}
				continue
			}

			txHash, err := rpc.sendRawTransaction(raw)
			if err != nil {
				results <- setRes{Err: fmt.Errorf("send failed for %s: %w", rec.ContractAddress, err)}
				continue
			}
			logger.Info("setvalue-sent contract=%s tx=%s", rec.ContractAddress, txHash)

			if waitReceipt {
				logger.Info("setvalue-wait contract=%s tx=%s", rec.ContractAddress, txHash)
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(receiptTimeout)*time.Second)
				receipt, rErr := waitForReceipt(ctx, rpc, txHash, 2*time.Second)
				cancel()
				if rErr != nil {
					results <- setRes{Err: fmt.Errorf("receipt timeout for %s: %w", rec.ContractAddress, rErr)}
					continue
				}
				if statusRaw, ok := receipt["status"]; ok {
					statusStr := fmt.Sprint(statusRaw)
					if statusStr == "0x0" || statusStr == "0" || strings.EqualFold(statusStr, "failed") {
						results <- setRes{Err: fmt.Errorf("tx failed for %s: status=%v", rec.ContractAddress, statusRaw)}
						continue
					}
				}
				logger.Info("setvalue-receipt contract=%s tx=%s status=success", rec.ContractAddress, txHash)
			}

			atomic.AddInt64(&finished, 1)
			results <- setRes{Result: setValueResult{
				ContractAddress: rec.ContractAddress,
				CallerAddress:   from.Hex(),
				Value:           valueArg.Uint64(),
				Nonce:           nonce,
				TxHash:          txHash,
				SentAt:          time.Now().UTC().Format(time.RFC3339),
			}}
			logger.Info("setvalue-done %d/%d contract=%s", atomic.LoadInt64(&finished), len(selected), rec.ContractAddress)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerFn()
		}()
	}

	go func() {
		for i, c := range selected {
			jobs <- setJob{Idx: i, Rec: c}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	ok := make([]setValueResult, 0, len(selected))
	errCount := 0
	for r := range results {
		if r.Err != nil {
			errCount++
			logger.Error("setvalue-error %v", r.Err)
			continue
		}
		ok = append(ok, r.Result)
		logger.Info("setvalue-ok contract=%s value=%d tx=%s", r.Result.ContractAddress, r.Result.Value, r.Result.TxHash)
	}

	if err := saveJSON(setResultFile, ok); err != nil {
		return err
	}

	logger.Info("========================================")
	logger.Info("SetValue finished. success=%d failed=%d", len(ok), errCount)
	logger.Info("Result file: %s", setResultFile)
	logger.Info("Unique contract guarantee: each selected contract used once only")
	logger.Info("========================================")

	if len(ok) == 0 {
		return fmt.Errorf("all setValue tx failed")
	}
	return nil
}
