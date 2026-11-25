//go:build evenerfuzz

package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
)

func FuzzRemainingExactCoverage(f *testing.F) {
	f.Add([]byte{0})
	f.Fuzz(func(t *testing.T, data []byte) {
		remainingRefCoverage(t)
		remainingManagerCoverage(t)
		remainingPromptCoverage(t)
		remainingOutlineCoverage(t)
	})
}

func remainingRefCoverage(t *testing.T) {
	t.Helper()
	if _, _, err := decodeRef("proj:missing-session"); err == nil {
		t.Fatal("malformed project ref was accepted")
	}
}

func remainingManagerCoverage(t *testing.T) {
	t.Helper()
	m := newSubagentManager(0)
	existing := &subagent{id: "existing"}
	m.track(existing)
	if got, pending, leader, err := m.beginReconstruction(existing.id); err != nil || got != existing || pending != nil || leader {
		t.Fatalf("existing reconstruction = (%p, %p, %v, %v)", got, pending, leader, err)
	}

	waiting := newSubagentManager(0)
	completed := &subagentReconstruction{done: make(chan struct{})}
	close(completed.done)
	waiting.reconstructing["completed"] = completed
	waiting.waitForReconstructions()
}

func remainingPromptCoverage(t *testing.T) {
	t.Helper()
	s := &Session{
		profile: NewOpenAIProfile("gpt-5.2"),
		reg:     tool.NewRegistry(),
	}
	oldExecute := executeSystemPromptTemplate
	executeSystemPromptTemplate = func(promptData) (string, error) {
		return "", errors.New("forced render failure")
	}
	t.Cleanup(func() { executeSystemPromptTemplate = oldExecute })
	got, warning, _ := s.renderSystemPrompt(execenv.NewLocalExecutionEnvironment(t.TempDir()))
	if !strings.Contains(got, "forced render failure") {
		t.Fatalf("render failure prompt = %q", got)
	}
	// The diagnostic is RETURNED, never emitted from in here: renderSystemPrompt
	// runs under s.mu at three call sites and emit takes s.mu to stamp
	// provenance, so emitting would self-deadlock. Assert the caller is actually
	// handed something to report, or the failure would be silent.
	if !strings.Contains(warning, "forced render failure") {
		t.Fatalf("render failure warning = %q, want it to name the failure", warning)
	}
}

func remainingOutlineCoverage(t *testing.T) {
	t.Helper()
	oldBudget := outlineBudgetChars
	oldReserve := outlineMarkerReserve
	t.Cleanup(func() {
		outlineBudgetChars = oldBudget
		outlineMarkerReserve = oldReserve
	})

	outlineBudgetChars = 20
	if _, truncated, elided := boundOutline([]string{strings.Repeat("x", 21)}); !truncated || elided != 1 {
		t.Fatalf("small outline budget = (%v, %d)", truncated, elided)
	}
	outlineBudgetChars = -1
	if _, truncated, elided := boundOutline([]string{"x"}); !truncated || elided != 1 {
		t.Fatalf("negative outline budget = (%v, %d)", truncated, elided)
	}
	outlineBudgetChars = 1
	outlineMarkerReserve = -3
	if got, truncated, elided := boundOutline([]string{"xx"}); got != "xx" || truncated || elided != 0 {
		t.Fatalf("fitting outline = (%q, %v, %d)", got, truncated, elided)
	}
}
