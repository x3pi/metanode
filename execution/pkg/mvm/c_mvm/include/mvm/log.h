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

    // Journal support (see _Processor::StateJournal in processor.cpp):
    // checkpoint() returns a mark before a LOG opcode is handled; rollback(m)
    // discards every log handled since that mark, in order, if the call
    // frame that emitted them ends up reverting.
    virtual size_t checkpoint() = 0;
    virtual void rollback(size_t mark) = 0;
  };

  struct NullLogHandler : public LogHandler
  {
    virtual void handle(LogEntry&&) override {}
    virtual size_t checkpoint() override { return 0; }
    virtual void rollback(size_t) override {}
  };

  struct VectorLogHandler : public LogHandler
  {
    std::vector<LogEntry> logs;

    virtual ~VectorLogHandler() = default;
    virtual void handle(LogEntry&& e) override
    {
      logs.emplace_back(e);
    }
    virtual size_t checkpoint() override { return logs.size(); }
    virtual void rollback(size_t mark) override
    {
      if (mark < logs.size())
        logs.resize(mark);
    }
  };
}