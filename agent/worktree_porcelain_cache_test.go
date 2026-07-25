package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/worktree"
)

// recordingRunner is a GitRunner that records every argv it is handed and
// replies with a canned porcelain listing.
func recordingRunner(calls *[]string, porcelain string) worktree.GitRunner {
	return func(args ...string) (string, error) {
		*calls = append(*calls, strings.Join(args, " "))
		if len(args) >= 2 && args[0] == "worktree" && args[1] == "list" {
			return porcelain, nil
		}
		return "", nil
	}
}

const twoLanePorcelain = "worktree /repo\nHEAD aaa\nbranch refs/heads/main\n\n" +
	"worktree /repo/wt/lane-a\nHEAD bbb\nbranch refs/heads/lane-a\nlocked serf:dlg:s1:d1\n\n" +
	"worktree /repo/wt/lane-b\nHEAD ccc\nbranch refs/heads/lane-b\n\n"

// Repeated reads with no intervening mutation must shell out once. This is the
// whole point: a close pass over N lanes ran `git worktree list` N times.
func TestPorcelainCacheReusesListingWithinAPass(t *testing.T) {
	t.Parallel()
	var calls []string
	run := cachingWorktreeRunner(recordingRunner(&calls, twoLanePorcelain))

	for range 5 {
		if _, err := readWorktreePorcelain(run); err != nil {
			t.Fatalf("readWorktreePorcelain: %v", err)
		}
	}

	if got := listCalls(calls); got != 1 {
		t.Fatalf("worktree list ran %d times, want 1", got)
	}
}

// A mutation invalidates the cache: the next read must see fresh state, or a
// dispose loop would judge later lanes against the worktree layout as it was
// before earlier lanes were removed.
func TestPorcelainCacheInvalidatesOnMutation(t *testing.T) {
	t.Parallel()
	for _, mutation := range [][]string{
		{"worktree", "remove", "--", "/repo/wt/lane-a"},
		{"worktree", "add", "/repo/wt/lane-c"},
		{"worktree", "unlock", "/repo/wt/lane-a"},
		{"worktree", "lock", "--reason", "serf:dlg:s1:d1", "/repo/wt/lane-b"},
		{"worktree", "prune"},
	} {
		t.Run(strings.Join(mutation, "_"), func(t *testing.T) {
			t.Parallel()
			var calls []string
			run := cachingWorktreeRunner(recordingRunner(&calls, twoLanePorcelain))

			if _, err := readWorktreePorcelain(run); err != nil {
				t.Fatal(err)
			}
			if _, err := run(mutation...); err != nil {
				t.Fatal(err)
			}
			if _, err := readWorktreePorcelain(run); err != nil {
				t.Fatal(err)
			}

			if got := listCalls(calls); got != 2 {
				t.Fatalf("worktree list ran %d times, want 2 (mutation must invalidate)", got)
			}
		})
	}
}

// Only worktree-state mutations invalidate. A read-only query alongside the
// listing must not throw the cache away, or the fix buys nothing on the paths
// that interleave reads.
func TestPorcelainCacheSurvivesReadOnlyCommands(t *testing.T) {
	t.Parallel()
	var calls []string
	run := cachingWorktreeRunner(recordingRunner(&calls, twoLanePorcelain))

	if _, err := readWorktreePorcelain(run); err != nil {
		t.Fatal(err)
	}
	for _, readOnly := range [][]string{
		{"rev-parse", "HEAD"},
		{"status", "--porcelain"},
		{"show-ref", "--verify", "refs/heads/lane-a"},
		{"merge-base", "--is-ancestor", "aaa", "bbb"},
		{"worktree", "list", "--porcelain"},
	} {
		if _, err := run(readOnly...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := readWorktreePorcelain(run); err != nil {
		t.Fatal(err)
	}

	if got := listCalls(calls); got != 1 {
		t.Fatalf("worktree list ran %d times, want 1: read-only commands must not invalidate", got)
	}
}

// A command that mutates refs or the working tree can change what the porcelain
// listing reports (HEAD, branch), so those invalidate too.
func TestPorcelainCacheInvalidatesOnRefMutation(t *testing.T) {
	t.Parallel()
	for _, mutation := range [][]string{
		{"branch", "-D", "lane-a"},
		{"checkout", "-b", "lane-d"},
		{"commit", "-m", "x"},
		{"merge", "lane-a"},
		{"switch", "main"},
		{"reset", "--hard"},
	} {
		t.Run(mutation[0], func(t *testing.T) {
			t.Parallel()
			var calls []string
			run := cachingWorktreeRunner(recordingRunner(&calls, twoLanePorcelain))
			if _, err := readWorktreePorcelain(run); err != nil {
				t.Fatal(err)
			}
			if _, err := run(mutation...); err != nil {
				t.Fatal(err)
			}
			if _, err := readWorktreePorcelain(run); err != nil {
				t.Fatal(err)
			}
			if got := listCalls(calls); got != 2 {
				t.Fatalf("worktree list ran %d times, want 2", got)
			}
		})
	}
}

// A failed listing must not be cached as a success: the next read has to retry.
func TestPorcelainCacheDoesNotCacheErrors(t *testing.T) {
	t.Parallel()
	var calls []string
	fail := true
	run := cachingWorktreeRunner(func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if fail {
			return "", &gitCmdError{code: 1, args: args, stderr: "boom"}
		}
		return twoLanePorcelain, nil
	})

	if _, err := readWorktreePorcelain(run); err == nil {
		t.Fatal("first read succeeded, want the runner's error")
	}
	fail = false
	entries, err := readWorktreePorcelain(run)
	if err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if got := listCalls(calls); got != 2 {
		t.Fatalf("worktree list ran %d times, want 2: a failed listing must not be cached", got)
	}
}

// The cached listing must parse to exactly what the uncached one does — the
// cache is a call-count optimization, never a change in reported state.
func TestPorcelainCacheReturnsSameEntriesAsUncached(t *testing.T) {
	t.Parallel()
	var direct, cached []string
	plain := recordingRunner(&direct, twoLanePorcelain)
	run := cachingWorktreeRunner(recordingRunner(&cached, twoLanePorcelain))

	want, err := readWorktreePorcelain(plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readWorktreePorcelain(run)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("cached %d entries, uncached %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d: cached %+v, uncached %+v", i, got[i], want[i])
		}
	}
	// And the lock state derived from it must agree.
	for _, p := range []string{"/repo/wt/lane-a", "/repo/wt/lane-b", "/repo"} {
		wl, wr := lockStateFromPorcelain(want, p)
		gl, gr := lockStateFromPorcelain(got, p)
		if wl != gl || wr != gr {
			t.Fatalf("%s: cached (%v,%q), uncached (%v,%q)", p, gl, gr, wl, wr)
		}
	}
}

// A nil runner must stay nil rather than becoming a non-nil wrapper that panics
// on first use.
func TestCachingWorktreeRunnerNilPassesThrough(t *testing.T) {
	t.Parallel()
	if cachingWorktreeRunner(nil) != nil {
		t.Fatal("wrapping a nil runner produced a non-nil runner")
	}
}

func listCalls(calls []string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "worktree list") {
			n++
		}
	}
	return n
}
