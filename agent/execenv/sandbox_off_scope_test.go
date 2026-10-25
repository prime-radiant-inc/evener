package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/sandbox"
)

func TestSandboxOffFileScope(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		name := "default"
		if explicit {
			name = "explicit"
		}
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "workspace")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			env := NewLocalExecutionEnvironment(root)
			if explicit {
				env.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeOff}
			}
			t.Cleanup(env.Cleanup)
			for _, path := range []string{filepath.Join(base, "absolute.txt"), "../relative.txt", "local.txt"} {
				if _, err := env.WriteFile(path, "original"); err != nil {
					t.Fatalf("write %q: %v", path, err)
				}
				if _, err := env.EditFile(path, "original", "edited", false); err != nil {
					t.Fatalf("edit %q: %v", path, err)
				}
				abs := path
				if !filepath.IsAbs(abs) {
					abs = filepath.Join(root, path)
				}
				got, err := os.ReadFile(abs)
				if err != nil || string(got) != "edited" {
					t.Fatalf("file %q = %q, %v", abs, got, err)
				}
			}
			source := filepath.Join(base, "patch-source.txt")
			target := filepath.Join(base, "patch-target.txt")
			if err := env.WriteFileRaw(source, []byte("patch"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := env.RenamePath(source, target); err != nil {
				t.Fatal(err)
			}
			got, err := env.ReadFileRaw(target)
			if err != nil || string(got) != "patch" {
				t.Fatalf("raw read = %q, %v", got, err)
			}
			if err := env.RemovePath(target); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("removed target: %v", err)
			}
		})
	}
}

func TestSandboxOffPermissionFailures(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	target := filepath.Join(base, "existing.txt")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A read-only filesystem boundary makes the permission contract deterministic,
	// including when the suite is run as root.
	env := NewLocalExecutionEnvironment(filepath.Join(base, "workspace")).SetFs(afero.NewReadOnlyFs(afero.NewOsFs()))
	for name, mutate := range map[string]func() error{
		"write": func() error { _, err := env.WriteFile(target, "changed"); return err },
		"edit":  func() error { _, err := env.EditFile(target, "original", "changed", false); return err },
		"patch": func() error { return env.WriteFileRaw(target, []byte("changed"), 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("permission failure = %v", err)
			}
		})
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "original" {
		t.Fatalf("failed mutation changed file: %q, %v", got, err)
	}
}

func TestSandboxOffCommandWorkingDirectory(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("pwd is POSIX-only")
	}
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		env := NewLocalExecutionEnvironment(root)
		if explicit {
			env.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeOff}
		}
		t.Cleanup(env.Cleanup)
		for _, cwd := range []string{base, "..", ""} {
			want := base
			if cwd == "" {
				want = root
			}
			want, err := filepath.EvalSymlinks(want)
			if err != nil {
				t.Fatal(err)
			}
			res, err := env.ExecCommand(context.Background(), "pwd", 1000, cwd, nil)
			if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != want {
				t.Fatalf("exec cwd %q = %+v, %v; want %s", cwd, res, err, want)
			}
			res, err = env.ExecArgv(context.Background(), "pwd", nil, 1000, cwd, nil)
			if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != want {
				t.Fatalf("argv cwd %q = %+v, %v; want %s", cwd, res, err, want)
			}
			var out bytes.Buffer
			handle, err := env.StreamCommand(context.Background(), "pwd", cwd, nil, &out)
			if err != nil {
				t.Fatalf("stream cwd %q: %v", cwd, err)
			}
			code, err := handle.Wait()
			if err != nil || code != 0 || strings.TrimSpace(out.String()) != want {
				t.Fatalf("stream cwd %q = %q, %d, %v; want %s", cwd, out.String(), code, err, want)
			}
		}
	}
}

func TestConfinedCommandWorkingDirectory(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		policy        sandbox.ResolvedPolicy
		acceptOutside bool
	}{
		{name: "read-only", policy: sandbox.ResolvedPolicy{Mode: sandbox.ModeReadOnly, Spawned: sandbox.AccessScope{Read: sandbox.ReadAnywhere}}, acceptOutside: true},
		{name: "workspace-write", policy: sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite, Spawned: sandbox.AccessScope{Read: sandbox.ReadAnywhere}}, acceptOutside: true},
		{name: "write-blocked-off", policy: sandbox.ResolvedPolicy{Mode: sandbox.ModeOff, WriteBlocked: true, Spawned: sandbox.AccessScope{Read: sandbox.ReadAnywhere}}, acceptOutside: true},
		{name: "restricted", policy: sandbox.ResolvedPolicy{Mode: sandbox.ModeRestricted}, acceptOutside: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			policy := tc.policy
			if policy.Mode == sandbox.ModeRestricted {
				// A hand-built scope's zero Read is ReadAnywhere; state the
				// restricted scope explicitly so this case tests the read roots.
				policy.Spawned = sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{root}}
			}
			env := NewLocalExecutionEnvironment(root)
			t.Cleanup(env.Cleanup)
			env.Sandbox = &policy
			outside := t.TempDir()

			if tc.acceptOutside {
				if err := env.EnsureCommandWorkingDirectory(outside); err != nil {
					t.Fatalf("cwd outside root was refused: %v", err)
				}
				if got, err := env.resolveCommandWorkingDir(outside); err != nil || got != outside {
					t.Fatalf("resolved outside cwd = %q, %v; want %q", got, err, outside)
				}
				if res, err := env.ExecArgv(context.Background(), executable, []string{"-test.run=^$"}, 5000, outside, nil); err != nil || res.ExitCode != 0 {
					t.Fatalf("allowed outside argv = %+v, %v", res, err)
				}
				if _, err := env.DetachCommand(context.Background(), "exit 0", outside, nil); err != nil && !errors.Is(err, ErrDetachUnsupported) {
					t.Fatalf("detached outside cwd = %v, want started or ErrDetachUnsupported", err)
				}
			} else {
				boundaryErr := env.EnsureCommandWorkingDirectory(outside)
				if boundaryErr == nil {
					t.Fatal("restricted cwd outside its read roots was accepted")
				}
				assertBoundaryError := func(err error) {
					t.Helper()
					if err == nil || !strings.HasSuffix(err.Error(), boundaryErr.Error()) {
						t.Fatalf("cwd error = %v; want containment error %v", err, boundaryErr)
					}
				}
				_, err := env.resolveCommandWorkingDir(outside)
				assertBoundaryError(err)
				_, err = env.ExecCommand(context.Background(), "exit 0", 1000, outside, nil)
				assertBoundaryError(err)
				_, err = env.ExecArgv(context.Background(), executable, []string{"-test.run=^$"}, 5000, outside, nil)
				assertBoundaryError(err)
				_, err = env.StreamCommand(context.Background(), "exit 0", outside, nil, &bytes.Buffer{})
				assertBoundaryError(err)
				_, err = env.DetachCommand(context.Background(), "exit 0", outside, nil)
				assertBoundaryError(err)
			}

			if res, err := env.ExecArgv(context.Background(), executable, []string{"-test.run=^$"}, 5000, env.RootDir, nil); err != nil || res.ExitCode != 0 {
				t.Fatalf("allowed argv = %+v, %v", res, err)
			}
			if got, err := env.resolveCommandWorkingDir(""); err != nil || got != env.RootDir {
				t.Fatalf("default cwd = %q, %v", got, err)
			}
		})
	}
}

func TestSandboxOffDetachedWorkingDirectory(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("detached execution is unsupported")
	}
	for _, explicit := range []bool{false, true} {
		base := t.TempDir()
		root := filepath.Join(base, "workspace")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		env := NewLocalExecutionEnvironment(root)
		if explicit {
			env.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeOff}
		}
		t.Cleanup(env.Cleanup)
		for caseIndex, cwd := range []string{base, "..", ""} {
			marker := fmt.Sprintf("detached-cwd-%d.txt", caseIndex)
			want := base
			if cwd == "" {
				want = root
			}
			receipt, err := env.DetachCommand(context.Background(), "pwd > "+marker, cwd, nil)
			if err != nil {
				t.Fatalf("detach cwd %q: %v", cwd, err)
			}
			select {
			case <-receipt.Done:
			case <-time.After(5 * time.Second):
				t.Fatal("detached process did not exit")
			}
			got, err := os.ReadFile(filepath.Join(want, marker))
			want, resolveErr := filepath.EvalSymlinks(want)
			if err != nil || resolveErr != nil || strings.TrimSpace(string(got)) != want {
				t.Fatalf("detached cwd = %q, %v; want %s, %v", got, err, want, resolveErr)
			}
		}
	}
}
