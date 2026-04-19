// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once
#include "address.h"

#include <nlohmann/json.hpp>
#include <vector>


namespace mvm
{
   namespace log
  {
    using Data = std::vector<uint8_t>;
    using Topic = uint256_t;
  }

  struct LogEntry
  {
    Address address;
    log::Data data;
    std::vector<log::Topic> topics;

    bool operator==(const LogEntry& that) const;

    friend void to_json(nlohmann::json&, const LogEntry&);
    friend void from_json(const nlohmann::json&, LogEntry&);
  };

  void to_json(nlohmann::json&, const LogEntry&);
  void from_json(const nlohmann::json&, LogEntry&);

  struct LogHandler
  {
    virtual ~LogHandler() = default;
    virtual void handle(LogEntry&&) = 0;
  };

  struct NullLogHandler : public LogHandler
  {
    virtual void handle(LogEntry&&) override {}
  };

  struct VectorLogHandler : public LogHandler
  {
    std::vector<LogEntry> logs;

    virtual ~VectorLogHandler() = default;
    virtual void handle(LogEntry&& e) override
    {
      logs.emplace_back(e);
    }
  };
}