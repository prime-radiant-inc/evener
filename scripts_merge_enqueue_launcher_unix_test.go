//go:build unix

package evener_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/testwait"
)

// resolvePath returns path with every symlink and relative component resolved.
//
// The launcher's scratch library canonicalizes TMPDIR with `cd ... && pwd -P`
// (scripts/lib/scratch-lib.sh), because on macOS $TMPDIR lives behind the
// /var -> /private/var symlink. A case that matched processes by the raw TMPDIR
// would search for a path the launcher never uses: the build's argv carries the
// canonical one, so detection would never fire and cleanup would sweep the wrong
// string, leaving a FIFO-blocked build in its own process group. Both sides go
// through this function instead, so the comparison is path identity, not string
// identity.
func resolvePath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		t.Fatalf("absolutize %s: %v", resolved, err)
	}
	return absolute
}

// resolvePathAfterCreate creates path, then returns its canonical spelling.
//
// The fixture has to give the launcher the path the launcher will use. The
// launcher resolves TMPDIR and the roots it is handed, so a case that keeps the
// pre-resolution spelling scans for a string the build's argv never contains:
// readiness never fires, cleanup sweeps the wrong path, and a FIFO-blocked build
// outlives the test in a group of its own. Both sides go through this so they
// agree on one spelling.
func resolvePathAfterCreate(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	return resolvePath(t, path)
}

// mergeEnqueueLauncher is the script under test. These cases drive it as an
// operator does -- a real process, a real signal at its PID or at its process
// group, its real exit status -- because the defects they pin are about process
// lifetime and cancellation, which no in-process seam can show.
//
// The helper is supplied through EVENER_DEV_BIN, the script's own documented
// seam for "run this binary instead of building one". Nothing is put on PATH:
// the fixture stands in for the helper binary the launcher would have built, and
// what these cases pin is the launcher's signal ordering, not the helper's
// decisions (internal/devtool/mergeq pins those, and cmd/evener-dev/mergeq.go
// pins that the helper's own NotifyContext turns a TERM into 130).
//
// Two rules hold in every case here, because a test for a leak must not leak:
//
//   - The launcher starts in its own process group, and t.Cleanup KILLs that
//     group plus every fixture pid the case learned. A failing assertion
//     therefore cannot leave a poller behind on the machine.
//   - Every wait for the launcher is bounded. An unbounded Wait on a
//     launcher that ignored the signal would stall the package until the global
//     timeout, which reads as an unrelated hang instead of the regression.
const mergeEnqueueLauncher = "scripts/ops/merge-enqueue.sh"

// TestMergeEnqueueLauncherLeavesNothingRunningAfterCancellation is the leak the
// review's own reproduction found: three real `evener-dev dev merge-enqueue
// --pr 6499 --wait 570` processes, reparented to PID 1 and still polling more
// than an hour after the tests that started them had finished. Each had a real
// `gh` child. PR 6499 happened to be merged, so the re-enqueue was a no-op; an
// unmerged PR would have been put in the merge queue by a test run.
//
// The property is stated as the operator sees it: after the case ends, no
// process from it survives. Two oracles, both structural rather than textual:
//
//   - the helper the launcher ran is the fixture, proven by the fixture's own
//     marker in its argv. A real helper names `merge-enqueue --repo
//     prime-radiant-inc/evener` and writes no such marker, so this could not
//     pass with the real one.
//   - cleanup leaves nothing behind, checked by scanning for the case's own
//     marker after the group kills rather than by observing that the direct
//     child exited. A kill of the direct child cannot reach a helper that leads
//     a group of its own, so a test tearing down that way leaks the tree. The
//     fixture's grandchild stands in for exactly that shape: it ignores TERM,
//     so only a group signal stops it.
func TestMergeEnqueueLauncherLeavesNothingRunningAfterCancellation(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap 'printf "helper-termed\n" >> "$EVENER_FIXTURE_LOG"; exit 130' TERM
"$EVENER_FIXTURE_GRANDCHILD" &
grandchild=$!
printf 'helper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
printf 'grandchild-pid=%s;pgid=%s\n' "$grandchild" "$(ps -o pgid= -p "$grandchild" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	// A grandchild the fixture does not signal itself: it survives a TERM to
	// the fixture's PID alone, which is exactly the shape that outlived the
	// review's tests.
	grandchild := filepath.Join(f.dir, "grandchild.sh")
	writeTestFile(t, grandchild, []byte(`#!/usr/bin/env bash
trap '' TERM
while :; do sleep 0.05; done
`), 0o755)
	var helperPID int
	var grandchildPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env, "EVENER_FIXTURE_GRANDCHILD="+grandchild)
	runLauncher(t, cmd, f, &helperPID, &grandchildPID)
	helperPID, helperGroup := logPIDAndGroup(t, f, "helper-pid=")
	_, grandchildGroup := logPIDAndGroup(t, f, "grandchild-pid=")
	if grandchildGroup != helperGroup {
		t.Fatalf("the fixture grandchild is in group %d, want the helper's group %d: this case needs it reachable only by a group signal", grandchildGroup, helperGroup)
	}
	// The helper is the fixture, not the real one. EVENER_DEV_BIN is the only
	// way the launcher can have started this pid, and it is a path inside this
	// case's fixture directory.
	helperCmd := commandLineOf(t, helperPID)
	if !strings.Contains(helperCmd, f.path) {
		t.Fatalf("the launcher ran %q, want the fixture helper at %s: a case that starts the real helper can enqueue a real pull request", helperCmd, f.path)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	// The helper stopped. That is what the launcher owes: it forwards the
	// cancellation and reaps the helper before reclaiming scratch.
	testwait.Until(t, fmt.Sprintf("helper pid %d to terminate", helperPID), func() bool {
		return pidGone(helperPID)
	})
	// The grandchild is deliberately not asserted here. The launcher does not
	// own a helper's descendants -- the real helper's cancelled context is what
	// stops its `gh` -- so requiring this one to die would be asserting the
	// fixture, not the launcher. What must hold is that nothing from the case
	// outlives the case, which is what cleanup's sweep proves: it runs after
	// the group kills and fails naming every survivor.
}

// commandLineOf returns the full argv of pid, or "" when it is gone.
func commandLineOf(t *testing.T, pid int) string {
	t.Helper()
	for _, proc := range scanProcs(t) {
		if proc.pid == pid {
			return proc.cmd
		}
	}
	return ""
}

// launcherExitDeadline bounds how long a case waits for the launcher to exit.
// It is generous against a loaded host and still far below the package's
// ceiling: the paths under test return in well under a second.
const launcherExitDeadline = 30 * time.Second

// fixtureCheckout is a stand-in checkout the launcher can be pointed at: a copy
// of the script and the libraries it sources, and a Go module whose only
// package under ./cmd/evener-dev/bin writes a sentinel and exits.
//
// The point is that the real helper cannot be built here, let alone started:
// this module has no `dev` subcommand, no merge-queue client and no code that
// could run `gh`. A launcher that reached the build path against *this*
// checkout and then ran the result would run the stub, which writes a file and
// exits.
type fixtureCheckout struct {
	root     string
	script   string
	tmp      string
	sentinel string
}

// newFixtureCheckout copies the launcher and its libraries into a fresh
// directory and writes the fixture module.
//
// tmp is the checkout's TMPDIR: the launcher mints its scratch under it, so the
// scratch path appears in the build's argv and the built helper runs from it.
//
// The build is held open by a FIFO source file rather than by a cold cache. A
// cold build is a race -- how long it takes depends on the host and on what the
// developer's cache already holds -- and the old case lost that race in the one
// direction that matters. A FIFO has no writer, so the go command blocks opening
// it and the build stays in flight until the case signals.
func newFixtureCheckout(t *testing.T, tmp string) fixtureCheckout {
	t.Helper()
	return newFixtureCheckoutNamed(t, tmp, "checkout")
}

// newFixtureCheckoutNamed is newFixtureCheckout with the copy's directory named
// by the caller, so a case can put the checkout on a path with a space in it.
func newFixtureCheckoutNamed(t *testing.T, tmp, name string) fixtureCheckout {
	t.Helper()
	return newFixtureCheckoutUnder(t, filepath.Dir(tmp), tmp, name)
}

// newFixtureCheckoutUnder is newFixtureCheckoutNamed with the top of the chain
// the constructor may chmod named explicitly. It exists for a case whose TMPDIR
// sits under a directory the test must not modify -- a real noexec mount, a
// shared ancestor -- because the default would walk up to that directory and
// change its mode.
func newFixtureCheckoutUnder(t *testing.T, chainRoot, tmp, name string) fixtureCheckout {
	t.Helper()
	// The checkout goes under a base whose whole ancestry the launcher's validator
	// accepts, not under `t.TempDir()`.
	//
	// The copy of the launcher and the libraries it sources are files this case
	// then EXECUTES (`bash <copy>`). `t.TempDir()` descends from the ambient TMPDIR,
	// and on a shared TMPDIR without the sticky bit another local user can replace
	// an ancestor of that path between the copy and the exec and have their own
	// script run instead: the fixture would be the privilege escalation it exists to
	// test for. Comparing the copies earlier does not help -- the files are read
	// again by bash.
	//
	// The base is created the same way every other fixture directory is, so it is
	// established rather than inherited: private, trusted and exec-capable, or the
	// case fails by name. A case with a deliberately unusable TMPDIR still passes
	// that TMPDIR as `tmp`; the checkout does not have to live on it, and must not.
	base, why := createPrivateFixtureDir(t, fixtureScratchBase(t))
	if base == "" {
		t.Fatalf("no base to create the fixture checkout under: %s", fixtureRefusalDetail(why))
	}
	// The base is validated a second time here, on purpose, and it is the
	// assertion this constructor exists for. The copy of the launcher below is a
	// script this case EXECUTES, so everything above it must be a path another
	// user cannot replace: bash reads the file again at exec, and a swap between
	// the copy and the exec runs their script instead of the launcher -- in the
	// privilege of whoever ran the tests. `t.TempDir()` descends from the ambient
	// TMPDIR, so on a shared TMPDIR without the sticky bit that path was exactly
	// the one this fixture used. Asking the question here means a future edit that
	// reaches for t.TempDir() again fails by name rather than passing on a machine
	// whose TMPDIR happens to be private.
	if !launcherTrustsPath(t, base) {
		t.Fatalf("the fixture checkout base %s is not a path the launcher's own rule trusts: this case executes a script from it, so another user being able to replace an ancestor of that path is code execution in the test run", base)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root := filepath.Join(base, name)
	c := fixtureCheckout{
		root:     root,
		script:   filepath.Join(root, "scripts", "ops", "merge-enqueue.sh"),
		tmp:      tmp,
		sentinel: filepath.Join(root, "helper-ran"),
	}
	for _, file := range []struct{ src, dst string }{
		{mergeEnqueueLauncher, filepath.Join("scripts", "ops", "merge-enqueue.sh")},
		{filepath.Join("scripts", "lib", "scratch-lib.sh"), filepath.Join("scripts", "lib", "scratch-lib.sh")},
		{filepath.Join("scripts", "lib", "gate-scratch-root.sh"), filepath.Join("scripts", "lib", "gate-scratch-root.sh")},
		{filepath.Join("scripts", "lib", "gate-bounded.sh"), filepath.Join("scripts", "lib", "gate-bounded.sh")},
	} {
		body, err := os.ReadFile(file.src)
		if err != nil {
			t.Fatalf("read %s: %v", file.src, err)
		}
		path := filepath.Join(root, file.dst)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", path, err)
		}
		writeTestFile(t, path, body, 0o755)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "evener-dev", "bin"), 0o755); err != nil {
		t.Fatalf("mkdir fixture package: %v", err)
	}
	// go.mod's directive stays below the toolchain's own version: a newer one
	// makes the go command try to download a toolchain, which would need the
	// network this suite must not touch.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.invalid/fixture\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write fixture go.mod: %v", err)
	}
	main := "package main\n\nimport \"os\"\n\nfunc main() { _ = os.WriteFile(" + strconv.Quote(c.sentinel) + ", []byte(\"ran\"), 0o644) }\n"
	if err := os.WriteFile(filepath.Join(root, "cmd", "evener-dev", "bin", "main.go"), []byte(main), 0o644); err != nil {
		t.Fatalf("write fixture main: %v", err)
	}
	// A FIFO named *.go: the build has to read every source in the package, so
	// it blocks in open until a writer appears. Nothing ever opens it.
	if err := syscall.Mkfifo(filepath.Join(root, "cmd", "evener-dev", "bin", "hold.go"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	// 0700, and the chain up to the test's own temp root with it. `t.TempDir()`
	// inherits the runner's umask, and under umask 0002 it is 0775 --
	// group-writable, which the launcher's chain check rejects. The launcher would
	// then move scratch to $XDG_RUNTIME_DIR and every marker-based assertion in
	// this file would name a path nothing uses: a correct launcher would time out,
	// and a leaked FIFO-blocked build would sit outside every sweep. Both of those
	// happened while this round was being written. A fixture TMPDIR has to be one
	// the launcher accepts.
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatalf("mkdir fixture TMPDIR: %v", err)
	}
	requirePrivateChain(t, chainRoot, tmp)
	// Both paths are resolved once, after they exist, and every later comparison
	// uses the resolved form. The launcher canonicalizes TMPDIR itself with
	// `pwd -P` (scripts/lib/scratch-lib.sh, written for macOS's
	// /var -> /private/var), so the build's argv carries the canonical path. A
	// case comparing the raw one would wait for a path the launcher never uses,
	// then sweep the same unmatched string in cleanup -- which is how a
	// FIFO-blocked build could survive in its own process group.
	c.root = resolvePath(t, root)
	c.script = filepath.Join(c.root, "scripts", "ops", "merge-enqueue.sh")
	c.sentinel = filepath.Join(c.root, "helper-ran")
	c.tmp = resolvePath(t, tmp)
	return c
}

// requireIsolated states this checkout's two isolation facts, so a case that
// drifts back to the repository's own checkout fails at once with the reason
// instead of timing out looking for a build.
//
//   - The script under test is the fixture copy. It is byte-identical to the
//     repository's script, so what runs is the real launcher; only the checkout
//     around it is a stand-in.
//   - The only package ./cmd/evener-dev/bin can find here is the fixture's
//     stub. The repository's helper lives in the repository's module, and this
//     module's go.mod is the only one on the path, so `go build
//     ./cmd/evener-dev/bin` from here cannot produce it.
func (c fixtureCheckout) requireIsolated(t *testing.T) {
	t.Helper()
	rootPrefix := c.root + string(filepath.Separator)
	if !strings.HasPrefix(c.script, rootPrefix) {
		t.Fatalf("the script under test is %s, want a copy under %s: a case that runs the repository's launcher builds the real helper, and the real helper can enqueue a real pull request", c.script, c.root)
	}
	repo, err := os.ReadFile(mergeEnqueueLauncher)
	if err != nil {
		t.Fatalf("read %s: %v", mergeEnqueueLauncher, err)
	}
	copied, err := os.ReadFile(c.script)
	if err != nil {
		t.Fatalf("read %s: %v", c.script, err)
	}
	if !bytes.Equal(repo, copied) {
		t.Fatalf("%s differs from %s: this case would be testing a script nobody ships", c.script, mergeEnqueueLauncher)
	}
	entries, err := os.ReadDir(filepath.Join(c.root, "cmd", "evener-dev", "bin"))
	if err != nil {
		t.Fatalf("read the fixture package: %v", err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "main.go", "hold.go":
		default:
			t.Fatalf("the fixture package holds %s: the fixture must contain only its stub, or the build could produce something other than it", entry.Name())
		}
	}
}

// foreignOwnedDirectory returns a directory on this machine owned by a uid that
// is neither this user's nor root, or "" when there is none.
//
// Ownership cannot be manufactured without root, so a test that needs a foreign
// owner either finds one or says it has none. Searching the top two levels is
// enough on any normal machine -- every home directory and every system service
// account sits there -- and it keeps the case from walking a filesystem.
func foreignOwnedDirectory() string {
	me := strconv.Itoa(os.Getuid())
	for _, root := range []string{"/home", "/var", "/srv", "/opt", "/run"} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			path := filepath.Join(root, entry.Name())
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() {
				continue
			}
			// Stat gives the owner as a name; ask ls for the number, which is what
			// the rule compares.
			out, err := exec.Command("ls", "-ldn", path).Output()
			if err != nil {
				continue
			}
			fields := strings.Fields(string(out))
			if len(fields) < 3 || fields[2] == me || fields[2] == "0" {
				continue
			}
			return path
		}
	}
	return ""
}

// requirePrivateChain creates dir and makes every directory from root down to it
// private, so the launcher's ancestor rule accepts it. Nothing above root is
// touched: root is the test's own temp directory.
//
// `t.TempDir()` inherits the runner's umask, and the umask here is 0002, so the
// test temp directory comes back 0775 and the ancestor rule rejects it --
// correctly, since a group member can replace an entry in it. A case whose
// premise is "a safe root exists" therefore has to manufacture one rather than
// assume the runner's umask.
func requirePrivateChain(t *testing.T, root, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	for probe := dir; ; probe = filepath.Dir(probe) {
		if reason := privateChainRefusal(probe); reason != "" {
			t.Fatalf("%s: %s", probe, reason)
		}
		if err := os.Chmod(probe, 0o700); err != nil {
			t.Fatalf("make %s private: %v", probe, err)
		}
		if probe == root || filepath.Dir(probe) == probe {
			return
		}
	}
}

// privateChainRefusal reports why makePrivate would be wrong to chmod path, or
// "" when it may.
//
// The reason is the same one that made the base selector validate its parent
// before creating in it: chmod follows a symlink, so chmodding a component
// another user can replace changes the mode of whatever they pointed at -- which
// can be a directory the operator owns, outside the fixture. A test that can do
// that to the operator's filesystem is a defect. Lstat answers about the entry
// itself rather than its target, which is exactly the question.
// fixtureRefusal says why a fixture base was refused, as a value an assertion
// can compare.
//
// The selectors below used to report a sentence, and a subtest compared it to a
// literal copy of that sentence. That pins the wording of a message nothing in
// production emits -- these are test helpers -- so a reworded message broke the
// assertion while the behaviour it stands for was unchanged, and the literal is
// the "asserts on copy" shape the testing guide bans. The code is the contract;
// the sentence is for the failure message.
type fixtureRefusal string

const (
	// refusalNone means the base was accepted.
	refusalNone fixtureRefusal = ""
	// refusalUntrusted means an ancestor or ACL lets another user replace entries.
	refusalUntrusted fixtureRefusal = "untrusted"
	// refusalNoexec means the base is trusted but cannot execute a program.
	refusalNoexec fixtureRefusal = "noexec"
	// refusalNotCreatable means the base accepted the checks and still failed to
	// yield a private directory.
	refusalNotCreatable fixtureRefusal = "not-creatable"
)

// fixtureRefusalDetail renders the operator-facing sentence for a refusal.
func fixtureRefusalDetail(code fixtureRefusal) string {
	switch code {
	case refusalUntrusted:
		return "an ancestor is replaceable by another user"
	case refusalNoexec:
		return "the filesystem cannot execute a program"
	case refusalNotCreatable:
		return "a private directory could not be created there"
	case refusalNone:
		return ""
	}
	return ""
}

func privateChainRefusal(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Sprintf("cannot inspect it: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "it is a symlink, so chmod would follow it and change a directory outside this fixture"
	}
	if !info.IsDir() {
		return "it is not a directory"
	}
	return ""
}

// launcherTrustsPath asks the launcher's own validator whether it would accept
// path, ancestors included, by evaluating the script's functions. It is the
// oracle for "would this fixture's scratch root be used", so a case never has to
// reimplement the rule and drift from it.
//
// The functions come from ONE list, built below, and never from a list written at
// the call site. dir_entry_is_replaceable called acl_getfacl_has_mask and
// acl_getfacl_group_grants_write, and six call sites still sourced only the
// functions they had needed before that change: the predicate then ran with those
// two names undefined, the ACL branch was skipped, and on Linux -- where a
// writable ACL mask with read-only group bits is exactly the shape the mode bits
// misreport -- the fixture rejected a base the launcher accepts. A fixture that
// loads a subset of the validator is not running the validator.
const validatorFuncs = "/^acl_getfacl_grants_foreign_write()/,/^}/p;" +
	"/^acl_getfacl_has_mask()/,/^}/p;" +
	"/^acl_getfacl_group_grants_write()/,/^}/p;" +
	"/^acl_ls_e_grants_foreign_write()/,/^}/p;" +
	"/^dir_acl_grants_foreign_write()/,/^}/p;" +
	"/^dir_entry_is_replaceable()/,/^}/p;" +
	"/^dir_is_owned_by_trusted()/,/^}/p"

// validatorFuncsWithWalk is validatorFuncs plus the walk itself, for the cases
// that ask the whole question rather than one predicate.
const validatorFuncsWithWalk = validatorFuncs + ";/^scratch_path_is_trusted()/,/^}/p"

// runLauncherFunc sources the launcher's own functions into bash and runs expr.
// Everything a fixture asks the launcher's rule goes through here, so there is
// exactly one place where the set of loaded functions is decided.
func runLauncherFunc(t *testing.T, funcs, expr string, args ...string) error {
	t.Helper()
	argv := append([]string{"bash", "-c",
		`eval "$(sed -n '` + funcs + `' "$1")"; ` + expr,
		"bash", mergeEnqueueLauncher}, args...)
	return exec.Command(argv[0], argv[1:]...).Run()
}

// runLauncherFuncStdin is runLauncherFunc for a predicate that reads its input
// from standard input, such as an ACL listing.
func runLauncherFuncStdin(t *testing.T, funcs, expr, stdin string, args ...string) error {
	t.Helper()
	argv := append([]string{"bash", "-c",
		`eval "$(sed -n '` + funcs + `' "$1")"; ` + expr,
		"bash", mergeEnqueueLauncher}, args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.Run()
}

func launcherTrustsPath(t *testing.T, path string) bool {
	t.Helper()
	return runLauncherFunc(t, validatorFuncsWithWalk, `scratch_path_is_trusted "$2"`, path) == nil
}

// launcherCanExecuteIn asks the repository's own probe whether a script written
// to path will run. It is the same function the launcher uses to pass over a
// noexec root, so the fixture cannot disagree with the launcher about what
// "executable" means.
//
// This runs a program, so it belongs after the base has been validated: see
// noexecFixtureProbeDir, which is the only caller that should reach a mkdir or an
// execution, and which validates first for exactly this reason.
func launcherCanExecuteIn(t *testing.T, path string) bool {
	t.Helper()
	err := exec.Command("bash", "-c",
		`eval "$(sed -n '/^gate_scratch_root_executes()/,/^}/p' "$1")"; gate_scratch_root_executes "$2"`,
		"bash", filepath.Join("scripts", "lib", "gate-scratch-root.sh"), path).Run()
	return err == nil
}

// noexecFixtureProbeDir creates a private directory under a real noexec mount
// and returns it, or skips by name when this host cannot supply the condition.
//
// The order of operations is the point, and it is why this is one helper rather
// than the same six lines in three cases. The fixtures here create a directory,
// chmod it, write a script into it and execute that script -- and all of that
// used to happen before anything asked whether the base could be trusted. A
// fixture that runs a program from a directory it never validated is the same
// defect the launcher was fixed for: a check that comes after the first use is
// not a check. So the base is resolved and validated against the launcher's own
// chain rule first, and only a base that passes gets a mkdir, a chmod, a write or
// an exec beneath it.
//
// The later validation of the created directory is still done by each case: the
// base being trustworthy does not say the new entry is, and the case's premise is
// about that entry.
//
// The returned string is empty and the reason says why when the host cannot
// supply the condition. It is a value rather than only a skip so a case can
// assert that an untrusted base created nothing -- which is the ordering this
// helper exists to hold, and the thing the pre-fix shape got wrong.
func noexecFixtureProbeDir(t *testing.T, noexecRoot, marker string) (dir, reason string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(noexecRoot)
	if err != nil {
		return "", fmt.Sprintf("cannot resolve %s (%v)", noexecRoot, err)
	}
	// Before the mkdir, the chmod, the probe write and the probe run. A base that
	// is replaceable makes every one of those a write into somebody else's
	// directory, and the probe in particular would run their file.
	if !launcherTrustsPath(t, base) {
		return "", base + " does not pass the chain rule on this host, so a fixture under it would be about trust and not execution"
	}
	dir, err = os.MkdirTemp(base, "merge-enqueue-noexec.")
	if err != nil {
		return "", fmt.Sprintf("cannot create a directory under %s (%v): this host offers no writable noexec mount", base, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:govet // a new err here is the chmod's, not the named return
		t.Fatalf("make %s private: %v", dir, err)
	}
	// The premise, checked rather than assumed: a script written here does not run.
	//
	// The probe records that it ran, when the caller asked for that, because "did
	// this execute" is otherwise only observable as an exit status the helper eats.
	// A marker survives the directory's removal, so a caller can ask the question
	// after the fact instead of racing the cleanup.
	body := "#!/bin/sh\nexit 0\n"
	if marker != "" {
		body = "#!/bin/sh\nprintf 'ran\\n' > " + shellquote.Literal(marker) + "\nexit 0\n"
	}
	probe := filepath.Join(dir, "probe.sh")
	writeTestFile(t, probe, []byte(body), 0o700)
	if err := exec.Command(probe).Run(); err == nil {
		_ = os.RemoveAll(dir)
		return "", dir + " executes after all: this host cannot supply the noexec condition"
	}
	return dir, ""
}

// TestMergeEnqueueLauncherNoexecFixturesValidateTheirBaseFirst pins the order of
// operations in the noexec fixtures: the base is validated before anything is
// created, chmodded or executed beneath it.
//
// The fixtures under /run/lock used to run mkdir, chmod and a probe script
// first, and only then ask whether that base was trustworthy. That is the
// launcher's own defect in miniature -- a check that comes after the first use is
// not a check -- and it has a live consequence for the probe in particular, which
// executes a file it wrote into a directory it never validated. A base another
// user can replace turns that write into their directory and that exec into
// their file.
//
// The oracle is a base the launcher's own rule rejects, with its entries counted
// before and after: a refused base must be refused with nothing created in it.
// Counting is the assertion, because a helper that created and then cleaned up
// would leave an error-only check looking right.
func TestMergeEnqueueLauncherNoexecFixturesValidateTheirBaseFirst(t *testing.T) {
	t.Parallel()
	untrusted := filepath.Join(resolvePath(t, fixtureScratchBase(t)), "noexec-untrusted-base")
	if err := os.MkdirAll(untrusted, 0o700); err != nil {
		t.Fatalf("create %s: %v", untrusted, err)
	}
	if err := os.Chmod(untrusted, 0o777); err != nil {
		t.Fatalf("make %s replaceable: %v", untrusted, err)
	}
	if launcherTrustsPath(t, untrusted) {
		t.Skipf("%s is trusted on this host, so the case cannot show the refusal", untrusted)
	}
	countEntries := func() int {
		entries, err := os.ReadDir(untrusted)
		if err != nil {
			t.Fatalf("read %s: %v", untrusted, err)
		}
		return len(entries)
	}
	before := countEntries()
	marker := filepath.Join(t.TempDir(), "probe-ran")
	dir, reason := noexecFixtureProbeDir(t, untrusted, marker)
	if dir != "" {
		_ = os.RemoveAll(dir)
		t.Fatalf("the helper created %s under %s, a base another user can replace", dir, untrusted)
	}
	if reason == "" {
		t.Fatalf("the helper refused %s without saying why", untrusted)
	}
	if after := countEntries(); after != before {
		t.Fatalf("%s held %d entries before the helper ran and %d after: the refusal left something behind", untrusted, before, after)
	}
	// The probe must not have run either. This is the half that survives cleanup:
	// the helper removes the directory it made, so a count taken afterwards reads
	// the same whether or not something was written and executed in between. A
	// marker written by the probe is still there.
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("the probe ran from %s before that base was validated: an exec under a directory another user can replace is their file, not this fixture's", untrusted)
	}
}

// fixtureScratchBase returns a directory whose full ancestry the launcher
// accepts, and fails the case by name when the machine has none.
//
// `t.TempDir()` is not necessarily such a place. It sits under the ambient
// TMPDIR, and when that is a shared group-writable directory without the sticky
// bit -- the shape a sandbox or a CI runner sometimes supplies -- the launcher
// correctly refuses the whole chain, reaches for a fallback, and every case that
// waited for a build under the fixture times out against a correct launcher. The
// fixture cannot fix ancestors it does not own, so it does the next honest thing:
// it looks for a base whose ancestry it does accept, and when there is none it
// says so at once instead of spending a deadline proving nothing.
func fixtureScratchBase(t *testing.T) string {
	t.Helper()
	return firstTrustedScratchBase(t, []string{
		os.TempDir(),
		os.Getenv("XDG_RUNTIME_DIR"),
		func() string {
			if home := os.Getenv("HOME"); home != "" {
				return filepath.Join(home, ".cache")
			}
			return ""
		}(),
	})
}

// firstTrustedScratchBase returns a private directory under the first candidate
// whose whole ancestry the launcher's own validator accepts.
//
// The fixture has to establish that chain rather than inherit it. `t.TempDir()`
// descends from whatever TMPDIR the runner has, and a shared group-writable one
// fails the launcher's rule. The fixture would then hand the launcher a scratch
// root it refuses, every build-waiting case would wait out its deadline against
// a correct launcher, and the failure would read as a launcher bug.
//
// The validator is the launcher's own function, extracted and run in bash, so
// the fixture cannot drift from the rule it is trying to satisfy. A candidate
// that is rejected is named in the failure: "no base this machine offers" and
// "the base was fine but something else broke" must not look alike.
//
// createPrivateFixtureDir makes a 0700 directory under base and returns it, or
// returns the reason the base cannot be used and nothing created.
//
// The order in this function IS the finding it fixes. `chmod` follows symlinks,
// so on a directory another user can replace entries in, the entry this creates
// can be swapped between the create and the chmod and the chmod then lands
// wherever they pointed it -- a directory the operator owns, changed by a test
// run. Validating the base first means no entry is ever created somewhere
// another user controls, and the chmod can only reach a directory nobody else
// can move. `os.MkdirTemp` already creates with 0700, so the chmod is a
// belt-and-braces step, not the thing privacy depends on; it is kept because a
// umask the caller cannot control is exactly the kind of ambient state this
// suite has to stop trusting.
//
// The base must also execute: it is the mount every fixture that launches a
// script lives on, so a noexec base moves the failure to the script's start,
// far from the cause.
func createPrivateFixtureDir(t *testing.T, base string) (string, fixtureRefusal) {
	t.Helper()
	if !launcherTrustsPath(t, base) {
		return "", refusalUntrusted
	}
	if !launcherCanExecuteIn(t, base) {
		return "", refusalNoexec
	}
	dir, err := os.MkdirTemp(base, "merge-enqueue-fixture.")
	if err != nil {
		return "", refusalNotCreatable
	}
	// Lstat, not Stat: IsDir on an lstat result is false for a symlink, so this
	// single condition rejects a created entry that is not the directory the
	// chmod below expects.
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		_ = os.RemoveAll(dir)
		return "", refusalNotCreatable
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("make %s private: %v", dir, err)
	}
	return dir, refusalNone
}

// A usable base is established on RESOLVED paths and holds all three of the
// launcher's requirements at once, which is why there is one predicate rather
// than one check per property:
//
//   - resolved: a symlinked candidate is judged by where it lands, because that
//     is the path the launcher canonicalizes and then uses. Checking the
//     spelling instead rejects a trusted destination (macOS's /var ->
//     /private/var) and accepts a symlink whose target nobody validated.
//   - trusted: no ancestor and no ACL lets another user replace entries.
//   - executable: the launcher builds a helper here and runs it, so a noexec
//     mount is writable, trusted and useless. Selecting one moves the failure to
//     the fixture helper's start, far from the cause.
//
// Splitting these across call sites is what let the earlier version check trust
// without executability and check the unresolved spelling; the same three hold
// for every fixture that launches a program.
func firstTrustedScratchBase(t *testing.T, candidates []string) string {
	t.Helper()
	var rejected []string
	for _, candidate := range candidates {
		if candidate == "" || !filepath.IsAbs(candidate) {
			continue
		}
		// Resolved before the temporary is made: mkdtemp on a symlinked candidate
		// creates it under the destination anyway, and resolving first means every
		// later check names the directory the launcher will name.
		base, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			rejected = append(rejected, candidate+" (cannot resolve)")
			continue
		}
		// The parent is validated before anything is created in it. `chmod`
		// follows symlinks, and on a shared directory without the sticky bit
		// another user can replace the entry this call creates in the window
		// between the create and the chmod -- so the chmod would land on whatever
		// they pointed it at, which can be a directory the operator owns. A test
		// that can change permissions outside its own fixture is a defect in the
		// test. Asking the launcher's own rule first means nothing is created
		// under a base whose entries another user can swap, and the chmod below
		// then acts on an entry nobody else can move.
		dir, reason := createPrivateFixtureDir(t, base)
		if reason != refusalNone {
			rejected = append(rejected, candidate+" ("+fixtureRefusalDetail(reason)+")")
			continue
		}
		// No second resolution: `dir` descends from the resolved base, which has
		// no symlinked component left by construction. A second EvalSymlinks here
		// would be dead code, and dead code that looks load-bearing is how the
		// resolve below went untested the first time.
		if reason := unusableScratchBase(t, dir); reason == refusalNone {
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			return dir
		}
		rejected = append(rejected, candidate+" ("+fixtureRefusalDetail(unusableScratchBase(t, dir))+")")
		_ = os.RemoveAll(dir)
	}
	t.Fatalf("no scratch base this machine offers is usable: %s; point TMPDIR at an exec-capable directory whose whole ancestry only you (or root) can write, and rerun",
		strings.Join(rejected, ", "))
	return ""
}

// unusableScratchBase reports why the launcher would refuse to build under path,
// or "" when it would accept it. The three requirements are one answer because a
// caller that reads them separately ends up checking two of them.
func unusableScratchBase(t *testing.T, path string) fixtureRefusal {
	t.Helper()
	if !launcherTrustsPath(t, path) {
		return refusalUntrusted
	}
	if !launcherCanExecuteIn(t, path) {
		return refusalNoexec
	}
	return refusalNone
}

// fixtureTmpDir is a fixture TMPDIR under a trusted base, named by the caller so
// a case can put a space or two in it. The fixture constructor privatises the
// chain from its own root down; this is what makes the directories above that
// root acceptable in the first place.
func fixtureTmpDir(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(fixtureScratchBase(t), name)
}

// launcherFixture is a stand-in helper: a script the launcher starts through
// EVENER_DEV_BIN and then signals.
type launcherFixture struct {
	dir    string
	path   string
	log    string
	stdout string
	stderr string
}

// newLauncherFixture writes script as the fake helper and returns its path, the
// file its handlers append to, and the files its output goes to.
//
// Every scratch root the launcher may consult is inside this fixture, and the
// chain down to it is private. Otherwise a run reads the developer's real
// $XDG_RUNTIME_DIR and $HOME: the launcher correctly refuses a TMPDIR other
// users can enter, and the fixture then depends on whichever roots the machine
// happens to have -- passing where one exists and failing where none does, and
// creating a cache directory outside the fixture where HOME is usable. A case
// that wants those roots unavailable sets them explicitly.
func newLauncherFixture(t *testing.T, script string) launcherFixture {
	t.Helper()
	// Resolved, because the launcher canonicalizes the TMPDIR it is handed and
	// the sweep matches against the path the launcher actually used.
	// A base whose whole ancestry the launcher accepts, not whatever the machine
	// put above t.TempDir(): a shared group-writable ancestor makes the launcher
	// refuse the fixture TMPDIR and reach for a fallback, and the case then fails
	// on a correct launcher.
	dir := resolvePath(t, fixtureScratchBase(t))
	f := launcherFixture{
		dir:    dir,
		path:   filepath.Join(dir, "evener-dev"),
		log:    filepath.Join(dir, "log"),
		stdout: filepath.Join(dir, "stdout"),
		stderr: filepath.Join(dir, "stderr"),
	}
	writeTestFile(t, f.path, []byte(script), 0o755)
	requirePrivateChain(t, dir, f.runtimeDir())
	requirePrivateChain(t, dir, f.homeDir())
	return f
}

// lines returns the fixture's handler log, one line per entry. The fixture
// writes values such as the directory it ran from, and a checkout path may hold
// a space: splitting on whitespace would truncate that value at the first one
// and fail a launcher that did the right thing.
func (f launcherFixture) lines(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(f.log)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read fixture log: %v", err)
	}
	text := strings.TrimSuffix(string(body), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// output returns what the launcher wrote, for a failure message.
func (f launcherFixture) output(t *testing.T) string {
	t.Helper()
	var parts []string
	for _, path := range []string{f.stdout, f.stderr} {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
			parts = append(parts, filepath.Base(path)+": "+trimmed)
		}
	}
	return strings.Join(parts, " | ")
}

// launcherEnv is the environment every launcher fixture runs under.
//
// One function rather than the same list in each constructor, so a pin added for
// a finding lands in both and a case cannot accidentally get the developer's
// machine through the half it did not edit.
func (f launcherFixture) launcherEnv() []string {
	return envOverride(os.Environ(),
		"EVENER_DEV_BIN="+f.path,
		"EVENER_FIXTURE_LOG="+f.log,
		"PATH="+os.Getenv("PATH"),
		// TMPDIR is the case's own directory, so a helper the launcher builds
		// and runs carries this case's marker in its argv. That is what lets
		// cleanup find a helper whose PID the case never learned -- including
		// the real one, were a case ever to reach it.
		"TMPDIR="+f.tmpDir(),
		// The fallback roots too, for the same reason: a TMPDIR the launcher
		// rejects must send it to the fixture's own runtime directory, not to
		// the developer's. A case that wants them unusable overrides these.
		"XDG_RUNTIME_DIR="+f.runtimeDir(),
		"HOME="+f.homeDir(),
		// GOTOOLCHAIN is inherited like every other variable, and an explicit one
		// makes a fixture build fail on something no fixture is about. A developer
		// with `GOTOOLCHAIN=go1.99.0` exported -- or a go.mod in a parent directory
		// asking for a release that does not exist -- turns every build into a failed
		// download, and the case fails on a timeout that says nothing about the
		// launcher.
		//
		// `auto` and not `local`. `local` would forbid the switch this repository
		// itself needs: the go on PATH here is 1.22.2 and go.work asks for 1.27, so a
		// fixture that builds the real module would fail with "go.work requires go >=
		// 1.27.0" on a machine that is working correctly. `auto` keeps the
		// repository's own resolutions and refuses only a value the caller exported.
		"GOTOOLCHAIN=auto",
	)
}

// launcherCmd builds the launcher command with the fixture as its helper.
//
// Its output goes to files, not pipes. A pipe is part of what the defect leaves
// open: an orphaned helper inherits the launcher's stdout, so waiting on a pipe
// blocks until the leaked helper exits and the case that should fail on exit 143
// hangs instead of reporting.
//
// The launcher leads its own process group so a case can deliver Ctrl-C's
// signal the way a tty does -- to the group -- without signalling the test
// binary itself, and so cleanup can KILL the group and everything left in it.
func (f launcherFixture) launcherCmd(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", append([]string{mergeEnqueueLauncher}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = f.launcherEnv()
	return cmd
}

// tmpDir is the TMPDIR this fixture's launcher runs under: inside the case's own
// directory, so the scratch path the launcher mints names the case.
func (f launcherFixture) tmpDir() string { return filepath.Join(f.dir, "tmp") }

// runtimeDir is the $XDG_RUNTIME_DIR this fixture's launcher falls back to, and
// homeDir is its $HOME. Both are the fixture's own, so a launcher that rejects
// TMPDIR stays inside the case instead of consulting the machine.
func (f launcherFixture) runtimeDir() string { return filepath.Join(f.dir, "runtime") }
func (f launcherFixture) homeDir() string    { return filepath.Join(f.dir, "home") }

// killGroups KILLs each pid's process group, ignoring ones already gone. It is
// the cleanup path: it must never fail the case it is cleaning up after.
func killGroups(pids ...int) {
	for _, pid := range pids {
		if pid <= 1 {
			continue
		}
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// runLauncher starts cmd with its output redirected, and registers cleanup that
// KILLs the launcher's process group, every fixture pid named so far, and every
// process still naming the fixture directory.
//
// The sweep covers what a pid list cannot: a pid list only holds the processes a
// case managed to learn before its assertion failed, so a helper that published
// nothing, or one the launcher started into a group of its own, is outside every
// list. A tree left that way outlives its test and runs unattended. The fixture directory is unique per case and
// appears in the argv of every process a case starts (the fixture helper by
// path, a build or a real helper by the scratch path under it), so it is the one
// handle that does not depend on the case getting that far.
func runLauncher(t *testing.T, cmd *exec.Cmd, f launcherFixture, fixturePIDs ...*int) {
	t.Helper()
	out, err := os.Create(f.stdout)
	if err != nil {
		t.Fatalf("create launcher stdout: %v", err)
	}
	t.Cleanup(func() { _ = out.Close() })
	errOut, err := os.Create(f.stderr)
	if err != nil {
		t.Fatalf("create launcher stderr: %v", err)
	}
	t.Cleanup(func() { _ = errOut.Close() })
	cmd.Stdout, cmd.Stderr = out, errOut
	// A case may have already put something at the TMPDIR path on purpose -- the
	// no-scratch case uses a regular file so directory creation fails for every
	// caller, root included. Creating the path is only for the cases that did
	// not, and an entry already there is left exactly as the case made it.
	if _, err := os.Lstat(f.tmpDir()); errors.Is(err, os.ErrNotExist) {
		// 0700, like the rest of the fixture: a group-writable TMPDIR is one the
		// launcher correctly refuses, and the case would then run out of the
		// fixture's fallback root instead of the TMPDIR it named.
		requirePrivateChain(t, f.dir, f.tmpDir())
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", mergeEnqueueLauncher, err)
	}
	launcherPID := cmd.Process.Pid
	// Cleanups run last-registered-first, so the sweep is registered first and
	// runs after the kills: it reports what the kills missed.
	t.Cleanup(func() { assertNoSurvivors(t, f.dir) })
	t.Cleanup(func() {
		pids := []int{launcherPID}
		for _, p := range fixturePIDs {
			pids = append(pids, *p)
		}
		killGroups(pids...)
	})
}

// launcherCmdAt is launcherCmd for a copy of the script, which is how the
// build-path case runs the launcher without the real checkout's sources in
// reach.
func (f launcherFixture) launcherCmdAt(t *testing.T, script string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = f.launcherEnv()
	return cmd
}

// scanProcs returns every process on the machine as pid, parent pid, process
// group and command line.
//
// It is `ps -axo`, the form scripts/lib/gate-bounded.sh already uses, because
// `ps --ppid` is a GNU extension that BSD `ps` -- which is what macOS ships, and
// this repo builds Darwin -- rejects with a usage error. The helper that used
// it turned that error into an empty answer, so on a macOS host the case waited
// for a build it could never see and timed out.
//
// Only the three numeric columns are split on whitespace; everything after them
// is the command line verbatim. Splitting the whole row and rejoining it with
// single spaces rewrote the command, so a path with two spaces in a row -- a
// TMPDIR a case deliberately chose -- became a string that could never appear in
// a process's argv. Readiness and cleanup then agreed with each other and with
// nothing on the machine: the case timed out and the process it was looking for
// survived it.
func scanProcs(t *testing.T) []procLine {
	t.Helper()
	out, err := exec.Command("ps", "-ww", "-axo", "pid=,ppid=,pgid=,command=").Output()
	if err != nil {
		t.Fatalf("ps -axo: %v", err)
	}
	var procs []procLine
	for line := range strings.SplitSeq(string(out), "\n") {
		pid, rest, ok := leadingField(line)
		if !ok {
			continue
		}
		ppid, rest, ok := leadingField(rest)
		if !ok {
			continue
		}
		pgid, cmd, ok := leadingField(rest)
		if !ok {
			continue
		}
		pidNum, pidErr := strconv.Atoi(pid)
		ppidNum, ppidErr := strconv.Atoi(ppid)
		pgidNum, pgidErr := strconv.Atoi(pgid)
		if pidErr != nil || ppidErr != nil || pgidErr != nil {
			continue
		}
		procs = append(procs, procLine{
			pid:  pidNum,
			ppid: ppidNum,
			pgid: pgidNum,
			cmd:  cmd,
		})
	}
	return procs
}

// leadingField splits the first whitespace-delimited field off line and returns
// it with the remainder of the line unchanged, so a later field keeps every byte
// it had. It reports false when the line is blank.
//
// strings.Fields plus a strings.Join would be shorter and would rewrite the
// remainder: two spaces in a row would come back as one, which is the defect
// scanProcs documents.
func leadingField(line string) (field, rest string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return "", "", false
	}
	end := strings.IndexAny(trimmed, " \t")
	if end < 0 {
		return trimmed, "", true
	}
	return trimmed[:end], strings.TrimLeft(trimmed[end:], " \t"), true
}

// procLine is one row of scanProcs.
type procLine struct {
	pid, ppid, pgid int
	cmd             string
}

// processesNaming returns the processes whose command line contains marker,
// excluding the test binary itself, which is not a process a case started.
//
// The scan's own `ps` child is not excluded and needs no exclusion: it is handed
// no marker, so its argv cannot match.
func processesNaming(t *testing.T, marker string) []procLine {
	t.Helper()
	var found []procLine
	for _, proc := range scanProcs(t) {
		if proc.pid == os.Getpid() || !strings.Contains(proc.cmd, marker) {
			continue
		}
		found = append(found, proc)
	}
	return found
}

// launcherExited reports whether pid is gone or a zombie -- a child this process
// has not reaped yet answers `kill -0` either way, so a case that wants to know
// "has it finished" has to ask about its state and not its existence.
func launcherExited(pid int) bool {
	out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return true
	}
	state := strings.TrimSpace(string(out))
	return state == "" || strings.HasPrefix(state, "Z")
}

// buildIn returns the pid and process group of the process building the fixture
// checkout, or zeros while there is none.
//
// Identification is positive: the launcher builds into scratch under the
// fixture's TMPDIR, so the build's argv names the fixture checkout. "The
// launcher's first child" was the old test, and it identified a scratch
// preflight child as the build on a correct launcher.
func buildIn(t *testing.T, marker string) (pid, group int) {
	t.Helper()
	for _, proc := range processesNaming(t, marker) {
		if strings.Contains(proc.cmd, "go build") {
			return proc.pid, proc.pgid
		}
	}
	return 0, 0
}

// assertNoSurvivors fails when any process still names marker and KILLs it
// first, so a failing case reports a leak without leaving one.
func assertNoSurvivors(t *testing.T, marker string) {
	t.Helper()
	// A zombie is dead and this process cannot reap it: it is another process's
	// exit this one has no business reporting as a leak.
	var survivors []procLine
	for _, proc := range processesNaming(t, marker) {
		if pidGone(proc.pid) {
			continue
		}
		survivors = append(survivors, proc)
	}
	if len(survivors) == 0 {
		return
	}
	var described []string
	for _, proc := range survivors {
		described = append(described, fmt.Sprintf("%d(pgid %d) %s", proc.pid, proc.pgid, proc.cmd))
	}
	for _, proc := range survivors {
		_ = syscall.Kill(-proc.pgid, syscall.SIGKILL)
		_ = syscall.Kill(proc.pid, syscall.SIGKILL)
	}
	t.Fatalf("left behind after cleanup: %s", strings.Join(described, " | "))
}

// waitExit waits for cmd to exit, bounded. It reports the exit code and true,
// or 0 and false when the deadline passed first -- which is a regression, not a
// hang, and the caller says so.
func waitExit(t *testing.T, cmd *exec.Cmd, within time.Duration) (int, bool) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0, true
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("wait for the launcher: %v", err)
		}
		return exitErr.ExitCode(), true
	case <-time.After(within):
		return 0, false
	}
}

// mustExit waits for cmd and fails with the fixture output when it did not exit
// in time or exited with a status other than want.
func mustExit(t *testing.T, cmd *exec.Cmd, f launcherFixture, want int) {
	t.Helper()
	code, exited := waitExit(t, cmd, launcherExitDeadline)
	if !exited {
		t.Fatalf("launcher still running after %s, want exit %d: the signal did not stop it; %s",
			launcherExitDeadline, want, f.output(t))
	}
	if code != want {
		t.Fatalf("launcher exit %d, want %d; %s", code, want, f.output(t))
	}
}

// signalLauncher sends sig to the launcher's own PID -- the signal the script
// documents for cancelling a background run.
func signalLauncher(t *testing.T, cmd *exec.Cmd, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(cmd.Process.Pid, sig); err != nil {
		t.Fatalf("signal the launcher: %v", err)
	}
}

// signalLauncherGroup sends sig to the launcher's process group, which is how a
// terminal delivers Ctrl-C: every process in the foreground group gets it.
func signalLauncherGroup(t *testing.T, cmd *exec.Cmd, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		t.Fatalf("signal the launcher's process group: %v", err)
	}
}

// pidGone reports whether the pid's process has terminated. A zombie counts:
// the process is dead, and its parent may not have reaped it yet.
func pidGone(pid int) bool {
	state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return true
	}
	trimmed := strings.TrimSpace(string(state))
	return trimmed == "" || strings.HasPrefix(trimmed, "Z")
}

// logPID reads the pid a fixture wrote under prefix, waiting for it to appear.
func logPID(t *testing.T, f launcherFixture, prefix string) int {
	t.Helper()
	var pid int
	testwait.Until(t, "the fixture to publish "+prefix, func() bool {
		pid = pidFromLines(f.lines(t), prefix)
		return pid != 0
	})
	return pid
}

// waitForLine waits until the fixture has recorded want, and fails naming what
// it saw instead. Failing here rather than proceeding is what stops a case from
// signalling a fixture that has not installed its handler yet.
func waitForLine(t *testing.T, f launcherFixture, want string) {
	t.Helper()
	testwait.UntilSeen(t, "the fixture to record "+want, func() (bool, string) {
		lines := f.lines(t)
		return slices.Contains(lines, want), strings.Join(lines, " ")
	})
}

// pidFromLines returns the pid recorded under prefix, or 0.
func pidFromLines(lines []string, prefix string) int {
	for _, line := range lines {
		rest, ok := strings.CutPrefix(line, prefix)
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(rest)
		if err != nil {
			continue
		}
		return pid
	}
	return 0
}

// logValue returns the value the fixture recorded under prefix, or "" when it
// recorded none. The value is the rest of the line verbatim: a path may hold a
// space, and a helper that logged one is not a helper that logged nothing.
func logValue(t *testing.T, f launcherFixture, prefix string) string {
	t.Helper()
	for _, line := range f.lines(t) {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return rest
		}
	}
	return ""
}

// refusalReason reads the machine-readable reason the launcher put in its
// refusal, or "" when it refused without one.
//
// The launcher emits `[reason=<value>]` beside the operator-facing sentence for
// exactly this: the sentence is copy and changes, the value does not. Reading a
// code is not the same as matching copy, so this does not trip the repository's
// error-text-assertions rule, and rewording the advice cannot fail a case.
func refusalReason(t *testing.T, f launcherFixture) string {
	t.Helper()
	body, err := os.ReadFile(f.stderr)
	if err != nil {
		t.Fatalf("read the launcher's stderr: %v", err)
	}
	_, rest, ok := strings.Cut(string(body), "[reason=")
	if !ok {
		return ""
	}
	reason, _, ok := strings.Cut(rest, "]")
	if !ok {
		return ""
	}
	return reason
}

// logPIDAndGroup reads the "pid=N;pgid=G" line a fixture wrote under prefix,
// waiting for it to appear, and returns both.
func logPIDAndGroup(t *testing.T, f launcherFixture, prefix string) (pid, group int) {
	t.Helper()
	testwait.UntilSeen(t, "the fixture to publish "+prefix, func() (bool, string) {
		lines := f.lines(t)
		for _, line := range lines {
			rest, ok := strings.CutPrefix(line, prefix)
			if !ok {
				continue
			}
			pidText, groupText, ok := strings.Cut(rest, ";pgid=")
			if !ok {
				continue
			}
			parsedPID, err := strconv.Atoi(pidText)
			if err != nil {
				continue
			}
			parsedGroup, err := strconv.Atoi(groupText)
			if err != nil {
				continue
			}
			pid, group = parsedPID, parsedGroup
			return true, ""
		}
		return false, strings.Join(lines, " ")
	})
	return pid, group
}

// TestMergeEnqueueLauncherInterruptsAColdBuildInItsOwnGroup pins the launcher's
// cold-build properties. Three invariants hold here, and the case is written so
// it cannot pass without them:
//
//   - The tracked child leads the process group that holds the build's tree, so
//     one signal reaches the build and every compiler it forks, including one
//     forked after stop_command's PID snapshot. The build runs under bash job
//     control for this (scripts/lib/gate-bounded.sh's run_bounded is the same
//     pattern); a background subshell whose child leads a group of its own would
//     make `$!` name a wrapper, and a compiler reparented when `go build` exits
//     would keep the caller's pipes open past the launcher's 130.
//     positively by the fixture's own scratch path in its argv rather than as
//     "the launcher's first child", which is a probe binary during scratch
//     preflight.
//   - No process here can reach a merge-queue helper. The script runs from a
//     fixture checkout whose only buildable package writes a sentinel and exits,
//     and requireIsolated fails the case if the script under test is not that
//     copy. A test that reached the real helper could put a real pull request in
//     the real merge queue from a `go test` run.
//
// The build is held open by a FIFO source file rather than a cold cache: a cold
// build is a race against the host, and this case has to observe the build
// before signalling it.
func TestMergeEnqueueLauncherInterruptsAColdBuildInItsOwnGroup(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckout(t, fixtureTmpDir(t, "tmp"))
	// The marker names the build: the launcher builds into scratch under the
	// fixture's TMPDIR, so the build's argv carries that path and no other
	// process on the machine does. That is positive identification -- the old
	// case asked for "the launcher's first child", which during scratch
	// preflight is whichever probe binary happens to be running.
	marker := c.tmp
	c.requireIsolated(t)
	// The checkout's scratch is outside launcherFixture's directory, so this
	// case sweeps its own markers. Registered before runLauncher, so it runs
	// after the group kills: a build the launcher leaked is reported here
	// rather than left running on the machine.
	for _, m := range []string{c.root, c.tmp} {
		t.Cleanup(func() { assertNoSurvivors(t, m) })
	}
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	// No EVENER_DEV_BIN: this case has to reach the build path. TMPDIR points at
	// the fixture, GOWORK=off keeps the build inside the fixture module even when
	// the fixture sits under a checkout that has a go.work, and GOENV=off drops
	// the developer's saved go env. A saved GOFLAGS such as -modfile=... would
	// otherwise make the build exit before it reaches the FIFO, and the case
	// would wait on a process that had already failed.
	cmd.Env = envOverride(cmd.Env, "TMPDIR="+c.tmp, "GOWORK=off", "GOENV=off")
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	runLauncher(t, cmd, f)
	launcherPID := cmd.Process.Pid
	var buildPID, buildGroup int
	testwait.UntilSeen(t, "the launcher's build to appear", func() (bool, string) {
		buildPID, buildGroup = buildIn(t, marker)
		if buildPID == 0 {
			return false, "no process names the fixture scratch yet"
		}
		return true, ""
	})
	// The tracked child leads the group: kill -TERM -$child reaches the build
	// and every compiler it forks, including one forked after this moment.
	if buildGroup != buildPID {
		t.Fatalf("the build child %d is in group %d: stop_command cannot signal the build's tree by group, so a compiler forked after its PID walk escapes", buildPID, buildGroup)
	}
	if buildGroup == launcherPID {
		t.Fatalf("the build shares the launcher's group %d, so a group stop would signal the launcher too", buildGroup)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	testwait.Until(t, fmt.Sprintf("build pid %d to terminate", buildPID), func() bool {
		return pidGone(buildPID)
	})
	// The stub never ran: the build was cancelled, so there was nothing to
	// start. The sentinel is what distinguishes "the fixture's helper was
	// cancelled" from "something else ran", and no merge-queue helper exists in
	// this checkout to run at all.
	if _, err := os.Stat(c.sentinel); err == nil {
		t.Fatalf("the fixture helper ran: the build finished after all, so this case did not exercise cancellation")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat fixture sentinel: %v", err)
	}
}

// TestMergeEnqueueLauncherForwardsSigtermToAPrebuiltHelper pins that the
// prebuilt path is interruptible: the traps are installed before either launch
// path, so a SIGTERM at the launcher's PID stops the helper and exits 130. A
// handler installed after the helper starts leaves the default action, which
// kills the launcher with 143 while the helper polls until the wait expires,
// holding the caller's output open for the rest of it.
//
// The fixture publishes its pid only after its TERM handler is installed, so a
// correct launcher cannot be failed by signalling a fixture that is still
// setting up.
func TestMergeEnqueueLauncherForwardsSigtermToAPrebuiltHelper(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap 'printf "helper-termed\n" >> "$EVENER_FIXTURE_LOG"; exit 130' TERM
printf 'helper-pid=%s\n' "$$" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	var helperPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	runLauncher(t, cmd, f, &helperPID)
	helperPID = logPID(t, f, "helper-pid=")
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	// The helper ran its own TERM handler: the launcher forwarded the signal
	// rather than dying on the shell's default action.
	waitForLine(t, f, "helper-termed")
	// And it is gone by the time the launcher exits: a forwarded signal that
	// left the poller running would still hold the caller's output open.
	testwait.Until(t, fmt.Sprintf("helper pid %d to terminate", helperPID), func() bool {
		return pidGone(helperPID)
	})
}

// TestMergeEnqueueLauncherRunsTheHelperInItsOwnProcessGroup pins that the helper
// leads a group of its own. Sharing the launcher's group would let a terminal
// Ctrl-C reach the helper's `gh` before the helper's own handler cancelled the
// context, and the helper would then read the interrupted call as an ordinary
// GitHub failure and print a drop verdict with re-enqueue advice for a wait the
// operator cancelled.
//
// The assertion is the process-group structure, not a log line, because a log
// line cannot honestly observe this. A bash fixture forked with `&` from a
// non-interactive shell has SIGINT forced to SIG_IGN before its first command
// runs (SigIgn 6, measured), so `trap ... INT` in the fixture never installs and
// a "did gh receive the SIGINT?" oracle passes whether or not the defect is
// present. What the fix changes, and what this pins, is that the helper leads a
// group of its own: the terminal's foreground group holds the launcher alone, so
// the launcher is the only thing a Ctrl-C touches and the helper's cancelled
// context is what stops `gh`.
//
// It also pins that the helper still ends: the launcher forwards TERM after
// taking the SIGINT, so an operator's Ctrl-C stops the wait.
// TestMergeEnqueueLauncherPublishesItsGroupFileInPrivateScratch pins where the
// file the helper publishes its live `gh` group in is allowed to live.
//
// The handle's name cannot be secret -- the helper has to be told it -- so the
// only thing that keeps another local user from pre-creating that name as a link
// to a file the operator can write is the directory: a `mktemp -d` directory
// only this user can enter, created atomically, not a name directly under a
// shared TMPDIR. A launcher that opens the predictable path itself truncates the
// operator's file once per GitHub call, and the operator never learns why.
//
// The oracle is the handle's own directory: its permission bits, and that it is
// not TMPDIR itself. The second subtest is the other half of the same
// requirement -- if private scratch cannot be created at all, the prebuilt path
// must still run. Hardening that turned into a new failure mode would be worse
// than the hole it closed.
func TestMergeEnqueueLauncherPublishesItsGroupFileInPrivateScratch(t *testing.T) {
	t.Parallel()
	const script = `#!/usr/bin/env bash
set -uo pipefail
printf 'handle=%s\n' "${EVENER_MERGEQ_GH_GROUP_FILE:-}" >> "$EVENER_FIXTURE_LOG"
printf 'helper-pid=%s\n' "$$" >> "$EVENER_FIXTURE_LOG"
printf 'helper-started\n' >> "$EVENER_FIXTURE_LOG"
while [ ! -e "${EVENER_FIXTURE_LOG}.release" ]; do sleep 0.02; done
`
	t.Run("private scratch", func(t *testing.T) {
		f := newLauncherFixture(t, script)
		// Declared before the launcher starts and handed to runLauncher, so the
		// cleanup kills this helper's group as soon as its pid is known. The
		// helper blocks until released, and an assertion that fails first would
		// otherwise leave it polling: a test that leaks the thing it is testing
		// for is worse than the defect, because it hides the next one.
		var helperPID int
		cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
		runLauncher(t, cmd, f, &helperPID)
		helperPID = logPID(t, f, "helper-pid=")
		waitForLine(t, f, "helper-started")
		handle := logValue(t, f, "handle=")
		if handle == "" {
			t.Fatalf("the launcher named no group file for the prebuilt helper; %s", f.output(t))
		}
		dir := filepath.Dir(handle)
		if dir == f.tmpDir() {
			t.Fatalf("the group file is %s directly under TMPDIR: its name is predictable, so on a shared TMPDIR that permits symlink traversal another user can plant a link the launcher truncates through", handle)
		}
		// Inside the case's own directory, not merely "somewhere else". The
		// launcher rejects a TMPDIR another user can enter and falls back to
		// $XDG_RUNTIME_DIR or $HOME/.cache; a fixture that leaves those alone is
		// reading the machine's, so the same case passes on a host with a usable
		// runtime directory and fails on one without, and writes a cache outside
		// the fixture on the way.
		if !strings.HasPrefix(handle, f.dir+string(filepath.Separator)) {
			t.Fatalf("the group file is %s, outside the fixture %s: the launcher used a scratch root this case does not own", handle, f.dir)
		}
		// Read while the helper is still running: the launcher removes the
		// directory with the handle, so after exit there is nothing to stat.
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat the group file's directory %s: %v", dir, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("the group file's directory %s has mode %04o, want 0700: another user can create names in it", dir, perm)
		}
		if err := os.WriteFile(f.log+".release", nil, 0o600); err != nil {
			t.Fatalf("release the fixture helper: %v", err)
		}
		mustExit(t, cmd, f, 0)
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the private directory %s survived the launcher: stat returned %v", dir, err)
		}
	})
	t.Run("no private scratch", func(t *testing.T) {
		f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'handle=%s\n' "${EVENER_MERGEQ_GH_GROUP_FILE:-}" >> "$EVENER_FIXTURE_LOG"
printf 'helper-ran\n' >> "$EVENER_FIXTURE_LOG"
`)
		// TMPDIR is a regular FILE, so `mktemp -d` fails there whatever the
		// caller's privileges are. Refusing by mode does not work: Linux running as
		// root has DAC override and creates the directory inside a 0500 one
		// anyway, so the subtest passed or failed on who ran it. All three roots
		// are denied, because the launcher tries them in turn: leaving
		// $XDG_RUNTIME_DIR usable would let it mint the handle there and the case
		// would be about a different fallback. The prebuilt path must still start
		// the helper -- it publishes nothing to a handle it could not mint, and it
		// does not fail the run. This is the fallback the high finding requires.
		tmp := f.tmpDir()
		if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
			t.Fatalf("create the fixture TMPDIR's parent: %v", err)
		}
		if err := os.WriteFile(tmp, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatalf("write the fixture TMPDIR as a file: %v", err)
		}
		cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
		denied := filepath.Join(f.dir, "not-a-directory")
		if err := os.WriteFile(denied, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatalf("write the denied root: %v", err)
		}
		cmd.Env = envOverride(cmd.Env,
			"XDG_RUNTIME_DIR="+denied,
			"HOME="+filepath.Join(denied, "home"),
		)
		runLauncher(t, cmd, f)
		mustExit(t, cmd, f, 0)
		waitForLine(t, f, "helper-ran")
		if handle := logValue(t, f, "handle="); handle != "" {
			t.Fatalf("the launcher named %s as the group file, but no private scratch could be created there", handle)
		}
	})
}

func TestMergeEnqueueLauncherRunsTheHelperInItsOwnProcessGroup(t *testing.T) {
	t.Parallel()
	// Inside the fixture, not `t.TempDir()`. `t.TempDir()` is under the ambient
	// TMPDIR, and a TMPDIR on a noexec mount -- the ordinary container shape --
	// makes this script unrunnable: the helper starts it, the kernel refuses, and
	// the case waits out its deadline on a call that could never happen. The
	// fixture's own directory is one the case already requires to be exec-capable
	// for its helper, so an executable belongs there.
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap 'printf "helper-interrupted\n" >> "$EVENER_FIXTURE_LOG"; kill -TERM "$gh" 2>/dev/null; exit 130' INT TERM
"$EVENER_FIXTURE_GH" &
gh=$!
printf 'helper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	gh := filepath.Join(f.dir, "gh.sh")
	writeTestFile(t, gh, []byte(`#!/usr/bin/env bash
trap 'printf "gh-termed\n" >> "$EVENER_FIXTURE_LOG"; exit 0' TERM
printf 'gh-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`), 0o755)
	var helperPID, ghPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env, "EVENER_FIXTURE_GH="+gh)
	runLauncher(t, cmd, f, &helperPID, &ghPID)
	launcherGroup := cmd.Process.Pid
	helperPID, helperGroup := logPIDAndGroup(t, f, "helper-pid=")
	ghPID, ghGroup := logPIDAndGroup(t, f, "gh-pid=")
	if helperGroup == launcherGroup {
		t.Fatalf("the helper runs in the launcher's process group (%d): a terminal Ctrl-C would reach its gh directly, which is what lets an interrupted wait report a drop", launcherGroup)
	}
	// The helper owns its `gh`: one signal from the launcher reaches both, and
	// the helper's cancelled context is what ends the call.
	if ghGroup != helperGroup {
		t.Fatalf("the helper's gh is in group %d, want the helper's own group %d", ghGroup, helperGroup)
	}
	signalLauncherGroup(t, cmd, syscall.SIGINT)
	mustExit(t, cmd, f, 130)
	waitForLine(t, f, "helper-interrupted")
	testwait.Until(t, fmt.Sprintf("fixture gh pid %d to terminate", ghPID), func() bool {
		return pidGone(ghPID)
	})
	testwait.Until(t, fmt.Sprintf("helper pid %d to terminate", helperPID), func() bool {
		return pidGone(helperPID)
	})
}

// TestMergeEnqueueLauncherForcedStopTakesTheHelpersGHCallsWithIt pins the second
// half of cancellation: a helper that does not act on the launcher's SIGTERM
// must not leave its `gh` call running after the launcher has reported 130.
//
// The client starts `gh` in a process group of its own (gh.go, so one signal
// reaches the CLI and anything a wrapper forked). That is what makes an
// interrupted wait stop promptly, and it is also why a group signal at the
// helper no longer reaches the call: the helper's group is the only one
// stop_command signals. A helper that never runs its handler therefore leaves a
// live enqueue call behind a launcher that has already returned 130.
//
// The fixture states that shape structurally. The helper ignores TERM, so the
// grace period expires and the launcher's forced stop is the only thing that can
// reach the call, and its `gh` leads a group of its own and ignores TERM as
// well, so only the forced KILL ends it. The assertion is that the call is gone
// after the launcher exits, which a TERM at the helper's group cannot satisfy.
func TestMergeEnqueueLauncherForcedStopTakesTheHelpersGHCallsWithIt(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap '' TERM
set -m
"$EVENER_FIXTURE_GH" &
gh=$!
set +m
printf 'helper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
printf 'gh-pid=%s;pgid=%s\n' "$gh" "$(ps -o pgid= -p "$gh" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	// The call stands in for one that has not acted on TERM: a real `gh` may
	// exit on it, and this case is about the signal that reaches the group
	// whether or not it does.
	gh := filepath.Join(f.dir, "gh.sh")
	writeTestFile(t, gh, []byte(`#!/usr/bin/env bash
trap '' TERM
while :; do sleep 0.05; done
`), 0o755)
	var helperPID, ghPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_GH="+gh,
		// The grace has to expire for this case to reach the forced stop, and the
		// stopper's own grace bounds it. Both are the launcher's own knobs, so the
		// case shrinks them instead of spending its ceiling on them.
		"EVENER_HELPER_TERM_GRACE_SECONDS=0",
		"EVENER_STOP_TREE_GRACE_SECONDS=1",
	)
	runLauncher(t, cmd, f, &helperPID, &ghPID)
	helperPID, helperGroup := logPIDAndGroup(t, f, "helper-pid=")
	ghPID, ghGroup := logPIDAndGroup(t, f, "gh-pid=")
	if ghGroup != ghPID {
		t.Fatalf("the fixture's gh is in group %d, want a group of its own led by %d: the case has to reproduce the shape the client creates", ghGroup, ghPID)
	}
	if ghGroup == helperGroup {
		t.Fatalf("the fixture's gh shares the helper's group %d, so this case would prove nothing about the group the forced stop has to name", helperGroup)
	}
	if pidGone(ghPID) {
		t.Fatalf("the fixture's gh %d exited before the launcher was signalled", ghPID)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	// The launcher reported the interrupt, so nothing it started may still be
	// submitting: a call left in its own group would outlive this line.
	testwait.Until(t, fmt.Sprintf("the helper's gh pid %d to be stopped before the launcher exits 130", ghPID), func() bool {
		return pidGone(ghPID)
	})
}

// TestMergeEnqueueLauncherDoesNotBuildUnderAReplaceableRoot pins the high
// finding on 7ef64c0d67: the launcher must not build and then exec a helper from
// a directory whose entry in its parent another local user can replace.
//
// `mktemp -d` gives the scratch mode 0700, which protects its contents. It says
// nothing about the entry: the parent's owner can rename a 0700 directory away
// whatever its mode is. So on a TMPDIR that is group- or other-writable and not
// sticky, an attacker replaces the whole directory after the build, and the `-x`
// test and the exec both resolve the attacker's file. The launcher then runs it
// with the operator's credentials and its `gh` token.
//
// The oracle is where the scratch was minted, read from the build's own argv:
// the case gives the launcher a replaceable TMPDIR and a safe root, and requires
// the scratch to be under the safe one. The second subtest is the requirement's
// other half -- with no safe root anywhere, the run still happens, because a
// launcher that refuses to merge on an ordinary host is worse than the window.
func TestMergeEnqueueLauncherDoesNotBuildUnderAReplaceableRoot(t *testing.T) {
	t.Parallel()
	t.Run("a safe root is used", func(t *testing.T) {
		// TMPDIR is replaceable: group- and other-writable, no sticky bit. mktemp's
		// own directories are 0700, so the mode is set explicitly -- the case has
		// to look like a shared one for there to be an entry to swap. The checkout
		// is the fixture, and its helper stub is `exit 0`, so this never reaches
		// the real merge helper however the scratch resolves.
		c := newFixtureCheckoutNamed(t, fixtureTmpDir(t, "tmp"), "checkout")
		c.requireIsolated(t)
		if err := os.Chmod(c.tmp, 0o777); err != nil {
			t.Fatalf("make the fixture TMPDIR replaceable: %v", err)
		}
		// The fallback base comes from the fixture selector, not from
		// t.TempDir(). t.TempDir() sits under the ambient TMPDIR, and a runner
		// whose TMPDIR has a group-writable ancestor or lives on a noexec mount
		// makes the launcher skip this fallback -- correctly -- while the case
		// waits for a build the launcher will never start there. The case's
		// premise is "a usable fallback exists"; the selector is what establishes
		// it instead of assuming the runner's.
		safeRoot := resolvePath(t, fixtureScratchBase(t))
		safe := resolvePathAfterCreate(t, filepath.Join(safeRoot, "runtime"))
		requirePrivateChain(t, safeRoot, safe)
		f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
		cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
		cmd.Env = envOverride(cmd.Env,
			"TMPDIR="+c.tmp, "XDG_RUNTIME_DIR="+safe,
			"GOWORK=off", "GOENV=off",
		)
		cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
			return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
		})
		var buildPID int
		// Registered before the launcher starts, because the assertion that fails
		// here is the one that leaves a build behind: the FIFO keeps the build
		// blocked forever, so a case that fails without killing it leaks a
		// `go build` in a process group of its own. That is the defect this whole
		// round is about, and a test for it must not commit it.
		//
		// The sweep by this case's own directory, not only by the recorded pid:
		// the branch that records nothing is exactly the branch the red takes, so
		// a pid list alone would miss the build there and report a clean cleanup
		// while leaving it running.
		t.Cleanup(func() { assertNoSurvivors(t, safe) })
		t.Cleanup(func() { assertNoSurvivors(t, c.root) })
		t.Cleanup(func() { assertNoSurvivors(t, c.tmp) })
		t.Cleanup(func() { killGroups(buildPID) })
		// The build blocks on the fixture's FIFO, so its argv is observable while
		// it is in flight: that is where the scratch path it will write to is
		// named, and reading it there is what proves where the scratch was minted
		// without waiting for the build to finish.
		runLauncher(t, cmd, f)
		testwait.UntilSeen(t, "the fixture build to appear", func() (bool, string) {
			buildPID, _ = buildIn(t, safe)
			if buildPID != 0 {
				return true, ""
			}
			if pid, _ := buildIn(t, c.tmp); pid != 0 {
				// Recorded too, so cleanup can stop it: this is the branch a
				// regression takes, and its build must not outlive the failure.
				buildPID = pid
				return false, "the build names the replaceable TMPDIR, not the safe root"
			}
			return false, "no build names either root yet"
		})
		// Explicit: nothing was minted under the replaceable root. A scratch left
		// there is the directory an attacker swaps.
		entries, err := os.ReadDir(c.tmp)
		if err != nil {
			t.Fatalf("read the fixture TMPDIR: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("the launcher minted scratch under the replaceable TMPDIR (%v): its entry can be replaced by another user", entries)
		}
		// The case is not finished when its assertions are. The launcher it
		// started is still waiting on the FIFO-blocked build, and returning here
		// would leave cleanup to stop a process the case never observed finishing
		// -- judged by whatever the sweep happened to see. So the run is signalled
		// and reaped, exactly as the cold-build case does: 130 is the interrupt
		// this signal means, and the build it was blocked on has to be gone too.
		signalLauncher(t, cmd, syscall.SIGTERM)
		mustExit(t, cmd, f, 130)
		testwait.Until(t, fmt.Sprintf("build pid %d to terminate", buildPID), func() bool {
			return pidGone(buildPID)
		})
	})
	t.Run("a private leaf under a replaceable ancestor is not a trusted root", func(t *testing.T) {
		// The finding this pins: the leaf's mode says nothing about its entry. A
		// per-user TMPDIR of 0700 beneath a shared directory without the sticky
		// bit passes any check that looks only at the leaf, and another user can
		// still replace the shared directory above it and move where every path
		// below resolves -- including the freshly built helper the launcher is
		// about to execute.
		//
		// The oracle is the refusal, not a build. With no usable root anywhere the
		// launcher must stop before it builds or executes anything and say so: exit
		// 2, nothing running, nothing minted. Waiting for a build to appear would
		// only ever pass against the accepting behaviour this case exists to
		// exclude, and it would spend the launcher's whole deadline doing it.
		shared := filepath.Join(resolvePath(t, t.TempDir()), "shared")
		if err := os.MkdirAll(shared, 0o755); err != nil {
			t.Fatalf("create the shared ancestor: %v", err)
		}
		shared = resolvePathAfterCreate(t, shared)
		c := newFixtureCheckoutNamed(t, filepath.Join(shared, "tmp"), "checkout")
		c.requireIsolated(t)
		// After the constructor, not before: it makes the chain private so the
		// launcher accepts the fixture TMPDIR in the ordinary cases, and a case
		// that wants the ancestor replaceable has to say so last or it is testing
		// a directory it made private itself.
		if err := os.Chmod(shared, 0o777); err != nil {
			t.Fatalf("make the ancestor replaceable: %v", err)
		}
		if err := os.Chmod(c.tmp, 0o700); err != nil {
			t.Fatalf("make the fixture TMPDIR private: %v", err)
		}
		f := newLauncherFixture(t, "#!/usr/bin/env bash\nprintf 'helper-ran\\n' >> \"$EVENER_FIXTURE_LOG\"\n")
		// All three candidates denied, so the case is about the ancestor rule and
		// not about which fallback this machine has: a missing runtime directory,
		// and a HOME that cannot grow a cache because it is a regular file.
		denied := filepath.Join(f.dir, "not-a-directory")
		if err := os.WriteFile(denied, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatalf("write the denied root: %v", err)
		}
		t.Cleanup(func() { assertNoSurvivors(t, c.root) })
		t.Cleanup(func() { assertNoSurvivors(t, c.tmp) })
		t.Cleanup(func() { assertNoSurvivors(t, shared) })
		cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
		cmd.Env = envOverride(cmd.Env,
			"TMPDIR="+c.tmp,
			"XDG_RUNTIME_DIR="+filepath.Join(denied, "runtime"),
			"HOME="+denied,
			"GOWORK=off", "GOENV=off",
		)
		cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
			return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
		})
		runLauncher(t, cmd, f)
		// Exit 2 is the launcher's "I could not get as far as a decision". It must
		// arrive promptly: the refusal precedes the build, so nothing blocks.
		mustExit(t, cmd, f, 2)
		for _, marker := range []string{c.root, c.tmp, shared} {
			if survivors := processesNaming(t, marker); len(survivors) > 0 {
				t.Fatalf("the launcher ran something under a directory another user can replace: %v", survivors)
			}
		}
		if entries, err := os.ReadDir(c.tmp); err != nil {
			t.Fatalf("read the fixture TMPDIR: %v", err)
		} else if len(entries) != 0 {
			t.Fatalf("the launcher minted scratch under a directory whose ancestor another user can replace (%v)", entries)
		}
		for _, line := range f.lines(t) {
			if strings.HasPrefix(line, "helper-ran") {
				t.Fatalf("the launcher started a helper it had already decided not to build: %v", f.lines(t))
			}
		}
	})
	t.Run("ownership is part of the question", func(t *testing.T) {
		// The high this pins: mode bits say what everyone else may do and never
		// what the owner may do, so a directory owned by a third party is
		// replaceable from this script's point of view whatever its mode. A 0755
		// directory somebody else owns is exactly as bad as a 0777 one, and a
		// modes-only check calls it safe.
		//
		// The rule is exercised directly, through the launcher's own function, over
		// directories whose owners really differ: this user's, and -- when one
		// exists on the machine -- a third uid's. A machine with no third uid has no
		// such directory, so that half is skipped rather than simulated: ownership
		// cannot be manufactured without root, and a case that pretended would be
		// testing its own fixture.
		probe := func(dir string) error {
			return runLauncherFunc(t, validatorFuncs, `dir_entry_is_replaceable "$2"`, dir)
		}
		own := filepath.Join(t.TempDir(), "owned-by-us")
		if err := os.MkdirAll(own, 0o700); err != nil {
			t.Fatalf("create %s: %v", own, err)
		}
		if err := os.Chmod(own, 0o700); err != nil {
			t.Fatalf("make %s private: %v", own, err)
		}
		if err := probe(own); err == nil {
			t.Fatalf("a 0700 directory owned by this user was reported replaceable")
		}
		stickyOwn := filepath.Join(t.TempDir(), "sticky-owned-by-us")
		if err := os.MkdirAll(stickyOwn, 0o700); err != nil {
			t.Fatalf("create %s: %v", stickyOwn, err)
		}
		// ModeSticky, not 0o1777: Go's FileMode puts the special bits in high
		// flags, so a literal 0o1777 is masked down to 0777 and the sticky bit
		// silently would not be set -- which is the very property under test.
		if err := os.Chmod(stickyOwn, os.FileMode(0o777)|os.ModeSticky); err != nil {
			t.Fatalf("make %s sticky: %v", stickyOwn, err)
		}
		if err := probe(stickyOwn); err == nil {
			t.Fatalf("a 1777 directory owned by this user was reported replaceable")
		}
		foreign := foreignOwnedDirectory()
		if foreign == "" {
			t.Skip("no directory owned by a third user on this machine: ownership cannot be manufactured without root")
		}
		if err := probe(foreign); err != nil {
			t.Fatalf("%s is owned by another user, so its owner can replace entries in it; the rule called it safe (%v)", foreign, err)
		}
		if err := probe("/tmp"); err == nil {
			t.Fatalf("/tmp (1777, owned by root) was reported replaceable: ordinary hosts must be unaffected")
		}
	})
	t.Run("the root is a component like any other", func(t *testing.T) {
		// The high: the walk began at the first NAMED component and asked nothing
		// about `/`, which is the one directory every absolute path goes through and
		// the one that cannot be skipped. On a root another user can modify -- a
		// container, a chroot, a root-owned sticky directory replaced by its owner --
		// a top-level entry can be replaced after it was validated and before it is
		// used.
		//
		// `/` cannot be made writable here without root, so this case asks the
		// question the fix changed rather than manufacturing the exploit: the
		// predicate is replaced by a recorder, and the walk has to call it with `/`.
		// That is the defect in one assertion -- a walk that never asks cannot
		// refuse -- and it is honest about what it proves: the ordering, not the
		// privilege escalation.
		out := filepath.Join(resolvePath(t, fixtureScratchBase(t)), "root-walk-recorder")
		if err := os.MkdirAll(out, 0o700); err != nil {
			t.Fatalf("create %s: %v", out, err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(out) })
		probe := resolvePathAfterCreate(t, filepath.Join(out, "leaf"))
		requirePrivateChain(t, out, probe)
		// The stub replaces dir_entry_is_replaceable, so `eval` order matters: the
		// stub is defined AFTER the real function is sourced, which is what makes it
		// the one the walk calls.
		//
		// The recorder path goes through the environment, not a positional argument.
		// A stub's `$2` is the STUB's second argument, which is empty, and the first
		// version of this case wrote to the empty name and then read back nothing --
		// "the walk asked about no component at all", which was the test's bug and
		// not the walk's.
		record := filepath.Join(out, "asked")
		script := `eval "$(sed -n '/^scratch_path_is_trusted()/,/^}/p' "$1")"
dir_entry_is_replaceable() { printf '%s\n' "$1" >> "$ROOT_WALK_RECORD"; return 1; }
dir_is_owned_by_trusted() { return 0; }
scratch_path_is_trusted "$2"`
		cmd := exec.Command("bash", "-c", script, "bash", mergeEnqueueLauncher, probe)
		cmd.Env = append(os.Environ(), "ROOT_WALK_RECORD="+record)
		if err := cmd.Run(); err != nil {
			t.Fatalf("the walk refused a path whose components all passed (%v)", err)
		}
		asked, err := os.ReadFile(record)
		if err != nil {
			t.Fatalf("the walk asked about no component at all: %v", err)
		}
		if !strings.HasPrefix(string(asked), "/\n") {
			t.Fatalf("the walk asked about %q, which does not begin with the root: every absolute path goes through `/`, so a walk that never asks about it leaves the one component that cannot be skipped unvalidated", strings.SplitN(string(asked), "\n", 2)[0])
		}
	})
	t.Run("a component that is not there is not trusted", func(t *testing.T) {
		// The high, as a rule rather than a race. The predicate answered "not
		// replaceable" for a directory that was not there at all, and the walk
		// asked about the ancestors last: an ancestor another user removed after
		// its child was checked, and restored after its own turn came, was never
		// asked about again. Both halves come down to one rule -- a component this
		// script cannot positively vouch for is a refusal -- and the walk has to go
		// from the root down, so no prefix is accepted before a component of it has
		// been checked.
		//
		// Exercised through the script's own functions, which is the only way to
		// ask about a path that does not exist: no launcher run can be given a
		// missing TMPDIR and reach the walk, because the walk is what is under test.
		trusted := func(path string) error {
			return runLauncherFunc(t, validatorFuncsWithWalk, `scratch_path_is_trusted "$2"`, path)
		}
		replaceable := func(path string) error {
			return runLauncherFunc(t, validatorFuncs, `dir_entry_is_replaceable "$2"`, path)
		}
		root := resolvePath(t, t.TempDir())
		requirePrivateChain(t, root, root)
		leaf := resolvePathAfterCreate(t, filepath.Join(root, "leaf"))
		requirePrivateChain(t, root, leaf)
		if err := trusted(leaf); err != nil {
			t.Fatalf("a private directory under a private chain was not trusted: %v", err)
		}
		// A missing component: exit 0 from dir_entry_is_replaceable means
		// "somebody can replace this", which is the answer for a name nobody owns.
		absent := filepath.Join(root, "absent")
		if err := replaceable(absent); err != nil {
			t.Fatalf("%s does not exist, so anyone who can write its parent can create it, and the rule called it safe: %v", absent, err)
		}
		if err := trusted(filepath.Join(leaf, "absent")); err == nil {
			t.Fatalf("%s does not exist; a path with a component that is absent was reported trusted", filepath.Join(leaf, "absent"))
		}
		// `/tmp` itself must stay trusted: a root-owned sticky directory protects
		// its entries from other users, and refusing it would refuse every
		// ordinary host.
		if err := trusted("/tmp"); err != nil {
			t.Fatalf("/tmp (1777, owned by root) was not trusted: ordinary hosts must be unaffected")
		}
	})
	t.Run("an extended ACL is part of the question", func(t *testing.T) {
		// The high: mode bits are not the whole permission. A directory with a
		// trusted owner and a mode of 0755 can carry an ACL that lets another local
		// user replace entries, and macOS is where a 0755 ancestor with an ACL is
		// ordinary rather than exotic. `ls -ldn` marks such an entry with a `+`, on
		// both platforms, so the rule refuses every entry that carries one.
		//
		// The ACL is set with setfacl, which is Linux's tool; when it is missing the
		// case skips and says so rather than pretending to have checked. What the
		// marker test covers on any platform is the read side of the rule.
		probe := filepath.Join(resolvePath(t, fixtureScratchBase(t)), "acl-probe")
		if err := os.MkdirAll(probe, 0o700); err != nil {
			t.Fatalf("create %s: %v", probe, err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(probe) })
		if err := os.Chmod(probe, 0o700); err != nil {
			t.Fatalf("make %s private: %v", probe, err)
		}
		if !launcherTrustsPath(t, probe) {
			t.Fatalf("a 0700 directory owned by this user under a trusted chain was not trusted")
		}
		setfacl, err := exec.LookPath("setfacl")
		if err != nil {
			t.Skip("setfacl is not installed: this machine cannot grant an ACL, so the grant half is untested here")
		}
		getfacl, err := exec.LookPath("getfacl")
		if err != nil {
			t.Skip("getfacl is not installed: this machine cannot read an ACL back")
		}
		user := "nobody"
		if err := exec.Command(setfacl, "-m", "u:"+user+":rwx", probe).Run(); err != nil {
			t.Skipf("could not grant an ACL to %s (%v): this machine cannot manufacture the condition", user, err)
		}
		t.Cleanup(func() { _ = exec.Command("setfacl", "-b", probe).Run() })
		// Asked of the ACL reader directly, not of the whole predicate. On Linux a
		// POSIX ACL's mask is also reflected in the group bits, so the mode test
		// refuses this directory on its own and the whole predicate cannot tell
		// whether the ACL was read. On macOS the ACL is independent of the mode
		// bits, which is the platform this rule exists for, so the reader is the
		// piece that has to be right and the piece that gets its own assertion.
		if err := runLauncherFunc(t, validatorFuncs, `dir_acl_grants_foreign_write "$2"`, probe); err != nil {
			t.Fatalf("%s grants write to %s through an extended ACL; the reader reported no foreign write (%v)", probe, user, err)
		}
		// A read-only grant is not a replaceable directory: refusing it would turn
		// a harmless ACL into a launcher that refuses to merge on a host somebody
		// once ran a backup tool in.
		if err := exec.Command(setfacl, "-b", probe).Run(); err != nil {
			t.Fatalf("reset the ACL: %v", err)
		}
		if err := exec.Command(setfacl, "-m", "u:"+user+":r-x", probe).Run(); err != nil {
			t.Fatalf("grant a read-only ACL: %v", err)
		}
		if err := runLauncherFunc(t, validatorFuncs, `dir_acl_grants_foreign_write "$2"`, probe); err == nil {
			t.Fatalf("%s grants only read to %s, so its entries are not replaceable; the reader reported a foreign write", probe, user)
		}
		// And with no ACL at all the reader says no, which is what makes it usable
		// on every ordinary directory.
		if err := exec.Command(setfacl, "-b", probe).Run(); err != nil {
			t.Fatalf("remove the ACL: %v", err)
		}
		if err := runLauncherFunc(t, validatorFuncs, `dir_acl_grants_foreign_write "$2"`, probe); err == nil {
			t.Fatalf("%s carries no extended ACL; the reader reported a foreign write", probe)
		}
		// The permission that matters is the named entry's bits AND the mask. A
		// named user granted rwx under an r-x mask can read and search and cannot
		// write, so it cannot replace entries. Reading the raw bits would refuse
		// this directory, and a launcher that refuses a usable root is the failure
		// mode the whole chain check exists to avoid.
		if err := exec.Command(setfacl, "-b", probe).Run(); err != nil {
			t.Fatalf("reset the ACL: %v", err)
		}
		if err := exec.Command(setfacl, "-m", "u:"+user+":rwx", "-m", "m::r-x", probe).Run(); err != nil {
			t.Fatalf("grant rwx under a read-only mask: %v", err)
		}
		// Confirmed against the tool itself, so this case cannot pass because the
		// ACL was never set the way it claims.
		listing, err := exec.Command(getfacl, "-p", "-c", probe).Output()
		if err != nil {
			t.Fatalf("read back the ACL: %v", err)
		}
		if !strings.Contains(string(listing), "mask::r-x") {
			t.Fatalf("the mask was not narrowed (%q): this case needs rwx under an r-x mask", string(listing))
		}
		// Asked of the reader with the ACL this host just produced: the mask caps
		// the named entry, so the answer is "no foreign write".
		//
		// The predicate is loaded from the shared set, not from its own list of
		// functions: a reader copied out alone is a reader whose helpers -- the
		// mask reader and the group reader -- come from nowhere, and the case then
		// fails on a missing function instead of on the ACL it is about.
		if err := runLauncherFuncStdin(t, validatorFuncs, `acl_getfacl_grants_foreign_write`, string(listing)); err == nil {
			t.Fatalf("%s grants %s rwx under the r-x mask in %q, which cannot replace entries; the reader reported a foreign write", probe, user, string(listing))
		}
		// The mode's group bits are not the owning group's permission when an
		// access ACL is present. On Linux, `ls -l` prints the MASK in the group
		// position, so a wide mask makes the mode read `rwx` while the owning group
		// and every named entry are read-only. The whole predicate used to answer
		// "replaceable" from those bits, refusing a directory nobody can write to.
		//
		// The premise is read back from the tool: the mode must actually show a
		// write in the group position for this case to mean anything, because the
		// whole defect is the mode disagreeing with the ACL.
		if err := exec.Command(setfacl, "-b", probe).Run(); err != nil {
			t.Fatalf("reset the ACL: %v", err)
		}
		// The mode first, and the mask last: chmod on a directory that already has
		// an access ACL rewrites the mask, so setting the mode in between would
		// leave the mask at whatever the mode says and this case would be about a
		// mask nobody set. That is the premise check below doing its job -- the
		// first version of this test set the mode after the mask and did not have
		// the mode it claimed.
		if err := os.Chmod(probe, 0o750); err != nil {
			t.Fatalf("set a known mode: %v", err)
		}
		for _, acl := range [][]string{
			{"-m", "u:" + user + ":r-x"},
			{"-m", "g::r-x"},
			{"-m", "m::rwx"},
		} {
			if out, err := exec.Command(setfacl, append(append([]string{}, acl...), probe)...).CombinedOutput(); err != nil {
				t.Fatalf("setfacl %v in %s: %v: %s", acl, probe, err, string(out))
			}
		}
		t.Cleanup(func() { _ = exec.Command("setfacl", "-b", probe).Run() })
		modeOut, err := exec.Command("stat", "-c", "%A", probe).Output()
		if err != nil {
			t.Fatalf("read the mode back: %v", err)
		}
		mode := strings.TrimSpace(string(modeOut))
		// The group triad is mode[4:7]; `w` in it is the mask showing through.
		if len(mode) < 7 || !strings.Contains(mode[4:7], "w") {
			t.Fatalf("%s reads %q, and this case needs the MASK in the group position for the mode to be the misleading half: the defect is the mode saying write when the ACL does not", probe, mode)
		}
		aclOut, err := exec.Command(getfacl, "-p", "-c", probe).Output()
		if err != nil {
			t.Fatalf("read the ACL back: %v", err)
		}
		for _, line := range []string{"group::r-x", "mask::rwx", "user:" + user + ":r-x"} {
			if !strings.Contains(string(aclOut), line) {
				t.Fatalf("the ACL was not set as this case claims: %q is missing from %q", line, string(aclOut))
			}
		}
		if err := runLauncherFunc(t, validatorFuncs, `dir_entry_is_replaceable "$2"`, probe); err == nil {
			t.Fatalf("%s reads %q with owning group r-x, %s at r-x and a rwx mask: nobody can write, so the predicate must not call it replaceable -- the mode's group position is the mask, not the group", probe, mode, user)
		}
		// And the opposite, so the fix is not "ignore the group bits entirely": an
		// owning group that really may write under a wide mask is a grant.
		if err := exec.Command(setfacl, "-m", "g::rwx", probe).Run(); err != nil {
			t.Fatalf("grant the owning group write: %v", err)
		}
		if err := runLauncherFunc(t, validatorFuncs, `dir_entry_is_replaceable "$2"`, probe); err != nil {
			t.Fatalf("%s grants its owning group rwx through an access ACL, so another user in that group can replace entries; the predicate called it safe (%v)", probe, err)
		}
	})

	// macOS has no getfacl. `ls -lde` is the only thing that prints an ACL there,
	// so the Darwin reader is what decides whether a Mac's scratch ancestor is
	// trusted -- and a reader that answered "grants" for everything would refuse
	// every marked directory, which is most of a Mac's home directory. It is a
	// text parser, so it is exercised here against the format Darwin emits even
	// though this host cannot produce it.
	t.Run("the Darwin ACL format is read, not guessed at", func(t *testing.T) {
		cases := []struct {
			name string
			acl  string
			want bool
		}{
			{
				name: "a named user granted write",
				acl:  " 0: user:nobody allow read,write,execute,delete,add_file,add_subdirectory,file_inherit\n",
				want: true,
			},
			{
				name: "a named group granted write",
				acl:  " 0: group:everyone allow read,write\n",
				want: true,
			},
			{
				// The ordinary Mac ACL: read-only, inherited. Refusing this would
				// stop the tool on hosts where nothing is actually at risk.
				name: "a read-only grant is not a replaceable directory",
				acl:  " 0: user:nobody allow read,readattr,readextattr,readsecurity,file_inherit\n",
				want: false,
			},
			{
				// A deny entry takes permission away; reading it as a grant would
				// be exactly backwards.
				name: "a deny entry is not a grant",
				acl:  " 0: user:nobody deny delete,add_file\n",
				want: false,
			},
			{
				// The owner and owning group are described by the mode bits, which
				// the caller has already checked.
				name: "owner and group entries are the mode bits",
				acl:  " 0: user::rwx\n 1: group::r-x\n",
				want: false,
			},
			{
				name: "only inheritance is requested",
				acl:  " 0: user:nobody allow read,file_inherit,directory_inherit\n",
				want: false,
			},
			{
				// The permission field is its own column, and a substring match over
				// the whole line reads the GROUP NAME as one: `writers` contains
				// "write", so a search-only grant to a group called writers was
				// classified as replaceable. On a Mac that is an ordinary group name,
				// and refusing it stops the launcher on a directory nobody can
				// tamper with.
				name: "a group whose name contains write grants nothing by its name",
				acl:  " 0: group:writers allow list,search\n",
				want: false,
			},
			{
				// The same mistake in the other direction: `writeattr` is not a way
				// to replace an entry, and matching it as `write` refuses a grant
				// that only lets another user change the directory's own attributes.
				name: "an attribute grant is not a replaceable directory",
				acl:  " 0: user:nobody allow list,writeattr,writeextattr\n",
				want: false,
			},
			{
				name: "a named user granted add_file",
				acl:  " 1: user:nobody allow list,add_file,search\n",
				want: true,
			},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				if err := runLauncherFuncStdin(t, validatorFuncs, `acl_ls_e_grants_foreign_write`, c.acl); (err == nil) != c.want {
					t.Fatalf("reading %q gave foreign-write=%v, want %v", strings.TrimSpace(c.acl), err == nil, c.want)
				}
			})
		}
	})
	t.Run("no safe root still runs", func(t *testing.T) {
		f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'helper-ran\n' >> "$EVENER_FIXTURE_LOG"
`)
		tmp := f.tmpDir()
		if err := os.MkdirAll(tmp, 0o755); err != nil {
			t.Fatalf("create the fixture TMPDIR: %v", err)
		}
		if err := os.Chmod(tmp, 0o777); err != nil {
			t.Fatalf("make the fixture TMPDIR replaceable: %v", err)
		}
		cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
		// No safe root to fall back to, and the premise has to be stated rather
		// than assumed: a runtime directory that is not there, and a HOME that is a
		// regular file, so the cache directory cannot be created under it either.
		// A HOME left inside the fixture would let the launcher create
		// $HOME/.cache/evener-scratch and this case would be about a fallback it
		// found rather than about the refusal to build.
		denied := filepath.Join(f.dir, "not-a-directory")
		if err := os.WriteFile(denied, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatalf("write the denied root: %v", err)
		}
		cmd.Env = envOverride(cmd.Env,
			"XDG_RUNTIME_DIR="+filepath.Join(denied, "runtime"),
			"HOME="+denied,
		)
		runLauncher(t, cmd, f)
		mustExit(t, cmd, f, 0)
		waitForLine(t, f, "helper-ran")
	})
	t.Run("a missing TMPDIR is not recreated", func(t *testing.T) {
		// The low: the selection loop created every candidate that was absent,
		// not just the cache directory this tool owns. An inherited TMPDIR naming
		// a sandbox that has since been reaped therefore came back into existence,
		// held the scratch, and was left behind -- on the prebuilt path too, where
		// nothing else needs a scratch root at all.
		f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'handle=%s\n' "${EVENER_MERGEQ_GH_GROUP_FILE:-}" >> "$EVENER_FIXTURE_LOG"
printf 'helper-ran\n' >> "$EVENER_FIXTURE_LOG"
`)
		stale := filepath.Join(f.dir, "reaped-sandbox", "tmp")
		cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
		cmd.Env = envOverride(cmd.Env, "TMPDIR="+stale)
		runLauncher(t, cmd, f)
		mustExit(t, cmd, f, 0)
		waitForLine(t, f, "helper-ran")
		if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the launcher recreated the TMPDIR it was handed (%s): stat returned %v", stale, err)
		}
		// It fell through to the fixture's own runtime directory, so the handle is
		// still somewhere this case owns.
		handle := logValue(t, f, "handle=")
		if handle == "" {
			t.Fatalf("the launcher published no group file: a missing TMPDIR left it with no scratch root at all, but the fixture's $XDG_RUNTIME_DIR is usable; %s", f.output(t))
		}
		if !strings.HasPrefix(handle, f.runtimeDir()+string(filepath.Separator)) {
			t.Fatalf("the group file is %s, want a file under the fixture's runtime directory %s", handle, f.runtimeDir())
		}
	})
	t.Run("a root that cannot execute is passed over for the build", func(t *testing.T) {
		// The medium: selection accepted a writable, trusted TMPDIR on a noexec
		// mount and the exec probe then exited 2 further down, with a usable
		// runtime directory sitting untried. Containers that point TMPDIR at a
		// noexec /dev/shm hit it.
		//
		// The noexec mount has to be real: a directory whose mode forbids execution
		// is not the same condition, and root ignores the modes anyway. /run/lock is
		// noexec on this host, so the case uses it when it is writable and skips by
		// name when it is not, rather than pretending.
		const noexecRoot = "/run/lock"
		info, err := os.Stat(noexecRoot)
		if err != nil || !info.IsDir() {
			t.Skipf("%s is not present on this host", noexecRoot)
		}
		probe, reason := noexecFixtureProbeDir(t, noexecRoot, "")
		if probe == "" {
			t.Skip(reason)
		}
		if !launcherTrustsPath(t, probe) {
			t.Skipf("%s does not pass the chain rule on this host, so the case would be about the chain and not about execution", probe)
		}
		// The chain is made private from the fixture's own root down, and that root
		// is the created directory: walking further would chmod /run/lock itself,
		// which is not this test's to change -- and the case needs it to stay 1777
		// besides.
		c := newFixtureCheckoutUnder(t, probe, probe, "checkout")
		c.requireIsolated(t)
		safeRoot := resolvePath(t, fixtureScratchBase(t))
		safe := resolvePathAfterCreate(t, filepath.Join(safeRoot, "runtime"))
		requirePrivateChain(t, safeRoot, safe)
		f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
		for _, m := range []string{c.root, c.tmp, safe} {
			t.Cleanup(func() { assertNoSurvivors(t, m) })
		}
		cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
		cmd.Env = envOverride(cmd.Env,
			"TMPDIR="+c.tmp,
			"XDG_RUNTIME_DIR="+safe,
			"GOWORK=off", "GOENV=off",
		)
		cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
			return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
		})
		var buildPID int
		t.Cleanup(func() { killGroups(buildPID) })
		runLauncher(t, cmd, f)
		launcherPID := cmd.Process.Pid
		// The build appears under the fallback, which is what proves selection passed
		// over the noexec root instead of dying on it later.
		testwait.UntilSeen(t, "the fixture build to appear under the fallback root", func() (bool, string) {
			buildPID, _ = buildIn(t, safe)
			if buildPID != 0 {
				return true, ""
			}
			if pid, _ := buildIn(t, c.tmp); pid != 0 {
				buildPID = pid
				return false, "the build ran in the noexec root, so selection accepted a root it cannot execute in"
			}
			// A launcher that has already exited is not going to produce a build, and
			// waiting out the deadline for one would report a timeout where the real
			// answer is on stderr. This closure runs on the test's own goroutine, so
			// failing here is a normal case failure.
			if launcherExited(launcherPID) {
				t.Fatalf("the launcher exited without building anywhere: a noexec root was chosen and the exec probe then refused it, instead of passing over it for the fallback; %s", f.output(t))
			}
			return false, "no build names either root yet"
		})
		signalLauncher(t, cmd, syscall.SIGTERM)
		mustExit(t, cmd, f, 130)
		testwait.Until(t, fmt.Sprintf("build pid %d to terminate", buildPID), func() bool {
			return pidGone(buildPID)
		})
	})
}

// TestMergeEnqueueLauncherFixtureSkipsAnUntrustedScratchBase pins the rule the
// fixture itself now follows: a scratch base is used only if the launcher's own
// validator accepts its whole ancestry.
//
// Without it the fixture inherits whatever TMPDIR the runner has. On a shared
// group-writable one the launcher correctly refuses the fixture's scratch and
// relocates, and every build-waiting case then waits out its deadline against a
// correct launcher -- a suite that fails for a reason unrelated to the
// behaviour under test, which is how a real regression gets missed. The
// selection is split out so a manufactured untrusted parent can exercise it;
// the real candidates cannot be made untrusted without changing the process
// environment, and a test that cannot fail proves nothing.
func TestMergeEnqueueLauncherFixtureSkipsAnUntrustedScratchBase(t *testing.T) {
	t.Parallel()
	// A parent another user can write to: 0777 with no sticky bit. Nothing here
	// depends on there being a second user on this machine -- the validator asks
	// what the mode permits, and this mode permits anybody to replace an entry.
	shared := filepath.Join(resolvePath(t, fixtureScratchBase(t)), "shared-base")
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatalf("create %s: %v", shared, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shared) })
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatalf("make %s shared: %v", shared, err)
	}
	// 0700 on the directory the fixture would create does not save it: the entry
	// in `shared` is what another user can replace, and the mode of the entry
	// says nothing about who may rename it.
	if launcherTrustsPath(t, shared) {
		t.Skipf("%s is not replaceable by another user on this host, so this machine cannot manufacture the condition", shared)
	}
	trusted := fixtureScratchBase(t)
	// The untrusted base is first and must be passed over, not accepted. The
	// trusted one after it is what the fixture has to end up with.
	got := firstTrustedScratchBase(t, []string{shared, trusted})
	if want := filepath.Dir(got); want != trusted {
		t.Fatalf("the fixture chose a scratch base under %s, want one under %s: an untrusted candidate was accepted, so the fixture would hand the launcher a root it refuses and wait out its deadline", want, trusted)
	}
	// And the chosen directory is one the launcher accepts, which is the property
	// every build-waiting case depends on.
	if !launcherTrustsPath(t, got) {
		t.Fatalf("%s was chosen but the launcher does not trust it", got)
	}

	// A symlinked candidate is judged by where it lands. The launcher
	// canonicalizes TMPDIR with `pwd -P` before checking it, so checking the
	// spelling instead rejects a trusted destination -- macOS's /var ->
	// /private/var is the case that matters -- and accepts a link whose target
	// nobody validated. Both the choice and the reason are asserted.
	t.Run("a symlink is judged by its destination", func(t *testing.T) {
		target := filepath.Join(trusted, "symlink-target")
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatalf("create %s: %v", target, err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(target) })
		link := filepath.Join(trusted, "symlink-candidate")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("cannot create a symlink here (%v): this machine cannot manufacture the case", err)
		}
		t.Cleanup(func() { _ = os.Remove(link) })
		chosen := firstTrustedScratchBase(t, []string{link})
		// The chosen directory must live under the resolved target, not under the
		// link's own spelling.
		resolvedChosen := resolvePath(t, chosen)
		resolvedTarget := resolvePath(t, target)
		if filepath.Dir(resolvedChosen) != resolvedTarget {
			t.Fatalf("the fixture chose %s, want a directory directly under %s: a symlinked candidate was not resolved to its destination", resolvedChosen, resolvedTarget)
		}
	})

	// A trusted but noexec base must be passed over. Selecting it moves the
	// failure to the fixture helper's start, far from the cause.
	t.Run("a trusted base that cannot execute is passed over", func(t *testing.T) {
		noexec := filepath.Join(trusted, "noexec-candidate")
		if err := os.MkdirAll(noexec, 0o700); err != nil {
			t.Fatalf("create %s: %v", noexec, err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(noexec) })
		if launcherCanExecuteIn(t, noexec) {
			t.Skipf("%s can execute a program, so this machine cannot manufacture the condition", noexec)
		}
		if !launcherTrustsPath(t, noexec) {
			t.Fatalf("%s is not trusted, so this case cannot show that executability alone rejects it", noexec)
		}
		if reason := unusableScratchBase(t, noexec); reason == refusalNone {
			t.Fatalf("%s cannot execute a program but the fixture predicate called it usable", noexec)
		}
	})
}

// TestMergeEnqueueLauncherDoesNotTouchACallerOwnedGroupFile pins the medium: an
// inherited variable must not make the prebuilt path treat a caller's directory
// as scratch.
//
// The launcher used to read `work` from the environment when naming the file the
// helper publishes its `gh` group in. A caller with `work` set for their own
// reasons therefore had a gh-group file in that directory truncated at launch
// and removed at exit -- data loss in a directory this tool never created. The
// scratch is passed explicitly now, and the prebuilt path mints its own.
func TestMergeEnqueueLauncherDoesNotTouchACallerOwnedGroupFile(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'helper-pid=%s\n' "$$" >> "$EVENER_FIXTURE_LOG"
printf 'handle=%s\n' "${EVENER_MERGEQ_GH_GROUP_FILE:-}" >> "$EVENER_FIXTURE_LOG"
`)
	// A caller's own directory with a file at the name the launcher would use,
	// holding bytes that are not the launcher's to touch.
	owned := filepath.Join(f.dir, "caller-work")
	if err := os.MkdirAll(owned, 0o755); err != nil {
		t.Fatalf("create the caller's directory: %v", err)
	}
	handle := filepath.Join(owned, "gh-group")
	const original = "the caller's own bytes\n"
	if err := os.WriteFile(handle, []byte(original), 0o600); err != nil {
		t.Fatalf("write the caller's file: %v", err)
	}
	var helperPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env, "work="+owned)
	runLauncher(t, cmd, f, &helperPID)
	mustExit(t, cmd, f, 0)
	if body, err := os.ReadFile(handle); err != nil {
		t.Fatalf("the caller's file is gone: the launcher treated a directory it did not create as scratch (%v)", err)
	} else if string(body) != original {
		t.Fatalf("the caller's file now holds %q: the launcher truncated it", body)
	}
	if got := logValue(t, f, "handle="); got == handle {
		t.Fatalf("the launcher named the caller's %s as the group file", handle)
	}
}

// TestMergeEnqueueLauncherStopsAHelperAWrapperLeftBehind pins the case where the
// launched PID is not the helper: EVENER_DEV_BIN names a wrapper that starts the
// Go helper as a child and exits on TERM without forwarding it.
//
// The launcher's graceful stop is a TERM at the launched PID, which is the
// wrapper. When the wrapper exits on that TERM the launcher used to treat the
// cancellation as done, because the only thing it checked was whether the traced
// child was still running. The Go helper it started is in the group recorded at
// launch and keeps polling -- or finishes submitting an enqueue -- after the
// launcher has reported 130.
//
// The recorded group is the handle that survives: the child is reaped, so `ps`
// assertions are needed. The launcher has to exit 130 because the operator asked
// it to stop, and the helper has to be gone because 130 means the enqueue
// stopped rather than merely that this process did.
func TestMergeEnqueueLauncherStopsAHelperAWrapperLeftBehind(t *testing.T) {
	t.Parallel()
	// The fixture IS the wrapper: EVENER_DEV_BIN points at it, so it is the
	// launcher's tracked child. It starts a stand-in for the Go helper in its own
	// group, records both, and exits on TERM without forwarding it -- the exit
	// that makes the launcher believe the cancellation is complete.
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
"$EVENER_FIXTURE_HELPER" &
inner=$!
printf 'wrapper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
printf 'inner-pid=%s;pgid=%s\n' "$inner" "$(ps -o pgid= -p "$inner" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
trap 'exit 0' TERM
while :; do sleep 0.05; done
`)
	inner := filepath.Join(f.dir, "helper.sh")
	writeTestFile(t, inner, []byte(`#!/usr/bin/env bash
trap '' TERM
while :; do sleep 0.05; done
`), 0o755)
	var innerPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	// The grace is long enough for the wrapper to act on TERM and exit, which is
	// the sequencing this case needs: the launcher's check must happen after the
	// wrapper is gone, or it would be testing the ordinary path. One second still
	// leaves the case well inside its ceiling, with the stopper's own grace at
	// one second as well.
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_HELPER="+inner,
		"EVENER_HELPER_TERM_GRACE_SECONDS=1",
		"EVENER_STOP_TREE_GRACE_SECONDS=1",
	)
	runLauncher(t, cmd, f, &innerPID)
	_, helperGroup := logPIDAndGroup(t, f, "wrapper-pid=")
	innerPID, innerGroup := logPIDAndGroup(t, f, "inner-pid=")
	if innerGroup != helperGroup {
		t.Fatalf("the inner stand-in is in group %d, want the wrapper's group %d: the recorded group is the only handle to it", innerGroup, helperGroup)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	// The helper the wrapper started must be gone: 130 has to mean the enqueue
	// stopped, and a wrapper's exit says nothing about its child.
	testwait.Until(t, fmt.Sprintf("the helper the wrapper left behind (pid %d) to be stopped before the launcher exits 130", innerPID), func() bool {
		return pidGone(innerPID)
	})
}

// TestMergeEnqueueLauncherStopsACallItsWrapperLeftUnpublished pins the third
// handle a forced stop needs.
//
// An EVENER_DEV_BIN wrapper that exits on TERM while the Go helper it started is
// between starting `gh` and publishing that call's group leaves the call in
// neither place the launcher used to look: the tracked pid is gone, so a
// descendant walk from it finds nothing, and the handle file is not written yet,
// so the published group names nothing. The helper it left behind is alive in
// the group recorded at launch, and that group is the only route to the call --
// so the walk has to start from the group's surviving members, not just from the
// dead wrapper.
//
// The fixture is exactly that shape, with the call in a group of its own so it is
// reachable only through the helper and not by a group kill of the recorded one.
func TestMergeEnqueueLauncherStopsACallItsWrapperLeftUnpublished(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
"$EVENER_FIXTURE_HELPER" &
inner=$!
printf 'wrapper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
printf 'inner-pid=%s;pgid=%s\n' "$inner" "$(ps -o pgid= -p "$inner" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
trap 'exit 0' TERM
while :; do sleep 0.05; done
`)
	call := filepath.Join(f.dir, "gh-call.sh")
	writeTestFile(t, call, []byte(`#!/usr/bin/env bash
trap '' TERM
while :; do sleep 0.05; done
`), 0o755)
	// The stand-in helper: it starts the call in a group of its own, exactly as
	// GHClient does (SysProcAttr leads a group, and the group id is the child's
	// pid), records it, and never writes EVENER_MERGEQ_GH_GROUP_FILE -- the
	// window between starting the call and publishing it.
	inner := filepath.Join(f.dir, "helper.sh")
	writeTestFile(t, inner, []byte(`#!/usr/bin/env bash
trap '' TERM
set -m
`+shellquote.Literal(call)+` &
call=$!
set +m
printf 'call-pid=%s;pgid=%s\n' "$call" "$(ps -o pgid= -p "$call" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`), 0o755)
	var innerPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_HELPER="+inner,
		"EVENER_HELPER_TERM_GRACE_SECONDS=1",
		"EVENER_STOP_TREE_GRACE_SECONDS=1",
	)
	runLauncher(t, cmd, f, &innerPID)
	_, helperGroup := logPIDAndGroup(t, f, "wrapper-pid=")
	innerPID, innerGroup := logPIDAndGroup(t, f, "inner-pid=")
	callPID, callGroup := logPIDAndGroup(t, f, "call-pid=")
	if innerGroup != helperGroup {
		t.Fatalf("the inner helper is in group %d, want the wrapper's group %d: the recorded group is the only handle to it", innerGroup, helperGroup)
	}
	if callGroup == helperGroup || callGroup == innerPID {
		t.Fatalf("the call is in group %d: this case needs it outside both the helper's group and the launcher's tree", callGroup)
	}
	t.Cleanup(func() { killGroups(callPID) })
	if pidGone(callPID) {
		t.Fatalf("the fixture call %d exited before the launcher was signalled", callPID)
	}
	if pidGone(innerPID) {
		t.Fatalf("the fixture helper %d exited before the launcher was signalled", innerPID)
	}
	// The wrapper is still alive here, and it is the signal that makes it exit:
	// the launcher TERMs the tracked pid, the wrapper exits without forwarding,
	// the grace expires, and the forced stop is what has to find the call.
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	testwait.Until(t, fmt.Sprintf("the unpublished call pid %d to be stopped before the launcher exits 130", callPID), func() bool {
		return pidGone(callPID)
	})
}

// TestMergeEnqueueLauncherStopsTheHelperWhenInterruptedDuringTheSurvivorStop
// pins where the interrupt handler gets its target from once the tracked child
// is gone.
//
// A wrapper that starts the helper and exits leaves the launcher in a shape the
// handler has to read correctly: the pid it tracked is reaped, and the only
// route to the helper is the group recorded at launch. The handler used to
// choose its stop from whether the tracked child was still a helper, and the
// launcher cleared that flag as soon as `wait` returned -- before the survivor
// scan that follows. An interrupt landing anywhere inside that scan took the
// build path instead: stop_command on a pid already reaped, which names no
// group, so neither the recorded helper group nor a published GitHub-call group
// was stopped and the launcher exited 130 over a helper that was still running.
//
// The window is held open by the helper's own term grace, which the stop spends
// waiting for a group that ignores TERM. The test sends the signal there -- not
// on a sleep, on the launcher's own "still running" line, which is printed
// immediately before that stop begins -- and asks whether the helper survived
// the exit. A launcher that reports 130 while the helper is alive fails.
func TestMergeEnqueueLauncherStopsTheHelperWhenInterruptedDuringTheSurvivorStop(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
"$EVENER_FIXTURE_HELPER" &
printf 'inner-pid=%s;pgid=%s\n' "$!" "$(ps -o pgid= -p "$!" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
# Exit at once, which is the shape the survivor scan exists for: the launcher's
# wait returns while the helper it started is still going.
exit 0
`)
	inner := filepath.Join(f.dir, "helper.sh")
	writeTestFile(t, inner, []byte(`#!/usr/bin/env bash
# TERM is ignored, so the graceful phase of the forced stop cannot finish the
# job: only the group kill can, which is what makes the interrupt's target
# matter.
trap '' TERM
while :; do sleep 0.05; done
`), 0o755)
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_HELPER="+inner,
		// Long enough to hold the window open while the test reads the line and
		// signals, short enough that a failed case does not sit in it.
		"EVENER_HELPER_TERM_GRACE_SECONDS=3",
	)
	runLauncher(t, cmd, f)
	innerPID, innerGroup := logPIDAndGroup(t, f, "inner-pid=")
	t.Cleanup(func() { killGroups(innerPID) })
	if innerGroup <= 1 {
		t.Fatalf("the fixture helper published group %d, want the group the launcher recorded", innerGroup)
	}
	// The launcher prints this the moment it finds the group still populated,
	// immediately before the stop that the interrupt below lands inside.
	testwait.UntilSeen(t, "the launcher to start stopping the group its wrapper left", func() (bool, string) {
		body, err := os.ReadFile(f.stderr)
		if err != nil {
			return false, ""
		}
		text := string(body)
		return strings.Contains(text, "still running"), text
	})
	signalLauncher(t, cmd, syscall.SIGTERM)
	code, exited := waitExit(t, cmd, launcherExitDeadline)
	if !exited {
		t.Fatalf("launcher still running after %s: the interrupt did not stop it; %s", launcherExitDeadline, f.output(t))
	}
	// Survival first. The defect this case exists for is a launcher that
	// reports an interrupt while the helper keeps polling or submitting, and a
	// status check ahead of this one would report whichever code the broken
	// handler happened to produce and leave the part that matters untested.
	if !pidGone(innerPID) {
		t.Fatalf("the launcher exited %d while the helper its wrapper left (pid %d, group %d) was still running: an interrupt during the survivor stop has to stop the recorded helper group, not the pid it already reaped; %s",
			code, innerPID, innerGroup, f.output(t))
	}
	if code != 130 {
		t.Fatalf("launcher exit %d, want 130; %s", code, f.output(t))
	}
}

// TestMergeEnqueueLauncherReportsAMissingLibraryAsALauncherFailure pins the exit
// code for a checkout whose scripts/lib is a directory but whose contents cannot
// be sourced.
//
// The sources run before the EXIT trap, so a `.` that failed used to end the
// script under `set -e` with the shell's own status. Exit 1 means "the queue
// dropped your PR", which a caller retries, and nothing had asked GitHub
// anything. The contract says a launcher that could not run the helper is 2, so
// each source is checked and routed through launcher_failed, which names the
// library it could not read.
func TestMergeEnqueueLauncherReportsAMissingLibraryAsALauncherFailure(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckout(t, fixtureTmpDir(t, "tmp"))
	// One library is missing, which is what a partial checkout or a bad copy
	// looks like. scripts/lib itself stays a directory, so the root-resolution
	// guard passes and the source is what fails.
	if err := os.Remove(filepath.Join(c.root, "scripts", "lib", "gate-bounded.sh")); err != nil {
		t.Fatalf("remove the fixture library: %v", err)
	}
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env, "TMPDIR="+c.tmp, "GOWORK=off", "GOENV=off")
	runLauncher(t, cmd, f)
	// Exit 2, not 1: the launcher never got as far as a decision, so the caller
	// must not read this as a queue outcome.
	mustExit(t, cmd, f, 2)
	// And it says which library, so the operator does not have to guess.
	if out := f.output(t); !strings.Contains(out, "gate-bounded.sh") {
		t.Fatalf("the launcher did not name the missing library: %s", out)
	}
}

// TestMergeEnqueueLauncherStopsACallWhoseWrapperExited pins the forced stop
// against the shape that defeats a descendant walk: the helper's `gh` is a
// wrapper that starts the real CLI in the wrapper's own process group, publishes
// that group, and then exits.
//
// Once the wrapper exits, the CLI is reparented out of the helper's tree, so
// nothing walking descendants from the helper can find it, while it is still
// submitting the enqueue. The launcher has to hold the group id itself, which is
// what the helper publishes through EVENER_MERGEQ_GH_GROUP_FILE. The helper here
// ignores TERM, so the grace expires and only the forced stop can end the call.
//
// The CLI ignores TERM as well, so a signal at the wrong group -- or one that
// never arrives -- is visible as survival rather than as a lucky exit. The
// assertion is that the CLI is gone after the launcher exits 130, which is the
// property the operator relies on: "interrupted" means nothing is submitting.
func TestMergeEnqueueLauncherStopsACallWhoseWrapperExited(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap '' TERM
# The wrapper runs in the background and exits immediately: the real CLI is
# reparented out of this tree, exactly as a gh wrapper without exec leaves it.
#
# The wrapper gets a group of its own, and the handle names it, which is what
# internal/devtool/mergeq does with SysProcAttr: gh leads its own group, and the
# group id is the wrapper's pid.
set -m
"$EVENER_FIXTURE_WRAPPER" &
wrapper=$!
set +m
printf '%s\n' "$wrapper" > "$EVENER_MERGEQ_GH_GROUP_FILE"
printf 'helper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	cli := filepath.Join(f.dir, "gh-cli.sh")
	writeTestFile(t, cli, []byte(`#!/usr/bin/env bash
trap '' TERM
printf 'cli-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`), 0o755)
	// The wrapper inherits the group the helper put it in, starts the CLI in that
	// same group -- no `exec`, no new group, exactly as a gh wrapper leaves the
	// real CLI -- and exits. The CLI keeps the group id after the reparent, which
	// is the only handle that still names it.
	wrapper := filepath.Join(f.dir, "gh")
	writeTestFile(t, wrapper, []byte(`#!/usr/bin/env bash
set -uo pipefail
`+shellquote.Literal(cli)+` &
# Give the CLI a moment to be scheduled, then leave it behind: this is the exit
# that makes a descendant walk from the helper useless.
sleep 0.1
exit 0
`), 0o755)
	var helperPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_WRAPPER="+wrapper,
		"EVENER_HELPER_TERM_GRACE_SECONDS=0",
		"EVENER_STOP_TREE_GRACE_SECONDS=1",
	)
	runLauncher(t, cmd, f, &helperPID)
	helperPID, helperGroup := logPIDAndGroup(t, f, "helper-pid=")
	cliPID, cliGroup := logPIDAndGroup(t, f, "cli-pid=")
	if cliGroup == helperGroup {
		t.Fatalf("the fixture CLI is in the helper's group %d: this case needs it outside the helper's tree", helperGroup)
	}
	// The wrapper has to be gone before the signal, or this case would be testing
	// the walk it exists to show is insufficient.
	testwait.Until(t, "the wrapper to exit, leaving its CLI behind", func() bool {
		for _, proc := range processesNaming(t, wrapper) {
			if strings.Contains(proc.cmd, wrapper) && proc.pid != cliPID {
				return false
			}
		}
		return true
	})
	if pidGone(cliPID) {
		t.Fatalf("the fixture CLI %d exited before the launcher was signalled", cliPID)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	testwait.Until(t, fmt.Sprintf("the reparented CLI pid %d to be stopped before the launcher exits 130", cliPID), func() bool {
		return pidGone(cliPID)
	})
}

// TestMergeEnqueueLauncherBuildsAHostHelperDespiteACrossBuildTarget pins that
// the helper is built for the machine that is about to run it.
//
// A developer can save GOOS=linux in their go env while working on a Mac, or
// export it for a container image build. The helper this launcher builds is
// executed by this script a few lines after the build, so a cross-built helper
// is a binary the kernel refuses: the launcher exits 2 before contacting GitHub,
// reporting a launcher failure for a host whose queue was never asked. The
// pre-port launcher ran `go run`, which had the same problem only in a place
// nobody looked; the port made it reachable and this pins the answer.
//
// GOOS and GOARCH are both real settings and both need pinning, for a reason the
// go command makes easy to get wrong: an empty GOOS in the environment does NOT
// beat a GOOS line in the go env file, and a filtered file does NOT beat an
// exported GOOS. Both halves are therefore required, and this case exercises a
// saved file because that is the shape a developer actually has.
//
// The oracle is the running helper, not the build: the fixture's `exit 0` helper
// must be executed, which is only possible when the binary is this host's. The
// fixture therefore uses a real build rather than the FIFO, because the FIFO
// proves the build started and this case is about what it produced.
func TestMergeEnqueueLauncherBuildsAHostHelperDespiteACrossBuildTarget(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckoutNamed(t, fixtureTmpDir(t, "tmp"), "checkout")
	c.requireIsolated(t)
	// A plain source file, not the fixture's FIFO: this build has to finish.
	if err := os.Remove(filepath.Join(c.root, "cmd", "evener-dev", "bin", "hold.go")); err != nil {
		t.Fatalf("remove the blocking fifo: %v", err)
	}
	// The cross target goes in the saved go env file, which is where a developer
	// who builds for another platform keeps it. The other platform is chosen by
	// the host so the shape is always "a target this host is not": darwin on a
	// Linux runner, linux on a Mac.
	crossOS, crossArch := "linux", "amd64"
	if runtime.GOOS == "linux" {
		crossOS, crossArch = "darwin", "arm64"
	}
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nprintf 'helper-ran\\n' >> \"$EVENER_FIXTURE_LOG\"\n")
	envFile := filepath.Join(f.dir, "go-env")
	writeTestFile(t, envFile, []byte("GOOS="+crossOS+"\nGOARCH="+crossArch+"\n"), 0o600)
	t.Cleanup(func() { assertNoSurvivors(t, c.root) })
	t.Cleanup(func() { assertNoSurvivors(t, c.tmp) })
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env,
		"TMPDIR="+c.tmp,
		"GOWORK=off",
		"GOENV="+envFile,
	)
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	runLauncher(t, cmd, f)
	// The oracle is the built helper having run, which is only possible when the
	// binary is this host's: the fixture's main writes this sentinel, and the
	// kernel never gets that far for another platform's executable.
	//
	// The launcher's own status is the second half. 0 is "the helper ran and
	// decided"; 2 is the launcher failing to get as far as a decision, which is
	// what a cross-built helper produces.
	mustExit(t, cmd, f, 0)
	testwait.Until(t, "the built helper to run", func() bool {
		_, err := os.Stat(c.sentinel)
		return err == nil
	})
}

// TestMergeEnqueueLauncherFindsABuildUnderAScratchPathWithTwoSpaces pins that
// the process scan keeps a command line verbatim. It is the cold-build case run
// with a TMPDIR holding two consecutive spaces.
//
// Two spaces is what separates "the path is present" from "the path survives a
// re-render": a scan that split the whole row on whitespace and rejoined it with
// single spaces produced a string that no process's argv can contain, so
// readiness waited for a build it could not see and the sweep looked for
// survivors under a path nothing was running under. Both then agreed with each
// other and with nothing on the machine -- the case timed out, and the
// FIFO-blocked build it failed to find kept running in its own process group
// after the test exited.
//
// Two oracles, both about the real processes: the build the case signalled is
// gone after the launcher exits 130, and cleanup's scan over the same marker
// reports no survivor. Cleanup is what would have hidden the leak, so it has to
// see what the machine sees.
func TestMergeEnqueueLauncherFindsABuildUnderAScratchPathWithTwoSpaces(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckoutNamed(t, fixtureTmpDir(t, "tmp  dir"), "checkout")
	// The marker is the scratch path the build's argv carries. Two spaces in it
	// make a re-rendered command line unmatchable.
	marker := c.tmp
	c.requireIsolated(t)
	for _, m := range []string{c.root, c.tmp} {
		t.Cleanup(func() { assertNoSurvivors(t, m) })
	}
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env, "TMPDIR="+c.tmp, "GOWORK=off", "GOENV=off")
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	runLauncher(t, cmd, f)
	var buildPID int
	testwait.UntilSeen(t, "the launcher's build to appear under a scratch path with two spaces", func() (bool, string) {
		buildPID, _ = buildIn(t, marker)
		if buildPID == 0 {
			return false, "no process names the fixture scratch yet"
		}
		return true, ""
	})
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	testwait.Until(t, fmt.Sprintf("build pid %d to terminate", buildPID), func() bool {
		return pidGone(buildPID)
	})
	// Explicit, because this is the half of the finding that does not show up as
	// a failure: the build has to be dead, not merely unreported.
	if survivors := processesNaming(t, marker); len(survivors) > 0 {
		t.Fatalf("a build survived the launcher: %v", survivors)
	}
}

// TestMergeEnqueueLauncherRunsFromACheckoutPathWithSpaces pins that a checkout
// directory with a space in it changes nothing.
//
// The launcher quotes every path it builds, so a space is ordinary to it. The
// fixture log is what used to break: entries are values such as the directory the
// helper ran from, and splitting the log on whitespace truncated such a value at
// its first space, so a correct launcher failed a comparison against the path it
// really used. Two things are pinned here: the launcher exits 0 from a spaced
// path, and the value the fixture recorded comes back whole.
func TestMergeEnqueueLauncherRunsFromACheckoutPathWithSpaces(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'helper-ran=%s\n' "$PWD" >> "$EVENER_FIXTURE_LOG"
exit 0
`)
	// The space is in the checkout path, which is what this case is about. TMPDIR
	// stays free of one: a spaced scratch is a different question, and this case
	// must fail only for its own reason.
	c := newFixtureCheckoutNamed(t, fixtureTmpDir(t, "tmp"), "checkout with spaces")
	c.requireIsolated(t)
	// The prebuilt path, so the helper that records its directory is the fixture
	// and not the fixture checkout's stub: start_helper changes into the checkout
	// root before launching, and the value it records is the one the log must
	// carry whole.
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env, "TMPDIR="+c.tmp, "GOWORK=off", "GOENV=off")
	runLauncher(t, cmd, f)
	mustExit(t, cmd, f, 0)
	var ran string
	testwait.Until(t, "the helper to record the directory it ran from", func() bool {
		for _, line := range f.lines(t) {
			if rest, ok := strings.CutPrefix(line, "helper-ran="); ok {
				ran = rest
				return true
			}
		}
		return false
	})
	if ran != c.root {
		t.Fatalf("helper ran from %q, want the checkout root %q", ran, c.root)
	}
}

// TestMergeEnqueueLauncherResolvesItsRootUnderCDPATH pins that the root resolves
// to one path: `cd` prints the directory it moved to when a CDPATH entry
// matches, so the root would otherwise capture two lines. Everything that path is used for then
// failed -- the scratch library would not load from a path with a newline in it,
// and `cd "$root"` before the helper fails.
//
// CDPATH is the checkout itself, which is what makes the relative
// `scripts/ops/...` invocation match. The claim is that the run still works: the
// root resolves to one path, the helper starts from it, and the launcher exits 0.
func TestMergeEnqueueLauncherResolvesItsRootUnderCDPATH(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'helper-ran=%s\n' "$PWD" >> "$EVENER_FIXTURE_LOG"
exit 0
`)
	cmd := f.launcherCmd(t, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env, "CDPATH="+wd)
	runLauncher(t, cmd, f)
	mustExit(t, cmd, f, 0)
	// The helper ran from the resolved root, not from a path with a newline in
	// it: start_helper changes directory before launching.
	var ran string
	testwait.Until(t, "the helper to record the directory it ran from", func() bool {
		for _, line := range f.lines(t) {
			if rest, ok := strings.CutPrefix(line, "helper-ran="); ok {
				ran = rest
				return true
			}
		}
		return false
	})
	if ran != wd {
		t.Fatalf("helper ran from %q, want the checkout root %q", ran, wd)
	}
}

// TestMergeEnqueueLauncherLeavesNoCallBehindWhenForcedCleanupRuns pins that the
// forced stop leaves nothing that can still submit.
//
// The forced stop freezes the helper before it takes its inventory of `gh`
// groups, because a census of a tree that is free to fork is not a census: a
// call started after the read and before the kill would be in no list and would
// keep submitting after the launcher returned 130.
//
// The consequence for testing it is worth stating plainly. With the freeze in
// place a call the fixture tries to start during the forced stop never starts at
// all, so there is no red this case can produce against the pre-freeze code by
// asking "did the late call die" -- the late call does not exist to die. What
// this case still pins is the observable that matters to an operator: after the
// launcher exits 130, no process naming this case's directory is left, whether
// it was the first call, the helper, or one the helper was about to start. The
// freeze's own guarantee is covered by the code path it makes atomic and by the
// forced-stop cases above.
func TestMergeEnqueueLauncherLeavesNoCallBehindWhenForcedCleanupRuns(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, "#!/usr/bin/env bash\n"+
		"set -uo pipefail\n"+
		"trap '' TERM\n"+
		"printf 'helper-pid=%s;pgid=%s\\n' \"$$\" \"$(ps -o pgid= -p \"$$\" | tr -d ' ')\" >> \"$EVENER_FIXTURE_LOG\"\n"+
		"\"$EVENER_FIXTURE_GH\" &\n"+
		"gh=$!\n"+
		"printf 'gh-pid=%s;pgid=%s\\n' \"$gh\" \"$(ps -o pgid= -p \"$gh\" | tr -d ' ')\" >> \"$EVENER_FIXTURE_LOG\"\n"+
		"while :; do sleep 0.05; done\n")
	// A `gh` that ignores TERM and never finishes: only the forced KILL ends it.
	gh := filepath.Join(f.dir, "gh.sh")
	writeTestFile(t, gh, []byte(`#!/usr/bin/env bash
trap '' TERM
while :; do sleep 0.05; done
`), 0o755)
	var helperPID, ghPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_GH="+gh,
		"EVENER_HELPER_TERM_GRACE_SECONDS=1",
		"EVENER_STOP_TREE_GRACE_SECONDS=1",
	)
	runLauncher(t, cmd, f, &helperPID, &ghPID)
	helperPID, _ = logPIDAndGroup(t, f, "helper-pid=")
	ghPID, ghGroup := logPIDAndGroup(t, f, "gh-pid=")
	if pidGone(ghPID) {
		t.Fatalf("the fixture's gh %d exited before the launcher was signalled", ghPID)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	// The call must be gone, and nothing else this case started may be alive.
	testwait.Until(t, fmt.Sprintf("the call in group %d to be stopped before the launcher exits 130", ghGroup), func() bool {
		return pidGone(ghPID)
	})
	for _, proc := range processesNaming(t, f.dir) {
		if pidGone(proc.pid) {
			continue
		}
		t.Fatalf("a process outlived the launcher's forced stop: %d(pgid %d) %s", proc.pid, proc.pgid, proc.cmd)
	}
}

// TestMergeEnqueueLauncherCancelsAStalledGoEnvRead pins the placement of the
// saved-goenv read. Reading it is a file read on a path the caller chose, and on
// a stalled network home directory that read does not return. Run in the
// foreground it would be a command before the build is a tracked child, and bash
// defers a trap until a foreground command returns -- so SIGTERM at the
// launcher's PID would do nothing until the read finished.
//
// The stall is a FIFO: opening it for reading blocks until a writer appears, and
// no writer ever does. That is the same shape as a hung NFS read, it needs no
// mount privileges, and unlike a sleep it cannot pass by finishing.
func TestMergeEnqueueLauncherCancelsAStalledGoEnvRead(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckout(t, fixtureTmpDir(t, "tmp"))
	c.requireIsolated(t)
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
	stalled := filepath.Join(f.dir, "stalled-go-env")
	if err := syscall.Mkfifo(stalled, 0o600); err != nil {
		t.Skipf("cannot create a FIFO here (%v): this machine cannot manufacture a stalled read", err)
	}
	for _, m := range []string{c.root, c.tmp} {
		t.Cleanup(func() { assertNoSurvivors(t, m) })
	}
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env,
		"TMPDIR="+c.tmp,
		"GOWORK=off",
		"GOENV="+stalled,
	)
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	runLauncher(t, cmd, f)
	// The read is reached before the compiler starts, and the FIFO blocks it
	// there, so the build never appears -- a build is what a working relocation
	// would produce and this fixture exists to show the opposite. Wait for the
	// blocked read instead: a process naming the FIFO that is still alive.
	var readerPID int
	testwait.UntilSeen(t, "the saved-goenv read to block on the fifo", func() (bool, string) {
		for _, proc := range processesNaming(t, stalled) {
			if proc.pid != cmd.Process.Pid {
				readerPID = proc.pid
				return true, ""
			}
		}
		return false, "no process is reading the fifo yet"
	})
	signalLauncher(t, cmd, syscall.SIGTERM)
	// The whole point: the launcher exits on its own signal rather than waiting
	// for the read to return. With the read in the foreground this never
	// happens, because bash defers the trap until it returns.
	mustExit(t, cmd, f, 130)
	testwait.Until(t, fmt.Sprintf("the blocked reader pid %d to be stopped", readerPID), func() bool {
		return pidGone(readerPID)
	})
}

// TestMergeEnqueueLauncherBuildsDespiteASavedModfile pins that the helper build
// is decided by this script and not by the developer's saved go env.
//
// `GOFLAGS=` in the environment does not win against the file. The go command
// skips an empty value and falls back to `go env -w`'s stored one, so a saved
// `GOFLAGS=-modfile=/some/other/checkout/go.mod` shapes this build: it fails
// opening a file that belongs to another checkout, and the launcher reports a
// build failure where the caller should have had a queue answer. The fixture
// builds in this file already set GOENV=off, which is exactly why they cannot
// see it -- the production path keeps whatever file the developer has saved.
//
// The oracle is the built helper running, the same one the cross-build case
// uses: a build handed the saved modfile never produces a binary, so the
// fixture's sentinel never appears, while a build given its own environment
// writes the helper that writes it.
//
// This case used to cancel the build at the fixture FIFO instead, and that
// oracle was wrong in both directions. On Linux it read the wait channel of the
// go command's leader, but a multi-threaded build is not its leader: another
// thread opening the FIFO leaves the leader in a different wait state, so a
// correct build was reported as not past the module file yet and the case timed
// out with the blocked build still running in its own group. On Darwin there is
// no wait channel, so the fallback accepted any sleeping process and the case
// could signal before Go had read the modfile, passing on exit 130 for a reason
// it did not have. Letting the build finish needs no intermediate observation.
func TestMergeEnqueueLauncherBuildsDespiteASavedModfile(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckout(t, fixtureTmpDir(t, "tmp"))
	c.requireIsolated(t)
	// A plain source file, not the fixture's FIFO: this build has to finish, and
	// the artifact it produces is the whole answer.
	if err := os.Remove(filepath.Join(c.root, "cmd", "evener-dev", "bin", "hold.go")); err != nil {
		t.Fatalf("remove the blocking fifo: %v", err)
	}
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
	envFile := filepath.Join(f.dir, "go-env")
	writeTestFile(t, envFile, []byte("GOFLAGS=-modfile="+filepath.Join(f.dir, "no-such.mod")+"\n"), 0o600)
	for _, m := range []string{c.root, c.tmp} {
		t.Cleanup(func() { assertNoSurvivors(t, m) })
	}
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	// GOENV names the saved file rather than being switched off: that is the
	// developer's state this case is about.
	cmd.Env = envOverride(cmd.Env,
		"TMPDIR="+c.tmp,
		"GOWORK=off",
		"GOENV="+envFile,
	)
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	runLauncher(t, cmd, f)
	// Exit 0 is the launcher reaching a queue decision through a helper it built
	// itself. It is 2 when the build failed, which is what passing the saved
	// modfile through produces.
	mustExit(t, cmd, f, 0)
	testwait.Until(t, "the helper this build produced to run", func() bool {
		_, err := os.Stat(c.sentinel)
		return err == nil
	})
}

// TestMergeEnqueueLauncherBuildsItsOwnWorkspaceDespiteAnExportedGOWORK pins
// which files decide how the launcher's build resolves its dependencies.
//
// GOWORK was the one Go variable the build did not pin. The go command reads an
// exported one in preference to the tree it is running in, so a developer with
// GOWORK exported for another checkout -- an ordinary thing when several trees
// are in play -- rewrote this build's module resolution. Two shapes, both real:
// a workspace that does not exist fails to load, and one that belongs to another
// tree does not use this directory, so the module resolves against dependencies
// that are not there. Either way the launcher reports a build failure where the
// caller should have had a queue answer.
//
// Switching it off is not the fix, which is why it is pinned to the checkout
// rather than cleared: this module requires its siblings at v0.0.0 and only
// go.work's replace directives name where they live. A fixture with a go.work of
// its own therefore proves both halves at once, because its build needs the
// replace to resolve the stub's import and fails without it.
func TestMergeEnqueueLauncherBuildsItsOwnWorkspaceDespiteAnExportedGOWORK(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckout(t, fixtureTmpDir(t, "tmp"))
	c.requireIsolated(t)
	if err := os.Remove(filepath.Join(c.root, "cmd", "evener-dev", "bin", "hold.go")); err != nil {
		t.Fatalf("remove the blocking fifo: %v", err)
	}
	// A sibling module the fixture's go.work brings into the build. Its own
	// go.mod is what makes it a module the workspace can use.
	sibling := filepath.Join(c.root, "sibling")
	if err := os.MkdirAll(filepath.Join(sibling, "marker"), 0o755); err != nil {
		t.Fatalf("mkdir sibling module: %v", err)
	}
	writeTestFile(t, filepath.Join(sibling, "go.mod"), []byte("module example.invalid/fixture/sibling\n\ngo 1.21\n"), 0o644)
	writeTestFile(t, filepath.Join(sibling, "marker", "marker.go"), []byte("package marker\n\nconst Name = \"sibling\"\n"), 0o644)
	writeTestFile(t, filepath.Join(c.root, "go.work"), []byte("go 1.21\n\nuse (\n\t.\n\t./sibling\n)\n"), 0o644)
	// The stub now needs the replace, so a launcher that builds standalone fails
	// instead of passing by luck.
	stub := "package main\n\nimport (\n\t\"os\"\n\n\t\"example.invalid/fixture/sibling/marker\"\n)\n\nfunc main() {\n\t_ = os.WriteFile(" +
		strconv.Quote(c.sentinel) + ", []byte(marker.Name), 0o644)\n}\n"
	writeTestFile(t, filepath.Join(c.root, "cmd", "evener-dev", "bin", "main.go"), []byte(stub), 0o644)
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
	for _, m := range []string{c.root, c.tmp} {
		marker := m
		t.Cleanup(func() { assertNoSurvivors(t, marker) })
	}
	// A workspace belonging to a tree that is not this one, and that cannot even
	// be loaded. The launcher has to ignore it entirely.
	foreign := filepath.Join(f.dir, "other-checkout")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatalf("mkdir foreign checkout: %v", err)
	}
	writeTestFile(t, filepath.Join(foreign, "go.work"), []byte("go 1.21\n\nuse ./not-here\n"), 0o644)
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env,
		"TMPDIR="+c.tmp,
		"GOWORK="+filepath.Join(foreign, "go.work"),
		"GOENV=off",
	)
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	runLauncher(t, cmd, f)
	mustExit(t, cmd, f, 0)
	testwait.Until(t, "the helper this build produced to run", func() bool {
		body, err := os.ReadFile(c.sentinel)
		return err == nil && string(body) == "sibling"
	})
}

// TestMergeEnqueueLauncherRefusesANonNumericWaitBeforeBuilding pins that a bad
// --wait is a usage error at once.
//
// It used to reach the helper, which put a scratch mint and a full `go build`
// between the operator's typo and the answer. On a cold or noexec host the build
// then failed first, and its exit 2 named the build while the real problem was
// the argument. The parser is still the helper's -- overflow, leading zeros and
// the duration bound are its business (#5304) -- but "not a number" is answerable
// here, and the pre-port launcher answered it here.
//
// The oracle is that nothing was built: a launcher that answered correctly but
// built first would still have spent minutes on the typo.
func TestMergeEnqueueLauncherRefusesANonNumericWaitBeforeBuilding(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckoutNamed(t, fixtureTmpDir(t, "tmp"), "checkout")
	c.requireIsolated(t)
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nprintf 'helper-ran\\n' >> \"$EVENER_FIXTURE_LOG\"\n")
	// Registered before the launcher starts, so a regression that gets as far as
	// the build still leaves nothing behind. `mustExit` has a deadline, and a
	// build blocked on the fixture's FIFO has a process group of its own that no
	// other cleanup here names: without these sweeps a regression would fail this
	// case and leave a `go build` running forever. The checkout and its scratch
	// path are the two strings its argv can carry.
	t.Cleanup(func() { assertNoSurvivors(t, c.root) })
	t.Cleanup(func() { assertNoSurvivors(t, c.tmp) })
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--wait", "nope", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env, "TMPDIR="+c.tmp, "GOWORK=off", "GOENV=off")
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	runLauncher(t, cmd, f)
	// Usage, not 1: nothing was decided, so a caller must not read this as a
	// queue outcome and must not retry.
	mustExit(t, cmd, f, 2)
	// Nothing was built: the answer came before the build, not after it.
	if survivors := processesNaming(t, c.tmp); len(survivors) > 0 {
		t.Fatalf("the launcher built before refusing the argument: %v", survivors)
	}
	for _, line := range f.lines(t) {
		if strings.HasPrefix(line, "helper-ran") {
			t.Fatalf("the launcher started the helper for a usage error: %v", f.lines(t))
		}
	}
}

// TestMergeEnqueueLauncherHelpPrintsTheWholeContract pins that --help prints the
// script's contract block: every comment line above `set -euo pipefail`, and
// nothing else.
//
// The oracle is structural rather than textual. --help used to be a numbered
// line range over the header, so every paragraph added to the contract moved the
// end and an un-updated range silently truncated it -- it stopped mid-sentence
// through one review round. Comparing the printed lines against the header's own
// comment block says the whole contract came out without restating any of it, so
// the help text stays free to change wording.
func TestMergeEnqueueLauncherHelpPrintsTheWholeContract(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("bash", mergeEnqueueLauncher, "--help").CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("--help exit = %v, want the usage status 2", err)
	}
	body, err := os.ReadFile(mergeEnqueueLauncher)
	if err != nil {
		t.Fatalf("read the launcher: %v", err)
	}
	// The contract is the leading comment block, stopping at the first line that
	// is not a comment. Its last line is the exit-code paragraph's final sentence.
	var contract []string
	for _, line := range strings.Split(string(body), "\n")[1:] {
		if !strings.HasPrefix(line, "#") {
			break
		}
		contract = append(contract, strings.TrimPrefix(strings.TrimPrefix(line, "#"), " "))
	}
	if len(contract) == 0 {
		t.Fatal("the launcher has no leading comment block to print")
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if strings.Join(got, "\n") != strings.Join(contract, "\n") {
		t.Fatalf("--help printed %d lines, the contract block is %d:\n%s", len(got), len(contract), out)
	}
}

// TestMergeEnqueueLauncherDoesNotPassThroughAWrapperStatusOverALiveHelper pins
// that the survivor check runs on every exit, not only a zero one.
//
// A wrapper that starts the helper in the background and then exits 1 is the same
// defect as one that exits 0, with a worse report. The launcher used to pass the
// wrapper's status straight through, so the operator was told "the queue dropped
// your PR, retry" -- a queue outcome -- for a run that never asked GitHub
// anything, while the helper went on polling or submitting and the EXIT trap
// removed the group file it was publishing into. A drop report invites a retry of
// a PR that may already be queued.
//
// The oracle is the launcher's status and the helper's death, not a log line: 2
// is "the launcher could not get as far as a decision", and the helper the wrapper
// left must be stopped before that status is returned.
func TestMergeEnqueueLauncherDoesNotPassThroughAWrapperStatusOverALiveHelper(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
# The wrapper starts the helper, records it, and exits 1 without waiting for it: a
# status that describes the wrapper and not the queue.
"$EVENER_FIXTURE_HELPER" &
printf 'inner-pid=%s;pgid=%s\n' "$!" "$(ps -o pgid= -p "$!" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
exit 1
`)
	inner := filepath.Join(f.dir, "helper.sh")
	writeTestFile(t, inner, []byte(`#!/usr/bin/env bash
trap '' TERM
printf 'helper-started\n' >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`), 0o755)
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_HELPER="+inner,
		"EVENER_HELPER_TERM_GRACE_SECONDS=0",
		"EVENER_STOP_TREE_GRACE_SECONDS=1",
	)
	t.Cleanup(func() { assertNoSurvivors(t, f.dir) })
	runLauncher(t, cmd, f)
	innerPID, _ := logPIDAndGroup(t, f, "inner-pid=")
	if innerPID == 0 {
		t.Fatalf("the wrapper never recorded the helper it started: %s", f.output(t))
	}
	// 1 is the wrapper's own status and would read as a queue drop. The launcher
	// must answer 2, which is its own failure, and must not leave the helper alive.
	mustExit(t, cmd, f, 2)
	testwait.Until(t, fmt.Sprintf("the helper the wrapper left behind (pid %d) to be stopped before the launcher returns", innerPID), func() bool {
		return pidGone(innerPID)
	})
}

// TestMergeEnqueueLauncherDoesNotReportSuccessForAWrapperThatLeftItsHelper pins
// that the launcher's exit status is a queue outcome and not a process spawn.
//
// An EVENER_DEV_BIN wrapper is allowed to start the real helper in the
// background. If it then exits 0 by itself, `wait` returns immediately and the
// launcher used to report 0 -- "your PR is in the queue" -- before the helper
// had said anything about the PR. Worse, exit 0 runs the EXIT trap, which removes
// the group file the helper publishes into, while the helper is still polling or
// still submitting. The operator is told the enqueue succeeded by a run that
// never saw a queue answer, and the handle the forced stop would have needed is
// deleted underneath a live call.
//
// The oracle is the launcher's status, not a log line: a run that exits 0 while
// the helper it started is still running is the defect, whatever it printed. The
// helper is `trap ” TERM` and loops, so a launcher that reports 0 and returns
// leaves it running, and the sweep at the end fails on it. A launcher that
// refuses reports 2 and stops the helper first, which is the second assertion.
//
// 2 rather than 1 is the contract's own choice: 1 means the queue dropped the PR
// and invites a retry, and nothing here reached the queue. 130 would claim this
// was a cancellation, and no signal was sent.
func TestMergeEnqueueLauncherDoesNotReportSuccessForAWrapperThatLeftItsHelper(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
# The wrapper starts the helper and exits zero without waiting for it: the shape
# the override contract forbids, and the one the launcher used to read as success.
#
# It waits for the helper's readiness marker before exiting, and that wait is the
# difference between a case and a coin toss. The helper publishes its call group
# to the handle and logs it a moment after it starts; an earlier version of this
# fixture exited as soon as it had logged the inner pid, so if the launcher
# reached its survivor check and froze the helper first, the call was never
# logged, the case waited for a line that could no longer appear, and it timed
# out on a launcher that had done the right thing. The marker makes "the helper
# is up and has published" the precondition of the wrapper's exit, so the state
# the assertions need is established before the case depends on it.
"$EVENER_FIXTURE_HELPER" &
printf 'inner-pid=%s;pgid=%s\n' "$!" "$(ps -o pgid= -p "$!" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
deadline=$((SECONDS + 30))
while [ "$SECONDS" -lt "$deadline" ]; do
  [ -f "$EVENER_FIXTURE_READY" ] && break
  sleep 0.02
done
if [ ! -f "$EVENER_FIXTURE_READY" ]; then
  printf 'wrapper-readiness-timeout\n' >> "$EVENER_FIXTURE_LOG"
fi
exit 0
`)
	ready := filepath.Join(f.dir, "helper-ready")
	inner := filepath.Join(f.dir, "helper.sh")
	writeTestFile(t, inner, []byte(`#!/usr/bin/env bash
trap '' TERM
# A poller in a group of its own, published in the handle: this is the second
# half of the finding. A gh call left behind by a wrapper is reachable only
# through the published handle, so a launcher that reports 0 and removes the
# handle leaves it submitting with nothing naming it.
set -m
"$EVENER_FIXTURE_GH" &
call=$!
set +m
printf '%s\n' "$call" > "$EVENER_MERGEQ_GH_GROUP_FILE"
printf 'call-pid=%s;pgid=%s\n' "$call" "$(ps -o pgid= -p "$call" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
printf 'helper-started\n' >> "$EVENER_FIXTURE_LOG"
# Published and logged, so the wrapper may exit -- and only now, because the
# assertions below read these two lines.
: > "$EVENER_FIXTURE_READY"
while :; do sleep 0.05; done
`), 0o755)
	gh := filepath.Join(f.dir, "gh.sh")
	writeTestFile(t, gh, []byte(`#!/usr/bin/env bash
trap '' TERM
while :; do sleep 0.05; done
`), 0o755)
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_HELPER="+inner,
		"EVENER_FIXTURE_GH="+gh,
		"EVENER_FIXTURE_READY="+ready,
		"EVENER_HELPER_TERM_GRACE_SECONDS=0",
		"EVENER_STOP_TREE_GRACE_SECONDS=1",
	)
	t.Cleanup(func() { assertNoSurvivors(t, f.dir) })
	runLauncher(t, cmd, f)
	innerPID, _ := logPIDAndGroup(t, f, "inner-pid=")
	if innerPID == 0 {
		t.Fatalf("the wrapper never recorded the helper it started: %s", f.output(t))
	}
	// The launcher must not answer for a queue it heard nothing from. Both halves
	// matter: the status must be the launcher failure 2, and the helper must be
	// stopped -- a refusal that leaves a polling helper behind is the leak this
	// case exists to prevent, not a fix for it.
	mustExit(t, cmd, f, 2)
	testwait.Until(t, fmt.Sprintf("the helper the wrapper left behind (pid %d) to be stopped before the launcher exits 2", innerPID), func() bool {
		return pidGone(innerPID)
	})
	// And the call the helper published, which is a group of its own: the forced
	// stop reads the handle, so this is what proves the second half was reached.
	callPID, _ := logPIDAndGroup(t, f, "call-pid=")
	if callPID == 0 {
		t.Fatalf("the helper never published a call: the fixture did not reach the second half; %s", f.output(t))
	}
	testwait.Until(t, fmt.Sprintf("the published call (pid %d) to be stopped before the launcher exits 2", callPID), func() bool {
		return pidGone(callPID)
	})
}

// TestMergeEnqueueLauncherRefusalNamesExecutionWhenTrustWasNotTheProblem pins
// that the no-scratch refusal tells the operator the truth about why it refused.
//
// All three candidates can pass the chain rule and still be useless, because the
// launcher builds a helper and runs it out of scratch: a noexec mount is
// writable, private and unexecutable. The old message said every candidate "sits
// under a directory another user can replace", which sends an operator on a
// noexec host -- a container with TMPDIR on a noexec /dev/shm is the ordinary
// case -- looking for a trust problem that is not there, and hides the one that
// is.
//
// The oracle is the reason word in the refusal, read from the launcher's stderr.
// The exit status is 2 either way, so status alone cannot tell the two apart;
// this is a case about what the message says, and about the fact that it is said
// before anything runs.
func TestMergeEnqueueLauncherRefusalNamesExecutionWhenTrustWasNotTheProblem(t *testing.T) {
	t.Parallel()
	// /run/lock is noexec on this host. The case skips by name when the host has
	// no writable noexec mount rather than pretending to have checked.
	const noexecRoot = "/run/lock"
	if info, err := os.Stat(noexecRoot); err != nil || !info.IsDir() {
		t.Skipf("%s is not present on this host", noexecRoot)
	}
	noexec, why := noexecFixtureProbeDir(t, noexecRoot, "")
	if noexec == "" {
		t.Skip(why)
	}
	if !launcherTrustsPath(t, noexec) {
		t.Skipf("%s does not pass the chain rule on this host, so the case would be about trust and not execution", noexec)
	}
	noexec = resolvePath(t, noexec)
	// All three candidates are the same trusted noexec directory, so the refusal
	// cannot be about trust: every one of them passed the chain rule.
	//
	// The script under test is the fixture's copy, and the helper it could build
	// is the fixture's stub. This case used to run the repository's own launcher
	// with EVENER_DEV_BIN deleted, which is the one shape that can reach the real
	// `go build ./cmd/evener-dev/bin` and then a live GitHub lookup: a regression
	// that selected an executable fallback would have run the real helper from a
	// test. Its assertion was vacuous in the same move -- it watched the override
	// the case had just removed, so it could not have seen that helper either.
	c := newFixtureCheckout(t, fixtureTmpDir(t, "tmp"))
	c.requireIsolated(t)
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nprintf 'helper-ran\\n' >> \"$EVENER_FIXTURE_LOG\"\n")
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env,
		"TMPDIR="+noexec,
		"XDG_RUNTIME_DIR="+noexec,
		"HOME="+noexec,
	)
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	t.Cleanup(func() { assertNoSurvivors(t, noexec) })
	// Registered before the launcher starts: a regression that gets as far as the
	// build leaves a FIFO-blocked `go build` in a process group of its own, and
	// the checkout and its scratch are the two strings that argv can carry.
	t.Cleanup(func() { assertNoSurvivors(t, c.root) })
	t.Cleanup(func() { assertNoSurvivors(t, c.tmp) })
	runLauncher(t, cmd, f)
	mustExit(t, cmd, f, 2)
	// The built helper writes this when it runs. It is the observable of the
	// helper this checkout would actually produce, so the assertion covers the
	// path the deleted override would have taken rather than the one it removed.
	if _, err := os.Stat(c.sentinel); err == nil {
		t.Fatalf("the launcher built and ran a helper although no candidate could execute a program: %s exists", c.sentinel)
	}
	if lines := f.lines(t); len(lines) > 0 {
		t.Fatalf("the launcher ran the fixture helper although no candidate could execute a program: %v", lines)
	}
	// The reason is asserted as a code, not as prose. The message around it is
	// the operator's copy and will be reworded; `reason=no-executable-root` is the
	// structured value that says which refusal happened, so a diagnostic edit
	// cannot break this case and this case cannot pin a sentence.
	if got := refusalReason(t, f); got != "no-executable-root" {
		t.Fatalf("the refusal reason is %q, want %q: every candidate passed the chain rule and none could execute a program, so a reason of another kind sends a noexec host's operator to look for a trust problem that is not there", got, "no-executable-root")
	}
}

// TestMergeEnqueueLauncherFixturesDoNotChmodThroughAReplaceableParent pins that
// the fixture selector validates a base before it creates anything there.
//
// The defect this replaces was a test that could change the operator's
// filesystem: `os.MkdirTemp` under a base another user can replace, then
// `os.Chmod` on the result. chmod follows symlinks, so a swap between the two
// lands the chmod on whatever the other user pointed at, and a directory the
// operator owns outside the fixture changes mode because a test ran.
//
// Two properties, both about what the function refuses and what it touches:
//
//   - a base another user can replace is refused by name, with nothing created
//     in it -- checked by counting the entries before and after, because "did
//     not create" is the assertion and a returned error alone would pass for a
//     function that created and then cleaned up;
//   - a base whose created entry would not be a directory is refused rather than
//     chmodded. That half is exercised by asking the function about a base that
//     cannot create, which is the same branch a successful mkdir takes into the
//     Lstat.
//
// The untrusted base is the ambient TMPDIR made world-writable: it is a real
// directory on this machine, so the launcher's own rule rejects it for the real
// reason and not for a manufactured one.
func TestMergeEnqueueLauncherFixturesDoNotChmodThroughAReplaceableParent(t *testing.T) {
	t.Parallel()
	// A base this user owns, at 0777 without the sticky bit: replaceable exactly
	// as a shared directory without stickiness is.
	untrusted := filepath.Join(resolvePath(t, fixtureScratchBase(t)), "replaceable-base")
	if err := os.MkdirAll(untrusted, 0o700); err != nil {
		t.Fatalf("create %s: %v", untrusted, err)
	}
	if err := os.Chmod(untrusted, 0o777); err != nil {
		t.Fatalf("make %s replaceable: %v", untrusted, err)
	}
	if launcherTrustsPath(t, untrusted) {
		t.Skipf("%s is trusted on this host, so the case cannot show the refusal", untrusted)
	}
	// The decidable half of the finding, and the one that can actually be
	// observed: chmod must not follow a link. A directory the operator owns
	// outside the fixture is planted as a symlink where a chain component would
	// be, and the guard has to refuse it rather than chmod through it. Reading the
	// target's mode afterwards is the proof -- a guard that reported the link and
	// changed the mode anyway would pass a reason-only assertion.
	//
	// The create-then-chmod window itself is not observable from a test: the
	// temp name is random, so no planted entry can catch the transient create, and
	// a before/after listing sees a create-and-remove pair as nothing. That
	// ordering is therefore correct by construction and unproven here, which the
	// PR body says rather than implying this case covers it.
	outside := filepath.Join(resolvePath(t, fixtureScratchBase(t)), "outside-target")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("create the link target: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	link := filepath.Join(resolvePath(t, fixtureScratchBase(t)), "planted-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("plant the link: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	if reason := privateChainRefusal(link); reason == "" {
		t.Fatalf("%s is a symlink and the chain guard accepted it, so the chmod that follows would change the mode of %s instead", link, outside)
	}
	// The fault is real, not hypothetical: chmod through the link does change the
	// target, which is why the guard exists.
	if err := os.Chmod(link, 0o700); err != nil {
		t.Fatalf("chmod through the link: %v", err)
	}
	if info, err := os.Stat(outside); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("chmod through a symlink did not reach its target, so this machine cannot show the hazard: %v %v", info, err)
	}
	if err := os.Chmod(outside, 0o755); err != nil {
		t.Fatalf("restore the target: %v", err)
	}
	// And the selector still refuses the untrusted base itself.
	if dir, reason := createPrivateFixtureDir(t, untrusted); reason == refusalNone {
		_ = os.RemoveAll(dir)
		t.Fatalf("the selector accepted %s, but that base is replaceable by another user", untrusted)
	}
	// The executable half: a base that is trusted but cannot run a program is
	// refused for that reason, not for the chain. /run/lock is noexec on this
	// host, and the case skips by name where it is not.
	const noexecRoot = "/run/lock"
	//
	// Not noexecFixtureProbeDir: that helper skips a host whose base cannot
	// execute, and this subtest exists to exercise exactly that host. The ordering
	// rule is the same though, and applies here first: the base is validated before
	// anything is created, chmodded or executed beneath it, because all three of
	// those are operations inside a directory this fixture never checked.
	if _, err := os.Stat(noexecRoot); err == nil {
		if base, err := filepath.EvalSymlinks(noexecRoot); err == nil && launcherTrustsPath(t, base) {
			probe, err := os.MkdirTemp(base, "merge-enqueue-noexec.")
			if err == nil {
				t.Cleanup(func() { _ = os.RemoveAll(probe) })
				_ = os.Chmod(probe, 0o700)
				if launcherTrustsPath(t, probe) && !launcherCanExecuteIn(t, probe) {
					dir, reason := createPrivateFixtureDir(t, probe)
					// A code, not the sentence: the sentence is this file's own
					// wording, and comparing it pinned a message nothing in
					// production emits.
					if reason != refusalNoexec {
						t.Fatalf("a trusted noexec base was refused with %q, want %q: a fixture that launches a script there fails at the script's start", reason, refusalNoexec)
					}
					if dir != "" {
						_ = os.RemoveAll(dir)
						t.Fatalf("the selector returned %s for a noexec base", dir)
					}
				}
			}
		}
	}
}
