package testredundancy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Status is what profiling one test did.
type Status string

const (
	StatusRan     Status = "ran"     // ran now; its profile is cached
	StatusCached  Status = "cached"  // a profile from the same test binary existed
	StatusFailed  Status = "failed"  // the test failed; nothing is cached
	StatusMissing Status = "missing" // the name matches no test in the package
)

// TestRun is one test's profiling result. Output is the test binary's output
// for a failed run.
type TestRun struct {
	Name    string
	Status  Status
	Profile string
	Output  string
}

// RestRun is a profile of the whole package minus some tests. Failed names the
// tests that failed in it; their partial coverage is still in the profile.
type RestRun struct {
	Profile string
	Failed  []string
}

// Runner profiles one package's tests through a single cover-instrumented test
// binary, caching every profile under CacheDir by the binary's content hash:
// an unchanged package reuses its profiles across invocations and worktrees.
type Runner struct {
	PkgDir      string // the package directory as the caller named it
	CacheDir    string
	Log         io.Writer // one progress line per test run
	TestTimeout time.Duration
	RestTimeout time.Duration

	absDir     string
	importPath string
}

// NewRunner resolves pkgDir's import path, which coverage profiles name its
// files by.
func NewRunner(ctx context.Context, pkgDir, cacheDir string, log io.Writer) (*Runner, error) {
	absDir, err := filepath.Abs(pkgDir)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "go", "list", "-f", "{{.ImportPath}}", ".")
	cmd.Dir = absDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list in %s: %w%s", pkgDir, err, stderrOf(err))
	}
	return &Runner{
		PkgDir:      pkgDir,
		CacheDir:    cacheDir,
		Log:         log,
		TestTimeout: 10 * time.Minute,
		RestTimeout: 90 * time.Minute,
		absDir:      absDir,
		importPath:  strings.TrimSpace(string(out)),
	}, nil
}

// DisplayFile maps a profile's import-path file name to the source file under
// PkgDir. A file from another package keeps its import-path name.
func (r *Runner) DisplayFile(profileFile string) string {
	if base, ok := strings.CutPrefix(profileFile, r.importPath+"/"); ok && !strings.Contains(base, "/") {
		return filepath.Join(r.PkgDir, base)
	}
	return profileFile
}

// build compiles the package's cover test binary and returns it with the cache
// directory its content hash names.
func (r *Runner) build(ctx context.Context) (bin, hashDir string, err error) {
	buildDir := filepath.Join(r.CacheDir, "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return "", "", err
	}
	bin = filepath.Join(buildDir, filepath.Base(r.absDir)+".test")
	cmd := exec.CommandContext(ctx, "go", "test", "-c", "-cover", "-o", bin, ".")
	cmd.Dir = r.absDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("building %s's test binary: %w\n%s", r.PkgDir, err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	hashDir = filepath.Join(r.CacheDir, hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(filepath.Join(hashDir, "tests"), 0o755); err != nil {
		return "", "", err
	}
	return bin, hashDir, nil
}

// noTestsRun is the testing package's report that -test.run matched nothing.
var noTestsRun = []byte("testing: warning: no tests to run")

// failLine matches the testing package's report of a failed top-level test.
var failLine = regexp.MustCompile(`(?m)^--- FAIL: (\S+)`)

// runBinary runs the test binary in the package directory, writing the profile
// to a temporary name and renaming it into place only after the binary exits,
// so an interrupted run never leaves a partial profile in the cache.
func (r *Runner) runBinary(ctx context.Context, bin, profile string, timeout time.Duration, args ...string) (out []byte, exitErr error, err error) {
	tmp := profile + ".partial"
	args = append([]string{"-test.count=1", "-test.timeout=" + timeout.String(), "-test.coverprofile=" + tmp}, args...)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = r.absDir
	out, runErr := cmd.CombinedOutput()
	if ctx.Err() != nil {
		_ = os.Remove(tmp)
		return out, nil, ctx.Err()
	}
	var exit *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exit) {
		return out, nil, runErr
	}
	if err := os.Rename(tmp, profile); err != nil {
		return out, runErr, fmt.Errorf("test binary wrote no profile: %w\n%s", err, out)
	}
	return out, runErr, nil
}

// ProfileTests runs each named top-level test alone, one at a time, and caches
// its coverage profile. A test whose profile the same binary already produced
// is not run again.
func (r *Runner) ProfileTests(ctx context.Context, names []string) ([]TestRun, error) {
	bin, hashDir, err := r.build(ctx)
	if err != nil {
		return nil, err
	}
	runs := make([]TestRun, 0, len(names))
	for i, name := range names {
		profile := filepath.Join(hashDir, "tests", name+".cov")
		if _, err := os.Stat(profile); err == nil {
			runs = append(runs, TestRun{Name: name, Status: StatusCached, Profile: profile})
			continue
		}
		start := time.Now()
		out, exitErr, err := r.runBinary(ctx, bin, profile, r.TestTimeout, "-test.run=^"+regexp.QuoteMeta(name)+"$")
		if err != nil {
			return runs, fmt.Errorf("%s: %w", name, err)
		}
		run := TestRun{Name: name, Status: StatusRan, Profile: profile}
		switch {
		case exitErr != nil:
			_ = os.Remove(profile)
			run = TestRun{Name: name, Status: StatusFailed, Output: string(out)}
		case bytes.Contains(out, noTestsRun):
			_ = os.Remove(profile)
			run = TestRun{Name: name, Status: StatusMissing}
		}
		_, _ = fmt.Fprintf(r.Log, "[%d/%d] %s %s %.1fs\n", i+1, len(names), name, run.Status, time.Since(start).Seconds())
		runs = append(runs, run)
	}
	return runs, nil
}

// ProfileRest runs the whole package except the skipped tests in one process
// and caches the profile by the binary and the skip set. With no skips it is
// the package's whole-suite profile.
func (r *Runner) ProfileRest(ctx context.Context, skip []string) (RestRun, error) {
	bin, hashDir, err := r.build(ctx)
	if err != nil {
		return RestRun{}, err
	}
	sorted := slices.Sorted(slices.Values(skip))
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	profile := filepath.Join(hashDir, "rest-"+hex.EncodeToString(sum[:8])+".cov")
	failedFile := profile + ".failed"
	if _, err := os.Stat(profile); err == nil {
		failed, err := os.ReadFile(failedFile)
		if err != nil {
			return RestRun{}, err
		}
		return RestRun{Profile: profile, Failed: strings.Fields(string(failed))}, nil
	}
	var args []string
	if len(sorted) > 0 {
		quoted := make([]string, len(sorted))
		for i, name := range sorted {
			quoted[i] = regexp.QuoteMeta(name)
		}
		args = append(args, "-test.skip=^("+strings.Join(quoted, "|")+")$")
	}
	start := time.Now()
	out, _, err := r.runBinary(ctx, bin, profile, r.RestTimeout, args...)
	if err != nil {
		return RestRun{}, err
	}
	var failed []string
	for _, m := range failLine.FindAllSubmatch(out, -1) {
		failed = append(failed, string(m[1]))
	}
	if err := os.WriteFile(failedFile, []byte(strings.Join(failed, "\n")), 0o644); err != nil {
		return RestRun{}, err
	}
	_, _ = fmt.Fprintf(r.Log, "package run skipping %d tests: %d failed, %.0fs\n", len(sorted), len(failed), time.Since(start).Seconds())
	return RestRun{Profile: profile, Failed: failed}, nil
}

// UniqueReport is the evidence for deleting candidates: what each reaches that
// nothing else in the package does, the candidates that could not be
// profiled, and the run of everything else.
type UniqueReport struct {
	Unique []Unique
	Failed []TestRun // candidates that failed or matched no test
	Rest   RestRun
}

// Unique profiles each candidate alone and the package without the candidates,
// then reports each candidate's unique statements. A candidate that cannot be
// profiled is listed in Failed and left out of the comparison.
func (r *Runner) Unique(ctx context.Context, names []string) (UniqueReport, error) {
	cands, failed, err := r.candidateProfiles(ctx, names)
	if err != nil {
		return UniqueReport{}, err
	}
	rest, err := r.ProfileRest(ctx, names)
	if err != nil {
		return UniqueReport{}, err
	}
	restProfile, err := LoadProfile(rest.Profile)
	if err != nil {
		return UniqueReport{}, err
	}
	return UniqueReport{Unique: UniqueCoverage(cands, restProfile), Failed: failed, Rest: rest}, nil
}

// Pairs profiles each candidate alone and ranks the pairs among them.
func (r *Runner) Pairs(ctx context.Context, names []string) ([]Pair, []TestRun, error) {
	cands, failed, err := r.candidateProfiles(ctx, names)
	if err != nil {
		return nil, nil, err
	}
	asserts, err := Assertions(r.absDir)
	if err != nil {
		return nil, nil, err
	}
	return RankPairs(cands, asserts), failed, nil
}

func (r *Runner) candidateProfiles(ctx context.Context, names []string) (map[string]Profile, []TestRun, error) {
	runs, err := r.ProfileTests(ctx, names)
	if err != nil {
		return nil, nil, err
	}
	cands := map[string]Profile{}
	var failed []TestRun
	for _, run := range runs {
		if run.Status != StatusRan && run.Status != StatusCached {
			failed = append(failed, run)
			continue
		}
		p, err := LoadProfile(run.Profile)
		if err != nil {
			return nil, nil, err
		}
		cands[run.Name] = p
	}
	return cands, failed, nil
}

func stderrOf(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return "\n" + string(exit.Stderr)
	}
	return ""
}
