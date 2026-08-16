package bundled

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func FuzzAssets(f *testing.F) {
	for _, seed := range []uint8{0, 1, 2} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, which uint8) {
		var filesystem fs.FS
		switch which % 3 {
		case 0:
			filesystem = Agents()
		case 1:
			filesystem = Skills()
		default:
			filesystem = Plugins()
		}
		entries, err := fs.ReadDir(filesystem, ".")
		if err != nil || len(entries) == 0 {
			t.Fatalf("embedded assets unavailable: entries=%d err=%v", len(entries), err)
		}
	})
}

func TestMustSubPanics(t *testing.T) {
	old := subFS
	subFS = func(fs.FS, string) (fs.FS, error) { return nil, errors.New("broken") }
	t.Cleanup(func() { subFS = old })
	defer func() {
		if recover() == nil {
			t.Fatal("mustSub did not panic")
		}
	}()
	_ = mustSub("missing")
}

func TestBundledCoordinatorUsesStableDelegateAndShellIdentities(t *testing.T) {
	body, err := fs.ReadFile(Plugins(), "coordinator-workflow/agents/coordinator.md")
	if err != nil {
		t.Fatalf("read bundled coordinator: %v", err)
	}
	prompt := string(body)

	for _, want := range []string{
		"Delegate control uses `delegate_id` (`dlg_...`); shell control uses `job_id` (`job_...`).",
		"`job_status(target=<dlg_...>)`",
		"`job_stop(target=<dlg_...>)`",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("bundled coordinator missing stable identity contract %q", want)
		}
	}
	for _, stale := range []string{
		"returned delegate_id, job_id, and transcript_ref",
		"max_wait_ms=120000",
	} {
		if strings.Contains(prompt, stale) {
			t.Errorf("bundled coordinator retains activation-job guidance %q", stale)
		}
	}
}

func TestBundledSubagentPreservesCallerRouteAndCommunicateFinal(t *testing.T) {
	body, err := fs.ReadFile(Agents(), "subagent.md")
	if err != nil {
		t.Fatalf("read bundled subagent: %v", err)
	}
	prompt := string(body)
	for _, want := range []string{
		"`delegate_send(to=\"caller\")`",
		"non-terminal",
		"`communicate(end_turn=true)`",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("bundled subagent missing caller/final contract %q", want)
		}
	}
}
