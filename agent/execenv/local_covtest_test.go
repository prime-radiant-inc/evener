package execenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/sandbox"
)

func TestCovWithSandboxInvocationGrant_NoGrantNeeded(t *testing.T) {
	tests := []struct {
		name string
		env  *LocalExecutionEnvironment
		path string
	}{
		{
			name: "no sandbox",
			env:  NewLocalExecutionEnvironment(t.TempDir()),
			path: "/some/path",
		},
		{
			name: "sandbox off",
			env: &LocalExecutionEnvironment{
				RootDir: t.TempDir(),
				Sandbox: &sandbox.ResolvedPolicy{Mode: sandbox.ModeOff},
			},
			path: "/some/path",
		},
		{
			name: "empty path",
			env: &LocalExecutionEnvironment{
				RootDir: t.TempDir(),
				Sandbox: &sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite},
			},
			path: "  ",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.env.WithSandboxInvocationGrant(tc.path); got != tc.env {
				t.Fatalf("WithSandboxInvocationGrant(%q) returned %T %p, want original env %p", tc.path, got, got, tc.env)
			}
		})
	}
}

func TestCovFilesystem_DefaultUsesHostFilesystem(t *testing.T) {
	dir := t.TempDir()
	e := &LocalExecutionEnvironment{}
	path := filepath.Join(dir, "written-through-default-fs")
	const want = "host payload"
	if err := afero.WriteFile(e.filesystem(), path, []byte(want), 0o600); err != nil {
		t.Fatalf("write through default filesystem: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read host file: %v", err)
	}
	if string(got) != want {
		t.Fatalf("host file = %q, want %q", got, want)
	}
	if e.filesystem() != e.fs {
		t.Fatal("filesystem() did not reuse its initialized filesystem")
	}
}

func TestCovFilesystem_UsesInjectedFilesystem(t *testing.T) {
	memfs := afero.NewMemMapFs()
	e := &LocalExecutionEnvironment{fs: memfs}
	hostDir := t.TempDir()
	path := filepath.Join(hostDir, "only-in-memfs")
	const want = "memory payload"
	if err := afero.WriteFile(e.filesystem(), path, []byte(want), 0o600); err != nil {
		t.Fatalf("write through injected filesystem: %v", err)
	}
	got, err := afero.ReadFile(memfs, path)
	if err != nil {
		t.Fatalf("read injected filesystem: %v", err)
	}
	if string(got) != want {
		t.Fatalf("injected file = %q, want %q", got, want)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("injected filesystem write leaked to host: os.Stat error = %v", err)
	}
}

func TestCovEnsureUnderRoot(t *testing.T) {
	dir := t.TempDir()
	e := NewLocalExecutionEnvironment(dir)

	if err := e.EnsureUnderRoot(dir); err != nil {
		t.Fatalf("root itself: %v", err)
	}

	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.EnsureUnderRoot(sub); err != nil {
		t.Fatalf("under root: %v", err)
	}

	outside := t.TempDir()
	if err := e.EnsureUnderRoot(outside); err == nil {
		t.Fatal("outside root should return error")
	}
}

func TestCovShellEscapeArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "simple", args: []string{"echo", "hello"}, want: "echo hello"},
		{name: "empty argument", args: []string{""}, want: "''"},
		{name: "space", args: []string{"echo", "hello world"}, want: "echo 'hello world'"},
		{name: "single quote", args: []string{"it's"}, want: `'it'"'"'s'`},
		{name: "shell syntax", args: []string{"$(rm -rf /)"}, want: "'$(rm -rf /)'"},
		{name: "no arguments", args: nil, want: ""},
		{name: "multiple arguments", args: []string{"git", "commit", "-m", "fix: issue #42"}, want: "git commit -m 'fix: issue #42'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShellEscapeArgs(tc.args...); got != tc.want {
				t.Fatalf("ShellEscapeArgs(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
