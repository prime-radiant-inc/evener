//go:build eval

package agent

// Live end-to-end verification of the default-on forced-note (Variant B): a REAL
// OAuth session, pushed over the compaction checkpoint by a large real input,
// must have the harness elicit + pin a note WITHOUT the agent calling compact.
//
// The real model's context window (272K) is resolved live and cannot be forced
// small (resolveLiveModelProfileWithTimeout overrides WithContextWindow), so we
// instead clamp the compaction thresholds to their 0.20 floor via the test-only
// threshold scale and feed an input large enough to cross 20% of the real window.
//
// Run: go test -tags eval ./agent/ -run TestForcedNoteLive -v -timeout 8m
//
// SECURITY: uses Jesse's real OAuth creds (authorized). Skips cleanly if absent.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"

	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/ollama"
	_ "primeradiant.com/serf/llm/providers/openai"
)

func TestForcedNoteLive(t *testing.T) {
	const stateHome = "/home/jesse/.local/state"
	if _, err := os.Stat(filepath.Join(stateHome, "serf", "auth", "openai.json")); err != nil {
		t.Skipf("no OAuth record: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", stateHome)

	cfg, exists, err := providercfg.LoadFile("/home/jesse/.serf/providers.toml")
	if err != nil || !exists {
		t.Skipf("providers.toml: %v exists=%v", err, exists)
	}
	client, _, err := llm.NewFromAvailableProviders(cfg)
	if err != nil {
		t.Fatalf("NewFromAvailableProviders: %v", err)
	}
	prof, err := provider.ResolveProfileFromConfig(cfg, "openai/gpt-5.5")
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig: %v", err)
	}

	// Default-on: do NOT set DisableNoteElicitation. Clamp thresholds to their
	// 0.20 floor so a large-but-affordable input crosses the checkpoint without
	// needing to fill the full 272K window.
	var sc SessionConfig
	sc.testOnly.compactionThresholdScale = 0.1

	dir := t.TempDir()
	sess, err := NewSession(client, prof, execenv.NewLocalExecutionEnvironment(dir), sc)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Logf("live window = %d tokens; checkpoint clamped to %.2f",
		prof.ContextWindowSize(), 0.20)

	// Distinctive facts + bulk to push char/4 pressure over 20% of 272K
	// (~54K tokens ⇒ ~218K chars). The agent is NOT told to compact — the
	// harness must elicit + pin the note on its own.
	facts := "Project facts you MUST preserve verbatim: the deploy token is " +
		"DEPLOY-LIVE-9X2Q; the cache deadlock triggers when numShards exceeds 42; " +
		"the primary region is eu-central-1."
	bulk := strings.Repeat("Irrelevant background filler line for context bulk; ignore this. ", 4200)
	prompt := facts + "\n\n" + bulk + "\n\n" + facts + "\n\nAcknowledge with the single word OK."
	t.Logf("prompt size = %d chars (~%d tokens)", len(prompt), len(prompt)/4)

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	out, perr := sess.ProcessInput(ctx, prompt, nil)
	t.Logf("ProcessInput err=%v out=%.120q", perr, out)
	note := sess.PinnedNote()
	sess.Close()
	for ev := range sess.Events() {
		t.Logf("event: %T %+v", ev.Data, ev.Data)
	}
	if perr != nil {
		t.Fatalf("ProcessInput: %v", perr)
	}
	t.Logf("pinned note after a real high-pressure turn (%d chars):\n%.600s", len(note), note)
	if strings.TrimSpace(note) == "" {
		t.Fatal("no pinned note — default-on forced-note elicitation did NOT fire end-to-end")
	}
	if !strings.Contains(note, "DEPLOY-LIVE-9X2Q") {
		t.Errorf("pinned note is missing the opaque token DEPLOY-LIVE-9X2Q — the elicitor fired but did not capture it")
	}
}
