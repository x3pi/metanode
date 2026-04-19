package stats

import (
	"testing"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

func TestStatsMarshalUnmarshal(t *testing.T) {
	original := &Stats{
		PbStats: &pb.Stats{
			TotalMemory:   1024 * 1024 * 100,
			HeapMemory:    1024 * 1024 * 50,
			NumGoroutines: 42,
			Uptime:        3600,
		},
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	restored := &Stats{}
	err = restored.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if restored.PbStats.TotalMemory != original.PbStats.TotalMemory {
		t.Errorf("TotalMemory mismatch: got %d, want %d",
			restored.PbStats.TotalMemory, original.PbStats.TotalMemory)
	}
	if restored.PbStats.HeapMemory != original.PbStats.HeapMemory {
		t.Errorf("HeapMemory mismatch: got %d, want %d",
			restored.PbStats.HeapMemory, original.PbStats.HeapMemory)
	}
	if restored.PbStats.NumGoroutines != original.PbStats.NumGoroutines {
		t.Errorf("NumGoroutines mismatch: got %d, want %d",
			restored.PbStats.NumGoroutines, original.PbStats.NumGoroutines)
	}
	if restored.PbStats.Uptime != original.PbStats.Uptime {
		t.Errorf("Uptime mismatch: got %d, want %d",
			restored.PbStats.Uptime, original.PbStats.Uptime)
	}
}

func TestStatsString(t *testing.T) {
	stats := &Stats{
		PbStats: &pb.Stats{
			TotalMemory:   1024,
			HeapMemory:    512,
			NumGoroutines: 10,
			Uptime:        60,
			Network:       &pb.NetworkStats{},
		},
	}

	str := stats.String()
	if len(str) == 0 {
		t.Error("String() returned empty string")
	}
}

func TestStatsUnmarshalInvalidData(t *testing.T) {
	s := &Stats{}
	err := s.Unmarshal([]byte{0xFF, 0xFF, 0xFF})
	if err == nil {
		t.Error("Expected error when unmarshaling invalid data, got nil")
	}
}

func TestStatsMarshalEmpty(t *testing.T) {
	s := &Stats{PbStats: &pb.Stats{}}
	data, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal empty stats failed: %v", err)
	}

	restored := &Stats{}
	err = restored.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal empty stats failed: %v", err)
	}
	if restored.PbStats.TotalMemory != 0 {
		t.Errorf("Expected 0, got %d", restored.PbStats.TotalMemory)
	}
}
