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
	"regexp"
	"strings"
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
