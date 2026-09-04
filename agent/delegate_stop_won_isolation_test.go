package agent

import (
	"context"
	"errors"
	"os"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// A construct failure that races a stop settles the delegate as stopped, and
// failCommittedStart takes its stop-won exit. Nothing else ever disposes what
// the isolation step built for the construction: the lane's clone environment
// (whose scratch the construction's own git snapshot mints) and the isolation
// worktree itself. That exit has to run the isolation cleanup like every other
// failure exit, or a stop that lands mid-construct leaves a directory, a held
// lease, and a registered lane behind.
func TestDelegateResourceCreate_StopWinningConstructFailureCleansIsolation(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s
	runtime := delegateRuntime{owner: root}
	ctx := context.Background()
	args := delegateArgs{Task: "stop mid-construct", Isolation: "worktree", DelegationAllowance: new(0)}
	selection, err := root.selectSubagentModel(ctx, args.Model, args.AgentType)
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	toolNameCeiling := root.stableDelegateEffectiveToolNameCeiling(selection, args, args.Isolation)
	descriptor, project, err := runtime.describe(ctx, args, args.Task, args.Isolation, nil, selection, toolNameCeiling)
	if err != nil {
		t.Fatalf("describe delegate: %v", err)
	}
	reservation, err := root.delegateController.ReserveCreate(rootDelegateActor(root.ID()), descriptor)
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	isolation, err := runtime.prepareIsolation(ctx, reservation, project, nil)
	if err != nil {
		t.Fatalf("prepareIsolation: %v", err)
	}
	if !isolation.ownsFreshEnv || isolation.worktreePath == "" || !laneWorktreePresent(isolation.worktreePath) {
		t.Fatalf("isolation = %+v, want an owned lane clone on a present worktree", isolation)
	}
	// The construction's git snapshot is what mints the lane clone's scratch;
	// one command on the clone stands in for it.
	lane, ok := isolation.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("isolation env = %T, want a local environment", isolation.env)
	}
	if _, err := lane.ExecCommand(ctx, "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand on the lane clone: %v", err)
	}
	scratch := lane.SessionScratchDir()
	if scratch == "" {
		t.Fatal("the lane clone minted no session scratch, so there is nothing to clean up")
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	_, cancelPlan, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.ID()), started.lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	constructErr := errors.New("construct failed under the stop")
	result := runtime.failCommittedStart(started, isolation, nil, false, constructErr, "construction_failed")

	if !errors.Is(result.Err, constructErr) {
		t.Fatalf("failed create error = %v, want the construct failure %v", result.Err, constructErr)
	}
	// The stop won: the generation settled as stopped with resumability retained,
	// where a plain construct failure would have closed it.
	aggregate := delegateAggregateSnapshot(t, root.delegateController, started.lease.delegateID)
	if !aggregate.Resumable || aggregate.CurrentRunOpen {
		t.Fatalf("stop-racing delegate = %#v, want settled with durable resumability retained", aggregate)
	}
	// The lease lives inside the scratch dir, so the directory's removal is the
	// lease's removal too.
	if _, err := os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the lane clone's scratch %s survived the stop-won construct failure: stat err = %v", scratch, err)
	}
	if laneWorktreePresent(isolation.worktreePath) {
		t.Errorf("the isolation lane %s survived the stop-won construct failure", isolation.worktreePath)
	}
}
