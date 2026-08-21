#pragma once
// 2026-08-21: setjmp/longjmp-based replacement for C++ throw/catch around
// mvm::Exception, used ONLY when MVM_TA_BUILD is defined (set exclusively by
// tz-llm-trustzone/scripts/kick-the-tires/cpp13-metanode-deps/
// mvm_toolchain_chcore_real.cmake -- the musl/chcore TA toolchain, and no
// other toolchain file, so the real x86/cgo production build and the
// aarch64-linux-gnu glibc board cross-compile both keep real throw/catch,
// completely unchanged).
//
// WHY: confirmed via an isolated hardware self-test (see
// tz-llm-trustzone/DEPLOYED_STATE.md's "DEFINITIVE answer" entry, 2026-08-21)
// that C++ exception throw/catch is genuinely broken in the mvm_ta build --
// libstdc++'s std::terminate() fires instead of reaching the catch, even
// when throw and catch are in the SAME function/compilation unit. That first
// fix (mvm_linker.cpp's sendNative()/processNativeMintBurn()) avoided throw
// entirely for 2 specific call sites by building the error result directly.
// This header generalizes the same idea to the EVM interpreter core
// (c_mvm/src/processor.cpp, stack.cpp, gas.cpp), which has ~30 throw sites
// deeply nested inside opcode dispatch -- rewriting every call site to
// return an error code instead of throwing would mean changing ~30 function
// signatures and every caller in between. setjmp/longjmp avoids that: the
// `throw Exception(...)` call sites become `MVM_THROW(Exception(...))` (a
// 1:1 textual substitution, no signature changes), and the small number of
// enclosing try/catch blocks become MVM_TRY/MVM_CATCH/MVM_END_TRY.
//
// Scope: this covers exactly the `mvm::Exception`-typed throws reachable
// through processor.cpp's 3 try/catch choke points (the interpreter's main
// dispatch loop, its precompile-call block, and the top-level exception
// formatter) -- all of processor.cpp, stack.cpp, gas.cpp, plus 2 more
// (my_storage.cpp, my_extension/xapian_handlers.cpp) that also throw
// mvm::Exception and are reached via the same precompile-call choke point.
// NOT covered: ~60 other throw sites elsewhere in the codebase
// (my_extension.cpp's hex-parsing helpers, crypto_handlers.cpp,
// cross_chain_precompile.cpp, and the Xapian DB layer under linker/src/
// xapian/) that throw std::runtime_error/invalid_argument/out_of_range/
// overflow_error/Xapian::Error rather than mvm::Exception -- those remain
// real C++ throws and will still hang if triggered. Documented as a known,
// narrower follow-up in DEPLOYED_STATE.md, not silently dropped.
//
// Known limitation of setjmp/longjmp vs real exceptions: destructors of
// C++ objects with non-trivial cleanup that are alive between the
// corresponding MVM_TRY and the throw point do NOT run (longjmp does not
// unwind the C++ stack). For plain heap-owning locals (vector/string) this
// only leaks memory, not correctness, for the single request the TA is
// currently processing. Anything that needs cleanup on the error path in
// this codebase already does it explicitly in the eh()-style handlers
// (e.g. journal.revert_to(...)) rather than relying on destructors, which
// is why this is safe here -- see mvm_linker.cpp's existing "KHÔNG ĐƯỢC
// throw Exception" comments for the same design principle applied earlier.

#include "mvm/exception.h"

#ifdef MVM_TA_BUILD

#include <csetjmp>
#include <cstdlib>
#include <optional>
#include <string>
#include <vector>

namespace mvm {
namespace safe_throw_detail {

struct PendingFault {
  Exception::Type type;
  std::string msg;
};

// One jmp_buf per currently-active MVM_TRY scope, most-recently-entered
// last. Needed because processor.cpp's 3 choke points nest (the
// precompile-call try is inside the dispatch-loop try; the top-level
// exception formatter's own try can itself run while recovering from the
// dispatch-loop's catch) -- a throw must always resolve to the innermost
// enclosing MVM_TRY, exactly like real exception unwinding would.
inline std::vector<jmp_buf *> &jmp_stack() {
  static std::vector<jmp_buf *> stack;
  return stack;
}

inline std::optional<PendingFault> &pending() {
  static std::optional<PendingFault> p;
  return p;
}

struct ScopedJmp {
  jmp_buf buf;
  ScopedJmp() { jmp_stack().push_back(&buf); }
  ~ScopedJmp() { jmp_stack().pop_back(); }
};

[[noreturn]] inline void do_throw(const Exception &ex) {
  pending().emplace(PendingFault{ex.type, ex.what()});
  if (jmp_stack().empty()) {
    // No active MVM_TRY at all. Not expected in practice -- every throw
    // site this header is used for sits inside dispatch() or call(),
    // always invoked from inside run()'s MVM_TRY -- but abort() cleanly
    // rather than calling longjmp with no valid target.
    std::abort();
  }
  longjmp(*jmp_stack().back(), 1);
}

} // namespace safe_throw_detail
} // namespace mvm

#define MVM_THROW(ex_expr) ::mvm::safe_throw_detail::do_throw((ex_expr))

#define MVM_TRY                                                              \
  {                                                                          \
    ::mvm::safe_throw_detail::ScopedJmp mvm_scoped_jmp__;                    \
    if (setjmp(mvm_scoped_jmp__.buf) == 0) {

#define MVM_CATCH(ex_var)                                                    \
  }                                                                           \
  else {                                                                     \
    ::mvm::Exception ex_var(::mvm::safe_throw_detail::pending()->type,       \
                            ::mvm::safe_throw_detail::pending()->msg);       \
    ::mvm::safe_throw_detail::pending().reset();

#define MVM_END_TRY                                                          \
  }                                                                          \
  }

#else // !MVM_TA_BUILD -- x86/cgo production build and the arm64-glibc board
      // cross-compile: real throw/catch, byte-for-byte unchanged behavior.

#define MVM_THROW(ex_expr) throw(ex_expr)
#define MVM_TRY try {
#define MVM_CATCH(ex_var) } catch (::mvm::Exception & ex_var) {
#define MVM_END_TRY }

#endif // MVM_TA_BUILD
