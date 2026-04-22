package rpcquery

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestMatchAddress_EmptyFilter(t *testing.T) {
	addr := common.HexToAddress("0xDEADBEEF")
	if !MatchAddress(nil, addr) {
		t.Error("nil filter should match all")
	}
	if !MatchAddress([]common.Address{}, addr) {
		t.Error("empty filter should match all")
	}
}

func TestMatchAddress_Match(t *testing.T) {
	addr := common.HexToAddress("0x1234")
	filter := []common.Address{
		common.HexToAddress("0xAAAA"),
		common.HexToAddress("0x1234"),
	}
	if !MatchAddress(filter, addr) {
		t.Error("should match address in filter")
	}
}

func TestMatchAddress_NoMatch(t *testing.T) {
	addr := common.HexToAddress("0x1234")
	filter := []common.Address{
		common.HexToAddress("0xAAAA"),
		common.HexToAddress("0xBBBB"),
	}
	if MatchAddress(filter, addr) {
		t.Error("should NOT match address not in filter")
	}
}

func TestMatchTopics_EmptyFilter(t *testing.T) {
	topics := []common.Hash{common.HexToHash("0xabc")}
	if !MatchTopics(nil, topics) {
		t.Error("nil filter should match all")
	}
	if !MatchTopics([][]common.Hash{}, topics) {
		t.Error("empty filter should match all")
	}
}

func TestMatchTopics_WildcardGroup(t *testing.T) {
	topics := []common.Hash{common.HexToHash("0xabc"), common.HexToHash("0xdef")}
	filter := [][]common.Hash{
		{},                          // wildcard for position 0
		{common.HexToHash("0xdef")}, // exact match for position 1
	}
	if !MatchTopics(filter, topics) {
		t.Error("wildcard group should match any topic")
	}
}

func TestMatchTopics_ExactMatch(t *testing.T) {
	topics := []common.Hash{common.HexToHash("0xabc")}
	filter := [][]common.Hash{
		{common.HexToHash("0xabc")},
	}
	if !MatchTopics(filter, topics) {
		t.Error("should match exact topic")
	}
}

func TestMatchTopics_OrGroup(t *testing.T) {
	topics := []common.Hash{common.HexToHash("0xabc")}
	filter := [][]common.Hash{
		{common.HexToHash("0x111"), common.HexToHash("0xabc"), common.HexToHash("0x222")},
	}
	if !MatchTopics(filter, topics) {
		t.Error("should match when topic is in OR group")
	}
}

func TestMatchTopics_NoMatch(t *testing.T) {
	topics := []common.Hash{common.HexToHash("0xabc")}
	filter := [][]common.Hash{
		{common.HexToHash("0x111"), common.HexToHash("0x222")},
	}
	if MatchTopics(filter, topics) {
		t.Error("should NOT match when topic is not in filter")
	}
}

func TestMatchTopics_LogHasFewerTopics(t *testing.T) {
	topics := []common.Hash{common.HexToHash("0xabc")} // only 1 topic
	filter := [][]common.Hash{
		{common.HexToHash("0xabc")}, // position 0
		{common.HexToHash("0xdef")}, // position 1 — log doesn't have this
	}
	if MatchTopics(filter, topics) {
		t.Error("should NOT match when log has fewer topics than filter requires")
	}
}
