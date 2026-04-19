package mvm

/*
#cgo CFLAGS: -w
#cgo CXXFLAGS: -std=c++17 -w
#cgo LDFLAGS: -L./linker/build/lib/static -lmvm_linker -lxapian -L./c_mvm/build/lib/static -lmvm -lstdc++
#cgo CPPFLAGS: -I./linker/build/include
#include "mvm_linker.hpp"
#include <stdlib.h>

typedef struct {
    unsigned char *data_p;
    int data_size;
} Extension_return;

*/
import "C"
import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/ethereum/go-ethereum/accounts/abi"
	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract/argument_encode"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
)

//export ExtensionCallGetApi
func ExtensionCallGetApi(
	bytes *C.uchar,
	size C.int,
) C.struct_Extension_return {
	bCallData := C.GoBytes(unsafe.Pointer(bytes), size)
	logger.Debug("Calling get api data ", hex.EncodeToString(bCallData))
	url := argument_encode.DecodeStringInput(bCallData[4:], 0)
	// Add safe HTTP client with SSRF protection and timeout
	safeHTTPClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					host = addr
				}
				ips, err := net.LookupIP(host)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
						return nil, fmt.Errorf("SSRF blocked: access to private/local IP %s is not allowed", ip.String())
					}
				}
				return net.DialTimeout(network, addr, 5*time.Second)
			},
		},
	}

	response, err := safeHTTPClient.Get(url)
	if err != nil {
		logger.Warn("Error when call get api to ", url, err)

		return C.struct_Extension_return{data_p: nil, data_size: 0}
	}
	defer response.Body.Close()
	responseData, err := io.ReadAll(io.LimitReader(response.Body, 10*1024*1024)) // 10MB limit limit
	if err != nil {
		logger.Warn("Error when call get api to ", url, err)
		return C.struct_Extension_return{data_p: nil, data_size: 0}
	}
	encodedRespone := argument_encode.EncodeSingleString(string(responseData))
	logger.Debug("Extension call get api result ", encodedRespone)
	data_size := C.int(len(encodedRespone))
	data_p := (*C.uchar)(C.CBytes(encodedRespone))
	return C.struct_Extension_return{
		data_p:    data_p,
		data_size: data_size,
	}
}

//export ExtensionExtractJsonField
func ExtensionExtractJsonField(
	bytes *C.uchar,
	size C.int,
) C.struct_Extension_return {
	bCallData := C.GoBytes(unsafe.Pointer(bytes), size)
	logger.Debug("Extension extract json field ", hex.EncodeToString(bCallData))
	jsonMap := make(map[string]interface{})
	jsonStr := argument_encode.DecodeStringInput(bCallData[4:], 0)
	field := argument_encode.DecodeStringInput(bCallData[4:], 1)
	var fieldData interface{}
	err := json.Unmarshal([]byte(jsonStr), &jsonMap)
	var data string
	// process json map
	if err == nil {
		fieldData = jsonMap[field]
	} else {
		// process json array
		jsonArr := []interface{}{}
		err = json.Unmarshal([]byte(jsonStr), &jsonArr)
		if err != nil {
			logger.Warn("Error when extract json field ", jsonStr, field, err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		intField, err := strconv.Atoi(field)
		if err != nil {
			logger.Warn("Error when extract json field ", jsonStr, field, err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		fieldData = jsonArr[intField]
	}

	if reflect.ValueOf(fieldData).Kind() == reflect.Map || reflect.ValueOf(fieldData).Kind() == reflect.Array {
		bData, _ := json.Marshal(fieldData)
		data = string(bData)
	} else {
		data = fmt.Sprintf("%v", fieldData)
		// reformat boolean
		if data == "false" {
			data = "0"
		}
		if data == "true" {
			data = "1"
		}
	}

	encodedData := argument_encode.EncodeSingleString(data)
	data_size := C.int(len(encodedData))
	data_p := (*C.uchar)(C.CBytes(encodedData))
	return C.struct_Extension_return{
		data_p:    data_p,
		data_size: data_size,
	}
}

//export ExtensionBlst
func ExtensionBlst(
	bytes *C.uchar,
	size C.int,
) C.struct_Extension_return {
	blstAbiStr := strings.NewReader(`
[
	{
		"inputs": [
			{
				"internalType": "bytes[]",
				"name": "publicKey",
				"type": "bytes[]"
			},
			{
				"internalType": "bytes",
				"name": "sign",
				"type": "bytes"
			},
			{
				"internalType": "bytes[]",
				"name": "message",
				"type": "bytes[]"
			}
		],
		"name": "verifyAggregateSign",
		"outputs": [
			{
				"internalType": "bool",
				"name": "",
				"type": "bool"
			}
		],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "bytes",
				"name": "publicKey",
				"type": "bytes"
			},
			{
				"internalType": "bytes",
				"name": "sign",
				"type": "bytes"
			},
			{
				"internalType": "bytes",
				"name": "message",
				"type": "bytes"
			}
		],
		"name": "verifySign",
		"outputs": [
			{
				"internalType": "bool",
				"name": "",
				"type": "bool"
			}
		],
		"stateMutability": "nonpayable",
		"type": "function"
	}
]
  `)
	blstAbi, err := abi.JSON(blstAbiStr)
	if err != nil {
		logger.Error("Error ", err)
		return C.struct_Extension_return{data_p: nil, data_size: 0}
	}

	bCallData := C.GoBytes(unsafe.Pointer(bytes), size)
	logger.Debug("Calling extention blst", hex.EncodeToString(bCallData))
	method, err := blstAbi.MethodById(bCallData[0:4])
	if err != nil {
		logger.Warn("Error when get method by id", err)
		return C.struct_Extension_return{data_p: nil, data_size: 0}
	}

	if method.RawName == "verifySign" {
		mapInput := make(map[string]interface{})
		method.Inputs.UnpackIntoMap(mapInput, bCallData[4:])
		outputs, err := method.Outputs.Pack(
			bls.VerifySign(
				common.PubkeyFromBytes(mapInput["publicKey"].([]byte)),
				common.SignFromBytes(mapInput["sign"].([]byte)),
				mapInput["message"].([]byte),
			),
		)
		if err != nil {
			logger.Warn("Error when pack output", err)
		}
		data_size := C.int(len(outputs))
		data_p := (*C.uchar)(C.CBytes(outputs))
		return C.struct_Extension_return{
			data_p:    data_p,
			data_size: data_size,
		}
	}

	if method.RawName == "verifyAggregateSign" {
		// VerifyAggregateSign(bPubs [][]byte, bSig []byte, bMsgs [][]byte) bool
		mapInput := make(map[string]interface{})
		method.Inputs.UnpackIntoMap(mapInput, bCallData[4:])
		outputs, err := method.Outputs.Pack(
			bls.VerifyAggregateSign(
				mapInput["publicKey"].([][]byte),
				mapInput["sign"].([]byte),
				mapInput["message"].([][]byte),
			),
		)
		if err != nil {
			logger.Warn("Error when pack output", err)
		}
		data_size := C.int(len(outputs))
		data_p := (*C.uchar)(C.CBytes(outputs))
		logger.Debug("Extension blst result ", hex.EncodeToString(outputs))
		return C.struct_Extension_return{
			data_p:    data_p,
			data_size: data_size,
		}
	}
	return C.struct_Extension_return{data_p: nil, data_size: 0}

}

func WrapExtensionBlst(
	data []byte,
) []byte {
	cData := C.CBytes(data)
	cReturn := ExtensionBlst((*C.uchar)(cData), C.int(len(data)))
	return C.GoBytes(unsafe.Pointer(cReturn.data_p), cReturn.data_size)
}

//export ExtensionGetOrCreateSimpleDb
func ExtensionGetOrCreateSimpleDb(
	bytes *C.uchar,
	size C.int,
	address *C.uchar,
	mvmId *C.uchar,
) C.struct_Extension_return {
	bCallData := C.GoBytes(unsafe.Pointer(bytes), size)
	logger.Error("Calling ExtensionGetOrCreateSimpleDb with data: ", hex.EncodeToString(bCallData))

	// ABI của hàm getOrCreateSimpleDb (bạn cần lấy từ hợp đồng Solidity)
	abiString := `[
	{
		"inputs": [
			{
				"internalType": "string",
				"name": "name",
				"type": "string"
			}
		],
		"name": "deleteDb",
		"outputs": [
			{
				"internalType": "bool",
				"name": "",
				"type": "bool"
			}
		],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "string",
				"name": "dbName",
				"type": "string"
			},
			{
				"internalType": "string",
				"name": "key",
				"type": "string"
			}
		],
		"name": "get",
		"outputs": [
			{
				"internalType": "string",
				"name": "",
				"type": "string"
			}
		],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "string",
				"name": "dbName",
				"type": "string"
			}
		],
		"name": "getAll",
		"outputs": [
			{
				"internalType": "string",
				"name": "",
				"type": "string"
			}
		],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "string",
				"name": "dbName",
				"type": "string"
			},
			{
				"internalType": "string",
				"name": "key",
				"type": "string"
			},
			{
				"internalType": "uint8",
				"name": "limit",
				"type": "uint8"
			}
		],
		"name": "getNextKeys",
		"outputs": [
			{
				"internalType": "string",
				"name": "",
				"type": "string"
			}
		],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "string",
				"name": "name",
				"type": "string"
			}
		],
		"name": "getOrCreateSimpleDb",
		"outputs": [
			{
				"internalType": "bool",
				"name": "",
				"type": "bool"
			}
		],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "string",
				"name": "dbName",
				"type": "string"
			},
			{
				"internalType": "string",
				"name": "value",
				"type": "string"
			}
		],
		"name": "searchByValue",
		"outputs": [
			{
				"internalType": "string",
				"name": "",
				"type": "string"
			}
		],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "string",
				"name": "dbName",
				"type": "string"
			},
			{
				"internalType": "string",
				"name": "key",
				"type": "string"
			},
			{
				"internalType": "string",
				"name": "value",
				"type": "string"
			}
		],
		"name": "set",
		"outputs": [
			{
				"internalType": "bool",
				"name": "",
				"type": "bool"
			}
		],
		"stateMutability": "nonpayable",
		"type": "function"
	}
]`
	parsedABI, err := abi.JSON(strings.NewReader(abiString))
	logger.Error("Calling ExtensionGetOrCreateSimpleDb parsedABI: ", parsedABI)

	if err != nil {
		logger.Error("Error parsing ABI: ", err) // Sử dụng logger thay vì log.Fatal
		return C.struct_Extension_return{data_p: nil, data_size: 0}
	}

	// Tìm method getOrCreateSimpleDb trong ABI
	method, err := parsedABI.MethodById(bCallData[0:4])
	if err != nil {
		logger.Error("Error finding method: ", err) // Sử dụng logger thay vì log.Fatal
		return C.struct_Extension_return{data_p: nil, data_size: 0}
	}
	logger.Error("method: ", method.Name)
	logger.Error("address", address)
	// Giải mã dữ liệu

	baddressId := C.GoBytes(unsafe.Pointer(address), 20)
	addressId := e_common.BytesToAddress(baddressId)
	logger.Error("address", addressId)

	bmvmId := C.GoBytes(unsafe.Pointer(mvmId), 20)
	addressMvmId := e_common.BytesToAddress(bmvmId)
	logger.Error("address", addressMvmId)

	decoded, err := method.Inputs.Unpack(bCallData[4:]) // Bỏ qua 4 bytes đầu tiên
	if err != nil {
		logger.Error("Error unpacking data: ", err) // Sử dụng logger thay vì log.Fatal
		return C.struct_Extension_return{data_p: nil, data_size: 0}
	}

	if method.Name == "deleteDb" {
		logger.Info("deleteDb")
		if len(decoded) < 1 {
			logger.Error("Missing arguments for deleteDb")
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		// Gọi hàm getOrCreateSimpleDb (bạn cần triển khai logic này)
		result, err := deleteDb(decoded[0].(string), addressMvmId, addressId) // Giả sử decoded[0] là string
		if err != nil {
			logger.Error("Error calling getOrCreateSimpleDb: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		// Mã hóa kết quả
		encodedResult, err := method.Outputs.Pack(result)
		if err != nil {
			logger.Error("Error packing result: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		data_size := C.int(len(encodedResult))
		data_p := (*C.uchar)(C.CBytes(encodedResult))
		return C.struct_Extension_return{
			data_p:    data_p,
			data_size: data_size,
		}
	}

	if method.Name == "getNextKeys" {
		logger.Info("set")
		if len(decoded) < 3 {
			logger.Error("Missing arguments for set")
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}

		limit, ok := decoded[2].(uint8)
		if !ok {
			logger.Error("decoded[2] is not of type uint8")
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		result, err := getNextKeys(decoded[0].(string), decoded[1].(string), int(limit), addressMvmId, addressId) // Giả sử decoded[0] là string
		if err != nil {
			logger.Error("Error calling setSimpledb: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		// Mã hóa kết quả
		encodedResult, err := method.Outputs.Pack(result)
		if err != nil {
			logger.Error("Error packing result: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		data_size := C.int(len(encodedResult))
		data_p := (*C.uchar)(C.CBytes(encodedResult))
		return C.struct_Extension_return{
			data_p:    data_p,
			data_size: data_size,
		}
	}

	if method.Name == "getOrCreateSimpleDb" {
		logger.Info("getOrCreateSimpleDb")
		if len(decoded) < 1 {
			logger.Error("Missing arguments for getOrCreateSimpleDb")
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		// Gọi hàm getOrCreateSimpleDb (bạn cần triển khai logic này)
		result, err := getOrCreateSimpleDb(decoded[0].(string), addressMvmId, addressId) // Giả sử decoded[0] là string
		if err != nil {
			logger.Error("Error calling getOrCreateSimpleDb: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		// Mã hóa kết quả
		encodedResult, err := method.Outputs.Pack(result)
		if err != nil {
			logger.Error("Error packing result: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		data_size := C.int(len(encodedResult))
		data_p := (*C.uchar)(C.CBytes(encodedResult))
		return C.struct_Extension_return{
			data_p:    data_p,
			data_size: data_size,
		}
	}

	if method.Name == "get" {
		if len(decoded) < 2 {
			logger.Error("Missing arguments for get")
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		result, err := getSimpledb(decoded[0].(string), decoded[1].(string), addressMvmId, addressId) // Giả sử decoded[0] là string
		if err != nil {
			logger.Error("Error calling getSimpledb: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		// Mã hóa kết quả
		encodedResult, err := method.Outputs.Pack(result)
		if err != nil {
			logger.Error("Error packing result: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		data_size := C.int(len(encodedResult))
		data_p := (*C.uchar)(C.CBytes(encodedResult))
		return C.struct_Extension_return{
			data_p:    data_p,
			data_size: data_size,
		}
	}

	if method.Name == "searchByValue" {
		logger.Info("searchByValue")
		if len(decoded) < 2 {
			logger.Error("Missing arguments for searchByValue")
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		result, err := searchByValue(decoded[0].(string), decoded[1].(string), addressMvmId, addressId) // Giả sử decoded[0] là string
		if err != nil {
			logger.Error("Error calling getSimpledb: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		// Mã hóa kết quả
		encodedResult, err := method.Outputs.Pack(result)
		if err != nil {
			logger.Error("Error packing result: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		data_size := C.int(len(encodedResult))
		data_p := (*C.uchar)(C.CBytes(encodedResult))
		return C.struct_Extension_return{
			data_p:    data_p,
			data_size: data_size,
		}
	}

	if method.Name == "set" {
		logger.Info("set")
		if len(decoded) < 3 {
			logger.Error("Missing arguments for set")
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		result, err := setSimpledb(decoded[0].(string), decoded[1].(string), decoded[2].(string), addressMvmId, addressId) // Giả sử decoded[0] là string
		if err != nil {
			logger.Error("Error calling setSimpledb: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		// Mã hóa kết quả
		encodedResult, err := method.Outputs.Pack(result)
		if err != nil {
			logger.Error("Error packing result: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		data_size := C.int(len(encodedResult))
		data_p := (*C.uchar)(C.CBytes(encodedResult))
		return C.struct_Extension_return{
			data_p:    data_p,
			data_size: data_size,
		}
	}

	if method.Name == "getAll" {
		logger.Info("getAll")
		if len(decoded) < 1 {
			logger.Error("Missing arguments for getAll")
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		// Gọi hàm getOrCreateSimpleDb (bạn cần triển khai logic này)
		result, err := getAllSimpledb(decoded[0].(string), addressMvmId, addressId) // Giả sử decoded[0] là string
		if err != nil {
			logger.Error("Error calling getSimpledb: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		// Mã hóa kết quả
		encodedResult, err := method.Outputs.Pack(result)
		if err != nil {
			logger.Error("Error packing result: ", err)
			return C.struct_Extension_return{data_p: nil, data_size: 0}
		}
		data_size := C.int(len(encodedResult))
		data_p := (*C.uchar)(C.CBytes(encodedResult))
		return C.struct_Extension_return{
			data_p:    data_p,
			data_size: data_size,
		}
	}

	return C.struct_Extension_return{data_p: nil, data_size: 0}

}
func getOrCreateSimpleDb(dbName string, addressMvmId e_common.Address, addressId e_common.Address) (bool, error) {
	logger.Info("getOrCreateSimpleDb", dbName, addressId)
	mvmApi := GetMVMApi(addressMvmId)
	as, err := mvmApi.accountStateDb.AccountState(addressId)
	if err != nil {
		return false, errors.New("get AccountState is nil")
	}
	hash := as.SmartContractState().GetTrieDatabaseMapValue(dbName)
	logger.Info("getOrCreateSimpleDb hash: ", hash)
	if hash == nil {
		trieMap := as.SmartContractState().TrieDatabaseMap()
		logger.Info("trieMap", trieMap)
		trieDBManager := trie_database.GetTrieDatabaseManager()
		logger.Info("trieDB: ", trieDBManager)

		key := crypto.Keccak256([]byte(dbName + addressId.String()))
		logger.Info("key: ", key)

		nTrie, ok := trieDBManager.GetOrCrateTrieDatabase(e_common.BytesToHash(key), e_common.Hash{}, addressMvmId, addressId, dbName)
		if !ok {
			return false, errors.New("nTrie is nil ok")
		}
		if nTrie == nil {
			return false, errors.New("nTrie is nil")
		}
		nTrie.Put(hex.EncodeToString(key), dbName)
		root, _ := nTrie.IntermediateRoot()
		as.SmartContractState().SetTrieDatabaseMapValue(dbName, root.Bytes())
		logger.Info("as sc1", as)
		mvmApi.accountStateDb.PublicSetDirtyAccountState(as)
		return true, nil
	}
	logger.Info("as sc2", as.SmartContractState())

	return true, nil

}

func deleteDb(dbName string, addressMvmId e_common.Address, addressId e_common.Address) (bool, error) {
	logger.Info("deleteDb")

	mvmApi := GetMVMApi(addressMvmId)
	as, err := mvmApi.accountStateDb.AccountState(addressId)
	if err != nil {
		return false, errors.New("get AccountState is nil")
	}
	hash := as.SmartContractState().GetTrieDatabaseMapValue(dbName)
	if hash == nil {
		return false, errors.New("db is nil")
	}
	trieDBManager := trie_database.GetTrieDatabaseManager()
	key := crypto.Keccak256([]byte(dbName + addressId.String()))
	logger.Info("key: ", key)

	nTrie, ok := trieDBManager.GetOrCrateTrieDatabase(e_common.BytesToHash(key), e_common.BytesToHash(hash), addressMvmId, addressId, dbName)
	if !ok {
		return false, errors.New("nTrie is nil ok")
	}
	nTrie.SetStatus(trie_database.Deleted)
	as.SmartContractState().DeleteTrieDatabaseMapValue(dbName)
	logger.Info("as sc1", as)
	mvmApi.accountStateDb.PublicSetDirtyAccountState(as)
	return true, nil

}

func getSimpledb(dbName string, name string, addressMvmId e_common.Address, addressId e_common.Address) (string, error) {
	mvmApi := GetMVMApi(addressMvmId)
	as, err := mvmApi.accountStateDb.AccountState(addressId)
	if err != nil {
		return "", errors.New("get AccountState is nil")
	}
	hash := as.SmartContractState().GetTrieDatabaseMapValue(dbName)
	if hash == nil {
		return "", errors.New("db is nil")
	}
	trieDBManager := trie_database.GetTrieDatabaseManager()
	key := crypto.Keccak256([]byte(dbName + addressId.String()))
	logger.Info("key: ", key)

	nTrie, ok := trieDBManager.GetOrCrateTrieDatabase(e_common.BytesToHash(key), e_common.BytesToHash(hash), addressMvmId, addressId, dbName)
	if !ok {
		return "", errors.New("nTrie is nil ok")
	}
	value, nErr := nTrie.Get(name)
	if nErr != nil {
		return value, errors.New("get value error")
	}
	return value, nil
}

func setSimpledb(dbName string, key string, value string, addressMvmId e_common.Address, addressId e_common.Address) (bool, error) {

	mvmApi := GetMVMApi(addressMvmId)
	as, _ := mvmApi.accountStateDb.AccountState(addressId)
	hash := as.SmartContractState().GetTrieDatabaseMapValue(dbName)
	if hash == nil {
		return false, errors.New("db is nil")
	}
	trieDBManager := trie_database.GetTrieDatabaseManager()

	keyTrie := crypto.Keccak256([]byte(dbName + addressId.String()))

	nTrie, ok := trieDBManager.GetOrCrateTrieDatabase(e_common.BytesToHash(keyTrie), e_common.BytesToHash(hash), addressMvmId, addressId, dbName)
	if !ok {
		return false, errors.New("nTrie is nil ok")
	}
	nErr := nTrie.Put(key, value)
	if nErr != nil {
		return false, errors.New("get value error")
	}
	return true, nil
}

func getNextKeys(dbName string, key string, limit int, addressMvmId e_common.Address, addressId e_common.Address) (string, error) {

	mvmApi := GetMVMApi(addressMvmId)
	as, _ := mvmApi.accountStateDb.AccountState(addressId)
	hash := as.SmartContractState().GetTrieDatabaseMapValue(dbName)
	if hash == nil {
		return "", errors.New("db is nil")
	}
	trieDBManager := trie_database.GetTrieDatabaseManager()

	keyTrie := crypto.Keccak256([]byte(dbName + addressId.String()))

	nTrie, ok := trieDBManager.GetOrCrateTrieDatabase(e_common.BytesToHash(keyTrie), e_common.BytesToHash(hash), addressMvmId, addressId, dbName)
	if !ok {
		return "", errors.New("nTrie is nil ok")
	}
	data, err := nTrie.GetNextKeys(key, limit)
	if err != nil {
		return "", errors.New("get value error")
	}
	valueJSON, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return string(valueJSON), nil
}

func getAllSimpledb(dbName string, addressMvmId e_common.Address, addressId e_common.Address) (string, error) {
	mvmApi := GetMVMApi(addressMvmId)
	as, err := mvmApi.accountStateDb.AccountState(addressId)
	if err != nil {
		return "", errors.New("get AccountState is nil")
	}
	hash := as.SmartContractState().GetTrieDatabaseMapValue(dbName)
	if hash == nil {
		return "", errors.New("db is nil")
	}
	trieDBManager := trie_database.GetTrieDatabaseManager()
	key := crypto.Keccak256([]byte(dbName + addressId.String()))
	logger.Info("key: ", key)

	nTrie, ok := trieDBManager.GetOrCrateTrieDatabase(e_common.BytesToHash(key), e_common.BytesToHash(hash), addressMvmId, addressId, dbName)
	if !ok {
		return "", errors.New("nTrie is nil ok")
	}
	value, nErr := nTrie.GetAllKeyValues()
	if nErr != nil {
		return "", errors.New("get value error")
	}
	// Convert the map to a JSON string
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return "", errors.New("error encoding to JSON")
	}
	return string(valueJSON), nil
}

func searchByValue(dbName string, name string, addressMvmId e_common.Address, addressId e_common.Address) (string, error) {
	mvmApi := GetMVMApi(addressMvmId)
	as, err := mvmApi.accountStateDb.AccountState(addressId)
	if err != nil {
		return "", errors.New("get AccountState is nil")
	}
	hash := as.SmartContractState().GetTrieDatabaseMapValue(dbName)
	if hash == nil {
		return "", errors.New("db is nil")
	}
	trieDBManager := trie_database.GetTrieDatabaseManager()
	key := crypto.Keccak256([]byte(dbName + addressId.String()))
	logger.Info("key: ", key)

	nTrie, ok := trieDBManager.GetOrCrateTrieDatabase(e_common.BytesToHash(key), e_common.BytesToHash(hash), addressMvmId, addressId, dbName)
	if !ok {
		return "", errors.New("nTrie is nil ok")
	}
	value, nErr := nTrie.SearchByValue(name)
	if nErr != nil {
		return "", errors.New("get value error")
	}
	// Convert the map to a JSON string
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return "", errors.New("error encoding to JSON")
	}
	return string(valueJSON), nil
}
