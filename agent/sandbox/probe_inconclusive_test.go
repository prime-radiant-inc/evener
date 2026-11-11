package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"testing"
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
	if !facts.BwrapCapable || !facts.Complete() || facts.BwrapRecheck != nil {
		t.Fatalf("a retry that answered must settle the fact: capable=%v complete=%v recheck=%v",
			facts.BwrapCapable, facts.Complete(), facts.BwrapRecheck != nil)
	}
}

func TestProbeLeavesAStillInconclusiveUsernsCheckUnconfirmed(t *testing.T) {
	t.Parallel()
	var runs int
	facts := probeHost(linuxBwrapProbeSystem(t.TempDir(), &runs, context.DeadlineExceeded))
	if runs != 2 {
		t.Fatalf("user-namespace check ran %d times, want 2 (one retry)", runs)
	}
	if facts.BwrapCapable || facts.Complete() || facts.BwrapRecheck == nil {
		t.Fatalf("an unanswered check must be marked: capable=%v complete=%v recheck=%v",
			facts.BwrapCapable, facts.Complete(), facts.BwrapRecheck != nil)
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
	if facts.BwrapCapable || !facts.Complete() || facts.BwrapRecheck != nil {
		t.Fatalf("a definitive no is a complete answer: capable=%v complete=%v recheck=%v",
			facts.BwrapCapable, facts.Complete(), facts.BwrapRecheck != nil)
	}
	_, err := resolveWorkspaceWrite(t, facts)
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Kind != RefusalDefinitive {
		t.Fatalf("a definitive no must refuse definitively, got %v", err)
	}
}

func TestProbeMarksAnUnansweredInformationalCheckIncomplete(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		kernelErr error
		complete  bool
	}{
		{"timed out", context.DeadlineExceeded, false},
		{"answered with an error", errors.New("uname: not found"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var runs int
			system := checkScriptSystem{osName: "linux", home: t.TempDir(), kernelErr: tc.kernelErr, runs: &runs, results: []error{nil}}
			if got := probeHost(system).Complete(); got != tc.complete {
				t.Fatalf("Complete() = %v, want %v", got, tc.complete)
			}
		})
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
	host := HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapRecheck: recheck, InconclusiveChecks: []string{"bwrap user namespaces"}}
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

	err := w.Confirm()
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Kind != RefusalUnconfirmed {
		t.Fatalf("an unanswered recheck must refuse as unconfirmed, got %v", err)
	}
	if !w.Policy().Unconfirmed() {
		t.Fatal("the policy must stay unconfirmed until the check passes")
	}

	if err := w.Confirm(); err != nil {
		t.Fatalf("a passing recheck must confirm the sandbox, got %v", err)
	}
	if err := w.Confirm(); err != nil || runs != 2 {
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
		err := w.Confirm()
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
