package file_model

import (
	"math/big"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/types"
)

type FileStatus int

const (
	Processing FileStatus = 0
	Active     FileStatus = 1
)

// --- Các cấu trúc để giao tiếp với Rust qua TCP ---

type Command struct {
	Command string      `json:"command"`
	Payload interface{} `json:"payload"`
}

type UploadChunkPayload struct {
	FileKey           string   `json:"file_key"`
	ChunkIndex        int      `json:"chunk_index"`
	ChunkDataBase64   string   `json:"chunk_data_base64"`
	Signature         string   `json:"signature"`
	MerkleProofHashes []string `json:"merkle_proof_hashes"` // Hex strings of proof hashes
	MerkleRoot        string   `json:"merkle_root"`         // Hex string of merkle root
}

type DownloadChunkPayload struct {
	FileKey    string `json:"file_key"`
	ChunkIndex int    `json:"chunk_index"`
}

type GenericResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type DownloadResponse struct {
	Status          string `json:"status"`
	ChunkDataBase64 string `json:"chunk_data_base64"`
}

type FileUploadProgress struct {
	UploadedChunks atomic.Uint64
	TotalChunks    *big.Int
}

type ConfirmationJob struct {
	FileKey [32]byte
	Tx      types.Transaction
}

type FileInfo struct {
	SomeString     string         // Trường 0
	OwnerPublicKey [32]byte       // Trường 2
	FileSize       *big.Int       // Trường 3
	TotalChunks    *big.Int       // Trường 4
	UploadTime     *big.Int       // Trường 5
	FileName       string         // Trường 6
	FileType       string         // Trường 7
	Category       string         // Trường 8
	ContentID      string         // Trường 9
	Status         uint8          // Trường 10
	MerkleRoot     [32]byte       // Trường 11
	OwnerAddress   common.Address // Trường 12
	Signature      string
}
