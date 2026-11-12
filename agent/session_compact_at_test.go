package agent

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
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

func withModelCompactionThreshold(p *provider.Profile, window, threshold int) *provider.Profile {
	res := p.Resolved()
	if window > 0 {
		res.Caps.ContextWindow = &window
	} else {
		res.Caps.ContextWindow = nil
	}
	res.Caps.MaxInputTokens = nil
	if threshold > 0 {
		res.Caps.CompactionThreshold = &threshold
	} else {
		res.Caps.CompactionThreshold = nil
	}
	return p.WithResolved(res)
}

func TestCompactAt_ModelCompactionThresholdGovernsNudgeElicitationAndPrecedence(t *testing.T) {
	t.Parallel()
	prof250 := withModelCompactionThreshold(NewOpenAIProfile("gpt-5.2"), 1_000_000, 250_000)
	s := newSession(t,
		withProfile(prof250),
		withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: t.TempDir()}),
	)
	seedNumberedSessionHistory(t, s, 12)
	elicited := 0
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		elicited++
		return "must keep", nil
	}

	// WarnThresholdFor = 0.20 (200,000 tokens on 1,000,000).
	forcePressureAbove(t, s, 0.19)
	if s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudged at 0.19 pressure (below 0.20 warn threshold)")
	}
	forcePressureAbove(t, s, 0.20)
	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("did not nudge at 0.20 pressure (200,000 tokens)")
	}

	// At 240,000 tokens (0.24): neither note elicitation nor checkpoint fires.
	forcePressureAbove(t, s, 0.24)
	s.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, s), 0)
	if elicited != 0 {
		t.Fatalf("elicited %d times at 0.24 pressure, want 0", elicited)
	}
	before, after := requestRound(t, s, 0.24)
	if hasCheckpoint(after) || !reflect.DeepEqual(before, after) {
		t.Fatal("checkpoint fired at 0.24 pressure (below 0.25 threshold)")
	}

	// At 250,000 tokens (0.25): note elicitation and Layer 1 checkpoint both fire.
	forcePressureAbove(t, s, 0.25)
	s.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, s), 0)
	if elicited != 1 {
		t.Fatalf("elicited %d times at 0.25 pressure, want 1", elicited)
	}
	_, after250 := requestRound(t, s, 0.25)
	if !hasCheckpoint(after250) {
		t.Fatal("expected checkpoint at 0.25 pressure (250,000 tokens)")
	}

	// CompactionThreshold = 920,000 above DefaultCompactAt (900,000) with CompactAtPercent == 0.
	prof920 := withModelCompactionThreshold(NewOpenAIProfile("gpt-5.2"), 1_000_000, 920_000)
	s920 := newSession(t,
		withProfile(prof920),
		withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: t.TempDir()}),
	)
	muteNoteElicitation(s920)
	seedNumberedSessionHistory(t, s920, 12)
	if metrics, _ := s920.ContextStatus(); metrics.CompactionThreshold != 920_000 {
		t.Fatalf("ContextStatus.CompactionThreshold = %d, want 920000", metrics.CompactionThreshold)
	}
	if _, after910 := requestRound(t, s920, 0.91); hasCheckpoint(after910) {
		t.Fatal("checkpoint fired at 0.91 when model CompactionThreshold is 0.92")
	}
	if _, after920 := requestRound(t, s920, 0.92); !hasCheckpoint(after920) {
		t.Fatal("expected checkpoint at 0.92 when model CompactionThreshold is 920,000")
	}

	// Explicit CompactAtPercent = 20 (200,000) is tighter than 920,000 and wins.
	s20 := newSession(t,
		withProfile(prof920),
		withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: t.TempDir(), CompactAtPercent: 20}),
	)
	muteNoteElicitation(s20)
	seedNumberedSessionHistory(t, s20, 12)
	if metrics, _ := s20.ContextStatus(); metrics.CompactionThreshold != 200_000 {
		t.Fatalf("with CompactAtPercent=20, ContextStatus.CompactionThreshold = %d, want 200000", metrics.CompactionThreshold)
	}
	if _, after20 := requestRound(t, s20, 0.21); !hasCheckpoint(after20) {
		t.Fatal("expected checkpoint at 0.21 when CompactAtPercent=20 overrides 920,000")
	}

	// Small threshold edge case: 4096 on 1,048,576 does not nudge on zero tokens.
	prof4096 := withModelCompactionThreshold(NewOpenAIProfile("gpt-5.2"), millionTokenWindow, 4096)
	s4096 := newSession(t,
		withProfile(prof4096),
		withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: t.TempDir()}),
	)
	if s4096.maybeNudgeSelfCompact(0) {
		t.Fatal("nudged on zero tokens with CompactionThreshold=4096")
	}
}

func TestCompactAt_UnknownWindowWithCompactionThresholdParity(t *testing.T) {
	unknownWithThreshold := withModelCompactionThreshold(newOpenAICompatProfile("gw", "deepseek-4.1-flash", 0), 0, 250_000)
	resolver := func(ref string) (*provider.Profile, error) {
		if ref == "gw/deepseek-4.1-flash" {
			return unknownWithThreshold, nil
		}
		return NewOpenAIProfile("gpt-5.2"), nil
	}
	s := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.2")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeAdapter{name: "gw"}),
		withConfig(SessionConfig{
			StateDir:       t.TempDir(),
			ResolveProfile: resolver,
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)
	if err := s.SetModel("gw/deepseek-4.1-flash"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	const wantDerivedWindow = 277_778
	metrics, pressure := s.ContextStatus()
	if metrics.Window != wantDerivedWindow || !metrics.Assumed || metrics.WindowSource != provider.ContextWindowSourceAssumed || metrics.CompactionThreshold != 250_000 {
		t.Fatalf("ContextStatus = %+v, want Window=%d Assumed=true WindowSource=assumed CompactionThreshold=250000", metrics, wantDerivedWindow)
	}
	checkContextStatusInvariants(t, metrics, pressure)

	meta := s.Meta()
	if meta.ContextWindow != wantDerivedWindow || meta.ContextWindowSource != provider.ContextWindowSourceAssumed {
		t.Fatalf("Meta window = %d (%q), want %d (assumed)", meta.ContextWindow, meta.ContextWindowSource, wantDerivedWindow)
	}
	rec := lastModelSwitchRecord(t, s)
	if rec.ContextWindow != wantDerivedWindow || rec.ContextWindowSource != provider.ContextWindowSourceAssumed {
		t.Fatalf("TurnModelSwitch window = %d (%q), want %d (assumed)", rec.ContextWindow, rec.ContextWindowSource, wantDerivedWindow)
	}

	allocs := testing.AllocsPerRun(20, func() { _, _ = s.ContextStatus() })
	if allocs > 2 {
		t.Fatalf("ContextStatus with CompactionThreshold allocates %v, budget 2", allocs)
	}
}

func TestCompactAt_SetModelEmitsThresholdContextAndMarkerWarning(t *testing.T) {
	t.Parallel()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"work": {
				Base:     "openai-compatible",
				Protocol: registry.ProtocolOpenAIChat,
				Surface:  registry.SurfaceOpenAI,
				APIKey:   "k",
				Transport: registry.Transport{
					BaseURL: "http://gw.invalid/v1",
				},
				Models: map[string]registry.Model{
					"model-1m-none": {
						Caps: registry.Caps{ContextWindow: new(1_000_000)},
					},
					"model-1m-250k": {
						Caps: registry.Caps{ContextWindow: new(1_000_000), CompactionThreshold: new(250_000)},
					},
					"model-0w-250k": {
						Caps: registry.Caps{CompactionThreshold: new(250_000)},
					},
				},
			},
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	unthresholded, err := provider.Resolve(r, "work/model-1m-none")
	if err != nil {
		t.Fatalf("provider.Resolve: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	client.Register(&fakeAdapter{name: "work"})

	s := newSession(t,
		withClient(client),
		withProfile(unthresholded),
		withConfig(SessionConfig{
			StateDir:       t.TempDir(),
			ResolveProfile: func(ref string) (*provider.Profile, error) { return provider.Resolve(r, ref) },
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)
	// Size history so EstimateUsage(s.history, len(s.cachedSystemPrompt)).Used == 260,000.
	sysTokens := len(s.cachedSystemPrompt) / 4
	s.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("x", (260_000-sysTokens)*4))),
	}

	if err := s.SetModel("work/model-1m-250k"); err != nil {
		t.Fatalf("SetModel to thresholded 1M: %v", err)
	}

	var switched *events.ModelChangedData
drain1:
	for {
		select {
		case ev := <-s.Events():
			if d, ok := ev.Data.(events.ModelChangedData); ok {
				switched = &d
			}
		default:
			break drain1
		}
	}
	if switched == nil {
		t.Fatal("no EventModelChanged emitted")
	}
	if switched.ContextUsed != 260_000 || switched.ContextWindow != 1_000_000 || !approxEqualFloat(switched.ContextPressure, 0.26) || switched.ContextAssumed || switched.CompactionThreshold != 250_000 {
		t.Fatalf("EventModelChanged = %+v, want Used=260000 Window=1000000 Pressure=0.26 Assumed=false CompactionThreshold=250000", *switched)
	}
	marker := lastMarkerTurn(t, s).Message.Text()
	if strings.Count(marker, "\n") < 1 || !strings.Contains(marker, "250000") {
		t.Fatalf("switch marker = %q, want warning line naming the 250000 threshold", marker)
	}

	// Switching to unknown-window model with CompactionThreshold = 250,000 omits the synthetic 277,778 figure from the marker.
	s.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("x", (260_000-sysTokens)*4))),
	}
	if err := s.SetModel("work/model-0w-250k"); err != nil {
		t.Fatalf("SetModel to unknown-window thresholded: %v", err)
	}
	markerUnknown := lastMarkerTurn(t, s).Message.Text()
	if strings.Count(markerUnknown, "\n") < 1 || !strings.Contains(markerUnknown, "250000") {
		t.Fatalf("unknown-window threshold marker = %q, want warning line naming 250000", markerUnknown)
	}
	if strings.Contains(markerUnknown, strconv.Itoa(277_778)) {
		t.Fatalf("unknown-window threshold marker = %q, must not cite synthetic window 277778", markerUnknown)
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
		{50, 0.45, 0.44},
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
		if got := s.contextMgr.Pressure(currentHistory(t, s), 0); got >= s.contextMgr.CheckpointThreshold {
			t.Errorf("percent %d: nudge pressure %.2f is not before the trigger %.2f", tc.percent, got, s.contextMgr.CheckpointThreshold)
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

// A request that has filled most of the window raises no user-facing usage
// warning: compaction and the self-compact nudge manage the window.
func TestRequestNearTheWindowRaisesNoContextBudgetWarning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	base := withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2"))
	standing := standingRequestTokens(t, dir, base, SessionConfig{StateDir: t.TempDir()}, "hi")
	profile := withContextWindow(base, standing*100/90)

	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("done") },
	}})
	sess := newSession(t, withClient(c), withProfile(profile), withDir(dir), withConfig(SessionConfig{StateDir: t.TempDir()}))
	captured := captureSessionEvents(sess)
	if _, err := sess.ProcessInput(context.Background(), "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	for _, w := range warningEvents(<-captured) {
		// The output-allocation notice shares the code; only the usage
		// warning carried the percent and token estimate.
		if w.Code == events.WarningCodeContextBudget && (w.Percent != 0 || w.ApproxTokens != 0) {
			t.Fatalf("request near the window raised a usage warning: %+v", w)
		}
	}
}
