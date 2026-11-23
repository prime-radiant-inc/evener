package agent

import (
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provenance"
)

// buildWatchFrameForTest renders a frame with no hub observation, the shape the
// frame renderer's callers outside the hub delivery path use.
func (jm *jobManager) buildWatchFrameForTest(cfg *watchConfig, jobID, trigger, deliveryID string, ev events.SessionEvent, p *provenance.Causal) string {
	return jm.buildWatchFrame(cfg, jobID, trigger, deliveryID, ev, p, nil)
}
