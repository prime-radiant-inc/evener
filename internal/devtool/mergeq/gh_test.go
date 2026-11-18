//go:build !windows

package mergeq

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/testwait"
)

// fixtureExecDir returns a private directory this test can execute a program
// from, under a base whose whole ancestry another local user cannot replace.
//
// The fixtures here launch shell scripts, and `t.TempDir()` descends from the
// ambient TMPDIR, so that made two things assumptions. The first is
// executability: on a host whose TMPDIR is mounted noexec -- a hardened CI
// image, a container with a locked-down /tmp -- the script cannot run, the
// fixture never publishes the PID the case waits for, and the case times out on
// a failure that says nothing about the client. An executable GOTMPDIR does not
// help: that is the go command's work directory, not where these fixtures write.
//
// The second is trust. A directory's own mode bounds who can write inside it,
// not who can replace it: another user who can replace an *ancestor* can swap
// this whole directory for one of their own, and the case then executes their
// script with this user's privileges. The fixture executes a program out of this
// directory, so it has to know the path it executes through. That check is the
// fixture's own (fixtureChainTrusted) and not the launcher's.
//
// Both are established before anything is created, written or run, and both are
// asked of the chain rather than of the leaf. A check that comes after the first
// mkdir, chmod, write or exec is too late to be a check.
//
// Candidate bases are tried cheapest-first -- the ambient temp dir, then the
// per-session runtime dir, then /tmp, then a directory under $HOME -- so a noexec
// or replaceable TMPDIR is passed over rather than failing the case. Every
// directory created here is removed with the test, so a fallback leaves nothing
// behind.
func fixtureExecDir(t *testing.T) string {
	t.Helper()
	var refusals []string
	for _, base := range fixtureBaseCandidates() {
		dir, reason := execDirUnder(t, base)
		if reason == "" {
			return dir
		}
		refusals = append(refusals, base+": "+reason)
	}
	t.Fatalf("no base on this machine can host these fixtures, so a case below would time out waiting for a script that never ran:\n\t%s\nset TMPDIR to an exec-capable directory whose ancestry this user controls, rather than reading the timeout as a client bug", strings.Join(refusals, "\n\t"))
	return ""
}

// fixtureBaseCandidates is the bases to try: the environment's own roots first,
// then the root every Unix has.
//
// /tmp is last and named explicitly because the other candidates all descend
// from the runner's environment, and that environment can offer nothing. Under
// umask 0002 the module runner makes group-writable components in TMPDIR, which
// the ancestor rule refuses; a headless run has no XDG_RUNTIME_DIR; and the
// module gate hands the test a private HOME whose cache lives in a separate
// XDG_CACHE_HOME, so $HOME/.cache does not exist. Without /tmp those runs failed
// to start a fixture on a machine with an executable, root-owned, sticky /tmp the
// whole time. It goes after the environment's own roots so a host that offers a
// usable one still uses it; the exec probe in execDirUnder decides usability.
func fixtureBaseCandidates() []string {
	candidates := []string{os.TempDir()}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		candidates = append(candidates, dir)
	}
	candidates = append(candidates, "/tmp")
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".cache"))
	}
	return candidates
}

// fixtureChainTrusted reports whether every component of path, from / down, is
// a directory another local user cannot replace, and whether path itself is
// absolute.
//
// This is a property of the fixture, not a restatement of the launcher's rule,
// and the distinction is the point. The fixtures here write shell scripts and
// execute them, so a base whose ancestry another user can swap would execute
// that user's script with this user's privileges. That hazard exists whatever
// the launcher does with its own scratch roots, and it is why this check lives
// beside the code it protects.
//
// It used to ask the launcher instead, by sed-ing its predicate out of
// scripts/ops/merge-enqueue.sh and eval-ing it. That coupled a Go test to a
// shell function's source text: the port moved the decisions out of that script
// and the tail of the file the predicate lived in was no longer this PR's, so
// the probe died with `scratch_path_is_trusted: command not found` (exit 127)
// and every fixture below failed at setup. A fixture that asks the code under
// test to build its own sandbox is testing the fixture; this one establishes its
// own chain.
//
// The rule is the one the hazard needs and no more:
//
//   - A component this user or root owns and that is neither group- nor
//     other-writable is trusted. /tmp's sticky bit is not special-cased: a
//     sticky directory still lets another user add entries, and this walk is
//     about replacing the components of a path this test has already resolved,
//     which sticky does prevent. Sticky therefore settles an other-writable
//     component, which is exactly /tmp.
//   - `other` is read from the mode and is independent of any ACL: no ACL can
//     widen it, and an ACL that shows a read-only owning group says nothing about
//     it. A world-writable directory with no sticky bit is replaceable however
//     tidy its ACL looks.
//   - An extended ACL is read with getfacl when one is present, because on Linux
//     the mode's group bits are the ACL MASK rather than the owning group's
//     permission. Reading the mode there refuses a directory the ACL shows nobody
//     can write, and on a runner where that directory is the only executable
//     candidate every fixture then fails at setup. The owning group's own
//     permission is the `group::` line, capped by the mask, and a named user or
//     named group entry counts too: its effective permission is its own bits AND
//     the mask, so a named rwx under a writable mask is a grant even though the
//     mode's group position is the only place the mask is visible.
//   - Anything that cannot be read is untrusted. "The check failed" and "the
//     answer is no" are different facts and only one is safe to act on.
//   - Every component is inspected with Lstat, never Stat. The walk runs over
//     the path EvalSymlinks already resolved, so a component that is a symlink
//     now is one that appeared AFTER that resolution: another user replaced it
//     in the window between the resolve and this check. Following it would
//     inspect the owner and mode of the target -- which can be this user's own
//     private directory -- while the path still holds an entry that user can
//     swap again, and the fixture would then write and execute scripts through
//     it. A component that resolves to a symlink is refused for that reason and
//     not because symlinks are unwanted in general: macOS's /var ->
//     /private/var is resolved away before this walk, which is why EvalSymlinks
//     happens first and only the resolved path is checked.
func fixtureChainTrusted(t *testing.T, path string) (reason string) {
	t.Helper()
	if !filepath.IsAbs(path) {
		return path + " is not an absolute path"
	}
	for _, component := range pathComponents(filepath.Clean(path)) {
		if reason := fixtureComponentReplaceable(t, component); reason != "" {
			return reason
		}
	}
	return ""
}

// fixtureComponentReplaceable names why component can be replaced by another
// local user, or "" when it cannot.
func fixtureComponentReplaceable(t *testing.T, component string) string {
	t.Helper()
	// Lstat, not Stat: this walks a path EvalSymlinks already resolved, so a
	// symlink here is a component that changed after that resolution. Stat would
	// follow it and describe the target -- a directory this user owns at 0700,
	// say -- while the name along the way is still one another user can retarget.
	info, err := os.Lstat(component)
	if err != nil {
		return fmt.Sprintf("cannot inspect %s (%v)", component, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(component)
		return fmt.Sprintf("%s is a symlink to %q, which appeared after the path was resolved, so the entry another user can retarget is what the fixture would write through rather than the directory it points at", component, target)
	}
	if !info.IsDir() {
		return component + " is not a directory"
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return component + ": cannot read its owner"
	}
	if uid := int(stat.Uid); uid != os.Getuid() && uid != 0 {
		return fmt.Sprintf("%s is owned by uid %d, not by this user or root", component, uid)
	}
	if info.Mode()&os.ModeSticky != 0 {
		// Another user may add entries here but cannot rename or delete one this
		// user created, so a component already resolved under it is safe.
		return ""
	}
	// `other` first, and from the mode only: no ACL can widen it, and an ACL whose
	// group question reads clean says nothing about it. Asking the ACL first and
	// returning on its answer skipped this check entirely, which accepted a
	// world-writable directory carrying a restrictive ACL.
	perm := info.Mode().Perm()
	if perm&0o002 != 0 {
		return component + " is writable by other users and has no sticky bit, so another user can replace entries in it"
	}
	if acl, ok := fixtureReadACL(t, component); ok {
		switch granted, readable := aclForeignWrite(acl); {
		case !readable:
			// The ACL is unreadable, so nothing about it can be shown to grant
			// nothing. That is a refusal, not a pass.
			return component + " has an access ACL this fixture could not read, so no entry in it can be shown to be harmless"
		case granted != "":
			return component + " has an access ACL entry that may write: " + granted + ", so another local user can replace entries in it"
		default:
			// No foreign entry is effective, so the ACL cannot be used to replace an
			// entry here whatever the mode's group position says.
			return ""
		}
	}
	if perm&0o020 != 0 {
		return fmt.Sprintf("%s is writable by group %d with no sticky bit, so another member of that group can replace entries in it", component, stat.Gid)
	}
	return ""
}

// fixtureReadACL returns component's access ACL as getfacl prints it, and
// whether it could be read at all.
//
// A directory with no extended ACL needs no read: getfacl still prints the three
// base entries, so the caller cannot tell "no ACL" from "an ACL that grants
// nothing" by the output alone. That is why the mode decides first for an
// ordinary directory and the ACL only refines the group answer, which is where
// Linux puts the mask.
//
// A missing getfacl is not a reason to fail a fixture: a platform without it
// (macOS) has no POSIX access ACL in this sense, and the mode answer stands.
func fixtureReadACL(t *testing.T, component string) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath("getfacl"); err != nil {
		return "", false
	}
	// An ACL is only present when ls marks the entry with `+`. Asking getfacl on
	// every component instead would fork a process per path component on every
	// fixture, and the answer for the ordinary case is in the mode.
	if _, err := exec.LookPath("ls"); err != nil {
		return "", false
	}
	listed, err := exec.Command("ls", "-ldn", component).Output()
	if err != nil {
		return "", false
	}
	if !strings.Contains(string(listed), "+") {
		return "", false
	}
	acl, err := exec.Command("getfacl", "-n", "-p", "-c", component).Output()
	if err != nil {
		return "", false
	}
	return string(acl), true
}

// aclForeignWrite reads numeric getfacl output and names the first entry that
// lets some user other than this one or root replace an entry in the directory,
// plus whether the ACL could be read at all.
//
// An entry may replace an entry in the directory when it holds BOTH write and
// search: write alone cannot see or remove a name, and search alone cannot create
// one. The effective permission is the entry's bits AND the mask, because the mask
// caps every named entry and the owning group; a named `rwx` under a read-only
// mask cannot write at all, and refusing that would refuse a directory nobody can
// tamper with.
//
// `user::`, `group::` and `other::` are not consulted: those are the owner,
// alongside the named ones. `other::` is deliberately absent because it lives in
// the mode and the caller checks it first: no ACL can widen it.
//
// A missing mask is not a pass. Without a mask the named entries cannot be shown
// to be limited, and the convention here is that an answer that cannot be shown
// is unsafe.
func aclForeignWrite(acl string) (entry string, readable bool) {
	var mask string
	var seenMask bool
	type aclEntry struct {
		kind string
		name string
		perm string
	}
	var entries []aclEntry
	for line := range strings.SplitSeq(acl, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 3 {
			continue
		}
		switch fields[0] {
		case "mask":
			seenMask = true
			mask = fields[2]
		case "user", "group":
			entries = append(entries, aclEntry{kind: fields[0], name: fields[1], perm: fields[2]})
		}
	}
	if len(entries) == 0 {
		return "", false
	}
	mayReplace := func(perm string) bool {
		return strings.Contains(perm, "w") && strings.Contains(perm, "x")
	}
	for _, e := range entries {
		if e.name == "" {
			// `user::` is the owner, whose grant is this user's own and cannot be
			// used against them. `group::` is the owning group, whose own permission
			// the mode's group position does NOT report once an ACL is present: that
			// position is the mask. It is the one base entry that has to be checked
			// here rather than from the mode.
			if e.kind == "user" {
				continue
			}
			if !mayReplace(e.perm) {
				continue
			}
			if seenMask && !mayReplace(mask) {
				continue
			}
			return "group::" + e.perm, true
		}
		// A named entry. One naming this user or root is this user's own grant, and
		// cannot be used to replace an entry out from under the fixture. It is
		// skipped rather than counted, because refusing it would fail every fixture
		// on a host that grants the operator access to their own TMPDIR.
		if e.kind == "user" && (e.name == strconv.Itoa(os.Getuid()) || e.name == "0") {
			continue
		}
		if _, err := strconv.ParseUint(e.name, 10, 32); err != nil {
			// getfacl -n prints numeric ids; anything else is not an entry this
			// function can reason about, so it is not counted as a grant.
			continue
		}
		if !mayReplace(e.perm) {
			continue
		}
		if seenMask && !mayReplace(mask) {
			continue
		}
		return e.kind + ":" + e.name + ":" + e.perm, true
	}
	return "", true
}

// execDirUnder returns a private, exec-capable directory under base, or "" and
// the reason base cannot host one.
//
// The order is the point. base is resolved, every component from the root down
// is checked, and only a base that passes gets a mkdir, a write or an exec
// beneath it.
func execDirUnder(t *testing.T, base string) (dir, reason string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Sprintf("cannot resolve it (%v)", err)
	}
	if reason := replaceableAncestor(t, resolved); reason != "" {
		return "", reason
	}
	// MkdirTemp is 0700 whatever the runner's umask is, which keeps the fixture's
	// own entry private once the chain above it is trusted.
	//
	// The created path is held in its own variable, not in the named result. A
	// `return "", reason` below clears the result, and a cleanup closing over that
	// result would then RemoveAll("") and leave the directory and its probe behind
	// on every rejected candidate -- a leak in the code that exists to refuse
	// candidates, which is the one place it is least likely to be noticed.
	created, err := os.MkdirTemp(resolved, "mergeq-fixture.")
	if err != nil {
		return "", fmt.Sprintf("cannot create a directory in it (%v)", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(created) })
	probe := filepath.Join(created, "exec-probe.sh")
	writeExecutable(t, probe, "#!/usr/bin/env bash\nexit 0\n")
	if err := exec.Command(probe).Run(); err != nil {
		return "", fmt.Sprintf("the filesystem cannot execute a program there (%v)", err)
	}
	return created, ""
}

// replaceableAncestor names the first component of path that another local user
// can replace, or "" when the chain is this user's to control.
//
// This is the fixture's own rule, applied to the path the fixture is about to
// create and execute under. It is deliberately not delegated to
// scripts/ops/merge-enqueue.sh: that script's copy of this predicate travels
// with the launcher, and a test does not get its environment by asking the code
// under test to describe one. See fixtureChainTrusted.
func replaceableAncestor(t *testing.T, path string) string {
	t.Helper()
	return fixtureChainTrusted(t, path)
}

// widenedFixtureDir creates a directory under the trusted fixture base and
// returns it, for a case that is about to WIDEN it to a mode another user could
// write through.
//
// The widening is the hazard, so the entry that gets widened has to be one this
// test already owns. `t.TempDir()` descends from the ambient TMPDIR, and on a
// TMPDIR whose ancestor another local user can replace, that user can swap the
// entry between the mkdir and the chmod: the chmod then lands on their target,
// and a test run changes the permissions of a directory the operator owns. The
// fixture would be performing the hazard it exists to describe.
//
// The base is the same one every other fixture here uses, so it is established
// rather than inherited: a private directory under a chain this fixture's own
// rule accepts, on a filesystem that can execute. The ancestry is asked about
// again after the create, because a base is only as good as what it resolved to.
func widenedFixtureDir(t *testing.T, name string) string {
	t.Helper()
	base := fixtureExecDir(t)
	dir := filepath.Join(base, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	created, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	if reason := replaceableAncestor(t, created); reason != "" {
		t.Fatalf("%s was created under %s, which this fixture's own chain rule does not trust: this case is about to widen that directory, so the widening has to land on an entry this test owns (%s)", created, base, reason)
	}
	return dir
}

// pathComponents lists path's components from the root down.
func pathComponents(path string) []string {
	var components []string
	for probe := filepath.Clean(path); ; probe = filepath.Dir(probe) {
		components = append(components, probe)
		if probe == filepath.Dir(probe) {
			break
		}
	}
	for i, j := 0, len(components)-1; i < j; i, j = i+1, j-1 {
		components[i], components[j] = components[j], components[i]
	}
	return components
}

// writeExecutable writes an executable script, holding syscall.ForkLock so a
// concurrent fork in this test binary cannot inherit the write descriptor and
// fail its own exec with ETXTBSY. execsupport/orphanpipe/orphanpipetest's
// WriteScript and install_test.go's writeExecutable hold it for the same reason.
func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// pidGone reports whether pid has terminated. A zombie counts: the process is
// dead, and its parent may not have reaped it yet.
func pidGone(pid int) bool {
	state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return true
	}
	trimmed := strings.TrimSpace(string(state))
	return trimmed == "" || strings.HasPrefix(trimmed, "Z")
}

// TestGHCancellationKillsTheRealCLIAChildWrapperLeaves pins that a cancelled
// call stops the request, not just the wait. WaitDelay bounds the captured pipes
// but kills nothing, so a `gh` that is a wrapper starting the real CLI without
// `exec` would leave the CLI running after the wrapper died, and the helper
// would report 130 for a wait whose enqueue was still in flight.
//
// The staging is the production shape: the wrapper backgrounds a child that
// ignores SIGTERM and then exits, exactly as a wrapper that does not `exec`
// does. The child publishes its pid, the context is cancelled, and the child
// must be gone. A pipe bound alone cannot pass this: closing the pipe returns
// from Run, it does not stop the child.
func TestGHCancellationKillsTheRealCLIAChildWrapperLeaves(t *testing.T) {
	dir := fixtureExecDir(t)
	pidFile := filepath.Join(dir, "child.pid")
	// The child ignores TERM, so only a group KILL reaches it, and it holds no
	// pipe: this case must fail on the child's survival alone.
	child := filepath.Join(dir, "child.sh")
	// Both paths are quoted: t.TempDir() can sit under a TMPDIR with a space in
	// it, and an unquoted one would make the redirect or the exec a syntax error,
	// so no pid would be published and the case would time out before it could
	// cancel anything.
	writeExecutable(t, child, `#!/usr/bin/env bash
trap '' TERM
printf '%s\n' "$$" > `+shellquote.Literal(pidFile)+`
while :; do sleep 0.05; done
`)
	wrapper := filepath.Join(dir, "gh")
	// No exec: the wrapper starts the child and exits, handing nothing on.
	writeExecutable(t, wrapper, `#!/usr/bin/env bash
`+shellquote.Literal(child)+` &
sleep 0.2
exit 0
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// An explicit handle. Empty reads EVENER_MERGEQ_GH_GROUP_FILE, and an
	// inherited value would make this fixture publish over, and then clear, a
	// live launcher's handle for the helper it is stopping.
	groupFile := filepath.Join(fixtureExecDir(t), "gh-group")
	done := make(chan error, 1)
	go func() {
		_, err := (GHClient{GH: wrapper, GroupFile: groupFile}).run(ctx, "api", "graphql")
		done <- err
	}()
	var childPID int
	testwait.Until(t, "the wrapper's child to publish its pid", func() bool {
		body, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(string(body)))
		if err != nil {
			return false
		}
		childPID = parsed
		return true
	})
	// Registered once the pid is known and before any assertion can abort, so a
	// regression fails this case instead of leaving the child to PID 1 -- the
	// same rule the launcher suite's cleanup follows, and the reason a case that
	// tests for a leak must not leak.
	t.Cleanup(func() {
		_ = syscall.Kill(-childPID, syscall.SIGKILL)
		_ = syscall.Kill(childPID, syscall.SIGKILL)
	})
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return after cancellation")
	}
	testwait.Until(t, fmt.Sprintf("the wrapper's client child %d to be killed", childPID), func() bool {
		return pidGone(childPID)
	})
}

// TestGHKilledWhileWaitingForItsReplyIsNotARefusal pins the three endings a GitHub
// call can have, and the one this client used to confuse with a refusal.
//
// A gh wrapper prints a diagnostic to stderr -- a progress note, a warning about
// the gh version -- and is then killed while it is still waiting for the reply,
// because the enqueue deadline expired. GitHub may have accepted the mutation
// before the kill. If stderr alone were treated as the refusal, the client would
// report a definite rejection, Execute would skip the read that finds out, and the
// operator would be told the enqueue failed for a pull request that is queued.
//
// What separates a refusal from an unknown ending is that the process reached its
// OWN exit status. This case distinguishes them by killing the wrapper with
// SIGKILL from inside itself -- an exit status is impossible, exactly as it is
// when the deadline kills the group -- after writing the diagnostic.
func TestGHKilledWhileWaitingForItsReplyIsNotARefusal(t *testing.T) {
	t.Parallel()
	dir := fixtureExecDir(t)
	killed := filepath.Join(dir, "gh-killed")
	// SIGKILL, not TERM: this is the ending the enqueue deadline produces (the
	// client cancels with a group KILL), and it is the one with no exit status.
	// The diagnostic goes to stderr first, so a client that reads stderr alone
	// calls this a refusal.
	writeExecutable(t, killed, `#!/usr/bin/env bash
printf 'gh: fetching the merge queue\n' >&2
kill -9 "$$"
`)
	c := GHClient{GH: killed, GroupFile: filepath.Join(dir, "group")}
	_, err := c.run(context.Background(), "api", "graphql")
	if err == nil {
		t.Fatal("a killed call was read as success: nothing answered it")
	}
	var refusal *RefusalError
	if errors.As(err, &refusal) {
		t.Fatalf("error = %v, reported as a refusal: the process never reached an exit status, so its outcome is unknown and an enqueue that GitHub accepted must still be reconciled", err)
	}
	// The diagnostic is still carried, so the operator keeps what gh said.
	if !strings.Contains(err.Error(), "fetching the merge queue") {
		t.Fatalf("error = %v, want gh's stderr carried as detail: the diagnostic is context a reader needs and is not what makes this a refusal", err)
	}

	// And a call that DID run to its own exit and answered is still a refusal, with
	// the same stderr: the classification is the reply, and stderr only carries it.
	rejected := filepath.Join(dir, "gh-rejected")
	writeExecutable(t, rejected, `#!/usr/bin/env bash
printf '%s' '{"data":{"enqueuePullRequest":null},"errors":[{"type":"UNPROCESSABLE","message":"protected branch"}]}'
printf 'gh: could not enqueue: protected branch\n' >&2
exit 1
`)
	c = GHClient{GH: rejected, GroupFile: filepath.Join(dir, "group")}
	_, err = c.run(context.Background(), "api", "graphql")
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a RefusalError: the process exited 1 on its own, so its answer is no and reconciling it would report another operator's entry as this invocation's work", err)
	}

	// And a call that reached its own exit WITHOUT a reply body is not a refusal,
	// whatever it wrote to stderr. That is a transport failure: GitHub may have
	// performed the mutation before the connection broke. `gh` answers a rejected
	// GraphQL request with the response body on stdout, so the body -- not the exit
	// status and not the diagnostic -- is what says GitHub declined.
	transport := filepath.Join(dir, "gh-transport")
	writeExecutable(t, transport, `#!/usr/bin/env bash
printf 'gh: Post "https://api.github.com/graphql": read tcp: connection reset by peer\n' >&2
exit 1
`)
	c = GHClient{GH: transport, GroupFile: filepath.Join(dir, "group")}
	_, err = c.run(context.Background(), "api", "graphql")
	if err == nil {
		t.Fatal("a failed call with no reply was read as success")
	}
	if errors.As(err, &refusal) {
		t.Fatalf("error = %v, reported as a refusal: the call exited 1 but GitHub's reply never arrived, so whether the mutation landed is unknown and the run must read the queue back", err)
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("error = %v, want ErrOutcomeUnknown: a body-less failure is an unknown outcome, and calling it a failure is what reported an accepted enqueue as refused", err)
	}
	if !strings.Contains(err.Error(), "connection reset by peer") {
		t.Fatalf("error = %v, want gh's own diagnostic carried: the operator needs to read what the transport said", err)
	}

	// The refusal direction: a rejected GraphQL request answers with a body on
	// stdout. That is the only ending where the answer is a definite no.
	deferred := filepath.Join(dir, "gh-graphql-refused")
	writeExecutable(t, deferred, `#!/usr/bin/env bash
printf '%s' '{"data":{"enqueuePullRequest":null},"errors":[{"type":"UNPROCESSABLE","message":"Pull request is not mergeable"}]}'
printf 'gh: Pull request is not mergeable\n' >&2
exit 1
`)
	c = GHClient{GH: deferred, GroupFile: filepath.Join(dir, "group")}
	_, err = c.run(context.Background(), "api", "graphql")
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a RefusalError: GitHub answered the request and the answer was no, which is an answer and not something to reconcile", err)
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("error = %v, also reads as unknown: a refused request has an answer", err)
	}
}

// TestGHPublishDoesNotRemoveAHandleItNeverWrote pins that the client only removes
// a handle it published.
//
// The handle path is configuration -- EVENER_MERGEQ_GH_GROUP_FILE or
// GroupFile -- and a caller can point it anywhere. Publication renames a fresh
// file over that name; when the rename fails, whatever was at the name is not this
// client's and must survive the call. Removing it anyway deleted a user's
// directory: an existing empty directory at the configured path made the rename
// fail and the deferred cleanup then removed the directory itself.
func TestGHPublishDoesNotRemoveAHandleItNeverWrote(t *testing.T) {
	t.Parallel()
	dir := fixtureExecDir(t)
	// A directory at the handle name: the rename onto it fails, which is the
	// publication failure this case needs. It is also the thing a careless cleanup
	// deletes, because os.Remove removes an empty directory.
	handle := filepath.Join(dir, "gh-group")
	if err := os.Mkdir(handle, 0o700); err != nil {
		t.Fatalf("make a directory at the handle name: %v", err)
	}
	gh := filepath.Join(dir, "gh")
	writeExecutable(t, gh, "#!/usr/bin/env bash\nprintf '{\"data\":{}}'\n")
	c := GHClient{GH: gh, GroupFile: handle}
	if _, err := c.run(context.Background(), "api", "graphql"); err != nil {
		t.Fatalf("run: %v, want the call to succeed: a handle that cannot be published costs the launcher a fallback and must not fail a GitHub call", err)
	}
	info, err := os.Lstat(handle)
	if err != nil {
		t.Fatalf("the configured handle name is gone: %v -- the client removed something it never wrote, and a caller that pointed it at an existing directory lost it", err)
	}
	if !info.IsDir() {
		t.Fatalf("the name is now %v, want the directory the caller had there left untouched", info.Mode())
	}
}

// TestGHPublishRefusesAPlantedSymlink pins that the publisher cannot be made to
// truncate a file an attacker chose.
//
// The handle's name is known in advance, and the directory it lives in may be a
// shared TMPDIR on a host that permits symlink traversal. A local user who
// pre-creates that name as a link to a file the operator can write would have
// had the publisher truncate that file, once per GitHub call. The publisher
// writes to a fresh O_EXCL file and renames it over the handle, so a planted link
// is replaced rather than followed.
//
// The link is planted at two names: the handle itself, and the staging name a
// publisher that builds its temporary as `path + ".tmp"` would write through.
// The second is the one that discriminates: rename replaces the handle rather
// than following it, so a link planted only there was never the danger. The
// staging name was, because a plain write follows it.
//
// The oracle is the victim file's bytes, not an error: a publisher that refused
// by failing would pass an error check and leave the operator's file intact by
// accident, and the point is that the handle still gets published.
// TestGHDrainWithoutAReplyIsNotSuccess pins the rule the drain bound cannot
// state by itself. orphanpipe reads ErrWaitDelay after a zero exit as success,
// because normally the child answered and only a descendant kept the pipe open.
// A gh wrapper that backgrounds the real CLI and returns exits before writing
// anything, and that case must not read as a reply: Enqueue would report a
// mutation that never landed.
func TestGHDrainWithoutAReplyIsNotSuccess(t *testing.T) {
	t.Parallel()
	// Both scripts exit 0 and leave a descendant holding the pipe. The first
	// writes nothing before it goes; the second writes a whole reply first and
	// then leaves the same kind of descendant behind. Only the first is a
	// missing answer, and the drain bound must tell them apart.
	silent := filepath.Join(fixtureExecDir(t), "gh-silent")
	writeExecutable(t, silent, "#!/usr/bin/env bash\n( sleep 30 ) &\nexit 0\n")
	answered := filepath.Join(fixtureExecDir(t), "gh-answered")
	writeExecutable(t, answered, "#!/usr/bin/env bash\nprintf '{\"data\":{\"repository\":{\"pullRequest\":{\"id\":\"PR_1\",\"state\":\"OPEN\"}}}}'\n( sleep 30 ) &\nexit 0\n")

	c := GHClient{GH: silent, GroupFile: filepath.Join(fixtureExecDir(t), "group")}
	out, err := c.run(context.Background(), "api", "graphql")
	if err == nil {
		t.Fatalf("a wrapper that exited before answering was read as a successful call (out %q): an enqueue would be reported as landed on a reply nobody received", string(out))
	}
	if !errors.Is(err, ErrIncompleteReply) {
		t.Fatalf("error = %v, want ErrIncompleteReply", err)
	}

	// And the case the drain bound exists for still succeeds: a complete reply,
	// with a descendant merely holding the pipe open.
	c = GHClient{GH: answered, GroupFile: filepath.Join(fixtureExecDir(t), "group")}
	out, err = c.run(context.Background(), "api", "graphql")
	if err != nil {
		t.Fatalf("a complete reply with a descendant holding the pipe was refused: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("the complete reply was discarded")
	}
}

// TestGraphqlRefusalWantsRejectionEvidenceNotAMessage pins what makes a reply a
// definitive refusal.
//
// A refusal is GitHub saying no to the REQUEST, and it says that in the entry's
// own shape: a `type` (UNPROCESSABLE, FORBIDDEN, NOT_FOUND) and no `path`.
//
// What is not a refusal is a field-resolution error -- an entry carrying a
// `path`, which means GitHub executed the mutation and then failed while
// building the reply. The write may already have landed, so classifying that as
// the answer skips the read that would find the PR queued and reports an enqueue
// GitHub performed as failed. Nor is an entry with no `type` and no `path`: it
// is an execution failure that says nothing structured about the request.
//
// Before this rule any nonempty message was the answer, so both of those were
// read as refusals.
func TestGraphqlRefusalWantsRejectionEvidenceNotAMessage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		body      string
		want      bool
		rationale string
	}{
		{
			name: "a request-level rejection is a refusal",
			body: `{"data":{"enqueuePullRequest":null},"errors":[{"type":"UNPROCESSABLE","message":"protected branch"}]}`,
			want: true,
		},
		{
			name: "another request-level type is a refusal too",
			body: `{"data":{"enqueuePullRequest":null},"errors":[{"type":"FORBIDDEN","message":"not authorized to jump the queue"}]}`,
			want: true,
		},
		{
			name: "a field-resolution error is not",
			body: `{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"position":3}}},"errors":[{"type":"INTERNAL","path":["enqueuePullRequest"],"message":"something went wrong"}]}`,
			want: false,
		},
		{
			name: "an untyped execution failure is not",
			body: `{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"position":3}}},"errors":[{"message":"internal error"}]}`,
			want: false,
		},
		{
			name: "a whole body with no errors is not",
			body: `{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"position":3}}}}`,
			want: false,
		},
		{
			name: "an empty errors array is not",
			body: `{"data":{"enqueuePullRequest":null},"errors":[]}`,
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg, got := graphqlRefusal([]byte(tc.body))
			if got != tc.want {
				t.Fatalf("graphqlRefusal(%s) = %q, %v, want a refusal of %v: reading this the other way either skips the read-back for a write that may have landed or spends one on a request GitHub declined", tc.body, msg, got, tc.want)
			}
		})
	}
}

// TestGHGatewayBodyIsNotAnAnswerToTheMutation pins that a whole JSON body on a
// non-zero exit is not, by itself, GitHub's answer.
//
// The three endings are refused, succeeded and unknown, and only GitHub's reply
// separates them. A reply is not the same thing as a body: `gh` prints whatever
// it received on stdout, and a gateway in front of the API answers a failed
// request with its own JSON diagnostic -- `{"message":"Server Error","status":
// "502"}` -- which is valid JSON with no `errors` array, so it is neither cut
// off nor a refusal.
//
// The client used to read a decodable body as "the call ended without a result"
// and report a plain failure. That is the misreport this PR is named for: the
// exit status is not an observation about the queue, the 502 establishes nothing
// about whether GitHub applied the mutation, and reporting a failure skipped the
// read that would have found the PR queued.
//
// Driven through Execute with a real gh on PATH, because the classification is
// only worth anything if the run acts on it: the enqueue's gateway error has to
// reach the read-back and let it decide.
func TestGHGatewayBodyIsNotAnAnswerToTheMutation(t *testing.T) {
	t.Parallel()
	dir := fixtureExecDir(t)
	// A token this test chose, so the detail check below pins that gh's stderr
	// survives the classification rather than that production wording does. The
	// literal cannot appear in production copy, which keeps it outside the
	// error-text rule's subject entirely.
	const detail = "fixture-gateway-detail-8c41"
	// One gh, three different calls: the two status reads around the mutation
	// answer properly, and the enqueue answers with a gateway body and exits 1.
	// The reply is chosen by the arguments, which is what makes the mutation the
	// call that fails.
	gh := filepath.Join(dir, "gh")
	writeExecutable(t, gh, fmt.Sprintf(`#!/usr/bin/env bash
for arg in "$@"; do
	if [[ "$arg" == query=*enqueuePullRequest* ]]; then
		printf '%%s' '{"message":"%[1]s","status":"502"}'
		printf 'gh: %[1]s\n' >&2
		exit 1
	fi
done
reads=$(dirname "$0")/status-reads
count=$(cat "$reads" 2>/dev/null | wc -l)
printf 'x\n' >>"$reads"
if [ "$count" -eq 0 ]; then
	printf '%%s' '{"data":{"repository":{"pullRequest":{"id":"PR_6499","state":"OPEN","baseRefName":"main","mergeQueueEntry":null}}}}'
else
	printf '%%s' '{"data":{"repository":{"pullRequest":{"id":"PR_6499","state":"OPEN","baseRefName":"main","mergeQueueEntry":{"position":3,"state":"QUEUED"}}}}}'
fi
`, detail))
	c := GHClient{GH: gh, GroupFile: filepath.Join(dir, "group")}

	// The client's own classification, before the run acts on it.
	_, enqueueErr := c.Enqueue(context.Background(), "prime-radiant-inc/evener", "PR_6499", false)
	if enqueueErr == nil {
		t.Fatal("an enqueue that exited 1 was reported as success")
	}
	if _, isRefusal := errors.AsType[*RefusalError](enqueueErr); isRefusal {
		t.Fatalf("error = %v, reported as a refusal: a gateway's 502 body is not GitHub declining the mutation", enqueueErr)
	}
	if !errors.Is(enqueueErr, ErrOutcomeUnknown) {
		t.Fatalf("error = %v, want ErrOutcomeUnknown: a whole JSON body that names no error is no answer at all, and calling it a failure is what reported an accepted enqueue as refused", enqueueErr)
	}
	// The diagnostic is carried through, checked against an opaque token this
	// test chose rather than against production copy: what is being pinned is
	// that the reader is handed whatever gh said, not that any particular
	// wording survived.
	if !strings.Contains(enqueueErr.Error(), detail) {
		t.Fatalf("error = %v, want the detail this fixture emitted (%s) carried: the operator needs to read what answered, and dropping it leaves a 502 with no account of itself", enqueueErr, detail)
	}

	// And the run reconciles it rather than reporting a refusal. --wait 0 keeps
	// this to the confirmation read: the mutation is unknown, the confirmation
	// finds the entry, and the outcome is the position.
	if err := os.WriteFile(filepath.Join(dir, "status-reads"), nil, 0o600); err != nil {
		t.Fatalf("reset the status-read counter: %v", err)
	}
	out, exitErr := Execute(context.Background(), opts(0), c, SystemClock)
	if exitErr != nil {
		t.Fatalf("Execute: %v, want the queue's entry reported: the gateway body left the outcome unknown, so the read decides and it found the PR queued", exitErr)
	}
	if out.Kind != Queued || out.Position != 3 {
		t.Fatalf("outcome = %+v, want Queued at position 3", out)
	}
}

func TestGHPublishRefusesAPlantedSymlink(t *testing.T) {
	t.Parallel()
	dir := fixtureExecDir(t)
	victim := filepath.Join(dir, "victim")
	const original = "keep these bytes\n"
	if err := os.WriteFile(victim, []byte(original), 0o600); err != nil {
		t.Fatalf("write the victim file: %v", err)
	}
	handle := filepath.Join(dir, "gh-group")
	// The planted links: the handle, and the staging name beside it.
	for _, name := range []string{handle, handle + ".tmp"} {
		if err := os.Symlink(victim, name); err != nil {
			t.Fatalf("plant the symlink at %s: %v", filepath.Base(name), err)
		}
	}
	publishGHGroup(handle, 4242)
	if body, err := os.ReadFile(victim); err != nil {
		t.Fatalf("read the victim file: %v", err)
	} else if string(body) != original {
		t.Fatalf("the victim file was truncated to %q: the publisher followed a planted symlink", body)
	}
	// And the handle is a real file holding the group, not the link any more.
	info, err := os.Lstat(handle)
	if err != nil {
		t.Fatalf("stat the handle: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the handle is still a symlink: the publisher did not replace it")
	}
	if body, err := os.ReadFile(handle); err != nil {
		t.Fatalf("read the handle: %v", err)
	} else if got := strings.TrimSpace(string(body)); got != "4242" {
		t.Fatalf("the handle holds %q, want the published group", got)
	}
}

// TestGHPublishesItsLiveProcessGroup pins the handle the launcher needs to stop a
// call whose wrapper exited. A `gh` that is a wrapper starting the real CLI
// without `exec` then exits reparents that CLI out of the helper's tree, so the
// launcher cannot find it by walking descendants: the group id is the only thing
// that still names it. The client publishes that id while the call is in flight
// and removes it when the call ends, so the launcher never reads a stale one.
//
// The fake `gh` blocks until the release file appears, so the group the test
// reads belongs to a live process rather than to one whose group id may be
// reused. Its context is cancellable and its cleanup is registered before the
// first assertion: a regression in publishing would otherwise fail the readiness
// wait with the fake still polling, leaving a live subprocess behind a test
// binary that has already exited -- a leak that hides the next regression.
func TestGHPublishesItsLiveProcessGroup(t *testing.T) {
	t.Parallel()
	dir := fixtureExecDir(t)
	groupFile := filepath.Join(dir, "gh-group")
	release := filepath.Join(dir, "release")
	gh := filepath.Join(dir, "gh")
	writeExecutable(t, gh, `#!/usr/bin/env bash
while [ ! -e `+shellquote.Literal(release)+` ]; do sleep 0.01; done
printf '{"data":{}}'
`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		_, err := (GHClient{GH: gh, GroupFile: groupFile}).run(ctx, "api", "graphql")
		done <- err
		close(finished)
	}()
	// Registered before the readiness wait and before any assertion, so every
	// failure path below ends the fake instead of leaving it polling. The wait is
	// bounded: a gh that ignored cancellation would otherwise hold this cleanup
	// until the binary's deadline, which reads as an unrelated hang.
	//
	// It waits on finished rather than on done, so it cannot consume the result
	// the success path still has to read.
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			t.Errorf("the fixture gh was still running after cancellation")
		}
	})
	var published int
	testwait.Until(t, "the client to publish the live gh group", func() bool {
		body, err := os.ReadFile(groupFile)
		if err != nil {
			return false
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(string(body)))
		if err != nil {
			return false
		}
		published = parsed
		return true
	})
	if published <= 1 {
		t.Fatalf("published group %d, want the gh call's own group", published)
	}
	// The published id has to be a group the call is actually in, or a launcher
	// acting on it would signal something else.
	out, err := exec.Command("ps", "-o", "pgid=", "-p", strconv.Itoa(published)).Output()
	if err != nil {
		t.Fatalf("ps for the published group %d: %v", published, err)
	}
	if got := strings.TrimSpace(string(out)); got != strconv.Itoa(published) {
		t.Fatalf("published %d, which is in group %s: the handle must name the call's own group", published, got)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release the fake gh: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run = %v, want the fake gh's answer", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return after the fake gh answered")
	}
	// And the handle is gone once the call is: a group id is reused, so a file
	// left behind would point a later forced stop at an unrelated process.
	if _, err := os.Stat(groupFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the handle survived the call: stat = %v", err)
	}
}

// The parsers below decode exactly the answers `gh api graphql` and `gh run
// list` give, keyed by GitHub's camelCase field names. These are the data
// seam: a fixture of GitHub's own JSON, not a subprocess standing in for gh.

// TestParseStatusReadsTheQueueEntryAndBase pins the fields the decisions act
// on: the PR id and state, its base branch, its merge commit and its queue
// entry, all out of one query.
func TestParseStatusReadsTheQueueEntryAndBase(t *testing.T) {
	t.Parallel()
	st, err := parseStatus([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_6499","state":"OPEN","baseRefName":"main","mergeCommit":null,"mergeQueueEntry":{"position":3,"state":"QUEUED"}}}}}`))
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	if st.ID != "PR_6499" || st.State != "OPEN" || st.Base != "main" {
		t.Fatalf("status = %+v, want id PR_6499, OPEN, base main", st)
	}
	if st.Entry == nil || st.Entry.Position != 3 || st.Entry.State != "QUEUED" {
		t.Fatalf("entry = %+v, want position 3 in QUEUED", st.Entry)
	}
	if st.MergeCommit != nil {
		t.Fatalf("mergeCommit = %v, want nil for a null answer", *st.MergeCommit)
	}
}

// TestParseStatusReadsAMergeCommitAndAbsentEntry pins the other half of the
// optional fields: a MERGED PR carries a merge commit, and a PR that is not
// queued has no entry at all. A null mergeCommit is not an error.
func TestParseStatusReadsAMergeCommitAndAbsentEntry(t *testing.T) {
	t.Parallel()
	st, err := parseStatus([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_1","state":"MERGED","baseRefName":"main","mergeCommit":{"oid":"deadbeef"},"mergeQueueEntry":null}}}}`))
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	if st.Entry != nil {
		t.Fatalf("entry = %+v, want nil for a null answer", st.Entry)
	}
	if st.MergeSHA() != "deadbeef" {
		t.Fatalf("MergeSHA() = %q, want deadbeef", st.MergeSHA())
	}
}

// TestParseStatusRefusesAMissingPullRequest pins that an answer with no pull
// request is an error: reading it as an empty status would enqueue an empty id.
func TestParseStatusRefusesAMissingPullRequest(t *testing.T) {
	t.Parallel()
	if _, err := parseStatus([]byte(`{"data":{"repository":{"pullRequest":null}}}`)); err == nil {
		t.Fatal("a null pullRequest must be an error")
	}
}

// TestParseMergedCommitReadsThePRsOwnState pins #6480's read: the PR's own
// state, not the queue snapshot, is what says MERGED, and a non-MERGED state
// is not a merge.
func TestParseMergedCommitReadsThePRsOwnState(t *testing.T) {
	t.Parallel()
	sha, merged, err := parseMergedCommit([]byte(`{"state":"MERGED","mergeCommit":{"oid":"cafe"}}`))
	if err != nil || !merged || sha != "cafe" {
		t.Fatalf("parseMergedCommit = %q, %v, %v; want cafe, true, nil", sha, merged, err)
	}
	sha, merged, err = parseMergedCommit([]byte(`{"state":"OPEN","mergeCommit":null}`))
	if err != nil {
		t.Fatalf("parseMergedCommit: %v", err)
	}
	if merged || sha != "" {
		t.Fatalf("parseMergedCommit = %q, %v; a non-MERGED state is not a merge", sha, merged)
	}
}

// TestParseDropEventReadsTheQueuesOwnReason pins the field the drop class is
// decided from, and the one the staleness check needs.
func TestParseDropEventReadsTheQueuesOwnReason(t *testing.T) {
	t.Parallel()
	event, err := parseDropEvent([]byte(`{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[{"createdAt":"2026-11-17T12:00:00Z","reason":"failed_checks"}]}}}}}`))
	if err != nil {
		t.Fatalf("parseDropEvent: %v", err)
	}
	if event == nil {
		t.Fatal("parseDropEvent returned no event for a timeline with one")
	}
	if event.Reason != "failed_checks" {
		t.Fatalf("Reason = %q, want failed_checks", event.Reason)
	}
	if want := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC); !event.CreatedAt.Equal(want) {
		t.Fatalf("CreatedAt = %v, want %v", event.CreatedAt, want)
	}
}

// TestParseDropEventReadsAnEmptyTimelineAsNone pins the no-event answer: nil,
// not an error and not an empty event with a zero timestamp that the staleness
// check would treat as ancient.
// TestEnqueueIncompleteReplyIsReconciled pins the other half of the drain rule.
// Refusing outright when the reply is lost reports an enqueue that may already
// have landed, so the run reads the state back and lets it decide. Both
// directions matter: a reply lost after the mutation landed must report the
// queue position, and one lost before it landed must still be a refusal.
func TestEnqueueIncompleteReplyIsReconciled(t *testing.T) {
	t.Parallel()
	// The reply is lost, but the PR is in the queue when the run looks again.
	landed := &fakeClient{
		enqueueErr: ErrIncompleteReply,
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN"},
			{ID: "PR_6499", State: "OPEN", Entry: queueEntry(2)},
		},
	}
	out, exitErr := Execute(context.Background(), opts(0), landed, SystemClock)
	if exitErr != nil {
		t.Fatalf("Execute: %v, want the queue to report where the PR landed", exitErr)
	}
	if out.Kind != Queued || out.Position != 2 {
		t.Fatalf("outcome = %+v, want Queued at position 2", out)
	}

	// The reply is lost and the PR is still absent, so the mutation did not land.
	absent := &fakeClient{
		enqueueErr: ErrIncompleteReply,
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN"},
			{ID: "PR_6499", State: "OPEN"},
		},
	}
	if _, exitErr := Execute(context.Background(), opts(0), absent, SystemClock); exitErr == nil {
		t.Fatal("an enqueue that could not be confirmed was reported as success")
	} else if exitErr.Kind != EnqueueFailed {
		t.Fatalf("kind = %v, want EnqueueFailed", exitErr.Kind)
	}
}

func TestParseDropEventReadsAnEmptyTimelineAsNone(t *testing.T) {
	t.Parallel()
	event, err := parseDropEvent([]byte(`{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[]}}}}}`))
	if err != nil {
		t.Fatalf("parseDropEvent: %v", err)
	}
	if event != nil {
		t.Fatalf("event = %+v, want nil for an empty timeline", event)
	}
}

// TestParseDropEventReadsANullNodeAsNone pins the literal null the API sends
// when the selected timeline item is not a removal event. Decoded into a struct
// rather than a pointer it arrives as a zero value, and the empty timestamp then
// reads as a malformed removal event -- an absent event reported as an
// unreadable one, which is the difference between "nothing to explain" and "the
// reason could not be read".
func TestParseDropEventReadsANullNodeAsNone(t *testing.T) {
	t.Parallel()
	event, err := parseDropEvent([]byte(`{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[null]}}}}}`))
	if err != nil {
		t.Fatalf("parseDropEvent: %v", err)
	}
	if event != nil {
		t.Fatalf("event = %+v, want nil for a null node", event)
	}
}

// TestParseRunsReadsGhsCamelCaseFields pins that `gh run list`'s own JSON keys
// decode: databaseId, url, conclusion and headBranch are what the prefix match
// and the run line read.
func TestParseRunsReadsGhsCamelCaseFields(t *testing.T) {
	t.Parallel()
	runs, err := parseRuns([]byte(`[{"databaseId":123456789,"url":"https://example.invalid/run","conclusion":"failure","headBranch":"gh-readonly-queue/main/pr-6499-abc"}]`))
	if err != nil {
		t.Fatalf("parseRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %v, want one", runs)
	}
	got := runs[0]
	if got.DatabaseID != 123456789 || got.URL != "https://example.invalid/run" ||
		got.Conclusion != "failure" || got.HeadBranch != "gh-readonly-queue/main/pr-6499-abc" {
		t.Fatalf("run = %+v, want the fixture's four fields", got)
	}
}

// TestSplitRepoRefusesANonOwnerNameRepo pins the argument check the two
// owner/name queries need: splitting "evener" would send an empty name and ask
// GitHub about a repository that does not exist.
func TestSplitRepoRefusesANonOwnerNameRepo(t *testing.T) {
	t.Parallel()
	for _, repo := range []string{"evener", "/evener", "evener/", ""} {
		if _, _, err := splitRepo(repo); err == nil {
			t.Fatalf("splitRepo(%q) succeeded; want a refusal", repo)
		}
	}
	owner, name, err := splitRepo("prime-radiant-inc/evener")
	if err != nil || owner != "prime-radiant-inc" || name != "evener" {
		t.Fatalf("splitRepo = %q, %q, %v; want prime-radiant-inc, evener, nil", owner, name, err)
	}
}

// TestRunRefRendersTheResolvedRunOrThePrefix pins the two renderings a drop
// reason can carry, and that an unresolved lookup names the prefix rather than
// a run it did not find.
func TestRunRefRendersTheResolvedRunOrThePrefix(t *testing.T) {
	t.Parallel()
	resolved := RunRef{Group: &GroupRun{DatabaseID: 42, URL: "https://example.invalid/42", Conclusion: "failure"}}
	if got := resolved.String(); !strings.Contains(got, "42") || !strings.Contains(got, "https://example.invalid/42") {
		t.Fatalf("resolved rendering = %q, want the run id and URL", got)
	}
	if got := (RunRef{Prefix: "gh-readonly-queue/main/pr-1-"}).String(); !strings.Contains(got, "gh-readonly-queue/main/pr-1-") {
		t.Fatalf("prefix rendering = %q, want the prefix", got)
	}
	// A reference with neither a run nor a prefix still renders something an
	// operator can read; it is asserted as non-empty rather than compared with
	// the production phrase, which would only re-read the source. The no-base
	// case's real claim -- no run and no prefix -- is pinned by
	// TestDropWithoutABaseSkipsTheRunLookup.
	if got := (RunRef{}).String(); got == "" {
		t.Fatal("empty rendering = \"\", want a readable fallback")
	}
}

// TestMergEQFixtureBaseIsTrustedAndExecutable pins the two properties the
// executable fixtures need from their base, and pins them where they are
// decided rather than through a case that would time out without them.
//
// The base these fixtures run from is not only storage: a script written there
// is executed, so a base another local user can replace is a way to run their
// program with this user's privileges. Checking the leaf's mode does not answer
// that -- the leaf is private and the ancestor is the way in. The same base also
// has to be able to execute a program, or every case under it waits out a
// deadline for a fixture that never ran.
//
// Both are asked directly, because a case that only fails by timing out says
// "something is wrong" and not which of the two it was.
func TestMergEQFixtureBaseIsTrustedAndExecutable(t *testing.T) {
	// Not parallel itself: the last subtest sets TMPDIR for the process.
	t.Run("a replaceable ancestor is refused", func(t *testing.T) {
		t.Parallel()
		// The directory has to be replaceable for the case to mean anything, and
		// it has to be created somewhere this test owns before it is made so. It
		// is therefore a child of the trusted fixture base, not of t.TempDir():
		// t.TempDir() descends from the ambient TMPDIR, and on a TMPDIR whose
		// ancestor another user can replace, the entry this call creates can be
		// swapped between the mkdir and the chmod. The chmod would then land on
		// that user's target, which is an operator-owned directory made writable
		// by a test run -- the hazard this case exists to describe, performed by
		// the case itself.
		dir := widenedFixtureDir(t, "replaceable")
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatalf("make %s replaceable: %v", dir, err)
		}
		// A 0777 directory is only replaceable if no sticky bit protects it. If the
		// runner's umask or mount options left one on, this case cannot state its
		// premise and says so instead of passing for the wrong reason.
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if info.Mode()&os.ModeSticky != 0 {
			t.Skipf("%s came back sticky, so it is not the replaceable base this case needs", dir)
		}
		if reason := replaceableAncestor(t, dir); reason == "" {
			t.Fatalf("%s is writable by other users with no sticky bit, and the ancestor rule accepted it: another user can replace entries in it, so a script this test executes from there may not be the script it wrote", dir)
		}
		// And the refusal reaches the function the fixtures actually call, not only
		// the predicate: handing it this base must not return a directory.
		if got, reason := execDirUnder(t, dir); reason == "" {
			t.Fatalf("execDirUnder(%s) returned %s under a replaceable ancestor: the fixture would execute a program from a path another user can swap", dir, got)
		}
	})
	t.Run("a writable ACL mask over a read-only group is accepted", func(t *testing.T) {
		t.Parallel()
		// The opposite error, and the one that broke fixture setup rather than
		// weakening a check: on Linux the mode's group position is the ACL MASK
		// when an access ACL is present, so a directory whose mask is rwx while
		// `group::` is r-x and no named entry holds write is NOT group-writable.
		// A rule that reads the mode alone refuses it.
		//
		// On a runner where such a directory is the only executable candidate -- a
		// noexec /tmp, for instance -- every fixture then fails while setting itself
		// up, on a host where nothing can actually tamper with it. That is why the
		// ACL's `group::` line decides when one is present.
		dir := widenedFixtureDir(t, "acl-mask-readonly-group")
		if _, err := exec.LookPath("setfacl"); err != nil {
			t.Skipf("setfacl is not installed, so a POSIX access ACL cannot be manufactured here: %v", err)
		}
		if _, err := exec.LookPath("getfacl"); err != nil {
			t.Skipf("getfacl is not installed, so the ACL could not be read either and this premise has no meaning: %v", err)
		}
		if err := exec.Command("setfacl", "-m", "g::r-x", "-m", "m::rwx", dir).Run(); err != nil {
			t.Skipf("could not set the ACL on %s (%v): the filesystem may not support POSIX ACLs", dir, err)
		}
		t.Cleanup(func() { _ = exec.Command("setfacl", "-b", dir).Run() })
		// The premise, checked rather than assumed: the mask carries write, the
		// owning group does not, and no named entry does.
		acl, err := exec.Command("getfacl", "-p", "-c", dir).Output()
		if err != nil {
			t.Fatalf("read the ACL of %s: %v", dir, err)
		}
		text := string(acl)
		if !strings.Contains(text, "mask::rwx") || !strings.Contains(text, "group::r-x") {
			t.Skipf("the ACL came back as %q, which is not the writable-mask/read-only-group shape this case needs", strings.TrimSpace(text))
		}
		if reason := replaceableAncestor(t, dir); reason != "" {
			t.Fatalf("the fixture selector refused %s, whose ACL grants the owning group no write: the mode bits are the ACL mask here, not the owning group's permission, so a selector that decides on them fails on a host where the directory is safe (%s)", dir, reason)
		}
		if got, reason := execDirUnder(t, dir); reason != "" {
			t.Fatalf("execDirUnder(%s) refused a directory whose ACL grants no foreign write (%s): on a runner where this is the only executable candidate, every fixture would fail during setup", dir, reason)
		} else if !strings.HasPrefix(got, dir+string(os.PathSeparator)) {
			t.Fatalf("execDirUnder(%s) returned %s, want a directory under it", dir, got)
		}
	})
	t.Run("a group-writable ancestor is refused even for a member", func(t *testing.T) {
		t.Parallel()
		// 0770 is writable by the owning group but not by others, so an
		// other-writable check does not see it. Being in the group is not the
		// question: another member is another user, and can rename or delete the
		// fixture's entry and put their own script where the test will execute it.
		// Under the trusted fixture base for the same reason as the case above:
		// the directory is deliberately widened, so the entry that is widened has
		// to be an entry this test already owns.
		dir := widenedFixtureDir(t, "group-writable")
		if err := os.Chmod(dir, 0o770); err != nil {
			t.Fatalf("make %s group-writable: %v", dir, err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if info.Mode()&os.ModeSticky != 0 {
			t.Skipf("%s came back sticky, so it is not the group-writable base this case needs", dir)
		}
		if info.Mode().Perm()&0o002 != 0 {
			t.Skipf("%s came back other-writable, so it does not isolate the group bit", dir)
		}
		// The premise: this process IS in the owning group, which is what made the
		// old predicate accept it.
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Skip("cannot read the owning group on this platform")
		}
		inGroup := int(stat.Gid) == os.Getgid()
		if !inGroup {
			if gids, err := os.Getgroups(); err == nil {
				inGroup = slices.Contains(gids, int(stat.Gid))
			}
		}
		if !inGroup {
			t.Skipf("%s is owned by group %d, which this process is not in, so the pre-fix acceptance cannot be reproduced here", dir, stat.Gid)
		}
		if reason := replaceableAncestor(t, dir); reason == "" {
			t.Fatalf("%s is writable by group %d and this test was expected to refuse it: another member of that group can replace entries in it", dir, stat.Gid)
		}
		if got, reason := execDirUnder(t, dir); reason == "" {
			t.Fatalf("execDirUnder(%s) returned %s under a group-writable ancestor: the fixture would execute a program from a path another member of the group can swap", dir, got)
		}
	})
	t.Run("a named grant on a private directory is refused", func(t *testing.T) {
		t.Parallel()
		// The mode says 0700, so a rule that reads the mode alone calls this
		// directory private. The ACL says otherwise: one named user holds rwx, and
		// the mask carries write so the grant is effective. That user can replace
		// entries in this directory, and the fixture executes scripts through it --
		// the mode's other and group bits never see a named entry at all.
		if _, err := exec.LookPath("setfacl"); err != nil {
			t.Skipf("setfacl is not installed, so a POSIX access ACL cannot be manufactured here: %v", err)
		}
		if _, err := exec.LookPath("getfacl"); err != nil {
			t.Skipf("getfacl is not installed, so the ACL could not be read either: %v", err)
		}
		dir := widenedFixtureDir(t, "named-grant")
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatalf("make %s private: %v", dir, err)
		}
		// nobody, so the grantee is a real uid that is neither this user nor root.
		if err := exec.Command("setfacl", "-m", "u:65534:rwx", dir).Run(); err != nil {
			t.Skipf("could not grant a named user rwx on %s (%v): the filesystem may not support POSIX ACLs", dir, err)
		}
		t.Cleanup(func() { _ = exec.Command("setfacl", "-b", dir).Run() })
		// The premise, checked rather than assumed: no base entry other than the
		// named user grants write, and the mask lets the named grant through.
		//
		// The `other` field is read from the mode, which is the only place it lives.
		// The owning group's own permission is NOT: `setfacl` writes the mask into
		// the mode's group position, so the mode here reads 0770 because `mask::rwx`,
		// not because `group::` can write. That is the same interaction the
		// ACL-mask case above is about, and reading the mode for the group answer
		// here would skip this case on the very hosts it applies to.
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if perm := info.Mode().Perm(); perm&0o007 != 0 {
			t.Skipf("%s came back %04o, so another user may write through its mode and this case would not isolate the named grant", dir, perm)
		}
		acl, err := exec.Command("getfacl", "-n", "-p", "-c", dir).Output()
		if err != nil {
			t.Fatalf("read the ACL of %s: %v", dir, err)
		}
		text := string(acl)
		if !strings.Contains(text, "user:65534:rwx") ||
			!strings.Contains(text, "mask::rwx") ||
			!strings.Contains(text, "group::---") {
			t.Skipf("the ACL came back as %q, which is not the effective named grant this case needs", strings.TrimSpace(text))
		}
		if reason := replaceableAncestor(t, dir); reason == "" {
			t.Fatalf("%s is 0700 but its ACL grants uid 65534 rwx through a writable mask, and the fixture rule accepted it: that user can replace an entry, and the suite executes scripts from this path", dir)
		}
		if got, reason := execDirUnder(t, dir); reason == "" {
			t.Fatalf("execDirUnder(%s) returned %s under a named rwx grant: the fixture would execute a program from a path another local user can swap", dir, got)
		}
	})
	t.Run("a world-writable directory is refused even behind a restrictive ACL", func(t *testing.T) {
		t.Parallel()
		// The other direction, and the one that skips a check rather than missing a
		// grant: a directory whose owning group and mask are read-only still has
		// `other::rwx`, so any local user can replace entries in it. Reading the ACL
		// first and returning on the group question never asks about `other` at all.
		//
		// No ACL can widen `other`; it is the mode's own field, which is why the
		// world check is independent of the ACL group question.
		if _, err := exec.LookPath("setfacl"); err != nil {
			t.Skipf("setfacl is not installed, so a POSIX access ACL cannot be manufactured here: %v", err)
		}
		dir := widenedFixtureDir(t, "world-writable-restrictive-acl")
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatalf("make %s world-writable: %v", dir, err)
		}
		if err := exec.Command("setfacl", "-m", "g::r-x", "-m", "m::r-x", dir).Run(); err != nil {
			t.Skipf("could not set the ACL on %s (%v): the filesystem may not support POSIX ACLs", dir, err)
		}
		t.Cleanup(func() { _ = exec.Command("setfacl", "-b", dir).Run() })
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if info.Mode()&os.ModeSticky != 0 {
			t.Skipf("%s came back sticky, so it is not the replaceable base this case needs", dir)
		}
		if perm := info.Mode().Perm(); perm&0o002 == 0 {
			t.Skipf("%s came back %04o, so it is not world-writable and this case has no premise", dir, perm)
		}
		if reason := replaceableAncestor(t, dir); reason == "" {
			t.Fatalf("%s is world-writable with no sticky bit, and the fixture rule accepted it because the ACL's group question came back clean: another user can replace entries in it", dir)
		}
	})
	t.Run("a component that changed to a symlink is refused", func(t *testing.T) {
		t.Parallel()
		// The race the check is for, in miniature. The fixture resolves its base
		// with EvalSymlinks and then walks the components. A component under a
		// sticky directory belonging to another user can be renamed away and
		// replaced with a symlink in that window: the walk would then describe the
		// TARGET (this user's own 0700 directory, say) while the name along the
		// path is one that user can retarget again at will.
		//
		// Stat follows the link and reports the target, so it accepts the swap. The
		// step that changes and then the check that reads it are separated here by
		// construction rather than by timing: the link is planted before the walk
		// runs, which is exactly the state the race produces.
		base := fixtureExecDir(t)
		real := filepath.Join(base, "resolved-target")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatalf("make the trusted target: %v", err)
		}
		// The parent the fixture walks through: this user owns it, so the only
		// reason to refuse is the link below.
		parent := filepath.Join(base, "swapped-parent")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatalf("make the walked parent: %v", err)
		}
		link := filepath.Join(parent, "component")
		if err := os.Symlink(real, link); err != nil {
			t.Fatalf("plant the swapped component: %v", err)
		}
		// The premise: Stat, and therefore the pre-fix check, sees the target and
		// would have called this component a trusted 0700 directory.
		if info, err := os.Stat(link); err != nil {
			t.Fatalf("stat the link: %v", err)
		} else if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("the target reads as %v, so this case does not isolate the symlink: the whole point is that following the link looks safe", info.Mode())
		}
		if reason := replaceableAncestor(t, link); reason == "" {
			t.Fatalf("%s is a symlink that appeared after the path was resolved, and the fixture rule accepted it by following it to a private 0700 directory: another user can retarget that name between the resolve and the check, so the fixture would write and execute scripts through a path they control", link)
		}
		// And the same path with the link replaced by the real directory is
		// accepted, so the rule refuses the symlink rather than the location.
		if err := os.Remove(link); err != nil {
			t.Fatalf("remove the link: %v", err)
		}
		if err := os.Mkdir(link, 0o700); err != nil {
			t.Fatalf("make the component a real directory: %v", err)
		}
		if reason := replaceableAncestor(t, link); reason != "" {
			t.Fatalf("a real 0700 directory under a trusted parent was refused (%s): the rule must refuse the swap, not the path", reason)
		}
	})
	t.Run("an exec-capable base is accepted", func(t *testing.T) {
		t.Parallel()
		base := fixtureExecDir(t)
		if reason := replaceableAncestor(t, base); reason != "" {
			t.Fatalf("the base the fixtures use is replaceable: %s", reason)
		}
		if err := exec.Command(filepath.Join(base, "exec-probe.sh")).Run(); err != nil {
			t.Fatalf("%s cannot execute a program (%v), and the fixtures there launch scripts", base, err)
		}
	})
	t.Run("a replaceable TMPDIR falls back rather than failing the case", func(t *testing.T) {
		// Not parallel: it changes the process environment the candidate list
		// reads, and t.Setenv panics in a parallel test.
		//
		// The fallback is established BEFORE TMPDIR is invalidated, and it is the
		// runtime directory rather than only a validated base. Overriding TMPDIR
		// otherwise leaves the case depending on whatever XDG_RUNTIME_DIR and HOME
		// the runner supplies, and the module gate supplies neither a runtime dir
		// nor a HOME with a .cache -- its cache lives in XDG_CACHE_HOME -- so
		// fixtureExecDir would fail before reaching the assertion. That is the
		// vacuous shape: a test that cannot reach its own check proves nothing.
		// Owning the runtime dir puts a usable candidate immediately after the
		// replaceable TMPDIR, so the assertion below is about the fallback and not
		// about which roots this host happens to have.
		fallback, reason := execDirUnder(t, fixtureExecDir(t))
		if reason != "" {
			t.Fatalf("could not establish the fallback this case needs: %s", reason)
		}
		t.Setenv("XDG_RUNTIME_DIR", fallback)
		// Under the trusted fixture base, not t.TempDir(): this directory is
		// widened to 0777, so the entry that gets widened must be one this test
		// already owns. Under an ambient TMPDIR another user could replace the
		// entry between the mkdir and the chmod and the chmod would land on a
		// directory of theirs.
		dir := widenedFixtureDir(t, "replaceable-tmp")
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatalf("make %s replaceable: %v", dir, err)
		}
		if info, err := os.Stat(dir); err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		} else if info.Mode()&os.ModeSticky != 0 {
			t.Skipf("%s came back sticky, so it is not the replaceable base this case needs", dir)
		}
		t.Setenv("TMPDIR", dir)
		got := fixtureExecDir(t)
		if strings.HasPrefix(got, dir+string(os.PathSeparator)) {
			t.Fatalf("fixtureExecDir returned %s under a replaceable TMPDIR: the fixtures would execute a program from a path another user can swap", got)
		}
		if !strings.HasPrefix(got, fallback+string(os.PathSeparator)) {
			t.Fatalf("fixtureExecDir returned %s, want a directory under the fixture-owned runtime dir %s: that candidate is named before every other and is valid, so a pass over it means the candidate order or its validation is wrong", got, fallback)
		}
		if reason := replaceableAncestor(t, got); reason != "" {
			t.Fatalf("fixtureExecDir fell back to %s, which the fixture's own chain rule does not trust (%s): passing over the replaceable TMPDIR is only a fix if what replaces it is trusted", got, reason)
		}
	})
	t.Run("a widened fixture is created where this user owns the entry", func(t *testing.T) {
		// The high: a case that widens a directory to 0777 must widen an entry it
		// already owns. `t.TempDir()` descends from the ambient TMPDIR, so on a
		// TMPDIR whose ancestor another local user can replace, that user can swap
		// the entry between the mkdir and the chmod -- and the chmod lands on their
		// target, which can be a directory the operator owns, made world-writable by
		// a test run. The case would be performing the hazard it describes.
		//
		// The premise is manufactured rather than assumed, because on a host whose
		// TMPDIR is already trusted (1777 root-owned /tmp is the ordinary case) the
		// pre-fix shape lands somewhere safe by accident and the case would pass
		// against it. TMPDIR is pointed at a directory this user owns with 0777, so
		// t.TempDir() below it is under a replaceable ancestor for real, and the
		// fixture's own base is not.
		//
		// Not parallel: it sets TMPDIR for the process, and t.Setenv panics in a
		// parallel test.
		replaceable := widenedFixtureDir(t, "replaceable-tmpdir-for-widening")
		if err := os.Chmod(replaceable, 0o777); err != nil {
			t.Fatalf("make %s replaceable: %v", replaceable, err)
		}
		if info, err := os.Stat(replaceable); err != nil {
			t.Fatalf("stat %s: %v", replaceable, err)
		} else if info.Mode()&os.ModeSticky != 0 {
			t.Skipf("%s came back sticky, so it is not the replaceable TMPDIR this case needs", replaceable)
		}
		if reason := replaceableAncestor(t, replaceable); reason == "" {
			t.Skipf("%s still reads as trusted on this host, so a directory beneath it is not replaceable and the case has no premise (%s is the base it descends from)", replaceable, fixtureExecDir(t))
		}
		t.Setenv("TMPDIR", replaceable)
		// What the fixture does when it widens a directory under a replaceable
		// TMPDIR. The pre-fix shape reached for t.TempDir() here, and the helper
		// asks the question the finding is about: is the entry this test is about
		// to widen one it already owns?
		dir := widenedFixtureDir(t, "widened-under-a-replaceable-tmpdir")
		// The assertion is the ancestry, not a path prefix: t.TempDir() gives a
		// numbered sibling under the test's own space rather than a child of the
		// TMPDIR the test just set, so "is it under the replaceable directory" is
		// the wrong question and answers no on both the fixed and the broken shape.
		// What the finding is about is whether another user can replace the entry
		// this call is about to widen, which is exactly what the chain is asked for.
		if reason := replaceableAncestor(t, dir); reason != "" {
			t.Fatalf("widenedFixtureDir returned %s, whose ancestry another user can replace, with TMPDIR set to %s: the entry this case is about to widen is not one it owns, so the chmod lands wherever that user pointed (%s)", dir, replaceable, reason)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatalf("widen %s: %v", dir, err)
		}
	})
}

// TestGHNonzeroExitKeepsTheEntryTheMutationReported pins M3: a reply that both
// performed the mutation and reported an error is still the record of the write.
//
// GitHub answers this shape when it applies the mutation and then fails to
// resolve a returned field: `{"data":{"enqueuePullRequest":{"mergeQueueEntry":
// {"position":7}}},"errors":[{"message":"...","path":["enqueuePullRequest"]}]}`,
// and gh prints it on stdout and exits non-zero. `run` used to discard stdout on
// any non-zero exit, so Enqueue returned a nil entry and Execute had no evidence
// the enqueue landed. If a concurrent push rebuilds the merge group before the
// confirmation read, the read finds no entry and the run reports an enqueue
// failure for a mutation GitHub confirmed -- and skips the drop diagnostics.
//
// Driven through Execute as well as the client, because the entry is only worth
// preserving if the run uses it: the client keeps the entry through the failed
// exit, and the run reports the drop instead of a failed enqueue.
func TestGHNonzeroExitKeepsTheEntryTheMutationReported(t *testing.T) {
	t.Parallel()
	dir := fixtureExecDir(t)
	gh := filepath.Join(dir, "gh")
	// The mutation performs the write and reports a field-resolution error in the
	// same reply, then exits non-zero. A field-resolution error carries a path and
	// no type, so graphqlRefusal does not call it a refusal -- the outcome stays
	// unknown and the entry is the data that resolves it.
	writeExecutable(t, gh, `#!/usr/bin/env bash
for arg in "$@"; do
	if [[ "$arg" == query=*enqueuePullRequest* ]]; then
		printf '%s' '{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"position":7,"state":"QUEUED"}}},"errors":[{"message":"field resolution failed","path":["enqueuePullRequest"]}]}'
		exit 1
	fi
done
for arg in "$@"; do
	if [[ "$arg" == "view" ]]; then
		printf '%s' '{"state":"OPEN","mergeCommit":null}'
		exit 0
	fi
done
# The confirmation reads find the PR open with no entry: a concurrent push
# rebuilt the merge group, which is exactly the case the saved entry is for.
printf '%s' '{"data":{"repository":{"pullRequest":{"id":"PR_6499","state":"OPEN","baseRefName":"main","mergeQueueEntry":null}}}}'
`)
	c := GHClient{GH: gh, GroupFile: filepath.Join(dir, "group")}

	// The client's own answer: the entry survives the nonzero exit.
	entry, err := c.Enqueue(context.Background(), "prime-radiant-inc/evener", "PR_6499", false)
	if err == nil {
		t.Fatal("an enqueue that exited non-zero was reported as success")
	}
	if _, isRefusal := errors.AsType[*RefusalError](err); isRefusal {
		t.Fatalf("error = %v, reported as a refusal: a field-resolution error says GitHub ran the mutation and failed to build the reply", err)
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("error = %v, want ErrOutcomeUnknown: nothing in a field-resolution error establishes whether the write landed", err)
	}
	if entry == nil {
		t.Fatal("the mutation's reply named a queue entry and it was discarded with the exit status")
	}
	if entry.Position != 7 || entry.State != "QUEUED" {
		t.Fatalf("entry = %+v, want position 7 QUEUED: that is the record of where GitHub put the pull request", entry)
	}

	// And the run acts on it: the enqueue's outcome is unknown, the read finds the
	// entry gone, and the reply is what makes that a drop rather than a failure.
	out, exitErr := Execute(context.Background(), opts(0), c, SystemClock)
	if exitErr != nil {
		t.Fatalf("Execute: %v, want the drop reported: the mutation's reply named an entry, so a read that finds it gone is the queue having removed it", exitErr)
	}
	if out.Kind != Dropped {
		t.Fatalf("outcome = %+v, want Dropped", out)
	}
}
