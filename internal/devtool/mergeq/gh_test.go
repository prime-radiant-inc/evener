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
	dir := t.TempDir()
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
	groupFile := filepath.Join(t.TempDir(), "gh-group")
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
	silent := filepath.Join(t.TempDir(), "gh-silent")
	writeExecutable(t, silent, "#!/usr/bin/env bash\n( sleep 30 ) &\nexit 0\n")
	answered := filepath.Join(t.TempDir(), "gh-answered")
	writeExecutable(t, answered, "#!/usr/bin/env bash\nprintf '{\"data\":{\"repository\":{\"pullRequest\":{\"id\":\"PR_1\",\"state\":\"OPEN\"}}}}'\n( sleep 30 ) &\nexit 0\n")

	c := GHClient{GH: silent, GroupFile: filepath.Join(t.TempDir(), "group")}
	out, err := c.run(context.Background(), "api", "graphql")
	if err == nil {
		t.Fatalf("a wrapper that exited before answering was read as a successful call (out %q): an enqueue would be reported as landed on a reply nobody received", string(out))
	}
	if !errors.Is(err, errIncompleteReply) {
		t.Fatalf("error = %v, want errIncompleteReply", err)
	}

	// And the case the drain bound exists for still succeeds: a complete reply,
	// with a descendant merely holding the pipe open.
	c = GHClient{GH: answered, GroupFile: filepath.Join(t.TempDir(), "group")}
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
	dir := t.TempDir()
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
	dir := t.TempDir()
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
