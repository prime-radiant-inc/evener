package main

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
//	AGENT_SHARD_SKIP       regex of tests to skip in every shard
//	AGENT_SHARD_NO_SURVEY  1 = ignore the cache and weight every test equally
//	AGENT_SHARD_RESURVEY   1 = force the survey to re-run even on a cache hit
//	AGENT_SHARD_CACHE_DIR  survey cache (default $(go env GOCACHE)/serf-agent-shards)
//
// plus pass-through `go test` flags. Every test lands in exactly one shard,
// proven before running anything; one PASS/FAIL line per shard with wall
// time; logs are deleted only on normal green completion and pointed at
// otherwise; the exit is nonzero on any shard failure or partition
// discrepancy, and 129/130/143 on HUP/INT/TERM.
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

	"primeradiant.com/serf/internal/devtool/scratch"
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
	signals := make(chan os.Signal, 1)
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
// explains the interruption and TERMs every group so in-flight waits return.
type interrupter struct {
	mu     sync.Mutex
	pgids  []int
	signal syscall.Signal
}

func (in *interrupter) add(pgid int) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.signal != 0 {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
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
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
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
			sig, ok := <-cfg.signals
			if !ok {
				return
			}
			s, isSyscall := sig.(syscall.Signal)
			if !isSyscall {
				s = syscall.SIGTERM
			}
			_, _ = fmt.Fprintf(cfg.stderr, "agent-shards: interrupted by %s\n", signalNames[s])
			in.interrupt(s)
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

	// Launch every shard, then report in shard order.
	extraFlags := translateFlags(cfg.flags)
	type shard struct {
		cmd     *exec.Cmd
		started time.Time
	}
	shards := make([]shard, len(bins))
	launchFailed := false
	for i, bin := range bins {
		args := []string{"-test.count=1", "-test.parallel", strconv.Itoa(cfg.parallel), "-test.run", nameRegex(bin)}
		args = append(args, extraFlags...)
		log, err := os.Create(filepath.Join(logdir, fmt.Sprintf("shard%d.log", i)))
		if err != nil {
			_, _ = fmt.Fprintf(cfg.stderr, "agent-shards: %v\n", err)
			return 1
		}
		cmd := exec.CommandContext(context.Background(), build, args...)
		cmd.Dir = cfg.agentDir
		cmd.Stdout, cmd.Stderr = log, log
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		shards[i].started = time.Now()
		err = cmd.Start()
		_ = log.Close()
		if err != nil {
			_, _ = fmt.Fprintf(cfg.stderr, "agent-shards: starting shard %d: %v\n", i, err)
			launchFailed = true
			break
		}
		in.add(cmd.Process.Pid)
		shards[i].cmd = cmd
	}

	fail := launchFailed
	for i, s := range shards {
		if s.cmd == nil {
			continue
		}
		err := s.cmd.Wait()
		seconds := time.Since(s.started).Seconds()
		if err == nil {
			_, _ = fmt.Fprintf(cfg.stdout, "PASS  agent:%-2d %8s (%d tests)\n", i, fmt.Sprintf("%.2fs", seconds), len(bins[i]))
		} else {
			_, _ = fmt.Fprintf(cfg.stdout, "FAIL  agent:%-2d\n", i)
			fail = true
		}
	}
	if code := in.exitCode(); code != 0 {
		return code
	}

	if fail {
		_, _ = fmt.Fprintln(cfg.stdout)
		_, _ = fmt.Fprintln(cfg.stdout, "=== failing shard output ===")
		for i := range shards {
			log := filepath.Join(logdir, fmt.Sprintf("shard%d.log", i))
			if fileMatches(log, shardRedLine) {
				_, _ = fmt.Fprintf(cfg.stdout, "----- agent:%d -----\n", i)
				copyFileTo(cfg.stdout, log)
			}
		}
		_, _ = fmt.Fprintln(cfg.stdout)
		_, _ = fmt.Fprintf(cfg.stdout, "full logs: %s\n", logdir)
		return 1
	}
	green = true
	return 0
}

// The two red-line shapes the script grepped for, kept separate the way it
// kept them: the survey excerpt never matched a bare FAIL summary line, the
// failing-shard replay did.
var (
	surveyRedLine = regexp.MustCompile(`^(--- FAIL|panic:)`)
	shardRedLine  = regexp.MustCompile(`^(FAIL|--- FAIL|panic:)`)
)

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
		cacheDir = filepath.Join(strings.TrimSpace(string(out)), "serf-agent-shards")
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
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

func fileMatches(path string, re *regexp.Regexp) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(strings.Split(string(data), "\n"), re.MatchString)
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

func copyFileTo(w io.Writer, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_, _ = w.Write(data)
}
