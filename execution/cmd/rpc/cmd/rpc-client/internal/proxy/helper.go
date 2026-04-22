package proxy

import (
	"io"
	"net/http"
	"sync"

	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/constants"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/utils"
)

var requestBufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 16*1024)
		return &buf
	},
}

func ReadBodyWithLimit(r *http.Request) ([]byte, func(), error) {
	bufPtr := requestBufferPool.Get().(*[]byte)
	buf := *bufPtr

	held := 0
	if cap(buf) > 0 {
		utils.AcquireBodyBytes(cap(buf))
		held += cap(buf)
	}

	requestCounted := false
	var once sync.Once
	release := func() {
		once.Do(func() {
			utils.ReleaseBodyBytes(held)
			if requestCounted {
				constants.CurrentBodyRequests.Add(-1)
			}
			if cap(buf) > 256*1024 {
				buf = make([]byte, 0, 16*1024)
			} else {
				buf = buf[:0]
			}
			*bufPtr = buf
			requestBufferPool.Put(bufPtr)
		})
	}

	for {
		if len(buf) == cap(buf) {
			if cap(buf) >= constants.MaxRequestBodyBytes {
				release()
				return nil, func() {}, constants.ErrRequestBodyTooLarge
			}
			oldCap := cap(buf)
			newCap := oldCap * 2
			if newCap == 0 {
				newCap = 4096
			}
			if newCap > constants.MaxRequestBodyBytes {
				newCap = constants.MaxRequestBodyBytes
			}
			utils.AcquireBodyBytes(newCap)
			held += newCap
			newBuf := make([]byte, len(buf), newCap)
			copy(newBuf, buf)
			utils.ReleaseBodyBytes(oldCap)
			held -= oldCap
			buf = newBuf
		}
		readSize := cap(buf) - len(buf)
		tmp := buf[len(buf):cap(buf)]
		n, err := r.Body.Read(tmp)
		buf = buf[:len(buf)+n]
		if err != nil {
			if err == io.EOF {
				break
			}
			release()
			return nil, func() {}, err
		}
		if n == 0 && readSize == 0 {
			break
		}
	}

	constants.CurrentBodyRequests.Add(1)
	requestCounted = true
	size := len(buf)
	constants.CurrentBodyRequests.Add(1)
	constants.CumulativeBodyBytes.Add(int64(size))
	constants.CumulativeBodyCount.Add(1)
	utils.MaybeLogBodyUsage(constants.CurrentBodyBytes.Load())
	return buf[:len(buf)], release, nil
}
