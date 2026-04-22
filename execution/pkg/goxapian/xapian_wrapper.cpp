#include "xapian.h"
#include "xapian_wrapper.h"
#include <vector>
#include <string.h>
#include <sstream>
#include <iostream>

extern "C"
{

    // === Database ===
    // ... (Giữ nguyên các hàm database)
    xapian_database_t database_new_writable(const char *path, int flags)
    {
        try
        {
            return new Xapian::WritableDatabase(std::string(path), flags);
        }
        catch (const Xapian::Error &e)
        {
            return nullptr;
        }
    }

    void database_close(xapian_database_t db)
    {
        if (db)
            delete reinterpret_cast<Xapian::Database *>(db);
    }

    unsigned int database_get_doccount(xapian_database_t db)
    {
        if (!db)
            return 0;
        try
        {
            return reinterpret_cast<Xapian::Database *>(db)->get_doccount();
        }
        catch (const Xapian::Error &e)
        {
            return 0;
        }
    }

    xapian_docid_t database_add_document(xapian_database_t db, xapian_document_t doc)
    {
        if (!db || !doc)
            return 0;
        try
        {
            return reinterpret_cast<Xapian::WritableDatabase *>(db)->add_document(*reinterpret_cast<Xapian::Document *>(doc));
        }
        catch (const Xapian::Error &e)
        {
            return 0;
        }
    }

    void database_replace_document_by_term(xapian_database_t db, const char *unique_term, xapian_document_t doc)
    {
        if (!db || !unique_term || !doc)
            return;
        try
        {
            Xapian::WritableDatabase *wdb = reinterpret_cast<Xapian::WritableDatabase *>(db);
            std::string term_str(unique_term);
            Xapian::Document *doc_ptr = reinterpret_cast<Xapian::Document *>(doc);
            wdb->replace_document(term_str, *doc_ptr);
        }
        catch (const Xapian::Error &e)
        {
            // Handle error
        }
    }

    void database_commit(xapian_database_t db)
    {
        if (!db)
            return;
        try
        {
            reinterpret_cast<Xapian::WritableDatabase *>(db)->commit();
        }
        catch (const Xapian::Error &e)
        {
        }
    }

    const char *database_dump_all_docs(xapian_database_t db_ptr)
    {
        if (!db_ptr)
            return strdup("Lỗi: Database pointer là null.");
        Xapian::Database *db = reinterpret_cast<Xapian::Database *>(db_ptr);
        try
        {
            if (db->get_doccount() == 0)
            {
                return strdup("[Database trống - không có document nào]");
            }
            std::stringstream ss;
            Xapian::PostingIterator it = db->postlist_begin("");
            Xapian::PostingIterator end = db->postlist_end("");

            while (it != end)
            {
                Xapian::docid did = *it;
                try
                {
                    Xapian::Document doc = db->get_document(did);
                    ss << "--- DocID: " << did << " ---\n";
                    ss << doc.get_data() << "\n";
                    ss << "Terms: [ ";
                    for (Xapian::TermIterator tit = doc.termlist_begin(); tit != doc.termlist_end(); ++tit)
                    {
                        ss << *tit << " ";
                    }
                    ss << "]\n\n";
                }
                catch (const Xapian::Error &e)
                {
                    ss << "--- DocID: " << did << " --- [ERROR: " << e.get_msg() << "]\n\n";
                }
                ++it;
            }
            std::cout << ss.str(); // <--- DÒNG ĐƯỢC THÊM VÀO
            return strdup(ss.str().c_str());
        }
        catch (const Xapian::Error &e)
        {
            return strdup(e.get_msg().c_str());
        }
        return strdup("");
    }

    // === Document ===
    // ... (Giữ nguyên các hàm document)
    xapian_document_t document_new() { return new Xapian::Document(); }
    void document_free(xapian_document_t doc) { delete reinterpret_cast<Xapian::Document *>(doc); }

    void document_set_data(xapian_document_t doc, const char *data)
    {
        if (doc)
            reinterpret_cast<Xapian::Document *>(doc)->set_data(std::string(data));
    }

    void document_add_term(xapian_document_t doc, const char *term)
    {
        if (doc)
            reinterpret_cast<Xapian::Document *>(doc)->add_term(std::string(term));
    }

    const char *document_get_data(xapian_document_t doc)
    {
        if (!doc)
            return strdup("");
        try
        {
            std::string data = reinterpret_cast<Xapian::Document *>(doc)->get_data();
            return strdup(data.c_str());
        }
        catch (const Xapian::Error &e)
        {
            return strdup("");
        }
    }

    // === QueryParser ===
    xapian_queryparser_t queryparser_new() { return new Xapian::QueryParser(); }
    void queryparser_free(xapian_queryparser_t qp) { delete reinterpret_cast<Xapian::QueryParser *>(qp); }

    void queryparser_set_database(xapian_queryparser_t qp, xapian_database_t db)
    {
        if (qp && db)
            reinterpret_cast<Xapian::QueryParser *>(qp)->set_database(*reinterpret_cast<Xapian::Database *>(db));
    }

    void queryparser_set_stemming_language(xapian_queryparser_t qp, const char *lang)
    {
        if (qp)
        {
            try
            {
                reinterpret_cast<Xapian::QueryParser *>(qp)->set_stemmer(Xapian::Stem(std::string(lang)));
            }
            catch (const Xapian::Error &e)
            {
            }
        }
    }

    void queryparser_set_default_op(xapian_queryparser_t qp, xapian_query_op op)
    {
        if (qp)
        {
            reinterpret_cast<Xapian::QueryParser *>(qp)->set_default_op(static_cast<Xapian::Query::op>(op));
        }
    }

    void queryparser_add_prefix(xapian_queryparser_t qp, const char *field, const char *prefix)
    {
        if (qp && field && prefix)
        {
            try
            {
                reinterpret_cast<Xapian::QueryParser *>(qp)->add_prefix(std::string(field), std::string(prefix));
            }
            catch (const Xapian::Error &e)
            {
                // Có thể thêm xử lý lỗi ở đây
            }
        }
    }

    // **ĐÃ XÓA**: Loại bỏ hàm triển khai không chính xác
    // void queryparser_add_feature(...) { ... }

    // **ĐÃ SỬA**: Triển khai hàm parse_query với tham số flags
    xapian_query_t queryparser_parse_query(xapian_queryparser_t qp, const char *query_string, unsigned int flags)
    {
        if (!qp)
            return nullptr;
        try
        {
            Xapian::QueryParser *parser = reinterpret_cast<Xapian::QueryParser *>(qp);
            std::string query_str(query_string);
            Xapian::Query q;
    
            // **SỬA LỖI**: Kiểm tra nếu chuỗi truy vấn rỗng
            if (query_str.empty())
            {
                // Tạo một truy vấn "match-all" để lấy tất cả tài liệu
                q = Xapian::Query("");
            }
            else
            {
                // Nếu không, phân tích chuỗi truy vấn như bình thường
                q = parser->parse_query(query_str, flags);
            }
    
            return new Xapian::Query(q);
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "QueryParser Error: " << e.get_msg() << std::endl;
            return nullptr;
        }
    }

    // === Query ===
    // ... (Giữ nguyên)
    void query_free(xapian_query_t query) { delete reinterpret_cast<Xapian::Query *>(query); }

    // === Enquire ===
    // ... (Giữ nguyên)
    xapian_enquire_t enquire_new(xapian_database_t db)
    {
        if (!db)
            return nullptr;
        return new Xapian::Enquire(*reinterpret_cast<Xapian::Database *>(db));
    }
    void enquire_free(xapian_enquire_t enq) { delete reinterpret_cast<Xapian::Enquire *>(enq); }

    void enquire_set_query(xapian_enquire_t enq, xapian_query_t query)
    {
        if (enq && query)
            reinterpret_cast<Xapian::Enquire *>(enq)->set_query(*reinterpret_cast<Xapian::Query *>(query));
    }

    xapian_mset_t enquire_get_mset(xapian_enquire_t enq, unsigned int first, unsigned int maxitems)
    {
        if (!enq)
            return nullptr;
        try
        {
            return new Xapian::MSet(reinterpret_cast<Xapian::Enquire *>(enq)->get_mset(first, maxitems));
        }
        catch (const Xapian::Error &e)
        {
            return nullptr;
        }
    }

    // === MSet ===
    // ... (Giữ nguyên)
    void mset_free(xapian_mset_t mset) { delete reinterpret_cast<Xapian::MSet *>(mset); }
    int mset_get_size(xapian_mset_t mset)
    {
        if (!mset)
            return 0;
        return reinterpret_cast<Xapian::MSet *>(mset)->size();
    }
    void *mset_get_document(xapian_mset_t mset, unsigned int index)
    {
        Xapian::MSet *m = reinterpret_cast<Xapian::MSet *>(mset);
        if (index >= m->size())
            return nullptr;
        try
        {
            Xapian::MSetIterator it = m->begin();
            std::advance(it, index);
            return new Xapian::Document(it.get_document());
        }
        catch (const Xapian::Error &e)
        {
            return nullptr;
        }
    }

    int mset_get_rank(xapian_mset_t mset, unsigned int index)
    {
        Xapian::MSet *m = reinterpret_cast<Xapian::MSet *>(mset);
        if (!m || index >= m->size())
        {
            return -1;
        }
        try
        {
            Xapian::MSetIterator it = m->begin();
            std::advance(it, index);
            return index;
        }
        catch (const Xapian::Error &e)
        {
            return -1;
        }
    }

    unsigned int mset_get_matches_estimated(xapian_mset_t mset)
    {
        if (!mset)
            return 0;
        try
        {
            return reinterpret_cast<Xapian::MSet *>(mset)->get_matches_estimated();
        }
        catch (const Xapian::Error &e)
        {
            return 0;
        }
    }

} // extern "C"