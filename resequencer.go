package subspacerelay

import (
	"context"
	"sync"
)

type Resequencer func(ctx context.Context, sequence uint32, cb func(context.Context) error) (err error)

// NoopResequencer is a dummy Resequencer that immediately calls the callback func ignoring the sequence.
func NoopResequencer(ctx context.Context, sequence uint32, cb func(context.Context) error) (err error) {
	return cb(ctx)
}

// NewResequencer will return a Resequencer that will reorder calls based on the provided sequence number before calling
// the callback func.
// Sequence numbers are expected to start at 0 and increase monotonically
// A received sequence number of 0 will reset the counter
func NewResequencer() Resequencer {
	cond := sync.NewCond(new(sync.Mutex))
	var sequence uint32
	return func(ctx context.Context, newSequence uint32, cb func(context.Context) error) (err error) {
		stop := context.AfterFunc(ctx, func() {
			cond.Broadcast()
		})
		defer stop()

		cond.L.Lock()
		if sequence == 0 {
			sequence = newSequence
		}
		for newSequence != sequence {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}

			cond.Wait()
		}
		defer cond.Broadcast()
		defer cond.L.Unlock()

		sequence++
		return cb(ctx)
	}
}
