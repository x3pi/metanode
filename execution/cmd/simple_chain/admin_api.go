package main

import (
	"context"
	"crypto/subtle"
	"fmt"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor"
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
