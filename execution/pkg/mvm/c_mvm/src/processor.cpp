// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#include "mvm/processor.h"
#include "mvm/cross_chain_precompile.h"

#include "mvm/bigint.h"
#include "mvm/exception.h"
#include "mvm/gas.h"
#include "mvm/opcode.h"
#include "mvm/stack.h"
#include "mvm/util.h"

#include <algorithm>
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

namespace mvm {

struct Consts {
  static constexpr auto MAX_CALL_DEPTH = 1024u;
  static constexpr auto WORD_SIZE = 32u;
  static constexpr auto MAX_MEM_SIZE = 1ull << 25; // 32 MB
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
 * Deliberately NOT journaled: nonce increments (CREATE's creator-nonce bump
 * survives a failed constructor on real Ethereum — it happens once, as an
 * effect of the CALLER's own frame, not as part of the reverted sub-call —
 * so leaving it unjournaled is the CORRECT behavior, not an oversight), gas
 * consumption (never refunded on revert by design — gas_tracker is a single
 * counter for the whole transaction and already behaves correctly without
 * any change here), and the read-only address/storage-read "warm" tracking
 * (EIP-2929 warmth persists for the rest of the transaction even if the
 * call that first touched an address/slot reverts — matches real Ethereum).
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

    // create the first context
    ExecResult result;
    auto rh = [&result](vector<uint8_t> output_) {
      result.er = ExitReason::returned;
      result.output = move(output_);
    };
    auto hh = [&result]() { result.er = ExitReason::halted; };
    auto eh = [&](const Exception &ex_) {
      journal.revert_to(0);
      result.er = ExitReason::threw;
      result.ex = ex_.type;
      try {
        if (result.ex == ET::ErrExecutionReverted) {
          if (ctxt && ctxt->s.size() >= 2) {
            const auto offset = ctxt->s.pop64();
            const auto size = ctxt->s.pop64();
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
      } catch (...) {
        result.exmsg = ex_.what();
      }
    };

    vector<uint8_t> exec_code;
    vector<uint8_t> calldata;
    if (deploy) {
      exec_code = move(input);
      calldata = vector<uint8_t>();
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

    push_context(caller, callee, move(calldata), move(exec_code), call_value,
                 rh, hh, eh, readOnly);

    // Intrinsic gas (base 21000/53000, calldata, access-list, EIP-7702
    // auth-tuple cost) is computed and charged on the Go side (see
    // vm_processor.computeIntrinsicGas / applyIntrinsicGas) and already
    // subtracted from gas_tracker's budget (tx.gas_limit, set below) before
    // this function runs — charging a flat 2100/32000 here on top of that
    // would double-charge every transaction.
    auto sm_size = ctxt->prog.code.size();
    // run
    while (ctxt && ctxt->get_pc() < ctxt->prog.code.size()) {
      try {
        dispatch(result);
      } catch (Exception &ex) {
        ctxt->eh(ex);
        pop_context();
      }

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
  // sstore()/compute_sstore_refund_delta): the value a slot held at the
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

  void push_context(const Address &caller, AccountState as,
                    vector<uint8_t> &&input, Program &&prog,
                    const uint256_t &call_value, Context::ReturnHandler &&rh,
                    Context::HaltHandler &&hh, Context::ExceptionHandler &&eh,
                    bool read_only) {
    if (get_call_depth() >= Consts::MAX_CALL_DEPTH)
      throw Exception(ET::ErrDepth, "Reached max call depth (" +
                                        to_string(Consts::MAX_CALL_DEPTH) +
                                        ")");

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
      throw Exception(ET::outOfBounds, "Integer overflow in copy_mem (" +
                                           to_string(lastDst) + " < " +
                                           to_string(offDst) + ")");

    if (lastDst > Consts::MAX_MEM_SIZE)
      throw Exception(ET::outOfBounds,
                      "Memory limit exceeded (" + to_string(lastDst) + " > " +
                          to_string(Consts::MAX_MEM_SIZE) + ")");

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
    const auto offDst = ctxt->s.pop64();
    const auto offSrc = ctxt->s.pop64();
    const auto size = ctxt->s.pop64();
    copy_mem_raw(offDst, offSrc, size, dst, src, pad);
    //
    uint64_t new_mem_word_size = ctxt->get_used_mem();
    gas_tracker.add_gas_used(getCopyOperationGasCost(
        size, ctxt->last_mem_gas_cost, old_mem_word_size, new_mem_word_size));
  }

  void prepare_mem_access(const uint64_t offset, const uint64_t size) {
    const auto end = offset + size;
    if (end < offset)
      throw Exception(ET::outOfBounds, "Integer overflow in memory access (" +
                                           to_string(end) + " < " +
                                           to_string(offset) + ")");

    if (end > Consts::MAX_MEM_SIZE)
      throw Exception(ET::outOfBounds,
                      "Memory limit exceeded (" + to_string(end) + " > " +
                          to_string(Consts::MAX_MEM_SIZE) + ")");

    if (end > ctxt->mem.size())
      ctxt->mem.resize(end);
  }

  vector<uint8_t> copy_from_mem(const uint64_t offset, const uint64_t size) {
    if (offset + size > ctxt->mem.size()) {
      return {};
    }
    uint64_t old_mem_word_size = ctxt->get_used_mem();
    //
    prepare_mem_access(offset, size);
    //
    uint64_t new_mem_word_size = ctxt->get_used_mem();
    gas_tracker.add_gas_used(
        getCopyOperationGasCost(get_word_size(size), ctxt->last_mem_gas_cost,
                                old_mem_word_size, new_mem_word_size));

    return {ctxt->mem.begin() + offset, ctxt->mem.begin() + offset + size};
  }

  void jump_to(const uint64_t newPc) {
    if (ctxt->prog.jump_dests.find(newPc) == ctxt->prog.jump_dests.end())
      throw Exception(ET::ErrInvalidCode,
                      to_string(newPc) + " is not a jump destination");
    ctxt->set_pc(newPc);
  }

  template <
      typename X, typename Y,
      typename = enable_if_t<is_unsigned<X>::value && is_unsigned<Y>::value>>
  static auto safeAdd(const X x, const Y y) {
    const auto r = x + y;
    if (r < x)
      throw overflow_error("integer overflow");
    return r;
  }

  template <typename T> static T shrink(uint256_t i) {
    return static_cast<T>(i & numeric_limits<T>::max());
  }

  void saveDebugInfo(const Transaction &tx, uint8_t op, const Context *ctxt) {

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
      throw Exception(Exception::Type::ErrInvalidCode, err.str());
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
    const auto e = ctxt->s.pop64();
    ctxt->s.push(intx::exp(b, uint256_t(e)));

    int e_byte_count = 0;
    for (int i = 0; i < 8; i++) {
      uint8_t v = uint8_t((e >> 8 * (7 - i)) & 0xFF);
      if (v > 0) {
        e_byte_count++;
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
    if (idx >= 32) {
      ctxt->s.push(0);
      return;
    }
    const auto shift = 256 - 8 - 8 * shrink<uint8_t>(idx);
    const auto mask = uint256_t(255) << shift;
    const auto val = ctxt->s.pop();
    ctxt->s.push((val & mask) >> shift);
  }

  void jump() {
    const auto newPc = ctxt->s.pop64();
    jump_to(newPc);
  }

  void jumpi() {
    const auto newPc = ctxt->s.pop64();
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

    const auto dest_offset = ctxt->s.pop64();
    const auto src_offset = ctxt->s.pop64();
    const auto length = ctxt->s.pop64();

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
    const auto index = ctxt->s.pop64();
    auto hash = blob_hash(index);
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
    ctxt->s.push(ctxt->st.load(k, &gas_tracker));
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

  // EIP-2200's refund bookkeeping, restricted to the "clear schedule" case
  // (SSTORE-ing a slot to zero) — the dominant, EIP-3529-motivating case.
  // Deliberately omits the "restore to original value" bonus refund
  // (returning a slot to exactly its transaction-start value after one or
  // more intermediate writes): that's real, spec-defined gas the user is
  // technically owed back, but it's a rarer case and skipping it can only
  // ever under-refund, never over-refund — safe to leave on the table
  // rather than risk getting the full 3-way comparison subtly wrong in
  // consensus-critical code.
  static int64_t compute_sstore_refund_delta(const uint256_t &original,
                                             const uint256_t &current,
                                             const uint256_t &newVal) {
    if (current == newVal)
      return 0; // no-op write, no refund change
    if (original == current) {
      // first change to this slot this transaction
      if (original != 0 && newVal == 0)
        return SSTORE_CLEARS_SCHEDULE;
      return 0;
    }
    // slot already changed at least once this transaction
    int64_t delta = 0;
    if (original != 0) {
      if (current == 0)
        delta -= SSTORE_CLEARS_SCHEDULE; // un-clearing: revoke the earlier refund
      if (newVal == 0)
        delta += SSTORE_CLEARS_SCHEDULE; // re-clearing: grant it again
    }
    return delta;
  }

  void sstore() {
    if (ctxt->read_only) {
      throw Exception(ET::ErrWriteProtection,
                      "Cant store stack from read only call");
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

    gs.add_addresses_storage_change(addr, k, v);
    if (!v)
      ctxt->st.remove(k);
    else
      ctxt->st.store(k, v, &gas_tracker);

    int64_t refundDelta = compute_sstore_refund_delta(originalValue, oldValue, v);
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
    if (is_precompile(addr)) {
      ctxt->s.push(1); // Fake size > 0 to bypass Solidity EXTCODESIZE check
    } else {
      ctxt->s.push(gs.get(addr).acc.get_code().size());
    }
  }

  void extcodecopy() {
    copy_mem(ctxt->mem, gs.get(pop_addr(ctxt->s)).acc.get_code(), Opcode::STOP);
  }

  void codesize() { ctxt->s.push(ctxt->prog.code.size()); }

  void calldataload() {
    const auto offset = ctxt->s.pop64();
    safeAdd(offset, Consts::WORD_SIZE);
    const auto sizeInput = ctxt->input.size();

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
    decltype(auto) acc = gs.get(pop_addr(ctxt->s)).acc;
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
    const uint8_t bytes = get_op() - PUSH1 + 1;
    const auto end = ctxt->get_pc() + bytes;
    if (end < ctxt->get_pc())
      throw Exception(ET::outOfBounds, "Integer overflow in push (" +
                                           to_string(end) + " < " +
                                           to_string(ctxt->get_pc()) + ")");

    if (end >= ctxt->prog.code.size())
      throw Exception(ET::outOfBounds,
                      "Push immediate exceeds size of program (" +
                          to_string(end) +
                          " >= " + to_string(ctxt->prog.code.size()) + ")");

    auto pc = ctxt->get_pc() + 1;
    uint256_t imm = 0;
    for (int i = 0; i < bytes; i++)
      imm = (imm << 8) | ctxt->prog.code[pc++];

    ctxt->s.push(imm);
    ctxt->set_pc(pc);
  }

  void pop() { ctxt->s.pop(); }

  void log() {
    if (ctxt->read_only) {
      throw Exception(ET::ErrWriteProtection,
                      "Cant create log from read only call");
    }
    const uint8_t n = get_op() - LOG0;
    const auto offset = ctxt->s.pop64();
    const auto size = ctxt->s.pop64();

    vector<uint256_t> topics(n);
    for (int i = 0; i < n; i++)
      topics[i] = ctxt->s.pop();

    size_t logMark = log_handler.checkpoint();
    log_handler.handle(
        {ctxt->acc.get_address(), copy_from_mem(offset, size), topics});
    gas_tracker.add_gas_used(getLogGasCost(n, size));
    journal.record([this, logMark]() { log_handler.rollback(logMark); });
  }

  void blockhash() {
    const auto i = ctxt->s.pop64();
    const auto dataValue = gs.get_block_hash(i);
    ctxt->s.push(dataValue);
  }

  void number() { ctxt->s.push(gs.get_block_context().number); }

  void gasprice() { ctxt->s.push(tx.gas_price); }

  void coinbase() { ctxt->s.push(gs.get_block_context().coinbase); }

  void timestamp() { ctxt->s.push(gs.get_block_context().time); }

  void prevrandao() { ctxt->s.push(gs.get_block_context().prevrandao); }

  void gas() { ctxt->s.push(tx.gas_limit); }

  void gaslimit() { ctxt->s.push(gs.get_block_context().gas_limit); }

  void sha3() {
    uint64_t old_mem_word_size = ctxt->get_used_mem();

    const auto offset = ctxt->s.pop64();
    const auto size = ctxt->s.pop64();
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
    const auto offset = ctxt->s.pop64();
    const auto size = ctxt->s.pop64();

    vector<uint8_t> output_data;
    if (size > 0) {
      if (offset + size > ctxt->mem.size()) {
      } else {
        output_data = copy_from_mem(offset, size);
      }
    }

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
      throw Exception(ET::ErrWriteProtection,
                      "Cannot delete from read-only call");
    }

    const Address addr = ctxt->acc.get_address();
    auto recipient = gs.get(pop_addr(ctxt->s));
    auto amount = ctxt->acc.get_balance();
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
      Code oldCode = ctxt->acc.get_code();
      bool hadDeployEntry = gs.has_newly_deploy(addr);
      Code oldDeployEntry =
          hadDeployEntry ? gs.get_newly_deploy_value(addr) : Code{};
      auto oldStorageDiff = gs.snapshot_storage_change(addr);
      auto oldCache = ctxt->st.snapshot_cached();

      ctxt->acc.set_code(Code{});
      gs.erase_newly_deploy(addr);
      gs.clear_storage_change(addr);
      ctxt->st.clear_all_cached();

      journal.record([this, addr, oldCode, hadDeployEntry, oldDeployEntry,
                      oldStorageDiff, oldCache]() {
        gs.get(addr).acc.set_code(Code(oldCode));
        if (hadDeployEntry)
          gs.add_addresses_newly_deploy(addr, oldDeployEntry);
        for (const auto &kv : oldStorageDiff)
          gs.add_addresses_storage_change(addr, kv.first, kv.second);
        Storage &st = gs.get(addr).st;
        for (const auto &kv : oldCache)
          st.set_cached_raw(kv.first, kv.second);
      });
    }

    result.er = ExitReason::returned;

    stop();
  }

  void create() {
    if (ctxt->read_only) {
      throw Exception(ET::ErrWriteProtection,
                      "Cant create from read only call");
    }
    const auto contractValue = ctxt->s.pop();
    const auto offset = ctxt->s.pop64();
    const auto size = ctxt->s.pop64();
    auto initCode = copy_from_mem(offset, size);
    auto nonce = ctxt->acc.get_nonce();
    Address newAddress =
        generate_address(ctxt->acc.get_address(), ctxt->acc.get_nonce());
    created_this_tx.insert(newAddress);

    // Nonce increment deliberately NOT journaled: on real Ethereum the
    // creator's nonce bump survives even if the constructor below reverts —
    // it's an effect of the CALLER's own frame, not part of the reverted
    // sub-call. See StateJournal's doc comment.
    ctxt->acc.increment_nonce();
    gs.set_addresses_nonce_change(ctxt->acc.get_address(), nonce + 1);

    decltype(auto) newAcc = gs.create(newAddress, contractValue, initCode, 0);

    // Mark AFTER the nonce increment (kept regardless) but BEFORE the
    // endowment transfer (undone if this CREATE's constructor reverts).
    size_t journalMark = journal.mark();
    journaled_pay_to(ctxt->acc.get_address(), newAddress, ctxt->acc,
                     newAcc.acc, contractValue);

    auto parentContext = ctxt;
    auto rh = [newAcc, parentContext, this](vector<uint8_t> output) {
      // EIP-3541: deploying code starting with 0xEF is rejected — without
      // this, a plain CREATE could forge a 23-byte EIP-7702 delegation
      // designator (0xef0100 || address) at an address the deployer fully
      // controls, impersonating a real 7702 authorization the account owner
      // never signed. Treated like any other deploy failure: no code is
      // set, caller sees 0 (failure) instead of the new address.
      if (!output.empty() && output[0] == 0xEF) {
        parentContext->s.push(0);
        return;
      }
      Address newAccAddr = newAcc.acc.get_address();
      Code oldCode = newAcc.acc.get_code();
      bool hadDeployEntry = gs.has_newly_deploy(newAccAddr);
      Code oldDeployEntry =
          hadDeployEntry ? gs.get_newly_deploy_value(newAccAddr) : Code{};
      newAcc.acc.set_code(move(output));
      parentContext->s.push(newAccAddr);
      gs.add_addresses_newly_deploy(newAccAddr, output);
      gas_tracker.add_gas_used(getCodeDepositCost(output.size()));
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
    auto hh = [parentContext]() { parentContext->s.push(0); };
    auto eh = [parentContext, journalMark, this](const Exception &) {
      journal.revert_to(journalMark);
      parentContext->s.push(0);
    };

    push_context(ctxt->acc.get_address(), newAcc, std::move(initCode),
                 newAcc.acc.get_code(), 0, rh, hh, eh,
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
  // designator, returns the delegate's code (one hop only — a delegate whose
  // own code is ALSO a designator resolves to empty, matching EIP-7702's "no
  // chained delegation" rule). Otherwise returns `code` unchanged.
  Code resolve_delegated_code(const Code &code) {
    if (!is_delegation_designator(code)) {
      return code;
    }
    const Address delegate = delegation_designator_address(code);
    Code delegateCode = gs.get(delegate).acc.get_code();
    if (is_delegation_designator(delegateCode)) {
      return {};
    }
    return delegateCode;
  }

  void call() {
    const auto op = get_op();
    ctxt->s.pop(); // gas limit not used
    const auto addr = pop_addr(ctxt->s);
    const auto value =
        (op == DELEGATECALL || op == STATICCALL) ? 0 : ctxt->s.pop();
    const auto offIn = ctxt->s.pop64();
    const auto sizeIn = ctxt->s.pop64();
    const auto offOut = ctxt->s.pop64();
    auto sizeOut = ctxt->s.pop64();
    prepare_mem_access(offOut, sizeOut);
    auto input = copy_from_mem(offIn, sizeIn);

    bool is_precompile_call = is_precompile(addr);

    if (is_precompile_call) {
      vector<uint8_t> precompile_output;
      bool success = true;

      try {
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
            throw Exception(ET::ErrWriteProtection,
                            "SimpleDatabase write protection");
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
          throw Exception(ET::ErrInvalidCode,
                          "Precompiled contract not implemented.");
        }
      } catch (const std::exception &e) {
        success = false;
        precompile_output = mvm::encode_revert_string(e.what());
      } catch (...) {
        success = false;
      }

      if (success) {
        ctxt->returnData = precompile_output;
        copy_mem_raw(offOut, 0, sizeOut, ctxt->mem, ctxt->returnData);
        ctxt->s.push(1);
      } else {
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
    if (value > 0) {
      gas_tracker.add_gas_used(getCallValueCost());
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
      ctxt->returnData.clear();
      ctxt->s.push(1);
      return;
    }

    auto parentContext = ctxt;
    auto rh = [offOut, sizeOut, parentContext,
               this](const vector<uint8_t> &output) {
      parentContext->returnData = output;
      copy_mem_raw(offOut, 0, sizeOut, parentContext->mem,
                   parentContext->returnData);
      parentContext->s.push(1);
    };
    auto hh = [parentContext]() {
      parentContext->returnData.clear();
      parentContext->s.push(1);
    };
    auto he = [this, parentContext, offOut, sizeOut,
               journalMark](const Exception &e) {
      journal.revert_to(journalMark);
      if (e.type == ET::ErrExecutionReverted) {
        const auto offset = ctxt->s.pop64();
        const auto size = ctxt->s.pop64();
        auto revertData = copy_from_mem(offset, size);
        parentContext->returnData = revertData;
        parentContext->s.push(0);
      } else {
        // ✅ Lưu exception message vào returnData để parent context có thể biết
        // lỗi ban đầu Lưu message trước để tránh vấn đề khi exception object bị
        // destruct
        std::string ex_msg = e.what();
        parentContext->returnData = mvm::encode_revert_string(ex_msg);
        parentContext->s.push(0);
        // ⚠️ KHÔNG throw exception trong exception handler để tránh double free
        // Exception message đã được lưu vào returnData, sẽ được xử lý ở
        // top-level context
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
      throw UnexpectedState("Unknown call opcode.");
    }
  }

  void opReturnDataSize() { ctxt->s.push(ctxt->returnData.size()); }

  void opReturnDataCopy() {
    const auto memOffset = ctxt->s.pop64();
    const auto dataOffset = ctxt->s.pop64();
    const auto length = ctxt->s.pop64();
    copy_mem_raw(memOffset, dataOffset, length, ctxt->mem, ctxt->returnData);
  }

  void opSHL() {
    const auto shift = ctxt->s.pop64();
    const auto value = ctxt->s.pop();
    if (shift < 256) {
      ctxt->s.push(value << shift);
    }
  }

  void opSHR() {
    const auto shift = ctxt->s.pop64();
    const auto value = ctxt->s.pop();
    if (shift < 256) {
      ctxt->s.push(value >> shift);
    }
  }

  void opSAR() {
    const auto shift = static_cast<uint64_t>(ctxt->s.pop());
    const auto value = ctxt->s.pop();

    int sign = getSignUint256(value);
    if (shift > 256) {
      ctxt->s.push(
          sign < 0 ? to_uint256("0xffffffffffffffffffffffffffffffffffffffff")
                   : uint256_t(0));
      return;
    }

    uint256_t shr = value >> shift;

    if (sign < 0) {
      uint256_t mask = (uint256_t(1) << shift) - 1;
      mask <<= (256 - shift);
      shr |= mask;
    }

    ctxt->s.push(shr);
  }

  void opExtCodeHash() {
    const auto code = gs.get(pop_addr(ctxt->s)).acc.get_code();
    if (code.size() > 0) {
      uint8_t h[32];
      keccak_256(code.data(), code.size(), h);
      ctxt->s.push(from_big_endian(h, sizeof(h)));
    } else {
      ctxt->s.push(0);
    }
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
      throw Exception(ET::ErrWriteProtection,
                      "Cant create from read only call");
    }
    const auto endowment = ctxt->s.pop();
    const auto offset = ctxt->s.pop64();
    const auto size = ctxt->s.pop64();
    const auto salt = ctxt->s.pop();

    auto input = copy_from_mem(offset, size);
    gas_tracker.add_gas_used(getCreate2DataSizeCost(get_word_size(size)));

    Address newAddress =
        generate_contract_address_2(ctxt->acc.get_address(), salt, input);
    created_this_tx.insert(newAddress);

    // BUG FIX: unlike create() (CREATE), this never incremented the
    // creator's own nonce — real Ethereum bumps it for CREATE2 exactly the
    // same way (EIP-161), and any contract chaining multiple CREATE/CREATE2
    // calls off its own nonce (or computing its own next CREATE address)
    // would otherwise see a stale value. Deliberately NOT journaled, same
    // reasoning as create()'s nonce bump: it's an effect of the CALLER's
    // own frame that survives even if the spawned contract's constructor
    // reverts, not part of the reverted sub-call itself.
    auto callerNonce = ctxt->acc.get_nonce();
    ctxt->acc.increment_nonce();
    gs.set_addresses_nonce_change(ctxt->acc.get_address(), callerNonce + 1);

    decltype(auto) newAcc = gs.create(newAddress, endowment, input, 0);

    size_t journalMark = journal.mark();
    journaled_pay_to(ctxt->acc.get_address(), newAddress, ctxt->acc,
                     newAcc.acc, endowment);

    auto parentContext = ctxt;
    auto rh = [newAcc, parentContext, this](vector<uint8_t> output) {
      // EIP-3541 — see the identical check in opCreate()'s rh for why.
      if (!output.empty() && output[0] == 0xEF) {
        parentContext->s.push(0);
        return;
      }
      Address newAccAddr = newAcc.acc.get_address();
      Code oldCode = newAcc.acc.get_code();
      bool hadDeployEntry = gs.has_newly_deploy(newAccAddr);
      Code oldDeployEntry =
          hadDeployEntry ? gs.get_newly_deploy_value(newAccAddr) : Code{};
      newAcc.acc.set_code(move(output));
      parentContext->s.push(newAccAddr);
      gs.add_addresses_newly_deploy(newAccAddr, output);
      gas_tracker.add_gas_used(getCodeDepositCost(output.size()));
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

    auto hh = [parentContext]() { parentContext->s.push(0); };
    auto eh = [parentContext, journalMark, this](const Exception &e) {
      journal.revert_to(journalMark);
      parentContext->eh(e);
    };

    push_context(ctxt->acc.get_address(), newAcc, move(input),
                 newAcc.acc.get_code(), 0, rh, hh, eh,
                 ctxt->read_only ? true : false);
  }

  void opRevert() {
    throw Exception(ET::ErrExecutionReverted, "Execution reverted");
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
