package research

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubExecutor stands in for the real binary. It claims the task done by
// applying the known fix to the workdir and writing a canned ATIF export.
type stubExecutor struct {
	calls []SessionRun
}

func (s *stubExecutor) Run(_ context.Context, run SessionRun) error {
	s.calls = append(s.calls, run)
	fix := filepath.Join(run.WorkDir, "calc", "calc.go")
	if _, err := os.Stat(fix); err == nil {
		_ = os.WriteFile(fix, []byte("package calc\n\nfunc Average(xs []float64) float64 {\n\tif len(xs) == 0 {\n\t\treturn 0\n\t}\n\tsum := 0.0\n\tfor _, x := range xs {\n\t\tsum += x\n\t}\n\treturn sum / float64(len(xs))\n}\n"), 0o644)
	}
	return os.WriteFile(run.AtifPath, []byte(`{"schema_version":"1.7","session_id":"x","steps":[],"final_metrics":{"total_prompt_tokens":1,"total_completion_tokens":1,"total_cached_tokens":0,"total_steps":0}}`), 0o644)
}

func repoRootForEnvs(t *testing.T) string { return repoRoot(t) }

func TestRunRollouts_OfflineMechanics(t *testing.T) {
	// The rollout seam tests pin the live opt-in: every RunRollouts pass
	// executes the real harness binary (no offline rollout mode in slice
	// 1), so the runner requires the opt-in regardless of --live.
	t.Setenv("EVENER_LIVE_TESTS", "1")
	root := repoRootForEnvs(t)
	runDir := t.TempDir()
	stub := &stubExecutor{}
	opts := RolloutOptions{
		EnvDir:      filepath.Join(root, "research", "environments"),
		Envs:        []string{"smoke-fix"},
		Arm:         "base",
		Runs:        2,
		MaxRounds:   20,
		RunDir:      runDir,
		MaxLiveRuns: 48,
		Stdout:      os.Stderr,
	}
	if err := RunRollouts(context.Background(), opts, stub); err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("executor calls = %d, want 2", len(stub.calls))
	}
	// The fixture repo must not be mutated: the workdir is a copy.
	orig, err := os.ReadFile(filepath.Join(root, "research", "environments", "smoke-fix", "repo", "calc", "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(orig), "len(xs)+1") {
		t.Fatal("fixture repo was mutated by the runner")
	}
	// Rows land in runs.jsonl with verify results and metrics.
	data, err := os.ReadFile(filepath.Join(runDir, "runs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("rows = %d, want 2", len(lines))
	}
	var row RunRow
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatal(err)
	}
	if row.Env != "smoke-fix" || row.Arm != "base" || !row.VerifyPass {
		t.Fatalf("row = %+v", row)
	}
	if row.Metrics.PromptTokens != 1 {
		t.Fatalf("metrics not extracted: %+v", row.Metrics)
	}
	// Workdir prompt came from task.md.
	if !strings.Contains(stub.calls[0].Prompt, "Average") {
		t.Fatal("prompt did not carry task.md contents")
	}
}

func TestRunRollouts_EnforcesCap(t *testing.T) {
	t.Setenv("EVENER_LIVE_TESTS", "1")
	root := repoRootForEnvs(t)
	opts := RolloutOptions{
		EnvDir:      filepath.Join(root, "research", "environments"),
		Envs:        []string{"smoke-fix", "smoke-feature"},
		Runs:        2,
		MaxRounds:   20,
		RunDir:      t.TempDir(),
		MaxLiveRuns: 3, // 2 envs x 2 runs = 4 planned > 3
		Stdout:      os.Stderr,
	}
	err := RunRollouts(context.Background(), opts, &stubExecutor{})
	if err == nil || !strings.Contains(err.Error(), "max-live-runs") {
		t.Fatalf("want max-live-runs cap error, got %v", err)
	}
}

func TestRunRollouts_InfraFailExcludedFromVerifyVerdict(t *testing.T) {
	t.Setenv("EVENER_LIVE_TESTS", "1")
	root := repoRootForEnvs(t)
	stub := &failingExecutor{}
	opts := RolloutOptions{
		EnvDir:    filepath.Join(root, "research", "environments"),
		Envs:      []string{"smoke-fix"},
		Runs:      1,
		RunDir:    t.TempDir(),
		MaxRounds: 20,
		Stdout:    os.Stderr,
	}
	if err := RunRollouts(context.Background(), opts, stub); err != nil {
		t.Fatalf("infra failures must not abort the pass: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(opts.RunDir, "runs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var row RunRow
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &row); err != nil {
		t.Fatal(err)
	}
	if !row.InfraFail {
		t.Fatalf("row must record infra failure: %+v", row)
	}
}

type failingExecutor struct{}

func (failingExecutor) Run(context.Context, SessionRun) error {
	return errors.New("provider stream cut")
}

// countEnv reports how many times the exact entry kv appears in env.
func countEnv(env []string, kv string) int {
	n := 0
	for _, e := range env {
		if e == kv {
			n++
		}
	}
	return n
}

func TestRunRollouts_RefusesWithoutLiveOptIn(t *testing.T) {
	// Slice 1 has no offline rollout mode: even a pass launched without
	// --live executes the real harness binary and issues live provider
	// calls, so the opt-in is required regardless of the flag. The refusal
	// must come before anything runs: no executor call, no ledger row.
	t.Setenv("EVENER_LIVE_TESTS", "")
	root := repoRootForEnvs(t)
	runDir := t.TempDir()
	stub := &stubExecutor{}
	opts := RolloutOptions{
		EnvDir:    filepath.Join(root, "research", "environments"),
		Envs:      []string{"smoke-fix"},
		RunDir:    runDir,
		MaxRounds: 20,
		Stdout:    os.Stderr,
	} // Live deliberately unset: the guard must not depend on it.
	err := RunRollouts(context.Background(), opts, stub)
	if err == nil || !strings.Contains(err.Error(), "EVENER_LIVE_TESTS") {
		t.Fatalf("want EVENER_LIVE_TESTS refusal, got %v", err)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("executor ran %d time(s) despite the refusal", len(stub.calls))
	}
	if _, err := os.Stat(filepath.Join(runDir, "runs.jsonl")); !os.IsNotExist(err) {
		t.Fatal("runs.jsonl was written despite the refusal")
	}
}

func TestExecExecutor_RefusesWithoutLiveOptIn(t *testing.T) {
	// Defense in depth: ExecExecutor executes the real harness binary, so
	// even a caller that bypasses RunRollouts must not get past the opt-in.
	t.Setenv("EVENER_LIVE_TESTS", "")
	err := ExecExecutor{Binary: "/nonexistent/definitely-missing"}.Run(context.Background(), SessionRun{
		WorkDir: t.TempDir(), StateDir: t.TempDir(), MaxRounds: 5,
		AtifPath: filepath.Join(t.TempDir(), "run.atif.json"), Prompt: "p",
	})
	if err == nil || !strings.Contains(err.Error(), "EVENER_LIVE_TESTS") {
		t.Fatalf("want EVENER_LIVE_TESTS refusal, got %v", err)
	}
}

func TestRunRollouts_RejectsNonPositiveMaxRounds(t *testing.T) {
	// evener treats --max-rounds 0 as UNLIMITED (cmd/evener/main.go), so
	// passing a non-positive cap through would remove the round limit
	// instead of imposing one. The rejection must run nothing.
	t.Setenv("EVENER_LIVE_TESTS", "1")
	root := repoRootForEnvs(t)
	for _, rounds := range []int{0, -3} {
		runDir := t.TempDir()
		stub := &stubExecutor{}
		opts := RolloutOptions{
			EnvDir:    filepath.Join(root, "research", "environments"),
			Envs:      []string{"smoke-fix"},
			RunDir:    runDir,
			MaxRounds: rounds,
			Stdout:    os.Stderr,
		}
		err := RunRollouts(context.Background(), opts, stub)
		if err == nil || !strings.Contains(err.Error(), "max rounds") {
			t.Fatalf("rounds=%d: want max-rounds rejection, got %v", rounds, err)
		}
		if len(stub.calls) != 0 {
			t.Fatalf("rounds=%d: executor ran despite the rejection", rounds)
		}
		if _, err := os.Stat(filepath.Join(runDir, "runs.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("rounds=%d: runs.jsonl was written despite the rejection", rounds)
		}
	}
}

func TestRunRollouts_VerifyChildCarriesGOWORKOff(t *testing.T) {
	// An exported GOWORK pointing at a missing file makes every raw go
	// invocation fail at workspace setup; without runner-side isolation
	// that failure would be recorded as verify_pass=false, silently
	// poisoning the ledger. The stub applies the known fix, so verify.sh
	// can only pass if its go test ran with GOWORK=off.
	t.Setenv("EVENER_LIVE_TESTS", "1")
	t.Setenv("GOWORK", "/definitely/missing/go.work")
	root := repoRootForEnvs(t)
	opts := RolloutOptions{
		EnvDir:    filepath.Join(root, "research", "environments"),
		Envs:      []string{"smoke-fix"},
		RunDir:    t.TempDir(),
		MaxRounds: 20,
		Stdout:    os.Stderr,
	}
	if err := RunRollouts(context.Background(), opts, &stubExecutor{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(opts.RunDir, "runs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var row RunRow
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &row); err != nil {
		t.Fatal(err)
	}
	if !row.VerifyPass {
		t.Fatalf("verify failed under an exported GOWORK; the verify child was not isolated: %+v", row)
	}
}

func TestExecExecutor_ChildEnvCarriesGOWORKOff(t *testing.T) {
	// A fake harness binary records the GOWORK it was handed; the harness
	// child env must pin off even when the parent exports a stale GOWORK.
	t.Setenv("EVENER_LIVE_TESTS", "1")
	t.Setenv("GOWORK", "/definitely/missing/go.work")
	dump := filepath.Join(t.TempDir(), "gowork.txt")
	bin := filepath.Join(t.TempDir(), "fake-harness.sh")
	script := "#!/bin/sh\nprintenv GOWORK > " + dump + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	run := SessionRun{
		WorkDir:   t.TempDir(),
		StateDir:  t.TempDir(),
		MaxRounds: 5,
		AtifPath:  filepath.Join(t.TempDir(), "run.atif.json"),
		Prompt:    "do the task",
	}
	if err := (ExecExecutor{Binary: bin}).Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "off" {
		t.Fatalf("harness child saw GOWORK=%q, want off", strings.TrimSpace(string(got)))
	}
}

func TestChildEnvForcesGOWORKOff(t *testing.T) {
	// Without an exported GOWORK the child environment still pins off.
	if countEnv(childEnv(), "GOWORK=off") != 1 {
		t.Fatalf("childEnv() = %v, want exactly one GOWORK=off", childEnv())
	}
	// A stale exported GOWORK is dropped first: POSIX getenv returns the
	// first match, so a trailing override alone could lose to it.
	t.Setenv("GOWORK", "/definitely/missing/go.work")
	env := childEnv()
	if countEnv(env, "GOWORK=off") != 1 || countEnv(env, "GOWORK=/definitely/missing/go.work") != 0 {
		t.Fatalf("childEnv() with exported GOWORK = %v, want the export dropped and exactly one GOWORK=off", env)
	}
}
