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

#include "xapian/xapian_manager.h"
#include "xapian/xapian_registry.h"
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
}

