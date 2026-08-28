package dev

// agent-shards runs the agent package's tests as cost-balanced shards. It is
// the port of scripts/agent-test-shards.sh, whose header carried the
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
//	AGENT_SHARD_SKIP       regex handed to the SURVEY's -test.skip, and only
//	                       to it: a skipped test draws no cost line, so it
//	                       lands in no shard. The shards themselves never
//	                       receive the flag, so on a cache hit or under
//	                       AGENT_SHARD_NO_SURVEY the variable does nothing at
//	                       all. The script behaved the same way; pinned by
//	                       TestAgentShardsSkipOnlyReachesTheSurvey.
//	AGENT_SHARD_NO_SURVEY  1 = ignore the cache and weight every test equally
//	AGENT_SHARD_RESURVEY   1 = force the survey to re-run even on a cache hit
//	AGENT_SHARD_CACHE_DIR  survey cache (default $(go env GOCACHE)/evener-agent-shards)
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
// Scratch is "agent-test-shards.<pid>" under TMPDIR, reclaimed from dead
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
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"primeradiant.com/evener/internal/devtool/procgroup"
	"primeradiant.com/evener/internal/devtool/scratch"
)

const shardScratchPrefix = "agent-test-shards"

// shardsConfig is one agent-shards run: which module to shard, how wide, and
// where its words go.
type shardsConfig struct {
	agentDir string
	count    int
	parallel int
	skip     string
	noSurvey bool
	resurvey bool
	cacheDir string
	flags    []string
	stdout   io.Writer
	stderr   io.Writer
	signals  <-chan os.Signal
}

// runAgentShards is the subcommand entry: environment in, exit code out.
func runAgentShards(args []string) int {
	count, err := envPositiveInt("AGENT_SHARD_COUNT", 4)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "agent-shards: %v\n", err)
		return 1
	}
	parallel, err := envPositiveInt("AGENT_SHARD_PARALLEL", 3)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "agent-shards: %v\n", err)
		return 1
	}
	// Two deep, because a second signal must be waiting when the first is
	// still being handled.
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	return runShards(shardsConfig{
		agentDir: "agent",
		count:    count,
		parallel: parallel,
		skip:     os.Getenv("AGENT_SHARD_SKIP"),
		noSurvey: envFlag("AGENT_SHARD_NO_SURVEY"),
		resurvey: envFlag("AGENT_SHARD_RESURVEY"),
		cacheDir: os.Getenv("AGENT_SHARD_CACHE_DIR"),
		flags:    args,
		stdout:   os.Stdout,
		stderr:   os.Stderr,
		signals:  signals,
	})
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
	if info, err := os.Stat(cfg.agentDir); err != nil || !info.IsDir() {
		_, _ = fmt.Fprintf(cfg.stderr, "agent-shards: no agent dir\n")
		return 2
	}

	dir, err := scratch.Acquire(shardScratchPrefix, cfg.stderr)
	if err != nil {
		_, _ = fmt.Fprintf(cfg.stderr, "agent-shards: could not create a scratch directory: %v\n", err)
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
					_, _ = fmt.Fprintf(cfg.stderr, "agent-shards: interrupted by %s\n", signalNames[s])
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
				_, _ = fmt.Fprintf(cfg.stderr, "agent-shards: %s again — abandoning the running shards; logs: %s\n", signalNames[s], logdir)
				os.Exit(128 + int(s))
			}
		}()
	}

	// Build the test binary once; every shard runs it.
	build := filepath.Join(logdir, "agent.test")
	buildLog := filepath.Join(logdir, "build.log")
	if err := cfg.runToLog(in, buildLog, cfg.agentDir, "go", "test", "-c", "-o", build, "."); err != nil {
		if code := in.exitCode(); code != 0 {
			return code
		}
		_, _ = fmt.Fprintln(cfg.stdout, "agent-shards: build failed")
		copyFileTo(cfg.stdout, buildLog)
		return 1
	}
	if code := in.exitCode(); code != 0 {
		return code
	}

	// The test set's identity keys the survey cache; the same listing also
	// backs the equal-weights fallback.
	listOut, _ := cfg.captureChild(in, cfg.agentDir, build, "-test.list", ".*")
	if code := in.exitCode(); code != 0 {
		return code
	}
	cachedSurvey := cfg.cachedSurveyPath(listOut)

	var costs []testCost
	if !cfg.noSurvey {
		surveyLog := filepath.Join(logdir, "survey.log")
		if !cfg.resurvey && fileHasContent(cachedSurvey) {
			if data, err := os.ReadFile(cachedSurvey); err == nil {
				_ = os.WriteFile(surveyLog, data, 0o644)
			}
		} else {
			_, _ = fmt.Fprintln(cfg.stdout, "agent-shards: surveying test costs (one-time for this test set)")
			surveyArgs := []string{"-test.count=1", "-test.parallel", "6", "-test.run", "^(Test|Example)", "-test.v"}
			if cfg.skip != "" {
				surveyArgs = append(surveyArgs, "-test.skip", cfg.skip)
			}
			if slices.Contains(cfg.flags, "-short") {
				surveyArgs = append(surveyArgs, "-test.short")
			}
			if err := cfg.runToLog(in, surveyLog, cfg.agentDir, build, surveyArgs...); err != nil {
				if code := in.exitCode(); code != 0 {
					return code
				}
				_, _ = fmt.Fprintln(cfg.stderr, "agent-shards: the survey pass failed — the suite is red")
				replayMatching(cfg.stderr, surveyLog, surveyRedLine, 20)
				_, _ = fmt.Fprintf(cfg.stderr, "full log: %s\n", surveyLog)
				return 1
			}
			if cachedSurvey != "" {
				if data, err := os.ReadFile(surveyLog); err == nil {
					_ = os.WriteFile(cachedSurvey, data, 0o644)
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
		_, _ = fmt.Fprintln(cfg.stderr, "agent-shards: found no tests to shard")
		return 1
	}

	bins, _, err := packShards(costs, cfg.count)
	if err != nil {
		_, _ = fmt.Fprintf(cfg.stderr, "agent-shards: %v\n", err)
		return 1
	}
	for i, bin := range bins {
		names := filepath.Join(logdir, fmt.Sprintf("shard%d.names", i))
		if err := os.WriteFile(names, []byte(strings.Join(bin, "\n")+"\n"), 0o644); err != nil {
			_, _ = fmt.Fprintf(cfg.stderr, "agent-shards: %v\n", err)
			return 1
		}
	}
	_, _ = fmt.Fprintf(cfg.stdout, "agent-shards: %d shards, -parallel %d each\n", len(bins), cfg.parallel)

	// Launch every shard, each waited by its own goroutine so its reported
	// wall time is its OWN clock (the script measured with /usr/bin/time -p
	// inside each invocation); results are still reported in shard order.
	extraFlags := translateFlags(cfg.flags)
	type shardResult struct {
		err     error
		seconds float64
	}
	results := make([]chan shardResult, len(bins))
	launchFailed := false
	for i, bin := range bins {
		// The -test.run regex for a large shard can exceed Linux's
		// MAX_ARG_STRLEN (128KB per single argument string). Write the
		// regex to a file and hand the path via env so the test binary
		// reads it in-process, never touching the execve argument list.
		runFile := filepath.Join(logdir, fmt.Sprintf("shard%d.run", i))
		if err := os.WriteFile(runFile, []byte(nameRegex(bin)), 0o644); err != nil {
			_, _ = fmt.Fprintf(cfg.stderr, "agent-shards: %v\n", err)
			return 1
		}
		args := []string{"-test.count=1", "-test.parallel", strconv.Itoa(cfg.parallel)}
		args = append(args, extraFlags...)
		log, err := os.Create(filepath.Join(logdir, fmt.Sprintf("shard%d.log", i)))
		if err != nil {
			_, _ = fmt.Fprintf(cfg.stderr, "agent-shards: %v\n", err)
			return 1
		}
		cmd := exec.CommandContext(context.Background(), build, args...)
		cmd.Dir = cfg.agentDir
		cmd.Stdout, cmd.Stderr = log, log
		cmd.Env = append(os.Environ(), "EVENER_SHARD_RUN_FILE="+runFile)
		started := time.Now()
		err = procgroup.Start(cmd)
		_ = log.Close()
		if err != nil {
			_, _ = fmt.Fprintf(cfg.stderr, "agent-shards: starting shard %d: %v\n", i, err)
			launchFailed = true
			break
		}
		in.add(cmd.Process.Pid)
		result := make(chan shardResult, 1)
		results[i] = result
		go func() {
			err := cmd.Wait()
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
			_, _ = fmt.Fprintf(cfg.stdout, "PASS  agent:%-2d %8s (%d tests)\n", i, fmt.Sprintf("%.2fs", r.seconds), len(bins[i]))
		} else {
			_, _ = fmt.Fprintf(cfg.stdout, "FAIL  agent:%-2d\n", i)
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
				_, _ = fmt.Fprintf(cfg.stdout, "----- agent:%d -----\n", i)
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

// surveyRedLine is the excerpt grep the script used when the survey pass came
// back red. The survey has no per-shard verdict to sort by — it is one pass
// over the whole suite whose log is pointed at in full — so the excerpt stays
// a grep here.
var surveyRedLine = regexp.MustCompile(`^(--- FAIL|panic:)`)

// cachedSurveyPath resolves the survey cache file for this test set, or ""
// when there is nowhere to cache. Cache trouble is never fatal — it only
// costs the next run a survey.
func (cfg shardsConfig) cachedSurveyPath(listOut string) string {
	cacheDir := cfg.cacheDir
	if cacheDir == "" {
		out, err := exec.CommandContext(context.Background(), "go", "env", "GOCACHE").Output()
		if err != nil || len(strings.TrimSpace(string(out))) == 0 {
			return ""
		}
		cacheDir = filepath.Join(strings.TrimSpace(string(out)), "evener-agent-shards")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "survey-"+testSetKey(listOut)+".log")
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

// replayMatching writes up to limit matching lines from a log, the script's
// `grep | head` diagnostic excerpt.
func replayMatching(w io.Writer, path string, re *regexp.Regexp, limit int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	printed := 0
	for line := range strings.SplitSeq(string(data), "\n") {
		if printed >= limit {
			return
		}
		if re.MatchString(line) {
			_, _ = fmt.Fprintln(w, line)
			printed++
		}
	}
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
