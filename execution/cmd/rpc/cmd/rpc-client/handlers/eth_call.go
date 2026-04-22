package handlers

import (
	"context"
	"encoding/json"

	ethCommon "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/app"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/models"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/utils"
	"github.com/meta-node-blockchain/meta-node/pkg/account_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	robothandler "github.com/meta-node-blockchain/meta-node/pkg/robot_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/rpc_client"
)

func HandleEthCall(appCtx *app.Context, req models.JSONRPCRequestRaw) rpc_client.JSONRPCResponse {
	var callParamsList []json.RawMessage
	if err := json.Unmarshal(req.Params, &callParamsList); err != nil || len(callParamsList) == 0 {
		return utils.MakeInvalidParamError(req.Id, "Cannot unmarshal params for eth_call")
	}
	return processEthCallParams(appCtx, req.Id, callParamsList[0])
}

func HandleEthCallRaw(appCtx *app.Context, callParam json.RawMessage, id interface{}) rpc_client.JSONRPCResponse {
	return processEthCallParams(appCtx, id, callParam)
}

func processEthCallParams(appCtx *app.Context, id interface{}, callObjectRaw json.RawMessage) rpc_client.JSONRPCResponse {
	decoded, err := utils.DecodeCallObject(callObjectRaw)
	if err != nil {
		return utils.MakeInvalidParamError(id, "Invalid eth_call parameter")
	}
	if decoded.HasTo && decoded.ToAddress == ethCommon.HexToAddress(appCtx.Cfg.ContractsInterceptor[0]) {
		accountHandler, err := account_handler.GetAccountHandler(appCtx)
		if err != nil {
			logger.Error("Failed to get account handler: %v", err)
			return utils.MakeInternalError(id, "Failed to get account handler: "+err.Error())
		}
		// Handle eth_call cho account operations
		result, err := accountHandler.HandleEthCall(context.Background(), decoded.Payload, decoded.FromAddress)
		if err != nil {
			// logger.Error("Account handler eth_call error: %v", err)
			return utils.MakeInternalError(id, "Account handler error: "+err.Error())
		}
		if result != nil && err == nil {
			// Encode result thành JSON hex string
			jsonBytes, err := json.Marshal(result)
			if err != nil {
				return utils.MakeInternalError(id, "Failed to encode result: "+err.Error())
			}

			// Convert JSON to hex string (0x...)
			hexResult := "0x" + ethCommon.Bytes2Hex(jsonBytes)
			return rpc_client.JSONRPCResponse{
				Jsonrpc: "2.0",
				Result:  hexResult,
				Id:      id,
			}
		}
	}
	// robot contract
	if decoded.HasTo && decoded.ToAddress == ethCommon.HexToAddress(appCtx.Cfg.ContractsInterceptor[1]) {
		robotHandler, err := robothandler.GetRobotHandler(appCtx)
		if err != nil {
			logger.Error("Failed to get robot handler: %v", err)
			return utils.MakeInternalError(id, "Failed to get robot handler: "+err.Error())
		}
		result, err := robotHandler.HandleEthCall(context.Background(), decoded.Payload)
		if err != nil {
			// logger.Error("Account handler eth_call error: %v", err)
			return utils.MakeInternalError(id, "Robot handler error: "+err.Error())
		}
		if result != nil && err == nil {
			// Encode result thành JSON hex string
			jsonBytes, err := json.Marshal(result)
			if err != nil {
				return utils.MakeInternalError(id, "Failed to encode result robot: "+err.Error())
			}
			// Convert JSON to hex string (0x...)
			hexResult := "0x" + ethCommon.Bytes2Hex(jsonBytes)
			return rpc_client.JSONRPCResponse{
				Jsonrpc: "2.0",
				Result:  hexResult,
				Id:      id,
			}
		}
	}
	var bTx []byte
	var buildErr error

	// ✅ Sử dụng appCtx.ClientRpc
	if !decoded.HasTo {
		bTx, buildErr = appCtx.ClientRpc.BuildDeployTransaction(decoded)
	} else {
		bTx, buildErr = appCtx.ClientRpc.BuildCallTransaction(decoded)
	}

	if buildErr != nil {
		return utils.MakeInternalError(id, "Failed to build transaction for eth_call "+buildErr.Error())
	}

	rs := appCtx.ClientRpc.SendCallTransaction(bTx)
	// Sử dụng helper function để tránh code lặp lại
	// Hàm này tự động extract revert data và decode error
	if decoded.HasTo && appCtx.ErrorDecoder != nil {
		appCtx.ErrorDecoder.DecodeError(
			context.Background(),
			&rs,
			decoded.ToAddress.Hex(),
			0,
		)
	}
	rs.Id = id
	if rs.Error != nil {
		logger.Info("2__✅✅✅ rs.Error.Message : %s Data %s Code %d", rs.Error.Message, rs.Error.Data, rs.Error.Code)
	}

	return rs
}
