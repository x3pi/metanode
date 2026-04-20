package handlers

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/app"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/models"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/utils"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/rpc_client"
)

func HandleDeployContract(appCtx *app.Context, req models.JSONRPCRequestRaw) rpc_client.JSONRPCResponse {
	var paramsList []json.RawMessage
	if err := json.Unmarshal(req.Params, &paramsList); err != nil || len(paramsList) == 0 {
		return utils.MakeInvalidParamError(req.Id, "Cannot unmarshal params for eth_deployContract")
	}
	// Parse constructor bytecode từ params[0]
	var constructorBytecodeHex string
	if err := json.Unmarshal(paramsList[0], &constructorBytecodeHex); err != nil {
		logger.Error("Failed to unmarshal constructor bytecode: %v", err)
		return utils.MakeInvalidParamError(req.Id, "Invalid constructor bytecode parameter")
	}

	// Convert hex string to hexutil.Bytes
	constructorBytecode, err := hexutil.Decode(constructorBytecodeHex)
	if err != nil {
		logger.Error("Failed to decode constructor bytecode: %v", err)
		return utils.MakeInvalidParamError(req.Id, "Invalid hex string for constructor bytecode: "+err.Error())
	}

	// Gọi SendDeployContract để lấy runtime bytecode
	rs := appCtx.ClientRpc.SendDeployContract(hexutil.Bytes(constructorBytecode))
	rs.Id = req.Id
	return rs
}
