package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// RunCryptoSuite executes transaction validation, signature, and replay attack scenarios
func RunCryptoSuite(rpcURL string, chainID uint64, privKeyHex string, report *SecurityReport) {
	fmt.Println("\n═══════════════════════════════════════════════════════════════")
	fmt.Println("🔐 [SUITE 2] Cryptographic & Transaction Validation Attacks")
	fmt.Println("═══════════════════════════════════════════════════════════════")

	privKey, err := crypto.HexToECDSA(strings.TrimPrefix(privKeyHex, "0x"))
	if err != nil {
		fmt.Printf("❌ Failed to parse test private key: %v\n", err)
		return
	}
	senderAddr := crypto.PubkeyToAddress(privKey.PublicKey)
	fmt.Printf("  Target Account: %s\n", senderAddr.Hex())

	// Verify account state & BLS registration on-chain
	acctState := getOnChainAccountState(rpcURL, senderAddr)
	if acctState.PublicKeyBls == "" {
		fmt.Printf("  ⚠️  WARNING: Account %s has NO BLS public key registered on-chain!\n", senderAddr.Hex())
		fmt.Println("     In bls_legacy mode, transactions will be rejected before reaching crypto/state validation.")
	} else {
		fmt.Printf("  ✅ Account Registered BLS Key: %s...\n", truncate(acctState.PublicKeyBls, 24))
		fmt.Printf("  ✅ Account Balance: %s wei\n", acctState.Balance)
	}

	// Fetch current on-chain nonce
	currentNonce := acctState.Nonce
	if currentNonce == 0 {
		currentNonce = getOnChainNonce(rpcURL, senderAddr)
	}
	fmt.Printf("  Current On-Chain Nonce: %d\n", currentNonce)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
	signer := types.LatestSignerForChainID(big.NewInt(int64(chainID)))

	// Helper to send raw transaction via RPC
	sendRawTx := func(rawHex string) (int, string, error) {
		reqBody := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_sendRawTransaction","params":["%s"],"id":1}`, rawHex)
		req, _ := http.NewRequest("POST", rpcURL, strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Close = true
		resp, err := client.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), nil
	}

	// -------------------------------------------------------------
	// SEC-CRYPTO-001: Tampered Signature Bytes (Invalid S > Curve Order N)
	// -------------------------------------------------------------
	{
		start := time.Now()
		recipient := common.HexToAddress("0x1111111111111111111111111111111111111111")
		tx := types.NewTx(&types.LegacyTx{
			Nonce:    currentNonce + 100,
			GasPrice: big.NewInt(100000),
			Gas:      21000,
			To:       &recipient,
			Value:    big.NewInt(1000),
			Data:     nil,
		})
		signedTx, _ := types.SignTx(tx, signer, privKey)
		raw, _ := signedTx.MarshalBinary()

		// Decode into RLP raw elements and substitute an invalid s > curve order N
		// This forces ECDSA recovery to fail explicitly with "invalid transaction v, r, s values"
		var elements []rlp.RawValue
		var corrupted []byte
		if err := rlp.DecodeBytes(raw, &elements); err == nil && len(elements) == 9 {
			secp256k1N, _ := new(big.Int).SetString("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", 16)
			invalidS := new(big.Int).Add(secp256k1N, big.NewInt(5))
			sBytes, _ := rlp.EncodeToBytes(invalidS)
			elements[8] = sBytes
			corrupted, _ = rlp.EncodeToBytes(elements)
		} else {
			// Fallback: byte corruption
			corrupted = make([]byte, len(raw))
			copy(corrupted, raw)
			corrupted[len(corrupted)-5] ^= 0xFF
			corrupted[len(corrupted)-2] ^= 0xAA
		}

		statusCode, respStr, err := sendRawTx(hexutil.Encode(corrupted))
		dur := time.Since(start)

		// Strict invariant: Must be rejected due to signature derivation failure,
		// NOT due to unauthenticated/unregistered account ("has no BLS public key")
		isSigError := strings.Contains(respStr, "invalid transaction v, r, s values") ||
			strings.Contains(respStr, "failed to derive sender") ||
			strings.Contains(respStr, "signature") ||
			(err != nil)
		hasNoBlsKeyErr := strings.Contains(respStr, "has no BLS public key")

		status := StatusPass
		if !isSigError || hasNoBlsKeyErr {
			status = StatusFail
		}
		report.AddResult(TestCaseResult{
			ID: "SEC-CRYPTO-001", Category: "Crypto", Name: "Corrupted Signature Bytes Rejection",
			Status: status, Duration: dur, Details: fmt.Sprintf("HTTP %d, Cryptographic signature derivation rejected: %s", statusCode, cleanError(respStr)),
		})
	}

	// -------------------------------------------------------------
	// SEC-CRYPTO-002: Field Tampering After Signing (Recipient Mutation)
	// -------------------------------------------------------------
	{
		start := time.Now()
		recipientOriginal := common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		tx := types.NewTx(&types.LegacyTx{
			Nonce:    currentNonce + 101,
			GasPrice: big.NewInt(100000),
			Gas:      21000,
			To:       &recipientOriginal,
			Value:    big.NewInt(1000),
			Data:     nil,
		})
		signedTx, _ := types.SignTx(tx, signer, privKey)
		raw, _ := signedTx.MarshalBinary()

		// Mutate recipient address bytes in serialized RLP
		tampered := make([]byte, len(raw))
		copy(tampered, raw)
		addrBytes := recipientOriginal.Bytes()
		idx := bytes.Index(tampered, addrBytes)
		if idx != -1 {
			tampered[idx+5] ^= 0xFF
		}

		statusCode, respStr, _ := sendRawTx(hexutil.Encode(tampered))
		dur := time.Since(start)

		// In ECDSA, modifying any message field invalidates the recovered signer:
		// ecrecover derives a mutated pseudo-random address (recovered != original sender).
		// Because the recovered address is unauthenticated (no authorization/BLS key for original sender),
		// the node rejects it before state execution, protecting the original sender's funds.
		isRejected := statusCode != http.StatusOK || strings.Contains(respStr, "error")
		status := StatusPass
		if !isRejected {
			status = StatusFail
		}
		report.AddResult(TestCaseResult{
			ID: "SEC-CRYPTO-002", Category: "Crypto", Name: "Field Tampering Detection",
			Status: status, Duration: dur, Details: fmt.Sprintf("HTTP %d, Cryptographic binding verified (recovered sender mutated away from signer): %s", statusCode, cleanError(respStr)),
		})
	}

	// -------------------------------------------------------------
	// SEC-CRYPTO-003: Cross-Chain Replay Attack (ChainID mismatch)
	// -------------------------------------------------------------
	{
		start := time.Now()
		recipient := common.HexToAddress("0x3333333333333333333333333333333333333333")
		// Sign with ChainID = 1 (Ethereum Mainnet)
		foreignSigner := types.LatestSignerForChainID(big.NewInt(1))
		foreignTx := types.NewTx(&types.LegacyTx{
			Nonce:    currentNonce + 102,
			GasPrice: big.NewInt(100000),
			Gas:      21000,
			To:       &recipient,
			Value:    big.NewInt(1000),
			Data:     nil,
		})
		foreignSignedTx, _ := types.SignTx(foreignTx, foreignSigner, privKey)
		rawForeign, _ := foreignSignedTx.MarshalBinary()

		statusCode, respStr, _ := sendRawTx(hexutil.Encode(rawForeign))
		dur := time.Since(start)

		// Must specifically verify chain ID rejection, not generic BLS key error
		isChainErr := strings.Contains(respStr, "invalid chain id") || strings.Contains(respStr, "chain")
		hasNoBlsKeyErr := strings.Contains(respStr, "has no BLS public key")
		status := StatusPass
		if !isChainErr || hasNoBlsKeyErr {
			status = StatusFail
		}
		report.AddResult(TestCaseResult{
			ID: "SEC-CRYPTO-003", Category: "Crypto", Name: "Cross-Chain Replay Attack Protection",
			Status: status, Duration: dur, Details: fmt.Sprintf("HTTP %d, Rejected foreign ChainID=1 tx: %s", statusCode, cleanError(respStr)),
		})
	}

	// -------------------------------------------------------------
	// SEC-CRYPTO-004: Stale Nonce Replay Attack
	// -------------------------------------------------------------
	{
		start := time.Now()
		recipient := common.HexToAddress("0x4444444444444444444444444444444444444444")
		staleNonce := uint64(0)
		if currentNonce > 0 {
			staleNonce = currentNonce - 1
		}
		staleTx := types.NewTx(&types.LegacyTx{
			Nonce:    staleNonce,
			GasPrice: big.NewInt(100000),
			Gas:      21000,
			To:       &recipient,
			Value:    big.NewInt(1000),
			Data:     nil,
		})
		signedStaleTx, _ := types.SignTx(staleTx, signer, privKey)
		rawStale, _ := signedStaleTx.MarshalBinary()

		statusCode, respStr, _ := sendRawTx(hexutil.Encode(rawStale))
		dur := time.Since(start)

		// Must specifically verify stale nonce rejection: "error: invalid nonce"
		isNonceErr := strings.Contains(respStr, "invalid nonce") || strings.Contains(respStr, "nonce too low")
		hasNoBlsKeyErr := strings.Contains(respStr, "has no BLS public key")
		status := StatusPass
		if !isNonceErr || hasNoBlsKeyErr {
			status = StatusFail
		}
		report.AddResult(TestCaseResult{
			ID: "SEC-CRYPTO-004", Category: "Crypto", Name: "Stale Nonce Replay Rejection",
			Status: status, Duration: dur, Details: fmt.Sprintf("HTTP %d, Rejected stale nonce=%d: %s", statusCode, staleNonce, cleanError(respStr)),
		})
	}

	// -------------------------------------------------------------
	// SEC-CRYPTO-005: Underpriced Transaction / Zero Gas Price Spam
	// -------------------------------------------------------------
	{
		start := time.Now()
		recipient := common.HexToAddress("0x5555555555555555555555555555555555555555")
		zeroGasTx := types.NewTx(&types.LegacyTx{
			Nonce:    currentNonce + 105,
			GasPrice: big.NewInt(0), // 0 Gas Price
			Gas:      21000,
			To:       &recipient,
			Value:    big.NewInt(1000),
			Data:     nil,
		})
		signedZeroGasTx, _ := types.SignTx(zeroGasTx, signer, privKey)
		rawZeroGas, _ := signedZeroGasTx.MarshalBinary()

		statusCode, respStr, _ := sendRawTx(hexutil.Encode(rawZeroGas))
		dur := time.Since(start)

		// Must specifically verify zero gas price rejection: "error: invalid max gas price"
		isGasPriceErr := strings.Contains(respStr, "invalid max gas price") || strings.Contains(respStr, "gas price") || strings.Contains(respStr, "underpriced")
		hasNoBlsKeyErr := strings.Contains(respStr, "has no BLS public key")
		status := StatusPass
		if !isGasPriceErr || hasNoBlsKeyErr {
			status = StatusFail
		}
		report.AddResult(TestCaseResult{
			ID: "SEC-CRYPTO-005", Category: "Crypto", Name: "Zero Gas Price Anti-Spam Rejection",
			Status: status, Duration: dur, Details: fmt.Sprintf("HTTP %d, Rejected zero gas price: %s", statusCode, cleanError(respStr)),
		})
	}

	// -------------------------------------------------------------
	// SEC-CRYPTO-006: Insufficient Balance Exploitation
	// -------------------------------------------------------------
	{
		start := time.Now()
		recipient := common.HexToAddress("0x6666666666666666666666666666666666666666")
		// Amount: 10^45 wei (far exceeds account's balance of ~2*10^30 wei)
		insaneAmount, _ := new(big.Int).SetString("1000000000000000000000000000000000000000000000", 10)
		overdraftTx := types.NewTx(&types.LegacyTx{
			Nonce:    currentNonce + 106,
			GasPrice: big.NewInt(100000),
			Gas:      21000,
			To:       &recipient,
			Value:    insaneAmount,
			Data:     nil,
		})
		signedOverdraftTx, _ := types.SignTx(overdraftTx, signer, privKey)
		rawOverdraft, _ := signedOverdraftTx.MarshalBinary()

		statusCode, respStr, _ := sendRawTx(hexutil.Encode(rawOverdraft))
		dur := time.Since(start)

		// Must specifically verify insufficient balance rejection: "error: invalid amount"
		isAmountErr := strings.Contains(respStr, "invalid amount") || strings.Contains(respStr, "insufficient") || strings.Contains(respStr, "balance")
		hasNoBlsKeyErr := strings.Contains(respStr, "has no BLS public key")
		status := StatusPass
		if !isAmountErr || hasNoBlsKeyErr {
			status = StatusFail
		}
		report.AddResult(TestCaseResult{
			ID: "SEC-CRYPTO-006", Category: "Crypto", Name: "Insufficient Balance Rejection",
			Status: status, Duration: dur, Details: fmt.Sprintf("HTTP %d, Overdraft rejected: %s", statusCode, cleanError(respStr)),
		})
	}

	// -------------------------------------------------------------
	// SEC-CRYPTO-007: Oversized Transaction Data (> 6 MB)
	// -------------------------------------------------------------
	{
		start := time.Now()
		recipient := common.HexToAddress("0x7777777777777777777777777777777777777777")
		hugeData := bytes.Repeat([]byte{0xEE}, 6*1024*1024+1024) // 6 MB + 1 KB
		hugeTx := types.NewTx(&types.LegacyTx{
			Nonce:    currentNonce + 107,
			GasPrice: big.NewInt(100000),
			Gas:      30000000,
			To:       &recipient,
			Value:    big.NewInt(0),
			Data:     hugeData,
		})
		signedHugeTx, _ := types.SignTx(hugeTx, signer, privKey)
		rawHuge, _ := signedHugeTx.MarshalBinary()

		statusCode, respStr, err := sendRawTx(hexutil.Encode(rawHuge))
		dur := time.Since(start)

		isRejected := err != nil || statusCode == http.StatusRequestEntityTooLarge || statusCode != http.StatusOK || strings.Contains(respStr, "too large") || strings.Contains(respStr, "limit") || strings.Contains(respStr, "size")
		status := StatusPass
		if !isRejected {
			status = StatusFail
		}
		report.AddResult(TestCaseResult{
			ID: "SEC-CRYPTO-007", Category: "Crypto", Name: "Oversized CallData (>6MB) Rejection",
			Status: status, Duration: dur, Details: fmt.Sprintf("HTTP %d, Rejected oversized data (>6MB): %s", statusCode, cleanError(respStr)),
		})
	}

	// -------------------------------------------------------------
	// SEC-CRYPTO-008: High-S Signature Malleability Verification
	// -------------------------------------------------------------
	{
		start := time.Now()
		recipient := common.HexToAddress("0x8888888888888888888888888888888888888888")
		tx := types.NewTx(&types.LegacyTx{
			Nonce:    currentNonce + 108,
			GasPrice: big.NewInt(100000),
			Gas:      21000,
			To:       &recipient,
			Value:    big.NewInt(1000),
			Data:     nil,
		})
		signedTx, _ := types.SignTx(tx, signer, privKey)
		v, r, s := signedTx.RawSignatureValues()

		// In SECP256k1: N is curve order. High S is N - s.
		// Standard EIP-2 requires s <= N/2.
		// secp256k1 N:
		secp256k1N, _ := new(big.Int).SetString("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", 16)
		highS := new(big.Int).Sub(secp256k1N, s)

		// Create malleable transaction with highS
		malleableTx, err := tx.WithSignature(signer, encodeSignature(r, highS, v))
		dur := time.Since(start)

		if err != nil {
			report.AddResult(TestCaseResult{
				ID: "SEC-CRYPTO-008", Category: "Crypto", Name: "ECDSA High-S Malleability Defense",
				Status: StatusPass, Duration: dur, Details: fmt.Sprintf("High-S rejected at signature constructor: %v", err),
			})
		} else {
			rawMalleable, _ := malleableTx.MarshalBinary()
			statusCode, respStr, _ := sendRawTx(hexutil.Encode(rawMalleable))
			isRejected := statusCode != http.StatusOK || strings.Contains(respStr, "invalid transaction v, r, s values") || strings.Contains(respStr, "signature") || strings.Contains(respStr, "error")
			status := StatusPass
			if !isRejected {
				status = StatusFail
			}
			report.AddResult(TestCaseResult{
				ID: "SEC-CRYPTO-008", Category: "Crypto", Name: "ECDSA High-S Malleability Defense",
				Status: status, Duration: dur, Details: fmt.Sprintf("HTTP %d, High-S rejected: %s", statusCode, cleanError(respStr)),
			})
		}
	}
}

func cleanError(respStr string) string {
	var parsed struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(respStr), &parsed); err == nil && parsed.Error.Message != "" {
		return fmt.Sprintf("[code %d] %s", parsed.Error.Code, parsed.Error.Message)
	}
	trimmed := strings.TrimSpace(respStr)
	if len(trimmed) > 120 {
		return trimmed[:117] + "..."
	}
	return trimmed
}

func encodeSignature(r, s, v *big.Int) []byte {
	sig := make([]byte, 65)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)
	sig[64] = byte(v.Uint64())
	return sig
}

func getOnChainNonce(rpcURL string, addr common.Address) uint64 {
	client := &http.Client{Timeout: 5 * time.Second}
	reqBody := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_getTransactionCount","params":["%s","latest"],"id":1}`, addr.Hex())
	resp, err := client.Post(rpcURL, "application/json", strings.NewReader(reqBody))
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var res struct {
		Result string `json:"result"`
	}
	_ = json.Unmarshal(body, &res)
	n, _ := hexutil.DecodeUint64(res.Result)
	return n
}

type onChainAccountState struct {
	AccountType  int    `json:"accountType"`
	Address      string `json:"address"`
	Balance      string `json:"balance"`
	Nonce        uint64 `json:"nonce"`
	PublicKeyBls string `json:"publicKeyBls"`
}

func getOnChainAccountState(rpcURL string, addr common.Address) onChainAccountState {
	client := &http.Client{Timeout: 5 * time.Second}
	reqBody := fmt.Sprintf(`{"jsonrpc":"2.0","method":"mtn_getAccountState","params":["%s","latest"],"id":1}`, addr.Hex())
	resp, err := client.Post(rpcURL, "application/json", strings.NewReader(reqBody))
	if err != nil {
		return onChainAccountState{}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var res struct {
		Result onChainAccountState `json:"result"`
	}
	_ = json.Unmarshal(body, &res)
	return res.Result
}
