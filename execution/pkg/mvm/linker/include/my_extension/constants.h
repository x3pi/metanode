#pragma once

#include <cstdint>
#include <mvm/util.h>
#include <string>
#include <unordered_map>

#define WORD_SIZE 32
#define SCALE_FACTOR 1e18

namespace mvm {
class FunctionSelector {
public:
  static uint32_t
  getFunctionSelectorFromString(const std::string &functionSignature);
  static uint256_t
  getPaddedAddressSelector(const std::string &functionSignature);

  static const uint32_t ADD;
  static const uint32_t SUB;
  static const uint32_t MUL;
  static const uint32_t DIV;
  static const uint32_t POW;
  static const uint32_t ROOT;
  static const uint32_t ATAN2;
  static const uint32_t ABS;
  static const uint32_t ACOS;
  static const uint32_t SIN;
  static const uint32_t ASIN;
  static const uint32_t ATAN;
  static const uint32_t CEIL;
  static const uint32_t COS;
  static const uint32_t COT;
  static const uint32_t EXP;
  static const uint32_t LOG;
  static const uint32_t LOG10;
  static const uint32_t LOG2;
  static const uint32_t SQRT;
  static const uint32_t TAN;
  static const uint32_t ENCODE_MPFR;
  static const uint32_t PI;
  static const uint32_t CEIL_MPFR;
  static const uint32_t COSH;
  static const uint32_t CSC;
  static const uint32_t EXP2;
  static const uint32_t FLOOR;
  static const uint32_t ROUND;
  static const uint32_t SEC;
  static const uint32_t SIGN;
  static const uint32_t SINH;
  static const uint32_t TANH;
  static const uint32_t MOD;
  static const uint32_t GCD;
  static const uint32_t LCM;

  static const uint32_t GET_OR_CREATE_SIMPLE_DB;
  static const uint32_t GET;
  static const uint32_t SET;
  static const uint32_t GET_ALL;
  static const uint32_t SEARCH_BY_VALUE;

  static const uint32_t XAPIAN_GET_OR_CREATE_DB;
  static const uint32_t XAPIAN_NEW_DOCUMENT;
  static const uint32_t XAPIAN_GET_DOCUMENT;
  static const uint32_t XAPIAN_DELETE_DOCUMENT;
  static const uint32_t XAPIAN_ADD_FIELD_TO_DOCUMENT;
  static const uint32_t XAPIAN_ADD_DATA_DOCUMENT;
  static const uint32_t XAPIAN_ADD_VALUE_DOCUMENT;
  static const uint32_t XAPIAN_ADD_TERM_DOCUMENT;
  static const uint32_t XAPIAN_GET_DATA_DOCUMENT;
  static const uint32_t XAPIAN_GET_VALUE_DOCUMENT;
  static const uint32_t XAPIAN_GET_TERMS_DOCUMENT;
  static const uint32_t XAPIAN_SET_DATA_DOCUMENT;
  static const uint32_t XAPIAN_SET_CONFIG_FILED;
  static const uint32_t XAPIAN_GET_FILED_DETAILS;
  static const uint32_t XAPIAN_INDEX_DOCUMENT;
  static const uint32_t XAPIAN_SEARCH;
  static const uint32_t XAPIAN_ADD_FIELD;
  static const uint32_t XAPIAN_GET_ALL_FIELDS;
  static const uint32_t XAPIAN_GET_FIELD_BY_NAME;
  static const uint32_t XAPIAN_REMOVE_FIELD;
  static const uint32_t XAPIAN_UPDATE_FIELD;
  static const uint32_t XAPIAN_ADD_TAG;
  static const uint32_t XAPIAN_REMOVE_TAG;
  static const uint32_t XAPIAN_UPDATE_TAG;
  static const uint32_t XAPIAN_GET_ALL_TAGS;
  static const uint32_t XAPIAN_INDEX_TEXT_DOCUMENT;
  static const uint32_t XAPIAN_GET_TAG;
  static const uint32_t XAPIAN_QUERY_SEARCH;
  static const uint32_t XAPIAN_COMMIT;

  // V1 (Bytes native) equivalents
  static const uint32_t XAPIAN_V1_NEW_DOCUMENT;
  static const uint32_t XAPIAN_V1_SET_DATA_DOCUMENT;
  static const uint32_t XAPIAN_V1_GET_DATA_DOCUMENT;
  static const uint32_t XAPIAN_V1_GET_OR_CREATE_DB;
  static const uint32_t XAPIAN_V1_DELETE_DOCUMENT;
  static const uint32_t XAPIAN_V1_ADD_VALUE_DOCUMENT;
  static const uint32_t XAPIAN_V1_ADD_TERM_DOCUMENT;
  static const uint32_t XAPIAN_V1_GET_VALUE_DOCUMENT;
  static const uint32_t XAPIAN_V1_GET_TERMS_DOCUMENT;
  static const uint32_t XAPIAN_V1_INDEX_TEXT_DOCUMENT;
  static const uint32_t XAPIAN_V1_QUERY_SEARCH;
  static const uint32_t XAPIAN_V1_COMMIT;

  static const uint32_t SINPLE_DB_DELETE;
  static const uint32_t SINPLE_GET_NEXT_KEYS;
  static const uint32_t PRECOMPILED_CONTRACT_ECRECOVER;

  static const uint256_t ADDRESS_PFP;
  static const uint32_t ESCP_PFP;
  static const uint32_t XAPIAN_V1_REMOVE_TAG;
  static const uint32_t XAPIAN_V1_UPDATE_TAG;
  static const uint32_t XAPIAN_V1_ADD_FIELD;
  static const uint32_t XAPIAN_V1_REMOVE_FIELD;
  static const uint32_t XAPIAN_V1_ADD_TAG;
  static const uint32_t XAPIAN_V1_GET_FIELD_BY_NAME;
  static const uint32_t XAPIAN_V1_GET_DOCUMENT;
  static const uint32_t XAPIAN_V1_GET_ALL_FIELDS;
  static const uint32_t XAPIAN_V1_UPDATE_FIELD;
  static const uint32_t XAPIAN_V1_GET_FILED_DETAILS;
  static const uint32_t XAPIAN_V1_ADD_FIELD_TO_DOCUMENT;
  static const uint32_t XAPIAN_V1_GET_ALL_TAGS;
  static const uint32_t XAPIAN_V1_GET_TAG;
  static const uint32_t XAPIAN_V1_ADD_DATA_DOCUMENT;
  static const uint32_t XAPIAN_V1_SET_CONFIG_FILED;
  static const uint32_t XAPIAN_V1_SEARCH;
  static const uint32_t XAPIAN_V1_INDEX_DOCUMENT;

private:
  static std::unordered_map<std::string, uint32_t> functionSelectorCache;
};
} // namespace mvm
