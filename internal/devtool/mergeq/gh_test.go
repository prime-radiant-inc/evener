//go:build !windows

package mergeq

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// script with this user's privileges. That is the same rule the launcher applies
// to its scratch roots, and it belongs on the fixture for the same reason -- the
// fixture executes a program out of this directory, so it has to know the path
// it executes through.
//
// Both are established before anything is created, written or run, and both are
// asked of the chain rather than of the leaf. A check that comes after the first
// mkdir, chmod, write or exec is too late to be a check.
//
// Candidate bases are tried in the launcher's own order -- the ambient temp dir,
// then the per-session runtime dir, then a directory under $HOME -- so a noexec
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

// fixtureBaseCandidates is the bases to try: the launcher's own order first, then
// the root every Unix has.
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

// launcherMergeEnqueue is the script whose validator decides what a scratch root
// is, read from the repository rather than restated here. The path is relative
// to this package's directory, which is where `go test` runs it from:
// internal/devtool/mergeq.
var launcherMergeEnqueue = filepath.Join("..", "..", "..", "scripts", "ops", "merge-enqueue.sh")

// launcherValidatorFuncs is the launcher's own trust rule, as sed expressions.
// One list, never a subset at a call site: a predicate that runs with a helper
// undefined silently skips the branch that needed it.
const launcherValidatorFuncs = "/^acl_getfacl_grants_foreign_write()/,/^}/p;" +
	"/^acl_getfacl_has_mask()/,/^}/p;" +
	"/^acl_getfacl_group_grants_write()/,/^}/p;" +
	"/^acl_ls_e_grants_foreign_write()/,/^}/p;" +
	"/^dir_acl_grants_foreign_write()/,/^}/p;" +
	"/^dir_entry_is_replaceable()/,/^}/p;" +
	"/^dir_is_owned_by_trusted()/,/^}/p;" +
	"/^scratch_path_is_trusted()/,/^}/p"

// launcherTrustsPath asks the launcher's own validator whether it would accept
// path, ancestors included, by sourcing the script's functions and running the
// predicate. It is the oracle for "would the launcher build here", so these
// fixtures never restate a rule they could ask about.
func launcherTrustsPath(t *testing.T, path string) bool {
	t.Helper()
	script := `eval "$(sed -n '` + launcherValidatorFuncs + `' "$1")"; scratch_path_is_trusted "$2"`
	return exec.Command("bash", "-c", script, "bash", launcherMergeEnqueue, path).Run() == nil
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
// The launcher's own validator is the authority. It checks the resolved chain
// from the root down, refuses group- and other-writable components unless a
// trusted sticky bit protects them, and on Darwin reads the extended ACL too.
// Asking it directly is the only way these fixtures and the launcher cannot
// disagree: reimplementing the rule in Go drifted the first time it was tried --
// the Go version accepted a group-writable ancestor whenever the test process
// belonged to the owning group, which the launcher rejects. Another member of
// that group is another user, and can replace the entry just the same.
//
// The Go walk stays in front of it and can only add refusals, never remove one:
// it names the specific component, which the bash predicate does not, and it is
// the only part that reports a component it cannot inspect at all.
func replaceableAncestor(t *testing.T, path string) string {
	t.Helper()
	if reason := replaceableAncestorByMode(path); reason != "" {
		return reason
	}
	if launcherTrustsPath(t, path) {
		return ""
	}
	return path + " is not a path the launcher would build under, and its own rule is the authority here: a component is replaceable, or an ACL grants write to another user"
}

// replaceableAncestorByMode is the part of the launcher's rule that needs no ACL
// reader, and it names the component that failed.
//
// Group-writable is refused whether or not this process is in the group. Another
// member of the group is another user: they can rename or delete the fixture's
// entry and put their own script where the test will execute it.
func replaceableAncestorByMode(path string) string {
	for _, component := range pathComponents(path) {
		info, err := os.Stat(component)
		if err != nil {
			return fmt.Sprintf("cannot inspect %s (%v)", component, err)
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
		// A sticky directory is settled: another user may add entries but cannot
		// rename or delete one this user created, which is what makes /tmp usable.
		if info.Mode()&os.ModeSticky != 0 {
			continue
		}
		perm := info.Mode().Perm()
		if perm&0o002 != 0 {
			return component + " is writable by other users and has no sticky bit, so another user can replace entries in it"
		}
		if perm&0o020 != 0 {
			return fmt.Sprintf("%s is writable by group %d with no sticky bit, so another member of that group can replace entries in it", component, stat.Gid)
		}
	}
	return ""
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
		// Not under t.TempDir(): the parent has to be replaceable for the case to
		// mean anything, and the point is to hand execDirUnder a base whose chain
		// is wrong. The directory is created inside this case's own temp space so
		// cleanup is the test's, then opened up to the mode the ancestor rule
		// exists for.
		dir := filepath.Join(t.TempDir(), "replaceable")
		if err := os.Mkdir(dir, 0o777); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
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
	t.Run("a group-writable ancestor is refused even for a member", func(t *testing.T) {
		t.Parallel()
		// The case the pre-fix predicate got wrong. 0770 is writable by the owning
		// group but not by others, so an other-writable check does not see it --
		// and the old Go rule accepted it whenever this process was in that group.
		// Being in the group is not the question: another member is another user,
		// and can rename or delete the fixture's entry and put their own script
		// where the test will execute it. The launcher refuses it, so the fixtures
		// have to as well.
		dir := filepath.Join(t.TempDir(), "group-writable")
		if err := os.Mkdir(dir, 0o770); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
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
				for _, gid := range gids {
					if gid == int(stat.Gid) {
						inGroup = true
						break
					}
				}
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
		dir := filepath.Join(t.TempDir(), "replaceable-tmp")
		if err := os.Mkdir(dir, 0o777); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
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
			t.Fatalf("fixtureExecDir fell back to %s, which the launcher would not build under (%s): passing over the replaceable TMPDIR is only a fix if what replaces it is trusted", got, reason)
		}
	})
}
