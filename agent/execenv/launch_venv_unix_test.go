//go:build unix

package execenv

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Every launcher puts the working directory's and the root's virtualenv bin
// directory on the command's PATH, and the argv launchers also resolve their
// executable through it. A tool that exists only in the project's .venv/bin
// must therefore run by bare name from each of ExecCommand, ExecArgv,
// StreamCommand and DetachCommand.
func TestLaunchersResolveBareNamesThroughTheProjectVenv(t *testing.T) {
	newEnv := func(t *testing.T) (env *LocalExecutionEnvironment, marker string) {
		root := realTempDir(t)
		bin := filepath.Join(root, ".venv", "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		tool := "#!/bin/sh\nprintf ran > \"$1\"\n"
		if err := os.WriteFile(filepath.Join(bin, "venvtool"), []byte(tool), 0o755); err != nil {
			t.Fatal(err)
		}
		env = NewLocalExecutionEnvironment(root)
		t.Cleanup(env.Cleanup)
		return env, filepath.Join(root, "marker")
	}
	ran := func(t *testing.T, marker string) {
		t.Helper()
		got, err := os.ReadFile(marker)
		if err != nil || string(got) != "ran" {
			t.Fatalf("venv tool did not run: marker %q, err %v", got, err)
		}
	}
	ctx := context.Background()

	t.Run("ExecCommand", func(t *testing.T) {
		env, marker := newEnv(t)
		if _, err := env.ExecCommand(ctx, "venvtool "+ShellEscapeArgs(marker), 5000, "", nil); err != nil {
			t.Fatal(err)
		}
		ran(t, marker)
	})
	t.Run("ExecArgv", func(t *testing.T) {
		env, marker := newEnv(t)
		if _, err := env.ExecArgv(ctx, "venvtool", []string{marker}, 5000, "", nil); err != nil {
			t.Fatal(err)
		}
		ran(t, marker)
	})
	t.Run("StreamCommand", func(t *testing.T) {
		env, marker := newEnv(t)
		h, err := env.StreamCommand(ctx, "venvtool "+ShellEscapeArgs(marker), "", nil, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.Wait(); err != nil {
			t.Fatal(err)
		}
		ran(t, marker)
	})
	t.Run("DetachCommand", func(t *testing.T) {
		env, marker := newEnv(t)
		if !env.DetachSupported() {
			t.Skip("detached execution is unsupported on this platform")
		}
		p, err := env.DetachCommand(ctx, "venvtool "+ShellEscapeArgs(marker), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		<-p.Done
		ran(t, marker)
	})
}
