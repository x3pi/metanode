// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#include "my_storage.h"
#include "mvm/util.h"
#include "mvm/gas.h"
#include "mvm_linker.hpp"
#include "mvm/exception.h"
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
            // [QUAN TRỌNG - BLOCK-STM FIX] 
            // KHÔNG ĐƯỢC throw Exception ở đây! 
            // get_rs.success = false xảy ra trong 2 trường hợp:
            // 1. Storage slot hoàn toàn trống (chưa từng được ghi) -> EVM chuẩn bắt buộc trả về 0.
            // 2. Giao dịch bị chặn (Suspend) bởi MVCC ở tầng Go.
            // Nếu throw Exception, C++ sẽ kích hoạt std::abort() làm crash toàn bộ Node khi đọc slot trống.
            // Việc trả về 0 khi bị Suspend cũng hoàn toàn AN TOÀN vì lớp Go sẽ kiểm tra cờ 
            // BlockingVersion sau khi thực thi xong và tự động VỨT BỎ kết quả sai này để chạy lại.
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

    inline std::ostream &operator<<(std::ostream &os, const MyStorage &s)
    {
        return os;
    }
} // namespace mvm