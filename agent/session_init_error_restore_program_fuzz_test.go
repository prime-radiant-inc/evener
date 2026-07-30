//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzSessionInitErrorRestoreProgram concentrates on constructor branches that
// the persistent happy-path target cannot reach: nil and Initialize guards,
// prompt/strategy failures, restore layering, and fallback validation. Every
// seed stays below the provider, process, clock, environment, and filesystem
// boundaries, so corpus replay is deterministic and offline.
func FuzzSessionInitErrorRestoreProgram(f *testing.F) {
	for mode := byte(0); mode < 12; mode++ {
		f.Add([]byte{mode, mode, mode})
	}
	for submode := byte(0); submode < 3; submode++ {
		f.Add([]byte{6, submode})
		f.Add([]byte{9, submode})
	}
	f.Add([]byte{8, 0})
	f.Add([]byte{8, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		r := &sierReader{data: data}
		mode := r.intn(12)
		client := sierClient()
		profile := NewOpenAIProfile("gpt-5.2")
		workspace := t.TempDir()
		env := &agenttest.DenyEnv{WorkDir: workspace, Seed: uint64(r.next())}
		clk := agenttest.NewFakeClock()
		cfg := sierConfig(clk)

		switch mode {
		case 0:
			sess, err := NewSession(nil, profile, env, cfg)
			sierRequireError(t, sess, err, "llm client is nil")
		case 1:
			sess, err := NewSession(client, nil, env, cfg)
			sierRequireError(t, sess, err, "profile is nil")
		case 2:
			sess, err := NewSession(client, profile, nil, cfg)
			sierRequireError(t, sess, err, "execution environment is nil")
		case 3:
			badEnv := sierFailInitEnv{ExecutionEnvironment: env}
			sess, err := NewSession(client, profile, badEnv, cfg)
			sierRequireError(t, sess, err, "env initialize")
		case 4:
			cfg.SystemPromptFile = filepath.Join(workspace, "missing-system-prompt.md")
			sess, err := NewSession(client, profile, env, cfg)
			sierRequireError(t, sess, err, "reading system prompt override")
		case 5:
			cfg.ContextStrategy = "sier-unknown"
			sess, err := NewSession(client, profile, env, cfg)
			sierRequireError(t, sess, err, "unknown context strategy")
		case 6:
			sierRestoreGuards(t, r, client, profile, env, clk)
		case 7:
			meta := sierMeta()
			badEnv := sierFailInitEnv{ExecutionEnvironment: env}
			sess, err := RestoreSessionFromMetaWithConfig(client, profile, badEnv, meta, sierRestoreConfig(workspace, clk))
			sierRequireError(t, sess, err, "env initialize")
		case 8:
			sierRestoreProjection(t, r, client, profile, env, workspace, clk)
		case 9:
			sierFallbackValidation(t, r, client, profile, env, cfg)
		case 10:
			sierRestoreWrapper(t, client, profile, env, workspace)
		case 11:
			sierPureInitHelpers(t, r)
		}
	})
}

type sierReader struct {
	data []byte
	pos  int
}

func (r *sierReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	v := r.data[r.pos]
	r.pos++
	return v
}

func (r *sierReader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next()) % n
}

type sierFailInitEnv struct{ execenv.ExecutionEnvironment }

func (sierFailInitEnv) Initialize() error { return errors.New("sier initialize fault") }

func sierClient() *llm.Client {
	c := llm.NewClient()
	c.Register(&agenttest.ScriptedAdapter{
		Provider:  "openai",
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("unused") },
	})
	return c
}

func sierTestConfig() testConfig {
	return testConfig{
		skipGitSnapshot: true,
		noSyncJobStore:  true,
		environmentInfo: func(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
			return schema.EnvironmentInfo{WorkingDir: env.WorkingDirectory(), Platform: "sier", Today: clk.Now().UTC().Format("2006-01-02")}
		},
	}
}

func sierConfig(clk clock.Clock) SessionConfig {
	return SessionConfig{
		NoProjectPrompts: true,
		MaxSubagentDepth: 1,
		clock:            clk,
		testOnly:         sierTestConfig(),
	}
}

func sierRestoreConfig(stateDir string, clk clock.Clock) RestoreSessionConfig {
	return RestoreSessionConfig{
		StateDir:                stateDir,
		clock:                   clk,
		deferRestoreSideEffects: true,
		testOnly:                sierTestConfig(),
	}
}

func sierMeta() schema.SessionMeta {
	return schema.SessionMeta{
		ID:        "01JSESSIONINITRESTORE00000001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
		Config:    (SessionConfig{NoProjectPrompts: true, MaxSubagentDepth: 1}).toSnapshot(),
	}
}

func sierRequireError(t *testing.T, sess *Session, err error, contains string) {
	t.Helper()
	if sess != nil {
		sess.Close()
		t.Fatalf("constructor returned non-nil session with error %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("error = %v, want text %q", err, contains)
	}
}

func sierRestoreGuards(t *testing.T, r *sierReader, client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, clk clock.Clock) {
	t.Helper()
	meta := sierMeta()
	restoreCfg := sierRestoreConfig(t.TempDir(), clk)
	switch r.intn(3) {
	case 0:
		sess, err := RestoreSessionFromMetaWithConfig(nil, profile, env, meta, restoreCfg)
		sierRequireError(t, sess, err, "llm client is nil")
	case 1:
		sess, err := RestoreSessionFromMetaWithConfig(client, nil, env, meta, restoreCfg)
		sierRequireError(t, sess, err, "profile is nil")
	case 2:
		sess, err := RestoreSessionFromMetaWithConfig(client, profile, nil, meta, restoreCfg)
		sierRequireError(t, sess, err, "execution environment is nil")
	}
}

func sierRestoreProjection(t *testing.T, r *sierReader, client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, stateDir string, clk clock.Clock) {
	t.Helper()
	meta := sierMeta()
	meta.CheapModel = "gpt-4.1-mini"
	meta.TurnCount = r.intn(5)
	meta.LastInputTokens = 1 + r.intn(1000)
	meta.WorkMillis = int64(r.intn(1000))
	meta.Origin = "test"
	meta.PinnedNote = "sier pinned"
	meta.Name = "sier restored"
	meta.NameSource = "prompt"
	meta.CumulativeUsage = schema.CumulativeUsage{InputTokens: 11, OutputTokens: 7, TotalTokens: 18, CacheReadTokens: int64(r.intn(2) * 3)}
	restoreCfg := sierRestoreConfig(stateDir, clk)
	restoreCfg.resumeHistory = []schema.Turn{}
	restoreCfg.ModelFallbacks = []string{"openai/gpt-4.1-mini"}
	restoreCfg.OpenAIResponsesContinuation = " auto "
	sess, err := RestoreSessionFromMetaWithConfig(client, profile, env, meta, restoreCfg)
	if err != nil {
		t.Fatalf("restore projection: %v", err)
	}
	defer sess.Close()
	if sess.id != meta.ID || sess.modelResponses != meta.TurnCount || sess.pinnedNote != meta.PinnedNote || sess.origin != meta.Origin {
		t.Fatalf("restore projection mismatch: id=%q turns=%d note=%q origin=%q", sess.id, sess.modelResponses, sess.pinnedNote, sess.origin)
	}
	if got := sess.cfg.OpenAIResponsesContinuation; got != "auto" {
		t.Fatalf("continuation = %q, want auto", got)
	}
	if len(sess.cfg.ModelFallbacks) != 1 || sess.cfg.ModelFallbacks[0] != "openai/gpt-4.1-mini" {
		t.Fatalf("fallbacks = %v", sess.cfg.ModelFallbacks)
	}
	if got := sess.profile.CheapModel(); got != "gpt-4.1-mini" {
		t.Fatalf("cheap model = %q, want gpt-4.1-mini", got)
	}
}

func sierFallbackValidation(t *testing.T, r *sierReader, client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg SessionConfig) {
	t.Helper()
	resolver := func(ref string) (*provider.Profile, error) {
		if strings.HasPrefix(ref, "anthropic/") {
			return newAnthropicProfile(strings.TrimPrefix(ref, "anthropic/")), nil
		}
		if strings.HasPrefix(ref, "fault/") {
			return nil, errors.New("sier resolver fault")
		}
		return NewOpenAIProfile(strings.TrimPrefix(ref, "openai/")), nil
	}
	cfg.ResolveProfile = resolver
	switch r.intn(3) {
	case 0:
		cfg.ModelFallbacks = []string{"openai/gpt-4.1-mini"}
		sess, err := NewSession(client, profile, env, cfg)
		if err != nil {
			t.Fatalf("same-provider fallback: %v", err)
		}
		sess.Close()
	case 1:
		cfg.ModelFallbacks = []string{"anthropic/claude-test"}
		sess, err := NewSession(client, profile, env, cfg)
		sierRequireError(t, sess, err, "cross-provider fallbacks are not supported")
	case 2:
		cfg.ModelFallbacks = []string{"fault/model"}
		sess, err := NewSession(client, profile, env, cfg)
		sierRequireError(t, sess, err, "sier resolver fault")
	}
}

func sierRestoreWrapper(t *testing.T, client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, stateDir string) {
	t.Helper()
	meta := sierMeta()
	sess, err := RestoreSessionFromMeta(client, profile, env, meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()
	if sess.id != meta.ID {
		t.Fatalf("restored id = %q, want %q", sess.id, meta.ID)
	}
}

func sierPureInitHelpers(t *testing.T, r *sierReader) {
	t.Helper()
	if got := cacheReadPtr(int64(r.intn(3))); got != nil && *got < 0 {
		t.Fatalf("cacheReadPtr returned negative value %d", *got)
	}
	eligible := modelFallbackEligible(errors.New("retryable by default"), llm.DefaultRetryPolicy())
	if eligible {
		t.Fatal("generic retryable error was fallback eligible")
	}
	if !modelFallbackEligible(context.Canceled, llm.DefaultRetryPolicy()) {
		t.Fatal("permanent cancellation was not fallback eligible")
	}
	if !strings.Contains(unknownHookEventWarning("plugin", "Typo"), "Typo") {
		t.Fatal("unknown hook warning omitted event")
	}
	if !strings.Contains(unsupportedHookEventWarning("plugin", "Stop"), "Stop") {
		t.Fatal("unsupported hook warning omitted event")
	}
	if !strings.Contains(unsupportedHandlerTypeWarning("plugin", "PreToolUse", ""), "(empty)") {
		t.Fatal("empty handler type was not made visible")
	}
	if got := reconnectRecoveryWarning("server"); got.Source != "mcp" || !strings.Contains(got.Message, "server") {
		t.Fatalf("reconnect warning = %+v", got)
	}
}
