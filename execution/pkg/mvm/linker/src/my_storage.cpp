// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#include "my_storage.h"
#include "mvm/util.h"
#include "mvm/gas.h"
#include "mvm_linker.hpp"
#include "mvm/exception.h"
#include "mvm/safe_throw.h"
#include "state.h"

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
            // BUG FIX: this previously only charged when value == old_value
            // (the no-op case, correctly billed at getSstoreGasCost's 100-gas
            // base) and charged NOTHING when the value actually changed —
            // meaning every real SSTORE write (the 20000/2900-gas cases
            // getSstoreGasCost already computes) was free. getSstoreGasCost
            // itself already handles the unchanged-value case internally
            // (early-returns just the 100 base), so the condition here was
            // both wrong and redundant — call it unconditionally.
            uint256_t old_value = load(key);
            gas_tracker->add_gas_used(getSstoreGasCost(old_value, value));
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
        if (get_rs.status == STORAGE_SUSPEND)
        {
            MVM_THROW(Exception(ET::ErrExecutionReverted, "Block-STM: Estimate Hit (Suspend)"));
        }
        if (get_rs.status == STORAGE_NOT_FOUND)
        {
            // [QUAN TRỌNG - BLOCK-STM FIX] 
            // KHÔNG ĐƯỢC throw Exception ở đây! 
            // get_rs.status = 1 xảy ra khi:
            // 1. Storage slot hoàn toàn trống (chưa từng được ghi) -> EVM chuẩn bắt buộc trả về 0.
            // Nếu throw Exception, C++ sẽ kích hoạt std::abort() làm crash toàn bộ Node khi đọc slot trống.
            cache[key] = 0;
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

    bool MyStorage::has_cached(const uint256_t &key)
    {
        return cache.find(key) != cache.end();
    }

    uint256_t MyStorage::get_cached(const uint256_t &key)
    {
        auto it = cache.find(key);
        return it != cache.end() ? it->second : 0;
    }

    void MyStorage::set_cached_raw(const uint256_t &key, const uint256_t &value)
    {
        cache[key] = value;
    }

    void MyStorage::erase_cached(const uint256_t &key)
    {
        cache.erase(key);
    }

    std::map<uint256_t, uint256_t> MyStorage::snapshot_cached()
    {
        return cache;
    }

    void MyStorage::clear_all_cached()
    {
        cache.clear();
    }

    inline std::ostream &operator<<(std::ostream &os, const MyStorage &s)
    {
        return os;
    }
} // namespace mvm