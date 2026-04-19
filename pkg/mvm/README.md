# Introduction
The Metanode Virtual Machine (MVM) is the runtime environment for smart contracts on the Metanode blockchain. It serves as the decentralized computer that executes smart contracts program logic. The MVM is at the heart of Metanode's functionality, enabling developers to write and deploy decentralized applications (DApps) in a secure and trustless manner.

This documentation provides an overview of the MVM package, a comprehensive toolkit designed to facilitate seamless interaction with the MVM. Whether you're building smart contracts, developing tools for blockchain analysis, or integrating decentralized features into your application, this package offers the functionality and flexibility required to simplify your workflow.

# Components
## C++ library
- fmt: this library provides a fast and efficient way to format strings in C++.
- intx: this library provides a set of integer types optimized for performance.
- keccak: this library provides a set of functions for hashing data using the Keccak algorithm.
- nlohmann: this library provides a JSON parser and serializer for C++.

## C++ source 
- account:
    this module provides a set of functions for managing accounts on C++. 
- address:
    this defines the address structure.
- bigint:
    this defines the bigint structure. 
- block_context:
    defines the block context structure. 
- exception:
    defines the exceptions constants. 
- extension:
    defines the extension structure and constants.
    + CALL_API_EXTENSION: address 101  
    + EXTRACT_JSON_FIELD_EXTENSION: address 102  
    + BLST: address 103  
- gas:
    defines the gas tracker. Which is used to track the gas usage of the contract.
- globalstate:
    defines the account state and global state structure on C++.
- log:
    used to track event's logs from smart contract.
- native_logger:
    used to log the messages to golang.
- opcode:
    defines the opcodes for the MVM. Detail opcodes:
```c++
STOP       = 0x00, // Halts execution
ADD        = 0x01, // Addition operation
MUL        = 0x02, // Multiplication operation
SUB        = 0x03, // Substraction operation
DIV        = 0x04, // Integer division operation
SDIV       = 0x05, // Signed integer division operation (truncated)
MOD        = 0x06, // Modulo remainder operation
SMOD       = 0x07, // Signed modulo remainder operation
ADDMOD     = 0x08, // Modulo addition operation
MULMOD     = 0x09, // Modulo multiplication operation
EXP        = 0x0a, // Exponential operation
SIGNEXTEND = 0x0b, // Extend length of two’s complement signed integer.

// 10s: Comparison & Bitwise Logic Operations
LT     = 0x10, // Less-than comparison
GT     = 0x11, // Greater-than comparison
SLT    = 0x12, // Signed less-than comparison
SGT    = 0x13, // Signed greater-than comparison
EQ     = 0x14, // Equality comparison
ISZERO = 0x15, // Simple not operator
AND    = 0x16, // Bitwise AND operation
OR     = 0x17, // Bitwise OR operation
XOR    = 0x18, // Bitwise XOR operation
NOT    = 0x19, // Bitwise NOT operation
BYTE   = 0x1a, // Retrieve single byte from word
SHL    = 0x1b, // Shift left
SHR    = 0x1c, // Logical shift right
SAR    = 0x1d, // Arithmetic shift right

// 20s: SHA3
SHA3   = 0x20, // Compute Keccak-256 hash.

// 30s: Environmental Information
ADDRESS        = 0x30, // Get address of currently executing account
BALANCE        = 0x31, // Get balance of the given account.
ORIGIN         = 0x32, //  Get execution origination address (This is the sender of original transaction; it is never an account with non-emptyassociated code.)
CALLER         = 0x33, // Get caller address (This is the address of the account that is directly responsible for this execution.)
CALLVALUE      = 0x34, // Get deposited value by the instruction/transaction responsible for this execution.
CALLDATALOAD   = 0x35, // Get input data of current environment. (This pertains to the input data passed with the message call instruction or transaction.)
CALLDATASIZE   = 0x36, // Get size of input data in current environment (This pertains to the input data passed with the message call instruction or transaction.)
CALLDATACOPY   = 0x37, //  Copy input data in current environment to memory. (This pertains to the input data passed with the message call instruction or transaction.)
CODESIZE       = 0x38, //  Get size of code running in current environment.
CODECOPY       = 0x39, // Copy code running in current environment to memory
GASPRICE       = 0x3a, //  Get price of gas in current environment. (This is gas price specified by the originating transaction.)
EXTCODESIZE    = 0x3b, // Get size of an account’s code.
EXTCODECOPY    = 0x3c, // Copy an account’s code to memory
RETURNDATASIZE = 0x3d, // Get size of output data from the previous call from the current environment.
RETURNDATACOPY = 0x3e, // Copy output data from the previous call to memory.
EXTCODEHASH    = 0x3f, // TODO

// 40s: Block Information
BLOCKHASH   = 0x40, // Get the hash of one of the 256 most recent complete blocks.
COINBASE    = 0x41, // Get the block’s beneficiary address
TIMESTAMP   = 0x42, // Get the block’s timestamp.
NUMBER      = 0x43, // Get the block’s number
PREVRANDAO  = 0x44, // It was used for difficulty calculation in the past. It is now deprecated and became kind of random number.
GASLIMIT    = 0x45, // Get the block’s gas limit
CHAINID     = 0x46, // Get the chain’s ID
SELFBALANCE = 0x47, // Get the current balance of the executing account.
BASEFEE     = 0x48, // Get the base fee per gas for the block.

// 50s: Stack, Memory, Storage and Flow Operations
POP      = 0x50, // Remove item from stack.
MLOAD    = 0x51, // Load word from memory.
MSTORE   = 0x52, // Save word to memory
MSTORE8  = 0x53, // Save byte to memory
SLOAD    = 0x54, // Load word from storage.
SSTORE   = 0x55, // Save word to storage
JUMP     = 0x56, // Alter the program counter.
JUMPI    = 0x57, // Conditionally alter the program counter.
PC       = 0x58, // Get the value of the program counter prior to the increment corresponding to this instruction.
M_SIZE    = 0x59, // Get the size of active memory in bytes.
GAS      = 0x5a, // Get the amount of available gas, including the corresponding reduction for the cost of this instruction.
JUMPDEST = 0x5b, // Mark a valid destination for jumps. This operation has no effect on machine state during execution

// 60s & 70s: Push Operations
PUSH1  = 0x60, // Place 1 byte item on stack.
PUSH2  = 0x61, // Place 2 byte item on stack.
PUSH3  = 0x62, // Place 3 byte item on stack.
PUSH4  = 0x63, // Place 4 byte item on stack.
PUSH5  = 0x64, // Place 5 byte item on stack.
PUSH6  = 0x65, // Place 6 byte item on stack.
PUSH7  = 0x66, // Place 7 byte item on stack.
PUSH8  = 0x67, // Place 8 byte item on stack.
PUSH9  = 0x68, // Place 9 byte item on stack.
PUSH10 = 0x69, // Place 10 byte item on stack.
PUSH11 = 0x6a, // Place 11 byte item on stack.
PUSH12 = 0x6b, // Place 12 byte item on stack.
PUSH13 = 0x6c, // Place 13 byte item on stack.
PUSH14 = 0x6d, // Place 14 byte item on stack.
PUSH15 = 0x6e, // Place 15 byte item on stack.
PUSH16 = 0x6f, // Place 16 byte item on stack.
PUSH17 = 0x70, // Place 17 byte item on stack.
PUSH18 = 0x71, // Place 18 byte item on stack.
PUSH19 = 0x72, // Place 19 byte item on stack.
PUSH20 = 0x73, // Place 20 byte item on stack.
PUSH21 = 0x74, // Place 21 byte item on stack.
PUSH22 = 0x75, // Place 22 byte item on stack.
PUSH23 = 0x76, // Place 23 byte item on stack.
PUSH24 = 0x77, // Place 24 byte item on stack.
PUSH25 = 0x78, // Place 25 byte item on stack.
PUSH26 = 0x79, // Place 26 byte item on stack.
PUSH27 = 0x7a, // Place 27 byte item on stack.
PUSH28 = 0x7b, // Place 28 byte item on stack.
PUSH29 = 0x7c, // Place 29 byte item on stack.
PUSH30 = 0x7d, // Place 30 byte item on stack.
PUSH31 = 0x7e, // Place 31 byte item on stack.
PUSH32 = 0x7f, // Place 32 byte item on stack.

// 80s: Duplication Operation
DUP1  = 0x80, // Duplicate 1st stack item
DUP2  = 0x81, // Duplicate 2nd stack item
DUP3  = 0x82, // Duplicate 3rd stack item
DUP4  = 0x83, // Duplicate 4th stack item
DUP5  = 0x84, // Duplicate 5th stack item
DUP6  = 0x85, // Duplicate 6th stack item
DUP7  = 0x86, // Duplicate 7th stack item
DUP8  = 0x87, // Duplicate 8th stack item
DUP9  = 0x88, // Duplicate 9th stack item
DUP10 = 0x89, // Duplicate 10th stack item
DUP11 = 0x8a, // Duplicate 11th stack item
DUP12 = 0x8b, // Duplicate 12th stack item
DUP13 = 0x8c, // Duplicate 13th stack item
DUP14 = 0x8d, // Duplicate 14th stack item
DUP15 = 0x8e, // Duplicate 15th stack item
DUP16 = 0x8f, // Duplicate 16th stack item

// 90s: Exchange Operation
SWAP1  = 0x90, // Exchange 1st and 2nd stack item
SWAP2  = 0x91, // Exchange 1st and 3rd stack item
SWAP3  = 0x92, // Exchange 1st and 4th stack item
SWAP4  = 0x93, // Exchange 1st and 5th stack item
SWAP5  = 0x94, // Exchange 1st and 6th stack item
SWAP6  = 0x95, // Exchange 1st and 7th stack item
SWAP7  = 0x96, // Exchange 1st and 8th stack item
SWAP8  = 0x97, // Exchange 1st and 9th stack item
SWAP9  = 0x98, // Exchange 1st and 10th stack item
SWAP10 = 0x99, // Exchange 1st and 11th stack item
SWAP11 = 0x9a, // Exchange 1st and 12th stack item
SWAP12 = 0x9b, // Exchange 1st and 13th stack item
SWAP13 = 0x9c, // Exchange 1st and 14th stack item
SWAP14 = 0x9d, // Exchange 1st and 15th stack item
SWAP15 = 0x9e, // Exchange 1st and 16th stack item
SWAP16 = 0x9f, // Exchange 1st and 17th stack item

// a0s: Logging Operations
LOG0 = 0xa0, // Append log record with no topics
LOG1 = 0xa1, // Append log record with 1 topic
LOG2 = 0xa2, // Append log record with 2 topics
LOG3 = 0xa3, // Append log record with 3 topics
LOG4 = 0xa4, // Append log record with 4 topics

// f0s: System operations
CREATE       = 0xf0, // Create a new account with associated code
CALL         = 0xf1, // Message-call into an account
CALLCODE     = 0xf2, // Message-call into this account with an alternative account’s code
RETURN       = 0xf3, // Halt execution returning output data
DELEGATECALL = 0xf4, // Message-call into this account with an alternative account’s code, but persisting the current values for sender and value.
CREATE2      = 0xf5, // Create a new account with associated code at a specified address
STATICCALL   = 0xfa, // Static message-call into an account. Exactly equivalent to CALL except: The argument µs is replaced with 0.
REVERT       = 0xfd, // Halt execution reverting state changes but returning data and remaining gas
SELFDESTRUCT = 0xff 
```

- processor:
    this module is mainly responsible for processing the smart contract bytecode. It contains the following classes: 
    + Program: this class holds the bytecode and the current instruction pointer.
    + Processor: this class is responsible for processing the smart contract bytecode.
    + Context: this class holds the context of the smart contract execution.
- stack:
    this module provides a uint256 stack implementation for the MVM.
- storage:
    this module provides a storage implementation for the MVM. Storage is used to store the state of the smart contract as key-value pairs.
- transaction:
    this module provides a transaction structure for the MVM.
- util:
    this module provides utility functions for the MVM.

## Golang packages
- Extension:
    this package provides a set of functions for interacting with the MVM extensions, include:
    + ExtensionCallGetApi: implements for CALL_API_EXTENSION extension, call api and return the result as a string.
    + ExtractJSONFieldExtension: implements for EXTRACT_JSON_FIELD_EXTENSION extension. Extract a field from a JSON string.
    + BLST: implements for BLST extension. BLST signature verification which have 2 functions: verifySign and verifyAggregateSign.
- helper:
    use to extractExecuteResult from C++ to Golang.
- logger:
    implement for C++ native logger 
- mvm_api:
    This is main go package for MVM. It provides a set of functions for interacting with the MVM, include:
    + Call: call a smart contract function.
    + Deploy: deploy a smart contract.
    + GlobalStateGet: get state of the account. This function is used by C++ to get the state of the account.
    + GetStorageValue: get the value of a storage key.

## Linker 
- linker:
    this module is responsible for linking the C++ library with the Golang packages. It contains the following classes:
    + Linker: this class is responsible for linking the C++ library with the Golang packages.
    + MyAccount: this class is my account structure for the MVM. 
    + MyExtension: this class is my extension structure for the MVM to link with Golang packages.
    + MyGlobalState: this class is my global state structure for the MVM to link with Golang packages.
    + MyLogger: this class is my logger structure for the MVM to link with Golang packages.
    + MyStorage: this class is my storage structure for the MVM to link with Golang packages.

## Flow
- Deploy:
    + Deploy smart contract by calling `Deploy` function in `mvm_api` golang package.
    + Deploy will call `deploy` in c++ mvm_linker.
    + mvm_linker will create context, processor, and program to process the bytecode. 
    + mvm_link use processor to run the bytecode.
- Call:
    + Call smart contract by calling `Call` function in `mvm_api` golang package.
    + Call will call `call` in c++ mvm_linker.
    + mvm_linker will create context, processor, and program to process the bytecode. 
    + mvm_link use processor to run the bytecode.
    + c++ globalstate will fetch the state of the account via Linker
    + linker will call `GlobalStateGet` in `mvm_api` golang package to get the state of the account.

- Extension:
    + To use extension, we need to register the extension address in the c++ code.
    + When the extension is called in the bytecode, the c++ code will call the extension function in the golang package.

## Installation
- Because we use the C++ library, we need to build the C++ library first.
- Doesn't need to share c_mvm code, just need to share the build library to run the Golang code.
- Build C++ library:
```bash
./build.sh
```
- This script will build c_mvm and linker to create libmvm.a and libmvm_linker.a 

## CGO
- We use CGO to link the C++ library with the Golang packages.
- These lines is used to link the C++ library with the Golang packages. Don't think it's commented out.
```go
/*
#cgo CFLAGS: -w -O3 -march=native -mtune=native
#cgo CXXFLAGS: -std=c++17 -w -O3 -march=native -mtune=native
#cgo LDFLAGS: -L./linker/build/lib/static -lmvm_linker -L./c_mvm/build/lib/static -lmvm -lstdc++
#cgo CPPFLAGS: -I./linker/build/include
#include "mvm_linker.hpp"
#include <stdlib.h>
*/
```
- These lines to export Golang functions to C++ code.
```go
//export GlobalStateGet
//export ClearProcessingPointers
//export GetStorageValue
```
