package utils

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"golang.org/x/sync/semaphore"
)

const MaxInFlightBodyBytes = int64(5 << 30) // 5GB

var (
	BodyMemLimiter      = semaphore.NewWeighted(MaxInFlightBodyBytes)
	CurrentBodyBytes    atomic.Int64
	CurrentBodyRequests atomic.Int64
	BodyUsageBucket     atomic.Int64
	PeakBodyBytes       atomic.Int64
	CumulativeBodyBytes atomic.Int64
	CumulativeBodyCount atomic.Int64
)

func AcquireBodyBytes(n int) {
	if n <= 0 {
		return
	}
	if err := BodyMemLimiter.Acquire(context.Background(), int64(n)); err != nil {
		panic(fmt.Sprintf("failed to acquire body memory: %v", err))
	}
	total := CurrentBodyBytes.Add(int64(n))
	UpdatePeakBodyBytes(total)
	MaybeLogBodyUsage(total)
}

func ReleaseBodyBytes(n int) {
	if n <= 0 {
		return
	}
	BodyMemLimiter.Release(int64(n))
	total := CurrentBodyBytes.Add(-int64(n))
	if total < 0 {
		total = 0
		CurrentBodyBytes.Store(0)
	}
	MaybeLogBodyUsage(total)
}

func UpdatePeakBodyBytes(total int64) {
	for {
		peak := PeakBodyBytes.Load()
		if total <= peak {
			return
		}
		if PeakBodyBytes.CompareAndSwap(peak, total) {
			logger.Info("Đỉnh mới bộ nhớ request đang giữ: %.2f MB", float64(total)/1024/1024)
			return
		}
	}
}

func MaybeLogBodyUsage(total int64) {
	if MaxInFlightBodyBytes <= 0 {
		return
	}
	percent := int64(0)
	if total > 0 {
		percent = total * 100 / MaxInFlightBodyBytes
	}
	bucket := percent / 10
	for {
		prev := BodyUsageBucket.Load()
		if bucket == prev {
			break
		}
		if BodyUsageBucket.CompareAndSwap(prev, bucket) {
			avg := int64(0)
			if cnt := CumulativeBodyCount.Load(); cnt > 0 {
				avg = CumulativeBodyBytes.Load() / cnt
			}
			logger.Info("Bộ nhớ request đang giữ: %.2f MB (%d%%), số request: %d, trung bình: %.2f MB",
				float64(total)/1024/1024, percent, CurrentBodyRequests.Load(), float64(avg)/1024/1024)
			break
		}
	}
}
