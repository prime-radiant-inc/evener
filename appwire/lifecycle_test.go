package appwire

import (
	"reflect"
	"testing"
)

func TestLifecycleMachinesValidate(t *testing.T) {
	seen := make(map[MachineKind]bool)
	for _, machine := range LifecycleMachines() {
		if err := machine.Validate(); err != nil {
			t.Errorf("%T.Validate(): %v", machine, err)
		}
		if seen[machine.Kind] {
			t.Errorf("machine kind %q declared twice", machine.Kind)
		}
		seen[machine.Kind] = true
	}
	for _, kind := range []MachineKind{
		MachineSession, MachineTurn, MachineThreadStatus, MachineJob,
		MachineDelegate, MachineDelegateRun, MachineDelegateOutcome, MachineWatch, MachineTask,
		MachineGoal, MachineHostOperation, MachineMutationProjection,
	} {
		if !seen[kind] {
			t.Errorf("machine kind %q is not registered", kind)
		}
	}
}

func TestLifecycleMachineFor(t *testing.T) {
	for _, machine := range LifecycleMachines() {
		got, ok := LifecycleMachineFor(machine.Kind)
		if !ok {
			t.Fatalf("LifecycleMachineFor(%q) not found", machine.Kind)
		}
		if got.Kind != machine.Kind {
			t.Errorf("LifecycleMachineFor(%q).Kind = %q", machine.Kind, got.Kind)
		}
	}
	if _, ok := LifecycleMachineFor("bogus"); ok {
		t.Error("LifecycleMachineFor(bogus) found a machine")
	}
}

// TestJobStatusTerminal pins the terminal set the job store has always used:
// every status but running ends the job.
func TestJobStatusTerminal(t *testing.T) {
	wantTerminal := map[JobStatus]bool{
		JobStatusRunning:              false,
		JobStatusCompleted:            true,
		JobStatusCommandExitedNonzero: true,
		JobStatusCommandKilled:        true,
		JobStatusFailed:               true,
		JobStatusExhausted:            true,
		JobStatusCancelled:            true,
		JobStatusStopped:              true,
	}
	for status, want := range wantTerminal {
		if got := status.IsTerminal(); got != want {
			t.Errorf("%q.IsTerminal() = %v, want %v", status, got, want)
		}
	}
	if len(wantTerminal) != len(AllJobStatuses()) {
		t.Errorf("job machine has %d statuses, test pins %d", len(AllJobStatuses()), len(wantTerminal))
	}
	if JobStatus("bogus").IsTerminal() {
		t.Error("unknown job status read as terminal")
	}
}

// TestJobOutcome pins the activity rollup mapping used by the job tree and the
// client's status helpers.
func TestJobOutcome(t *testing.T) {
	want := map[JobStatus]string{
		JobStatusRunning:              "",
		JobStatusCompleted:            "success",
		JobStatusCommandExitedNonzero: "failure",
		JobStatusCommandKilled:        "failure",
		JobStatusFailed:               "failure",
		JobStatusExhausted:            "failure",
		JobStatusCancelled:            "neutral",
		JobStatusStopped:              "neutral",
	}
	for status, wantOutcome := range want {
		if got := JobOutcome(status); got != wantOutcome {
			t.Errorf("JobOutcome(%q) = %q, want %q", status, got, wantOutcome)
		}
	}
	if got := JobOutcome(JobStatus("bogus")); got != "" {
		t.Errorf("JobOutcome(bogus) = %q, want empty", got)
	}
}

func TestDelegateOutcomeRollup(t *testing.T) {
	want := map[DelegateOutcome]string{
		DelegateOutcomeCompleted: "success",
		DelegateOutcomeFailed:    "failure",
		DelegateOutcomeExhausted: "failure",
		DelegateOutcomeCancelled: "neutral",
		DelegateOutcomeStopped:   "neutral",
	}
	for outcome, wantRollup := range want {
		if got := DelegateOutcomeRollup(outcome); got != wantRollup {
			t.Errorf("DelegateOutcomeRollup(%q) = %q, want %q", outcome, got, wantRollup)
		}
	}
	if got := DelegateOutcomeRollup(DelegateOutcome("bogus")); got != "" {
		t.Errorf("DelegateOutcomeRollup(bogus) = %q, want empty", got)
	}
}

func TestDelegateRunTerminal(t *testing.T) {
	cases := []struct {
		hasOutcome bool
		runOpen    bool
		want       bool
	}{
		{false, false, false},
		{false, true, false},
		{true, true, false},
		{true, false, true},
	}
	for _, tc := range cases {
		if got := DelegateRunTerminal(tc.hasOutcome, tc.runOpen); got != tc.want {
			t.Errorf("DelegateRunTerminal(%v, %v) = %v, want %v", tc.hasOutcome, tc.runOpen, got, tc.want)
		}
	}
}

// TestDelegatePhaseTransitions pins the edges the durable fold accepts.
func TestDelegatePhaseTransitions(t *testing.T) {
	legal := map[[2]DelegatePhase]bool{
		{DelegatePhaseIdle, DelegatePhaseRunning}:      true,
		{DelegatePhaseIdle, DelegatePhaseStopping}:     true,
		{DelegatePhaseIdle, DelegatePhaseClosed}:       true,
		{DelegatePhaseRunning, DelegatePhaseSettling}:  true,
		{DelegatePhaseRunning, DelegatePhaseStopping}:  true,
		{DelegatePhaseRunning, DelegatePhaseIdle}:      true,
		{DelegatePhaseRunning, DelegatePhaseClosed}:    true,
		{DelegatePhaseSettling, DelegatePhaseStopping}: true,
		{DelegatePhaseSettling, DelegatePhaseIdle}:     true,
		{DelegatePhaseSettling, DelegatePhaseClosed}:   true,
		{DelegatePhaseStopping, DelegatePhaseStopping}: true,
		{DelegatePhaseStopping, DelegatePhaseIdle}:     true,
		{DelegatePhaseStopping, DelegatePhaseClosed}:   true,
	}
	machine := DelegateLifecycle()
	for _, from := range []DelegatePhase{
		DelegatePhaseIdle, DelegatePhaseRunning, DelegatePhaseSettling,
		DelegatePhaseStopping, DelegatePhaseClosed,
	} {
		for _, to := range []DelegatePhase{
			DelegatePhaseIdle, DelegatePhaseRunning, DelegatePhaseSettling,
			DelegatePhaseStopping, DelegatePhaseClosed,
		} {
			want := legal[[2]DelegatePhase{from, to}]
			if got := machine.CanTransition(string(from), string(to)); got != want {
				t.Errorf("delegate transition %q -> %q = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestSessionLifecycleEdges(t *testing.T) {
	machine := SessionLifecycle()
	if !machine.CanTransition(string(SessionStateIdle), string(SessionStateIdle)) {
		t.Error("idle session cannot re-publish idle")
	}
	if !machine.CanTransition(string(SessionStateAwaiting), string(SessionStateAwaiting)) {
		t.Error("awaiting session cannot re-publish awaiting")
	}
	if !machine.IsTerminal(string(SessionStateClosed)) {
		t.Error("closed session is not terminal")
	}
	for _, state := range []SessionState{
		SessionStateIdle, SessionStateActive, SessionStateAwaiting,
	} {
		if !machine.CanTransition(string(state), string(SessionStateClosed)) {
			t.Errorf("%q cannot close", state)
		}
	}
	if machine.CanTransition(string(SessionStateClosed), string(SessionStateActive)) {
		t.Error("closed session can become active again")
	}
}

func TestTurnLifecycleTerminal(t *testing.T) {
	for _, status := range []string{TurnStatusCompleted, TurnStatusFailed, TurnStatusInterrupted} {
		if !IsTerminalTurnStatus(status) {
			t.Errorf("%q is not terminal", status)
		}
	}
	if IsTerminalTurnStatus(TurnStatusInProgress) {
		t.Error("in-progress turn read as terminal")
	}
	if !IsActiveTurnStatus(TurnStatusInProgress) {
		t.Error("in-progress turn read as not active")
	}
	if IsActiveItemStatus("bogus") {
		t.Error("unknown item status read as active")
	}
	if IsTerminalItemStatus("bogus") {
		t.Error("unknown item status read as terminal")
	}
}

// TestThreadStatusPredicates pins the resting and active sets the wire
// predicates have always used.
func TestThreadStatusPredicates(t *testing.T) {
	wantResting := map[string]bool{
		ThreadStatusIdle:            true,
		ThreadStatusSystemError:     true,
		ThreadStatusActive:          false,
		ThreadStatusAwaiting:        false,
		ThreadStatusWarning:         false,
		ThreadStatusClosed:          false,
		ThreadStatusNotLoaded:       false,
		ThreadStatusRestartRequired: false,
		"bogus":                     false,
	}
	for status, want := range wantResting {
		if got := IsRestingThreadStatus(status); got != want {
			t.Errorf("IsRestingThreadStatus(%q) = %v, want %v", status, got, want)
		}
	}
	if !IsActiveThreadStatus(ThreadStatusActive) {
		t.Error("active thread status read as not active")
	}
	for status := range wantResting {
		if status != ThreadStatusActive && IsActiveThreadStatus(status) {
			t.Errorf("IsActiveThreadStatus(%q) = true", status)
		}
	}
}

func TestTaskStatusLifecycle(t *testing.T) {
	machine := TaskLifecycle()
	if machine.IsTerminal(string(TaskDone)) || machine.IsTerminal(string(TaskCancelled)) {
		t.Error("done/cancelled tasks must stay reopenable")
	}
	statuses := []string{string(TaskOpen), string(TaskInProgress), string(TaskDone), string(TaskCancelled)}
	for _, from := range statuses {
		for _, to := range statuses {
			want := from != to
			if got := machine.CanTransition(from, to); got != want {
				t.Errorf("task transition %q -> %q = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestWatchStatusLifecycle(t *testing.T) {
	machine := WatchLifecycle()
	if got := WatchState(true); got != SessionWatchStateArmed {
		t.Errorf("WatchState(true) = %q, want armed", got)
	}
	if got := WatchState(false); got != SessionWatchStateEnded {
		t.Errorf("WatchState(false) = %q, want ended", got)
	}
	if !machine.CanTransition(string(SessionWatchStateArmed), string(SessionWatchStateEnded)) {
		t.Error("armed watch cannot end")
	}
	if machine.CanTransition(string(SessionWatchStateEnded), string(SessionWatchStateArmed)) {
		t.Error("ended watch can re-arm")
	}
	if len(machine.Next(string(SessionWatchStateUnknown))) != 0 {
		t.Error("observed unknown watch state has transitions")
	}
}

func TestGoalStatusValid(t *testing.T) {
	for _, status := range []GoalStatus{GoalStatusActive, GoalStatusComplete, GoalStatusBlocked} {
		if !status.Valid() {
			t.Errorf("%q is not valid", status)
		}
	}
	if GoalStatus("bogus").Valid() {
		t.Error("bogus goal status accepted")
	}
	if !GoalStatusComplete.IsTerminal() || !GoalStatusBlocked.IsTerminal() {
		t.Error("terminal goal status read as active")
	}
	if GoalStatusActive.IsTerminal() {
		t.Error("active goal read as terminal")
	}
}

func TestHostOperationLifecycle(t *testing.T) {
	machine := HostOperationLifecycle()
	if !machine.IsTerminal(string(OperationStateComplete)) {
		t.Error("complete operation is not terminal")
	}
	if !machine.CanTransition(string(OperationStateOrphanUnverified), string(OperationStateInterrupted)) {
		t.Error("orphan-unverified cannot resolve to interrupted")
	}
	if machine.CanTransition(string(OperationStateOrphanUnverified), string(OperationStateComplete)) {
		t.Error("orphan-unverified can resolve to complete")
	}
	if machine.CanTransition(string(OperationStateComplete), string(OperationStateRunning)) {
		t.Error("terminal operation can move again")
	}
}

func TestLifecycleStateNamesAreDeclared(t *testing.T) {
	for _, machine := range LifecycleMachines() {
		names := machine.stateNames()
		if len(names) == 0 {
			t.Errorf("%q has no states", machine.Kind)
		}
		for _, name := range names {
			if name == "" {
				t.Errorf("%q has an empty state name", machine.Kind)
			}
		}
	}
}

func TestLifecycleMachinesStableOrder(t *testing.T) {
	first := LifecycleMachines()
	second := LifecycleMachines()
	if !reflect.DeepEqual(first, second) {
		t.Error("LifecycleMachines is not stable between calls")
	}
}
