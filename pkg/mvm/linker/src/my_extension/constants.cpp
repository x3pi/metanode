#include "my_extension/constants.h"
#include <mvm/util.h>

namespace mvm {
std::unordered_map<std::string, uint32_t>
    FunctionSelector::functionSelectorCache;

uint32_t FunctionSelector::getFunctionSelectorFromString(
    const std::string &functionSignature) {
  auto it = functionSelectorCache.find(functionSignature);
  if (it != functionSelectorCache.end()) {
    return it->second;
  }

  KeccakHash hash = keccak_256(functionSignature);
  uint32_t selector = (static_cast<uint32_t>(hash[0]) << 24) |
                      (static_cast<uint32_t>(hash[1]) << 16) |
                      (static_cast<uint32_t>(hash[2]) << 8) |
                      static_cast<uint32_t>(hash[3]);

  functionSelectorCache[functionSignature] = selector;
  return selector;
}

uint256_t FunctionSelector::getPaddedAddressSelector(
    const std::string &functionSignature) {
  // Lấy bộ chọn 4-byte bằng phương thức đã có (có sử dụng cache)
  uint32_t selector = getFunctionSelectorFromString(functionSignature);

  // Chuyển đổi nó thành uint256_t
  // Giả sử uint32_to_uint256 thực hiện chính xác việc chuyển đổi này (ví dụ: mở
  // rộng bằng các số 0) và có sẵn trong phạm vi này (ví dụ, thông qua
  // <mvm/util.h> hoặc tương tự).
  return uint32_to_uint256(selector);
}

const uint32_t FunctionSelector::ADD =
    getFunctionSelectorFromString("add(int256,int256)");
const uint32_t FunctionSelector::SUB =
    getFunctionSelectorFromString("sub(int256,int256)");
const uint32_t FunctionSelector::MUL =
    getFunctionSelectorFromString("mul(int256,int256)");
const uint32_t FunctionSelector::DIV =
    getFunctionSelectorFromString("div(int256,int256)");
const uint32_t FunctionSelector::POW =
    getFunctionSelectorFromString("pow(int256,int256)");
const uint32_t FunctionSelector::ROOT =
    getFunctionSelectorFromString("root(int256,int256)");
const uint32_t FunctionSelector::ATAN2 =
    getFunctionSelectorFromString("atan2(int256,int256)");
const uint32_t FunctionSelector::ABS =
    getFunctionSelectorFromString("abs(int256)");
const uint32_t FunctionSelector::ACOS =
    getFunctionSelectorFromString("acos(int256)");
const uint32_t FunctionSelector::SIN =
    getFunctionSelectorFromString("sin(int256)");
const uint32_t FunctionSelector::ASIN =
    getFunctionSelectorFromString("asin(int256)");
const uint32_t FunctionSelector::ATAN =
    getFunctionSelectorFromString("atan(int256)");
const uint32_t FunctionSelector::CEIL =
    getFunctionSelectorFromString("ceil(int256)");
const uint32_t FunctionSelector::COS =
    getFunctionSelectorFromString("cos(int256)");
const uint32_t FunctionSelector::COT =
    getFunctionSelectorFromString("cot(int256)");
const uint32_t FunctionSelector::EXP =
    getFunctionSelectorFromString("exp(int256)");
const uint32_t FunctionSelector::LOG =
    getFunctionSelectorFromString("log(int256)");
const uint32_t FunctionSelector::LOG10 =
    getFunctionSelectorFromString("log10(int256)");
const uint32_t FunctionSelector::LOG2 =
    getFunctionSelectorFromString("log2(int256)");
const uint32_t FunctionSelector::SQRT =
    getFunctionSelectorFromString("sqrt(int256)");
const uint32_t FunctionSelector::TAN =
    getFunctionSelectorFromString("tan(int256)");
const uint32_t FunctionSelector::ENCODE_MPFR =
    getFunctionSelectorFromString("FromInt256String(int256)");
const uint32_t FunctionSelector::PI = getFunctionSelectorFromString("PI()");
const uint32_t FunctionSelector::CEIL_MPFR =
    getFunctionSelectorFromString("ceil_mpfr(int256)");
const uint32_t FunctionSelector::COSH =
    getFunctionSelectorFromString("cosh(int256)");
const uint32_t FunctionSelector::CSC =
    getFunctionSelectorFromString("csc(int256)");
const uint32_t FunctionSelector::EXP2 =
    getFunctionSelectorFromString("exp2(int256)");
const uint32_t FunctionSelector::FLOOR =
    getFunctionSelectorFromString("floor(int256)");
const uint32_t FunctionSelector::ROUND =
    getFunctionSelectorFromString("round(int256)");
const uint32_t FunctionSelector::SEC =
    getFunctionSelectorFromString("sec(int256)");
const uint32_t FunctionSelector::SIGN =
    getFunctionSelectorFromString("sign(int256)");
const uint32_t FunctionSelector::SINH =
    getFunctionSelectorFromString("sinh(int256)");
const uint32_t FunctionSelector::TANH =
    getFunctionSelectorFromString("tanh(int256)");
const uint32_t FunctionSelector::MOD =
    getFunctionSelectorFromString("mod(int256,int256)");
const uint32_t FunctionSelector::GCD =
    getFunctionSelectorFromString("gcd(int256,int256)");
const uint32_t FunctionSelector::LCM =
    getFunctionSelectorFromString("lcm(int256,int256)");

const uint32_t FunctionSelector::GET_OR_CREATE_SIMPLE_DB =
    getFunctionSelectorFromString("getOrCreateSimpleDb(string)");
const uint32_t FunctionSelector::SINPLE_DB_DELETE =
    getFunctionSelectorFromString("deleteDb(string)");
const uint32_t FunctionSelector::SINPLE_GET_NEXT_KEYS =
    getFunctionSelectorFromString("getNextKeys(string,string,uint8)");
const uint32_t FunctionSelector::GET =
    getFunctionSelectorFromString("get(string,string)");
const uint32_t FunctionSelector::SET =
    getFunctionSelectorFromString("set(string,string,string)");
const uint32_t FunctionSelector::GET_ALL =
    getFunctionSelectorFromString("getAll(string)");
const uint32_t FunctionSelector::SEARCH_BY_VALUE =
    getFunctionSelectorFromString("searchByValue(string,string)");

const uint32_t FunctionSelector::XAPIAN_GET_OR_CREATE_DB =
    getFunctionSelectorFromString("getOrCreateDb(string)");
const uint32_t FunctionSelector::XAPIAN_NEW_DOCUMENT =
    getFunctionSelectorFromString("newDocument(string,string)");
const uint32_t FunctionSelector::XAPIAN_GET_DOCUMENT =
    getFunctionSelectorFromString("getDocument(string,uint256)");
const uint32_t FunctionSelector::XAPIAN_DELETE_DOCUMENT =
    getFunctionSelectorFromString("deleteDocument(string,uint256)");
const uint32_t FunctionSelector::XAPIAN_ADD_FIELD_TO_DOCUMENT =
    getFunctionSelectorFromString("addFieldToDocument(uint256,string,string)");
const uint32_t FunctionSelector::XAPIAN_SET_DATA_DOCUMENT =
    getFunctionSelectorFromString("setDataDocument(string,uint256,string)");
const uint32_t FunctionSelector::XAPIAN_ADD_VALUE_DOCUMENT =
    getFunctionSelectorFromString(
        "addValueDocument(string,uint256,uint256,string,bool)");
const uint32_t FunctionSelector::XAPIAN_ADD_TERM_DOCUMENT =
    getFunctionSelectorFromString("addTermDocument(string,uint256,string)");
const uint32_t FunctionSelector::XAPIAN_GET_DATA_DOCUMENT =
    getFunctionSelectorFromString("getDataDocument(string,uint256)");
const uint32_t FunctionSelector::XAPIAN_GET_VALUE_DOCUMENT =
    getFunctionSelectorFromString(
        "getValueDocument(string,uint256,uint256,bool)");
const uint32_t FunctionSelector::XAPIAN_GET_TERMS_DOCUMENT =
    getFunctionSelectorFromString("getTermsDocument(string,uint256)");
const uint32_t FunctionSelector::XAPIAN_INDEX_TEXT_DOCUMENT =
    getFunctionSelectorFromString(
        "indexTextForDocument(string,uint256,string,uint8,string)");

const uint32_t FunctionSelector::XAPIAN_SET_CONFIG_FILED =
    getFunctionSelectorFromString("setConfigField(string,string,string)");
const uint32_t FunctionSelector::XAPIAN_GET_FILED_DETAILS =
    getFunctionSelectorFromString("getFieldDetails(string,string)");
const uint32_t FunctionSelector::XAPIAN_INDEX_DOCUMENT =
    getFunctionSelectorFromString("indexDocument(string,string[])");
const uint32_t FunctionSelector::XAPIAN_SEARCH =
    getFunctionSelectorFromString("search(string,string)");
const uint32_t FunctionSelector::XAPIAN_GET_ALL_FIELDS =
    getFunctionSelectorFromString("getAllFields(string)");
const uint32_t FunctionSelector::XAPIAN_ADD_FIELD =
    getFunctionSelectorFromString("addField(string,string)");
const uint32_t FunctionSelector::XAPIAN_GET_FIELD_BY_NAME =
    getFunctionSelectorFromString("getFieldByName(string,string)");
const uint32_t FunctionSelector::XAPIAN_REMOVE_FIELD =
    getFunctionSelectorFromString("removeField(string,string)");
const uint32_t FunctionSelector::XAPIAN_UPDATE_FIELD =
    getFunctionSelectorFromString(
        "updateField(string,string,uint16,uint8,bool,bool,uint8)");

// Tags

const uint32_t FunctionSelector::XAPIAN_ADD_TAG =
    getFunctionSelectorFromString("addTag(string,string,string)");
const uint32_t FunctionSelector::XAPIAN_REMOVE_TAG =
    getFunctionSelectorFromString("removeTag(string,string,string)");
const uint32_t FunctionSelector::XAPIAN_UPDATE_TAG =
    getFunctionSelectorFromString("updateTag(string,string,string,string)");
const uint32_t FunctionSelector::XAPIAN_GET_ALL_TAGS =
    getFunctionSelectorFromString("getAllTags(string,string)");
const uint32_t FunctionSelector::XAPIAN_GET_TAG =
    getFunctionSelectorFromString("getTag(string,string,string)");

// Seach
const uint32_t FunctionSelector::XAPIAN_QUERY_SEARCH =
    getFunctionSelectorFromString(
        "querySearch(string,(string,(string,string)[],string[],uint64,uint64,"
        "int64,bool,(uint256,string,string)[]))");

// Commit
const uint32_t FunctionSelector::XAPIAN_COMMIT =
    getFunctionSelectorFromString("commit(string)");

// ─── V1 CONSTANTS (Native Bytes)
// ────────────────────────────────────────────────
const uint32_t FunctionSelector::XAPIAN_V1_NEW_DOCUMENT =
    getFunctionSelectorFromString("newDocument(string,bytes)");
const uint32_t FunctionSelector::XAPIAN_V1_SET_DATA_DOCUMENT =
    getFunctionSelectorFromString("setDataDocument(string,uint256,bytes)");
const uint32_t FunctionSelector::XAPIAN_V1_GET_DATA_DOCUMENT =
    getFunctionSelectorFromString(
        "getDataDocument(string,uint256)"); // Returns bytes!
const uint32_t FunctionSelector::XAPIAN_V1_GET_OR_CREATE_DB =
    getFunctionSelectorFromString("getOrCreateDb(string)");
const uint32_t FunctionSelector::XAPIAN_V1_DELETE_DOCUMENT =
    getFunctionSelectorFromString("deleteDocument(string,uint256)");
const uint32_t FunctionSelector::XAPIAN_V1_ADD_VALUE_DOCUMENT =
    getFunctionSelectorFromString(
        "addValueDocument(string,uint256,uint256,string,bool)");
const uint32_t FunctionSelector::XAPIAN_V1_ADD_TERM_DOCUMENT =
    getFunctionSelectorFromString("addTermDocument(string,uint256,string)");
const uint32_t FunctionSelector::XAPIAN_V1_GET_VALUE_DOCUMENT =
    getFunctionSelectorFromString(
        "getValueDocument(string,uint256,uint256,bool)");
const uint32_t FunctionSelector::XAPIAN_V1_GET_TERMS_DOCUMENT =
    getFunctionSelectorFromString("getTermsDocument(string,uint256)");
const uint32_t FunctionSelector::XAPIAN_V1_INDEX_TEXT_DOCUMENT =
    getFunctionSelectorFromString(
        "indexTextForDocument(string,uint256,string,uint8,string)");
const uint32_t FunctionSelector::XAPIAN_V1_QUERY_SEARCH =
    getFunctionSelectorFromString(
        "querySearch(string,(string,(string,string)[],string[],uint64,uint64,"
        "int64,bool,(uint256,string,string)[]))");
const uint32_t FunctionSelector::XAPIAN_V1_COMMIT =
    getFunctionSelectorFromString("commit(string)");

// ecRecover
const uint32_t FunctionSelector::PRECOMPILED_CONTRACT_ECRECOVER =
    getFunctionSelectorFromString("ecrecover(bytes32,uint8,bytes32,bytes32)");

// wallet
const uint256_t FunctionSelector::ADDRESS_PFP =
    getPaddedAddressSelector("wallet v1");
const uint32_t FunctionSelector::ESCP_PFP =
    getFunctionSelectorFromString("getPublicKeyFromPrivate(bytes32)");

const uint32_t FunctionSelector::XAPIAN_V1_GET_FILED_DETAILS =
    getFunctionSelectorFromString("getFieldDetails(string,string)");
const uint32_t FunctionSelector::XAPIAN_V1_GET_FIELD_BY_NAME =
    getFunctionSelectorFromString("getFieldByName(string,string)");
const uint32_t FunctionSelector::XAPIAN_V1_ADD_FIELD_TO_DOCUMENT =
    getFunctionSelectorFromString("addFieldToDocument(uint256,string,string)");
const uint32_t FunctionSelector::XAPIAN_V1_REMOVE_FIELD =
    getFunctionSelectorFromString("removeField(string,string)");
const uint32_t FunctionSelector::XAPIAN_V1_GET_TAG =
    getFunctionSelectorFromString("getTag(string,string,string)");
const uint32_t FunctionSelector::XAPIAN_V1_ADD_TAG =
    getFunctionSelectorFromString("addTag(string,string,string)");
const uint32_t FunctionSelector::XAPIAN_V1_SEARCH =
    getFunctionSelectorFromString("search(string,string)");
const uint32_t FunctionSelector::XAPIAN_V1_REMOVE_TAG =
    getFunctionSelectorFromString("removeTag(string,string,string)");
const uint32_t FunctionSelector::XAPIAN_V1_INDEX_DOCUMENT =
    getFunctionSelectorFromString("indexDocument(string,string[])");
const uint32_t FunctionSelector::XAPIAN_V1_GET_ALL_TAGS =
    getFunctionSelectorFromString("getAllTags(string,string)");
const uint32_t FunctionSelector::XAPIAN_V1_GET_DOCUMENT =
    getFunctionSelectorFromString("getDocument(string,uint256)");
const uint32_t FunctionSelector::XAPIAN_V1_GET_ALL_FIELDS =
    getFunctionSelectorFromString("getAllFields(string)");
const uint32_t FunctionSelector::XAPIAN_V1_UPDATE_TAG =
    getFunctionSelectorFromString("updateTag(string,string,string,string)");
const uint32_t FunctionSelector::XAPIAN_V1_ADD_FIELD =
    getFunctionSelectorFromString("addField(string,string)");
const uint32_t FunctionSelector::XAPIAN_V1_SET_CONFIG_FILED =
    getFunctionSelectorFromString("setConfigField(string,string,string)");
const uint32_t FunctionSelector::XAPIAN_V1_UPDATE_FIELD =
    getFunctionSelectorFromString(
        "updateField(string,string,uint16,uint8,bool,bool,uint8)");
} // namespace mvm
