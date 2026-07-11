package tx_processor

import "context"

func runUnboundedQueue(ctx context.Context, in <-chan uint32, out chan<- uint32) {
	var queue []uint32
	for {
		if len(queue) == 0 {
			select {
			case <-ctx.Done():
				return
			case val, ok := <-in:
				if !ok {
					return
				}
				queue = append(queue, val)
			}
		} else {
			select {
			case <-ctx.Done():
				return
			case val, ok := <-in:
				if !ok {
					return
				}
				queue = append(queue, val)
			case out <- queue[0]:
				queue = queue[1:]
			}
		}
	}
}
