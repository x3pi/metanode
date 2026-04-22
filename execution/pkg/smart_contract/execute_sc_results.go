package smart_contract

import (
	"bytes"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto" // Nếu bạn sử dụng Keccak256
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

type ExecuteSCResults struct {
	results     []types.ExecuteSCResult
	groupId     uint64
	blockNumber uint64
}

// ExecuteSCResults

func NewExecuteSCResults(
	results []types.ExecuteSCResult,
	groupId uint64,
	blockNumber uint64,
) *ExecuteSCResults {
	return &ExecuteSCResults{
		results:     results,
		groupId:     groupId,
		blockNumber: blockNumber,
	}
}

func (er *ExecuteSCResults) Unmarshal(b []byte) error {
	pbExecuteSCResults := &pb.ExecuteSCResults{}
	err := proto.Unmarshal(b, pbExecuteSCResults)
	if err != nil {
		return err
	}
	er.FromProto(pbExecuteSCResults)
	return nil
}

func (er *ExecuteSCResults) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(er.Proto())
}

func (er *ExecuteSCResults) Proto() protoreflect.ProtoMessage {
	pbData := &pb.ExecuteSCResults{
		Results:     make([]*pb.ExecuteSCResult, len(er.results)),
		BlockNumber: er.blockNumber,
		GroupId:     er.groupId,
	}
	for i, v := range er.results {
		pbData.Results[i] = v.Proto().(*pb.ExecuteSCResult)
	}
	return pbData
}

func (er *ExecuteSCResults) FromProto(pbData *pb.ExecuteSCResults) {
	for _, v := range pbData.Results {
		executeResult := &ExecuteSCResult{}
		executeResult.FromProto(v)
		er.results = append(er.results, executeResult)
	}
	er.groupId = pbData.GroupId
	er.blockNumber = pbData.BlockNumber
}

func (er *ExecuteSCResults) String() string {
	str := ""
	for _, v := range er.results {
		str += v.String()
	}

	return str
}

func (er *ExecuteSCResults) GroupId() uint64 {
	return er.groupId
}

func (er *ExecuteSCResults) BlockNumber() uint64 {
	return er.blockNumber
}

func (er *ExecuteSCResults) Results() []types.ExecuteSCResult {
	return er.results
}

func HashExecuteSCResultsKeccak256(results []types.ExecuteSCResult) common.Hash {
	// Tạo một buffer để chứa dữ liệu đã serialize
	var buffer bytes.Buffer

	for _, result := range results {
		// Serialize mỗi ExecuteSCResult thành byte
		data, err := proto.MarshalOptions{Deterministic: true}.Marshal(result.Proto())
		if err != nil {
			fmt.Printf("Error marshaling ExecuteSCResult: %v\n", err)
			return common.Hash{} // hoặc xử lý lỗi khác nếu cần
		}
		// Thêm byte của mỗi ExecuteSCResult vào buffer
		buffer.Write(data)
	}

	// Tính hash Keccak256 cho toàn bộ buffer
	hashBytes := crypto.Keccak256(buffer.Bytes())
	return common.BytesToHash(hashBytes)
}
