package hub

import (
	"context"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
)

// rowReadCountingSource counts the thread-row reads the skill-input gate makes.
// It embeds the Source interface so any call the gate makes other than
// ReadThread panics loudly rather than silently passing.
type rowReadCountingSource struct {
	appsource.Source
	reads      *atomic.Int32
	supported  bool
	lastParams appwire.ThreadReadParams
}

func (s *rowReadCountingSource) ReadThread(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	s.reads.Add(1)
	s.lastParams = params
	return appwire.ThreadReadResponse{
		Thread: appwire.Thread{Evener: appwire.EvenerThread{
			Capabilities: appwire.ThreadCapabilities{SkillInput: s.supported, CommandInput: s.supported},
		}},
	}, nil
}

// TestSkillInputGateReadsTheRowOnlyForSkillBearingInput pins round-7 S8's
// gate-coverage fact: the hub-side capability check that turn/start has
// (hubRelay.startTurn reads the row in prepareRelay and validates against it,
// app_relay.go) does not exist on the queue and steer paths. Their handlers
// (sessionOperations.Steer and sessionOperations.Queue, session_input.go) gate
// input through ensureSkillInputSupported alone, whose first line returns nil
// for input with no skill or command item (appwire_validation.go) — so
// ORDINARY-TEXT queue/steer never reads the target's row. Start is refused
// mid-turn, so the append window — the fence's primary scenario — is answerable
// only through queue and steer: precisely the modes whose relay never learns
// the row's capabilities. An AskFence bit checked in startTurn's prepare
// covers none of the answer paths; the fence's older-daemon gate needs a
// prepare step all three modes route through (see
// specs/manager-tools/fence-spike.md).
//
// This characterization passes today and is the flip target: post-change, a
// fence-bearing queue/steer relay must read the row (the common prepare) even
// for ordinary text, and the assertion on reads becomes 1.
func TestSkillInputGateReadsTheRowOnlyForSkillBearingInput(t *testing.T) {
	t.Parallel()
	reads := &atomic.Int32{}
	src := &rowReadCountingSource{reads: reads, supported: true}
	ctx := context.Background()

	// Ordinary text — the shape every queue/steer relay of an answer takes.
	if err := ensureSkillInputSupported(ctx, src, "local:t1", "t1", []appwire.InputItem{{Type: "text", Text: "delayed answer"}}); err != nil {
		t.Fatalf("ordinary-text gate: %v", err)
	}
	if got := reads.Load(); got != 0 {
		t.Fatalf("ordinary-text input read the row %d times — the gate must return before any row read for text-only input", got)
	}

	// Skill-bearing input does read the row and honors the capability.
	if err := ensureSkillInputSupported(ctx, src, "local:t1", "t1", []appwire.InputItem{{Type: "skill", Name: "deploy"}}); err != nil {
		t.Fatalf("skill-bearing gate on a supporting daemon: %v", err)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("skill-bearing input read the row %d times, want 1", got)
	}
	if src.lastParams.IncludeTurns {
		t.Fatal("the gate's row read requested turns — it is a capability verdict, not a content read")
	}

	// And it fails closed on a daemon that does not advertise the capability.
	unsupported := &rowReadCountingSource{reads: reads, supported: false}
	if err := ensureSkillInputSupported(ctx, unsupported, "local:t1", "t1", []appwire.InputItem{{Type: "skill", Name: "deploy"}}); err == nil {
		t.Fatal("skill-bearing input on a daemon without the capability was admitted")
	}
}
