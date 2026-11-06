package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/appwire"
)

// The hub watch tool takes the same arguments as the ordinary watch tool
// wherever they can apply to a session-level watch. These tests pin the
// affordances through configureHubWatchRuntime, the Manager's real tool entry,
// with the shared hub watch machinery (journal, pending rail, projections)
// exercised underneath — the same rails ordinary watches use.

// hubWatchAffordancesRoot wires a Manager session to a stub hub client whose
// first update is preloaded, so a hub watch create settles synchronously.
func hubWatchAffordancesRoot(t *testing.T, first appwire.SessionWatchUpdate) (*Session, *managerWatchClient) {
	t.Helper()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	client := &managerWatchClient{
		view:     appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true},
		updates:  make(chan appwire.SessionWatchUpdate, 8),
		requests: make(chan appwire.SessionWatchRequest, 2),
	}
	client.updates <- first
	root.SetHubClient(client)
	return root, client
}

func hubWorkerSnapshot() appwire.SessionWatchUpdate {
	return appwire.SessionWatchUpdate{
		Snapshot: &appwire.ThreadReadResponse{
			Thread:         appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:worker", InstanceID: "worker-instance"}},
			BootGeneration: "7@worker",
		},
		Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker"},
	}
}

func hubCreateResultState(t *testing.T, result any) jobWatchToolResult {
	t.Helper()
	stateResult, ok := result.(tool.StateResult)
	if !ok {
		t.Fatalf("hub watch create result %T is not a state result", result)
	}
	state, ok := stateResult.State.(jobWatchToolResult)
	if !ok {
		t.Fatalf("hub watch create state %T is not a job_watch result", stateResult.State)
	}
	return state
}

func hubWatchPendingSends(t *testing.T, jm *jobManager, watchID string) []jobstore.WatchSendState {
	t.Helper()
	var sends []jobstore.WatchSendState
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchSendPending && event.WatchSend != nil && event.WatchSend.Key.WatchID == watchID {
			sends = append(sends, *event.WatchSend)
		}
	}
	return sends
}

func hubWatchSentIdentities(sends []jobstore.WatchSendState) []string {
	identities := make([]string, 0, len(sends))
	for _, send := range sends {
		identities = append(identities, send.TriggerIdentity)
	}
	return identities
}

// The acceptance centerpiece: note must work end to end on hub watches —
// accepted at create, reported by the create result, stored on the durable
// watch record, shown by job_watch list and inspect, and delivered with every
// fire of the watch, exactly like an ordinary watch.
func TestHubWatchNoteRidesCreateListInspectAndDelivery(t *testing.T) {
	t.Parallel()
	const note = "annotating this watch per the fleet checklist"
	root, _ := hubWatchAffordancesRoot(t, hubWorkerSnapshot())

	result, err := root.configureHubWatchRuntime(t.Context(), map[string]any{
		"operation": "create",
		"hub":       map[string]any{"ref": "local:worker", "events": []string{"communicate"}},
		"note":      note,
	})
	if err != nil {
		t.Fatalf("hub watch create with note rejected: %v", err)
	}
	state := hubCreateResultState(t, result)
	if state.Note != note {
		t.Fatalf("create result note=%q, want %q", state.Note, note)
	}
	jm := root.jobManager

	registered := false
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchRegistered && event.Watch != nil && event.Watch.Config != nil && event.WatchID == state.WatchID {
			registered = true
			if event.Watch.Config.Note != note {
				t.Fatalf("registered watch note=%q, want %q", event.Watch.Config.Note, note)
			}
		}
	}
	if !registered {
		t.Fatal("hub watch registration record not found")
	}

	var listed jobWatchInspectToolResult
	for _, row := range jm.watchListToolResult().Watches {
		if row.WatchID == state.WatchID {
			listed = row
		}
	}
	if listed.WatchID == "" {
		t.Fatal("hub watch missing from job_watch list")
	}
	if listed.Note != note {
		t.Fatalf("list row note=%q, want %q", listed.Note, note)
	}
	if inspect := jm.inspectWatchByID(state.WatchID); inspect.Note != note {
		t.Fatalf("inspect note=%q, want %q", inspect.Note, note)
	}

	if err := jm.onHubObservation(state.WatchID, appwire.SessionWatchUpdate{
		Event: "communicate",
		Metadata: appwire.SessionObservationMetadata{
			Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker",
			EntryID: "result", CausalKnown: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	sends := hubWatchPendingSends(t, jm, state.WatchID)
	if len(sends) != 1 {
		t.Fatalf("observation deliveries=%d, want 1", len(sends))
	}
	if !strings.Contains(sends[0].Frame, note) {
		t.Fatalf("delivered frame does not carry the note:\n%s", sends[0].Frame)
	}
}

// The roster watch is also a watch the Manager annotates: its note rides the
// initial snapshot frame like any other watch payload.
func TestHubRosterWatchNoteRidesTheRosterFrame(t *testing.T) {
	t.Parallel()
	const note = "fleet roster annotated for the morning sweep"
	root, _ := hubWatchAffordancesRoot(t, rosterUpdate(1, true, rosterEntry("local:worker", "new")))

	result, err := root.configureHubWatchRuntime(t.Context(), map[string]any{
		"operation": "create",
		"hub":       map[string]any{"scope": "top_level"},
		"note":      note,
	})
	if err != nil {
		t.Fatalf("roster watch create with note rejected: %v", err)
	}
	state := hubCreateResultState(t, result)
	if state.Note != note {
		t.Fatalf("create result note=%q, want %q", state.Note, note)
	}
	sends := hubWatchPendingSends(t, root.jobManager, state.WatchID)
	if len(sends) != 1 {
		t.Fatalf("roster deliveries=%d, want the initial snapshot", len(sends))
	}
	if !strings.Contains(sends[0].Frame, note) {
		t.Fatalf("roster frame does not carry the note:\n%s", sends[0].Frame)
	}
}

// A hub watch create installs a watch like the ordinary path: re-registering
// the same target with a different note replaces the watch (the note is part
// of the watch's identity), while an identical re-registration is the same
// watch.
func TestHubWatchNoteChangeReplacesWatch(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	jm := root.jobManager
	register := func(note string) watchResult {
		result, err := jm.configureHubWatchContext(t.Context(), "local:worker", "worker-instance", "7@worker", "local:manager", []string{"communicate"}, false, note, 0)
		if err != nil {
			t.Fatalf("hub watch create with note %q rejected: %v", note, err)
		}
		return result
	}
	first := register("first note")
	if first.ReplacedExisting {
		t.Fatal("first registration replaced a watch")
	}
	second := register("second note")
	if !second.ReplacedExisting {
		t.Fatal("re-registration with a different note did not replace the watch")
	}
	if inspect := jm.inspectWatchByID(second.WatchID); inspect.Note != "second note" {
		t.Fatalf("live watch note after replacement=%q, want the second note", inspect.Note)
	}
	third := register("second note")
	if third.ReplacedExisting {
		t.Fatal("identical re-registration replaced the watch")
	}
	if third.WatchID != second.WatchID {
		t.Fatalf("identical re-registration minted a new watch: %q vs %q", third.WatchID, second.WatchID)
	}
	// A changed every replaces the watch the same way a changed note does.
	fourth, err := jm.configureHubWatchContext(t.Context(), "local:worker", "worker-instance", "7@worker", "local:manager", []string{"communicate"}, false, "second note", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !fourth.ReplacedExisting {
		t.Fatal("re-registration with a changed every did not replace the watch")
	}
}

// every maps onto the hub event vocabulary: with one selected kind, each Nth
// matching observation delivers. A replay of a suppressed entry is not a new
// occurrence — it must not advance the throttle count.
func TestHubWatchEveryThrottlesObservationFires(t *testing.T) {
	t.Parallel()
	root, _ := hubWatchAffordancesRoot(t, hubWorkerSnapshot())
	result, err := root.configureHubWatchRuntime(t.Context(), map[string]any{
		"operation": "create",
		"hub":       map[string]any{"ref": "local:worker", "events": []string{"communicate"}},
		"every":     float64(2),
	})
	if err != nil {
		t.Fatalf("hub watch create with every rejected: %v", err)
	}
	state := hubCreateResultState(t, result)
	jm := root.jobManager

	observe := func(entry string) error {
		return jm.onHubObservation(state.WatchID, appwire.SessionWatchUpdate{
			Event: "communicate",
			Metadata: appwire.SessionObservationMetadata{
				Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker",
				EntryID: entry, CausalKnown: true,
			},
		})
	}
	for _, entry := range []string{"first", "second", "third"} {
		if err := observe(entry); err != nil {
			t.Fatal(err)
		}
	}
	// The first entry was suppressed by the throttle, so its replay dedups
	// rather than counting again and delivering the third observation.
	if err := observe("first"); err != nil {
		t.Fatal(err)
	}

	sends := hubWatchPendingSends(t, jm, state.WatchID)
	wantIdentity := "worker-instance/7@worker/second:communicate"
	if len(sends) != 1 || sends[0].TriggerIdentity != wantIdentity {
		t.Fatalf("delivered observation identities=%v, want exactly [%s]", hubWatchSentIdentities(sends), wantIdentity)
	}
}

// every:1 is the unset default, like the ordinary path: every matching
// observation delivers.
func TestHubWatchEveryOneDeliversEveryObservation(t *testing.T) {
	t.Parallel()
	root, _ := hubWatchAffordancesRoot(t, hubWorkerSnapshot())
	result, err := root.configureHubWatchRuntime(t.Context(), map[string]any{
		"operation": "create",
		"hub":       map[string]any{"ref": "local:worker", "events": []string{"communicate"}},
		"every":     float64(1),
	})
	if err != nil {
		t.Fatalf("hub watch create with every=1 rejected: %v", err)
	}
	state := hubCreateResultState(t, result)
	jm := root.jobManager

	for _, entry := range []string{"first", "second"} {
		if err := jm.onHubObservation(state.WatchID, appwire.SessionWatchUpdate{
			Event: "communicate",
			Metadata: appwire.SessionObservationMetadata{
				Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker",
				EntryID: entry, CausalKnown: true,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	sends := hubWatchPendingSends(t, jm, state.WatchID)
	if len(sends) != 2 {
		t.Fatalf("delivered observation identities=%v, want both observations delivered", hubWatchSentIdentities(sends))
	}
}

// Completion is the watch's terminal condition: it delivers and ends the
// watch even when the throttle would have suppressed an ordinary observation.
func TestHubWatchCompletionDeliversDespiteEveryThrottle(t *testing.T) {
	t.Parallel()
	root, _ := hubWatchAffordancesRoot(t, hubWorkerSnapshot())
	result, err := root.configureHubWatchRuntime(t.Context(), map[string]any{
		"operation": "create",
		"hub":       map[string]any{"ref": "local:worker", "events": []string{"communicate"}, "completion": true},
		"every":     float64(2),
	})
	if err != nil {
		t.Fatalf("hub watch create with every and completion rejected: %v", err)
	}
	state := hubCreateResultState(t, result)
	jm := root.jobManager

	observe := func(entry string, terminal bool) error {
		metadata := appwire.SessionObservationMetadata{
			Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker",
			EntryID: entry, CausalKnown: true,
		}
		if terminal {
			accepted := true
			metadata.EndTurn, metadata.EndReason, metadata.TerminalAccepted = true, "done", &accepted
		}
		return jm.onHubObservation(state.WatchID, appwire.SessionWatchUpdate{Event: "communicate", Metadata: metadata})
	}
	if err := observe("suppressed", false); err != nil {
		t.Fatal(err)
	}
	if err := observe("done-entry", true); err != nil {
		t.Fatal(err)
	}
	if managerWatchActive(jm, state.WatchID) {
		t.Fatal("accepted terminal done did not complete the throttled watch")
	}
	completions := 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchSend != nil && event.WatchSend.Key.WatchID == state.WatchID &&
			event.WatchSend.TriggerIdentity == "worker-instance/7@worker/done-entry:communicate:completed" && !event.WatchSend.EndNotice {
			completions++
		}
	}
	if completions != 1 {
		t.Fatalf("completion deliveries=%d, want 1", completions)
	}
	if sends := hubWatchPendingSends(t, jm, state.WatchID); len(sends) != 1 {
		t.Fatalf("non-completion deliveries=%v, want the suppressed observation silent", hubWatchSentIdentities(sends))
	}
}

// every follows the ordinary path's shape rule: it counts one selected event
// kind, and the roster's fixed events are not selectable.
func TestHubWatchEveryRequiresOneSelectedKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		hub  map[string]any
	}{
		{"no selected kind", map[string]any{"ref": "local:worker", "completion": true}},
		{"several selected kinds", map[string]any{"ref": "local:worker", "events": []string{"communicate", "question"}}},
		{"roster scope", map[string]any{"scope": "top_level"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{"operation": "create", "hub": tc.hub, "every": float64(2)}
			_, err := (&Session{}).configureHubWatchRuntime(context.Background(), args)
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
				t.Fatalf("every on %v: got %v, want typed invalid params", tc.hub, err)
			}
			if !strings.Contains(err.Error(), "every") {
				t.Fatalf("error does not name the refused argument every: %v", err)
			}
		})
	}
}

// Structurally inapplicable ordinary arguments are refused each with an error
// naming that argument and why, before any client contact — the refusal says
// which of the two vocabularies the call reached for.
func TestHubWatchRefusesInapplicableOrdinaryArgumentsSeparately(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		argument string
		args     map[string]any
	}{
		{"output_match", "output_match", map[string]any{"output_match": "READY"}},
		{"progress_interval_ms", "progress_interval_ms", map[string]any{"progress_interval_ms": float64(60000)}},
		{"after_seconds", "after_seconds", map[string]any{"after_seconds": float64(600)}},
		{"repeat_seconds", "repeat_seconds", map[string]any{"repeat_seconds": float64(300)}},
		{"event_filter", "event_filter", map[string]any{"event_filter": map[string]any{"tool_name": "read_file"}}},
		{"top-level events", "events", map[string]any{"events": []string{"communicate"}}},
		{"source", "source", map[string]any{"source": "self"}},
		{"watch_id", "watch_id", map[string]any{"watch_id": "watch_abc"}},
		{"target", "target", map[string]any{"target": "job_1"}},
		{"send", "send", map[string]any{"send": map[string]any{"to": "main", "message": "hi"}}},
		{"receiver_session_id", "receiver_session_id", map[string]any{"receiver_session_id": "local:other"}},
		{"receiver_delegate_id", "receiver_delegate_id", map[string]any{"receiver_delegate_id": "dlg_abc"}},
		{"unknown argument", "deadline", map[string]any{"deadline": float64(600)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{"operation": "create", "hub": map[string]any{"ref": "local:worker", "events": []string{"status"}}}
			maps.Copy(args, tc.args)
			// The session has no hub client, so an argument-level refusal is
			// the only way this call can fail before client contact: an error
			// typed invalid params names the argument, and any later failure
			// would carry the unavailable-client code instead.
			_, err := (&Session{}).configureHubWatchRuntime(context.Background(), args)
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
				t.Fatalf("got %v, want typed invalid params", err)
			}
			// The argument name is the wire contract, not prose: the refusal
			// must say which argument cannot apply.
			if !strings.Contains(err.Error(), tc.argument) {
				t.Fatalf("error does not name the refused argument %q: %v", tc.argument, err)
			}
		})
	}
}

// Providers materialize optional properties; present-but-neutral values of the
// ordinary vocabulary read as omitted, exactly as on the ordinary path, and
// must not refuse a hub watch create.
func TestHubWatchToleratesProviderMaterializedNeutralArguments(t *testing.T) {
	t.Parallel()
	root, _ := hubWatchAffordancesRoot(t, hubWorkerSnapshot())
	result, err := root.configureHubWatchRuntime(t.Context(), map[string]any{
		"operation":            "create",
		"hub":                  map[string]any{"ref": "local:worker", "events": []string{"status"}},
		"intent":               "checking the fleet",
		"note":                 "",
		"every":                float64(0),
		"events":               []any{},
		"output_match":         "",
		"progress_interval_ms": float64(0),
		"after_seconds":        float64(0),
		"repeat_seconds":       float64(0),
		"event_filter":         nil,
		"source":               "",
		"watch_id":             "",
		"target":               "",
		"send":                 nil,
		"receiver_session_id":  "",
		"receiver_delegate_id": "",
	})
	if err != nil {
		t.Fatalf("hub watch create with neutral ordinary arguments rejected: %v", err)
	}
	state := hubCreateResultState(t, result)
	if !managerWatchActive(root.jobManager, state.WatchID) {
		t.Fatal("hub watch not registered after create")
	}
}

// The Manager's job_watch schema advertises the ordinary arguments that apply:
// note and every sit beside the hub object, and the hub object itself carries
// only its own selectors.
func TestHubWatchSchemaAdvertisesOrdinaryArguments(t *testing.T) {
	t.Parallel()
	_, wire := managerProviderSchemas(t)
	encoded, err := json.Marshal(wire["job_watch"])
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.CompileString("https://manager.test/hub-watch-affordances.json", string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		args  map[string]any
		valid bool
	}{
		{"note and every beside hub", map[string]any{
			"operation": "create",
			"hub":       map[string]any{"ref": "local:worker", "events": []any{"communicate"}},
			"intent":    "watching the worker",
			"note":      "why I armed it",
			"every":     2,
		}, true},
		{"note beside roster scope", map[string]any{
			"operation": "create",
			"hub":       map[string]any{"scope": "top_level"},
			"intent":    "watching the roster",
			"note":      "why I armed it",
		}, true},
		{"note inside the hub object", map[string]any{
			"operation": "create",
			"hub":       map[string]any{"ref": "local:worker", "events": []any{"communicate"}, "note": "misplaced"},
			"intent":    "watching the worker",
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := schema.Validate(tc.args); (err == nil) != tc.valid {
				t.Errorf("valid=%v want %v: %v", err == nil, tc.valid, err)
			}
		})
	}
}
