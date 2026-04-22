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
  uint256_t blob_base_fee;

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
    if (ctxt) {
      delete ctxt;
    }
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
      result.er = ExitReason::threw;
      result.ex = ex_.type;
      if (result.ex == ET::ErrExecutionReverted) {
        const auto offset = ctxt->s.pop64();
        const auto size = ctxt->s.pop64();
        if (size != 0) {
          auto revert_data = copy_from_mem(offset, size);
          result.output = move(revert_data);
        }
        // ✅ Ưu tiên sử dụng exception message từ returnData (từ nested call)
        // Nếu returnData không rỗng, nó có thể chứa exception message gốc từ
        // nested call (được lưu bằng mvm::to_bytes(e.what()) trong exception
        // handler của nested call)
        if (!ctxt->returnData.empty()) {
          // Decode exception message từ returnData (raw bytes từ mvm::to_bytes)
          std::string original_msg(ctxt->returnData.begin(),
                                   ctxt->returnData.end());
          result.exmsg = original_msg;
        } else {
          // Nếu returnData rỗng, sử dụng exception message mặc định
          result.exmsg = ex_.what();
        }
      } else {
        result.output = mvm::encode_revert_string(ex_.what());
        result.exmsg = ex_.what();
      }
    };

    vector<uint8_t> exec_code;
    vector<uint8_t> calldata;
    if (deploy) {
      exec_code = move(input);
      calldata = vector<uint8_t>();
    } else {
      exec_code = callee.acc.get_code();
      calldata = move(input);
    }

    push_context(caller, callee, move(calldata), move(exec_code), call_value,
                 rh, hh, eh, readOnly);

    // add general gas use
    try {
      gas_tracker.add_gas_used(2100);
      if (deploy) {
        gas_tracker.add_gas_used(32000);
      }
    } catch (Exception &ex) {
      ctxt->eh(ex);
      pop_context();
      result.gas_used = gas_tracker.get_gas_used();
      return result;
    }
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

    result.gas_used = gas_tracker.get_gas_used();
    return result;
  }

private:
  std::unordered_map<uint256_t, uint256_t> transient_storage; // Bộ nhớ tạm

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
    // Lấy các tham số từ stack theo đúng thứ tự của EVM
    const auto dest_offset = ctxt->s.pop64();
    const auto src_offset = ctxt->s.pop64();
    const auto length = ctxt->s.pop64();

    // Không làm gì nếu length bằng 0
    if (length == 0) {
      return;
    }

    // Tự động mở rộng bộ nhớ nếu cần
    uint64_t new_size = std::max(dest_offset + length, src_offset + length);
    if (new_size > ctxt->mem.size()) {
      ctxt->mem.resize(new_size, 0);
    }

    // Xử lý việc copy byte-by-byte như trong EVM
    // MCOPY trong Solidity xử lý đúng các trường hợp chồng chéo bộ nhớ
    if (dest_offset == src_offset || length == 0) {
      // Không cần sao chép nếu vị trí giống nhau hoặc length = 0
      return;
    } else if (dest_offset > src_offset) {
      // Copy từ cuối lên đầu để tránh ghi đè dữ liệu nguồn trong trường hợp
      // chồng chéo
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

  void tLoad() {
    const auto key = ctxt->s.pop();
    auto value = transient_storage[key];
    ctxt->s.push(value);
  }
  void tStore() {
    const auto key = ctxt->s.pop();
    const auto value = ctxt->s.pop();
    transient_storage[key] = value;
  }
  uint256_t blob_hash(uint64_t index) {
    uint8_t input[8];
    std::memcpy(input, &index, sizeof(index));

    uint8_t hash[32];
    keccak_256(input, sizeof(input), hash);

    return from_big_endian(hash, sizeof(hash));
  }

  void blobHash() {
    const auto index = ctxt->s.pop64();
    auto hash = blob_hash(index);
    ctxt->s.push(hash);
  }
  void blobBashFee() {
    auto base_fee = ctxt->blob_base_fee;
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

  void sstore() {
    if (ctxt->read_only) {
      throw Exception(ET::ErrWriteProtection,
                      "Cant store stack from read only call");
    }
    const auto k = ctxt->s.pop();
    const auto v = ctxt->s.pop();
    gs.add_addresses_storage_change(ctxt->acc.get_address(), k, v);
    if (!v)
      ctxt->st.remove(k);
    else
      ctxt->st.store(k, v, &gas_tracker);
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

    log_handler.handle(
        {ctxt->acc.get_address(), copy_from_mem(offset, size), topics});
    gas_tracker.add_gas_used(getLogGasCost(n, size));
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

    auto recipient = gs.get(pop_addr(ctxt->s));
    auto amount = ctxt->acc.get_balance();
    ctxt->acc.pay_to(recipient.acc, amount);
    gs.add_addresses_sub_balance_change(ctxt->acc.get_address(), amount);
    gs.add_addresses_add_balance_change(recipient.acc.get_address(), amount);

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

    ctxt->acc.increment_nonce();
    gs.set_addresses_nonce_change(ctxt->acc.get_address(), nonce + 1);

    decltype(auto) newAcc = gs.create(newAddress, contractValue, initCode, 0);

    ctxt->acc.pay_to(newAcc.acc, contractValue);
    gs.add_addresses_sub_balance_change(ctxt->acc.get_address(), contractValue);
    gs.add_addresses_add_balance_change(newAddress, contractValue);

    auto parentContext = ctxt;
    auto rh = [newAcc, parentContext, this](vector<uint8_t> output) {
      Address newAccAddr = newAcc.acc.get_address();
      newAcc.acc.set_code(move(output));
      parentContext->s.push(newAccAddr);
      gs.add_addresses_newly_deploy(newAccAddr, output);
      gas_tracker.add_gas_used(getCodeDepositCost(output.size()));
    };
    auto hh = [parentContext]() { parentContext->s.push(0); };
    auto eh = [parentContext](const Exception &) { parentContext->s.push(0); };

    push_context(ctxt->acc.get_address(), newAcc, std::move(initCode),
                 newAcc.acc.get_code(), 0, rh, hh, eh,
                 ctxt->read_only ? true : false);
  }

  std::string address_to_hex_string(const Address address) {
    std::stringstream ss;
    ss << "0x" << std::hex << std::setw(40) << std::setfill('0') << address;
    return ss.str();
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
                                     gs.get_block_context().number);
        } else if (addr == FULL_DATABASE_ADDRESS_V1) {
          precompile_output =
              extension.FullDatabaseV1(input, ctxt->as.acc.get_address(), false,
                                       gs.get_block_context().number);
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
    if (value > 0) {
      gas_tracker.add_gas_used(getCallValueCost());
      ctxt->acc.pay_to(callee.acc, value);
      gs.add_addresses_sub_balance_change(ctxt->acc.get_address(), value);
      gs.add_addresses_add_balance_change(addr, value);
    }

    if (!callee.acc.has_code()) {
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
    auto he = [this, parentContext, offOut, sizeOut](const Exception &e) {
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
                   callee.acc.get_code(), value, rh, hh, he,
                   ctxt->read_only ? true : false);
      break;
    case Opcode::CALLCODE:
      push_context(ctxt->acc.get_address(), ctxt->as, move(input),
                   callee.acc.get_code(), value, rh, hh, he,
                   ctxt->read_only ? true : false);
      break;
    case Opcode::DELEGATECALL:
      push_context(ctxt->caller, ctxt->as, move(input), callee.acc.get_code(),
                   ctxt->call_value, rh, hh, he,
                   ctxt->read_only ? true : false);
      break;
    case Opcode::STATICCALL:
      push_context(ctxt->acc.get_address(), callee, move(input),
                   callee.acc.get_code(), value, rh, hh, he, true);
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

    decltype(auto) newAcc = gs.create(newAddress, endowment, input, 0);

    ctxt->acc.pay_to(newAcc.acc, endowment);
    gs.add_addresses_sub_balance_change(ctxt->acc.get_address(), endowment);
    gs.add_addresses_add_balance_change(newAddress, endowment);

    auto parentContext = ctxt;
    auto rh = [newAcc, parentContext, this](vector<uint8_t> output) {
      Address newAccAddr = newAcc.acc.get_address();
      newAcc.acc.set_code(move(output));
      parentContext->s.push(newAccAddr);
      gs.add_addresses_newly_deploy(newAccAddr, output);
      gas_tracker.add_gas_used(getCodeDepositCost(output.size()));
    };

    auto hh = [parentContext]() { parentContext->s.push(0); };
    auto eh = [parentContext, this](const Exception &e) {
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
