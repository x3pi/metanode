#pragma once

namespace mvm
{
 struct NativeLogger
  {
    virtual void LogString(int, char*) = 0;
    virtual void LogBytes(int, unsigned char*, int) = 0;
  };
}