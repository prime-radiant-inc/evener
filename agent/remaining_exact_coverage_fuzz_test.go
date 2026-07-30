//go:build serffuzz

package agent

import (
	"embed"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
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
	m := newSubagentManager(nil, 0)
	existing := &subagent{id: "existing"}
	m.track(existing)
	if got, pending, leader, err := m.beginReconstruction(existing.id); err != nil || got != existing || pending != nil || leader {
		t.Fatalf("existing reconstruction = (%p, %p, %v, %v)", got, pending, leader, err)
	}

	closing := newSubagentManager(nil, 0)
	tracked := &subagent{id: "tracked"}
	closing.track(tracked)
	closing.closing = true
	if finish, err := closing.beginReconstructionSideEffects(tracked.id, tracked); finish != nil || !errors.Is(err, errSubagentManagerClosing) || closing.get(tracked.id) != nil {
		t.Fatalf("closing side effects = (%v, %v, %p)", finish != nil, err, closing.get(tracked.id))
	}
	other := &subagent{id: "other"}
	closing.track(other)
	if _, err := closing.beginReconstructionSideEffects(other.id, tracked); !errors.Is(err, errSubagentManagerClosing) || closing.get(other.id) != other {
		t.Fatalf("closing side effects removed nonmatching runtime: %v", err)
	}

	waiting := newSubagentManager(nil, 0)
	finish, err := waiting.beginReconstructionSideEffects("child", nil)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	done := make(chan struct{})
	oldWait := restoreSideEffectsWait
	restoreSideEffectsWait = func(cond *sync.Cond) {
		close(entered)
		cond.Wait()
	}
	t.Cleanup(func() { restoreSideEffectsWait = oldWait })
	go func() {
		waiting.waitForReconstructionSideEffects()
		close(done)
	}()
	<-entered
	finish()
	<-done

	completed := &subagentReconstruction{done: make(chan struct{})}
	close(completed.done)
	waiting.reconstructing["completed"] = completed
	waiting.waitForReconstructions()
}

func remainingPromptCoverage(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	globalRoot := filepath.Join(root, "global")
	projectSections := filepath.Join(projectRoot, "sections")
	globalSections := filepath.Join(globalRoot, "sections")
	for _, dir := range []string{projectSections, globalSections} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := &Session{
		profile: NewOpenAIProfile("gpt-5.2"),
		reg:     tool.NewRegistry(),
	}
	oldRender := renderEmbeddedSystemPrompt
	oldProjectDir := projectPromptDir
	oldGlobalDir := globalPromptDir
	projectPromptDir = func(execenv.ExecutionEnvironment, string) string { return projectRoot }
	globalPromptDir = func() string { return globalRoot }
	renderEmbeddedSystemPrompt = func(*sectionResolver, embed.FS, string, string, promptData) (string, []promptSource, error) {
		return "", nil, errors.New("forced render failure")
	}
	t.Cleanup(func() {
		renderEmbeddedSystemPrompt = oldRender
		projectPromptDir = oldProjectDir
		globalPromptDir = oldGlobalDir
	})
	got, warning := s.renderSystemPrompt(execenv.NewLocalExecutionEnvironment(t.TempDir()))
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
