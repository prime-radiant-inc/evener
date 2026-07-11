//go:build serffuzz

package agent

import (
	"errors"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
)

// FuzzWatchGrantLifecycleProgram exercises the durable capability that lets an
// observer read its parent's watched job. The fixture uses only test-owned job
// stores and metadata files; it does not start a process or contact a provider.
//
// Oracles:
//   - a granted view is snapshot-safe and reads the same retained output as its
//     owner, while all ungranted views fail closed;
//   - create and fire-time grants deduplicate by observer-session/job pair; and
//   - the best-effort worker metadata link is idempotent and never changes the
//     durable grant's authority.
func FuzzWatchGrantLifecycleProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0, 0, 0},
		{1, 2, 3, 4},
		{255, 254, 253, 252},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		observer := watchGrantFuzzText(data, "observer")
		fx := newGrantReadFixture(t)

		view, ok := fx.parent.lookupGrantedJobRead("child_job_obs", fx.watched)
		if !ok || view == nil || view.record == nil {
			t.Fatal("granted observer could not resolve watched job")
		}
		if view.record.JobID != fx.watched || view.record.Status != jobstore.StatusCompleted {
			t.Fatalf("granted record = %+v", view.record)
		}
		view.record.Reason = "mutated"
		next, ok := fx.parent.lookupGrantedJobRead("child_job_obs", fx.watched)
		if !ok || next.record.Reason == "mutated" {
			t.Fatalf("granted record aliases parent state: %+v", next)
		}

		content, total, dropped, truncated, err := view.readWindow(256, len(data)%2 == 0)
		if err != nil || total != int64(len(grantReadWatchedOutput)) || dropped != 0 || truncated || !strings.Contains(content, "bravo ready") {
			t.Fatalf("granted output window = (%q, %d, %d, %v, %v)", content, total, dropped, truncated, err)
		}
		matches, err := view.grepOutput(regexp.MustCompile("ready"))
		if err != nil || len(matches) != 1 || !strings.Contains(matches[0].Line, "bravo ready") {
			t.Fatalf("granted grep = (%+v, %v)", matches, err)
		}
		for _, request := range [][2]string{
			{"", fx.watched},
			{"child_job_obs", "job_missing"},
			{"ungranted", fx.watched},
		} {
			if got, ok := fx.parent.lookupGrantedJobRead(request[0], request[1]); ok || got != nil {
				t.Fatalf("ungranted lookup(%q, %q) = (%+v, %v)", request[0], request[1], got, ok)
			}
		}
		var nilSession *Session
		if got, ok := nilSession.lookupGrantedJobRead("child_job_obs", fx.watched); ok || got != nil {
			t.Fatalf("nil-session lookup = (%+v, %v)", got, ok)
		}

		jm := fx.parentJM
		seedWatchSendDelegateTarget(t, jm, "dlg_grant_extra")
		childID, grantable, err := jm.watchReadGrantObserver("dlg_grant_extra")
		if err != nil || !grantable || childID != "child_job_grant_extra" {
			t.Fatalf("grant observer = (%q, %v, %v)", childID, grantable, err)
		}
		if childID, grantable, err := jm.watchReadGrantObserver("caller"); err != nil || grantable || childID != "" {
			t.Fatalf("non-delegate grant observer = (%q, %v, %v)", childID, grantable, err)
		}
		if childID, grantable, err := jm.watchReadGrantObserver("dlg_missing"); err != nil || grantable || childID != "" {
			t.Fatalf("missing grant observer = (%q, %v, %v)", childID, grantable, err)
		}

		var signals atomic.Int32
		workerSessionID := "worker_" + observer
		workerJobID := seedRunningDelegate(t, jm, encodeRef("", workerSessionID), &signals)
		if got, found := jm.watchedWorkerSessionID(workerJobID); !found || got != workerSessionID {
			t.Fatalf("watched worker = (%q, %v), want (%q, true)", got, found, workerSessionID)
		}
		if got, found := jm.watchedWorkerSessionID(fx.watched); found || got != "" {
			t.Fatalf("shell job resolved worker = (%q, %v)", got, found)
		}

		stateDir := stateDirForJM(jm)
		if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: workerSessionID, IsSubagent: true}); err != nil {
			t.Fatalf("seed worker meta: %v", err)
		}
		jm.recordObserverLink(workerJobID, observer)
		if err := jm.stampObservedBy(workerSessionID, observer); err != nil {
			t.Fatalf("deduplicated observer stamp: %v", err)
		}
		meta, err := schema.LoadSessionMeta(stateDir, workerSessionID)
		if err != nil || len(meta.ObservedBy) != 1 || meta.ObservedBy[0] != observer {
			t.Fatalf("worker observed-by = (%+v, %v)", meta.ObservedBy, err)
		}
		if err := jm.stampObservedBy("", observer); err != nil {
			t.Fatalf("empty worker stamp: %v", err)
		}
		if err := jm.stampObservedBy(workerSessionID, ""); err != nil {
			t.Fatalf("empty observer stamp: %v", err)
		}
		if err := jm.stampObservedBy("missing-worker", observer); err == nil {
			t.Fatal("missing worker metadata stamp succeeded")
		}
		if err := (&jobManager{}).stampObservedBy(workerSessionID, observer); err != nil {
			t.Fatalf("empty state-dir stamp: %v", err)
		}
		crossProjectWorker := seedRunningDelegate(t, jm, encodeRef("other-project", "cross-worker"), &signals)
		if got, found := jm.watchedWorkerSessionID(crossProjectWorker); found || got != "" {
			t.Fatalf("cross-project worker resolved = (%q, %v)", got, found)
		}

		cfg, err := newWatchConfig(watchArgs{
			Target: workerJobID,
			Send:   &watchSendArgs{To: "dlg_grant_extra", Message: "observe"},
		}, jm.now())
		if err != nil {
			t.Fatalf("new grant watch config: %v", err)
		}
		if err := jm.mintWatchCreateReadGrant(cfg); err != nil {
			t.Fatalf("mint create grant: %v", err)
		}
		jm.mintWatchSendReadGrant(cfg, "dlg_grant_extra", workerJobID)
		jm.mintWatchSendReadGrant(cfg, "dlg_grant_extra", workerJobID)
		grants := loadGrantTable(t, jm)
		if !grants["child_job_grant_extra"][workerJobID] {
			t.Fatalf("grant table missing observer/job pair: %+v", grants)
		}
		if got := watchGrantEventCount(t, jm, "child_job_grant_extra", workerJobID); got != 1 {
			t.Fatalf("duplicate grant events = %d, want 1", got)
		}
		jm.mintWatchSendReadGrant(cfg, runtimeMessageAliasCaller, workerJobID)
		jm.mintWatchSendReadGrant(cfg, "dlg_grant_extra", runtimeMessageAliasCaller)
		if got := watchGrantEventCount(t, jm, "child_job_grant_extra", workerJobID); got != 1 {
			t.Fatalf("non-grantable send changed grant event count to %d", got)
		}

		receiverCfg, err := newWatchConfig(watchArgs{
			Target:            "*",
			ReceiverSessionID: observer,
			Send:              &watchSendArgs{To: "dlg_missing", Message: "direct receiver"},
		}, jm.now())
		if err != nil {
			t.Fatalf("new direct receiver config: %v", err)
		}
		jm.mintWatchSendReadGrant(receiverCfg, "dlg_missing", workerJobID)
		if !loadGrantTable(t, jm)[observer][workerJobID] || receiverCfg.grantsMinted == nil {
			t.Fatalf("receiver-keyed grant did not mint: grants=%+v cfg=%+v", loadGrantTable(t, jm), receiverCfg.grantsMinted)
		}

		unresolvedCfg, err := newWatchConfig(watchArgs{
			Target: "*",
			Send:   &watchSendArgs{To: "dlg_missing", Message: "missing receiver"},
		}, jm.now())
		if err != nil {
			t.Fatalf("new unresolved receiver config: %v", err)
		}
		jm.mintWatchSendReadGrant(unresolvedCfg, "dlg_missing", workerJobID)
		if unresolvedCfg.grantsMinted != nil {
			t.Fatalf("unresolved receiver marked grants minted: %+v", unresolvedCfg.grantsMinted)
		}

		var notifications []jobNotification
		jm.enqueue = func(n jobNotification) { notifications = append(notifications, n) }
		originalAppend := jm.appendEvent
		grantFailure := errors.New("fuzz grant append failure")
		jm.appendEvent = func(event jobstore.Event) error {
			if event.Kind == jobstore.EventWatchReadGrant {
				return grantFailure
			}
			return originalAppend(event)
		}
		failureCfg, err := newWatchConfig(watchArgs{
			Target:            "*",
			ReceiverSessionID: "failed-" + observer,
			Send:              &watchSendArgs{To: "dlg_grant_extra", Message: "failure"},
		}, jm.now())
		if err != nil {
			t.Fatalf("new failure config: %v", err)
		}
		jm.mintWatchSendReadGrant(failureCfg, "dlg_grant_extra", workerJobID)
		jm.appendEvent = originalAppend
		if failureCfg.grantsMinted != nil {
			t.Fatalf("failed grant poisoned dedup set: %+v", failureCfg.grantsMinted)
		}
		if len(notifications) != 1 || !strings.Contains(notifications[0].Reason, grantFailure.Error()) {
			t.Fatalf("grant failure notifications = %+v", notifications)
		}
		jm.mintWatchSendReadGrant(failureCfg, "dlg_grant_extra", workerJobID)
		if !loadGrantTable(t, jm)["failed-"+observer][workerJobID] {
			t.Fatalf("grant did not recover after append failure: %+v", loadGrantTable(t, jm))
		}

		closed := newGrantReadFixture(t)
		if err := closed.parentJM.store.Close(); err != nil {
			t.Fatalf("close granted parent store: %v", err)
		}
		if got, ok := closed.parent.lookupGrantedJobRead("child_job_obs", closed.watched); ok || got != nil {
			t.Fatalf("closed-store lookup = (%+v, %v)", got, ok)
		}
	})
}

func watchGrantFuzzText(data []byte, fallback string) string {
	if len(data) == 0 {
		return fallback
	}
	var b strings.Builder
	for _, value := range data {
		b.WriteByte('a' + value%26)
		if b.Len() == 32 {
			break
		}
	}
	return b.String()
}

func watchGrantEventCount(t *testing.T, jm *jobManager, observerSessionID, jobID string) int {
	t.Helper()
	events, err := jm.store.LoadEvents()
	if err != nil {
		t.Fatalf("load grant events: %v", err)
	}
	count := 0
	for _, event := range events {
		if event.Kind == jobstore.EventWatchReadGrant && event.ObserverSessionID == observerSessionID && event.JobID == jobID {
			count++
		}
	}
	return count
}
