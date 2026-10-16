package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/appwire"
)

// A discarded saved diagnostic must not make an authoritative live read fail.
// A directory at the journal path makes an attempted read fail even as root.
func TestMergePastThreadForReadSkipsUnusedSavedDelegateDiagnostics(t *testing.T) {
	cfg, entry := seedPastSessionWithShellExits(t, 0, []shellRound{{0}})
	journalPath := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID, "delegates.jsonl")
	if err := os.MkdirAll(journalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	store := task.NewTaskStore(entry.StateDir, entry.Meta.ID)
	if _, err := store.Append([]task.TaskInput{{Type: task.TaskTypeVerify, Description: "saved-task", Prompt: "fixture"}}); err != nil {
		t.Fatal(err)
	}
	params := appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID}
	for _, tc := range []struct {
		name             string
		includeTurns     bool
		emptyDiagnostics bool
		liveTasks        *appwire.TaskAggregate
	}{
		{name: "metadata with live tasks", liveTasks: &appwire.TaskAggregate{}},
		{name: "metadata with saved tasks"},
		{name: "empty live diagnostics", emptyDiagnostics: true},
		{name: "saved turn fallback", includeTurns: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diagnostics := &appwire.EvenerDiagnostics{Tools: []appwire.EvenerToolInfo{{Name: "live-tool"}}}
			wantTools := 1
			if tc.emptyDiagnostics {
				diagnostics = &appwire.EvenerDiagnostics{}
				wantTools = 0
			}
			got, err := mergePastThreadForRead(context.Background(), cfg, params, appwire.Thread{
				ID: entry.Meta.ID, SessionID: entry.Meta.ID,
				Evener: appwire.EvenerThread{Diagnostics: diagnostics, Tasks: tc.liveTasks},
			}, tc.includeTurns)
			if err != nil {
				t.Fatalf("live read depended on unused saved delegate journal: %v", err)
			}
			if got.Evener.Diagnostics != diagnostics || len(got.Evener.Diagnostics.Tools) != wantTools {
				t.Fatalf("live diagnostics changed: %+v", got.Evener.Diagnostics)
			}
			if got.ModelProvider != entry.Meta.Model || got.CWD != entry.Meta.EnvInfo.WorkingDir || got.CreatedAt != entry.Meta.CreatedAt.Unix() {
				t.Fatalf("saved metadata fallback lost: %+v", got)
			}
			if tc.liveTasks != nil {
				if got.Evener.Tasks != tc.liveTasks || got.Evener.Tasks.Total != 0 {
					t.Fatalf("authoritative empty live tasks replaced: %+v", got.Evener.Tasks)
				}
			} else if got.Evener.Tasks == nil || got.Evener.Tasks.Total != 1 || got.Evener.Tasks.Remaining != 1 {
				t.Fatalf("saved task fallback lost: %+v", got.Evener.Tasks)
			}
			if tc.includeTurns && len(got.Turns) != 1 {
				t.Fatalf("saved turns = %d, want 1", len(got.Turns))
			}
			if !tc.includeTurns && len(got.Turns) != 0 {
				t.Fatalf("metadata read included turns: %+v", got.Turns)
			}
		})
	}
	t.Run("absent live diagnostics still consult saved journal", func(t *testing.T) {
		_, err := mergePastThreadForRead(context.Background(), cfg, params, appwire.Thread{ID: entry.Meta.ID}, false)
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || pathErr.Path != journalPath {
			t.Fatalf("saved fallback error = %v, want PathError for %s", err, journalPath)
		}
	})
	t.Run("remote does not consult local fallback", func(t *testing.T) {
		live := appwire.Thread{ID: entry.Meta.ID, Source: "remote", Evener: appwire.EvenerThread{Ref: "remote:" + entry.Meta.ID}}
		got, err := mergePastThreadForRead(context.Background(), cfg, params, live, true)
		if err != nil || !reflect.DeepEqual(got, live) {
			t.Fatalf("remote read changed by local saved state: got=%+v err=%v", got, err)
		}
	})
}
