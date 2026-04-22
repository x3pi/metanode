package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"
	"time"

	"reflect"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

type Config struct {
	RPCUrl     string `json:"rpc_url"`
	PrivateKey string `json:"private_key"`
	ChainID    int64  `json:"chain_id"`
}

type DataPayload struct {
	Contract  string        `json:"contract"`
	AbiPath   string        `json:"abi_path"`
	Action    string        `json:"action"` // "call" hoặc "send"
	Method    string        `json:"method"`
	Args      []interface{} `json:"args"`
	InputData string        `json:"input_data"`
}

func main() {
	// Khai báo flags
	configFlag := flag.String("config", "config.json", "Đường dẫn đến file cấu hình mạng")
	dataFlag := flag.String("data", "data.json", "Đường dẫn đến file dữ liệu (Action, Method, Args)")
	rpcFlag := flag.String("rpc", "", "Ghi đè RPC URL (Override)")
	pkFlag := flag.String("pk", "", "Ghi đè Private Key (Override)")
	chainFlag := flag.Int64("chain", 0, "Ghi đè Chain ID (Override, ví dụ: 991)")
	flag.Parse()

	fmt.Println("==================================================")
	fmt.Printf("🚀 START XỊN CALLER CLI (-config=%s, -data=%s)\n", *configFlag, *dataFlag)
	fmt.Println("==================================================")

	// 1. Tải cấu hình file JSON
	cfg := loadConfig(*configFlag)

	// Ghi đè (Override) cấu hình nếu người dùng truyền Flag từ dòng lệnh
	if *rpcFlag != "" {
		cfg.RPCUrl = *rpcFlag
		fmt.Printf("⚠️  Bật chế độ Override: RPC URL <- %s\n", cfg.RPCUrl)
	}
	if *pkFlag != "" {
		cfg.PrivateKey = *pkFlag
		fmt.Printf("⚠️  Bật chế độ Override: Private Key <- (đã ghi đè)\n")
	}
	if *chainFlag != 0 {
		cfg.ChainID = *chainFlag
		fmt.Printf("⚠️  Bật chế độ Override: Chain ID <- %d\n", cfg.ChainID)
	}

	dataList := loadData(*dataFlag)

	// 2. Kết nối tới Chain
	client, err := ethclient.Dial(cfg.RPCUrl)
	if err != nil {
		log.Fatalf("❌ Lỗi kết nối RPC %s: %v", cfg.RPCUrl, err)
	}

	// 3. Chuẩn bị Private Key & Address
	privateKey, err := crypto.HexToECDSA(cfg.PrivateKey)
	if err != nil {
		log.Fatalf("❌ Lỗi parse private key: %v", err)
	}

	publicKey := privateKey.Public()
	fromAddress := crypto.PubkeyToAddress(*publicKey.(*ecdsa.PublicKey))

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

		// 4. Đọc ABI (Không bắt buộc nếu chỉ dùng InputData)
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

		// 5. Tương tác chọn InputData hay Method (nếu có cả hai)
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
			if !strings.HasPrefix(d.InputData, "0x") {
				d.InputData = "0x" + d.InputData
			}
			payloadData, err = hexutil.Decode(d.InputData)
			if err != nil {
				log.Fatalf("❌ Lỗi giải mã InputData: %v", err)
			}
			fmt.Println("   📝 Đang sử dụng raw InputData Hex...")
			// Nếu không có ABI thì tự động xóa Method để tránh lỗi cố gắng Unpack kết quả
			if !hasAbi {
				d.Method = ""
			}
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
				log.Fatalf("❌ Action Deploy yêu cầu phải có Bytecode truyền vào biến 'input_data'!")
			}
			newAddr := executeDeploy(client, privateKey, cfg.ChainID, fromAddress, payloadData)
			if newAddr != nil {
				lastDeployedAddress = newAddr
			}
		} else if action == "call" || action == "read" {
			executeCall(client, contractAddress, contractAbi, d.Method, payloadData)
		} else if action == "send" || action == "write" {
			executeSend(client, privateKey, cfg.ChainID, fromAddress, contractAddress, payloadData, d.Method, contractAbi, hasAbi)
		} else {
			log.Fatalf("❌ Action không hợp lệ: %s", d.Action)
		}
	}

	fmt.Println("==================================================")
	fmt.Println("🎉 HOÀN THẤT THỰC THI!")
	fmt.Println("==================================================")
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
	// Đưa value về dạng chuỗi trung gian cứng (chống sai số float của JSON)
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

// ----------------------------------------------------
// THỰC THI ACTION: DEPLOY (Tạo Contract Mới)
// ----------------------------------------------------
func executeDeploy(client *ethclient.Client, privateKey *ecdsa.PrivateKey, chainId int64, fromAddress common.Address, bytecode []byte) *common.Address {
	fmt.Println("▶️  Chạy eth_sendRawTransaction (DEPLOY CONTRACT)...")

	nonce, err := client.PendingNonceAt(context.Background(), fromAddress)
	if err != nil {
		log.Fatalf("Lỗi lấy nonce: %v", err)
	}
	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		log.Fatalf("Lỗi lấy gas price: %v", err)
	}

	// Estimate Gas
	msg := ethereum.CallMsg{
		From: fromAddress,
		Data: bytecode,
	}
	gasLimit, err := client.EstimateGas(context.Background(), msg)
	if err != nil {
		fmt.Printf("   ⚠️ EstimateGas Deploy thất bại: %v. Dùng GasLimit tĩnh (5.000.000).\n", err)
		gasLimit = 5000000
	} else {
		gasLimit += 50000
	}

	tx := types.NewContractCreation(nonce, big.NewInt(0), gasLimit, gasPrice, bytecode)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(chainId)), privateKey)
	if err != nil {
		log.Fatalf("Lỗi ký transaction: %v", err)
	}

	fmt.Printf("   📝 CHI TIẾT TX DEPLOY:\n")
	fmt.Printf("      - Nonce: %d\n", signedTx.Nonce())
	fmt.Printf("      - Gas Price: %s wei\n", gasPrice.String())
	fmt.Printf("      - Gas Limit: %d\n", signedTx.Gas())
	fmt.Printf("      - Type: Contract Creation (To: 0x0)\n")
	fmt.Printf("      - Bytecode Length: %d bytes\n", len(bytecode))

	err = client.SendTransaction(context.Background(), signedTx)
	if err != nil {
		log.Fatalf("Lỗi gửi transaction Deploy: %v", err)
	}

	fmt.Printf("   🚀 Đã gửi Tx Deploy. Hash: %s\n", signedTx.Hash().Hex())
	fmt.Printf("   ⏳ Đang đợi mạng Mining (Polling Receipt) ")

	for {
		receipt, err := client.TransactionReceipt(context.Background(), signedTx.Hash())
		if err == nil {
			if receipt.Status == 1 {
				fmt.Printf("\n   ✅ DEPLOY THÀNH CÔNG! (Gas used: %d)\n", receipt.GasUsed)
				fmt.Printf("   📌 CONTRACT ADDRESS MỚI TẠO: %s\n", receipt.ContractAddress.Hex())
				return &receipt.ContractAddress
			} else {
				fmt.Printf("\n   ❌ DEPLOY THẤT BẠI! (Giao dịch bị Revert)\n")
				return nil
			}
		}
		fmt.Print(".")
		time.Sleep(1 * time.Second)
	}
}

// ----------------------------------------------------
// THỰC THI ACTION: CALL (Mô phỏng/Đọc)
// ----------------------------------------------------
func executeCall(client *ethclient.Client, contractAddress common.Address, parsedABI abi.ABI, methodName string, payloadData []byte) {
	fmt.Printf("▶️  Chạy thử eth_call (READ/SIMULATE) cho hàm %s...\n", methodName)

	msg := ethereum.CallMsg{
		To:   &contractAddress,
		Data: payloadData,
	}

	result, err := client.CallContract(context.Background(), msg, nil)
	if err != nil {
		log.Fatalf("Lỗi gọi eth_call %s: %v", methodName, err)
	}

	if methodName != "" {
		outputs, err := parsedABI.Unpack(methodName, result)
		if err != nil {
			log.Printf("⚠️ Không thể unpack kết quả hàm %s: %v", methodName, err)
			fmt.Printf("   ✅ Kết quả RAW: %x\n", result)
		} else {
			if len(outputs) > 0 {
				fmt.Printf("   ✅ KẾT QUẢ ĐỌC: %+v\n", outputs)
			} else {
				fmt.Printf("   ✅ eth_call thành công (Kết quả trả về rỗng).\n")
			}
		}
	} else {
		fmt.Printf("   ✅ Kết quả RAW: %x\n", result)
	}
}

// ----------------------------------------------------
// THỰC THI ACTION: SEND (Giao dịch thực thụ)
// ----------------------------------------------------
func executeSend(client *ethclient.Client, privateKey *ecdsa.PrivateKey, chainId int64, fromAddress common.Address, contractAddress common.Address, payloadData []byte, methodName string, parsedABI abi.ABI, hasAbi bool) {
	fmt.Printf("▶️  Chạy eth_sendRawTransaction (WRITE/SEND) cho hàm %s...\n", methodName)

	// 2. Cấu hình giao dịch
	nonce, err := client.PendingNonceAt(context.Background(), fromAddress)
	if err != nil {
		log.Fatalf("Lỗi lấy nonce: %v", err)
	}

	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		log.Fatalf("Lỗi lấy gas price: %v", err)
	}

	msg := ethereum.CallMsg{
		From:     fromAddress,
		To:       &contractAddress,
		GasPrice: gasPrice,
		Data:     payloadData,
	}
	gasLimit, err := client.EstimateGas(context.Background(), msg)
	if err != nil {
		fmt.Printf("   ⚠️ EstimateGas thất bại: %v. Dùng GasLimit tĩnh (3.000.000).\n", err)
		gasLimit = 3000000
	} else {
		gasLimit += 30000 // Buffer an toàn
	}

	// 3. Tạo và Ký Transaction
	tx := types.NewTransaction(nonce, contractAddress, big.NewInt(0), gasLimit, gasPrice, payloadData)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(chainId)), privateKey)
	if err != nil {
		log.Fatalf("Lỗi ký transaction: %v", err)
	}

	fmt.Printf("   📝 CHI TIẾT TX GỬI ĐI (%s):\n", methodName)
	fmt.Printf("      - Hash: %s\n", signedTx.Hash().Hex())
	fmt.Printf("      - Nonce: %d\n", signedTx.Nonce())
	fmt.Printf("      - Gas Price: %s wei\n", gasPrice.String())
	fmt.Printf("      - Gas Limit: %d\n", signedTx.Gas())
	fmt.Printf("      - To Contract: %s\n", contractAddress.Hex())
	fmt.Printf("      - Data Length: %d bytes\n", len(payloadData))

	// 4. Bắn lên mạng
	err = client.SendTransaction(context.Background(), signedTx)
	if err != nil {
		log.Fatalf("Lỗi gửi transaction: %v", err)
	}

	fmt.Printf("   🚀 Đã gửi Tx. Hash: %s\n", signedTx.Hash().Hex())
	fmt.Printf("   ⏳ Đang đợi mạng Mining (Polling Receipt) ")

	// 5. Polling đợi mạng lưới
	for {
		receipt, err := client.TransactionReceipt(context.Background(), signedTx.Hash())
		if err == nil {
			fmt.Println()
			if receipt.Status == 1 {
				fmt.Printf("   ✅ Tx THÀNH CÔNG (Gas used: %d)\n", receipt.GasUsed)
				if hasAbi && len(receipt.Logs) > 0 {
					fmt.Printf("   📝 SỰ KIỆN (EVENTS):\n")
					for i, vLog := range receipt.Logs {
						if len(vLog.Topics) == 0 {
							continue
						}
						event, err := parsedABI.EventByID(vLog.Topics[0])
						if err != nil {
							fmt.Printf("      - Log [%d]: Topic0=%s (Không tìm thấy trong ABI)\n", i, vLog.Topics[0].Hex())
							continue
						}
						fmt.Printf("      - Log [%d] Event: %s\n", i, event.Name)
						for j, topic := range vLog.Topics {
							fmt.Printf("         + Topic[%d]: %s\n", j, topic.Hex())
						}
						if len(vLog.Data) > 0 {
							unpacked, err := event.Inputs.NonIndexed().Unpack(vLog.Data)
							if err == nil {
								fmt.Printf("         + Data: ")
								for k, unp := range unpacked {
									fmt.Printf("%v", unp)
									if k < len(unpacked)-1 {
										fmt.Printf(", ")
									}
								}
								fmt.Println()
							} else {
								fmt.Printf("         + Lỗi đọc Data: %v\n", err)
							}
						}
					}
				}
			} else {
				fmt.Printf("   ❌ Tx THẤT BẠI (Revert)\n")
			}
			return
		} else if err != ethereum.NotFound {
			log.Fatalf("\n   ❌ Lỗi hệ thống khi check receipt: %v", err)
		}
		fmt.Print(".")
		time.Sleep(1 * time.Second)
	}
}

// Hàm phụ trợ tải JSON
func loadConfig(path string) *Config {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("❌ Lỗi đõc file config (%s): %v", path, err)
	}
	var c Config
	json.Unmarshal(b, &c)
	return &c
}

func loadData(path string) []DataPayload {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("❌ Lỗi đõc file data (%s): %v", path, err)
	}
	var d []DataPayload
	json.Unmarshal(b, &d)
	return d
}
