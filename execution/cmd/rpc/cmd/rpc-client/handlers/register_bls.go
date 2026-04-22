package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	ethCommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/app"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/models"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/utils"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/rpc_client"
)

func HandleRpcRegisterBlsKeyWithSignatureRaw(appCtx *app.Context, param json.RawMessage, id interface{}) rpc_client.JSONRPCResponse {
	var params models.RegisterBlsKeyParams
	if err := json.Unmarshal(param, &params); err != nil {
		return utils.MakeInvalidParamError(id, "Invalid parameters for rpc_registerBlsKeyWithSignature: "+err.Error())
	}

	return processRegisterBlsKeyParams(appCtx, params, id)
}
func HandleRpcRegisterBlsKeyWithSignature(appCtx *app.Context, req models.JSONRPCRequestRaw) rpc_client.JSONRPCResponse {
	var rawParams []json.RawMessage
	if err := json.Unmarshal(req.Params, &rawParams); err != nil || len(rawParams) == 0 {
		return utils.MakeInvalidParamError(req.Id, "Cannot unmarshal params or params array is empty for rpc_registerBlsKeyWithSignature")
	}

	var params models.RegisterBlsKeyParams
	if err := json.Unmarshal(rawParams[0], &params); err != nil {
		return utils.MakeInvalidParamError(req.Id, "Invalid parameters for rpc_registerBlsKeyWithSignature: "+err.Error())
	}

	return processRegisterBlsKeyParams(appCtx, params, req.Id)
}
func processRegisterBlsKeyParams(appCtx *app.Context, params models.RegisterBlsKeyParams, id interface{}) rpc_client.JSONRPCResponse {
	if !ethCommon.IsHexAddress(params.Address) {
		return utils.MakeInvalidParamError(id, "Invalid Ethereum address format.")
	}
	signerAddress := ethCommon.HexToAddress(params.Address)

	if !strings.HasPrefix(params.BlsPrivateKey, "0x") || len(params.BlsPrivateKey) != 66 {
		return utils.MakeInvalidParamError(id, "Invalid BLS private key format. Expected 0x prefixed 32-byte hex string.")
	}
	blsPrivKeyBytes, releaseBls, err := utils.DecodeHexPooled(params.BlsPrivateKey)
	if err != nil || len(blsPrivKeyBytes) != 32 {
		releaseBls()
		return utils.MakeInvalidParamError(id, "Invalid BLS private key hex data.")
	}

	// Validate BLS private key using proper BLS validation
	if !bls.ValidateBlsPrivateKey(blsPrivKeyBytes) {
		releaseBls()
		return utils.MakeInvalidParamError(id, "Invalid BLS private key: key is invalid or out of bounds.")
	}

	// Use comprehensive BLS validation
	clientTimestamp, err := time.Parse(time.RFC3339Nano, params.Timestamp)
	if err != nil {
		clientTimestamp, err = time.Parse(time.RFC3339, params.Timestamp)
		if err != nil {
			return utils.MakeInvalidParamError(id, "Invalid timestamp format. Expected ISO 8601.")
		}
	}

	if time.Since(clientTimestamp).Abs() > 2*time.Minute {
		return utils.MakeAuthError(id, "Timestamp is too old or in the future.")
	}
	messageToVerify := fmt.Sprintf("BLS Data: %s\nTimestamp: %s", params.BlsPrivateKey, params.Timestamp)
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(messageToVerify), messageToVerify)
	messageHash := crypto.Keccak256Hash([]byte(prefixedMessage))

	signatureBytes, releaseSig, err := utils.DecodeHexPooled(params.Signature)
	if err != nil {
		releaseSig()
		return utils.MakeInvalidParamError(id, "Invalid signature hex data: "+err.Error())
	}
	defer releaseSig()
	if len(signatureBytes) == 65 && (signatureBytes[64] == 27 || signatureBytes[64] == 28) {
		signatureBytes[64] -= 27
	}

	recoveredSigPubKeyBytes, err := crypto.Ecrecover(messageHash.Bytes(), signatureBytes)
	if err != nil {
		return utils.MakeAuthError(id, "Signature verification failed: could not recover public key.")
	}
	unmarshaledRecPubKey, err := crypto.UnmarshalPubkey(recoveredSigPubKeyBytes)
	if err != nil {
		return utils.MakeAuthError(id, "Signature verification failed: could not unmarshal recovered public key.")
	}
	recoveredAddress := crypto.PubkeyToAddress(*unmarshaledRecPubKey)

	if !bytes.Equal(recoveredAddress.Bytes(), signerAddress.Bytes()) {
		return utils.MakeAuthError(id, "Signature verification failed: address mismatch.")
	}

	if appCtx.PKS == nil {
		return utils.MakeInternalError(id, "Internal server error: Private key store not available.")
	}

	err = appCtx.PKS.SetPrivateKey(signerAddress, params.BlsPrivateKey)
	if err != nil {
		return utils.MakeInternalError(id, fmt.Sprintf("Failed to store BLS private key: %v", err))
	}

	return utils.MakeSuccessResponse(id, "BLS private key successfully registered.")
}
