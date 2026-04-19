// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once

#include "mvm/account.h"

#include <nlohmann/json.hpp>

namespace mvm
{
  class MyAccount : public Account
  {
  private:
    Address address = {};
    uint256_t balance = {};
    Code code = {};
    Nonce nonce = {};
    uint256_t last_hash = {};

    uint256_t add_balance_change = 0;
    uint256_t sub_balance_change = 0;

  public:
    MyAccount() = default;

    MyAccount(const Address &a, const uint256_t &b, const Code &c) : address(a),
                                                                     balance(b),
                                                                     code(c),
                                                                     nonce(0)
    {
    }

    MyAccount(
        const Address &a, const uint256_t &b, const Code &c, const uint256_t &n) : address(a),
                                                                        balance(b),
                                                                        code(c),
                                                                        nonce(n)
    {
    }

    virtual Address get_address() const override;
    void set_address(const Address &a);

    virtual uint256_t get_balance() const override;
    virtual void set_balance(const uint256_t &b) override;

    virtual Nonce get_nonce() const override;
    void set_nonce(Nonce n);
    virtual void increment_nonce() override;
    
    virtual Code get_code() const override;
    virtual void set_code(Code &&c) override;
    virtual bool has_code() override;

    bool operator==(const Account &) const;

    virtual uint256_t get_last_hash() const override;
    uint256_t get_storage_root() const;
  };
} // namespace mvm
