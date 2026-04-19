cmake_minimum_required(VERSION 3.13)

project(mvm_linker)

# Always generate compile_commands.json for clangd (Ctrl+Click / Go to Definition)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)

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

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
# Common variables 
# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
# Include directories
set(MVM_INCLUDE_DIRS
    ../c_mvm/build/include
    ../c_mvm/build/include/3rdparty
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
  src/xapian/xapian_search.cpp
  src/xapian/xapian_registry.cpp
  src/xapian/xapian_log.cpp
  src/abi/encoding_utils.cpp
  src/cross_chain_precompile.cpp
)

set(MVM_SRCS
  ${CMAKE_CURRENT_SOURCE_DIR}/../c_mvm/build/libmvm.a
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