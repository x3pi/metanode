package config

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
)

// GenesisConfig cấu trúc dữ liệu cho phần config trong genesis.json
type GenesisConfig struct {
	ChainId              *big.Int `json:"chainId"`
	Epoch                int      `json:"epoch"`
	EpochTimestampMs     uint64   `json:"epoch_timestamp_ms,omitempty"`
	AttestationInterval  uint64   `json:"attestation_interval,omitempty"`   // Blocks between state attestations (default 10)
	EpochDurationSeconds uint64   `json:"epoch_duration_seconds,omitempty"` // Epoch duration in seconds (default 900 = 15 min)

	// Protocol is the optional, shared declaration of the rules every validator of this chain must run with. Nothing here
	// changes execution: at startup the node compares each DECLARED field with its own config.json and refuses to start
	// on a difference (see pkg/protocolparams). Absent = no cross-check (all chains created before this existed).
	// Because genesis.json is what GenesisDigest commits to, declare it before the digest is published; adding it to a
	// chain whose digest is already published changes the file the digest was computed over.
	Protocol *ProtocolSection `json:"protocol,omitempty"`
}

// ProtocolSection lists the rules that decide how blocks execute. A nil field (or nil slice) is "not declared" and is not
// checked; a declared empty list means "none".
type ProtocolSection struct {
	TxSignatureMode  *string             `json:"tx_signature_mode,omitempty"`  // "secp" or "default"
	AccountGate      *string             `json:"account_gate,omitempty"`       // "parent_registered" or "off"
	FreeFeeAddresses []string            `json:"free_fee_addresses,omitempty"` // hex, with or without 0x
	CrossChain       *ProtocolCrossChain `json:"cross_chain,omitempty"`
}

// ProtocolCrossChain mirrors the cross_chain config fields that are copied into the gateway's on-chain state.
type ProtocolCrossChain struct {
	ReserveChainID               *uint64 `json:"reserve_chain_id,omitempty"`
	BondLeverage                 *uint64 `json:"bond_leverage,omitempty"`
	UnbondingPeriodSeconds       *uint64 `json:"unbonding_period_seconds,omitempty"`
	MinSecurityBondToRegisterWei *string `json:"min_security_bond_to_register_wei,omitempty"`
	MinNativeStakeToRegisterWei  *string `json:"min_native_stake_to_register_wei,omitempty"`
	VelocityLimitDisabled        *bool   `json:"velocity_limit_disabled,omitempty"`
}

// GenesisData cấu trúc dữ liệu chính cho toàn bộ genesis.json
type GenesisData struct {
	Config             GenesisConfig            `json:"config"`
	Validators         []*pb.Validator          `json:"validators"`
	Alloc              []state.JsonAccountState `json:"alloc"`
	RegisteredAccounts []string                 `json:"registered_accounts,omitempty"`
}

// LoadGenesisData loads genesis data from a JSON file.  Returns an error if the file cannot be opened or parsed.
func LoadGenesisData(filePath string) (*GenesisData, error) {
	// Mở tệp genesis.json
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("lỗi khi mở tệp: %w", err)
	}
	defer file.Close()
	// Giải mã dữ liệu JSON
	decoder := json.NewDecoder(file)
	var genesisData GenesisData
	err = decoder.Decode(&genesisData)
	if err != nil {
		return nil, fmt.Errorf("lỗi khi giải mã JSON: %w", err)
	}

	return &genesisData, nil
}

// PrintGenesisData prints the genesis data to the console.  Primarily for debugging purposes.
func PrintGenesisData(genesisData *GenesisData) {
	fmt.Println("Config:")
	fmt.Println("  ChainID:", genesisData.Config.ChainId)
	fmt.Println("\nAlloc:")
	for _, alloc := range genesisData.Alloc {
		fmt.Println("  Address:", alloc.Address)
		fmt.Println("  Balance:", alloc.Balance)
		fmt.Println("  Pending Balance:", alloc.PendingBalance)
		fmt.Println("  Last Hash:", alloc.LastHash)
		fmt.Println("  Device Key:", alloc.DeviceKey)
		fmt.Println("  Public Key BLS:", alloc.PublicKeyBls)
		fmt.Println("---")
	}
}
