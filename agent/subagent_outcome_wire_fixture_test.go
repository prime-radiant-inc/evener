package agent

// A finished subagent's outcome reaches the phone through evener/jobs/list:
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
			inputs: delegateTerminalRunInputs{runErr: errors.New("provider returned 500")},
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

	tree, err := s.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("JobActivityTree: %v", err)
	}
	for _, run := range runs {
		stableReadonlyActivityRow(t, tree, run.id)
	}
	// The session's id is minted per run; the corpus names it "root".
	encoded, err := json.Marshal(appwire.JobsListResponse{Data: tree})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var response json.RawMessage = []byte(strings.ReplaceAll(string(encoded), s.ID(), "root"))

	checkWireFixture(t, subagentOutcomeWireFixturePath, struct {
		Note     string          `json:"note"`
		Response json.RawMessage `json:"response"`
	}{
		Note:     "evener/jobs/list's answer for a coordinator with three finished subagents: one that reported, one the user stopped, one whose provider failed. Each is finished through stableDelegateFinishFromRun and the controller's journal events, and read through Session.JobActivityTree; the session id is written as root.",
		Response: response,
	}, "the mobile-native tests that read it")
}
