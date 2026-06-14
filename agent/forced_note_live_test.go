//go:build eval

package agent

// Live end-to-end verification of the default-on forced-note handoff: a REAL OAuth
// session, driven across two turns so an early fact-bearing turn is actually FOLDED
// by a real compaction. Without any compact call by the agent, the harness must
// elicit a note over the about-to-be-folded prefix and hand it back into the fresh
// post-compaction context as a "note to yourself from before compaction" message.
//
// This proves the wiring fires through the real ProcessInput loop AND that a real
// fold happens. The precise claim "the elicitor captures a token that lives only in
// a tool result" is hard-gated deterministically by
// contextmgr.TestElicitNoteCapturesToolResult — this test is the full-loop smoke.
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

	"primeradiant.com/serf/agent/events"
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

	// The real window (272K) can't be forced small, so clamp thresholds to their
	// 0.20 floor and feed a large early turn.
	var sc SessionConfig
	sc.testOnly.compactionThresholdScale = 0.1

	dir := t.TempDir()
	sess, err := NewSession(client, prof, execenv.NewLocalExecutionEnvironment(dir), sc)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Preserve only the single most-recent turn so the fact-bearing first turn ages
	// into the about-to-be-folded prefix by turn 2 (and is therefore in scope for
	// the elicitor, and actually folded by the compaction).
	sess.contextMgr.PreserveRecentTurns = 1

	// A non-secret opaque identifier — a credential-framed value triggers the
	// model's secret-redaction behavior (a real, separate limit of the mechanism).
	const token = "ARTIFACT-LIVE-9X2Q"
	facts := "Project facts you MUST preserve verbatim: the build artifact id is " + token +
		"; the cache deadlock triggers when numShards exceeds 42; the primary region is eu-central-1."
	// Bulk first, facts LAST: the foldable prefix is tail-truncated into the elicit
	// prompt, so the must-keep facts must sit at the end of the turn to survive it.
	// Size it to clear the 0.20 floor against the live window with margin.
	bulkReps := prof.ContextWindowSize() / 50 // ~0.3 pressure worth of ~62-char lines
	bulk := strings.Repeat("Irrelevant background filler line for context bulk; ignore this. ", bulkReps)
	turn1 := bulk + "\n\n" + facts + "\n\nAcknowledge with the single word OK."
	t.Logf("turn-1 size = %d chars (~%d tokens), window = %d", len(turn1), len(turn1)/4, prof.ContextWindowSize())

	// Collect compaction events as ground truth that a fold actually happened.
	var sawCompaction bool
	done := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			if cc, ok := ev.Data.(events.ContextCompactionData); ok {
				sawCompaction = true
				t.Logf("compaction: layer=%s turns %d→%d tokens %d→%d",
					cc.Layer, cc.TurnsBefore, cc.TurnsAfter, cc.EstTokensBefore, cc.EstTokensAfter)
			}
		}
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()

	if _, err := sess.ProcessInput(ctx, turn1, nil); err != nil {
		t.Fatalf("ProcessInput turn 1: %v", err)
	}
	// Turn 2: a trivial turn whose presence pushes the big turn-1 input out of the
	// single preserved slot, so the pre-request compaction folds it.
	out2, err := sess.ProcessInput(ctx, "Acknowledge with the single word OK.", nil)
	if err != nil {
		t.Fatalf("ProcessInput turn 2: %v", err)
	}
	t.Logf("turn-2 out=%.80q", out2)

	hist := currentHistory(t, sess)
	pinned := sess.PinnedNote()
	sess.Close()
	<-done

	if !sawCompaction {
		t.Fatal("no ContextCompactionData event — a real fold did not happen, so the handoff path was never exercised")
	}

	// The note is consumed by the compaction (one-shot handoff), so after the fold
	// it lives in history as a steering turn, not in the pinned slot.
	var handoff string
	for _, turn := range hist {
		text := turn.Message.Text()
		if strings.Contains(text, noteHandoffPrefix) {
			handoff = text
		}
	}
	if handoff == "" {
		t.Fatalf("no 'note to yourself from before compaction' handoff turn in post-compaction history (pinned slot = %q)", pinned)
	}
	t.Logf("handoff turn (%d chars):\n%.600s", len(handoff), handoff)

	if !strings.Contains(handoff, token) {
		t.Errorf("handoff note is missing the opaque token %q — the elicitor fired but did not capture the folded fact", token)
	}
}
