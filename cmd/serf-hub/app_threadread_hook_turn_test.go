package main

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/llm"
)

func replayHookEntry(t *testing.T, persisted schema.Turn) hubcore.ReplayTurn {
	t.Helper()
	raw, err := json.Marshal(transcript.Entry{Kind: "entry", Seq: 1, Turn: persisted})
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	var entry hubcore.ReplayEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal replay entry: %v", err)
	}
	return entry.Turn
}

// kata qm9y: hubcore.ReplayTurn is a hand-maintained partial mirror of
// schema.Turn, so a hook's exit code has to be named here too or the hub's
// reload path drops it — and the two hook-exit toggles go back to governing
// nothing on exactly the path this kata was filed about.
func TestReplayTurnCarriesHookExitCode(t *testing.T) {
	persisted := schema.NewTurn(schema.TurnHookCompleted, llm.System("Stop hook my-plugin exit 3"))
	persisted.Hook = &schema.HookInfo{
		Event:      "Stop",
		HookType:   "command",
		Matcher:    "*",
		PluginName: "my-plugin",
		ExitCode:   3,
		DurationMS: 12,
	}
	got, _ := replayTurnToAgentTurn(replayHookEntry(t, persisted))

	if got.Kind != schema.TurnHookCompleted {
		t.Fatalf("Kind = %q, want %q", got.Kind, schema.TurnHookCompleted)
	}
	if got.Hook == nil {
		t.Fatal("Hook is nil: the hook detail did not survive the ReplayTurn round trip")
	}
	if got.Hook.ExitCode != 3 {
		t.Errorf("Hook.ExitCode = %d, want 3", got.Hook.ExitCode)
	}
	if got.Hook.Event != "Stop" || got.Hook.PluginName != "my-plugin" {
		t.Errorf("Hook = %+v, want the persisted detail", got.Hook)
	}
}

// A hook that exited 0 must arrive as a present zero. If the mirror serialised
// it away, the reloaded item would carry no code and "Hook exits (normal
// only)" would hide the clean hook it exists to show.
func TestReplayedCleanHookKeepsItsZeroExit(t *testing.T) {
	persisted := schema.NewTurn(schema.TurnHookCompleted, llm.System("SessionStart hook exit 0"))
	persisted.Hook = &schema.HookInfo{Event: "SessionStart", ExitCode: 0}

	items := appItemsFromReplayTurn("s", "turn_1", 1, replayHookEntry(t, persisted), map[string]string{})

	if len(items) != 1 {
		t.Fatalf("items = %+v, want exactly one", items)
	}
	if items[0].EventKind != appwire.ThreadItemEventKindHookCompleted {
		t.Errorf("EventKind = %q, want %q", items[0].EventKind, appwire.ThreadItemEventKindHookCompleted)
	}
	if items[0].ExitCode == nil {
		t.Fatal("ExitCode is nil after reload; the normal-only toggle would hide this clean hook")
	}
	if *items[0].ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", *items[0].ExitCode)
	}
}
