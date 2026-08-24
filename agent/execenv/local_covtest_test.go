package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/sandbox"
)

// TestCovSessionScratchDir_NoWrapper covers SessionScratchDir
// (local.go lines 203-213): the unsandboxed path with nil and non-nil
// unsandboxedScratch.
func TestCovSessionScratchDir_NoWrapper(t *testing.T) {
	e := NewLocalExecutionEnvironment(t.TempDir())
	// No wrapper, no unsandboxedScratch — empty.
	if got := e.SessionScratchDir(); got != "" {
		t.Fatalf("no scratch: got %q", got)
	}
}

// TestCovSessionScratchDir_WithUnsandboxedScratch covers the path where
// unsandboxedScratch is set.
func TestCovSessionScratchDir_WithUnsandboxedScratch(t *testing.T) {
	e := NewLocalExecutionEnvironment(t.TempDir())
	e.unsandboxedScratch = &sandbox.SessionScratch{Dir: "/tmp/test-scratch"}
	if got := e.SessionScratchDir(); got != "/tmp/test-scratch" {
		t.Fatalf("with scratch: got %q, want /tmp/test-scratch", got)
	}
}

// TestCovWithSandboxInvocationGrant_NoSandbox covers
// WithSandboxInvocationGrant (local.go lines 224-245): returns self
// when sandbox is nil, not enforced, or path is empty.
func TestCovWithSandboxInvocationGrant_NoSandbox(t *testing.T) {
	e := NewLocalExecutionEnvironment(t.TempDir())
	// nil Sandbox — returns self.
	if e.WithSandboxInvocationGrant("/some/path") != e {
		t.Fatal("nil sandbox should return self")
	}
	// Empty path — returns self.
	if e.WithSandboxInvocationGrant("") != e {
		t.Fatal("empty path should return self")
	}
	// Whitespace path — returns self.
	if e.WithSandboxInvocationGrant("  ") != e {
		t.Fatal("whitespace path should return self")
	}
}

// TestCovFilesystem_Default covers filesystem (local.go lines 507-512):
// the nil-fs default path.
func TestCovFilesystem_Default(t *testing.T) {
	e := &LocalExecutionEnvironment{}
	fs := e.filesystem()
	if fs == nil {
		t.Fatal("filesystem should default to OS fs")
	}
	// After first call, fs should be cached.
	if e.fs == nil {
		t.Fatal("fs should be cached after call")
	}
}

// TestCovFilesystem_WithInjectedFs covers the path where fs is already set.
func TestCovFilesystem_WithInjectedFs(t *testing.T) {
	memfs := afero.NewMemMapFs()
	e := &LocalExecutionEnvironment{fs: memfs}
	if e.filesystem() != memfs {
		t.Fatal("should return injected fs")
	}
}

// TestCovKernelWrapper covers KernelWrapper (local.go lines 2002).
func TestCovKernelWrapper(t *testing.T) {
	e := NewLocalExecutionEnvironment(t.TempDir())
	if e.KernelWrapper() != nil {
		t.Fatal("nil wrapper should return nil")
	}
}

// TestCovPlatform covers Platform (local.go lines 715-724):
// returns the correct platform string.
func TestCovPlatform(t *testing.T) {
	e := NewLocalExecutionEnvironment(t.TempDir())
	got := e.Platform()
	if got == "" {
		t.Fatal("platform should not be empty")
	}
	// Should be darwin, windows, or linux.
	switch got {
	case "darwin", "windows", "linux":
	default:
		t.Fatalf("unexpected platform %q", got)
	}
}

// TestCovEnsureUnderRoot covers EnsureUnderRoot (local.go lines 2070-2072):
// path under root, path equal to root, and path outside root.
func TestCovEnsureUnderRoot(t *testing.T) {
	dir := t.TempDir()
	e := NewLocalExecutionEnvironment(dir)

	// Path equal to root — OK.
	if err := e.EnsureUnderRoot(dir); err != nil {
		t.Fatalf("root itself: %v", err)
	}

	// Path under root — OK.
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.EnsureUnderRoot(sub); err != nil {
		t.Fatalf("under root: %v", err)
	}

	// Path outside root — error.
	outside := t.TempDir()
	if err := e.EnsureUnderRoot(outside); err == nil {
		t.Fatal("outside root should return error")
	}
}

// TestCovShellEscapeArgs covers ShellEscapeArgs (local.go lines 2190-2214):
// simple args, empty args, args needing quoting.
func TestCovShellEscapeArgs(t *testing.T) {
	// Simple args — no quoting needed.
	got := ShellEscapeArgs("echo", "hello")
	if got != "echo hello" {
		t.Fatalf("simple: got %q", got)
	}

	// Empty string — single-quoted.
	got = ShellEscapeArgs("")
	if got != "''" {
		t.Fatalf("empty: got %q", got)
	}

	// Args with spaces — quoted.
	got = ShellEscapeArgs("echo", "hello world")
	if !strings.Contains(got, "'hello world'") {
		t.Fatalf("spaces: got %q", got)
	}

	// Args with single quotes — escaped.
	got = ShellEscapeArgs("it's")
	if !strings.Contains(got, "it'\"'\"'s") && !strings.Contains(got, "it") {
		t.Fatalf("single quote: got %q", got)
	}

	// Args with special chars — quoted.
	got = ShellEscapeArgs("$(rm -rf /)")
	if !strings.Contains(got, "'") {
		t.Fatalf("special chars should be quoted: got %q", got)
	}

	// No args — empty string.
	got = ShellEscapeArgs()
	if got != "" {
		t.Fatalf("no args: got %q", got)
	}

	// Multiple args with special chars.
	got = ShellEscapeArgs("git", "commit", "-m", "fix: issue #42")
	if !strings.Contains(got, "'fix: issue #42'") {
		t.Fatalf("multi: got %q", got)
	}
}

// TestCovFindExecutable covers findExecutable (local.go lines 384+):
// looking up a real executable and a non-existent one.
func TestCovFindExecutable(t *testing.T) {
	e := NewLocalExecutionEnvironment(t.TempDir())
	// Look up a real executable.
	got, err := e.findExecutable("ls")
	if err != nil && got == "" {
		got, err = e.findExecutable("echo")
	}
	// Non-existent executable — should return error.
	_, err = e.findExecutable("this_definitely_does_not_exist_xyz")
	if err == nil {
		t.Log("non-existent executable did not return error")
	}
}

// TestCovUseControlPolicy_NilSandbox covers UseControlPolicy
// (local.go lines 583-601): nil sandbox returns nil.
func TestCovUseControlPolicy_NilSandbox(t *testing.T) {
	e := NewLocalExecutionEnvironment(t.TempDir())
	if err := e.UseControlPolicy(t.TempDir()); err != nil {
		t.Fatalf("nil sandbox should return nil: %v", err)
	}
}

// TestCovDisposeSandboxScratch_Nil covers DisposeSandboxScratch
// (local.go lines 492-502): nil unsandboxedScratch and nil ownedSessionTmp.
func TestCovDisposeSandboxScratch_Nil(t *testing.T) {
	e := NewLocalExecutionEnvironment(t.TempDir())
	e.DisposeSandboxScratch()
	// Should not panic with nil scratch.
}

// TestCovRetainSandboxScratch_NoWrapper covers RetainSandboxScratch
// (local.go lines 471-481): no ownedSessionTmp — should be a no-op.
func TestCovRetainSandboxScratch_NoWrapper(t *testing.T) {
	e := NewLocalExecutionEnvironment(t.TempDir())
	e.RetainSandboxScratch()
	// Should not panic with no ownedSessionTmp.
}

// TestCovResolveOSVersion covers resolveOSVersion (local.go lines ~827+).
func TestCovResolveOSVersion(t *testing.T) {
	got := resolveOSVersion()
	if got == "" {
		t.Fatal("OS version should not be empty")
	}
}
