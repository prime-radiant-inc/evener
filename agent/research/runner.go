package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"primeradiant.com/evener/agent/internal/liveeval"
)

type environment struct {
	Name       string
	Dir        string
	TaskPrompt string
	RepoDir    string
	VerifyPath string
}

func loadEnvironment(dir string) (environment, error) {
	name := filepath.Base(dir)
	task, err := os.ReadFile(filepath.Join(dir, "task.md"))
	if err != nil {
		return environment{}, fmt.Errorf("%s: task.md: %w", name, err)
	}
	verify := filepath.Join(dir, "verify.sh")
	st, err := os.Stat(verify)
	if err != nil {
		return environment{}, fmt.Errorf("%s: verify.sh: %w", name, err)
	}
	if st.Mode()&0o111 == 0 {
		return environment{}, fmt.Errorf("%s: verify.sh not executable", name)
	}
	repo := filepath.Join(dir, "repo")
	if _, err := os.Stat(filepath.Join(repo, "go.mod")); err != nil {
		return environment{}, fmt.Errorf("%s: repo/go.mod: %w", name, err)
	}
	return environment{Name: name, Dir: dir, TaskPrompt: string(task), RepoDir: repo, VerifyPath: verify}, nil
}

type SessionRun struct {
	Env       string
	WorkDir   string
	StateDir  string
	Model     string
	MaxRounds int
	AtifPath  string
	Prompt    string
}

type sessionExecutor interface {
	Run(ctx context.Context, run SessionRun) error
}

// ExecExecutor runs the harness binary headless:
//
//	evener --model <model> --state-dir <dir> --max-rounds <n> -n <atif> <prompt>
//
// with the process working directory set to the run workdir.
type ExecExecutor struct {
	Binary string
	Live   bool
}

func (e ExecExecutor) Run(ctx context.Context, run SessionRun) error {
	args := []string{
		"--state-dir", run.StateDir,
		"--max-rounds", strconv.Itoa(run.MaxRounds),
		"-n", run.AtifPath,
	}
	if run.Model != "" {
		args = append(args, "--model", run.Model)
	}
	args = append(args, run.Prompt)
	cmd := exec.CommandContext(ctx, e.Binary, args...)
	cmd.Dir = run.WorkDir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

type RunRow struct {
	Env        string      `json:"env"`
	Arm        string      `json:"arm"`
	Rep        int         `json:"rep"`
	Model      string      `json:"model,omitempty"`
	Binary     string      `json:"binary,omitempty"`
	VerifyPass bool        `json:"verify_pass"`
	InfraFail  bool        `json:"infra_fail"`
	Metrics    AtifMetrics `json:"metrics"`
	AtifPath   string      `json:"atif_path"`
	Timestamp  time.Time   `json:"timestamp"`
}

type RolloutOptions struct {
	EnvDir      string
	Envs        []string
	Arm         string
	Model       string
	Binary      string
	Runs        int
	MaxRounds   int
	RunDir      string
	Live        bool
	MaxLiveRuns int
	Stdout      io.Writer
}

// liveGuard refuses a live rollout pass without the explicit opt-in.
func liveGuard(live bool) error {
	if !live {
		return nil
	}
	if !liveeval.Enabled(os.Getenv(liveeval.OptInEnv)) {
		return fmt.Errorf("live rollouts require %s=1", liveeval.OptInEnv)
	}
	return nil
}

// copyTree copies a directory tree (regular files and dirs only).
func copyTree(dst, src string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, st.Mode().Perm())
	})
}

func RunRollouts(ctx context.Context, opts RolloutOptions, ex sessionExecutor) error {
	if err := liveGuard(opts.Live); err != nil {
		return err
	}
	if len(opts.Envs) == 0 {
		return errors.New("no environments selected")
	}
	runs := opts.Runs
	if runs <= 0 {
		runs = 1
	}
	maxRuns := opts.MaxLiveRuns
	if maxRuns <= 0 {
		maxRuns = 48
	}
	planned := len(opts.Envs) * runs
	if planned > maxRuns {
		return fmt.Errorf("planned %d runs exceed --max-live-runs %d", planned, maxRuns)
	}
	if err := os.MkdirAll(opts.RunDir, 0o755); err != nil {
		return err
	}
	ledger, err := os.OpenFile(filepath.Join(opts.RunDir, "runs.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = ledger.Close() }()
	for _, envName := range opts.Envs {
		env, err := loadEnvironment(filepath.Join(opts.EnvDir, envName))
		if err != nil {
			return err
		}
		for rep := 1; rep <= runs; rep++ {
			row, err := runOnce(ctx, opts, env, rep, ex)
			if err != nil {
				return err
			}
			line, err := json.Marshal(row)
			if err != nil {
				return err
			}
			if _, err := ledger.Write(append(line, '\n')); err != nil {
				return err
			}
		}
	}
	return nil
}

func runOnce(ctx context.Context, opts RolloutOptions, env environment, rep int, ex sessionExecutor) (RunRow, error) {
	row := RunRow{
		Env: env.Name, Arm: opts.Arm, Rep: rep,
		Model: opts.Model, Binary: opts.Binary,
		Timestamp: time.Now().UTC(),
	}
	runDir := filepath.Join(opts.RunDir, env.Name, fmt.Sprintf("%s-%02d", opts.Arm, rep))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return row, err
	}
	workDir := filepath.Join(runDir, "work")
	if err := copyTree(workDir, env.RepoDir); err != nil {
		return row, err
	}
	stateDir := filepath.Join(runDir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return row, err
	}
	atifPath := filepath.Join(runDir, "run.atif.json")
	run := SessionRun{
		Env: env.Name, WorkDir: workDir, StateDir: stateDir,
		Model: opts.Model, MaxRounds: opts.MaxRounds,
		AtifPath: atifPath, Prompt: env.TaskPrompt,
	}
	if err := ex.Run(ctx, run); err != nil {
		row.InfraFail = true
		row.AtifPath = atifPath
		return row, nil //nolint:nilerr // infra failure: recorded, not fatal to the pass
	}
	row.AtifPath = atifPath
	row.Metrics, _ = ExtractAtifMetrics(atifPath)
	verify := exec.CommandContext(ctx, env.VerifyPath, workDir)
	if err := verify.Run(); err != nil {
		row.VerifyPass = false
	} else {
		row.VerifyPass = true
	}
	return row, nil
}
