//go:build eval

package contextmgr

// Controlled with/without-note compaction-quality comparison, run against the
// real OAuth OpenAI endpoint (NOT mocked).
//
// Smoke first:
//   go test -tags eval ./agent/internal/contextmgr/ -run TestEvalSmoke -v -timeout 5m
// Full eval:
//   go test -tags eval ./agent/internal/contextmgr/ -run TestCompactionComparison -v -timeout 20m
//
// OAuth wiring: the creds are stored at <XDG_STATE_HOME>/serf/auth/openai.json,
// not env vars. We set XDG_STATE_HOME=/home/jesse/.local/state so the openai
// adapter's OAuth resolution (auth/openai.DefaultStateDirWithStateHome ->
// "<stateHome>/serf") finds the real record, load the real providers.toml
// (~/.serf/providers.toml, which has [instances.openai] type="openai"), build a
// client via llm.NewFromAvailableProviders (which threads StateHome from
// XDG_STATE_HOME into the openai factory), and resolve the "openai" profile via
// provider.ResolveProfileFromConfig. NewManager(profile, client) then summarizes
// through the OAuth-backed Codex backend.
//
// Three arms per case (all combined-output scored):
//   A baseline:    summarizeWithLLMSteered(hist, 2, "")              -> summary only
//   B note only:   [NOTE TO SELF] mustKeep [END] + baseline summary
//   C note + steer:[NOTE TO SELF] mustKeep [END] + steered summary (c.instruction)
// Scored two ways: objective needle-match (mustKeep present?) and an LLM-judge
// 1-5 "could a fresh agent continue?" score from the same real model.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"

	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/ollama"
	_ "primeradiant.com/serf/llm/providers/openai"
)

// oauthStateHome is the XDG_STATE_HOME under which serf/auth/openai.json lives.
const oauthStateHome = "/home/jesse/.local/state"

// oauthProvidersConfig is the real providers.toml with [instances.openai].
const oauthProvidersConfig = "/home/jesse/.serf/providers.toml"

// evalModel is the openai model the eval drives. The profile's cheap-model
// resolution picks the actual summarization model; the OAuth token works for
// any openai model on the Codex backend.
const evalModel = "gpt-5.5"

// resultsDoc is where the full eval writes its markdown summary.
const resultsDoc = "../../../docs/design/2026-06-14-compaction-comparison-results.md"

// newOAuthManager builds a *Manager whose openai client uses the stored OAuth
// credentials. It skips the test if the OAuth record or providers.toml is
// missing so the eval never fails for an unconfigured machine.
func newOAuthManager(t *testing.T) *Manager {
	t.Helper()

	if _, err := os.Stat(filepath.Join(oauthStateHome, "serf", "auth", "openai.json")); err != nil {
		t.Skipf("no OAuth record at %s/serf/auth/openai.json: %v", oauthStateHome, err)
	}

	// Point OAuth resolution at the real state home for the duration of the test.
	t.Setenv("XDG_STATE_HOME", oauthStateHome)

	cfg, exists, err := providercfg.LoadFile(oauthProvidersConfig)
	if err != nil {
		t.Fatalf("load providers.toml %s: %v", oauthProvidersConfig, err)
	}
	if !exists {
		t.Skipf("providers.toml not found at %s", oauthProvidersConfig)
	}

	// Build the client. NewFromAvailableProviders threads StateHome (from
	// XDG_STATE_HOME) into each factory, so the openai factory resolves OAuth
	// from <stateHome>/serf/auth/openai.json and prefers it over any API key.
	client, errs, err := llm.NewFromAvailableProviders(cfg)
	if err != nil {
		t.Fatalf("llm.NewFromAvailableProviders: %v (partial errs: %v)", err, errs)
	}

	prof, err := provider.ResolveProfileFromConfig(cfg, "openai/"+evalModel)
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig openai/%s: %v", evalModel, err)
	}

	return NewManager(prof, client)
}

// pinnedNote wraps a note the way agent/session_compaction.renderPinnedNote does.
// Replicated inline because renderPinnedNote lives in package agent.
func pinnedNote(note string) string {
	return "[NOTE TO SELF]\n" + note + "\n[END NOTE TO SELF]\n"
}

// TestEvalSmoke confirms the OAuth client is wired: summarize one short history
// through the real model and require a non-empty summary.
func TestEvalSmoke(t *testing.T) {
	cm := newOAuthManager(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	hist := turnsFromText(t, block("build output", repeat("go build ./...\n", 30))+"\n"+
		fact("The team decided the public API signature is: func CreateOrder(ctx, req) (*Order, error)"))

	out, err := cm.summarizeWithLLMSteered(ctx, hist, 2, "")
	if err != nil {
		t.Fatalf("summarizeWithLLMSteered (real OAuth model): %v", err)
	}
	summary := summaryText(out)
	if strings.TrimSpace(summary) == "" {
		t.Fatalf("empty summary from real model")
	}
	t.Logf("SMOKE OK — real model returned %d chars:\n%.800s", len(summary), summary)
}

// armResult records one arm's output and scores for one case.
type armResult struct {
	output      string
	needleKept  bool
	judge       int // 1-5, 0 if unparseable
	judgeParsed bool
}

// caseRow holds the three arm results for one corpus case.
type caseRow struct {
	name    string
	a, b, c armResult
}

// caseWin names a case where the baseline dropped the needle but B/C kept it.
type caseWin struct{ name string }

// judgeOutput asks the real model to rate a handoff 1-5 and parses the integer.
// Deterministic-ish: temperature 0, strict rubric, integer-only answer.
func (cm *Manager) judgeOutput(ctx context.Context, taskContext, handoff string) (int, bool, error) {
	prof := cm.currentProfile()
	models := summarizationModels(prof)
	if len(models) == 0 {
		return 0, false, fmt.Errorf("no summarization model for judge")
	}

	temp := 0.0
	prompt := "You are grading a context-compaction handoff for a software-engineering agent.\n\n" +
		"TASK CONTEXT (what the agent was doing, including the key fact that must survive):\n" +
		taskContext + "\n\n" +
		"HANDOFF (all that survives compaction; a fresh agent sees only this):\n" +
		handoff + "\n\n" +
		"Rate 1-5 on this single question: Could a fresh agent seamlessly continue the work from this handoff? " +
		"Consider whether the key facts/decisions survived AND whether noise was dropped.\n" +
		"Rubric: 5 = key facts fully preserved and noise dropped; 3 = key facts present but buried in noise OR partially lost; " +
		"1 = key facts lost or handoff unusable.\n" +
		"Answer with ONLY a single integer 1-5. No words, no punctuation."

	req := llm.Request{
		Model:       models[0],
		Provider:    prof.ID(),
		Messages:    []llm.Message{llm.User(prompt)},
		Temperature: &temp,
	}
	resp, err := cm.client.Complete(ctx, req)
	if err != nil {
		return 0, false, err
	}
	n, ok := parseJudgeInt(resp.Text())
	return n, ok, nil
}

// perCallTimeout bounds a single model round-trip so one slow call cannot
// consume the whole run's budget.
const perCallTimeout = 3 * time.Minute

// summarizeRetry runs summarizeWithLLMSteered with a bounded per-call timeout,
// retrying once on a transient error so a single slow/timed-out call does not
// drop a case.
func summarizeRetry(t *testing.T, cm *Manager, hist []schema.Turn, instructions string) (string, error) {
	t.Helper()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), perCallTimeout)
		out, err := cm.summarizeWithLLMSteered(ctx, hist, 2, instructions)
		cancel()
		if err == nil {
			return summaryText(out), nil
		}
		lastErr = err
	}
	return "", lastErr
}

// judgeRetry runs the LLM-judge with a bounded per-call timeout, retrying once
// on a transient error.
func judgeRetry(t *testing.T, cm *Manager, taskContext, handoff string) (int, bool, error) {
	t.Helper()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), perCallTimeout)
		n, ok, err := cm.judgeOutput(ctx, taskContext, handoff)
		cancel()
		if err == nil {
			return n, ok, nil
		}
		lastErr = err
	}
	return 0, false, lastErr
}

// parseJudgeInt extracts the first 1-5 integer from the model's reply.
func parseJudgeInt(s string) (int, bool) {
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r < '0' || r > '9' }) {
		if n, err := strconv.Atoi(f); err == nil && n >= 1 && n <= 5 {
			return n, true
		}
	}
	return 0, false
}

// taskContextFor builds the judge's reference: the original history plus the
// must-keep fact stated plainly, so the judge knows what should have survived.
func taskContextFor(c obedienceCase) string {
	return "The agent's work history contained a large block of droppable noise and one critical fact " +
		"that MUST survive compaction:\n  KEY FACT (must survive): " + c.mustKeep + "\n  (full fact text: " +
		strings.TrimSpace(c.history[strings.LastIndex(c.history, "IMPORTANT FACT:"):]) + ")"
}

// TestCompactionComparison runs the three-arm with/without-note comparison over
// the obedience corpus against the real OAuth model and reports per-arm
// needle-retention + mean judge score, the A->B and B->C gaps, and the concrete
// baseline-drop wins.
func TestCompactionComparison(t *testing.T) {
	cm := newOAuthManager(t)
	cases := obedienceCases()

	rows := make([]caseRow, 0, len(cases))
	var skipped []string

	for _, c := range cases {
		hist := turnsFromText(t, c.history)
		note := pinnedNote(c.mustKeep)
		taskCtx := taskContextFor(c)

		// Arm A: baseline summary only.
		aSummary, err := summarizeRetry(t, cm, hist, "")
		if err != nil {
			t.Logf("case %q SKIPPED (arm A transient error after retries): %v", c.name, err)
			skipped = append(skipped, c.name)
			continue
		}

		// Arm C: steered summary (separate call; instruction-guided).
		cSummary, err := summarizeRetry(t, cm, hist, c.instruction)
		if err != nil {
			t.Logf("case %q SKIPPED (arm C transient error after retries): %v", c.name, err)
			skipped = append(skipped, c.name)
			continue
		}

		a := armResult{output: aSummary}
		b := armResult{output: note + aSummary}
		cc := armResult{output: note + cSummary}

		for _, ar := range []*armResult{&a, &b, &cc} {
			ar.needleKept = strings.Contains(ar.output, c.mustKeep)
		}

		// LLM-judge each arm.
		judgeFailed := false
		for _, ar := range []*armResult{&a, &b, &cc} {
			n, ok, jerr := judgeRetry(t, cm, taskCtx, ar.output)
			if jerr != nil {
				t.Logf("case %q SKIPPED (judge transient error after retries): %v", c.name, jerr)
				judgeFailed = true
				break
			}
			ar.judge, ar.judgeParsed = n, ok
		}
		if judgeFailed {
			skipped = append(skipped, c.name)
			continue
		}

		rows = append(rows, caseRow{name: c.name, a: a, b: b, c: cc})
		t.Logf("case %-32s needle A=%v B=%v C=%v | judge A=%d B=%d C=%d",
			c.name, a.needleKept, b.needleKept, cc.needleKept, a.judge, b.judge, cc.judge)
	}
	if len(skipped) > 0 {
		t.Logf("NOTE: %d case(s) skipped on transient errors: %s", len(skipped), strings.Join(skipped, ", "))
	}
	if len(rows) == 0 {
		t.Fatalf("no cases completed — every case hit a transient error")
	}

	// Aggregate.
	n := len(rows)
	var keepA, keepB, keepC int
	var sumA, sumB, sumC, cntA, cntB, cntC int
	var wins []caseWin

	for _, r := range rows {
		if r.a.needleKept {
			keepA++
		}
		if r.b.needleKept {
			keepB++
		}
		if r.c.needleKept {
			keepC++
		}
		if r.a.judgeParsed {
			sumA += r.a.judge
			cntA++
		}
		if r.b.judgeParsed {
			sumB += r.b.judge
			cntB++
		}
		if r.c.judgeParsed {
			sumC += r.c.judge
			cntC++
		}
		if !r.a.needleKept && (r.b.needleKept || r.c.needleKept) {
			wins = append(wins, caseWin{name: r.name})
		}
	}

	rate := func(k int) float64 { return float64(k) / float64(n) }
	mean := func(s, c int) float64 {
		if c == 0 {
			return 0
		}
		return float64(s) / float64(c)
	}

	retA, retB, retC := rate(keepA), rate(keepB), rate(keepC)
	jA, jB, jC := mean(sumA, cntA), mean(sumB, cntB), mean(sumC, cntC)

	sort.Slice(wins, func(i, j int) bool { return wins[i].name < wins[j].name })

	t.Logf("=== AGGREGATE (%d cases) ===", n)
	t.Logf("needle retention:  A=%.2f  B=%.2f  C=%.2f", retA, retB, retC)
	t.Logf("mean judge (1-5):  A=%.2f  B=%.2f  C=%.2f", jA, jB, jC)
	t.Logf("A->B needle gap: %+.2f  (note's value)", retB-retA)
	t.Logf("B->C needle gap: %+.2f  (steering's value)", retC-retB)
	t.Logf("A->B judge gap:  %+.2f", jB-jA)
	t.Logf("B->C judge gap:  %+.2f", jC-jB)
	t.Logf("baseline-drop wins (A dropped needle, B/C kept it): %d", len(wins))
	for _, w := range wins {
		t.Logf("  WIN: %s", w.name)
	}

	writeResultsDoc(t, n, len(cases), skipped, rows, retA, retB, retC, jA, jB, jC, wins)
}

// writeResultsDoc renders the markdown summary alongside the design docs.
func writeResultsDoc(t *testing.T, n, total int, skipped []string, rows []caseRow, retA, retB, retC, jA, jB, jC float64, wins []caseWin) {
	t.Helper()

	var b strings.Builder
	fmt.Fprintf(&b, "# Compaction comparison: with vs without an agent-authored note\n\n")
	fmt.Fprintf(&b, "_Generated by `TestCompactionComparison` (`//go:build eval`) on %s against the real OAuth OpenAI endpoint (model `%s`, no mocks)._\n\n",
		time.Now().Format("2006-01-02"), evalModel)
	fmt.Fprintf(&b, "Three post-compaction arms per case over the %d-case obedience corpus (%d/%d cases completed; %d skipped on transient model errors).\n\n", total, n, total, len(skipped))
	if len(skipped) > 0 {
		fmt.Fprintf(&b, "Skipped (transient errors, not scored): %s\n\n", strings.Join(skipped, ", "))
	}
	fmt.Fprintf(&b, "- **A baseline**: `summarizeWithLLMSteered(hist, 2, \"\")` — cheap-model summary only.\n")
	fmt.Fprintf(&b, "- **B note only**: `[NOTE TO SELF]`+key fact+`[END]` + the same baseline summary.\n")
	fmt.Fprintf(&b, "- **C note + steer**: the note block + `summarizeWithLLMSteered(hist, 2, instruction)`.\n\n")
	fmt.Fprintf(&b, "The note content is the case's `mustKeep` key fact, simulating an agent that pinned the fact before compaction.\n\n")

	fmt.Fprintf(&b, "## Aggregate\n\n")
	fmt.Fprintf(&b, "| Metric | A baseline | B note | C note+steer |\n")
	fmt.Fprintf(&b, "|---|---|---|---|\n")
	fmt.Fprintf(&b, "| Needle retention | %.2f | %.2f | %.2f |\n", retA, retB, retC)
	fmt.Fprintf(&b, "| Mean judge (1-5) | %.2f | %.2f | %.2f |\n\n", jA, jB, jC)
	fmt.Fprintf(&b, "- **A→B needle gap (the note's value): %+.2f**\n", retB-retA)
	fmt.Fprintf(&b, "- **B→C needle gap (the steering's value): %+.2f**\n", retC-retB)
	fmt.Fprintf(&b, "- A→B judge gap: %+.2f\n", jB-jA)
	fmt.Fprintf(&b, "- B→C judge gap: %+.2f\n\n", jC-jB)

	fmt.Fprintf(&b, "## Concrete baseline-drop wins\n\n")
	fmt.Fprintf(&b, "Cases where the baseline summary (A) dropped the needle but the note/steer arms (B/C) kept it:\n\n")
	if len(wins) == 0 {
		fmt.Fprintf(&b, "_None — the baseline summary retained every needle in this run._\n\n")
	} else {
		for _, w := range wins {
			fmt.Fprintf(&b, "- `%s`\n", w.name)
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "## Per-case detail\n\n")
	fmt.Fprintf(&b, "| Case | needle A/B/C | judge A/B/C |\n")
	fmt.Fprintf(&b, "|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s/%s/%s | %d/%d/%d |\n",
			r.name,
			yn(r.a.needleKept), yn(r.b.needleKept), yn(r.c.needleKept),
			r.a.judge, r.b.judge, r.c.judge)
	}

	if err := os.MkdirAll(filepath.Dir(resultsDoc), 0o755); err != nil {
		t.Fatalf("mkdir results doc dir: %v", err)
	}
	if err := os.WriteFile(resultsDoc, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write results doc: %v", err)
	}
	abs, _ := filepath.Abs(resultsDoc)
	t.Logf("wrote results doc: %s", abs)
}

func yn(b bool) string {
	if b {
		return "Y"
	}
	return "n"
}
