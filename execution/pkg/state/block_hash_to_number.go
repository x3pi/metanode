package state

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

type BlockHashToNumber struct {
	Hash        common.Hash
	BlockNumber uint64
}

func NewBlockHashToNumber(Hash common.Hash, BlockNumber uint64) *BlockHashToNumber {
	return &BlockHashToNumber{
		Hash:        Hash,
		BlockNumber: BlockNumber, // trạng thái khởi tạo
	}
}
func (ts *BlockHashToNumber) FromProto(pbData *pb.BlockHashToNumber) {
	ts.Hash = common.Hash(pbData.Hash) // Cập nhật trạng thái từ proto
	//Thêm xử lý block number
	ts.BlockNumber = pbData.BlockNumber // Sửa: Trực tiếp lấy giá trị BlockNumber từ pbData
}

// ... existing code ...
func (ts *BlockHashToNumber) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(&pb.BlockHashToNumber{
		Hash:        ts.Hash[:],     // Chuyển đổi Hash thành []byte
		BlockNumber: ts.BlockNumber, // Thêm trường block number
	})
}

// Unmarshal giải mã dữ liệu từ byte
func (ts *BlockHashToNumber) Unmarshal(b []byte) error {
	tsProto := &pb.BlockHashToNumber{} // Thay đổi tên gói proto cho phù hợp
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
func (ts *BlockHashToNumber) Proto() *BlockHashToNumber {
	return &BlockHashToNumber{
		Hash:        ts.Hash,
		BlockNumber: ts.BlockNumber,
	}
}

func (ts *BlockHashToNumber) String() string {
	b, _ := json.MarshalIndent(ts, "", " ")
	return string(b)
}

// getter
func (ts *BlockHashToNumber) TransactionID() common.Hash {
	return ts.Hash
}

func (ts *BlockHashToNumber) BlockNumberID() uint64 {
	return ts.BlockNumber
}
