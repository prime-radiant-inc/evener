package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// maskedRemoteThreadCapabilities is the set a remote thread may advertise for the
// full daemon claim: the remote's own flags intersected with the actions this
// source forwards. Shutdown is the one action remoteForwardedThreadCapabilities
// names today; every other staged action stays masked.
var maskedRemoteThreadCapabilities = appwire.ThreadCapabilities{Shutdown: true}

// A remote hub reports the capability set of its OWN session daemon. Those flags
// describe what the remote daemon can do, not what the controller can forward, so
// the mask must drop every action this source cannot carry. Shutdown is the one
// action remoteForwardedThreadCapabilities names today (the controller rpc
// thread/shutdown forwards through RemoteHubSource.ShutdownThread), so it is the
// one flag that survives; every other staged action stays masked.
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
	want := maskedRemoteThreadCapabilities
	if got := maskRemoteThreadCapabilities(allRemoteThreadCapabilities()); got != want {
		t.Fatalf("masked capabilities = %+v, want %+v", got, want)
	}
	if thread.Evener.Capabilities != want {
		t.Fatalf("remote thread advertised %+v, want %+v", thread.Evener.Capabilities, want)
	}
	// The mask is a filter over the remote's own claim, not a replacement for it:
	// nothing outside the capability set may change.
	if thread.Source != "host" {
		t.Fatalf("masked thread source = %q, want host", thread.Source)
	}
	if thread.Evener.Ref != "host:t1" {
		t.Fatalf("masked thread ref = %q, want host:t1", thread.Evener.Ref)
	}
	if thread.Evener.InstanceID != "inst-1" {
		t.Fatalf("masked thread instance = %q, want inst-1", thread.Evener.InstanceID)
	}
}

// Every capability a remote thread is allowed to advertise must have a working
// method behind it. The methods below are the 05c/05d work: component 05c
// implements the turn mutations and lifecycle verbs in remote_hub_mutations.go,
// and a capability is re-enabled only once remoteForwardedThreadCapabilities
// names it. Shutdown is named today; every other staged action stays masked until
// 05d's host capability probe answers what the host supports. Enumerating the
// promise here means a field cannot be flipped on while its method still fails
// closed, and a future capability field cannot be added without deciding which
// method answers for it.
func TestRemoteHubCapabilitiesMatchForwardedMethods(t *testing.T) {
	ctx := context.Background()
	// A connector that always fails: every method below must reach the forward
	// path (never the staged not-implemented one) without a live remote hub.
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return nil, errors.New("no remote client in this test")
	})
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
			// An implemented method must never report the staged not-implemented
			// error: the capability assertions below would then advertise an action
			// nothing answers for.
			if err == nil || strings.Contains(err.Error(), "not implemented yet") {
				t.Errorf("%s = %v, want the implemented method's own error, not the staged not-implemented one", name, err)
			}
		}
		advertised, ok := capabilityFieldValue(masked, tc.field)
		if !ok {
			t.Errorf("capability %s is not a field of appwire.ThreadCapabilities", tc.field)
			continue
		}
		// Only the actions remoteForwardedThreadCapabilities names may be
		// advertised. The expectation is read from that set rather than restated as
		// a literal here, so enabling another action cannot leave this test agreeing
		// with a stale copy of it.
		wantAdvertised, ok := capabilityFieldValue(maskedRemoteThreadCapabilities, tc.field)
		if !ok {
			t.Errorf("capability %s is not a field of the shared masked set", tc.field)
			continue
		}
		if advertised != wantAdvertised {
			t.Errorf("capability %s advertised = %v, want %v", tc.field, advertised, wantAdvertised)
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
		{"empty restricts to local", nil, []string{"local"}},
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

// TestRemoteHubListThreadsDefaultRestrictsToLocalSource pins the default
// (unfiltered) thread/list forward: this source must ask the remote hub for its
// own "local" source only. A remote hub's reflected response can advertise
// threads from ITS OWN nested remote sources, whose refs live in another host's
// namespace and are not representable in the controller namespace;
// fromRemoteThread rejects such a response outright, so an unfiltered forward
// would lose every thread from this host, not merely the nested ones. The
// scripted remote models that: it only answers when the forwarded filter names
// "local", and otherwise returns the nested thread that the controller refuses.
func TestRemoteHubListThreadsDefaultRestrictsToLocalSource(t *testing.T) {
	localThread := appwire.Thread{ID: "S", Source: "local", Evener: appwire.EvenerThread{Ref: "local:S"}}
	nestedThread := appwire.Thread{ID: "N", Source: "nested", Evener: appwire.EvenerThread{Ref: "nested:N"}}

	source, calls := newScriptedRemote(t, "host", func(method string, params json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadList {
			t.Errorf("unexpected method %q", method)
			return scriptedReply{result: map[string]any{}}
		}
		var forwarded struct {
			SourceIDs []string `json:"sourceIds"`
		}
		if err := json.Unmarshal(params, &forwarded); err != nil {
			t.Errorf("decode forwarded thread/list params %s: %v", params, err)
		}
		if !reflect.DeepEqual(forwarded.SourceIDs, []string{"local"}) {
			// An unfiltered request reaches the remote's nested remote sources,
			// and the nested ref then fails the controller's whole translation.
			return scriptedReply{result: appwire.ThreadListResponse{Data: []appwire.Thread{localThread, nestedThread}}}
		}
		return scriptedReply{result: appwire.ThreadListResponse{Data: []appwire.Thread{localThread}}}
	})

	resp, err := source.ListThreads(t.Context(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Evener.Ref != "host:S" || resp.Data[0].Source != "host" {
		t.Fatalf("threads = %+v, want the single translated local thread", resp.Data)
	}
	params := lastMethodCall(t, calls(), appwire.MethodThreadList)
	var forwarded struct {
		SourceIDs []string `json:"sourceIds"`
	}
	if err := json.Unmarshal(params, &forwarded); err != nil {
		t.Fatalf("decode forwarded params %s: %v", params, err)
	}
	if !reflect.DeepEqual(forwarded.SourceIDs, []string{"local"}) {
		t.Fatalf("forwarded sourceIds = %v, want [local]", forwarded.SourceIDs)
	}
}

// TestRemoteHubListThreadsExcludingFilterReturnsEmpty pins that a non-empty
// SourceIDs filter which does not name this source yields no threads and never
// reaches the remote hub. remapRemoteSourceIDs alone would map such a filter to
// an empty slice, and ThreadListParams.SourceIDs is `omitempty` on the wire, so
// the empty slice would be omitted and ask the remote for ALL of its sources —
// the opposite of what the caller requested. The hub's thread/list fan-out
// (sourceAllowedForList) is the only thing that currently stops that, so the
// source method must honor its own contract rather than rely on its caller.
func TestRemoteHubListThreadsExcludingFilterReturnsEmpty(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadList {
			return scriptedReply{result: appwire.ThreadListResponse{Data: []appwire.Thread{{
				ID: "S", Source: "local", Evener: appwire.EvenerThread{Ref: "local:S"},
			}}}}
		}
		return scriptedReply{result: map[string]any{}}
	})

	resp, err := source.ListThreads(t.Context(), appwire.ThreadListParams{SourceIDs: []string{"other"}})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("Data = %+v, want no threads for a filter that excludes this source", resp.Data)
	}
	for _, call := range calls() {
		if call.method == appwire.MethodThreadList {
			t.Fatalf("a filter excluding this source was forwarded to the remote hub: %+v", calls())
		}
	}
}

// TestRemoteHubListThreadsKeepsValidRowsWhenRowUnrepresentable pins that a
// thread/list response carrying one row from a nested remote hub — a ref this
// source cannot represent in the controller namespace — does not discard the
// valid local rows translated alongside it. The nested row is skipped; every
// representable row is still returned.
func TestRemoteHubListThreadsKeepsValidRowsWhenRowUnrepresentable(t *testing.T) {
	localThread := appwire.Thread{ID: "S", Source: "local", Evener: appwire.EvenerThread{Ref: "local:S"}}
	nestedThread := appwire.Thread{ID: "N", Source: "nested", Evener: appwire.EvenerThread{Ref: "nested:N"}}

	source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadList {
			return scriptedReply{result: map[string]any{}}
		}
		return scriptedReply{result: appwire.ThreadListResponse{Data: []appwire.Thread{nestedThread, localThread}}}
	})

	resp, err := source.ListThreads(t.Context(), appwire.ThreadListParams{SourceIDs: []string{"host"}})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("Data = %+v, want only the representable local row", resp.Data)
	}
	if resp.Data[0].ID != "S" || resp.Data[0].Source != "host" || resp.Data[0].Evener.Ref != "host:S" {
		t.Fatalf("row = %+v, want the translated local thread", resp.Data[0])
	}
}

// TestRemoteHubJobsListPreservesUnrecognizedPayloads pins the pass-through half
// of the jobs/list activity-ref translation through the real stream client: a
// Data payload that is not a recognized activity tree reaches the controller as
// the value it arrived as, with no nested ref rewritten.
//
// Spec 05 requires the translator to RECOGNIZE a tree before walking it —
// `revision` a non-negative integer and `root` an object carrying `sessionId`
// and `ref` as strings — and names these cases as pass-through: an empty object,
// an object carrying `root` without its required fields, a payload whose required
// fields carry another type, and an unrelated object. The cases carrying a
// tree-shaped `root.ref` are the discriminating ones: a walk that follows the
// container names alone rewrites exactly those refs, so the controller would
// receive an address the remote hub never minted for a payload it does not
// understand.
//
// Each payload is served as raw JSON (JobsListResponse.Data is `any`, so the
// client decodes it into generic maps) and compared against a decode of the same
// bytes that never went through the source; deep equality on that value is the
// byte-for-byte pin, and assertNoRewrittenRefs makes a rewrite that slipped past
// it explicit instead of a whole-payload diff.
func TestRemoteHubJobsListPreservesUnrecognizedPayloads(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"empty object", `{}`},
		{"root without required fields", `{"root":{}}`},
		{"root without revision", `{"root":{"sessionId":"S","ref":"local:S"}}`},
		{"root missing its own ref", `{"revision":3,"root":{"sessionId":"S"}}`},
		{"tree-shaped root without revision", `{"root":{"sessionId":"S","ref":"local:S","entries":[{"kind":"delegate","delegate":{"childRef":"local:S"}}]}}`},
		{"revision is a string", `{"revision":"3","root":{"sessionId":"S","ref":"local:S"}}`},
		{"revision is an object", `{"revision":{"value":3},"root":{"sessionId":"S","ref":"local:S"}}`},
		{"revision is negative", `{"revision":-1,"root":{"sessionId":"S","ref":"local:S"}}`},
		{"revision is fractional", `{"revision":1.5,"root":{"sessionId":"S","ref":"local:S"}}`},
		{"sessionId is not a string", `{"revision":3,"root":{"sessionId":3,"ref":"local:S"}}`},
		{"ref is not a string", `{"revision":3,"root":{"sessionId":"S","ref":["local:S"]}}`},
		{"root is not an object", `{"revision":3,"root":"local:S"}`},
		{"unrelated object", `{"unrelated":{"ref":"local:main","transcriptRef":"local:main","childRef":"local:main"}}`},
		{"declared containers outside root", `{"entries":[{"delegate":{"childRef":"local:main"}}]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var want any
			if err := json.Unmarshal([]byte(tc.payload), &want); err != nil {
				t.Fatalf("decode the expected payload %s: %v", tc.payload, err)
			}
			source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
				if method != appwire.MethodEvenerJobsList {
					return scriptedReply{result: map[string]any{}}
				}
				return scriptedReply{result: appwire.JobsListResponse{Data: json.RawMessage(tc.payload)}}
			})

			resp, err := source.ListJobs(t.Context(), appwire.JobsListParams{Ref: testControllerRef})
			if err != nil {
				t.Fatalf("ListJobs: %v", err)
			}
			if !reflect.DeepEqual(resp.Data, want) {
				t.Fatalf("Data = %#v, want the unrecognized payload preserved as %#v", resp.Data, want)
			}
			assertNoRewrittenRefs(t, resp.Data)
		})
	}
}

// TestRemoteHubJobsListPreservesUndecodableTreePayloads pins the second half of
// the recognition boundary: a payload can satisfy activityTreeRecognized's
// discriminator test — `revision` a non-negative integer, `root` an object
// carrying `sessionId` and `ref` as strings — and STILL fail to decode as a
// complete appwire.JobActivityTree. Spec 05 names exactly this case: "every
// payload that fails it — or that passes it but still fails to decode as a tree
// — is preserved as the `any` value it arrived as, never rewritten."
//
// The declared field types are what make these payloads malformed: entries is
// []JobActivityEntry, so a JSON object there is a type error (the case a
// reviewer named as `entries: {}`); a declared nested node carries its own
// required string fields; a declared container is a struct, not a scalar; and
// revision is a uint64, so a non-negative integral float64 too large for it is
// recognized but not decodable. Each payload carries a tree-shaped `root.ref`
// because that is the rewrite a walk that trusted the discriminator gate alone
// would perform: the controller would then receive an address the remote hub
// never minted for a payload it does not understand.
//
// As in TestRemoteHubJobsListPreservesUnrecognizedPayloads, each payload is
// served as raw JSON through the real stream client and compared against an
// independent decode of the same bytes; deep equality on that value is the
// byte-for-byte pin, and assertNoRewrittenRefs names a rewrite explicitly.
func TestRemoteHubJobsListPreservesUndecodableTreePayloads(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"entries is an object", `{"revision":1,"root":{"sessionId":"S","ref":"local:th_1","entries":{}}}`},
		{"entry node carries a non-string kind", `{"revision":1,"root":{"sessionId":"S","ref":"local:th_1","entries":[{"kind":5,"job":{"jobId":"job_a","ownerRef":"local:th_1","transcriptRef":"local:th_1"}}]}}`},
		{"counts is not an object", `{"revision":1,"root":{"sessionId":"S","ref":"local:th_1","counts":"x"}}`},
		{"branch is not an object", `{"revision":1,"root":{"sessionId":"S","ref":"local:th_1","branch":"x"}}`},
		{"revision overflows uint64", `{"revision":1e300,"root":{"sessionId":"S","ref":"local:th_1","entries":[]}}`},
		{"delegate child is not an object", `{"revision":1,"root":{"sessionId":"S","ref":"local:th_1","entries":[{"kind":"delegate","delegate":{"delegateId":"dlg_1","childRef":"local:th_1","child":"local:th_1"}}]}}`},
		{"delegate turn carries a non-string job id", `{"revision":1,"root":{"sessionId":"S","ref":"local:th_1","entries":[{"kind":"delegate","delegate":{"delegateId":"dlg_1","childRef":"local:th_1","turns":[{"jobId":7,"ownerRef":"local:th_1"}]}}]}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var want any
			if err := json.Unmarshal([]byte(tc.payload), &want); err != nil {
				t.Fatalf("decode the expected payload %s: %v", tc.payload, err)
			}
			// The case is only interesting if the discriminator gate accepts it, so
			// the payload must actually be recognized as a tree. That is asserted
			// rather than assumed: a payload that failed recognition would pass
			// through for the wrong reason.
			object, ok := want.(map[string]any)
			if !ok || !activityTreeRecognized(object) {
				t.Fatalf("payload %s is not recognized as a tree, so it does not exercise the decode gate", tc.payload)
			}
			source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
				if method != appwire.MethodEvenerJobsList {
					return scriptedReply{result: map[string]any{}}
				}
				return scriptedReply{result: appwire.JobsListResponse{Data: json.RawMessage(tc.payload)}}
			})

			resp, err := source.ListJobs(t.Context(), appwire.JobsListParams{Ref: testControllerRef})
			if err != nil {
				t.Fatalf("ListJobs: %v", err)
			}
			if !reflect.DeepEqual(resp.Data, want) {
				t.Fatalf("Data = %#v, want the undecodable payload preserved as %#v", resp.Data, want)
			}
			assertNoRewrittenRefs(t, resp.Data)
		})
	}
}

// assertNoRewrittenRefs walks a decoded JSON value and fails on any string that
// names the controller namespace. It is a failure-mode aid for the pass-through
// cases, whose authoritative pin is the deep-equality assertion against an
// independent decode of the same bytes.
func assertNoRewrittenRefs(t *testing.T, value any) {
	t.Helper()
	var walk func(path string, node any)
	walk = func(path string, node any) {
		switch typed := node.(type) {
		case string:
			if strings.HasPrefix(typed, "host:") {
				t.Errorf("%s = %q, want it left in the remote namespace", path, typed)
			}
		case map[string]any:
			for key, child := range typed {
				walk(path+"."+key, child)
			}
		case []any:
			for index, child := range typed {
				walk(fmt.Sprintf("%s[%d]", path, index), child)
			}
		}
	}
	walk("data", value)
}

// TestRemoteHubJobsListTranslatesRefsOfRecognizedTrees pins the other half of
// the recognition boundary: a payload that DOES satisfy it — `revision` a
// non-negative integer, `root` an object carrying `sessionId` and `ref` as
// strings — is walked, and every ref field the JobActivity* nodes declare is
// rewritten into the controller namespace. Recognition must be permissive about
// everything the wire types do not require: `revision: 0` is a non-negative
// integer, the required strings may be empty (neither JobActivityTree.Root or
// .Revision nor JobActivitySession.SessionID or .Ref carries omitempty), and a
// forward-compatible field this controller does not know must not make the
// payload unrecognizable.
func TestRemoteHubJobsListTranslatesRefsOfRecognizedTrees(t *testing.T) {
	t.Run("every declared ref field", func(t *testing.T) {
		payload := `{
			"revision": 0,
			"forwardCompatible": {"ref": "local:future", "transcriptRef": "local:future"},
			"root": {
				"sessionId": "root",
				"ref": "local:root",
				"label": "root session",
				"futureField": "kept",
				"entries": [
					{"kind": "shell", "job": {"jobId": "job_a", "ownerRef": "local:root", "transcriptRef": "job:job_a"}},
					{"kind": "delegate", "delegate": {
						"delegateId": "dlg_1",
						"childRef": "local:child",
						"turns": [{"jobId": "turn_1", "ownerRef": "local:root", "transcriptRef": "local:child"}],
						"child": {"sessionId": "child", "ref": "local:child", "entries": [
							{"kind": "shell", "job": {"jobId": "job_b", "ownerRef": "local:child", "transcriptRef": "job:job_b"}}
						]}
					}}
				]
			}
		}`
		tree := listJobsData(t, payload)

		root := activityMap(t, tree["root"], "root")
		if root["ref"] != "host:root" {
			t.Errorf("root.ref = %v, want %q", root["ref"], "host:root")
		}
		if root["sessionId"] != "root" {
			t.Errorf("root.sessionId = %v, want the bare id %q (ids are not refs)", root["sessionId"], "root")
		}
		if root["futureField"] != "kept" {
			t.Errorf("root.futureField = %v, want the forward-compatible field preserved", root["futureField"])
		}
		entries, ok := root["entries"].([]any)
		if !ok || len(entries) != 2 {
			t.Fatalf("root.entries = %#v, want two entries", root["entries"])
		}
		shellJob := activityMap(t, activityMap(t, entries[0], "entry 0")["job"], "job_a")
		if shellJob["ownerRef"] != "host:root" {
			t.Errorf("shell job ownerRef = %v, want %q", shellJob["ownerRef"], "host:root")
		}
		if shellJob["transcriptRef"] != "job:job_a" {
			t.Errorf("shell job transcriptRef = %v, want the opaque %q", shellJob["transcriptRef"], "job:job_a")
		}
		delegate := activityMap(t, activityMap(t, entries[1], "entry 1")["delegate"], "delegate")
		if delegate["childRef"] != "host:child" {
			t.Errorf("delegate childRef = %v, want %q", delegate["childRef"], "host:child")
		}
		turns, ok := delegate["turns"].([]any)
		if !ok || len(turns) != 1 {
			t.Fatalf("delegate.turns = %#v, want one turn", delegate["turns"])
		}
		turn := activityMap(t, turns[0], "turn 0")
		if turn["ownerRef"] != "host:root" {
			t.Errorf("turn ownerRef = %v, want %q", turn["ownerRef"], "host:root")
		}
		if turn["transcriptRef"] != "host:child" {
			t.Errorf("turn transcriptRef = %v, want %q", turn["transcriptRef"], "host:child")
		}
		child := activityMap(t, delegate["child"], "child session")
		if child["ref"] != "host:child" {
			t.Errorf("child.ref = %v, want %q", child["ref"], "host:child")
		}
		childEntries, ok := child["entries"].([]any)
		if !ok || len(childEntries) != 1 {
			t.Fatalf("child.entries = %#v, want one entry", child["entries"])
		}
		childJob := activityMap(t, activityMap(t, childEntries[0], "child entry")["job"], "job_b")
		if childJob["ownerRef"] != "host:child" {
			t.Errorf("child job ownerRef = %v, want %q", childJob["ownerRef"], "host:child")
		}
		if childJob["transcriptRef"] != "job:job_b" {
			t.Errorf("child job transcriptRef = %v, want the opaque %q", childJob["transcriptRef"], "job:job_b")
		}
		// An undeclared top-level key is not walked: its contents are data, not
		// addresses this source knows how to re-point.
		forward := activityMap(t, tree["forwardCompatible"], "forwardCompatible")
		if forward["ref"] != "local:future" || forward["transcriptRef"] != "local:future" {
			t.Errorf("undeclared top-level key was rewritten: %#v", forward)
		}
	})

	t.Run("empty required strings", func(t *testing.T) {
		payload := `{"revision":0,"root":{"sessionId":"","ref":"","entries":[{"kind":"shell","job":{"jobId":"job_a","ownerRef":"local:S","transcriptRef":"local:S"}}]}}`
		tree := listJobsData(t, payload)

		root := activityMap(t, tree["root"], "root")
		entries, ok := root["entries"].([]any)
		if !ok || len(entries) != 1 {
			t.Fatalf("root.entries = %#v, want one entry", root["entries"])
		}
		job := activityMap(t, activityMap(t, entries[0], "entry 0")["job"], "job_a")
		if job["ownerRef"] != "host:S" || job["transcriptRef"] != "host:S" {
			t.Errorf("job refs = %#v, want both translated to host:S: an empty required string still recognizes a tree", job)
		}
	})
}

// listJobsData serves one raw jobs/list payload through the real stream client
// and returns the generic value the source handed back.
func listJobsData(t *testing.T, payload string) map[string]any {
	t.Helper()
	source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodEvenerJobsList {
			return scriptedReply{result: map[string]any{}}
		}
		return scriptedReply{result: appwire.JobsListResponse{Data: json.RawMessage(payload)}}
	})
	resp, err := source.ListJobs(t.Context(), appwire.JobsListParams{Ref: testControllerRef})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	return activityMap(t, resp.Data, "tree")
}

// TestRemoteHubJobsListPreservesUnaddressableLegacyTranscriptRefs complements
// TestRemoteHubJobsListTranslatesLegacyFlatArrayRefs, which pins a
// session-valued transcriptRef being translated and the opaque "job:<id>" being
// preserved. The remaining value classes a retired flat array can carry — a bare
// id from an older daemon, a ref in a namespace this controller cannot address,
// and an empty value — must reach the controller exactly as they arrived rather
// than failing the list or being re-pointed at this source.
func TestRemoteHubJobsListPreservesUnaddressableLegacyTranscriptRefs(t *testing.T) {
	legacy := `[
		{"jobId": "turn_1", "jobType": "delegate", "ownerSessionId": "S", "transcriptRef": "child"},
		{"jobId": "turn_2", "jobType": "delegate", "ownerSessionId": "S", "transcriptRef": "proj:project:thread"},
		{"jobId": "turn_3", "jobType": "delegate", "ownerSessionId": "S", "transcriptRef": "other:thread"},
		{"jobId": "turn_4", "jobType": "delegate", "ownerSessionId": "S", "transcriptRef": ""}
	]`
	source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodEvenerJobsList {
			return scriptedReply{result: map[string]any{}}
		}
		return scriptedReply{result: appwire.JobsListResponse{Data: json.RawMessage(legacy)}}
	})

	resp, err := source.ListJobs(t.Context(), appwire.JobsListParams{Ref: testControllerRef})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	jobs, ok := resp.Data.([]any)
	if !ok || len(jobs) != 4 {
		t.Fatalf("Data = %#v, want the legacy flat array of four jobs", resp.Data)
	}
	want := []struct{ name, ref string }{
		{"bare id", "child"},
		{"foreign namespace", "proj:project:thread"},
		{"nested hub namespace", "other:thread"},
		{"empty ref", ""},
	}
	for index, tc := range want {
		job := activityMap(t, jobs[index], tc.name)
		if job["transcriptRef"] != tc.ref {
			t.Errorf("%s transcriptRef = %v, want it preserved as %q", tc.name, job["transcriptRef"], tc.ref)
		}
	}
	assertNoRewrittenRefs(t, resp.Data)
}
