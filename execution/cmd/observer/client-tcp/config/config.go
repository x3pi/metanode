package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types"
)

type RemoteChain struct {
	Name                    string `json:"name"`
	NationId                uint64 `json:"nation_id"`
	ConnectionAddress       string `json:"connection_address"`       // TCP address của remote chain (mới)
	LocalContract           string `json:"local_contract"`
	ParentAddress           string `json:"parent_address"`
	ParentConnectionAddress string `json:"parent_connection_address"` // backward compat
	RemoteContract          string `json:"remote_contract"`
	Privatekey              string `json:"private_key"`
	EthPrivateKey           string `json:"eth_private_key"`
}

type ClientConfig struct {
	PrivateKey_ string `json:"private_key"`

	ConnectionAddress_       string `json:"connection_address"`
	PublicConnectionAddress_ string `json:"public_connection_address"`

	Version_          string       `json:"version"`
	TransactionFeeHex string       `json:"transaction_fee"`
	TransactionFee    *uint256.Int `json:"-"`

	ParentAddress           string `json:"parent_address"`
	ParentConnectionAddress string `json:"parent_connection_address"`
	ParentConnectionType    string `json:"parent_connection_type"`
	ChainId                 uint64 `json:"chain_id"`
	NationId                uint64 `json:"nation_id"`

	// Supervisor fields
	LogPath string `json:"log_path"`

	// Cross-chain gateway contract
	CrossChainAbiPath_  string  `json:"cross_chain_abi_path"`
	CrossChainContract_ string  `json:"contract_cross_chain"`
	CrossChainAbi       abi.ABI `json:"-"`

	// Config registry contract (batchUpdateScanProgress, embassy management)
	ConfigAbiPath_  string  `json:"config_abi_path"`
	ConfigContract_ string  `json:"contract_config"`
	ConfigAbi       abi.ABI `json:"-"`

	// Demo ABI config
	DemoAbiPath_        string  `json:"demo_abi_path"`
	DemoContractAddress string  `json:"demo_contract_address"`
	DemoAbi             abi.ABI `json:"-"`
	EthPrivateKey       string  `json:"eth_private_key"`

	// Remote chains to scan logs from (B, C, D...)
	RemoteChains []RemoteChain `json:"remote_chains"`
}

func (c *ClientConfig) CrossChainAbiPath() string {
	return c.CrossChainAbiPath_
}

func (c *ClientConfig) CrossChainContract() common.Address {
	return common.HexToAddress(c.CrossChainContract_)
}

func (c *ClientConfig) ConnectionAddress() string {
	return c.ConnectionAddress_
}

func (c *ClientConfig) PublicConnectionAddress() string {
	return c.PublicConnectionAddress_
}

func (c *ClientConfig) Version() string {
	return c.Version_
}

func (c *ClientConfig) PrivateKey() []byte {
	return common.FromHex(c.PrivateKey_)
}

// GetRemoteChainConnectionAddress tìm parent_connection_address theo nationId.
// Tìm cả local chain (nếu nationIdStr == NationId) và remote chains.
func (c *ClientConfig) GetRemoteChainConnectionAddress(nationIdStr string) string {
	// Check local chain
	if fmt.Sprintf("%d", c.NationId) == nationIdStr {
		return c.ParentConnectionAddress
	}
	// Check remote chains
	for _, rc := range c.RemoteChains {
		if fmt.Sprintf("%d", rc.NationId) == nationIdStr {
			return rc.ParentConnectionAddress
		}
	}
	return ""
}

func (c *ClientConfig) Address() common.Address {
	_, _, address := bls.GenerateKeyPairFromSecretKey(c.PrivateKey_)
	return address
}

// BlsPublicKey trả về raw BLS public key bytes (48 bytes) derive từ PrivateKey.
// Observer dùng để gửi kèm trong batchSubmit calldata → chain verify O(1).
func (c *ClientConfig) BlsPublicKey() []byte {
	_, pub, _ := bls.GenerateKeyPairFromSecretKey(c.PrivateKey_)
	return pub.Bytes()
}

func (c *ClientConfig) NodeType() string {
	return p_common.CLIENT_CONNECTION_TYPE
}

func LoadConfig(configPath string) (types.Config, error) {
	// general config
	config := &ClientConfig{}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(raw, config)
	if err != nil {
		return nil, err
	}
	config.TransactionFee = uint256.NewInt(0).SetBytes(common.FromHex(config.TransactionFeeHex))

	// Load ABI file if path is specified
	if config.CrossChainAbiPath_ != "" {
		abiData, err := os.ReadFile(config.CrossChainAbiPath_)
		if err != nil {
			return nil, err
		}
		// Parse ABI JSON into abi.ABI object
		parsedAbi, err := abi.JSON(strings.NewReader(string(abiData)))
		if err != nil {
			logger.Error("Failed to parse ABI JSON: %v", err)
			return nil, err
		}
		config.CrossChainAbi = parsedAbi
	} else {
		logger.Error("CrossChainAbiPath is empty")
		return nil, errors.New("CrossChainAbiPath is empty")
	}

	// Load Demo ABI if path is specified
	if config.DemoAbiPath_ != "" {
		demoAbiData, err := os.ReadFile(config.DemoAbiPath_)
		if err != nil {
			logger.Warn("Failed to read demo ABI file: %v", err)
		} else {
			parsedDemoAbi, err := abi.JSON(strings.NewReader(string(demoAbiData)))
			if err != nil {
				logger.Warn("Failed to parse demo ABI JSON: %v", err)
			} else {
				config.DemoAbi = parsedDemoAbi
			}
		}
	}

	// Load Config ABI if path is specified
	if config.ConfigAbiPath_ != "" {
		configAbiData, err := os.ReadFile(config.ConfigAbiPath_)
		if err != nil {
			logger.Warn("Failed to read config ABI file: %v", err)
		} else {
			parsedConfigAbi, err := abi.JSON(strings.NewReader(string(configAbiData)))
			if err != nil {
				logger.Warn("Failed to parse config ABI JSON: %v", err)
			} else {
				config.ConfigAbi = parsedConfigAbi
			}
		}
	}

	return config, nil
}
