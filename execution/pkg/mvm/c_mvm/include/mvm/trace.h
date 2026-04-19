// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

#pragma once
#include "disassembler.h"
#include "opcode.h"
#include "stack.h"

#include <fmt/format_header_only.h>
#include <iostream>
#include <memory>
#include <utility>
#include <vector>

namespace mvm
{
  struct TraceEvent
  {
    const uint64_t pc;
    const Opcode op;
    const uint16_t call_depth;
    std::unique_ptr<Stack> s;

    TraceEvent(
        const uint64_t pc,
        const Opcode op,
        const uint16_t call_depth,
        const Stack s) : pc(pc),
                         op(op),
                         call_depth(call_depth),
                         s(std::make_unique<Stack>(s))
    {
    }

    TraceEvent(TraceEvent &&other) : pc(other.pc),
                                     op(other.op),
                                     call_depth(other.call_depth),
                                     s(std::move(other.s))
    {
    }
  };

  /**
   * Runtime trace of a smart contract (for debugging)
   */
  struct Trace
  {
    std::vector<TraceEvent> events;

    template <class... Args>
    TraceEvent &add(Args &&...args)
    {
      events.emplace_back(std::forward<Args>(args)...);
      auto &e = events.back();
      return e;
    }

    void reset()
    {
      events.clear();
    }

    void print_last_n(std::ostream &os, size_t n) const
    {
      auto first = n < events.size() ? events.size() - n : 0;
      for (auto i = first; i < events.size(); ++i)
      {
        os << fmt::format("{}", events[i]) << std::endl;
      }
    }
  };
} // namespace mvm

namespace fmt
{
  template <>
  struct formatter<mvm::Stack>
  {
    constexpr auto parse(format_parse_context &ctx)
    {
      return ctx.begin();
    }

    template <typename FormatContext>
    auto format(const mvm::Stack &s, FormatContext &ctx)
    {
      auto out = ctx.out();
      const auto &data = s.getData();

      // Đảm bảo dùng fmt::format_to trực tiếp với out thay vì chuyển đổi
      for (size_t i = 0; i < data.size(); ++i)
      {
        auto formatted_string = fmt::format("[{}] = {}\n", i, data[i]);
        // Sử dụng fmt::format_to để in trực tiếp vào out
        fmt::format_to(out, "{}", formatted_string);
      }
      return out;
    }
  };

  template <>
  struct formatter<mvm::TraceEvent>
  {
    template <typename ParseContext>
    constexpr auto parse(ParseContext &ctx)
    {
      return ctx.begin();
    }

    template <typename FormatContext>
    auto format(const mvm::TraceEvent &e, FormatContext &ctx) const -> decltype(ctx.out())
    {
      auto out = ctx.out();

      // Gọi format_to trực tiếp, không lưu lại vào `out`
      out = format_to(
          out,
          FMT_STRING("{} ({}): {}"),
          e.pc,
          e.call_depth,
          mvm::Disassembler::getOp(e.op).mnemonic);

      if (e.s)
      {
        out = fmt::format_to(out, "\nstack before:\n{}", *e.s);
      }

      return out;
    }
  };

  template <>
  struct formatter<mvm::Trace>
  {
    template <typename ParseContext>
    constexpr auto parse(ParseContext &ctx)
    {
      return ctx.begin();
    }

    template <typename FormatContext>
    auto format(const mvm::Trace &t, FormatContext &ctx) const -> decltype(ctx.out())
    {
      return format_to(ctx.out(), "{}", fmt::join(t.events, "\n"));
    }
  };
} // namespace fmt
