package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestSessionActivityDelegateNameSurvivesRetainedRead(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"inspect-cache", strings.Repeat("界", 220), ""} {
		t.Run(name[:min(len(name), 24)], func(t *testing.T) {
			s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "absent-AGENTS.md")}))
			descriptor := stableReadonlyDescriptor(s, "dlg_named")
			descriptor.Name = name
			descriptor.Task = "Inspect the complete cache ownership and repair plan"
			at := time.Unix(100, 0).UTC()
			seedStableReadonlyFinish(t, s, "dlg_named", descriptor, at, stableDelegateFinishFromRun(delegateTerminalRunInputs{descriptor: descriptor, result: "Report remains available", communicated: true, startedAt: at, endedAt: at.Add(time.Second)}), true)
			params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID())}
			live, err := s.ListActivityDelegates(t.Context(), params)
			if err != nil {
				t.Fatal(err)
			}
			key := s.stateDir + "\x00" + s.ID()
			sessionActivityIndexes.Lock()
			if cached := sessionActivityIndexes.entries[key]; cached != nil {
				delete(sessionActivityIndexes.entries, key)
				sessionActivityIndexes.order.Remove(cached.element)
			}
			sessionActivityIndexes.Unlock()
			retained, err := LoadSessionActivityDelegates(t.Context(), s.stateDir, s.ID(), params)
			if err != nil {
				t.Fatal(err)
			}
			for _, page := range []appwire.SessionDelegatesResponse{live, retained} {
				if len(page.Delegates) != 1 {
					t.Fatalf("rows=%d", len(page.Delegates))
				}
				row := page.Delegates[0]
				raw, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				want := name
				if len([]rune(name)) > 200 {
					want = strings.Repeat("界", 199) + "…"
				}
				if name == "" {
					if _, exists := fields["name"]; exists {
						t.Fatal("unnamed descriptor acquired a display name")
					}
				} else if fields["name"] != want {
					t.Fatalf("name=%v, want %q", fields["name"], want)
				}
				if row.Task != descriptor.Task || row.RunGeneration != 1 || row.ReportPreview != "Report remains available" {
					t.Fatalf("name changed delegate facts: %+v", row)
				}
			}
		})
	}
}
