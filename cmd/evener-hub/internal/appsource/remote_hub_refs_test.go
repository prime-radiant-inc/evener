package appsource

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// allRemoteThreadCapabilities is every thread action a remote hub can advertise,
// spelled out field by field so a fixture cannot silently stop covering a field.
func allRemoteThreadCapabilities() appwire.ThreadCapabilities {
	return appwire.ThreadCapabilities{
		Send:              true,
		Steer:             true,
		Interrupt:         true,
		Compact:           true,
		Clear:             true,
		ForkFromTurn:      true,
		Shutdown:          true,
		ChangeModel:       true,
		ChangeVisionModel: true,
		Queue:             true,
		Goal:              true,
		SharedNotes:       true,
		Rename:            true,
		SkillInput:        true,
	}
}

// A remote hub reports the capability set of its OWN session daemon. Those flags
// describe what the remote daemon can do, not what the controller can forward:
// every mutation, lifecycle, and subscription method on RemoteHubSource is still
// staged, so a thread read from a remote hub must not advertise an action that
// would fail with an internal error the moment a client used it.
func TestRemoteHubFromRemoteThreadMasksUnforwardedCapabilities(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	thread, err := source.fromRemoteThread(appwire.Thread{
		ID:     "t1",
		Source: "local",
		Evener: appwire.EvenerThread{
			Ref:          "local:t1",
			InstanceID:   "inst-1",
			Capabilities: allRemoteThreadCapabilities(),
		},
	})
	if err != nil {
		t.Fatalf("fromRemoteThread: %v", err)
	}
	if got := maskRemoteThreadCapabilities(allRemoteThreadCapabilities()); got != (appwire.ThreadCapabilities{}) {
		t.Fatalf("masked capabilities = %+v, want every staged action masked", got)
	}
	if thread.Evener.Capabilities != (appwire.ThreadCapabilities{}) {
		t.Fatalf("remote thread advertised %+v, want the ref translation to mask staged actions", thread.Evener.Capabilities)
	}
	// The mask is a filter over the remote's own claim, not a replacement for it:
	// nothing outside the capability set may change.
	if thread.Source != "host" || thread.Evener.Ref != "host:t1" || thread.Evener.InstanceID != "inst-1" {
		t.Fatalf("masked thread = %+v, want refs and opaque fields untouched", thread.Evener)
	}
}

// Every capability a remote thread is allowed to advertise must have a working
// method behind it. The staged methods below are the 05c/05d work: 05c forwards
// the turn mutations and 05d intersects the result with a completed capability
// probe, and each re-enables its capability in remoteForwardedThreadCapabilities.
// Enumerating the promise here means a field cannot be flipped on while its
// method still fails closed, and a future capability field cannot be added
// without deciding which method answers for it.
func TestRemoteHubCapabilitiesMatchForwardedMethods(t *testing.T) {
	ctx := context.Background()
	source := NewRemoteHubSource("host", nil, nil)
	cases := []struct {
		field   string
		methods map[string]func() error
	}{
		{"Send", map[string]func() error{
			"StartTurn": func() error { _, err := source.StartTurn(ctx, appwire.TurnStartParams{}); return err },
			"ResumeThread": func() error {
				_, err := source.ResumeThread(ctx, appwire.ThreadResumeParams{})
				return err
			},
		}},
		{"Steer", map[string]func() error{
			"SteerTurn": func() error { _, err := source.SteerTurn(ctx, appwire.TurnSteerParams{}); return err },
		}},
		{"Interrupt", map[string]func() error{
			"InterruptTurn": func() error { _, err := source.InterruptTurn(ctx, appwire.TurnInterruptParams{}); return err },
		}},
		{"Compact", map[string]func() error{
			"CompactThread": func() error { return source.CompactThread(ctx, appwire.ThreadCompactStartParams{}) },
		}},
		{"Clear", map[string]func() error{
			"ClearThread": func() error { _, err := source.ClearThread(ctx, appwire.ThreadClearParams{}); return err },
		}},
		{"ForkFromTurn", map[string]func() error{
			"ForkThread": func() error { _, err := source.ForkThread(ctx, appwire.ThreadForkParams{}); return err },
		}},
		{"Shutdown", map[string]func() error{
			"ShutdownThread": func() error { return source.ShutdownThread(ctx, appwire.ThreadShutdownParams{}) },
		}},
		{"ChangeModel", map[string]func() error{
			"SetThreadModel": func() error { return source.SetThreadModel(ctx, appwire.ThreadModelSetParams{}) },
			"SetThreadReasoningEffort": func() error {
				return source.SetThreadReasoningEffort(ctx, appwire.ThreadReasoningEffortSetParams{})
			},
		}},
		{"ChangeVisionModel", map[string]func() error{
			"SetThreadVisionModel": func() error { return source.SetThreadVisionModel(ctx, appwire.ThreadVisionModelSetParams{}) },
		}},
		{"Queue", map[string]func() error{
			"QueueTurn":    func() error { _, err := source.QueueTurn(ctx, appwire.TurnQueueParams{}); return err },
			"CancelQueued": func() error { _, err := source.CancelQueued(ctx, appwire.TurnCancelQueuedParams{}); return err },
			"DrainAsSteer": func() error {
				_, err := source.DrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{})
				return err
			},
			"PromoteQueuedAsSteer": func() error {
				_, err := source.PromoteQueuedAsSteer(ctx, appwire.TurnPromoteQueuedAsSteerParams{})
				return err
			},
		}},
		{"Goal", map[string]func() error{
			"GoalSet": func() error { _, err := source.GoalSet(ctx, appwire.GoalSetParams{}); return err },
		}},
		{"SharedNotes", map[string]func() error{
			"NotesHumanSet": func() error { _, err := source.NotesHumanSet(ctx, appwire.NotesHumanSetParams{}); return err },
			"UrlsRemove":    func() error { _, err := source.UrlsRemove(ctx, appwire.UrlsRemoveParams{}); return err },
		}},
		{"Rename", map[string]func() error{
			"SetThreadName": func() error { return source.SetThreadName(ctx, appwire.ThreadNameSetParams{}) },
		}},
		{"SkillInput", map[string]func() error{
			"StartTurn": func() error { _, err := source.StartTurn(ctx, appwire.TurnStartParams{}); return err },
			"QueueTurn": func() error { _, err := source.QueueTurn(ctx, appwire.TurnQueueParams{}); return err },
		}},
	}

	masked := maskRemoteThreadCapabilities(allRemoteThreadCapabilities())
	enumerated := make(map[string]bool, len(cases))
	for _, tc := range cases {
		if enumerated[tc.field] {
			t.Errorf("capability %s is enumerated twice", tc.field)
		}
		enumerated[tc.field] = true
		for name, call := range tc.methods {
			err := call()
			// A method that stops failing closed must have its capability re-enabled
			// in remoteForwardedThreadCapabilities by whichever component implements
			// it, otherwise the flag and the method disagree.
			if err == nil || !strings.Contains(err.Error(), "not implemented yet") {
				t.Errorf("%s = %v, want the staged notImplemented error until its capability is forwarded", name, err)
			}
		}
		advertised, ok := capabilityFieldValue(masked, tc.field)
		if !ok {
			t.Errorf("capability %s is not a field of appwire.ThreadCapabilities", tc.field)
			continue
		}
		if advertised {
			t.Errorf("capability %s is advertised while its methods still fail closed", tc.field)
		}
	}

	for field := range reflect.TypeFor[appwire.ThreadCapabilities]().Fields() {
		if !enumerated[field.Name] {
			t.Errorf("capability field %s is not covered by this test's method table", field.Name)
		}
	}
}

// capabilityFieldValue reads one named bool field of a capability set.
func capabilityFieldValue(caps appwire.ThreadCapabilities, field string) (bool, bool) {
	value := reflect.ValueOf(caps).FieldByName(field)
	if !value.IsValid() || value.Kind() != reflect.Bool {
		return false, false
	}
	return value.Bool(), true
}

func TestRemoteHubRefRoundTrip(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	refs := []string{"host:X", "host:sub-child", "host:owner", "host:turn_1"}
	for _, raw := range refs {
		remote, err := source.toRemoteRef(raw, "")
		if err != nil {
			t.Fatalf("toRemoteRef(%q): %v", raw, err)
		}
		if remote.SourceID != remoteHubNamespace {
			t.Fatalf("toRemoteRef(%q).SourceID = %q, want %q", raw, remote.SourceID, remoteHubNamespace)
		}
		back, err := source.fromRemoteRefString(remote.String())
		if err != nil {
			t.Fatalf("fromRemoteRefString(%q): %v", remote.String(), err)
		}
		if back != raw {
			t.Fatalf("round trip: got %q, want %q", back, raw)
		}
	}
}

func TestRemoteHubRefBareThreadID(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	remote, err := source.toRemoteRef("", "X")
	if err != nil {
		t.Fatalf("toRemoteRef bare thread id: %v", err)
	}
	if remote.String() != "local:X" {
		t.Fatalf("toRemoteRef(\"\", \"X\") = %q, want %q", remote.String(), "local:X")
	}
	empty, err := source.toRemoteRef("", "")
	if err != nil {
		t.Fatalf("toRemoteRef empty: %v", err)
	}
	if empty.String() != "" {
		t.Fatalf("toRemoteRef(\"\", \"\") = %q, want empty", empty.String())
	}
}

func TestRemoteHubRefForeignSourceRefused(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	_, err := source.toRemoteRef("other:X", "")
	if err == nil {
		t.Fatal("toRemoteRef accepted a foreign source")
	}
	if !strings.Contains(err.Error(), "source not found: other") {
		t.Fatalf("error = %v, want source not found: other", err)
	}
}

func TestRemoteHubNestedNonLocalRefRefused(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	_, err := source.fromRemoteRefString("other:X")
	if err == nil {
		t.Fatal("fromRemoteRefString accepted a nested non-local ref")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInternalError {
		t.Fatalf("wire code = %d, want %d", wire.Code, appwire.CodeInternalError)
	}
	if !strings.Contains(err.Error(), "only \"local\" is representable") {
		t.Fatalf("error = %v, want nested-ref refusal", err)
	}
}

func TestRemoteHubFromRemoteThreadTranslates(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	thread, err := source.fromRemoteThread(appwire.Thread{
		ID:     "t1",
		Source: "local",
		Evener: appwire.EvenerThread{
			Ref:        "local:t1",
			ParentRef:  "local:owner",
			InstanceID: "inst-1",
		},
	})
	if err != nil {
		t.Fatalf("fromRemoteThread: %v", err)
	}
	if thread.Source != "host" {
		t.Fatalf("Source = %q, want host", thread.Source)
	}
	if thread.Evener.Ref != "host:t1" {
		t.Fatalf("Evener.Ref = %q, want host:t1", thread.Evener.Ref)
	}
	if thread.Evener.ParentRef != "host:owner" {
		t.Fatalf("Evener.ParentRef = %q, want host:owner", thread.Evener.ParentRef)
	}
	if thread.Evener.InstanceID != "inst-1" {
		t.Fatalf("Evener.InstanceID = %q, want inst-1", thread.Evener.InstanceID)
	}
}

func TestRemoteHubFromRemoteThreadNestedRefRefused(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	_, err := source.fromRemoteThread(appwire.Thread{Evener: appwire.EvenerThread{Ref: "other:X"}})
	if err == nil {
		t.Fatal("fromRemoteThread accepted a nested remote ref")
	}
}

// A remote thread's diagnostics carry nested transcript refs of its own.
// Delegate refs are session refs in the remote hub's own "local:" namespace
// (agent/delegate_tree_start.go stamps encodeRef("", childSessionID)), so an
// untranslated one makes a client read the controller's local session — or fail —
// instead of the remote child. Refs in other namespaces are not source-qualified
// thread refs the controller could route, so they must survive untouched rather
// than fail the whole response.
func TestRemoteHubFromRemoteThreadTranslatesNestedDiagnosticRefs(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	thread, err := source.fromRemoteThread(appwire.Thread{
		ID: "t1",
		Evener: appwire.EvenerThread{
			Ref: "local:t1",
			Diagnostics: &appwire.EvenerDiagnostics{
				Delegates: []appwire.EvenerDelegateInfo{
					{DelegateID: "dlg_1", TranscriptRef: "local:child"},
					{DelegateID: "dlg_2", TranscriptRef: ""},
					{DelegateID: "dlg_3", TranscriptRef: "proj:project:child"},
				},
				Jobs: []appwire.EvenerJobInfo{
					{JobID: "job_1", TranscriptRef: "job:job_1"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("fromRemoteThread: %v", err)
	}
	diagnostics := thread.Evener.Diagnostics
	if got := diagnostics.Delegates[0].TranscriptRef; got != "host:child" {
		t.Fatalf("delegate transcript ref = %q, want host:child", got)
	}
	if got := diagnostics.Delegates[1].TranscriptRef; got != "" {
		t.Fatalf("empty delegate transcript ref = %q, want empty", got)
	}
	if got := diagnostics.Delegates[2].TranscriptRef; got != "proj:project:child" {
		t.Fatalf("project-scoped delegate transcript ref = %q, want it left untouched", got)
	}
	if got := diagnostics.Jobs[0].TranscriptRef; got != "job:job_1" {
		t.Fatalf("job transcript ref = %q, want it left untouched", got)
	}
}

// A remote thread's pending escalation cards name the owning session with the
// remote hub's own "local:<session>" ref: server/thread_envelope.go stamps each
// card's Ref from the thread envelope and server/appwire_escalation_test.go pins
// "local:th_1". An untranslated card leaves the browser/TUI resolving the answer
// through the controller's own local source (evener/sandbox/escalation/resolve
// routes by params.Ref through sourceForThread): on a session-id collision that
// answers the wrong machine's escalation, and otherwise fails as source-not-
// found. Translate each card's Ref through the nested-ref helper like the
// diagnostic transcript refs, leaving ThreadID (already unnamespaced) untouched.
// The helper must stay lenient: an empty ref, an already-qualified ref, a bare id
// that is not valid ref syntax, and a foreign namespace all survive byte-for-byte
// rather than failing the whole response.
func TestRemoteHubFromRemoteThreadTranslatesPendingEscalationRefs(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	thread, err := source.fromRemoteThread(appwire.Thread{
		ID: "t1",
		Evener: appwire.EvenerThread{
			Ref: "local:t1",
			PendingEscalations: []appwire.SandboxEscalationRequested{
				{ThreadID: "th_1", Ref: "local:th_1", EscalationID: "esc_1"},
				{ThreadID: "th_2", Ref: "", EscalationID: "esc_2"},
				{ThreadID: "th_3", Ref: "host:th_3", EscalationID: "esc_3"},
				{ThreadID: "th_4", Ref: "th_4", EscalationID: "esc_4"},
				{ThreadID: "th_5", Ref: "other:th_5", EscalationID: "esc_5"},
			},
		},
	})
	if err != nil {
		t.Fatalf("fromRemoteThread: %v", err)
	}
	pending := thread.Evener.PendingEscalations
	if len(pending) != 5 {
		t.Fatalf("PendingEscalations length = %d, want 5", len(pending))
	}
	if got := pending[0].Ref; got != "host:th_1" {
		t.Fatalf("pending escalation ref = %q, want host:th_1", got)
	}
	if got := pending[0].ThreadID; got != "th_1" {
		t.Fatalf("pending escalation thread id = %q, want th_1 preserved", got)
	}
	if got := pending[1].Ref; got != "" {
		t.Fatalf("empty pending escalation ref = %q, want empty", got)
	}
	if got := pending[2].Ref; got != "host:th_3" {
		t.Fatalf("already-qualified pending escalation ref = %q, want host:th_3 unchanged", got)
	}
	if got := pending[3].Ref; got != "th_4" {
		t.Fatalf("bare pending escalation id = %q, want it left untouched", got)
	}
	if got := pending[4].Ref; got != "other:th_5" {
		t.Fatalf("foreign pending escalation ref = %q, want it left untouched", got)
	}
}

func TestRemapRemoteSourceIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty stays nil", nil, nil},
		{"host only", []string{"host"}, []string{"local"}},
		{"host and other drops other", []string{"host", "other"}, []string{"local"}},
		{"other only becomes empty", []string{"other"}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := remapRemoteSourceIDs("host", tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("remapRemoteSourceIDs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
