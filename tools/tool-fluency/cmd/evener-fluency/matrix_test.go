package main

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/envvars"
)

func TestMatrixConfigsExpandsVersionsByModels(t *testing.T) {
	t.Parallel()
	base := runConfig{repetitions: 3, probesDir: "tasks", outDir: "out"}
	cfgs := matrixConfigs(base,
		[]matrixVersion{{Label: "baseline", Bin: "/b/base"}, {Label: "v1-A", Bin: "/b/a"}},
		[]string{"lunarouter/m1", "lunarouter/m2"})
	if len(cfgs) != 4 {
		t.Fatalf("got %d configs, want 4", len(cfgs))
	}
	want := map[string]string{
		filepath.Join("out", "baseline", "lunarouter-m1"): "/b/base",
		filepath.Join("out", "baseline", "lunarouter-m2"): "/b/base",
		filepath.Join("out", "v1-A", "lunarouter-m1"):     "/b/a",
		filepath.Join("out", "v1-A", "lunarouter-m2"):     "/b/a",
	}
	for _, cfg := range cfgs {
		if bin, ok := want[cfg.outDir]; !ok || cfg.evenerBin != bin {
			t.Errorf("config out=%q bin=%q, want one of %v", cfg.outDir, cfg.evenerBin, want)
		}
		if cfg.repetitions != 3 || cfg.probesDir != "tasks" {
			t.Errorf("config %+v lost the base settings", cfg)
		}
		if !strings.HasSuffix(cfg.outDir, safeName(cfg.model)) {
			t.Errorf("config out=%q does not end in its model %q", cfg.outDir, cfg.model)
		}
	}
}

// TestRunMatrixBoundsConcurrencyAndJoinsErrors: at most maxConcurrent runs
// happen at once, every configuration runs, and one failure does not stop
// the rest. Not parallel: it replaces the package's suite runner.
func TestRunMatrixBoundsConcurrencyAndJoinsErrors(t *testing.T) {
	var running, peak, ran atomic.Int32
	var mu sync.Mutex
	orig := runMatrixSuite
	t.Cleanup(func() { runMatrixSuite = orig })
	runMatrixSuite = func(cfg runConfig) error {
		n := running.Add(1)
		mu.Lock()
		if n > peak.Load() {
			peak.Store(n)
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		ran.Add(1)
		if cfg.model == "bad" {
			return errors.New("boom")
		}
		return nil
	}
	cfgs := make([]runConfig, 5)
	for i := range cfgs {
		cfgs[i] = runConfig{model: "good", outDir: filepath.Join("out", string(rune('a'+i)))}
	}
	cfgs[2].model = "bad"
	err := runMatrix(cfgs, 2)
	if ran.Load() != 5 {
		t.Errorf("ran %d configurations, want 5", ran.Load())
	}
	if peak.Load() > 2 {
		t.Errorf("peak concurrency %d, want at most 2", peak.Load())
	}
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want the failing configuration's error", err)
	}
}

func TestRunMatrixCommandRejectsPerRunFlags(t *testing.T) {
	t.Parallel()
	err := runMatrixCommand([]string{"--model", "x", "--out", t.TempDir(), "--version", "a=/bin/true", "--models", "m"})
	if err == nil || !strings.Contains(err.Error(), "--model") {
		t.Fatalf("err = %v, want a refusal naming --model", err)
	}
}

func TestRunMatrixCommandRefusesIncompleteInput(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	run := []string{"--out", out, "--version", "a=/bin/a", "--models", "m"}
	for _, c := range []struct {
		args []string
		want string
	}{
		{append([]string{"--evener-bin", "/bin/x"}, run...), "--evener-bin"},
		{append([]string{"--build"}, run...), "--build"},
		{append([]string{"--harness", "live"}, run...), "--harness"},
		{[]string{"--version", "a=/bin/a", "--models", "m"}, "--out is required"},
		{append([]string{"--max-concurrent", "0"}, run...), "--max-concurrent must be at least 1"},
		{[]string{"--out", out, "--models", "m"}, "at least one --version"},
		{[]string{"--out", out, "--version", "a=/bin/a", "--models", " , "}, "one model in --models"},
		{[]string{"--out", out, "--version", "nolabel", "--models", "m"}, "want LABEL=VALUE"},
	} {
		if err := runMatrixCommand(c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("runMatrixCommand(%q) = %v, want an error containing %q", c.args, err, c.want)
		}
	}
}

// TestRunMatrixCommandRefusesCellsThatCollideOrHoldResults: two runs sharing a
// directory, or a run into one that already holds results, would mix runs that
// prose-stats and review-pack then read as one.
func TestRunMatrixCommandRefusesCellsThatCollideOrHoldResults(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	mustWrite(t, filepath.Join(out, "v1-A", "m2", "results.jsonl"), "{}\n")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--out", out, "--version", "v0=/bin/a", "--models", "m1,m1"}, "share"},
		{[]string{"--out", out, "--version", "v0=/bin/a", "--models", "a/b,a-b"}, "share"},
		{[]string{"--out", out, "--version", "v1-A=/bin/a", "--models", "m1,m2"}, "already holds results"},
	} {
		if err := runMatrixCommand(c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("runMatrixCommand(%q) = %v, want an error containing %q", c.args, err, c.want)
		}
	}
}

func TestRunSuiteRefusesAnOutDirThatHoldsResults(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	mustWrite(t, filepath.Join(out, "results.jsonl"), "{}\n")
	cfg := runConfig{repetitions: 1, maxRounds: 1, harness: "cli", outDir: out, evenerBin: filepath.Join(out, "missing-evener")}
	if err := runSuiteWithConfig(cfg); err == nil || !strings.Contains(err.Error(), "already holds results") {
		t.Fatalf("runSuiteWithConfig = %v, want a refusal", err)
	}
}

// TestRunMatrixCommandRunsEveryPairOnTheCLIHarness: each version-model pair
// reaches the suite runner on the CLI harness with the shared run flags. Not
// parallel: it replaces the package's suite runner.
func TestRunMatrixCommandRunsEveryPairOnTheCLIHarness(t *testing.T) {
	var mu sync.Mutex
	var got []runConfig
	orig := runMatrixSuite
	t.Cleanup(func() { runMatrixSuite = orig })
	runMatrixSuite = func(cfg runConfig) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, cfg)
		return nil
	}
	out := t.TempDir()
	err := runMatrixCommand([]string{"--version", "v1-A=/bin/a", "--models", "lunarouter/m1, lunarouter/m2",
		"--out", out, "--repetitions", "3", "--system-prompt-append", "extra.md"})
	if err != nil {
		t.Fatalf("matrix: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ran %d configurations, want 2", len(got))
	}
	for _, cfg := range got {
		if cfg.harness != "cli" || cfg.evenerBin != "/bin/a" || cfg.repetitions != 3 || !slices.Equal(cfg.systemPromptAppend, []string{"extra.md"}) {
			t.Errorf("config %+v, want the cli harness, /bin/a, 3 repetitions, and extra.md appended", cfg)
		}
	}
}

// TestRunMatrixCommandInheritOperatorEnvFlowsToEachCell pins that
// --inherit-operator-env, given once on the matrix invocation, reaches every
// version/model cell's runConfig (#3227): the flag decides the whole
// process's hermetic env once, not per cell, but each cell's cliProbeArgs
// still needs its own copy to decide whether to add --enabled-plugins. Not
// parallel: it replaces the package's suite runner.
func TestRunMatrixCommandInheritOperatorEnvFlowsToEachCell(t *testing.T) {
	// --inherit-operator-env unsets the variable; t.Setenv restores it.
	t.Setenv(envvars.EVENERNoUserSkills.Name, "1")
	var mu sync.Mutex
	var got []runConfig
	orig := runMatrixSuite
	t.Cleanup(func() { runMatrixSuite = orig })
	runMatrixSuite = func(cfg runConfig) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, cfg)
		return nil
	}
	out := t.TempDir()
	err := runMatrixCommand([]string{"--version", "v1-A=/bin/a", "--models", "m1,m2", "--out", out, "--inherit-operator-env"})
	if err != nil {
		t.Fatalf("matrix: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ran %d configurations, want 2", len(got))
	}
	for _, cfg := range got {
		if !cfg.inheritOperatorEnv {
			t.Errorf("config %+v, want inheritOperatorEnv=true", cfg)
		}
	}
}

// TestRunMatrixCommandConfiguresHermeticEnvOnceBeforeDispatch pins that a
// matrix invocation decides EVENER_NO_USER_SKILLS from --inherit-operator-env
// exactly once, before any cell (goroutine) runs, so concurrent cells share
// one setting instead of racing on a per-cell set/restore (#3227). Not
// parallel: it replaces the package's suite runner and mutates process
// environment.
func TestRunMatrixCommandConfiguresHermeticEnvOnceBeforeDispatch(t *testing.T) {
	t.Setenv(envvars.EVENERNoUserSkills.Name, "")
	orig := runMatrixSuite
	t.Cleanup(func() { runMatrixSuite = orig })
	runMatrixSuite = func(cfg runConfig) error { return nil }
	out := t.TempDir()
	if err := runMatrixCommand([]string{"--version", "v1=/bin/a", "--models", "m", "--out", out}); err != nil {
		t.Fatalf("matrix: %v", err)
	}
	if got := envvars.EVENERNoUserSkills.Getenv(); got != "1" {
		t.Fatalf("%s = %q after a default (hermetic) matrix run, want 1", envvars.EVENERNoUserSkills.Name, got)
	}
}
