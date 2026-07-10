#include "xapian/xapian_manager.h"
#include <xapian.h>
#include <string>

// Helper function to insert into xapian_manager.cpp
Xapian::docid XapianManager::resolveVirtualDocId(const std::string& virtualDocIdStr, bool use_read_db) {
    if (virtualDocIdStr.empty()) return 0;
    try {
        if (virtualDocIdStr.length() < 16) {
            return static_cast<Xapian::docid>(std::stoul(virtualDocIdStr, nullptr, 16));
        }
        
        std::string clean_id = virtualDocIdStr;
        if (clean_id.substr(0, 2) == "0x") clean_id = clean_id.substr(2);
        
        std::string term = "Q" + clean_id;
        
        if (use_read_db) {
            Xapian::PostingIterator it = read_db.postlist_begin(term);
            if (it != read_db.postlist_end(term)) {
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
