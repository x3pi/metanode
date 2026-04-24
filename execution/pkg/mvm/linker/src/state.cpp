#include "state.h"
#include <atomic>     // For atomic members and cleaner flag
#include <chrono>     // For time operations
#include <exception>  // For std::exception
#include <functional> // For std::hash wrapper
#include <iostream>   // For std::cout, std::cerr
#include <memory>     // For std::shared_ptr, std::make_shared
#include <mutex> // For potential future use (though TBB map is mostly thread-safe)
#include <thread> // For cleaner thread
#include <vector> // For keys_to_erase vector

// Định nghĩa các phương thức của KeyHashCompare
bool KeyHashCompare::equal(const KeyType &lhs, const KeyType &rhs) const {
  return lhs == rhs;
}

// Simple hash for KeyType (array<uint8_t, 32>)
size_t KeyHashCompare::hash(const KeyType &key) const {
  // Using std::hash on the underlying bytes as a string view
  return std::hash<std::string_view>()(
      std::string_view(reinterpret_cast<const char *>(key.data()), key.size()));
}

// Định nghĩa các phương thức của AddressHashCompare
bool AddressHashCompare::equal(const uint256_t &lhs,
                               const uint256_t &rhs) const {
  return lhs == rhs;
}

// Simple hash for uint256_t
size_t AddressHashCompare::hash(const uint256_t &address) const {
  // Simple XOR hash - consider a better one if collisions become an issue
  auto low = static_cast<uint64_t>(address); // Lower 64 bits
  auto mid1 = static_cast<uint64_t>(address >> 64);
  auto mid2 = static_cast<uint64_t>(address >> 128);
  auto high = static_cast<uint64_t>(address >> 192); // Upper 64 bits

  // Combine hashes using XOR and bitwise rotation (simple example)
  size_t h1 = std::hash<uint64_t>{}(low);
  size_t h2 = std::hash<uint64_t>{}(mid1);
  size_t h3 = std::hash<uint64_t>{}(mid2);
  size_t h4 = std::hash<uint64_t>{}(high);

  // Combine hashes (example combination)
  size_t combined_hash = h1;
  combined_hash = (combined_hash << 1) |
                  (combined_hash >> (sizeof(size_t) * 8 - 1)); // Rotate left 1
  combined_hash ^= h2;
  combined_hash =
      (combined_hash << 1) | (combined_hash >> (sizeof(size_t) * 8 - 1));
  combined_hash ^= h3;
  combined_hash =
      (combined_hash << 1) | (combined_hash >> (sizeof(size_t) * 8 - 1));
  combined_hash ^= h4;

  return combined_hash;

  // Previous simpler hash:
  // auto low_s = static_cast<size_t>(address & 0xFFFFFFFFFFFFFFFF);
  // auto high_s = static_cast<size_t>(address >> 192);
  // return low_s ^ high_s;
}

// Khai báo biến static
std::unordered_map<uint256_t, shared_ptr<State>, AddressStdHash> State::instances;
std::shared_mutex State::instances_mutex;

void State::clearAllInstances() {
  std::unique_lock<std::shared_mutex> write_lock(instances_mutex);
  std::cerr << "[State] Clearing all " << instances.size()
            << " cached State instances (sync→validator transition)"
            << std::endl;
  
  // THREAD-SAFE CLEAR: std::vector key collection to safely iterate map without iterator invalidation races
  std::vector<uint256_t> keys;
  for (auto it = instances.begin(); it != instances.end(); ++it) {
      keys.push_back(it->first);
  }
  for (const auto& key : keys) {
      instances.erase(key);
  }

  std::cerr << "[State] All State instances cleared in a thread-safe manner. EVM will re-read from Go on next access."
            << std::endl;
}

// Constructor implementation (used internally by getInstance)
// Constructor now initializes last_interaction_time using member initializer
// list in .h State::State(const uint256_t &addr)
//     : address(addr), nonce(0),
//     last_interaction_time(std::chrono::steady_clock::now()) {}

// Triển khai các phương thức của State
shared_ptr<State> State::getInstance(const uint256_t &address)
{
    // Giai đoạn 1: Đọc bằng shared_lock (nhiều luồng đọc song song)
    {
        std::shared_lock<std::shared_mutex> read_lock(instances_mutex);
        auto it = instances.find(address);
        if (it != instances.end())
        {
            if (it->second)
                it->second->update_interaction_time();
            return it->second;
        }
    }

    // Giai đoạn 2: Không tìm thấy, lấy unique_lock để tạo mới
    std::unique_lock<std::shared_mutex> write_lock(instances_mutex);
    // Double-check: có thể luồng khác đã tạo xong rồi
    auto it = instances.find(address);
    if (it != instances.end())
    {
        if (it->second)
            it->second->update_interaction_time();
        return it->second;
    }

    // Tạo instance mới
    auto instance = make_shared<State>(address);
    instances[address] = instance;
    return instance;
}

std::optional<uint256_t> State::getValue(const KeyType &key) const {
  // Reading does not usually update interaction time, but check requirements
  // update_interaction_time(); // Uncomment if reads should prevent cleanup
  concurrent_hash_map<KeyType, uint256_t, KeyHashCompare>::const_accessor acc;
  if (stateMap.find(acc, key)) {
    return acc->second;
  }
  return std::nullopt;
}

void State::insertOrUpdate(const KeyType &key, const uint256_t &value) {
  update_interaction_time(); // Update time on modification
  concurrent_hash_map<KeyType, uint256_t, KeyHashCompare>::accessor acc;
  // Insert first, then update the value regardless of whether it was new or
  // existing
  stateMap.insert(acc, key);
  acc->second = value;
}

void State::erase(const KeyType &key) {
  update_interaction_time(); // Update time on modification
  stateMap.erase(key);
}

bool State::keyExists(const KeyType &key) const {
  // Reading does not usually update interaction time
  // update_interaction_time(); // Uncomment if checks should prevent cleanup
  concurrent_hash_map<KeyType, uint256_t, KeyHashCompare>::const_accessor acc;
  return stateMap.find(acc, key);
}

bool State::instanceExists(const uint256_t &address) {
  std::shared_lock<std::shared_mutex> read_lock(instances_mutex);
  auto it = instances.find(address);
  return it != instances.end() && it->second != nullptr;
}

// --- Getters and Setters ---
// (Update interaction time on setters)

uint256_t State::getAddress() const { /* read-only */ return address; }
uint256_t State::getBalance() const { /* read-only */ return balance; }
void State::setBalance(const uint256_t &newBalance) {
  update_interaction_time();
  balance = newBalance;
}

const std::vector<uint8_t> &State::getCode() const { /* read-only */
  return code;
}
void State::setCode(const std::vector<uint8_t> &newCode) {
  update_interaction_time();
  code = newCode;
}

uint256_t State::getNonce() const { /* read-only */ return nonce; }
void State::setNonce(const uint256_t &newNonce) {
  update_interaction_time();
  nonce = newNonce;
}

uint256_t State::getLastHash() const { /* read-only */ return last_hash; }
void State::setLastHash(const uint256_t &newLastHash) {
  update_interaction_time();
  last_hash = newLastHash;
}

KeyType State::toKeyType(const uint8_t cArray[32]) {
  KeyType key;
  if (cArray) { // Ensure input pointer is not null
    std::copy(cArray, cArray + 32, key.begin());
  } else {
    // Handle null input? Set key to zero or throw?
    key.fill(0); // Example: set to zero
    std::cerr << "Warning: State::toKeyType received null input array."
              << std::endl;
  }
  return key;
}

// --- Added Missing Function Definitions ---

// Method to update the last interaction time
void State::update_interaction_time() {
  // Use memory_order_release for potential optimization on architectures where
  // it matters
  last_interaction_time.store(std::chrono::steady_clock::now(),
                              std::memory_order_release);
}

// Method to get the last interaction time
std::chrono::steady_clock::time_point State::get_last_interaction_time() const {
  // Use memory_order_acquire for potential optimization
  return last_interaction_time.load(std::memory_order_acquire);
}

// --- State Idle Management and Cleanup Thread ---
namespace {
// Atomic flag to signal the cleaner thread to stop
std::atomic<bool> state_cleaner_running = true;

// The background thread function for State cleanup
void state_cleaner_task() {
  std::cout << "[State Cleaner Thread] Started." << std::endl;
  while (state_cleaner_running.load(std::memory_order_acquire)) {
    try {
      // Wait for a specified interval
      std::this_thread::sleep_for(
          std::chrono::minutes(1)); // Check every 1 minute

      if (!state_cleaner_running.load(std::memory_order_acquire))
        break; // Check again after sleep

      std::vector<uint256_t> keys_to_erase; // Use uint256_t for keys
      auto now = std::chrono::steady_clock::now();
      const auto idle_threshold = std::chrono::minutes(2); // Idle threshold

      // --- Phase 1: Identify potentially idle instances ---
      {
        std::shared_lock<std::shared_mutex> read_lock(State::instances_mutex);
        for (auto it = State::instances.cbegin(); it != State::instances.cend();
             ++it) {
          if (it->second) { // Check if the shared_ptr is valid
            try {
              auto last_interaction = it->second->get_last_interaction_time();
              auto time_since_interaction =
                  std::chrono::duration_cast<std::chrono::minutes>(
                      now - last_interaction);

              if (time_since_interaction >= idle_threshold) {
                // Instance is idle, check use count
                std::shared_ptr<State> temp_ptr =
                    it->second; // Temporary copy for use_count check
                long count = temp_ptr.use_count();
                // If only the map and our temp_ptr hold it (count <= 2), it's a
                // candidate
                if (count <= 2) {
                  keys_to_erase.push_back(it->first); // Add uint256_t key
                } else {
                }
              }
            } catch (const std::exception &e) {
            } catch (...) {
            }
          } else {
            // Found a null pointer in the map - definitely remove it
            keys_to_erase.push_back(it->first);
          }
        } // End Phase 1 loop
      }

      // --- Phase 2: Attempt to erase identified instances ---
      if (!keys_to_erase.empty()) {
        std::cout << "[State Cleaner Thread] Attempting to clean up "
                  << keys_to_erase.size() << " State instances." << std::endl;
        for (const uint256_t &key :
             keys_to_erase) { // Iterate over uint256_t keys
          std::unique_lock<std::shared_mutex> write_lock(State::instances_mutex);
          auto it = State::instances.find(key);
          if (it != State::instances.end()) {
            // Double-check conditions *inside* the lock
            if (it->second) { // Check instance pointer validity again
              std::shared_ptr<State> instance_ptr = it->second; // Hold ptr
              bool is_still_idle = false;
              long use_count_inside_lock = 0;

              try {
                auto last_interaction =
                    instance_ptr->get_last_interaction_time();
                is_still_idle =
                    (std::chrono::duration_cast<std::chrono::minutes>(
                         now - last_interaction) >= idle_threshold);
                use_count_inside_lock = instance_ptr.use_count();
              } catch (...) { /* Ignore errors in final check? Or log? */
              }

              if (is_still_idle && use_count_inside_lock <= 2) {
                std::cerr << "[State Cleaner Thread] Erasing idle State "
                             "instance (Address Hash: "
                          << AddressHashCompare().hash(key)
                          << ", use_count=" << use_count_inside_lock << ")"
                          << std::endl;
                try {
                  bool erased = (State::instances.erase(key) > 0);
                  if (erased) {
                    std::cerr << "   - Successfully erased State from map "
                                 "(Address Hash: "
                              << AddressHashCompare().hash(key) << ")"
                              << std::endl;
                  } else {
                    std::cerr << "   - Erase failed for State (concurrent "
                                 "modification?) (Address Hash: "
                              << AddressHashCompare().hash(key) << ")"
                              << std::endl;
                  }
                } catch (const std::exception &e) {
                  std::cerr << "   - Error during final State cleanup/erase "
                               "(Address Hash: "
                            << AddressHashCompare().hash(key)
                            << "): " << e.what() << std::endl;
                } catch (...) {
                  std::cerr << "   - Unknown error during final State "
                               "cleanup/erase (Address Hash: "
                            << AddressHashCompare().hash(key) << ")."
                            << std::endl;
                }
              } else {
                std::cout << "[State Cleaner Thread] Skipping erase for State "
                             "(Address Hash: "
                          << AddressHashCompare().hash(key)
                          << ") (Not idle anymore or use_count="
                          << use_count_inside_lock << " > 2)." << std::endl;
              }
            } else {
              // Pointer associated with key is null, erase it
              State::instances.erase(key);
            }
          }
        } // End Phase 2 loop
      } else {
      }

    } catch (const std::exception &e) {
      // Catch exceptions in the main loop to prevent the thread from dying
      // Optional: Sleep longer after an error?
      std::this_thread::sleep_for(std::chrono::seconds(300));
    } catch (...) {
      std::this_thread::sleep_for(std::chrono::seconds(300));
    }
  } // End of while loop
} // End of state_cleaner_task

// Create and detach the State cleaner thread
// Using a lambda ensures state_cleaner_task is defined before the thread is
// initialized.
std::thread state_cleaner_thread(state_cleaner_task);

// Function to stop the State cleaner thread cleanly
void stopStateCleanerThread() {
  state_cleaner_running.store(false, std::memory_order_release); // Signal stop
  if (state_cleaner_thread.joinable()) {
    state_cleaner_thread.join(); // Wait for the thread to finish
  } else {
  }
}

// RAII object to automatically stop the State cleaner thread on program exit
struct StateCleanerStopper {
  ~StateCleanerStopper() { stopStateCleanerThread(); }
};

// Create an instance of the stopper to manage thread lifecycle
StateCleanerStopper state_stopper;

} // end anonymous namespace