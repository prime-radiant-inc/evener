package execenv

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/envvars"
)

// The guards in this file answer one question each: where would bash and a
// direct exec of the same words disagree? Every case is proven against bash,
// the oracle, by running the command string both ways — forced through the
// shell and through ExecCommand's choice — and comparing what the program sees.
// A command's own non-zero exit is a result here, not a test failure.

// guardEnv returns an environment rooted at root whose child inherits inherited,
// and whose factory records the runtime choice while delegating to the real one,
// so the recorded run also produces real output and a real exit status.
func guardEnv(t *testing.T, root string, inherited []string) (*LocalExecutionEnvironment, *recordingCommandFactory) {
	t.Helper()
	env := NewLocalExecutionEnvironment(root)
	factory := &recordingCommandFactory{delegate: systemCommandRuntimeFactory{}}
	env.commandFactory = factory
	env.inheritedEnv = func() []string { return inherited }
	t.Cleanup(env.Cleanup)
	return env, factory
}

// runBothWays runs command through the forced shell and through ExecCommand's
// choice, both rooted at root and in workingDir. It returns the shell result,
// the fast-path result, and the factory that recorded the fast-path route.
//
// The oracle arm goes through execPreparedCommand with env.commands().Shell, the
// way TestExecCommandFastPathMatchesTheShell does. Calling ExecCommand there
// instead would take the fast path too, and comparing the fast path with itself
// proves nothing.
func runBothWays(t *testing.T, root string, inherited []string, command, workingDir string) (shell ExecResult, fast ExecResult, factory *recordingCommandFactory) {
	t.Helper()
	shellEnv, _ := guardEnv(t, root, inherited)
	shell, shellErr := shellEnv.execPreparedCommand(context.Background(), shellEnv.commands().Shell(command), 5000, workingDir, nil)

	env, factory := guardEnv(t, root, inherited)
	fast, fastErr := env.ExecCommand(context.Background(), command, 5000, workingDir, nil)
	if (fastErr == nil) != (shellErr == nil) {
		t.Fatalf("%q: ExecCommand error %v, the shell error %v", command, fastErr, shellErr)
	}
	return shell, fast, factory
}

// TestExecCommandKeepsTheShellForABareNameTheKernelCannotLoad proves finding 2. A
// bare name resolves through the child PATH, and the file it resolves to need
// not be a format execve runs: an executable, shebang-less script (a venv's
// console script, for instance) is run by bash as a shell script, where an argv
// runtime fails with ENOEXEC and the caller sees 127 instead of the script's own
// exit status. The resolved PATH target must pass the same kernelLoadsProgram
// check the path-shaped branch applies.
func TestExecCommandKeepsTheShellForABareNameTheKernelCannotLoad(t *testing.T) {
	t.Parallel()
	if runtimeGOOS == "windows" {
		t.Skip("POSIX exec semantics do not apply on windows")
	}

	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Exits 7 with no shebang: bash runs the contents and reports 7, execve
	// reports ENOEXEC.
	writeExecFixture(t, filepath.Join(bin, "plainscript"), "exit 7\n")
	writeExecFixture(t, filepath.Join(bin, "shebangscript"), "#!/bin/sh\nexit 7\n")
	inherited := fixturePathEnv(bin, "/usr/local/bin", "/usr/bin", "/bin")

	shell, fast, factory := runBothWays(t, root, inherited, "plainscript", "")
	if shell.ExitCode != 7 {
		t.Fatalf("bash did not run the shebang-less script's contents: exit %d stderr %q", shell.ExitCode, shell.Stderr)
	}
	if fast.ExitCode != shell.ExitCode || fast.Stdout != shell.Stdout || fast.Stderr != shell.Stderr {
		t.Fatalf("ExecCommand gave %q/%q exit %d, the shell gave %q/%q exit %d",
			fast.Stdout, fast.Stderr, fast.ExitCode, shell.Stdout, shell.Stderr, shell.ExitCode)
	}
	if len(factory.argv) != 0 {
		t.Fatalf("a bare name resolving to a file the kernel cannot load ran as argv %q, want the shell", factory.argv)
	}

	// The same lookup for a shebang script may still take the fast path, so the
	// guard is the format check and not "a bare name that came from PATH".
	_, fast, factory = runBothWays(t, root, inherited, "shebangscript", "")
	if fast.ExitCode != 7 {
		t.Fatalf("shebang script exit = %d, want 7", fast.ExitCode)
	}
	if len(factory.argv) != 1 || !slices.Equal(factory.argv[0], []string{"shebangscript"}) {
		t.Fatalf("argv = %q, want the shebang script on the fast path", factory.argv)
	}
}

// TestExecCommandKeepsTheShellForARelativePathEntry proves finding 3. bash
// resolves a relative PATH component against the directory the command runs in;
// a lookup done by this process would resolve it against Evener's own directory
// and could fall through to a later absolute entry, running a different program
// entirely. With PATH=bin:<system>, a project-local bin/pwtool and a system
// pwtool, bash runs the project's tool where a process-relative lookup finds the
// system one.
func TestExecCommandKeepsTheShellForARelativePathEntry(t *testing.T) {
	t.Parallel()
	if runtimeGOOS == "windows" {
		t.Skip("POSIX PATH contract does not apply on windows")
	}

	root := t.TempDir()
	projectBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(projectBin, 0o755); err != nil {
		t.Fatal(err)
	}
	systemBin := filepath.Join(t.TempDir(), "systembin")
	if err := os.MkdirAll(systemBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same program name in both places, distinguished by output.
	writeExecFixture(t, filepath.Join(projectBin, "pwtool"), "#!/bin/sh\necho project\n")
	writeExecFixture(t, filepath.Join(systemBin, "pwtool"), "#!/bin/sh\necho system\n")

	// A relative first component and an absolute second one: the shape bash
	// resolves against the command's own directory.
	relativePath := envvars.Path.Name + "=" + strings.Join([]string{"bin", systemBin, "/usr/bin", "/bin"}, string(os.PathListSeparator))
	inherited := []string{relativePath, "HOME=/nonexistent-test-home"}

	shell, fast, factory := runBothWays(t, root, inherited, "pwtool", "")
	if strings.TrimSpace(shell.Stdout) != "project" {
		t.Fatalf("bash did not run the project-local tool: stdout=%q stderr=%q", shell.Stdout, shell.Stderr)
	}
	if fast.Stdout != shell.Stdout || fast.ExitCode != shell.ExitCode {
		t.Fatalf("ExecCommand gave %q exit %d, the shell gave %q exit %d", fast.Stdout, fast.ExitCode, shell.Stdout, shell.ExitCode)
	}
	if len(factory.argv) != 0 {
		t.Fatalf("a command whose PATH holds a relative component ran as argv %q, want the shell", factory.argv)
	}
}

// TestExecCommandKeepsTheShellForAnExportedFunction proves finding 4. $BASH_ENV
// is not the only way a shell function arrives: bash imports an exported
// function definition from a BASH_FUNC_<name>%% environment entry, and the
// default environment filter passes such an entry through. With BASH_ENV unset,
// an argv runtime would run the binary and silently skip the user's wrapper.
func TestExecCommandKeepsTheShellForAnExportedFunction(t *testing.T) {
	t.Parallel()
	if runtimeGOOS == "windows" {
		t.Skip("POSIX shell contract does not apply on windows")
	}

	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecFixture(t, filepath.Join(bin, "shadowtool"), "#!/bin/sh\necho binary\n")

	// The entry bash reads as `shadowtool() { echo shadowed; }`, exactly as an
	// exporting parent shell leaves it. No BASH_ENV is set.
	exported := "BASH_FUNC_shadowtool%%=() {  echo shadowed\n}"
	inherited := append(fixturePathEnv(bin, "/usr/local/bin", "/usr/bin", "/bin"), exported)

	shell, fast, factory := runBothWays(t, root, inherited, "shadowtool", "")
	if strings.TrimSpace(shell.Stdout) != "shadowed" {
		t.Fatalf("bash did not import the exported function: stdout=%q stderr=%q", shell.Stdout, shell.Stderr)
	}
	if fast.Stdout != shell.Stdout || fast.ExitCode != shell.ExitCode {
		t.Fatalf("ExecCommand gave %q exit %d, the shell gave %q exit %d", fast.Stdout, fast.ExitCode, shell.Stdout, shell.ExitCode)
	}
	if len(factory.argv) != 0 {
		t.Fatalf("a command shadowed by an exported function ran as argv %q, want the shell", factory.argv)
	}

	// A function for a different name does not shadow this program, so the fast
	// path stays available.
	other := append(fixturePathEnv(bin, "/usr/local/bin", "/usr/bin", "/bin"), "BASH_FUNC_unrelated%%=() {  echo x\n}")
	env, otherFactory := guardEnv(t, root, other)
	if _, err := env.ExecCommand(context.Background(), "shadowtool", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if len(otherFactory.argv) != 1 || !slices.Equal(otherFactory.argv[0], []string{"shadowtool"}) {
		t.Fatalf("argv = %q, want the fast path for a name no function shadows", otherFactory.argv)
	}
}

// TestExecCommandFastPathMatchesTheShellOnPWD proves finding 5. A command runs in
// the prepared working directory, but the argv child inherits this process's own
// $PWD; bash reconciles PWD with the directory it actually runs in. A tool that
// reads PWD as its project root would otherwise see the wrong one.
func TestExecCommandFastPathMatchesTheShellOnPWD(t *testing.T) {
	t.Parallel()
	if runtimeGOOS == "windows" {
		t.Skip("POSIX shell contract does not apply on windows")
	}
	if _, err := execLookPath("printenv"); err != nil {
		t.Skip("printenv not available")
	}

	root := t.TempDir()
	sub := filepath.Join(root, "project")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	inherited := fixturePathEnv("/usr/local/bin", "/usr/bin", "/bin")

	shell, fast, factory := runBothWays(t, root, inherited, "printenv PWD", sub)
	if len(factory.argv) != 1 {
		t.Fatalf("route = argv %q shell %q, want the fast path", factory.argv, factory.shell)
	}
	if fast.Stdout != shell.Stdout {
		t.Fatalf("fast-path PWD = %q, the shell's PWD = %q", fast.Stdout, shell.Stdout)
	}
	if strings.TrimSpace(fast.Stdout) != sub {
		t.Fatalf("PWD = %q, want the working directory %q", fast.Stdout, sub)
	}
}

// TestReconcilePWDEnv pins the reconciliation itself: it keeps an inherited PWD
// that still names the run directory (preserving a logical spelling reached
// through a symlink, as bash does), replaces one that does not, adds PWD when the
// environment has none, and never leaves two.
func TestReconcilePWDEnv(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	physical, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		env  []string
		dir  string
		want string
	}{
		{"foreign PWD is replaced", []string{"PWD=/elsewhere", "KEEP=1"}, link, physical},
		{"missing PWD is added", []string{"KEEP=1"}, link, physical},
		{"PWD naming the same directory is kept", []string{"PWD=" + link, "KEEP=1"}, link, link},
		{"PWD naming the physical path is kept", []string{"PWD=" + physical, "KEEP=1"}, link, physical},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := reconcilePWDEnv(slices.Clone(tc.env), tc.dir)
			var pwds []string
			for _, kv := range got {
				if value, ok := strings.CutPrefix(kv, pwdEnvPrefix); ok {
					pwds = append(pwds, value)
				}
			}
			if len(pwds) != 1 || pwds[0] != tc.want {
				t.Fatalf("PWD entries = %q, want exactly %q", pwds, tc.want)
			}
			for _, kv := range tc.env {
				if strings.HasPrefix(kv, pwdEnvPrefix) {
					continue
				}
				if !slices.Contains(got, kv) {
					t.Fatalf("reconcilePWDEnv dropped %q from %q", kv, got)
				}
			}
		})
	}
}

// TestExecCommandKeepsTheShellForANonRegularProgram proves finding 6. An
// executable-mode FIFO passes a permission check and then blocks in os.Open,
// which runs before any timeout or cancellation exists to release it. bash
// refuses a FIFO as a command (126), so the fast path must refuse it too — and
// must refuse it without opening it.
func TestExecCommandKeepsTheShellForANonRegularProgram(t *testing.T) {
	t.Parallel()
	if runtimeGOOS == "windows" {
		t.Skip("POSIX file types do not apply on windows")
	}

	root := t.TempDir()
	pipe := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(pipe, 0o755); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// A symlink to a regular, loadable program must still be followed: the check
	// refuses the file type, not the indirection.
	writeExecFixture(t, filepath.Join(root, "real"), "#!/bin/sh\nexit 0\n")
	linkToProgram := filepath.Join(root, "link-to-program")
	if err := os.Symlink(filepath.Join(root, "real"), linkToProgram); err != nil {
		t.Fatal(err)
	}

	if kernelLoadsProgram(pipe) {
		t.Fatal("kernelLoadsProgram accepted a FIFO")
	}
	if !kernelLoadsProgram(linkToProgram) {
		t.Fatal("kernelLoadsProgram refused a symlink to a regular executable")
	}

	// The runtime choice must return without blocking in an open, because that
	// open would happen before any timeout or cancellation exists to release it.
	// bash does not run a FIFO as a program either: with no writer it blocks too,
	// but its block is inside a child the timeout and cancellation can kill,
	// which is exactly what keeping the shell preserves. So the assertion is on
	// the choice, not on a run that would hang on both sides.
	env, factory := guardEnv(t, root, fixturePathEnv("/usr/local/bin", "/usr/bin", "/bin"))
	choice := make(chan struct{})
	go func() {
		defer close(choice)
		env.commandRuntimeForCommand("./pipe", root, env.launchEnv(root, nil))
	}()
	select {
	case <-choice:
	case <-time.After(30 * time.Second):
		t.Fatal("commandRuntimeForCommand(./pipe) did not return: the choice blocked on the FIFO")
	}
	if len(factory.argv) != 0 {
		t.Fatalf("a FIFO program ran as argv %q, want the shell", factory.argv)
	}

	// The symlink to a real program takes the fast path.
	factory.shell, factory.argv = nil, nil
	if _, err := env.ExecCommand(context.Background(), "./link-to-program", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand(./link-to-program): %v", err)
	}
	if len(factory.argv) != 1 || !slices.Equal(factory.argv[0], []string{"./link-to-program"}) {
		t.Fatalf("argv = %q, want the symlinked program on the fast path", factory.argv)
	}
}
