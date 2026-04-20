package rpc_client

import (
	"github.com/ethereum/go-ethereum/common"
)

type CallObjectSchema struct {
	From                 string `json:"from"`
	To                   string `json:"to"`
	Gas                  string `json:"gas"`
	GasPrice             string `json:"gasPrice"`
	MaxFeePerGas         string `json:"maxFeePerGas"`
	MaxPriorityFeePerGas string `json:"maxPriorityFeePerGas"`
	Value                string `json:"value"`
	Nonce                string `json:"nonce"`
	Data                 string `json:"data"`
	Input                string `json:"input"`
}

type DecodedCallObject struct {
	Schema      CallObjectSchema
	FromAddress common.Address
	ToAddress   common.Address
	HasTo       bool
	Payload     []byte
}
