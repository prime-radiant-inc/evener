package agent

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

const millionTokenWindow = 1 << 20

// millionWindowProfile is gpt-5.2 with a 1,048,576-token window and no input
// cap, so a request at 0.9 of the window is still dispatched.
func millionWindowProfile() *provider.Profile {
	base := NewOpenAIProfile("gpt-5.2")
	res := base.Resolved()
	window := millionTokenWindow
	res.Caps.ContextWindow = &window
	res.Caps.MaxInputTokens = nil
	return base.WithResolved(res)
}

// compactAtSession is a session on a 1,048,576-token window with the given
// compact_at_percent setting and a history long enough to fold.
func compactAtSession(t *testing.T, percent int) *Session {
	t.Helper()
	s := newSession(t,
		withProfile(millionWindowProfile()),
		withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: t.TempDir(), CompactAtPercent: percent}),
		withoutGitSnapshot(),
	)
	muteNoteElicitation(s)
	seedNumberedSessionHistory(t, s, 12)
	return s
}

// requestRound runs one request preparation at the given measured usage and
// reports the history it sent and whether a fold published a checkpoint.
func requestRound(t *testing.T, s *Session, usage float64) (before, after []schema.Turn) {
	t.Helper()
	forcePressureAbove(t, s, usage)
	before = currentHistory(t, s)
	var rt events.RoundTimings
	if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 1, &rt); err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	return before, currentHistory(t, s)
}

func hasCheckpoint(history []schema.Turn) bool {
	for _, turn := range history {
		if turn.Kind == schema.TurnCheckpoint {
			return true
		}
	}
	return false
}

func TestCompactAt_DefaultTriggersAboveNinetyPercent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		usage float64
		want  bool
	}{{0.65, false}, {0.85, false}, {0.91, true}} {
		s := compactAtSession(t, 0)
		before, after := requestRound(t, s, tc.usage)
		if got := hasCheckpoint(after); got != tc.want {
			t.Errorf("usage %.2f: checkpoint published = %v, want %v", tc.usage, got, tc.want)
		}
		if !tc.want && !reflect.DeepEqual(before, after) {
			t.Errorf("usage %.2f: history before the fold point changed below the trigger", tc.usage)
		}
	}
}

func TestCompactAt_ConfiguredPercentMovesTheTrigger(t *testing.T) {
	t.Parallel()
	s := compactAtSession(t, 70)
	_, after := requestRound(t, s, 0.75)
	if !hasCheckpoint(after) {
		t.Fatal("0.75 usage with compact_at_percent 70 should publish a checkpoint")
	}
}

func TestCompactAt_NudgeStaysBelowTheTrigger(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		percent    int
		nudgeAt    float64
		silentJust float64
	}{
		{0, 0.75, 0.74},
		{70, 0.65, 0.64},
	} {
		s := compactAtSession(t, tc.percent)
		forcePressureAbove(t, s, tc.silentJust)
		if s.maybeNudgeSelfCompact(0) {
			t.Errorf("percent %d: nudged at %.2f", tc.percent, tc.silentJust)
		}
		forcePressureAbove(t, s, tc.nudgeAt)
		if !s.maybeNudgeSelfCompact(0) {
			t.Errorf("percent %d: no nudge at %.2f", tc.percent, tc.nudgeAt)
		}
	}
}

func TestCompactAt_RejectsOutOfRangePercent(t *testing.T) {
	t.Parallel()
	for _, bad := range []int{-1, 1, 19, 101, 1000} {
		if err := ValidateCompactAtPercent(bad); err == nil {
			t.Errorf("percent %d accepted", bad)
		}
	}
	for _, good := range []int{0, 20, 75, 90, 100} {
		if err := ValidateCompactAtPercent(good); err != nil {
			t.Errorf("percent %d rejected: %v", good, err)
		}
	}
}

func TestCompactAt_NewSessionRejectsBadPercent(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	good, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{CompactAtPercent: 60})
	if err != nil {
		t.Fatalf("NewSession rejected compact_at_percent 60: %v", err)
	}
	good.Close()
	if _, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{CompactAtPercent: 5}); err == nil {
		t.Fatal("NewSession accepted compact_at_percent 5")
	}
}

func TestCompactAt_RoundTripsThroughSnapshot(t *testing.T) {
	t.Parallel()
	cfg := SessionConfig{CompactAtPercent: 80}
	if got := configFromSnapshot(cfg.toSnapshot()).CompactAtPercent; got != 80 {
		t.Fatalf("snapshot round trip = %d, want 80", got)
	}
}

func TestCompactAt_ResumeKeepsPersistedValueUnlessOverridden(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	persisted := SessionConfig{CompactAtPercent: 80}.toSnapshot()
	for _, tc := range []struct {
		override int
		want     float64
	}{{0, 0.80}, {60, 0.60}} {
		meta := schema.SessionMeta{ID: "compact-at-resume", ProfileID: "openai", Model: "gpt-5.2", Config: persisted}
		restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta,
			RestoreSessionConfig{CompactAtPercent: tc.override, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}})
		if err != nil {
			t.Fatal(err)
		}
		if got := restored.contextMgr.CheckpointThreshold; !approxEqualFloat(got, tc.want) {
			t.Errorf("override %d: trigger = %v, want %v", tc.override, got, tc.want)
		}
		restored.Close()
	}
}

func approxEqualFloat(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }
