package gen_bytecode

import "encoding/json"

// --- 1. Cấu trúc mapping file config.json (từ Remix) ---
type ConfigFile struct {
	Compiler CompilerInfo              `json:"compiler"`
	Output   ABIContainer              `json:"output"`
	Language string                    `json:"language"`
	Settings BuildMetadata             `json:"settings"`
	Sources  map[string]SourceMetadata `json:"sources"`
}
type ABIContainer struct {
	ABI json.RawMessage `json:"abi"`
}

// CompiledBytecodes chứa cả creation và deployed bytecode
type CompiledBytecodes struct {
	CreationBytecode string // Bytecode để deploy (bao gồm constructor)
	DeployedBytecode string // Runtime bytecode (để so sánh với chain)
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
		DeployedBytecode struct {
			Object string `json:"object"`
		} `json:"deployedBytecode"`
	} `json:"evm"`
}

type SolcError struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}
