// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once
#include <stdint.h>

namespace mvm
{
  /**
   * All opcodes supported by our EVM. Note that we currently target Cancun
   * version solidity support 0.8.28
   * https://ethereum.org/en/developers/docs/evm/opcodes/
   * https://www.evm.codes/?fork=cancun
   * https://github.com/wolflo/evm-opcodes/blob/main/gas.md
   */
  enum Opcode : uint8_t
  {
    // 0s: Stop and Arithmetic Operations
    STOP = 0x00,       // Halts execution
    ADD = 0x01,        // Addition operation
    MUL = 0x02,        // Multiplication operation
    SUB = 0x03,        // Substraction operation
    DIV = 0x04,        // Integer division operation
    SDIV = 0x05,       // Signed integer division operation (truncated)
    MOD = 0x06,        // Modulo remainder operation
    SMOD = 0x07,       // Signed modulo remainder operation
    ADDMOD = 0x08,     // Modulo addition operation
    MULMOD = 0x09,     // Modulo multiplication operation
    EXP = 0x0a,        // Exponential operation
    SIGNEXTEND = 0x0b, // Extend length of two’s complement signed integer.

    // 10s: Comparison & Bitwise Logic Operations
    LT = 0x10,     // Less-than comparison
    GT = 0x11,     // Greater-than comparison
    SLT = 0x12,    // Signed less-than comparison
    SGT = 0x13,    // Signed greater-than comparison
    EQ = 0x14,     // Equality comparison
    ISZERO = 0x15, // Simple not operator
    AND = 0x16,    // Bitwise AND operation
    OR = 0x17,     // Bitwise OR operation
    XOR = 0x18,    // Bitwise XOR operation
    NOT = 0x19,    // Bitwise NOT operation
    BYTE = 0x1a,   // Retrieve single byte from word
    SHL = 0x1b,
    SHR = 0x1c,
    SAR = 0x1d,

    // 20s: SHA3
    SHA3 = 0x20, // Compute Keccak-256 hash. tên khác là KECCAK256

    // 30s: Environmental Information
    ADDRESS = 0x30,        // Get address of currently executing account
    BALANCE = 0x31,        // Get balance of the given account.
    ORIGIN = 0x32,         //  Get execution origination address (This is the sender of original transaction; it is never an account with non-emptyassociated code.)
    CALLER = 0x33,         // Get caller address (This is the address of the account that is directly responsible for this execution.)
    CALLVALUE = 0x34,      // Get deposited value by the instruction/transaction responsible for this execution.
    CALLDATALOAD = 0x35,   // Get input data of current environment. (This pertains to the input data passed with the message call instruction or transaction.)
    CALLDATASIZE = 0x36,   // Get size of input data in current environment (This pertains to the input data passed with the message call instruction or transaction.)
    CALLDATACOPY = 0x37,   //  Copy input data in current environment to memory. (This pertains to the input data passed with the message call instruction or transaction.)
    CODESIZE = 0x38,       //  Get size of code running in current environment.
    CODECOPY = 0x39,       // Copy code running in current environment to memory
    GASPRICE = 0x3a,       //  Get price of gas in current environment. (This is gas price specified by the originating transaction.)
    EXTCODESIZE = 0x3b,    // Get size of an account’s code.
    EXTCODECOPY = 0x3c,    // Copy an account’s code to memory
    RETURNDATASIZE = 0x3d, // Get size of output data from the previous call from the current environment.
    RETURNDATACOPY = 0x3e, // Copy output data from the previous call to memory.
    EXTCODEHASH = 0x3f,    // TODO

    // 40s: Block Information
    BLOCKHASH = 0x40,  // Get the hash of one of the 256 most recent complete blocks.
    COINBASE = 0x41,   // Get the block’s beneficiary address
    TIMESTAMP = 0x42,  // Get the block’s timestamp.
    NUMBER = 0x43,     // Get the block’s number
    PREVRANDAO = 0x44, // Same as DIFFICULTY
    GASLIMIT = 0x45,   // Get the block’s gas limit
    CHAINID = 0x46,
    SELFBALANCE = 0x47,
    BASEFEE = 0x48,

    // 50s: Stack, Memory, Storage and Flow Operations
    POP = 0x50,      // Remove item from stack.
    MLOAD = 0x51,    // Load word from memory.
    MSTORE = 0x52,   // Save word to memory
    MSTORE8 = 0x53,  // Save byte to memory
    SLOAD = 0x54,    // Load word from storage.
    SSTORE = 0x55,   // Save word to storage
    JUMP = 0x56,     // Alter the program counter.
    JUMPI = 0x57,    // Conditionally alter the program counter.
    PC = 0x58,       // Get the value of the program counter prior to the increment corresponding to this instruction.
    M_SIZE = 0x59,   // Get the size of active memory in bytes.
    GAS = 0x5a,      // Get the amount of available gas, including the corresponding reduction for the cost of this instruction.
    JUMPDEST = 0x5b, // Mark a valid destination for jumps. This operation has no effect on machine state during execution

    // 60s & 70s: Push Operations
    PUSH1 = 0x60,  // Place 1 byte item on stack.
    PUSH2 = 0x61,  // Place 2 byte item on stack.
    PUSH3 = 0x62,  // Place 3 byte item on stack.
    PUSH4 = 0x63,  // Place 4 byte item on stack.
    PUSH5 = 0x64,  // Place 5 byte item on stack.
    PUSH6 = 0x65,  // Place 6 byte item on stack.
    PUSH7 = 0x66,  // Place 7 byte item on stack.
    PUSH8 = 0x67,  // Place 8 byte item on stack.
    PUSH9 = 0x68,  // Place 9 byte item on stack.
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
    DUP1 = 0x80,  // Duplicate 1st stack item
    DUP2 = 0x81,  // Duplicate 2nd stack item
    DUP3 = 0x82,  // Duplicate 3rd stack item
    DUP4 = 0x83,  // Duplicate 4th stack item
    DUP5 = 0x84,  // Duplicate 5th stack item
    DUP6 = 0x85,  // Duplicate 6th stack item
    DUP7 = 0x86,  // Duplicate 7th stack item
    DUP8 = 0x87,  // Duplicate 8th stack item
    DUP9 = 0x88,  // Duplicate 9th stack item
    DUP10 = 0x89, // Duplicate 10th stack item
    DUP11 = 0x8a, // Duplicate 11th stack item
    DUP12 = 0x8b, // Duplicate 12th stack item
    DUP13 = 0x8c, // Duplicate 13th stack item
    DUP14 = 0x8d, // Duplicate 14th stack item
    DUP15 = 0x8e, // Duplicate 15th stack item
    DUP16 = 0x8f, // Duplicate 16th stack item

    // 90s: Exchange Operation
    SWAP1 = 0x90,  // Exchange 1st and 2nd stack item
    SWAP2 = 0x91,  // Exchange 1st and 3rd stack item
    SWAP3 = 0x92,  // Exchange 1st and 4th stack item
    SWAP4 = 0x93,  // Exchange 1st and 5th stack item
    SWAP5 = 0x94,  // Exchange 1st and 6th stack item
    SWAP6 = 0x95,  // Exchange 1st and 7th stack item
    SWAP7 = 0x96,  // Exchange 1st and 8th stack item
    SWAP8 = 0x97,  // Exchange 1st and 9th stack item
    SWAP9 = 0x98,  // Exchange 1st and 10th stack item
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
    CREATE = 0xf0,       // Create a new account with associated code
    CALL = 0xf1,         // Message-call into an account
    CALLCODE = 0xf2,     // Message-call into this account with an alternative account’s code
    RETURN = 0xf3,       // Halt execution returning output data
    DELEGATECALL = 0xf4, // Message-call into this account with an alternative account’s code, but persisting the current values for sender and value.
    CREATE2 = 0xf5,
    STATICCALL = 0xfa, // Static message-call into an account. Exactly equivalent to CALL except: The argument µs is replaced with 0.
    REVERT = 0xfd,     // Halt execution reverting state changes but returning data and remaining gas
                       // INVALID      = 0xfe, // Designated invalid instruction => // Opcode không hợp lệ là đã được bắt trong trường hợp default ở switch trong file src/processor.cpp hàm dispatch()
    SELFDESTRUCT = 0xff,

    // Mới bổ sung 0.8.28
    PUSH0 = 0x5f,      // Đẩy giá trị 0 lên stack
    MCOPY = 0x5e,      // Sao chép bộ nhớ (EIP-5656)
    TLOAD = 0x5c,      // Tải dữ liệu từ bộ nhớ tạm (EIP-1153)
    TSTORE = 0x5d,     // Lưu dữ liệu vào bộ nhớ tạm (EIP-1153)
    BLOBHASH = 0x49,   // Hash blob (EIP-4844)
    BLOBBASEFEE = 0x4a // Base fee cho blob (EIP-4844)
  };

  
  // opcode to string để debug
  inline const char *opcodeToString(uint8_t opcode)
  {
    static const char *opcodeMap[256] = {0}; 
    opcodeMap[STOP] = "STOP";
    opcodeMap[ADD] = "ADD";
    opcodeMap[MUL] = "MUL";
    opcodeMap[SUB] = "SUB";
    opcodeMap[DIV] = "DIV";
    opcodeMap[SDIV] = "SDIV";
    opcodeMap[MOD] = "MOD";
    opcodeMap[SMOD] = "SMOD";
    opcodeMap[ADDMOD] = "ADDMOD";
    opcodeMap[MULMOD] = "MULMOD";
    opcodeMap[EXP] = "EXP";
    opcodeMap[SIGNEXTEND] = "SIGNEXTEND";

    opcodeMap[LT] = "LT";
    opcodeMap[GT] = "GT";
    opcodeMap[SLT] = "SLT";
    opcodeMap[SGT] = "SGT";
    opcodeMap[EQ] = "EQ";
    opcodeMap[ISZERO] = "ISZERO";
    opcodeMap[AND] = "AND";
    opcodeMap[OR] = "OR";
    opcodeMap[XOR] = "XOR";
    opcodeMap[NOT] = "NOT";
    opcodeMap[BYTE] = "BYTE";
    opcodeMap[SHL] = "SHL";
    opcodeMap[SHR] = "SHR";
    opcodeMap[SAR] = "SAR";

    opcodeMap[SHA3] = "SHA3";
    opcodeMap[ADDRESS] = "ADDRESS";
    opcodeMap[BALANCE] = "BALANCE";
    opcodeMap[ORIGIN] = "ORIGIN";
    opcodeMap[CALLER] = "CALLER";
    opcodeMap[CALLVALUE] = "CALLVALUE";
    opcodeMap[CALLDATALOAD] = "CALLDATALOAD";
    opcodeMap[CALLDATASIZE] = "CALLDATASIZE";
    opcodeMap[CALLDATACOPY] = "CALLDATACOPY";
    opcodeMap[CODESIZE] = "CODESIZE";
    opcodeMap[CODECOPY] = "CODECOPY";
    opcodeMap[GASPRICE] = "GASPRICE";
    opcodeMap[EXTCODESIZE] = "EXTCODESIZE";
    opcodeMap[EXTCODECOPY] = "EXTCODECOPY";
    opcodeMap[RETURNDATASIZE] = "RETURNDATASIZE";
    opcodeMap[RETURNDATACOPY] = "RETURNDATACOPY";
    opcodeMap[EXTCODEHASH] = "EXTCODEHASH";

    opcodeMap[BLOCKHASH] = "BLOCKHASH";
    opcodeMap[COINBASE] = "COINBASE";
    opcodeMap[TIMESTAMP] = "TIMESTAMP";
    opcodeMap[NUMBER] = "NUMBER";
    opcodeMap[PREVRANDAO] = "PREVRANDAO";
    opcodeMap[GASLIMIT] = "GASLIMIT";
    opcodeMap[CHAINID] = "CHAINID";
    opcodeMap[SELFBALANCE] = "SELFBALANCE";
    opcodeMap[BASEFEE] = "BASEFEE";

    opcodeMap[POP] = "POP";
    opcodeMap[MLOAD] = "MLOAD";
    opcodeMap[MSTORE] = "MSTORE";
    opcodeMap[MSTORE8] = "MSTORE8";
    opcodeMap[SLOAD] = "SLOAD";
    opcodeMap[SSTORE] = "SSTORE";
    opcodeMap[JUMP] = "JUMP";
    opcodeMap[JUMPI] = "JUMPI";
    opcodeMap[PC] = "PC";
    opcodeMap[M_SIZE] = "M_SIZE";
    opcodeMap[GAS] = "GAS";
    opcodeMap[JUMPDEST] = "JUMPDEST";

    opcodeMap[PUSH1] = "PUSH1";
    opcodeMap[PUSH2] = "PUSH2";
    opcodeMap[PUSH3] = "PUSH3";
    opcodeMap[PUSH4] = "PUSH4";
    opcodeMap[PUSH5] = "PUSH5";
    opcodeMap[PUSH6] = "PUSH6";
    opcodeMap[PUSH7] = "PUSH7";
    opcodeMap[PUSH8] = "PUSH8";
    opcodeMap[PUSH9] = "PUSH9";
    opcodeMap[PUSH10] = "PUSH10";
    opcodeMap[PUSH11] = "PUSH11";
    opcodeMap[PUSH12] = "PUSH12";
    opcodeMap[PUSH13] = "PUSH13";
    opcodeMap[PUSH14] = "PUSH14";
    opcodeMap[PUSH15] = "PUSH15";
    opcodeMap[PUSH16] = "PUSH16";
    opcodeMap[PUSH17] = "PUSH17";
    opcodeMap[PUSH18] = "PUSH18";
    opcodeMap[PUSH19] = "PUSH19";
    opcodeMap[PUSH20] = "PUSH20";
    opcodeMap[PUSH21] = "PUSH21";
    opcodeMap[PUSH22] = "PUSH22";
    opcodeMap[PUSH23] = "PUSH23";
    opcodeMap[PUSH24] = "PUSH24";
    opcodeMap[PUSH25] = "PUSH25";
    opcodeMap[PUSH26] = "PUSH26";
    opcodeMap[PUSH27] = "PUSH27";
    opcodeMap[PUSH28] = "PUSH28";
    opcodeMap[PUSH29] = "PUSH29";
    opcodeMap[PUSH30] = "PUSH30";
    opcodeMap[PUSH31] = "PUSH31";
    opcodeMap[PUSH32] = "PUSH32";
    opcodeMap[PUSH0] = "PUSH0";

    opcodeMap[DUP1] = "DUP1";
    opcodeMap[DUP2] = "DUP2";
    opcodeMap[DUP3] = "DUP3";
    opcodeMap[DUP4] = "DUP4";
    opcodeMap[DUP5] = "DUP5";
    opcodeMap[DUP6] = "DUP6";
    opcodeMap[DUP7] = "DUP7";
    opcodeMap[DUP8] = "DUP8";
    opcodeMap[DUP9] = "DUP9";
    opcodeMap[DUP10] = "DUP10";
    opcodeMap[DUP11] = "DUP11";
    opcodeMap[DUP12] = "DUP12";
    opcodeMap[DUP13] = "DUP13";
    opcodeMap[DUP14] = "DUP14";
    opcodeMap[DUP15] = "DUP15";
    opcodeMap[DUP16] = "DUP16";

    opcodeMap[SWAP1] = "SWAP1";
    opcodeMap[SWAP2] = "SWAP2";
    opcodeMap[SWAP3] = "SWAP3";
    opcodeMap[SWAP4] = "SWAP4";
    opcodeMap[SWAP5] = "SWAP5";
    opcodeMap[SWAP6] = "SWAP6";
    opcodeMap[SWAP7] = "SWAP7";
    opcodeMap[SWAP8] = "SWAP8";
    opcodeMap[SWAP9] = "SWAP9";
    opcodeMap[SWAP10] = "SWAP10";
    opcodeMap[SWAP11] = "SWAP11";
    opcodeMap[SWAP12] = "SWAP12";
    opcodeMap[SWAP13] = "SWAP13";
    opcodeMap[SWAP14] = "SWAP14";
    opcodeMap[SWAP15] = "SWAP15";
    opcodeMap[SWAP16] = "SWAP16";

    opcodeMap[LOG0] = "LOG0";
    opcodeMap[LOG1] = "LOG1";
    opcodeMap[LOG2] = "LOG2";
    opcodeMap[LOG3] = "LOG3";
    opcodeMap[LOG4] = "LOG4";

    opcodeMap[CREATE] = "CREATE";
    opcodeMap[CALL] = "CALL";
    opcodeMap[CALLCODE] = "CALLCODE";
    opcodeMap[RETURN] = "RETURN";
    opcodeMap[DELEGATECALL] = "DELEGATECALL";
    opcodeMap[CREATE2] = "CREATE2";
    opcodeMap[STATICCALL] = "STATICCALL";
    opcodeMap[REVERT] = "REVERT";
    opcodeMap[SELFDESTRUCT] = "SELFDESTRUCT";
    opcodeMap[MCOPY] = "MCOPY";
    opcodeMap[TLOAD] = "TLOAD";
    opcodeMap[TSTORE] = "TSTORE";
    opcodeMap[BLOBHASH] = "BLOBHASH";
    opcodeMap[BLOBBASEFEE] = "BLOBBASEFEE";

    // Handle cases where opcode is out of range or not mapped.
    return opcodeMap[opcode] ? opcodeMap[opcode] : "UNKNOWN";
  }

} // namespace mvm
