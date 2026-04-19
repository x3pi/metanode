cmake_minimum_required(VERSION 3.10)

project(mvm)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED True)
# Set C compiler flags
set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -Os")
set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -O3")
set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -march=native")
set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -mtune=native")
set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -ffunction-sections -fdata-sections")
set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -finline-functions")

# Set C++ compiler flags
set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -Os")
set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -O3")
set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -march=native")
set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -mtune=native")
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

install(TARGETS ${PROJECT_NAME}
        RUNTIME DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/bin
        LIBRARY DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/lib
        PUBLIC_HEADER DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/include/mvm
        ARCHIVE DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/lib/static)

INSTALL (
  DIRECTORY ${CMAKE_CURRENT_SOURCE_DIR}/include/mvm/
  DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/include/mvm
  FILES_MATCHING PATTERN "*.h*"
)

INSTALL (
  DIRECTORY ${CMAKE_CURRENT_SOURCE_DIR}/include/evmmax/
  DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/include/evmmax
  FILES_MATCHING PATTERN "*.h*"
)

install(FILES
  ${CMAKE_CURRENT_SOURCE_DIR}/3rdparty/nlohmann/json.hpp
  DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/include/3rdparty/nlohmann
)

INSTALL (
  DIRECTORY ${CMAKE_CURRENT_SOURCE_DIR}/3rdparty/keccak/
  DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/include/3rdparty/keccak
  FILES_MATCHING PATTERN "*.h*"
)

install(FILES
${CMAKE_CURRENT_SOURCE_DIR}/3rdparty/intx/include/intx/intx.hpp
# ${CMAKE_CURRENT_SOURCE_DIR}/3rdparty/intx/include/intx/int128.hpp
  DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/include/3rdparty/intx
)

INSTALL (
  DIRECTORY ${CMAKE_CURRENT_SOURCE_DIR}/3rdparty/fmt/
  DESTINATION ${CMAKE_CURRENT_SOURCE_DIR}/build/include/3rdparty/fmt
  FILES_MATCHING PATTERN "*.h*"
)
