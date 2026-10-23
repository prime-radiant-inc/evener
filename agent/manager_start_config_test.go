package agent

import (
	"encoding/json"
	"testing"
)

// TestManagerSurvivesConfigRoundTrip pins that the start-time Manager property
// is durable: it is written into the persisted config and read back on resume.
//
// Manager is not a setting, so nothing in the hub re-asserts it after launch. If
// the round trip dropped it, a resumed Manager session would come back ordinary
// and no later call could fix that.
func TestManagerSurvivesConfigRoundTrip(t *testing.T) {
	for _, want := range []bool{true, false} {
		cfg := SessionConfig{Manager: want}
		if got := configFromSnapshot(cfg.toSnapshot()); got.Manager != want {
			t.Fatalf("Manager=%v round-tripped to %v", want, got.Manager)
		}
	}

	// The persisted JSON carries the property, so a meta written by this build
	// reloads as a Manager. Field name is the contract; a rename would silently
	// demote every saved Manager session.
	raw, err := json.Marshal(SessionConfig{Manager: true}.toSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["manager"] != true {
		t.Fatalf("persisted snapshot has no manager property: %s", raw)
	}

	// A session started without the flag must not acquire it by loading a meta
	// that never mentioned it.
	var ordinary SessionConfig
	if err := json.Unmarshal([]byte(`{}`), &ordinary); err != nil {
		t.Fatal(err)
	}
	if configFromSnapshot(ordinary.toSnapshot()).Manager {
		t.Fatal("a session without the flag restored as a Manager")
	}
}
