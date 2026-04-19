#include "mvm/gas.h"

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
        // use separated func to calculate
            return 5;
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
            return 0;
        case MSTORE:
         // use separated func to calculate
            return 0;
        case MSTORE8:
         // use separated func to calculate
            return 0;
        case SLOAD:
        // use separated func to calculate
            return 0;
        case SSTORE:
        // use separated func to calculate
            return 5;
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
            throw Exception(
                    Exception::Type::ErrGasUintOverflow,
                    "Gas uint64 overflow");
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
        return 3 + 3 * data_word_size + getMemExpansionGasCost(last_mem_fee, old_mem_word_size, new_mem_word_size);
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
}