package agent

// A finished subagent's outcome reaches the phone through delegates/list.
// This corpus records the compact domain response and a resumed run with
// identical timestamps, so clients must join reports by run generation.
// The legacy tree response remains recorded for consumers of that fixture:
// the thread/read roster drops the report (appwire.SlimDelegateForRoster), so
// the transcript's subagent row reads it from the same tree the Subagents list
// does. The row picks its line from the delegate's outcome, packet kind and
// message, and a hand-built tree carries only the fields its author thought
// of — a report the hub sends JSON-encoded, or a stop that arrives as a
// cancelled outcome with the stop message, would pass a hand-built test and
// fail on the wire.
//
// This test is the corpus for those rows. It finishes three subagents the way
// a run does (stableDelegateFinishFromRun, the journal events the controller
// appends) — one that reported, one the user stopped, one that failed — and
// reads the session's activity tree as evener/jobs/list answers it.
//
// Regenerate after an intentional change with `make fuzz-goldens`
// (wire_fixture_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

// subagentOutcomeWireFixturePath is the committed corpus the phone reads.
const subagentOutcomeWireFixturePath = "testdata/subagentwire/outcomes.json"

func TestSubagentOutcomeWireFixtures(t *testing.T) {
	t.Parallel()
	stateDir := realTempDirForTest(t)
	s := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
		withoutGitSnapshot(),
	)
	runs := []struct {
		id     string
		title  string
		inputs delegateTerminalRunInputs
	}{
		{
			id:    "dlg_reported",
			title: "Fix race in tree settle",
			inputs: delegateTerminalRunInputs{
				result:       "Fixed the race: settle now waits for the drain.\n\nThe new test covers both orders.",
				communicated: true,
			},
		},
		{
			id:     "dlg_stopped",
			title:  "Bisect the flaky test",
			inputs: delegateTerminalRunInputs{runErr: context.Canceled, stoppedByUser: true},
		},
		{
			id:     "dlg_failed",
			title:  "Update the lockfile",
			inputs: delegateTerminalRunInputs{runErr: errors.New("provider returned 500\nretry-after: 30")},
		},
	}
	for i, run := range runs {
		descriptor := stableReadonlyDescriptor(s, run.id)
		descriptor.Task = run.title
		descriptor.Description = run.title
		started := wireFixtureStart.Add(time.Duration(i) * time.Minute)
		inputs := run.inputs
		inputs.descriptor = descriptor
		inputs.startedAt = started
		inputs.latestActivityAt = started.Add(20 * time.Second)
		inputs.endedAt = started.Add(30 * time.Second)
		seedStableReadonlyFinish(t, s, run.id, descriptor, started, stableDelegateFinishFromRun(inputs), true)
	}

	delegates, err := s.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Scope: appwire.SessionActivityScopeSubtree})
	if err != nil {
		t.Fatal(err)
	}
	delegates.Context.Epoch = "fixture"
	delegateWire, err := json.Marshal(delegates)
	if err != nil {
		t.Fatal(err)
	}
	delegateWire = []byte(strings.ReplaceAll(string(delegateWire), s.ID(), "root"))

	tree, err := s.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("JobActivityTree: %v", err)
	}
	for _, run := range runs {
		stableReadonlyActivityRow(t, tree, run.id)
	}
	// A failed run's cause rides beside its reason code: the error's first
	// line, on the activity tree, the status roster and delegate/updated.
	if got := stableReadonlyActivityRow(t, tree, "dlg_failed"); got["reason"] != "run_error" || got["error"] != "provider returned 500" {
		t.Fatalf("failed row reason=%v error=%v, want reason run_error and error %q", got["reason"], got["error"], "provider returned 500")
	}
	if got := stableReadonlyActivityRow(t, tree, "dlg_reported"); got["error"] != nil {
		t.Fatalf("reported row error=%v, want none", got["error"])
	}
	var failedRow delegateSnapshot
	for _, row := range s.delegateController.Snapshot().rows {
		if row.id == "dlg_failed" {
			failedRow = row
		}
	}
	status := delegateStatusInfoFromSnapshot(wireFixtureStart, s.ID(), failedRow)
	if status.Error != "provider returned 500" {
		t.Fatalf("status error=%q, want %q", status.Error, "provider returned 500")
	}
	if updated := delegateUpdatedDataFromStatus(status); updated.Error != "provider returned 500" {
		t.Fatalf("delegate/updated error=%q, want %q", updated.Error, "provider returned 500")
	}
	// The session's id is minted per run; the corpus names it "root".
	encoded, err := json.Marshal(appwire.JobsListResponse{Data: tree})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var response json.RawMessage = []byte(strings.ReplaceAll(string(encoded), s.ID(), "root"))

	// Same timestamps deliberately demonstrate that generation, not a clock,
	// distinguishes a resumed run's report from its predecessor.
	c := s.delegateController
	nextPacket := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: json.RawMessage(`"Second run report."`)}
	c.mu.Lock()
	_, err = c.appendLocked(delegateControllerRunStartedEvent("dlg_reported", 2, delegatestore.TriggerOwnerInput, wireFixtureStart), delegateRunFinishedEvent(delegateLease{delegateID: "dlg_reported", generation: 2}, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "", wireFixtureStart.Add(30*time.Second), delegateDeliveryID("dlg_reported", 2), &nextPacket))
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := s.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Scope: appwire.SessionActivityScopeSubtree})
	if err != nil {
		t.Fatal(err)
	}
	resumed.Context.Epoch = "fixture"
	resumedWire, err := json.Marshal(resumed)
	if err != nil {
		t.Fatal(err)
	}
	resumedWire = []byte(strings.ReplaceAll(string(resumedWire), s.ID(), "root"))
	checkWireFixture(t, subagentOutcomeWireFixturePath, struct {
		Note                     string          `json:"note"`
		Response                 json.RawMessage `json:"response"`
		DelegatesResponse        json.RawMessage `json:"delegatesResponse"`
		ResumedDelegatesResponse json.RawMessage `json:"resumedDelegatesResponse"`
	}{
		Note:                     "evener/jobs/list's answer for a coordinator with three finished subagents: one that reported, one the user stopped, one whose provider failed. Each is finished through stableDelegateFinishFromRun and the controller's journal events, and read through Session.JobActivityTree; the session id is written as root.",
		Response:                 response,
		DelegatesResponse:        delegateWire,
		ResumedDelegatesResponse: resumedWire,
	}, "the mobile-native tests that read it")
}
