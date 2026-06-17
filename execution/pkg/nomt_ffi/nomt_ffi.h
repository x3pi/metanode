/*
 * Auto-generated C header for mtn-nomt-ffi
 * NOMT (Nearly Optimal Merkle Trie) FFI bridge
 */

#ifndef MTN_NOMT_FFI_H
#define MTN_NOMT_FFI_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Opaque handles */
typedef struct NomtHandle NomtHandle;
typedef struct SessionHandle SessionHandle;
typedef struct FinishedSessionHandle FinishedSessionHandle;

/* Database lifecycle */
NomtHandle* nomt_open(const char* path, int commit_concurrency, int page_cache_mb, int leaf_cache_mb, int hashtable_buckets, int preallocate_ht);
void nomt_close(NomtHandle* handle);
int nomt_root(const NomtHandle* handle, uint8_t* root_out);

/* Read operations (thread-safe, can be called concurrently) */
int nomt_read(const NomtHandle* handle, const uint8_t* key, uint8_t* val_out, size_t val_max_len, size_t* val_actual_len);

/* Write session (single-writer, batch commit) */
SessionHandle* nomt_session_begin(NomtHandle* handle);
int nomt_session_warm_up(SessionHandle* session, const uint8_t* key);
int nomt_session_record_read(SessionHandle* session, const uint8_t* key, const uint8_t* val, size_t val_len);
int nomt_session_batch_record_read(SessionHandle* session, const uint8_t* keys, const uint8_t* vals, const size_t* val_lens, size_t count);
int nomt_session_write(SessionHandle* session, const uint8_t* key, const uint8_t* val, size_t val_len);
int nomt_session_batch_write(SessionHandle* session, const uint8_t* keys, const uint8_t* vals, const size_t* val_lens, size_t count);
int nomt_session_commit(NomtHandle* handle, SessionHandle* session, uint8_t* new_root_out);
FinishedSessionHandle* nomt_session_finish(NomtHandle* handle, SessionHandle* session, uint8_t* new_root_out);
int nomt_commit_payload(NomtHandle* handle, FinishedSessionHandle* finished_session);
void nomt_session_abort(SessionHandle* session);
void nomt_finished_session_abort(FinishedSessionHandle* finished_session);

/* Checkpoint: copy database files to dest without close/reopen */
int nomt_checkpoint(const NomtHandle* handle, const char* src_path, const char* dest_path);

#ifdef __cplusplus
}
#endif

/* Proof Generation */
int nomt_generate_proof(const NomtHandle* handle, const uint8_t* key, uint8_t** proof_out, size_t* proof_len);
void nomt_free_proof(uint8_t* proof_ptr, size_t proof_len);

/* Unified State DB FFI APIs (Simplified Bridge) */
typedef struct RustStateDBHandle RustStateDBHandle;

RustStateDBHandle* state_db_open(const char* path, int commit_concurrency, int page_cache_mb, int leaf_cache_mb, int hashtable_buckets, int preallocate_ht);
void state_db_close(RustStateDBHandle* handle);
int state_db_root(const RustStateDBHandle* handle, uint8_t* root_out);
int state_db_get(const RustStateDBHandle* handle, const uint8_t* key, uint8_t* val_out, size_t val_max_len, size_t* val_actual_len);
int state_db_commit(RustStateDBHandle* handle, const uint8_t* write_keys, const uint8_t* write_vals, const size_t* write_val_lens, size_t write_count, const uint8_t* read_keys, const uint8_t* read_vals, const size_t* read_val_lens, size_t read_count, uint8_t* root_out);
int state_db_checkpoint(const RustStateDBHandle* handle, const char* dest_path);
int state_db_prune(RustStateDBHandle* handle, uint64_t old_epoch);

#endif /* MTN_NOMT_FFI_H */
