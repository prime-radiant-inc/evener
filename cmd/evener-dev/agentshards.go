package dev

// agent-shards runs the agent package's tests as cost-balanced shards, and
// hub-shards and cli-shards do the same for cmd/evener-hub and cmd/evener, whose
// mostly-serial tests otherwise run one after another in a single binary. Each
// reads its own variables: AGENT_SHARD_* below, and HUB_SHARD_* / CLI_SHARD_*
// with the same suffixes. The
// runner is the port of scripts/agent-test-shards.sh, whose header carried the
// measurements this design rests on: one ~2750-test binary spends ~26-32s as
// a single invocation, and cost-balanced shards (4 × -parallel 3) take it to
// ~21s. BALANCE is what matters, not shard count — the weights come from a
// survey cached by test-set identity, so a run pays the survey only when a
// test is added, renamed, or removed.
//
// Interface (unchanged from the script):
//
//	AGENT_SHARD_COUNT      number of shards (default 4)
//	AGENT_SHARD_PARALLEL   -parallel within each shard (default 3)
//	AGENT_SHARD_CONCURRENCY  shards running at once (default 0 = all at once)
//	AGENT_SHARD_SURVEY_PARALLEL  -parallel for the survey pass (default 6)
//	AGENT_SHARD_SKIP       regex handed to the survey's -test.skip and to
//	                       every shard's: a skipped test draws no cost line,
//	                       so it lands in no shard, but a cached survey means
//	                       no survey ran and the shards would otherwise run
//	                       the test the operator asked to skip. Pinned by
//	                       TestAgentShardsSkipReachesTheShardsToo.
//	AGENT_SHARD_NO_SURVEY  1 = ignore the cache and weight every test equally
//	AGENT_SHARD_RESURVEY   1 = force the survey to re-run even on a cache hit
//	AGENT_SHARD_CACHE_DIR  survey cache (default $(go env GOCACHE)/evener-<label>-shards)
//
// plus pass-through `go test` flags. Every test lands in exactly one shard,
// proven before running anything; one PASS/FAIL line per shard with wall
// time; logs are deleted only on normal green completion and pointed at
// otherwise; the exit is nonzero on any shard failure or partition
// discrepancy, and 129/130/143 on HUP/INT/TERM.
//
// The -test.run regex for each shard is written to a file (shardN.run in
// the scratch dir) and the path handed via EVENER_SHARD_RUN_FILE so the
// test binary's TestMain reads it in-process. This keeps the regex off
// the execve argument list: a large shard's regex can exceed Linux's
// MAX_ARG_STRLEN (128KB per single argument string).
//
// Scratch is "<label>-test-shards.<pid>" under TMPDIR, reclaimed from dead
// runs at startup (internal/devtool/scratch): the janitor this replaced is
// gone, and a SIGKILLed run's debris lives exactly until the next run.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"primeradiant.com/evener/internal/devtool/procgroup"
	"primeradiant.com/evener/internal/devtool/scratch"
)

// defaultSurveyParallel is the survey pass's default -parallel. The shards get
// their width from AGENT_SHARD_PARALLEL; the survey measures cost on a single
// binary, which has always run slightly wider.
const defaultSurveyParallel = 6

// shardsConfig is one shards run: which package to shard, how wide, and
// where its words go.
type shardsConfig struct {
	label          string
	envPrefix      string
	moduleDir      string
	pkgDir         string
	count          int
	parallel       int
	concurrency    int
	surveyParallel int
	skip           string
	noSurvey       bool
	resurvey       bool
	cacheDir       string
	flags          []string
	stdout         io.Writer
	stderr         io.Writer
	signals        <-chan os.Signal
	// slotWait, when set, is called each time a shard has to wait for a free
	// concurrency slot: the observable moment the runner holds a shard back.
	// A test seam; nil in production.
	slotWait func()
}

// runAgentShards and runHubShards are the subcommand entries: environment in,
// exit code out. Each package reads its own <PREFIX>_SHARD_* variables.
func runAgentShards(args []string) int {
	return runPackageShards("agent", "agent", "agent", "AGENT", args)
}

// runHubShards builds from the repository root, the module cmd/evener-hub
// belongs to, so path-valued build flags resolve where the gate's root-module
// go test resolves them; the shards still run in the package directory.
func runHubShards(args []string) int {
	return runPackageShards("hub", ".", filepath.Join("cmd", "evener-hub"), "HUB", args)
}

// runCLIShards shards cmd/evener, the CLI's ~340 mostly-serial serve and run
// lifecycle tests, the same way.
func runCLIShards(args []string) int {
	return runPackageShards("cli", ".", filepath.Join("cmd", "evener"), "CLI", args)
}

// runPackageShards shards the package in pkgDir, building it from moduleDir
// (its module's root; both relative to the repository root), naming it label
// in its output and reading envPrefix_SHARD_* for its settings.
func runPackageShards(label, moduleDir, pkgDir, envPrefix string, args []string) int {
	env := func(name string) string { return envPrefix + "_SHARD_" + name }
	count, err := envPositiveInt(env("COUNT"), 4)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s-shards: %v\n", label, err)
		return 1
	}
	parallel, err := envPositiveInt(env("PARALLEL"), 3)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s-shards: %v\n", label, err)
		return 1
	}
	// Shards are independent processes; _SHARD_PARALLEL bounds each one's
	// tests but not how many run at once. Zero (and unset) means all of them,
	// the historical behavior; a positive value is the total concurrency the
	// load-aware gate lowers on a busy host.
	concurrency, err := envNonNegativeInt(env("CONCURRENCY"), 0)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s-shards: %v\n", label, err)
		return 1
	}
	surveyParallel, err := envPositiveInt(env("SURVEY_PARALLEL"), defaultSurveyParallel)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s-shards: %v\n", label, err)
		return 1
	}
	// Two deep, because a second signal must be waiting when the first is
	// still being handled.
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	return runShards(shardsConfig{
		label:          label,
		envPrefix:      envPrefix,
		moduleDir:      moduleDir,
		pkgDir:         pkgDir,
		count:          count,
		parallel:       parallel,
		concurrency:    concurrency,
		surveyParallel: surveyParallel,
		skip:           os.Getenv(env("SKIP")),
		noSurvey:       envFlag(env("NO_SURVEY")),
		resurvey:       envFlag(env("RESURVEY")),
		cacheDir:       os.Getenv(env("CACHE_DIR")),
		flags:          args,
		stdout:         os.Stdout,
		stderr:         os.Stderr,
		signals:        signals,
	})
}

// buildLocation is where the test binary is built from and the package path
// it builds: the module root and the package relative to it, so path-valued
// build flags (-overlay, -modfile, -pgo) resolve against the module root the
// way a module-wide go test resolves them. A config with no moduleDir builds
// in the package directory itself.
func (cfg shardsConfig) buildLocation() (dir, target string, err error) {
	if cfg.moduleDir == "" || cfg.moduleDir == cfg.pkgDir {
		return cfg.pkgDir, ".", nil
	}
	rel, err := filepath.Rel(cfg.moduleDir, cfg.pkgDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("package %s is not inside module %s", cfg.pkgDir, cfg.moduleDir)
	}
	return cfg.moduleDir, "./" + filepath.ToSlash(rel), nil
}

// surveyArgs is the survey pass's test-binary arguments. The survey runs one
// binary at a single parallelism to measure each test's cost; the shards then
// split that work. Keeping the two separate is what lets a loaded host lower
// the survey's parallelism through AGENT_SHARD_SURVEY_PARALLEL without
// changing the shard split.
//
// The caller's -timeout comes here too, and for the same reason it was raised:
// the survey is the longest single run of the lot, one binary over the whole
// suite, so a timeout that the shards need is a timeout the survey needed
// first. -failfast stays with the shards: a survey that stops at the first
// failure has measured part of the suite, and a partial measurement is worse
// than none, since the shards would be packed from it as though it were
// complete.
func surveyArgs(parallel int, skip string, short bool, testFlags []string) []string {
	// A config built without runAgentShards leaves the field zero; never hand
	// the test binary "-test.parallel 0".
	if parallel < 1 {
		parallel = defaultSurveyParallel
	}
	args := []string{"-test.count=1", "-test.parallel", strconv.Itoa(parallel), "-test.run", "^(Test|Example)", "-test.v"}
	for _, f := range testFlags {
		if strings.HasPrefix(f, "-test.timeout=") {
			args = append(args, f)
		}
	}
	if skip != "" {
		args = append(args, "-test.skip", skip)
	}
	if short {
		args = append(args, "-test.short")
	}
	return args
}

func envPositiveInt(name string, def int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a positive integer (got %q)", name, raw)
	}
	return n, nil
}

// envNonNegativeInt is envPositiveInt with zero allowed: AGENT_SHARD_CONCURRENCY
// uses zero for "no limit", so it cannot share the positive-only parse. Unset
// is def.
func envNonNegativeInt(name string, def int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer (got %q)", name, raw)
	}
	return n, nil
}

// envFlag reads the script's `-eq 0` convention: unset and "0" are off,
// anything else is on.
func envFlag(name string) bool {
	raw := os.Getenv(name)
	return raw != "" && raw != "0"
}

// interrupter tracks live shard process groups and, on the first signal,
// TERMs every group so in-flight waits return. The Setpgid/Terminate pair is
// shared with module-lint via internal/devtool/procgroup, which this file
// adopts as its third caller; the inline duplication it used to carry is
// retired.
type interrupter struct {
	mu     sync.Mutex
	pgids  []int
	signal syscall.Signal
}

func (in *interrupter) add(pgid int) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.signal != 0 {
		procgroup.Terminate(pgid)
		return
	}
	in.pgids = append(in.pgids, pgid)
}

func (in *interrupter) interrupt(sig syscall.Signal) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.signal != 0 {
		return
	}
	in.signal = sig
	for _, pgid := range in.pgids {
		procgroup.Terminate(pgid)
	}
}

// exitCode returns 0 while uninterrupted, else the script's 128+signal codes
// (129/130/143).
func (in *interrupter) exitCode() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.signal == 0 {
		return 0
	}
	return 128 + int(in.signal)
}

var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP:  "SIGHUP",
	syscall.SIGINT:  "SIGINT",
	syscall.SIGTERM: "SIGTERM",
}

// runShards runs the module's tests as cost-balanced shards.
func runShards(cfg shardsConfig) int {
	if info, err := os.Stat(cfg.pkgDir); err != nil || !info.IsDir() {
		_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: no %s dir\n", cfg.label, cfg.pkgDir)
		return 2
	}

	dir, err := scratch.Acquire(cfg.label+"-test-shards", cfg.stderr)
	if err != nil {
		_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: could not create a scratch directory: %v\n", cfg.label, err)
		return 2
	}
	logdir := dir.Path()
	green := false
	defer func() {
		if !green {
			dir.KeepOnFailure()
		}
		dir.Release()
	}()

	in := &interrupter{}
	if cfg.signals != nil {
		go func() {
			first := true
			for sig := range cfg.signals {
				s, isSyscall := sig.(syscall.Signal)
				if !isSyscall {
					s = syscall.SIGTERM
				}
				if first {
					first = false
					_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: interrupted by %s\n", cfg.label, signalNames[s])
					in.interrupt(s)
					continue
				}
				// A shard that ignores TERM would otherwise hold the run
				// hostage forever: the wait for it never returns, and while
				// signals are relayed to this channel none of them takes its
				// default action. So the second one leaves immediately, with
				// the same 128+signal code the first would have exited on,
				// and says where the logs it is abandoning are — os.Exit
				// runs no deferred cleanup, so the scratch directory stays
				// on disk as the record.
				//
				// The script got here by clearing its traps in the first
				// handler and letting the next signal kill the shell.
				// signal.Stop plus a re-raise is the direct translation and
				// was tried; the re-raised signal does not reliably take the
				// default action before the process continues, so this exits
				// under its own power instead.
				_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: %s again — abandoning the running shards; logs: %s\n", cfg.label, signalNames[s], logdir)
				os.Exit(128 + int(s))
			}
		}()
	}

	// Build the test binary once; every shard runs it.
	build := filepath.Join(logdir, cfg.label+".test")
	buildLog := filepath.Join(logdir, "build.log")
	// The build gets the caller's build flags: a -race run has to compile a
	// race-detector binary, and a -tags run has to compile the files that tag
	// selects, or the shards test something the caller did not ask for.
	parsed, err := parseFlags(cfg.flags, cfg.envPrefix)
	if err != nil {
		_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: %v\n", cfg.label, err)
		return 1
	}
	goflags, err := effectiveGoflags()
	if err != nil {
		_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: %v\n", cfg.label, err)
		return 1
	}
	if err := checkGoflags(goflags, cfg.envPrefix); err != nil {
		_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: %v\n", cfg.label, err)
		return 1
	}
	extraFlags := parsed.test
	buildArgs := append([]string{"test", "-c"}, parsed.build...)
	buildDir, buildTarget, err := cfg.buildLocation()
	if err != nil {
		_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: %v\n", cfg.label, err)
		return 1
	}
	buildArgs = append(buildArgs, "-o", build, buildTarget)
	if err = cfg.runToLog(in, buildLog, buildDir, "go", buildArgs...); err != nil {
		if code := in.exitCode(); code != 0 {
			return code
		}
		_, _ = fmt.Fprintf(cfg.stdout, "%s-shards: build failed\n", cfg.label)
		copyFileTo(cfg.stdout, buildLog)
		return 1
	}
	if code := in.exitCode(); code != 0 {
		return code
	}

	// The test set's identity keys the survey cache; the same listing also
	// backs the equal-weights fallback.
	listOut, _ := cfg.captureChild(in, cfg.pkgDir, build, "-test.list", ".*")
	if code := in.exitCode(); code != 0 {
		return code
	}
	cachedSurvey := cfg.cachedSurveyPath(listOut, parsed, goflags)

	var costs []testCost
	if !cfg.noSurvey {
		surveyLog := filepath.Join(logdir, "survey.log")
		cacheHit := false
		if !cfg.resurvey && fileHasContent(cachedSurvey) {
			if data, err := os.ReadFile(cachedSurvey); err == nil && cfg.surveyCoversTestSet(data, listOut) {
				_ = os.WriteFile(surveyLog, data, 0o644)
				cacheHit = true
			}
			// An unreadable cache, or one measuring only part of the current
			// test set, is no cache at all: survey. The shared cache is
			// written by concurrent gate runs, so a nonempty file can be a
			// partial write caught mid-flight.
		}
		if !cacheHit {
			_, _ = fmt.Fprintf(cfg.stdout, "%s-shards: surveying test costs (one-time for this test set)\n", cfg.label)
			args := surveyArgs(cfg.surveyParallel, cfg.skip, parsed.short, parsed.test)
			if err := cfg.runToLog(in, surveyLog, cfg.pkgDir, build, args...); err != nil {
				if code := in.exitCode(); code != 0 {
					return code
				}
				_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: the survey pass failed — the suite is red\n", cfg.label)
				replaySurveyFailures(cfg.stderr, surveyLog, maxSurveyFailures)
				_, _ = fmt.Fprintf(cfg.stderr, "full log: %s\n", surveyLog)
				return 1
			}
			if cachedSurvey != "" {
				if data, err := os.ReadFile(surveyLog); err == nil {
					_ = writeFileAtomic(cachedSurvey, data)
				}
			}
		}
		if data, err := os.ReadFile(surveyLog); err == nil {
			costs = parseSurvey(string(data))
		}
	}
	if code := in.exitCode(); code != 0 {
		return code
	}
	if len(costs) == 0 {
		// No survey, or it measured nothing: weight every test equally.
		// This still partitions correctly, it is just unbalanced.
		costs = equalWeights(listOut)
	}
	if len(costs) == 0 {
		_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: found no tests to shard\n", cfg.label)
		return 1
	}

	bins, _, err := packShards(costs, cfg.count, cfg.envPrefix)
	if err != nil {
		_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: %v\n", cfg.label, err)
		return 1
	}
	for i, bin := range bins {
		names := filepath.Join(logdir, fmt.Sprintf("shard%d.names", i))
		if err := os.WriteFile(names, []byte(strings.Join(bin, "\n")+"\n"), 0o644); err != nil {
			_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: %v\n", cfg.label, err)
			return 1
		}
	}
	_, _ = fmt.Fprintf(cfg.stdout, "%s-shards: %d shards, -parallel %d each\n", cfg.label, len(bins), cfg.parallel)

	// A shard is one OS process, so cfg.parallel bounds only the tests inside
	// it. limit bounds the processes themselves: on a busy host or a
	// quota-limited cgroup, running every shard at once is what oversubscribes
	// the machine, whatever each shard's -parallel says. A concurrency of zero,
	// or one at least the shard count, starts every shard at once: the
	// historical behavior and what an idle run still gets.
	limit := cfg.concurrency
	if limit < 1 || limit > len(bins) {
		limit = len(bins)
	}
	if limit < len(bins) {
		_, _ = fmt.Fprintf(cfg.stdout, "%s-shards: at most %d of %d shards run at once\n", cfg.label, limit, len(bins))
	}
	slots := make(chan struct{}, limit)

	// Launch shards, at most limit at a time. Slots are acquired before
	// starting a shard and released by the goroutine that waits for it, so a
	// long shard holds its slot exactly as long as the process lives. Each
	// shard is waited by its own goroutine so its reported wall time is its OWN
	// clock (the script measured with /usr/bin/time -p inside each invocation);
	// results are still reported in shard order.
	type shardResult struct {
		err     error
		seconds float64
	}
	results := make([]chan shardResult, len(bins))
	launchFailed := false
	for i, bin := range bins {
		select {
		case slots <- struct{}{}:
		default:
			// Every slot is taken: this shard waits for a running one to exit.
			if cfg.slotWait != nil {
				cfg.slotWait()
			}
			slots <- struct{}{}
		}
		// A signal that arrived while we waited for a slot must not start more
		// work: the interrupter has already TERMed the live shards, and a shard
		// started now would outlive the run we are trying to stop.
		if in.exitCode() != 0 {
			<-slots
			break
		}
		// The -test.run regex for a large shard can exceed Linux's
		// MAX_ARG_STRLEN (128KB per single argument string). Write the
		// regex to a file and hand the path via env so the test binary
		// reads it in-process, never touching the execve argument list.
		runFile := filepath.Join(logdir, fmt.Sprintf("shard%d.run", i))
		if err := os.WriteFile(runFile, []byte(nameRegex(bin)), 0o644); err != nil {
			_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: %v\n", cfg.label, err)
			return 1
		}
		args := []string{"-test.count=1", "-test.parallel", strconv.Itoa(cfg.parallel)}
		// The skip reaches the shards, not just the survey: a cached survey
		// means no survey runs at all, and the shards would then run the very
		// test the operator asked to skip.
		if cfg.skip != "" {
			args = append(args, "-test.skip", cfg.skip)
		}
		args = append(args, extraFlags...)
		log, err := os.Create(filepath.Join(logdir, fmt.Sprintf("shard%d.log", i)))
		if err != nil {
			_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: %v\n", cfg.label, err)
			return 1
		}
		cmd := exec.CommandContext(context.Background(), build, args...)
		cmd.Dir = cfg.pkgDir
		cmd.Stdout, cmd.Stderr = log, log
		cmd.Env = append(os.Environ(), "EVENER_SHARD_RUN_FILE="+runFile)
		started := time.Now()
		err = procgroup.Start(cmd)
		_ = log.Close()
		if err != nil {
			<-slots
			_, _ = fmt.Fprintf(cfg.stderr, "%s-shards: starting shard %d: %v\n", cfg.label, i, err)
			launchFailed = true
			break
		}
		in.add(cmd.Process.Pid)
		result := make(chan shardResult, 1)
		results[i] = result
		go func() {
			err := cmd.Wait()
			<-slots
			result <- shardResult{err: err, seconds: time.Since(started).Seconds()}
		}()
	}

	var failed []int
	for i, result := range results {
		if result == nil {
			continue
		}
		r := <-result
		if r.err == nil {
			_, _ = fmt.Fprintf(cfg.stdout, "PASS  %s:%-2d %8s (%d tests)\n", cfg.label, i, fmt.Sprintf("%.2fs", r.seconds), len(bins[i]))
		} else {
			_, _ = fmt.Fprintf(cfg.stdout, "FAIL  %s:%-2d\n", cfg.label, i)
			failed = append(failed, i)
		}
	}
	fail := launchFailed || len(failed) > 0
	if code := in.exitCode(); code != 0 {
		return code
	}

	if fail {
		// Replay by verdict, never by matching failure markers in the log. A
		// shard can fail with no `go test` marker anywhere in its output — a
		// build error, an os.Exit, a killed process — and marker matching
		// dropped exactly those, leaving the verdicts with the most to
		// explain with nothing behind them (kata mjzx; run-module-tests.sh
		// carries the same fix for the same reason). A shard that never
		// started has no verdict and no log; its error is already on stderr.
		if len(failed) > 0 {
			_, _ = fmt.Fprintln(cfg.stdout)
			_, _ = fmt.Fprintln(cfg.stdout, "=== failing shard output ===")
			for _, i := range failed {
				log := filepath.Join(logdir, fmt.Sprintf("shard%d.log", i))
				_, _ = fmt.Fprintf(cfg.stdout, "----- %s:%d -----\n", cfg.label, i)
				if !copyFileTo(cfg.stdout, log) {
					_, _ = fmt.Fprintf(cfg.stdout, "(no output captured: %s is empty or missing)\n", log)
				}
			}
		}
		_, _ = fmt.Fprintln(cfg.stdout)
		_, _ = fmt.Fprintf(cfg.stdout, "full logs: %s\n", logdir)
		return 1
	}
	green = true
	return 0
}

// effectiveGoflags is what the toolchain will actually apply, which is the
// environment's GOFLAGS layered over `go env -w`'s. It is part of the survey
// cache key: a flag that arrives this way changes the binary without appearing
// in any argument list the runner can see. An unreadable answer is not treated
// as an empty one -- that is the key for "no GOFLAGS at all", and handing one
// run's survey to another under a different build is the mistake this is in
// the key to prevent -- so the run stops instead.
func effectiveGoflags() (string, error) {
	out, err := exec.CommandContext(context.Background(), "go", "env", "GOFLAGS").Output()
	if err != nil {
		return "", fmt.Errorf("reading GOFLAGS: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// surveyRedLine is the marker that announces a failure in a `go test -v` log:
// a failing test's verdict, or the panic that ended the binary. The verdict is
// matched through its colon, so `--- FAILURE: ...` — a test's own output — is
// not a marker; `panic:` stays a prefix, since the message after it is the
// panic's own text. The survey has no per-shard verdict to sort by — it is one
// pass over the whole suite — so the excerpt is built around these markers.
var surveyRedLine = regexp.MustCompile(`^(?:--- FAIL:|panic:)`)

// The red survey's excerpt is one failure block per marker, never the suite
// log: these bound how much of the failing test's own output a block carries,
// how many blocks print at all, and how much of the log a markerless run's
// tail fallback carries. The CI job summary shows the excerpt in full, so an
// unbounded dump here would drown the job it exists to make readable.
const (
	surveyContextBefore = 10
	surveyContextAfter  = 6
	maxSurveyFailures   = 20
	surveyTailLines     = surveyContextBefore + surveyContextAfter
)

// cachedSurveyPath resolves the survey cache file for this test set, or ""
// when there is nowhere to cache. Cache trouble is never fatal — it only
// costs the next run a survey.
func (cfg shardsConfig) cachedSurveyPath(listOut string, parsed parsedFlags, goflags string) string {
	cacheDir := cfg.cacheDir
	if cacheDir == "" {
		out, err := exec.CommandContext(context.Background(), "go", "env", "GOCACHE").Output()
		if err != nil || len(strings.TrimSpace(string(out))) == 0 {
			return ""
		}
		cacheDir = filepath.Join(strings.TrimSpace(string(out)), "evener-"+cfg.label+"-shards")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "survey-"+testSetKey(listOut, parsed, goflags, cfg.skip)+".log")
}

// surveyCoversTestSet reports whether a cached survey accounts for every test
// this run must shard. The cache is keyed by test-set identity and written by
// concurrent gate runs; a nonempty file can be a partial write caught
// mid-flight, in which case accepting it would pack shards over the subset it
// measured and still report green while the rest run in no shard. Tests the
// survey deliberately skips (the -test.skip regex) are exempt: they draw no
// cost line by design.
func (cfg shardsConfig) surveyCoversTestSet(data []byte, listOut string) bool {
	have := map[string]bool{}
	for _, tc := range parseSurvey(string(data)) {
		have[tc.name] = true
	}
	var skip *regexp.Regexp
	if cfg.skip != "" {
		skip, _ = regexp.Compile(cfg.skip)
	}
	for _, tc := range equalWeights(listOut) {
		if skip != nil && skip.MatchString(tc.name) {
			continue
		}
		if !have[tc.name] {
			return false
		}
	}
	return true
}

// writeFileAtomic replaces path with data by writing a uniquely named temp
// file in the same directory and renaming it into place. A reader sharing the
// path — the survey cache is shared across concurrent gate runs — therefore
// only ever observes a complete prior survey or a complete new one, never a
// truncated in-place write.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	// CreateTemp's files are 0o600; the cache was written 0o644.
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = ""
	return nil
}

// runToLog runs a child in its own process group with both output streams in
// one log file, registered with the interrupter for signal forwarding.
func (cfg shardsConfig) runToLog(in *interrupter, logPath, dir, name string, args ...string) error {
	log, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	cmd := exec.CommandContext(context.Background(), name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = log, log
	if err := procgroup.Start(cmd); err != nil {
		return err
	}
	in.add(cmd.Process.Pid)
	return cmd.Wait()
}

// captureChild runs a child in its own process group and returns its stdout,
// discarding stderr the way the script's `2>/dev/null` did.
func (cfg shardsConfig) captureChild(in *interrupter, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), name, args...)
	cmd.Dir = dir
	var out strings.Builder
	cmd.Stdout = &out
	if err := procgroup.Start(cmd); err != nil {
		return "", err
	}
	in.add(cmd.Process.Pid)
	err := cmd.Wait()
	return out.String(), err
}

func fileHasContent(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// replaySurveyFailures writes the excerpt a red survey prints: the failing
// tests' own output from a `go test -v` log, one failure block per marker,
// bounded per block by surveyContextBefore/surveyContextAfter lines and in the
// number of blocks by maxBlocks. Those bounds are what keep the excerpt an
// excerpt — a CI job summary shows it in full.
//
// A block is the marker with the test's own output around it, and it runs from
// the previous framework line to the next one, bounded by the two line counts.
// If a mismatched owner separates a marker from its diagnostic context, or the
// ordinary window is empty, owner-aware expansion recovers bounded context
// instead. That keeps the indented t.Log/t.Error lines and the test's
// unindented direct output (fmt.Println, log.Print, a child process) alike;
// only the toolchain's own framing — `=== `, `--- `, `ok `, `FAIL`, `PASS`, or
// another failure marker such as `panic:` — ends the run, on either side of
// the marker.
//
// A survey that died with no marker at all — a fatal error, an os.Exit, a
// killed binary — has no block to show, so a bounded tail of the log stands in.
// The caller reaches this only after the survey pass exited nonzero — the run
// is already known red — so there is no green verdict to consult here, and a
// log tail that happens to end in a test's own `ok done` print must not
// suppress the fallback. The excerpt is non-empty whenever the log has content:
// the run has just written that log, so the only silent case is a path this
// function cannot read (or one holding nothing but whitespace) — an unreadable
// log, not an absent one.
func replaySurveyFailures(w io.Writer, path string, maxBlocks int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return
	}
	lines := strings.Split(trimmed, "\n")
	matched := false
	emitted := 0 // exclusive end of the last block written
	emittedLines := make(map[int]struct{})
	for i := 0; i < len(lines) && maxBlocks > 0; {
		if !surveyRedLine.MatchString(lines[i]) {
			i++
			continue
		}
		maxBlocks--
		matched = true
		start := i
		for n := 0; n < surveyContextBefore && start > emitted && !surveyFrameworkLine(lines[start-1]); n++ {
			start--
		}
		end := i + 1
		for n := 0; n < surveyContextAfter && end < len(lines) && !surveyFrameworkLine(lines[end]); n++ {
			end++
		}
		if start == i || surveyFailureHasMismatchedOwner(lines, i) {
			if expanded, ok := expandSurveyFailure(lines, i, start, emittedLines); ok {
				for _, excerpt := range expanded {
					_, _ = fmt.Fprintln(w, excerpt)
				}
				for _, excerpt := range lines[i+1 : end] {
					_, _ = fmt.Fprintln(w, excerpt)
				}
				for index := i; index < end; index++ {
					emittedLines[index] = struct{}{}
				}
				emitted = end
				i = end
				continue
			}
		}
		for _, excerpt := range lines[start:end] {
			_, _ = fmt.Fprintln(w, excerpt)
		}
		for index := start; index < end; index++ {
			emittedLines[index] = struct{}{}
		}
		// Scanning resumes past the ordinary window. Expansion can reach back
		// earlier, so emittedLines prevents reprinting lines already emitted.
		emitted = end
		i = end
	}
	if !matched && maxBlocks > 0 {
		for _, line := range lines[max(len(lines)-surveyTailLines, 0):] {
			_, _ = fmt.Fprintln(w, line)
		}
	}
}

// surveyFailureName recovers the test name from the framing line the testing
// package emits. The name lets a failure reach back to its own run, rather than
// treating a completed subtest or an interleaved parallel test as the parent's
// boundary.
func surveyFailureName(line string) string {
	if strings.HasPrefix(line, "panic:") {
		return ""
	}
	name := strings.TrimPrefix(line, "--- FAIL:")
	if end := strings.Index(name, " ("); end >= 0 {
		name = name[:end]
	}
	return strings.TrimSpace(name)
}

// surveyFailedChildNames collects failed descendants from a parent's indented
// verdict block. The scan stops at the first unindented line, which is the next
// top-level frame or failure marker rather than another child verdict.
func surveyFailedChildNames(lines []string, marker int, parent string) map[string]struct{} {
	var failed map[string]struct{}
	for index := marker + 1; index < len(lines); index++ {
		line := lines[index]
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			break
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--- FAIL:") {
			child := surveyFailureName(trimmed)
			if strings.HasPrefix(child, parent+"/") {
				if failed == nil {
					failed = make(map[string]struct{})
				}
				failed[child] = struct{}{}
			}
		}
	}
	return failed
}

// surveyFailureHasMismatchedOwner reports whether the nearest ownership frame
// before a failure differs from the failing test itself. Descendant frames
// intentionally count as mismatches: their ordinary tail can hide the
// parent's diagnostic window, so expansion must rank the combined owners.
func surveyFailureHasMismatchedOwner(lines []string, marker int) bool {
	name := surveyFailureName(lines[marker])
	if name == "" {
		return false
	}
	for index := marker - 1; index >= 0; index-- {
		if owner := surveyPhaseOwner(lines[index]); owner != "" {
			return owner != name
		}
	}
	return false
}

// surveyDiagnosticLine matches the source location that testing prefixes on
// t.Error/t.Fatal output. These lines are the useful part of a parent failure
// even when the test framework has put many subtest frames between them and
// the parent's verdict.
var surveyDiagnosticLine = regexp.MustCompile(`(?:^|[[:space:]])[^[:space:]]+\.go:[0-9]+:`)

// expandSurveyFailure recovers a bounded set of a parent's output when the
// nearby excerpt contains only its verdict or a different test owns the
// nearest context. The lines from ordinaryStart up to marker are the
// already-selected ordinary context. Selection starts with owned ordinary
// output or, when that window has no lines owned by the failing test or its
// descendants, descendant diagnostics. A diagnostic kind reserves a slot only
// when its newest candidate would otherwise be dropped from the current
// ordinary tail after other reservations. Newest parent diagnostics then fill
// up to all but the failed-child reservation, followed by failed-child
// diagnostics; remaining slots are backfilled from owned diagnostics, then
// owned output, then unindented ordinary-window lines owned by other tests.
// Source diagnostics are associated with the most recent go test RUN/CONT/NAME
// frame; a verdict returns ownership to the failing test. If ordinary context
// owned by the failing test or its descendants exists, expansion requires a
// source diagnostic owned by the failing test or one of its failed children.
// When ordinary context overflows its budget, the newest budget-sized tail is
// kept contiguously, dropping only older lines.
// The result is still no larger than one block's existing before bound plus its
// marker.
func expandSurveyFailure(lines []string, marker, ordinaryStart int, emittedLines map[int]struct{}) ([]string, bool) {
	name := surveyFailureName(lines[marker])
	if name == "" {
		return nil, false
	}
	run := -1
	for i := marker - 1; i >= 0; i-- {
		if lines[i] == "=== RUN   "+name {
			run = i
			break
		}
	}
	if run < 0 {
		return nil, false
	}

	const maxExpandedLines = surveyContextBefore
	appendNewest := func(candidates *[]int, index int) {
		if len(*candidates) == maxExpandedLines {
			copy((*candidates)[:], (*candidates)[1:])
			(*candidates)[maxExpandedLines-1] = index
			return
		}
		*candidates = append(*candidates, index)
	}
	owner := name
	ordinaryOwnedCandidates := make([]int, 0, maxExpandedLines)
	parentDiagnosticCandidates := make([]int, 0, maxExpandedLines)
	descendantDiagnosticCandidates := make([]int, 0, maxExpandedLines)
	failedChildDiagnosticCandidates := make([]int, 0, maxExpandedLines)
	ownedDiagnosticCandidates := make([]int, 0, maxExpandedLines)
	ownedOutputCandidates := make([]int, 0, maxExpandedLines)
	ordinaryContextCandidates := make([]int, 0, maxExpandedLines)
	failedChildNames := surveyFailedChildNames(lines, marker, name)
	for index, line := range lines[run+1 : marker] {
		if frameOwner := surveyPhaseOwner(line); frameOwner != "" {
			owner = frameOwner
		}
		trimmed := strings.TrimSpace(line)
		if surveyTestVerdictLine.MatchString(trimmed) {
			owner = name
		}
		if surveyFrameworkLine(line) || trimmed == "" {
			continue
		}
		lineIndex := run + 1 + index
		if _, alreadyEmitted := emittedLines[lineIndex]; alreadyEmitted {
			continue
		}
		diagnostic := surveyDiagnosticLine.MatchString(line)
		if diagnostic && owner == name {
			appendNewest(&parentDiagnosticCandidates, lineIndex)
		}
		owned := owner == name || strings.HasPrefix(owner, name+"/")
		ordinary := lineIndex >= ordinaryStart
		if ordinary && owned {
			appendNewest(&ordinaryOwnedCandidates, lineIndex)
		}
		if owned {
			if diagnostic {
				if owner != name {
					appendNewest(&descendantDiagnosticCandidates, lineIndex)
					for failedChild := range failedChildNames {
						if owner == failedChild || strings.HasPrefix(owner, failedChild+"/") {
							appendNewest(&failedChildDiagnosticCandidates, lineIndex)
							break
						}
					}
				}
				appendNewest(&ownedDiagnosticCandidates, lineIndex)
			} else {
				appendNewest(&ownedOutputCandidates, lineIndex)
			}
		}
		if ordinary && !owned && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			appendNewest(&ordinaryContextCandidates, lineIndex)
		}
	}
	hasFailureDiagnostic := len(parentDiagnosticCandidates) > 0 || len(failedChildDiagnosticCandidates) > 0
	if len(ordinaryOwnedCandidates) > 0 && !hasFailureDiagnostic {
		return nil, false
	}
	reserveParentDiagnostic := false
	reserveFailedChildDiagnostic := false
	candidateInOrdinaryTail := func(candidates []int, ordinaryBudget int) bool {
		if len(candidates) == 0 || ordinaryBudget <= 0 {
			return false
		}
		candidate := candidates[len(candidates)-1]
		start := len(ordinaryOwnedCandidates) - ordinaryBudget
		if start < 0 {
			start = 0
		}
		for _, ordinaryCandidate := range ordinaryOwnedCandidates[start:] {
			if ordinaryCandidate == candidate {
				return true
			}
		}
		return false
	}
	for {
		reservedDiagnostics := 0
		if reserveParentDiagnostic {
			reservedDiagnostics++
		}
		if reserveFailedChildDiagnostic {
			reservedDiagnostics++
		}
		ordinaryBudget := maxExpandedLines - reservedDiagnostics
		changed := false
		if !reserveParentDiagnostic && len(parentDiagnosticCandidates) > 0 && !candidateInOrdinaryTail(parentDiagnosticCandidates, ordinaryBudget) {
			reserveParentDiagnostic = true
			changed = true
		}
		if !reserveFailedChildDiagnostic && len(failedChildDiagnosticCandidates) > 0 && !candidateInOrdinaryTail(failedChildDiagnosticCandidates, ordinaryBudget) {
			reserveFailedChildDiagnostic = true
			changed = true
		}
		if !changed {
			break
		}
	}
	keep := make(map[int]struct{}, maxExpandedLines)
	selectedCount := 0
	selectNewest := func(candidates []int, limit int) {
		for i := len(candidates) - 1; i >= 0 && selectedCount < limit; i-- {
			if _, exists := keep[candidates[i]]; exists {
				continue
			}
			keep[candidates[i]] = struct{}{}
			selectedCount++
		}
	}
	reservedDiagnostics := 0
	if reserveParentDiagnostic {
		reservedDiagnostics++
	}
	if reserveFailedChildDiagnostic {
		reservedDiagnostics++
	}
	ordinaryBudget := maxExpandedLines - reservedDiagnostics
	selectNewest(ordinaryOwnedCandidates, ordinaryBudget)
	if len(ordinaryOwnedCandidates) == 0 {
		descendantBudget := maxExpandedLines - reservedDiagnostics
		selectNewest(descendantDiagnosticCandidates, descendantBudget)
	}
	if len(parentDiagnosticCandidates) > 0 {
		parentBudget := maxExpandedLines
		if reserveFailedChildDiagnostic {
			parentBudget--
		}
		selectNewest(parentDiagnosticCandidates, parentBudget)
	}
	selectNewest(failedChildDiagnosticCandidates, maxExpandedLines)
	selectNewest(ownedDiagnosticCandidates, maxExpandedLines)
	selectNewest(ownedOutputCandidates, maxExpandedLines)
	selectNewest(ordinaryContextCandidates, maxExpandedLines)
	if selectedCount == 0 {
		return nil, false
	}
	result := make([]string, 0, selectedCount+1)
	for index := run + 1; index < marker; index++ {
		if _, exists := keep[index]; exists {
			result = append(result, lines[index])
		}
	}
	for index := range keep {
		emittedLines[index] = struct{}{}
	}
	result = append(result, lines[marker])
	return result, true
}

// surveyPhaseOwner extracts the test name from the testing package's RUN,
// CONT, or NAME frame. PAUSE is a framing boundary without an owner.
func surveyPhaseOwner(line string) string {
	phase, owner, ok := surveyPhaseParts(line)
	if !ok || phase == "PAUSE" {
		return ""
	}
	return strings.TrimSpace(owner)
}

// surveyPhaseLine matches the phases `-test.v` frames with `=== `: `RUN` when
// a test starts, `PAUSE` and `CONT` around a parallel test's wait, and `NAME`
// when testing switches output ownership. The space after the directive
// closes it off from the test name, so a test's own line that merely begins
// with one of the words (`=== PAUSED ...`) is output.
func surveyPhaseLine(line string) bool {
	_, _, ok := surveyPhaseParts(line)
	return ok
}

func surveyPhaseParts(line string) (phase, owner string, ok bool) {
	if !strings.HasPrefix(line, "=== ") {
		return "", "", false
	}
	line = line[len("=== "):]
	switch {
	case strings.HasPrefix(line, "RUN "):
		phase = "RUN"
		owner = line[len("RUN "):]
	case strings.HasPrefix(line, "PAUSE "):
		phase = "PAUSE"
		owner = line[len("PAUSE "):]
	case strings.HasPrefix(line, "CONT "):
		phase = "CONT"
		owner = line[len("CONT "):]
	case strings.HasPrefix(line, "NAME "):
		phase = "NAME"
		owner = line[len("NAME "):]
	default:
		return "", "", false
	}
	return phase, owner, strings.TrimSpace(owner) != ""
}

// surveyTestVerdictLine matches a test verdict: `--- ` and the verdict word,
// closed by the colon `go test -v` always writes. Without the colon a line is
// a test's own output — a printed diff's `--- expected`, or `--- FAILURE: ...`.
var surveyTestVerdictLine = regexp.MustCompile(`^--- (?:PASS|FAIL|SKIP):`)

// surveyVerdictLine matches a `go test -v` verdict line and nothing that
// merely begins like one. The test binary prints its bare `PASS` or `FAIL`
// verdict with nothing after it, so those are whole lines; the package verdict
// `go test` prints is `FAIL`, a tab, the package, and a tab (`FAIL\tpkg\t1.2s`);
// its summary is `ok`, two spaces, a tab, the package, and a tab
// (`ok  \tpkg\t1.2s`). The package and timing after the tab are left open —
// import paths vary — but the tabs are not: a test's own `FAIL reason`,
// `PASS details`, or `ok  details` starts like a form without being one, and
// stays in the block rather than ending it.
var surveyVerdictLine = regexp.MustCompile(`^(?:PASS|FAIL)$|^FAIL\t[^\t]+\t|^ok  \t[^\t]+\t`)

// surveyFrameworkLine reports whether a `go test -v` log line is the
// toolchain's own framing rather than a test's output: a phase line, a test
// verdict, one of the binary's or `go test`'s own verdicts, or another failure
// marker. A failure's excerpt runs until the next such line, so a test's own
// unindented output — fmt.Println, log.Print, a child process — stays in the
// block instead of being cut at the first line that is not indented.
//
// Each form is matched through the delimiter the toolchain always writes, not
// a prefix it merely starts with. A phase line is `=== ` plus `RUN`, `PAUSE`,
// `CONT`, or `NAME` and a space (`surveyPhaseLine`); a test verdict is `--- ` plus
// `PASS:`, `FAIL:`, or `SKIP:` (`surveyTestVerdictLine`); and the bare binary
// verdict plus `go test`'s package verdict and summary come from
// `surveyVerdictLine`. The variable tail of each — the test name and time, the
// package path and timing — stays open, because the toolchain's own text there
// can be anything; the delimiter is what tells framing from output. `panic:`
// alone is still a prefix: `panic: ` is the whole framing and the message
// after it is the panic's own. An ambiguous line is kept, and the ambiguous
// ones here all broke the same way: a test's direct fmt.Println stays
// unindented, so `--- expected` from a printed diff, `--- FAILURE: ...`,
// `FAIL reason`, `PASS details`, and `ok  details` were mistaken for framing
// and cut the diagnosis out of the excerpt it exists to show. Including a
// lookalike costs a bounded amount of context; mis-classifying one loses the
// diagnosis.
func surveyFrameworkLine(line string) bool {
	return surveyPhaseLine(line) ||
		surveyTestVerdictLine.MatchString(line) ||
		surveyVerdictLine.MatchString(line) ||
		surveyRedLine.MatchString(line)
}

// copyFileTo writes a whole log to w and reports whether there was anything
// to write: a verdict with an empty log behind it is worth saying out loud.
func copyFileTo(w io.Writer, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return false
	}
	_, _ = w.Write(data)
	return true
}
