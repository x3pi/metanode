cmake_minimum_required(VERSION 3.10)

project(mvm)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED True)
# Set C compiler flags
# NOTE: We keep `-g` combined with `-O3` in Release mode. 
# This preserves debug symbols (function names & line numbers) for meaningful stack traces 
# when the node crashes (e.g. std::abort fail-stop), without sacrificing execution speed.
set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -O3 -g")

# `-march=native`/`-mtune=native` only make sense when the compiler runs on
# the same machine it targets. When cross-compiling (e.g. the chcore/musl TA
# target — see metanode/note/tee_dual_mode_execution_plan.md, GĐ3), "native"
# is meaningless (there is no local CPU to query) and GCC/Clang reject it
# outright. Fall back to the ARMv8-A baseline in that case; leave the
# existing native-tuned x86 build path untouched otherwise.
if(CMAKE_CROSSCOMPILING)
  set(MVM_ARCH_FLAGS "-march=armv8-a -mtune=generic")
else()
  set(MVM_ARCH_FLAGS "-march=native -mtune=native")
endif()
set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} ${MVM_ARCH_FLAGS}")
set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -ffunction-sections -fdata-sections")
set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -finline-functions")

# Set C++ compiler flags
# NOTE: Same as above, `-O3 -g` ensures "shallow debugging" (stack traces) is available in production.
set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -O3 -g")
set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} ${MVM_ARCH_FLAGS}")
set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -ffunction-sections -fdata-sections")
set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -finline-functions")

if(MSVC)
  # Set Windows compiler options.
  add_compile_options(/W3 /std:c++latest)
else()
  # Set Linux compiler options
   # add_compile_options(-Wall -Werror)

  if("${CMAKE_CXX_COMPILER_ID}" STREQUAL "GNU")
    separate_arguments(COVERAGE_FLAGS UNIX_COMMAND "--coverage -fprofile-arcs -ftest-coverage")
    set(PLATFORM_SPECIFIC_TEST_LIBS "gcov")
  else()
    separate_arguments(COVERAGE_FLAGS UNIX_COMMAND "-fprofile-instr-generate -fcoverage-mapping")
    set(PLATFORM_SPECIFIC_TEST_LIBS "-fprofile-instr-generate")
  endif()
endif()

file(GLOB KECCAK_SOURCES
  3rdparty/keccak/*.c
)

add_subdirectory(3rdparty)

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
# Options
# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
option(RECORD_TRACE "Record a detailed trace of EVM execution during test runs" OFF)
if(RECORD_TRACE)
  add_definitions(-DRECORD_TRACE)
endif(RECORD_TRACE)

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
# Common variables
# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
set(MVM_INCLUDE_DIRS
  ${CMAKE_CURRENT_SOURCE_DIR}/3rdparty
  ${CMAKE_CURRENT_SOURCE_DIR}/include
)

set(MVM_CORE_SRCS
  src/disassembler.cpp
  src/processor.cpp
  src/stack.cpp
  src/transaction.cpp
  src/log.cpp
  src/util.cpp
  src/gas.cpp
  src/crypto/sha256.cpp
  src/crypto/ripemd160.cpp
  src/crypto/bn254.cpp
  src/crypto/pairing/bn254/pairing.cpp
  src/crypto/blake2b.cpp
  src/crypto/bls.cpp
  src/crypto/kzg.cpp
  src/crypto/secp256k1.cpp
)

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
# Libraries
# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
add_library(${PROJECT_NAME} STATIC
  ${MVM_CORE_SRCS}
  ${KECCAK_SOURCES}
  
)
target_include_directories(mvm SYSTEM PRIVATE
    ${CMAKE_CURRENT_LIST_DIR}/../../bls/blst/bindings
)

target_include_directories(${PROJECT_NAME}  PUBLIC
  ${MVM_INCLUDE_DIRS}
)
set(SOLIDITY_DIR "${CMAKE_CURRENT_SOURCE_DIR}/3rdparty/solidity/build/libsolidity")

# Thêm thư mục include (giả sử nó ở build/include)
include_directories("${CMAKE_CURRENT_SOURCE_DIR}/3rdparty/solidity/build/include")


target_link_libraries(${PROJECT_NAME}
  ${SOLIDITY_DIR}/libsolidity.a
  intx::intx
)

set_target_properties(
  ${PROJECT_NAME}
  PROPERTIES
  PUBLIC_HEADER
    "${PUBLIC_HEADER}"
)

# Where `make install` writes to. Hardcoded to
# ${CMAKE_CURRENT_SOURCE_DIR}/build (i.e. always the real in-tree c_mvm/build/,
# regardless of which directory was actually configured/built in) until
# 2026-08-16 — that meant *any* build, including an out-of-tree cross-compile
# one, would silently overwrite the real x86 production install output on
# `make install`. Confirmed this actually happened once during GĐ3
# cross-build verification (aarch64 libmvm.a clobbered the real
# c_mvm/build/lib/static/libmvm.a that pkg/mvm's cgo LDFLAGS links against;
# fixed by reinstalling the x86 build). Default preserved exactly; override
# with -DMVM_INSTALL_PREFIX=... for a cross build so it never touches the
# real path. See metanode/note/tee_dual_mode_execution_plan.md, 2026-08-16.
if(NOT DEFINED MVM_INSTALL_PREFIX)
  set(MVM_INSTALL_PREFIX ${CMAKE_CURRENT_SOURCE_DIR}/build)
endif()

install(TARGETS ${PROJECT_NAME}
        RUNTIME DESTINATION ${MVM_INSTALL_PREFIX}/bin
        LIBRARY DESTINATION ${MVM_INSTALL_PREFIX}/lib
        PUBLIC_HEADER DESTINATION ${MVM_INSTALL_PREFIX}/include/mvm
        ARCHIVE DESTINATION ${MVM_INSTALL_PREFIX}/lib/static)

INSTALL (
  DIRECTORY ${CMAKE_CURRENT_SOURCE_DIR}/include/mvm/
  DESTINATION ${MVM_INSTALL_PREFIX}/include/mvm
  FILES_MATCHING PATTERN "*.h*"
)

INSTALL (
  DIRECTORY ${CMAKE_CURRENT_SOURCE_DIR}/include/evmmax/
  DESTINATION ${MVM_INSTALL_PREFIX}/include/evmmax
  FILES_MATCHING PATTERN "*.h*"
)

install(FILES
  ${CMAKE_CURRENT_SOURCE_DIR}/3rdparty/nlohmann/json.hpp
  DESTINATION ${MVM_INSTALL_PREFIX}/include/3rdparty/nlohmann
)

INSTALL (
  DIRECTORY ${CMAKE_CURRENT_SOURCE_DIR}/3rdparty/keccak/
  DESTINATION ${MVM_INSTALL_PREFIX}/include/3rdparty/keccak
  FILES_MATCHING PATTERN "*.h*"
)

install(FILES
${CMAKE_CURRENT_SOURCE_DIR}/3rdparty/intx/include/intx/intx.hpp
# ${CMAKE_CURRENT_SOURCE_DIR}/3rdparty/intx/include/intx/int128.hpp
  DESTINATION ${MVM_INSTALL_PREFIX}/include/3rdparty/intx
)

INSTALL (
  DIRECTORY ${CMAKE_CURRENT_SOURCE_DIR}/3rdparty/fmt/
  DESTINATION ${MVM_INSTALL_PREFIX}/include/3rdparty/fmt
  FILES_MATCHING PATTERN "*.h*"
)
