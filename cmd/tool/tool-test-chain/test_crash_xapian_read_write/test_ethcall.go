//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

const (
	// rpcURL          = "http://139.59.243.85:8545"
	contractAddress = "0xc3Ae1dE2Fc9863A50b83928BB53C475d99540421" // <--- THAY ĐỊA CHỈ CONTRACT MỚI DEPLOY VÀO ĐÂY
	rpcURL          = "http://127.0.0.1:8545"
	// contractAddress = "0x9DDDf80eC30F73F7646364188393Df3b778a02e7"
)

type rpcRequest struct {
	JsonRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcResponse struct {
	JsonRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Result  string `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func doEthCall(callDataHex string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	reqBody := rpcRequest{
		JsonRPC: "2.0",
		Method:  "eth_call",
		Params: []interface{}{
			map[string]string{
				"from": "0x0B143e894a600114C4A3729874214e5fC5EA9cbc",
				"to":   contractAddress,
				"data": callDataHex,
			},
			"latest",
		},
		ID: 1,
	}

	payload, _ := json.Marshal(reqBody)
	resp, err := client.Post(rpcURL, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var res rpcResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return "", err
	}
	if res.Error != nil {
		return "", fmt.Errorf("RPC error: %s", res.Error.Message)
	}
	return res.Result, nil
}

func main() {
	// callData1: testLeak_Create()
	callData1 := "0x6c29a2d2"

	fmt.Println("🚀 Bắt đầu test rò rỉ dữ liệu xuống Xapian qua eth_call...")
	fmt.Println("⏳ [1] Gọi eth_call testLeak_Create() ... (Lẽ ra data ảo này không được lưu)")

	res1, err := doEthCall(callData1)
	if err != nil {
		fmt.Printf("❌ Lỗi gọi eth_call: %v\n", err)
		return
	}
	if res1 == "0x" {
		fmt.Println("❌ Không gọi được hàm testLeak_Create()! Vui lòng kiểm tra lại bạn ĐÃ DEPLOY LẠI CONTRACT test-db chưa và ĐÃ CẬP NHẬT contractAddress trong code chưa.")
		return
	}

	// Cắt bỏ "0x" và parse chuỗi hex để lấy giá trị số nguyên của docId
	docIdHex := strings.TrimPrefix(res1, "0x")
	docId := big.NewInt(0)
	docId.SetString(docIdHex, 16)

	fmt.Printf("✅ Nhận được DOC_ID ẢO dId = %s\n", docId.String())

	fmt.Println("\nĐợi 1 giây để chắc chắn đĩa đã được cập nhật...")
	time.Sleep(1 * time.Second)

	// callData2: testLeak_Read(uint256) (gắn params docId vào)
	// Hash của testLeak_Read(uint256) là 0x76c7e14a
	callData2 := fmt.Sprintf("0x76c7e14a%064x", docId)

	fmt.Printf("\n⏳ [2] Dùng DOC_ID %s vừa nãy để gọi tiếp eth_call testLeak_Read(%s)...\n", docId.String(), docId.String())
	fmt.Println("Nếu Xapian lưu thật xuống đĩa, nó sẽ trả về chuỗi. Nếu không lưu, nó sẽ lỗi hoặc trả về khoảng trống (an toàn).")

	res2, err := doEthCall(callData2)
	if err != nil {
		fmt.Printf("✅ An toàn! Hàm báo lỗi do không tìm thấy Document: %v\n", err)
	} else if len(res2) <= 130 {
		// chuỗi string decode abi gồm offset (32 bytes), length (32 bytes), và data rỗng hoặc 0
		fmt.Printf("✅ Hoàn hảo! Xapian trả về rỗng. Data rác từ eth_call ĐÃ HOÀN TOÀN BỊ LOẠI BỎ (KHÔNG LƯU VÀO ĐĨA). (Raw= %s)\n", res2)
		fmt.Println("🎉 KẾT LUẬN: BẠN ĐÃ FIX HOÀN TOÀN TẬN GỐC LỖI RÒ RỈ CỦA XAPIAN MANAGER!")
	} else {
		// Giải mã ABI string
		stringT, _ := abi.NewType("string", "", nil)
		arguments := abi.Arguments{
			{Type: stringT},
		}

		resBytes, _ := hexutil.Decode(res2)
		unpacked, err := arguments.UnpackValues(resBytes)
		if err == nil && len(unpacked) > 0 {
			decodedStr := unpacked[0].(string)
			fmt.Printf("❌ CẢNH BÁO LỖI NGHIÊM TRỌNG! Data leak đọc được: '%s'\n", decodedStr)
		} else {
			fmt.Printf("❌ CẢNH BÁO LỖI NGHIÊM TRỌNG! Unpack error: %v, raw: %s\n", err, res2)
		}
		fmt.Println("💀 KẾT LUẬN: DỮ LIỆU ĐÃ BỊ GHI LÉN XUỐNG ĐĨA VÀ TỒN TẠI VĨNH VIỄN!! RLock CHỈ GIÚP KHÔNG CRASH CHỨ KHÔNG NGĂN CẢN ĐƯỢC LỖI GHI LÉN!")
	}
}
