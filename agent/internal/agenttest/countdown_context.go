package agenttest

import (
	"context"
	"sync/atomic"
)

// CountdownContext reports itself canceled once its Err method has been
// called more times than Allow, so a test can deterministically stop a scan
// or loop partway through without depending on real time, file size, or
// goroutine scheduling.
type CountdownContext struct {
	context.Context
	Allow int32
}

func (c *CountdownContext) Err() error {
	if atomic.AddInt32(&c.Allow, -1) < 0 {
		return context.Canceled
	}
	return nil
}
