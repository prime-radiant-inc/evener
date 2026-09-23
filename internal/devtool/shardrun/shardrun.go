// Package shardrun is the test-binary half of `evener dev <pkg>-shards`: the
// runner splits one package's tests into shards and hands each shard its
// -test.run regex through a file, and a sharded package's TestMain calls
// ConfigureRunFile to apply it.
//
// The regex travels in a file, not on the command line, because a large
// shard's regex can exceed Linux's MAX_ARG_STRLEN (128KB per argument).
package shardrun

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// RunFileEnv names the file holding this shard's -test.run regex.
const RunFileEnv = "EVENER_SHARD_RUN_FILE"

// ConfigureRunFile sets -test.run from the file RunFileEnv names, and does
// nothing when the variable is unset. Call it from TestMain after flag.Parse,
// so the command line's (absent) -test.run cannot clobber it when m.Run parses
// flags.
//
// It consumes the variable: a test that re-execs its own binary as a helper
// passes that helper an explicit -test.run, and a helper that inherited the
// variable would swap it for the whole shard's regex and run the shard again,
// recursively.
func ConfigureRunFile() error {
	runFile, supplied := os.LookupEnv(RunFileEnv)
	if !supplied {
		return nil
	}
	if err := os.Unsetenv(RunFileEnv); err != nil {
		return fmt.Errorf("%s: unset failed: %w", RunFileEnv, err)
	}
	data, err := os.ReadFile(runFile)
	if err != nil {
		return fmt.Errorf("%s %q: read failed: %w", RunFileEnv, runFile, err)
	}
	pattern := strings.TrimSpace(string(data))
	if pattern == "" {
		return fmt.Errorf("%s %q: run regex is empty", RunFileEnv, runFile)
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return fmt.Errorf("%s %q: invalid run regex: %w", RunFileEnv, runFile, err)
	}
	if err := flag.Set("test.run", pattern); err != nil {
		return fmt.Errorf("%s %q: setting test.run failed: %w", RunFileEnv, runFile, err)
	}
	return nil
}

// probeChildEnv marks the re-exec'd child of RequireTestMainAppliesRunFile. It
// is deliberately not an EVENER_* name: a sharded package's TestMain may clear
// those, and a child that lost the mark would re-exec itself again.
const probeChildEnv = "SHARDRUN_PROBE_CHILD"

// RequireTestMainAppliesRunFile proves the calling package's TestMain wires
// ConfigureRunFile in correctly. Without that wiring every shard silently runs
// the whole package, which still passes, so nothing else would notice.
//
// It re-execs the test binary with -test.run=^$ on the command line and a run
// file naming only the calling test. A TestMain that applies the file after
// flag.Parse runs exactly that one test; one that never applies it, or
// applies it before flag.Parse lets the command line win, runs nothing.
func RequireTestMainAppliesRunFile(t *testing.T) {
	t.Helper()
	if os.Getenv(probeChildEnv) != "" {
		return
	}
	runFile := filepath.Join(t.TempDir(), "probe.run")
	if err := os.WriteFile(runFile, []byte("^"+regexp.QuoteMeta(t.Name())+"$"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$", "-test.v=true", "-test.count=1")
	cmd.Env = append(os.Environ(), RunFileEnv+"="+runFile, probeChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe child failed: %v\n%s", err, out)
	}
	var ran []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if name, ok := strings.CutPrefix(line, "=== RUN   "); ok {
			ran = append(ran, name)
		}
	}
	if len(ran) != 1 || ran[0] != t.Name() {
		t.Fatalf("with %s naming only %s, the test binary ran %q; TestMain must call ConfigureRunFile after flag.Parse\n%s", RunFileEnv, t.Name(), ran, out)
	}
}
