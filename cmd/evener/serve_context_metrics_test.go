package main

import (
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
)

// assumedWindowSampling pins liveThreadEnvelopeSource's schema→server bridge
// under test: the embedded interface leaves every method but ContextStatus
// unreachable, which is fine because the source under test reaches nothing
// else (the bridge's own drain-goroutine doc is the reason the seam stays
// this narrow).
type assumedWindowSampling struct {
	agent.EnvelopeSampling
	metrics schema.ContextMetrics
}

func (s assumedWindowSampling) ContextStatus() (schema.ContextMetrics, float64) {
	return s.metrics, 75.0 / 100.0
}

// TestLiveEnvelopeSourceCarriesAssumedWindow: the assumed flag must survive
// the copy into the hub's server.ContextMetrics (#4596) — on the far side of
// this bridge the meter renders the window, so a dropped flag publishes the
// assumption as fact.
func TestLiveEnvelopeSourceCarriesAssumedWindow(t *testing.T) {
	src := liveThreadEnvelopeSource{session: func() agent.EnvelopeSampling {
		return assumedWindowSampling{metrics: schema.ContextMetrics{Used: 75, Window: 100, Remaining: 25, Assumed: true}}
	}}
	got, pressure := src.ContextStatus()
	if !got.Assumed || got.Window != 100 || got.Used != 75 {
		t.Fatalf("ContextStatus() metrics = %+v, want the figures with the assumed flag carried", got)
	}
	if pressure != 0.75 {
		t.Fatalf("ContextStatus() pressure = %v, want 0.75", pressure)
	}
}
