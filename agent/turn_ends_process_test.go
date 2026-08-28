package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestTurnEndsProcessReachesChildSessions: children are built from the
// parent's own toSnapshot (delegate_runtime describe, subagents' frozen
// descriptor), so the flag has to ride the snapshot to reach them. Its json
// tag alone is inert — SessionConfig persists through ConfigSnapshot, not
// directly. A delegate of a one-shot run dies with the same process, so it
// needs the same answer.
func TestTurnEndsProcessReachesChildSessions(t *testing.T) {
	parent := SessionConfig{TurnEndsProcess: true}
	child := configFromSnapshot(parent.toSnapshot().Clone())
	if !child.TurnEndsProcess {
		t.Error("TurnEndsProcess did not survive the parent->child snapshot: a delegate of a one-shot run would not know its process dies with the turn")
	}
}

// TestRestoreTakesTurnEndsProcessFromTheRestoringProcess: whether the process
// outlives the turn belongs to whoever is restoring the session, not to the
// session on disk. The same session id resumed by one-shot `evener run` dies
// with its turn; resumed by the daemon it does not. So restore REPLACES the
// persisted value rather than inheriting it — otherwise a run-created session
// resumed under serve would treat background jobs as dying with the turn.
func TestRestoreTakesTurnEndsProcessFromTheRestoringProcess(t *testing.T) {
	for _, tt := range []struct {
		name      string
		persisted bool
		restorer  bool
	}{
		{name: "serve resuming a one-shot session", persisted: true, restorer: false},
		{name: "one-shot resuming a serve session", persisted: false, restorer: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := llm.NewClient()
			client.Register(&fakeAdapter{name: "openai"})
			meta := schema.SessionMeta{
				ID:        "01KTURNENDSPROCESS000000000",
				ProfileID: "openai",
				Model:     "gpt-5.2",
				Config:    schema.ConfigSnapshot{TurnEndsProcess: tt.persisted, NoProjectPrompts: true},
			}
			sess, err := RestoreSessionFromMetaWithConfig(
				client,
				NewOpenAIProfile("gpt-5.2"),
				execenv.NewLocalExecutionEnvironment(t.TempDir()),
				meta,
				RestoreSessionConfig{StateDir: t.TempDir(), TurnEndsProcess: tt.restorer},
			)
			if err != nil {
				t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
			}
			t.Cleanup(sess.Close)
			if got := sess.cfg.TurnEndsProcess; got != tt.restorer {
				t.Fatalf("restored TurnEndsProcess = %v, want the restoring process's %v (persisted was %v)", got, tt.restorer, tt.persisted)
			}
		})
	}
}

// TestFrozenDescriptorTakesTurnEndsProcessFromTheLiveParent pins the third
// path, which the drain-guidance attempt missed: a stable delegate restarted
// from its frozen descriptor. The descriptor's snapshot answers "what was this
// delegate asked to be"; process lifetime belongs to the process now hosting
// it. A descriptor frozen during a one-shot run and restarted under serve must
// not carry true into the daemon, nor false the other way.
func TestFrozenDescriptorTakesTurnEndsProcessFromTheLiveParent(t *testing.T) {
	for _, tt := range []struct {
		name   string
		frozen bool
		parent bool
	}{
		{name: "one-shot descriptor restarted under serve", frozen: true, parent: false},
		{name: "serve descriptor restarted under one-shot", frozen: false, parent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			frozenConfig := SessionConfig{TurnEndsProcess: tt.frozen, NoProjectPrompts: true}.toSnapshot()
			parentCfg := SessionConfig{TurnEndsProcess: tt.parent}
			got := subagentConfigFromFrozenDescriptor(frozenConfig, parentCfg)
			if got.TurnEndsProcess != tt.parent {
				t.Fatalf("frozen-descriptor TurnEndsProcess = %v, want the live parent's %v (frozen was %v)", got.TurnEndsProcess, tt.parent, tt.frozen)
			}
			if !got.NoProjectPrompts {
				t.Fatal("frozen-descriptor merge dropped a descriptor-scoped field; the helper must still start from the snapshot")
			}
		})
	}
}

func TestFrozenDescriptorTakesLifetimeContextFromLiveParent(t *testing.T) {
	owner, cancelOwner := context.WithCancel(context.Background())
	got := subagentConfigFromFrozenDescriptor(
		SessionConfig{NoProjectPrompts: true}.toSnapshot(),
		SessionConfig{LifetimeContext: owner},
	)
	if got.LifetimeContext == nil {
		t.Fatal("frozen delegate dropped the live parent's lifetime context")
	}
	cancelOwner()
	select {
	case <-got.LifetimeContext.Done():
	default:
		t.Fatal("frozen delegate lifetime outlived the live parent")
	}
}
