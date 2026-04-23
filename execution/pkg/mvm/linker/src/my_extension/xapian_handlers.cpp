// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.
// Xapian database handlers - extracted from my_extension.cpp for
// maintainability. Each XAPIAN_* opcode handler is implemented here.
#include "mvm/util.h"
#include "mvm_linker.hpp"
#include "my_extension/constants.h"
#include "my_extension/my_extension.h"
#include "my_extension/utils.h"
#include "xapian/xapian_manager.h"
#include "xapian/xapian_registry.h"
#include "xapian/xapian_search.h"
#include <arpa/inet.h>
#include <filesystem>
#include <iostream>
#include <nlohmann/json.hpp>
#include <optional>
#include <sstream>
#include <vector>
#include <xapian.h>

using nlohmann::json;
using std::string;
using std::vector;

#define DBNAME_MAX_LEN 128
#define TERM_MAX_LEN 64

// Forward declarations for helpers defined in my_extension.cpp
extern bool parseABI(const std::vector<uint8_t> &, std::string &,
                     std::string &);
extern std::vector<uint8_t> encodeStringArray(const std::vector<std::string> &);
extern std::string decimalToHex(int decimal);
extern std::vector<uint8_t> getInputWithoutOpcode(const mvm::Code &input);
extern uint64_t hex_to_uint64(const std::string &hex_str);
extern std::optional<int64_t> hex_to_int64(const std::string &hex_str);
extern void printDocInfo(const std::vector<std::string> &docInfo);

// Forward declarations for ABI encode/decode (defined in
// abi_decode.hpp/abi_encode.hpp, already compiled via my_extension.cpp — do NOT
// include here to avoid duplicate symbols)
extern nlohmann::json decode(std::vector<uint8_t> bytes, std::string strAbi);
extern std::vector<uint8_t> encodeArgument(nlohmann::json abi,
                                           std::string argument);
extern std::vector<uint8_t> addOffsetPrefix(const std::vector<uint8_t> &input);
extern std::string joinStringArgument(const std::vector<std::string> &parts);

// Main function
mvm::Code MyExtension::FullDatabase(mvm::Code input, mvm::Address address,
                                    bool isReset, uint256_t blockNumber) {
  // Kiểm tra kích thước input hợp lệ
  if (input.size() < 4) {
    std::cerr << "Error: Input size too small!" << std::endl;
    return mvm::Code(32, 0);
  }

  // Lấy opcode từ 4 byte đầu tiên
  uint32_t opCode =
      (input[0] << 24) | (input[1] << 16) | (input[2] << 8) | input[3];

  // Chuyển đổi địa chỉ thành dạng byte array
  uint256_t addr = address;
  std::vector<uint8_t> addressBytes(20);
  for (size_t i = 0; i < 20; ++i) {
    addressBytes[19 - i] = static_cast<uint8_t>(addr >> (i * 8));
  }
  if (this->isOffChain) {
    bool isWriteOp =
        (opCode == mvm::FunctionSelector::XAPIAN_NEW_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_DELETE_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_ADD_TERM_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_INDEX_TEXT_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_SET_DATA_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_ADD_VALUE_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_ADD_FIELD_TO_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_SET_CONFIG_FILED ||
         opCode == mvm::FunctionSelector::XAPIAN_INDEX_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_ADD_FIELD ||
         opCode == mvm::FunctionSelector::XAPIAN_REMOVE_FIELD ||
         opCode == mvm::FunctionSelector::XAPIAN_UPDATE_FIELD ||
         opCode == mvm::FunctionSelector::XAPIAN_ADD_TAG ||
         opCode == mvm::FunctionSelector::XAPIAN_REMOVE_TAG ||
         opCode == mvm::FunctionSelector::XAPIAN_UPDATE_TAG ||
         opCode == mvm::FunctionSelector::XAPIAN_COMMIT);
    if (isWriteOp) {
      // Off-chain: trả về success giả (0x1) mà không thực sự ghi
      // Giúp smart contract không revert, nhưng data không bị ghi xuống DB
      json uint256Abi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(1);
      return encodeArgument(uint256Abi, hexNumber);
    }
  }
  // Xử lý các operation dựa vào opCode
  try {
    // GET_OR_CREATE_DB
    if (opCode == mvm::FunctionSelector::XAPIAN_GET_OR_CREATE_DB) {
      std::string selector, extracted_str;
      bool result = parseABI(input, selector, extracted_str);
      if (extracted_str.empty()) {
        std::cerr << "[ERROR] XAPIAN_GET_OR_CREATE_DB: dbname is empty!"
                  << std::endl;
        return mvm::Code(32, 0);
      }

      std::filesystem::path fullPath =
          mvm::createFullPath(address, extracted_str);

      std::cerr << "[DEBUG] XAPIAN_GET_OR_CREATE_DB: dbname=" << extracted_str
                << " fullPath=" << fullPath.string() << " address=" << address
                << std::endl;

      if (!std::filesystem::exists(fullPath)) {
        std::error_code ec;
        std::filesystem::create_directories(fullPath, ec);
        if (ec) {
          std::cerr
              << "[ERROR] XAPIAN_GET_OR_CREATE_DB: create_directories failed: "
              << ec.message() << " path=" << fullPath.string() << std::endl;
          return mvm::Code(32, 0);
        }
        std::cerr << "[DEBUG] XAPIAN_GET_OR_CREATE_DB: created directory: "
                  << fullPath.string() << std::endl;
      } else {
        std::cerr
            << "[DEBUG] XAPIAN_GET_OR_CREATE_DB: directory already exists: "
            << fullPath.string() << std::endl;
      }

      auto manager =
          XapianManager::getInstance(extracted_str, address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
        std::cerr << "[DEBUG] XAPIAN_GET_OR_CREATE_DB: OK dbname="
                  << extracted_str << std::endl;
      } else {
        std::cerr
            << "[ERROR] XAPIAN_GET_OR_CREATE_DB: getInstance returned null for "
            << fullPath.string() << std::endl;
        return mvm::Code(32, 0);
      }
      json uint256Abi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(true);
      return encodeArgument(uint256Abi, hexNumber);
    }

    // NEW_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_NEW_DOCUMENT) {
      auto input_without_opcode = getInputWithoutOpcode(input);
      std::string dbname;
      std::string rawData;

      std::string inputABI = R"([
                  {"internalType": "string", "name": "dbname", "type": "string"},
                  {"internalType": "string", "name": "data", "type": "string"}
              ])";

      nlohmann::json input_argument = decode(input_without_opcode, inputABI);
      dbname = input_argument["dbname"].get<std::string>();
      rawData = input_argument["data"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      auto manager = XapianManager::getInstance(dbname, address, isReset);
      if (!manager) {
        std::cerr << "Failed to create XapianManager" << std::endl;
        return mvm::Code(32, 0);
      }

      auto newDocID = manager->new_document(rawData, blockNumber);

      std::cerr << "[DEBUG] XAPIAN_NEW_DOCUMENT: dbname=" << dbname
                << " blockNumber=" << mvm::uint256_to_double(blockNumber)
                << " newDocID=" << newDocID << std::endl;

      if (newDocID == 0) {
        std::cerr << "[ERROR] XAPIAN_NEW_DOCUMENT: new_document returned 0 "
                     "(failed to add document)"
                  << std::endl;
      }

      json uint256Abi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(newDocID);
      manager->dump_all_documents(blockNumber);

      return encodeArgument(uint256Abi, hexNumber);
    }

    // GET_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_GET_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_GET_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_GET_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      intx::uint256 number = intx::from_string<intx::uint256>(hex_str);

      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (!manager) {
        // FORK-SAFETY: Read-only operation — do NOT register with registry.
        // Registering would include this manager's (potentially stale)
        // comprehensive_log in the hash computation for this transaction.
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo =
          manager->get_document(static_cast<int>(number), blockNumber);

      return mvm::Code(32, 0);
    }

    // DELETE_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_DELETE_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_DELETE_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_DELETE_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      intx::uint256 number = intx::from_string<intx::uint256>(hex_str);
      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo =
          manager->delete_document(static_cast<int>(number), blockNumber);

      json uint256Abi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(docInfo);
      auto encodedData = encodeArgument(uint256Abi, hexNumber);
      printHex(encodedData);

      return encodedData;
    }

    // ADD_TERM_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_ADD_TERM_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"},
                {"internalType": "string", "name": "term", "type": "string"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();
      std::string term = input_argument["term"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_ADD_TERM_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr << "[Error] FullDatabase (XAPIAN_ADD_TERM_DOCUMENT): dbname "
                     "length ("
                  << dbname.length() << ") exceeds maximum of "
                  << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      // *** Kiểm tra term ***
      if (term.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_ADD_TERM_DOCUMENT): term "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (term.length() >= TERM_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_ADD_TERM_DOCUMENT): term length ("
            << term.length() << ") exceeds maximum of " << (TERM_MAX_LEN - 1)
            << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      intx::uint256 number = intx::from_string<intx::uint256>(hex_str);

      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo = manager->add_term(static_cast<int>(number),
                                       input_argument["term"], blockNumber);

      std::cerr << "[DEBUG] XAPIAN_ADD_TERM_DOCUMENT: dbname=" << dbname
                << " inputDocId=" << static_cast<int>(number)
                << " term=" << term
                << " blockNumber=" << mvm::uint256_to_double(blockNumber)
                << " returnedDocId=" << docInfo << std::endl;

      if (docInfo == 0) {
        std::cerr << "[ERROR] XAPIAN_ADD_TERM_DOCUMENT: add_term returned 0 "
                     "(document not found or Xapian error)"
                  << std::endl;
      }
      manager->dump_all_documents(blockNumber);

      json uint256Abi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(docInfo);
      auto encodedData = encodeArgument(uint256Abi, hexNumber);

      return encodedData;
    }

    // ADD_TERM_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_INDEX_TEXT_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"},
                {"internalType": "string", "name": "text", "type": "string"},
                {"internalType": "uint8", "name": "weight", "type": "uint8"},
                {"internalType": "string", "name": "prefix", "type": "string"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_INDEX_TEXT_DOCUMENT): "
                     "dbname cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr << "[Error] FullDatabase (XAPIAN_INDEX_TEXT_DOCUMENT): "
                     "dbname length ("
                  << dbname.length() << ") exceeds maximum of "
                  << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      intx::uint256 docId = intx::from_string<intx::uint256>(hex_str);
      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo =
          manager->index_text(static_cast<int>(docId), input_argument["text"],
                              hex_to_uint64(input_argument["weight"]),
                              input_argument["prefix"], blockNumber);

      std::cerr << "[DEBUG] XAPIAN_INDEX_TEXT_DOCUMENT: dbname=" << dbname
                << " inputDocId=" << static_cast<int>(docId)
                << " prefix=" << input_argument["prefix"].get<std::string>()
                << " text=" << input_argument["text"].get<std::string>()
                << " blockNumber=" << mvm::uint256_to_double(blockNumber)
                << " returnedDocId=" << docInfo << std::endl;

      if (docInfo == 0) {
        std::cerr
            << "[ERROR] XAPIAN_INDEX_TEXT_DOCUMENT: index_text returned 0 "
               "(failed to index)"
            << std::endl;
      }
      manager->dump_all_documents(blockNumber);
      json uint256Abi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(docInfo);
      auto encodedData = encodeArgument(uint256Abi, hexNumber);
      return encodedData;
    }

    // SET_DATA_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_SET_DATA_DOCUMENT) {
      auto input_without_opcode = getInputWithoutOpcode(input);
      std::string dbname;
      intx::uint256 number;
      std::string rawData;

      std::string inputABI = R"([
                  {"internalType": "string", "name": "dbname", "type": "string"},
                  {"internalType": "uint256", "name": "docId", "type": "uint256"},
                  {"internalType": "string", "name": "data", "type": "string"}
              ])";

      nlohmann::json input_argument = decode(input_without_opcode, inputABI);
      dbname = input_argument["dbname"].get<std::string>();
      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      number = intx::from_string<intx::uint256>(hex_str);
      rawData = input_argument["data"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_SET_DATA_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr << "[Error] FullDatabase (XAPIAN_SET_DATA_DOCUMENT): dbname "
                     "length ("
                  << dbname.length() << ") exceeds maximum of "
                  << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }
      auto manager = XapianManager::getInstance(dbname, address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho " << dbname
                  << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo =
          manager->set_data(static_cast<int>(number), rawData, blockNumber);

      std::cerr << "[DEBUG] XAPIAN_SET_DATA_DOCUMENT: dbname=" << dbname
                << " inputDocId=" << static_cast<int>(number)
                << " data=" << rawData
                << " blockNumber=" << mvm::uint256_to_double(blockNumber)
                << " returnedDocId=" << docInfo << std::endl;

      if (docInfo == 0) {
        std::cerr << "[ERROR] XAPIAN_SET_DATA_DOCUMENT: set_data returned 0 "
                     "(document not found or Xapian error)"
                  << std::endl;
      }

      json uint256Abi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(docInfo);
      return encodeArgument(uint256Abi, hexNumber);
    }

    // ADD_VALUE_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_ADD_VALUE_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"},
                {"internalType": "uint256", "name": "slot", "type": "uint256"},
                {"internalType": "string", "name": "data", "type": "string"},
                {"internalType": "bool", "name": "isSerialise", "type": "bool"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_ADD_VALUE_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr << "[Error] FullDatabase (XAPIAN_ADD_VALUE_DOCUMENT): dbname "
                     "length ("
                  << dbname.length() << ") exceeds maximum of "
                  << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo = manager->add_value(
          hex_to_uint64(input_argument["docId"]),
          hex_to_uint64(input_argument["slot"]), input_argument["data"],
          input_argument["isSerialise"].get<bool>(), blockNumber);

      json uint256Abi = {{"type", "uint256"}};
      std::string value = decimalToHex(docInfo);

      auto encodedData = encodeArgument(uint256Abi, value);
      printHex(encodedData);

      return encodedData;
    }

    // GET_DATA_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_GET_DATA_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_GET_DATA_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr << "[Error] FullDatabase (XAPIAN_GET_DATA_DOCUMENT): dbname "
                     "length ("
                  << dbname.length() << ") exceeds maximum of "
                  << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      intx::uint256 number = intx::from_string<intx::uint256>(hex_str);
      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (!manager) {
        // FORK-SAFETY: Read-only operation — do NOT register with registry.
        // Registering would include this manager's (potentially stale)
        // comprehensive_log in the hash computation for this transaction.
        std::cerr << "Lỗi[XAPIAN_GET_DATA_DOCUMENT]: Không thể lấy/tạo "
                     "XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo = manager->get_data(static_cast<int>(number), blockNumber);

      json stringAbi = {{"type", "string"}};
      auto encodedData = encodeArgument(stringAbi, docInfo);
      return addOffsetPrefix(encodedData);
    }

    // GET_TERMS_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_GET_TERMS_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_GET_TERMS_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr << "[Error] FullDatabase (XAPIAN_GET_TERMS_DOCUMENT): dbname "
                     "length ("
                  << dbname.length() << ") exceeds maximum of "
                  << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      intx::uint256 number = intx::from_string<intx::uint256>(hex_str);
      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (!manager) {
        // FORK-SAFETY: Read-only operation — do NOT register with registry.
        // Registering would include this manager's (potentially stale)
        // comprehensive_log in the hash computation for this transaction.
        std::cerr << "Lỗi[XAPIAN_GET_TERMS_DOCUMENT]: Không thể lấy/tạo "
                     "XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo = manager->get_terms(static_cast<int>(number), blockNumber);
      printDocInfo(docInfo);

      json stringArrayAbi = {{"type", "string[]"}};
      auto encodedData =
          encodeArgument(stringArrayAbi, joinStringArgument(docInfo));

      printHex(encodedData);
      return addOffsetPrefix(encodedData);
    }

    // GET_VALUE_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_GET_VALUE_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"},
                {"internalType": "uint256", "name": "slot", "type": "uint256"},
                {"internalType": "bool", "name": "isSerialise", "type": "bool"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_GET_VALUE_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr << "[Error] FullDatabase (XAPIAN_GET_VALUE_DOCUMENT): dbname "
                     "length ("
                  << dbname.length() << ") exceeds maximum of "
                  << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (!manager) {
        // FORK-SAFETY: Read-only operation — do NOT register with registry.
        // Registering would include this manager's (potentially stale)
        // comprehensive_log in the hash computation for this transaction.
        std::cerr << "Lỗi[XAPIAN_GET_VALUE_DOCUMENT]: Không thể lấy/tạo "
                     "XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo = manager->get_value(
          hex_to_uint64(input_argument["docId"]),
          hex_to_uint64(input_argument["slot"]),
          input_argument["isSerialise"].get<bool>(), blockNumber);

      json stringAbi = {{"type", "string"}};
      auto encodedData = encodeArgument(stringAbi, docInfo);
      printHex(encodedData);
      return addOffsetPrefix(encodedData);
    }

    if (opCode == mvm::FunctionSelector::XAPIAN_QUERY_SEARCH) {
      string abi_string = R"([
      {
        "name": "dbName",
        "type": "string"
      },
      {
        "name": "options",
        "type": "tuple",
        "components": [
          {
            "name": "queries",
            "type": "string"
          },
          {
            "name": "prefixMap",
            "type": "tuple[]",
            "components": [
              {
                "name": "key",
                "type": "string"
              },
              {
                "name": "value",
                "type": "string"
              }
            ]
          },
          {
            "name": "stopWords",
            "type": "string[]"
          },
          {
            "name": "offset",
            "type": "uint64"
          },
          {
            "name": "limit",
            "type": "uint64"
          },
          {
            "name": "sortByValueSlot",
            "type": "int64"
          },
          {
            "name": "sortAscending",
            "type": "bool"
          },
          {
            "name": "rangeFilters",
            "type": "tuple[]",
            "components": [
              {
                "name": "slot",
                "type": "uint256"
              },
              {
                "name": "begin",
                "type": "string"
              },
              {
                "name": "end",
                "type": "string"
              }
            ]
          }
        ]
      }
    ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      std::string dbName = getDbNameFromABI(input_without_opcode);
      // Giải mã dữ liệu
      json decodedData = decode(input_without_opcode, abi_string);

      // *** Kiểm tra dbname ***
      if (dbName.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_QUERY_SEARCH): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbName.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_QUERY_SEARCH): dbname length ("
            << dbName.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }
      std::filesystem::path fullPath = mvm::createFullPath(address, dbName);

      XapianSearcher searcher(fullPath);
      std::vector<std::string> queries1 = {decodedData["options"]["queries"]};

      std::map<std::string, std::string> product_prefix_map =
          convertJsonToMap(decodedData["options"]["prefixMap"]);
      std::optional<std::vector<std::string>> stop_words_list =
          convertJsonToStopWordsList(decodedData["options"]["stopWords"]);

      std::optional<std::string> stem_lang = std::nullopt;
      Xapian::doccount offset = 0;
      try {
        offset = hex_to_uint64(decodedData["options"]["offset"]);
      } catch (...) {
        // Giữ giá trị mặc định nếu có lỗi
      }

      // Gán limit với giá trị mặc định là 10
      Xapian::doccount limit = 10;
      try {
        limit = hex_to_uint64(decodedData["options"]["limit"]);
      } catch (...) {
        // Giữ giá trị mặc định nếu có lỗi
      }

      // Gán sort_by_value_slot với giá trị mặc định là 0
      std::optional<Xapian::valueno> sort_by_value_slot = std::nullopt;
      try {

        auto sort_slot =
            hex_to_int64(decodedData["options"]["sortByValueSlot"]);

        if (sort_slot.has_value()) {
          if (sort_slot >= 0)
            sort_by_value_slot = sort_slot;
        }
      } catch (...) {
        // Giữ giá trị mặc định nếu có lỗi
      }

      bool sort_ascending = true;

      try {
        sort_ascending = decodedData["options"]["sortAscending"].get<bool>();
      } catch (...) {
        // Giữ giá trị mặc định nếu có lỗi
      }

      std::vector<RangeFilter> range_filters =
          convertJsonToRangeFilters(decodedData["options"]);

      std::cerr << "--- [XAPIAN_QUERY_SEARCH] ---" << std::endl;
      std::cerr << "DB Name: " << dbName << std::endl;
      std::cerr << "Queries: " << queries1[0] << std::endl;
      std::cerr << "Offset: " << offset << ", Limit: " << limit << std::endl;
      std::cerr << "Block Number: " << mvm::uint256_to_double(blockNumber)
                << std::endl;

      if (sort_by_value_slot.has_value()) {
        std::cerr << "Sort by Slot: " << sort_by_value_slot.value()
                  << (sort_ascending ? " (ASC)" : " (DESC)") << std::endl;
      } else {
        std::cerr << "Sort: NONE" << std::endl;
      }

      for (const auto &rf : range_filters) {
        std::cerr << "Range Filter - Slot: " << rf.slot << std::endl;
      }

      std::cerr << "[searcher] Dumping Index..." << std::endl;
      searcher.dumpIndex();

      auto [results1, total1] = searcher.search(
          queries1, Xapian::Query::OP_AND, Xapian::Query::OP_AND,
          product_prefix_map, stem_lang, stop_words_list, offset, limit,
          sort_by_value_slot, sort_ascending, range_filters, blockNumber);

      std::cerr << "[searcher] Search completed." << std::endl;
      std::cerr << "[searcher] Total estimated: " << total1 << std::endl;
      std::cerr << "[searcher] Results size: " << results1.size() << std::endl;

      for (size_t i = 0; i < results1.size(); ++i) {
        std::cerr << "  Result[" << i << "]: DocID=" << results1[i].docid
                  << ", Data=" << results1[i].data.substr(0, 100)
                  << (results1[i].data.length() > 100 ? "..." : "")
                  << std::endl;
      }
      std::cerr << "-----------------------------" << std::endl;

      auto dataReturn = searcher.encodeSearchResultsPage(total1, results1);
      return addOffsetPrefix(dataReturn);
    }

    if (opCode == mvm::FunctionSelector::XAPIAN_COMMIT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_COMMIT): dbname cannot be empty."
            << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr << "[Error] FullDatabase (XAPIAN_COMMIT): dbname length ("
                  << dbname.length() << ") exceeds maximum of "
                  << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto hash = manager->getChangeHash();
      auto log = manager->getChangeLogs();
      auto status = registry.commitChangesForMvmId(this->mvmId);
      json stringAbi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(status);
      auto encodedData = encodeArgument(stringAbi, hexNumber);
      printHex(encodedData);
      return encodedData;
    }
  } catch (const Xapian::Error &e) {
    std::cerr << "[ERROR] FullDatabase Xapian error: " << e.get_description()
              << std::endl;
  } catch (const std::exception &e) {
    std::cerr << "Error in operation: " << e.what() << std::endl;
  } catch (...) {
    std::cerr << "Unknown error" << std::endl;
  }

  return mvm::Code(32, 0);
}

mvm::Code MyExtension::FullDatabaseV1(mvm::Code input, mvm::Address address,
                                      bool isReset, uint256_t blockNumber) {
  // Kiểm tra kích thước input hợp lệ
  if (input.size() < 4) {
    std::cerr << "Error: Input size too small!" << std::endl;
    return mvm::Code(32, 0);
  }

  // Lấy opcode từ 4 byte đầu tiên
  uint32_t opCode =
      (input[0] << 24) | (input[1] << 16) | (input[2] << 8) | input[3];

  // Chuyển đổi địa chỉ thành dạng byte array
  uint256_t addr = address;
  std::vector<uint8_t> addressBytes(20);
  for (size_t i = 0; i < 20; ++i) {
    addressBytes[19 - i] = static_cast<uint8_t>(addr >> (i * 8));
  }

  // Xử lý các operation dựa vào opCode
  // [GUARD] Block write operations khi chạy off-chain (eth_call)
  // Giống Ethereum block SSTORE trong STATICCALL
  if (this->isOffChain) {
    bool isWriteOp =
        (opCode == mvm::FunctionSelector::XAPIAN_V1_NEW_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_DELETE_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_ADD_TERM_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_INDEX_TEXT_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_SET_DATA_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_ADD_VALUE_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_ADD_FIELD_TO_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_SET_CONFIG_FILED ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_INDEX_DOCUMENT ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_ADD_FIELD ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_REMOVE_FIELD ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_UPDATE_FIELD ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_ADD_TAG ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_REMOVE_TAG ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_UPDATE_TAG ||
         opCode == mvm::FunctionSelector::XAPIAN_V1_COMMIT);
    if (isWriteOp) {
      // Off-chain: trả về success giả (0x1) mà không thực sự ghi
      // Giúp smart contract không revert, nhưng data không bị ghi xuống DB
      json uint256Abi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(1);
      return encodeArgument(uint256Abi, hexNumber);
    }
  }

  try {
    // GET_OR_CREATE_DB
    if (opCode == mvm::FunctionSelector::XAPIAN_V1_GET_OR_CREATE_DB) {
      std::string selector, extracted_str;
      bool result = parseABI(input, selector, extracted_str);
      if (extracted_str.empty()) {
        std::cerr << "Error: Extracted database name is empty!" << std::endl;
        return mvm::Code(32, 0);
      }

      std::filesystem::path fullPath =
          mvm::createFullPath(address, extracted_str);

      if (!std::filesystem::exists(fullPath)) {
        std::filesystem::create_directories(fullPath);
      }

      auto manager =
          XapianManager::getInstance(extracted_str, address, isReset);
      if (manager) {
        // [PERF] Off-chain (eth_call) KHÔNG cần register vào registry
        // vì write đã bị guard chặn, và cancelTransaction không cần cleanup
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << fullPath.string() << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      json uint256Abi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(true);
      return encodeArgument(uint256Abi, hexNumber);
    }

    // NEW_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_V1_NEW_DOCUMENT) {
      auto input_without_opcode = getInputWithoutOpcode(input);
      std::string dbname;
      std::string rawData;

      if (input_without_opcode.size() < 64) {
        std::cerr << "Error: Input size too small for version 1 NEW_DOCUMENT"
                  << std::endl;
        return mvm::Code(32, 0);
      }
      uint32_t dbname_offset =
          (input_without_opcode[28] << 24) | (input_without_opcode[29] << 16) |
          (input_without_opcode[30] << 8) | input_without_opcode[31];
      uint32_t data_offset =
          (input_without_opcode[60] << 24) | (input_without_opcode[61] << 16) |
          (input_without_opcode[62] << 8) | input_without_opcode[63];

      uint32_t dbname_len = (input_without_opcode[dbname_offset + 28] << 24) |
                            (input_without_opcode[dbname_offset + 29] << 16) |
                            (input_without_opcode[dbname_offset + 30] << 8) |
                            input_without_opcode[dbname_offset + 31];

      dbname = std::string(input_without_opcode.begin() + dbname_offset + 32,
                           input_without_opcode.begin() + dbname_offset + 32 +
                               dbname_len);
      uint32_t data_len = (input_without_opcode[data_offset + 28] << 24) |
                          (input_without_opcode[data_offset + 29] << 16) |
                          (input_without_opcode[data_offset + 30] << 8) |
                          input_without_opcode[data_offset + 31];
      rawData = std::string(input_without_opcode.begin() + data_offset + 32,
                            input_without_opcode.begin() + data_offset + 32 +
                                data_len);

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      auto manager = XapianManager::getInstance(dbname, address, isReset);
      if (!manager) {
        std::cerr << "Failed to create XapianManager" << std::endl;
        return mvm::Code(32, 0);
      }

      auto newDocID = manager->new_document(rawData, blockNumber);

      json uint256Abi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(newDocID);
      return encodeArgument(uint256Abi, hexNumber);
    }

    // GET_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_V1_GET_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      intx::uint256 number = intx::from_string<intx::uint256>(hex_str);

      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo =
          manager->get_document(static_cast<int>(number), blockNumber);

      return mvm::Code(32, 0);
    }

    // DELETE_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_V1_DELETE_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      intx::uint256 number = intx::from_string<intx::uint256>(hex_str);
      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo =
          manager->delete_document(static_cast<int>(number), blockNumber);

      json uint256Abi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(docInfo);
      auto encodedData = encodeArgument(uint256Abi, hexNumber);
      printHex(encodedData);

      return encodedData;
    }

    // ADD_TERM_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_V1_ADD_TERM_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"},
                {"internalType": "string", "name": "term", "type": "string"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();
      std::string term = input_argument["term"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      // *** Kiểm tra dbname ***
      if (term.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (term.length() >= TERM_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of " << (TERM_MAX_LEN - 1)
            << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      intx::uint256 number = intx::from_string<intx::uint256>(hex_str);

      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo = manager->add_term(static_cast<int>(number),
                                       input_argument["term"], blockNumber);

      json uint256Abi = {{"type", "uint256"}};
      std::string value = std::to_string(docInfo);
      std::string hexNumber = decimalToHex(docInfo);
      auto encodedData = encodeArgument(uint256Abi, hexNumber);

      return encodedData;
    }

    // ADD_TERM_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_V1_INDEX_TEXT_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"},
                {"internalType": "string", "name": "text", "type": "string"},
                {"internalType": "uint8", "name": "weight", "type": "uint8"},
                {"internalType": "string", "name": "prefix", "type": "string"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      intx::uint256 docId = intx::from_string<intx::uint256>(hex_str);
      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo =
          manager->index_text(static_cast<int>(docId), input_argument["text"],
                              hex_to_uint64(input_argument["weight"]),
                              input_argument["prefix"], blockNumber);

      json uint256Abi = {{"type", "uint256"}};
      std::string value = std::to_string(docInfo);
      std::string hexNumber = decimalToHex(docInfo);
      auto encodedData = encodeArgument(uint256Abi, hexNumber);
      return encodedData;
    }

    // SET_DATA_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_V1_SET_DATA_DOCUMENT) {
      auto input_without_opcode = getInputWithoutOpcode(input);
      std::string dbname;
      intx::uint256 number;
      std::string rawData;

      if (input_without_opcode.size() < 96) {
        std::cerr
            << "Error: Input size too small for version 1 SET_DATA_DOCUMENT"
            << std::endl;
        return mvm::Code(32, 0);
      }
      uint32_t dbname_offset =
          (input_without_opcode[28] << 24) | (input_without_opcode[29] << 16) |
          (input_without_opcode[30] << 8) | input_without_opcode[31];

      std::stringstream docid_ss;
      docid_ss << "0x";
      for (int i = 32; i < 64; ++i) {
        docid_ss << std::hex << std::setw(2) << std::setfill('0')
                 << (int)input_without_opcode[i];
      }
      number = intx::from_string<intx::uint256>(docid_ss.str());

      uint32_t data_offset =
          (input_without_opcode[92] << 24) | (input_without_opcode[93] << 16) |
          (input_without_opcode[94] << 8) | input_without_opcode[95];

      uint32_t dbname_len = (input_without_opcode[dbname_offset + 28] << 24) |
                            (input_without_opcode[dbname_offset + 29] << 16) |
                            (input_without_opcode[dbname_offset + 30] << 8) |
                            input_without_opcode[dbname_offset + 31];

      dbname = std::string(input_without_opcode.begin() + dbname_offset + 32,
                           input_without_opcode.begin() + dbname_offset + 32 +
                               dbname_len);
      uint32_t data_len = (input_without_opcode[data_offset + 28] << 24) |
                          (input_without_opcode[data_offset + 29] << 16) |
                          (input_without_opcode[data_offset + 30] << 8) |
                          input_without_opcode[data_offset + 31];
      rawData = std::string(input_without_opcode.begin() + data_offset + 32,
                            input_without_opcode.begin() + data_offset + 32 +
                                data_len);

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      auto manager = XapianManager::getInstance(dbname, address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho " << dbname
                  << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo =
          manager->set_data(static_cast<int>(number), rawData, blockNumber);

      json uint256Abi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(docInfo);
      return encodeArgument(uint256Abi, hexNumber);
    }

    // ADD_VALUE_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_V1_ADD_VALUE_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"},
                {"internalType": "uint256", "name": "slot", "type": "uint256"},
                {"internalType": "string", "name": "data", "type": "string"},
                {"internalType": "bool", "name": "isSerialise", "type": "bool"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo = manager->add_value(
          hex_to_uint64(input_argument["docId"]),
          hex_to_uint64(input_argument["slot"]), input_argument["data"],
          input_argument["isSerialise"].get<bool>(), blockNumber);

      json uint256Abi = {{"type", "uint256"}};
      std::string value = decimalToHex(docInfo);

      auto encodedData = encodeArgument(uint256Abi, value);
      printHex(encodedData);

      return encodedData;
    }

    // GET_DATA_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_V1_GET_DATA_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      intx::uint256 number = intx::from_string<intx::uint256>(hex_str);
      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo = manager->get_data(static_cast<int>(number), blockNumber);

      mvm::Code dataVec;
      size_t data_len = docInfo.size();
      // Prepend 32-byte big-endian length
      dataVec.resize(32, 0);
      dataVec[28] = (data_len >> 24) & 0xFF;
      dataVec[29] = (data_len >> 16) & 0xFF;
      dataVec[30] = (data_len >> 8) & 0xFF;
      dataVec[31] = data_len & 0xFF;
      // Append actual data bytes
      dataVec.insert(dataVec.end(), docInfo.begin(), docInfo.end());
      // Pad to 32-byte boundary
      size_t remainder = dataVec.size() % 32;
      if (remainder != 0) {
        dataVec.insert(dataVec.end(), 32 - remainder, 0x00);
      }
      return addOffsetPrefix(dataVec);
    }

    // GET_TERMS_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_V1_GET_TERMS_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      std::string hex_str = "0x" + input_argument["docId"].get<std::string>();
      intx::uint256 number = intx::from_string<intx::uint256>(hex_str);
      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo = manager->get_terms(static_cast<int>(number), blockNumber);
      printDocInfo(docInfo);

      json stringArrayAbi = {{"type", "string[]"}};
      auto encodedData =
          encodeArgument(stringArrayAbi, joinStringArgument(docInfo));

      printHex(encodedData);
      return addOffsetPrefix(encodedData);
    }

    // GET_VALUE_DOCUMENT
    if (opCode == mvm::FunctionSelector::XAPIAN_V1_GET_VALUE_DOCUMENT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"},
                {"internalType": "uint256", "name": "docId", "type": "uint256"},
                {"internalType": "uint256", "name": "slot", "type": "uint256"},
                {"internalType": "bool", "name": "isSerialise", "type": "bool"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto docInfo = manager->get_value(
          hex_to_uint64(input_argument["docId"]),
          hex_to_uint64(input_argument["slot"]),
          input_argument["isSerialise"].get<bool>(), blockNumber);

      json stringAbi = {{"type", "string"}};
      auto encodedData = encodeArgument(stringAbi, docInfo);
      printHex(encodedData);
      return addOffsetPrefix(encodedData);
    }

    if (opCode == mvm::FunctionSelector::XAPIAN_V1_QUERY_SEARCH) {
      string abi_string = R"([
      {
        "name": "dbName",
        "type": "string"
      },
      {
        "name": "options",
        "type": "tuple",
        "components": [
          {
            "name": "queries",
            "type": "string"
          },
          {
            "name": "prefixMap",
            "type": "tuple[]",
            "components": [
              {
                "name": "key",
                "type": "string"
              },
              {
                "name": "value",
                "type": "string"
              }
            ]
          },
          {
            "name": "stopWords",
            "type": "string[]"
          },
          {
            "name": "offset",
            "type": "uint64"
          },
          {
            "name": "limit",
            "type": "uint64"
          },
          {
            "name": "sortByValueSlot",
            "type": "int64"
          },
          {
            "name": "sortAscending",
            "type": "bool"
          },
          {
            "name": "rangeFilters",
            "type": "tuple[]",
            "components": [
              {
                "name": "slot",
                "type": "uint256"
              },
              {
                "name": "begin",
                "type": "string"
              },
              {
                "name": "end",
                "type": "string"
              }
            ]
          }
        ]
      }
    ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      std::string dbName = getDbNameFromABI(input_without_opcode);
      // Giải mã dữ liệu
      json decodedData = decode(input_without_opcode, abi_string);

      // *** Kiểm tra dbname ***
      if (dbName.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbName.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbName.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }
      std::filesystem::path fullPath = mvm::createFullPath(address, dbName);

      XapianSearcher searcher(fullPath);
      std::vector<std::string> queries1 = {decodedData["options"]["queries"]};

      std::map<std::string, std::string> product_prefix_map =
          convertJsonToMap(decodedData["options"]["prefixMap"]);
      std::optional<std::vector<std::string>> stop_words_list =
          convertJsonToStopWordsList(decodedData["options"]["stopWords"]);

      std::optional<std::string> stem_lang = std::nullopt;
      Xapian::doccount offset = 0;
      try {
        offset = hex_to_uint64(decodedData["options"]["offset"]);
      } catch (...) {
        // Giữ giá trị mặc định nếu có lỗi
      }

      // Gán limit với giá trị mặc định là 10
      Xapian::doccount limit = 10;
      try {
        limit = hex_to_uint64(decodedData["options"]["limit"]);
      } catch (...) {
        // Giữ giá trị mặc định nếu có lỗi
      }

      // Gán sort_by_value_slot với giá trị mặc định là 0
      std::optional<Xapian::valueno> sort_by_value_slot = std::nullopt;
      try {

        auto sort_slot =
            hex_to_int64(decodedData["options"]["sortByValueSlot"]);

        if (sort_slot.has_value()) {
          if (sort_slot >= 0)
            sort_by_value_slot = sort_slot;
        }
      } catch (...) {
        // Giữ giá trị mặc định nếu có lỗi
      }

      bool sort_ascending = true;

      try {
        sort_ascending = decodedData["options"]["sortAscending"].get<bool>();
      } catch (...) {
        // Giữ giá trị mặc định nếu có lỗi
      }

      std::vector<RangeFilter> range_filters =
          convertJsonToRangeFilters(decodedData["options"]);
      std::cerr << "[searcher] dumpIndex" << std::endl;

      searcher.dumpIndex();
      std::cerr << "[searcher] dumpIndex: " << blockNumber << std::endl;

      auto [results1, total1] = searcher.search(
          queries1, Xapian::Query::OP_AND, Xapian::Query::OP_AND,
          product_prefix_map, stem_lang, stop_words_list, offset, limit,
          sort_by_value_slot, sort_ascending, range_filters, blockNumber);

      auto dataReturn = searcher.encodeSearchResultsPage(total1, results1);
      std::cerr << "[searcher] results1 size: " << results1.size() << std::endl;

      return addOffsetPrefix(dataReturn);
    }

    if (opCode == mvm::FunctionSelector::XAPIAN_V1_COMMIT) {
      std::string inputABI = R"([
                {"internalType": "string", "name": "dbname", "type": "string"}
            ])";

      auto input_without_opcode = getInputWithoutOpcode(input);
      nlohmann::json input_argument = decode(input_without_opcode, inputABI);

      std::string dbname = input_argument["dbname"].get<std::string>();

      // *** Kiểm tra dbname ***
      if (dbname.empty()) {
        std::cerr << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname "
                     "cannot be empty."
                  << std::endl;
        return mvm::Code(32, 0);
      }
      if (dbname.length() >= DBNAME_MAX_LEN) {
        std::cerr
            << "[Error] FullDatabase (XAPIAN_V1_NEW_DOCUMENT): dbname length ("
            << dbname.length() << ") exceeds maximum of "
            << (DBNAME_MAX_LEN - 1) << " characters." << std::endl;
        return mvm::Code(32, 0);
      }

      auto manager = XapianManager::getInstance(input_argument["dbname"],
                                                address, isReset);
      if (manager) {
        if (!this->isOffChain) {
          registry.registerManager(this->mvmId, manager);
        }
      } else {
        std::cerr << "Lỗi: Không thể lấy/tạo XapianManager cho "
                  << input_argument["dbname"] << std::endl;
        return mvm::Code(32, 0); // Trả về lỗi
      }
      auto hash = manager->getChangeHash();
      auto log = manager->getChangeLogs();
      auto status = registry.commitChangesForMvmId(this->mvmId);
      json stringAbi = {{"type", "uint256"}};
      std::string hexNumber = decimalToHex(status);
      auto encodedData = encodeArgument(stringAbi, hexNumber);
      printHex(encodedData);
      return encodedData;
    }
  } catch (const std::exception &e) {
    std::cerr << "Error in operation: " << e.what() << std::endl;
  } catch (...) {
    std::cerr << "Unknown error" << std::endl;
  }

  return mvm::Code(32, 0);
}