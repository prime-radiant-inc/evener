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

	"primeradiant.com/evener/internal/devtool/procgroup"
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

const (
	testTimeout = 10 * time.Minute // one test alone, or one mutation's tests
	restTimeout = 90 * time.Minute // the whole package in one process
)

// Runner profiles one package's tests through a single cover-instrumented test
// binary, caching every profile under its cache directory by the binary's
// content hash: an unchanged package reuses its profiles across invocations
// and worktrees.
type Runner struct {
	pkgDir     string // the package directory as the caller named it
	absDir     string
	importPath string
	cacheDir   string
	log        io.Writer // one progress line per test run
}

// testBinary is one build of the package's cover test binary and the cache
// directory its content names.
type testBinary struct {
	path, hashDir string
}

// NewRunner resolves pkgDir's import path, which coverage profiles name its
// files by. An empty cacheDir means evener-test-redundancy under GOCACHE, beside
// the other dev tool caches.
func NewRunner(ctx context.Context, pkgDir, cacheDir string, log io.Writer) (*Runner, error) {
	absDir, err := filepath.Abs(pkgDir)
	if err != nil {
		return nil, err
	}
	importPath, err := goOutput(ctx, absDir, "list", "-f", "{{.ImportPath}}", ".")
	if err != nil {
		return nil, err
	}
	if cacheDir == "" {
		gocache, err := goOutput(ctx, absDir, "env", "GOCACHE")
		if err != nil {
			return nil, err
		}
		cacheDir = filepath.Join(gocache, "evener-test-redundancy")
	}
	return &Runner{pkgDir: pkgDir, absDir: absDir, importPath: importPath, cacheDir: cacheDir, log: log}, nil
}

func goOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go %s in %s: %w%s", strings.Join(args, " "), dir, err, stderrOf(err))
	}
	return strings.TrimSpace(string(out)), nil
}

// DisplayFile maps a profile's import-path file name to the source file under
// the package directory. A file from another package keeps its import-path
// name.
func (r *Runner) DisplayFile(profileFile string) string {
	if base, ok := strings.CutPrefix(profileFile, r.importPath+"/"); ok && !strings.Contains(base, "/") {
		return filepath.Join(r.pkgDir, base)
	}
	return profileFile
}

// build compiles the package's cover test binary and hashes it.
func (r *Runner) build(ctx context.Context) (testBinary, error) {
	buildDir := filepath.Join(r.cacheDir, "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return testBinary{}, err
	}
	bin := filepath.Join(buildDir, filepath.Base(r.absDir)+".test")
	if err := r.goTestBuild(ctx, bin, "-cover"); err != nil {
		return testBinary{}, err
	}
	f, err := os.Open(bin)
	if err != nil {
		return testBinary{}, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return testBinary{}, err
	}
	hashDir := filepath.Join(r.cacheDir, hex.EncodeToString(h.Sum(nil)[:8]))
	if err := os.MkdirAll(filepath.Join(hashDir, "tests"), 0o755); err != nil {
		return testBinary{}, err
	}
	return testBinary{path: bin, hashDir: hashDir}, nil
}

// errBuild marks a package that does not compile.
var errBuild = errors.New("build failed")

func (r *Runner) goTestBuild(ctx context.Context, bin string, flags ...string) error {
	cmd := exec.CommandContext(ctx, "go", append(append([]string{"test", "-c"}, flags...), "-o", bin, ".")...)
	cmd.Dir = r.absDir
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s: %s", errBuild, r.pkgDir, firstError(out))
	}
	return nil
}

// binaryRun is what one run of the test binary showed.
type binaryRun struct {
	out     []byte
	failed  []string // top-level tests reported as failed
	exitErr bool     // the binary exited nonzero
	noTests bool     // -test.run matched nothing
}

// noTestsRun is the testing package's report that -test.run matched nothing.
var noTestsRun = []byte("testing: warning: no tests to run")

// failLine matches the testing package's report of a failed top-level test.
var failLine = regexp.MustCompile(`(?m)^--- FAIL: (\S+)`)

// runBinary runs the test binary in the package directory, in its own process
// group so cancelling stops anything a test started. With a profile path it
// writes the coverage profile to a unique temporary name and renames it into
// place only after the binary exits, so an interrupted run, or a concurrent
// one sharing the cache, never sees a partial profile.
func (r *Runner) runBinary(ctx context.Context, bin, profile string, timeout time.Duration, args ...string) (binaryRun, error) {
	args = append([]string{"-test.count=1", "-test.timeout=" + timeout.String()}, args...)
	var tmp string
	if profile != "" {
		f, err := os.CreateTemp(filepath.Dir(profile), filepath.Base(profile)+".partial-*")
		if err != nil {
			return binaryRun{}, err
		}
		tmp = f.Name()
		_ = f.Close()
		defer func() { _ = os.Remove(tmp) }()
		args = append(args, "-test.coverprofile="+tmp)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = r.absDir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Cancel = func() error {
		procgroup.Kill(cmd.Process.Pid)
		return nil
	}
	// A process a test left holding the output pipe cannot hold Wait open.
	cmd.WaitDelay = 10 * time.Second
	if err := procgroup.Start(cmd); err != nil {
		return binaryRun{}, err
	}
	runErr := cmd.Wait()
	if ctx.Err() != nil {
		return binaryRun{}, ctx.Err()
	}
	var exit *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exit) {
		return binaryRun{}, runErr
	}
	run := binaryRun{out: out.Bytes(), exitErr: runErr != nil, noTests: bytes.Contains(out.Bytes(), noTestsRun)}
	for _, m := range failLine.FindAllSubmatch(run.out, -1) {
		run.failed = append(run.failed, string(m[1]))
	}
	if profile != "" {
		if err := os.Rename(tmp, profile); err != nil {
			return binaryRun{}, err
		}
	}
	return run, nil
}

// ProfileTests runs each named top-level test alone, one at a time, and caches
// its coverage profile. A test whose profile the same binary already produced
// is not run again.
func (r *Runner) ProfileTests(ctx context.Context, names []string) ([]TestRun, error) {
	bin, err := r.build(ctx)
	if err != nil {
		return nil, err
	}
	return r.profileTests(ctx, bin, names)
}

func (r *Runner) profileTests(ctx context.Context, bin testBinary, names []string) ([]TestRun, error) {
	runs := make([]TestRun, 0, len(names))
	for i, name := range names {
		profile := filepath.Join(bin.hashDir, "tests", name+".cov")
		if _, err := os.Stat(profile); err == nil {
			runs = append(runs, TestRun{Name: name, Status: StatusCached, Profile: profile})
			continue
		}
		start := time.Now()
		staged := profile + ".staged"
		res, err := r.runBinary(ctx, bin.path, staged, testTimeout, "-test.run=^"+regexp.QuoteMeta(name)+"$")
		if err != nil {
			return runs, fmt.Errorf("%s: %w", name, err)
		}
		var run TestRun
		switch {
		case res.exitErr:
			_ = os.Remove(staged)
			run = TestRun{Name: name, Status: StatusFailed, Output: string(res.out)}
		case res.noTests:
			_ = os.Remove(staged)
			run = TestRun{Name: name, Status: StatusMissing}
		default:
			if err := os.Rename(staged, profile); err != nil {
				return runs, err
			}
			run = TestRun{Name: name, Status: StatusRan, Profile: profile}
		}
		_, _ = fmt.Fprintf(r.log, "[%d/%d] %s %s %.1fs\n", i+1, len(names), name, run.Status, time.Since(start).Seconds())
		runs = append(runs, run)
	}
	return runs, nil
}

// ProfileRest runs the whole package except the skipped tests in one process
// and caches the profile by the binary and the skip set. With no skips it is
// the package's whole-suite profile.
func (r *Runner) ProfileRest(ctx context.Context, skip []string) (RestRun, error) {
	bin, err := r.build(ctx)
	if err != nil {
		return RestRun{}, err
	}
	return r.profileRest(ctx, bin, skip)
}

func (r *Runner) profileRest(ctx context.Context, bin testBinary, skip []string) (RestRun, error) {
	sorted := slices.Sorted(slices.Values(skip))
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	profile := filepath.Join(bin.hashDir, "rest-"+hex.EncodeToString(sum[:8])+".cov")
	failedFile := profile + ".failed"
	// The profile is renamed into place after its failure list is written, so
	// a cached profile always has one.
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
	staged := profile + ".staged"
	res, err := r.runBinary(ctx, bin.path, staged, restTimeout, args...)
	if err != nil {
		return RestRun{}, err
	}
	if err := os.WriteFile(failedFile, []byte(strings.Join(res.failed, "\n")), 0o644); err != nil {
		return RestRun{}, err
	}
	if err := os.Rename(staged, profile); err != nil {
		return RestRun{}, err
	}
	_, _ = fmt.Fprintf(r.log, "package run skipping %d tests: %d failed, %.0fs\n", len(sorted), len(res.failed), time.Since(start).Seconds())
	return RestRun{Profile: profile, Failed: res.failed}, nil
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
// then reports each candidate's unique statements and assertion count. A
// candidate that cannot be profiled is listed in Failed and left out of the
// comparison.
func (r *Runner) Unique(ctx context.Context, names []string) (UniqueReport, error) {
	bin, err := r.build(ctx)
	if err != nil {
		return UniqueReport{}, err
	}
	cands, failed, err := r.candidateProfiles(ctx, bin, names)
	if err != nil {
		return UniqueReport{}, err
	}
	rest, err := r.profileRest(ctx, bin, names)
	if err != nil {
		return UniqueReport{}, err
	}
	restProfile, err := LoadProfile(rest.Profile)
	if err != nil {
		return UniqueReport{}, err
	}
	asserts, err := Assertions(r.absDir)
	if err != nil {
		return UniqueReport{}, err
	}
	unique := UniqueCoverage(cands, restProfile)
	for i := range unique {
		unique[i].Assertions = len(asserts[unique[i].Name])
	}
	return UniqueReport{Unique: unique, Failed: failed, Rest: rest}, nil
}

// Pairs profiles each candidate alone and ranks the pairs among them.
func (r *Runner) Pairs(ctx context.Context, names []string) ([]Pair, []TestRun, error) {
	bin, err := r.build(ctx)
	if err != nil {
		return nil, nil, err
	}
	cands, failed, err := r.candidateProfiles(ctx, bin, names)
	if err != nil {
		return nil, nil, err
	}
	asserts, err := Assertions(r.absDir)
	if err != nil {
		return nil, nil, err
	}
	return RankPairs(cands, asserts), failed, nil
}

func (r *Runner) candidateProfiles(ctx context.Context, bin testBinary, names []string) (map[string]Profile, []TestRun, error) {
	runs, err := r.profileTests(ctx, bin, names)
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
