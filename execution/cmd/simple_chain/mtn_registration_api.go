package main

import (
	"encoding/hex"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
)

// Account registration RPC (namespace "mtn"). With the account gate on, an address may only send transactions once the
// Parent Chain has registered it to THIS cluster. The user talks only to this node: it relays the registration to the
// Parent Chain automatically (rollup.RegistrationRelay) and the confirmed registration comes back as an ordered system
// event that sets the on-chain flag. A client signs the message returned by mtn_getRegistrationMessage with the
// address's secp256k1 key, calls mtn_registerAccount, then polls mtn_getRegistrationStatus.

var errRegistrationDisabled = errors.New("account registration is not enabled on this node (account_gate is not parent_registered)")

// ClusterIdentity is returned by mtn_getClusterIdentity.
type ClusterIdentity struct {
	ClusterKey  string `json:"clusterKey"`  // BLS public key of this cluster (hex)
	ChainID     uint64 `json:"chainId"`     // chain ID of this execution chain
	AccountGate bool   `json:"accountGate"` // whether senders must be parent-registered
	MessageTag  string `json:"messageTag"`  // domain tag of the message to sign
}

// RegistrationMessage is returned by mtn_getRegistrationMessage: sign HashToSign (raw secp256k1 signature over the
// 32-byte hash, no Ethereum prefix) with the address's key.
type RegistrationMessage struct {
	Digest     string `json:"digest"`
	HashToSign string `json:"hashToSign"`
}

// RegistrationInfo is returned by mtn_registerAccount and mtn_getRegistrationStatus.
type RegistrationInfo struct {
	Status      string `json:"status"`                // NONE | PENDING | CONFIRMED | REJECTED | FAILED
	HomeCluster string `json:"homeCluster,omitempty"` // the cluster that won (REJECTED only)
	Reason      string `json:"reason,omitempty"`
}

func (api *MtnAPI) registrationRelay() (*rollup.RegistrationRelay, error) {
	if api.App == nil || api.App.regRelay == nil || api.App.config == nil || !api.App.config.AccountGateParentRegistered() {
		return nil, errRegistrationDisabled
	}
	return api.App.regRelay, nil
}

func toRegistrationInfo(res rollup.RegistrationResult) *RegistrationInfo {
	info := &RegistrationInfo{Status: string(res.Status), Reason: res.Reason}
	if res.HomeCluster != nil {
		info.HomeCluster = "0x" + hex.EncodeToString(res.HomeCluster[:])
	}
	return info
}

// GetClusterIdentity returns the cluster key users register against.
func (api *MtnAPI) GetClusterIdentity() (*ClusterIdentity, error) {
	relay, err := api.registrationRelay()
	if err != nil {
		return nil, err
	}
	key := relay.ClusterKey()
	return &ClusterIdentity{
		ClusterKey:  "0x" + hex.EncodeToString(key[:]),
		ChainID:     api.App.config.ChainId.Uint64(),
		AccountGate: true,
		MessageTag:  string(parentchain.RegisterAccountDomainTag),
	}, nil
}

// GetRegistrationMessage returns the exact bytes a user must sign to register address with this cluster.
func (api *MtnAPI) GetRegistrationMessage(address common.Address) (*RegistrationMessage, error) {
	relay, err := api.registrationRelay()
	if err != nil {
		return nil, err
	}
	digest := relay.RegistrationDigest(address)
	return &RegistrationMessage{
		Digest:     hexutil.Encode(digest),
		HashToSign: hexutil.Encode(crypto.Keccak256(digest)),
	}, nil
}

// RegisterAccount accepts a registration request. It returns immediately with PENDING (or the current state if the
// address is already known); the relay and the Parent Chain finish the job automatically.
func (api *MtnAPI) RegisterAccount(address common.Address, userSig hexutil.Bytes) (*RegistrationInfo, error) {
	relay, err := api.registrationRelay()
	if err != nil {
		return nil, err
	}
	res, err := relay.Submit(address, userSig)
	if err != nil {
		return nil, err
	}
	return toRegistrationInfo(res), nil
}

// GetRegistrationStatus reports where an address is in the registration flow. The on-chain flag always wins.
func (api *MtnAPI) GetRegistrationStatus(address common.Address) (*RegistrationInfo, error) {
	relay, err := api.registrationRelay()
	if err != nil {
		return nil, err
	}
	return toRegistrationInfo(relay.Status(address)), nil
}
