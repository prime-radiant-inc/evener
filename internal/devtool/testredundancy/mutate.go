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
	return loadJSON[[]Mutation](path)
}

// LoadMutationResults reads the results WriteMutationResults wrote.
func LoadMutationResults(path string) ([]MutationResult, error) {
	return loadJSON[[]MutationResult](path)
}

// WriteMutationResults writes results as the JSON proof reads.
func WriteMutationResults(path string, results []MutationResult) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func loadJSON[T any](path string) (T, error) {
	var v T
	data, err := os.ReadFile(path)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}

// Mutate applies each mutation alone, runs the tests it selects, and restores
// the file with a file-scoped git restore before the next. Restoring discards
// uncommitted work, so it refuses a mutation of a file git reports modified.
func (r *Runner) Mutate(ctx context.Context, muts []Mutation) ([]MutationResult, error) {
	bin := filepath.Join(r.cacheDir, "build", "mutant.test")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return nil, err
	}
	var results []MutationResult
	for _, m := range muts {
		res, err := r.mutateOne(ctx, bin, m)
		if err != nil {
			return results, fmt.Errorf("mutation %s: %w", m.Name, err)
		}
		_, _ = fmt.Fprintf(r.log, "%s: %s %s\n", m.Name, res.Outcome, strings.Join(res.Failed, " "))
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

	if berr := r.goTestBuild(ctx, bin); berr != nil {
		if !errors.Is(berr, errBuild) {
			return res, berr
		}
		res.Detail = berr.Error()
		return res, nil
	}
	run, err := r.runBinary(ctx, bin, "", testTimeout, "-test.run="+m.Run)
	if err != nil {
		return res, err
	}
	switch {
	case run.exitErr:
		res.Outcome = OutcomeCaught
		res.Failed = run.failed
	case run.noTests:
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
