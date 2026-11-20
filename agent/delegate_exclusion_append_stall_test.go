//go:build unix

package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
)

// appendStallTripwire bounds how long the rebuild may take before the test
// declares it wedged. It is generous: the assertion is "reaches an answer at
// all", and a passing run finishes in milliseconds.
const appendStallTripwire = 10 * time.Second

// TestExclusionRefreshDoesNotRereadStalledAppendFiles pins that the
// delegate-reachability rebuild reuses the append contents it already has.
//
// The defect is the same class as the bounded MCP probe and sits on the same
// path: request preparation resolved the freshness verdict under a bound, then
// rebuilt the prompt while holding s.mu, and that rebuild read every
// --system-prompt-append file with os.ReadFile. A file readable at startup
// whose mount has since stalled blocks that read forever, and the block lands
// INSIDE the session lock — so the session does not merely run slow, it wedges,
// and Stop cannot interrupt a read that never observes cancellation.
//
// A FIFO with no writer is exactly that shape without needing a stalled mount.
// The file is loaded as a real file first so the snapshot exists, then replaced
// by the FIFO, and the rebuild under test must finish from the snapshot.
func TestExclusionRefreshDoesNotRereadStalledAppendFiles(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	dir := realTempDirForTest(t)
	appendPath := filepath.Join(dir, "append.md")
	const body = "operator instructions"
	if err := os.WriteFile(appendPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth:   1,
		SystemPromptAppend: []string{appendPath},
		AgentsDocPath:      filepath.Join(dir, "absent-AGENTS.md"),
	}))
	if s.appendSnapshot == nil {
		t.Fatal("fixture is wrong: the startup render did not record an append snapshot, so there is nothing to reuse")
	}
	if got := s.appendSnapshot.loaded; len(got) != 1 || got[0].path != appendPath || got[0].content != body {
		t.Fatalf("append snapshot = %+v, want the startup contents", got)
	}

	release := stallAppendFile(t, appendPath)

	// The exclusion refresh renders the prompt only while it is still mutable: a
	// frozen launch prompt (#6959) carries no per-role tool list to correct, so
	// the refresh leaves it alone and never reaches an append read. This fixture
	// therefore stands for the one case that does render — a session whose launch
	// render failed, leaving systemPromptFrozen false — because that render is
	// where a stalled append file would wedge s.mu. Unfreezing here is what keeps
	// the test from passing without ever entering the reuse path.
	s.mu.Lock()
	s.systemPromptFrozen = false
	s.mu.Unlock()

	// The exclusion decision flips, which is what drives the cache rebuild down
	// the reuse path.
	flipExclusionDecision(s)

	// The error travels SEPARATELY from the prompt, because the pass condition
	// is that request preparation SUCCEEDED from the snapshot. Folding the two
	// into one string made any error read as a non-empty answer: the assertion
	// below then passed on a failed preparation and the test stopped proving
	// anything about the reuse path.
	type prepareOutcome struct {
		sys string
		err error
	}
	returned := make(chan prepareOutcome, 1)
	go func() {
		var timings events.RoundTimings
		_, sys, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &timings)
		returned <- prepareOutcome{sys: sys, err: err}
	}()

	select {
	case got := <-returned:
		release()
		if got.err != nil {
			t.Fatalf("the rebuilt request failed: %v", got.err)
		}
		// A prompt that came back at all is not the property under test: the
		// rebuild must have RENDERED THE APPEND from the snapshot it already had,
		// which is the difference between reusing the load and skipping it.
		if !strings.Contains(got.sys, body) {
			t.Errorf("the rebuilt system prompt does not carry the append fixture %q, so the rebuild did not render the snapshot's contents", body)
		}
	case <-time.After(appendStallTripwire): // TRIPWIRE: the rebuild read the stalled file under s.mu
		release()
		t.Fatal("the request path blocked on a stalled --system-prompt-append read while holding s.mu")
	}
}

// TestExclusionRefreshReusesAPartialAppendLoad pins the same reuse for a load
// that could not read every file.
//
// A file missing at startup used to leave NO snapshot at all, because only a
// complete load was recorded. The next delegate-reachability refresh therefore
// re-read every configured path under s.mu — including the readable one, whose
// mount may since have stalled — so one absent file defeated the protection for
// all the others. The omission is part of the load's answer: a reuse must
// reproduce both the files that read and the paths that did not, and it must
// reach that answer without touching the filesystem at all.
func TestExclusionRefreshReusesAPartialAppendLoad(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	dir := realTempDirForTest(t)
	appendPath := filepath.Join(dir, "append.md")
	const body = "operator instructions"
	if err := os.WriteFile(appendPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "absent-append.md")

	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth:   1,
		SystemPromptAppend: []string{appendPath, missing},
		AgentsDocPath:      filepath.Join(dir, "absent-AGENTS.md"),
	}))
	release := stallAppendFile(t, appendPath)
	flipExclusionDecision(s)

	got := awaitAppendReuse(t, s, release)
	if len(got.appends) != 1 || got.appends[0] != body {
		t.Errorf("reused appends = %q, want the one file that loaded", got.appends)
	}
	if want := []string{missing}; !slices.Equal(got.unreadable, want) {
		t.Errorf("reused unreadable = %v, want %v: the reuse dropped the omission the read had disclosed", got.unreadable, want)
	}

	// The mechanism behind the reuse: the partial load was recorded as it
	// happened, so there is something to answer from at all.
	snapshot := s.appendSnapshot
	if snapshot == nil {
		t.Fatal("a startup load that skipped an unreadable file recorded no snapshot, so the next reachability refresh has nothing to reuse")
	}
	if len(snapshot.loaded) != 1 || snapshot.loaded[0].path != appendPath || snapshot.loaded[0].content != body {
		t.Errorf("snapshot loaded = %+v, want only %q", snapshot.loaded, appendPath)
	}
	if want := []string{missing}; !slices.Equal(snapshot.unreadable, want) {
		t.Errorf("snapshot unreadable = %v, want %v: the load's omission was not recorded with it", snapshot.unreadable, want)
	}
}

// appendDataProbe is what one reuse-path prompt build put in the append data.
type appendDataProbe struct {
	appends    []string
	unreadable []string
}

// awaitAppendReuse builds the prompt data from the append SNAPSHOT and fails the
// test if that build has to touch the filesystem to answer. The build runs in
// its own goroutine because a regression blocks inside the read, and the release
// lets that goroutine finish so a failing run reports rather than hangs.
func awaitAppendReuse(t *testing.T, s *Session, release func()) appendDataProbe {
	t.Helper()
	got := make(chan appendDataProbe, 1)
	go func() {
		data, _ := s.buildPromptDataWithAppends(s.env, true)
		got <- appendDataProbe{appends: data.CLIAppends, unreadable: data.unreadableAppends}
	}()
	select {
	case probe := <-got:
		return probe
	case <-time.After(appendStallTripwire): // TRIPWIRE: the reuse path reread a stalled file
		release()
		t.Fatal("the reachability refresh re-read --system-prompt-append instead of reusing the load it already had")
		return appendDataProbe{}
	}
}

// stallAppendFile replaces path with a named pipe no writer opens, which is how
// a mount that stops answering behaves for a reader: os.ReadFile blocks.
//
// The returned release opens the write end so a FAILING run can finish: the read
// under test may hold s.mu, and leaving the reader parked would deadlock the
// session's own Close at cleanup and turn a clean assertion failure into a hung
// test binary.
func stallAppendFile(t *testing.T, path string) (release func()) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("named pipes unavailable here: %v", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			// O_NONBLOCK is load-bearing: a blocking write-open waits for a
			// reader, and on the PASSING path the rebuild never opened one, so
			// it would hang the very test that proved the fix works.
			// Non-blocking answers ENXIO when nobody is reading, which is
			// exactly the case to leave alone.
			if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = f.Close()
			}
		})
	}
}

// flipExclusionDecision moves the recorded roster decision, which is what makes
// the next request rebuild both caches down the reuse path.
//
// The session's evidence is deliberately left EMPTY: nothing can verify against
// it, so the probe the rebuild runs answers "not current" and the flip is the one
// this fixture asked for. The assertion is about the append read, not the answer.
func flipExclusionDecision(s *Session) {
	s.setRosterExclusion(!s.rosterExclusion())
}
