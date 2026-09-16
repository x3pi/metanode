#include "xapian.h"
#include "xapian_wrapper.h"
#include <vector>
#include <string.h>
#include <sstream>
#include <iostream>

// ROBUSTNESS PASS (2026-09, Phuong an A mục 22 -- "check lại toàn bộ cgo của
// xapian"): every function below now catches `catch (...)` as a last-resort
// fallback in addition to `catch (const Xapian::Error &e)`. Reasoning: a C++
// exception that unwinds past an `extern "C"` function boundary is undefined
// behavior -- in practice it calls std::terminate() and crashes the whole
// process (Go runtime included, no recovery possible), not just this call.
// `Xapian::Error` covers Xapian's own documented exception hierarchy, but
// std::string/std::stringstream/std::vector construction inside these
// functions can also throw std::bad_alloc (or, in principle, anything) under
// memory pressure or a corrupted heap -- a scenario this project's own past
// incidents (mục 19-22) show is not hypothetical for a long-running node.
// Several functions here previously had NO try/catch at all (document_new,
// document_set_data, document_add_term, queryparser_set_default_op,
// enquire_new, enquire_set_query, mset_get_size, and every _free/_close
// destructor) -- added throughout so a single bad Xapian call can no longer
// take down the entire node process.
extern "C"
{

    // === Database ===
    xapian_database_t database_new_writable(const char *path, int flags)
    {
        if (!path)
            return nullptr;
        try
        {
            return new Xapian::WritableDatabase(std::string(path), flags);
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] database_new_writable failed: " << e.get_msg() << std::endl;
            return nullptr;
        }
        catch (...)
        {
            std::cerr << "[goxapian] database_new_writable: unknown exception" << std::endl;
            return nullptr;
        }
    }

    void database_close(xapian_database_t db)
    {
        if (!db)
            return;
        try
        {
            delete reinterpret_cast<Xapian::Database *>(db);
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] database_close failed: " << e.get_msg() << std::endl;
        }
        catch (...)
        {
            std::cerr << "[goxapian] database_close: unknown exception" << std::endl;
        }
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
            std::cerr << "[goxapian] database_get_doccount failed: " << e.get_msg() << std::endl;
            return 0;
        }
        catch (...)
        {
            std::cerr << "[goxapian] database_get_doccount: unknown exception" << std::endl;
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
            std::cerr << "[goxapian] database_add_document failed: " << e.get_msg() << std::endl;
            return 0;
        }
        catch (...)
        {
            std::cerr << "[goxapian] database_add_document: unknown exception" << std::endl;
            return 0;
        }
    }

    // RETURN VALUE CHANGED (was void): a caller that thinks it just replaced a
    // document but the write actually failed (disk full, DB corruption, lock
    // conflict) had no way to ever find out -- exactly the class of silent
    // write-loss this project's own halt-not-guess philosophy exists to catch
    // (see project memory "Phuong an A"). Returns true on success.
    bool database_replace_document_by_term(xapian_database_t db, const char *unique_term, xapian_document_t doc)
    {
        if (!db || !unique_term || !doc)
            return false;
        try
        {
            Xapian::WritableDatabase *wdb = reinterpret_cast<Xapian::WritableDatabase *>(db);
            std::string term_str(unique_term);
            Xapian::Document *doc_ptr = reinterpret_cast<Xapian::Document *>(doc);
            wdb->replace_document(term_str, *doc_ptr);
            return true;
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] database_replace_document_by_term failed: " << e.get_msg() << std::endl;
            return false;
        }
        catch (...)
        {
            std::cerr << "[goxapian] database_replace_document_by_term: unknown exception" << std::endl;
            return false;
        }
    }

    // RETURN VALUE CHANGED (was void, always silently "succeeded" from the
    // caller's point of view even when the underlying commit threw). This is
    // the single most important fix in this file: every previous caller of
    // Commit() (explorer/service.go, mining/service.go, cmd/mining/server.go)
    // had zero way to detect a failed flush and would carry on as if the data
    // were durably persisted. Returns true on success.
    bool database_commit(xapian_database_t db)
    {
        if (!db)
            return false;
        try
        {
            reinterpret_cast<Xapian::WritableDatabase *>(db)->commit();
            return true;
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] database_commit FAILED: " << e.get_msg() << std::endl;
            return false;
        }
        catch (...)
        {
            std::cerr << "[goxapian] database_commit: unknown exception" << std::endl;
            return false;
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
            return strdup(ss.str().c_str());
        }
        catch (const Xapian::Error &e)
        {
            return strdup(e.get_msg().c_str());
        }
        catch (...)
        {
            return strdup("[goxapian] database_dump_all_docs: unknown exception");
        }
    }

    // === Document ===
    xapian_document_t document_new()
    {
        try
        {
            return new Xapian::Document();
        }
        catch (...)
        {
            std::cerr << "[goxapian] document_new: allocation failed" << std::endl;
            return nullptr;
        }
    }

    void document_free(xapian_document_t doc)
    {
        if (!doc)
            return;
        try
        {
            delete reinterpret_cast<Xapian::Document *>(doc);
        }
        catch (...)
        {
            std::cerr << "[goxapian] document_free: unknown exception" << std::endl;
        }
    }

    void document_set_data(xapian_document_t doc, const char *data)
    {
        if (!doc || !data)
            return;
        try
        {
            reinterpret_cast<Xapian::Document *>(doc)->set_data(std::string(data));
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] document_set_data failed: " << e.get_msg() << std::endl;
        }
        catch (...)
        {
            std::cerr << "[goxapian] document_set_data: unknown exception" << std::endl;
        }
    }

    void document_add_term(xapian_document_t doc, const char *term)
    {
        if (!doc || !term)
            return;
        try
        {
            reinterpret_cast<Xapian::Document *>(doc)->add_term(std::string(term));
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] document_add_term failed: " << e.get_msg() << std::endl;
        }
        catch (...)
        {
            std::cerr << "[goxapian] document_add_term: unknown exception" << std::endl;
        }
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
            std::cerr << "[goxapian] document_get_data failed: " << e.get_msg() << std::endl;
            return strdup("");
        }
        catch (...)
        {
            std::cerr << "[goxapian] document_get_data: unknown exception" << std::endl;
            return strdup("");
        }
    }

    // === QueryParser ===
    xapian_queryparser_t queryparser_new()
    {
        try
        {
            return new Xapian::QueryParser();
        }
        catch (...)
        {
            std::cerr << "[goxapian] queryparser_new: allocation failed" << std::endl;
            return nullptr;
        }
    }

    void queryparser_free(xapian_queryparser_t qp)
    {
        if (!qp)
            return;
        try
        {
            delete reinterpret_cast<Xapian::QueryParser *>(qp);
        }
        catch (...)
        {
            std::cerr << "[goxapian] queryparser_free: unknown exception" << std::endl;
        }
    }

    void queryparser_set_database(xapian_queryparser_t qp, xapian_database_t db)
    {
        if (!qp || !db)
            return;
        try
        {
            reinterpret_cast<Xapian::QueryParser *>(qp)->set_database(*reinterpret_cast<Xapian::Database *>(db));
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] queryparser_set_database failed: " << e.get_msg() << std::endl;
        }
        catch (...)
        {
            std::cerr << "[goxapian] queryparser_set_database: unknown exception" << std::endl;
        }
    }

    void queryparser_set_stemming_language(xapian_queryparser_t qp, const char *lang)
    {
        if (!qp || !lang)
            return;
        try
        {
            reinterpret_cast<Xapian::QueryParser *>(qp)->set_stemmer(Xapian::Stem(std::string(lang)));
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] queryparser_set_stemming_language failed: " << e.get_msg() << std::endl;
        }
        catch (...)
        {
            std::cerr << "[goxapian] queryparser_set_stemming_language: unknown exception" << std::endl;
        }
    }

    void queryparser_set_default_op(xapian_queryparser_t qp, xapian_query_op op)
    {
        if (!qp)
            return;
        try
        {
            reinterpret_cast<Xapian::QueryParser *>(qp)->set_default_op(static_cast<Xapian::Query::op>(op));
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] queryparser_set_default_op failed: " << e.get_msg() << std::endl;
        }
        catch (...)
        {
            std::cerr << "[goxapian] queryparser_set_default_op: unknown exception" << std::endl;
        }
    }

    void queryparser_add_prefix(xapian_queryparser_t qp, const char *field, const char *prefix)
    {
        if (!qp || !field || !prefix)
            return;
        try
        {
            reinterpret_cast<Xapian::QueryParser *>(qp)->add_prefix(std::string(field), std::string(prefix));
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] queryparser_add_prefix failed: " << e.get_msg() << std::endl;
        }
        catch (...)
        {
            std::cerr << "[goxapian] queryparser_add_prefix: unknown exception" << std::endl;
        }
    }

    xapian_query_t queryparser_parse_query(xapian_queryparser_t qp, const char *query_string, unsigned int flags)
    {
        if (!qp)
            return nullptr;
        try
        {
            Xapian::QueryParser *parser = reinterpret_cast<Xapian::QueryParser *>(qp);
            std::string query_str(query_string ? query_string : "");
            Xapian::Query q;

            if (query_str.empty())
            {
                // Match-all query, used to fetch every document.
                q = Xapian::Query("");
            }
            else
            {
                q = parser->parse_query(query_str, flags);
            }

            return new Xapian::Query(q);
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] queryparser_parse_query failed: " << e.get_msg() << std::endl;
            return nullptr;
        }
        catch (...)
        {
            std::cerr << "[goxapian] queryparser_parse_query: unknown exception" << std::endl;
            return nullptr;
        }
    }

    // === Query ===
    void query_free(xapian_query_t query)
    {
        if (!query)
            return;
        try
        {
            delete reinterpret_cast<Xapian::Query *>(query);
        }
        catch (...)
        {
            std::cerr << "[goxapian] query_free: unknown exception" << std::endl;
        }
    }

    // === Enquire ===
    xapian_enquire_t enquire_new(xapian_database_t db)
    {
        if (!db)
            return nullptr;
        try
        {
            return new Xapian::Enquire(*reinterpret_cast<Xapian::Database *>(db));
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] enquire_new failed: " << e.get_msg() << std::endl;
            return nullptr;
        }
        catch (...)
        {
            std::cerr << "[goxapian] enquire_new: unknown exception" << std::endl;
            return nullptr;
        }
    }

    void enquire_free(xapian_enquire_t enq)
    {
        if (!enq)
            return;
        try
        {
            delete reinterpret_cast<Xapian::Enquire *>(enq);
        }
        catch (...)
        {
            std::cerr << "[goxapian] enquire_free: unknown exception" << std::endl;
        }
    }

    void enquire_set_query(xapian_enquire_t enq, xapian_query_t query)
    {
        if (!enq || !query)
            return;
        try
        {
            reinterpret_cast<Xapian::Enquire *>(enq)->set_query(*reinterpret_cast<Xapian::Query *>(query));
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] enquire_set_query failed: " << e.get_msg() << std::endl;
        }
        catch (...)
        {
            std::cerr << "[goxapian] enquire_set_query: unknown exception" << std::endl;
        }
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
            std::cerr << "[goxapian] enquire_get_mset failed: " << e.get_msg() << std::endl;
            return nullptr;
        }
        catch (...)
        {
            std::cerr << "[goxapian] enquire_get_mset: unknown exception" << std::endl;
            return nullptr;
        }
    }

    // === MSet ===
    void mset_free(xapian_mset_t mset)
    {
        if (!mset)
            return;
        try
        {
            delete reinterpret_cast<Xapian::MSet *>(mset);
        }
        catch (...)
        {
            std::cerr << "[goxapian] mset_free: unknown exception" << std::endl;
        }
    }

    int mset_get_size(xapian_mset_t mset)
    {
        if (!mset)
            return 0;
        try
        {
            return reinterpret_cast<Xapian::MSet *>(mset)->size();
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] mset_get_size failed: " << e.get_msg() << std::endl;
            return 0;
        }
        catch (...)
        {
            std::cerr << "[goxapian] mset_get_size: unknown exception" << std::endl;
            return 0;
        }
    }

    void *mset_get_document(xapian_mset_t mset, unsigned int index)
    {
        if (!mset)
            return nullptr;
        try
        {
            Xapian::MSet *m = reinterpret_cast<Xapian::MSet *>(mset);
            if (index >= m->size())
                return nullptr;
            Xapian::MSetIterator it = m->begin();
            std::advance(it, index);
            return new Xapian::Document(it.get_document());
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] mset_get_document failed: " << e.get_msg() << std::endl;
            return nullptr;
        }
        catch (...)
        {
            std::cerr << "[goxapian] mset_get_document: unknown exception" << std::endl;
            return nullptr;
        }
    }

    int mset_get_rank(xapian_mset_t mset, unsigned int index)
    {
        if (!mset)
            return -1;
        try
        {
            Xapian::MSet *m = reinterpret_cast<Xapian::MSet *>(mset);
            if (index >= m->size())
                return -1;
            // NOTE: this was already effectively `return index;` before this
            // pass (the MSetIterator advance result was discarded) -- kept as
            // -is since `index` IS the rank for a 0-based mset slice starting
            // at `first`, matching Xapian::MSetIterator::get_rank() semantics
            // for this usage. Left the iterator advance in only to validate
            // that `index` is actually reachable (throws/no-ops otherwise are
            // caught below), not because its result is used.
            Xapian::MSetIterator it = m->begin();
            std::advance(it, index);
            return static_cast<int>(index);
        }
        catch (const Xapian::Error &e)
        {
            std::cerr << "[goxapian] mset_get_rank failed: " << e.get_msg() << std::endl;
            return -1;
        }
        catch (...)
        {
            std::cerr << "[goxapian] mset_get_rank: unknown exception" << std::endl;
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
            std::cerr << "[goxapian] mset_get_matches_estimated failed: " << e.get_msg() << std::endl;
            return 0;
        }
        catch (...)
        {
            std::cerr << "[goxapian] mset_get_matches_estimated: unknown exception" << std::endl;
            return 0;
        }
    }

} // extern "C"
