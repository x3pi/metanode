package complier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/models/gen_bytecode"
)

// compileBytecodeFromMetadata compile bytecode từ metadata (config.json) và source code
func CompileBytecodeFromMetadata(metadataJSON string, sourceCodeJSON string) (*gen_bytecode.CompiledBytecodes, error) {
	logger.Info("=== Starting CompileBytecodeFromMetadata ===")
	logger.Info("Metadata length: %d", len(metadataJSON))
	logger.Info("Source code length: %v", sourceCodeJSON)

	// Parse metadata (config.json)
	var config gen_bytecode.ConfigFile
	if err := json.Unmarshal([]byte(metadataJSON), &config); err != nil {
		logger.Error("Failed to parse metadata: %v", err)
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}
	logger.Info("✅ Parsed metadata successfully")
	logger.Info("Compiler version: %s", config.Compiler.Version)
	logger.Info("Language: %s", config.Language)

	// Parse source code (map[string]string: filepath -> content)
	var sourceCodeMap map[string]string
	if err := json.Unmarshal([]byte(sourceCodeJSON), &sourceCodeMap); err != nil {
		logger.Error("Failed to parse source code: %v", err)
		return nil, fmt.Errorf("failed to parse source code: %w", err)
	}
	logger.Info("✅ Parsed source code successfully")
	logger.Info("Source files count: %d", len(sourceCodeMap))
	for filePath := range sourceCodeMap {
		logger.Info("  - Source file: %s", filePath)
	}

	// Trích xuất compilation target
	var mainFileName string
	var mainContractName string
	if len(config.Settings.CompilationTarget) > 0 {
		for f, c := range config.Settings.CompilationTarget {
			mainFileName = f
			mainContractName = c
			break
		}
		logger.Info("🎯 Compilation target: File=%s, Contract=%s", mainFileName, mainContractName)
	} else {
		logger.Error("compilationTarget not found in metadata")
		return nil, fmt.Errorf("compilationTarget not found in metadata")
	}

	// Kiểm tra xem mainFileName có trong sourceCodeMap không
	if _, exists := sourceCodeMap[mainFileName]; !exists {
		logger.Warn("⚠️ Main file '%s' not found in sourceCodeMap. Available files:", mainFileName)
		for filePath := range sourceCodeMap {
			logger.Warn("  - Available: %s", filePath)
		}
		// Thử tìm file tương tự (case-insensitive hoặc chỉ tên file)
		for filePath := range sourceCodeMap {
			// Lấy tên file cuối cùng từ path
			parts := strings.Split(filePath, "/")
			fileName := parts[len(parts)-1]
			if fileName == mainFileName || strings.HasSuffix(filePath, mainFileName) {
				logger.Info("🔄 Found matching file: %s -> using as main file", filePath)
				mainFileName = filePath
				break
			}
		}
		// Nếu vẫn không tìm thấy, dùng file đầu tiên
		if _, stillNotExists := sourceCodeMap[mainFileName]; stillNotExists {
			logger.Warn("⚠️ Still not found, using first available file")
			for filePath := range sourceCodeMap {
				mainFileName = filePath
				logger.Info("🔄 Using: %s", mainFileName)
				break
			}
		}
	}
	// Parse version từ config
	configVersion := parseVersionFromConfig(config.Compiler.Version)
	logger.Info("Parsed version: %s", configVersion)
	useSolcJS := checkNodeJS()
	logger.Info("Using solc-js: %v", useSolcJS)

	// Xác định solc version
	var solcVersion string
	if useSolcJS {
		solcVersion = configVersion
		logger.Info("Solc version: %s (from config)", solcVersion)
	} else {
		solcPath := "./solc"
		var versionErr error
		solcVersion, versionErr = getSolcVersion(solcPath)
		if versionErr != nil {
			logger.Warn("Could not get solc version: %v", versionErr)
			solcVersion = ""
		} else {
			logger.Info("Solc version: %s (from binary)", solcVersion)
		}
	}

	// Tạo sources map từ sourceCodeMap
	sources := make(map[string]gen_bytecode.Source)
	logger.Info("Processing %d source files...", len(sourceCodeMap))
	for filePath, content := range sourceCodeMap {
		originalContent := content
		// Điều chỉnh pragma version nếu cần
		if solcVersion != "" {
			content = adjustPragmaVersion(content, solcVersion)
			if content != originalContent {
				logger.Info("  - Adjusted pragma version for: %s", filePath)
			}
		}
		sources[filePath] = gen_bytecode.Source{Content: content}
		logger.Info("  ✅ Added source: %s (length: %d)", filePath, len(content))
	}
	// Tải các file từ IPFS nếu cần (từ config.Sources)
	for sourcePath, sourceMeta := range config.Sources {
		// Nếu đã có trong sourceCodeMap thì bỏ qua
		if _, exists := sources[sourcePath]; exists {
			logger.Info("  ⏭ Skipping %s (already in sourceCodeMap)", sourcePath)
			continue
		}

		// Tải từ IPFS
		var ipfsURL string
		for _, url := range sourceMeta.URLs {
			if strings.HasPrefix(url, "dweb:/ipfs/") {
				ipfsURL = url
				break
			}
		}
		if ipfsURL == "" {
			logger.Info("  ⏭ Skipping %s (no IPFS URL)", sourcePath)
			continue
		}

		logger.Info("  📥 Downloading %s from IPFS: %s", sourcePath, ipfsURL)
		content, err := downloadFromIPFS(ipfsURL)
		if err != nil {
			logger.Warn("  ❌ Failed to download %s from IPFS: %v", sourcePath, err)
			continue
		}

		contentStr := string(content)
		if solcVersion != "" {
			contentStr = adjustPragmaVersion(contentStr, solcVersion)
		}
		sources[sourcePath] = gen_bytecode.Source{Content: contentStr}
		logger.Info("  ✅ Downloaded and added: %s (length: %d)", sourcePath, len(contentStr))
	}
	logger.Info("Final sources count: %d", len(sources))

	// Cấu hình output selection - lấy cả creation và deployed bytecode
	config.Settings.CompilationTarget = nil
	config.Settings.OutputSelection = map[string]map[string][]string{
		mainFileName: {
			mainContractName: {"evm.bytecode.object", "evm.deployedBytecode.object"},
		},
	}
	logger.Info("Output selection configured for: %s -> %s", mainFileName, mainContractName)

	// Tạo input cho solc
	input := gen_bytecode.SolcInput{
		Language: config.Language,
		Sources:  sources,
		Settings: config.Settings,
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		logger.Error("Failed to marshal input: %v", err)
		return nil, fmt.Errorf("failed to marshal input: %w", err)
	}
	logger.Info("✅ Input JSON prepared (length: %d)", len(inputJSON))

	// Compile
	logger.Info("Starting compilation...")
	var outputBytes []byte
	if useSolcJS {
		logger.Info("Using solc-js version: %s", configVersion)
		outputBytes, err = compileWithSolcJS(inputJSON, configVersion)
		if err != nil {
			logger.Error("❌ Compilation failed with solc-js: %v", err)
			return nil, fmt.Errorf("failed to compile with solc-js: %w", err)
		}
		logger.Info("✅ Compilation completed with solc-js")
	} else {
		solcPath := "./solc"
		logger.Info("Using solc binary: %s", solcPath)
		cmd := exec.Command(solcPath, "--standard-json")
		cmd.Stdin = bytes.NewReader(inputJSON)
		var out bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			logger.Error("❌ Compilation failed with solc binary: %v", err)
			logger.Error("Stderr: %s", stderr.String())
			return nil, fmt.Errorf("failed to run solc: %w, stderr: %s", err, stderr.String())
		}
		outputBytes = out.Bytes()
		logger.Info("✅ Compilation completed with solc binary")
	}
	logger.Info("Output length: %d bytes", len(outputBytes))

	// Parse output
	logger.Info("Parsing solc output...")
	var output gen_bytecode.SolcOutput
	if err := json.Unmarshal(outputBytes, &output); err != nil {
		logger.Error("❌ Failed to parse solc output: %v", err)
		logger.Error("Output preview (first 500 chars): %s", string(outputBytes[:min(500, len(outputBytes))]))
		return nil, fmt.Errorf("failed to parse solc output: %w", err)
	}
	logger.Info("✅ Parsed solc output successfully")

	// Kiểm tra lỗi
	logger.Info("Checking compilation errors...")
	hasErrors := false
	for _, e := range output.Errors {
		logger.Info("  - Error [%s]: %s", e.Severity, e.Message)
		if e.Severity == "error" {
			hasErrors = true
		}
	}
	if hasErrors {
		logger.Error("❌ Compilation failed with errors")
		// Log tất cả errors
		for _, e := range output.Errors {
			if e.Severity == "error" {
				logger.Error("  ERROR: %s", e.Message)
			}
		}
		return nil, fmt.Errorf("compilation error: see logs for details")
	}
	logger.Info("✅ No compilation errors")

	// Lấy cả creation và deployed bytecode
	logger.Info("Extracting bytecode from output...")
	logger.Info("Looking for file: %s, contract: %s", mainFileName, mainContractName)
	logger.Info("Available files in output:")
	for fileName := range output.Contracts {
		logger.Info("  - File: %s", fileName)
		for contractName := range output.Contracts[fileName] {
			logger.Info("    - Contract: %s", contractName)
		}
	}

	result := &gen_bytecode.CompiledBytecodes{}
	if fileContracts, ok := output.Contracts[mainFileName]; ok {
		if contractData, ok := fileContracts[mainContractName]; ok {
			result.CreationBytecode = contractData.EVM.Bytecode.Object
			result.DeployedBytecode = contractData.EVM.DeployedBytecode.Object

			logger.Info("Creation bytecode length: %d", len(result.CreationBytecode))
			logger.Info("Deployed bytecode length: %d", len(result.DeployedBytecode))

			if result.CreationBytecode == "" {
				logger.Error("❌ Creation bytecode is empty")
				return nil, fmt.Errorf("creation bytecode is empty")
			}
			if result.DeployedBytecode == "" {
				logger.Warn("⚠️ Deployed bytecode is empty, using creation bytecode as fallback")
				// Nếu không có deployed bytecode, dùng creation bytecode làm fallback
				result.DeployedBytecode = result.CreationBytecode
			}
			return result, nil
		} else {
			logger.Error("❌ Contract '%s' not found in file '%s'", mainContractName, mainFileName)
			logger.Error("Available contracts in this file:")
			for contractName := range fileContracts {
				logger.Error("  - %s", contractName)
			}
		}
	} else {
		logger.Error("❌ File '%s' not found in output", mainFileName)
		logger.Error("Available files:")
		for fileName := range output.Contracts {
			logger.Error("  - %s", fileName)
		}
	}

	return nil, fmt.Errorf("contract %s not found in output (file: %s)", mainContractName, mainFileName)
}

// Helper functions từ main.go

func parseVersionFromConfig(versionStr string) string {
	parts := strings.Split(versionStr, "+")
	if len(parts) > 0 {
		return parts[0]
	}
	return versionStr
}

func checkNodeJS() bool {
	cmd := exec.Command("node", "--version")
	if err := cmd.Run(); err != nil {
		return false
	}
	cmd = exec.Command("npx", "--version")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

func compileWithSolcJS(inputJSON []byte, version string) ([]byte, error) {
	cmd := exec.Command("npx", "-y", fmt.Sprintf("solc@%s", version), "--standard-json")
	cmd.Stdin = bytes.NewReader(inputJSON)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("solc-js error: %v, stderr: %s", err, stderr.String())
	}

	output := out.Bytes()
	jsonStart := bytes.IndexByte(output, '{')
	if jsonStart == -1 {
		return nil, fmt.Errorf("no JSON found in output. stdout: %s, stderr: %s", string(output), stderr.String())
	}

	return output[jsonStart:], nil
}

func getSolcVersion(solcPath string) (string, error) {
	cmd := exec.Command(solcPath, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}

	output := out.String()
	re := regexp.MustCompile(`Version: (\d+\.\d+\.\d+)`)
	matches := re.FindStringSubmatch(output)
	if len(matches) > 1 {
		return matches[1], nil
	}
	return "", fmt.Errorf("cannot parse version from output: %s", output)
}

func compareVersion(v1, v2 string) int {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var p1, p2 int
		if i < len(parts1) {
			fmt.Sscanf(parts1[i], "%d", &p1)
		}
		if i < len(parts2) {
			fmt.Sscanf(parts2[i], "%d", &p2)
		}

		if p1 > p2 {
			return 1
		}
		if p1 < p2 {
			return -1
		}
	}
	return 0
}

func adjustPragmaVersion(content string, targetVersion string) string {
	re := regexp.MustCompile(`pragma\s+solidity\s+([^;]+);`)
	adjustedPragma := fmt.Sprintf("pragma solidity ^%s;", targetVersion)

	return re.ReplaceAllStringFunc(content, func(match string) string {
		versionPart := re.FindStringSubmatch(match)
		if len(versionPart) < 2 {
			return match
		}

		versionStr := strings.TrimSpace(versionPart[1])

		if strings.HasPrefix(versionStr, "^") {
			versionNum := strings.TrimPrefix(versionStr, "^")
			versionNum = strings.TrimSpace(versionNum)
			if compareVersion(versionNum, targetVersion) > 0 {
				return adjustedPragma
			}
		} else if strings.Contains(versionStr, ">=") {
			reVersion := regexp.MustCompile(`>=(\d+\.\d+\.\d+)`)
			matches := reVersion.FindStringSubmatch(versionStr)
			if len(matches) > 1 {
				versionNum := matches[1]
				if compareVersion(versionNum, targetVersion) > 0 {
					return adjustedPragma
				}
			}
		}

		return match
	})
}

func downloadFromIPFS(ipfsURL string) ([]byte, error) {
	gateways := []string{
		"https://ipfs.io/ipfs/",
		"https://gateway.pinata.cloud/ipfs/",
	}
	cid := strings.Replace(ipfsURL, "dweb:/ipfs/", "", 1)
	var lastErr error
	for _, gw := range gateways {
		httpURL := gw + cid
		client := &http.Client{Timeout: 15 * time.Second}

		resp, err := client.Get(httpURL)
		if err == nil && resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			return io.ReadAll(resp.Body)
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("status: %d", resp.StatusCode)
		}
	}
	return nil, fmt.Errorf("all gateways failed: %v", lastErr)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
