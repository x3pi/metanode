package main

import (
	"context"
	"crypto/subtle"
	"fmt"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor"
	"github.com/meta-node-blockchain/meta-node/executor"
	mt_filters "github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/snapshot"
)

type AdminApi struct {
	App    *App // Export field Client
	events *mt_filters.EventSystem
}

// LoginAPI is a simple API for user login using only a password.
func (api *AdminApi) LoginAPI(ctx context.Context, password string) (string, error) {
	if subtle.ConstantTimeCompare([]byte(password), []byte(api.App.config.Securepassword)) != 1 {
		return "", errInvalidCredentials
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
	if subtle.ConstantTimeCompare([]byte(password), []byte(api.App.config.Securepassword)) != 1 {
		return -1, errInvalidCredentials
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
	if subtle.ConstantTimeCompare([]byte(password), []byte(api.App.config.Securepassword)) != 1 {
		return -1, errInvalidCredentials
	}
	return executor.AttestPayloadLossForCommit(commitIndex), nil
}

func (api *AdminApi) SetState(ctx context.Context, password string, state processor.State) (processor.State, error) {

	if subtle.ConstantTimeCompare([]byte(password), []byte(api.App.config.Securepassword)) != 1 {
		return -1, errInvalidCredentials
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
	if subtle.ConstantTimeCompare([]byte(password), []byte(api.App.config.Securepassword)) != 1 {
		return "", errInvalidCredentials
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
