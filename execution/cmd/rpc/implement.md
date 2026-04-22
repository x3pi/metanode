# Hướng dẫn Triển khai Hệ thống Self-Debugging cho Smart Contract

## 📋 Mục lục
1. [Tổng quan Kiến trúc](#tổng-quan-kiến-trúc)
2. [Phase 1: Artifact Registry](#phase-1-artifact-registry)
3. [Phase 2: Universal Error Decoder](#phase-2-universal-error-decoder)
4. [Phase 3: Call Tree & Trace](#phase-3-call-tree--trace)
5. [Phase 4: Source Mapping & Stack Trace](#phase-4-source-mapping--stack-trace)
6. [Phase 5: Dump State & Local Replay](#phase-5-dump-state--local-replay)
7. [Phase 6: CLI & Developer Tools](#phase-6-cli--developer-tools)
8. [Phase 7: Security & Access Control](#phase-7-security--access-control)
9. [Testing & Validation](#testing--validation)
10. [Deployment Strategy](#deployment-strategy)

---

## 🎯 Tổng quan Kiến trúc

### Tech Stack
- **Backend**: Go (1.23+)
- **EVM**: go-ethereum v1.14.12
- **Smart Contracts**: Solidity (≥ 0.8.4)
- **Storage**: BadgerDB v4, LevelDB
- **RPC**: JSON-RPC 2.0
- **CLI**: Cobra hoặc similar

### Cấu trúc Module mới cần tạo

pkg/
├── artifact/
│ ├── registry.go # Artifact Registry core
│ ├── verifier.go # Bytecode verification
│ ├── storage.go # Artifact persistence
│ └── types.go # Artifact data structures
├── debug/
│ ├── error_decoder.go # Universal Error Decoder
│ ├── call_tree.go # Call Tree builder
│ ├── source_mapper.go # Source mapping
│ ├── stack_trace.go # Stack trace builder
│ └── state_dump.go # State dumping
├── replay/
│ ├── executor.go # Replay execution engine
│ ├── state_manager.go # State management for replay
│ └── hotswap.go # Hot-swap bytecode
└── rpc/
└── debug_api.go # Debug RPC endpoints
---

## Phase 1: Artifact Registry

### 1.1. Data Structures

**File: `pkg/artifact/types.go`**

package artifact

import (
    "crypto/sha3"
    "encoding/hex"
    "github.com/ethereum/go-ethereum/common"
)

// ArtifactSet chứa toàn bộ artifact cho một contract
type ArtifactSet struct {
    ArtifactID   string            `json:"artifact_id"`
    ContractAddr common.Address    `json:"contract_address"`
    BytecodeHash common.Hash       `json:"bytecode_hash"`
    
    // Metadata
    Compiler     CompilerMetadata  `json:"compiler"`
    
    // Artifacts
    ABI          json.RawMessage   `json:"abi"`
    SourceCode   map[string]string `json:"source_code"` // filename -> content
    SourceMap    string            `json:"source_map"`
    StorageLayout json.RawMessage  `json:"storage_layout"`
    
    // Verification
    Status       VerificationStatus `json:"status"`
    VerifiedAt   int64             `json:"verified_at"`
    SignedBy     common.Address    `json:"signed_by"`
    Signature    []byte            `json:"signature"`
}

type CompilerMetadata struct {
    SolcVersion   string `json:"solc_version"`
    Optimizer     bool   `json:"optimizer"`
    OptimizerRuns int    `json:"optimizer_runs"`
    EVMVersion    string `json:"evm_version"`
    ViaIR         bool   `json:"via_ir"`
    Libraries     map[string]common.Address `json:"libraries"`
}

type VerificationStatus string

const (
    StatusPending   VerificationStatus = "pending"
    StatusVerified  VerificationStatus = "verified"
    StatusRejected  VerificationStatus = "rejected"
    StatusActive    VerificationStatus = "active"
)### 1.2. Artifact ID Generation

**File: `pkg/artifact/registry.go`**

package artifact

import (
    "crypto/sha3"
    "encoding/json"
    "github.com/ethereum/go-ethereum/common"
    "github.com/ethereum/go-ethereum/crypto"
)

// GenerateArtifactID tạo ID duy nhất từ bytecode + compiler settings
func GenerateArtifactID(
    bytecodeHash common.Hash,
    metadata CompilerMetadata,
) (string, error) {
    // Serialize metadata
    metadataBytes, err := json.Marshal(metadata)
    if err != nil {
        return "", err
    }
    
    // Concatenate
    combined := append(bytecodeHash.Bytes(), metadataBytes...)
    
    // Hash
    hash := crypto.Keccak256Hash(combined)
    return hex.EncodeToString(hash.Bytes()), nil
}

// Registry interface
type Registry interface {
    // Push artifact (chưa verify)
    PushArtifact(ctx context.Context, artifact *ArtifactSet) error
    
    // Verify artifact (compile lại và so sánh bytecode)
    VerifyArtifact(ctx context.Context, artifactID string) error
    
    // Activate artifact (chỉ khi đã verify)
    ActivateArtifact(ctx context.Context, artifactID string) error
    
    // Lookup
    GetArtifact(ctx context.Context, contractAddr common.Address) (*ArtifactSet, error)
    GetArtifactByID(ctx context.Context, artifactID string) (*ArtifactSet, error)
    GetArtifactByBytecode(ctx context.Context, bytecodeHash common.Hash) (*ArtifactSet, error)
}### 1.3. Verification Engine

**File: `pkg/artifact/verifier.go`**

package artifact

import (
    "bytes"
    "context"
    "os/exec"
    "path/filepath"
    "github.com/ethereum/go-ethereum/common"
)

type Verifier struct {
    tempDir string
}

// VerifyArtifact compile lại source code và so sánh bytecode
func (v *Verifier) VerifyArtifact(
    ctx context.Context,
    artifact *ArtifactSet,
    onChainBytecode []byte,
) (bool, error) {
    // 1. Tạo thư mục tạm
    workDir, err := os.MkdirTemp(v.tempDir, "verify-")
    if err != nil {
        return false, err
    }
    defer os.RemoveAll(workDir)
    
    // 2. Ghi source files
    for filename, content := range artifact.SourceCode {
        filePath := filepath.Join(workDir, filename)
        if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
            return false, err
        }
    }
    
    // 3. Compile với exact settings
    compiledBytecode, err := v.compile(
        ctx,
        workDir,
        artifact.Compiler,
        artifact.StorageLayout, // Libraries
    )
    if err != nil {
        return false, err
    }
    
    // 4. So sánh bytecode (100% match)
    return bytes.Equal(compiledBytecode, onChainBytecode), nil
}

func (v *Verifier) compile(
    ctx context.Context,
    workDir string,
    metadata CompilerMetadata,
    libraries map[string]common.Address,
) ([]byte, error) {
    // Sử dụng solc compiler với exact settings
    // Implementation chi tiết ở đây
    // ...
    return nil, nil
}### 1.4. Storage Layer

**File: `pkg/artifact/storage.go`**

package artifact

import (
    "context"
    "encoding/json"
    "github.com/dgraph-io/badger/v4"
    "github.com/ethereum/go-ethereum/common"
)

type BadgerStorage struct {
    db *badger.DB
}

// Store artifact
func (s *BadgerStorage) StoreArtifact(ctx context.Context, artifact *ArtifactSet) error {
    key := []byte("artifact:" + artifact.ArtifactID)
    value, err := json.Marshal(artifact)
    if err != nil {
        return err
    }
    
    return s.db.Update(func(txn *badger.Txn) error {
        return txn.Set(key, value)
    })
}

// Index by contract address
func (s *BadgerStorage) IndexByAddress(ctx context.Context, addr common.Address, artifactID string) error {
    key := []byte("index:addr:" + addr.Hex())
    return s.db.Update(func(txn *badger.Txn) error {
        return txn.Set(key, []byte(artifactID))
    })
}### 1.5. RPC API để Push Artifact

**File: `pkg/rpc/debug_api.go`** (phần Artifact)

package rpc

// debug_pushArtifact - Push artifact vào registry
func (api *DebugAPI) PushArtifact(ctx context.Context, artifact ArtifactSet) (string, error) {
    // 1. Validate input
    if err := validateArtifact(&artifact); err != nil {
        return "", err
    }
    
    // 2. Generate artifact ID
    artifactID, err := artifact.GenerateArtifactID()
    if err != nil {
        return "", err
    }
    artifact.ArtifactID = artifactID
    artifact.Status = StatusPending
    
    // 3. Store
    if err := api.registry.PushArtifact(ctx, &artifact); err != nil {
        return "", err
    }
    
    // 4. Trigger verification (async)
    go api.verifyArtifactAsync(ctx, artifactID)
    
    return artifactID, nil
}

func validateArtifact(artifact *ArtifactSet) error {
    // Validate ABI
    if len(artifact.ABI) == 0 {
        return errors.New("ABI is required")
    }
    
    // Validate source code
    if len(artifact.SourceCode) == 0 {
        return errors.New("source code is required")
    }
    
    // Validate storage layout
    if len(artifact.StorageLayout) == 0 {
        return errors.New("storage layout is required")
    }
    
    return nil
}---

## Phase 2: Universal Error Decoder

### 2.1. Error Types & Detection

**File: `pkg/debug/error_decoder.go`**

package debug

import (
    "bytes"
    "encoding/hex"
    "github.com/ethereum/go-ethereum/accounts/abi"
    "github.com/ethereum/go-ethereum/common"
)

// ErrorSelector constants
var (
    ErrorSelectorString = common.HexToHash("0x08c379a0") // Error(string)
    PanicSelector       = common.HexToHash("0x4e487b71") // Panic(uint256)
)

// DecodedError represents decoded error
type DecodedError struct {
    Decoded      bool                   `json:"decoded"`
    ErrorType    string                 `json:"error_type"` // "custom", "panic", "standard", "unknown"
    ErrorName    string                 `json:"error_name,omitempty"`
    ErrorSig     string                 `json:"error_signature,omitempty"`
    Arguments    map[string]interface{} `json:"arguments,omitempty"`
    Message      string                 `json:"message"`
    PanicCode    string                 `json:"panic_code,omitempty"`
    ContractAddr common.Address         `json:"contract_address,omitempty"`
    ContractName string                 `json:"contract_name,omitempty"`
    RawData      string                 `json:"raw_revert_data,omitempty"`
    PC           uint64                 `json:"pc,omitempty"`
}

// ErrorDecoder chịu trách nhiệm decode revert data
type ErrorDecoder struct {
    registry ArtifactRegistry
}

func NewErrorDecoder(registry ArtifactRegistry) *ErrorDecoder {
    return &ErrorDecoder{registry: registry}
}

// DecodeError decode revert data thành DecodedError
func (d *ErrorDecoder) DecodeError(
    ctx context.Context,
    revertData []byte,
    contractAddr common.Address,
    pc uint64,
) (*DecodedError, error) {
    if len(revertData) < 4 {
        return &DecodedError{
            Decoded:   false,
            ErrorType: "unknown",
            RawData:   hex.EncodeToString(revertData),
        }, nil
    }
    
    selector := common.BytesToHash(revertData[:4])
    
    // Priority order: Panic > Custom Error > Standard Error
    if bytes.Equal(selector.Bytes(), PanicSelector.Bytes()) {
        return d.decodePanic(revertData)
    }
    
    // Try Custom Error (require artifact)
    if artifact, err := d.registry.GetArtifact(ctx, contractAddr); err == nil {
        if decoded, err := d.decodeCustomError(revertData, artifact); err == nil {
            decoded.ContractAddr = contractAddr
            decoded.PC = pc
            return decoded, nil
        }
    }
    
    // Try Standard Error(string)
    if bytes.Equal(selector.Bytes(), ErrorSelectorString.Bytes()) {
        return d.decodeStandardError(revertData)
    }
    
    // Unknown
    return &DecodedError{
        Decoded:   false,
        ErrorType: "unknown",
        RawData:   hex.EncodeToString(revertData),
    }, nil
}

func (d *ErrorDecoder) decodePanic(revertData []byte) (*DecodedError, error) {
    if len(revertData) < 36 {
        return nil, errors.New("invalid panic data")
    }
    
    panicCode := hex.EncodeToString(revertData[4:36])
    message := getPanicMessage(panicCode)
    
    return &DecodedError{
        Decoded:   true,
        ErrorType: "panic",
        PanicCode: panicCode,
        Message:   message,
    }, nil
}

func getPanicMessage(code string) string {
    panicMessages := map[string]string{
        "0x01": "Assertion failed",
        "0x11": "Arithmetic overflow or underflow",
        "0x12": "Division by zero",
        "0x21": "Invalid enum value",
        "0x22": "Storage byte array incorrectly encoded",
        "0x31": "Empty array pop",
        "0x32": "Array index out of bounds",
    }
    
    if msg, ok := panicMessages[code]; ok {
        return msg
    }
    return "Unknown panic: " + code
}

func (d *ErrorDecoder) decodeCustomError(
    revertData []byte,
    artifact *artifact.ArtifactSet,
) (*DecodedError, error) {
    selector := revertData[:4]
    
    // Parse ABI
    var contractABI abi.ABI
    if err := json.Unmarshal(artifact.ABI, &contractABI); err != nil {
        return nil, err
    }
    
    // Find error by selector
    for name, errDef := range contractABI.Errors {
        if errDef.ID[:4] == selector {
            // Decode arguments
            args, err := errDef.Inputs.Unpack(revertData[4:])
            if err != nil {
                return nil, err
            }
            
            // Map arguments to names
            argMap := make(map[string]interface{})
            for i, input := range errDef.Inputs {
                argMap[input.Name] = args[i]
            }
            
            return &DecodedError{
                Decoded:   true,
                ErrorType: "custom",
                ErrorName: name,
                ErrorSig:  errDef.Sig,
                Arguments: argMap,
                Message:   formatErrorMessage(name, argMap),
            }, nil
        }
    }
    
    return nil, errors.New("error not found in ABI")
}

func (d *ErrorDecoder) decodeStandardError(revertData []byte) (*DecodedError, error) {
    if len(revertData) < 36 {
        return nil, errors.New("invalid error data")
    }
    
    // Decode ABI-encoded string
    message, err := abi.DecodeString(revertData[4:])
    if err != nil {
        return nil, err
    }
    
    return &DecodedError{
        Decoded:   true,
        ErrorType: "standard",
        Message:   message,
    }, nil
}### 2.2. Tích hợp vào RPC Layer

**Hook vào transaction execution:**

// Trong pkg/mvm hoặc transaction processor
func (p *Processor) ProcessTransaction(tx *Transaction) (*Result, error) {
    // ... execute transaction ...
    
    if result.Status == "revert" {
        // Decode error
        decodedError, err := p.errorDecoder.DecodeError(
            ctx,
            result.RevertData,
            result.ContractAddr,
            result.PC,
        )
        if err == nil {
            result.DecodedError = decodedError
        }
    }
    
    return result, nil
}---

## Phase 3: Call Tree & Trace

### 3.1. Call Tree Structures

**File: `pkg/debug/call_tree.go`**

package debug

import (
    "github.com/ethereum/go-ethereum/common"
)

// CallNode represents a node in the call tree
type CallNode struct {
    NodeID      string                 `json:"node_id"`
    CallType    string                 `json:"call_type"` // "external", "internal"
    Opcode      string                 `json:"opcode"`    // "CALL", "DELEGATECALL", "STATICCALL", "JUMP"
    
    Contract    ContractInfo           `json:"contract"`
    Function    FunctionInfo           `json:"function,omitempty"`
    CallDepth   int                    `json:"call_depth"`
    Status      string                 `json:"status"` // "success", "revert"
    
    Gas         GasInfo                `json:"gas"`
    Execution   ExecutionInfo          `json:"execution,omitempty"`
    Error       *DecodedError          `json:"error,omitempty"`
    
    Children    []*CallNode            `json:"children"`
}

type ContractInfo struct {
    Address common.Address `json:"address"`
    Name    string         `json:"name,omitempty"`
    
    // For DELEGATECALL
    ExecutionContext *ExecutionContext `json:"execution_context,omitempty"`
}

type ExecutionContext struct {
    CodeAddress    common.Address `json:"code_address"`    // Logic contract
    StorageAddress common.Address `json:"storage_address"` // Proxy contract
}

type FunctionInfo struct {
    Name      string `json:"name"`
    Signature string `json:"signature,omitempty"`
    Visibility string `json:"visibility,omitempty"`
}

type GasInfo struct {
    Provided int64 `json:"provided"`
    Used     int64 `json:"used"`
    Refunded int64 `json:"refunded,omitempty"`
}

type ExecutionInfo struct {
    Index   int    `json:"execution_index"`
    StartPC uint64 `json:"start_pc,omitempty"`
    EndPC   uint64 `json:"end_pc,omitempty"`
}### 3.2. Call Tree Builder

**Tích hợp vào EVM Interpreter:**

package debug

import (
    "github.com/ethereum/go-ethereum/core/vm"
)

// CallTreeBuilder xây dựng call tree từ EVM execution
type CallTreeBuilder struct {
    root        *CallNode
    current     *CallNode
    stack       []*CallNode
    registry    ArtifactRegistry
    errorDecoder *ErrorDecoder
}

func NewCallTreeBuilder(registry ArtifactRegistry, decoder *ErrorDecoder) *CallTreeBuilder {
    return &CallTreeBuilder{
        registry: registry,
        errorDecoder: decoder,
        stack: make([]*CallNode, 0),
    }
}

// Hook vào EVM CALL opcode
func (b *CallTreeBuilder) OnCall(op vm.Call) {
    node := &CallNode{
        NodeID:    generateNodeID(),
        CallType:  "external",
        Opcode:    op.Opcode.String(),
        CallDepth: len(b.stack),
        Contract: ContractInfo{
            Address: op.To,
        },
        Gas: GasInfo{
            Provided: op.Gas,
        },
    }
    
    // Detect DELEGATECALL
    if op.Opcode == vm.DELEGATECALL {
        node.Contract.ExecutionContext = &ExecutionContext{
            CodeAddress:    op.To,  // Logic contract
            StorageAddress: op.From, // Proxy contract
        }
    }
    
    // Resolve function name từ artifact
    if artifact, err := b.registry.GetArtifact(context.Background(), op.To); err == nil {
        node.Contract.Name = artifact.ContractName
        node.Function = b.resolveFunction(op.Input, artifact)
    }
    
    // Add to tree
    if b.current != nil {
        b.current.Children = append(b.current.Children, node)
    } else {
        b.root = node
    }
    
    b.stack = append(b.stack, b.current)
    b.current = node
}

func (b *CallTreeBuilder) OnCallReturn(status string, gasUsed int64, revertData []byte) {
    if b.current == nil {
        return
    }
    
    b.current.Status = status
    b.current.Gas.Used = gasUsed
    
    // Attach error if reverted
    if status == "revert" {
        decoded, _ := b.errorDecoder.DecodeError(
            context.Background(),
            revertData,
            b.current.Contract.Address,
            0, // PC từ execution context
        )
        b.current.Error = decoded
    }
    
    // Pop stack
    if len(b.stack) > 0 {
        b.current = b.stack[len(b.stack)-1]
        b.stack = b.stack[:len(b.stack)-1]
    } else {
        b.current = nil
    }
}### 3.3. Internal Call Synthesis

**File: `pkg/debug/internal_call.go`**

package debug

// SynthesizeInternalCalls tạo internal call nodes từ source map
func (b *CallTreeBuilder) SynthesizeInternalCalls(
    artifact *artifact.ArtifactSet,
    pcTrace []uint64,
) error {
    // Parse source map
    sourceMap, err := parseSourceMap(artifact.SourceMap)
    if err != nil {
        return err
    }
    
    // Track function boundaries
    functionMap := buildFunctionMap(artifact)
    
    // Scan PC trace
    currentFunction := ""
    for _, pc := range pcTrace {
        // Map PC to source location
        sourceLoc := sourceMap.MapPC(pc)
        
        // Check if entered new function
        if funcName := functionMap.GetFunctionAt(sourceLoc); funcName != "" && funcName != currentFunction {
            // Create internal call node
            node := &CallNode{
                NodeID:   generateNodeID(),
                CallType: "internal",
                Function: FunctionInfo{
                    Name:       funcName,
                    Visibility: "internal",
                },
            }
            
            // Add to current node
            if b.current != nil {
                b.current.Children = append(b.current.Children, node)
                b.current = node
            }
            
            currentFunction = funcName
        }
    }
    
    return nil
}---

## Phase 4: Source Mapping & Stack Trace

### 4.1. Source Map Parser

**File: `pkg/debug/source_mapper.go`**

package debug

import (
    "strings"
)

// SourceMapSegment represents a source map segment
type SourceMapSegment struct {
    StartOffset int    // Source file character offset
    Length      int    // Length in source
    FileIndex   int    // Source file index
    Line        int    // Line number (1-indexed)
    Column      int    // Column number (1-indexed)
}

// SourceMapper maps PC to source code location
type SourceMapper struct {
    segments []SourceMapSegment
    sourceFiles []string
}

func NewSourceMapper(sourceMap string, sourceFiles []string) (*SourceMapper, error) {
    segments, err := parseSourceMapString(sourceMap)
    if err != nil {
        return nil, err
    }
    
    return &SourceMapper{
        segments: segments,
        sourceFiles: sourceFiles,
    }, nil
}

// MapPC maps program counter to source location
func (m *SourceMapper) MapPC(pc uint64) *SourceLocation {
    // Binary search trong segments
    // Implementation...
    
    return &SourceLocation{
        File:   m.sourceFiles[segment.FileIndex],
        Line:   segment.Line,
        Column: segment.Column,
    }
}

// SourceLocation represents a location in source code
type SourceLocation struct {
    File       string `json:"file"`
    Line       int    `json:"line"`
    Column     int    `json:"column"`
    Snippet    string `json:"source_snippet,omitempty"`
}### 4.2. Stack Trace Builder

**File: `pkg/debug/stack_trace.go`**

package debug

// StackFrame represents a frame in stack trace
type StackFrame struct {
    Depth      int            `json:"depth"`
    Contract   ContractInfo   `json:"contract"`
    Function   string         `json:"function"`
    CallType   string         `json:"call_type"`
    Source     SourceLocation `json:"source"`
}

// BuildStackTrace builds stack trace from call tree
func BuildStackTrace(callTree *CallNode, mapper *SourceMapper) []StackFrame {
    frames := make([]StackFrame, 0)
    
    // Traverse call tree from error node to root
    node := findErrorNode(callTree)
    depth := 0
    
    for node != nil {
        frame := StackFrame{
            Depth:    depth,
            Contract: node.Contract,
            Function: node.Function.Name,
            CallType: node.CallType,
        }
        
        // Map PC to source (nếu có)
        if node.Execution.EndPC > 0 && mapper != nil {
            sourceLoc := mapper.MapPC(node.Execution.EndPC)
            frame.Source = *sourceLoc
        }
        
        frames = append(frames, frame)
        
        // Move to parent
        node = findParent(callTree, node)
        depth++
    }
    
    // Reverse (error at top)
    reverse(frames)
    
    return frames
}---

## Phase 5: Dump State & Local Replay

### 5.1. State Dump API

**File: `pkg/debug/state_dump.go`**

package debug

import (
    "encoding/json"
    "github.com/ethereum/go-ethereum/common"
)

// StateDump represents dumped state for replay
type StateDump struct {
    BlockEnv BlockEnvironment `json:"block_env"`
    Tx       TransactionContext `json:"tx"`
    Accounts map[common.Address]AccountState `json:"accounts"`
}

type BlockEnvironment struct {
    Number    uint64    `json:"number"`
    Timestamp uint64    `json:"timestamp"`
    BaseFee   string    `json:"basefee"`
    Coinbase  common.Address `json:"coinbase"`
    GasLimit  uint64    `json:"gaslimit"`
    ChainID   uint64    `json:"chainid"`
}

type TransactionContext struct {
    From     common.Address `json:"from"`
    To       common.Address `json:"to,omitempty"`
    Value    string         `json:"value"`
    Data     []byte         `json:"data"`
    Gas      uint64         `json:"gas"`
    GasPrice string         `json:"gas_price"`
}

type AccountState struct {
    Nonce   uint64            `json:"nonce"`
    Balance string            `json:"balance"`
    Code    []byte            `json:"code,omitempty"`
    Storage map[string]string `json:"storage,omitempty"` // slot -> value
}

// DumpState dumps state for a transaction
func DumpState(
    ctx context.Context,
    txHash common.Hash,
    blockNumber uint64,
    stateDB StateDB,
    callTree *CallNode,
) (*StateDump, error) {
    dump := &StateDump{
        Accounts: make(map[common.Address]AccountState),
    }
    
    // 1. Block environment
    block := stateDB.GetBlock(blockNumber)
    dump.BlockEnv = BlockEnvironment{
        Number:    block.Number,
        Timestamp: block.Timestamp,
        BaseFee:   block.BaseFee.String(),
        Coinbase:  block.Coinbase,
        GasLimit:  block.GasLimit,
        ChainID:   stateDB.ChainID(),
    }
    
    // 2. Transaction context
    tx := stateDB.GetTransaction(txHash)
    dump.Tx = TransactionContext{
        From:     tx.From,
        To:       tx.To,
        Value:    tx.Value.String(),
        Data:     tx.Data,
        Gas:      tx.Gas,
        GasPrice: tx.GasPrice.String(),
    }
    
    // 3. Account states (lazy dump - chỉ contracts trong call tree)
    accounts := extractAccountsFromCallTree(callTree)
    for _, addr := range accounts {
        account := AccountState{
            Nonce:   stateDB.GetNonce(addr),
            Balance: stateDB.GetBalance(addr).String(),
            Code:    stateDB.GetCode(addr),
        }
        
        // Full storage dump cho contract gây lỗi
        if isErrorContract(callTree, addr) {
            account.Storage = dumpStorage(stateDB, addr)
        }
        
        dump.Accounts[addr] = account
    }
    
    return dump, nil
}
### 5.2. Replay Engine

**File: `pkg/replay/executor.go`**

package replay

import (
    "context"
    "github.com/ethereum/go-ethereum/core/vm"
    "github.com/ethereum/go-ethereum/core/state"
)

// ReplayExecutor executes transaction in replay mode
type ReplayExecutor struct {
    stateDB    *state.StateDB
    blockEnv   BlockEnvironment
    txContext  TransactionContext
}

func NewReplayExecutor(dump *StateDump) (*ReplayExecutor, error) {
    // Create state DB from dump
    stateDB := createStateDBFromDump(dump.Accounts)
    
    return &ReplayExecutor{
        stateDB: stateDB,
        blockEnv: dump.BlockEnv,
        txContext: dump.Tx,
    }, nil
}

// Replay executes transaction
func (e *ReplayExecutor) Replay(ctx context.Context) (*ReplayResult, error) {
    // Override block environment opcodes
    evmConfig := vm.Config{
        // Hook TIMESTAMP, NUMBER, etc.
        Debug: true,
        Tracer: NewReplayTracer(),
    }
    
    // Create EVM instance với overridden environment
    evm := vm.NewEVM(
        e.createBlockContext(), // Override opcodes
        e.createTxContext(),
        e.stateDB,
        e.blockEnv.ChainID,
        evmConfig,
    )
    
    // Execute transaction
    result, err := evm.Call(
        vm.AccountRef(e.txContext.From),
        e.txContext.To,
        e.txContext.Data,
        e.txContext.Gas,
        e.txContext.Value,
    )
    
    return &ReplayResult{
        Success: result.Success,
        ReturnData: result.ReturnData,
        GasUsed: result.GasUsed,
        Error: result.Err,
    }, nil
}

// createBlockContext tạo block context với overridden opcodes
func (e *ReplayExecutor) createBlockContext() vm.BlockContext {
    return vm.BlockContext{
        // Override để trả về giá trị từ dump
        CanTransfer: func(db vm.StateDB, addr common.Address, amount *big.Int) bool {
            return db.GetBalance(addr).Cmp(amount) >= 0
        },
        Transfer: func(db vm.StateDB, sender, recipient common.Address, amount *big.Int) {
            db.SubBalance(sender, amount)
            db.AddBalance(recipient, amount)
        },
        GetHash: func(uint64) common.Hash {
            return common.Hash{}
        },
        // Custom opcode handlers sẽ override TIMESTAMP, NUMBER, etc.
    }
}---

## Phase 6: CLI & Developer Tools

### 6.1. CLI Structure

**File: `cmd/debug/main.go`**

package main

import (
    "github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
    Use:   "my-cli",
    Short: "Blockchain Debug CLI",
}

var debugCmd = &cobra.Command{
    Use:   "debug <tx_hash>",
    Short: "Debug a transaction",
    Args:  cobra.ExactArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        txHash := args[0]
        return debugTransaction(txHash)
    },
}

var dumpCmd = &cobra.Command{
    Use:   "dump <tx_hash>",
    Short: "Dump state for replay",
    Args:  cobra.ExactArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        txHash := args[0]
        return dumpState(txHash)
    },
}

var replayCmd = &cobra.Command{
    Use:   "replay",
    Short: "Replay transaction",
    RunE: func(cmd *cobra.Command, args []string) error {
        dumpFile, _ := cmd.Flags().GetString("dump")
        trace, _ := cmd.Flags().GetBool("trace")
        replaceCode, _ := cmd.Flags().GetString("replace-code")
        
        return replayTransaction(dumpFile, trace, replaceCode)
    },
}

func init() {
    rootCmd.AddCommand(debugCmd)
    rootCmd.AddCommand(dumpCmd)
    rootCmd.AddCommand(replayCmd)
    
    replayCmd.Flags().String("dump", "", "State dump file")
    replayCmd.Flags().Bool("trace", false, "Enable trace")
    replayCmd.Flags().String("replace-code", "", "Replace contract code")
}

func main() {
    if err := rootCmd.Execute(); err != nil {
        os.Exit(1)
    }
}### 6.2. RPC Endpoints

**File: `pkg/rpc/debug_api.go`** (full)

package rpc

// DebugAPI provides debug RPC endpoints
type DebugAPI struct {
    registry    artifact.Registry
    errorDecoder *debug.ErrorDecoder
    callTreeBuilder *debug.CallTreeBuilder
    stateDB     StateDB
}

// debug_debugTransaction - Full debug info
func (api *DebugAPI) DebugTransaction(ctx context.Context, txHash common.Hash) (*DebugResult, error) {
    // 1. Get transaction
    tx := api.stateDB.GetTransaction(txHash)
    
    // 2. Build call tree
    callTree := api.callTreeBuilder.Build(tx)
    
    // 3. Build stack trace
    stackTrace := debug.BuildStackTrace(callTree, api.sourceMapper)
    
    return &DebugResult{
        TxHash: txHash,
        CallTree: callTree,
        StackTrace: stackTrace,
        Error: callTree.Error,
    }, nil
}

// debug_dumpState - Dump state for replay
func (api *DebugAPI) DumpState(ctx context.Context, txHash common.Hash, blockNumber uint64) (*StateDump, error) {
    callTree, err := api.getCallTree(txHash)
    if err != nil {
        return nil, err
    }
    
    return debug.DumpState(ctx, txHash, blockNumber, api.stateDB, callTree)
}

// debug_replayTransaction - Replay transaction locally
func (api *DebugAPI) ReplayTransaction(ctx context.Context, dump *StateDump) (*ReplayResult, error) {
    executor, err := replay.NewReplayExecutor(dump)
    if err != nil {
        return nil, err
    }
    
    return executor.Replay(ctx)
}---

## Phase 7: Security & Access Control

### 7.1. Artifact Signing

**File: `pkg/artifact/signer.go`**

package artifact

import (
    "crypto/ecdsa"
    "github.com/ethereum/go-ethereum/crypto"
)

// SignArtifact signs artifact với developer key
func SignArtifact(artifact *ArtifactSet, privKey *ecdsa.PrivateKey) ([]byte, error) {
    // Create manifest
    manifest := ArtifactManifest{
        ArtifactID: artifact.ArtifactID,
        Contract:   artifact.ContractAddr.Hex(),
        Compiler:   artifact.Compiler.SolcVersion,
    }
    
    // Serialize
    data, err := json.Marshal(manifest)
    if err != nil {
        return nil, err
    }
    
    // Sign
    hash := crypto.Keccak256Hash(data)
    signature, err := crypto.Sign(hash.Bytes(), privKey)
    if err != nil {
        return nil, err
    }
    
    return signature, nil
}

// VerifyArtifactSignature verifies artifact signature
func VerifyArtifactSignature(artifact *ArtifactSet, pubKey *ecdsa.PublicKey) (bool, error) {
    manifest := ArtifactManifest{
        ArtifactID: artifact.ArtifactID,
        Contract:   artifact.ContractAddr.Hex(),
        Compiler:   artifact.Compiler.SolcVersion,
    }
    
    data, _ := json.Marshal(manifest)
    hash := crypto.Keccak256Hash(data)
    
    // Recover public key from signature
    pubKeyRecovered, err := crypto.SigToPub(hash.Bytes(), artifact.Signature)
    if err != nil {
        return false, err
    }
    
    return pubKeyRecovered.Equal(pubKey), nil
}### 7.2. Access Control

**File: `pkg/rpc/access_control.go`**

package rpc

// AccessControl manages access to debug APIs
type AccessControl struct {
    whitelist map[common.Address]bool
    rateLimiter *RateLimiter
}

func (ac *AccessControl) CanDumpState(ctx context.Context, caller common.Address) (bool, error) {
    // Check whitelist
    if !ac.whitelist[caller] {
        return false, errors.New("not whitelisted")
    }
    
    // Check rate limit
    if !ac.rateLimiter.Allow(caller, "dump_state", time.Hour, 10) {
        return false, errors.New("rate limit exceeded")
    }
    
    return true, nil
}---

## Testing & Validation

### Test Cases cần có:

1. **Artifact Registry**
   - Push & verify artifact
   - Reject invalid artifact
   - Lookup by address/bytecode/ID

2. **Error Decoder**
   - Decode Panic errors
   - Decode Custom errors
   - Decode Standard errors
   - Unknown error fallback

3. **Call Tree**
   - External calls
   - Internal calls
   - DELEGATECALL context

4. **Source Mapping**
   - PC to line mapping
   - Stack trace generation

5. **Replay**
   - Deterministic execution
   - Storage delta tracking

---

## Deployment Strategy

### Phase 1 (Week 1-2): Foundation
- Artifact Registry core
- Basic verification

### Phase 2 (Week 3-4): Error Decoder
- Universal Error Decoder
- RPC integration

### Phase 3 (Week 5-6): Call Tree
- External call tree
- Internal call synthesis

### Phase 4 (Week 7-8): Source Mapping
- Source map parser
- Stack trace

### Phase 5 (Week 9-10): Replay
- State dump
- Replay engine

### Phase 6 (Week 11-12): CLI & Polish
- CLI tools
- Security hardening
- Documentation

---

## Tài liệu tham khảo

- Solidity Source Maps: https://docs.soliditylang.org/en/latest/internals/source_mappings.html
- go-ethereum EVM: https://github.com/ethereum/go-ethereum/tree/master/core/vm
- JSON-RPC 2.0: https://www.jsonrpc.org/specification

---

**Last Updated**: 2025-01-XX
**Version**: 1.0.0