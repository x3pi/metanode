#pragma once

#include <stdbool.h>
#include <stddef.h> // Cho size_t (mặc dù không trực tiếp dùng ở đây)

// Struct này phải khớp HOÀN TOÀN với struct trong khối CGO của Go
typedef struct {
  unsigned char *address_ptr;
  int address_len; // Sẽ là 20
  unsigned char *log_data_ptr;
  int log_data_len;
} LogReplayEntryC;

#ifdef __cplusplus
extern "C" {
#endif
struct ExecuteResult {
  char b_exitReason;
  char b_exception;
  char *b_exmsg;
  int length_exmsg;

  char *b_output;
  int length_output;

  // *** Sửa đổi full_db_hash ***
  char **full_db_hash;     // Mảng các con trỏ (mỗi con trỏ tới Address + Hash)
  int length_full_db_hash; // Số lượng cặp (Address, Hash)
  int *length_full_db_hashes; // Mảng chứa độ dài của mỗi cặp (sẽ là 64)

  // Logs (Thêm mới)
  char *
      *full_db_logs; // Mảng con trỏ (mỗi con trỏ tới Address + Serialized Log)
  int length_full_db_logs;       // Số lượng cặp (Address, Log)
  int *length_full_db_logs_data; // Mảng chứa độ dài của mỗi cặp (32 +
                                 // serialized log size)

  char **b_add_balance_change;
  int length_add_balance_change;

  char **b_nonce_change;
  int length_nonce_change;

  char **b_sub_balance_change;
  int length_sub_balance_change;

  char **b_code_change;
  int length_code_change;
  int *length_codes;

  char **b_storage_change;
  int length_storage_change;
  int *length_storages;

  char *b_logs;
  int length_logs;

  unsigned long long gas_used;
};

struct ExecuteResult *deploy(
    // transaction data
    unsigned char *b_caller_address, unsigned char *b_contract_constructor,
    int contract_constructor_length, unsigned char *b_amount,
    unsigned long long gas_price, unsigned long long gas_limit,
    // block context data
    unsigned long long block_prevrandao, unsigned long long block_gas_limit,
    unsigned long long block_time, unsigned long long block_base_fee,
    unsigned char *b_block_number, unsigned char *b_block_coinbase,
    unsigned char *mvmId, unsigned char *b_tx_hash, bool is_debug,
    bool is_cache, bool is_off_chain);

struct ExecuteResult *call(
    // transaction data
    unsigned char *b_caller_address, unsigned char *b_contract_address,
    unsigned char *b_input, int length_input, unsigned char *b_amount,
    unsigned long long gas_price, unsigned long long gas_limit,
    // block context data
    unsigned long long block_prevrandao, unsigned long long block_gas_limit,
    unsigned long long block_time, unsigned long long block_base_fee,
    unsigned char *b_block_number, unsigned char *b_block_coinbase,
    unsigned char *mvmId, bool readOnly, unsigned char *b_tx_hash,
    bool is_debug,
    unsigned char
        *b_related_addresses,    // Flatten array: addr1(20) + addr2(20) + ...
    int related_addresses_count, // Số lượng addresses
    bool is_off_chain);

struct ExecuteResult *execute(
    // transaction data
    unsigned char *b_caller_address, unsigned char *b_contract_address,
    unsigned char *b_input, int length_input, unsigned char *b_amount,
    unsigned long long gas_price, unsigned long long gas_limit,
    // block context data
    unsigned long long block_prevrandao, unsigned long long block_gas_limit,
    unsigned long long block_time, unsigned long long block_base_fee,
    unsigned char *b_block_number, unsigned char *b_block_coinbase,
    unsigned char *mvmId, unsigned char *b_tx_hash, bool is_debug,
    unsigned char
        *b_related_addresses,   // Flatten array: addr1(20) + addr2(20) + ...
    int related_addresses_count // Số lượng addresses
);

typedef struct {
  unsigned char *b_caller_address;
  unsigned char *b_contract_address;
  unsigned char *b_input;
  int length_input;
  unsigned char *b_amount;
  unsigned long long gas_price;
  unsigned long long gas_limit;
  unsigned char *b_tx_hash;
  bool is_debug;
  unsigned char *b_related_addresses;
  int related_addresses_count;
} ExecuteBatchInputC;

typedef struct {
  struct ExecuteResult **results;
  int num_results;
} ExecuteBatchResultC;

ExecuteBatchResultC *
executeBatch(ExecuteBatchInputC *inputs, int num_inputs,
             // block context data
             unsigned long long block_prevrandao,
             unsigned long long block_gas_limit, unsigned long long block_time,
             unsigned long long block_base_fee, unsigned char *b_block_number,
             unsigned char *b_block_coinbase, unsigned char *mvmId);
struct ExecuteResult *processNativeMintBurn(
    unsigned char *b_from, unsigned char *b_to, unsigned char *b_amount,
    unsigned long long operation_type, // 0: mint, 1: burn
    unsigned long long gas_price, unsigned long long gas_limit,
    unsigned long long block_prevrandao, unsigned long long block_gas_limit,
    unsigned long long block_time, unsigned long long block_base_fee,
    unsigned char *b_block_number, unsigned char *b_block_coinbase,
    unsigned char *mvmId);
struct ExecuteResult *
sendNative(unsigned char *b_from, unsigned char *b_to, unsigned char *b_amount,
           unsigned long long gas_price, unsigned long long gas_limit,
           unsigned long long block_prevrandao,
           unsigned long long block_gas_limit, unsigned long long block_time,
           unsigned long long block_base_fee, unsigned char *b_block_number,
           unsigned char *b_block_coinbase, unsigned char *mvmId);

struct ExecuteResult *
noncePlusOne(unsigned char *b_from, unsigned long long gas_price,
             unsigned long long gas_limit, unsigned long long block_prevrandao,
             unsigned long long block_gas_limit, unsigned long long block_time,
             unsigned long long block_base_fee, unsigned char *b_block_number,
             unsigned char *b_block_coinbase, unsigned char *mvmId);

extern int commit_full_db(unsigned char *mvmId);
extern int revert_full_db(unsigned char *mvmId);
extern int ReplayFullDbLogs(LogReplayEntryC *entries, int num_entries);

struct Extension_return {
  unsigned char *data_p;
  int data_size;
};

struct Value_return {
  unsigned char *data_p;
  int data_size;
  bool success;
};

void freeResult(struct ExecuteResult *);

void freeBatchResult(ExecuteBatchResultC *);

void freePendingResult();

extern struct GlobalStateGet_return GlobalStateGet(unsigned char *mvmId,
                                                   unsigned char *);
extern struct GetStorageValue_return
GetStorageValue(unsigned char *mvmId, unsigned char *, unsigned char *);
extern void ClearProcessingPointers(unsigned char *mvmId);
extern struct Extension_return ExtensionCallGetApi(unsigned char *, int);
extern struct Extension_return ExtensionExtractJsonField(unsigned char *, int);
extern struct Extension_return ExtensionBlst(unsigned char *, int);
extern struct Extension_return ExtensionGetOrCreateSimpleDb(unsigned char *,
                                                            int,
                                                            unsigned char *,
                                                            unsigned char *);
extern struct Value_return GetBlockHash(int);
extern struct Value_return GetChainId();
extern struct Value_return GetCrossChainSender(unsigned char *mvmId);
extern struct Value_return GetCrossChainSourceId(unsigned char *mvmId);

// Redirect C++ cout/cerr sang file log riêng
// name: tên process (ví dụ "master", "sub-write") → tạo file mvm_cpp_{name}.log
void InitCppFileLog(const char *log_dir, const char *name);
void CloseCppFileLog();

extern void SetXapianBasePath(const char *path);

extern void GoLogString(int, char *);
extern void GoLogBytes(int, unsigned char *, int);

struct ExecuteResult *testMemLeak();
void testMemLeakGS(int total_address, unsigned char *b_addresses);
// void free_global_state(unsigned char *mvmId);
void clearAllStateInstances();
void updateStateNonce(unsigned char *b_address, unsigned long long nonce);

#ifdef __cplusplus
}
#endif
