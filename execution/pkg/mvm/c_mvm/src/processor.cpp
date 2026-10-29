// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#include "mvm/processor.h"
#include "mvm/cross_chain_precompile.h"

#include "mvm/bigint.h"
#include "mvm/exception.h"
#include "mvm/gas.h"
#include "mvm/opcode.h"
#include "mvm/safe_throw.h"
#include "mvm/stack.h"
#include "mvm/util.h"

#include <algorithm>
#include <atomic>
#include <cstdio>
#include <exception>
#include <fstream>
#include <functional>
#include <limits>
#include <memory>
#include <set>
#include <type_traits>
#include <utility>
// #include <chrono>
#include <filesystem> // Thư viện để thao tác với thư mục
#include <unordered_map>
#include <unordered_set>

#include <intx/intx.hpp>

#include <cctype>
#include <iomanip>
#include <iostream>
#include <sstream>

namespace std {
template <> struct hash<intx::uint<256>> {
  std::size_t operator()(const intx::uint<256> &x) const noexcept {
    // Sử dụng giá trị thấp nhất của uint256_t để tính hash
    return std::hash<uint64_t>()(static_cast<uint64_t>(x));
  }
};
} // namespace std

using namespace std;

// 2026-08-20 (plan §9.26/§9.27): see mvm_linker.hpp's doc comment on
// MVM_SetDebugFileLoggingEnabled() for the full story -- saveDebugInfo()
// below does real filesystem I/O, which the secure-world TA build has none
// of. Defaults to true (matches every existing cgo/Go caller's behavior
// unchanged); mvm_ta_main.cpp is the one caller that turns it off.
static std::atomic<bool> g_debug_file_logging_enabled{true};

extern "C" void MVM_SetDebugFileLoggingEnabled(bool enabled) {
  g_debug_file_logging_enabled.store(enabled, std::memory_order_relaxed);
}

namespace mvm {

struct Consts {
  static constexpr auto MAX_CALL_DEPTH = 1024u;
  static constexpr auto WORD_SIZE = 32u;
  static constexpr auto MAX_MEM_SIZE = 1ull << 25; // 32 MB
  static constexpr uint64_t MAX_INITCODE_SIZE = 1024 * 1024; // 1 MB (allows deploying 512 KB runtime code)
  static constexpr uint64_t MAX_CODE_SIZE = 512 * 1024;      // 512 KB runtime code
};

inline int get_sign(const uint256_t &v) { return (v >> 255) ? -1 : 1; }

/**
 * bytecode program
 */
class Program {
public:
  const vector<uint8_t> code;
  const set<uint64_t> jump_dests;

  Program(vector<uint8_t> &&c)
      : code(c), jump_dests(compute_jump_dests(code)) {}

private:
  set<uint64_t> compute_jump_dests(const vector<uint8_t> &code) {
    set<uint64_t> dests;
    for (uint64_t i = 0; i < code.size(); i++) {
      const auto op = code[i];
      if (op >= PUSH1 && op <= PUSH32) {
        const uint8_t immediate_bytes = op - static_cast<uint8_t>(PUSH1) + 1;
        i += immediate_bytes;
      } else if (op == JUMPDEST)
        dests.insert(i);
    }
    return dests;
  }
};

/**
 * execution context of a call
 */
class Context {
private:
  uint64_t pc = 0;
  bool pc_changed = true;

  using PcType = decltype(pc);

public:
  using ReturnHandler = function<void(vector<uint8_t>)>;
  using HaltHandler = function<void()>;
  using ExceptionHandler = function<void(const Exception &)>;

  vector<uint8_t> mem;
  uint64_t last_mem_gas_cost;
  Stack s;

  AccountState as;
  Account &acc;
  Storage &st;
  const Address caller;
  const vector<uint8_t> input;
  const uint256_t call_value;
  const Program prog;
  ReturnHandler rh;
  HaltHandler hh;
  ExceptionHandler eh;
  bool read_only;
  vector<uint8_t> returnData;

  Context(const Address &caller, AccountState as, vector<uint8_t> &&input,
          const uint256_t &call_value, Program &&prog, ReturnHandler &&rh,
          HaltHandler &&hh, ExceptionHandler &&eh, bool read_only)
      : as(as), acc(as.acc), st(as.st), caller(caller), input(input),
        call_value(call_value), prog(prog), rh(rh), hh(hh), eh(eh),
        read_only(read_only) {
    last_mem_gas_cost = 0;
  }

  /// increment the pc if it wasn't changed before
  void step() {
    if (pc_changed)
      pc_changed = false;
    else
      pc++;
  }

  PcType get_pc() const { return pc; }

  void set_pc(const PcType pc_) {
    pc = pc_;
    pc_changed = true;
  }

  bool pc_valid() const { return pc < prog.code.size(); }

  auto get_used_mem() const {
    return (mem.size() + Consts::WORD_SIZE - 1) / Consts::WORD_SIZE;
  }
};

/**
 * Call-frame state journal.
 *
 * Every state mutation (storage, balance, code deploy, transient storage,
 * logs — see each call site below) records an "undo" closure here BEFORE it
 * mutates anything. mark() returns a checkpoint (just the current entry
 * count); revert_to(m) runs and discards every undo closure recorded since
 * that checkpoint, in reverse (LIFO) order, restoring exactly the state that
 * existed at the checkpoint — regardless of how many opcodes or how deeply
 * nested the reverted call frame's own sub-calls were, since a revert_to
 * simply pops back to an index into one shared, flat, transaction-scoped
 * stack.
 *
 * This fixes a real EVM-atomicity violation: previously, when a nested
 * CALL/CALLCODE/DELEGATECALL/STATICCALL/CREATE/CREATE2 reverted (or threw
 * any other exception — out of gas, invalid jump, etc.), the caller caught
 * it and kept running, but any storage/balance/code/log/transient-storage
 * changes the reverted callee (or its own nested calls) had already made
 * were never undone.
 *
 * Nonce increments from CREATE/CREATE2 are journaled so that if the CALLER's
 * own frame reverts in a nested call, the caller's nonce is correctly rolled back.
 * However, inside create()/opCreate2(), the child constructor's journalMark is
 * placed AFTER the caller's nonce bump, so a failed child constructor preserves
 * the caller's nonce increment (per EIP-161).
 * Deliberately NOT journaled: gas consumption (never refunded on revert by
 * design — gas_tracker is a single counter for the whole transaction and
 * already behaves correctly without any change here), and the read-only
 * address/storage-read "warm" tracking (EIP-2929 warmth persists for the rest
 * of the transaction even if the call that first touched an address/slot reverts).
 */
class StateJournal {
  vector<function<void()>> entries;

public:
  size_t mark() const { return entries.size(); }

  void record(function<void()> undo) { entries.push_back(move(undo)); }

  void revert_to(size_t m) {
    while (entries.size() > m) {
      entries.back()();
      entries.pop_back();
    }
  }
};

/**
 * implementation of the VM
 */
class _Processor {
private:
  /// the interface to the global state
  GlobalState &gs;
  LogHandler &log_handler;
  Extension &extension;
  NativeLogger &native_logger;
  GasTracker &gas_tracker;

  /// the transaction object
  Transaction &tx;
  /// pointer to trace object (for debugging)
  Trace *const tr;
  /// the stack of contexts (one per nested call)
  vector<unique_ptr<Context>> ctxts;
  /// pointer to the current context
  Context *ctxt;

  // THAY ĐỔI 1: Thêm bộ đệm để lưu trữ dấu vết opcode
  std::vector<std::string> opcode_trace_buffer;

  using ET = Exception::Type;

public:
  _Processor(GlobalState &gs, LogHandler &log_handler, Extension &extension,
             Transaction &tx, Trace *tr, NativeLogger &native_logger,
             GasTracker &gas_tracker)
      : gs(gs), log_handler(log_handler), extension(extension), tx(tx), tr(tr),
        native_logger(native_logger), gas_tracker(gas_tracker) {}

  ~_Processor() {
    ctxts.clear();
  }

  ExecResult
  run(const Address &caller, AccountState callee, bool deploy,
      vector<uint8_t> input, // Take a copy here, then move it into context
      const uint256_t &call_value, bool readOnly) {
    // THAY ĐỔI 2: Xóa bộ đệm khi bắt đầu một lần chạy mới
    opcode_trace_buffer.clear();

    // EIP-2929 / EIP-3651: the sender, the transaction target (or the contract being
    // created), the block's coinbase and the precompiles start warm.
    // EIP-2930: addresses and storage keys declared in the transaction's access list start warm.
    warm_addresses.clear();
    warm_slots.clear();
    warm_addresses.insert(tx.origin);
    warm_addresses.insert(caller);
    warm_addresses.insert(callee.acc.get_address());
    warm_addresses.insert(gs.get_block_context().coinbase);
    for (uint64_t pc = 1; pc <= 10; pc++)
      warm_addresses.insert(Address(pc));
    for (const auto &item : tx.access_list) {
      warm_addresses.insert(item.first);
      for (const auto &slot : item.second) {
        warm_slots[item.first].insert(slot);
      }
    }
    // EIP-7702: If the transaction target is a delegation designator, add its
    // delegated account to warm_addresses (matches go-ethereum core/state_transition.go:504).
    if (!deploy && is_delegation_designator(callee.acc.get_code())) {
      warm_addresses.insert(delegation_designator_address(callee.acc.get_code()));
    }

    // create the first context
    ExecResult result;
    auto rh = [&result, deploy, this](vector<uint8_t> output_) {
      if (deploy) {
        // A contract-creation transaction fails (nothing is deployed and all
        // the gas is consumed) when the init code returns runtime code that
        // is too large (EIP-170), starts with 0xEF (EIP-3541), or that the
        // remaining gas cannot pay the 200-gas-per-byte deposit for.
        const char *failure = nullptr;
        ET failureType = ET::outOfBounds;
        const uint64_t depositCost = getCodeDepositCost(output_.size());
        if (output_.size() > Consts::MAX_CODE_SIZE) {
          failure = "Max code size exceeded (limit: 524288 bytes / 512 KB)";
          failureType = ET::ErrMaxCodeSizeExceeded;
        } else if (!output_.empty() && output_[0] == 0xEF) {
          failure = "invalid code: must not begin with 0xef (EIP-3541)";
          failureType = ET::ErrInvalidCode;
        } else if (gas_tracker.gas_left() < depositCost) {
          failure = "Out of gas (code deposit)";
          failureType = ET::ErrOutOfGas;
        }
        if (failure != nullptr) {
          journal.revert_to(0);
          gas_tracker.burn_frame();
          result.er = ExitReason::threw;
          result.ex = failureType;
          result.exmsg = failure;
          result.output = mvm::encode_revert_string(result.exmsg);
          return;
        }
        gas_tracker.add_gas_used(depositCost);
      }
      result.er = ExitReason::returned;
      result.output = move(output_);
    };
    auto hh = [&result]() { result.er = ExitReason::halted; };
    auto eh = [&](const Exception &ex_) {
      journal.revert_to(0);
      // An exceptional halt (anything but REVERT) consumes all the gas of the
      // transaction, as on Ethereum; a REVERT keeps the unused gas.
      if (ex_.type != ET::ErrExecutionReverted)
        gas_tracker.burn_frame();
      result.er = ExitReason::threw;
      result.ex = ex_.type;
      // MVM_TRY/MVM_CATCH (mvm/safe_throw.h): was try{...}catch(...){...}.
      // Anything thrown while formatting the exception (e.g. stack pop64()
      // underflow, copy_from_mem() out-of-bounds) is caught here rather
      // than escaping -- see safe_throw.h's doc comment for why this
      // can't be a real C++ catch in the TA build.
      MVM_TRY
        if (result.ex == ET::ErrExecutionReverted) {
          if (ctxt && ctxt->s.size() >= 2) {
            const auto offsetWord = ctxt->s.pop();
            const auto size = ctxt->s.pop64();
            const auto offset = mem_offset(offsetWord, size);
            if (size != 0) {
              auto revert_data = copy_from_mem(offset, size);
              result.output = move(revert_data);
            }
          }
          if (ctxt && !ctxt->returnData.empty()) {
            std::string original_msg(ctxt->returnData.begin(),
                                     ctxt->returnData.end());
            result.exmsg = original_msg;
          } else {
            result.exmsg = ex_.what();
          }
        } else {
          result.output = mvm::encode_revert_string(ex_.what());
          result.exmsg = ex_.what();
        }
      MVM_CATCH(inner_ex)
        (void)inner_ex;
        result.exmsg = ex_.what();
      MVM_END_TRY
    };

    vector<uint8_t> exec_code;
    vector<uint8_t> calldata;
    if (deploy) {
      exec_code = move(input);
      calldata = vector<uint8_t>();
      created_this_tx.insert(callee.acc.get_address());
    } else {
      // EIP-7702: this is the top-level entry point for a transaction whose
      // `To` is called directly (as opposed to a CALL/CALLCODE/DELEGATECALL/
      // STATICCALL opcode executed from within an already-running context,
      // handled by call() below) — must resolve a delegation designator the
      // same way, or a tx that calls a delegated EOA directly throws
      // ErrInvalidCode the moment dispatch() hits the designator's 0xEF byte.
      exec_code = resolve_delegated_code(callee.acc.get_code());
      calldata = move(input);
    }

    if (!deploy && exec_code.empty()) {
      rh({});
      uint64_t used = gas_tracker.get_gas_used();
      uint64_t refund = std::min<uint64_t>(
          gas_tracker.get_refund() > 0
              ? static_cast<uint64_t>(gas_tracker.get_refund())
              : 0,
          used / 5);
      result.gas_used = used - refund;
      result.gas_pre_refund = used;
      result.gas_refund = gas_tracker.get_refund() > 0
                              ? static_cast<uint64_t>(gas_tracker.get_refund())
                              : 0;
      return result;
    }

    // EIP-3860: init code of a contract-creation transaction is limited to
    // 1048576 bytes (1 MB).
    if (deploy && exec_code.size() > Consts::MAX_INITCODE_SIZE) {
      eh(Exception(ET::outOfBounds, "Max initcode size exceeded (limit: 1048576 bytes / 1 MB)"));
      result.gas_used = gas_tracker.get_gas_used();
      result.gas_pre_refund = result.gas_used;
      return result;
    }

    push_context(caller, callee, move(calldata), move(exec_code), call_value,
                 rh, hh, eh, readOnly);
    // A context starts with its "pc changed" flag set because the run loop steps the context that a CALL / CREATE
    // just pushed once, right after the call instruction was dispatched. The root context is not pushed by any
    // instruction, so consume that flag here: otherwise its first instruction (unless it is a PUSH or a jump) would
    // run twice.
    ctxt->step();

    // Intrinsic gas (base 21000/53000, calldata, access-list, EIP-7702
    // auth-tuple cost) is computed and charged on the Go side (see
    // vm_processor.computeIntrinsicGas / applyIntrinsicGas) and already
    // subtracted from gas_tracker's budget (tx.gas_limit, set below) before
    // this function runs — charging a flat 2100/32000 here on top of that
    // would double-charge every transaction.
    auto sm_size = ctxt->prog.code.size();
    // run
    while (ctxt) {
      if (ctxt->get_pc() >= ctxt->prog.code.size()) {
        if (ctxts.size() > 1) {
          // A sub-context (e.g. child constructor or callee) ran off the end of its code.
          // In EVM, running off the end of code is an implicit STOP:
          // it halts normally with empty return data and returns control to the parent context.
          stop();
          if (ctxt)
            ctxt->step();
          continue;
        } else {
          // The root context ran off the end of its code: also an implicit
          // STOP (same as an explicit STOP opcode), not a failure.
          stop();
          break;
        }
      }

      // MVM_TRY/MVM_CATCH (mvm/safe_throw.h): was try{...}catch(Exception&){...}.
      MVM_TRY
        dispatch(result);
      MVM_CATCH(ex)
        ctxt->eh(ex);
        pop_context();
      MVM_END_TRY

      if (!ctxt)
        break;
      ctxt->step();
    }

    // halt outer context if it did not do so itself
    if (ctxt) {
      auto hh = ctxt->hh;
      pop_context();
      hh();
    }

    // EIP-6780 finalization: contracts created in THIS transaction that
    // self-destructed must have their accounts cleaned up (nonce reset/erased,
    // code/deploy entry erased, storage changes erased, and account removed
    // from global state) so they do not persist as zombie accounts with nonce > 0.
    if (result.er != ExitReason::threw) {
      for (const auto &addr : destructed_this_tx) {
        auto acc = gs.get(addr);
        uint256_t remBalance = acc.acc.get_balance();
        if (remBalance > 0) {
          acc.acc.set_balance(0);
          gs.add_addresses_sub_balance_change(addr, remBalance);
        }
        gs.erase_nonce_change(addr);
        gs.erase_newly_deploy(addr);
        gs.clear_storage_change(addr);
        gs.remove(addr);
      }
    }

    // EIP-3529: refund is capped at gasUsed/5 (post-London; this VM has no
    // hardfork-activation mechanism so the pre-London 1/2 cap never
    // applies here — see params.AllDevChainProtocolChanges elsewhere in
    // this codebase for the same "every EIP always active" choice) and
    // applied once, here, for the whole top-level transaction — never
    // per-nested-call-frame, since refunds are a transaction-wide budget
    // reduction, not a per-call return value.
    uint64_t used = gas_tracker.get_gas_used();
    uint64_t refund = std::min<uint64_t>(
        gas_tracker.get_refund() > 0
            ? static_cast<uint64_t>(gas_tracker.get_refund())
            : 0,
        used / 5);
    result.gas_used = used - refund;
    result.gas_pre_refund = used;
    result.gas_refund = gas_tracker.get_refund() > 0
                            ? static_cast<uint64_t>(gas_tracker.get_refund())
                            : 0;
    return result;
  }

private:
  // PHASE-5 FIX: was a single flat map keyed only by slot, shared across every
  // contract in the whole call stack — two unrelated contracts touching the
  // "same" slot number (near-guaranteed, since slots are usually small
  // sequential integers) silently aliased onto each other's transient
  // storage. EIP-1153 requires transient storage to be per-address, exactly
  // like persistent storage. Now keyed by (address, slot).
  //
  // Rolled back on a reverted nested call via `journal` below — see tLoad/
  // tStore and StateJournal's doc comment.
  std::unordered_map<Address, std::unordered_map<uint256_t, uint256_t>> transient_storage;

  // See StateJournal's doc comment above this class.
  StateJournal journal;

  // EIP-2200/3529 "original value" tracking for the SSTORE gas refund (see
  // sstore()): the value a slot held at the
  // START of this transaction, captured once on first touch and never
  // updated again afterwards (deliberately NOT journaled/reverted — the
  // "original" value is a fact about transaction start, unaffected by
  // anything that happens, or gets undone, later in the same transaction).
  std::unordered_map<Address, std::unordered_map<uint256_t, uint256_t>> original_storage_values;

  // EIP-6780 support (see selfdestruct()): addresses a CREATE/CREATE2 was
  // attempted for in THIS transaction, recorded the moment gs.create(...) is
  // called (not only on successful constructor return) — matches "was this
  // account created earlier in the same transaction" regardless of what the
  // constructor goes on to do. Deliberately NOT journaled: even a reverted
  // CREATE attempt leaves the address with no code (see StateJournal's
  // rollback of the code-deploy entry), so a later SELFDESTRUCT on it can't
  // do anything harmful either way — same reasoning as CREATE's nonce bump.
  std::unordered_set<Address> created_this_tx;

  // EIP-6780 support: accounts created in THIS transaction that executed SELFDESTRUCT.
  // These accounts must be deleted (clearing code, storage, and nonce changes)
  // at the end of the transaction if it succeeds.
  std::unordered_set<Address> destructed_this_tx;

  // EIP-2929 access sets: an address or storage slot is "cold" until its first access in the transaction, which costs
  // more and then warms it. Warming is journaled like any other state change: a reverted frame un-warms what it touched.
  std::unordered_set<Address> warm_addresses;
  std::unordered_map<Address, std::unordered_set<uint256_t>> warm_slots;

  // Returns true if the address was already warm; warms it otherwise.
  bool touch_address(const Address &a) {
    if (!warm_addresses.insert(a).second)
      return true;
    journal.record([this, a]() { warm_addresses.erase(a); });
    return false;
  }

  // Returns true if the slot was already warm; warms it otherwise.
  bool touch_slot(const Address &a, const uint256_t &k) {
    if (!warm_slots[a].insert(k).second)
      return true;
    journal.record([this, a, k]() { warm_slots[a].erase(k); });
    return false;
  }

  // Gas of accessing an account: 100 when warm, 2600 when cold (and warms it).
  uint64_t account_access_cost(const Address &a) {
    return touch_address(a) ? 100 : 2600;
  }

  // EIP-161 "empty": no code, zero nonce and zero balance.
  bool account_is_empty(const Address &a) {
    decltype(auto) acc = gs.get(a).acc;
    return acc.get_nonce() == 0 && acc.get_balance() == 0 &&
           acc.get_code().empty();
  }

  void push_context(const Address &caller, AccountState as,
                    vector<uint8_t> &&input, Program &&prog,
                    const uint256_t &call_value, Context::ReturnHandler &&rh,
                    Context::HaltHandler &&hh, Context::ExceptionHandler &&eh,
                    bool read_only) {
    if (get_call_depth() > Consts::MAX_CALL_DEPTH)
      MVM_THROW(Exception(ET::ErrDepth, "Reached max call depth (" +
                                            to_string(Consts::MAX_CALL_DEPTH) +
                                            ")"));

    auto c =
        make_unique<Context>(caller, as, move(input), call_value, move(prog),
                             move(rh), move(hh), move(eh), read_only);
    ctxts.emplace_back(move(c));
    ctxt = ctxts.back().get();
  }

  uint16_t get_call_depth() const {
    return static_cast<uint16_t>(ctxts.size());
  }

  // Transfers `amount` from fromAcc to toAcc (mirroring the existing
  // Account::pay_to + gs.add_addresses_*_balance_change pattern used at
  // every balance-mutating call site — CALL's value transfer, CREATE/
  // CREATE2's endowment, SELFDESTRUCT), and records a journal entry that
  // restores both accounts' balances (and the corresponding diff-tracker
  // entries) to their exact pre-transfer values if the enclosing call frame
  // is later reverted. Absolute snapshot/restore rather than "transfer back
  // the same amount" so it composes correctly under LIFO revert_to() no
  // matter what other operations touch these same accounts in between.
  void journaled_pay_to(const Address &fromAddr, const Address &toAddr,
                        Account &fromAcc, Account &toAcc,
                        const uint256_t &amount) {
    if (amount == 0)
      return;
    // A transfer to oneself changes nothing. Recording it as a separate
    // sub + add would make the sub total exceed the balance when the Go side
    // applies the diffs (subs first), failing a valid transaction.
    if (fromAddr == toAddr)
      return;
    uint256_t oldFromBalance = fromAcc.get_balance();
    uint256_t oldToBalance = toAcc.get_balance();
    fromAcc.pay_to(toAcc, amount);
    gs.add_addresses_sub_balance_change(fromAddr, amount);
    gs.add_addresses_add_balance_change(toAddr, amount);
    journal.record([this, fromAddr, toAddr, oldFromBalance, oldToBalance,
                    amount]() {
      gs.get(fromAddr).acc.set_balance(oldFromBalance);
      gs.get(toAddr).acc.set_balance(oldToBalance);
      gs.undo_sub_balance_change(fromAddr, amount);
      gs.undo_add_balance_change(toAddr, amount);
    });
  }

  Opcode get_op() const {
    return static_cast<Opcode>(ctxt->prog.code[ctxt->get_pc()]);
  }

  uint256_t pop_addr(Stack &st) {
    static const uint256_t MASK_160 = (uint256_t(1) << 160) - 1;
    return st.pop() & MASK_160;
  }

  void pop_context() {
    ctxts.pop_back();
    if (!ctxts.empty())
      ctxt = ctxts.back().get();
    else
      ctxt = nullptr;
  }

  static void copy_mem_raw(const uint64_t offDst, const uint64_t offSrc,
                           const uint64_t size, vector<uint8_t> &dst,
                           const vector<uint8_t> &src, const uint8_t pad = 0) {
    if (!size)
      return;

    const auto lastDst = offDst + size;
    if (lastDst < offDst)
      MVM_THROW(Exception(ET::outOfBounds, "Integer overflow in copy_mem (" +
                                               to_string(lastDst) + " < " +
                                               to_string(offDst) + ")"));

    if (lastDst > Consts::MAX_MEM_SIZE)
      MVM_THROW(Exception(ET::outOfBounds,
                          "Memory limit exceeded (" + to_string(lastDst) + " > " +
                              to_string(Consts::MAX_MEM_SIZE) + ")"));

    if (lastDst > dst.size())
      dst.resize(lastDst);

    const auto lastSrc = offSrc + size;
    const auto endSrc =
        min(lastSrc, static_cast<decltype(lastSrc)>(src.size()));
    uint64_t remaining;
    if (endSrc > offSrc) {
      copy(src.begin() + offSrc, src.begin() + endSrc, dst.begin() + offDst);
      remaining = lastSrc - endSrc;
    } else {
      remaining = size;
    }

    // if there are more bytes to copy than available, add padding
    fill(dst.begin() + lastDst - remaining, dst.begin() + lastDst, pad);
  }

  void copy_mem(vector<uint8_t> &dst, const vector<uint8_t> &src,
                const uint8_t pad) {
    uint64_t old_mem_word_size = ctxt->get_used_mem();
    //
    const auto offDstWord = ctxt->s.pop();
    // The source offset is a full 256-bit word: anything at or beyond the end
    // of the source is simply all padding (zeros), never an error.
    const auto offSrcWord = ctxt->s.pop();
    const uint64_t offSrc =
        offSrcWord > uint256_t(src.size()) ? src.size()
                                           : static_cast<uint64_t>(offSrcWord);
    const auto size = ctxt->s.pop64();
    const auto offDst = mem_offset(offDstWord, size);
    copy_mem_raw(offDst, offSrc, size, dst, src, pad);
    //
    uint64_t new_mem_word_size = ctxt->get_used_mem();
    gas_tracker.add_gas_used(getCopyOperationGasCost(
        get_word_size(size), ctxt->last_mem_gas_cost, old_mem_word_size,
        new_mem_word_size));
  }

  void prepare_mem_access(const uint64_t offset, const uint64_t size) {
    const auto end = offset + size;
    if (end < offset)
      MVM_THROW(Exception(ET::outOfBounds, "Integer overflow in memory access (" +
                                               to_string(end) + " < " +
                                               to_string(offset) + ")"));

    if (end > Consts::MAX_MEM_SIZE)
      MVM_THROW(Exception(ET::outOfBounds,
                          "Memory limit exceeded (" + to_string(end) + " > " +
                              to_string(Consts::MAX_MEM_SIZE) + ")"));

    if (end > ctxt->mem.size())
      ctxt->mem.resize(end);
  }

  // Expands memory to cover [offset, offset+size) and charges the expansion (nothing for an empty range).
  void expand_mem_charged(const uint64_t offset, const uint64_t size) {
    if (size == 0)
      return;
    const uint64_t old_mem_word_size = ctxt->get_used_mem();
    prepare_mem_access(offset, size);
    gas_tracker.add_gas_used(getMemExpansionGasCost(
        ctxt->last_mem_gas_cost, old_mem_word_size, ctxt->get_used_mem()));
  }

  // The offset of a memory range whose length is zero is irrelevant (the EVM
  // neither expands memory nor fails for it, even when the offset does not fit
  // in 64 bits); otherwise the offset must fit in 64 bits (a larger one would
  // exceed any possible memory anyway).
  static uint64_t mem_offset(const uint256_t &offsetWord, const uint64_t size) {
    if (size == 0)
      return 0;
    if (offsetWord > uint256_t(std::numeric_limits<uint64_t>::max()))
      MVM_THROW(Exception(ET::outOfBounds,
                          "Memory offset (" + to_hex_string(offsetWord) +
                              ") exceeds uint64_t maximum"));
    return static_cast<uint64_t>(offsetWord);
  }

  vector<uint8_t> copy_from_mem(const uint64_t offset, const uint64_t size) {
    // A zero-length access never touches memory, whatever the offset. Any
    // other access expands memory (zero-filled) and charges for it, exactly
    // like the EVM: reading a region that was never written yields zeros, not
    // an empty buffer (RETURN / REVERT / LOG / CREATE / CALL input depend on it).
    if (size == 0) {
      return {};
    }
    uint64_t old_mem_word_size = ctxt->get_used_mem();
    //
    prepare_mem_access(offset, size);
    //
    uint64_t new_mem_word_size = ctxt->get_used_mem();
    // Reading a memory range costs only its expansion; the per-word copy fee
    // belongs to the opcodes that copy (CALLDATACOPY, CODECOPY, ...).
    gas_tracker.add_gas_used(getMemExpansionGasCost(
        ctxt->last_mem_gas_cost, old_mem_word_size, new_mem_word_size));

    return {ctxt->mem.begin() + offset, ctxt->mem.begin() + offset + size};
  }

  void jump_to(const uint256_t &newPc) {
    if (newPc > std::numeric_limits<uint64_t>::max() ||
        ctxt->prog.jump_dests.find(static_cast<uint64_t>(newPc)) ==
            ctxt->prog.jump_dests.end())
      MVM_THROW(Exception(ET::ErrInvalidCode,
                          to_string(newPc) + " is not a jump destination"));
    ctxt->set_pc(static_cast<uint64_t>(newPc));
  }

  template <
      typename X, typename Y,
      typename = enable_if_t<is_unsigned<X>::value && is_unsigned<Y>::value>>
  static auto safeAdd(const X x, const Y y) {
    const auto r = x + y;
    if (r < x)
      // 2026-08-21: real C++ throw, deliberately NOT converted to
      // MVM_THROW (mvm/safe_throw.h) -- it's std::overflow_error, not
      // mvm::Exception, and this header's mechanism only covers the
      // latter. Will still hang if hit in the TA build, same as before
      // this file's other throws were fixed -- a known, narrower residual
      // gap (astronomically large unsigned addition, far less likely to
      // be hit by real contract execution than anything covered).
      throw overflow_error("integer overflow");
    return r;
  }

  template <typename T> static T shrink(uint256_t i) {
    return static_cast<T>(i & numeric_limits<T>::max());
  }

  void saveDebugInfo(const Transaction &tx, uint8_t op, const Context *ctxt) {
    // 2026-08-20: see MVM_SetDebugFileLoggingEnabled()'s doc comment
    // (mvm_linker.hpp) -- this function does real filesystem I/O, which
    // crashes hard (not a graceful error) on the secure-world TA build.
    if (!g_debug_file_logging_enabled.load(std::memory_order_relaxed)) {
      return;
    }

    // Thư mục lưu trữ file log
    const std::string directory = "./tx_debug/";

    // Kiểm tra và tạo thư mục nếu chưa tồn tại
    std::error_code ec;
    if (!std::filesystem::exists(directory) &&
        !std::filesystem::create_directories(directory, ec)) {
      std::cerr << "Lỗi: Không thể tạo thư mục " << directory << " ("
                << ec.message() << ")" << std::endl;
      return;
    }

    // Chuyển đổi tx_hash thành chuỗi hex
    std::string stx_hash_hex = to_hex_string(tx.tx_hash);

    // Tạo filename hợp lệ
    std::string filename = directory;
    for (char c : stx_hash_hex) {
      filename += isalnum(c) ? c : '_';
    }

    // Giới hạn độ dài tối đa 250 ký tự (chừa chỗ cho ".log")
    filename = filename.substr(0, std::min<size_t>(250, filename.length()));
    filename += ".log"; // Thêm phần mở rộng

    // Mở file để ghi (chế độ append)
    std::ofstream outFile(filename, std::ios::app);
    if (!outFile) {
      std::cerr << "Lỗi: Không thể mở file " << filename << std::endl;
      return;
    }

    // Ghi dữ liệu vào file
    outFile << "Op code: 0x" << std::hex << static_cast<unsigned int>(op)
            << std::endl;
    outFile << "Op string: " << std::hex << mvm::opcodeToString(op)
            << std::endl;
    outFile << "Stack: 0x" << std::hex << ctxt->s << std::endl;
    outFile << "Mem: ";

    // Ghi từng byte của bộ nhớ theo hàng 32 byte
    for (size_t i = 0; i < ctxt->mem.size(); ++i) {
      outFile << std::hex << std::setw(2) << std::setfill('0')
              << static_cast<int>(ctxt->mem[i]);
      if ((i + 1) % 32 == 0) {
        outFile << std::endl;
      }
    }
    outFile << "\n---------------------\n" << std::dec;
  }
  // void printHex(const std::vector<uint8_t> &bytes)
  // {
  //   for (uint8_t byte : bytes)
  //   {
  //     std::cout << std::hex << std::setw(2) << std::setfill('0') <<
  //     static_cast<int>(byte);
  //   }
  //   std::cout << std::dec << std::endl;
  // }

  void dispatch(ExecResult &result) {
    const auto op = get_op();

    std::stringstream ss;
    ss << "[PC:" << std::setw(4) << std::setfill('0') << ctxt->get_pc() << "] "
       << "Op: 0x" << std::hex << static_cast<int>(op) << " ("
       << mvm::opcodeToString(op) << ")" << std::dec;
    opcode_trace_buffer.push_back(ss.str());

    gas_tracker.add_gas_used(getGasCost(op));
    if (tr)
      tr->add(ctxt->get_pc(), op, get_call_depth(), ctxt->s);

    if (tx.is_debug) {
      saveDebugInfo(tx, op, ctxt);
    }

    switch (op) {
    case Opcode::PUSH0:
    case Opcode::PUSH1:
    case Opcode::PUSH2:
    case Opcode::PUSH3:
    case Opcode::PUSH4:
    case Opcode::PUSH5:
    case Opcode::PUSH6:
    case Opcode::PUSH7:
    case Opcode::PUSH8:
    case Opcode::PUSH9:
    case Opcode::PUSH10:
    case Opcode::PUSH11:
    case Opcode::PUSH12:
    case Opcode::PUSH13:
    case Opcode::PUSH14:
    case Opcode::PUSH15:
    case Opcode::PUSH16:
    case Opcode::PUSH17:
    case Opcode::PUSH18:
    case Opcode::PUSH19:
    case Opcode::PUSH20:
    case Opcode::PUSH21:
    case Opcode::PUSH22:
    case Opcode::PUSH23:
    case Opcode::PUSH24:
    case Opcode::PUSH25:
    case Opcode::PUSH26:
    case Opcode::PUSH27:
    case Opcode::PUSH28:
    case Opcode::PUSH29:
    case Opcode::PUSH30:
    case Opcode::PUSH31:
    case Opcode::PUSH32:
      push();
      break;
    case Opcode::POP:
      pop();
      break;
    case Opcode::SWAP1:
    case Opcode::SWAP2:
    case Opcode::SWAP3:
    case Opcode::SWAP4:
    case Opcode::SWAP5:
    case Opcode::SWAP6:
    case Opcode::SWAP7:
    case Opcode::SWAP8:
    case Opcode::SWAP9:
    case Opcode::SWAP10:
    case Opcode::SWAP11:
    case Opcode::SWAP12:
    case Opcode::SWAP13:
    case Opcode::SWAP14:
    case Opcode::SWAP15:
    case Opcode::SWAP16:
      swap();
      break;
    case Opcode::DUP1:
    case Opcode::DUP2:
    case Opcode::DUP3:
    case Opcode::DUP4:
    case Opcode::DUP5:
    case Opcode::DUP6:
    case Opcode::DUP7:
    case Opcode::DUP8:
    case Opcode::DUP9:
    case Opcode::DUP10:
    case Opcode::DUP11:
    case Opcode::DUP12:
    case Opcode::DUP13:
    case Opcode::DUP14:
    case Opcode::DUP15:
    case Opcode::DUP16:
      dup();
      break;
    case Opcode::LOG0:
    case Opcode::LOG1:
    case Opcode::LOG2:
    case Opcode::LOG3:
    case Opcode::LOG4:
      log();
      break;
    case Opcode::ADD:
      add();
      break;
    case Opcode::MUL:
      mul();
      break;
    case Opcode::SUB:
      sub();
      break;
    case Opcode::DIV:
      div();
      break;
    case Opcode::SDIV:
      sdiv();
      break;
    case Opcode::MOD:
      mod();
      break;
    case Opcode::SMOD:
      smod();
      break;
    case Opcode::ADDMOD:
      addmod();
      break;
    case Opcode::MULMOD:
      mulmod();
      break;
    case Opcode::EXP:
      exp();
      break;
    case Opcode::SIGNEXTEND:
      signextend();
      break;
    case Opcode::LT:
      lt();
      break;
    case Opcode::GT:
      gt();
      break;
    case Opcode::SLT:
      slt();
      break;
    case Opcode::SGT:
      sgt();
      break;
    case Opcode::EQ:
      eq();
      break;
    case Opcode::ISZERO:
      isZero();
      break;
    case Opcode::AND:
      and_();
      break;
    case Opcode::OR:
      or_();
      break;
    case Opcode::XOR:
      xor_();
      break;
    case Opcode::NOT:
      not_();
      break;
    case Opcode::BYTE:
      byte();
      break;
    case Opcode::SHL:
      opSHL();
      break;
    case Opcode::SHR:
      opSHR();
      break;
    case Opcode::SAR:
      opSAR();
      break;
    case Opcode::JUMP:
      jump();
      break;
    case Opcode::JUMPI:
      jumpi();
      break;
    case Opcode::PC:
      pc();
      break;
    case Opcode::M_SIZE:
      msize();
      break;
    case Opcode::MCOPY:
      mcopy();
      break;
    case Opcode::MLOAD:
      mload();
      break;
    case Opcode::MSTORE:
      mstore();
      break;
    case Opcode::MSTORE8:
      mstore8();
      break;
    case Opcode::CODESIZE:
      codesize();
      break;
    case Opcode::CODECOPY:
      codecopy();
      break;
    case Opcode::EXTCODESIZE:
      extcodesize();
      break;
    case Opcode::EXTCODECOPY:
      extcodecopy();
      break;
    case Opcode::RETURNDATASIZE:
      opReturnDataSize();
      break;
    case Opcode::RETURNDATACOPY:
      opReturnDataCopy();
      break;
    case Opcode::EXTCODEHASH:
      opExtCodeHash();
      break;
    case Opcode::SLOAD:
      sload();
      break;
    case Opcode::SSTORE:
      sstore();
      break;
    case Opcode::ADDRESS:
      address();
      break;
    case Opcode::BALANCE:
      balance();
      break;
    case Opcode::ORIGIN:
      origin();
      break;
    case Opcode::CALLER:
      caller();
      break;
    case Opcode::CALLVALUE:
      callvalue();
      break;
    case Opcode::CALLDATALOAD:
      calldataload();
      break;
    case Opcode::CALLDATASIZE:
      calldatasize();
      break;
    case Opcode::CALLDATACOPY:
      calldatacopy();
      break;
    case Opcode::RETURN:
      return_();
      break;
    case Opcode::SELFDESTRUCT:
      selfdestruct(result);
      break;
    case Opcode::CREATE:
      create();
      break;
    case Opcode::CALL:
    case Opcode::CALLCODE:
    case Opcode::DELEGATECALL:
    case Opcode::STATICCALL:
      call();
      break;
    case Opcode::CREATE2:
      opCreate2();
      break;
    case Opcode::JUMPDEST:
      jumpdest();
      break;
    case Opcode::BLOCKHASH:
      blockhash();
      break;
    case Opcode::NUMBER:
      number();
      break;
    case Opcode::GASPRICE:
      gasprice();
      break;
    case Opcode::COINBASE:
      coinbase();
      break;
    case Opcode::TIMESTAMP:
      timestamp();
      break;
    case Opcode::PREVRANDAO:
      prevrandao();
      break;
    case Opcode::CHAINID:
      opChainId();
      break;
    case Opcode::SELFBALANCE:
      opSelfBalance();
      break;
    case Opcode::BASEFEE:
      opBaseFee();
      break;
    case Opcode::GASLIMIT:
      gaslimit();
      break;
    case Opcode::GAS:
      gas();
      break;
    case Opcode::SHA3:
      sha3();
      break;
    case Opcode::STOP:
      stop();
      break;
    case Opcode::REVERT:
      opRevert();
      break;
    case Opcode::TLOAD:
      tLoad();
      break;
    case Opcode::TSTORE:
      tStore();
      break;
    case Opcode::BLOBHASH:
      blobHash();
      break;
    case Opcode::BLOBBASEFEE:
      blobBashFee();
      break;
    default:
      stringstream err;
      err << fmt::format("Unknown/unsupported Opcode: 0x{:02x}", int{get_op()})
          << endl;
      err << fmt::format(" in contract {}",
                         to_checksum_address(ctxt->as.acc.get_address()))
          << endl;
      err << fmt::format(" called by {}", to_checksum_address(ctxt->caller))
          << endl;
      err << fmt::format(" at position {}, call-depth {}", ctxt->get_pc(),
                         get_call_depth())
          << endl;
      MVM_THROW(Exception(Exception::Type::ErrInvalidCode, err.str()));
    };
  }

  //
  // op codes
  //
  void swap() { ctxt->s.swap(get_op() - SWAP1 + 1); }

  void dup() { ctxt->s.dup(get_op() - DUP1); }

  void add() {
    const auto x = ctxt->s.pop();
    const auto y = ctxt->s.pop();
    ctxt->s.push(x + y);
  }

  void sub() {
    const auto x = ctxt->s.pop();
    const auto y = ctxt->s.pop();
    ctxt->s.push(x - y);
  }

  void mul() {
    const auto x = ctxt->s.pop();
    const auto y = ctxt->s.pop();
    ctxt->s.push(x * y);
  }

  void div() {
    const auto x = ctxt->s.pop();
    const auto y = ctxt->s.pop();
    if (!y) {
      ctxt->s.push(0);
    } else {
      ctxt->s.push(x / y);
    }
  }

  void sdiv() {
    auto x = ctxt->s.pop();
    auto y = ctxt->s.pop();
    const auto min = (numeric_limits<uint256_t>::max() / 2) + 1;

    if (y == 0)
      ctxt->s.push(0);
    // special "overflow case" from the yellow paper
    else if (x == min && y == -1)
      ctxt->s.push(x);
    else {
      const auto signX = get_sign(x);
      const auto signY = get_sign(y);
      if (signX == -1)
        x = 0 - x;
      if (signY == -1)
        y = 0 - y;

      auto z = (x / y);
      if (signX != signY)
        z = 0 - z;
      ctxt->s.push(z);
    }
  }

  void mod() {
    const auto x = ctxt->s.pop();
    const auto m = ctxt->s.pop();
    if (!m)
      ctxt->s.push(0);
    else
      ctxt->s.push(x % m);
  }

  void smod() {
    auto x = ctxt->s.pop();
    auto m = ctxt->s.pop();
    if (m == 0)
      ctxt->s.push(0);
    else {
      const auto signX = get_sign(x);
      const auto signM = get_sign(m);
      if (signX == -1)
        x = 0 - x;
      if (signM == -1)
        m = 0 - m;

      auto z = (x % m);
      if (signX == -1)
        z = 0 - z;
      ctxt->s.push(z);
    }
  }

  void addmod() {
    const uint512_t x = ctxt->s.pop();
    const uint512_t y = ctxt->s.pop();
    const auto m = ctxt->s.pop();
    if (!m) {
      ctxt->s.push(0);
    } else {
      const uint512_t n = (x + y) % m;
      ctxt->s.push(intx::uint<256>(n));
    }
  }

  void mulmod() {
    const uint512_t x = ctxt->s.pop();
    const uint512_t y = ctxt->s.pop();
    const auto m = ctxt->s.pop();
    if (!m) {
      ctxt->s.push(m);
    } else {
      const uint512_t n = (x * y) % m;
      ctxt->s.push(intx::uint<256>(n));
    }
  }

  void exp() {
    const auto b = ctxt->s.pop();
    const auto e = ctxt->s.pop(); // the full 256-bit exponent
    ctxt->s.push(intx::exp(b, e));

    // EIP-160: gas is charged per byte of the exponent's minimal big-endian
    // representation (not per non-zero byte).
    int e_byte_count = 0;
    for (int i = 31; i >= 0; i--) {
      if (static_cast<uint8_t>((e >> (8 * i)) & 0xFF) != 0) {
        e_byte_count = i + 1;
        break;
      }
    }
    gas_tracker.add_gas_used(getExpGasCost(e_byte_count));
  }

  void signextend() {
    const auto x = ctxt->s.pop();
    const auto y = ctxt->s.pop();
    if (x >= 32) {
      ctxt->s.push(y);
      return;
    }
    const auto idx = 8 * shrink<uint8_t>(x) + 7;
    const auto sign = static_cast<uint8_t>((y >> idx) & 1);
    constexpr auto zero = uint256_t(0);
    const auto mask = ~zero >> (256 - idx);
    const auto yex = ((sign ? ~zero : zero) << idx) | (y & mask);
    ctxt->s.push(yex);
  }

  void lt() {
    const auto x = ctxt->s.pop();
    const auto y = ctxt->s.pop();
    ctxt->s.push((x < y) ? 1 : 0);
  }

  void gt() {
    const auto x = ctxt->s.pop();
    const auto y = ctxt->s.pop();
    ctxt->s.push((x > y) ? 1 : 0);
  }

  void slt() {
    const auto x = ctxt->s.pop();
    const auto y = ctxt->s.pop();
    if (x == y) {
      ctxt->s.push(0);
      return;
    }

    const auto signX = get_sign(x);
    const auto signY = get_sign(y);
    if (signX != signY) {
      if (signX == -1)
        ctxt->s.push(1);
      else
        ctxt->s.push(0);
    } else {
      ctxt->s.push((x < y) ? 1 : 0);
    }
  }

  void sgt() {
    ctxt->s.swap(1);
    slt();
  }

  void eq() {
    const auto x = ctxt->s.pop();
    const auto y = ctxt->s.pop();
    if (x == y)
      ctxt->s.push(1);
    else
      ctxt->s.push(0);
  }

  void isZero() {
    const auto x = ctxt->s.pop();
    if (x == 0) {
      ctxt->s.push(1);
    } else {
      ctxt->s.push(0);
    }
  }

  void and_() {
    const auto x = ctxt->s.pop();
    const auto y = ctxt->s.pop();
    ctxt->s.push(x & y);
  }

  void or_() {
    const auto x = ctxt->s.pop();
    const auto y = ctxt->s.pop();
    ctxt->s.push(x | y);
  }

  void xor_() {
    const auto x = ctxt->s.pop();
    const auto y = ctxt->s.pop();
    ctxt->s.push(x ^ y);
  }

  void not_() {
    const auto x = ctxt->s.pop();
    ctxt->s.push(~x);
  }

  void byte() {
    const auto idx = ctxt->s.pop();
    const auto val = ctxt->s.pop(); // consumed even when idx is out of range
    if (idx >= 32) {
      ctxt->s.push(0);
      return;
    }
    const auto shift = 256 - 8 - 8 * shrink<uint8_t>(idx);
    const auto mask = uint256_t(255) << shift;
    ctxt->s.push((val & mask) >> shift);
  }

  void jump() {
    const auto newPc = ctxt->s.pop();
    jump_to(newPc);
  }

  void jumpi() {
    const auto newPc = ctxt->s.pop();
    const auto cond = ctxt->s.pop();
    if (cond)
      jump_to(newPc);
  }

  void jumpdest() {}

  void pc() { ctxt->s.push(ctxt->get_pc()); }

  void msize() { ctxt->s.push(ctxt->get_used_mem() * 32); }

  void mcopy() {
    // PHASE-5 FIX: this never charged gas at all — no static cost (fell into
    // getGasCost's `default: return 0`), no dynamic per-word cost, and no
    // memory-expansion cost despite resizing ctxt->mem. Dynamic part now
    // mirrors CODECOPY/CALLDATACOPY exactly (getCopyOperationGasCost), the
    // same formula EIP-5656 specifies for MCOPY.
    uint64_t old_mem_word_size = ctxt->get_used_mem();

    const auto dest_offset_word = ctxt->s.pop();
    const auto src_offset_word = ctxt->s.pop();
    const auto length = ctxt->s.pop64();
    const auto dest_offset = mem_offset(dest_offset_word, length);
    const auto src_offset = mem_offset(src_offset_word, length);

    if (length > 0) {
      // Tự động mở rộng bộ nhớ nếu cần
      uint64_t new_size = std::max(dest_offset + length, src_offset + length);
      if (new_size > ctxt->mem.size()) {
        ctxt->mem.resize(new_size, 0);
      }

      // Xử lý việc copy byte-by-byte như trong EVM
      // MCOPY trong Solidity xử lý đúng các trường hợp chồng chéo bộ nhớ
      if (dest_offset != src_offset) {
        if (dest_offset > src_offset) {
          // Copy từ cuối lên đầu để tránh ghi đè dữ liệu nguồn trong trường
          // hợp chồng chéo
          for (uint64_t i = length; i > 0; i--) {
            ctxt->mem[dest_offset + i - 1] = ctxt->mem[src_offset + i - 1];
          }
        } else {
          // Copy từ đầu xuống cuối (không có nguy cơ ghi đè dữ liệu nguồn)
          for (uint64_t i = 0; i < length; i++) {
            ctxt->mem[dest_offset + i] = ctxt->mem[src_offset + i];
          }
        }
      }
    }

    uint64_t new_mem_word_size = ctxt->get_used_mem();
    uint64_t word_count = (length + 31) / 32;
    gas_tracker.add_gas_used(getCopyOperationGasCost(
        word_count, ctxt->last_mem_gas_cost, old_mem_word_size, new_mem_word_size));
  }

  void tLoad() {
    const auto key = ctxt->s.pop();
    auto value = transient_storage[ctxt->acc.get_address()][key];
    ctxt->s.push(value);
  }
  void tStore() {
    if (ctxt->read_only) {
      MVM_THROW(Exception(ET::ErrWriteProtection,
                          "Cant transient store from read only call"));
    }
    const auto key = ctxt->s.pop();
    const auto value = ctxt->s.pop();
    const Address addr = ctxt->acc.get_address();
    auto &addrMap = transient_storage[addr];
    const bool hadEntry = addrMap.find(key) != addrMap.end();
    const uint256_t oldValue = hadEntry ? addrMap[key] : 0;
    addrMap[key] = value;
    journal.record([this, addr, key, hadEntry, oldValue]() {
      auto &m = transient_storage[addr];
      if (hadEntry)
        m[key] = oldValue;
      else
        m.erase(key);
    });
  }
  uint256_t blob_hash(uint64_t index) {
    // Sourced from Go's per-mvmId blob context (MVMApi.SetBlobContext), not a
    // BlockContext/Transaction ABI field — see the comment on
    // GlobalState::get_blob_hash. Out-of-range index correctly yields 0
    // rather than a fabricated value.
    return gs.get_blob_hash(index);
  }

  void blobHash() {
    const auto indexWord = ctxt->s.pop();
    if (indexWord > uint256_t(std::numeric_limits<uint64_t>::max())) {
      ctxt->s.push(0);
      return;
    }
    auto hash = blob_hash(static_cast<uint64_t>(indexWord));
    ctxt->s.push(hash);
  }
  void blobBashFee() {
    auto base_fee = gs.get_blob_base_fee();
    ctxt->s.push(base_fee);
  }
  void mload() {
    uint64_t old_mem_word_size = ctxt->get_used_mem();

    const auto offset = ctxt->s.pop64();
    prepare_mem_access(offset, Consts::WORD_SIZE);

    const auto start = ctxt->mem.data() + offset;
    ctxt->s.push(from_big_endian(start, Consts::WORD_SIZE));
    uint64_t new_mem_word_size = ctxt->get_used_mem();
    gas_tracker.add_gas_used(getMemExpansionGasCost(
        ctxt->last_mem_gas_cost, old_mem_word_size, new_mem_word_size));
  }

  void mstore() {
    uint64_t old_mem_word_size = ctxt->get_used_mem();
    const auto offset = ctxt->s.pop64();
    const auto word = ctxt->s.pop();
    prepare_mem_access(offset, Consts::WORD_SIZE);
    to_big_endian(word, ctxt->mem.data() + offset);
    uint64_t new_mem_word_size = ctxt->get_used_mem();
    gas_tracker.add_gas_used(getMemExpansionGasCost(
        ctxt->last_mem_gas_cost, old_mem_word_size, new_mem_word_size));
  }

  void mstore8() {
    uint64_t old_mem_word_size = ctxt->get_used_mem();
    const auto offset = ctxt->s.pop64();
    const auto b = shrink<uint8_t>(ctxt->s.pop());
    prepare_mem_access(offset, sizeof(b));
    ctxt->mem[offset] = b;
    uint64_t new_mem_word_size = ctxt->get_used_mem();
    gas_tracker.add_gas_used(getMemExpansionGasCost(
        ctxt->last_mem_gas_cost, old_mem_word_size, new_mem_word_size));
  }
  std::string addressToHex(const uint256_t &address) {
    std::stringstream ss;
    ss << "0x";

    const uint8_t *bytes = reinterpret_cast<const uint8_t *>(&address);

    for (int i = 0; i < 20; ++i) {
      ss << std::hex << std::setw(2) << std::setfill('0')
         << static_cast<int>(bytes[i]);
    }

    return ss.str();
  }

  void sload() {
    const auto k = ctxt->s.pop();
    // EIP-2929: 100 when the slot is warm, 2100 when cold.
    gas_tracker.add_gas_used(touch_slot(ctxt->acc.get_address(), k) ? 100 : 2100);
    ctxt->s.push(ctxt->st.load(k, nullptr));
  }

  // EIP-3529's reduced clear-refund constant (was 15000 pre-London).
  static constexpr int64_t SSTORE_CLEARS_SCHEDULE = 4800;

  // Returns the value `addr`'s `key` slot held at the START of this
  // transaction — captured once, on first touch, from `currentValue`
  // (whatever the cache/state already resolved it to); returns the
  // previously-captured value on every subsequent call for the same slot.
  uint256_t original_storage_value(const Address &addr, const uint256_t &key,
                                   const uint256_t &currentValue) {
    auto &addrMap = original_storage_values[addr];
    auto it = addrMap.find(key);
    if (it != addrMap.end())
      return it->second;
    addrMap[key] = currentValue;
    return currentValue;
  }

  // SSTORE gas and refund are computed in sstore() (EIP-2200/2929/3529).

  void sstore() {
    if (ctxt->read_only) {
      MVM_THROW(Exception(ET::ErrWriteProtection,
                          "Cant store stack from read only call"));
    }
    const auto k = ctxt->s.pop();
    const auto v = ctxt->s.pop();
    const Address addr = ctxt->acc.get_address();

    const bool hadDiffEntry = gs.has_storage_change(addr, k);
    const uint256_t oldDiffValue =
        hadDiffEntry ? gs.get_storage_change_value(addr, k) : 0;

    // Fetch-and-cache the pre-write value with no gas charge here (SLOAD's
    // own gas is billed separately when the program actually executes
    // SLOAD; MyStorage::store() below re-derives the same now-cached value
    // to compute SSTORE's own gas cost, so this doesn't trigger a second
    // FFI/state round-trip). Guaranteed to leave the slot cached, which
    // simplifies the journal restore below (always "had a cache entry").
    const uint256_t oldValue = ctxt->st.load(k, nullptr);
    const uint256_t originalValue = original_storage_value(addr, k, oldValue);

    // EIP-2200 + EIP-2929 + EIP-3529 pricing (go-ethereum's gasSStoreEIP3529): the call fails if 2300 gas or less is
    // left (reentrancy sentry); a cold slot costs 2100 extra; then the cost and refund depend on the current,
    // original and new values.
    if (gas_tracker.gas_left() <= 2300)
      MVM_THROW(Exception(ET::ErrOutOfGas, "Not enough gas for reentrancy sentry"));
    uint64_t sstoreCost = touch_slot(addr, k) ? 0 : 2100;
    int64_t refundDelta = 0;
    if (oldValue == v) {
      sstoreCost += 100;
    } else if (originalValue == oldValue) {
      if (originalValue == 0) {
        sstoreCost += 20000;
      } else {
        sstoreCost += 2900;
        if (v == 0)
          refundDelta += SSTORE_CLEARS_SCHEDULE;
      }
    } else {
      sstoreCost += 100;
      if (originalValue != 0) {
        if (oldValue == 0)
          refundDelta -= SSTORE_CLEARS_SCHEDULE; // the slot is re-created
        else if (v == 0)
          refundDelta += SSTORE_CLEARS_SCHEDULE; // the slot is cleared
      }
      if (originalValue == v) {
        // back to the original value: part of the cost is refunded
        refundDelta += originalValue == 0 ? 20000 - 100 : 5000 - 2100 - 100;
      }
    }
    gas_tracker.add_gas_used(sstoreCost);

    gs.add_addresses_storage_change(addr, k, v);
    if (!v)
      ctxt->st.remove(k);
    else
      ctxt->st.store(k, v, nullptr);

    if (refundDelta != 0)
      gas_tracker.add_refund(refundDelta);

    journal.record([this, addr, k, hadDiffEntry, oldDiffValue, oldValue,
                    refundDelta]() {
      if (hadDiffEntry)
        gs.add_addresses_storage_change(addr, k, oldDiffValue);
      else
        gs.erase_storage_change(addr, k);
      gs.get(addr).st.set_cached_raw(k, oldValue);
      if (refundDelta != 0)
        gas_tracker.add_refund(-refundDelta);
    });
  }

  void codecopy() { copy_mem(ctxt->mem, ctxt->prog.code, Opcode::STOP); }

  bool is_precompile(uint256_t addr) const {
    return (addr >= 1 && addr <= 409) ||
           (addr == mvm::getPaddedAddressSelector("wallet v1")) ||
           (addr == CALL_API_EXTENSION) ||
           (addr == EXTRACT_JSON_FIELD_EXTENSION) || (addr == BLST) ||
           (addr == MATH_EXTENSTON_ADDRESS) ||
           (addr == SIMPLE_DATABASE_ADDRESS) ||
           (addr == FULL_DATABASE_ADDRESS) || (addr == CROSS_CHAIN_ADDRESS);
  }

  void extcodesize() {
    auto addr = pop_addr(ctxt->s);
    gas_tracker.add_gas_used(account_access_cost(addr));
    if (is_precompile(addr)) {
      ctxt->s.push(1); // Fake size > 0 to bypass Solidity EXTCODESIZE check
    } else {
      ctxt->s.push(gs.get(addr).acc.get_code().size());
    }
  }

  void extcodecopy() {
    const auto target = pop_addr(ctxt->s);
    gas_tracker.add_gas_used(account_access_cost(target));
    copy_mem(ctxt->mem, gs.get(target).acc.get_code(), Opcode::STOP);
  }

  void codesize() { ctxt->s.push(ctxt->prog.code.size()); }

  void calldataload() {
    const auto offsetWord = ctxt->s.pop(); // full 256-bit offset
    const auto sizeInput = ctxt->input.size();
    if (offsetWord >= uint256_t(sizeInput)) {
      ctxt->s.push(0); // reading past the calldata yields zeros
      return;
    }
    const auto offset = static_cast<uint64_t>(offsetWord);
    safeAdd(offset, Consts::WORD_SIZE);

    uint256_t v = 0;
    for (uint8_t i = 0; i < Consts::WORD_SIZE; i++) {
      const auto j = offset + i;
      if (j < sizeInput) {
        v = (v << 8) + ctxt->input[j];
      } else {
        v <<= 8 * (Consts::WORD_SIZE - i);
        break;
      }
    }
    ctxt->s.push(v);
  }

  void calldatasize() { ctxt->s.push(ctxt->input.size()); }

  void calldatacopy() { copy_mem(ctxt->mem, ctxt->input, 0); }

  void address() { ctxt->s.push(ctxt->acc.get_address()); }

  void balance() {
    const auto target = pop_addr(ctxt->s);
    gas_tracker.add_gas_used(account_access_cost(target));
    decltype(auto) acc = gs.get(target).acc;
    std::string log_msg = "__Balance of " +
                          to_checksum_address(acc.get_address()) + " is " +
                          to_string(acc.get_balance());
    native_logger.LogString(0, const_cast<char *>(log_msg.c_str()));
    ctxt->s.push(acc.get_balance());
  }

  void origin() { ctxt->s.push(tx.origin); }

  void caller() { ctxt->s.push(ctxt->caller); }

  void callvalue() { ctxt->s.push(ctxt->call_value); }

  void push() {
    const auto op = get_op();
    const uint8_t bytes = (op == PUSH0) ? 0 : (op - PUSH1 + 1);
    const auto pc_base = ctxt->get_pc();
    const auto code_size = ctxt->prog.code.size();

    if (pc_base > std::numeric_limits<uint64_t>::max() - 1 - bytes)
      MVM_THROW(Exception(ET::outOfBounds, "Integer overflow in push"));

    uint64_t pc = pc_base + 1;
    uint256_t imm = 0;
    for (uint8_t i = 0; i < bytes; i++, pc++) {
      const uint8_t b = (pc < code_size) ? ctxt->prog.code[pc] : 0;
      imm = (imm << 8) | b;
    }

    ctxt->s.push(imm);
    ctxt->set_pc(pc);
  }

  void pop() { ctxt->s.pop(); }

  void log() {
    if (ctxt->read_only) {
      MVM_THROW(Exception(ET::ErrWriteProtection,
                          "Cant create log from read only call"));
    }
    const uint8_t n = get_op() - LOG0;
    const auto offsetWord = ctxt->s.pop();
    const auto size = ctxt->s.pop64();
    const auto offset = mem_offset(offsetWord, size);

    vector<uint256_t> topics(n);
    for (int i = 0; i < n; i++)
      topics[i] = ctxt->s.pop();

    auto log_data = copy_from_mem(offset, size);
    gas_tracker.add_gas_used(getLogGasCost(n, size));

    size_t logMark = log_handler.checkpoint();
    log_handler.handle(
        {ctxt->acc.get_address(), move(log_data), topics});
    journal.record([this, logMark]() { log_handler.rollback(logMark); });
  }

  void blockhash() {
    const auto iWord = ctxt->s.pop();
    // Anything that is not a plausible block number has no hash (0); never
    // truncate it into a small number.
    if (iWord > uint256_t(0x7fffffff)) {
      ctxt->s.push(0);
      return;
    }
    const auto dataValue = gs.get_block_hash(static_cast<int>(iWord));
    ctxt->s.push(dataValue);
  }

  void number() { ctxt->s.push(gs.get_block_context().number); }

  void gasprice() { ctxt->s.push(tx.gas_price); }

  void coinbase() { ctxt->s.push(gs.get_block_context().coinbase); }

  void timestamp() { ctxt->s.push(gs.get_block_context().time); }

  void prevrandao() { ctxt->s.push(gs.get_block_context().prevrandao); }

  // GAS pushes the gas left in the CURRENT call frame (after the GAS instruction itself was charged).
  void gas() { ctxt->s.push(gas_tracker.gas_left()); }

  void gaslimit() { ctxt->s.push(gs.get_block_context().gas_limit); }

  void sha3() {
    uint64_t old_mem_word_size = ctxt->get_used_mem();

    const auto offsetWord = ctxt->s.pop();
    const auto size = ctxt->s.pop64();
    const auto offset = mem_offset(offsetWord, size);
    prepare_mem_access(offset, size);

    uint8_t h[32];
    keccak_256(ctxt->mem.data() + offset, static_cast<unsigned int>(size), h);
    ctxt->s.push(from_big_endian(h, sizeof(h)));

    uint64_t new_mem_word_size = ctxt->get_used_mem();
    gas_tracker.add_gas_used(
        getSha3GasCost(get_word_size(size), ctxt->last_mem_gas_cost,
                       old_mem_word_size, new_mem_word_size));
  }

  void return_() {
    const auto offsetWord = ctxt->s.pop();
    const auto size = ctxt->s.pop64();
    const auto offset = mem_offset(offsetWord, size);

    // copy_from_mem expands memory (zero-filled) as the EVM does, so a RETURN
    // of a never-written region yields zeros, not an empty buffer.
    vector<uint8_t> output_data = copy_from_mem(offset, size);

    ctxt->rh(output_data);
    pop_context();
  }

  void stop() {
    auto rh = ctxt->rh;
    pop_context();
    rh({});
  }

  void selfdestruct(ExecResult &result) {
    if (ctxt->read_only) {
      MVM_THROW(Exception(ET::ErrWriteProtection,
                          "Cannot delete from read-only call"));
    }

    const Address addr = ctxt->acc.get_address();
    const auto beneficiary = pop_addr(ctxt->s);
    auto amount = ctxt->acc.get_balance();
    // 5000 base, +2600 for a cold beneficiary (EIP-2929), +25000 when value
    // is sent to an empty account (it gets created).
    uint64_t selfdestructCost = 5000;
    if (!touch_address(beneficiary))
      selfdestructCost += 2600;
    if (amount > 0 && account_is_empty(beneficiary))
      selfdestructCost += 25000;
    gas_tracker.add_gas_used(selfdestructCost);
    auto recipient = gs.get(beneficiary);
    journaled_pay_to(addr, recipient.acc.get_address(), ctxt->acc,
                     recipient.acc, amount);

    // EIP-6780: SELFDESTRUCT only fully clears a contract's code and
    // storage when it was created earlier in THIS SAME transaction —
    // otherwise (a contract that already existed before this transaction
    // began) it only ever moves the balance, per journaled_pay_to above.
    // This VM declares itself Cancun-targeted (see opcode.h), so this is
    // the correct, current-spec behavior rather than the older
    // "SELFDESTRUCT always deletes the account" semantics.
    if (created_this_tx.count(addr)) {
      bool newly_destructed = destructed_this_tx.insert(addr).second;
      if (newly_destructed) {
        journal.record([this, addr]() {
          destructed_this_tx.erase(addr);
        });
      }

      // EIP-6780: a contract created in this transaction that selfdestructs
      // with ITSELF as beneficiary burns its ether (nothing was paid above).
      if (recipient.acc.get_address() == addr) {
        const uint256_t burned = ctxt->acc.get_balance();
        if (burned > 0) {
          ctxt->acc.set_balance(0);
          gs.add_addresses_sub_balance_change(addr, burned);
          journal.record([this, addr, burned]() {
            gs.get(addr).acc.set_balance(burned);
            gs.undo_sub_balance_change(addr, burned);
          });
        }
      }
    }

    result.er = ExitReason::returned;

    stop();
  }

  void create() {
    if (ctxt->read_only) {
      MVM_THROW(Exception(ET::ErrWriteProtection,
                          "Cant create from read only call"));
    }
    const auto contractValue = ctxt->s.pop();
    const auto offsetWord = ctxt->s.pop();
    const auto size = ctxt->s.pop64();
    const auto offset = mem_offset(offsetWord, size);
    // EIP-3860: init code larger than 49152 bytes is an exceptional halt of
    // the creating frame, before any state change.
    if (size > Consts::MAX_INITCODE_SIZE) {
      MVM_THROW(Exception(ET::outOfBounds, "Max initcode size exceeded (limit: 1048576 bytes / 1 MB)"));
    }
    auto initCode = copy_from_mem(offset, size);
    gas_tracker.add_gas_used(2 * get_word_size(size)); // EIP-3860: 2 gas per init code word
    // CREATE overwrites the return data: empty unless the constructor REVERTs
    // (then it holds the revert data, set in eh below).
    ctxt->returnData.clear();
    // Insufficient balance for the endowment, or the call depth limit: CREATE
    // fails with 0 BEFORE the creator's nonce is bumped (go-ethereum checks
    // the depth and CanTransfer first) and nothing is charged for the init frame.
    if ((contractValue > 0 && ctxt->acc.get_balance() < contractValue) ||
        get_call_depth() > Consts::MAX_CALL_DEPTH) {
      ctxt->s.push(0);
      return;
    }
    // EIP-150: the init code may use 63/64 of the gas left.
    const uint64_t availableGas = gas_tracker.gas_left();
    const uint64_t allowance = availableGas - availableGas / 64;
    Address newAddress =
        generate_address(ctxt->acc.get_address(), ctxt->acc.get_nonce());
    // creator's nonce bump: journaled so that if THIS frame reverts, the nonce
    // bump is undone. But child's constructor rollback mark is placed AFTER this,
    // so a failed constructor does NOT undo this bump.
    Address creatorAddr = ctxt->acc.get_address();
    auto prevNonce = ctxt->acc.get_nonce();
    bool hadNonceChange = gs.has_nonce_change(creatorAddr);
    uint256_t oldNonceChange = hadNonceChange ? gs.get_nonce_change_value(creatorAddr) : 0;

    ctxt->acc.increment_nonce();
    gs.set_addresses_nonce_change(creatorAddr, prevNonce + 1);

    journal.record([this, creatorAddr, prevNonce, hadNonceChange, oldNonceChange]() {
      gs.get(creatorAddr).acc.set_nonce(prevNonce);
      if (hadNonceChange)
        gs.set_addresses_nonce_change(creatorAddr, oldNonceChange);
      else
        gs.erase_nonce_change(creatorAddr);
    });

    // EIP-684: destination address must not already have code, nonzero nonce,
    // or have been created in this execution.
    touch_address(newAddress); // the new address is warm even if the creation fails
    decltype(auto) existingAcc = gs.get(newAddress);
    if (existingAcc.acc.get_nonce() > 0 || !existingAcc.acc.get_code().empty() ||
        gs.has_newly_deploy(newAddress) || created_this_tx.count(newAddress)) {
      gas_tracker.add_gas_used(allowance); // a collision consumes the gas set aside for the init code
      ctxt->s.push(0);
      return;
    }

    auto prevChildNonce = existingAcc.acc.get_nonce();
    bool childHadNonceChange = gs.has_nonce_change(newAddress);
    uint256_t childOldNonceChange =
        childHadNonceChange ? gs.get_nonce_change_value(newAddress) : 0;

    size_t journalMark = journal.mark();
    created_this_tx.insert(newAddress);
    journal.record([this, newAddress, prevChildNonce, childHadNonceChange,
                    childOldNonceChange]() {
      created_this_tx.erase(newAddress);
      gs.get(newAddress).acc.set_code({});
      gs.get(newAddress).acc.set_nonce(prevChildNonce);
      if (childHadNonceChange)
        gs.set_addresses_nonce_change(newAddress, childOldNonceChange);
      else
        gs.erase_nonce_change(newAddress);
    });

    // EIP-161: a new contract starts with nonce 1 (it already counts for the CREATEs its init code runs).
    // The account's code remains empty during construction per EVM spec.
    decltype(auto) newAcc = gs.create(newAddress, 0, {}, 1);
    gs.set_addresses_nonce_change(newAddress, 1);

    // Mark AFTER the nonce increment (kept regardless) but BEFORE the
    // endowment transfer (undone if this CREATE's constructor reverts).
    journaled_pay_to(ctxt->acc.get_address(), newAddress, ctxt->acc,
                     newAcc.acc, contractValue);

    auto parentContext = ctxt;
    auto rh = [newAcc, parentContext, journalMark, this](vector<uint8_t> output) {
      // EIP-3541: deploying code starting with 0xEF is rejected — without
      // this, a plain CREATE could forge a 23-byte EIP-7702 delegation
      // designator (0xef0100 || address) at an address the deployer fully
      // controls, impersonating a real 7702 authorization the account owner
      // never signed. Treated like any other deploy failure: no code is
      // set, caller sees 0 (failure) instead of the new address, and any
      // endowment transferred is rolled back.
      // EIP-170: deployed code larger than 24576 bytes fails the creation.
      // The creation fails (undoing it and consuming the init frame's gas)
      // when the code is too large, starts with 0xEF, or the init frame cannot
      // pay the 200-gas-per-byte deposit.
      const uint64_t depositCost = getCodeDepositCost(output.size());
      if (output.size() > Consts::MAX_CODE_SIZE ||
          (!output.empty() && output[0] == 0xEF) ||
          gas_tracker.gas_left() < depositCost) {
        journal.revert_to(journalMark);
        gas_tracker.burn_frame();
        gas_tracker.pop_frame();
        parentContext->s.push(0);
        return;
      }
      gas_tracker.add_gas_used(depositCost);
      gas_tracker.pop_frame();
      Address newAccAddr = newAcc.acc.get_address();
      Code oldCode = newAcc.acc.get_code();
      bool hadDeployEntry = gs.has_newly_deploy(newAccAddr);
      Code oldDeployEntry =
          hadDeployEntry ? gs.get_newly_deploy_value(newAccAddr) : Code{};
      newAcc.acc.set_code(move(output));
      parentContext->s.push(newAccAddr);
      gs.add_addresses_newly_deploy(newAccAddr, output);
      // Recorded even on this success path: an ANCESTOR frame may still
      // revert later (e.g. this CREATE succeeded but the caller that
      // issued it hits a REVERT afterwards), which must undo this deploy
      // too — see StateJournal's doc comment on LIFO composition.
      journal.record([this, newAccAddr, oldCode, hadDeployEntry,
                      oldDeployEntry]() {
        gs.get(newAccAddr).acc.set_code(Code(oldCode));
        if (hadDeployEntry)
          gs.add_addresses_newly_deploy(newAccAddr, oldDeployEntry);
        else
          gs.erase_newly_deploy(newAccAddr);
      });
    };
    auto hh = [parentContext, journalMark, this]() {
      journal.revert_to(journalMark);
      gas_tracker.pop_frame();
      parentContext->s.push(0);
    };
    auto eh = [parentContext, journalMark, this](const Exception &e) {
      journal.revert_to(journalMark);
      // Only a REVERT gives the unused init-frame gas back; any other failure consumes it all.
      if (e.type != ET::ErrExecutionReverted)
        gas_tracker.burn_frame();
      gas_tracker.pop_frame();
      if (e.type == ET::ErrExecutionReverted) {
        // The constructor's REVERT data becomes the creator's return data.
        const auto revertOffsetWord = ctxt->s.pop();
        const auto revertSize = ctxt->s.pop64();
        const auto revertOffset = mem_offset(revertOffsetWord, revertSize);
        parentContext->returnData = copy_from_mem(revertOffset, revertSize);
      }
      parentContext->s.push(0);
    };

    gas_tracker.push_frame(allowance);
    push_context(ctxt->acc.get_address(), newAcc, {},
                 std::move(initCode), contractValue, rh, hh, eh,
                 ctxt->read_only ? true : false);
  }

  std::string address_to_hex_string(const Address address) {
    std::stringstream ss;
    ss << "0x" << std::hex << std::setw(40) << std::setfill('0') << address;
    return ss.str();
  }

  // EIP-7702 delegation designator: 0xef0100 || 20-byte address, exactly 23
  // bytes. Written to an authority's code by SetCodeAuthorization processing
  // (Go side) via the same SetCodeHash/code-store path used for real
  // contract deploys — see FromEthSetCodeTx.
  static bool is_delegation_designator(const Code &code) {
    return code.size() == 23 && code[0] == 0xEF && code[1] == 0x01 &&
           code[2] == 0x00;
  }

  static Address delegation_designator_address(const Code &code) {
    uint8_t buf[32] = {0};
    std::copy(code.begin() + 3, code.end(), buf + 12);
    return from_big_endian(buf, sizeof(buf));
  }

  // Resolves `code` for CALL-family dispatch: if it's a delegation
  // designator, returns the delegate's code (one hop only — if the delegate's
  // own code is ALSO a delegation designator, it is NOT resolved further;
  // instead its raw 23-byte designator starting with 0xEF is executed in EVM,
  // which throws ErrInvalidCode and halts exceptionally per EIP-7702). Otherwise returns `code` unchanged.
  Code resolve_delegated_code(const Code &code) {
    if (!is_delegation_designator(code)) {
      return code;
    }
    // EIP-7702: Charge account access gas for resolving delegation designator
    // (warm 100 gas if already warmed, cold 2600 gas if accessed for the first time).
    const Address delegate = delegation_designator_address(code);
    gas_tracker.add_gas_used(account_access_cost(delegate));
    return gs.get(delegate).acc.get_code();
  }

  void call() {
    const auto op = get_op();
    const auto requestedGasWord = ctxt->s.pop();
    const auto addr = pop_addr(ctxt->s);
    const auto value =
        (op == DELEGATECALL || op == STATICCALL) ? 0 : ctxt->s.pop();
    const auto offInWord = ctxt->s.pop();
    const auto sizeIn = ctxt->s.pop64();
    const auto offOutWord = ctxt->s.pop();
    auto sizeOut = ctxt->s.pop64();
    const auto offIn = mem_offset(offInWord, sizeIn);
    const auto offOut = mem_offset(offOutWord, sizeOut);

    // ---- gas charged before the callee runs ----
    // account access (EIP-2929), memory expansion of the input and output
    // ranges, and the value rules of CALL / CALLCODE: +9000 for a transfer,
    // +25000 more when CALL sends value to an empty account.
    gas_tracker.add_gas_used(account_access_cost(addr));
    expand_mem_charged(offIn, sizeIn);
    expand_mem_charged(offOut, sizeOut);
    const bool carriesValue = value > 0 && (op == CALL || op == CALLCODE);
    if (carriesValue) {
      gas_tracker.add_gas_used(getCallValueCost());
      if (op == CALL && account_is_empty(addr))
        gas_tracker.add_gas_used(25000);
    }
    auto input = copy_from_mem(offIn, sizeIn); // memory is already expanded: free

    // EIP-214: only CALL with a non-zero value is forbidden in a static
    // context. CALLCODE moves the value to the caller itself (no state
    // change), so it stays allowed.
    if (ctxt->read_only && op == CALL && value > 0) {
      MVM_THROW(Exception(ET::ErrWriteProtection,
                          "Cant transfer value from read only call"));
    }

    // EIP-150: the callee may use at most 63/64 of the gas left, and no more
    // than it asked for; a call that moves value also gets the 2300 stipend,
    // which is not charged to the caller and is handed back when unused.
    const uint64_t availableGas = gas_tracker.gas_left();
    const uint64_t maxAllowance = availableGas - availableGas / 64;
    const uint64_t allowance =
        requestedGasWord > uint256_t(maxAllowance)
            ? maxAllowance
            : static_cast<uint64_t>(requestedGasWord);
    const uint64_t stipend = carriesValue ? 2300 : 0;

    // A call that cannot be made (not enough balance for the value, or the call depth limit) fails only the
    // call: 0 is pushed, nothing moves, the return data is empty and all the gas it was given comes back
    // (a net credit of the stipend, as on Ethereum).
    if ((carriesValue && ctxt->acc.get_balance() < value) ||
        get_call_depth() > Consts::MAX_CALL_DEPTH) {
      gas_tracker.credit(stipend);
      ctxt->returnData.clear();
      ctxt->s.push(0);
      return;
    }

    bool is_precompile_call = is_precompile(addr);

    if (is_precompile_call) {
      // Standard precompiles (1 to 10) are priced like go-ethereum; any other
      // precompile of this chain keeps its flat price (0). Running out of the
      // gas given to the call fails it and consumes that gas, before any work.
      const uint64_t precompileGas =
          addr <= 10 ? getPrecompileGasCost(static_cast<uint64_t>(addr), input) : 0;
      if (precompileGas > allowance + stipend) {
        gas_tracker.add_gas_used(allowance);
        ctxt->returnData.clear();
        ctxt->s.push(0);
        return;
      }
      vector<uint8_t> precompile_output;
      bool success = true;

      // MVM_TRY/MVM_CATCH (mvm/safe_throw.h): was
      // try{...}catch(const std::exception&){...}catch(...){...}. Covers
      // this block's own 2 throw sites (ErrWriteProtection/ErrInvalidCode
      // below) plus my_storage.cpp/xapian_handlers.cpp's mvm::Exception
      // throws reached via extension.SimpleDatabase/FullDatabase* below --
      // see safe_throw.h's doc comment for the (smaller, separately
      // tracked) set of non-Exception throw sites elsewhere in extension.*/
      // crypto_handlers.cpp/xapian_manager|search|log.cpp NOT covered here.
      MVM_TRY
        if (addr == mvm::getPaddedAddressSelector("wallet v1")) {
          precompile_output = extension.PublicKeyFromPrivateKey(input);
        } else if (addr == 1) {
          precompile_output = extension.Ecrecover(input);
        } else if (addr == 2) {
          precompile_output = extension.Sha256(input);
        } else if (addr == 3) {
          precompile_output = extension.Ripemd160(input);
        } else if (addr == 4) {
          precompile_output = input; // Identity precompile
        } else if (addr == 5) {
          precompile_output = extension.Modexp(input);
        } else if (addr == 6) {
          precompile_output = extension.EcAdd(input);
        } else if (addr == 7) {
          precompile_output = extension.EcMul(input);
        } else if (addr == 8) {
          precompile_output = extension.EcPairing(input);
        } else if (addr == 9) {
          precompile_output = extension.Blake2f(input);
          if (precompile_output.empty()) {
            success = false;
          }
        } else if (addr == 10) {
          precompile_output = extension.PointEvaluationVerify(input);
        } else if (addr == 400) {
          precompile_output.resize(32);
          for (int i = 0; i < 32; i++) {
            precompile_output[i] =
                static_cast<uint8_t>((tx.tx_hash >> (8 * (31 - i))) & 0xFF);
          }
        } else if (addr == CALL_API_EXTENSION) {
          precompile_output = extension.CallGetApi(input);
        } else if (addr == EXTRACT_JSON_FIELD_EXTENSION) {
          precompile_output = extension.ExtractJsonField(input);
        } else if (addr == BLST) {
          precompile_output = extension.Blst(input);
        } else if (addr == MATH_EXTENSTON_ADDRESS) {
          precompile_output = extension.Math(input);
        } else if (addr == SIMPLE_DATABASE_ADDRESS) {
          if (ctxt->read_only || !gs.is_cache()) {
            MVM_THROW(Exception(ET::ErrWriteProtection,
                                "SimpleDatabase write protection"));
          }
          precompile_output =
              extension.SimpleDatabase(input, ctxt->as.acc.get_address());
        } else if (addr == FULL_DATABASE_ADDRESS) {
          precompile_output =
              extension.FullDatabase(input, ctxt->as.acc.get_address(), false,
                                     gs.get_block_context().number, &gs);
        } else if (addr == FULL_DATABASE_ADDRESS_V1) {
          precompile_output =
              extension.FullDatabaseV1(input, ctxt->as.acc.get_address(), false,
                                       gs.get_block_context().number, &gs);
        } else if (addr == CROSS_CHAIN_ADDRESS) {
          success = handle_cross_chain_precompile(
              gs, input, precompile_output, ctxt->as, value, log_handler,
              gs.get_block_context().time, addr);
        } else {
          MVM_THROW(Exception(ET::ErrInvalidCode,
                              "Precompiled contract not implemented."));
        }
      MVM_CATCH(e)
        success = false;
        precompile_output = mvm::encode_revert_string(e.what());
      MVM_END_TRY

      // The standard precompiles signal failure with an empty output (apart
      // from blake2f, handled above), so an empty output is a failure there.
      if (success && precompile_output.empty() &&
          (addr == 6 || addr == 7 || addr == 8 || addr == 10)) {
        success = false;
      }
      if (success) {
        if (precompileGas >= stipend)
          gas_tracker.add_gas_used(precompileGas - stipend);
        else
          gas_tracker.credit(stipend - precompileGas);
        ctxt->returnData = precompile_output;
        copy_mem_raw(offOut, 0, std::min<uint64_t>(sizeOut, ctxt->returnData.size()),
                     ctxt->mem, ctxt->returnData);
        ctxt->s.push(1);
      } else {
        // A failed precompile consumes everything it was given. The standard
        // ones leave no return data; this chain's own precompiles may
        // explain the failure in it.
        gas_tracker.add_gas_used(allowance);
        if (addr <= 10)
          ctxt->returnData.clear();
        else
          ctxt->returnData = precompile_output;
        ctxt->s.push(0);
      }
      return;
    }

    decltype(auto) callee = gs.get(addr);
    // Marked BEFORE the value transfer: unlike CREATE's creator-nonce bump,
    // a CALL's value transfer IS part of the message call being made and
    // must be undone if the callee reverts (see StateJournal's doc
    // comment) — this is the well-known EVM guarantee that a reverted
    // external call never actually moves value.
    size_t journalMark = journal.mark();
    // CALLCODE runs the callee's code in the caller's own context: the
    // "transfer" is from the caller to itself, i.e. no balance change.
    if (carriesValue && op != CALLCODE) {
      journaled_pay_to(ctxt->acc.get_address(), addr, ctxt->acc, callee.acc,
                       value);
    }

    // EIP-7702: if the callee's code is a delegation designator, CALL/
    // CALLCODE/DELEGATECALL/STATICCALL must actually execute the delegate's
    // code, not the 23-byte designator (which isn't valid bytecode — 0xEF
    // isn't a real opcode, dispatch() would throw ErrInvalidCode on it).
    // EXTCODESIZE/EXTCODEHASH/EXTCODECOPY are deliberately NOT routed through
    // this: they call gs.get(addr).acc.get_code() directly elsewhere and so
    // continue to see the raw designator unresolved, matching mainnet
    // Ethereum (those opcodes report the 23-byte designator, not the
    // delegate's code, for a 7702-delegated account).
    Code executableCode = resolve_delegated_code(callee.acc.get_code());

    if (executableCode.empty()) {
      // Nothing to run: the whole allowance and the stipend come back.
      gas_tracker.credit(stipend);
      ctxt->returnData.clear();
      ctxt->s.push(1);
      return;
    }

    // The callee runs in its own gas frame; when it ends the unused gas goes
    // back to the caller (and the stipend, which the caller never paid, is
    // taken off again). An exceptional halt (anything but REVERT) consumes the
    // whole frame.
    gas_tracker.push_frame(allowance + stipend);
    auto endFrame = [this, stipend](bool burn) {
      if (burn)
        gas_tracker.burn_frame();
      gas_tracker.pop_frame();
      gas_tracker.credit(stipend);
    };

    // Only min(retSize, len(returnData)) bytes are copied into the return
    // area; the rest of it keeps its previous content (it is NOT zero-filled).
    auto parentContext = ctxt;
    auto rh = [offOut, sizeOut, parentContext, endFrame,
               this](const vector<uint8_t> &output) {
      endFrame(false);
      parentContext->returnData = output;
      copy_mem_raw(offOut, 0,
                   std::min<uint64_t>(sizeOut, parentContext->returnData.size()),
                   parentContext->mem, parentContext->returnData);
      parentContext->s.push(1);
    };
    auto hh = [parentContext, endFrame]() {
      endFrame(false);
      parentContext->returnData.clear();
      parentContext->s.push(1);
    };
    auto he = [this, parentContext, offOut, sizeOut, journalMark,
               endFrame](const Exception &e) {
      journal.revert_to(journalMark);
      endFrame(e.type != ET::ErrExecutionReverted);
      if (e.type == ET::ErrExecutionReverted) {
        const auto offsetWord = ctxt->s.pop();
        const auto size = ctxt->s.pop64();
        const auto offset = mem_offset(offsetWord, size);
        auto revertData = copy_from_mem(offset, size);
        parentContext->returnData = revertData;
        // As on Ethereum, the revert data is also copied into the caller's
        // return area (min(retSize, len) bytes).
        copy_mem_raw(offOut, 0, std::min<uint64_t>(sizeOut, parentContext->returnData.size()),
                     parentContext->mem, parentContext->returnData);
        parentContext->s.push(0);
      } else {
        // Any other failure (INVALID, out of gas, stack error, write
        // protection...) leaves EMPTY return data on Ethereum: RETURNDATASIZE
        // is 0 and nothing is copied. The failure reason is still reported
        // for the top-level transaction by its own exception handler.
        parentContext->returnData.clear();
        parentContext->s.push(0);
      }
    };

    switch (op) {
    case Opcode::CALL:
      push_context(ctxt->acc.get_address(), callee, move(input),
                   move(executableCode), value, rh, hh, he,
                   ctxt->read_only ? true : false);
      break;
    case Opcode::CALLCODE:
      push_context(ctxt->acc.get_address(), ctxt->as, move(input),
                   move(executableCode), value, rh, hh, he,
                   ctxt->read_only ? true : false);
      break;
    case Opcode::DELEGATECALL:
      push_context(ctxt->caller, ctxt->as, move(input), move(executableCode),
                   ctxt->call_value, rh, hh, he,
                   ctxt->read_only ? true : false);
      break;
    case Opcode::STATICCALL:
      push_context(ctxt->acc.get_address(), callee, move(input),
                   move(executableCode), value, rh, hh, he, true);
      break;
    default:
      // 2026-08-21: real C++ throw, deliberately NOT converted -- it's
      // UnexpectedState, not mvm::Exception (mvm/safe_throw.h's MVM_THROW
      // only covers the latter). Per Exception.h's own doc comment,
      // UnexpectedState "should never be thrown under normal conditions
      // ... should be impossible to reach for smart contracts" -- `op`
      // here is one of CALL/CALLCODE/DELEGATECALL/STATICCALL by
      // construction (see the switch above), so this default case is
      // genuinely dead code, not a real residual risk.
      throw UnexpectedState("Unknown call opcode.");
    }
  }

  void opReturnDataSize() { ctxt->s.push(ctxt->returnData.size()); }

  void opReturnDataCopy() {
    const uint64_t old_mem_word_size = ctxt->get_used_mem();
    const auto memOffsetWord = ctxt->s.pop();
    const auto dataOffsetWord = ctxt->s.pop();
    const auto length = ctxt->s.pop64();
    const auto memOffset = mem_offset(memOffsetWord, length);
    // A source offset that does not fit in 64 bits is necessarily out of bounds.
    if (dataOffsetWord > uint256_t(std::numeric_limits<uint64_t>::max())) {
      MVM_THROW(Exception(ET::outOfBounds, "Return data out of bounds"));
    }
    const auto dataOffset = static_cast<uint64_t>(dataOffsetWord);
    // EIP-211: reading past the end of the return data is an exceptional
    // halt of the current frame (unlike CALLDATACOPY, which zero-pads).
    const uint64_t available = ctxt->returnData.size();
    if (dataOffset > available || length > available - dataOffset) {
      MVM_THROW(Exception(ET::outOfBounds,
                          "Return data out of bounds"));
    }
    copy_mem_raw(memOffset, dataOffset, length, ctxt->mem, ctxt->returnData);
    gas_tracker.add_gas_used(getCopyOperationGasCost(
        get_word_size(length), ctxt->last_mem_gas_cost, old_mem_word_size,
        ctxt->get_used_mem()));
  }

  // SHL / SHR / SAR take the shift amount as a full 256-bit word: a shift of
  // 256 or more is well defined (0, or the sign fill for SAR) and must still
  // push a result.
  void opSHL() {
    const auto shift = ctxt->s.pop();
    const auto value = ctxt->s.pop();
    ctxt->s.push(shift >= 256 ? uint256_t(0)
                              : value << static_cast<unsigned>(shift));
  }

  void opSHR() {
    const auto shift = ctxt->s.pop();
    const auto value = ctxt->s.pop();
    ctxt->s.push(shift >= 256 ? uint256_t(0)
                              : value >> static_cast<unsigned>(shift));
  }

  void opSAR() {
    const auto shift = ctxt->s.pop();
    const auto value = ctxt->s.pop();
    const bool negative = getSignUint256(value) < 0;
    if (shift >= 256) {
      ctxt->s.push(negative ? ~uint256_t(0) : uint256_t(0));
      return;
    }
    const auto sh = static_cast<unsigned>(shift);
    if (sh == 0) {
      ctxt->s.push(value);
      return;
    }
    uint256_t shr = value >> sh;
    if (negative) {
      shr |= (~uint256_t(0)) << (256 - sh); // fill the vacated top bits
    }
    ctxt->s.push(shr);
  }

  void opExtCodeHash() {
    const auto targetAddr = pop_addr(ctxt->s);
    gas_tracker.add_gas_used(account_access_cost(targetAddr));
    decltype(auto) target = gs.get(targetAddr);
    const auto code = target.acc.get_code();
    // EIP-1052: 0 for a non-existent or empty account (EIP-161: no code, zero
    // nonce, zero balance); keccak256(code) otherwise. An existing account
    // without code (e.g. a funded EOA) therefore hashes the empty string.
    if (code.empty() && target.acc.get_nonce() == 0 &&
        target.acc.get_balance() == 0) {
      ctxt->s.push(0);
      return;
    }
    uint8_t h[32];
    keccak_256(code.data(), code.size(), h);
    ctxt->s.push(from_big_endian(h, sizeof(h)));
  }

  void opChainId() { ctxt->s.push(gs.get_chain_id()); }

  void opSelfBalance() {
    std::string log_msg = "__SelfBalance of " +
                          to_checksum_address(ctxt->acc.get_address()) +
                          " is " + to_string(ctxt->acc.get_balance());
    native_logger.LogString(0, const_cast<char *>(log_msg.c_str()));
    ctxt->s.push(ctxt->acc.get_balance());
  }

  void opBaseFee() { ctxt->s.push(gs.get_block_context().base_fee); }

  void opCreate2() {
    if (ctxt->read_only) {
      MVM_THROW(Exception(ET::ErrWriteProtection,
                          "Cant create from read only call"));
    }
    const auto endowment = ctxt->s.pop();
    const auto offsetWord = ctxt->s.pop();
    const auto size = ctxt->s.pop64();
    const auto offset = mem_offset(offsetWord, size);
    const auto salt = ctxt->s.pop();

    if (size > Consts::MAX_INITCODE_SIZE) { // EIP-3860, see create()
      MVM_THROW(Exception(ET::outOfBounds, "Max initcode size exceeded (limit: 1048576 bytes / 1 MB)"));
    }
    auto input = copy_from_mem(offset, size);
    gas_tracker.add_gas_used(getCreate2DataSizeCost(get_word_size(size)));
    gas_tracker.add_gas_used(2 * get_word_size(size)); // EIP-3860: 2 gas per init code word
    ctxt->returnData.clear(); // see create()

    // See create(): insufficient endowment or the depth limit fails the CREATE2 before the nonce bump.
    if ((endowment > 0 && ctxt->acc.get_balance() < endowment) ||
        get_call_depth() > Consts::MAX_CALL_DEPTH) {
      ctxt->s.push(0);
      return;
    }
    const uint64_t availableGas = gas_tracker.gas_left();
    const uint64_t allowance = availableGas - availableGas / 64;

    Address newAddress =
        generate_contract_address_2(ctxt->acc.get_address(), salt, input);

    // The creator's nonce is bumped for CREATE2 exactly as for CREATE
    // (EIP-161); any contract chaining CREATE/CREATE2 calls off its own nonce
    // must see the new value. The bump is journaled BEFORE the child's
    // journalMark below: if the creator's own frame reverts, the bump is
    // undone with it; if only the child constructor fails, rollback stops at
    // journalMark and the bump survives (same as create()).
    Address creatorAddr = ctxt->acc.get_address();
    auto prevNonce = ctxt->acc.get_nonce();
    bool hadNonceChange = gs.has_nonce_change(creatorAddr);
    uint256_t oldNonceChange = hadNonceChange ? gs.get_nonce_change_value(creatorAddr) : 0;

    ctxt->acc.increment_nonce();
    gs.set_addresses_nonce_change(creatorAddr, prevNonce + 1);

    journal.record([this, creatorAddr, prevNonce, hadNonceChange, oldNonceChange]() {
      gs.get(creatorAddr).acc.set_nonce(prevNonce);
      if (hadNonceChange)
        gs.set_addresses_nonce_change(creatorAddr, oldNonceChange);
      else
        gs.erase_nonce_change(creatorAddr);
    });

    // EIP-684 / EIP-1014: destination address must not already have code,
    // nonzero nonce, or have been created in this execution.
    touch_address(newAddress); // the new address is warm even if the creation fails
    decltype(auto) existingAcc = gs.get(newAddress);
    if (existingAcc.acc.get_nonce() > 0 || !existingAcc.acc.get_code().empty() ||
        gs.has_newly_deploy(newAddress) || created_this_tx.count(newAddress)) {
      gas_tracker.add_gas_used(allowance); // a collision consumes the gas set aside for the init code
      ctxt->s.push(0);
      return;
    }

    auto prevChildNonce = existingAcc.acc.get_nonce();
    bool childHadNonceChange = gs.has_nonce_change(newAddress);
    uint256_t childOldNonceChange =
        childHadNonceChange ? gs.get_nonce_change_value(newAddress) : 0;

    size_t journalMark = journal.mark();
    created_this_tx.insert(newAddress);
    journal.record([this, newAddress, prevChildNonce, childHadNonceChange,
                    childOldNonceChange]() {
      created_this_tx.erase(newAddress);
      gs.get(newAddress).acc.set_code({});
      gs.get(newAddress).acc.set_nonce(prevChildNonce);
      if (childHadNonceChange)
        gs.set_addresses_nonce_change(newAddress, childOldNonceChange);
      else
        gs.erase_nonce_change(newAddress);
    });

    // EIP-161: a new contract starts with nonce 1 (see create()).
    // The account's code remains empty during construction per EVM spec.
    decltype(auto) newAcc = gs.create(newAddress, 0, {}, 1);
    gs.set_addresses_nonce_change(newAddress, 1);

    journaled_pay_to(ctxt->acc.get_address(), newAddress, ctxt->acc,
                     newAcc.acc, endowment);

    auto parentContext = ctxt;
    auto rh = [newAcc, parentContext, journalMark, this](vector<uint8_t> output) {
      // EIP-3541 — see the identical check in opCreate()'s rh for why.
      // EIP-170: deployed code larger than 24576 bytes fails the creation.
      // The creation fails (undoing it and consuming the init frame's gas)
      // when the code is too large, starts with 0xEF, or the init frame cannot
      // pay the 200-gas-per-byte deposit.
      const uint64_t depositCost = getCodeDepositCost(output.size());
      if (output.size() > Consts::MAX_CODE_SIZE ||
          (!output.empty() && output[0] == 0xEF) ||
          gas_tracker.gas_left() < depositCost) {
        journal.revert_to(journalMark);
        gas_tracker.burn_frame();
        gas_tracker.pop_frame();
        parentContext->s.push(0);
        return;
      }
      gas_tracker.add_gas_used(depositCost);
      gas_tracker.pop_frame();
      Address newAccAddr = newAcc.acc.get_address();
      Code oldCode = newAcc.acc.get_code();
      bool hadDeployEntry = gs.has_newly_deploy(newAccAddr);
      Code oldDeployEntry =
          hadDeployEntry ? gs.get_newly_deploy_value(newAccAddr) : Code{};
      newAcc.acc.set_code(move(output));
      parentContext->s.push(newAccAddr);
      gs.add_addresses_newly_deploy(newAccAddr, output);
      // See the matching comment in create()'s rh: recorded even on this
      // success path since an ancestor frame may still revert later.
      journal.record([this, newAccAddr, oldCode, hadDeployEntry,
                      oldDeployEntry]() {
        gs.get(newAccAddr).acc.set_code(Code(oldCode));
        if (hadDeployEntry)
          gs.add_addresses_newly_deploy(newAccAddr, oldDeployEntry);
        else
          gs.erase_newly_deploy(newAccAddr);
      });
    };

    auto hh = [parentContext, journalMark, this]() {
      journal.revert_to(journalMark);
      gas_tracker.pop_frame();
      parentContext->s.push(0);
    };
    auto eh = [parentContext, journalMark, this](const Exception &e) {
      journal.revert_to(journalMark);
      // Only a REVERT gives the unused init-frame gas back; any other failure consumes it all.
      if (e.type != ET::ErrExecutionReverted)
        gas_tracker.burn_frame();
      gas_tracker.pop_frame();
      if (e.type == ET::ErrExecutionReverted) {
        // The constructor's REVERT data becomes the creator's return data.
        const auto revertOffsetWord = ctxt->s.pop();
        const auto revertSize = ctxt->s.pop64();
        const auto revertOffset = mem_offset(revertOffsetWord, revertSize);
        parentContext->returnData = copy_from_mem(revertOffset, revertSize);
      }
      parentContext->s.push(0);
    };

    gas_tracker.push_frame(allowance);
    push_context(ctxt->acc.get_address(), newAcc, {}, move(input),
                 endowment, rh, hh, eh,
                 ctxt->read_only ? true : false);
  }

  void opRevert() {
    // REVERT pays for expanding the memory range of its data inside the reverting frame (the handlers that read the
    // data afterwards then find the memory already expanded).
    const auto revertOffsetWord = ctxt->s.pop();
    const auto revertSize = ctxt->s.pop64();
    copy_from_mem(mem_offset(revertOffsetWord, revertSize), revertSize);
    ctxt->s.push(revertSize);
    ctxt->s.push(revertOffsetWord);
    MVM_THROW(Exception(ET::ErrExecutionReverted, "Execution reverted"));
  }
};

Processor::Processor(GlobalState &gs, LogHandler &log_handler,
                     Extension &extension, NativeLogger &native_logger)
    : gs(gs), log_handler(log_handler), extension(extension),
      native_logger(native_logger) {}

ExecResult Processor::run(Transaction &tx, bool deploy, const Address &caller,
                          AccountState callee, const vector<uint8_t> &input,
                          const uint256_t &call_value, Trace *tr,
                          bool readOnly) {
  GasTracker gas_tracker(tx.gas_limit);
  _Processor p(gs, log_handler, extension, tx, tr, native_logger, gas_tracker);
  return p.run(caller, callee, deploy, input, call_value, readOnly);
}
} // namespace mvm
