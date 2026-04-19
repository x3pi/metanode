#pragma once

#include "mvm/globalstate.h"
#include "my_storage.h"
#include "my_account.h"
#include <set>
#include <vector>
// Thêm vào file header (my_global_state.h hoặc nơi phù hợp)
namespace mvm
{
    /**
     * My implementation of GlobalState
     */
    class MyGlobalState : public GlobalState
    {
    public:
        using StateEntry = std::pair<MyAccount, MyStorage>;

        std::map<Address, Code> addresses_newly_deploy;
        std::map<Address, std::map<uint256_t, uint256_t>> addresses_storage_change;
        std::map<Address, uint256_t> addresses_add_balance_change;
        std::map<Address, uint256_t> addresses_sub_balance_change;
        std::map<Address, uint256_t> addresses_nonce_change;

    private:
        BlockContext blockContext;

        std::map<Address, StateEntry> accounts;

        bool isCache;
        std::set<Address> allowedAddresses;

    public:
        MyGlobalState() = default;
        explicit MyGlobalState(BlockContext blockContext, bool isCache) : blockContext(std::move(blockContext)), isCache(isCache) {} // Sửa lỗi thiếu dấu chấm phẩy và thêm khởi tạo isCache

        explicit MyGlobalState(
            BlockContext blockContext,
            bool isCache,
            const std::vector<Address> &relatedAddrs) : blockContext(std::move(blockContext)), isCache(isCache)
        {
            for (const auto &addr : relatedAddrs)
            {
                allowedAddresses.insert(addr);
            }
        }
        bool isAddressAllowed(const Address &addr) const
        {
            if (allowedAddresses.empty())
            {
                return true;
            }
            return allowedAddresses.find(addr) != allowedAddresses.end();
        }
        bool is_cache() override;
        virtual void remove(const Address &addr) override;

        AccountState get(const Address &addr, GasTracker *gas_tracker = NULL) override;
        AccountState get(const Address &addr, GasTracker *gas_tracker, const uint256_t &lashHash) override;
        AccountState getUpdate(const Address &addr) override;

        AccountState create(
            const Address &addr, const uint256_t &balance, const Code &code, const uint256_t &nonce) override;
        bool exists(const Address &addr);
        size_t num_accounts();

        virtual const BlockContext &get_block_context() override;
        virtual void set_block_context(const BlockContext &blockContext) override;

        virtual uint256_t get_block_hash(int) override;
        virtual uint256_t get_chain_id() override;

        // Cross-chain precompile (address 263)
        virtual std::vector<uint8_t> get_cross_chain_sender() override;
        virtual std::vector<uint8_t> get_cross_chain_source_id() override;
        void iterate_storage_changes(std::function<void(const Address &, const uint256_t &, const uint256_t &)> callback) const
        {
            for (const auto &pair : addresses_storage_change)
            {
                const Address &address = pair.first;
                for (const auto &innerPair : pair.second)
                {
                    callback(address, innerPair.first, innerPair.second);
                }
            }
        }

        void iterate_newly_deployed(std::function<void(const Address &, const Code &)> callback) const
        {
            for (const auto &pair : addresses_newly_deploy)
            {
                callback(pair.first, pair.second);
            }
        }

        void iterate_add_balance_changes(std::function<void(const Address &, const uint256_t &)> callback) const
        {
            for (const auto &pair : addresses_add_balance_change)
            {
                callback(pair.first, pair.second);
            }
        }

        void iterate_sub_balance_changes(std::function<void(const Address &, const uint256_t &)> callback) const
        {
            for (const auto &pair : addresses_sub_balance_change)
            {
                callback(pair.first, pair.second);
            }
        }

        void iterate_nonce_changes(std::function<void(const Address &, const uint256_t &)> callback) const
        {
            for (const auto &pair : addresses_nonce_change)
            {
                callback(pair.first, pair.second);
            }
        }

        /**
         * Add and Extract changes data from global state
         * to create result
         */
        virtual void add_addresses_newly_deploy(const Address &addr, const Code &code) override;
        virtual void add_addresses_storage_change(const Address &addr, const uint256_t &key, const uint256_t &value) override;
        virtual void add_addresses_add_balance_change(const Address &addr, const uint256_t &amount) override;
        virtual void add_addresses_sub_balance_change(const Address &addr, const uint256_t &amount) override;
        virtual void set_addresses_nonce_change(const Address &addr, const uint256_t &nonce) override;

        const std::vector<std::vector<uint8_t>> get_newly_deploy();
        const std::vector<std::vector<uint8_t>> get_storage_change();
        const std::vector<std::vector<uint8_t>> get_add_balance_change();
        const std::vector<std::vector<uint8_t>> get_sub_balance_change();
        const std::vector<std::vector<uint8_t>> get_nonce_change();

        /**
         * For tests which require some initial state, allow manual insertion of
         * pre-constructed accounts
         */
        void insert(const StateEntry &e);
        // operator== removed — was never called and always returned true (bug)
        void Clear();
        void clear_differences();
    };

} // namespace mvm
