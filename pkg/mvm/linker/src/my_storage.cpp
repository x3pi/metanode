// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#include "my_storage.h"
#include "mvm/util.h"
#include "mvm/gas.h"
#include "mvm_linker.hpp"
#include "mvm/exception.h"
#include "state.h"

struct GetStorageValue_return
{
    unsigned char *value;
    bool success;
};
namespace mvm
{
    using ET = Exception::Type;

    MyStorage::~MyStorage() {}

    void MyStorage::Clear()
    {
        cache.clear();
    }

    void MyStorage::store(const uint256_t &key, const uint256_t &value, GasTracker *gas_tracker)
    {
        if (gas_tracker)
        {
            uint256_t old_value = load(key);
            if (value == old_value)
            {
                gas_tracker->add_gas_used(getSstoreGasCost(old_value, value));
            }
        }
        cache[key] = value;
    }

    uint256_t MyStorage::load(const uint256_t &key, GasTracker *gas_tracker)
    {
        if (gas_tracker)
        {
            gas_tracker->add_gas_used(getTouchedStorageGasCost());
        }
        auto it = cache.find(key);
        if (it != cache.end())
            return it->second;

        uint8_t b_address[32], b_key[32];
        mvm::to_big_endian(address, b_address);
        mvm::to_big_endian(key, b_key);

        if (isCache && State::instanceExists(address))
        {
            KeyType bkey = State::toKeyType(b_key); // Chuyển đổi sang KeyType

            auto result = State::getInstance(address)->getValue(bkey);
            if (result)
            {
                cache[key] = *result;
                return *result;
            }
        }

        auto get_rs = GetStorageValue(this->mvmId, b_address + 12, b_key);
        if (!get_rs.success)
        {
            return 0;
        }
        uint256_t value = mvm::from_big_endian(get_rs.value, 32u);
        cache[key] = value;

        if (isCache && State::instanceExists(address))
        {
            KeyType bkey = State::toKeyType(b_key); // Chuyển đổi sang KeyType
            State::getInstance(address)->insertOrUpdate(bkey, value);
        }
        return value;
    }

    bool MyStorage::exists(const uint256_t &key)
    {
        return cache.find(key) != cache.end();
    }

    bool MyStorage::remove(const uint256_t &key)
    {
        load(key, nullptr);
        if (cache.find(key) == cache.end())
            return false;
        cache[key] = 0;
        return true;
    }

    inline std::ostream &operator<<(std::ostream &os, const MyStorage &s)
    {
        return os;
    }
} // namespace mvm