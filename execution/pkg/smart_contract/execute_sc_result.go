package smart_contract

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

type executeSCResultExt struct {
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
	mapFullDbLogs                       map[string][]byte

	mapStorageChange map[string]map[string][]byte

	mapPublicKeyBls map[string][]byte
	mapAccountType  map[string]uint8
	mapNewDeviceKey map[string][]byte

	mapCodeChange  map[string][]byte
	mapStorageRead map[string][][]byte
}

// ExecuteSCResult places its pointer-bearing fields (ext and returnData) at the start of the struct
// so the Go GC only scans the first 16 bytes (ptrdata = 16) and leaves the remaining 96 value bytes unscanned.
type ExecuteSCResult struct {
	ext             *executeSCResultExt
	returnData      []byte
	transactionHash common.Hash
	logsHash        common.Hash
	gasUsed         uint64
	status          pb.RECEIPT_STATUS
	exception       pb.EXCEPTION
}

func (rs *ExecuteSCResult) ensureExt() *executeSCResultExt {
	if rs.ext == nil {
		rs.ext = &executeSCResultExt{}
	}
	return rs.ext
}

func (rs *ExecuteSCResult) MapCodeChange() map[string][]byte {
	if rs.ext == nil {
		return nil
	}
	return rs.ext.mapCodeChange
}
func (rs *ExecuteSCResult) SetMapCodeChange(m map[string][]byte) {
	if m == nil && rs.ext == nil {
		return
	}
	rs.ensureExt().mapCodeChange = m
}

func (rs *ExecuteSCResult) MapPublicKeyBls() map[string][]byte {
	if rs.ext == nil {
		return nil
	}
	return rs.ext.mapPublicKeyBls
}
func (rs *ExecuteSCResult) SetMapPublicKeyBls(m map[string][]byte) {
	if m == nil && rs.ext == nil {
		return
	}
	rs.ensureExt().mapPublicKeyBls = m
}

func (rs *ExecuteSCResult) MapAccountType() map[string]uint8 {
	if rs.ext == nil {
		return nil
	}
	return rs.ext.mapAccountType
}
func (rs *ExecuteSCResult) SetMapAccountType(m map[string]uint8) {
	if m == nil && rs.ext == nil {
		return
	}
	rs.ensureExt().mapAccountType = m
}

func (rs *ExecuteSCResult) MapNewDeviceKey() map[string][]byte {
	if rs.ext == nil {
		return nil
	}
	return rs.ext.mapNewDeviceKey
}
func (rs *ExecuteSCResult) SetMapNewDeviceKey(m map[string][]byte) {
	if m == nil && rs.ext == nil {
		return
	}
	rs.ensureExt().mapNewDeviceKey = m
}

func (rs *ExecuteSCResult) MapStorageChange() map[string]map[string][]byte {
	if rs.ext == nil {
		return nil
	}
	return rs.ext.mapStorageChange
}
func (rs *ExecuteSCResult) SetMapStorageChange(m map[string]map[string][]byte) {
	if m == nil && rs.ext == nil {
		return
	}
	rs.ensureExt().mapStorageChange = m
}

func (rs *ExecuteSCResult) MapStorageRead() map[string][][]byte {
	if rs.ext == nil {
		return nil
	}
	return rs.ext.mapStorageRead
}
func (rs *ExecuteSCResult) SetMapStorageRead(m map[string][][]byte) {
	if m == nil && rs.ext == nil {
		return
	}
	rs.ensureExt().mapStorageRead = m
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
	}
	if mapAddBalance != nil ||
		mapSubBalance != nil ||
		mapNonce != nil ||
		mapCodeHash != nil ||
		mapStorageRoot != nil ||
		mapStorageAddress != nil ||
		mapCreatorPubkey != nil ||
		mapStorageAddressTouchedAddresses != nil ||
		mapNativeSmartContractUpdateStorage != nil ||
		eventLogs != nil {
		rs.ext = &executeSCResultExt{
			mapAddBalance:                       mapAddBalance,
			mapSubBalance:                       mapSubBalance,
			mapNonce:                            mapNonce,
			mapCodeHash:                         mapCodeHash,
			mapStorageRoot:                      mapStorageRoot,
			mapStorageAddress:                   mapStorageAddress,
			mapCreatorPubkey:                    mapCreatorPubkey,
			mapStorageAddressTouchedAddresses:   mapStorageAddressTouchedAddresses,
			mapNativeSmartContractUpdateStorage: mapNativeSmartContractUpdateStorage,
			eventLogs:                           eventLogs,
		}
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
	for i, v := range ex.MapAddBalance() {
		str += fmt.Sprintf("%v: %v \n", i, uint256.NewInt(0).SetBytes(v))
	}
	str += fmt.Sprintln("Sub Balance Change: ")
	for i, v := range ex.MapSubBalance() {
		str += fmt.Sprintf("%v: %v \n", i, uint256.NewInt(0).SetBytes(v))
	}
	str += fmt.Sprintln("Code Hash: ")
	for i, v := range ex.MapCodeHash() {
		str += fmt.Sprintf("%v: %v \n", i, hex.EncodeToString(v))
	}
	str += fmt.Sprintln("Storage Root: ")
	for i, v := range ex.MapStorageRoot() {
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
	if r.ext == nil {
		return &pb.ExecuteSCResult{
			TransactionHash: r.transactionHash.Bytes(),
			Status:          r.status,
			Exception:       r.exception,
			Return:          r.returnData,
			GasUsed:         r.gasUsed,
		}
	}
	ext := r.ext
	mapStorageAddressTouchedAddresses := make(map[string]*pb.TouchedAddresses)
	for k, v := range ext.mapStorageAddressTouchedAddresses {
		touchedAddresses := &pb.TouchedAddresses{
			Addresses: make([][]byte, len(v)),
		}
		for i, a := range v {
			touchedAddresses.Addresses[i] = a.Bytes()
		}
		mapStorageAddressTouchedAddresses[hex.EncodeToString(k.Bytes())] = touchedAddresses
	}
	//
	mapStorageAddress := make(map[string][]byte, len(ext.mapStorageAddress))
	for k, v := range ext.mapStorageAddress {
		mapStorageAddress[k] = v.Bytes()
	}
	//
	mapNativeSmartContractUpdateStorage := make(
		map[string]*pb.StorageDatas,
		len(ext.mapNativeSmartContractUpdateStorage),
	)
	for k, v := range ext.mapNativeSmartContractUpdateStorage {
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

	mapStorageChange := make(
		map[string]*pb.StorageDatas,
		len(ext.mapStorageChange),
	)
	for k, v := range ext.mapStorageChange {
		datas := make([]*pb.StorageData, 0, len(v))
		keys := make([]string, 0, len(v))
		for k2 := range v {
			keys = append(keys, k2)
		}
		sort.Strings(keys)
		for _, k2 := range keys {
			datas = append(datas, &pb.StorageData{
				Key:   []byte(k2),
				Value: v[k2],
			})
		}
		mapStorageChange[k] = &pb.StorageDatas{
			Datas: datas,
		}
	}

	mapAccountType := make(map[string]uint32, len(ext.mapAccountType))
	for k, v := range ext.mapAccountType {
		mapAccountType[k] = uint32(v)
	}

	eventLogs := make([]*pb.EventLog, len(ext.eventLogs))
	for k, v := range ext.eventLogs {
		eventLogs[k] = v.Proto()
	}

	protoData := &pb.ExecuteSCResult{
		TransactionHash: r.transactionHash.Bytes(),
		MapAddBalance:   ext.mapAddBalance,
		MapSubBalance:   ext.mapSubBalance,
		MapCodeHash:     ext.mapCodeHash,
		MapStorageRoot:  ext.mapStorageRoot,
		Status:          r.status,
		Exception:       r.exception,
		Return:          r.returnData,
		GasUsed:         r.gasUsed,

		MapStorageAddress: mapStorageAddress,
		MapCreatorPubkey:  ext.mapCreatorPubkey,

		MapStorageAddressTouchedAddresses: mapStorageAddressTouchedAddresses,

		MapNativeSmartContractUpdateStorage: mapNativeSmartContractUpdateStorage,

		EventLogs: eventLogs,

		MapNonce:         ext.mapNonce,
		MapFullDbLogs:    ext.mapFullDbLogs,
		MapStorageChange: mapStorageChange,
		MapPublicKeyBls:  ext.mapPublicKeyBls,
		MapAccountType:   mapAccountType,
		MapNewDeviceKey:  ext.mapNewDeviceKey,
		MapCodeChange:    ext.mapCodeChange,
	}
	return protoData
}

func (r *ExecuteSCResult) FromProto(pbData *pb.ExecuteSCResult) {
	r.transactionHash = common.BytesToHash(pbData.TransactionHash)
	r.status = pbData.Status
	r.exception = pbData.Exception
	r.returnData = pbData.Return
	r.gasUsed = pbData.GasUsed

	hasExt := len(pbData.MapAddBalance) > 0 ||
		len(pbData.MapSubBalance) > 0 ||
		len(pbData.MapCodeHash) > 0 ||
		len(pbData.MapStorageRoot) > 0 ||
		len(pbData.MapStorageAddressTouchedAddresses) > 0 ||
		len(pbData.MapCreatorPubkey) > 0 ||
		len(pbData.MapNativeSmartContractUpdateStorage) > 0 ||
		len(pbData.EventLogs) > 0 ||
		len(pbData.MapNonce) > 0 ||
		len(pbData.MapFullDbLogs) > 0 ||
		len(pbData.MapPublicKeyBls) > 0 ||
		len(pbData.MapNewDeviceKey) > 0 ||
		len(pbData.MapCodeChange) > 0 ||
		len(pbData.MapAccountType) > 0 ||
		len(pbData.MapStorageChange) > 0
	if !hasExt {
		r.ext = nil
		return
	}

	ext := r.ensureExt()
	ext.mapAddBalance = pbData.MapAddBalance
	ext.mapSubBalance = pbData.MapSubBalance
	ext.mapCodeHash = pbData.MapCodeHash
	ext.mapStorageRoot = pbData.MapStorageRoot
	ext.mapStorageAddressTouchedAddresses = make(map[common.Address][]common.Address)
	for k, v := range pbData.MapStorageAddressTouchedAddresses {
		address := common.HexToAddress(k)
		touchedAddresses := make([]common.Address, len(v.Addresses))
		for i, a := range v.Addresses {
			touchedAddresses[i] = common.BytesToAddress(a)
		}
		ext.mapStorageAddressTouchedAddresses[address] = touchedAddresses
	}
	if len(pbData.MapCreatorPubkey) > 0 {
		ext.mapStorageAddress = make(map[string]common.Address)
		for k, v := range pbData.MapStorageAddress {
			ext.mapStorageAddress[k] = common.BytesToAddress(v)
		}
		ext.mapCreatorPubkey = pbData.MapCreatorPubkey
	}
	ext.mapNativeSmartContractUpdateStorage = make(
		map[common.Address][][2][]byte,
		len(pbData.MapNativeSmartContractUpdateStorage),
	)
	for k, v := range pbData.MapNativeSmartContractUpdateStorage {
		address := common.HexToAddress(k)
		ext.mapNativeSmartContractUpdateStorage[address] = make([][2][]byte, len(v.Datas))
		for i, vv := range v.Datas {
			ext.mapNativeSmartContractUpdateStorage[address][i] = [2][]byte{vv.Key, vv.Value}
		}
	}

	ext.eventLogs = make([]types.EventLog, len(pbData.EventLogs))
	for idx, eventLog := range pbData.EventLogs {
		ext.eventLogs[idx] = &EventLog{}
		ext.eventLogs[idx].FromProto(eventLog)
	}

	ext.mapNonce = pbData.MapNonce
	ext.mapFullDbLogs = pbData.MapFullDbLogs
	ext.mapPublicKeyBls = pbData.MapPublicKeyBls
	ext.mapNewDeviceKey = pbData.MapNewDeviceKey
	ext.mapCodeChange = pbData.MapCodeChange

	if len(pbData.MapAccountType) > 0 {
		ext.mapAccountType = make(map[string]uint8, len(pbData.MapAccountType))
		for k, v := range pbData.MapAccountType {
			ext.mapAccountType[k] = uint8(v)
		}
	}

	if len(pbData.MapStorageChange) > 0 {
		ext.mapStorageChange = make(map[string]map[string][]byte, len(pbData.MapStorageChange))
		for k, v := range pbData.MapStorageChange {
			inner := make(map[string][]byte, len(v.Datas))
			for _, data := range v.Datas {
				inner[string(data.Key)] = data.Value
			}
			ext.mapStorageChange[k] = inner
		}
	}
}

func (r *ExecuteSCResult) TransactionHash() common.Hash {
	return r.transactionHash
}

func (r *ExecuteSCResult) MapAddBalance() map[string][]byte {
	if r.ext == nil {
		return nil
	}
	return r.ext.mapAddBalance
}

func (r *ExecuteSCResult) SetMapAddBalance(m map[string][]byte) {
	if m == nil && r.ext == nil {
		return
	}
	r.ensureExt().mapAddBalance = m
}

func (r *ExecuteSCResult) MapSubBalance() map[string][]byte {
	if r.ext == nil {
		return nil
	}
	return r.ext.mapSubBalance
}

func (r *ExecuteSCResult) SetMapSubBalance(m map[string][]byte) {
	if m == nil && r.ext == nil {
		return
	}
	r.ensureExt().mapSubBalance = m
}

func (r *ExecuteSCResult) MapNonce() map[string][]byte {
	if r.ext == nil {
		return nil
	}
	return r.ext.mapNonce
}

func (r *ExecuteSCResult) SetMapNonce(m map[string][]byte) {
	if m == nil && r.ext == nil {
		return
	}
	r.ensureExt().mapNonce = m
}

func (r *ExecuteSCResult) MapStorageRoot() map[string][]byte {
	if r.ext == nil {
		return nil
	}
	return r.ext.mapStorageRoot
}

func (r *ExecuteSCResult) MapCodeHash() map[string][]byte {
	if r.ext == nil {
		return nil
	}
	return r.ext.mapCodeHash
}

func (r *ExecuteSCResult) MapStorageAddress() map[string]common.Address {
	if r.ext == nil {
		return nil
	}
	return r.ext.mapStorageAddress
}

func (r *ExecuteSCResult) MapCreatorPubkey() map[string][]byte {
	if r.ext == nil {
		return nil
	}
	return r.ext.mapCreatorPubkey
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
	if r.ext == nil {
		return nil
	}
	return r.ext.eventLogs
}

func (r *ExecuteSCResult) MapStorageAddressTouchedAddresses() map[common.Address][]common.Address {
	if r.ext == nil {
		return nil
	}
	return r.ext.mapStorageAddressTouchedAddresses
}

func (r *ExecuteSCResult) MapNativeSmartContractUpdateStorage() map[common.Address][][2][]byte {
	if r.ext == nil {
		return nil
	}
	return r.ext.mapNativeSmartContractUpdateStorage
}

func (r *ExecuteSCResult) MapFullDbLogs() map[string][]byte {
	if r.ext == nil {
		return nil
	}
	return r.ext.mapFullDbLogs
}

func (r *ExecuteSCResult) SetMapFullDbLogs(logs map[string][]byte) {
	if logs == nil && r.ext == nil {
		return
	}
	r.ensureExt().mapFullDbLogs = logs
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

	mapAddBalanceStr := make(map[string]string, len(r.MapAddBalance()))
	for k, v := range r.MapAddBalance() {
		// Giả sử giá trị là uint256, chuyển thành chuỗi thập phân cho dễ đọc
		mapAddBalanceStr[k] = uint256.NewInt(0).SetBytes(v).String()
	}

	mapSubBalanceStr := make(map[string]string, len(r.MapSubBalance()))
	for k, v := range r.MapSubBalance() {
		// Giả sử giá trị là uint256, chuyển thành chuỗi thập phân
		mapSubBalanceStr[k] = uint256.NewInt(0).SetBytes(v).String()
	}

	mapNonceStr := make(map[string]string, len(r.MapNonce()))
	for k, v := range r.MapNonce() { // Giả sử giá trị là uint64 hoặc tương tự, chuyển thành chuỗi thập phân
		// Nếu là giá trị lớn hơn, có thể dùng uint256 như trên hoặc hex
		mapNonceStr[k] = uint256.NewInt(0).SetBytes(v).String() // Hoặc hex.EncodeToString(v) tùy ngữ cảnh
	}

	mapStorageRootStr := make(map[string]string, len(r.MapStorageRoot()))
	for k, v := range r.MapStorageRoot() {
		mapStorageRootStr[k] = hex.EncodeToString(v)
	}

	mapCodeHashStr := make(map[string]string, len(r.MapCodeHash()))
	for k, v := range r.MapCodeHash() {
		mapCodeHashStr[k] = hex.EncodeToString(v)
	}

	mapStorageAddressStr := make(map[string]string, len(r.MapStorageAddress()))
	for k, v := range r.MapStorageAddress() {
		mapStorageAddressStr[k] = v.Hex()
	}

	mapCreatorPubkeyStr := make(map[string]string, len(r.MapCreatorPubkey()))
	for k, v := range r.MapCreatorPubkey() {
		mapCreatorPubkeyStr[k] = hex.EncodeToString(v)
	}

	mapStorageAddressTouchedAddressesStr := make(map[string][]string, len(r.MapStorageAddressTouchedAddresses()))
	for k, v := range r.MapStorageAddressTouchedAddresses() {
		addrs := make([]string, len(v))
		for i, addr := range v {
			addrs[i] = addr.Hex()
		}
		mapStorageAddressTouchedAddressesStr[k.Hex()] = addrs
	}

	mapNativeSmartContractUpdateStorageStr := make(map[string][][2]string, len(r.MapNativeSmartContractUpdateStorage()))
	for k, v := range r.MapNativeSmartContractUpdateStorage() {
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
	evLogs := r.EventLogs()
	eventLogsJSON := make([]json.RawMessage, len(evLogs))
	for i, log := range evLogs {
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
