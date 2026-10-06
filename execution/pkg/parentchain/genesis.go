package parentchain

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

var (
	ErrGenesisInvalid = errors.New("parentchain: genesis configuration invalid")
)

type GenesisValidator struct {
	Name         string `json:"name"`
	Address      string `json:"address"`
	Stake        string `json:"stake"`
	AuthorityKey string `json:"authority_key"` // Base64 or Hex
	ProtocolKey  string `json:"protocol_key"`  // Base64 or Hex
	NetworkKey   string `json:"network_key"`   // Base64 or Hex
	P2PAddress   string `json:"p2p_address"`
}

type GenesisAccount struct {
	Address string `json:"address"`
	Balance string `json:"balance"`
}

type Genesis struct {
	ChainID              uint64             `json:"chain_id"`
	EpochDurationSeconds uint64             `json:"epoch_duration_seconds"`
	EpochTimestampMs     uint64             `json:"epoch_timestamp_ms,omitempty"`
	Validators           []GenesisValidator `json:"validators"`
	Accounts             []GenesisAccount   `json:"accounts,omitempty"`

	// OpenClusterRegistration lets any key register as a cluster (devnet only). When false, only the keys listed
	// in Clusters may register. See ClusterPolicy.
	OpenClusterRegistration bool `json:"open_cluster_registration"`
	// Clusters lists the BLS public keys (hex or base64, 48 bytes) of the clusters allowed to register.
	Clusters []string `json:"clusters,omitempty"`

	// FloatAccounts are the float balances that exist at genesis, keyed by BLS public key. They are applied
	// deterministically in block 1 and are the whole float supply when AllowDepositToFloat is false.
	FloatAccounts []GenesisFloatAccount `json:"float_accounts,omitempty"`
	// MinFloatToRegister (decimal base units; 1 unit = 10^18) lets any key whose float balance reaches it register
	// as a cluster, in addition to Clusters. Empty or "0" disables the balance rule.
	MinFloatToRegister string `json:"min_float_to_register,omitempty"`
	// AllowDepositToFloat enables depositToFloat (the only mint path). Devnet / tests only.
	AllowDepositToFloat bool `json:"allow_deposit_to_float"`
}

// GenesisFloatAccount is one genesis float allocation.
type GenesisFloatAccount struct {
	BLSPublicKey string `json:"bls_public_key"` // 48 bytes, hex or base64
	Balance      string `json:"balance"`        // decimal base units
}

// FloatAllocation is a validated genesis float allocation.
type FloatAllocation struct {
	Key     cm.PublicKey
	Balance *big.Int
}

// FloatAllocations validates and returns the genesis float allocations in file order.
func (g *Genesis) FloatAllocations() ([]FloatAllocation, error) {
	seen := map[cm.PublicKey]bool{}
	out := make([]FloatAllocation, 0, len(g.FloatAccounts))
	for i, a := range g.FloatAccounts {
		raw, err := decodeKey(a.BLSPublicKey)
		if err != nil || len(raw) != 48 {
			return nil, fmt.Errorf("%w: float_accounts[%d] is not a 48-byte BLS public key", ErrGenesisInvalid, i)
		}
		var k cm.PublicKey
		copy(k[:], raw)
		if seen[k] {
			return nil, fmt.Errorf("%w: float_accounts[%d] duplicate key", ErrGenesisInvalid, i)
		}
		seen[k] = true
		bal, ok := new(big.Int).SetString(strings.TrimSpace(a.Balance), 10)
		if !ok || bal.Sign() <= 0 {
			return nil, fmt.Errorf("%w: float_accounts[%d] balance must be a positive decimal integer", ErrGenesisInvalid, i)
		}
		out = append(out, FloatAllocation{Key: k, Balance: bal})
	}
	return out, nil
}

// ClusterPolicy converts the genesis cluster settings into the policy enforced during execution.
func (g *Genesis) ClusterPolicy() (ClusterPolicy, error) {
	p := ClusterPolicy{Open: g.OpenClusterRegistration, Allowed: map[cm.PublicKey]struct{}{}, AllowDeposit: g.AllowDepositToFloat}
	if m := strings.TrimSpace(g.MinFloatToRegister); m != "" {
		v, ok := new(big.Int).SetString(m, 10)
		if !ok || v.Sign() < 0 {
			return ClusterPolicy{}, fmt.Errorf("%w: min_float_to_register must be a non-negative decimal integer", ErrGenesisInvalid)
		}
		p.MinFloat = v
	}
	for i, c := range g.Clusters {
		raw, err := decodeKey(c)
		if err != nil || len(raw) != 48 {
			return ClusterPolicy{}, fmt.Errorf("%w: clusters[%d] is not a 48-byte BLS public key", ErrGenesisInvalid, i)
		}
		var k cm.PublicKey
		copy(k[:], raw)
		p.Allowed[k] = struct{}{}
	}
	return p, nil
}

// LoadGenesis reads and validates a parent chain genesis file.
func LoadGenesis(path string) (*Genesis, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read genesis file %s: %w", path, err)
	}

	var g Genesis
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("%w: failed to parse JSON: %v", ErrGenesisInvalid, err)
	}

	if g.ChainID == 0 {
		return nil, fmt.Errorf("%w: chain_id must be positive", ErrGenesisInvalid)
	}

	if len(g.Validators) == 0 {
		return nil, fmt.Errorf("%w: validators list cannot be empty", ErrGenesisInvalid)
	}
	if len(g.Validators) < 4 {
		fmt.Printf("⚠️ Warning: Genesis has %d validators (< 4). Minimum 4 required for BFT quorum in production.\n", len(g.Validators))
	}

	seenAddresses := make(map[string]bool)
	seenProtocol := make(map[string]bool)
	seenNetwork := make(map[string]bool)

	for i, v := range g.Validators {
		if strings.TrimSpace(v.Name) == "" {
			return nil, fmt.Errorf("%w: validator %d missing name", ErrGenesisInvalid, i)
		}
		addr := strings.ToLower(strings.TrimSpace(v.Address))
		if addr == "" || seenAddresses[addr] {
			return nil, fmt.Errorf("%w: validator %d duplicate or empty address %s", ErrGenesisInvalid, i, v.Address)
		}
		seenAddresses[addr] = true

		stakeVal, ok := new(big.Int).SetString(strings.TrimSpace(v.Stake), 10)
		if !ok || stakeVal.Sign() <= 0 {
			return nil, fmt.Errorf("%w: validator %d invalid or non-positive stake: %s", ErrGenesisInvalid, i, v.Stake)
		}

		if strings.TrimSpace(v.P2PAddress) == "" {
			return nil, fmt.Errorf("%w: validator %d missing p2p_address", ErrGenesisInvalid, i)
		}

		protBytes, err := decodeKey(v.ProtocolKey)
		if err != nil || len(protBytes) != 32 {
			return nil, fmt.Errorf("%w: validator %d invalid protocol_key (expected 32 bytes): %v", ErrGenesisInvalid, i, err)
		}
		if seenProtocol[string(protBytes)] {
			return nil, fmt.Errorf("%w: validator %d duplicate protocol_key", ErrGenesisInvalid, i)
		}
		seenProtocol[string(protBytes)] = true

		netBytes, err := decodeKey(v.NetworkKey)
		if err != nil || len(netBytes) != 32 {
			return nil, fmt.Errorf("%w: validator %d invalid network_key (expected 32 bytes): %v", ErrGenesisInvalid, i, err)
		}
		if seenNetwork[string(netBytes)] {
			return nil, fmt.Errorf("%w: validator %d duplicate network_key", ErrGenesisInvalid, i)
		}
		seenNetwork[string(netBytes)] = true

		authBytes, err := decodeKey(v.AuthorityKey)
		if err != nil || (len(authBytes) != 48 && len(authBytes) != 96) {
			return nil, fmt.Errorf("%w: validator %d invalid authority_key length %d (expected 48 or 96 bytes)", ErrGenesisInvalid, i, len(authBytes))
		}
	}

	if g.EpochDurationSeconds == 0 {
		g.EpochDurationSeconds = 86400
	}

	if _, err := g.ClusterPolicy(); err != nil {
		return nil, err
	}
	if _, err := g.FloatAllocations(); err != nil {
		return nil, err
	}

	return &g, nil
}

func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return hex.DecodeString(s[2:])
	}
	// Try hex first if valid hex
	if len(s)%2 == 0 {
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	// Otherwise try base64
	return base64.StdEncoding.DecodeString(s)
}

// ToProtoValidators converts Genesis.Validators into protobuf ValidatorInfo.
func (g *Genesis) ToProtoValidators() ([]*pb.ValidatorInfo, error) {
	validators := make([]*pb.ValidatorInfo, len(g.Validators))
	for i, v := range g.Validators {
		authBytes, err := decodeKey(v.AuthorityKey)
		if err != nil {
			return nil, fmt.Errorf("validator %d invalid authority_key: %w", i, err)
		}
		protBytes, err := decodeKey(v.ProtocolKey)
		if err != nil {
			return nil, fmt.Errorf("validator %d invalid protocol_key: %w", i, err)
		}
		netBytes, err := decodeKey(v.NetworkKey)
		if err != nil {
			return nil, fmt.Errorf("validator %d invalid network_key: %w", i, err)
		}

		addr := common.HexToAddress(v.Address)
		stake := v.Stake
		if stake == "" {
			stake = "1000000000000000000"
		}

		validators[i] = &pb.ValidatorInfo{
			Address:      addr.Hex(),
			Stake:        stake,
			AuthorityKey: authBytes,
			ProtocolKey:  protBytes,
			NetworkKey:   netBytes,
			Name:         v.Name,
			P2PAddress:   v.P2PAddress,
		}
	}
	return validators, nil
}
