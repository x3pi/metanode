package utils

import (
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// StartWatchdog starts a background goroutine that prints a warning if it takes longer than the timeout.
// getContext is called every time the timer fires to get the latest status.
// It returns a Stop function that MUST be called when the tracked process finishes.
func StartWatchdog(name string, timeout time.Duration, getContext func() string) func() {
	doneChan := make(chan struct{})
	startTime := time.Now()

	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				logger.Warn("🆘 [WATCHDOG-%s] TIẾN TRÌNH KẸT QUÁ LÂU (%v)! Ngữ cảnh: %s", name, time.Since(startTime), getContext())
				timer.Reset(timeout)
			case <-doneChan:
				return
			}
		}
	}()

	return func() {
		close(doneChan)
	}
}
