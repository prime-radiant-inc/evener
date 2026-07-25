package appprojector

import (
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
)

// kata qm9y: the live hook line and the reloaded one are built by the same
// function. Two builders for one sentence would let a watching reader and a
// returning reader be shown differently-worded accounts of the same hook —
// the smaller version of the divergence this kata is about.
func TestHookAnnouncementIsSharedWithThePersistedShape(t *testing.T) {
	cases := []events.HookEndData{
		{Event: "PreToolUse", HookType: "command", Matcher: "Bash", PluginName: "my-plugin", ExitCode: 0},
		{Event: "Stop", ExitCode: 3},
		{ExitCode: -1},
		{Event: "  ", HookType: " command ", Matcher: "", PluginName: "p", ExitCode: 2},
	}
	for _, data := range cases {
		live := hookEndAnnouncement(data)
		persisted := hookInfoFromEvent(data).Announcement()
		if live != persisted {
			t.Errorf("live = %q, persisted = %q; the two must agree for %+v", live, persisted, data)
		}
	}
}

// The projected shape a live client receives must match the one the persisted
// entry carries field for field, or a reload silently drops hook provenance.
func TestHookInfoFromEventCarriesEveryField(t *testing.T) {
	data := events.HookEndData{
		Event: "PostToolUse", HookType: "command", Matcher: "Read",
		PluginName: "p", ExitCode: 7, DurationMS: 42,
	}
	want := schema.HookInfo{
		Event: "PostToolUse", HookType: "command", Matcher: "Read",
		PluginName: "p", ExitCode: 7, DurationMS: 42,
	}
	if got := hookInfoFromEvent(data); got != want {
		t.Errorf("hookInfoFromEvent = %+v, want %+v", got, want)
	}
}
