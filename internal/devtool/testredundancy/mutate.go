package testredundancy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Mutation is one seeded fault: replace the single occurrence of Old in File
// (relative to the package directory) with New, then run the tests Run
// selects (a -test.run pattern).
type Mutation struct {
	Name string `json:"name"`
	File string `json:"file"`
	Old  string `json:"old"`
	New  string `json:"new"`
	Run  string `json:"run"`
}

// Outcome is what the selected tests made of a mutation.
type Outcome string

const (
	OutcomeCaught   Outcome = "caught"   // a selected test failed
	OutcomeSurvived Outcome = "survived" // every selected test passed
	OutcomeInvalid  Outcome = "invalid"  // not applied, not built, or no test ran
)

// MutationResult is one mutation's outcome. Failed names the tests that failed
// on a caught mutation; Detail says why a mutation is invalid.
type MutationResult struct {
	Mutation
	Outcome Outcome  `json:"outcome"`
	Failed  []string `json:"failed,omitempty"`
	Detail  string   `json:"detail,omitempty"`
}

// LoadMutations reads a JSON array of mutations.
func LoadMutations(path string) ([]Mutation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var muts []Mutation
	if err := json.Unmarshal(data, &muts); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return muts, nil
}

// Mutate applies each mutation alone, runs the tests it selects, and restores
// the file with a file-scoped git restore before the next. Restoring discards
// uncommitted work, so it refuses a mutation of a file git reports modified.
func (r *Runner) Mutate(ctx context.Context, muts []Mutation) ([]MutationResult, error) {
	bin := filepath.Join(r.CacheDir, "build", "mutant.test")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return nil, err
	}
	var results []MutationResult
	for _, m := range muts {
		res, err := r.mutateOne(ctx, bin, m)
		if err != nil {
			return results, fmt.Errorf("mutation %s: %w", m.Name, err)
		}
		_, _ = fmt.Fprintf(r.Log, "%s: %s %s\n", m.Name, res.Outcome, strings.Join(res.Failed, " "))
		results = append(results, res)
	}
	return results, nil
}

func (r *Runner) mutateOne(ctx context.Context, bin string, m Mutation) (res MutationResult, err error) {
	res = MutationResult{Mutation: m, Outcome: OutcomeInvalid}
	path := filepath.Join(r.absDir, m.File)
	status, err := r.git(ctx, "status", "--porcelain", "--", path)
	if err != nil {
		return res, err
	}
	if status != "" {
		return res, fmt.Errorf("%s has uncommitted changes; restoring it would discard them", m.File)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return res, err
	}
	if n := bytes.Count(src, []byte(m.Old)); n != 1 {
		res.Detail = fmt.Sprintf("old text occurs %d times", n)
		return res, nil
	}
	if err := os.WriteFile(path, bytes.Replace(src, []byte(m.Old), []byte(m.New), 1), 0o644); err != nil {
		return res, err
	}
	defer func() {
		// A restore must run even when ctx is cancelled mid-mutation.
		if _, rerr := r.git(context.WithoutCancel(ctx), "restore", "--", path); rerr != nil {
			err = errors.Join(err, fmt.Errorf("restoring %s: %w", m.File, rerr))
		}
	}()

	build := exec.CommandContext(ctx, "go", "test", "-c", "-o", bin, ".")
	build.Dir = r.absDir
	if out, berr := build.CombinedOutput(); berr != nil {
		res.Detail = "does not build: " + firstError(out)
		return res, nil
	}
	run := exec.CommandContext(ctx, bin, "-test.count=1", "-test.timeout="+r.TestTimeout.String(), "-test.run="+m.Run)
	run.Dir = r.absDir
	out, runErr := run.CombinedOutput()
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	var exit *exec.ExitError
	switch {
	case runErr != nil && !errors.As(runErr, &exit):
		return res, runErr
	case runErr != nil:
		res.Outcome = OutcomeCaught
		for _, match := range failLine.FindAllSubmatch(out, -1) {
			res.Failed = append(res.Failed, string(match[1]))
		}
	case bytes.Contains(out, noTestsRun):
		res.Detail = "no test matches " + m.Run
	default:
		res.Outcome = OutcomeSurvived
	}
	return res, nil
}

func (r *Runner) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.absDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w%s", strings.Join(args, " "), err, stderrOf(err))
	}
	return strings.TrimSpace(string(out)), nil
}

// firstError is the first line of go build output that is not a "# package"
// header.
func firstError(out []byte) string {
	for line := range strings.Lines(string(out)) {
		if !strings.HasPrefix(line, "#") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
