package utils

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"sync"

	ethCommon "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/rpc_client"
)

var hexDecodePool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 64*1024)
		return &buf
	},
}

func DecodeHexString(hexStr string) ([]byte, error) {
	if strings.HasPrefix(hexStr, "0x") || strings.HasPrefix(hexStr, "0X") {
		hexStr = hexStr[2:]
	}
	if len(hexStr)%2 == 1 {
		hexStr = "0" + hexStr
	}

	bufPtr := hexDecodePool.Get().(*[]byte)
	buf := *bufPtr
	if cap(buf) < len(hexStr)/2 {
		buf = make([]byte, len(hexStr)/2)
	} else {
		buf = buf[:len(hexStr)/2]
	}

	n, err := hex.Decode(buf, []byte(hexStr))
	if err != nil {
		hexDecodePool.Put(bufPtr)
		return nil, err
	}
	result := make([]byte, n)
	copy(result, buf[:n])
	*bufPtr = buf
	hexDecodePool.Put(bufPtr)
	return result, nil
}

func DecodeHexPooled(hexStr string) ([]byte, func(), error) {
	if strings.HasPrefix(hexStr, "0x") || strings.HasPrefix(hexStr, "0X") {
		hexStr = hexStr[2:]
	}
	if len(hexStr)%2 != 0 {
		hexStr = "0" + hexStr
	}

	bufPtr := hexDecodePool.Get().(*[]byte)
	buf := *bufPtr
	needed := len(hexStr) / 2
	if cap(buf) < needed {
		buf = make([]byte, needed)
	} else {
		buf = buf[:needed]
	}

	decoder := hex.NewDecoder(strings.NewReader(hexStr))
	if _, err := io.ReadFull(decoder, buf); err != nil {
		if cap(buf) > 256*1024 {
			buf = make([]byte, 0, 64*1024)
		} else {
			buf = buf[:0]
		}
		*bufPtr = buf
		hexDecodePool.Put(bufPtr)
		return nil, func() {}, err
	}

	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if cap(buf) > 256*1024 {
			buf = make([]byte, 0, 64*1024)
		} else {
			buf = buf[:0]
		}
		*bufPtr = buf
		hexDecodePool.Put(bufPtr)
	}

	return buf, release, nil
}

func DecodeCallObject(raw json.RawMessage) (rpc_client.DecodedCallObject, error) {
	var schema rpc_client.CallObjectSchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		logger.Error("Failed to unmarshal call object JSON: %v, raw: %s", err, string(raw))
		return rpc_client.DecodedCallObject{}, err
	}

	var fromAddress ethCommon.Address
	if schema.From != "" {
		fromAddress = ethCommon.HexToAddress(schema.From)
	}

	payloadHex := schema.Data
	if payloadHex == "" {
		payloadHex = schema.Input
	}

	var payload []byte
	if payloadHex != "" {
		if !strings.HasPrefix(payloadHex, "0x") && !strings.HasPrefix(payloadHex, "0X") {
			payloadHex = "0x" + payloadHex
		}
		data, err := DecodeHexString(payloadHex)
		if err != nil {
			logger.Error("Failed to decode hex string for call object: %v, hex: %s", err, payloadHex)
			return rpc_client.DecodedCallObject{}, err
		}
		payload = data
		// đồng bộ lại các trường dữ liệu hex
		schema.Data = payloadHex
		if schema.Input == "" {
			schema.Input = payloadHex
		}
	} else {
		// đảm bảo cả hai trường cùng rỗng nếu input không có
		schema.Data = ""
		schema.Input = ""
	}

	hasTo := !(schema.To == "" || schema.To == "0x")
	var toAddress ethCommon.Address
	if hasTo {
		toAddress = ethCommon.HexToAddress(schema.To)
	}

	return rpc_client.DecodedCallObject{
		Schema:      schema,
		FromAddress: fromAddress,
		ToAddress:   toAddress,
		HasTo:       hasTo,
		Payload:     payload,
	}, nil
}
