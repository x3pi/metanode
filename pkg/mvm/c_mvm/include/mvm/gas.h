
#pragma once

#include <iostream>
#include <map>
#include <stdint.h>
#include "exception.h"
#include "opcode.h"
#include "bigint.h"

namespace mvm
{
    class GasTracker{
        private:
            uint64_t max_gas;
            uint64_t gas_used;
        public:
            GasTracker(uint64_t max_gas) : max_gas(max_gas) {
                gas_used = 0;
            };
            inline void add_gas_used(uint64_t gas) {
                gas_used += gas;
                if (gas_used > max_gas) {
                    throw Exception(
                        Exception::Type::ErrOutOfGas,
                        "Out of gas");
                }
            };

            inline uint64_t get_gas_used() {
                return gas_used;
            }
    };

    uint64_t getGasCost(Opcode opcode);
    uint64_t getExpGasCost(int byte_length);
    uint64_t getMemExpansionGasCost(uint64_t &last_mem_fee, uint64_t old_mem_word_size, uint64_t new_mem_word_size);
    uint64_t getSha3GasCost(uint64_t data_word_size, uint64_t &last_mem_fee, uint64_t old_mem_word_size, uint64_t new_mem_word_size);
    uint64_t getTouchedAddressGasCost();
    uint64_t getUnTouchedAddressGasCost();
    uint64_t getCopyOperationGasCost(uint64_t data_word_size, uint64_t &last_mem_fee, uint64_t old_mem_word_size, uint64_t new_mem_word_size);
    uint64_t getTouchedStorageGasCost();
    uint64_t getUnTouchedStorageGasCost();
    uint64_t getLogGasCost(uint64_t n, uint64_t size);
    uint64_t getSstoreGasCost(uint256_t old_value, uint256_t new_value);
    uint64_t getCodeDepositCost(uint64_t size);
    uint64_t getCallValueCost();
    uint64_t getCreate2DataSizeCost(uint64_t word_size);
}