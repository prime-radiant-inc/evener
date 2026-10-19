package hubcore

import (
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A whole input can run between probes. The completion changes navigation's
// fingerprint even though both probes see idle; its indexed history must be
// refreshed before that navigation publication, not at the next full rebuild.
func TestRosterCompletionRefreshesIndexedDormancy(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project-test-0123456789")
	const id = "02wMz5Txv2enqVTitaig6F"
	base := time.Unix(1_700_000_000, 0)
	meta := schema.SessionMeta{ID: id, UpdatedAt: base, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	writeMeta(t, project, meta)
	past := NewPastIndex(filepath.Join(root, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})
	probe := &runningSubagentProber{result: ProbeResult{SessionID: id, Status: appwire.ThreadStatusIdle, OK: true}}
	roster := NewRoster(dir, probe)
	roster.Refresh()

	readRow := func() TreeNode {
		t.Helper()
		entry, ok := past.Find(id)
		if !ok {
			t.Fatal("session missing from real past index")
		}
		tree := BuildTreeAt([]schema.SessionMeta{entry.Meta}, roster.List(), nil, base.Add(time.Minute))
		if len(tree.Live) != 1 || tree.Live[0].ID != id {
			t.Fatalf("unexpected live tree: %+v", tree.Live)
		}
		return tree.Live[0]
	}
	if !readRow().Dormant {
		t.Fatal("fixture must begin with an indexed never-run session")
	}
	refreshes := 0
	roster.SetOnStatusChange(func(sessionID string) {
		refreshes++
		past.RefreshOne(sessionID)
	})
	publications := 0
	roster.SetOnChange(func() {
		publications++
		if readRow().Dormant {
			t.Error("completion fingerprint published navigation with stale indexed dormancy")
		}
	})

	meta.AcceptedInputTurns = 1
	meta.TurnCount = 1
	meta.UpdatedAt = base.Add(time.Second)
	writeMeta(t, project, meta)
	probe.result.LastTurnEndedAt = meta.UpdatedAt
	roster.Refresh()
	if publications != 1 {
		t.Fatalf("completion must change the fingerprint once, got %d publications", publications)
	}
	if refreshes != 1 {
		t.Fatalf("completion must refresh indexed history once despite unchanged idle status, got %d", refreshes)
	}
	if readRow().Dormant {
		t.Fatal("completed input still projects as never run")
	}
	entry, _ := past.Find(id)
	if entry.Meta.AcceptedInputTurns != 1 || entry.Meta.TurnCount != 1 {
		t.Fatalf("index did not consume recorded input and response: %+v", entry.Meta)
	}
	roster.Refresh()
	if refreshes != 1 || publications != 1 {
		t.Fatalf("unchanged probe repeated work: refreshes=%d publications=%d", refreshes, publications)
	}
}

func TestRosterHistoryRefreshTriggers(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for _, tc := range []struct {
		name   string
		status string
		ended  time.Time
		want   int
	}{
		{name: "unchanged", status: appwire.ThreadStatusIdle, ended: base},
		{name: "completion", status: appwire.ThreadStatusIdle, ended: base.Add(time.Second), want: 1},
		{name: "status", status: appwire.ThreadStatusActive, ended: base, want: 1},
		{name: "both fire once", status: appwire.ThreadStatusActive, ended: base.Add(time.Second), want: 1},
		{name: "older completion", status: appwire.ThreadStatusIdle, ended: base.Add(-time.Second)},
		{name: "unknown completion", status: appwire.ThreadStatusIdle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})
			probe := &runningSubagentProber{result: ProbeResult{SessionID: "02wMz5Txv2enqVTitaig6F", Status: appwire.ThreadStatusIdle, LastTurnEndedAt: base, OK: true}}
			roster := NewRoster(dir, probe)
			calls := 0
			roster.SetOnStatusChange(func(string) { calls++ })
			roster.Refresh()
			if calls != 0 {
				t.Fatal("first discovery must not report a transition")
			}
			probe.result.Status, probe.result.LastTurnEndedAt = tc.status, tc.ended
			roster.Refresh()
			if calls != tc.want {
				t.Fatalf("refresh calls = %d, want %d", calls, tc.want)
			}
			roster.Refresh()
			if calls != tc.want {
				t.Fatal("unchanged subsequent probe repeated history refresh")
			}
		})
	}
}
