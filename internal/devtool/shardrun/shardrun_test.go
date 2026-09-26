package shardrun

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// restoreRunFlag puts -test.run back after a case changes it, so the cases
// cannot filter the rest of this binary's tests.
func restoreRunFlag(t *testing.T) {
	t.Helper()
	previous := flag.Lookup("test.run").Value.String()
	t.Cleanup(func() { _ = flag.Set("test.run", previous) })
}

func writeRunFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shard.run")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigureRunFileWithoutTheVariableLeavesRunAlone(t *testing.T) {
	restoreRunFlag(t)
	_ = flag.Set("test.run", "^Untouched$")
	t.Setenv(RunFileEnv, "")
	os.Unsetenv(RunFileEnv)
	if err := ConfigureRunFile(); err != nil {
		t.Fatalf("ConfigureRunFile: %v", err)
	}
	if got := flag.Lookup("test.run").Value.String(); got != "^Untouched$" {
		t.Fatalf("test.run = %q, want it untouched", got)
	}
}

func TestConfigureRunFileSetsRunFromTheFile(t *testing.T) {
	restoreRunFlag(t)
	t.Setenv(RunFileEnv, writeRunFile(t, "^(TestA|TestB)$\n"))
	if err := ConfigureRunFile(); err != nil {
		t.Fatalf("ConfigureRunFile: %v", err)
	}
	if got := flag.Lookup("test.run").Value.String(); got != "^(TestA|TestB)$" {
		t.Fatalf("test.run = %q, want the file's regex", got)
	}
}

func TestConfigureRunFileRefusesAnUnusableFile(t *testing.T) {
	for name, path := range map[string]string{
		"missing":       filepath.Join(t.TempDir(), "absent.run"),
		"empty":         writeRunFile(t, " \n"),
		"invalid regex": writeRunFile(t, "^(unclosed"),
	} {
		t.Run(name, func(t *testing.T) {
			restoreRunFlag(t)
			t.Setenv(RunFileEnv, path)
			err := ConfigureRunFile()
			if err == nil || !strings.Contains(err.Error(), RunFileEnv) {
				t.Fatalf("ConfigureRunFile = %v, want an error naming %s", err, RunFileEnv)
			}
		})
	}
}

// TestConfigureRunFileIsNotInheritedByChildren pins that the variable is
// consumed: tests that re-exec their own binary as a helper (with an explicit
// -test.run) would otherwise inherit it, have their TestMain replace that
// -test.run with the whole shard's regex, and run the shard again, recursively.
func TestConfigureRunFileIsNotInheritedByChildren(t *testing.T) {
	restoreRunFlag(t)
	t.Setenv(RunFileEnv, writeRunFile(t, "^TestA$"))
	if err := ConfigureRunFile(); err != nil {
		t.Fatalf("ConfigureRunFile: %v", err)
	}
	if value, set := os.LookupEnv(RunFileEnv); set {
		t.Fatalf("%s still set to %q after ConfigureRunFile; child processes would inherit it", RunFileEnv, value)
	}
}
