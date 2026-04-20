package constants

import (
	"errors"
	"sync/atomic"

	"golang.org/x/sync/semaphore"
)

const (
	MaxRequestBodyBytes  = 8 << 20 // 8MB
	MaxInFlightBodyBytes = int64(5 << 30)
)

var BodyMemLimiter = semaphore.NewWeighted(MaxInFlightBodyBytes)
var CurrentBodyBytes atomic.Int64
var CurrentBodyRequests atomic.Int64
var BodyUsageBucket atomic.Int64
var PeakBodyBytes atomic.Int64
var CumulativeBodyBytes atomic.Int64
var CumulativeBodyCount atomic.Int64
var DefaultLogsDir string
var ErrRequestBodyTooLarge = errors.New("request body exceeded limit")
