package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/helppay"
)

// HelpPayAPI exposes JSON-RPC methods for managing the sponsor wallet pool.
type HelpPayAPI struct {
	App            *App
	usedSignatures sync.Map // map[string]int64: sigHash -> timestamp (anti-replay cache within ±120s / 2 minutes)
}

// NewHelpPayAPI creates a new HelpPayAPI instance.
func NewHelpPayAPI(app *App) *HelpPayAPI {
	return &HelpPayAPI{App: app}
}

// verifyAdminAuth verifies admin signature, ensures admin addresses are configured (fail-closed),
// and provides anti-replay protection.
func (api *HelpPayAPI) verifyAdminAuth(adminAddress string, signature string, timestamp int64, expectedAction string) (common.Address, error) {
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

	now := time.Now().Unix()
	diff := now - timestamp
	if diff < -120 || diff > 120 {
		return common.Address{}, fmt.Errorf("timestamp out of range: server time is %d, diff is %ds (max allowed 120s / 2 minutes)", now, diff)
	}

	// Anti-replay protection: each signature can only be consumed once
	sigHash := crypto.Keccak256Hash([]byte(signature)).Hex()
	if _, loaded := api.usedSignatures.LoadOrStore(sigHash, timestamp); loaded {
		return common.Address{}, fmt.Errorf("signature already consumed (replay attack detected)")
	}

	// Opportunistic pruning of signatures older than 4 minutes (240s)
	if now%10 == 0 {
		api.usedSignatures.Range(func(key, val interface{}) bool {
			if ts, ok := val.(int64); ok && now-ts > 240 {
				api.usedSignatures.Delete(key)
			}
			return true
		})
	}

	if err := api.App.helpPayManager.VerifyAdminSignature(expectedAction, signature, adminAddr); err != nil {
		return common.Address{}, fmt.Errorf("admin authorization failed: %w", err)
	}
	return adminAddr, nil
}

// AddWallet adds a sponsor wallet by private key (hex string) with admin ECDSA signature verification.
// Message format for signing: "helppay:add_wallet:<targetAddress>:<timestamp>"
func (api *HelpPayAPI) AddWallet(ctx context.Context, adminAddress string, signature string, timestamp int64, privateKeyHex string) (map[string]interface{}, error) {
	cleanKey := strings.TrimSpace(privateKeyHex)
	cleanKey = strings.TrimPrefix(cleanKey, "0x")
	cleanKey = strings.TrimPrefix(cleanKey, "0X")

	privKey, err := crypto.HexToECDSA(cleanKey)
	if err != nil {
		return nil, fmt.Errorf("invalid ECDSA private key: %w", err)
	}
	targetAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	expectedMsg := fmt.Sprintf("helppay:add_wallet:%s:%d", targetAddr.Hex(), timestamp)
	adminAddr, err := api.verifyAdminAuth(adminAddress, signature, timestamp, expectedMsg)
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
// Message format for signing: "helppay:remove_wallet:<addressHex>:<timestamp>"
func (api *HelpPayAPI) RemoveWallet(ctx context.Context, adminAddress string, signature string, timestamp int64, addressHex string) (map[string]interface{}, error) {
	targetAddr := common.HexToAddress(addressHex)
	expectedMsg := fmt.Sprintf("helppay:remove_wallet:%s:%d", targetAddr.Hex(), timestamp)
	adminAddr, err := api.verifyAdminAuth(adminAddress, signature, timestamp, expectedMsg)
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
// Message format for signing: "helppay:get_wallets:<timestamp>"
func (api *HelpPayAPI) GetWallets(ctx context.Context, adminAddress string, signature string, timestamp int64) ([]helppay.WalletStatus, error) {
	expectedMsg := fmt.Sprintf("helppay:get_wallets:%d", timestamp)
	if _, err := api.verifyAdminAuth(adminAddress, signature, timestamp, expectedMsg); err != nil {
		return nil, err
	}
	return api.App.helpPayManager.GetWallets(), nil
}

// Toggle enables or disables help-pay with authorized admin signature.
// Message format for signing: "helppay:toggle:<enabled>:<timestamp>"
func (api *HelpPayAPI) Toggle(ctx context.Context, adminAddress string, signature string, timestamp int64, enabled bool) (map[string]interface{}, error) {
	expectedMsg := fmt.Sprintf("helppay:toggle:%t:%d", enabled, timestamp)
	adminAddr, err := api.verifyAdminAuth(adminAddress, signature, timestamp, expectedMsg)
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
// Message format for signing: "helppay:set_config:<enabled>:<minBalance>:<extraAmount>:<timestamp>"
func (api *HelpPayAPI) SetConfigWithAdmin(ctx context.Context, adminAddress string, signature string, timestamp int64, enabled bool, minBalance string, extraAmount string) (map[string]interface{}, error) {
	expectedMsg := fmt.Sprintf("helppay:set_config:%t:%s:%s:%d", enabled, minBalance, extraAmount, timestamp)
	adminAddr, err := api.verifyAdminAuth(adminAddress, signature, timestamp, expectedMsg)
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
