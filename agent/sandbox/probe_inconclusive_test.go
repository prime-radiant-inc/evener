package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// checkScriptSystem is a hermetic probe host: a Linux or macOS machine whose
// bwrap user-namespace check answers from a script (one entry per run, the last
// repeating) and whose uname answers with kernelErr.
type checkScriptSystem struct {
	osName     string
	home       string
	bwrapFound bool
	kernelErr  error
	runs       *int
	results    []error
}

func (s checkScriptSystem) goos() string                 { return s.osName }
func (checkScriptSystem) getenv(string) string           { return "" }
func (checkScriptSystem) environ() []string              { return nil }
func (s checkScriptSystem) userHomeDir() (string, error) { return s.home, nil }
func (checkScriptSystem) loginHomeDir() (string, error)  { return "", errors.New("no login home") }
func (checkScriptSystem) nonDirectoryFile(string) bool   { return false }
func (checkScriptSystem) userConfigDir() (string, error) { return "", errors.New("no config dir") }
func (checkScriptSystem) readFile(string) ([]byte, error) {
	return nil, errors.New("no file")
}

func (s checkScriptSystem) lookPath(name string) (string, error) {
	if name == "bwrap" && s.bwrapFound {
		return "/usr/bin/bwrap", nil
	}
	return "", errors.New("not found")
}

func (s checkScriptSystem) run(context.Context, string, ...string) error {
	i := *s.runs
	*s.runs++
	return s.results[min(i, len(s.results)-1)]
}

func (checkScriptSystem) combinedOutput(context.Context, string, ...string) ([]byte, error) {
	return []byte("--overlay-src"), nil
}

func (s checkScriptSystem) output(context.Context, string, ...string) ([]byte, error) {
	return []byte("6.8.0"), s.kernelErr
}

func linuxBwrapProbeSystem(home string, runs *int, results ...error) checkScriptSystem {
	return checkScriptSystem{osName: "linux", home: home, bwrapFound: true, runs: runs, results: results}
}

func resolveWorkspaceWrite(t *testing.T, host HostFacts) (ResolvedPolicy, error) {
	t.Helper()
	return Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite}, host, t.TempDir())
}

func TestProbeRetriesATimedOutUsernsCheckOnce(t *testing.T) {
	t.Parallel()
	var runs int
	facts := probeHost(linuxBwrapProbeSystem(t.TempDir(), &runs, context.DeadlineExceeded, nil))
	if runs != 2 {
		t.Fatalf("user-namespace check ran %d times, want a single retry after the timeout (2)", runs)
	}
	if !facts.BwrapCapable || facts.BwrapRecheck != nil {
		t.Fatalf("a retry that answered must settle the fact: capable=%v recheck=%v", facts.BwrapCapable, facts.BwrapRecheck != nil)
	}
}

func TestProbeLeavesAStillInconclusiveUsernsCheckUnconfirmed(t *testing.T) {
	t.Parallel()
	var runs int
	facts := probeHost(linuxBwrapProbeSystem(t.TempDir(), &runs, context.DeadlineExceeded))
	if runs != 2 {
		t.Fatalf("user-namespace check ran %d times, want 2 (one retry)", runs)
	}
	if facts.BwrapCapable || facts.BwrapRecheck == nil || !facts.BwrapAvailable() {
		t.Fatalf("an unanswered check must leave bwrap pending: capable=%v recheck=%v available=%v",
			facts.BwrapCapable, facts.BwrapRecheck != nil, facts.BwrapAvailable())
	}
	rp, err := resolveWorkspaceWrite(t, facts)
	if err != nil {
		t.Fatalf("an unconfirmed bwrap host must still resolve the mode, got %v", err)
	}
	if rp.Backend != BackendBwrap || !rp.Unconfirmed() {
		t.Fatalf("resolved backend=%v unconfirmed=%v, want bwrap and unconfirmed", rp.Backend, rp.Unconfirmed())
	}
}

func TestProbeKeepsADefinitiveUsernsFailureFailClosed(t *testing.T) {
	t.Parallel()
	var runs int
	exitErr := exec.Command("false").Run()
	if exitErr == nil {
		t.Fatal("test setup: false exited 0")
	}
	facts := probeHost(linuxBwrapProbeSystem(t.TempDir(), &runs, exitErr))
	if runs != 1 {
		t.Fatalf("a definitive answer must not be retried, ran %d times", runs)
	}
	if facts.BwrapCapable || facts.BwrapRecheck != nil || facts.BwrapAvailable() {
		t.Fatalf("a definitive no leaves no backend: capable=%v recheck=%v available=%v",
			facts.BwrapCapable, facts.BwrapRecheck != nil, facts.BwrapAvailable())
	}
	_, err := resolveWorkspaceWrite(t, facts)
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Kind != RefusalDefinitive {
		t.Fatalf("a definitive no must refuse definitively, got %v", err)
	}
}

// scriptedRecheck answers from a script and counts its runs.
func scriptedRecheck(runs *int, outcomes ...CheckOutcome) *BackendRecheck {
	return NewBackendRecheck(func() CheckOutcome {
		i := *runs
		*runs++
		if i < len(outcomes) {
			return outcomes[i]
		}
		return outcomes[len(outcomes)-1]
	})
}

func unconfirmedWrapper(t *testing.T, recheck *BackendRecheck) *Wrapper {
	t.Helper()
	host := HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapRecheck: recheck}
	rp, err := resolveWorkspaceWrite(t, host)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	w, err := NewWrapper(rp, host.BwrapPath, t.TempDir())
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	return w
}

func TestWrapperConfirmRecoversOnceTheCheckAnswers(t *testing.T) {
	t.Parallel()
	var runs int
	w := unconfirmedWrapper(t, scriptedRecheck(&runs, CheckInconclusive, CheckPassed))
	unconfirmedLine := EnforcementLine(w.Policy())

	err := w.Confirm(context.Background())
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Kind != RefusalUnconfirmed {
		t.Fatalf("an unanswered recheck must refuse as unconfirmed, got %v", err)
	}
	if !w.Policy().Unconfirmed() {
		t.Fatal("the policy must stay unconfirmed until the check passes")
	}

	if err := w.Confirm(context.Background()); err != nil {
		t.Fatalf("a passing recheck must confirm the sandbox, got %v", err)
	}
	if err := w.Confirm(context.Background()); err != nil || runs != 2 {
		t.Fatalf("a confirmed sandbox must not recheck: err=%v runs=%d", err, runs)
	}
	if w.Policy().Unconfirmed() {
		t.Fatal("the policy must read as confirmed once the check passed")
	}
	if confirmed := EnforcementLine(w.Policy()); confirmed == "" || confirmed == unconfirmedLine {
		t.Fatalf("the status line must change once confirmed: before %q, after %q", unconfirmedLine, confirmed)
	}
}

func TestWrapperConfirmKeepsADefinitiveRecheckRefusal(t *testing.T) {
	t.Parallel()
	var runs int
	w := unconfirmedWrapper(t, scriptedRecheck(&runs, CheckFailed, CheckPassed))
	for range 2 {
		err := w.Confirm(context.Background())
		var refusal *RefusalError
		if !errors.As(err, &refusal) || refusal.Kind != RefusalDefinitive {
			t.Fatalf("a recheck that answered no must refuse definitively, got %v", err)
		}
	}
	if runs != 1 {
		t.Fatalf("a definitive answer must be kept, recheck ran %d times", runs)
	}
}

func TestWrapperRefusesToConfineAnUnconfirmedPolicy(t *testing.T) {
	t.Parallel()
	var runs int
	w := unconfirmedWrapper(t, scriptedRecheck(&runs, CheckInconclusive))
	defer func() {
		if recover() == nil {
			t.Fatal("confining under an unconfirmed policy must not produce a command")
		}
	}()
	w.Confine(exec.Command("true"), t.TempDir())
}

// blockingRecheck is a recheck whose runs each wait for one value on answers,
// counting how many runs started.
func blockingRecheck(runs *atomic.Int32, started chan<- struct{}, answers <-chan CheckOutcome) *BackendRecheck {
	return NewBackendRecheck(func() CheckOutcome {
		runs.Add(1)
		started <- struct{}{}
		return <-answers
	})
}

func TestBackendRecheckSharesOneRunAcrossConcurrentCallers(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int32
		started := make(chan struct{}, 4)
		answers := make(chan CheckOutcome)
		recheck := blockingRecheck(&runs, started, answers)

		const callers = 3
		results := make(chan CheckOutcome, callers)
		for range callers {
			go func() { results <- recheck.Outcome(context.Background()) }()
		}
		// Every caller is now waiting: one run is blocked on its answer and
		// the others wait for that run.
		synctest.Wait()
		answers <- CheckInconclusive
		for range callers {
			if got := <-results; got != CheckInconclusive {
				t.Fatalf("a caller sharing the run got %v, want that run's inconclusive answer", got)
			}
		}
		if got := runs.Load(); got != 1 {
			t.Fatalf("%d concurrent callers started %d runs, want them to share one", callers, got)
		}
	})
}

func TestBackendRecheckStopsWaitingWhenTheCallerIsDone(t *testing.T) {
	t.Parallel()
	var runs atomic.Int32
	started := make(chan struct{}, 2)
	answers := make(chan CheckOutcome)
	recheck := blockingRecheck(&runs, started, answers)

	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan CheckOutcome, 1)
	go func() { waited <- recheck.Outcome(ctx) }()
	<-started
	cancel()
	if got := <-waited; got != CheckInconclusive {
		t.Fatalf("a caller whose context ended got %v, want inconclusive", got)
	}

	answers <- CheckPassed
	if got := recheck.Outcome(context.Background()); got != CheckPassed || runs.Load() != 1 {
		t.Fatalf("the abandoned run's answer must be kept: got %v after %d runs", got, runs.Load())
	}
}

func TestARecheckThatAnswersNoLeavesTheHostWithoutABackend(t *testing.T) {
	t.Parallel()
	var runs int
	host := HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapRecheck: scriptedRecheck(&runs, CheckFailed)}
	if !host.BwrapAvailable() {
		t.Fatal("an unanswered check must leave bwrap available")
	}
	host.BwrapRecheck.Outcome(context.Background())
	if host.BwrapAvailable() {
		t.Fatal("a recheck that answered no must leave the host without a backend")
	}
	_, err := resolveWorkspaceWrite(t, host)
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Kind != RefusalDefinitive {
		t.Fatalf("a host whose recheck answered no must refuse definitively, got %v", err)
	}
}
