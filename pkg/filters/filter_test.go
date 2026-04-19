package filters

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func makeLogs() []*types.Log {
	return []*types.Log{
		{
			Address:     common.HexToAddress("0x1111111111111111111111111111111111111111"),
			Topics:      []common.Hash{common.HexToHash("0xaaa"), common.HexToHash("0xbbb")},
			BlockNumber: 10,
		},
		{
			Address:     common.HexToAddress("0x2222222222222222222222222222222222222222"),
			Topics:      []common.Hash{common.HexToHash("0xaaa"), common.HexToHash("0xccc")},
			BlockNumber: 20,
		},
		{
			Address:     common.HexToAddress("0x1111111111111111111111111111111111111111"),
			Topics:      []common.Hash{common.HexToHash("0xddd")},
			BlockNumber: 30,
		},
	}
}

func TestFilterLogs_NoFilter(t *testing.T) {
	logs := makeLogs()
	result := FilterLogs(logs, nil, nil, nil, nil)
	if len(result) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(result))
	}
}

func TestFilterLogs_ByBlockRange(t *testing.T) {
	logs := makeLogs()
	// From block 15 to block 25 — should only match block 20
	result := FilterLogs(logs, big.NewInt(15), big.NewInt(25), nil, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 log, got %d", len(result))
	}
	if result[0].BlockNumber != 20 {
		t.Fatalf("expected block 20, got %d", result[0].BlockNumber)
	}
}

func TestFilterLogs_ByFromBlock(t *testing.T) {
	logs := makeLogs()
	// From block 20 — should match blocks 20 and 30
	result := FilterLogs(logs, big.NewInt(20), nil, nil, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(result))
	}
}

func TestFilterLogs_ByToBlock(t *testing.T) {
	logs := makeLogs()
	// To block 20 — should match blocks 10 and 20
	result := FilterLogs(logs, nil, big.NewInt(20), nil, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(result))
	}
}

func TestFilterLogs_ByAddress(t *testing.T) {
	logs := makeLogs()
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	result := FilterLogs(logs, nil, nil, []common.Address{addr}, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 logs for addr1, got %d", len(result))
	}
}

func TestFilterLogs_ByTopic(t *testing.T) {
	logs := makeLogs()
	// First topic must be 0xaaa
	topics := [][]common.Hash{{common.HexToHash("0xaaa")}}
	result := FilterLogs(logs, nil, nil, nil, topics)
	if len(result) != 2 {
		t.Fatalf("expected 2 logs with topic 0xaaa, got %d", len(result))
	}
}

func TestFilterLogs_ByTopicAndAddress(t *testing.T) {
	logs := makeLogs()
	addr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	topics := [][]common.Hash{{common.HexToHash("0xaaa")}}
	result := FilterLogs(logs, nil, nil, []common.Address{addr}, topics)
	if len(result) != 1 {
		t.Fatalf("expected 1 log, got %d", len(result))
	}
	if result[0].BlockNumber != 20 {
		t.Fatalf("expected block 20, got %d", result[0].BlockNumber)
	}
}

func TestFilterLogs_TopicWildcard(t *testing.T) {
	logs := makeLogs()
	// First topic = wildcard (empty), second topic = 0xbbb
	topics := [][]common.Hash{{}, {common.HexToHash("0xbbb")}}
	result := FilterLogs(logs, nil, nil, nil, topics)
	if len(result) != 1 {
		t.Fatalf("expected 1 log with second topic 0xbbb, got %d", len(result))
	}
}

func TestFilterLogs_MoreTopicsThanLog(t *testing.T) {
	logs := makeLogs()
	// Log at block 30 only has 1 topic, but filter requires 2 → should exclude it
	topics := [][]common.Hash{{common.HexToHash("0xddd")}, {common.HexToHash("0xeee")}}
	result := FilterLogs(logs, nil, nil, nil, topics)
	if len(result) != 0 {
		t.Fatalf("expected 0 logs (topic count mismatch), got %d", len(result))
	}
}

func TestFilterLogs_EmptyLogs(t *testing.T) {
	result := FilterLogs(nil, nil, nil, nil, nil)
	if len(result) != 0 {
		t.Fatalf("expected 0 logs for nil input, got %d", len(result))
	}
}

func TestFilterLogs_NoMatch(t *testing.T) {
	logs := makeLogs()
	addr := common.HexToAddress("0x9999999999999999999999999999999999999999")
	result := FilterLogs(logs, nil, nil, []common.Address{addr}, nil)
	if len(result) != 0 {
		t.Fatalf("expected 0 logs for non-matching address, got %d", len(result))
	}
}
