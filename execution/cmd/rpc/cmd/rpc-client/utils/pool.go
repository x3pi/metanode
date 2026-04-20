package utils

import "sync"

var RequestBufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 16*1024)
		return &buf
	},
}

var HexDecodePool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 64*1024)
		return &buf
	},
}
