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
	AttestationInterval  uint64   `json:"attestation_interval,omitempty"`  // Blocks between state attestations (default 10)
	EpochDurationSeconds uint64   `json:"epoch_duration_seconds,omitempty"` // Epoch duration in seconds (default 900 = 15 min)
}

// GenesisData cấu trúc dữ liệu chính cho toàn bộ genesis.json
type GenesisData struct {
	Config     GenesisConfig            `json:"config"`
	Validators []*pb.Validator          `json:"validators"`
	Alloc      []state.JsonAccountState `json:"alloc"`
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
