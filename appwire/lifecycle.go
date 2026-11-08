// Package appwire's lifecycle registry declares the closed state sets and
// legal transitions of the durable things an evener session owns: the session
// itself, its turns, jobs, delegates, watches, tasks, goals, host operations
// and client-mutation projections.
//
// The registry is the one place those machines are written down. Owners keep
// their own mutexes and stores; this file says what their states mean, which
// states are terminal, and which transitions an owner may take. Clients and
// agent tools read the same declaration, so a state can no longer acquire a
// second presentation meaning on one surface and a different one on another.
//
// A machine may contain an observed state that no owner transitions into
// (SessionWatchStateUnknown is retained evidence). It is still declared,
// because a client must classify it; it simply has no edges. Other machines,
// such as turns and thread status, are projected from recorded evidence, but
// their legal edges are still declared.
package appwire

import "fmt"

// MachineKind names one declared lifecycle.
type MachineKind string

const (
	MachineSession            MachineKind = "session"
	MachineTurn               MachineKind = "turn"
	MachineThreadStatus       MachineKind = "threadStatus"
	MachineJob                MachineKind = "job"
	MachineDelegate           MachineKind = "delegate"
	MachineDelegateRun        MachineKind = "delegateRun"
	MachineDelegateOutcome    MachineKind = "delegateOutcome"
	MachineWatch              MachineKind = "watch"
	MachineTask               MachineKind = "task"
	MachineGoal               MachineKind = "goal"
	MachineHostOperation      MachineKind = "hostOperation"
	MachineMutationProjection MachineKind = "mutationProjection"
)

// LifecycleClass is the presentation-neutral family a state belongs to. One
// rule turns a class into a dot, a word, or an agent-facing next action, so
// every surface classifies the same state the same way.
type LifecycleClass string

const (
	// LifecycleActive means work the entity owns is still expected to move it.
	LifecycleActive LifecycleClass = "active"
	// LifecycleWaiting means the entity waits on a person before it can move.
	LifecycleWaiting LifecycleClass = "waiting"
	// LifecycleSettled means a run finished without failure or cancellation.
	LifecycleSettled LifecycleClass = "settled"
	// LifecycleFailed means a run finished because something broke.
	LifecycleFailed LifecycleClass = "failed"
	// LifecycleNeutral means a run was ended deliberately (stopped, cancelled,
	// interrupted) rather than finishing or breaking.
	LifecycleNeutral LifecycleClass = "neutral"
	// LifecycleEnded means the entity itself is gone and cannot move again.
	LifecycleEnded LifecycleClass = "ended"
)

// LifecycleState describes one state of one machine.
type LifecycleState struct {
	Name string `json:"name"`
	// Class is the family a surface renders this state in.
	Class LifecycleClass `json:"class"`
	// Terminal means no legal transition leaves this state.
	Terminal bool `json:"terminal"`
	// Resting means the entity is not processing in this state, whatever it
	// last reported. A failed rest (systemError) is resting even though its
	// class is failed.
	Resting bool `json:"resting,omitempty"`
	// Summarize carries the one-line meaning shown by `evener doctor
	// lifecycle` and included in the generated client catalog.
	Summary string `json:"summary,omitempty"`
}

// LifecycleEdge is one legal transition.
type LifecycleEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// LifecycleMachine is one declared lifecycle: its states, its initial state,
// and every legal edge between them.
type LifecycleMachine struct {
	Kind        MachineKind      `json:"kind"`
	Initial     string           `json:"initial,omitempty"`
	States      []LifecycleState `json:"states"`
	Transitions []LifecycleEdge  `json:"transitions"`
}

// State returns the declared state named name.
func (m LifecycleMachine) State(name string) (LifecycleState, bool) {
	for _, state := range m.States {
		if state.Name == name {
			return state, true
		}
	}
	return LifecycleState{}, false
}

// HasState reports whether name is declared by this machine.
func (m LifecycleMachine) HasState(name string) bool {
	_, ok := m.State(name)
	return ok
}

// IsTerminal reports whether name is declared terminal. An unknown state is
// not terminal: an entity holding a state this build does not know must stay
// visible rather than read as finished.
func (m LifecycleMachine) IsTerminal(name string) bool {
	state, ok := m.State(name)
	return ok && state.Terminal
}

// Class returns the declared class of name. An unknown state reads as
// LifecycleActive so a newer daemon's state never disappears as settled.
func (m LifecycleMachine) Class(name string) LifecycleClass {
	if state, ok := m.State(name); ok {
		return state.Class
	}
	return LifecycleActive
}

// CanTransition reports whether the machine declares from -> to. A self edge
// is legal only when it is declared, because some owners re-publish the same
// state (a repeated stop request) while others must not.
func (m LifecycleMachine) CanTransition(from, to string) bool {
	if !m.HasState(from) || !m.HasState(to) {
		return false
	}
	for _, edge := range m.Transitions {
		if edge.From == from && edge.To == to {
			return true
		}
	}
	return false
}

// Next returns the declared successors of name, in declaration order.
func (m LifecycleMachine) Next(name string) []string {
	var next []string
	for _, edge := range m.Transitions {
		if edge.From == name {
			next = append(next, edge.To)
		}
	}
	return next
}

// Validate checks the table's internal consistency: every edge names declared
// states, the initial state is declared, and no terminal state has an outgoing
// edge.
func (m LifecycleMachine) Validate() error {
	if m.Kind == "" {
		return fmt.Errorf("lifecycle machine has no kind")
	}
	if len(m.States) == 0 {
		return fmt.Errorf("lifecycle machine %q has no states", m.Kind)
	}
	declared := make(map[string]bool, len(m.States))
	for _, state := range m.States {
		if state.Name == "" {
			return fmt.Errorf("lifecycle machine %q has an unnamed state", m.Kind)
		}
		if declared[state.Name] {
			return fmt.Errorf("lifecycle machine %q declares %q twice", m.Kind, state.Name)
		}
		if state.Class == "" {
			return fmt.Errorf("lifecycle machine %q state %q has no class", m.Kind, state.Name)
		}
		declared[state.Name] = true
	}
	if m.Initial != "" && !declared[m.Initial] {
		return fmt.Errorf("lifecycle machine %q initial state %q is not declared", m.Kind, m.Initial)
	}
	for _, edge := range m.Transitions {
		if !declared[edge.From] {
			return fmt.Errorf("lifecycle machine %q edge from undeclared state %q", m.Kind, edge.From)
		}
		if !declared[edge.To] {
			return fmt.Errorf("lifecycle machine %q edge to undeclared state %q", m.Kind, edge.To)
		}
		if m.IsTerminal(edge.From) {
			return fmt.Errorf("lifecycle machine %q leaves terminal state %q", m.Kind, edge.From)
		}
	}
	return nil
}

// SessionState is a session's lifecycle state as its owner holds it.
type SessionState string

const (
	SessionStateIdle     SessionState = "idle"
	SessionStateActive   SessionState = "active"
	SessionStateAwaiting SessionState = "awaiting"
	SessionStateClosed   SessionState = "closed"
)

// JobStatus is the lifecycle state of one job. A job is created running and
// moves exactly once, to a terminal status.
type JobStatus string

const (
	JobStatusRunning              JobStatus = "running"
	JobStatusCompleted            JobStatus = "completed"
	JobStatusCommandExitedNonzero JobStatus = "command_exited_nonzero"
	JobStatusCommandKilled        JobStatus = "command_killed"
	JobStatusFailed               JobStatus = "failed"
	JobStatusExhausted            JobStatus = "exhausted"
	JobStatusCancelled            JobStatus = "cancelled"
	JobStatusStopped              JobStatus = "stopped"
)

// IsTerminal reports whether the status means no further runtime work is
// expected.
func (s JobStatus) IsTerminal() bool { return jobMachine.IsTerminal(string(s)) }

// Class returns the status's presentation family.
func (s JobStatus) Class() LifecycleClass { return jobMachine.Class(string(s)) }

// DelegatePhase is the durable aggregate phase of one delegate. It is the
// delegate's own machine; the wire's per-run outcome is a separate machine
// (DelegateOutcome).
type DelegatePhase string

const (
	DelegatePhaseIdle     DelegatePhase = "idle"
	DelegatePhaseRunning  DelegatePhase = "running"
	DelegatePhaseSettling DelegatePhase = "settling"
	DelegatePhaseStopping DelegatePhase = "stopping"
	DelegatePhaseClosed   DelegatePhase = "closed"
)

// DelegateOutcome is the terminal outcome of one delegate run.
type DelegateOutcome string

const (
	DelegateOutcomeCompleted DelegateOutcome = "completed"
	DelegateOutcomeFailed    DelegateOutcome = "failed"
	DelegateOutcomeExhausted DelegateOutcome = "exhausted"
	DelegateOutcomeCancelled DelegateOutcome = "cancelled"
	DelegateOutcomeStopped   DelegateOutcome = "stopped"
)

// Valid reports whether the outcome is one this build records.
func (o DelegateOutcome) Valid() bool { return delegateOutcomeMachine.HasState(string(o)) }

// Class returns the outcome's presentation family.
func (o DelegateOutcome) Class() LifecycleClass { return delegateOutcomeMachine.Class(string(o)) }

// DelegateRunStatus is a delegate run's status while it is open or after it
// settles. It is the vocabulary the in-memory subagent model reports.
type DelegateRunStatus string

const (
	DelegateRunRunning   DelegateRunStatus = "running"
	DelegateRunCompleted DelegateRunStatus = "completed"
	DelegateRunFailed    DelegateRunStatus = "failed"
	DelegateRunExhausted DelegateRunStatus = "exhausted"
	DelegateRunCancelled DelegateRunStatus = "cancelled"
	DelegateRunStopped   DelegateRunStatus = "stopped"
)

// IsTerminal reports whether the run has stopped.
func (s DelegateRunStatus) IsTerminal() bool { return delegateRunMachine.IsTerminal(string(s)) }

// GoalStatus is the lifecycle state of a session goal.
type GoalStatus string

const (
	GoalStatusActive   GoalStatus = "active"
	GoalStatusComplete GoalStatus = "complete"
	GoalStatusBlocked  GoalStatus = "blocked"
)

// Valid reports whether the status is one this build records.
func (s GoalStatus) Valid() bool { return goalMachine.HasState(string(s)) }

// IsTerminal reports whether the goal has stopped.
func (s GoalStatus) IsTerminal() bool { return goalMachine.IsTerminal(string(s)) }

// Valid reports whether the task status is one this build records.
func (s TaskStatus) Valid() bool { return taskMachine.HasState(string(s)) }

var (
	sessionMachine = LifecycleMachine{
		Kind:    MachineSession,
		Initial: string(SessionStateIdle),
		States: []LifecycleState{
			{Name: string(SessionStateIdle), Class: LifecycleSettled, Summary: "resting between turns"},
			{Name: string(SessionStateActive), Class: LifecycleActive, Summary: "running a turn"},
			{Name: string(SessionStateAwaiting), Class: LifecycleWaiting, Summary: "resting with the next move owed to the human"},
			{Name: string(SessionStateClosed), Class: LifecycleEnded, Terminal: true, Summary: "closed and unable to move again"},
		},
		Transitions: edges(
			string(SessionStateIdle), string(SessionStateActive),
			string(SessionStateIdle), string(SessionStateAwaiting),
			string(SessionStateIdle), string(SessionStateClosed),
			string(SessionStateActive), string(SessionStateIdle),
			string(SessionStateActive), string(SessionStateAwaiting),
			string(SessionStateActive), string(SessionStateClosed),
			string(SessionStateAwaiting), string(SessionStateIdle),
			string(SessionStateAwaiting), string(SessionStateActive),
			string(SessionStateAwaiting), string(SessionStateClosed),
		),
	}

	turnMachine = LifecycleMachine{
		Kind:    MachineTurn,
		Initial: TurnStatusInProgress,
		States: []LifecycleState{
			{Name: TurnStatusInProgress, Class: LifecycleActive, Summary: "turn is running"},
			{Name: TurnStatusCompleted, Class: LifecycleSettled, Terminal: true, Summary: "turn finished"},
			{Name: TurnStatusFailed, Class: LifecycleFailed, Terminal: true, Summary: "turn ended on an error"},
			{Name: TurnStatusInterrupted, Class: LifecycleNeutral, Terminal: true, Summary: "turn was interrupted"},
		},
		Transitions: edges(
			TurnStatusInProgress, TurnStatusCompleted,
			TurnStatusInProgress, TurnStatusFailed,
			TurnStatusInProgress, TurnStatusInterrupted,
		),
	}

	threadStatusMachine = LifecycleMachine{
		Kind: MachineThreadStatus,
		States: []LifecycleState{
			{Name: ThreadStatusIdle, Class: LifecycleSettled, Resting: true, Summary: "resting between turns"},
			{Name: ThreadStatusActive, Class: LifecycleActive, Summary: "running a turn"},
			{Name: ThreadStatusAwaiting, Class: LifecycleWaiting, Summary: "waiting on a question or a reply"},
			{Name: ThreadStatusWarning, Class: LifecycleWaiting, Summary: "working with a warning that needs attention"},
			{Name: ThreadStatusClosed, Class: LifecycleEnded, Terminal: true, Summary: "the session ended"},
			{Name: ThreadStatusNotLoaded, Class: LifecycleEnded, Summary: "no session is loaded for this row"},
			{Name: ThreadStatusSystemError, Class: LifecycleFailed, Resting: true, Summary: "resting on a failed turn"},
			{Name: ThreadStatusRestartRequired, Class: LifecycleWaiting, Summary: "an older daemon process must be restarted"},
		},
	}

	jobMachine = LifecycleMachine{
		Kind:    MachineJob,
		Initial: string(JobStatusRunning),
		States: []LifecycleState{
			{Name: string(JobStatusRunning), Class: LifecycleActive, Summary: "runtime work is in progress"},
			{Name: string(JobStatusCompleted), Class: LifecycleSettled, Terminal: true, Summary: "the command exited zero"},
			{Name: string(JobStatusCommandExitedNonzero), Class: LifecycleFailed, Terminal: true, Summary: "the supervised command reported failure"},
			{Name: string(JobStatusCommandKilled), Class: LifecycleFailed, Terminal: true, Summary: "the supervised command was killed by a signal"},
			{Name: string(JobStatusFailed), Class: LifecycleFailed, Terminal: true, Summary: "the job system failed"},
			{Name: string(JobStatusExhausted), Class: LifecycleFailed, Terminal: true, Summary: "the job stopped at a configured budget"},
			{Name: string(JobStatusCancelled), Class: LifecycleNeutral, Terminal: true, Summary: "the job was cancelled"},
			{Name: string(JobStatusStopped), Class: LifecycleNeutral, Terminal: true, Summary: "the job was stopped"},
		},
		Transitions: edges(
			string(JobStatusRunning), string(JobStatusCompleted),
			string(JobStatusRunning), string(JobStatusCommandExitedNonzero),
			string(JobStatusRunning), string(JobStatusCommandKilled),
			string(JobStatusRunning), string(JobStatusFailed),
			string(JobStatusRunning), string(JobStatusExhausted),
			string(JobStatusRunning), string(JobStatusCancelled),
			string(JobStatusRunning), string(JobStatusStopped),
		),
	}

	delegatePhaseMachine = LifecycleMachine{
		Kind:    MachineDelegate,
		Initial: string(DelegatePhaseIdle),
		States: []LifecycleState{
			{Name: string(DelegatePhaseIdle), Class: LifecycleSettled, Summary: "no run is open; a new run may start"},
			{Name: string(DelegatePhaseRunning), Class: LifecycleActive, Summary: "a run is in progress"},
			{Name: string(DelegatePhaseSettling), Class: LifecycleActive, Summary: "a run is preparing its terminal packet"},
			{Name: string(DelegatePhaseStopping), Class: LifecycleActive, Summary: "a stop is draining the open run"},
			{Name: string(DelegatePhaseClosed), Class: LifecycleEnded, Terminal: true, Summary: "the delegate cannot run again"},
		},
		Transitions: edges(
			string(DelegatePhaseIdle), string(DelegatePhaseRunning),
			string(DelegatePhaseIdle), string(DelegatePhaseStopping),
			string(DelegatePhaseIdle), string(DelegatePhaseClosed),
			string(DelegatePhaseRunning), string(DelegatePhaseSettling),
			string(DelegatePhaseRunning), string(DelegatePhaseStopping),
			string(DelegatePhaseRunning), string(DelegatePhaseIdle),
			string(DelegatePhaseRunning), string(DelegatePhaseClosed),
			string(DelegatePhaseSettling), string(DelegatePhaseStopping),
			string(DelegatePhaseSettling), string(DelegatePhaseIdle),
			string(DelegatePhaseSettling), string(DelegatePhaseClosed),
			string(DelegatePhaseStopping), string(DelegatePhaseStopping),
			string(DelegatePhaseStopping), string(DelegatePhaseIdle),
			string(DelegatePhaseStopping), string(DelegatePhaseClosed),
		),
	}

	delegateOutcomeMachine = LifecycleMachine{
		Kind: MachineDelegateOutcome,
		States: []LifecycleState{
			{Name: string(DelegateOutcomeCompleted), Class: LifecycleSettled, Terminal: true, Summary: "the run finished"},
			{Name: string(DelegateOutcomeFailed), Class: LifecycleFailed, Terminal: true, Summary: "the run ended on an error"},
			{Name: string(DelegateOutcomeExhausted), Class: LifecycleFailed, Terminal: true, Summary: "the run stopped at a configured budget"},
			{Name: string(DelegateOutcomeCancelled), Class: LifecycleNeutral, Terminal: true, Summary: "the run was cancelled"},
			{Name: string(DelegateOutcomeStopped), Class: LifecycleNeutral, Terminal: true, Summary: "the run was stopped"},
		},
	}

	delegateRunMachine = LifecycleMachine{
		Kind:    MachineDelegateRun,
		Initial: string(DelegateRunRunning),
		States: []LifecycleState{
			{Name: string(DelegateRunRunning), Class: LifecycleActive, Summary: "the subagent run is executing"},
			{Name: string(DelegateRunCompleted), Class: LifecycleSettled, Terminal: true, Summary: "the run finished"},
			{Name: string(DelegateRunFailed), Class: LifecycleFailed, Terminal: true, Summary: "the run ended on an error"},
			{Name: string(DelegateRunExhausted), Class: LifecycleFailed, Terminal: true, Summary: "the run stopped at a configured budget"},
			{Name: string(DelegateRunCancelled), Class: LifecycleNeutral, Terminal: true, Summary: "the run was cancelled"},
			{Name: string(DelegateRunStopped), Class: LifecycleNeutral, Terminal: true, Summary: "the run was stopped"},
		},
		Transitions: edges(
			string(DelegateRunRunning), string(DelegateRunCompleted),
			string(DelegateRunRunning), string(DelegateRunFailed),
			string(DelegateRunRunning), string(DelegateRunExhausted),
			string(DelegateRunRunning), string(DelegateRunCancelled),
			string(DelegateRunRunning), string(DelegateRunStopped),
		),
	}

	watchMachine = LifecycleMachine{
		Kind:    MachineWatch,
		Initial: string(SessionWatchStateArmed),
		States: []LifecycleState{
			{Name: string(SessionWatchStateArmed), Class: LifecycleActive, Summary: "the watch can still fire"},
			{Name: string(SessionWatchStateEnded), Class: LifecycleEnded, Terminal: true, Summary: "the watch can never fire again"},
			{Name: string(SessionWatchStateUnknown), Class: LifecycleNeutral, Summary: "retained evidence cannot establish liveness"},
		},
		Transitions: edges(
			string(SessionWatchStateArmed), string(SessionWatchStateEnded),
		),
	}

	taskMachine = LifecycleMachine{
		Kind:    MachineTask,
		Initial: string(TaskOpen),
		States: []LifecycleState{
			{Name: string(TaskOpen), Class: LifecycleSettled, Summary: "ready to start once its dependencies settle"},
			{Name: string(TaskInProgress), Class: LifecycleActive, Summary: "the task is being worked"},
			{Name: string(TaskDone), Class: LifecycleSettled, Summary: "the task is done and may be reopened"},
			{Name: string(TaskCancelled), Class: LifecycleNeutral, Summary: "the task is cancelled and may be reopened"},
		},
		// The task store accepts any of the four statuses on update, so every
		// non-self pair is legal: done and cancelled are settled, not terminal,
		// because reopening clears CompletedAt.
		Transitions: transitionsBetween(
			string(TaskOpen), string(TaskInProgress), string(TaskDone), string(TaskCancelled),
		),
	}

	goalMachine = LifecycleMachine{
		Kind:    MachineGoal,
		Initial: string(GoalStatusActive),
		States: []LifecycleState{
			{Name: string(GoalStatusActive), Class: LifecycleActive, Summary: "the goal is being pursued"},
			{Name: string(GoalStatusComplete), Class: LifecycleSettled, Terminal: true, Summary: "the goal was completed"},
			{Name: string(GoalStatusBlocked), Class: LifecycleFailed, Terminal: true, Summary: "the goal stopped without completing"},
		},
		Transitions: edges(
			string(GoalStatusActive), string(GoalStatusComplete),
			string(GoalStatusActive), string(GoalStatusBlocked),
		),
	}

	hostOperationMachine = LifecycleMachine{
		Kind:    MachineHostOperation,
		Initial: string(OperationStatePending),
		States: []LifecycleState{
			{Name: string(OperationStatePending), Class: LifecycleActive, Summary: "accepted and waiting for its worker"},
			{Name: string(OperationStateRunning), Class: LifecycleActive, Summary: "the operation is running"},
			{Name: string(OperationStateComplete), Class: LifecycleSettled, Terminal: true, Summary: "the operation completed"},
			{Name: string(OperationStateFailed), Class: LifecycleFailed, Terminal: true, Summary: "the operation failed"},
			{Name: string(OperationStateInterrupted), Class: LifecycleNeutral, Terminal: true, Summary: "a controller shutdown interrupted the operation"},
			{Name: string(OperationStateOrphanUnverified), Class: LifecycleNeutral, Summary: "historical record whose boundary was never verified"},
		},
		Transitions: edges(
			string(OperationStatePending), string(OperationStatePending),
			string(OperationStatePending), string(OperationStateRunning),
			string(OperationStatePending), string(OperationStateComplete),
			string(OperationStatePending), string(OperationStateFailed),
			string(OperationStatePending), string(OperationStateInterrupted),
			string(OperationStatePending), string(OperationStateOrphanUnverified),
			string(OperationStateRunning), string(OperationStateRunning),
			string(OperationStateRunning), string(OperationStateComplete),
			string(OperationStateRunning), string(OperationStateFailed),
			string(OperationStateRunning), string(OperationStateInterrupted),
			string(OperationStateRunning), string(OperationStateOrphanUnverified),
			string(OperationStateOrphanUnverified), string(OperationStateOrphanUnverified),
			string(OperationStateOrphanUnverified), string(OperationStateInterrupted),
		),
	}

	mutationProjectionMachine = LifecycleMachine{
		Kind:    MachineMutationProjection,
		Initial: string(MutationProjectionPending),
		States: []LifecycleState{
			{Name: string(MutationProjectionPending), Class: LifecycleActive, Summary: "accepted, not yet in the transcript projection"},
			{Name: string(MutationProjectionReflected), Class: LifecycleSettled, Summary: "visible in the transcript projection"},
			{Name: string(MutationProjectionRemoved), Class: LifecycleEnded, Terminal: true, Summary: "removed from the transcript projection"},
		},
		Transitions: edges(
			string(MutationProjectionPending), string(MutationProjectionReflected),
			string(MutationProjectionPending), string(MutationProjectionRemoved),
			string(MutationProjectionReflected), string(MutationProjectionRemoved),
		),
	}
)

// LifecycleMachines returns every declared machine in a stable order. The
// doctor command and the generated client catalog both read this list, so a
// new machine reaches every surface from one declaration.
func LifecycleMachines() []LifecycleMachine {
	declared := []LifecycleMachine{
		sessionMachine,
		turnMachine,
		threadStatusMachine,
		jobMachine,
		delegatePhaseMachine,
		delegateRunMachine,
		delegateOutcomeMachine,
		watchMachine,
		taskMachine,
		goalMachine,
		hostOperationMachine,
		mutationProjectionMachine,
	}
	// Each call owns its slices so a caller (or a JSON encoder) cannot mutate
	// the declaration and change another caller's view.
	out := make([]LifecycleMachine, len(declared))
	for i, machine := range declared {
		out[i] = LifecycleMachine{
			Kind:        machine.Kind,
			Initial:     machine.Initial,
			States:      append([]LifecycleState(nil), machine.States...),
			Transitions: append([]LifecycleEdge(nil), machine.Transitions...),
		}
	}
	return out
}

// LifecycleMachineFor returns the declared machine for kind.
func LifecycleMachineFor(kind MachineKind) (LifecycleMachine, bool) {
	for _, machine := range LifecycleMachines() {
		if machine.Kind == kind {
			return machine, true
		}
	}
	return LifecycleMachine{}, false
}

// SessionLifecycle returns the session machine.
func SessionLifecycle() LifecycleMachine { return sessionMachine }

// TurnLifecycle returns the turn machine.
func TurnLifecycle() LifecycleMachine { return turnMachine }

// ThreadStatusLifecycle returns the client-facing thread status machine.
func ThreadStatusLifecycle() LifecycleMachine { return threadStatusMachine }

// JobLifecycle returns the job machine.
func JobLifecycle() LifecycleMachine { return jobMachine }

// DelegateLifecycle returns the delegate phase machine.
func DelegateLifecycle() LifecycleMachine { return delegatePhaseMachine }

// DelegateOutcomeLifecycle returns the delegate outcome machine.
func DelegateOutcomeLifecycle() LifecycleMachine { return delegateOutcomeMachine }

// DelegateRunLifecycle returns the delegate run status machine.
func DelegateRunLifecycle() LifecycleMachine { return delegateRunMachine }

// WatchLifecycle returns the watch machine.
func WatchLifecycle() LifecycleMachine { return watchMachine }

// TaskLifecycle returns the task machine.
func TaskLifecycle() LifecycleMachine { return taskMachine }

// GoalLifecycle returns the goal machine.
func GoalLifecycle() LifecycleMachine { return goalMachine }

// HostOperationLifecycle returns the host operation machine.
func HostOperationLifecycle() LifecycleMachine { return hostOperationMachine }

// Valid reports whether the state is one this build records.
func (s OperationState) Valid() bool { return hostOperationMachine.HasState(string(s)) }

// Terminal reports whether the state ends an operation.
func (s OperationState) Terminal() bool { return hostOperationMachine.IsTerminal(string(s)) }

// InFlight reports whether a controller restart owns this state: a record
// still pending or running when the controller came up.
func (s OperationState) InFlight() bool {
	return s == OperationStatePending || s == OperationStateRunning
}

// MutationProjectionLifecycle returns the client-mutation projection machine.
func MutationProjectionLifecycle() LifecycleMachine { return mutationProjectionMachine }

// AllJobStatuses returns every declared job status in table order.
func AllJobStatuses() []string { return jobMachine.stateNames() }

// AllSessionStates returns every declared session state in table order.
func AllSessionStates() []string { return sessionMachine.stateNames() }

// AllDelegatePhases returns every declared delegate phase in table order.
func AllDelegatePhases() []string { return delegatePhaseMachine.stateNames() }

// AllDelegateRunStatuses returns every declared delegate run status in table
// order.
func AllDelegateRunStatuses() []string { return delegateRunMachine.stateNames() }

// AllDelegateOutcomes returns every declared delegate outcome in table order.
func AllDelegateOutcomes() []string { return delegateOutcomeMachine.stateNames() }

// AllGoalStatuses returns every declared goal status in table order.
func AllGoalStatuses() []string { return goalMachine.stateNames() }

// AllOperationStates returns every declared host operation state in table
// order.
func AllOperationStates() []string { return hostOperationMachine.stateNames() }

// WatchState maps a watch's liveness onto its declared state. Retained
// evidence that cannot establish liveness is SessionWatchStateUnknown and is
// the caller's choice, not this mapping's.
func WatchState(active bool) SessionWatchState {
	if active {
		return SessionWatchStateArmed
	}
	return SessionWatchStateEnded
}

// JobOutcome returns the activity rollup family for a terminal job status:
// "success", "failure", or "neutral". A running job has none.
func JobOutcome(status JobStatus) string {
	return rollupForClass(status.Class(), status.IsTerminal())
}

// DelegateOutcomeRollup returns the activity rollup family for a delegate
// outcome: "success", "failure", or "neutral". An unknown outcome has none.
func DelegateOutcomeRollup(outcome DelegateOutcome) string {
	if !outcome.Valid() {
		return ""
	}
	return rollupForClass(outcome.Class(), true)
}

// DelegateRunTerminal reports whether a delegate's latest run has settled: it
// has an outcome and no run is open. A resumed delegate can carry the previous
// run's outcome while the new run is open, so both facts are required.
func DelegateRunTerminal(hasOutcome, currentRunOpen bool) bool {
	return hasOutcome && !currentRunOpen
}

func rollupForClass(class LifecycleClass, terminal bool) string {
	if !terminal {
		return ""
	}
	switch class {
	case LifecycleSettled:
		return "success"
	case LifecycleFailed:
		return "failure"
	case LifecycleNeutral:
		return "neutral"
	}
	return ""
}

func (m LifecycleMachine) stateNames() []string {
	names := make([]string, len(m.States))
	for i, state := range m.States {
		names[i] = state.Name
	}
	return names
}

func edges(pairs ...string) []LifecycleEdge {
	if len(pairs)%2 != 0 {
		panic("appwire: lifecycle edges need from/to pairs")
	}
	out := make([]LifecycleEdge, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, LifecycleEdge{From: pairs[i], To: pairs[i+1]})
	}
	return out
}

func transitionsBetween(names ...string) []LifecycleEdge {
	out := make([]LifecycleEdge, 0, len(names)*(len(names)-1))
	for _, from := range names {
		for _, to := range names {
			if from != to {
				out = append(out, LifecycleEdge{From: from, To: to})
			}
		}
	}
	return out
}
