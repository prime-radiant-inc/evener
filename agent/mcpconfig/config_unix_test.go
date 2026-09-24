//go:build unix

package mcpconfig

import "testing"

// The expansion table's one real-execution case, on its own behind a unix
// tag: a command expression runs through the host shell and its trimmed
// stdout is the value. The command is POSIX shell syntax (the evaluator
// runs sh -c); the windows executor runs cmd /c and printf does not exist
// there.
func TestExpandEnvVarsRealCommandExecution(t *testing.T) {
	got, err := expandEnvVars("$(printf minted)")
	if err != nil {
		t.Fatalf("expandEnvVars($(printf minted)): %v", err)
	}
	if got != "minted" {
		t.Fatalf("expandEnvVars($(printf minted)) = %q, want minted", got)
	}
}
