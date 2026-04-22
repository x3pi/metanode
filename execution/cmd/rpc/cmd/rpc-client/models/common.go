package models

import (
	"encoding/json"
)

type JSONRPCRequestRaw struct {
	Jsonrpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Id      interface{}     `json:"id"`
}
type RegisterBlsKeyParams struct {
	Address       string `json:"address"`
	BlsPrivateKey string `json:"blsPrivateKey"`
	Timestamp     string `json:"timestamp"`
	Signature     string `json:"signature"`
}
