// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once

#include "mvm/storage.h"
#include "my_account.h"

#include <map>
#include <nlohmann/json.hpp>

namespace mvm
{
  /**
   * merkle patricia trie implementation of Storage
   */
  class MyStorage : public Storage
  {
  public:
    unsigned char *mvmId;

  private:
    Address address = {};
    std::map<uint256_t, uint256_t> cache;
    bool isCache;

  public:
    MyStorage(){};
    MyStorage(const Address &a,   unsigned char* _mvmId, bool isCache) : address(a), mvmId(_mvmId) , isCache(isCache)//Thêm tham số và khởi tạo mvmId
    {
    }
    ~MyStorage();
    // MyStorage(const std::vector<std::vector<uint8_t>>&storage);

    bool isCached() const { return isCache; }
    void Clear();
    void store(const uint256_t& key, const uint256_t& value, GasTracker* gas_tracker = NULL) override;
    uint256_t load(const uint256_t& key, GasTracker* gas_tracker = NULL) override;
    bool remove(const uint256_t& key) override;
    bool exists(const uint256_t& key);
  };
} // namespace mvm
