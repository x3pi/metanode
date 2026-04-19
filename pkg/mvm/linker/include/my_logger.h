#pragma once

#include "mvm/native_logger.h"

class MyLogger : public mvm::NativeLogger
{
    public:
    MyLogger() = default;
    virtual void LogString(int, char*) override;
    virtual void LogBytes(int, unsigned char*, int) override;
};