package metrics

import (
	"testing"
	"time"
)

func TestObserveDuration(t *testing.T) {
	start := time.Now().Add(-100 * time.Millisecond)
	// Should not panic
	ObserveDuration(BlockProcessingDuration, start)
}

func TestMetricsRegistered(t *testing.T) {
	// Verify that key metrics are not nil (they were registered via promauto)
	if RPCRequestsTotal == nil {
		t.Error("RPCRequestsTotal is nil")
	}
	if RPCErrorsTotal == nil {
		t.Error("RPCErrorsTotal is nil")
	}
	if BlocksProcessedTotal == nil {
		t.Error("BlocksProcessedTotal is nil")
	}
	if TxsReceivedTotal == nil {
		t.Error("TxsReceivedTotal is nil")
	}
	if TxsProcessedTotal == nil {
		t.Error("TxsProcessedTotal is nil")
	}
	if CurrentBlock == nil {
		t.Error("CurrentBlock is nil")
	}
	if CurrentEpoch == nil {
		t.Error("CurrentEpoch is nil")
	}
	if TxPoolSize == nil {
		t.Error("TxPoolSize is nil")
	}
	if BlockProcessingDuration == nil {
		t.Error("BlockProcessingDuration is nil")
	}
}

func TestCounterIncrement(t *testing.T) {
	// Should not panic
	BlocksProcessedTotal.Inc()
	TxsReceivedTotal.Inc()
	TxsProcessedTotal.Inc()
}

func TestGaugeSet(t *testing.T) {
	// Should not panic
	CurrentBlock.Set(42)
	CurrentEpoch.Set(1)
	TxPoolSize.Set(100)
	Goroutines.Set(50)
	HeapAllocBytes.Set(1024 * 1024)
	PeersConnected.Set(5)
}

func TestHistogramObserve(t *testing.T) {
	// Should not panic
	BlockProcessingDuration.Observe(0.1)
	TxProcessingDuration.Observe(0.05)
	RPCDuration.WithLabelValues("eth_blockNumber").Observe(0.001)
}
