package tx_processor

import (
	"container/heap"
	"context"
)

// minHeap implements heap.Interface for a queue of uint32
type minHeap []uint32

func (h minHeap) Len() int           { return len(h) }
func (h minHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h minHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *minHeap) Push(x any) {
	*h = append(*h, x.(uint32))
}

func (h *minHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

func runUnboundedQueue(ctx context.Context, in <-chan uint32, out chan<- uint32) {
	h := &minHeap{}
	heap.Init(h)

	for {
		if h.Len() == 0 {
			select {
			case <-ctx.Done():
				return
			case val, ok := <-in:
				if !ok {
					return
				}
				heap.Push(h, val)
			}
		} else {
			// Peek the smallest element
			minVal := (*h)[0]
			select {
			case <-ctx.Done():
				return
			case val, ok := <-in:
				if !ok {
					return
				}
				heap.Push(h, val)
			case out <- minVal:
				heap.Pop(h)
			}
		}
	}
}
