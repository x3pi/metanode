// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#include "my_account.h"

#include "mvm/rlp.h"
#include "mvm/util.h"
#include <iostream>

namespace mvm {
Address MyAccount::get_address() const { return address; }

void MyAccount::set_address(const Address &a) { address = a; }

uint256_t MyAccount::get_balance() const { return balance; }

void MyAccount::set_balance(const uint256_t &b) { balance = b; }

Account::Nonce MyAccount::get_nonce() const { return nonce; }

void MyAccount::set_nonce(Nonce n) { nonce = n; }

void MyAccount::increment_nonce() { ++nonce; }

Code MyAccount::get_code() const { return code; }

void MyAccount::set_code(Code &&c) { code = c; }

uint256_t MyAccount::get_last_hash() const { return last_hash; }

bool MyAccount::has_code() { return !get_code().empty(); }

bool MyAccount::operator==(const Account &a) const {
  return get_address() == a.get_address() && get_balance() == a.get_balance() &&
         get_nonce() == a.get_nonce() && get_code() == a.get_code();
}
} // namespace mvm
