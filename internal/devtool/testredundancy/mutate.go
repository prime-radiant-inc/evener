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
// uncommitted work and cannot bring back an untracked file, so it refuses a
// mutation of a file git does not track or reports modified. Each mutation's
// tests first run on the unmutated source: a failure there would read as a
// catch, so the mutation is invalid instead.
func (r *Runner) Mutate(ctx context.Context, muts []Mutation) ([]MutationResult, error) {
	for _, m := range muts {
		if err := r.checkRestorable(ctx, m.File); err != nil {
			return nil, fmt.Errorf("mutation %s: %w", m.Name, err)
		}
	}
	clean, err := r.newBinaryPath("unmutated")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(clean) }()
	if err := r.goTestBuild(ctx, clean); err != nil {
		return nil, err
	}
	mutant, err := r.newBinaryPath("mutant")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(mutant) }()

	baseline := map[string]string{} // run pattern -> why it cannot judge a mutation
	var results []MutationResult
	for _, m := range muts {
		if _, seen := baseline[m.Run]; !seen {
			if baseline[m.Run], err = r.baselineProblem(ctx, clean, m.Run); err != nil {
				return results, err
			}
		}
		res := MutationResult{Mutation: m, Outcome: OutcomeInvalid, Detail: baseline[m.Run]}
		if res.Detail == "" {
			if res, err = r.mutateOne(ctx, mutant, m); err != nil {
				return results, fmt.Errorf("mutation %s: %w", m.Name, err)
			}
		}
		_, _ = fmt.Fprintf(r.log, "%s: %s %s\n", m.Name, res.Outcome, strings.Join(res.Failed, " "))
		results = append(results, res)
	}
	return results, nil
}

// ErrNotRestorable marks a mutation target that a file-scoped git restore
// could not put back as it is.
var ErrNotRestorable = errors.New("git restore cannot undo a mutation of this file")

// checkRestorable refuses a file git restore could not put back as it is: an
// untracked or ignored file, a modified one, or a symlink (restore puts back
// the link, not the file it points to).
func (r *Runner) checkRestorable(ctx context.Context, file string) error {
	path := filepath.Join(r.absDir, file)
	if _, err := r.git(ctx, "ls-files", "--error-unmatch", "--", path); err != nil {
		return fmt.Errorf("%s is not tracked by git: %w", file, ErrNotRestorable)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file: %w", file, ErrNotRestorable)
	}
	status, err := r.git(ctx, "status", "--porcelain", "--", path)
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("%s has uncommitted changes: %w", file, ErrNotRestorable)
	}
	return nil
}

// baselineProblem runs the tests pattern selects on the unmutated binary and
// says why they cannot judge a mutation, or "" when they can.
func (r *Runner) baselineProblem(ctx context.Context, bin, pattern string) (string, error) {
	if pattern == "" {
		return "an empty run pattern selects every test", nil
	}
	run, err := r.runBinary(ctx, bin, false, r.testTimeout, "-test.run="+pattern)
	switch {
	case err != nil:
		return "", err
	case run.noTests:
		return "no test matches " + pattern, nil
	case run.exitErr:
		return "the selected tests fail without the mutation: " + strings.Join(run.failed, " "), nil
	}
	return "", nil
}

func (r *Runner) mutateOne(ctx context.Context, bin string, m Mutation) (res MutationResult, err error) {
	res = MutationResult{Mutation: m, Outcome: OutcomeInvalid}
	path := filepath.Join(r.absDir, m.File)
	if err := r.checkRestorable(ctx, m.File); err != nil {
		return res, err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return res, err
	}
	if n := bytes.Count(src, []byte(m.Old)); n != 1 {
		res.Detail = fmt.Sprintf("old text occurs %d times", n)
		return res, nil
	}
	// The restore is registered before the write, so a write that truncates
	// the file and then fails is restored too, and it runs even when ctx is
	// cancelled mid-mutation.
	defer func() {
		if _, rerr := r.git(context.WithoutCancel(ctx), "restore", "--", path); rerr != nil {
			err = errors.Join(err, fmt.Errorf("restoring %s: %w", m.File, rerr))
		}
	}()
	if err := os.WriteFile(path, bytes.Replace(src, []byte(m.Old), []byte(m.New), 1), 0o644); err != nil {
		return res, err
	}

	if berr := r.goTestBuild(ctx, bin); berr != nil {
		if !errors.Is(berr, errBuild) {
			return res, berr
		}
		res.Detail = berr.Error()
		return res, nil
	}
	run, err := r.runBinary(ctx, bin, false, r.testTimeout, "-test.run="+m.Run)
	if err != nil {
		return res, err
	}
	res.Outcome = OutcomeSurvived
	if run.exitErr {
		res.Outcome = OutcomeCaught
		res.Failed = run.failed
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
