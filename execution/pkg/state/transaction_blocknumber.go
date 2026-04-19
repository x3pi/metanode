package state

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

type TransactionBlockNumber struct {
	Hash        common.Hash
	BlockNumber uint64
}

func NewTransactionBlockNumber(Hash common.Hash, BlockNumber uint64) *TransactionBlockNumber {
	return &TransactionBlockNumber{
		Hash:        Hash,
		BlockNumber: BlockNumber, // trạng thái khởi tạo
	}
}
func (ts *TransactionBlockNumber) FromProto(pbData *pb.TransactionBlockNumber) {
	ts.Hash = common.Hash(pbData.Hash) // Cập nhật trạng thái từ proto
	//Thêm xử lý block number
	ts.BlockNumber = pbData.BlockNumber // Sửa: Trực tiếp lấy giá trị BlockNumber từ pbData
}

// ... existing code ...
func (ts *TransactionBlockNumber) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(&pb.TransactionBlockNumber{
		Hash:        ts.Hash[:],     // Chuyển đổi Hash thành []byte
		BlockNumber: ts.BlockNumber, // Thêm trường block number
	})
}

// Unmarshal giải mã dữ liệu từ byte
func (ts *TransactionBlockNumber) Unmarshal(b []byte) error {
	tsProto := &pb.TransactionBlockNumber{} // Thay đổi tên gói proto cho phù hợp
	err := proto.Unmarshal(b, tsProto)
	if err != nil {
		return err
	}
	ts.Hash = common.Hash(tsProto.Hash) // Cập nhật trạng thái từ proto
	//Thêm xử lý block number
	ts.BlockNumber = tsProto.BlockNumber // Sửa: Trực tiếp lấy giá trị BlockNumber từ tsProto
	return nil
}

// general
func (ts *TransactionBlockNumber) Proto() *TransactionBlockNumber {
	return &TransactionBlockNumber{
		Hash:        ts.Hash,
		BlockNumber: ts.BlockNumber,
	}
}

func (ts *TransactionBlockNumber) String() string {
	b, _ := json.MarshalIndent(ts, "", " ")
	return string(b)
}

// getter
func (ts *TransactionBlockNumber) TransactionID() common.Hash {
	return ts.Hash
}

func (ts *TransactionBlockNumber) BlockNumberID() uint64 {
	return ts.BlockNumber
}
