#include "xapian/xapian_manager.h"
#include <xapian.h>
#include <string>

bool XapianManager::isReservedSystemDocIdTerm(const std::string& term) {
    if (term.empty() || term[0] != 'Q') {
        return false;
    }
    // Reserved format is 'Q' + hex_encoded_id (between 16 and 64 hex characters)
    size_t hex_len = term.length() - 1;
    if (hex_len < 16 || hex_len > 64) {
        return false;
    }
    for (size_t i = 1; i < term.length(); ++i) {
        char c = term[i];
        if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'))) {
            return false;
        }
    }
    return true;
}

bool XapianManager::isReservedSystemPrefix(const std::string& prefix) {
    if (prefix.empty()) {
        return false;
    }
    if (prefix[0] != 'Q' && prefix[0] != 'q') {
        return false;
    }
    // Any prefix starting with 'Q' or 'q' where all remaining characters are hex digits
    // is a potential prefix or partial prefix of a system document identifier.
    for (size_t i = 1; i < prefix.length(); ++i) {
        char c = prefix[i];
        if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'))) {
            return false;
        }
    }
    return true;
}

// Helper function to resolve a virtual document ID solely via its 'Q' + clean_id term.
// Never treats short strings (< 16 chars) as raw physical Xapian::docids, preventing
// short docid strings (e.g. "0x2") from aliasing or overwriting unrelated documents.
Xapian::docid XapianManager::resolveVirtualDocId(const std::string& virtualDocIdStr, Xapian::Database* search_db) {
    if (virtualDocIdStr.empty()) return 0;
    try {
        std::string clean_id = normalize_docid(virtualDocIdStr);
        if (clean_id.length() >= 2 && clean_id.substr(0, 2) == "0x") {
            clean_id = clean_id.substr(2);
        }
        if (clean_id.empty()) return 0;

        std::string term = "Q" + clean_id;

        if (search_db != nullptr) {
            Xapian::PostingIterator it = search_db->postlist_begin(term);
            if (it != search_db->postlist_end(term)) {
                return *it;
            }
        } else {
            Xapian::PostingIterator it = db.postlist_begin(term);
            if (it != db.postlist_end(term)) {
                return *it;
            }
        }
        return 0; // Not found
    } catch (...) {
        return 0;
    }
}
