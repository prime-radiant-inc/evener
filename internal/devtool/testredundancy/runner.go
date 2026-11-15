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

	"primeradiant.com/evener/execsupport/fsatomic"
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
	defaultTestTimeout = 10 * time.Minute // one test alone, or one mutation's tests
	defaultRestTimeout = 90 * time.Minute // the whole package in one process
)

// maxRestArgBytes bounds one argument handed to a test binary. Linux rejects a
// single argument longer than MAX_ARG_STRLEN (128 KiB) with E2BIG, and macOS
// has its own per-argument bound; half of Linux's limit leaves room for the
// flag name and a chunk's separators. A skip set larger than this is split
// over several -test.run processes instead of one -test.skip argument.
const maxRestArgBytes = 64 << 10

const (
	testSkipPrefix = "-test.skip=^("
	testRunPrefix  = "-test.run=^("
	testArgSuffix  = ")$"
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

	testTimeout, restTimeout time.Duration
	restArgLimit             int // max bytes of one test-binary argument
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
	// Subprocesses run in the package directory, so every path handed to them
	// must be absolute.
	if cacheDir, err = filepath.Abs(cacheDir); err != nil {
		return nil, err
	}
	return &Runner{
		pkgDir: pkgDir, absDir: absDir, importPath: importPath, cacheDir: cacheDir, log: log,
		testTimeout: defaultTestTimeout, restTimeout: defaultRestTimeout,
		restArgLimit: maxRestArgBytes,
	}, nil
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

// build compiles the package's cover test binary and hashes it. Each build
// gets its own file, so runs sharing the cache never run each other's binary;
// the caller removes it.
func (r *Runner) build(ctx context.Context) (testBinary, error) {
	bin, err := r.newBinaryPath(filepath.Base(r.absDir))
	if err != nil {
		return testBinary{}, err
	}
	if err := r.goTestBuild(ctx, bin, "-cover"); err != nil {
		_ = os.Remove(bin)
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

func (b testBinary) remove() { _ = os.Remove(b.path) }

// newBinaryPath reserves a unique file in the cache's build directory.
func (r *Runner) newBinaryPath(name string) (string, error) {
	buildDir := filepath.Join(r.cacheDir, "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(buildDir, name+"-*.test")
	if err != nil {
		return "", err
	}
	return f.Name(), f.Close()
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
	out      []byte
	failed   []string // top-level tests reported as failed
	exitErr  bool     // the binary exited nonzero
	noTests  bool     // -test.run matched nothing
	cutShort bool     // a panic, timeout or hang ended the binary, or it wrote no profile
	profile  string   // the coverage profile it wrote, which the caller owns
}

// crashed reports a run that ended before its tests finished: a panic, a
// timeout or hang, an exit before writing its profile, or a nonzero exit with
// no failed test to account for it. Its profile is missing or partial.
func (b binaryRun) crashed() bool {
	return b.cutShort || (b.exitErr && len(b.failed) == 0)
}

// noTestsRun is the testing package's report that -test.run matched nothing.
var noTestsRun = []byte("testing: warning: no tests to run")

// failLine matches the testing package's report of a failed top-level test.
var failLine = regexp.MustCompile(`(?m)^--- FAIL: (\S+)`)

// panicLine matches the runtime's report of a panic, including the testing
// package's -test.timeout panic.
var panicLine = regexp.MustCompile(`(?m)^panic: `)

// waitDelay bounds how long a process a test left behind can hold the output
// pipe open after the binary exits.
const waitDelay = 3 * time.Second

// runBinary runs the test binary in the package directory, in its own process
// group, and kills that group when the binary is done or cancelled, so nothing
// a test started outlives it. With withProfile it writes the coverage profile
// to a unique temporary file that the caller renames into the cache or
// removes: a concurrent run sharing the cache never sees a partial profile.
func (r *Runner) runBinary(ctx context.Context, bin string, withProfile bool, timeout time.Duration, args ...string) (binaryRun, error) {
	args = append([]string{"-test.count=1", "-test.timeout=" + timeout.String()}, args...)
	var run binaryRun
	if withProfile {
		f, err := os.CreateTemp(filepath.Dir(bin), "profile-*.cov")
		if err != nil {
			return binaryRun{}, err
		}
		run.profile = f.Name()
		_ = f.Close()
		args = append(args, "-test.coverprofile="+run.profile)
	}
	fail := func(err error) (binaryRun, error) {
		if run.profile != "" {
			_ = os.Remove(run.profile)
		}
		return binaryRun{}, err
	}
	// The testing package's timeout cannot fire before tests start, so a hang
	// in init or TestMain is bounded here, after giving that timeout its turn.
	runCtx, cancel := context.WithTimeout(ctx, timeout+timeout/10+time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = r.absDir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Cancel = func() error {
		procgroup.Kill(cmd.Process.Pid)
		return nil
	}
	cmd.WaitDelay = waitDelay
	if err := procgroup.Start(cmd); err != nil {
		return fail(err)
	}
	runErr := cmd.Wait()
	procgroup.Kill(cmd.Process.Pid)
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	var exit *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exit) && !errors.Is(runErr, exec.ErrWaitDelay) {
		return fail(runErr)
	}
	run.out = out.Bytes()
	run.exitErr = exit != nil
	run.noTests = bytes.Contains(run.out, noTestsRun)
	run.cutShort = panicLine.Match(run.out) || runCtx.Err() != nil
	if withProfile && !run.noTests && !profileWritten(run.profile) {
		run.cutShort = true
	}
	for _, m := range failLine.FindAllSubmatch(run.out, -1) {
		run.failed = append(run.failed, string(m[1]))
	}
	return run, nil
}

// profileWritten reports whether the binary wrote a coverage profile: the file
// starts with its mode header. A binary that exits before its tests leaves the
// empty placeholder.
func profileWritten(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, len("mode:"))
	_, err = io.ReadFull(f, head)
	return err == nil && string(head) == "mode:"
}

// tail is the last n lines of out.
func tail(out []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// ProfileTests runs each named top-level test alone, one at a time, and caches
// its coverage profile. A test whose profile the same binary already produced
// is not run again.
func (r *Runner) ProfileTests(ctx context.Context, names []string) ([]TestRun, error) {
	bin, err := r.build(ctx)
	if err != nil {
		return nil, err
	}
	defer bin.remove()
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
		res, err := r.runBinary(ctx, bin.path, true, r.testTimeout, "-test.run=^"+regexp.QuoteMeta(name)+"$")
		if err != nil {
			return runs, fmt.Errorf("%s: %w", name, err)
		}
		var run TestRun
		switch {
		case res.exitErr || res.crashed():
			run = TestRun{Name: name, Status: StatusFailed, Output: string(res.out)}
		case res.noTests:
			run = TestRun{Name: name, Status: StatusMissing}
		default:
			if err := os.Rename(res.profile, profile); err != nil {
				return runs, err
			}
			run = TestRun{Name: name, Status: StatusRan, Profile: profile}
		}
		_ = os.Remove(res.profile)
		_, _ = fmt.Fprintf(r.log, "[%d/%d] %s %s %.1fs\n", i+1, len(names), name, run.Status, time.Since(start).Seconds())
		runs = append(runs, run)
	}
	return runs, nil
}

// ProfileRest runs the whole package except the skipped tests and caches the
// profile by the binary and the skip set. With no skips it is the package's
// whole-suite profile. A skip set too large for one -test.skip argument is
// split over several -test.run processes whose profiles are concatenated.
func (r *Runner) ProfileRest(ctx context.Context, skip []string) (RestRun, error) {
	bin, err := r.build(ctx)
	if err != nil {
		return RestRun{}, err
	}
	defer bin.remove()
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
	start := time.Now()
	var (
		merged string
		failed []string
		runs   int
		err    error
	)
	var skipArg string
	if len(sorted) > 0 {
		skipArg = testSkipArg(sorted)
	}
	if len(skipArg) > r.restArgLimit {
		merged, failed, runs, err = r.runRestChunks(ctx, bin, sorted)
		if err != nil {
			return RestRun{}, err
		}
		defer func() { _ = os.Remove(merged) }()
	} else {
		var args []string
		if skipArg != "" {
			args = append(args, skipArg)
		}
		res, err := r.runRestOnce(ctx, bin.path, true, r.restTimeout, args...)
		if err != nil {
			return RestRun{}, err
		}
		merged, failed, runs = res.profile, res.failed, 1
		defer func() { _ = os.Remove(res.profile) }()
	}
	if err := fsatomic.WriteWithoutSync(failedFile, []byte(strings.Join(failed, "\n")), 0o644); err != nil {
		return RestRun{}, err
	}
	if err := os.Rename(merged, profile); err != nil {
		return RestRun{}, err
	}
	_, _ = fmt.Fprintf(r.log, "package run skipping %d tests across %d process(es): %d failed, %.0fs\n", len(sorted), runs, len(failed), time.Since(start).Seconds())
	return RestRun{Profile: profile, Failed: failed}, nil
}

// testSkipArg is the one-argument skip regex for names.
func testSkipArg(names []string) string {
	return testSkipPrefix + joinedQuoted(names) + testArgSuffix
}

// testRunArg is the one-argument run regex for names.
func testRunArg(names []string) string {
	return testRunPrefix + joinedQuoted(names) + testArgSuffix
}

func joinedQuoted(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = regexp.QuoteMeta(name)
	}
	return strings.Join(quoted, "|")
}

// runRestChunks runs the package in several processes when one -test.skip
// argument would exceed restArgLimit. It lists the runnable names, removes the
// skipped set, and passes each chunk of the remainder through -test.run,
// concatenating the coverage profiles. The chunks partition the included
// tests, so their union is exactly the profile of the package without the
// skipped tests: covstmt reads a concatenation as an any-hit union.
func (r *Runner) runRestChunks(ctx context.Context, bin testBinary, skip []string) (string, []string, int, error) {
	start := time.Now()
	timeLeft := func() (time.Duration, error) {
		left := r.restTimeout - time.Since(start)
		if left <= 0 {
			return 0, fmt.Errorf("the chunked rest run used its %s budget before finishing", r.restTimeout)
		}
		return left, nil
	}
	left, err := timeLeft()
	if err != nil {
		return "", nil, 0, err
	}
	all, err := r.listRunnable(ctx, bin.path, left)
	if err != nil {
		return "", nil, 0, err
	}
	skipped := make(map[string]struct{}, len(skip))
	for _, name := range skip {
		skipped[name] = struct{}{}
	}
	remaining := make([]string, 0, len(all))
	for _, name := range all {
		if _, ok := skipped[name]; !ok {
			remaining = append(remaining, name)
		}
	}
	if len(remaining) == 0 {
		// Every runnable name is skipped. A small skip-everything regex still
		// runs the package's initialization, whose profile belongs to the rest.
		left, err := timeLeft()
		if err != nil {
			return "", nil, 0, err
		}
		res, err := r.runRestOnce(ctx, bin.path, true, left, "-test.skip=.*")
		if err != nil {
			return "", nil, 0, err
		}
		return res.profile, res.failed, 1, nil
	}
	chunks, err := chunkTestNames(remaining, r.restArgLimit)
	if err != nil {
		return "", nil, 0, err
	}
	merged, err := os.CreateTemp(bin.hashDir, "rest-chunks-*.cov")
	if err != nil {
		return "", nil, 0, err
	}
	fail := func(err error) (string, []string, int, error) {
		_ = merged.Close()
		_ = os.Remove(merged.Name())
		return "", nil, 0, err
	}
	var failed []string
	for _, chunk := range chunks {
		left, err := timeLeft()
		if err != nil {
			return fail(err)
		}
		res, err := r.runRestOnce(ctx, bin.path, true, left, testRunArg(chunk))
		if err != nil {
			return fail(err)
		}
		if err := appendProfile(merged, res.profile); err != nil {
			_ = os.Remove(res.profile)
			return fail(err)
		}
		_ = os.Remove(res.profile)
		// The chunks partition the included tests, so one name can fail in
		// only one of them.
		failed = append(failed, res.failed...)
	}
	if err := merged.Close(); err != nil {
		_ = os.Remove(merged.Name())
		return "", nil, 0, err
	}
	return merged.Name(), failed, len(chunks), nil
}

// runRestOnce runs the test binary once and refuses a run that crashed before
// finishing: its profile is missing or partial.
func (r *Runner) runRestOnce(ctx context.Context, bin string, withProfile bool, timeout time.Duration, args ...string) (binaryRun, error) {
	res, err := r.runBinary(ctx, bin, withProfile, timeout, args...)
	if err != nil {
		return binaryRun{}, err
	}
	if res.crashed() {
		if res.profile != "" {
			_ = os.Remove(res.profile)
		}
		return binaryRun{}, fmt.Errorf("the package run crashed (a panic, timeout or exit), so its profile is incomplete:\n%s", tail(res.out, 40))
	}
	return res, nil
}

// appendProfile appends src's coverage profile to dst. covstmt reads the
// concatenation as an any-hit union, so the profile files need only a newline
// between them.
func appendProfile(dst *os.File, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(dst, f); err != nil {
		return err
	}
	_, err = dst.WriteString("\n")
	return err
}

// listRunnable lists the names -test.run and -test.skip match: tests, examples
// and fuzz targets. Benchmarks are left out because the rest run never enables
// them.
func (r *Runner) listRunnable(ctx context.Context, bin string, timeout time.Duration) ([]string, error) {
	res, err := r.runRestOnce(ctx, bin, false, timeout, "-test.list=.*")
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range strings.Lines(string(res.out)) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Test") || strings.HasPrefix(line, "Example") || strings.HasPrefix(line, "Fuzz") {
			names = append(names, line)
		}
	}
	return names, nil
}

// chunkTestNames splits names into -test.run arguments no longer than limit.
func chunkTestNames(names []string, limit int) ([][]string, error) {
	const overhead = len(testRunPrefix) + len(testArgSuffix)
	var chunks [][]string
	var cur []string
	curLen := overhead
	for _, name := range names {
		part := regexp.QuoteMeta(name)
		need := len(part)
		if len(cur) > 0 {
			need++ // the '|' separator
		}
		if curLen+need > limit {
			if len(cur) == 0 {
				return nil, fmt.Errorf("test %q needs a %d-byte -test.run argument, over the %d-byte limit", name, len(testRunArg([]string{name})), limit)
			}
			chunks = append(chunks, cur)
			cur = nil
			curLen = overhead
			need = len(part)
		}
		cur = append(cur, name)
		curLen += need
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks, nil
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
	defer bin.remove()
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
	defer bin.remove()
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
