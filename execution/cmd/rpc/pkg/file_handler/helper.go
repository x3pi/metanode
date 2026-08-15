package file_handler

import (
	"log"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

func PredictContractAddress(deployer common.Address) common.Address {
	data, err := rlp.EncodeToBytes([]interface{}{deployer, uint64(2)})
	if err != nil {
		log.Fatalf("RLP encode error: %v", err)
	}
	// Băm Keccak256
	hash := crypto.Keccak256(data)
	// Địa chỉ = 20 byte cuối của hash
	return common.BytesToAddress(hash[12:])
}
