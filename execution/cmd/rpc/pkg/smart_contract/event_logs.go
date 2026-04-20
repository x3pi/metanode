package smart_contract

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/ethereum/go-ethereum/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

type EventLogs struct {
	proto *pb.EventLogs
}

func NewEventLogs(eventLogs []types.EventLog) types.EventLogs {
	pbEventLogs := make([]*pb.EventLog, len(eventLogs))
	for idx, eventLog := range eventLogs {
		pbEventLogs[idx] = eventLog.Proto()
	}
	return &EventLogs{
		proto: &pb.EventLogs{
			EventLogs: pbEventLogs,
		},
	}
}

// general
func (l *EventLogs) FromProto(logPb *pb.EventLogs) {
	l.proto = logPb
}

func (l *EventLogs) Unmarshal(b []byte) error {
	logsPb := &pb.EventLogs{}
	err := proto.Unmarshal(b, logsPb)
	if err != nil {
		return err
	}
	l.FromProto(logsPb)
	return nil
}

func (l *EventLogs) Marshal() ([]byte, error) {
	return proto.Marshal(l.proto)
}

func (l *EventLogs) Proto() *pb.EventLogs {
	return l.proto
}

// getter
func (l *EventLogs) EventLogList() []types.EventLog {
	eventLogsPb := l.proto.EventLogs
	eventLogList := make([]types.EventLog, len(eventLogsPb))
	for idx, eventLog := range eventLogsPb {
		eventLogList[idx] = &EventLog{}
		eventLogList[idx].FromProto(eventLog)
	}
	return eventLogList
}

func (l *EventLogs) Copy() types.EventLogs {
	cp := &EventLogs{}
	cp.FromProto(proto.Clone(l.proto).(*pb.EventLogs))
	return cp
}

// MarshalEventLogs nhận vào một slice của types.EventLog và mã hóa chúng thành một byte slice.
func MarshalEventLogs(eventLogs []types.EventLog) ([]byte, error) {
	// Tạo một đối tượng protobuf EventLogs để chứa danh sách
	pbEventLogs := &pb.EventLogs{
		EventLogs: make([]*pb.EventLog, len(eventLogs)),
	}

	// Lặp qua từng event log trong slice đầu vào
	for i, el := range eventLogs {
		// Chuyển đổi từ kiểu Go (types.EventLog) sang kiểu protobuf (*pb.EventLog)
		if eventLog, ok := el.(*EventLog); ok {
			// Không cần type assertion vì eventLog.Proto() đã trả về *pb.EventLog
			pbEventLog := eventLog.Proto()
			pbEventLogs.EventLogs[i] = pbEventLog
		} else {
			return nil, fmt.Errorf("phần tử tại chỉ số %d không phải là *EventLog", i)
		}
	}

	// Mã hóa đối tượng protobuf EventLogs thành byte slice
	return proto.Marshal(pbEventLogs)
}

// UnmarshalEventLogs nhận vào một byte slice và giải mã nó thành một slice của types.EventLog.
func UnmarshalEventLogs(data []byte) ([]types.EventLog, error) {
	pbEventLogs := &pb.EventLogs{}
	if err := proto.Unmarshal(data, pbEventLogs); err != nil {
		return nil, err
	}

	eventLogs := make([]types.EventLog, len(pbEventLogs.EventLogs))
	for i, r := range pbEventLogs.EventLogs {
		eventLogs[i] = NewEventLogFromProto(r)
	}
	return eventLogs, nil
}

// HÀM MỚI: MarshalMapEventLogs mã hóa một map[common.Address][]types.EventLog thành []byte.
func MarshalMapEventLogs(mapEvents map[common.Address][]types.EventLog) ([]byte, error) {
	pbMap := &pb.MapEventLogs{
		EventsByAddress: make(map[string]*pb.EventLogs, len(mapEvents)),
	}

	for addr, logs := range mapEvents {
		// Tạo đối tượng pb.EventLogs cho mỗi danh sách
		pbLogs := &pb.EventLogs{
			EventLogs: make([]*pb.EventLog, len(logs)),
		}
		// Chuyển đổi từng event log
		for i, log := range logs {
			if l, ok := log.(*EventLog); ok {
				pbLogs.EventLogs[i] = l.Proto()
			} else {
				return nil, fmt.Errorf("không thể chuyển đổi event log cho địa chỉ %s", addr.Hex())
			}
		}
		// Dùng địa chỉ dạng hex làm key cho map protobuf
		pbMap.EventsByAddress[addr.Hex()] = pbLogs
	}

	return proto.Marshal(pbMap)
}

// HÀM MỚI: UnmarshalMapEventLogs giải mã []byte trở lại thành map[common.Address][]types.EventLog.
func UnmarshalMapEventLogs(data []byte) (map[common.Address][]types.EventLog, error) {
	pbMap := &pb.MapEventLogs{}
	if err := proto.Unmarshal(data, pbMap); err != nil {
		return nil, fmt.Errorf("lỗi giải mã MapEventLogs: %w", err)
	}

	resultMap := make(map[common.Address][]types.EventLog, len(pbMap.EventsByAddress))

	for hexAddr, pbLogs := range pbMap.EventsByAddress {
		// Chuyển đổi key từ hex string trở lại common.Address
		addr := common.HexToAddress(hexAddr)

		logs := make([]types.EventLog, len(pbLogs.EventLogs))
		for i, pbLog := range pbLogs.EventLogs {
			logs[i] = NewEventLogFromProto(pbLog)
		}
		resultMap[addr] = logs
	}

	return resultMap, nil
}
