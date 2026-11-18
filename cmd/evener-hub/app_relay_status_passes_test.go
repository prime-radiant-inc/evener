package hub

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

// The relay fan-out stamps a status frame's hub-owned capability answers once
// per route, and a status frame is the frame every subscribed session's browser
// sees most (finding #6421). The four single-answer stampers used to run in
// sequence each decoded the params and re-encoded them to patch one field, so
// one route paid six to eight JSON passes and every intermediate map for one
// frame. The fan-out now resolves its three answers per route, which it must:
// each depends on the thread that route names. What it no longer does is decode
// and encode the frame once per answer.
//
// This bounds the allocations of one stamp, which is what those extra passes
// cost. It is a regression guard rather than a benchmark: the pass count itself
// is pinned by the measurement in #6421's report, since encoding/json offers no
// seam to count its passes from a test.
func TestRelayedStatusFrameStampsCapabilitiesWithoutAPassPerAnswer(t *testing.T) {
	frames := []struct {
		name   string
		params string
	}{
		// The common case: an open thread whose daemon sent a capability set.
		{"open with a capability set", `{"threadId":"t1","ref":"local:t1","status":{"type":"active"},"capabilities":{"send":true,"compact":true}}`},
		// A close carries no set, so the hub mints one.
		{"closed with no capability set", `{"threadId":"t1","ref":"local:t1","status":{"type":"closed"}}`},
		// A daemon that sent a set on its close frame.
		{"closed with a capability set", `{"threadId":"t1","ref":"local:t1","status":{"type":"closed"},"capabilities":{"send":false}}`},
	}
	answers := relayCapabilityAnswers{forkFromTurn: true, pageBefore: true, voiceArm: false}
	// Ceilings sit above the one-pass stamp's measured cost and below the
	// four-stamper sequence's, so removing the single pass fails this test.
	//
	// Measured with testing.AllocsPerRun(200) on the same three frames:
	//   one-pass stamp (this code):      41,  74,  75
	//   four stampers in sequence:      125, 225, 226
	ceilings := []float64{60, 100, 100}
	for i, frame := range frames {
		t.Run(frame.name, func(t *testing.T) {
			notification := appwire.Notification{
				Method: appwire.NotifyThreadStatusChanged,
				Params: json.RawMessage(frame.params),
			}
			allocs := testing.AllocsPerRun(200, func() {
				_ = stampStatusCapabilities(notification, answers)
			})
			if allocs > ceilings[i] {
				t.Fatalf("stamping one status frame allocated %v times, want at most %v", allocs, ceilings[i])
			}
		})
	}
}
