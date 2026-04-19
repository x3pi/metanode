package file_handler

import (
	"github.com/meta-node-blockchain/meta-node/pkg/models/file_model"
	"github.com/meta-node-blockchain/meta-node/types"
)

type BlockchainCommunicator interface {
	GetFileInfo(fileKey [32]byte, tx types.Transaction) (*file_model.FileInfo, error)
	GetRustServerAddresses(tx types.Transaction) ([]string, error)
	SendConfirmation(fileKey [32]byte, tx types.Transaction) (types.Receipt, error)
}
