//go:build serffuzz

package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
)

// fuzzRecordingDenyEnv preserves DenyEnv's deterministic, no-subprocess
// boundary while making command dispatch observable to this fuzzer.
type fuzzRecordingDenyEnv struct {
	*agenttest.DenyEnv
	commands []string
}

func (e *fuzzRecordingDenyEnv) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, env map[string]string) (execenv.ExecResult, error) {
	e.commands = append(e.commands, command)
	return e.DenyEnv.ExecCommand(ctx, command, timeoutMS, workingDir, env)
}

func FuzzExpandInertArgumentDirectives(f *testing.F) {
	f.Add("report", uint64(1))
	f.Add("quotes ' and \\\" and $1", uint64(7))
	f.Add("unicode \\x00 tail", uint64(42))

	f.Fuzz(func(t *testing.T, prefix string, seed uint64) {
		dir := t.TempDir()
		env := &fuzzRecordingDenyEnv{
			DenyEnv: &agenttest.DenyEnv{WorkDir: dir, Seed: seed},
		}
		// Every generated argument has both directive forms. They are deliberately
		// absent from the raw template below, so Expand must leave them inert.
		args := strings.ReplaceAll(prefix, "$", "?") + " !`injected command` @injected.txt"

		got, err := Expand(context.Background(), "arguments: $ARGUMENTS", args, env)
		if err != nil {
			t.Fatalf("Expand inert arguments: %v", err)
		}
		if got != "arguments: "+args {
			t.Fatalf("inert expansion = %q, want exact argument text %q", got, "arguments: "+args)
		}
		if len(env.commands) != 0 {
			t.Fatalf("argument-only directives executed commands: %q", env.commands)
		}

		got, err = Expand(context.Background(), "literal !`template command` $ARGUMENTS", args, env)
		if err != nil {
			t.Fatalf("Expand literal command: %v", err)
		}
		if len(env.commands) != 1 || env.commands[0] != "template command" {
			t.Fatalf("literal template command calls = %q, want only template command", env.commands)
		}
		if !strings.HasSuffix(got, " "+args) {
			t.Fatalf("literal command expansion lost or reinterpreted arguments: %q", got)
		}

		if err := os.WriteFile(filepath.Join(dir, "literal.txt"), []byte("literal file contents"), 0o600); err != nil {
			t.Fatalf("write literal fixture: %v", err)
		}
		env.commands = nil
		got, err = Expand(context.Background(), "file @literal.txt $ARGUMENTS", args, env)
		if err != nil {
			t.Fatalf("Expand literal file: %v", err)
		}
		if got != "file literal file contents "+args {
			t.Fatalf("literal file expansion = %q", got)
		}
		if len(env.commands) != 0 {
			t.Fatalf("literal file expansion executed commands: %q", env.commands)
		}
	})
}
