package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor"
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	mt_filters "github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/helppay"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
	"github.com/meta-node-blockchain/meta-node/pkg/snapshot"
)

type AdminApi struct {
	App    *App // Export field Client
	events *mt_filters.EventSystem
	meta   *MetaAPI // used by admin_verifyHistoricalRoot
}

// authenticate checks if admin password is configured and matches constant-time.
func (api *AdminApi) authenticate(ctx context.Context, password string) error {
	// Admin operations are for operators (curl/CLI). A browser always sends an Origin header, so any
	// admin call carrying one is a cross-site request (CSWSH/CSRF through the public "*"-origin /ws
	// and CORS RPC endpoint) and is refused even if the password is known to the page.
	if ctx != nil {
		if pi := rpc.PeerInfoFromContext(ctx); pi.HTTP.Origin != "" {
			return errAdminFromBrowserOrigin
		}
	}
	if api == nil || api.App == nil || strings.TrimSpace(api.App.config.Securepassword) == "" {
		return errPasswordNotConfigured
	}
	configuredPw := strings.TrimSpace(api.App.config.Securepassword)
	if configuredPw == "mysecretpassword" || configuredPw == "securepassword" || configuredPw == "devnet-test-password" {
		return errPasswordNotConfigured
	}
	if subtle.ConstantTimeCompare([]byte(password), []byte(api.App.config.Securepassword)) != 1 {
		return errInvalidCredentials
	}
	return nil
}

// LoginAPI is a simple API for user login using only a password.
func (api *AdminApi) LoginAPI(ctx context.Context, password string) (string, error) {
	if err := api.authenticate(ctx, password); err != nil {
		return "", err
	}
	return "Login successful", nil
}

// AttestPayloadLoss is the operator-facing RPC wrapper (admin_attestPayloadLoss) for the new
// Quorum-Certified Payload-Loss Attestation mechanism (2026-09-11) -- see the doc comment on
// consensus/metanode/src/ffi.rs's metanode_attest_payload_loss and mục 11 of
// note/consensus_local_dag_trust_gap_design_2026-09.md. NOT called automatically from anywhere
// -- an operator invokes this manually only after CONSENSUS-HALT-TX-PAYLOAD-LOST
// (block_delivery.rs mục 10) has been showing for this exact commit for a genuinely long time.
// txDigestHex: exactly 2*DIGEST_LENGTH hex characters, no "0x" prefix (see the halt log line
// itself for the exact digest -- e.g. "digest 6UToZqfJJ1D3c/GMz65Ivd..." there is base64, this
// wants hex; convert before calling).
// Returns the raw status from the Rust side: 0 = quorum-certified skip applied, 1 = recovered
// (no skip needed), 2 = insufficient stake attested so far, -1 = could not run. Full detail is
// always in the node's own logs (grep for PAYLOAD-LOSS-SKIP), never only in this return value.
func (api *AdminApi) AttestPayloadLoss(ctx context.Context, password string, commitIndex uint32, txDigestHex string) (int32, error) {
	if err := api.authenticate(ctx, password); err != nil {
		return -1, err
	}
	if raftfeed.Enabled() {
		return -1, fmt.Errorf("unsupported in raft mode")
	}
	return executor.AttestPayloadLoss(commitIndex, txDigestHex), nil
}

// AttestPayloadLossForCommit is the operator-facing RPC wrapper (admin_attestPayloadLossForCommit)
// for the whole-commit convenience form of AttestPayloadLoss above (see
// consensus/metanode/src/ffi.rs's metanode_attest_payload_loss_for_commit, 2026-09-11) -- attests
// every digest this node is currently stuck on for commitIndex in one call, instead of requiring
// one AttestPayloadLoss call per digest (a single halted commit can have several missing digests
// at once -- reproduced live with 8 on one commit). Prefer this over AttestPayloadLoss whenever
// you don't already know there's exactly one missing digest.
// Returns: 0 = every claim resolved, 1 = nothing was stuck for this commit on this node right
// now, 2 = at least one claim still needs more attested stake (others were still resolved), -1 =
// could not run, or at least one claim hit a hard error. Full detail is always in the node's own
// logs (grep for PAYLOAD-LOSS-SKIP), never only in this return value.
func (api *AdminApi) AttestPayloadLossForCommit(ctx context.Context, password string, commitIndex uint32) (int32, error) {
	if err := api.authenticate(ctx, password); err != nil {
		return -1, err
	}
	if raftfeed.Enabled() {
		return -1, fmt.Errorf("unsupported in raft mode")
	}
	return executor.AttestPayloadLossForCommit(commitIndex), nil
}

func (api *AdminApi) SetState(ctx context.Context, password string, state processor.State) (processor.State, error) {
	if err := api.authenticate(ctx, password); err != nil {
		return -1, err
	}
	oldState := api.App.blockProcessor.GetState()
	if (oldState != processor.StatePendingLook && state != processor.StateLook) && (state == processor.StatePendingLook && oldState == processor.StateLook) {
		api.App.blockProcessor.SetState(state)
	} else {
		return -1, errInvalidTypeState
	}
	stateNew := api.App.blockProcessor.GetState()

	return stateNew, nil
}

func (api *AdminApi) GetState(ctx context.Context) (processor.State, error) {
	state := api.App.blockProcessor.GetState()
	stateString := state
	return stateString, nil
}

func (api *AdminApi) CreateBackup(ctx context.Context, password string) (string, error) {
	if err := api.authenticate(ctx, password); err != nil {
		return "", err
	}
	state := api.App.blockProcessor.GetState()

	if state == processor.StateLook {
		blockNumber := api.App.blockProcessor.GetLastBlock().Header().BlockNumber() // Correctly assign lastBlock
		backupFileName := fmt.Sprintf("BackupFromBlockNumber-%d", blockNumber)
		snapshot.Backup(api.App.config.Databases.RootPath, api.App.config.BackupPath, backupFileName)
		return backupFileName, nil

	} else {
		return "", errStateNotReady

	}
}

// VerifyHistoricalRoot (admin_verifyHistoricalRoot) rebuilds the account trie at a historical block from the changelog
// and compares the recovered root with the block header's stateRoot. It is a full-state rebuild under a global mutex,
// so it is an operator diagnostic and not part of the public eth namespace.
func (api *AdminApi) VerifyHistoricalRoot(ctx context.Context, password string, blockNrOrHash rpc.BlockNumberOrHash) (map[string]interface{}, error) {
	if err := api.authenticate(ctx, password); err != nil {
		return nil, err
	}
	if api.meta == nil {
		return nil, fmt.Errorf("verifyHistoricalRoot is not available")
	}
	return api.meta.verifyHistoricalRoot(ctx, blockNrOrHash)
}

// AddHelpPayWallet (admin_addHelpPayWallet) adds a sponsor wallet by private key and syncs across the cluster.
func (api *AdminApi) AddHelpPayWallet(ctx context.Context, password string, privateKeyHex string) (map[string]interface{}, error) {
	if err := api.authenticate(ctx, password); err != nil {
		return nil, err
	}
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
	}
	cleanKey := strings.TrimSpace(privateKeyHex)
	addr, err := api.App.helpPayManager.AddWallet(cleanKey, false)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"address": addr.Hex(),
		"status":  "added",
		"local":   true,
	}, nil
}

// RemoveHelpPayWallet (admin_removeHelpPayWallet) removes a sponsor wallet by address locally on this node.
func (api *AdminApi) RemoveHelpPayWallet(ctx context.Context, password string, addressHex string) (map[string]interface{}, error) {
	if err := api.authenticate(ctx, password); err != nil {
		return nil, err
	}
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
	}
	addr := common.HexToAddress(addressHex)
	if err := api.App.helpPayManager.RemoveWallet(addr, false); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"address": addr.Hex(),
		"status":  "removed",
		"local":   true,
	}, nil
}

// GetHelpPayWallets (admin_getHelpPayWallets) lists all sponsor wallets.
func (api *AdminApi) GetHelpPayWallets(ctx context.Context, password string) ([]helppay.WalletStatus, error) {
	if err := api.authenticate(ctx, password); err != nil {
		return nil, err
	}
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
	}
	return api.App.helpPayManager.GetWallets(), nil
}

// SetHelpPayConfig (admin_setHelpPayConfig) updates help-pay configuration and syncs across the cluster.
func (api *AdminApi) SetHelpPayConfig(ctx context.Context, password string, enabled bool, minBalance string, extraAmount string) (map[string]interface{}, error) {
	if err := api.authenticate(ctx, password); err != nil {
		return nil, err
	}
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
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

// ToggleHelpPay (admin_toggleHelpPay) enables or disables help-pay and syncs across the cluster.
func (api *AdminApi) ToggleHelpPay(ctx context.Context, password string, enabled bool) (map[string]interface{}, error) {
	if err := api.authenticate(ctx, password); err != nil {
		return nil, err
	}
	if api.App == nil || api.App.helpPayManager == nil {
		return nil, fmt.Errorf("helppay manager not initialized")
	}
	if err := api.App.helpPayManager.SetEnabled(enabled, true); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"status":  "success",
		"enabled": enabled,
		"synced":  true,
	}, nil
}

// GetHelpPayConfig (admin_getHelpPayConfig) returns the current help-pay configuration.
func (api *AdminApi) GetHelpPayConfig(ctx context.Context, password string) (config.HelpPayConfig, error) {
	if err := api.authenticate(ctx, password); err != nil {
		return config.HelpPayConfig{}, err
	}
	if api.App == nil || api.App.helpPayManager == nil {
		return config.HelpPayConfig{}, fmt.Errorf("helppay manager not initialized")
	}
	return api.App.helpPayManager.GetConfig(), nil
}
