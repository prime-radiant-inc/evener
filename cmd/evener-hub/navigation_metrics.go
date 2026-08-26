package hub

import "sync"

// navigationMetricEvent is deliberately key-free. It describes transport work
// without retaining a navigation identity, query, title, or filesystem path.
type navigationMetricEvent struct {
	RouteClass        string
	Status            int
	Encoding          string
	Conditional       bool
	UncompressedBytes int
	TransferredBytes  int
	DurationNanos     int64
}

// navigationMetricSink is injectable on WebServer for transport observation.
// Implementations must not infer or attach resource identities to an event.
type navigationMetricSink interface {
	RecordNavigationMetric(navigationMetricEvent)
}

// navigationMetricFunc makes focused tests able to capture events without a
// global registry. Its mutex permits concurrent HTTP requests safely.
type navigationMetricFunc struct {
	mu sync.Mutex
	fn func(navigationMetricEvent)
}

func (f *navigationMetricFunc) RecordNavigationMetric(event navigationMetricEvent) {
	if f == nil || f.fn == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fn(event)
}

func (s *WebServer) recordNavigationMetric(event navigationMetricEvent) {
	if s.navigationMetrics != nil {
		s.navigationMetrics.RecordNavigationMetric(event)
	}
}
