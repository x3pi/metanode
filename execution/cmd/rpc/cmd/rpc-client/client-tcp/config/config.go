package config

import (
	"encoding/json"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/types"
)

type ClientConfig struct {
	PrivateKey_ string `json:"private_key"`

	ConnectionAddress_       string `json:"connection_address"`
	PublicConnectionAddress_ string `json:"public_connection_address"`

	Version_          string       `json:"version"`
	TransactionFeeHex string       `json:"transaction_fee"`
	TransactionFee    *uint256.Int `json:"-"`

	ParentAddress           string   `json:"parent_address"`
	ParentConnectionAddress string   `json:"parent_connection_address"`
	ParentConnectionType    string   `json:"parent_connection_type"`
	ChainId                 uint64   `json:"chain_id"`
	OwnerFileStorageAddress string   `json:"owner_file_storage_address"`
	PkAdminFileStorage      string   `json:"pk_admin_file_storage"`
	BlsAdminStorage         string   `json:"bls_admin_storage"`
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

func (c *ClientConfig) Address() common.Address {
	_, _, address := bls.GenerateKeyPairFromSecretKey(c.PrivateKey_)
	return address
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
	return config, nil
}
