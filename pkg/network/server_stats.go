package network

import (
	"sync/atomic"
	"time"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

func (s *SocketServer) logStats(activeWorkers *int32, processedRequests *int64, workerCount int) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			active := atomic.LoadInt32(activeWorkers)
			processed := atomic.SwapInt64(processedRequests, 0)
			queueLen := len(s.requestChan)
			
			logger.Info("📊 [NET-STATS] active_workers=%d/%d | queue=%d/%d | req_rate=%.1f/s",
				active, workerCount, queueLen, s.config.RequestChanSize, float64(processed)/10.0)
		}
	}
}
