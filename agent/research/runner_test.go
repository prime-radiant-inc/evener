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
	root := repoRootForEnvs(t)
	opts := RolloutOptions{
		EnvDir:      filepath.Join(root, "research", "environments"),
		Envs:        []string{"smoke-fix", "smoke-feature"},
		Runs:        2,
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
