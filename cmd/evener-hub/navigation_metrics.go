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

// navigationMetricTotals aggregates a sequence of key-free metric events into
// resource-class/status/byte/duration/counter totals. It never retains a
// navigation identity, query, title, or filesystem path.
type navigationMetricTotals struct {
	Requests          int
	NotModified       int
	UncompressedBytes int
	TransferredBytes  int
	DurationNanos     int64
	ByClass           map[string]navigationMetricClassTotals
}

// navigationMetricClassTotals is one resource-class slice of the aggregate.
type navigationMetricClassTotals struct {
	Requests          int
	UncompressedBytes int
	TransferredBytes  int
	DurationNanos     int64
}

// aggregateNavigationMetrics folds a slice of key-free events into totals. It
// is the live-diagnostics shape the transport budget tests assert against: no
// title, prompt, ref, or path value is retained.
func aggregateNavigationMetrics(events []navigationMetricEvent) navigationMetricTotals {
	totals := navigationMetricTotals{ByClass: make(map[string]navigationMetricClassTotals)}
	for _, event := range events {
		totals.Requests++
		if event.Status == 304 {
			totals.NotModified++
		}
		totals.UncompressedBytes += event.UncompressedBytes
		totals.TransferredBytes += event.TransferredBytes
		totals.DurationNanos += event.DurationNanos
		class := totals.ByClass[event.RouteClass]
		class.Requests++
		class.UncompressedBytes += event.UncompressedBytes
		class.TransferredBytes += event.TransferredBytes
		class.DurationNanos += event.DurationNanos
		totals.ByClass[event.RouteClass] = class
	}
	return totals
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
