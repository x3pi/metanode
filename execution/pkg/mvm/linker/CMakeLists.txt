cmake_minimum_required(VERSION 3.13)

project(mvm_linker)

# Always generate compile_commands.json for clangd (Ctrl+Click / Go to Definition)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED True)
# Add an option to easily toggle debug mode
option(ENABLE_DEBUG "Enable debug compilation (O0, -g) instead of release (O3)" OFF)

if(ENABLE_DEBUG)
    # Debug mode flags
    set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -O0 -g -DDEBUG_MODE -DMVM_LINKER_BUILD")
    set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -O0 -g -DDEBUG_MODE -DMVM_LINKER_BUILD")
    message(STATUS "MVM_LINKER is configured in DEBUG mode (ENABLE_DEBUG=ON)")
else()
    # `-march=native`/`-mtune=native` only make sense when the compiler runs
    # on the same machine it targets. When cross-compiling (e.g. the
    # chcore/musl TA target — see
    # metanode/note/tee_dual_mode_execution_plan.md, GĐ3), fall back to the
    # ARMv8-A baseline instead; leave the existing native-tuned x86 build
    # path untouched otherwise.
    if(CMAKE_CROSSCOMPILING)
      set(MVM_LINKER_ARCH_FLAGS "-march=armv8-a -mtune=generic")
    else()
      set(MVM_LINKER_ARCH_FLAGS "-march=native -mtune=native")
    endif()
    # Release mode flags
    # NOTE: We keep `-g` combined with `-O3` here to preserve debug symbols.
    # This ensures that if a std::abort() or crash occurs on-chain, the panic stack trace
    # will show exactly which function and line number caused it, without slowing down execution.
    set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -O3 -g ${MVM_LINKER_ARCH_FLAGS} -ffunction-sections -fdata-sections -finline-functions -DMVM_LINKER_BUILD")
    set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -O3 -g ${MVM_LINKER_ARCH_FLAGS} -ffunction-sections -fdata-sections -finline-functions -DMVM_LINKER_BUILD")
    message(STATUS "MVM_LINKER is configured in RELEASE mode (ENABLE_DEBUG=OFF)")
endif()

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
# Common variables
# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
# Which c_mvm build output to link against. Defaults to the sibling
# in-tree ../c_mvm/build/ directory (unchanged behavior). Override with
# -DMVM_C_MVM_BUILD_DIR=... for an out-of-tree cross-build (e.g. chcore/musl
# TA target, GĐ3) so it never has to touch — and risk clobbering — the real
# x86 in-tree c_mvm/build/ that the production cgo build links against.
# (2026-08-16: this exact mistake happened once during verification —
# `make install` from an aarch64 cross-build dir overwrote the real repo's
# c_mvm/build/lib/static/libmvm.a with an AArch64 archive; fixed by
# reinstalling the x86 build, and by adding this override so it can't
# recur. See metanode/note/tee_dual_mode_execution_plan.md, 2026-08-16.)
if(NOT DEFINED MVM_C_MVM_BUILD_DIR)
  set(MVM_C_MVM_BUILD_DIR ${CMAKE_CURRENT_SOURCE_DIR}/../c_mvm/build)
endif()

# Include directories
set(MVM_INCLUDE_DIRS
    ${MVM_C_MVM_BUILD_DIR}/include
    ${MVM_C_MVM_BUILD_DIR}/include/3rdparty
)

set(INCLUDE_DIRS
  ${CMAKE_CURRENT_SOURCE_DIR}/include
  ${CMAKE_CURRENT_SOURCE_DIR}/include/my_extension
  ${CMAKE_CURRENT_SOURCE_DIR}/include/abi
  ${CMAKE_CURRENT_SOURCE_DIR}/include/xapian
  ${MVM_INCLUDE_DIRS}
)

set(LINKER_SRCS
  src/mvm_linker.cpp
  src/my_account.cpp
  src/my_global_state.cpp
  src/my_logger.cpp
  src/my_storage.cpp
  src/state.cpp
  src/my_extension/my_extension.cpp
  src/my_extension/xapian_handlers.cpp
  src/my_extension/crypto_handlers.cpp
  src/my_extension/constants.cpp
  src/my_extension/utils.cpp
  src/xapian/xapian_manager.cpp
  src/xapian/xapian_manager_resolve.cpp
  src/xapian/xapian_search.cpp
  src/xapian/xapian_registry.cpp
  src/xapian/xapian_log.cpp

  src/abi/encoding_utils.cpp
  src/cross_chain_precompile.cpp
)

set(MVM_SRCS
  ${MVM_C_MVM_BUILD_DIR}/libmvm.a
)



# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
# Libraries
# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
add_library(${PROJECT_NAME} STATIC
  ${LINKER_SRCS}
)

target_link_libraries(
  ${PROJECT_NAME}
  PUBLIC
  ${MVM_SRCS}
)

target_include_directories(${PROJECT_NAME} PUBLIC
  ${INCLUDE_DIRS}
)

# 3rd-party libs needed by src/my_extension/{my_extension,utils}.cpp (GMP/
# MPFR high-precision math precompile, secp256k1 ecrecover) and
# src/xapian/xapian_manager.cpp (libuuid instance IDs). On the existing x86
# build these resolve via system packages, pulled in only at the final `go
# build`'s cgo LDFLAGS (see pkg/mvm/mvm_api.go's `#cgo LDFLAGS`) -- this
# CMake target itself has never needed to know about them, since it only
# ever produces a static .a consumed by that later Go link step.
#
# A standalone TA executable (GĐ3 -- no Go/cgo runtime to fall back on, and
# notably: on x86 secp256k1_context_create et al. actually resolve against
# go-ethereum's own vendored/cgo-compiled libsecp256k1 copy, not any system
# -lsecp256k1 -- confirmed via `nm` on a real linked test binary, 2026-08-16
# -- a trick that only works because Go merges all cgo objects into one
# link unit; it doesn't exist here) will need these linked explicitly.
# Record them as PUBLIC usage requirements, guarded so nothing changes for
# the existing x86 build unless MVM_3RDPARTY_ROOT is explicitly set (e.g.
# pointed at tz-llm-trustzone's cpp13-metanode-deps' extracted 3rdparty/
# dir -- see metanode/note/tee_dual_mode_execution_plan.md, GĐ3,
# 2026-08-16, for exact provenance/versions of each lib).
if(MVM_3RDPARTY_ROOT)
  target_include_directories(${PROJECT_NAME} PUBLIC ${MVM_3RDPARTY_ROOT}/include)
  target_link_directories(${PROJECT_NAME} PUBLIC ${MVM_3RDPARTY_ROOT}/lib)
  target_link_libraries(${PROJECT_NAME} PUBLIC mpfr gmp secp256k1 uuid)
endif()

set_target_properties(${PROJECT_NAME}
    PROPERTIES
    PUBLIC_HEADER 
      ${CMAKE_CURRENT_SOURCE_DIR}/include/mvm_linker.hpp
)

install(TARGETS ${PROJECT_NAME} 
        RUNTIME DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/bin
        LIBRARY DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/lib
        PUBLIC_HEADER DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/include
        ARCHIVE DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/lib/static)

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
# Tests (optional, enable with -DBUILD_TESTS=ON)
# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
option(BUILD_TESTS "Build unit tests" OFF)

if(BUILD_TESTS)
  enable_testing()

  set(DOCTEST_INCLUDE_DIR ${CMAKE_CURRENT_SOURCE_DIR}/../c_mvm/3rdparty)

  add_executable(mvm_linker_tests
    tests/test_encoding_utils.cpp
    src/abi/encoding_utils.cpp
  )

  target_include_directories(mvm_linker_tests PRIVATE
    ${INCLUDE_DIRS}
    ${DOCTEST_INCLUDE_DIR}
  )

  add_test(NAME encoding_utils_tests COMMAND mvm_linker_tests)

  # Crypto helpers tests (standalone — no MVM runtime dependency)
  add_executable(mvm_crypto_helpers_tests
    tests/test_crypto_helpers.cpp
  )

  target_include_directories(mvm_crypto_helpers_tests PRIVATE
    ${DOCTEST_INCLUDE_DIR}
  )

  add_test(NAME crypto_helpers_tests COMMAND mvm_crypto_helpers_tests)

  # Xapian Manager tests
  add_executable(mvm_xapian_manager_tests
    tests/test_xapian_manager.cpp
  )

  target_link_libraries(mvm_xapian_manager_tests
    PRIVATE
    ${PROJECT_NAME}
    xapian
    tbb
    crypto
    ssl
    uuid
    mpfr
    gmp
  )

  target_include_directories(mvm_xapian_manager_tests PRIVATE
    ${INCLUDE_DIRS}
    ${DOCTEST_INCLUDE_DIR}
  )

  add_test(NAME xapian_manager_tests COMMAND mvm_xapian_manager_tests)
endif()