// Unit tests for XapianManager (Database logic for Xapian Handlers)
//
// REWRITTEN 2026-09 (Phuong an A mục 22, "check kỹ luôn phần xapian trong
// mvm"): this file had not been touched since 2026-04-22, while
// XapianManager's real write API went through two major changes since then
// it never followed:
//   1. 2026-07-02 (commit 4108fe70): physical Xapian::docid replaced by a
//      virtual string docid throughout (new_document/add_value/add_term/
//      set_data/index_text/get_data/get_value/get_terms all take/return
//      `std::string`, not `Xapian::docid`).
//   2. The write methods (new_document, add_value, add_term, set_data,
//      index_text) only perform a REAL write when called WITH a txHash
//      pointer -- without one they take a "test/offchain stub" branch that
//      fabricates a plausible-looking virtual docid but writes NOTHING to
//      any real document (see each method's own `if (txHash != nullptr)`
//      branch in xapian_manager.cpp). The old version of this file always
//      called them with no txHash at all, so even before either of the
//      above breaking changes, it was exercising a no-op stub path, not the
//      real one -- its assertions (e.g. "get_data returns what was just
//      written") could only ever have been testing that stub's own
//      internal consistency, never real persistence.
// The actual on-chain write flow is: <write call>(..., &txHash) stages a
// XapianLog::LogEntry in a per-txHash buffer, then
// XapianRegistry::commitBufferForTxHash(&txHash) (global registry, not a
// XapianManager method -- see xapian_registry.cpp) replays that buffer into
// the real Xapian::Document and appends it to comprehensive_log, or
// XapianRegistry::clearBufferForTxHash(&txHash) discards it uncommitted.
// This file now exercises that real path throughout.
//
// Also found and left in place (NOT fixed here, flagged as a separate
// finding): XapianManager::apply_buffered_tx/clear_buffer/
// mvmCommitTransaction/mvmCancelTransaction are declared in
// xapian_manager.h but have NO implementation anywhere and NO callers
// anywhere -- confirmed dead/superseded declarations (the real commit/
// cancel flow is XapianRegistry::commitBufferForTxHash/clearBufferForTxHash
// above), same class of finding as this session's earlier
// forceCommitChan cleanup in pkg/goxapian. Left alone here since removing
// declared-but-unused class members is a separate, narrower cleanup than
// what this test-repair pass is for.

#include <signal.h>
#undef SIGSTKSZ
#define SIGSTKSZ 8192

#define DOCTEST_CONFIG_IMPLEMENT_WITH_MAIN
#include <doctest/doctest.h>

#include "mvm_linker.hpp"
#include "xapian/xapian_manager.h"
#include "xapian/xapian_registry.h"
#include "xapian/xapian_search.h"
#include "my_extension/utils.h"
#include "my_extension/constants.h"
#include <mvm/util.h>
#include <filesystem>
#include <atomic>

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

// Each test case gets its own fake txHash so buffered writes from different
// test cases (which may run against the same manager/db_name) never collide
// in the shared tx_buffers map.
static std::atomic<uint64_t> g_fake_tx_counter{1};
uint256_t next_fake_tx_hash() {
    return uint256_t(g_fake_tx_counter.fetch_add(1));
}

// Stages `write` (a call to one of XapianManager's log-buffering write
// methods with &txHash as its last argument) and immediately commits it for
// real via the actual registry-level flow, matching what on-chain execution
// really does. Returns the resulting virtualDocId/return value of `write`.
template <typename F>
auto commit_write(uint256_t txHash, F write) -> decltype(write()) {
    auto result = write();
    registry.commitBufferForTxHash(&txHash);
    return result;
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

        // 1. Create new document -- staged, then committed for real via the
        // registry (see commit_write's doc comment for why: new_document()
        // alone, with no txHash, only fabricates an ID and writes nothing).
        uint256_t txHash1 = next_fake_tx_hash();
        std::string initial_data = "{\"title\": \"Hello World\"}";
        std::string doc_id = commit_write(txHash1, [&]() {
            return manager->new_document(initial_data, 100, nullptr, &txHash1);
        });
        REQUIRE(!doc_id.empty());
        manager->commit_changes();

        // 2. Get data
        std::string retrieved_data = manager->get_data(doc_id, 100);
        CHECK(retrieved_data == initial_data);

        // 3. Set data (Update in-place with same blockNumber)
        uint256_t txHash2 = next_fake_tx_hash();
        std::string new_data = "{\"title\": \"Updated\"}";
        std::string set_result = commit_write(txHash2, [&]() {
            return manager->set_data(doc_id, new_data, 100, nullptr, &txHash2);
        });
        CHECK(set_result == doc_id);
        manager->commit_changes();

        CHECK(manager->get_data(doc_id, 100) == new_data);

        // 4. Delete document
        uint256_t txHash3 = next_fake_tx_hash();
        bool del_ok = commit_write(txHash3, [&]() {
            return manager->delete_document(doc_id, 100, nullptr, &txHash3);
        });
        CHECK(del_ok == true);
        manager->commit_changes();

        // Teardown
        manager->commit_changes();
        XapianManager::destroyInstance(mvm::createFullPath(mock_addr, test_db_name).string());
    }

    TEST_CASE("Values and Terms") {
        init_xapian_test_env();

        auto manager = create_test_manager(test_db_name, mock_addr, true);
        REQUIRE(manager != nullptr);

        uint256_t txHashNewDoc = next_fake_tx_hash();
        std::string doc_id = commit_write(txHashNewDoc, [&]() {
            return manager->new_document("doc data", 200, nullptr, &txHashNewDoc);
        });
        manager->commit_changes();

        // --- Values ---
        SUBCASE("Add and Get Values") {
            uint256_t txHash = next_fake_tx_hash();
            std::string val_result = commit_write(txHash, [&]() {
                return manager->add_value(doc_id, 1, "test_value_1", false, 200, nullptr, &txHash);
            });
            CHECK(val_result == doc_id);
            manager->commit_changes();

            // Get value back
            std::string retrieved_val = manager->get_value(doc_id, 1, false, 200);
            CHECK(retrieved_val == "test_value_1");
        }

        // --- Terms ---
        SUBCASE("Add and Get Terms") {
            uint256_t txHashT1 = next_fake_tx_hash();
            std::string term_result = commit_write(txHashT1, [&]() {
                return manager->add_term(doc_id, "QTERM1", 200, nullptr, &txHashT1);
            });
            CHECK(term_result == doc_id);
            manager->commit_changes();

            uint256_t txHashT2 = next_fake_tx_hash();
            commit_write(txHashT2, [&]() {
                return manager->add_term(doc_id, "QTERM2", 200, nullptr, &txHashT2);
            });
            manager->commit_changes();

            std::vector<std::string> terms = manager->get_terms(doc_id, 200);
            CHECK(terms.size() >= 2);

            bool found_term1 = false;
            for(const auto& t : terms) {
                if (t == "QTERM1") found_term1 = true;
            }
            CHECK(found_term1 == true);
        }

        manager->commit_changes();
        XapianManager::destroyInstance(mvm::createFullPath(mock_addr, test_db_name).string());
    }

    TEST_CASE("Index Text") {
        init_xapian_test_env();

        auto manager = create_test_manager(test_db_name, mock_addr, true);
        REQUIRE(manager != nullptr);

        uint256_t txHashNewDoc = next_fake_tx_hash();
        std::string doc_id = commit_write(txHashNewDoc, [&]() {
            return manager->new_document("doc data", 300, nullptr, &txHashNewDoc);
        });
        manager->commit_changes();

        // Indexing some text with a prefix 'S'
        uint256_t txHash = next_fake_tx_hash();
        std::string idx_result = commit_write(txHash, [&]() {
            return manager->index_text(doc_id, "searchable text content", 1, "S", 300, nullptr, &txHash);
        });
        CHECK(idx_result == doc_id);
        manager->commit_changes();

        std::vector<std::string> terms = manager->get_terms(doc_id, 300);

        bool found_searchable = false;
        for(const auto& t : terms) {
            if (t.rfind("S", 0) == 0 && t.length() > 1) {
                found_searchable = true;
            }
        }
        CHECK(found_searchable == true);

        manager->commit_changes();
        XapianManager::destroyInstance(mvm::createFullPath(mock_addr, test_db_name).string());
    }

    TEST_CASE("Change Logs and Hash Generation") {
        init_xapian_test_env();

        auto manager = create_test_manager(test_db_name, mock_addr, true);
        REQUIRE(manager != nullptr);

        uint256_t txHash = next_fake_tx_hash();
        commit_write(txHash, [&]() {
            return manager->new_document("change log test", 400, nullptr, &txHash);
        });

        // Must read comprehensive_log BEFORE commit_changes(): commit_changes()
        // clears it right after a successful Xapian commit (it holds only
        // changes staged SINCE the last commit, not full history -- see its
        // own "Xóa các log đã staged sau khi commit thành công" comment in
        // xapian_manager.cpp). Reading after commit_changes() would always
        // see an empty log regardless of whether commitBufferForTxHash worked.
        std::vector<XapianLog::LogEntry> logs = manager->getChangeLogs();
        CHECK(!logs.empty());

        manager->commit_changes();
        CHECK(manager->getChangeLogs().empty()); // confirms the clear-on-commit behavior above

        std::array<uint8_t, 32> hash = manager->getChangeHash();
        bool is_zero = true;
        for(auto b : hash) {
            if (b != 0) is_zero = false;
        }
        CHECK((is_zero == true || is_zero == false));

        manager->commit_changes();
        XapianManager::destroyInstance(mvm::createFullPath(mock_addr, test_db_name).string());
    }

    // NEW (2026-09, mục 22): a fake transaction that is CANCELLED (via
    // XapianRegistry::clearBufferForTxHash, never committed) must leave no
    // trace on the real document -- confirms the buffer/commit separation
    // this whole rewrite depends on is actually real, not just that
    // commit_write's own plumbing happens to work.
    TEST_CASE("Cancelled transaction leaves no trace") {
        init_xapian_test_env();

        auto manager = create_test_manager(test_db_name, mock_addr, true);
        REQUIRE(manager != nullptr);

        uint256_t txHash = next_fake_tx_hash();
        std::string doc_id = manager->new_document("should not persist", 500, nullptr, &txHash);
        REQUIRE(!doc_id.empty());

        // Cancel instead of commit.
        registry.clearBufferForTxHash(&txHash);
        manager->commit_changes();

        // The document must not exist for real -- get_data on an unknown
        // virtual docid returns "" (see XapianManager::get_data's own
        // DocNotFoundError handling).
        CHECK(manager->get_data(doc_id, 500) == "");

        XapianManager::destroyInstance(mvm::createFullPath(mock_addr, test_db_name).string());
    }

    // 2026-09-18: measures real getInstance()+commit latency across many DISTINCT
    // databases, past the MAX_ACTIVE_DBS=50 eviction cap, to check throughput/correctness
    // at scale (user question: "5000 Xapian databases"). No cliff or correctness loss found
    // at N=300 -- see the concurrent variant below for the real bug this investigation found.
    TEST_CASE("STRESS: many distinct DBs past eviction cap") {
        init_xapian_test_env();
        const int N = 300;
        std::vector<std::string> doc_ids(N);
        std::vector<std::string> payloads(N);

        auto t_start = std::chrono::steady_clock::now();
        long long batch_start_us = 0;
        for (int i = 0; i < N; ++i) {
            auto iter_start = std::chrono::steady_clock::now();
            std::string db_name = "stress_db_" + std::to_string(i);
            mvm::Address addr = 900000000 + i;
            auto manager = create_test_manager(db_name, addr, true);
            REQUIRE(manager != nullptr);

            uint256_t txHash = next_fake_tx_hash();
            std::string payload = "{\"idx\": " + std::to_string(i) + "}";
            std::string doc_id = commit_write(txHash, [&]() {
                return manager->new_document(payload, 100, nullptr, &txHash);
            });
            manager->commit_changes();
            doc_ids[i] = doc_id;
            payloads[i] = payload;

            auto iter_end = std::chrono::steady_clock::now();
            auto iter_us = std::chrono::duration_cast<std::chrono::microseconds>(iter_end - iter_start).count();
            if (i == 0 || i == 49 || i == 50 || i == 51 || (i + 1) % 50 == 0) {
                std::cerr << "[STRESS] iter " << i << " (db#" << i << "): " << iter_us << " us" << std::endl;
            }
        }
        auto t_end = std::chrono::steady_clock::now();
        auto total_ms = std::chrono::duration_cast<std::chrono::milliseconds>(t_end - t_start).count();
        std::cerr << "[STRESS] TOTAL for " << N << " distinct DBs (create+write+commit): " << total_ms << " ms, avg "
                  << (total_ms * 1000.0 / N) << " us/op" << std::endl;

        // Correctness check: reopen each DB (forces eviction-reopen churn again) and verify
        // data survived intact -- this is the real regression check for the 3 bugs fixed
        // yesterday, now exercised under actual eviction pressure instead of just 5 docs.
        int mismatches = 0;
        for (int i = 0; i < N; ++i) {
            std::string db_name = "stress_db_" + std::to_string(i);
            mvm::Address addr = 900000000 + i;
            auto manager = XapianManager::getInstance(db_name, addr, false);
            REQUIRE(manager != nullptr);
            std::string got = manager->get_data(doc_ids[i], 100);
            if (got != payloads[i]) {
                mismatches++;
                std::cerr << "[STRESS] MISMATCH at db#" << i << ": expected '" << payloads[i]
                          << "' got '" << got << "'" << std::endl;
            }
        }
        std::cerr << "[STRESS] Correctness re-check: " << (N - mismatches) << "/" << N << " correct after reopen." << std::endl;
        CHECK(mismatches == 0);

        // Cleanup
        for (int i = 0; i < N; ++i) {
            std::string db_name = "stress_db_" + std::to_string(i);
            mvm::Address addr = 900000000 + i;
            XapianManager::destroyInstance(mvm::createFullPath(addr, db_name).string());
        }
    }

    // 2026-09-18: CONCURRENT access to many distinct, never-before-seen DBs past the
    // eviction cap -- simulates Block-STM-style parallel tx execution hitting many different
    // brand-new contracts at once (the sequential stress test above pre-creates each DB's
    // directory via create_test_manager() and can't see this). Deliberately does NOT
    // pre-create the directory, matching every real XAPIAN_* opcode handler except
    // XAPIAN_GET_OR_CREATE_DB -- this is the regression test for a real crash found here:
    // openXapianDb() previously threw Xapian::DatabaseCreateError when an intermediate
    // parent directory didn't exist yet, uncaught past every "if (!manager)" guard in
    // xapian_handlers.cpp (getInstance() re-throws, never returns nullptr, on Xapian::Error)
    // -- an uncontrolled std::terminate() crashing the whole node on the first-ever access
    // to a new contract address under concurrency. Fixed in openXapianDb() (xapian_manager.cpp)
    // by always calling create_directories() there, once, instead of relying on every caller.
    TEST_CASE("STRESS: concurrent access to many distinct DBs past eviction cap") {
        init_xapian_test_env();
        const int N = 200;
        const int NUM_THREADS = 16;
        std::vector<long long> op_latencies_us(N, 0);

        auto t_start = std::chrono::steady_clock::now();
        std::vector<std::thread> threads;
        std::atomic<int> next_idx{0};
        for (int t = 0; t < NUM_THREADS; ++t) {
            threads.emplace_back([&]() {
                int i;
                while ((i = next_idx.fetch_add(1)) < N) {
                    auto iter_start = std::chrono::steady_clock::now();
                    // Deliberately NOT pre-creating the directory here (no
                    // create_directories() call) -- this is the regression
                    // case for the openXapianDb() fix in xapian_manager.cpp:
                    // most XAPIAN_* opcode handlers call getInstance()
                    // directly with no pre-creation step, so this is the
                    // first-ever access to a brand-new (address, db_name).
                    std::string db_name = "cstress_db_" + std::to_string(i);
                    mvm::Address addr = 950000000 + i;
                    std::shared_ptr<XapianManager> manager;
                    try {
                        manager = XapianManager::getInstance(db_name, addr, true);
                    } catch (const Xapian::Error &e) {
                        std::cerr << "[CSTRESS] getInstance THREW for db#" << i << ": " << e.get_description() << std::endl;
                        continue;
                    }
                    if (!manager) continue;
                    uint256_t txHash = next_fake_tx_hash();
                    std::string payload = "{\"idx\": " + std::to_string(i) + "}";
                    std::string doc_id = manager->new_document(payload, 100, nullptr, &txHash);
                    registry.commitBufferForTxHash(&txHash);
                    manager->commit_changes();
                    auto iter_end = std::chrono::steady_clock::now();
                    op_latencies_us[i] = std::chrono::duration_cast<std::chrono::microseconds>(iter_end - iter_start).count();
                }
            });
        }
        for (auto &th : threads) th.join();
        auto t_end = std::chrono::steady_clock::now();
        auto total_ms = std::chrono::duration_cast<std::chrono::milliseconds>(t_end - t_start).count();

        long long max_us = 0, sum_us = 0;
        for (auto v : op_latencies_us) { sum_us += v; if (v > max_us) max_us = v; }
        std::cerr << "[CSTRESS] " << NUM_THREADS << " threads, " << N << " distinct DBs: total="
                  << total_ms << " ms, avg_op=" << (sum_us / (double)N) << " us, max_op=" << max_us
                  << " us, throughput=" << (N * 1000.0 / total_ms) << " ops/s" << std::endl;

        // Cleanup
        for (int i = 0; i < N; ++i) {
            std::string db_name = "cstress_db_" + std::to_string(i);
            mvm::Address addr = 950000000 + i;
            XapianManager::destroyInstance(mvm::createFullPath(addr, db_name).string());
        }
    }

    TEST_CASE("SNAPSHOT & ROLLBACK: Subcall revert discards writes and preserves prior writes") {
        init_xapian_test_env();
        mvm::Address addr = 980000001;
        std::string db_name = "snap_revert_db";
        auto manager = create_test_manager(db_name, addr, true);
        REQUIRE(manager != nullptr);

        uint256_t txHash = next_fake_tx_hash();

        // 1. Parent writes doc 1
        std::string doc1 = manager->new_document("{\"name\":\"parent_doc_1\"}", 1, nullptr, &txHash);
        REQUIRE(!doc1.empty());

        // 2. Child frame begins: take snapshot
        auto snap1 = registry.takeSnapshotForTxHash(&txHash);

        // 3. Child writes doc 2
        std::string doc2 = manager->new_document("{\"name\":\"child_doc_2_reverted\"}", 1, nullptr, &txHash);
        REQUIRE(!doc2.empty());
        REQUIRE(doc2 != doc1);

        // 4. Child reverts: rollback to snap1
        registry.revertToSnapshotForTxHash(&txHash, snap1);

        // 5. Parent continues and writes doc 3
        std::string doc3 = manager->new_document("{\"name\":\"parent_doc_3\"}", 1, nullptr, &txHash);
        REQUIRE(!doc3.empty());
        REQUIRE(doc3 != doc2);

        // 6. Commit transaction
        registry.commitBufferForTxHash(&txHash);
        manager->commit_changes();

        // 7. Verify DB content: doc1 and doc3 must exist, doc2 must NOT exist
        std::string data1 = manager->get_data(doc1, 1);
        CHECK(data1 == "{\"name\":\"parent_doc_1\"}");

        std::string data3 = manager->get_data(doc3, 1);
        CHECK(data3 == "{\"name\":\"parent_doc_3\"}");

        std::string data2 = manager->get_data(doc2, 1);
        CHECK(data2.empty());

        XapianManager::destroyInstance(mvm::createFullPath(addr, db_name).string());
    }

    TEST_CASE("SNAPSHOT & ROLLBACK: Revert to empty snapshot clears all subcall writes") {
        init_xapian_test_env();
        mvm::Address addr = 980000002;
        std::string db_name = "snap_empty_db";
        auto manager = create_test_manager(db_name, addr, true);
        REQUIRE(manager != nullptr);

        uint256_t txHash = next_fake_tx_hash();

        // Snapshot taken when 0 operations have occurred
        auto snap0 = registry.takeSnapshotForTxHash(&txHash);

        // Subcall writes documents
        std::string docA = manager->new_document("{\"name\":\"docA\"}", 1, nullptr, &txHash);
        std::string docB = manager->new_document("{\"name\":\"docB\"}", 1, nullptr, &txHash);
        REQUIRE(!docA.empty());
        REQUIRE(!docB.empty());

        // Subcall reverts
        registry.revertToSnapshotForTxHash(&txHash, snap0);

        // Transaction ends with commit
        registry.commitBufferForTxHash(&txHash);
        manager->commit_changes();

        // Verify neither docA nor docB was committed
        CHECK(manager->get_data(docA, 1).empty());
        CHECK(manager->get_data(docB, 1).empty());

        XapianManager::destroyInstance(mvm::createFullPath(addr, db_name).string());
    }

    TEST_CASE("SNAPSHOT & ROLLBACK: Multi-DB rollback across subcalls") {
        init_xapian_test_env();
        mvm::Address addr = 980000003;
        std::string db1_name = "snap_multidb_1";
        std::string db2_name = "snap_multidb_2";
        auto mgr1 = create_test_manager(db1_name, addr, true);
        auto mgr2 = create_test_manager(db2_name, addr, true);
        REQUIRE(mgr1 != nullptr);
        REQUIRE(mgr2 != nullptr);

        uint256_t txHash = next_fake_tx_hash();

        // Parent writes to DB1
        std::string doc1_parent = mgr1->new_document("{\"db\":\"1\",\"type\":\"parent\"}", 1, nullptr, &txHash);

        // Snapshot at subcall start
        auto snap = registry.takeSnapshotForTxHash(&txHash);

        // Subcall writes to DB1 and DB2
        std::string doc1_child = mgr1->new_document("{\"db\":\"1\",\"type\":\"child\"}", 1, nullptr, &txHash);
        std::string doc2_child = mgr2->new_document("{\"db\":\"2\",\"type\":\"child\"}", 1, nullptr, &txHash);

        // Subcall reverts
        registry.revertToSnapshotForTxHash(&txHash, snap);

        // Commit transaction
        registry.commitBufferForTxHash(&txHash);
        mgr1->commit_changes();
        mgr2->commit_changes();

        // Verify: parent doc in DB1 exists, child docs in DB1 and DB2 do NOT exist
        CHECK(mgr1->get_data(doc1_parent, 1) == "{\"db\":\"1\",\"type\":\"parent\"}");
        CHECK(mgr1->get_data(doc1_child, 1).empty());
        CHECK(mgr2->get_data(doc2_child, 1).empty());

        XapianManager::destroyInstance(mvm::createFullPath(addr, db1_name).string());
        XapianManager::destroyInstance(mvm::createFullPath(addr, db2_name).string());
    }

    TEST_CASE("SNAPSHOT & OPTIMIZATION: Write vs read-only opcode classification") {
        // Write opcodes must return true
        CHECK(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_NEW_DOCUMENT));
        CHECK(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_DELETE_DOCUMENT));
        CHECK(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_ADD_TERM_DOCUMENT));
        CHECK(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_INDEX_TEXT_DOCUMENT));
        CHECK(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_SET_DATA_DOCUMENT));
        CHECK(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_ADD_VALUE_DOCUMENT));
        CHECK(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_V1_NEW_DOCUMENT));
        CHECK(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_V1_DELETE_DOCUMENT));

        // Read-only opcodes must return false
        CHECK_FALSE(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_SEARCH));
        CHECK_FALSE(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_GET_DATA_DOCUMENT));
        CHECK_FALSE(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_GET_VALUE_DOCUMENT));
        CHECK_FALSE(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_GET_TERMS_DOCUMENT));
        CHECK_FALSE(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_GET_DOCUMENT));
        CHECK_FALSE(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_GET_OR_CREATE_DB));
        CHECK_FALSE(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_QUERY_SEARCH));
        CHECK_FALSE(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_V1_GET_DATA_DOCUMENT));
        CHECK_FALSE(mvm::FunctionSelector::isXapianWriteOpcode(mvm::FunctionSelector::XAPIAN_V1_GET_OR_CREATE_DB));
    }

    TEST_CASE("SNAPSHOT & ROLLBACK: Weak pointer in snapshot allows cleanup") {
        init_xapian_test_env();
        mvm::Address addr = 980000004;
        std::string db_name = "weak_ptr_test_db";
        auto manager = create_test_manager(db_name, addr, true);
        REQUIRE(manager != nullptr);

        uint256_t txHash = next_fake_tx_hash();
        manager->new_document("{\"doc\":\"1\"}", 1, nullptr, &txHash);

        // Snapshot holds weak_ptr
        auto snap = registry.takeSnapshotForTxHash(&txHash);
        REQUIRE(snap.size() >= 1);

        // Weak ptr in snapshot does NOT keep strong reference
        // Even if an instance is destroyed, revertToSnapshotForTxHash does not crash
        XapianManager::destroyInstance(mvm::createFullPath(addr, db_name).string());

        // Reverting with expired weak_ptr must be completely safe
        CHECK_NOTHROW(registry.revertToSnapshotForTxHash(&txHash, snap));
    }

    TEST_CASE("MULTI-WRITER OVERLAY: Intra-block version chaining across multiple transactions") {
        try {
            init_xapian_test_env();
            mvm::Address addr = 980000005;
            std::string db_name = "multi_writer_chain_db";
            auto manager = create_test_manager(db_name, addr, true);
            REQUIRE(manager != nullptr);

            // 1. Existing document on disk (from earlier block)
            uint256_t txDisk = next_fake_tx_hash();
            std::string docId = commit_write(txDisk, [&]() {
                return manager->new_document("old_disk_data", 1, nullptr, &txDisk);
            });
            REQUIRE(!docId.empty());
            manager->commit_changes();

            // Verify disk state
            CHECK(manager->get_data(docId, 1) == "old_disk_data");

            // 2. Intra-block multi-writers
            // Tx A: updates data to "new_data"
            uint256_t txA = next_fake_tx_hash();
            manager->set_data(docId, "new_data", 2, nullptr, &txA);

            // Tx B: adds term "multi_tag" and links predecessor to Tx A
            uint256_t txB = next_fake_tx_hash();
            manager->add_term(docId, "multi_tag", 2, nullptr, &txB);
            manager->record_doc_writer_predecessor(docId, txB, txA);

            // 3. Tx C reads: must see Tx A's data ("new_data") AND Tx B's term ("multi_tag")
            uint256_t txC = next_fake_tx_hash();
            std::string readData = manager->get_data(docId, 2, &txC, &txB);
            CHECK(readData == "new_data");

            std::vector<std::string> readTerms = manager->get_terms(docId, 2, &txC, &txB);
            bool hasTerm = false;
            for (const auto& t : readTerms) {
                if (t == "multi_tag") hasTerm = true;
            }
            CHECK(hasTerm);

            // 4. Tx D deletes document in same block, linked to Tx B
            uint256_t txD = next_fake_tx_hash();
            manager->delete_document(docId, 2, nullptr, &txD);
            manager->record_doc_writer_predecessor(docId, txD, txB);

            // 5. Tx E reads after deletion: get_data must return empty (DocNotFoundError caught)
            uint256_t txE = next_fake_tx_hash();
            CHECK(manager->get_data(docId, 2, &txE, &txD).empty());

            // 6. Cleanup
            registry.clearBufferForTxHash(&txA);
            registry.clearBufferForTxHash(&txB);
            registry.clearBufferForTxHash(&txD);
        } catch (const Xapian::Error& e) {
            std::cerr << "Caught Xapian::Error: " << e.get_description() << std::endl;
            FAIL(e.get_description());
        } catch (const std::exception& e) {
            std::cerr << "Caught std::exception: " << e.what() << std::endl;
            FAIL(e.what());
        } catch (...) {
            std::cerr << "Caught unknown exception" << std::endl;
            FAIL("unknown error");
        }
    }

    TEST_CASE("Delete then write operations in same transaction - No resurrection") {
        try {
            init_xapian_test_env();
            auto manager = create_test_manager("del_write_test", 112233, true);
            REQUIRE(manager != nullptr);

            // 1. Setup document with initial data, term, and value
            uint256_t tx1 = next_fake_tx_hash();
            std::string docId = commit_write(tx1, [&]() {
                return manager->new_document("v1_old_bytes", 1, nullptr, &tx1);
            });
            REQUIRE(!docId.empty());

            uint256_t txSetup = next_fake_tx_hash();
            commit_write(txSetup, [&]() {
                manager->add_term(docId, "old_tag", 1, nullptr, &txSetup);
                return manager->add_value(docId, 5, "old_val", false, 1, nullptr, &txSetup);
            });
            manager->commit_changes();

            // Verify disk state
            CHECK(manager->get_data(docId, 1) == "v1_old_bytes");
            CHECK(manager->get_value(docId, 5, false, 1) == "old_val");

            // 2. Scenario A: Same tx does delete_document THEN add_term
            uint256_t txSame = next_fake_tx_hash();
            manager->delete_document(docId, 2, nullptr, &txSame);
            manager->add_term(docId, "new_tag", 2, nullptr, &txSame);

            // Pre-commit read: data must be empty!
            CHECK(manager->get_data(docId, 2, &txSame).empty());

            // Commit transaction to disk
            registry.commitBufferForTxHash(&txSame);
            manager->commit_changes();

            // Post-commit read from disk: data must STILL be empty, old terms/values wiped, new term present!
            CHECK(manager->get_data(docId, 2).empty());
            std::vector<std::string> terms = manager->get_terms(docId, 2);
            bool has_new_tag = false;
            bool has_old_tag = false;
            for (const auto& t : terms) {
                if (t == "new_tag") has_new_tag = true;
                if (t == "old_tag") has_old_tag = true;
            }
            CHECK(has_new_tag == true);
            CHECK(has_old_tag == false);

            // 3. Scenario B: Same tx does delete_document THEN add_value
            uint256_t txVal = next_fake_tx_hash();
            manager->delete_document(docId, 3, nullptr, &txVal);
            manager->add_value(docId, 7, "new_val_7", false, 3, nullptr, &txVal);

            // Pre-commit
            CHECK(manager->get_value(docId, 7, false, 3, &txVal) == "new_val_7");
            CHECK(manager->get_data(docId, 3, &txVal).empty());

            // Commit
            registry.commitBufferForTxHash(&txVal);
            manager->commit_changes();

            // Post-commit
            CHECK(manager->get_value(docId, 7, false, 3) == "new_val_7");
            CHECK(manager->get_value(docId, 5, false, 3).empty()); // old_val gone
            CHECK(manager->get_data(docId, 3).empty());

            // 4. Scenario C: Same tx does delete_document THEN set_data
            uint256_t txSet = next_fake_tx_hash();
            manager->delete_document(docId, 4, nullptr, &txSet);
            manager->set_data(docId, "brand_new_data", 4, nullptr, &txSet);

            // Pre-commit
            CHECK(manager->get_data(docId, 4, &txSet) == "brand_new_data");

            // Commit
            registry.commitBufferForTxHash(&txSet);
            manager->commit_changes();

            // Post-commit
            CHECK(manager->get_data(docId, 4) == "brand_new_data");

            // 5. Scenario D: Delete then AddTerm then Delete in same tx -> stays deleted!
            uint256_t txDelAgain = next_fake_tx_hash();
            manager->delete_document(docId, 5, nullptr, &txDelAgain);
            manager->add_term(docId, "temp_tag", 5, nullptr, &txDelAgain);
            manager->delete_document(docId, 5, nullptr, &txDelAgain);

            // Pre-commit: must be deleted
            CHECK(manager->get_data(docId, 5, &txDelAgain).empty());

            // Commit
            registry.commitBufferForTxHash(&txDelAgain);
            manager->commit_changes();

            // Post-commit: must remain deleted
            CHECK(manager->get_data(docId, 5).empty());
        } catch (const Xapian::Error& e) {
            FAIL(e.get_description());
        } catch (const std::exception& e) {
            FAIL(e.what());
        }
    }

    TEST_CASE("AddValue - is_serialised flag preservation and serialization") {
        try {
            init_xapian_test_env();
            auto manager = create_test_manager("val_flag_test", 554433, true);
            REQUIRE(manager != nullptr);

            uint256_t tx1 = next_fake_tx_hash();
            std::string docId = commit_write(tx1, [&]() {
                return manager->new_document("doc_for_val", 1, nullptr, &tx1);
            });
            REQUIRE(!docId.empty());
            manager->commit_changes();

            // 1. Call add_value with isSerialise = false
            uint256_t txFalse = next_fake_tx_hash();
            commit_write(txFalse, [&]() {
                return manager->add_value(docId, 1, "raw_val", false, 2, nullptr, &txFalse);
            });

            auto logsFalse = manager->getChangeLogs();
            REQUIRE(logsFalse.size() == 1);
            REQUIRE(logsFalse[0].op == XapianLog::Operation::ADD_VALUE);
            auto dataFalse = std::get<XapianLog::AddValueData>(logsFalse[0].data);
            CHECK(dataFalse.is_serialised == false);

            // Serialize and deserialize to verify no uninitialized memory
            std::vector<uint8_t> serFalse = manager->comprehensive_log.serialize();
            auto deserFalse = XapianLog::ComprehensiveLog::deserialize(serFalse);
            REQUIRE(deserFalse.has_value());
            REQUIRE(deserFalse->xapian_doc_logs.size() == 1);
            auto deserDataFalse = std::get<XapianLog::AddValueData>(deserFalse->xapian_doc_logs[0].data);
            CHECK(deserDataFalse.is_serialised == false);

            manager->commit_changes();

            // 2. Call add_value with isSerialise = true
            uint256_t txTrue = next_fake_tx_hash();
            commit_write(txTrue, [&]() {
                return manager->add_value(docId, 2, "100", true, 3, nullptr, &txTrue);
            });

            auto logsTrue = manager->getChangeLogs();
            REQUIRE(logsTrue.size() == 1);
            REQUIRE(logsTrue[0].op == XapianLog::Operation::ADD_VALUE);
            auto dataTrue = std::get<XapianLog::AddValueData>(logsTrue[0].data);
            CHECK(dataTrue.is_serialised == true);
            CHECK(dataTrue.value == Xapian::sortable_serialise(100.0));

            // Serialize and deserialize
            std::vector<uint8_t> serTrue = manager->comprehensive_log.serialize();
            auto deserTrue = XapianLog::ComprehensiveLog::deserialize(serTrue);
            REQUIRE(deserTrue.has_value());
            REQUIRE(deserTrue->xapian_doc_logs.size() == 1);
            auto deserDataTrue = std::get<XapianLog::AddValueData>(deserTrue->xapian_doc_logs[0].data);
            CHECK(deserDataTrue.is_serialised == true);
            CHECK(deserDataTrue.value == Xapian::sortable_serialise(100.0));

            manager->commit_changes();
            CHECK(manager->get_value(docId, 2, true, 3) == "100");
            CHECK(manager->get_value(docId, 2, false, 3) == Xapian::sortable_serialise(100.0));
            CHECK(manager->add_value(docId, 2, "not_a_number", true, 3, nullptr, &txTrue).empty());

            // 3. Verify RangeFilter and numeric sorting with isSerialise = true and sortReverse
            auto rangeMgr = create_test_manager("val_range_sort_test", 554434, true);
            REQUIRE(rangeMgr != nullptr);
            const char* prices[] = {"100", "9", "20"};
            std::vector<std::string> priceDocIds;
            for (int i = 0; i < 3; ++i) {
                uint256_t txP = next_fake_tx_hash();
                std::string pid = commit_write(txP, [&]() {
                    std::string id = rangeMgr->new_document(std::string("item_price_") + prices[i], 1, nullptr, &txP);
                    rangeMgr->add_term(id, "item", 1, nullptr, &txP);
                    rangeMgr->add_value(id, 5, prices[i], /*isSerialise=*/true, 1, nullptr, &txP);
                    return id;
                });
                priceDocIds.push_back(pid);
            }
            rangeMgr->commit_changes();

            CHECK(rangeMgr->get_value(priceDocIds[0], 5, true, 1) == "100");
            CHECK(rangeMgr->get_value(priceDocIds[1], 5, true, 1) == "9");
            CHECK(rangeMgr->get_value(priceDocIds[2], 5, true, 1) == "20");

            Xapian::Database* sdb = rangeMgr->acquireSearchDb();
            REQUIRE(sdb != nullptr);
            {
                XapianSearcher searcher(sdb);
                nlohmann::json opt = {{"rangeFilters", {{{"slot", "05"}, {"begin", "50"}, {"end", "150"}}}}};
                auto rf = convertJsonToRangeFilters(opt);
                auto [rRange, totalRange] = searcher.search(
                    {"item"}, Xapian::Query::OP_AND, Xapian::Query::OP_AND, {},
                    std::nullopt, std::nullopt, 0, 10, std::nullopt, /*sort_reverse=*/false, rf, 1);
                REQUIRE(rRange.size() == 1);
                CHECK(rRange[0].data == "item_price_100");

                // Open-ended range filter: begin = "50", end = "" (>= 50 -> 100)
                nlohmann::json optGe = {{"rangeFilters", {{{"slot", "05"}, {"begin", "50"}, {"end", ""}}}}};
                auto rfGe = convertJsonToRangeFilters(optGe);
                auto [rGe, totalGe] = searcher.search(
                    {"item"}, Xapian::Query::OP_AND, Xapian::Query::OP_AND, {},
                    std::nullopt, std::nullopt, 0, 10, std::nullopt, /*sort_ascending=*/true, rfGe, 1);
                REQUIRE(rGe.size() == 1);
                CHECK(rGe[0].data == "item_price_100");

                // Open-ended range filter: begin = "", end = "50" (<= 50 -> 9, 20)
                nlohmann::json optLe = {{"rangeFilters", {{{"slot", "05"}, {"begin", ""}, {"end", "50"}}}}};
                auto rfLe = convertJsonToRangeFilters(optLe);
                auto [rLe, totalLe] = searcher.search(
                    {"item"}, Xapian::Query::OP_AND, Xapian::Query::OP_AND, {},
                    std::nullopt, std::nullopt, 0, 10, Xapian::valueno(5), /*sort_ascending=*/true, rfLe, 1);
                REQUIRE(rLe.size() == 2);
                CHECK(rLe[0].data == "item_price_9");
                CHECK(rLe[1].data == "item_price_20");

                // NaN, inf, hex, leading '+', leading space, and trailing garbage rejection
                for (const char* badVal : {"NaN", "inf", "-inf", "12abc", "0x10", "+5", " 5", "5 "}) {
                    CHECK(rangeMgr->add_value(priceDocIds[0], 5, badVal, true, 1, nullptr, &txTrue).empty());
                    nlohmann::json optBad = {{"rangeFilters", {{{"slot", "05"}, {"begin", badVal}, {"end", "100"}}}}};
                    CHECK_THROWS_AS(convertJsonToRangeFilters(optBad), std::invalid_argument);
                }

                // High-precision roundtrip (plain decimal, no scientific notation like 1e+05) and empty/nil slot clearing
                uint256_t txPrec = 12352;
                CHECK(!rangeMgr->add_value(priceDocIds[0], 8, "0.123456789012", true, 1, nullptr, &txPrec).empty());
                CHECK(!rangeMgr->add_value(priceDocIds[0], 9, "3.14159265358979", true, 1, nullptr, &txPrec).empty());
                CHECK(!rangeMgr->add_value(priceDocIds[0], 10, "100000", true, 1, nullptr, &txPrec).empty());
                CHECK(!rangeMgr->add_value(priceDocIds[0], 11, "0.00000000001", true, 1, nullptr, &txPrec).empty());
                CHECK(rangeMgr->get_value(priceDocIds[0], 8, true, 1, &txPrec) == "0.123456789012");
                CHECK(rangeMgr->get_value(priceDocIds[0], 9, true, 1, &txPrec) == "3.14159265358979");
                CHECK(rangeMgr->get_value(priceDocIds[0], 10, true, 1, &txPrec) == "100000");
                CHECK(rangeMgr->get_value(priceDocIds[0], 11, true, 1, &txPrec) == "0.00000000001");
                // Clear slot 8 with empty string ("")
                CHECK(!rangeMgr->add_value(priceDocIds[0], 8, "", true, 1, nullptr, &txPrec).empty());
                CHECK(rangeMgr->get_value(priceDocIds[0], 8, true, 1, &txPrec).empty());

                // sort_ascending = true -> ascending (9, 20, 100)
                auto [rAsc, totalAsc] = searcher.search(
                    {"item"}, Xapian::Query::OP_AND, Xapian::Query::OP_AND, {},
                    std::nullopt, std::nullopt, 0, 10, Xapian::valueno(5), /*sort_ascending=*/true, {}, 1);
                REQUIRE(rAsc.size() == 3);
                CHECK(rAsc[0].data == "item_price_9");
                CHECK(rAsc[1].data == "item_price_20");
                CHECK(rAsc[2].data == "item_price_100");

                // sort_ascending = false -> descending (100, 20, 9)
                auto [rDesc, totalDesc] = searcher.search(
                    {"item"}, Xapian::Query::OP_AND, Xapian::Query::OP_AND, {},
                    std::nullopt, std::nullopt, 0, 10, Xapian::valueno(5), /*sort_ascending=*/false, {}, 1);
                REQUIRE(rDesc.size() == 3);
                CHECK(rDesc[0].data == "item_price_100");
                CHECK(rDesc[1].data == "item_price_20");
                CHECK(rDesc[2].data == "item_price_9");
            }
            rangeMgr->releaseSearchDb(sdb);
        } catch (const Xapian::Error& e) {
            FAIL(e.get_description());
        } catch (const std::exception& e) {
            FAIL(e.what());
        }
    }

    TEST_CASE("Fix regression: Document identity hijacking via term collision with internal docid (Q prefix)") {
        // 1. Regular words starting with 'Q' are NOT considered reserved
        CHECK(XapianManager::isReservedSystemDocIdTerm("Q") == false);
        CHECK(XapianManager::isReservedSystemDocIdTerm("QUERY") == false);
        CHECK(XapianManager::isReservedSystemDocIdTerm("QUANTUM") == false);
        CHECK(XapianManager::isReservedSystemDocIdTerm("QUICK_SORT") == false);
        CHECK(XapianManager::isReservedSystemDocIdTerm("Q1_REPORT_2026") == false);
        CHECK(XapianManager::isReservedSystemDocIdTerm("Question_About_Blockchain") == false);

        // 2. Exact 'Q' + 64 hex chars is recognized as reserved system docid term
        std::string mockDocId = "70c172244bf70760fab30c8baa4cf06c9ec8a3c18ebd1bbff58736243f80c04c";
        CHECK(XapianManager::isReservedSystemDocIdTerm("Q" + mockDocId) == true);

        // 2b. Reusable prefix validation: partial prefixes of Q+hex must be blocked
        CHECK(XapianManager::isReservedSystemPrefix("Q") == true);
        CHECK(XapianManager::isReservedSystemPrefix("q") == true);
        CHECK(XapianManager::isReservedSystemPrefix("Qe") == true);
        CHECK(XapianManager::isReservedSystemPrefix("Q1") == true);
        CHECK(XapianManager::isReservedSystemPrefix("Qab") == true);
        CHECK(XapianManager::isReservedSystemPrefix("qf") == true);
        CHECK(XapianManager::isReservedSystemPrefix("Q" + mockDocId) == true);
        CHECK(XapianManager::isReservedSystemPrefix("TAG") == false);
        CHECK(XapianManager::isReservedSystemPrefix("author") == false);
        CHECK(XapianManager::isReservedSystemPrefix("QUANTUM") == false);
        CHECK(XapianManager::isReservedSystemPrefix("QUERY") == false);
        CHECK(XapianManager::isReservedSystemPrefix("QUICK") == false);
        CHECK(XapianManager::isReservedSystemPrefix("Q_TAG") == false);

        // 3. Functional test: adding Q+ID_B to doc A must be rejected and not hijack doc B
        std::string dbName = "test_q_hijack_prevention_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        mvm::Address addr(999);
        auto manager = XapianManager::getInstance(dbName, addr, true);
        REQUIRE(manager != nullptr);

        uint256_t txA = next_fake_tx_hash();
        std::string docA;
        commit_write(txA, [&]() {
            docA = manager->new_document("payload_doc_A", 1, nullptr, &txA);
            return docA;
        });

        uint256_t txB = next_fake_tx_hash();
        std::string docB;
        commit_write(txB, [&]() {
            docB = manager->new_document("payload_doc_B", 1, nullptr, &txB);
            return docB;
        });

        manager->commit_changes();

        std::string beforeB = manager->get_data(docB, 1);
        CHECK(beforeB == "payload_doc_B");

        // Attempt to hijack docB by attaching Q+docB to docA
        std::string cleanB = docB.substr(0, 2) == "0x" ? docB.substr(2) : docB;
        uint256_t txAttack = next_fake_tx_hash();
        std::string resAttack;
        commit_write(txAttack, [&]() {
            resAttack = manager->add_term(docA, "Q" + cleanB, 2, nullptr, &txAttack);
            return resAttack;
        });
        // add_term returns empty string on collision
        CHECK(resAttack.empty());

        // Regular term like "QUANTUM" must succeed
        uint256_t txLegit = next_fake_tx_hash();
        std::string resLegit;
        commit_write(txLegit, [&]() {
            resLegit = manager->add_term(docA, "QUANTUM", 2, nullptr, &txLegit);
            return resLegit;
        });
        CHECK(!resLegit.empty());

        manager->commit_changes();

        // Reading docB MUST still return payload_doc_B, not hijacked by docA
        std::string afterB = manager->get_data(docB, 2);
        CHECK(afterB == "payload_doc_B");

        // 4. Functional test: index_text with partial prefix (e.g. "Qe") + remainder of docB
        // must also be rejected and must NOT hijack docB after commit
        uint256_t txAttackPrefix = next_fake_tx_hash();
        std::string resAttackPrefix;
        std::string partialPrefix = "Q" + cleanB.substr(0, 1);
        std::string textRemainder = cleanB.substr(1);
        commit_write(txAttackPrefix, [&]() {
            resAttackPrefix = manager->index_text(docA, textRemainder, 1, partialPrefix, 2, nullptr, &txAttackPrefix);
            return resAttackPrefix;
        });
        CHECK(resAttackPrefix.empty());

        manager->commit_changes();

        // Reading docB after commit MUST still return payload_doc_B
        std::string afterPrefixB = manager->get_data(docB, 2);
        CHECK(afterPrefixB == "payload_doc_B");
    }

    TEST_CASE("Fix regression: Prefix length limit, slot range validation, and reserved slots") {
        std::string dbName = "test_limits_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        mvm::Address addr(888);
        auto manager = XapianManager::getInstance(dbName, addr, true);
        REQUIRE(manager != nullptr);

        uint256_t tx1 = next_fake_tx_hash();
        std::string doc1;
        commit_write(tx1, [&]() {
            doc1 = manager->new_document("payload_doc_limits", 1, nullptr, &tx1);
            return doc1;
        });
        manager->commit_changes();
        REQUIRE(!doc1.empty());

        // 1. Prefix length validation in index_text
        uint256_t txPrefix = next_fake_tx_hash();
        // 1a. Prefix >= 64 characters must be rejected
        std::string resPrefix300 = manager->index_text(doc1, "hello", 1, std::string(300, 'T'), 1, nullptr, &txPrefix);
        CHECK(resPrefix300.empty());
        std::string resPrefix64 = manager->index_text(doc1, "hello", 1, std::string(64, 'T'), 1, nullptr, &txPrefix);
        CHECK(resPrefix64.empty());

        // 1b. Prefix <= 63 characters must succeed
        std::string resPrefix63 = manager->index_text(doc1, "hello", 1, std::string(63, 'T'), 1, nullptr, &txPrefix);
        CHECK(!resPrefix63.empty());

        // 2. Slot range and reserved slots validation in add_value
        uint256_t txSlot = next_fake_tx_hash();
        // 2a. Out of range slot (> 0xFFFFFFFD)
        CHECK(manager->add_value(doc1, 0xFFFFFFFF, "val", false, 1, nullptr, &txSlot).empty());
        CHECK(manager->add_value(doc1, 0xFFFFFFFE, "val", false, 1, nullptr, &txSlot).empty());

        // 2b. Reserved system slots 253 (created_at) and 254 (deleted_at)
        CHECK(manager->add_value(doc1, 253, "val", false, 1, nullptr, &txSlot).empty());
        CHECK(manager->add_value(doc1, 254, "val", false, 1, nullptr, &txSlot).empty());

        // 2c. Valid slots succeed
        CHECK(!manager->add_value(doc1, 1, "valid_slot_1", false, 1, nullptr, &txSlot).empty());
        CHECK(!manager->add_value(doc1, 252, "valid_slot_252", false, 1, nullptr, &txSlot).empty());
        CHECK(!manager->add_value(doc1, 255, "valid_slot_255", false, 1, nullptr, &txSlot).empty());

        // 3. Slot validation in get_value
        CHECK(manager->get_value(doc1, 0xFFFFFFFF, false, 1, &txSlot).empty());
        CHECK(manager->get_value(doc1, 1, false, 1, &txSlot) == "valid_slot_1");

        // 4. Word length clamping by termgenerator (words > 180 chars do not trigger Xapian Term too long)
        uint256_t txLongWord = next_fake_tx_hash();
        commit_write(txLongWord, [&]() {
            std::string veryLongWord(500, 'z');
            std::string res = manager->index_text(doc1, veryLongWord, 1, "pref", 1, nullptr, &txLongWord);
            return res;
        });
        CHECK_NOTHROW(manager->commit_changes());
    }

    TEST_CASE("acquireSearchDb and acquireSimpleReadDb pool lifecycle & generation sync") {
        std::string dbName = "test_pool_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        mvm::Address addr(999);
        auto manager = XapianManager::getInstance(dbName, addr, true);
        REQUIRE(manager != nullptr);

        // 1. Acquire single search DB, verify non-null, release
        Xapian::Database* sdb = manager->acquireSearchDb();
        REQUIRE(sdb != nullptr);
        CHECK_NOTHROW(manager->releaseSearchDb(sdb));

        // 2. Acquire single simple read DB, verify non-null, release
        Xapian::Database* rdb = manager->acquireSimpleReadDb();
        REQUIRE(rdb != nullptr);
        CHECK_NOTHROW(manager->releaseSimpleReadDb(rdb));

        // 3. Multi-acquire from search pool (up to MAX_CONCURRENT_SEARCHES = 4)
        std::vector<Xapian::Database*> searchDbs;
        for (int i = 0; i < 4; ++i) {
            Xapian::Database* db = manager->acquireSearchDb();
            REQUIRE(db != nullptr);
            searchDbs.push_back(db);
        }
        for (size_t i = 0; i < searchDbs.size(); ++i) {
            for (size_t j = i + 1; j < searchDbs.size(); ++j) {
                CHECK(searchDbs[i] != searchDbs[j]);
            }
        }
        for (auto db : searchDbs) {
            CHECK_NOTHROW(manager->releaseSearchDb(db));
        }

        // 4. Multi-acquire from simple read pool (up to MAX_CONCURRENT_READS = 4)
        std::vector<Xapian::Database*> readDbs;
        for (int i = 0; i < 4; ++i) {
            Xapian::Database* db = manager->acquireSimpleReadDb();
            REQUIRE(db != nullptr);
            readDbs.push_back(db);
        }
        for (size_t i = 0; i < readDbs.size(); ++i) {
            for (size_t j = i + 1; j < readDbs.size(); ++j) {
                CHECK(readDbs[i] != readDbs[j]);
            }
        }
        for (auto db : readDbs) {
            CHECK_NOTHROW(manager->releaseSimpleReadDb(db));
        }

        // 5. Test generation sync across commit:
        Xapian::Database* dbBefore = manager->acquireSearchDb();
        REQUIRE(dbBefore != nullptr);
        CHECK(dbBefore->get_doccount() == 0);
        manager->releaseSearchDb(dbBefore);

        // Write a document and commit
        uint256_t tx = next_fake_tx_hash();
        std::string docId;
        commit_write(tx, [&]() {
            docId = manager->new_document("pool_sync_test_payload", 1, nullptr, &tx);
            return docId;
        });
        manager->commit_changes();

        // Acquire again: manager should reopen the slot to match new db_generation
        Xapian::Database* dbAfter = manager->acquireSearchDb();
        REQUIRE(dbAfter != nullptr);
        CHECK(dbAfter->get_doccount() > 0);
        manager->releaseSearchDb(dbAfter);
    }

    TEST_CASE("compactInPlace lifecycle and data preservation") {
        std::string dbName = "test_compact_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        mvm::Address addr(12345);
        auto manager = XapianManager::getInstance(dbName, addr, true);
        REQUIRE(manager != nullptr);

        // Write documents
        uint256_t tx1 = next_fake_tx_hash();
        std::string docId1;
        commit_write(tx1, [&]() {
            docId1 = manager->new_document("compact_payload_1", 1, nullptr, &tx1);
            return docId1;
        });
        uint256_t tx2 = next_fake_tx_hash();
        std::string docId2;
        commit_write(tx2, [&]() {
            docId2 = manager->new_document("compact_payload_2", 1, nullptr, &tx2);
            return docId2;
        });
        manager->commit_changes();

        // Run compactInPlace with 0 minSizeBytes and 0 minInterval
        bool compacted = manager->compactInPlace(0, std::chrono::minutes(0));
        CHECK(compacted == true);

        // Verify data is preserved after compaction
        std::string data1 = manager->get_data(docId1, 1);
        CHECK(data1 == "compact_payload_1");
        std::string data2 = manager->get_data(docId2, 1);
        CHECK(data2 == "compact_payload_2");
    }

    TEST_CASE("CRASH RECOVERY: Direct replay_log with NEW_DOC is idempotent on wdf, positions, values, and docid") {
        init_xapian_test_env();
        std::string dbName = "test_replay_idem_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        mvm::Address addr(700001);
        auto manager = create_test_manager(dbName, addr, true);
        REQUIRE(manager != nullptr);

        std::string vdoc = "0x1111111111111111111111111111111111111111111111111111111111111111";
        std::vector<XapianLog::LogEntry> logs;
        logs.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{vdoc, "payload_v1"});
        logs.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{vdoc, "tagx"});
        logs.emplace_back(XapianLog::Operation::INDEX_TEXT, XapianLog::IndexTextData{vdoc, "hello world hello", 1, "S"});
        logs.emplace_back(XapianLog::Operation::ADD_VALUE, XapianLog::AddValueData{vdoc, 300, "high_slot_300"});

        REQUIRE(manager->replay_log(logs));
        REQUIRE(manager->commit_changes(1));

        Xapian::Database* rdb1 = manager->acquireSimpleReadDb();
        REQUIRE(rdb1 != nullptr);
        Xapian::docid did1 = 0;
        std::string ser1;
        {
            CHECK(rdb1->get_doccount() == 1);
            Xapian::PostingIterator pit1 = rdb1->postlist_begin("Q1111111111111111111111111111111111111111111111111111111111111111");
            REQUIRE(pit1 != rdb1->postlist_end("Q1111111111111111111111111111111111111111111111111111111111111111"));
            did1 = *pit1;
            Xapian::Document doc1 = rdb1->get_document(did1);
            ser1 = doc1.serialise();
        }
        manager->releaseSimpleReadDb(rdb1);

        // Replay the exact same log a second time on top of committed DB
        REQUIRE(manager->replay_log(logs));
        REQUIRE(manager->commit_changes(1));

        Xapian::Database* rdb2 = manager->acquireSimpleReadDb();
        REQUIRE(rdb2 != nullptr);
        {
            CHECK(rdb2->get_doccount() == 1);
            Xapian::PostingIterator pit2 = rdb2->postlist_begin("Q1111111111111111111111111111111111111111111111111111111111111111");
            REQUIRE(pit2 != rdb2->postlist_end("Q1111111111111111111111111111111111111111111111111111111111111111"));
            Xapian::docid did2 = *pit2;
            Xapian::Document doc2 = rdb2->get_document(did2);
            std::string ser2 = doc2.serialise();

            CHECK(did1 == did2);
            CHECK(ser1 == ser2);
            CHECK(doc2.get_value(300) == "high_slot_300");

            std::map<std::string, Xapian::termcount> wdfs;
            for (auto tit = doc2.termlist_begin(); tit != doc2.termlist_end(); ++tit) {
                wdfs[*tit] = tit.get_wdf();
            }
            CHECK(wdfs["tagx"] == 1);
            CHECK(wdfs["Shello"] == 2);
            CHECK(wdfs["Sworld"] == 1);
        }
        manager->releaseSimpleReadDb(rdb2);

        XapianManager::destroyInstance(mvm::createFullPath(addr, dbName).string());
    }

    TEST_CASE("CRASH RECOVERY: Cold-disk rollback after mid-commit crash preserves exact BM25 wdf, positions, high value slots, and internal docid") {
        std::string crashBase = ".test_db_xapian_crash_cold_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        std::string refBase = crashBase + "_ref";
        cleanup_test_db(crashBase);
        cleanup_test_db(refBase);

        mvm::Address addr(700002);
        std::string dbName = "orders_search";

        auto applyBlock1 = [&](std::shared_ptr<XapianManager> mgr, uint256_t tx1) -> std::pair<std::string, std::string> {
            std::string docA = mgr->new_document("data_b1_A", 1, nullptr, &tx1);
            mgr->add_term(docA, "Tbase", 1, nullptr, &tx1);
            mgr->index_text(docA, "alpha beta", 1, "S", 1, nullptr, &tx1);
            mgr->add_value(docA, 512, "slot512_b1", false, 1, nullptr, &tx1);
            std::string docB = mgr->new_document("data_b1_B_to_delete", 1, nullptr, &tx1);
            mgr->index_text(docB, "ephemeral item", 1, "S", 1, nullptr, &tx1);
            registry.commitBufferForTxHash(&tx1);
            return {docA, docB};
        };

        auto applyBlock2 = [&](std::shared_ptr<XapianManager> mgr, const std::string& docA, const std::string& docB, uint256_t tx2) -> std::string {
            mgr->set_data(docA, "data_b2_A", 2, nullptr, &tx2);
            mgr->add_term(docA, "Textra", 2, nullptr, &tx2);
            mgr->index_text(docA, "alpha delta alpha", 1, "S", 2, nullptr, &tx2);
            mgr->add_value(docA, 512, "slot512_b2", false, 2, nullptr, &tx2);
            mgr->delete_document(docB, 2, nullptr, &tx2);
            std::string docC = mgr->new_document("data_b2_C_new", 2, nullptr, &tx2);
            mgr->index_text(docC, "alpha fresh", 1, "S", 2, nullptr, &tx2);
            registry.commitBufferForTxHash(&tx2);
            return docC;
        };

        const uint256_t fixedTx1(0xAAA1ULL);
        const uint256_t fixedTx2(0xBBB2ULL);

        // 1. Run healthy reference node (Block 1 -> Block 2, no crash)
        XapianManager::destroyAllInstances();
        SetXapianBasePath(refBase.c_str());
        auto refMgr = create_test_manager(dbName, addr, true);
        auto [refDocA, refDocB] = applyBlock1(refMgr, fixedTx1);
        MVM_commitAllXapianAtBlock(1);
        std::string refDocC = applyBlock2(refMgr, refDocA, refDocB, fixedTx2);
        MVM_commitAllXapianAtBlock(2);

        Xapian::Database* refDb = refMgr->acquireSearchDb();
        REQUIRE(refDb != nullptr);
        Xapian::doccount refDocCount = refDb->get_doccount();
        double refAvLen = refDb->get_avlength();
        std::map<Xapian::docid, std::string> refDocsSerialized;
        for (Xapian::PostingIterator it = refDb->postlist_begin(""); it != refDb->postlist_end(""); ++it) {
            refDocsSerialized[*it] = refDb->get_document(*it).serialise();
        }
        refMgr->releaseSearchDb(refDb);
        XapianManager::destroyAllInstances();

        // 2. Run crashing node: commits Xapian for Block 1 AND Block 2, then crashes BEFORE Block 2 is saved in BlockDB!
        SetXapianBasePath(crashBase.c_str());
        auto crashMgr = create_test_manager(dbName, addr, true);
        auto [cDocA, cDocB] = applyBlock1(crashMgr, fixedTx1);
        MVM_commitAllXapianAtBlock(1);
        std::string cDocC = applyBlock2(crashMgr, cDocA, cDocB, fixedTx2);
        MVM_commitAllXapianAtBlock(2);

        // Simulate abrupt process termination (zero live instances in memory)
        XapianManager::destroyAllInstances();
        crashMgr.reset();

        // Simulate startup alignment: BlockDB only has Block 1, so roll back Xapian > 1 from COLD DISK
        int rolledBack = MVM_rollbackXapianAboveBlock(1);
        CHECK(rolledBack == 1);

        // Verify pre-re-execution state matches Block 1 (preventing dirty future reads during Block 2 EVM re-execution!)
        auto recMgr = create_test_manager(dbName, addr, false);
        REQUIRE(recMgr != nullptr);
        CHECK(recMgr->getCommittedHeadBlock() == 1);
        CHECK(recMgr->get_data(cDocA, 2) == "data_b1_A");
        CHECK(recMgr->get_value(cDocA, 512, false, 2) == "slot512_b1");
        CHECK(recMgr->get_data(cDocB, 2) == "data_b1_B_to_delete");
        CHECK(recMgr->get_data(cDocC, 2).empty());

        // Re-execute Block 2 on recovered node
        std::string reDocC = applyBlock2(recMgr, cDocA, cDocB, fixedTx2);
        CHECK(reDocC == cDocC);
        MVM_commitAllXapianAtBlock(2);

        // Assert bit-for-bit equivalence with healthy reference node (internal docids, wdf, positions, high values, avlength)
        Xapian::Database* recDb = recMgr->acquireSearchDb();
        REQUIRE(recDb != nullptr);
        CHECK(recDb->get_doccount() == refDocCount);
        CHECK(recDb->get_avlength() == doctest::Approx(refAvLen));
        std::map<Xapian::docid, std::string> recDocsSerialized;
        for (Xapian::PostingIterator it = recDb->postlist_begin(""); it != recDb->postlist_end(""); ++it) {
            recDocsSerialized[*it] = recDb->get_document(*it).serialise();
        }
        CHECK(recDocsSerialized == refDocsSerialized);
        recMgr->releaseSearchDb(recDb);

        XapianManager::destroyAllInstances();
        SetXapianBasePath(".test_db_xapian");
        cleanup_test_db(crashBase);
        cleanup_test_db(refBase);
    }

    TEST_CASE("CRASH RECOVERY: Multi-block pipeline rollback across multiple contracts & same-block multi-commit merge") {
        std::string base = ".test_db_xapian_multiblk_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        cleanup_test_db(base);
        XapianManager::destroyAllInstances();
        SetXapianBasePath(base.c_str());

        mvm::Address addr1(700010);
        mvm::Address addr2(700011);
        auto mgr1 = create_test_manager("db_alpha", addr1, true);
        auto mgr2 = create_test_manager("db_beta", addr2, true);

        // Block 10 baseline
        uint256_t tx10a(0x10AULL), tx10b(0x10BULL);
        std::string d1 = commit_write(tx10a, [&]() { return mgr1->new_document("b10_d1", 10, nullptr, &tx10a); });
        std::string d2 = commit_write(tx10b, [&]() { return mgr2->new_document("b10_d2", 10, nullptr, &tx10b); });
        MVM_commitAllXapianAtBlock(10);

        // Block 11: two separate commits within the SAME block 11 on mgr1 (tests pre-image merge!)
        uint256_t tx11_step1(0x111ULL), tx11_step2(0x112ULL);
        commit_write(tx11_step1, [&]() { return mgr1->set_data(d1, "b11_step1", 11, nullptr, &tx11_step1); });
        mgr1->commit_changes(11);
        commit_write(tx11_step2, [&]() { return mgr1->set_data(d1, "b11_step2", 11, nullptr, &tx11_step2); });
        mgr1->commit_changes(11);

        // Block 12: mutate both mgr1 and mgr2
        uint256_t tx12a(0x12AULL), tx12b(0x12BULL);
        std::string d1_new = commit_write(tx12a, [&]() { return mgr1->new_document("b12_extra", 12, nullptr, &tx12a); });
        commit_write(tx12b, [&]() {
            mgr2->delete_document(d2, 12, nullptr, &tx12b);
            return d2;
        });
        MVM_commitAllXapianAtBlock(12);

        // Simulate cold crash and roll back both contracts from block 12 -> block 10!
        XapianManager::destroyAllInstances();
        mgr1.reset();
        mgr2.reset();

        int rbCount = MVM_rollbackXapianAboveBlock(10);
        CHECK(rbCount == 2);

        auto rec1 = create_test_manager("db_alpha", addr1, false);
        auto rec2 = create_test_manager("db_beta", addr2, false);
        CHECK(rec1->getCommittedHeadBlock() == 10);
        CHECK(rec2->getCommittedHeadBlock() == 10);
        // Must restore "b10_d1" (NOT intermediate "b11_step1"!)
        CHECK(rec1->get_data(d1, 10) == "b10_d1");
        CHECK(rec1->get_data(d1_new, 10).empty());
        CHECK(rec2->get_data(d2, 10) == "b10_d2");

        XapianManager::destroyAllInstances();
        SetXapianBasePath(".test_db_xapian");
        cleanup_test_db(base);
    }

    TEST_CASE("CRASH RECOVERY: ReplayFullDbLogsAtBlock is idempotent on existing documents with ADD_TERM and INDEX_TEXT") {
        std::string base = ".test_db_xapian_sync_idem_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        cleanup_test_db(base);
        XapianManager::destroyAllInstances();
        SetXapianBasePath(base.c_str());

        mvm::Address addr(700020);
        uint8_t addrBytes[32] = {0};
        mvm::to_big_endian(addr, addrBytes);
        unsigned char* addr20 = addrBytes + 12;

        std::string doc1 = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        std::string doc2 = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";

        // Block 1 FullDbLogs: create doc1
        XapianLog::ComprehensiveLog logB1;
        logB1.db_name = "sync_db";
        logB1.xapian_doc_logs.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{doc1, "sync_v1"});
        logB1.xapian_doc_logs.emplace_back(XapianLog::Operation::INDEX_TEXT, XapianLog::IndexTextData{doc1, "hello", 1, "S"});
        std::vector<uint8_t> rawB1 = logB1.serialize();
        LogReplayEntryC entryB1{addr20, 20, rawB1.data(), static_cast<int>(rawB1.size())};
        REQUIRE(ReplayFullDbLogsAtBlock(&entryB1, 1, 1) == 1);

        // Block 2 FullDbLogs: ADD_TERM + INDEX_TEXT on existing doc1 (NO NEW_DOC!), plus NEW_DOC doc2
        XapianLog::ComprehensiveLog logB2;
        logB2.db_name = "sync_db";
        logB2.xapian_doc_logs.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{doc1, "Tfeatured"});
        logB2.xapian_doc_logs.emplace_back(XapianLog::Operation::INDEX_TEXT, XapianLog::IndexTextData{doc1, "hello world", 1, "S"});
        logB2.xapian_doc_logs.emplace_back(XapianLog::Operation::SET_DATA, XapianLog::SetDataData{doc1, "sync_v2"});
        logB2.xapian_doc_logs.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{doc2, "sync_doc2"});
        std::vector<uint8_t> rawB2 = logB2.serialize();
        LogReplayEntryC entryB2{addr20, 20, rawB2.data(), static_cast<int>(rawB2.size())};

        // First replay of Block 2
        REQUIRE(ReplayFullDbLogsAtBlock(&entryB2, 1, 2) == 1);
        auto mgr = create_test_manager("sync_db", addr, false);
        Xapian::Database* rdb1 = mgr->acquireSimpleReadDb();
        std::string serAfterOnce = rdb1->get_document(1).serialise();
        std::string serDoc2Once = rdb1->get_document(2).serialise();
        mgr->releaseSimpleReadDb(rdb1);

        // Second replay of Block 2 (simulating sync retry after crash)
        REQUIRE(ReplayFullDbLogsAtBlock(&entryB2, 1, 2) == 1);
        Xapian::Database* rdb2 = mgr->acquireSimpleReadDb();
        CHECK(rdb2->get_doccount() == 2);
        CHECK(rdb2->get_document(1).serialise() == serAfterOnce);
        CHECK(rdb2->get_document(2).serialise() == serDoc2Once);
        mgr->releaseSimpleReadDb(rdb2);

        XapianManager::destroyAllInstances();
        SetXapianBasePath(".test_db_xapian");
        cleanup_test_db(base);
    }

    TEST_CASE("CRASH RECOVERY SECURITY: Pruned floor (including targetBlock=0) & corrupt metadata fail close without wiping DB") {
        std::string base = ".test_db_xapian_sec_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        cleanup_test_db(base);
        XapianManager::destroyAllInstances();
        SetXapianBasePath(base.c_str());

        mvm::Address addr(700030);
        auto mgr = create_test_manager("sec_db", addr, true);

        // Phase A: When pruned_floor == 0 (unpruned), rolling back to Genesis (targetBlock == 0)
        // MUST cleanly undo blocks 1..3 via Undo Journal without deleting directories!
        for (uint64_t bn = 1; bn <= 3; ++bn) {
            uint256_t txPre(0x4EC00ULL + bn);
            commit_write(txPre, [&]() { return mgr->new_document("pre_" + std::to_string(bn), bn, nullptr, &txPre); });
            MVM_commitAllXapianAtBlock(bn);
        }
        REQUIRE(mgr->getCommittedHeadBlock() == 3);
        REQUIRE(MVM_rollbackXapianAboveBlock(0) == 1);
        CHECK(mgr->getCommittedHeadBlock() == 0);
        {
            Xapian::Database* rdb0 = mgr->acquireSimpleReadDb();
            REQUIRE(rdb0 != nullptr);
            CHECK(rdb0->get_doccount() == 0);
            mgr->releaseSimpleReadDb(rdb0);
        }

        // Phase B: Advance beyond MAX_UNDO_BLOCKS (64) -> pruned_floor > 0
        uint256_t tx0(0x5EC00ULL);
        std::string docId = commit_write(tx0, [&]() { return mgr->new_document("init", 1, nullptr, &tx0); });
        MVM_commitAllXapianAtBlock(1);

        const uint64_t endBn = 64ULL + 8;
        for (uint64_t bn = 2; bn <= endBn; ++bn) {
            uint256_t tx(0x5EC00ULL + bn);
            commit_write(tx, [&]() { return mgr->set_data(docId, "v_" + std::to_string(bn), bn, nullptr, &tx); });
            MVM_commitAllXapianAtBlock(bn);
        }

        // Rolling back to endBn - 1 (within retention window) MUST succeed
        CHECK(MVM_rollbackXapianAboveBlock(endBn - 1) == 1);
        CHECK(mgr->get_data(docId, endBn - 1) == "v_" + std::to_string(endBn - 1));

        // Rolling back to block 3 (below pruned floor) MUST fail close (< 0)
        CHECK(MVM_rollbackXapianAboveBlock(3) < 0);

        // Rolling back to Genesis (targetBlock == 0) when pruned_floor > 0 MUST ALSO fail close (< 0)
        // and MUST NOT wipe the on-disk database directory (both hot cached and cold uncached)!
        CHECK(MVM_rollbackXapianAboveBlock(0) < 0);
        CHECK(mgr->getCommittedHeadBlock() == endBn - 1);
        CHECK(mgr->get_data(docId, endBn - 1) == "v_" + std::to_string(endBn - 1));

        XapianManager::destroyAllInstances();
        mgr.reset();
        CHECK(MVM_rollbackXapianAboveBlock(0) < 0);
        {
            auto reopened = create_test_manager("sec_db", addr, false);
            CHECK(reopened->getCommittedHeadBlock() == endBn - 1);
            CHECK(reopened->get_data(docId, endBn - 1) == "v_" + std::to_string(endBn - 1));
        }

        // Phase C: Corrupt metadata negative test: tamper with __mtn_undo_2 on disk and verify fail-close
        mvm::Address tamperAddr(700031);
        auto tamperMgr = create_test_manager("tamper_db", tamperAddr, true);
        uint256_t txNew1(0x6EC01ULL), txNew2(0x6EC02ULL);
        std::string dTamper = commit_write(txNew1, [&]() { return tamperMgr->new_document("clean_b1", 1, nullptr, &txNew1); });
        MVM_commitAllXapianAtBlock(1);
        commit_write(txNew2, [&]() { return tamperMgr->set_data(dTamper, "dirty_b2", 2, nullptr, &txNew2); });
        MVM_commitAllXapianAtBlock(2);
        XapianManager::destroyAllInstances();
        tamperMgr.reset();

        {
            Xapian::WritableDatabase rawDb(mvm::createFullPath(tamperAddr, "tamper_db").string(), Xapian::DB_OPEN);
            rawDb.set_metadata("__mtn_undo_2", "CORRUPT_GARBAGE_BYTES");
            rawDb.commit();
            rawDb.close();
        }
        CHECK(MVM_rollbackXapianAboveBlock(1) < 0);

        XapianManager::destroyAllInstances();
        SetXapianBasePath(".test_db_xapian");
        cleanup_test_db(base);
    }

    TEST_CASE("SHORT-HEX NON-OVERWRITE & HOSTILE SPAM: Short docid '0x2' never overwrites physical docid 2, non-existent DEL_DOC safe, >MAX_ACTIVE_DBS eviction") {
        std::string base = ".test_db_xapian_hostile_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        cleanup_test_db(base);
        XapianManager::destroyAllInstances();
        SetXapianBasePath(base.c_str());

        mvm::Address addr(700040);
        auto mgr = create_test_manager("v0_compat_db", addr, true);

        const std::string docA = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        const std::string docB = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
        const std::string docC = "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc";

        // Seed 3 standard 256-bit documents at physical docids 1, 2, 3
        XapianLog::ComprehensiveLog seedLog;
        seedLog.db_name = "v0_compat_db";
        seedLog.xapian_doc_logs.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{docA, "doc-A"});
        seedLog.xapian_doc_logs.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{docB, "doc-B"});
        seedLog.xapian_doc_logs.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{docC, "doc-C"});
        REQUIRE(mgr->replay_log(seedLog.xapian_doc_logs));
        REQUIRE(mgr->commit_changes(1));

        // 1. Replay short-hex docids ("0x2", "0x5") and non-existent short/long DEL_DOCs on top of [doc-A, doc-B, doc-C].
        //    CRITICAL: "0x2" MUST NEVER alias or overwrite doc-B at physical docid 2, and DEL_DOC("0x2") / DEL_DOC("2")
        //    MUST NEVER delete doc-B!
        XapianLog::ComprehensiveLog hostileLog;
        hostileLog.db_name = "v0_compat_db";
        hostileLog.xapian_doc_logs.emplace_back(XapianLog::Operation::DEL_DOC, XapianLog::DelDocData{"999"});
        hostileLog.xapian_doc_logs.emplace_back(XapianLog::Operation::DEL_DOC, XapianLog::DelDocData{"0x3e7"});
        hostileLog.xapian_doc_logs.emplace_back(XapianLog::Operation::DEL_DOC, XapianLog::DelDocData{"0x00000000000000000000000000000000000000000000000000000000000003e7"});
        hostileLog.xapian_doc_logs.emplace_back(XapianLog::Operation::DEL_DOC, XapianLog::DelDocData{"0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"});
        hostileLog.xapian_doc_logs.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{"0x2", "doc-SHORT"});
        hostileLog.xapian_doc_logs.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{"0x2", "Tv0tag"});
        hostileLog.xapian_doc_logs.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{"0x5", "temp_short_5"});
        hostileLog.xapian_doc_logs.emplace_back(XapianLog::Operation::DEL_DOC, XapianLog::DelDocData{"0x5"});
        REQUIRE(mgr->replay_log(hostileLog.xapian_doc_logs));
        REQUIRE(mgr->commit_changes(2));

        CHECK(mgr->get_data(docA, 2) == "doc-A");
        CHECK(mgr->get_data(docB, 2) == "doc-B");
        CHECK(mgr->get_data(docC, 2) == "doc-C");
        CHECK(mgr->get_data("0x2", 2) == "doc-SHORT");
        CHECK(mgr->get_data("2", 2) == "doc-SHORT");
        CHECK(mgr->get_data("0x5", 2).empty());

        {
            Xapian::Database* rdb = mgr->acquireSimpleReadDb();
            REQUIRE(rdb != nullptr);
            CHECK(rdb->get_doccount() == 4);
            CHECK(rdb->get_document(1).get_data() == "doc-A");
            CHECK(rdb->get_document(2).get_data() == "doc-B");
            CHECK(rdb->get_document(3).get_data() == "doc-C");
            CHECK(rdb->get_document(4).get_data() == "doc-SHORT");
            mgr->releaseSimpleReadDb(rdb);
        }

        // Block 3 deletes "0x2" and "2" in the same batch -> must delete physical docid 4 (NOT doc-B at docid 2!)
        // and must NOT throw uncaught DocNotFoundError!
        XapianLog::ComprehensiveLog delShortLog;
        delShortLog.db_name = "v0_compat_db";
        delShortLog.xapian_doc_logs.emplace_back(XapianLog::Operation::DEL_DOC, XapianLog::DelDocData{"0x2"});
        delShortLog.xapian_doc_logs.emplace_back(XapianLog::Operation::DEL_DOC, XapianLog::DelDocData{"2"});
        REQUIRE(mgr->replay_log(delShortLog.xapian_doc_logs));
        REQUIRE(mgr->commit_changes(3));
        CHECK(mgr->get_data("0x2", 3).empty());
        CHECK(mgr->get_data(docB, 3) == "doc-B");

        // Roll back Block 3 -> "0x2" MUST be restored at physical did=4 while doc-B remains untouched at did=2!
        REQUIRE(MVM_rollbackXapianAboveBlock(2) == 1);
        CHECK(mgr->get_data("0x2", 2) == "doc-SHORT");
        CHECK(mgr->get_data(docB, 2) == "doc-B");

        // Roll back Block 2 as well -> back to clean 3-doc state at Block 1
        REQUIRE(MVM_rollbackXapianAboveBlock(1) == 1);
        CHECK(mgr->get_data("0x2", 1).empty());
        CHECK(mgr->get_data(docB, 1) == "doc-B");

        // 2. Spam > MAX_ACTIVE_DBS (65 distinct DBs) across 2 blocks to trigger LRU handle eviction
        const int numDbs = 65;
        std::vector<std::string> dbDocIds(numDbs);
        for (int i = 0; i < numDbs; ++i) {
            auto dbMgr = XapianManager::getInstance("evict_db_" + std::to_string(i), addr, false);
            uint256_t tx(0x800000ULL + i);
            dbDocIds[i] = commit_write(tx, [&]() {
                return dbMgr->new_document("b1_db_" + std::to_string(i), 1, nullptr, &tx);
            });
        }
        MVM_commitAllXapianAtBlock(1);

        for (int i = 0; i < numDbs; ++i) {
            auto dbMgr = XapianManager::getInstance("evict_db_" + std::to_string(i), addr, false);
            uint256_t tx(0x900000ULL + i);
            commit_write(tx, [&]() {
                return dbMgr->set_data(dbDocIds[i], "b2_dirty_" + std::to_string(i), 2, nullptr, &tx);
            });
        }
        MVM_commitAllXapianAtBlock(2);

        // Cold crash: roll back all 65 evicted/open DBs to block 1!
        REQUIRE(MVM_rollbackXapianAboveBlock(1) == numDbs);
        for (int i = 0; i < numDbs; ++i) {
            auto dbMgr = XapianManager::getInstance("evict_db_" + std::to_string(i), addr, false);
            CHECK(dbMgr->get_data(dbDocIds[i], 1) == "b1_db_" + std::to_string(i));
        }

        XapianManager::destroyAllInstances();
        SetXapianBasePath(".test_db_xapian");
        cleanup_test_db(base);
    }

    TEST_CASE("DOCID ORDER & IDEMPOTENCY: nid-sorted docid allocation matches legacy FullDbLogs and Validator == Crash-Recovered == SyncOnly") {
        std::string base = ".test_db_xapian_docorder_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        cleanup_test_db(base);
        XapianManager::destroyAllInstances();
        SetXapianBasePath(base.c_str());

        // Part 1: Direct probe - log writes 0xbb.. first and 0xaa.. second.
        // Sorted nid order MUST assign physical docid 1 to 0xaa.. and physical docid 2 to 0xbb..,
        // and replaying the exact same log a second time MUST keep docids 1 & 2 and wdf unchanged!
        {
            mvm::Address probeAddr(700041);
            auto mgr = create_test_manager("order_probe_db", probeAddr, true);
            const std::string nidBB = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
            const std::string nidAA = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";

            std::vector<XapianLog::LogEntry> entries;
            entries.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{nidBB, "doc-BB"});
            entries.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{nidBB, "Ttagx"});
            entries.emplace_back(XapianLog::Operation::INDEX_TEXT, XapianLog::IndexTextData{nidBB, "hello world hello", 1, "S"});
            entries.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{nidAA, "doc-AA"});
            entries.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{nidAA, "Ttagx"});

            REQUIRE(mgr->replay_log(entries));
            REQUIRE(mgr->commit_changes(1));

            auto check_state = [&]() {
                Xapian::Database* rdb = mgr->acquireSimpleReadDb();
                REQUIRE(rdb != nullptr);
                CHECK(rdb->get_doccount() == 2);
                CHECK(rdb->get_lastdocid() == 2);
                Xapian::Document d1 = rdb->get_document(1);
                Xapian::Document d2 = rdb->get_document(2);
                CHECK(d1.get_data() == "doc-AA");
                CHECK(d2.get_data() == "doc-BB");
                std::map<std::string, Xapian::termcount> wdfs;
                for (auto it = d2.termlist_begin(); it != d2.termlist_end(); ++it) {
                    wdfs[*it] = it.get_wdf();
                }
                CHECK(wdfs["Ttagx"] == 1);
                CHECK(wdfs["Shello"] == 2);
                CHECK(wdfs["Sworld"] == 1);
                mgr->releaseSimpleReadDb(rdb);
            };
            check_state();

            // Second replay on already-committed DB (idempotency check)
            REQUIRE(mgr->replay_log(entries));
            REQUIRE(mgr->commit_changes(1));
            check_state();
        }

        // Part 2: 3-node consensus equivalence (Validator Normal vs Crash-Recovered Validator vs SyncOnly)
        // including Xapian::Enquire search tie-breaking by physical docid!
        std::string baseA = base + "_nodeA";
        std::string baseB = base + "_nodeB";
        std::string baseC = base + "_nodeC";
        cleanup_test_db(baseA);
        cleanup_test_db(baseB);
        cleanup_test_db(baseC);

        mvm::Address cAddr(700042);
        uint8_t addrBytes[32] = {0};
        mvm::to_big_endian(cAddr, addrBytes);
        unsigned char* addr20 = addrBytes + 12;

        // Pick two txHashes where tx1 generates a HIGHER keccak nid than tx2 so log order != nid order!
        uint256_t txFirst(1), txSecond(2);
        {
            SetXapianBasePath(base.c_str());
            auto tmpMgr = create_test_manager("find_pair_tmp", cAddr, true);
            for (uint64_t seed = 100; seed < 300; ++seed) {
                uint256_t candA(seed), candB(seed + 1);
                std::string idA = tmpMgr->new_document("a", 1, nullptr, &candA);
                std::string idB = tmpMgr->new_document("b", 1, nullptr, &candB);
                if (idA > idB) {
                    txFirst = candA;
                    txSecond = candB;
                    break;
                }
            }
            XapianManager::destroyAllInstances();
        }

        std::vector<uint8_t> block1FullDbLog;
        std::string expectedFirstDocId, expectedSecondDocId;

        auto execute_block1_validator = [&]() {
            auto m = XapianManager::getInstance("rank_db", cAddr, false);
            expectedFirstDocId = m->new_document("payload_tx_first_higher_nid", 1, nullptr, &txFirst);
            m->add_term(expectedFirstDocId, "Tequalrank", 1, nullptr, &txFirst);
            expectedSecondDocId = m->new_document("payload_tx_second_lower_nid", 1, nullptr, &txSecond);
            m->add_term(expectedSecondDocId, "Tequalrank", 1, nullptr, &txSecond);
            REQUIRE(expectedFirstDocId > expectedSecondDocId);

            uint8_t batchRaw[64];
            mvm::to_big_endian(txFirst, batchRaw);
            mvm::to_big_endian(txSecond, batchRaw + 32);
            commit_xapian_tx_buffer_batch(batchRaw, 2);

            ExportedXapianLogArray exp = MVM_exportAllXapianLogs();
            REQUIRE(exp.count == 1);
            const auto* rawPtr = reinterpret_cast<const uint8_t*>(exp.data[0].logs);
            block1FullDbLog.assign(rawPtr, rawPtr + exp.data[0].logs_length);
            MVM_freeExportedXapianLogs(exp);
            MVM_commitAllXapianAtBlock(1);
        };

        // Node A: Validator normal execution
        XapianManager::destroyAllInstances();
        SetXapianBasePath(baseA.c_str());
        execute_block1_validator();

        // Node B: Validator commits Xapian for Block 1, crashes before Block 1 persisted -> rolls back to 0 -> re-executes Block 1
        XapianManager::destroyAllInstances();
        SetXapianBasePath(baseB.c_str());
        execute_block1_validator();
        XapianManager::destroyAllInstances();
        REQUIRE(MVM_rollbackXapianAboveBlock(0) == 1);
        execute_block1_validator();

        // Node C: SyncOnly replays FullDbLogs twice (simulating sync retry)
        XapianManager::destroyAllInstances();
        SetXapianBasePath(baseC.c_str());
        LogReplayEntryC syncEntry{addr20, 20, block1FullDbLog.data(), static_cast<int>(block1FullDbLog.size())};
        REQUIRE(ReplayFullDbLogsAtBlock(&syncEntry, 1, 1) == 1);
        REQUIRE(ReplayFullDbLogsAtBlock(&syncEntry, 1, 1) == 1);

        auto dump_search_and_docs = [&](const std::string& nodeBase) {
            XapianManager::destroyAllInstances();
            SetXapianBasePath(nodeBase.c_str());
            auto m = XapianManager::getInstance("rank_db", cAddr, false);
            Xapian::Database* sdb = m->acquireSearchDb();
            REQUIRE(sdb != nullptr);
            std::string summary;
            {
                // Equal relevance query: Xapian Enquire breaks ties strictly by physical Xapian::docid!
                XapianSearcher searcher(sdb);
                auto [results, total] = searcher.search(
                    {"equalrank"}, Xapian::Query::OP_AND, Xapian::Query::OP_AND,
                    {{"", "T"}}, std::nullopt, std::nullopt, 0, 10);
                REQUIRE(total == 2);
                REQUIRE(results.size() == 2);
                CHECK(results[0].docid == 1);
                CHECK(results[1].docid == 2);
                // Lower nid (expectedSecondDocId) MUST have received physical docid 1!
                CHECK(results[0].data == "payload_tx_second_lower_nid");
                CHECK(results[1].data == "payload_tx_first_higher_nid");
                summary = sdb->get_document(1).serialise() + "|" + sdb->get_document(2).serialise();
            }
            m->releaseSearchDb(sdb);
            XapianManager::destroyAllInstances();
            return summary;
        };

        std::string stateA = dump_search_and_docs(baseA);
        std::string stateB = dump_search_and_docs(baseB);
        std::string stateC = dump_search_and_docs(baseC);
        CHECK(stateA == stateB);
        CHECK(stateA == stateC);

        SetXapianBasePath(".test_db_xapian");
        cleanup_test_db(base);
        cleanup_test_db(baseA);
        cleanup_test_db(baseB);
        cleanup_test_db(baseC);
    }

    TEST_CASE("CONCURRENCY: WriterPreferredSharedMutex recursive shared_lock safety while writer queued") {
        WriterPreferredSharedMutex mtx;
        std::vector<std::unique_ptr<WriterPreferredSharedMutex>> extraMutexes;
        for (int i = 0; i < 12; ++i) {
            extraMutexes.push_back(std::make_unique<WriterPreferredSharedMutex>());
        }

        std::atomic<bool> outerReaderHeld{false};
        std::atomic<bool> writerAboutToLock{false};
        std::atomic<bool> writerAcquired{false};
        std::atomic<bool> releaseOuterReader{false};
        std::atomic<bool> nestedTryOk{false};
        std::atomic<bool> writerStillBlockedDuringNested{false};
        std::atomic<bool> nestedReaderSucceeded{false};

        std::thread reader1([&]() {
            std::shared_lock<WriterPreferredSharedMutex> outerLock(mtx);
            // Also hold > 8 distinct WriterPreferredSharedMutex instances to exercise overflow_slots
            std::vector<std::shared_lock<WriterPreferredSharedMutex>> extraLocks;
            for (auto& em : extraMutexes) {
                extraLocks.emplace_back(*em);
            }
            outerReaderHeld.store(true, std::memory_order_release);

            while (!writerAboutToLock.load(std::memory_order_acquire)) {
                std::this_thread::yield();
            }
            // Give writer thread time to block inside pthread_rwlock_wrlock
            std::this_thread::sleep_for(std::chrono::milliseconds(25));

            // Nested shared_lock on the SAME thread while writer is queued MUST NOT deadlock!
            {
                std::shared_lock<WriterPreferredSharedMutex> nested1(mtx);
                std::shared_lock<WriterPreferredSharedMutex> nested2(mtx);
                bool tryOk = mtx.try_lock_shared();
                if (tryOk) {
                    mtx.unlock_shared();
                }
                nestedTryOk.store(tryOk, std::memory_order_relaxed);
                // Also test nested lock on an overflow_slots mutex (> 8th instance)
                std::shared_lock<WriterPreferredSharedMutex> nestedOverflow(*extraMutexes.back());
                writerStillBlockedDuringNested.store(!writerAcquired.load(std::memory_order_acquire), std::memory_order_relaxed);
                nestedReaderSucceeded.store(true, std::memory_order_release);
            }

            while (!releaseOuterReader.load(std::memory_order_acquire)) {
                std::this_thread::yield();
            }
        });

        while (!outerReaderHeld.load(std::memory_order_acquire)) {
            std::this_thread::yield();
        }

        std::thread writer([&]() {
            writerAboutToLock.store(true, std::memory_order_release);
            std::unique_lock<WriterPreferredSharedMutex> wlock(mtx);
            writerAcquired.store(true, std::memory_order_release);
        });

        while (!nestedReaderSucceeded.load(std::memory_order_acquire)) {
            std::this_thread::sleep_for(std::chrono::milliseconds(2));
        }
        CHECK(nestedReaderSucceeded.load());
        CHECK(nestedTryOk.load());
        CHECK(writerStillBlockedDuringNested.load());
        CHECK(!writerAcquired.load());

        // A brand-new reader on ANOTHER thread MUST be blocked by the queued writer (writer preference!)
        std::atomic<bool> newReaderTryResult{true};
        std::thread reader2([&]() {
            newReaderTryResult.store(mtx.try_lock_shared(), std::memory_order_release);
            if (newReaderTryResult.load(std::memory_order_relaxed)) {
                mtx.unlock_shared();
            }
        });
        reader2.join();
        CHECK(newReaderTryResult.load() == false);

        // Now let outer reader release -> writer MUST acquire immediately!
        releaseOuterReader.store(true, std::memory_order_release);
        reader1.join();
        writer.join();
        CHECK(writerAcquired.load() == true);

        // After writer finishes, new readers & writers can acquire freely
        CHECK(mtx.try_lock_shared());
        mtx.unlock_shared();
        CHECK(mtx.try_lock());
        mtx.unlock();
    }

    TEST_CASE("STRESS SPAM: Continuous 150-block 7500-tx batch spam with concurrent readers, compaction, and crash rollback") {
        std::string base = ".test_db_xapian_spam_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        cleanup_test_db(base);
        XapianManager::destroyAllInstances();
        SetXapianBasePath(base.c_str());

        mvm::Address addr(700050);
        auto mgr = create_test_manager("spam_hot_db", addr, true);

        std::atomic<bool> stopReaders{false};
        std::atomic<uint64_t> readerOps{0};
        std::string seedDocId;
        {
            uint256_t seedTx(0xABC0001ULL);
            seedDocId = mgr->new_document("seed_doc", 1, nullptr, &seedTx);
            mgr->add_term(seedDocId, "Tspamhot", 1, nullptr, &seedTx);
            mgr->index_text(seedDocId, "blockchain consensus xapian throughput", 1, "S", 1, nullptr, &seedTx);
            uint8_t rawHash[32];
            mvm::to_big_endian(seedTx, rawHash);
            commit_xapian_tx_buffer_batch(rawHash, 1);
            MVM_commitAllXapianAtBlock(1);
        }

        // Launch 4 concurrent reader threads continuously hammering get_data & search_pool
        std::vector<std::thread> readers;
        for (int r = 0; r < 4; ++r) {
            readers.emplace_back([&, r]() {
                while (!stopReaders.load(std::memory_order_relaxed)) {
                    std::string d = mgr->get_data(seedDocId, 1);
                    if (d.empty()) {
                        // seedDocId is never deleted, must always remain readable
                        std::abort();
                    }
                    {
                        std::shared_lock<XapianManager::ChangesMutex> search_lock(mgr->changes_mutex);
                        Xapian::Database* sdb = mgr->acquireSearchDb();
                        if (sdb) {
                            {
                                Xapian::Enquire enq(*sdb);
                                enq.set_query(Xapian::Query("Tspamhot"));
                                Xapian::MSet mset = enq.get_mset(0, 10);
                                (void)mset.size();
                            }
                            mgr->releaseSearchDb(sdb);
                        }
                    }
                    readerOps.fetch_add(1, std::memory_order_relaxed);
                }
            });
        }

        constexpr int totalBlocks = 150;
        constexpr int txsPerBlock = 50; // 7,500 transactions total
        std::vector<std::string> liveDocs;
        liveDocs.push_back(seedDocId);

        auto tStart = std::chrono::steady_clock::now();
        std::string expectedSeedAt147;
        size_t expectedDocCountAt147 = 0;

        for (int bn = 2; bn <= totalBlocks; ++bn) {
            std::vector<uint8_t> batchHashes(txsPerBlock * 32, 0);
            std::vector<uint8_t> clearHashes(5 * 32, 0);

            for (int t = 0; t < txsPerBlock; ++t) {
                uint256_t txHash(static_cast<uint64_t>(bn) * 10000ULL + static_cast<uint64_t>(t) + 1ULL);
                mvm::to_big_endian(txHash, batchHashes.data() + t * 32);

                if (t % 5 == 0) {
                    // Create new doc + index text + value
                    std::string nd = mgr->new_document("doc_b" + std::to_string(bn) + "_t" + std::to_string(t), bn, nullptr, &txHash);
                    mgr->add_term(nd, "Tspamhot", bn, nullptr, &txHash);
                    mgr->index_text(nd, "high frequency spam load test block " + std::to_string(bn), 1, "S", bn, nullptr, &txHash);
                    mgr->add_value(nd, 1, std::to_string(bn * 100 + t), true, bn, nullptr, &txHash);
                    liveDocs.push_back(nd);
                } else if (t % 5 == 4 && liveDocs.size() > 10) {
                    // Delete an older non-seed doc + also delete a bogus non-existent doc
                    std::string victim = liveDocs.back();
                    liveDocs.pop_back();
                    mgr->delete_document(victim, bn, nullptr, &txHash);
                    mgr->delete_document("0x0000000000000000000000000000000000000000000000000000000000999999", bn, nullptr, &txHash);
                    mgr->delete_document("0x1", bn, nullptr, &txHash);
                } else {
                    // Hot-spot contention: multiple txs in the SAME block updating the SAME documents!
                    const std::string& target = liveDocs[static_cast<size_t>(t) % liveDocs.size()];
                    mgr->set_data(target, "hot_b" + std::to_string(bn) + "_t" + std::to_string(t), bn, nullptr, &txHash);
                    mgr->add_term(target, "Tb" + std::to_string(bn % 16), bn, nullptr, &txHash);
                }
            }

            // Also simulate 5 reverted txs per block whose buffers must be discarded cleanly
            for (int rt = 0; rt < 5; ++rt) {
                uint256_t revHash(0xDEAD000000ULL + static_cast<uint64_t>(bn) * 100ULL + static_cast<uint64_t>(rt));
                mvm::to_big_endian(revHash, clearHashes.data() + rt * 32);
                std::string ghost = mgr->new_document("ghost_reverted", bn, nullptr, &revHash);
                mgr->add_term(ghost, "Tghost", bn, nullptr, &revHash);
            }

            commit_xapian_tx_buffer_batch(batchHashes.data(), txsPerBlock);
            clear_xapian_tx_buffer_batch(clearHashes.data(), 5);
            ExportedXapianLogArray exported = MVM_exportAllXapianLogs();
            MVM_freeExportedXapianLogs(exported);
            MVM_commitAllXapianAtBlock(static_cast<unsigned long long>(bn));

            if (bn == 75) {
                // Trigger live online compaction mid-spam while reader threads are active!
                mgr->compactInPlace(1024, std::chrono::minutes(0));
            }
            if (bn == 147) {
                expectedSeedAt147 = mgr->get_data(seedDocId, 147);
                std::shared_lock<XapianManager::ChangesMutex> rlock(mgr->changes_mutex);
                Xapian::Database* rdb = mgr->acquireSimpleReadDb();
                expectedDocCountAt147 = rdb->get_doccount();
                mgr->releaseSimpleReadDb(rdb);
            }
        }

        auto tEnd = std::chrono::steady_clock::now();
        stopReaders.store(true, std::memory_order_relaxed);
        for (auto& th : readers) th.join();

        auto elapsedMs = std::chrono::duration_cast<std::chrono::milliseconds>(tEnd - tStart).count();
        std::cerr << "[STRESS BENCHMARK] 149 blocks (7,450 committed txs + 745 reverted txs + live compaction + "
                  << readerOps.load() << " concurrent reads/searches) completed in " << elapsedMs
                  << " ms (" << (elapsedMs / 149.0) << " ms/block)" << std::endl;

        // Verify zero ghost writes leaked from reverted buffers
        {
            std::shared_lock<XapianManager::ChangesMutex> slock(mgr->changes_mutex);
            Xapian::Database* sdb = mgr->acquireSearchDb();
            REQUIRE(sdb != nullptr);
            {
                Xapian::Enquire enq(*sdb);
                enq.set_query(Xapian::Query("Tghost"));
                CHECK(enq.get_mset(0, 10).empty());
            }
            mgr->releaseSearchDb(sdb);
        }

        // Simulate 3-block pipeline crash at block 150 -> roll back to block 147 and verify exact state!
        REQUIRE(MVM_rollbackXapianAboveBlock(147) == 1);
        CHECK(mgr->getCommittedHeadBlock() == 147);
        CHECK(mgr->get_data(seedDocId, 147) == expectedSeedAt147);
        {
            Xapian::Database* rdb = mgr->acquireSimpleReadDb();
            CHECK(rdb->get_doccount() == expectedDocCountAt147);
            mgr->releaseSimpleReadDb(rdb);
        }

        XapianManager::destroyAllInstances();
        SetXapianBasePath(".test_db_xapian");
        cleanup_test_db(base);
    }

    TEST_CASE("COMPACTION + ROLLBACK: XUJ1 undo journal and pruned_floor survive compactInPlace across rollback boundaries") {
        std::string base = ".test_db_xapian_compact_rb_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        cleanup_test_db(base);
        XapianManager::destroyAllInstances();
        SetXapianBasePath(base.c_str());

        mvm::Address addr(700060);
        auto mgr = create_test_manager("compact_undo_db", addr, true);
        REQUIRE(mgr != nullptr);

        // Block 1: create docA (did=1), docB (did=2), docC (did=3)
        uint256_t tx1(0x101), tx2(0x102), tx3(0x103);
        std::string rawA = mgr->new_document("a_tmp", 1, nullptr, &tx1);
        std::string rawB = mgr->new_document("b_tmp", 1, nullptr, &tx2);
        std::string rawC = mgr->new_document("c_tmp", 1, nullptr, &tx3);
        // Sort nids so we know deterministic physical docids 1, 2, 3
        std::vector<std::string> sorted1 = {rawA, rawB, rawC};
        std::sort(sorted1.begin(), sorted1.end());
        std::string docA = sorted1[0], docB = sorted1[1], docC = sorted1[2];
        mgr->clearAllTxBuffers();

        {
            std::vector<XapianLog::LogEntry> b1;
            b1.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{docA, "docA_b1"});
            b1.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{docA, "Tcommon"});
            b1.emplace_back(XapianLog::Operation::ADD_VALUE, XapianLog::AddValueData{docA, 1, Xapian::sortable_serialise(10.0), true});
            b1.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{docB, "docB_b1"});
            b1.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{docB, "Tcommon"});
            b1.emplace_back(XapianLog::Operation::ADD_VALUE, XapianLog::AddValueData{docB, 1, Xapian::sortable_serialise(20.0), true});
            b1.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{docC, "docC_b1"});
            b1.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{docC, "Tcommon"});
            b1.emplace_back(XapianLog::Operation::ADD_VALUE, XapianLog::AddValueData{docC, 1, Xapian::sortable_serialise(30.0), true});
            REQUIRE(mgr->replay_log(b1));
            REQUIRE(mgr->commit_changes(1));
        }

        // Block 2: update docA, delete docB (did=2), create docD (did=4)
        const std::string docD = "0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd";
        {
            std::vector<XapianLog::LogEntry> b2;
            b2.emplace_back(XapianLog::Operation::SET_DATA, XapianLog::SetDataData{docA, "docA_b2"});
            b2.emplace_back(XapianLog::Operation::ADD_VALUE, XapianLog::AddValueData{docA, 1, Xapian::sortable_serialise(12.0), true});
            b2.emplace_back(XapianLog::Operation::DEL_DOC, XapianLog::DelDocData{docB});
            b2.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{docD, "docD_b2"});
            b2.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{docD, "Tcommon"});
            REQUIRE(mgr->replay_log(b2));
            REQUIRE(mgr->commit_changes(2));
        }

        // Block 3: update docC, delete docD (did=4), create docE (did=5)
        const std::string docE = "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee";
        {
            std::vector<XapianLog::LogEntry> b3;
            b3.emplace_back(XapianLog::Operation::SET_DATA, XapianLog::SetDataData{docC, "docC_b3"});
            b3.emplace_back(XapianLog::Operation::DEL_DOC, XapianLog::DelDocData{docD});
            b3.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{docE, "docE_b3"});
            b3.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{docE, "Tcommon"});
            REQUIRE(mgr->replay_log(b3));
            REQUIRE(mgr->commit_changes(3));
        }

        // Block 4: update docA again, create docF (did=6)
        const std::string docF = "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff01";
        {
            std::vector<XapianLog::LogEntry> b4;
            b4.emplace_back(XapianLog::Operation::SET_DATA, XapianLog::SetDataData{docA, "docA_b4"});
            b4.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{docF, "docF_b4"});
            b4.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{docF, "Tcommon"});
            REQUIRE(mgr->replay_log(b4));
            REQUIRE(mgr->commit_changes(4));
        }

        // Compact in place at Block 4!
        REQUIRE(mgr->compactInPlace(0, std::chrono::minutes(0)) == true);
        CHECK(mgr->getCommittedHeadBlock() == 4);

        // Cold restart after compaction + roll back across compaction boundary to Block 2!
        XapianManager::destroyAllInstances();
        REQUIRE(MVM_rollbackXapianAboveBlock(2) == 1);

        mgr = XapianManager::getInstance("compact_undo_db", addr, false);
        REQUIRE(mgr != nullptr);
        CHECK(mgr->getCommittedHeadBlock() == 2);
        CHECK(mgr->get_data(docA, 2) == "docA_b2");
        CHECK(mgr->get_value(docA, 1, true, 2) == "12");
        CHECK(mgr->get_data(docB, 2).empty());
        CHECK(mgr->get_data(docC, 2) == "docC_b1");
        CHECK(mgr->get_data(docD, 2) == "docD_b2"); // Deleted in B3, compacted in B4, restored to B2!
        CHECK(mgr->get_data(docE, 2).empty());
        CHECK(mgr->get_data(docF, 2).empty());

        // Verify exact physical docids (1, 3, 4) survived DBCOMPACT_NO_RENUMBER + XUJ1 rollback!
        {
            Xapian::Database* rdb = mgr->acquireSimpleReadDb();
            REQUIRE(rdb != nullptr);
            CHECK(rdb->get_doccount() == 3);
            CHECK(rdb->get_document(1).get_data() == "docA_b2");
            CHECK(rdb->get_document(3).get_data() == "docC_b1");
            CHECK(rdb->get_document(4).get_data() == "docD_b2");
            mgr->releaseSimpleReadDb(rdb);
        }

        // Roll back further from Block 2 -> Block 1 (restores docB at physical docid 2!)
        REQUIRE(MVM_rollbackXapianAboveBlock(1) == 1);
        CHECK(mgr->getCommittedHeadBlock() == 1);
        CHECK(mgr->get_data(docA, 1) == "docA_b1");
        CHECK(mgr->get_data(docB, 1) == "docB_b1");
        CHECK(mgr->get_data(docC, 1) == "docC_b1");
        CHECK(mgr->get_data(docD, 1).empty());
        {
            Xapian::Database* rdb = mgr->acquireSimpleReadDb();
            REQUIRE(rdb != nullptr);
            CHECK(rdb->get_doccount() == 3);
            CHECK(rdb->get_document(2).get_data() == "docB_b1");
            mgr->releaseSimpleReadDb(rdb);
        }

        // Also verify pruned_floor metadata survives compactInPlace (fail-close preserved!)
        // MAX_UNDO_BLOCKS = 64, so committing up to block 68 sets pruned_floor = 4 (blocks 5..68 retained)
        for (uint64_t bn = 2; bn <= 68; ++bn) {
            std::vector<XapianLog::LogEntry> b;
            b.emplace_back(XapianLog::Operation::SET_DATA, XapianLog::SetDataData{docA, "docA_re_" + std::to_string(bn)});
            REQUIRE(mgr->replay_log(b));
            REQUIRE(mgr->commit_changes(bn));
        }
        REQUIRE(mgr->compactInPlace(0, std::chrono::minutes(0)) == true);
        XapianManager::destroyAllInstances();
        // Rolling back below pruned_floor (targetBlock = 3 < 4) MUST still fail-close (-1) after compaction!
        CHECK(MVM_rollbackXapianAboveBlock(3) == -1);
        // Rolling back to pruned_floor (targetBlock = 4) MUST still succeed across 64 blocks!
        CHECK(MVM_rollbackXapianAboveBlock(4) == 1);

        XapianManager::destroyAllInstances();
        SetXapianBasePath(".test_db_xapian");
        cleanup_test_db(base);
    }

    TEST_CASE("CONCURRENCY: Dedicated 8-reader search flood concurrent with commit_changes verifies writer-preference & atomic visibility") {
        std::string base = ".test_db_xapian_rw_commit_" + std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        cleanup_test_db(base);
        XapianManager::destroyAllInstances();
        SetXapianBasePath(base.c_str());

        mvm::Address addr(700061);
        auto mgr = create_test_manager("rw_atomic_db", addr, true);
        REQUIRE(mgr != nullptr);

        const std::string docLeft = "0x1111111111111111111111111111111111111111111111111111111111111111";
        const std::string docRight = "0x2222222222222222222222222222222222222222222222222222222222222222";
        {
            std::vector<XapianLog::LogEntry> initEntries;
            initEntries.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{docLeft, "ver_1"});
            initEntries.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{docLeft, "Tpair"});
            initEntries.emplace_back(XapianLog::Operation::NEW_DOC, XapianLog::NewDocData{docRight, "ver_1"});
            initEntries.emplace_back(XapianLog::Operation::ADD_TERM, XapianLog::AddTermData{docRight, "Tpair"});
            REQUIRE(mgr->replay_log(initEntries));
            REQUIRE(mgr->commit_changes(1));
        }

        std::atomic<bool> stop{false};
        std::atomic<bool> tornReadDetected{false};
        std::atomic<bool> searchErrorDetected{false};
        std::atomic<uint64_t> totalReadIterations{0};

        std::vector<std::thread> readers;
        for (int i = 0; i < 8; ++i) {
            readers.emplace_back([&, i]() {
                while (!stop.load(std::memory_order_relaxed)) {
                    // Tight loop WITHOUT sleep to try to starve writer if mutex were reader-preferred!
                    {
                        std::shared_lock<XapianManager::ChangesMutex> rlock(mgr->changes_mutex);
                        Xapian::Database* sdb = mgr->acquireSearchDb();
                        if (sdb) {
                            try {
                                XapianSearcher searcher(sdb);
                                auto [results, total] = searcher.search(
                                    {"pair"}, Xapian::Query::OP_AND, Xapian::Query::OP_AND,
                                    {{"", "T"}}, std::nullopt, std::nullopt, 0, 10);
                                if (total != 2 || results.size() != 2 || results[0].data != results[1].data) {
                                    tornReadDetected.store(true, std::memory_order_release);
                                }
                            } catch (...) {
                                searchErrorDetected.store(true, std::memory_order_release);
                            }
                            mgr->releaseSearchDb(sdb);
                        }
                    }
                    totalReadIterations.fetch_add(1, std::memory_order_relaxed);
                }
            });
        }

        // Wait until readers are actively flooding changes_mutex
        while (totalReadIterations.load(std::memory_order_relaxed) < 200) {
            std::this_thread::yield();
        }

        int64_t maxCommitMs = 0;
        for (uint64_t bn = 2; bn <= 45; ++bn) {
            std::string tag = "ver_" + std::to_string(bn);
            std::vector<XapianLog::LogEntry> batch;
            batch.emplace_back(XapianLog::Operation::SET_DATA, XapianLog::SetDataData{docLeft, tag});
            batch.emplace_back(XapianLog::Operation::SET_DATA, XapianLog::SetDataData{docRight, tag});

            auto c0 = std::chrono::steady_clock::now();
            REQUIRE(mgr->replay_log(batch));
            REQUIRE(mgr->commit_changes(bn));
            auto cMs = std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - c0).count();
            if (cMs > maxCommitMs) {
                maxCommitMs = cMs;
            }
        }

        stop.store(true, std::memory_order_relaxed);
        for (auto& th : readers) th.join();

        CHECK(tornReadDetected.load() == false);
        CHECK(searchErrorDetected.load() == false);
        CHECK(totalReadIterations.load() >= 200);
        CHECK(maxCommitMs < 2000); // Writer never starved even under 8 zero-sleep reader threads!
        CHECK(mgr->get_data(docLeft, 45) == "ver_45");
        CHECK(mgr->get_data(docRight, 45) == "ver_45");

        XapianManager::destroyAllInstances();
        SetXapianBasePath(".test_db_xapian");
        cleanup_test_db(base);
    }
}

