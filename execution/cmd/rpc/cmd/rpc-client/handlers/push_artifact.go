package handlers

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/app"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/models"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/utils"
	"github.com/meta-node-blockchain/meta-node/pkg/complier"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/models/gen_bytecode"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rpc_client"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// PushArtifactParams chứa các tham số để push artifact
type PushArtifactParams struct {
	ContractAddress string `json:"contract_address"` // Contract address (required)
	SourceCode      string `json:"source_code"`      // Full source code (all .sol files, JSON encoded)
	SourceMap       string `json:"source_map"`       // Source map string
	StorageLayout   string `json:"storage_layout"`   // Storage layout JSON string
	Metadata        string `json:"metadata"`         // Metadata/config.json (JSON string) - required, contains all compiler settings including ABI
	VerifyBytecode  bool   `json:"verify_bytecode"`  // Whether to verify bytecode on-chain (default: true)
}

// HandlePushArtifact xử lý RPC method rpc_pushArtifact
func HandlePushArtifact(appCtx *app.Context, req models.JSONRPCRequestRaw) rpc_client.JSONRPCResponse {
	if appCtx.LdbArtifactRegistry == nil {
		return utils.MakeInternalError(req.Id, "Artifact Registry storage not initialized")
	}

	var params PushArtifactParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		logger.Error("Failed to unmarshal push artifact params: %v", err)
		return utils.MakeInvalidParamError(req.Id, "Invalid params for rpc_pushArtifact: "+err.Error())
	}

	if params.SourceCode == "" {
		return utils.MakeInvalidParamError(req.Id, "source_code is required")
	}
	if params.Metadata == "" {
		return utils.MakeInvalidParamError(req.Id, "metadata is required for bytecode generation")
	}
	if params.ContractAddress == "" {
		return utils.MakeInvalidParamError(req.Id, "contract_address is required")
	}
	// --- BƯỚC 1: Compile bytecode từ metadata và source code ---
	compiledBytecodes, err := complier.CompileBytecodeFromMetadata(params.Metadata, params.SourceCode)
	if err != nil {
		logger.Error("Failed to compile bytecode: %v", err)
		return utils.MakeInternalError(req.Id, "Failed to compile bytecode: "+err.Error())
	}
	logger.Info("✅ Compiled bytecode successfully, creation length: %s, deployed length: %s",
		compiledBytecodes.CreationBytecode, compiledBytecodes.DeployedBytecode)

	// --- BƯỚC 2: Deploy bytecode compile lên chain → nhận bytecode 1 ---
	logger.Info("🚀 Deploying compiled bytecode to chain...")
	bytecodeFromDeploy, err := deployOnChain(appCtx, compiledBytecodes.CreationBytecode)
	if err != nil {
		logger.Error("Failed to deploy bytecode: %v", err)
		return utils.MakeInternalError(req.Id, "Failed to deploy bytecode: "+err.Error())
	}
	logger.Info("✅ Deployed bytecode received, length: %d", len(bytecodeFromDeploy))

	// --- BƯỚC 3: Lấy bytecode từ chain bằng contract address từ params (bytecode 2) ---
	logger.Info("📥 Getting bytecode from chain using contract address: %s", params.ContractAddress)
	bytecodeFromChain, err := getBytecodeFromChain(appCtx, params.ContractAddress)
	if err != nil {
		logger.Error("Failed to get bytecode from chain: %v", err)
		return utils.MakeInternalError(req.Id, "Failed to get bytecode from chain: "+err.Error())
	}
	logger.Info("✅ Got bytecode from chain, length: %d", len(bytecodeFromChain))
	// --- BƯỚC 4: So sánh bytecode 1 (từ deploy) và bytecode 2 (từ chain) ---
	// Chuẩn hóa: remove 0x prefix và so sánh
	bytecodeFromDeployNormalized := strings.TrimPrefix(strings.ToLower(bytecodeFromDeploy), "0x")
	bytecodeFromChainNormalized := strings.TrimPrefix(strings.ToLower(bytecodeFromChain), "0x")

	// So sánh bytecode từ deploy và bytecode từ chain
	bytecodeMatch := false
	if len(bytecodeFromDeployNormalized) == len(bytecodeFromChainNormalized) {
		// So sánh toàn bộ nếu độ dài bằng nhau
		bytecodeMatch = (bytecodeFromDeployNormalized == bytecodeFromChainNormalized)
	} else if len(bytecodeFromDeployNormalized) < len(bytecodeFromChainNormalized) {
		// Nếu chain bytecode dài hơn, có thể có thêm metadata, so sánh phần đầu
		if strings.HasPrefix(bytecodeFromChainNormalized, bytecodeFromDeployNormalized) {
			bytecodeMatch = true
		}
	} else {
		// Nếu deploy bytecode dài hơn, so sánh phần đầu
		if strings.HasPrefix(bytecodeFromDeployNormalized, bytecodeFromChainNormalized) {
			bytecodeMatch = true
		}
	}

	if !bytecodeMatch {
		logger.Error("❌ Bytecode mismatch! From deploy (first 50): %s, From chain (first 50): %s",
			bytecodeFromDeployNormalized,
			bytecodeFromChainNormalized)
		logger.Error("Deploy length: %d, Chain length: %d", len(bytecodeFromDeployNormalized), len(bytecodeFromChainNormalized))
		return utils.MakeInternalError(req.Id, "Bytecode verification failed: deployed bytecode does not match bytecode from chain")
	}
	logger.Info("✅ Bytecode verification passed!")

	// --- BƯỚC 5: Tính toán bytecode hash và lưu vào leveldb ---
	bytecodeHash, err := storage.CalculateBytecodeHash(bytecodeFromChain)
	if err != nil {
		return utils.MakeInternalError(req.Id, "Failed to calculate bytecode hash: "+err.Error())
	}

	verified := true

	// Parse metadata để lấy các giá trị cần thiết cho CalculateArtifactID
	compilerSettings, err := extractCompilerSettingsFromMetadata(params.Metadata)
	if err != nil {
		logger.Error("Failed to extract compiler settings from metadata: %v", err)
		return utils.MakeInternalError(req.Id, "Failed to parse metadata: "+err.Error())
	}

	// Calculate artifact_id after we have bytecode_hash và compiler settings
	artifactID := storage.CalculateArtifactID(
		bytecodeHash,
		compilerSettings.SolcVersion,
		compilerSettings.OptimizerSettings,
		compilerSettings.EVMVersion,
		compilerSettings.LinkedLibraries,
	)
	logger.Info("Compiler Settings: %v", compilerSettings)
	logger.Info("Artifact ID: %s", artifactID)

	// Create artifact data
	artifactData := &pb.ArtifactData{
		ArtifactId:      artifactID,
		BytecodeHash:    bytecodeHash,
		Metadata:        params.Metadata,
		Abi:             compilerSettings.ABI,
		SourceCode:      params.SourceCode,
		SourceMap:       params.SourceMap,
		StorageLayout:   params.StorageLayout,
		ContractAddress: params.ContractAddress,
		CreatedAt:       time.Now().Unix(),
		Verified:        verified,
		VerifiedAt:      0,
	}

	if verified {
		artifactData.VerifiedAt = time.Now().Unix()
	}

	// Save to storage
	if err := appCtx.LdbArtifactRegistry.SaveArtifact(artifactData); err != nil {
		logger.Error("Failed to save artifact: %v", err)
		return utils.MakeInternalError(req.Id, "Failed to save artifact: "+err.Error())
	}

	logger.Info("✅ Artifact pushed successfully: artifact_id=%s, contract=%s, verified=%v",
		artifactID, params.ContractAddress, verified)

	// Return success response
	return rpc_client.JSONRPCResponse{
		Jsonrpc: "2.0",
		Result: map[string]interface{}{
			"artifact_id":      artifactID,
			"bytecode_hash":    bytecodeHash,
			"contract_address": params.ContractAddress,
			"verified":         verified,
		},
		Id: req.Id,
	}
}

// getBytecodeFromChain lấy bytecode từ chain bằng eth_getCode
func getBytecodeFromChain(appCtx *app.Context, contractAddress string) (string, error) {
	// Validate address
	if !common.IsHexAddress(contractAddress) {
		return "", fmt.Errorf("invalid contract address: %s", contractAddress)
	}

	// Call eth_getCode
	request := &rpc_client.JSONRPCRequest{
		Jsonrpc: "2.0",
		Method:  "eth_getCode",
		Params:  []interface{}{contractAddress, "latest"},
		Id:      1,
	}

	response := appCtx.ClientRpc.SendHTTPRequest(request)
	if response.Error != nil {
		return "", fmt.Errorf("RPC error: code=%d, message=%s", response.Error.Code, response.Error.Message)
	}

	bytecodeHex, ok := response.Result.(string)
	if !ok {
		return "", fmt.Errorf("invalid bytecode result type: %T", response.Result)
	}

	// Remove 0x prefix for consistency
	bytecodeHex = strings.TrimPrefix(bytecodeHex, "0x")
	if bytecodeHex == "" {
		return "", fmt.Errorf("bytecode is empty")
	}

	return "0x" + bytecodeHex, nil
}
func deployOnChain(appCtx *app.Context, bytecode string) (string, error) {
	// Đảm bảo bytecode có prefix 0x
	if !strings.HasPrefix(bytecode, "0x") {
		bytecode = "0x" + bytecode
	}

	// Tạo request gọi method eth_deployContract
	request := &rpc_client.JSONRPCRequest{
		Jsonrpc: "2.0",
		Method:  "eth_deployContract",
		Params:  []interface{}{bytecode},
		Id:      1,
	}

	// Gửi request qua ClientRpc (giống như cách làm trong getBytecodeFromChain)
	response := appCtx.ClientRpc.SendHTTPRequest(request)

	if response.Error != nil {
		return "", fmt.Errorf("RPC error (eth_deployContract): code=%d, message=%s",
			response.Error.Code, response.Error.Message)
	}
	// Response trả về bytecode (hex string)
	deployedBytecode, ok := response.Result.(string)
	if !ok {
		return "", fmt.Errorf("invalid response type from eth_deployContract: %T", response.Result)
	}

	// Chuẩn hóa: xóa prefix 0x để dễ so sánh hoặc lưu trữ
	deployedBytecode = strings.TrimPrefix(deployedBytecode, "0x")

	if deployedBytecode == "" {
		return "", fmt.Errorf("deployed bytecode is empty")
	}

	return "0x" + deployedBytecode, nil
}

// CompilerSettings chứa các giá trị compiler settings được extract từ metadata
type CompilerSettings struct {
	SolcVersion       string // Từ metadata.compiler.version
	OptimizerSettings string // JSON string từ metadata.settings.optimizer
	EVMVersion        string // Từ metadata.settings.evmVersion
	LinkedLibraries   string // JSON string từ metadata.settings.libraries
	ABI               string // JSON string từ metadata.output.abi
}

// extractCompilerSettingsFromMetadata parse metadata và extract các compiler settings
func extractCompilerSettingsFromMetadata(metadataJSON string) (*CompilerSettings, error) {
	var config gen_bytecode.ConfigFile
	if err := json.Unmarshal([]byte(metadataJSON), &config); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	// Extract solc version (bỏ phần commit hash nếu có)
	solcVersion := config.Compiler.Version
	if idx := strings.Index(solcVersion, "+"); idx > 0 {
		solcVersion = solcVersion[:idx]
	}

	// Extract optimizer settings (serialize thành JSON)
	optimizerJSON, err := json.Marshal(config.Settings.Optimizer)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal optimizer settings: %w", err)
	}
	optimizerSettings := string(optimizerJSON)

	// Extract EVM version
	evmVersion := config.Settings.EVMVersion
	if evmVersion == "" {
		evmVersion = "default" // Fallback nếu không có
	}

	// Extract linked libraries (serialize thành JSON)
	librariesJSON, err := json.Marshal(config.Settings.Libraries)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal libraries: %w", err)
	}
	linkedLibraries := string(librariesJSON)

	abiJson, err := json.Marshal(config.Output.ABI)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal libraries: %w", err)
	}
	abi := string(abiJson)

	return &CompilerSettings{
		SolcVersion:       solcVersion,
		OptimizerSettings: optimizerSettings,
		EVMVersion:        evmVersion,
		LinkedLibraries:   linkedLibraries,
		ABI:               abi,
	}, nil
}
