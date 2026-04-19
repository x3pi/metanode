#ifndef XAPIAN_WRAPPER_H
#define XAPIAN_WRAPPER_H

#ifdef __cplusplus
extern "C" {
#endif

// Opaque pointers
typedef void* xapian_database_t;
typedef void* xapian_document_t;
typedef void* xapian_queryparser_t;
typedef void* xapian_query_t;
typedef void* xapian_enquire_t;
typedef void* xapian_mset_t;
typedef unsigned int xapian_docid_t;
typedef int xapian_query_op;

// === Database Functions ===
// ... (Giữ nguyên các hàm database)
xapian_database_t database_new_writable(const char* path, int flags);
void database_close(xapian_database_t db);
unsigned int database_get_doccount(xapian_database_t db);
xapian_docid_t database_add_document(xapian_database_t db, xapian_document_t doc);
void database_commit(xapian_database_t db);
void database_replace_document_by_term(xapian_database_t db, const char* unique_term, xapian_document_t doc);
const char* database_dump_all_docs(xapian_database_t db);

// === Document Functions ===
// ... (Giữ nguyên các hàm document)
xapian_document_t document_new();
void document_free(xapian_document_t doc);
void document_set_data(xapian_document_t doc, const char* data);
void document_add_term(xapian_document_t doc, const char* term);
const char* document_get_data(xapian_document_t doc);


// === QueryParser Functions ===
xapian_queryparser_t queryparser_new();
void queryparser_free(xapian_queryparser_t qp);
void queryparser_set_database(xapian_queryparser_t qp, xapian_database_t db);
void queryparser_set_stemming_language(xapian_queryparser_t qp, const char* lang);
void queryparser_set_default_op(xapian_queryparser_t qp, xapian_query_op op);

void queryparser_add_prefix(xapian_queryparser_t qp, const char* field, const char* prefix);
xapian_query_t queryparser_parse_query(xapian_queryparser_t qp, const char* query_string, unsigned int flags);

// **ĐÃ SỬA**: Thay đổi hàm parse_query để nhận cờ tính năng
xapian_query_t queryparser_parse_query(xapian_queryparser_t qp, const char* query_string, unsigned int flags);

// **ĐÃ XÓA**: Loại bỏ hàm không chính xác
// void queryparser_add_feature(xapian_queryparser_t qp, xapian_queryparser_feature feature);


// === Query Functions ===
void query_free(xapian_query_t query);

// === Enquire Functions ===
// ... (Giữ nguyên các hàm enquire)
xapian_enquire_t enquire_new(xapian_database_t db);
void enquire_free(xapian_enquire_t enq);
void enquire_set_query(xapian_enquire_t enq, xapian_query_t query);
xapian_mset_t enquire_get_mset(xapian_enquire_t enq, unsigned int first, unsigned int maxitems);

// === MSet Functions ===
// ... (Giữ nguyên các hàm MSet)
void mset_free(xapian_mset_t mset);
int mset_get_size(xapian_mset_t mset);
xapian_document_t mset_get_document(xapian_mset_t mset, unsigned int index);
int mset_get_rank(xapian_mset_t mset, unsigned int index);
unsigned int mset_get_matches_estimated(xapian_mset_t mset);



#ifdef __cplusplus
}
#endif

#endif // XAPIAN_WRAPPER_H