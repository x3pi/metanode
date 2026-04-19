#include "my_logger.h"
#include "mvm_linker.hpp"

#include <fstream>
#include <iostream>
#include <mutex>
#include <string>

// ============================================================================
// Redirect C++ stdout/stderr sang file log riêng
// Khi InitCppFileLog được gọi, tất cả cout/cerr sẽ ghi vào file
// thay vì hiện ra terminal. Giúp tách log MVM C++ ra khỏi Go log.
// ============================================================================

static std::ofstream g_cpp_log_file;
static std::streambuf *g_orig_cout_buf = nullptr;
static std::streambuf *g_orig_cerr_buf = nullptr;
static bool g_cpp_log_initialized = false;
static std::mutex g_cpp_log_mutex;

void InitCppFileLog(const char *log_dir, const char *name) {
  std::lock_guard<std::mutex> lock(g_cpp_log_mutex);

  if (g_cpp_log_initialized) {
    return; // Đã init rồi
  }

  if (log_dir == nullptr || log_dir[0] == '\0') {
    return; // Không có log dir
  }

  // Tạo tên file log theo process name, ví dụ: mvm_cpp_master.log
  std::string suffix =
      (name != nullptr && name[0] != '\0') ? std::string(name) : "debug";
  std::string log_path = std::string(log_dir) + "/mvm_cpp_" + suffix + ".log";
  g_cpp_log_file.open(log_path, std::ios::out | std::ios::app);

  if (!g_cpp_log_file.is_open()) {
    std::cerr << "[MVM_CPP_LOG] Error: Cannot open log file: " << log_path
              << std::endl;
    return;
  }

  // Lưu lại buffer gốc để có thể restore khi close
  g_orig_cout_buf = std::cout.rdbuf();
  g_orig_cerr_buf = std::cerr.rdbuf();

  // Redirect cout và cerr sang file
  std::cout.rdbuf(g_cpp_log_file.rdbuf());
  std::cerr.rdbuf(g_cpp_log_file.rdbuf());

  g_cpp_log_initialized = true;

  // Log message đầu tiên vào file
  std::cout << "[MVM_CPP_LOG] ✅ C++ stdout/stderr redirected to: " << log_path
            << std::endl;
}

void CloseCppFileLog() {
  std::lock_guard<std::mutex> lock(g_cpp_log_mutex);

  if (!g_cpp_log_initialized) {
    return;
  }

  // Restore buffer gốc
  if (g_orig_cout_buf) {
    std::cout.rdbuf(g_orig_cout_buf);
    g_orig_cout_buf = nullptr;
  }
  if (g_orig_cerr_buf) {
    std::cerr.rdbuf(g_orig_cerr_buf);
    g_orig_cerr_buf = nullptr;
  }

  g_cpp_log_file.flush();
  g_cpp_log_file.close();
  g_cpp_log_initialized = false;
}

// ============================================================================
// NativeLogger callback — gọi từ C++ MVM processor sang Go
// ============================================================================

void MyLogger::LogString(int f, char *str) { GoLogString(f, str); }

void MyLogger::LogBytes(int f, unsigned char *d, int s) { GoLogBytes(f, d, s); }
