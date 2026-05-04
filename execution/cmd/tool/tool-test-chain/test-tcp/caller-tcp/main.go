//go:build ignore

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp/config"
	tx_helper "github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp/utils/tx_helper"
	"github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/pkg/models/tx_models"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

type ExpectedEvent struct {
	Name     string   `json:"name"`
	Contains []string `json:"contains"`
}

type DataPayload struct {
	Contract  string        `json:"contract"`
	AbiPath   string        `json:"abi_path"`
	Action    string        `json:"action"` // "call" hoặc "send" / "deploy" / "transfer"
	Method         string          `json:"method"`
	Args           []interface{}   `json:"args"`
	InputData      string          `json:"input_data"`
	ExpectedEvents []ExpectedEvent `json:"expected_events"`
	Amount         string          `json:"amount"` // Số lượng MOC (đơn vị wei)
	Verify         []interface{}   `json:"verify"` // Kiểm tra dữ liệu trả về của call
}

func main() {
	logger.SetConfig(&logger.LoggerConfig{
		Flag:    logger.FLAG_INFO,
		Outputs: []*os.File{os.Stdout},
	})

	// Khai báo flags
	configFlag := flag.String("config", "config-main.json", "Đường dẫn đến file cấu hình TCP")
	dataFlag := flag.String("data", "data.json", "Đường dẫn đến file dữ liệu (Action, Method, Args)")
	connAddrFlag := flag.String("conn", "", "Ghi đè Parent Connection Address (Override)")
	pkFlag := flag.String("pk", "", "Ghi đè Private Key (Override)")
	chainFlag := flag.Int("chain", 0, "Ghi đè Chain ID (Override, ví dụ: 991)")
	flag.Parse()

	fmt.Println("==================================================")
	fmt.Printf("🚀 START TCP CALLER CLI (-config=%s, -data=%s)\n", *configFlag, *dataFlag)
	fmt.Println("==================================================")

	// 1. Tải cấu hình file JSON
	cfgRaw, err := tcp_config.LoadConfig(*configFlag)
	if err != nil {
		log.Fatalf("❌ Lỗi đọc file config: %v", err)
	}
	cfg := cfgRaw.(*tcp_config.ClientConfig)

	// Ghi đè (Override) cấu hình nếu người dùng truyền Flag
	if *connAddrFlag != "" {
		cfg.ParentConnectionAddress = *connAddrFlag
		fmt.Printf("⚠️  Bật chế độ Override: Connection Address <- %s\n", cfg.ParentConnectionAddress)
	}
	if *pkFlag != "" {
		cfg.EthPrivateKey = *pkFlag
		fmt.Printf("⚠️  Bật chế độ Override: Private Key <- (đã ghi đè)\n")
	}
	if *chainFlag != 0 {
		cfg.ChainId = uint64(*chainFlag)
		fmt.Printf("⚠️  Bật chế độ Override: Chain ID <- %d\n", cfg.ChainId)
	}

	dataList := loadData(*dataFlag)

	// 2. Kết nối tới Chain qua TCP
	fmt.Printf("Connecting to TCP: %s\n", cfg.ConnectionAddress())
	tcpClient, err := client_tcp.NewClient(cfg)
	if err != nil {
		log.Fatalf("❌ Lỗi kết nối TCP %s: %v", cfg.ConnectionAddress(), err)
	}
	time.Sleep(1 * time.Second)

	fromAddress := common.HexToAddress(cfg.ParentAddress)

	var lastDeployedAddress *common.Address

	// Lặp qua từng task cấu hình
	for idx, d := range dataList {
		fmt.Printf("\n--- THỰC THI TASK %d: %s ---\n", idx+1, d.Method)

		var contractAddress common.Address
		action := strings.ToLower(d.Action)

		if d.Contract != "" {
			contractAddress = common.HexToAddress(d.Contract)
		} else if lastDeployedAddress != nil {
			contractAddress = *lastDeployedAddress
		} else if action != "deploy" {
			log.Fatalf("❌ Task %d không có thuộc tính 'contract' và cũng không có contract nào được deploy trước đó!", idx+1)
		}
		
		var txAmount *big.Int
		if d.Amount != "" {
			var ok bool
			txAmount, ok = new(big.Int).SetString(d.Amount, 10)
			if !ok {
				log.Fatalf("❌ Amount không hợp lệ (không phải là số nguyên): %s", d.Amount)
			}
		}

		// 4. Đọc ABI
		var contractAbi abi.ABI
		var hasAbi bool

		if d.AbiPath != "" {
			abiFile, err := os.Open(d.AbiPath)
			if err != nil {
				fmt.Printf("   ⚠️ Bỏ qua đọc ABI: Lỗi mở file %s\n", d.AbiPath)
			} else {
				contractAbi, err = abi.JSON(abiFile)
				abiFile.Close()
				if err == nil {
					hasAbi = true
				} else {
					fmt.Printf("   ⚠️ Bỏ qua đọc ABI: Parse JSON lỗi\n")
				}
			}
		}

		var payloadData []byte

		// 5. Tương tác chọn InputData hay Method
		useInputData := false
		if d.Method != "" && d.InputData != "" && hasAbi {
			fmt.Printf("⚠️ Task có CẢ Method (%s) VÀ InputData (%s).\n", d.Method, d.InputData)
			fmt.Print("👉 Bạn muốn dùng cái nào? (1: Pack theo Method+Args, 2: Dùng InputData trực tiếp): ")
			var choice string
			fmt.Scanln(&choice)
			if choice == "2" {
				useInputData = true
			}
		} else if d.InputData != "" {
			useInputData = true
		}

		if useInputData {
			payloadData, err = hexutil.Decode(d.InputData)
			if err != nil {
				log.Fatalf("❌ Lỗi giải mã InputData: %v", err)
			}
			fmt.Println("   📝 Đang sử dụng raw InputData Hex...")
			if !hasAbi {
				d.Method = ""
			}
		} else if action == "transfer" {
			// Bỏ qua kiểm tra ABI nếu là transfer thuần
			useInputData = true
		} else {
			if !hasAbi {
				log.Fatalf("❌ Task giao dịch bằng Method+Args BẮT BUỘC phải đọc được ABI hợp lệ!")
			}
			method, ok := contractAbi.Methods[d.Method]
			if !ok && d.Method != "" {
				log.Fatalf("❌ Hàm '%s' không tồn tại trong file ABI!", d.Method)
			}
			parsedArgs := prepareArgs(method, d.Args)
			payloadData, err = contractAbi.Pack(d.Method, parsedArgs...)
			if err != nil {
				log.Fatalf("❌ Lỗi Pack tham số %s: %v", d.Method, err)
			}
		}

		// 6. Thực thi
		if action == "deploy" {
			if len(payloadData) == 0 {
				log.Fatalf("❌ Action Deploy yêu cầu Bytecode trong 'input_data'!")
			}
			executeDeployTCP(tcpClient, cfg, fromAddress, payloadData, &lastDeployedAddress)
		} else if action == "call" || action == "read" {
			executeCallTCP(tcpClient, cfg, contractAddress, fromAddress, contractAbi, d.Method, payloadData, d.Verify)
		} else if action == "send" || action == "write" {
			executeSendTCP(tcpClient, cfg, contractAddress, fromAddress, payloadData, txAmount, d.Method)
		} else if action == "transfer" {
			executeSendTCP(tcpClient, cfg, contractAddress, fromAddress, nil, txAmount, "NativeTransfer")
		} else {
			log.Fatalf("❌ Action không hợp lệ: %s", d.Action)
		}
	}

	fmt.Println("==================================================")
	fmt.Println("🎉 HOÀN THẤT THỰC THI QUA TCP!")
	fmt.Println("==================================================")
}

func loadData(path string) []DataPayload {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("❌ Lỗi đọc file data (%s): %v", path, err)
	}
	var d []DataPayload
	json.Unmarshal(b, &d)
	return d
}

// ----------------------------------------------------
// THỰC THI ACTION: DEPLOY (TCP)
// ----------------------------------------------------
func executeDeployTCP(cli *client_tcp.Client, cfg *tcp_config.ClientConfig, fromAddress common.Address, bytecode []byte, lastDeployed **common.Address) {
	fmt.Println("▶️  Chạy TCP Deploy Contract...")
	emptyAddress := common.Address{}

	receipt, err := tx_helper.SendTransaction(
		"deploy",
		cli,
		cfg,
		emptyAddress,
		fromAddress,
		bytecode,
		&tx_models.TxOptions{
			MaxGas:      5000000,
			MaxGasPrice: 2000000000,
		},
	)
	if err != nil {
		log.Fatalf("❌ Lỗi deploy TCP: %v", err)
	}

	if receipt != nil && (receipt.Status() == pb.RECEIPT_STATUS_RETURNED || receipt.Status() == pb.RECEIPT_STATUS_HALTED) {
		fmt.Printf("   ✅ DEPLOY THÀNH CÔNG! (Gas used: %d)\n", receipt.GasUsed())
		// Trong TCP metanode, contract address thường được trả về qua ToAddress hoặc Return
		var addr common.Address
		toAddr := receipt.ToAddress()
		retBytes := receipt.Return()

		if (toAddr != common.Address{}) {
			addr = toAddr
			fmt.Printf("   📌 CONTRACT ADDRESS MỚI TẠO (ToAddress): %s\n", addr.Hex())
			*lastDeployed = &addr
		} else if len(retBytes) >= 20 {
			addr = common.BytesToAddress(retBytes)
			fmt.Printf("   📌 CONTRACT ADDRESS MỚI TẠO (Return Data): %s\n", addr.Hex())
			*lastDeployed = &addr
		} else {
			fmt.Printf("   ⚠️ Deploy thành công nhưng không lấy được address! (Return len: %d)\n", len(retBytes))
		}
	} else {
		status := pb.RECEIPT_STATUS_TRANSACTION_ERROR
		if receipt != nil {
			status = receipt.Status()
		}
		log.Fatalf("❌ DEPLOY THẤT BẠI (Status: %s)!", status.String())
	}
}

// ----------------------------------------------------
// THỰC THI ACTION: CALL (TCP)
// ----------------------------------------------------
func executeCallTCP(cli *client_tcp.Client, cfg *tcp_config.ClientConfig, contractAddress common.Address, fromAddress common.Address, parsedABI abi.ABI, methodName string, payloadData []byte, expectedVerify []interface{}) {
	fmt.Printf("▶️  Chạy thử TCP Call (READ) cho hàm %s...\n", methodName)

	receipt, err := tx_helper.SendReadTransaction(
		methodName,
		cli,
		cfg,
		contractAddress,
		fromAddress,
		payloadData,
		nil,
	)
	if err != nil {
		log.Fatalf("❌ Lỗi gọi TCP Call %s: %v", methodName, err)
	}

	result := receipt.Return()
	if methodName != "" && len(result) > 0 {
		outputs, err := parsedABI.Unpack(methodName, result)
		if err != nil {
			log.Printf("⚠️ Không thể unpack kết quả hàm %s: %v", methodName, err)
			fmt.Printf("   ✅ Kết quả RAW: %x\n", result)
		} else {
			if len(outputs) > 0 {
				fmt.Printf("   ✅ KẾT QUẢ ĐỌC: %+v\n", outputs)
				
				// Kiểm tra mảng Verify nếu được cung cấp
				if len(expectedVerify) > 0 {
					if len(outputs) != len(expectedVerify) {
						log.Fatalf("❌ KẾT QUẢ ĐỌC SAI: Mong đợi %d giá trị trả về, nhưng nhận được %d", len(expectedVerify), len(outputs))
					}
					
					method, ok := parsedABI.Methods[methodName]
					if ok {
						for i, expectedRaw := range expectedVerify {
							expectedVal, err := convertToType(method.Outputs[i].Type, expectedRaw)
							if err != nil {
								log.Fatalf("❌ Lỗi chuyển đổi tham số Verify thứ [%d]: %v", i, err)
							}
							
							outputStr := fmt.Sprintf("%v", outputs[i])
							expectedStr := fmt.Sprintf("%v", expectedVal)
							
							if outputStr != expectedStr {
								log.Fatalf("❌ KẾT QUẢ ĐỌC SAI ở tham số thứ [%d]: Mong đợi '%s', nhận được '%s'", i, expectedStr, outputStr)
							}
						}
						fmt.Printf("   ✅ VERIFY MATCH: Kết quả trả về khớp 100%% với mong đợi!\n")
					}
				}
			} else {
				fmt.Printf("   ✅ eth_call thành công (Kết quả trả về rỗng).\n")
			}
		}
	} else {
		fmt.Printf("   ✅ Kết quả RAW: %x\n", result)
	}
}

// ----------------------------------------------------
// THỰC THI ACTION: SEND & TRANSFER (TCP)
// ----------------------------------------------------
func executeSendTCP(cli *client_tcp.Client, cfg *tcp_config.ClientConfig, contractAddress common.Address, fromAddress common.Address, payloadData []byte, amount *big.Int, methodName string) {
	fmt.Printf("▶️  Chạy TCP SendTransaction (WRITE) cho hàm/hành động %s...\n", methodName)

	var options *tx_models.TxOptions
	if amount != nil {
		options = &tx_models.TxOptions{Amount: amount}
	}

	receipt, err := tx_helper.SendTransaction(
		methodName,
		cli,
		cfg,
		contractAddress,
		fromAddress,
		payloadData,
		options,
	)
	if err != nil {
		log.Fatalf("❌ Lỗi gửi TCP Send: %v", err)
	}

	if receipt != nil {
		status := receipt.Status()
		if status == pb.RECEIPT_STATUS_RETURNED || status == pb.RECEIPT_STATUS_HALTED {
			fmt.Printf("   ✅ Tx THÀNH CÔNG (Gas used: %d, Status: %s)\n", receipt.GasUsed(), status.String())
		} else {
			fmt.Printf("   ❌ Tx THẤT BẠI (Status: %s)\n", status.String())
		}
	} else {
		fmt.Printf("   ⚠️ Tx gửi thành công nhưng không có Receipt\n")
	}
}

// ----------------------------------------------------
// NƠI XỬ LÝ ARGUMENTS THÔNG MINH MAP VỚI ABI
// ----------------------------------------------------
func prepareArgs(method abi.Method, jsonArgs []interface{}) []interface{} {
	if len(method.Inputs) != len(jsonArgs) {
		log.Fatalf("❌ Kích thước tham số không khớp: Hàm '%s' yêu cầu %d tham số, nhưng file json gửi lên %d", method.Name, len(method.Inputs), len(jsonArgs))
	}

	var packedArgs []interface{}
	for i, input := range method.Inputs {
		rawVal := jsonArgs[i]
		val, err := convertToType(input.Type, rawVal)
		if err != nil {
			log.Fatalf("❌ Lỗi chuyển đổi tham số thứ [%d] (kiểu %s): %v", i, input.Type.String(), err)
		}
		packedArgs = append(packedArgs, val)
	}
	return packedArgs
}

func convertToType(t abi.Type, val interface{}) (interface{}, error) {
	strVal := fmt.Sprintf("%v", val)

	switch t.T {
	case abi.SliceTy, abi.ArrayTy:
		valSlice, ok := val.([]interface{})
		if !ok {
			return nil, fmt.Errorf("tham số yêu cầu mảng JSON")
		}

		sliceType := reflect.SliceOf(t.Elem.GetType())
		sliceReflect := reflect.MakeSlice(sliceType, len(valSlice), len(valSlice))

		for i, v := range valSlice {
			converted, err := convertToType(*t.Elem, v)
			if err != nil {
				return nil, err
			}
			sliceReflect.Index(i).Set(reflect.ValueOf(converted))
		}

		if t.T == abi.ArrayTy {
			arrType := reflect.ArrayOf(t.Size, t.Elem.GetType())
			arrReflect := reflect.New(arrType).Elem()
			reflect.Copy(arrReflect, sliceReflect)
			return arrReflect.Interface(), nil
		}
		return sliceReflect.Interface(), nil

	case abi.BytesTy:
		if !strings.HasPrefix(strVal, "0x") {
			strVal = "0x" + strVal
		}
		b, err := hexutil.Decode(strVal)
		if err != nil {
			return nil, err
		}
		return b, nil

	case abi.FixedBytesTy:
		if !strings.HasPrefix(strVal, "0x") {
			strVal = "0x" + strVal
		}
		b, err := hexutil.Decode(strVal)
		if err != nil {
			return nil, err
		}
		if len(b) > t.Size {
			return nil, fmt.Errorf("kích thước bytes %d vượt quá %d", len(b), t.Size)
		}
		arrType := reflect.ArrayOf(t.Size, reflect.TypeOf(uint8(0)))
		arrReflect := reflect.New(arrType).Elem()
		reflect.Copy(arrReflect, reflect.ValueOf(b))
		return arrReflect.Interface(), nil

	case abi.TupleTy:
		valMap, ok := val.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("tham số Struct(%s) phải là JSON Object", t.String())
		}
		structType := t.GetType()
		structReflect := reflect.New(structType).Elem()

		for i, elemType := range t.TupleElems {
			fieldName := t.TupleRawNames[i]
			fieldJSONValue, exist := valMap[fieldName]
			if !exist {
				return nil, fmt.Errorf("Struct thiếu trường '%s'", fieldName)
			}
			converted, err := convertToType(*elemType, fieldJSONValue)
			if err != nil {
				return nil, err
			}
			structReflect.Field(i).Set(reflect.ValueOf(converted))
		}
		return structReflect.Interface(), nil

	case abi.IntTy, abi.UintTy:
		n := new(big.Int)
		if strings.Contains(strVal, ".") {
			strVal = strings.Split(strVal, ".")[0]
		}
		n.SetString(strVal, 10)
		if t.Size <= 64 {
			if t.T == abi.UintTy {
				switch t.Size {
				case 8:
					return uint8(n.Uint64()), nil
				case 16:
					return uint16(n.Uint64()), nil
				case 32:
					return uint32(n.Uint64()), nil
				case 64:
					return uint64(n.Uint64()), nil
				}
			} else {
				switch t.Size {
				case 8:
					return int8(n.Int64()), nil
				case 16:
					return int16(n.Int64()), nil
				case 32:
					return int32(n.Int64()), nil
				case 64:
					return int64(n.Int64()), nil
				}
			}
		}
		return n, nil

	case abi.StringTy:
		return strVal, nil

	case abi.AddressTy:
		if !common.IsHexAddress(strVal) {
			return nil, fmt.Errorf("không phải địa chỉ hợp lệ: %s", strVal)
		}
		return common.HexToAddress(strVal), nil

	case abi.BoolTy:
		if v, ok := val.(bool); ok {
			return v, nil
		}
		if strVal == "true" || strVal == "1" {
			return true, nil
		}
		return false, nil
	}

	return val, nil
}
