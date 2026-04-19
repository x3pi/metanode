#pragma once
#include "mvm/bigint.h"
#include <tbb/concurrent_hash_map.h>
#include <array>
#include <iostream>
#include <optional>
#include <memory>
#include <vector>
#include <unordered_map>
#include <shared_mutex>

using namespace std;
using namespace tbb;

using KeyType = array<uint8_t, 32>;

struct KeyHashCompare
{
    bool equal(const KeyType &lhs, const KeyType &rhs) const;
    size_t hash(const KeyType &key) const;
};

struct AddressHashCompare
{
    bool equal(const uint256_t &lhs, const uint256_t &rhs) const;
    size_t hash(const uint256_t &address) const;
};
struct AddressStdHash
{
    size_t operator()(const uint256_t &address) const
    {
        auto low = static_cast<uint64_t>(address);
        auto mid1 = static_cast<uint64_t>(address >> 64);
        auto mid2 = static_cast<uint64_t>(address >> 128);
        auto high = static_cast<uint64_t>(address >> 192);
        size_t h1 = std::hash<uint64_t>{}(low);
        size_t h2 = std::hash<uint64_t>{}(mid1);
        size_t h3 = std::hash<uint64_t>{}(mid2);
        size_t h4 = std::hash<uint64_t>{}(high);
        size_t combined = h1;
        combined = (combined << 1) | (combined >> (sizeof(size_t) * 8 - 1));
        combined ^= h2;
        combined = (combined << 1) | (combined >> (sizeof(size_t) * 8 - 1));
        combined ^= h3;
        combined = (combined << 1) | (combined >> (sizeof(size_t) * 8 - 1));
        combined ^= h4;
        return combined;
    }
};
class State
{
public:
    static std::unordered_map<uint256_t, shared_ptr<State>, AddressStdHash> instances;
    static std::shared_mutex instances_mutex;

    State(const uint256_t &addr) : address(addr), nonce(0) {} // Đặt giá trị mặc định cho nonce
     // Phương thức mới để cập nhật thời gian tương tác
    void update_interaction_time();

    // Lấy thời gian tương tác cuối (có thể cần cho việc dọn dẹp)
    std::chrono::steady_clock::time_point get_last_interaction_time() const;

    static bool instanceExists(const uint256_t &address);         // Thêm hàm kiểm tra sự tồn tại của instance
    static void clearAllInstances();                              // Clear all cached state instances

    static shared_ptr<State> getInstance(const uint256_t &address);

    std::optional<uint256_t> getValue(const KeyType &key) const;
    void insertOrUpdate(const KeyType &key, const uint256_t &value);
    void erase(const KeyType &key);
    static KeyType toKeyType(const uint8_t cArray[32]); // Thêm hàm chuyển đổi

    uint256_t getAddress() const;
    uint256_t getBalance() const;
    void setBalance(const uint256_t &newBalance);

    const std::vector<uint8_t> &getCode() const;
    void setCode(const std::vector<uint8_t> &newCode);

    uint256_t getNonce() const;
    void setNonce(const uint256_t &newNonce);

    uint256_t getLastHash() const;
    void setLastHash(const uint256_t &newLastHash);
    bool keyExists(const KeyType &key) const;

private:
    State(const State &) = delete;
    State &operator=(const State &) = delete;


    concurrent_hash_map<KeyType, uint256_t, KeyHashCompare> stateMap;
    uint256_t address;
    uint256_t balance;
    std::vector<uint8_t> code;
    uint256_t nonce;
    uint256_t last_hash = {};

    // Thêm thành viên lưu thời gian tương tác cuối
    std::atomic<std::chrono::steady_clock::time_point> last_interaction_time;
};
