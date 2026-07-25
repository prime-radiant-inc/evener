package main

import (
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// kata qm9y: a hook's typed exit code has to survive the trip from the
// transcript back into a thread item, or the two hook-exit toggles govern
// nothing at all on the reload path.
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
	got := hubDecodedTurn(t, persisted)

	if got.Kind != schema.TurnHookCompleted {
		t.Fatalf("Kind = %q, want %q", got.Kind, schema.TurnHookCompleted)
	}
	if got.Hook == nil {
		t.Fatal("Hook is nil: the hook detail did not survive the transcript round trip")
	}
	if got.Hook.ExitCode != 3 {
		t.Errorf("Hook.ExitCode = %d, want 3", got.Hook.ExitCode)
	}
	if got.Hook.Event != "Stop" || got.Hook.PluginName != "my-plugin" {
		t.Errorf("Hook = %+v, want the persisted detail", got.Hook)
	}
}

// A hook that exited 0 must arrive as a present zero. Serialised away, the
// reloaded item would carry no code and "Hook exits (normal only)" would hide
// the clean hook it exists to show.
func TestReplayedCleanHookKeepsItsZeroExit(t *testing.T) {
	persisted := schema.NewTurn(schema.TurnHookCompleted, llm.System("SessionStart hook exit 0"))
	persisted.Hook = &schema.HookInfo{Event: "SessionStart", ExitCode: 0}

	items := appItemsFromReplayTurn("s", "turn_1", 1, hubDecodedTurn(t, persisted), map[string]string{})

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
