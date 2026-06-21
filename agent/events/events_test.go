package events_test

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provenance"
)

func TestSessionEventCarriesCausalProvenanceOnEnvelope(t *testing.T) {
	ev := events.New(events.CommunicateData{EndTurn: false, Message: "actually alpha marker"})
	ev.SessionID = "session_1"
	ev.Provenance = provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_1", "caller")

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"provenance"`, `"watch_id":"watch_A"`, `"watch_generation":"wg_1"`, `"delivery_id":"wd_1"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("event JSON missing %s: %s", want, s)
		}
	}
	if strings.Contains(s, `"data":{"provenance"`) {
		t.Fatalf("provenance must live on event envelope, not payload: %s", s)
	}
}
