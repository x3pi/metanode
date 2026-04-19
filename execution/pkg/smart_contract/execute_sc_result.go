package smart_contract

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

type ExecuteSCResult struct {
	transactionHash common.Hash
	status          pb.RECEIPT_STATUS
	exception       pb.EXCEPTION
	returnData      []byte
	gasUsed         uint64
	logsHash        common.Hash

	mapAddBalance     map[string][]byte
	mapSubBalance     map[string][]byte
	mapNonce          map[string][]byte
	mapStorageRoot    map[string][]byte
	mapCodeHash       map[string][]byte
	mapStorageAddress map[string]common.Address
	mapCreatorPubkey  map[string][]byte

	mapStorageAddressTouchedAddresses map[common.Address][]common.Address

	mapNativeSmartContractUpdateStorage map[common.Address][][2][]byte
	eventLogs                           []types.EventLog
}

func NewExecuteSCResult(
	transactionHash common.Hash,
	status pb.RECEIPT_STATUS,
	exception pb.EXCEPTION,
	rt []byte,
	gasUsed uint64,
	logsHash common.Hash,

	mapAddBalance map[string][]byte,
	mapSubBalance map[string][]byte,
	mapNonce map[string][]byte,
	mapCodeHash map[string][]byte,
	mapStorageRoot map[string][]byte,

	mapStorageAddress map[string]common.Address,
	mapCreatorPubkey map[string][]byte,

	mapStorageAddressTouchedAddresses map[common.Address][]common.Address,

	mapNativeSmartContractUpdateStorage map[common.Address][][2][]byte,

	eventLogs []types.EventLog,
) *ExecuteSCResult {
	rs := &ExecuteSCResult{
		transactionHash: transactionHash,
		status:          status,
		exception:       exception,
		returnData:      rt,
		gasUsed:         gasUsed,
		logsHash:        logsHash,

		mapAddBalance:  mapAddBalance,
		mapSubBalance:  mapSubBalance,
		mapNonce:       mapNonce,
		mapCodeHash:    mapCodeHash,
		mapStorageRoot: mapStorageRoot,

		mapStorageAddress: mapStorageAddress,
		mapCreatorPubkey:  mapCreatorPubkey,

		mapStorageAddressTouchedAddresses: mapStorageAddressTouchedAddresses,

		mapNativeSmartContractUpdateStorage: mapNativeSmartContractUpdateStorage,

		eventLogs: eventLogs,
	}

	return rs
}

func NewErrorExecuteSCResult(
	transactionHash common.Hash,
	status pb.RECEIPT_STATUS,
	exception pb.EXCEPTION,
	rt []byte,
) *ExecuteSCResult {
	rs := &ExecuteSCResult{
		transactionHash: transactionHash,
		status:          status,
		exception:       exception,
		returnData:      rt,
		gasUsed:         0,
	}

	return rs
}

// general
func (r *ExecuteSCResult) Unmarshal(b []byte) error {
	pbRequest := &pb.ExecuteSCResult{}
	err := proto.Unmarshal(b, pbRequest)
	if err != nil {
		return err
	}
	r.FromProto(pbRequest)
	return nil
}

func (r *ExecuteSCResult) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(r.Proto())
}

func (ex *ExecuteSCResult) String() string {
	str := fmt.Sprintf(`
	Transaction Hash: %v
	Add Balance Change:
	`,
		common.Bytes2Hex(ex.transactionHash[:]),
	)
	for i, v := range ex.mapAddBalance {
		str += fmt.Sprintf("%v: %v \n", i, uint256.NewInt(0).SetBytes(v))
	}
	str += fmt.Sprintln("Sub Balance Change: ")
	for i, v := range ex.mapSubBalance {
		str += fmt.Sprintf("%v: %v \n", i, uint256.NewInt(0).SetBytes(v))
	}
	str += fmt.Sprintln("Code Hash: ")
	for i, v := range ex.mapCodeHash {
		str += fmt.Sprintf("%v: %v \n", i, hex.EncodeToString(v))
	}
	str += fmt.Sprintln("Storage Root: ")
	for i, v := range ex.mapStorageRoot {
		str += fmt.Sprintf("%v: %v \n", i, hex.EncodeToString(v))
	}
	str += fmt.Sprintf(`
	Status: %v
	Exception: %v
	Return: %v
	GasUsed: %v
	`,
		ex.status,
		ex.exception,
		hex.EncodeToString(ex.returnData),
		ex.gasUsed,
	)
	return str
}

// getter
func (r *ExecuteSCResult) Proto() protoreflect.ProtoMessage {
	mapStorageAddressTouchedAddresses := make(map[string]*pb.TouchedAddresses)
	for k, v := range r.mapStorageAddressTouchedAddresses {
		touchedAddresses := &pb.TouchedAddresses{
			Addresses: make([][]byte, len(v)),
		}
		for i, a := range v {
			touchedAddresses.Addresses[i] = a.Bytes()
		}
		mapStorageAddressTouchedAddresses[hex.EncodeToString(k.Bytes())] = touchedAddresses
	}
	//
	mapStorageAddress := make(map[string][]byte, len(r.mapStorageAddress))
	for k, v := range r.mapStorageAddress {
		mapStorageAddress[k] = v.Bytes()
	}
	//
	mapNativeSmartContractUpdateStorage := make(
		map[string]*pb.StorageDatas,
		len(r.mapNativeSmartContractUpdateStorage),
	)
	for k, v := range r.mapNativeSmartContractUpdateStorage {
		datas := make([]*pb.StorageData, len(v))
		for i, vv := range v {
			datas[i] = &pb.StorageData{
				Key:   vv[0],
				Value: vv[1],
			}
		}
		mapNativeSmartContractUpdateStorage[k.Hex()] = &pb.StorageDatas{
			Datas: datas,
		}
	}
	eventLogs := make([]*pb.EventLog, len(r.eventLogs))
	for k, v := range r.eventLogs {
		eventLogs[k] = v.Proto()
	}

	protoData := &pb.ExecuteSCResult{
		TransactionHash: r.transactionHash.Bytes(),
		MapAddBalance:   r.mapAddBalance,
		MapSubBalance:   r.mapSubBalance,
		MapCodeHash:     r.mapCodeHash,
		MapStorageRoot:  r.mapStorageRoot,
		Status:          r.status,
		Exception:       r.exception,
		Return:          r.returnData,
		GasUsed:         r.gasUsed,

		MapStorageAddress: mapStorageAddress,
		MapCreatorPubkey:  r.mapCreatorPubkey,

		MapStorageAddressTouchedAddresses: mapStorageAddressTouchedAddresses,

		MapNativeSmartContractUpdateStorage: mapNativeSmartContractUpdateStorage,

		EventLogs: eventLogs,
	}
	return protoData
}

func (r *ExecuteSCResult) FromProto(pbData *pb.ExecuteSCResult) {
	r.transactionHash = common.BytesToHash(pbData.TransactionHash)
	r.mapAddBalance = pbData.MapAddBalance
	r.mapSubBalance = pbData.MapSubBalance
	r.mapCodeHash = pbData.MapCodeHash
	r.mapStorageRoot = pbData.MapStorageRoot
	r.status = pbData.Status
	r.exception = pbData.Exception
	r.returnData = pbData.Return
	r.gasUsed = pbData.GasUsed
	r.mapStorageAddressTouchedAddresses = make(map[common.Address][]common.Address)
	for k, v := range pbData.MapStorageAddressTouchedAddresses {
		address := common.HexToAddress(k)
		touchedAddresses := make([]common.Address, len(v.Addresses))
		for i, a := range v.Addresses {
			touchedAddresses[i] = common.BytesToAddress(a)
		}
		r.mapStorageAddressTouchedAddresses[address] = touchedAddresses
	}
	if len(pbData.MapCreatorPubkey) > 0 {
		r.mapStorageAddress = make(map[string]common.Address)
		for k, v := range pbData.MapStorageAddress {
			r.mapStorageAddress[k] = common.BytesToAddress(v)
		}
		r.mapCreatorPubkey = pbData.MapCreatorPubkey
	}
	r.mapNativeSmartContractUpdateStorage = make(
		map[common.Address][][2][]byte,
		len(pbData.MapNativeSmartContractUpdateStorage),
	)
	for k, v := range pbData.MapNativeSmartContractUpdateStorage {
		address := common.HexToAddress(k)
		r.mapNativeSmartContractUpdateStorage[address] = make([][2][]byte, len(v.Datas))
		for i, vv := range v.Datas {
			r.mapNativeSmartContractUpdateStorage[address][i] = [2][]byte{vv.Key, vv.Value}
		}
	}

	r.eventLogs = make([]types.EventLog, len(pbData.EventLogs))
	for idx, eventLog := range pbData.EventLogs {
		r.eventLogs[idx] = &EventLog{}
		r.eventLogs[idx].FromProto(eventLog)
	}

}

func (r *ExecuteSCResult) TransactionHash() common.Hash {
	return r.transactionHash
}

func (r *ExecuteSCResult) MapAddBalance() map[string][]byte {
	return r.mapAddBalance
}

func (r *ExecuteSCResult) MapSubBalance() map[string][]byte {
	return r.mapSubBalance
}

func (r *ExecuteSCResult) MapNonce() map[string][]byte {
	return r.mapNonce
}

func (r *ExecuteSCResult) MapStorageRoot() map[string][]byte {
	return r.mapStorageRoot
}

func (r *ExecuteSCResult) MapCodeHash() map[string][]byte {
	return r.mapCodeHash
}

func (r *ExecuteSCResult) MapStorageAddress() map[string]common.Address {
	return r.mapStorageAddress
}

func (r *ExecuteSCResult) MapCreatorPubkey() map[string][]byte {
	return r.mapCreatorPubkey
}

func (r *ExecuteSCResult) GasUsed() uint64 {
	return r.gasUsed
}

func (r *ExecuteSCResult) ReceiptStatus() pb.RECEIPT_STATUS {
	return r.status
}

func (r *ExecuteSCResult) Exception() pb.EXCEPTION {
	return r.exception
}

func (r *ExecuteSCResult) Return() []byte {
	return r.returnData
}

func (r *ExecuteSCResult) LogsHash() common.Hash {
	return r.logsHash
}

func (r *ExecuteSCResult) EventLogs() []types.EventLog {
	return r.eventLogs
}

func (r *ExecuteSCResult) MapStorageAddressTouchedAddresses() map[common.Address][]common.Address {
	return r.mapStorageAddressTouchedAddresses
}

func (r *ExecuteSCResult) MapNativeSmartContractUpdateStorage() map[common.Address][][2][]byte {
	return r.mapNativeSmartContractUpdateStorage
}

func ExecuteSCResultsFromProto(pbData []*pb.ExecuteSCResult) []types.ExecuteSCResult {
	results := make([]types.ExecuteSCResult, len(pbData))
	for i, v := range pbData {
		rs := &ExecuteSCResult{}
		rs.FromProto(v)
		results[i] = rs
	}
	return results
}

func ExecuteSCResultsToProto(results []types.ExecuteSCResult) []*pb.ExecuteSCResult {
	pbResults := make([]*pb.ExecuteSCResult, len(results))
	for i, v := range results {
		pbResults[i] = v.Proto().(*pb.ExecuteSCResult)
	}
	return pbResults
}

// Các giá trị byte và hash sẽ được biểu diễn dưới dạng chuỗi hex.
func (r *ExecuteSCResult) MarshalJSON() ([]byte, error) {

	mapAddBalanceStr := make(map[string]string, len(r.mapAddBalance))
	for k, v := range r.mapAddBalance {
		// Giả sử giá trị là uint256, chuyển thành chuỗi thập phân cho dễ đọc
		mapAddBalanceStr[k] = uint256.NewInt(0).SetBytes(v).String()
	}

	mapSubBalanceStr := make(map[string]string, len(r.mapSubBalance))
	for k, v := range r.mapSubBalance {
		// Giả sử giá trị là uint256, chuyển thành chuỗi thập phân
		mapSubBalanceStr[k] = uint256.NewInt(0).SetBytes(v).String()
	}

	mapNonceStr := make(map[string]string, len(r.mapNonce))
	for k, v := range r.mapNonce { // Giả sử giá trị là uint64 hoặc tương tự, chuyển thành chuỗi thập phân
		// Nếu là giá trị lớn hơn, có thể dùng uint256 như trên hoặc hex
		mapNonceStr[k] = uint256.NewInt(0).SetBytes(v).String() // Hoặc hex.EncodeToString(v) tùy ngữ cảnh
	}

	mapStorageRootStr := make(map[string]string, len(r.mapStorageRoot))
	for k, v := range r.mapStorageRoot {
		mapStorageRootStr[k] = hex.EncodeToString(v)
	}

	mapCodeHashStr := make(map[string]string, len(r.mapCodeHash))
	for k, v := range r.mapCodeHash {
		mapCodeHashStr[k] = hex.EncodeToString(v)
	}

	mapStorageAddressStr := make(map[string]string, len(r.mapStorageAddress))
	for k, v := range r.mapStorageAddress {
		mapStorageAddressStr[k] = v.Hex()
	}

	mapCreatorPubkeyStr := make(map[string]string, len(r.mapCreatorPubkey))
	for k, v := range r.mapCreatorPubkey {
		mapCreatorPubkeyStr[k] = hex.EncodeToString(v)
	}

	mapStorageAddressTouchedAddressesStr := make(map[string][]string, len(r.mapStorageAddressTouchedAddresses))
	for k, v := range r.mapStorageAddressTouchedAddresses {
		addrs := make([]string, len(v))
		for i, addr := range v {
			addrs[i] = addr.Hex()
		}
		mapStorageAddressTouchedAddressesStr[k.Hex()] = addrs
	}

	mapNativeSmartContractUpdateStorageStr := make(map[string][][2]string, len(r.mapNativeSmartContractUpdateStorage))
	for k, v := range r.mapNativeSmartContractUpdateStorage {
		pairs := make([][2]string, len(v))
		for i, pair := range v {
			pairs[i] = [2]string{hex.EncodeToString(pair[0]), hex.EncodeToString(pair[1])}
		}
		mapNativeSmartContractUpdateStorageStr[k.Hex()] = pairs
	}

	// Marshal các event logs. Giả sử types.EventLog có thể marshal thành JSON.
	// Nếu không, bạn cần xử lý tương tự như các map ở trên.
	// Ở đây, chúng ta sẽ cố gắng marshal trực tiếp. Nếu EventLog không có MarshalJSON,
	// bạn cần tạo một cấu trúc JSON cho nó và chuyển đổi thủ công.
	eventLogsJSON := make([]json.RawMessage, len(r.eventLogs))
	for i, log := range r.eventLogs {
		logBytes, err := json.Marshal(log) // Thử marshal log
		if err != nil {
			// Xử lý lỗi hoặc tạo biểu diễn JSON thủ công nếu cần
			// Ví dụ: return nil, fmt.Errorf("failed to marshal event log %d: %w", i, err)
			// Hoặc tạo một struct tạm thời với các trường hex
			// tempLog := struct{ Address string; Topics []string; Data string } { ... }
			// logBytes, err = json.Marshal(tempLog)
			// if err != nil { ... }
			return nil, fmt.Errorf("failed to marshal event log %d: %w", i, err) // Ví dụ xử lý lỗi đơn giản
		}
		eventLogsJSON[i] = logBytes
	}

	// Sử dụng một struct ẩn danh hoặc một struct được định nghĩa riêng
	// để chứa các giá trị đã được chuyển đổi sang định dạng JSON mong muốn.
	return json.Marshal(&struct {
		TransactionHash                     string                 `json:"transactionHash"`
		Status                              string                 `json:"status"`    // Sử dụng string representation
		Exception                           string                 `json:"exception"` // Sử dụng string representation
		ReturnData                          string                 `json:"returnData"`
		GasUsed                             uint64                 `json:"gasUsed"`
		LogsHash                            string                 `json:"logsHash"`
		MapAddBalance                       map[string]string      `json:"mapAddBalance"`
		MapSubBalance                       map[string]string      `json:"mapSubBalance"`
		MapNonce                            map[string]string      `json:"mapNonce"`
		MapStorageRoot                      map[string]string      `json:"mapStorageRoot"`
		MapCodeHash                         map[string]string      `json:"mapCodeHash"`
		MapStorageAddress                   map[string]string      `json:"mapStorageAddress"`
		MapCreatorPubkey                    map[string]string      `json:"mapCreatorPubkey"`
		MapStorageAddressTouchedAddresses   map[string][]string    `json:"mapStorageAddressTouchedAddresses"`
		MapNativeSmartContractUpdateStorage map[string][][2]string `json:"mapNativeSmartContractUpdateStorage"`
		EventLogs                           []json.RawMessage      `json:"eventLogs"` // Sử dụng RawMessage để giữ nguyên JSON của từng log
	}{
		TransactionHash:                     r.transactionHash.Hex(),
		Status:                              r.status.String(),
		Exception:                           r.exception.String(),
		ReturnData:                          hex.EncodeToString(r.returnData),
		GasUsed:                             r.gasUsed,
		LogsHash:                            r.logsHash.Hex(), // Chuyển đổi LogsHash
		MapAddBalance:                       mapAddBalanceStr,
		MapSubBalance:                       mapSubBalanceStr,
		MapNonce:                            mapNonceStr,
		MapStorageRoot:                      mapStorageRootStr,
		MapCodeHash:                         mapCodeHashStr,
		MapStorageAddress:                   mapStorageAddressStr,
		MapCreatorPubkey:                    mapCreatorPubkeyStr,
		MapStorageAddressTouchedAddresses:   mapStorageAddressTouchedAddressesStr,
		MapNativeSmartContractUpdateStorage: mapNativeSmartContractUpdateStorageStr,
		EventLogs:                           eventLogsJSON,
	})
}
func (r *ExecuteSCResult) PrintJSON() {
	jsonData, err := r.MarshalJSON()
	if err != nil {
		fmt.Printf("Error marshalling ExecuteSCResult to JSON: %v\n", err)
		return
	}
	fmt.Println(string(jsonData))
}
