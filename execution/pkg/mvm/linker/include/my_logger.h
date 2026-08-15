#pragma once

#include "mvm/native_logger.h"
#include <string>
#include <utility>
#include <vector>

// TEE-packaging B2 (note/tee_core_packaging_plan.md): LogString/LogBytes
// used to call back into Go mid-execution (GoLogString/GoLogBytes in
// pkg/mvm/logger.go) on every call. They now buffer into buffered_logs
// instead — the caller (run(), mvm_linker.cpp) reads this out via
// TakeBufferedLogs() before this MyLogger goes out of scope, and threads it
// through processResult() into ExecuteResult's b_native_logs, so Go flushes
// them into its own logger AFTER the call returns, not mid-flight.
class MyLogger : public mvm::NativeLogger
{
    public:
    MyLogger() = default;
    virtual void LogString(int, char*) override;
    virtual void LogBytes(int, unsigned char*, int) override;

    // (flag, message) pairs, in call order. LogBytes hex-encodes before
    // buffering (matching the pre-B2 GoLogBytes behavior exactly), so every
    // entry here is plain text regardless of which method produced it.
    std::vector<std::pair<int, std::string>> buffered_logs;
};
