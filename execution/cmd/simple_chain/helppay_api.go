package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/helppay"
)

// HelpPayAPI exposes JSON-RPC methods for managing the sponsor wallet pool.
type HelpPayAPI struct {
	App *App
}

// NewHelpPayAPI creates a new HelpPayAPI instance.
func NewHelpPayAPI(app *App) *HelpPayAPI {
	return &HelpPayAPI{App: app}
}

// AddWallet adds a sponsor wallet by private key (hex string) and synchronizes across the Raft cluster.
func (api *HelpPayAPI) AddWallet(ctx context.Context, privateKeyHex string) (map[string]interface{}, error) {
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
	}
	cleanKey := strings.TrimSpace(privateKeyHex)
	addr, err := api.App.helpPayManager.AddWallet(cleanKey, true)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"address": addr.Hex(),
		"status":  "added",
		"synced":  true,
	}, nil
}

// RemoveWallet removes a sponsor wallet by address and synchronizes across the Raft cluster.
func (api *HelpPayAPI) RemoveWallet(ctx context.Context, addressHex string) (map[string]interface{}, error) {
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
	}
	addr := common.HexToAddress(addressHex)
	if err := api.App.helpPayManager.RemoveWallet(addr, true); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"address": addr.Hex(),
		"status":  "removed",
		"synced":  true,
	}, nil
}

// GetWallets returns all sponsor wallets and their current balances and statuses.
func (api *HelpPayAPI) GetWallets(ctx context.Context) ([]helppay.WalletStatus, error) {
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
	}
	return api.App.helpPayManager.GetWallets(), nil
}

// Toggle enables or disables help-pay. If admin addresses are configured in the node's
// config, it requires a valid signature from an authorized admin.
// Message format for signing: "helppay:toggle:<enabled>:<timestamp>"
func (api *HelpPayAPI) Toggle(ctx context.Context, adminAddress string, signature string, timestamp int64, enabled bool) (map[string]interface{}, error) {
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
	}

	adminAddr := common.Address{}
	if api.App.helpPayManager.HasAdminsConfigured() {
		cleanAddr := strings.TrimSpace(adminAddress)
		if !common.IsHexAddress(cleanAddr) {
			return nil, fmt.Errorf("invalid admin address: %s", adminAddress)
		}
		adminAddr = common.HexToAddress(cleanAddr)

		now := time.Now().Unix()
		diff := now - timestamp
		if diff < -300 || diff > 300 {
			return nil, fmt.Errorf("timestamp out of range: server time is %d, diff is %ds (max allowed 300s)", now, diff)
		}

		expectedMsg := fmt.Sprintf("helppay:toggle:%t:%d", enabled, timestamp)
		if err := api.App.helpPayManager.VerifyAdminSignature(expectedMsg, signature, adminAddr); err != nil {
			return nil, fmt.Errorf("admin authorization failed: %w", err)
		}
	} else if strings.TrimSpace(adminAddress) != "" && common.IsHexAddress(adminAddress) {
		adminAddr = common.HexToAddress(adminAddress)
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

// SetEnabled is an alias for Toggle to provide intuitive RPC naming.
func (api *HelpPayAPI) SetEnabled(ctx context.Context, adminAddress string, signature string, timestamp int64, enabled bool) (map[string]interface{}, error) {
	return api.Toggle(ctx, adminAddress, signature, timestamp, enabled)
}

// GetAdmins returns all authorized admin addresses configured for this node.
func (api *HelpPayAPI) GetAdmins(ctx context.Context) ([]string, error) {
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
	}
	return api.App.helpPayManager.GetAdmins(), nil
}

// SetConfig updates help-pay configuration and synchronizes across the Raft cluster.
// If admin addresses are configured on the node, use SetConfigWithAdmin or admin_setHelpPayConfig.
func (api *HelpPayAPI) SetConfig(ctx context.Context, enabled bool, minBalance string, extraAmount string) (map[string]interface{}, error) {
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
	}
	if api.App.helpPayManager.HasAdminsConfigured() {
		return nil, fmt.Errorf("admin addresses are configured on this node; please use helppay_toggle or admin_setHelpPayConfig")
	}
	if err := api.App.helpPayManager.SetConfig(enabled, minBalance, extraAmount, true); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"status":       "updated",
		"enabled":      enabled,
		"min_balance":  minBalance,
		"extra_amount": extraAmount,
		"synced":       true,
	}, nil
}

// SetConfigWithAdmin updates help-pay configuration with admin ECDSA signature verification.
// Message format for signing: "helppay:set_config:<enabled>:<minBalance>:<extraAmount>:<timestamp>"
func (api *HelpPayAPI) SetConfigWithAdmin(ctx context.Context, adminAddress string, signature string, timestamp int64, enabled bool, minBalance string, extraAmount string) (map[string]interface{}, error) {
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
	}

	cleanAddr := strings.TrimSpace(adminAddress)
	if !common.IsHexAddress(cleanAddr) {
		return nil, fmt.Errorf("invalid admin address: %s", adminAddress)
	}
	adminAddr := common.HexToAddress(cleanAddr)

	if api.App.helpPayManager.HasAdminsConfigured() {
		now := time.Now().Unix()
		diff := now - timestamp
		if diff < -300 || diff > 300 {
			return nil, fmt.Errorf("timestamp out of range: server time is %d, diff is %ds (max allowed 300s)", now, diff)
		}

		expectedMsg := fmt.Sprintf("helppay:set_config:%t:%s:%s:%d", enabled, minBalance, extraAmount, timestamp)
		if err := api.App.helpPayManager.VerifyAdminSignature(expectedMsg, signature, adminAddr); err != nil {
			return nil, fmt.Errorf("admin authorization failed: %w", err)
		}
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

// GetConfig returns the current help-pay configuration.
func (api *HelpPayAPI) GetConfig(ctx context.Context) (config.HelpPayConfig, error) {
	if api.App == nil || api.App.helpPayManager == nil {
		return config.HelpPayConfig{}, fmt.Errorf("helppay manager not initialized")
	}
	return api.App.helpPayManager.GetConfig(), nil
}
