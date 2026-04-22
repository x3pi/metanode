package smart_contract

import (
	"encoding/hex"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

type SmartContractUpdateData struct {
	code      []byte
	storage   map[string][]byte
	eventLogs []types.EventLog
}

func NewSmartContractUpdateData(
	code []byte,
	storage map[string][]byte,
	eventLogs []types.EventLog,
) *SmartContractUpdateData {
	return &SmartContractUpdateData{
		code:      code,
		storage:   storage,
		eventLogs: eventLogs,
	}
}

func (s *SmartContractUpdateData) Code() []byte {
	return s.code
}

func (s *SmartContractUpdateData) Storage() map[string][]byte {
	return s.storage
}

func (s *SmartContractUpdateData) EventLogs() []types.EventLog {
	return s.eventLogs
}

func (s *SmartContractUpdateData) CodeHash() common.Hash {
	return crypto.Keccak256Hash(s.code)
}

func (s *SmartContractUpdateData) SetCode(code []byte) {
	s.code = code
}

func (s *SmartContractUpdateData) UpdateStorage(storage map[string][]byte) {
	for k, v := range storage {
		s.storage[k] = v
	}
}

func (s *SmartContractUpdateData) AddEventLog(eventLog types.EventLog) {
	s.eventLogs = append(s.eventLogs, eventLog)
}

func (s *SmartContractUpdateData) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(s.Proto())
}

// general
func (s *SmartContractUpdateData) Unmarshal(b []byte) error {
	pbData := &pb.SmartContractUpdateData{}
	err := proto.Unmarshal(b, pbData)
	if err != nil {
		return err
	}
	s.FromProto(pbData)
	return nil
}

func (s *SmartContractUpdateData) FromProto(fbProto *pb.SmartContractUpdateData) {
	s.code = fbProto.Code
	s.storage = fbProto.Storage
	s.eventLogs = EventLogsFromProto(fbProto.EventLogs)
}

func (s *SmartContractUpdateData) Proto() *pb.SmartContractUpdateData {
	return &pb.SmartContractUpdateData{
		Code:      s.code,
		Storage:   s.storage,
		EventLogs: EventLogsToProto(s.eventLogs),
	}
}

func (s *SmartContractUpdateData) String() string {
	str := "SmartContractUpdateData\n"
	str += "Code: " + hex.EncodeToString(s.code) + "\n"
	str += "Storage: \n"
	for k, v := range s.storage {
		str += "  " + k + ": " + hex.EncodeToString(v) + "\n"
	}
	str += "EventLogs: \n"
	for _, v := range s.eventLogs {
		str += "  " + v.String() + "\n"
	}
	return str
}
