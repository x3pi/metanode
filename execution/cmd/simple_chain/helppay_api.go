package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/helppay"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

type failedAuthRecord struct {
	mu           sync.Mutex
	count        int
	firstFailed  int64
	blockedUntil int64
}

// HelpPayAPI exposes JSON-RPC methods for managing the sponsor wallet pool.
type HelpPayAPI struct {
	App            *App
	usedSignatures sync.Map // map[string]int64: sigHash -> timestamp (anti-replay cache within ±120s / 2 minutes)
	failedAuths    sync.Map // map[string]*failedAuthRecord: clientIP -> tracking failed attempts
}

// NewHelpPayAPI creates a new HelpPayAPI instance.
func NewHelpPayAPI(app *App) *HelpPayAPI {
	return &HelpPayAPI{App: app}
}

func (api *HelpPayAPI) recordFailedAuth(clientIP string) {
	if clientIP == "" {
		return
	}
	now := time.Now().Unix()
	val, _ := api.failedAuths.LoadOrStore(clientIP, &failedAuthRecord{firstFailed: now})
	rec := val.(*failedAuthRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()

	if now-rec.firstFailed > 60 {
		rec.count = 1
		rec.firstFailed = now
	} else {
		rec.count++
		if rec.count >= 5 {
			rec.blockedUntil = now + 300 // block for 5 minutes (300s)
			logger.Warn("⚠️ [HelpPay API] IP %s blocked for 5 minutes due to %d failed admin auth attempts", clientIP, rec.count)
			logger.Telegram(fmt.Sprintf("⚠️ [HelpPay Security] IP %s bị tạm khóa 5 phút do thử sai chữ ký Admin %d lần!", clientIP, rec.count))
		}
	}
}

// verifyAdminAuth verifies admin signature, ensures admin addresses are configured (fail-closed),
// rate-limits repeated failed attempts per IP, and provides anti-replay protection.
func (api *HelpPayAPI) verifyAdminAuth(clientIP string, adminAddress string, signature string, timestamp int64, expectedAction string) (common.Address, error) {
	now := time.Now().Unix()
	if clientIP != "" {
		if val, ok := api.failedAuths.Load(clientIP); ok {
			rec := val.(*failedAuthRecord)
			rec.mu.Lock()
			blocked := rec.blockedUntil > now
			remaining := rec.blockedUntil - now
			rec.mu.Unlock()
			if blocked {
				return common.Address{}, fmt.Errorf("ip %s is temporarily blocked due to repeated failed admin authentication, try again in %ds", clientIP, remaining)
			}
		}
	}

	if api.App == nil || api.App.helpPayManager == nil {
		return common.Address{}, fmt.Errorf("helppay manager not initialized")
	}
	if !api.App.helpPayManager.HasAdminsConfigured() {
		return common.Address{}, fmt.Errorf("no admin addresses configured on this node; operation refused (fail-closed)")
	}
	cleanAddr := strings.TrimSpace(adminAddress)
	if !common.IsHexAddress(cleanAddr) {
		return common.Address{}, fmt.Errorf("invalid admin address: %s", adminAddress)
	}
	adminAddr := common.HexToAddress(cleanAddr)

	diff := now - timestamp
	if diff < -120 || diff > 120 {
		return common.Address{}, fmt.Errorf("timestamp out of range: server time is %d, diff is %ds (max allowed 120s / 2 minutes)", now, diff)
	}

	// 1. SECURITY FIX: Verify admin signature BEFORE touching anti-replay cache!
	// Prevents unauthenticated memory bloat / cache stuffing attacks.
	// Strictly enforces chain-bound message format ("helppay:<chainID>:<action>...") with NO fallback.
	if err := api.App.helpPayManager.VerifyAdminSignature(expectedAction, signature, adminAddr); err != nil {
		api.recordFailedAuth(clientIP)
		return common.Address{}, fmt.Errorf("admin authorization failed: %w", err)
	}

	// 2. Anti-replay protection: valid signatures can only be consumed once
	sigHash := crypto.Keccak256Hash([]byte(signature)).Hex()
	if _, loaded := api.usedSignatures.LoadOrStore(sigHash, timestamp); loaded {
		return common.Address{}, fmt.Errorf("signature already consumed (replay attack detected)")
	}

	// 3. Opportunistic pruning of consumed signatures (>4m) and expired failed auth records (>10m)
	if now%10 == 0 {
		api.usedSignatures.Range(func(key, val interface{}) bool {
			if ts, ok := val.(int64); ok && now-ts > 240 {
				api.usedSignatures.Delete(key)
			}
			return true
		})
		api.failedAuths.Range(func(key, val interface{}) bool {
			if rec, ok := val.(*failedAuthRecord); ok {
				rec.mu.Lock()
				expired := now-rec.firstFailed > 600 && now > rec.blockedUntil
				rec.mu.Unlock()
				if expired {
					api.failedAuths.Delete(key)
				}
			}
			return true
		})
	}

	return adminAddr, nil
}

func (api *HelpPayAPI) chainIDStr() string {
	if api.App != nil && api.App.config != nil && api.App.config.ChainId != nil {
		return api.App.config.ChainId.String()
	}
	return "0"
}

func getClientIPFromContext(ctx context.Context) string {
	if pi := rpc.PeerInfoFromContext(ctx); pi.RemoteAddr != "" {
		return extractIP(pi.RemoteAddr)
	}
	return ""
}

// AddWallet adds a sponsor wallet by private key (hex string) with admin ECDSA signature verification.
// Message format for signing: "helppay:<chainID>:add_wallet:<targetAddress>:<timestamp>"
func (api *HelpPayAPI) AddWallet(ctx context.Context, adminAddress string, signature string, timestamp int64, privateKeyHex string) (map[string]interface{}, error) {
	cleanKey := strings.TrimSpace(privateKeyHex)
	cleanKey = strings.TrimPrefix(cleanKey, "0x")
	cleanKey = strings.TrimPrefix(cleanKey, "0X")

	privKey, err := crypto.HexToECDSA(cleanKey)
	if err != nil {
		return nil, fmt.Errorf("invalid ECDSA private key: %w", err)
	}
	targetAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	expectedMsg := fmt.Sprintf("helppay:%s:add_wallet:%s:%d", api.chainIDStr(), targetAddr.Hex(), timestamp)
	adminAddr, err := api.verifyAdminAuth(getClientIPFromContext(ctx), adminAddress, signature, timestamp, expectedMsg)
	if err != nil {
		return nil, err
	}
	addr, err := api.App.helpPayManager.AddWallet(cleanKey, false)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"address": addr.Hex(),
		"status":  "added",
		"admin":   adminAddr.Hex(),
		"local":   true,
	}, nil
}

// RemoveWallet removes a sponsor wallet by address with admin ECDSA signature verification.
// Message format for signing: "helppay:<chainID>:remove_wallet:<addressHex>:<timestamp>"
func (api *HelpPayAPI) RemoveWallet(ctx context.Context, adminAddress string, signature string, timestamp int64, addressHex string) (map[string]interface{}, error) {
	targetAddr := common.HexToAddress(addressHex)
	expectedMsg := fmt.Sprintf("helppay:%s:remove_wallet:%s:%d", api.chainIDStr(), targetAddr.Hex(), timestamp)
	adminAddr, err := api.verifyAdminAuth(getClientIPFromContext(ctx), adminAddress, signature, timestamp, expectedMsg)
	if err != nil {
		return nil, err
	}
	if err := api.App.helpPayManager.RemoveWallet(targetAddr, false); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"address": targetAddr.Hex(),
		"status":  "removed",
		"admin":   adminAddr.Hex(),
		"local":   true,
	}, nil
}

// GetWallets returns all sponsor wallets and their current balances and statuses with admin ECDSA signature verification.
// Message format for signing: "helppay:<chainID>:get_wallets:<timestamp>"
func (api *HelpPayAPI) GetWallets(ctx context.Context, adminAddress string, signature string, timestamp int64) ([]helppay.WalletStatus, error) {
	expectedMsg := fmt.Sprintf("helppay:%s:get_wallets:%d", api.chainIDStr(), timestamp)
	if _, err := api.verifyAdminAuth(getClientIPFromContext(ctx), adminAddress, signature, timestamp, expectedMsg); err != nil {
		return nil, err
	}
	return api.App.helpPayManager.GetWallets(), nil
}

// Toggle enables or disables help-pay with authorized admin signature.
// Message format for signing: "helppay:<chainID>:toggle:<enabled>:<timestamp>"
func (api *HelpPayAPI) Toggle(ctx context.Context, adminAddress string, signature string, timestamp int64, enabled bool) (map[string]interface{}, error) {
	expectedMsg := fmt.Sprintf("helppay:%s:toggle:%t:%d", api.chainIDStr(), enabled, timestamp)
	adminAddr, err := api.verifyAdminAuth(getClientIPFromContext(ctx), adminAddress, signature, timestamp, expectedMsg)
	if err != nil {
		return nil, err
	}

	if err := api.App.helpPayManager.SetEnabled(enabled, true); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status":  "success",
		"enabled": enabled,
		"admin":   adminAddr.Hex(),
		"synced":  true,
	}, nil
}

// GetAdmins returns all authorized admin addresses configured for this node.
func (api *HelpPayAPI) GetAdmins(ctx context.Context) ([]string, error) {
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
	}
	return api.App.helpPayManager.GetAdmins(), nil
}

// SetConfig updates help-pay configuration with admin ECDSA signature verification.
func (api *HelpPayAPI) SetConfig(ctx context.Context, adminAddress string, signature string, timestamp int64, enabled bool, minBalance string, extraAmount string) (map[string]interface{}, error) {
	return api.SetConfigWithAdmin(ctx, adminAddress, signature, timestamp, enabled, minBalance, extraAmount)
}

// SetConfigWithAdmin updates help-pay configuration with admin ECDSA signature verification.
// Message format for signing: "helppay:<chainID>:set_config:<enabled>:<minBalance>:<extraAmount>:<timestamp>"
func (api *HelpPayAPI) SetConfigWithAdmin(ctx context.Context, adminAddress string, signature string, timestamp int64, enabled bool, minBalance string, extraAmount string) (map[string]interface{}, error) {
	expectedMsg := fmt.Sprintf("helppay:%s:set_config:%t:%s:%s:%d", api.chainIDStr(), enabled, minBalance, extraAmount, timestamp)
	adminAddr, err := api.verifyAdminAuth(getClientIPFromContext(ctx), adminAddress, signature, timestamp, expectedMsg)
	if err != nil {
		return nil, err
	}

	if err := api.App.helpPayManager.SetConfig(enabled, minBalance, extraAmount, true); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"status":       "updated",
		"enabled":      enabled,
		"min_balance":  minBalance,
		"extra_amount": extraAmount,
		"admin":        adminAddr.Hex(),
		"synced":       true,
	}, nil
}

// GetConfig returns the current help-pay configuration (public metadata).
func (api *HelpPayAPI) GetConfig(ctx context.Context) (config.HelpPayConfig, error) {
	if api.App == nil || api.App.helpPayManager == nil {
		return config.HelpPayConfig{}, fmt.Errorf("helppay manager not initialized")
	}
	return api.App.helpPayManager.GetConfig(), nil
}
