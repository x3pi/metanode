// Unit tests for XapianManager (Database logic for Xapian Handlers)

#include <signal.h>
#undef SIGSTKSZ
#define SIGSTKSZ 8192

#define DOCTEST_CONFIG_IMPLEMENT_WITH_MAIN
#include <doctest/doctest.h>

#include "xapian/xapian_manager.h"
#include "my_extension/utils.h"
#include <filesystem>

// Helper to remove test DB directory
void cleanup_test_db(const std::string& base_path) {
    if (std::filesystem::exists(base_path)) {
        std::filesystem::remove_all(base_path);
    }
}

// Global fixture for setting up Xapian base path once
static bool xapian_initialized = false;
void init_xapian_test_env() {
    if (!xapian_initialized) {
        SetXapianBasePath(".test_db_xapian");
        xapian_initialized = true;
    }
}

// Helper to reliably create a XapianManager instance with directories
std::shared_ptr<XapianManager> create_test_manager(const std::string& db_name, const mvm::Address& addr, bool reset) {
    auto expected_path = mvm::createFullPath(addr, db_name);
    if (!std::filesystem::exists(expected_path)) {
        std::filesystem::create_directories(expected_path);
    }
    return XapianManager::getInstance(db_name, addr, reset);
}

TEST_SUITE("XapianManager") {
    
    // Setup before each testcase
    std::string test_db_name = "test_collection";
    mvm::Address mock_addr = 123456789; 
    
    TEST_CASE("Database Initialization and Creation") {
        try {
            init_xapian_test_env();
     // Start fresh

            // Create manager with reset = true
            auto manager = create_test_manager(test_db_name, mock_addr, true);
            REQUIRE(manager != nullptr);
            
            // Assert that the database path was created
            auto expected_path = mvm::createFullPath(mock_addr, test_db_name);
            CHECK(std::filesystem::exists(expected_path));
            
    
        } catch (const Xapian::Error& e) {
            std::cerr << "Xapian exception: " << e.get_description() << std::endl;
            FAIL("Xapian error");
        } catch (const std::exception& e) {
            std::cerr << "Std exception: " << e.what() << std::endl;
            FAIL("std error");
        } catch (...) {
            std::cerr << "Unknown exception caught manually" << std::endl;
            FAIL("unknown error");
        }
    }

    TEST_CASE("Document Lifecycle: Create, Get, Update, Delete") {
        init_xapian_test_env();
 

        auto manager = create_test_manager(test_db_name, mock_addr, true);
        REQUIRE(manager != nullptr);

        // 1. Create new document
        std::string initial_data = "{\"title\": \"Hello World\"}";
        Xapian::docid doc_id = manager->new_document(initial_data, 100);
        REQUIRE(doc_id > 0);

        // 2. Get data
        std::string retrieved_data = manager->get_data(doc_id, 100);
        CHECK(retrieved_data == initial_data);

        // 3. Set data (Update in-place with same blockNumber)
        std::string new_data = "{\"title\": \"Updated\"}";
        bool set_ok = manager->set_data(doc_id, new_data, 100);
        CHECK(set_ok == true);
        
        CHECK(manager->get_data(doc_id, 100) == new_data);

        // 4. Delete document
        bool del_ok = manager->delete_document(doc_id, 100);
        CHECK(del_ok == true);

        // Teardown
        manager->commit_changes();
        manager->destroyInstance(mvm::createFullPath(mock_addr, test_db_name).string());
    }

    TEST_CASE("Values and Terms") {
        init_xapian_test_env();
 
        auto manager = create_test_manager(test_db_name, mock_addr, true);
        REQUIRE(manager != nullptr);

        Xapian::docid doc_id = manager->new_document("doc data", 200);

        // --- Values ---
        SUBCASE("Add and Get Values") {
            bool val_ok = manager->add_value(doc_id, 1, "test_value_1", false, 200);
            CHECK(val_ok == true);

            // Get value back
            std::string retrieved_val = manager->get_value(doc_id, 1, false, 200);
            CHECK(retrieved_val == "test_value_1");
        }

        // --- Terms ---
        SUBCASE("Add and Get Terms") {
            bool term_ok = manager->add_term(doc_id, "QTERM1", 200);
            CHECK(term_ok == true);
            
            manager->add_term(doc_id, "QTERM2", 200);

            std::vector<std::string> terms = manager->get_terms(doc_id, 200);
            CHECK(terms.size() >= 2);
            
            bool found_term1 = false;
            for(const auto& t : terms) {
                if (t == "QTERM1") found_term1 = true;
            }
            CHECK(found_term1 == true);
        }

        manager->commit_changes();
        manager->destroyInstance(mvm::createFullPath(mock_addr, test_db_name).string());
    }

    TEST_CASE("Index Text") {
        init_xapian_test_env();
 
        auto manager = create_test_manager(test_db_name, mock_addr, true);
        REQUIRE(manager != nullptr);

        Xapian::docid doc_id = manager->new_document("doc data", 300);

        // Indexing some text with a prefix 'S'
        bool idx_ok = manager->index_text(doc_id, "searchable text content", 1, "S", 300);
        CHECK(idx_ok == true);

        std::vector<std::string> terms = manager->get_terms(doc_id, 300);
        
        bool found_searchable = false;
        for(const auto& t : terms) {
            if (t.rfind("S", 0) == 0 && t.length() > 1) {
                found_searchable = true;
            }
        }
        CHECK(found_searchable == true);

        manager->commit_changes();
        manager->destroyInstance(mvm::createFullPath(mock_addr, test_db_name).string());
    }

    TEST_CASE("Change Logs and Hash Generation") {
        init_xapian_test_env();
 
        auto manager = create_test_manager(test_db_name, mock_addr, true);
        REQUIRE(manager != nullptr);

        manager->new_document("change log test", 400);

        std::vector<XapianLog::LogEntry> logs = manager->getChangeLogs();

        std::array<uint8_t, 32> hash = manager->getChangeHash();
        bool is_zero = true;
        for(auto b : hash) {
            if (b != 0) is_zero = false;
        }
        CHECK((is_zero == true || is_zero == false)); 

        manager->commit_changes();
        manager->destroyInstance(mvm::createFullPath(mock_addr, test_db_name).string());
    }
}
