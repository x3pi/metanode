package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// --- 1. Cấu trúc mapping file config.json (từ Remix) ---
type ConfigFile struct {
	Compiler CompilerInfo              `json:"compiler"`
	Language string                    `json:"language"`
	Settings BuildMetadata             `json:"settings"`
	Sources  map[string]SourceMetadata `json:"sources"`
}

type CompilerInfo struct {
	Version string `json:"version"`
}

type SourceMetadata struct {
	Keccak256 string   `json:"keccak256"`
	License   string   `json:"license"`
	URLs      []string `json:"urls"`
}

type BuildMetadata struct {
	CompilationTarget map[string]string              `json:"compilationTarget,omitempty"`
	Optimizer         Optimizer                      `json:"optimizer"`
	EVMVersion        string                         `json:"evmVersion,omitempty"`
	ViaIR             bool                           `json:"viaIR,omitempty"`
	OutputSelection   map[string]map[string][]string `json:"outputSelection,omitempty"`
	Libraries         map[string]interface{}         `json:"libraries,omitempty"`
	Metadata          map[string]interface{}         `json:"metadata,omitempty"`
	Remappings        []string                       `json:"remappings,omitempty"`
}

type Optimizer struct {
	Enabled bool `json:"enabled"`
	Runs    int  `json:"runs"`
}

// --- 2. Cấu trúc Input gửi cho solc (Standard JSON Input) ---
type SolcInput struct {
	Language string            `json:"language"`
	Sources  map[string]Source `json:"sources"`
	Settings BuildMetadata     `json:"settings"` // Nhúng trực tiếp struct Metadata vào đây
}

type Source struct {
	Content string `json:"content"`
}

// --- 3. Cấu trúc Output nhận về từ solc ---
type SolcOutput struct {
	Contracts map[string]map[string]ContractOutput `json:"contracts"`
	Errors    []SolcError                          `json:"errors"`
}

type ContractOutput struct {
	EVM struct {
		Bytecode struct {
			Object string `json:"object"`
		} `json:"bytecode"`
	} `json:"evm"`
}

type SolcError struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Hàm tải file từ IPFS
func downloadFromIPFS(ipfsURL string) ([]byte, error) {
	// Danh sách các gateway dự phòng
	gateways := []string{
		"https://ipfs.io/ipfs/",
		"https://gateway.pinata.cloud/ipfs/",
	}
	cid := strings.Replace(ipfsURL, "dweb:/ipfs/", "", 1)
	var lastErr error
	for _, gw := range gateways {
		logger.Info("downloadFromIPFS: gw %s", gw)
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
		logger.Error("downloadFromIPFS: err %v", err)
	}
	return nil, fmt.Errorf("tất cả gateway đều thất bại: %v", lastErr)
}

// Hàm parse version từ config (ví dụ: "0.8.30+commit.73712a01" -> "0.8.30")
func parseVersionFromConfig(versionStr string) string {
	// Tách phần version (bỏ phần commit hash)
	parts := strings.Split(versionStr, "+")
	if len(parts) > 0 {
		return parts[0]
	}
	return versionStr
}

// Hàm kiểm tra xem có Node.js và npx không
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

// Hàm compile bằng solc-js (qua npx)
func compileWithSolcJS(inputJSON []byte, version string) ([]byte, error) {
	// Sử dụng npx để gọi solc-js với version cụ thể
	// npx solc@0.8.30 --standard-json
	cmd := exec.Command("npx", "-y", fmt.Sprintf("solc@%s", version), "--standard-json")
	cmd.Stdin = bytes.NewReader(inputJSON)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("lỗi chạy solc-js: %v, stderr: %s", err, stderr.String())
	}

	output := out.Bytes()

	// npx có thể output progress messages trước JSON, cần tìm JSON object
	// Tìm ký tự '{' đầu tiên (bắt đầu JSON)
	jsonStart := bytes.IndexByte(output, '{')
	if jsonStart == -1 {
		// Không tìm thấy JSON, có thể có lỗi
		return nil, fmt.Errorf("không tìm thấy JSON trong output. stdout: %s, stderr: %s", string(output), stderr.String())
	}

	// Trả về phần JSON (từ '{' đến cuối)
	return output[jsonStart:], nil
}

// Hàm lấy version của solc binary (fallback)
func getSolcVersion(solcPath string) (string, error) {
	cmd := exec.Command(solcPath, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}

	output := out.String()
	// Tìm pattern "Version: 0.8.20+commit..."
	re := regexp.MustCompile(`Version: (\d+\.\d+\.\d+)`)
	matches := re.FindStringSubmatch(output)
	if len(matches) > 1 {
		return matches[1], nil
	}
	return "", fmt.Errorf("không thể parse version từ output: %s", output)
}

// Hàm so sánh version (đơn giản: so sánh major.minor.patch)
func compareVersion(v1, v2 string) int {
	// Tách version thành parts
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

// Hàm điều chỉnh pragma version trong source code để tương thích với solc version
func adjustPragmaVersion(content string, targetVersion string) string {
	// Tìm và thay thế pragma solidity
	// Pattern: pragma solidity ^0.8.30; hoặc pragma solidity >=0.8.20 <0.9.0;
	re := regexp.MustCompile(`pragma\s+solidity\s+([^;]+);`)

	// Parse target version để tạo range tương thích
	// Ví dụ: 0.8.20 -> ^0.8.20 (cho phép >=0.8.20 <0.9.0)
	adjustedPragma := fmt.Sprintf("pragma solidity ^%s;", targetVersion)

	return re.ReplaceAllStringFunc(content, func(match string) string {
		// Lấy version từ match (phần trong ngoặc)
		versionPart := re.FindStringSubmatch(match)
		if len(versionPart) < 2 {
			return match
		}

		versionStr := strings.TrimSpace(versionPart[1])

		// Nếu version yêu cầu cao hơn target, thay thế
		// Ví dụ: ^0.8.30 với target 0.8.20 -> thay thành ^0.8.20
		if strings.HasPrefix(versionStr, "^") {
			// Extract version number từ ^0.8.30
			versionNum := strings.TrimPrefix(versionStr, "^")
			versionNum = strings.TrimSpace(versionNum)

			// So sánh version
			if compareVersion(versionNum, targetVersion) > 0 {
				return adjustedPragma
			}
		} else if strings.Contains(versionStr, ">=") {
			// Xử lý trường hợp >=0.8.30
			reVersion := regexp.MustCompile(`>=(\d+\.\d+\.\d+)`)
			matches := reVersion.FindStringSubmatch(versionStr)
			if len(matches) > 1 {
				versionNum := matches[1]
				if compareVersion(versionNum, targetVersion) > 0 {
					return adjustedPragma
				}
			}
		}

		// Giữ nguyên nếu đã tương thích
		return match
	})
}

// Hàm parse các imports từ file Solidity
func parseImports(content string) []string {
	var imports []string
	// Pattern: import "@openzeppelin/..."; hoặc import "path/to/file.sol";
	re := regexp.MustCompile(`import\s+["']([^"']+)["']`)
	matches := re.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if len(match) > 1 {
			imports = append(imports, match[1])
		}
	}
	return imports
}

// Hàm kiểm tra xem file có được import (trực tiếp hoặc gián tiếp) không
func isFileNeeded(sourcePath string, imports []string, allSources map[string]SourceMetadata) bool {
	// 1. Kiểm tra xem có được import trực tiếp không
	for _, imp := range imports {
		if sourcePath == imp || strings.HasSuffix(sourcePath, imp) {
			return true
		}
		// Kiểm tra với @ mapping (ví dụ: @openzeppelin/... -> node_modules/@openzeppelin/...)
		if strings.HasPrefix(imp, "@") && strings.Contains(sourcePath, imp) {
			return true
		}
	}

	// 2. Kiểm tra xem có phải là dependency của các file được import không
	// (đơn giản: nếu có @ trong path, thường là dependency)
	if strings.Contains(sourcePath, "@") {
		return true
	}

	// 3. File local không có @ và không được import -> không cần
	// (trừ khi là file chính test.sol)
	if !strings.Contains(sourcePath, "@") && !strings.Contains(sourcePath, "test.sol") {
		return false
	}

	return true
}

func main() {
	// --- BƯỚC 1: Đọc file Metadata (config.json) ---
	configFile, err := os.ReadFile("config.json")
	if err != nil {
		log.Fatalf("Lỗi đọc config.json: %v", err)
	}

	var config ConfigFile
	if err := json.Unmarshal(configFile, &config); err != nil {
		log.Fatalf("Lỗi parse JSON config: %v", err)
	}

	// --- BƯỚC 1.1: Parse compiler version từ config ---
	configVersion := parseVersionFromConfig(config.Compiler.Version)
	fmt.Printf("Version từ config.json: %s\n", configVersion)

	// Lấy settings từ config
	metadata := config.Settings
	metadata.CompilationTarget = nil

	// Kiểm tra xem có dùng solc-js không (qua Node.js)
	useSolcJS := checkNodeJS()
	if useSolcJS {
		fmt.Printf("✓ Phát hiện Node.js và npx. Sẽ dùng solc-js version %s\n", configVersion)
	} else {
		fmt.Printf("⚠ Không tìm thấy Node.js/npx. Sẽ dùng solc binary local\n")
	}
	// Thêm outputSelection nếu chưa có (cần để solc trả về bytecode)
	if metadata.OutputSelection == nil {
		metadata.OutputSelection = map[string]map[string][]string{
			"*": {
				"*": {"evm.bytecode.object"},
			},
		}
	}

	// --- BƯỚC 1.5: Xác định solc version để điều chỉnh pragma version ---
	var solcVersion string
	if useSolcJS {
		// Nếu dùng solc-js, dùng version từ config
		solcVersion = configVersion
		fmt.Printf("Sẽ dùng solc-js version: %s\n", solcVersion)
	} else {
		// Nếu dùng solc binary, lấy version từ binary
		solcPath := "./solc"
		if _, statErr := os.Stat(solcPath); os.IsNotExist(statErr) {
			solcPath = "solc"
		}

		var versionErr error
		solcVersion, versionErr = getSolcVersion(solcPath)
		if versionErr != nil {
			log.Printf("Cảnh báo: Không thể lấy solc version: %v. Sẽ không điều chỉnh pragma version.\n", versionErr)
			solcVersion = "" // Không điều chỉnh nếu không lấy được version
		} else {
			fmt.Printf("Solc binary version: %s\n", solcVersion)
		}
	}

	// --- BƯỚC 2: Tải các file sources từ config.json (bao gồm file chính và dependencies) ---
	sources := make(map[string]Source)

	// Đọc file test.sol từ file system nếu tồn tại (file chính cần biên dịch)
	testSolFile := "test.sol"
	var testSolContent string
	if _, err := os.Stat(testSolFile); err == nil {
		content, err := os.ReadFile(testSolFile)
		if err == nil {
			testSolContent = string(content)
			// Điều chỉnh pragma version nếu cần
			if solcVersion != "" {
				testSolContent = adjustPragmaVersion(testSolContent, solcVersion)
			}
			sources[testSolFile] = Source{Content: testSolContent}
			fmt.Printf("✓ Đã đọc %s từ file system\n", testSolFile)
		}
	}

	// Parse imports từ test.sol để biết file nào cần tải
	var imports []string
	if testSolContent != "" {
		imports = parseImports(testSolContent)
		fmt.Printf("Đã tìm thấy %d imports trong test.sol: %v\n", len(imports), imports)
	}

	fmt.Println("Đang tải các file sources từ IPFS...")
	for sourcePath, sourceMeta := range config.Sources {
		logger.Info("___sourcePath %s", sourcePath)
		// Bỏ qua nếu đã có trong sources (ví dụ test.sol đã đọc từ file system)
		// if _, exists := sources[sourcePath]; exists {
		// 	continue
		// }

		// Kiểm tra xem file có cần thiết không (được import hoặc là dependency)
		// if !isFileNeeded(sourcePath, imports, config.Sources) {
		// 	fmt.Printf("  ⏭ Bỏ qua %s (không được import trong test.sol)\n", sourcePath)
		// 	continue
		// }

		var content []byte
		var err error

		// Thử đọc từ file system trước (cho file local)
		if _, err := os.Stat(sourcePath); err == nil {
			content, err = os.ReadFile(sourcePath)
			if err == nil {
				contentStr := string(content)
				// Điều chỉnh pragma version nếu cần
				if solcVersion != "" {
					contentStr = adjustPragmaVersion(contentStr, solcVersion)
				}
				sources[sourcePath] = Source{Content: contentStr}
				fmt.Printf("  ✓ Đã đọc %s từ file system\n", sourcePath)
				continue
			}
		}
		// Nếu không có file local, tải từ IPFS
		var ipfsURL string
		for _, url := range sourceMeta.URLs {
			if strings.HasPrefix(url, "dweb:/ipfs/") {
				ipfsURL = url
				break
			}
		}
		if ipfsURL == "" {
			fmt.Printf("  Cảnh báo: Không tìm thấy IPFS URL cho %s, bỏ qua...\n", sourcePath)
			continue
		}
		// Tải từ IPFS
		fmt.Printf("  Đang tải %s từ IPFS...\n", sourcePath)
		content, err = downloadFromIPFS(ipfsURL)
		if err != nil {
			fmt.Printf("  Cảnh báo: Không thể tải %s từ IPFS: %v\n", sourcePath, err)
			// Tiếp tục với các file khác
			continue
		}
		contentStr := string(content)
		// Điều chỉnh pragma version nếu cần
		if solcVersion != "" {
			contentStr = adjustPragmaVersion(contentStr, solcVersion)
		}
		sources[sourcePath] = Source{Content: contentStr}
		fmt.Printf("  ✓ Đã tải %s\n", sourcePath)
	}

	// --- BƯỚC 4: Tạo payload cho Compiler ---
	// Kết hợp nội dung file .sol và cấu hình từ .json
	input := SolcInput{
		Language: config.Language,
		Sources:  sources,
		Settings: metadata, // Gán metadata đã đọc vào đây
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Fatalf("Lỗi marshal input: %v", err)
		return
	}
	logger.Info("inputJSON %s", string(inputJSON))

	// --- BƯỚC 5: Gọi lệnh solc (solc-js hoặc solc binary) ---
	var outputBytes []byte

	fmt.Println("Đang biên dịch...")
	if useSolcJS {
		// Dùng solc-js với version từ config
		fmt.Printf("Đang compile với solc-js version %s...\n", configVersion)
		var compileErr error
		outputBytes, compileErr = compileWithSolcJS(inputJSON, configVersion)
		if compileErr != nil {
			log.Fatalf("Lỗi chạy solc-js: %v", compileErr)
		}
	} else {
		// Dùng solc binary local
		solcPath := "./solc"
		if _, statErr := os.Stat(solcPath); os.IsNotExist(statErr) {
			solcPath = "solc"
		}

		cmd := exec.Command(solcPath, "--standard-json")
		cmd.Stdin = bytes.NewReader(inputJSON)
		var out bytes.Buffer
		cmd.Stdout = &out

		if runErr := cmd.Run(); runErr != nil {
			log.Fatalf("Lỗi chạy solc (kiểm tra xem đã cài solc chưa?): %v", runErr)
		}
		outputBytes = out.Bytes()
	}

	// --- BƯỚC 6: Xử lý kết quả ---
	var output SolcOutput
	if err := json.Unmarshal(outputBytes, &output); err != nil {
		log.Fatalf("Lỗi parse output từ solc: %v", err)
	}

	// Kiểm tra lỗi biên dịch (Syntax error, v.v.)
	hasError := false
	for _, e := range output.Errors {
		if e.Severity == "error" {
			fmt.Printf("[LỖI BIÊN DỊCH]: %s\n", e.Message)
			hasError = true
		}
	}
	if hasError {
		return
	}

	// Lấy Bytecode ra
	// Lưu ý: Output trả về map lồng nhau: filename -> contract name
	// Ở đây giả định tên contract trùng tên file (thường thấy), hoặc ta duyệt qua map
	for fName, contracts := range output.Contracts {
		for cName, contractData := range contracts {
			bytecode := contractData.EVM.Bytecode.Object

			if len(bytecode) > 0 {
				fmt.Printf("\n>>> THÀNH CÔNG <<<\n")
				fmt.Printf("File: %s | Contract: %s\n", fName, cName)
				fmt.Printf("Cấu hình: Optimizer=%v (Runs=%d), EVM=%s, ViaIR=%v\n",
					metadata.Optimizer.Enabled, metadata.Optimizer.Runs, metadata.EVMVersion, metadata.ViaIR)
				fmt.Printf("Bytecode Length: %d bytes\n", len(bytecode)/2) // Mỗi 2 ký tự hex = 1 byte
				fmt.Printf("Bytecode (Full): 0x%s\n", bytecode)
				if len(bytecode) > 200 {
					fmt.Printf("Bytecode (First 100 chars): 0x%s...\n", bytecode[:100])
				}

				// Lưu bytecode vào file
				outputFilename := fmt.Sprintf("%s.bytecode", cName)
				if err := os.WriteFile(outputFilename, []byte(bytecode), 0644); err != nil {
					log.Printf("Cảnh báo: Không thể lưu bytecode vào file %s: %v", outputFilename, err)
				} else {
					fmt.Printf("Bytecode đã được lưu vào: %s\n", outputFilename)
				}
			}
		}
	}
}
