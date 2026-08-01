package agent

import (
	"os"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// The durable registry row names who RECEIVES a watch, not just who owns it. A
// receiver watch installs on the OWNER's manager, so owner and visible session
// are both the owner; without the receiver identity nothing in jobs.jsonl can
// tell a cross-session receiver watch from the owner's own watch, and an
// operator reading "no receiver session on this watch" learns nothing — it reads
// the same for a correctly installed parent-source watch and a broken one.
func TestWatchRegistryRowCarriesReceiverIdentity(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "go test ./..."})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:             rec.JobID,
		OutputMatch:        "(FAIL|ok  |PASS)",
		ReceiverSessionID:  "S-observer",
		ReceiverDelegateID: "dlg_obs",
		ReceiverNotify:     func(jobNotification) {},
	}); err != nil {
		t.Fatalf("install receiver watch: %v", err)
	}

	log, err := os.ReadFile(jm.dir + "/jobs.jsonl")
	if err != nil {
		t.Fatalf("read jobs.jsonl: %v", err)
	}
	for _, want := range []string{`"receiver_session_id":"S-observer"`, `"receiver_delegate_id":"dlg_obs"`} {
		if !strings.Contains(string(log), want) {
			t.Errorf("durable watch registry row missing %s; got:\n%s", want, log)
		}
	}
}

// The folded registry — what serf-doctor and the restore paths read — carries
// the receiver through, so nobody has to re-parse the config snapshot to answer
// "who is watching this".
func TestFoldedWatchRecordCarriesReceiverIdentity(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "go test ./..."})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	res, err := jm.configureWatch(watchArgs{
		Target:            rec.JobID,
		OutputMatch:       "(FAIL|ok  |PASS)",
		ReceiverSessionID: "S-observer",
		ReceiverNotify:    func(jobNotification) {},
	})
	if err != nil {
		t.Fatalf("install receiver watch: %v", err)
	}

	watches, err := jm.store.LoadWatches()
	if err != nil {
		t.Fatalf("load watches: %v", err)
	}
	w := watches[res.WatchID]
	if w == nil {
		t.Fatalf("watch %q missing from the durable registry", res.WatchID)
	}
	if w.ReceiverSessionID != "S-observer" || w.ReceiverDelegateID != "" {
		t.Errorf("folded receiver = %q/%q, want S-observer and no delegate", w.ReceiverSessionID, w.ReceiverDelegateID)
	}
	if w.OwnerSessionID != jm.sessionID || w.VisibleSessionID != jm.sessionID {
		t.Errorf("folded owner/visible = %q/%q, want the owning manager on both — the reason the receiver field is needed", w.OwnerSessionID, w.VisibleSessionID)
	}
}

// An owner's own watch has no receiver, and its row must stay exactly as it was:
// both fields omitempty, so an existing jobs.jsonl folds unchanged.
func TestWatchRegistryRowOmitsReceiverForOwnerWatch(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "go test ./..."})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	res, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(FAIL|ok  |PASS)"})
	if err != nil {
		t.Fatalf("install watch: %v", err)
	}

	log, err := os.ReadFile(jm.dir + "/jobs.jsonl")
	if err != nil {
		t.Fatalf("read jobs.jsonl: %v", err)
	}
	if strings.Contains(string(log), "receiver_") {
		t.Errorf("owner watch row carries a receiver field; got:\n%s", log)
	}
	watches, err := jm.store.LoadWatches()
	if err != nil {
		t.Fatalf("load watches: %v", err)
	}
	w := watches[res.WatchID]
	if w == nil {
		t.Fatalf("watch %q missing from the durable registry", res.WatchID)
	}
	if w.ReceiverSessionID != "" || w.ReceiverDelegateID != "" {
		t.Errorf("owner watch receiver = %q/%q, want both empty", w.ReceiverSessionID, w.ReceiverDelegateID)
	}
}

// Moving the receiver identity INTO the hashed config snapshot must not move the
// config hash: it is durable, it keys watch equivalence and replacement across a
// restart, and a silent reshuffle would make every already-written row's hash
// unrecognizable. These goldens are the hashes the code produced when the
// receiver rode a wrapper struct beside the snapshot.
func TestNormalizedWatchConfigHashSurvivesReceiverFieldsJoiningTheSnapshot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args watchArgs
		want string
	}{
		{
			name: "no receiver",
			args: watchArgs{Target: "job_x", OutputMatch: "ready"},
			want: "sha256:e6c0d2901cefa3ae7d28ed8afe2263e1d9721eb33ac84958d0b0fefda56c4197",
		},
		{
			name: "receiver session only",
			args: watchArgs{Target: "job_x", OutputMatch: "ready", ReceiverSessionID: "S-observer"},
			want: "sha256:ce6bbc0eeedaa04fd4ef04e9ff5907d8b1aacfc48111128875a491825a0a6245",
		},
		{
			name: "receiver session and delegate",
			args: watchArgs{Target: "job_x", OutputMatch: "ready", ReceiverSessionID: "S-observer", ReceiverDelegateID: "dlg_obs"},
			want: "sha256:2e84c38984918b03ba1dd3ca6dd55d205185324b716946ef07a617ad9f461c44",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizedWatchConfigHash(tc.args); got != tc.want {
				t.Errorf("normalizedWatchConfigHash = %q, want the pre-move hash %q", got, tc.want)
			}
		})
	}
}

// A registry row written before the snapshot carried a receiver still folds — to
// an empty receiver, which is the truth about it, not to a dropped watch.
func TestFoldWatchesToleratesRegistryRowWithoutConfigSnapshot(t *testing.T) {
	t.Parallel()
	events := []jobstore.Event{{
		Seq:     1,
		Kind:    jobstore.EventWatchRegistered,
		WatchID: "w1",
		Watch: &jobstore.WatchEvent{
			Generation:       "g1",
			OwnerSessionID:   testOwnerSessionID,
			VisibleSessionID: testOwnerSessionID,
			Target:           "job_x",
			ConfigHash:       "sha256:old",
		},
	}}
	w := jobstore.FoldWatches(events)["w1"]
	if w == nil {
		t.Fatalf("watch dropped from the fold of a snapshot-less row")
	}
	if w.ReceiverSessionID != "" || w.ReceiverDelegateID != "" {
		t.Errorf("legacy row receiver = %q/%q, want both empty", w.ReceiverSessionID, w.ReceiverDelegateID)
	}
}
