#include "mvm/gas.h"
#include "mvm/safe_throw.h"

namespace mvm
{
    uint64_t getGasCost(Opcode opcode)
    {
        switch (opcode)
        {
        case STOP:
            return 0;
        case ADD:
            return 3;
        case MUL:
            return 5;
        case SUB:
            return 3;
        case DIV:
            return 5;
        case SDIV:
            return 5;
        case MOD:
            return 5;
        case SMOD:
            return 5;
        case ADDMOD:
            return 8;
        case MULMOD:
            return 8;
        case EXP:
            // use separated func to calculate
            return 0;
        case SIGNEXTEND:
            return 5;
        case LT:
            return 3;
        case GT:
            return 3;
        case SLT:
            return 3;
        case SGT:
            return 3;
        case EQ:
            return 3;
        case ISZERO:
            return 3;
        case AND:
            return 3;
        case OR:
            return 3;
        case XOR:
            return 3;
        case NOT:
            return 3;
        case BYTE:
            return 3;
        case SHL:
            return 3;
        case SHR:
            return 3;
        case SAR:
            return 3;
        case SHA3:
        // use separated func to calculate
            return 0;
        case ADDRESS:
            return 2;
        case BALANCE:
        // use separated func to calculate
            return 0;
        case ORIGIN:
            return 2;
        case CALLER:
            return 2;
        case CALLVALUE:
            return 2;
        case CALLDATALOAD:
            return 3;
        case CALLDATASIZE:
            return 2;
        case CALLDATACOPY:
        // use separated func to calculate
            return 3;
        case CODESIZE:
            return 2;
        case CODECOPY:
        // use separated func to calculate
            return 3;
        case GASPRICE:
            return 2;
        case EXTCODESIZE:
        // use separated func to calculate
            return 0;
        case EXTCODECOPY:
        // use separated func to calculate
            return 0;
        case RETURNDATASIZE:
            return 2;
        case RETURNDATACOPY:
        // use separated func to calculate
            return 3;
        case EXTCODEHASH:
        // account access cost is charged by the opcode (EIP-2929)
            return 0;
        case BLOCKHASH:
            return 20;
        case COINBASE:
            return 2;
        case TIMESTAMP:
            return 2;
        case NUMBER:
            return 2;
        case PREVRANDAO:
            return 2;
        case GASLIMIT:
            return 2;
        case CHAINID:
            return 2;
        case SELFBALANCE:
            return 5;
        case BASEFEE:
            return 2;
        case POP:
            return 2;
        case MLOAD:
        // use separated func to calculate
            return 3;
        case MSTORE:
         // use separated func to calculate
            return 3;
        case MSTORE8:
         // use separated func to calculate
            return 3;
        case SLOAD:
        // use separated func to calculate
            return 0;
        case SSTORE:
        // charged by the opcode (EIP-2200 / EIP-2929 / EIP-3529)
            return 0;
        case JUMP:
            return 8;
        case JUMPI:
            return 10;
        case PC:
            return 2;
        case M_SIZE:
            return 2;
        case GAS:
            return 2;
        case JUMPDEST:
            return 1;
        case PUSH1:
            return 3;
        case PUSH2:
            return 3;
        case PUSH3:
            return 3;
        case PUSH4:
            return 3;
        case PUSH5:
            return 3;
        case PUSH6:
            return 3;
        case PUSH7:
            return 3;
        case PUSH8:
            return 3;
        case PUSH9:
            return 3;
        case PUSH10:
            return 3;
        case PUSH11:
            return 3;
        case PUSH12:
            return 3;
        case PUSH13:
            return 3;
        case PUSH14:
            return 3;
        case PUSH15:
            return 3;
        case PUSH16:
            return 3;
        case PUSH17:
            return 3;
        case PUSH18:
            return 3;
        case PUSH19:
            return 3;
        case PUSH20:
            return 3;
        case PUSH21:
            return 3;
        case PUSH22:
            return 3;
        case PUSH23:
            return 3;
        case PUSH24:
            return 3;
        case PUSH25:
            return 3;
        case PUSH26:
            return 3;
        case PUSH27:
            return 3;
        case PUSH28:
            return 3;
        case PUSH29:
            return 3;
        case PUSH30:
            return 3;
        case PUSH31:
            return 3;
        case PUSH32:
            return 3;
        case DUP1:
            return 3;
        case DUP2:
            return 3;
        case DUP3:
            return 3;
        case DUP4:
            return 3;
        case DUP5:
            return 3;
        case DUP6:
            return 3;
        case DUP7:
            return 3;
        case DUP8:
            return 3;
        case DUP9:
            return 3;
        case DUP10:
            return 3;
        case DUP11:
            return 3;
        case DUP12:
            return 3;
        case DUP13:
            return 3;
        case DUP14:
            return 3;
        case DUP15:
            return 3;
        case DUP16:
            return 3;
        case SWAP1:
            return 3;
        case SWAP2:
            return 3;
        case SWAP3:
            return 3;
        case SWAP4:
            return 3;
        case SWAP5:
            return 3;
        case SWAP6:
            return 3;
        case SWAP7:
            return 3;
        case SWAP8:
            return 3;
        case SWAP9:
            return 3;
        case SWAP10:
            return 3;
        case SWAP11:
            return 3;
        case SWAP12:
            return 3;
        case SWAP13:
            return 3;
        case SWAP14:
            return 3;
        case SWAP15:
            return 3;
        case SWAP16:
            return 3;
        case LOG0:
        // use separated func to calculate
            return 0;
        case LOG1:
        // use separated func to calculate
            return 0;
        case LOG2:
        // use separated func to calculate
            return 0;
        case LOG3:
        // use separated func to calculate
            return 0;
        case LOG4:
        // use separated func to calculate
            return 0;
        case CREATE:
       // use separated func to calculate
            return 32000;
        case CALL:
        // use separated func to calculate
            return 0;
        case CALLCODE:
       // use separated func to calculate
            return 0;
        case RETURN:
        // use separated func to calculate
            return 0;
       // use separated func to calculate
        case DELEGATECALL:
       // use separated func to calculate
            return 0;
        case CREATE2:
        // use separated func to calculate
            return 32000;
        case STATICCALL:
        // use separated func to calculate
            return 0;
        case REVERT:
        // use separated func to calculate
            return 0;
        case SELFDESTRUCT:
       // use separated func to calculate
            return 0;
        // --- Cancun (EIP-3855/1153/5656/4844) ---
        // Previously all six fell through to `default: return 0` — this VM
        // declares itself Cancun-targeted (see opcode.h) but charged zero gas
        // for every one of them. Costs below match evm.codes' Cancun table.
        case PUSH0:
            return 2; // EIP-3855: same base tier as other zero-arg pushes (ADDRESS, CALLER, ...)
        case TLOAD:
            return 100; // EIP-1153: priced like a warm SLOAD
        case TSTORE:
            return 100; // EIP-1153: priced like a warm SSTORE
        case BLOBHASH:
            return 3; // EIP-4844
        case BLOBBASEFEE:
            return 2; // EIP-7516: same base tier as BASEFEE
        case MCOPY:
        // Static part only (3) — dynamic part (3 * word_count + memory
        // expansion) is charged in processor.cpp's mcopy(), same split as
        // CODECOPY/CALLDATACOPY above.
            return 3;
        default:
            return 0;
        }
    }

    uint64_t getExpGasCost(int byte_length)
    {   
        return 10 + 50 * byte_length;
    }

    uint64_t getMemExpansionGasCost(uint64_t &last_mem_fee, uint64_t old_mem_word_size, uint64_t new_mem_word_size)
    {   
        if (new_mem_word_size > 0x1FFFFFFFE0) {
            MVM_THROW(Exception(
                    Exception::Type::ErrGasUintOverflow,
                    "Gas uint64 overflow"));
        }
        if(new_mem_word_size > old_mem_word_size) {
            uint64_t square = new_mem_word_size * new_mem_word_size;
            uint64_t lin_coef = new_mem_word_size * 3; // I Dont know will 3, i just copy from eth :))
            uint64_t quad_coef = square / 512;  // I Dont know will 512, i just copy from eth :))
            uint64_t new_total_fee = lin_coef + quad_coef;
            uint64_t fee = new_total_fee - last_mem_fee;    
            last_mem_fee = new_total_fee;
            return fee;
        }
        return 0;
    }

    uint64_t getSha3GasCost(uint64_t data_word_size, uint64_t &last_mem_fee, uint64_t old_mem_word_size, uint64_t new_mem_word_size)
    {   
        return 30 + 6 * data_word_size + getMemExpansionGasCost(last_mem_fee, old_mem_word_size, new_mem_word_size);
    }
    
    uint64_t getTouchedAddressGasCost()
    {   
        return 100;
    }

    uint64_t getUnTouchedAddressGasCost()
    {   
        return 2600;
    }

    uint64_t getCopyOperationGasCost(uint64_t data_word_size, uint64_t &last_mem_fee, uint64_t old_mem_word_size, uint64_t new_mem_word_size)
    {   
        return 3 * data_word_size + getMemExpansionGasCost(last_mem_fee, old_mem_word_size, new_mem_word_size);
    }

    uint64_t getTouchedStorageGasCost()
    {   
        return 100;
    }

    uint64_t getUnTouchedStorageGasCost()
    {   
        return 2100;
    }

    uint64_t getLogGasCost(uint64_t n, uint64_t size)
    {   
        return 375 + 375 * n + 8 * size;
    }

    uint64_t getSstoreGasCost(uint256_t old_value, uint256_t new_value)
    {   
        uint64_t total_gas_cost = 0;
        total_gas_cost += 100;
        if(old_value == new_value) {
            return total_gas_cost;
        }
        if(old_value == 0) {
            // change or create new value from 0 is costly
            total_gas_cost += 20000;
        } else {
            if(new_value == 0) {
                // update value to 0 is cheap
                total_gas_cost += 2900;
            }
        }
        return total_gas_cost;
    }

    uint64_t getCodeDepositCost(uint64_t size)
    {   
        return 200 * size;
    }

    uint64_t getCallValueCost()
    {   
        return 9000;
    }

    uint64_t getCreate2DataSizeCost(uint64_t word_size)
    {   
        return 6 * word_size;
    }   

    // ---- standard precompile gas (go-ethereum, Berlin/Cancun pricing) ----
    static uint64_t words32(uint64_t n) { return (n + 31) / 32; }

    static uint64_t readLen(const std::vector<uint8_t>& in, size_t off, bool& tooBig)
    {
        // a 32-byte big-endian length taken from in[off..off+32) (zero padded); anything above 2^64 is "too big"
        uint8_t buf[32] = {0};
        for (size_t i = 0; i < 32; i++)
            if (off + i < in.size()) buf[i] = in[off + i];
        for (size_t i = 0; i < 24; i++)
            if (buf[i] != 0) { tooBig = true; return 0; }
        uint64_t v = 0;
        for (size_t i = 24; i < 32; i++) v = (v << 8) | buf[i];
        return v;
    }

    uint64_t getPrecompileGasCost(uint64_t address, const std::vector<uint8_t>& input)
    {
        switch (address)
        {
        case 1: return 3000;                                         // ecrecover
        case 2: return 60 + 12 * words32(input.size());             // sha256
        case 3: return 600 + 120 * words32(input.size());           // ripemd160
        case 4: return 15 + 3 * words32(input.size());              // identity
        case 5: // modexp, EIP-2565
        {
            bool big = false;
            const uint64_t baseLen = readLen(input, 0, big);
            const uint64_t expLen = readLen(input, 32, big);
            const uint64_t modLen = readLen(input, 64, big);
            if (big) return UINT64_MAX;
            const std::vector<uint8_t> rest = input.size() > 96 ? std::vector<uint8_t>(input.begin() + 96, input.end()) : std::vector<uint8_t>();
            // head (up to 32 bytes) of the exponent, which starts after the base
            unsigned msb = 0;
            if (rest.size() > baseLen)
            {
                const uint64_t headLen = expLen > 32 ? 32 : expLen;
                uint8_t head[32] = {0};
                for (uint64_t i = 0; i < headLen; i++)
                {
                    const uint64_t pos = baseLen + i;
                    if (pos < rest.size()) head[32 - headLen + i] = rest[pos];
                }
                for (int i = 0; i < 32; i++)
                {
                    if (head[i] != 0)
                    {
                        unsigned bits = 8 - __builtin_clz((unsigned)head[i]) + 24; // bit length of the byte
                        msb = (unsigned)(31 - i) * 8 + bits - 1;
                        break;
                    }
                }
            }
            unsigned __int128 adj = 0;
            if (expLen > 32) adj = (unsigned __int128)(expLen - 32) * 8;
            adj += msb;
            const uint64_t mx = baseLen > modLen ? baseLen : modLen;
            const unsigned __int128 words = ((unsigned __int128)mx + 7) / 8;
            unsigned __int128 gas = words * words;
            if (adj < 1) adj = 1;
            // saturate instead of overflowing 128 bits
            if (gas != 0 && adj > ((unsigned __int128)1 << 100) / gas) return UINT64_MAX;
            gas = gas * adj / 3;
            if (gas > (unsigned __int128)UINT64_MAX) return UINT64_MAX;
            return gas < 200 ? 200 : (uint64_t)gas;
        }
        case 6: return 150;                                          // bn256Add (Istanbul)
        case 7: return 6000;                                         // bn256ScalarMul (Istanbul)
        case 8: return 45000 + 34000 * (input.size() / 192);        // bn256Pairing (Istanbul)
        case 9:                                                      // blake2F: one gas per round
            if (input.size() != 213) return 0;
            return ((uint64_t)input[0] << 24) | ((uint64_t)input[1] << 16) | ((uint64_t)input[2] << 8) | input[3];
        case 10: return 50000;                                       // KZG point evaluation (EIP-4844)
        default: return 0;
        }
    }
}