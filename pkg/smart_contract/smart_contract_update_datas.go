package smart_contract

import (
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

type SmartContractUpdateDatas struct {
	blockNumber uint64
	data        map[common.Address]types.SmartContractUpdateData
}

func NewSmartContractUpdateDatas(
	blockNumber uint64,
	data map[common.Address]types.SmartContractUpdateData,
) *SmartContractUpdateDatas {
	return &SmartContractUpdateDatas{
		blockNumber: blockNumber,
		data:        data,
	}
}

func (s *SmartContractUpdateDatas) Data() map[common.Address]types.SmartContractUpdateData {
	return s.data
}

func (s *SmartContractUpdateDatas) BlockNumber() uint64 {
	return s.blockNumber
}

func (s *SmartContractUpdateDatas) Proto() *pb.SmartContractUpdateDatas {
	data := make(map[string]*pb.SmartContractUpdateData, len(s.data))
	for i, v := range s.data {
		data[i.String()] = v.Proto()
	}
	return &pb.SmartContractUpdateDatas{
		BlockNumber: s.blockNumber,
		Data:        data,
	}
}

func (s *SmartContractUpdateDatas) FromProto(pbData *pb.SmartContractUpdateDatas) {
	s.data = make(map[common.Address]types.SmartContractUpdateData, len(pbData.Data))
	for i, v := range pbData.Data {
		addr := common.HexToAddress(i)
		data := &SmartContractUpdateData{}
		data.FromProto(v)
		s.data[addr] = data
	}
	s.blockNumber = pbData.BlockNumber
}

func (s *SmartContractUpdateDatas) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(s.Proto())
}

func (s *SmartContractUpdateDatas) Unmarshal(data []byte) error {
	pbData := &pb.SmartContractUpdateDatas{}
	if err := proto.Unmarshal(data, pbData); err != nil {
		return err
	}
	s.FromProto(pbData)
	return nil
}
