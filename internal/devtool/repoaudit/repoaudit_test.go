package repoaudit

import (
	"strings"
	"testing"
)

// TestRunChecksReportsEachFindingUnderItsCheck pins the runner's contract:
// Errorf lets a check go on, Fatalf ends it, a clean check reports nothing,
// and any finding makes the run fail.
func TestRunChecksReportsEachFindingUnderItsCheck(t *testing.T) {
	var out strings.Builder
	status := runChecks([]check{
		{"clean", func(*audit) {}},
		{"keeps-going", func(a *audit) {
			a.Errorf("first-%d", 1)
			a.Error("second")
		}},
		{"stops", func(a *audit) {
			a.Fatal("fatal-finding")
			a.Error("after-fatal")
		}},
	}, &out)
	if status != 1 {
		t.Fatalf("status = %d, want 1 for a run with findings", status)
	}
	var reported []string
	for line := range strings.Lines(out.String()) {
		check, finding, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "FAIL "), ": ")
		if !ok {
			t.Fatalf("unparseable report line %q", line)
		}
		reported = append(reported, check+"="+finding)
	}
	want := []string{"keeps-going=first-1", "keeps-going=second", "stops=fatal-finding"}
	if strings.Join(reported, "\n") != strings.Join(want, "\n") {
		t.Fatalf("reported %q, want %q", reported, want)
	}
}

func TestRunChecksPassesWhenNoCheckFindsAnything(t *testing.T) {
	var out strings.Builder
	if status := runChecks([]check{{"clean", func(*audit) {}}}, &out); status != 0 {
		t.Fatalf("status = %d, want 0", status)
	}
}

// TestRunChecksReportsNotesWithoutFailing pins the note channel: a ratchet
// that has slack says so on a NOTE line, and the run still passes.
func TestRunChecksReportsNotesWithoutFailing(t *testing.T) {
	var out strings.Builder
	status := runChecks([]check{{"ratchet", func(a *audit) { a.Notef("slack-%d", 3) }}}, &out)
	if status != 0 {
		t.Fatalf("status = %d, want 0: a note is not a finding", status)
	}
	var notes []string
	for line := range strings.Lines(out.String()) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "NOTE "); ok {
			notes = append(notes, rest)
		}
	}
	if want := []string{"ratchet: slack-3"}; strings.Join(notes, "\n") != strings.Join(want, "\n") {
		t.Fatalf("notes %q, want %q", notes, want)
	}
}

// TestRunCheckPropagatesAPanicThatIsNotAStop keeps a crashing check from
// reading as a passing one.
func TestRunCheckPropagatesAPanicThatIsNotAStop(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("runCheck swallowed a check's own panic")
		}
	}()
	runCheck(check{"crashes", func(*audit) { panic("boom") }})
}
