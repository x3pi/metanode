#include "xapian/xapian_manager.h"
#include <xapian.h>
#include <string>

// Helper function to insert into xapian_manager.cpp
Xapian::docid XapianManager::resolveVirtualDocId(const std::string& virtualDocIdStr) {
    if (virtualDocIdStr.empty()) return 0;
    try {
        // Parse the hex string to uint256_t
        // But wait, mvm::from_hex_string? Or just check length?
        // Let's just check length. If it's a UUID, it's 64 chars long (256 bits).
        // If it's short, it might be a legacy native docid.
        if (virtualDocIdStr.length() < 16) {
            // Probably a legacy uint32 docid
            return static_cast<Xapian::docid>(std::stoul(virtualDocIdStr, nullptr, 16));
        }
        
        std::string clean_id = virtualDocIdStr;
        if (clean_id.substr(0, 2) == "0x") clean_id = clean_id.substr(2);

        // Search by UUID term Q<clean_id>
        Xapian::Query query("Q" + clean_id);
        Xapian::Enquire enquire(db);
        enquire.set_query(query);
        Xapian::MSet matches = enquire.get_mset(0, 1);
        if (matches.empty()) {
            return 0; // Not found
        }
        return *matches.begin();
    } catch (...) {
        return 0;
    }
}
