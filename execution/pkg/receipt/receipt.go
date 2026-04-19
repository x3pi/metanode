package receipt

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

type Receipt struct {
	proto *pb.Receipt
}

func NewReceipt(
	transactionHash common.Hash,
	fromAddress common.Address,
	toAddress common.Address,
	amount *big.Int,
	status pb.RECEIPT_STATUS,
	returnValue []byte,
	exception pb.EXCEPTION,
	gastFee uint64,
	gasUsed uint64,
	eventLogs []types.EventLog,
	transactionIndex uint64,
	blockHash common.Hash,
	blockNumber uint64,
) types.Receipt {
	pbEventlogs := make([]*pb.EventLog, len(eventLogs))
	for idx, eventLog := range eventLogs {
		pbEventlogs[idx] = eventLog.Proto()
	}
	saveStatus := status
	saveReturn := returnValue
	if status != pb.RECEIPT_STATUS_RETURNED {
		saveStatus = pb.RECEIPT_STATUS_TRANSACTION_ERROR

		if status == pb.RECEIPT_STATUS_HALTED {
			saveReturn = utils.EncodeRevertReason("transaction halted")
		}
		if exception == pb.EXCEPTION_ERR_INVALID_CODE {
			saveReturn = utils.EncodeRevertReason("transaction error invalid code")
		}
	}
	proto := &pb.Receipt{
		TransactionHash:  transactionHash.Bytes(),
		FromAddress:      fromAddress.Bytes(),
		ToAddress:        toAddress.Bytes(),
		Amount:           amount.Bytes(),
		Status:           saveStatus,
		Return:           saveReturn,
		Exception:        exception,
		GasUsed:          gasUsed,
		GasFee:           gastFee,
		EventLogs:        pbEventlogs,
		TransactionIndex: transactionIndex,
	}
	return ReceiptFromProto(proto)
}

// setter
func (r *Receipt) SetProcessingType(processingType pb.RECEIPT_PROCESSING_TYPE) {
	r.proto.ProcessingType = processingType
}

func (r *Receipt) ProcessingType() pb.RECEIPT_PROCESSING_TYPE {
	return r.proto.ProcessingType
}

func ReceiptFromProto(proto *pb.Receipt) types.Receipt {
	return &Receipt{
		proto: proto,
	}
}
func (r *Receipt) ToTypes() types.Receipt {
	return &Receipt{
		proto: r.proto,
	}
}

func (r *Receipt) SetRHash(rHash common.Hash) {
	r.proto.RHash = rHash.Bytes()
}

func (r *Receipt) RHash() common.Hash {
	return common.BytesToHash(r.proto.RHash)
}

// general
func (r *Receipt) FromProto(proto *pb.Receipt) {
	r.proto = proto
}

func (r *Receipt) Unmarshal(b []byte) error {
	receiptPb := &pb.Receipt{}
	err := proto.Unmarshal(b, receiptPb)
	if err != nil {
		return err
	}
	r.proto = receiptPb
	return nil
}

func (r *Receipt) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(r.proto)
}

func (r *Receipt) Proto() protoreflect.ProtoMessage {
	return r.proto
}

func (r *Receipt) String() string {
	str := fmt.Sprintf(`
	Transaction hash: %v
	From address: %v
	To address: %v
	Amount: %v
	Status: %v
	Return: %v
	Exception: %v
	GasUsed: %v
	GasFee: %v
	TransactionIndex: %v
	EventLogs: %v
	RHash: %v
`,
		common.BytesToHash(r.proto.TransactionHash),
		common.BytesToAddress(r.proto.FromAddress),
		common.BytesToAddress(r.proto.ToAddress),
		uint256.NewInt(0).SetBytes(r.proto.Amount),
		r.proto.Status,
		common.Bytes2Hex(r.proto.Return),
		r.proto.Exception,
		r.proto.GasUsed,
		r.proto.GasFee,
		r.proto.TransactionIndex,
		r.proto.GetEventLogs(),
		common.BytesToHash(r.proto.RHash),
	)
	return str
}
func (r *Receipt) Exception() pb.EXCEPTION {
	return r.proto.Exception
}

// getter
func (r *Receipt) TransactionHash() common.Hash {
	return common.BytesToHash(r.proto.TransactionHash)
}

func (r *Receipt) FromAddress() common.Address {
	return common.BytesToAddress(r.proto.FromAddress)
}

func (r *Receipt) ToAddress() common.Address {
	return common.BytesToAddress(r.proto.ToAddress)
}

func (r *Receipt) SetToAddress(toAddress common.Address) {
	r.proto.ToAddress = toAddress.Bytes()
}

func (r *Receipt) GasUsed() uint64 {
	return r.proto.GasUsed
}

func (r *Receipt) GasFee() uint64 {
	return r.proto.GasFee
}

func (r *Receipt) Amount() *big.Int {
	return big.NewInt(0).SetBytes(r.proto.Amount)
}

func (r *Receipt) Return() []byte {
	return r.proto.Return
}

func (r *Receipt) SetReturn(data []byte) {
	r.proto.Return = data
}

func (r *Receipt) Status() pb.RECEIPT_STATUS {
	return r.proto.Status
}

func (r *Receipt) EventLogs() []*pb.EventLog {
	return r.proto.EventLogs
}

func (r *Receipt) TransactionIndex() uint64 {
	return r.proto.TransactionIndex
}

// setter
func (r *Receipt) UpdateExecuteResult(
	status pb.RECEIPT_STATUS,
	returnValue []byte,
	exception pb.EXCEPTION,
	gasUsed uint64,
	eventLogs []types.EventLog,
) {
	saveStatus := status
	saveReturn := returnValue
	if status != pb.RECEIPT_STATUS_RETURNED {
		saveStatus = pb.RECEIPT_STATUS_TRANSACTION_ERROR

		if status == pb.RECEIPT_STATUS_HALTED {
			saveReturn = utils.EncodeRevertReason("transaction halted")
		}
		if exception == pb.EXCEPTION_ERR_INVALID_CODE {
			saveReturn = utils.EncodeRevertReason("transaction error invalid code")
		}
		// FORK-SAFETY: Always normalize ERR_ADDRESS_NOT_IN_RELATED return value
		// The EVM returns an error string containing the local MVM address (mvmId),
		// which differs between nodes due to different transaction grouping.
		// Using a fixed string ensures deterministic receipt bytes across all nodes.
		if exception == pb.EXCEPTION_ERR_ADDRESS_NOT_IN_RELATED {
			saveReturn = utils.EncodeRevertReason("address not in related addresses")
		}
	}

	r.proto.Status = saveStatus
	r.proto.Return = saveReturn
	r.proto.Exception = exception
	r.proto.GasUsed = gasUsed
	pbEventlogs := make([]*pb.EventLog, len(eventLogs))
	for idx, eventLog := range eventLogs {
		pbEventlogs[idx] = eventLog.Proto()
	}
	r.proto.EventLogs = pbEventlogs
	if exception == pb.EXCEPTION_ERR_ADDRESS_NOT_IN_RELATED {
		logger.Info("_________________Receipt updated", "receipt", r.String())
	}
}

func (r *Receipt) Json() ([]byte, error) {
	mapReceipt := map[string]interface{}{
		"transaction_hash":  hex.EncodeToString(r.TransactionHash().Bytes()),
		"from_address":      hex.EncodeToString(r.FromAddress().Bytes()),
		"to_address":        hex.EncodeToString(r.ToAddress().Bytes()),
		"amount":            hex.EncodeToString(r.Amount().Bytes()),
		"status":            r.Status().Enum(),
		"return_value":      hex.EncodeToString(r.Return()),
		"exception":         r.proto.Exception.Enum(),
		"gas_fee":           r.proto.GasFee,
		"gas_used":          r.GasUsed(),
		"transaction_index": r.proto.TransactionIndex,
	}
	return json.Marshal(mapReceipt)
}

func (r *Receipt) MarshalReceiptToMap() (map[string]interface{}, error) {
	mapReceipt := map[string]interface{}{
		"transaction_hash":  hex.EncodeToString(r.TransactionHash().Bytes()),
		"from_address":      hex.EncodeToString(r.FromAddress().Bytes()),
		"to_address":        hex.EncodeToString(r.ToAddress().Bytes()),
		"amount":            hex.EncodeToString(r.Amount().Bytes()),
		"status":            r.Status().Number(),
		"return_value":      hex.EncodeToString(r.Return()),
		"exception":         r.proto.Exception.Enum(),
		"gas_fee":           r.proto.GasFee,
		"gas_used":          r.GasUsed(),
		"event_logs":        r.EventLogs(),
		"transaction_index": r.proto.TransactionIndex,
	}
	return mapReceipt, nil
}

func ReceiptsToProto(receipts []types.Receipt) []*pb.Receipt {
	protoReceipts := make([]*pb.Receipt, len(receipts))
	for i, receipt := range receipts {
		protoReceipts[i] = receipt.Proto().(*pb.Receipt)
	}
	return protoReceipts
}

func ProtoToReceipts(protoReceipts []*pb.Receipt) []types.Receipt {
	receipts := make([]types.Receipt, len(protoReceipts))
	for i, protoReceipt := range protoReceipts {
		receipts[i] = ReceiptFromProto(protoReceipt)
	}
	return receipts
}

func SaveReceiptToFile(receipt types.Receipt, path string) error {
	// Ensure the directory exists
	dir := filepath.Dir(path)
	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		logger.Error("Failed to create directory", err)
		return err // Return the error here
	}

	raw, err := receipt.Marshal()
	if err != nil {
		return err
	}

	return os.WriteFile(path, raw, 0644)
}

func LoadReceiptFromFile(path string) (types.Receipt, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		logger.Error("Error reading file:", err)
		return nil, err
	}

	receipt := &Receipt{}
	err = receipt.Unmarshal(raw)
	if err != nil {
		logger.Error("Error unmarshalling receipt:", err)
		return nil, err
	}

	return receipt, nil
}

func LoadReceiptByHash(receiptsDataDir string, hash common.Hash) (types.Receipt, error) {
	// Nếu không tìm thấy trong trie, hãy thử tìm trong file
	receipt, err := LoadReceiptFromFile(fmt.Sprintf("%v%v.dat", receiptsDataDir, hash))
	if err != nil {
		return nil, ErrorReceiptNotFound // Trả về lỗi nếu không tìm thấy trong cả trie và file
	}
	return receipt, nil
}

// MarshalJSON implements the json.Marshaler interface.
// This method defines how a Receipt object is converted to JSON.
func (r *Receipt) MarshalJSON() ([]byte, error) {
	return r.Json()
}
