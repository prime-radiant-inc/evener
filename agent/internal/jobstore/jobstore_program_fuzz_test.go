//go:build serffuzz

package jobstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/fuzz/fault"
)

// FuzzJobstoreLifecycleProgram drives the real job event store, retained output
// store, metadata recovery, output matcher, reconciliation, and forensic reader
// through bounded temp-directory and in-memory filesystem fixtures. A dedicated
// 0xff seed adds a deterministic error and fault matrix at the afero filesystem
// boundary; it exercises real rollback and metadata cleanup code without a
// provider, network, shell, Git, or ambient filesystem dependency.
func FuzzJobstoreLifecycleProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0x00},
		{0x14, 0x27, 0x41, 0x63},
		{0xff, 0x11, 0x72, 0x00, 0x5a},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		r := jcpReader{data: program}
		mode := r.next()
		root := t.TempDir()

		jcpStoreLifecycle(t, root, &r)
		jcpOutputLifecycle(t, root, &r)
		jcpWatchAndUtilityPaths(t, &r)
		if mode == 0xff {
			jcpStoreRecoveryPaths(t, &r)
			jcpOutputMetadataPaths(t, root, &r)
			jcpFaultPaths(t, &r)
			jcpFoldEdgePaths(t, &r)
			jcpWatchErrorPaths(t)
			jcpReadErrorPaths(t, root)
			jcpOutputErrorPaths(t, root)
			jcpStoreErrorPaths(t, root, &r)
		}
	})
}

type jcpReader struct {
	data []byte
	pos  int
}

func (r *jcpReader) next() byte {
	if len(r.data) == 0 {
		return 0
	}
	b := r.data[r.pos%len(r.data)]
	r.pos++
	return b
}

func jcpTime(b byte) time.Time {
	return time.Unix(1_700_000_000+int64(b), 0).UTC()
}

func jcpStarted(jobID string, b byte) Event {
	started := jcpTime(b)
	return Event{
		Kind:             EventJobStarted,
		TS:               started,
		JobID:            jobID,
		Type:             JobShell,
		Command:          "echo jobstore",
		Description:      "jobstore fuzz fixture",
		OwnerSessionID:   "owner",
		VisibleToSession: "visible",
		StartedAt:        &started,
		OutputPath:       "/jobs/" + jobID + ".log",
		WorkingDir:       "/fixture/work",
		Provenance:       provenance.WithWatch(nil, "watch-fixture", "wg-fixture", "wd-fixture", "visible", jobID),
	}
}

func jcpStoreLifecycle(t *testing.T, root string, r *jcpReader) {
	t.Helper()
	fs := afero.NewMemMapFs()
	const path = "/jobs.jsonl"
	s, err := openFs(fs, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	s.disableSync = true

	started := jcpStarted("job-primary", r.next())
	started.DelegateID = "dlg-primary"
	finishedAt := jcpTime(r.next())
	valid := true
	events := []Event{
		started,
		{Kind: EventJobSessionAssigned, TS: jcpTime(r.next()), JobID: started.JobID, TranscriptRef: "local:job-primary", Resumable: &valid},
		{Kind: EventDelegateCreated, TS: jcpTime(r.next()), JobID: started.JobID, DelegateID: "dlg-primary", Delegate: &DelegateEvent{ChildSessionID: "child", TranscriptRef: "local:child", OwnerSessionID: "owner", VisibleSessionID: "visible", Generation: "dg-primary", Resumable: true}},
		{Kind: EventWatchRegistered, TS: jcpTime(r.next()), WatchID: "watch-primary", Watch: &WatchEvent{Generation: "wg-primary", OwnerSessionID: "owner", VisibleSessionID: "visible", Target: started.JobID, SendTo: "visible", ConfigHash: "cfg", Condition: "output", Deliveries: 1}},
		{Kind: EventWatchSendPending, TS: jcpTime(r.next()), JobID: started.JobID, WatchSend: &WatchSendState{Key: WatchSendKey{VisibleSessionID: "visible", WatchID: "watch-primary", WatchTarget: started.JobID, ResolvedWatchedIdentity: started.JobID, ResolvedSendTo: "visible", WatchGeneration: "wg-primary"}, DeliveryID: "wd-primary", UpdateSeq: 1, Message: "ready", CreatedAt: jcpTime(r.next()), Provenance: provenance.WithWatch(nil, "watch-primary", "wg-primary", "wd-primary", "visible", started.JobID)}},
		{Kind: EventJobFinished, TS: jcpTime(r.next()), JobID: started.JobID, Status: StatusCompleted, Reason: "done", EndedAt: &finishedAt, OutputBytes: 23, TerminalGen: "terminal-primary", StructuredResult: map[string]any{"byte": int(r.next())}, StructuredResultValid: &valid},
		{Kind: EventJobNotificationPending, TS: jcpTime(r.next()), JobID: started.JobID, TerminalGen: "terminal-primary", Provenance: provenance.WithWatch(nil, "watch-primary", "wg-primary", "wd-primary", "visible", started.JobID)},
		{Kind: EventJobNotificationDelivered, TS: jcpTime(r.next()), JobID: started.JobID, TerminalGen: "terminal-primary"},
		{Kind: EventWatchSendDelivered, TS: jcpTime(r.next()), JobID: started.JobID, WatchSend: &WatchSendState{Key: WatchSendKey{VisibleSessionID: "visible", WatchID: "watch-primary", WatchTarget: started.JobID, ResolvedWatchedIdentity: started.JobID, ResolvedSendTo: "visible", WatchGeneration: "wg-primary"}, DeliveryID: "wd-primary", UpdateSeq: 2}},
		{Kind: EventWatchCleared, TS: jcpTime(r.next()), WatchID: "watch-primary", Watch: &WatchEvent{Generation: "wg-primary", EndReason: "done"}},
		{Kind: EventDelegateStopGateClosed, TS: jcpTime(r.next()), DelegateID: "dlg-primary", Delegate: &DelegateEvent{Generation: "dg-primary", StopJobID: started.JobID}},
		{Kind: EventDelegateDisposed, TS: jcpTime(r.next()), DelegateID: "dlg-primary", Delegate: &DelegateEvent{}},
	}
	if err := s.Append(events[0]); err != nil {
		t.Fatalf("Append started: %v", err)
	}
	if err := s.AppendBatch(events[1:]); err != nil {
		t.Fatalf("AppendBatch lifecycle: %v", err)
	}
	if err := s.AppendBatch(nil); err != nil {
		t.Fatalf("AppendBatch nil: %v", err)
	}

	records, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	record := records[started.JobID]
	if record == nil || record.Status != StatusCompleted || record.NotifyState != NotifyDelivered || !record.Disposed {
		t.Fatalf("folded record = %#v", record)
	}
	if key := record.DedupeKey(); key.JobID != started.JobID || key.VisibleSessionID != "visible" || key.TerminalGen != "terminal-primary" {
		t.Fatalf("DedupeKey = %#v", key)
	}
	if ShouldDeliver(record) {
		t.Fatalf("delivered record still reports ShouldDeliver")
	}
	if !ShouldDeliver(&JobRecord{NotifyState: NotifyPending}) {
		t.Fatalf("pending record does not report ShouldDeliver")
	}
	if !StatusCompleted.IsTerminal() || StatusRunning.IsTerminal() {
		t.Fatalf("Status.IsTerminal contract regressed")
	}

	ordered, err := s.LoadOrdered()
	if err != nil || len(ordered) != 1 || ordered[0].JobID != started.JobID {
		t.Fatalf("LoadOrdered = %#v, %v", ordered, err)
	}
	delegates, err := s.LoadDelegates()
	if err != nil || delegates["dlg-primary"] == nil || !delegates["dlg-primary"].StopGateClosed {
		t.Fatalf("LoadDelegates = %#v, %v", delegates, err)
	}
	watches, err := s.LoadWatches()
	if err != nil || watches["watch-primary"] == nil || watches["watch-primary"].Active {
		t.Fatalf("LoadWatches = %#v, %v", watches, err)
	}
	sends, err := s.LoadWatchSends()
	if err != nil || len(sends.Pending) != 0 {
		t.Fatalf("LoadWatchSends = %#v, %v", sends, err)
	}
	loaded, err := s.LoadEvents()
	if err != nil || len(loaded) != len(events) {
		t.Fatalf("LoadEvents = %d/%v, want %d", len(loaded), err, len(events))
	}
	if _, err := s.readAll(); err != nil {
		t.Fatalf("readAll: %v", err)
	}

	lost := Reconcile(map[string]*JobRecord{
		"live": {JobID: "live", Status: StatusRunning},
		"lost": {JobID: "lost", Status: StatusRunning},
		"done": {JobID: "done", Status: StatusCompleted},
	}, map[string]bool{"live": true}, jcpTime(r.next()))
	if len(lost) != 1 || lost[0].JobID != "lost" || lost[0].Status != StatusStopped || lost[0].TerminalGen == "" {
		t.Fatalf("Reconcile = %#v", lost)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}
	for _, operation := range []func() error{
		func() error { return s.Append(Event{}) },
		func() error { return s.AppendBatch([]Event{{}}) },
		func() error { _, err := s.Load(); return err },
		func() error { _, err := s.LoadOrdered(); return err },
		func() error { _, err := s.LoadDelegates(); return err },
		func() error { _, err := s.LoadWatches(); return err },
		func() error { _, err := s.LoadWatchSends(); return err },
		func() error { _, err := s.LoadEvents(); return err },
		func() error { _, err := s.readAll(); return err },
	} {
		if err := operation(); !errors.Is(err, ErrStoreClosed) {
			t.Fatalf("closed store operation error = %v, want ErrStoreClosed", err)
		}
	}

	publicPath := filepath.Join(root, "public-jobs.jsonl")
	public, err := OpenNoSync(publicPath)
	if err != nil {
		t.Fatalf("OpenNoSync: %v", err)
	}
	if err := public.Append(jcpStarted("job-public", r.next())); err != nil {
		t.Fatalf("public Append: %v", err)
	}
	if err := public.Close(); err != nil {
		t.Fatalf("public Close: %v", err)
	}
}

func jcpOutputLifecycle(t *testing.T, root string, r *jcpReader) {
	t.Helper()
	fs := afero.NewMemMapFs()
	const path = "/output.log"
	o, err := openOutputFsNoSync(fs, path, 0)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	data := []byte(fmt.Sprintf("first-%02x\nready\nlast\n", r.next()))
	if n, err := o.Append(data); err != nil || n != len(data) {
		t.Fatalf("Append output = %d/%v", n, err)
	}
	if o.Len() != int64(len(data)) || o.RetainedStart() != 0 {
		t.Fatalf("output counters = %d/%d", o.Len(), o.RetainedStart())
	}
	if _, _, _, err := o.Tail(-1); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("Tail negative = %v", err)
	}
	if _, _, _, err := o.Head(-1); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("Head negative = %v", err)
	}
	tail, total, truncated, err := o.Tail(6)
	if err != nil || total != int64(len(data)) || !truncated || !bytes.Equal(tail, data[len(data)-6:]) {
		t.Fatalf("Tail = %q/%d/%v/%v", tail, total, truncated, err)
	}
	head, total, truncated, err := o.Head(5)
	if err != nil || total != int64(len(data)) || !truncated || !bytes.Equal(head, data[:5]) {
		t.Fatalf("Head = %q/%d/%v/%v", head, total, truncated, err)
	}
	re := regexp.MustCompile(`ready`)
	for _, call := range []func() ([]Match, error){
		func() ([]Match, error) { return o.Grep(re, 128) },
		func() ([]Match, error) { return o.GrepLimit(re, 128, 1) },
		func() ([]Match, error) { return o.GrepLimitLineBytes(re, 128, 0, 128) },
	} {
		matches, err := call()
		if err != nil || len(matches) != 1 || matches[0].Line != "ready" {
			t.Fatalf("Grep = %#v/%v", matches, err)
		}
	}
	if _, err := o.GrepLimitLineBytes(re, -1, 0, 8); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("Grep negative = %v", err)
	}
	if matches, err := o.GrepLimitLineBytes(re, 0, 0, 8); err != nil || matches != nil {
		t.Fatalf("Grep zero = %#v/%v", matches, err)
	}
	if err := o.Close(); err != nil {
		t.Fatalf("Close output: %v", err)
	}

	pruned, err := openOutputFsNoSync(fs, "/pruned.log", 8)
	if err != nil {
		t.Fatalf("open pruned output: %v", err)
	}
	lifetime := []byte("0123456789abcdef")
	if _, err := pruned.Append(lifetime); err != nil {
		t.Fatalf("append pruned output: %v", err)
	}
	if pruned.Len() != int64(len(lifetime)) || pruned.RetainedStart() != int64(len(lifetime)-8) {
		t.Fatalf("pruned counters = %d/%d", pruned.Len(), pruned.RetainedStart())
	}
	if got, _, wasTruncated, err := pruned.Head(8); err != nil || !wasTruncated || string(got) != "89abcdef" {
		t.Fatalf("pruned Head = %q/%v/%v", got, wasTruncated, err)
	}
	if err := pruned.Close(); err != nil {
		t.Fatalf("close pruned output: %v", err)
	}

	missing, err := openOutputFsNoSync(fs, "/missing.log", 0)
	if err != nil {
		t.Fatalf("open removable output: %v", err)
	}
	if _, err := missing.Append([]byte("gone\n")); err != nil {
		t.Fatalf("append removable output: %v", err)
	}
	if err := fs.Remove("/missing.log"); err != nil {
		t.Fatalf("remove output backing file: %v", err)
	}
	if _, _, _, err := missing.Tail(8); err == nil {
		t.Fatal("Tail after remove succeeded")
	}
	if _, _, _, err := missing.Head(8); err == nil {
		t.Fatal("Head after remove succeeded")
	}
	if _, err := missing.Grep(re, 8); err == nil {
		t.Fatal("Grep after remove succeeded")
	}
	_ = missing.Close()

	publicPath := filepath.Join(root, "public-output.log")
	public, err := OpenOutputNoSync(publicPath, 0)
	if err != nil {
		t.Fatalf("OpenOutputNoSync: %v", err)
	}
	if _, err := public.Append([]byte("public ready\n")); err != nil {
		t.Fatalf("public output append: %v", err)
	}
	if err := public.Close(); err != nil {
		t.Fatalf("public output close: %v", err)
	}
	if total, start, err := OutputFileStats(publicPath); err != nil || total != int64(len("public ready\n")) || start != 0 {
		t.Fatalf("OutputFileStats = %d/%d/%v", total, start, err)
	}
	for _, call := range []func() ([]Match, error){
		func() ([]Match, error) { return GrepFileLimit(publicPath, regexp.MustCompile(`public`), 128, 0, 128) },
		func() ([]Match, error) {
			return GrepFileLimitAt(publicPath, regexp.MustCompile(`public`), 128, 0, 128, 10)
		},
	} {
		matches, err := call()
		if err != nil || len(matches) != 1 {
			t.Fatalf("public grep = %#v/%v", matches, err)
		}
	}
	if _, err := GrepFileLimit(publicPath, re, -1, 0, 8); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("GrepFileLimit negative = %v", err)
	}
	if matches, err := GrepFileLimit(publicPath, re, 0, 0, 8); err != nil || matches != nil {
		t.Fatalf("GrepFileLimit zero = %#v/%v", matches, err)
	}
	if err := RemoveOutputArtifacts(publicPath); err != nil {
		t.Fatalf("RemoveOutputArtifacts: %v", err)
	}
	if err := RemoveOutputArtifacts(publicPath); err != nil {
		t.Fatalf("second RemoveOutputArtifacts: %v", err)
	}

	syncedPath := filepath.Join(root, "synced-output.log")
	synced, err := OpenOutput(syncedPath, 0)
	if err != nil {
		t.Fatalf("OpenOutput: %v", err)
	}
	if _, err := synced.Append([]byte("sync\n")); err != nil {
		t.Fatalf("synced append: %v", err)
	}
	if err := synced.Close(); err != nil {
		t.Fatalf("synced close: %v", err)
	}
}

func jcpWatchAndUtilityPaths(t *testing.T, r *jcpReader) {
	t.Helper()
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	if m.Regexp().String() != "ready" {
		t.Fatalf("matcher Regexp = %q", m.Regexp())
	}
	m.SetScanOffset(4)
	if matches := m.FeedAt([]byte("skip\nready\n"), int64(len("skip\nready\n"))); len(matches) != 1 || matches[0] != "ready" {
		t.Fatalf("FeedAt scan offset = %#v", matches)
	}
	m.SetScanOffset(0)
	m.SeedCarry([]byte("rea"))
	p := provenance.WithWatch(nil, "watch", "generation", "delivery", "session", "job")
	matches := m.FeedAtWithProvenance([]byte("dy\nignored\n"), int64(len("dy\nignored\n")), p)
	if len(matches) != 1 || matches[0].Line != "ready" || matches[0].Provenance == nil {
		t.Fatalf("FeedAtWithProvenance = %#v", matches)
	}
	m.Feed([]byte("ready"))
	if flushed := m.FlushWithProvenance(p); len(flushed) != 1 || flushed[0].Line != "ready" {
		t.Fatalf("FlushWithProvenance = %#v", flushed)
	}
	if got := m.Flush(); got != nil {
		t.Fatalf("second Flush = %#v", got)
	}
	if line, matched := NewOutputMatcher(regexp.MustCompile(`ready`)).ScanRetained([]byte("ready\nno\nready\npartial")); !matched || line != "ready" {
		t.Fatalf("ScanRetained = %q/%v", line, matched)
	}
	if carry, overlong := appendLineFragment(nil, false, bytes.Repeat([]byte("x"), maxOutputMatcherLineBytes+1)); carry != nil || !overlong {
		t.Fatalf("appendLineFragment overlong = %q/%v", carry, overlong)
	}
	if string(completedLine([]byte("line\r"))) != "line" {
		t.Fatalf("completedLine did not trim CR")
	}

	ids := []struct {
		prefix string
		value  string
	}{
		{"job_", NewJobID("02wMz5TxvEMoJEDTDGOTil")},
		{"dlg_", NewDelegateID()},
		{"dg_", NewDelegateGeneration()},
		{"watch_", NewWatchID()},
		{"wg_", NewWatchGeneration()},
		{"wd_", NewWatchSendDeliveryID()},
		{"", NewTerminalGeneration()},
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !strings.HasPrefix(id.value, id.prefix) || id.value == "" || seen[id.value] {
			t.Fatalf("generated identifier = %q, prefix %q", id.value, id.prefix)
		}
		seen[id.value] = true
	}
	_ = r.next()
}

func jcpFoldEdgePaths(t *testing.T, r *jcpReader) {
	t.Helper()
	valid := true
	started := jcpStarted("delegate-job", r.next())
	started.DelegateID = "delegate-edge"
	started.Seq = 1
	finished := Event{Kind: EventJobFinished, Seq: 3, JobID: started.JobID, Status: StatusCompleted, TerminalGen: "terminal-edge", StructuredResultValid: &valid}
	delegates := FoldDelegates([]Event{
		{Kind: EventDelegateCreated, Seq: 0, DelegateID: "delegate-fresh", Delegate: &DelegateEvent{Generation: "fresh"}},
		{Kind: EventDelegateCreated, Seq: 0},
		{Kind: EventJobStarted, Seq: 0, JobID: "no-delegate"},
		started,
		{Kind: EventDelegateCreated, Seq: 2, DelegateID: "delegate-edge", Delegate: &DelegateEvent{Generation: "generation-edge", Resumable: false}},
		{Kind: EventJobSessionAssigned, Seq: 2, JobID: "missing", Resumable: &valid},
		{Kind: EventJobSessionAssigned, Seq: 2, JobID: started.JobID, Resumable: new(bool)},
		{Kind: EventDelegateStopGateClosed, Seq: 2, DelegateID: ""},
		{Kind: EventDelegateStopGateClosed, Seq: 2, DelegateID: "delegate-edge", Delegate: &DelegateEvent{StopJobID: "wrong-running"}},
		finished,
		{Kind: EventDelegateStopGateClosed, Seq: 4, DelegateID: "delegate-edge", Delegate: &DelegateEvent{StopJobID: "wrong-latest"}},
		{Kind: EventDelegateStopGateClosed, Seq: 5, DelegateID: "delegate-edge", Delegate: &DelegateEvent{Generation: "generation-edge", StopJobID: started.JobID}},
	})
	if got := delegates["delegate-edge"]; got == nil || !got.StopGateClosed || got.Status != DelegateNotResumable {
		t.Fatalf("FoldDelegates edge record = %#v", got)
	}

	watches := FoldWatches([]Event{
		{Kind: EventWatchRegistered, WatchID: "invalid", Watch: &WatchEvent{Generation: "", OwnerSessionID: "owner"}},
		{Kind: EventWatchCleared, WatchID: "missing", Watch: &WatchEvent{}},
		{Kind: EventWatchRegistered, WatchID: "watch-edge", Watch: &WatchEvent{Generation: "one", OwnerSessionID: "owner", VisibleSessionID: "visible", Target: "target", ConfigHash: "config"}},
		{Kind: EventWatchCleared, WatchID: "watch-edge", Watch: &WatchEvent{Generation: "wrong"}},
		{Kind: EventWatchCleared, WatchID: "watch-edge", Watch: &WatchEvent{Generation: "one", EndReason: "edge"}},
		{Kind: EventWatchCleared, WatchID: "watch-edge", Watch: &WatchEvent{Generation: "one"}},
	})
	if got := watches["watch-edge"]; got == nil || got.Active || got.EndReason != "edge" {
		t.Fatalf("FoldWatches edge record = %#v", got)
	}

	key := WatchSendKey{WatchID: "watch-edge", WatchTarget: "target"}
	sends := FoldWatchSends([]Event{
		{Kind: EventWatchSendPending, Seq: 1, WatchSend: &WatchSendState{Key: key, UpdateSeq: 2}},
		{Kind: EventWatchSendPending, Seq: 2, WatchSend: &WatchSendState{Key: key, UpdateSeq: 1}},
		{Kind: EventWatchSendDelivered, Seq: 3, WatchSend: &WatchSendState{Key: key, UpdateSeq: 2}},
		{Kind: EventWatchSendPending, Seq: 4, WatchSend: &WatchSendState{Key: key, UpdateSeq: 1}},
	})
	if len(sends.Pending) != 0 {
		t.Fatalf("FoldWatchSends retained stale state: %#v", sends)
	}

	records := Fold([]Event{
		{Kind: EventJobStarted, Seq: 1, JobID: "job-edge"},
		{Kind: EventJobFinished, Seq: 2, JobID: "job-edge", Status: StatusCompleted, TerminalGen: "terminal-edge", StructuredResultValid: new(bool), StructuredResultReason: "invalid"},
		{Kind: EventJobMessageSent, Seq: 3, JobID: "job-edge"},
		{Kind: EventJobNotificationPending, Seq: 4, JobID: "job-edge", TerminalGen: "wrong"},
		{Kind: EventJobNotificationDelivered, Seq: 5, JobID: "job-edge", TerminalGen: "wrong"},
		{Kind: EventJobFinished, Seq: 6, JobID: "job-edge", Status: StatusFailed, TerminalGen: "later"},
	})
	if got := records["job-edge"]; got == nil || got.Status != StatusCompleted || got.StructuredResultReason != "invalid" || got.NotifyState != NotifyNotArmed {
		t.Fatalf("Fold edge record = %#v", got)
	}
}

func jcpWatchErrorPaths(t *testing.T) {
	t.Helper()
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	if got := m.FeedAt(nil, 0); got != nil {
		t.Fatalf("empty FeedAt = %#v", got)
	}
	m.SeedCarry(bytes.Repeat([]byte("x"), maxOutputMatcherLineBytes+1))
	if got := m.FlushWithProvenance(nil); got != nil {
		t.Fatalf("overlong FlushWithProvenance = %#v", got)
	}
	m.SeedCarry([]byte("other"))
	if got := m.FlushWithProvenance(nil); got != nil {
		t.Fatalf("nonmatching FlushWithProvenance = %#v", got)
	}
	carry, overlong := appendLineFragment(nil, false, append(bytes.Repeat([]byte("x"), maxOutputMatcherLineBytes), '\r'))
	if overlong || len(carry) != maxOutputMatcherLineBytes+1 {
		t.Fatalf("CR-sized matcher carry = %d/%v", len(carry), overlong)
	}
}

func jcpReadErrorPaths(t *testing.T, root string) {
	t.Helper()
	if _, err := ReadEvents(root); err == nil {
		t.Fatal("ReadEvents accepted a directory")
	}
	path := filepath.Join(root, "blank-lines.jsonl")
	line := jcpEventLine(t, jcpStarted("read-edge", 0))
	data := append([]byte("\n \t\n"), line...)
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write blank-line forensic log: %v", err)
	}
	if events, err := ReadEvents(path); err != nil || len(events) != 1 {
		t.Fatalf("ReadEvents blank lines = %#v/%v", events, err)
	}
}

func jcpOutputErrorPaths(t *testing.T, root string) {
	t.Helper()
	meta := outputMeta{TotalBytes: 3, RetainedSHA256: outputBytesSHA256([]byte("abc"))}

	if _, err := openOutputFsNoSync(&jcpHookFS{Fs: afero.NewMemMapFs(), openFileErr: jcpInjectedErr}, "/open.log", 0); err == nil {
		t.Fatal("openOutputFsNoSync accepted an open failure")
	}
	if _, err := openOutputFsNoSync(&jcpHookFS{
		Fs:   afero.NewMemMapFs(),
		wrap: func(f afero.File) afero.File { return &jcpHookFile{File: f, statErr: jcpInjectedErr} },
	}, "/stat.log", 0); err == nil {
		t.Fatal("openOutputFsNoSync accepted a stat failure")
	}

	for _, operation := range []struct {
		label string
		call  func(*OutputStore) error
		setup func(*jcpHookFS)
	}{
		{"Tail stat", func(o *OutputStore) error { _, _, _, err := o.Tail(2); return err }, func(fs *jcpHookFS) { fs.statErr = jcpInjectedErr }},
		{"Tail open", func(o *OutputStore) error { _, _, _, err := o.Tail(2); return err }, func(fs *jcpHookFS) { fs.openErr = jcpInjectedErr }},
		{"Tail seek", func(o *OutputStore) error { _, _, _, err := o.Tail(2); return err }, func(fs *jcpHookFS) {
			fs.wrap = func(f afero.File) afero.File { return &jcpHookFile{File: f, seekErr: jcpInjectedErr} }
		}},
		{"Tail read", func(o *OutputStore) error { _, _, _, err := o.Tail(2); return err }, func(fs *jcpHookFS) {
			fs.wrap = func(f afero.File) afero.File { return &jcpHookFile{File: f, readErr: jcpInjectedErr} }
		}},
		{"Tail close", func(o *OutputStore) error { _, _, _, err := o.Tail(2); return err }, func(fs *jcpHookFS) {
			fs.wrap = func(f afero.File) afero.File { return &jcpHookFile{File: f, closeErr: jcpInjectedErr} }
		}},
		{"Head stat", func(o *OutputStore) error { _, _, _, err := o.Head(2); return err }, func(fs *jcpHookFS) { fs.statErr = jcpInjectedErr }},
		{"Head open", func(o *OutputStore) error { _, _, _, err := o.Head(2); return err }, func(fs *jcpHookFS) { fs.openErr = jcpInjectedErr }},
		{"Head read", func(o *OutputStore) error { _, _, _, err := o.Head(2); return err }, func(fs *jcpHookFS) {
			fs.wrap = func(f afero.File) afero.File { return &jcpHookFile{File: f, readErr: jcpInjectedErr} }
		}},
		{"Head close", func(o *OutputStore) error { _, _, _, err := o.Head(2); return err }, func(fs *jcpHookFS) {
			fs.wrap = func(f afero.File) afero.File { return &jcpHookFile{File: f, closeErr: jcpInjectedErr} }
		}},
		{"Grep open", func(o *OutputStore) error { _, err := o.Grep(regexp.MustCompile(`ready`), 32); return err }, func(fs *jcpHookFS) { fs.openErr = jcpInjectedErr }},
		{"Grep close", func(o *OutputStore) error { _, err := o.Grep(regexp.MustCompile(`ready`), 32); return err }, func(fs *jcpHookFS) {
			fs.wrap = func(f afero.File) afero.File { return &jcpHookFile{File: f, closeErr: jcpInjectedErr} }
		}},
	} {
		o := jcpReadOutput(t, []byte("ready\nlast\n"), operation.setup)
		jcpRequireError(t, operation.label, operation.call(o))
	}
	retainedTail := jcpReadOutput(t, []byte("ready\n"), func(*jcpHookFS) {})
	retainedTail.retainedStart = 1
	if _, _, truncated, err := retainedTail.Tail(32); err != nil || !truncated {
		t.Fatalf("retained Tail = %v/%v", truncated, err)
	}
	grepReadFailure := jcpReadOutput(t, []byte("ready\n"), func(fs *jcpHookFS) {
		fs.wrap = func(f afero.File) afero.File { return &jcpHookFile{File: f, readErr: jcpInjectedErr} }
	})
	if _, err := grepReadFailure.Grep(regexp.MustCompile(`ready`), 32); err == nil {
		t.Fatal("Grep accepted a reader failure")
	}

	appendBase := afero.NewMemMapFs()
	appendFile := jcpOpenAferoFile(t, appendBase, "/append.log")
	appendStore := &OutputStore{path: "/append.log", fs: appendBase, f: &jcpHookFile{File: appendFile, writeErr: jcpInjectedErr}, disableSync: true}
	if _, err := appendStore.Append([]byte("x")); err == nil {
		t.Fatal("Append accepted a write failure")
	}
	_ = appendFile.Close()

	pruneBase := afero.NewMemMapFs()
	pruneFile := jcpOpenAferoFile(t, pruneBase, "/append-prune.log")
	pruneStore := &OutputStore{path: "/append-prune.log", metaPath: "/append-prune.log.meta.json", fs: pruneBase, f: &jcpHookFile{File: pruneFile, statErr: jcpInjectedErr}, capBytes: 1, disableSync: true}
	if _, err := pruneStore.Append([]byte("xx")); err == nil {
		t.Fatal("Append accepted a prune stat failure")
	}
	_ = pruneFile.Close()

	for _, test := range []struct {
		label string
		file  *jcpHookFile
	}{
		{"prune stat", &jcpHookFile{statErr: jcpInjectedErr}},
		{"prune seek tail", &jcpHookFile{seekErr: jcpInjectedErr, seekFailAt: 1}},
		{"prune read tail", &jcpHookFile{readErr: jcpInjectedErr}},
		{"prune truncate", &jcpHookFile{truncateErr: jcpInjectedErr, truncateFailAt: 1}},
		{"prune seek rewrite", &jcpHookFile{seekErr: jcpInjectedErr, seekFailAt: 2}},
		{"prune rewrite", &jcpHookFile{writeErr: jcpInjectedErr}},
		{"prune short rewrite", &jcpHookFile{shortWrite: true}},
		{"prune trim", &jcpHookFile{truncateErr: jcpInjectedErr, truncateFailAt: 2}},
		{"prune seek eof", &jcpHookFile{seekErr: jcpInjectedErr, seekFailAt: 3}},
	} {
		jcpRequireError(t, test.label, jcpPruneWithHook(t, test.file))
	}

	persistBase := afero.NewMemMapFs()
	persistFile := jcpOpenAferoFile(t, persistBase, "/persist.log")
	if err := (&OutputStore{f: persistFile}).persistMetaLocked(); err != nil {
		t.Fatalf("empty metadata path = %v", err)
	}
	_ = persistFile.Close()

	syncBase := afero.NewMemMapFs()
	syncFile := jcpOpenAferoFile(t, syncBase, "/persist-sync.log")
	if err := (&OutputStore{path: "/persist-sync.log", metaPath: "/persist-sync.meta", fs: syncBase, f: &jcpHookFile{File: syncFile, syncErr: jcpInjectedErr}}).persistMetaLocked(); err == nil {
		t.Fatal("persistMetaLocked accepted a sync failure")
	}
	_ = syncFile.Close()

	hashBase := afero.NewMemMapFs()
	hashFile := jcpOpenAferoFile(t, hashBase, "/persist-hash.log")
	if err := (&OutputStore{path: "/persist-hash.log", metaPath: "/persist-hash.meta", fs: &jcpHookFS{Fs: hashBase, openErr: jcpInjectedErr}, f: hashFile, disableSync: true}).persistMetaLocked(); err == nil {
		t.Fatal("persistMetaLocked accepted a hash-open failure")
	}
	_ = hashFile.Close()

	removeBase := afero.NewMemMapFs()
	if err := afero.WriteFile(removeBase, "/persist-remove.log", []byte("x"), 0o644); err != nil {
		t.Fatalf("seed persist-remove: %v", err)
	}
	removeFile := jcpOpenAferoFile(t, removeBase, "/persist-remove.log")
	if err := (&OutputStore{path: "/persist-remove.log", metaPath: "/persist-remove.meta", fs: &jcpHookFS{Fs: removeBase, removeErr: jcpInjectedErr}, f: removeFile, total: 1, disableSync: true}).persistMetaLocked(); err == nil {
		t.Fatal("persistMetaLocked accepted a pending-remove failure")
	}
	_ = removeFile.Close()

	if err := writeOutputMetaFileFsSync(afero.NewMemMapFs(), "", meta, true); err != nil {
		t.Fatalf("empty metadata write = %v", err)
	}
	for _, test := range []struct {
		label string
		fs    afero.Fs
	}{
		{"metadata open", &jcpHookFS{Fs: afero.NewMemMapFs(), openFileErr: jcpInjectedErr}},
		{"metadata write", &jcpHookFS{Fs: afero.NewMemMapFs(), wrap: func(f afero.File) afero.File { return &jcpHookFile{File: f, writeErr: jcpInjectedErr} }}},
		{"metadata short write", &jcpHookFS{Fs: afero.NewMemMapFs(), wrap: func(f afero.File) afero.File { return &jcpHookFile{File: f, shortWrite: true} }}},
		{"metadata sync", &jcpHookFS{Fs: afero.NewMemMapFs(), wrap: func(f afero.File) afero.File { return &jcpHookFile{File: f, syncErr: jcpInjectedErr} }}},
		{"metadata close", &jcpHookFS{Fs: afero.NewMemMapFs(), wrap: func(f afero.File) afero.File { return &jcpHookFile{File: f, closeErr: jcpInjectedErr} }}},
		{"metadata replace", &jcpHookFS{Fs: afero.NewMemMapFs(), renameErr: jcpInjectedErr}},
		{"metadata directory open", &jcpHookFS{Fs: afero.NewMemMapFs(), openErr: jcpInjectedErr}},
	} {
		jcpRequireError(t, test.label, writeOutputMetaFileFsSync(test.fs, "/metadata.json", meta, true))
	}
	if err := syncParentDir(&jcpHookFS{
		Fs:   afero.NewMemMapFs(),
		wrap: func(f afero.File) afero.File { return &jcpHookFile{File: f, syncErr: jcpInjectedErr} },
	}, "/metadata.json"); err == nil {
		t.Fatal("syncParentDir accepted a sync failure")
	}

	if _, ok, err := readOutputMeta(afero.NewMemMapFs(), "/missing.meta"); err != nil || ok {
		t.Fatalf("missing metadata = %v/%v", ok, err)
	}
	if _, _, err := readOutputMeta(&jcpHookFS{Fs: afero.NewMemMapFs(), openErr: jcpInjectedErr}, "/read.meta"); err == nil {
		t.Fatal("readOutputMeta accepted a read failure")
	}
	badMetaFS := afero.NewMemMapFs()
	if err := afero.WriteFile(badMetaFS, "/read.meta", []byte("not json"), 0o644); err != nil {
		t.Fatalf("seed malformed metadata: %v", err)
	}
	if _, _, err := readOutputMeta(badMetaFS, "/read.meta"); err == nil {
		t.Fatal("readOutputMeta accepted malformed JSON")
	}

	for _, metaCase := range []outputMeta{
		{TotalBytes: 2, RetainedSHA256: "bad"},
		{TotalBytes: 4, RetainedSHA256: outputBytesSHA256([]byte("abc"))},
		{TotalBytes: 3, RetainedSHA256: "bad"},
	} {
		fs := afero.NewMemMapFs()
		if err := afero.WriteFile(fs, "/validate.log", []byte("abc"), 0o644); err != nil {
			t.Fatalf("seed validation output: %v", err)
		}
		if err := writeOutputMetaFileFs(fs, "/validate.meta", metaCase); err != nil {
			t.Fatalf("seed validation metadata: %v", err)
		}
		if _, _, err := readValidOutputMetaFs(fs, "/validate.meta", "/validate.log", 3); err == nil {
			t.Fatal("readValidOutputMetaFs accepted inconsistent metadata")
		}
	}

	pendingFS, pendingPath, finalPath, outputPath := jcpPendingScenario(t)
	if got, ok, err := readValidPendingOutputMeta(pendingFS, pendingPath, finalPath, outputPath, 10); err != nil || !ok || got.RetainedStart != 10 {
		t.Fatalf("pending recovery = %#v/%v/%v", got, ok, err)
	}
	if err := writeOutputMetaFileFs(pendingFS, pendingPath, outputMeta{TotalBytes: 20, RetainedStart: 14, RetainedSHA256: "bad"}); err != nil {
		t.Fatalf("rewrite pending mismatch: %v", err)
	}
	if _, _, err := readValidPendingOutputMeta(pendingFS, pendingPath, finalPath, outputPath, 10); err == nil {
		t.Fatal("pending metadata accepted a suffix mismatch")
	}
	exactPending := afero.NewMemMapFs()
	if err := afero.WriteFile(exactPending, "/pending.log", []byte("abc"), 0o644); err != nil {
		t.Fatalf("seed exact pending output: %v", err)
	}
	if err := writeOutputMetaFileFs(exactPending, "/pending.meta", outputMeta{TotalBytes: 3, RetainedSHA256: "bad"}); err != nil {
		t.Fatalf("seed exact pending metadata: %v", err)
	}
	if _, _, err := readValidPendingOutputMeta(exactPending, "/pending.meta", "/final.meta", "/pending.log", 3); err == nil {
		t.Fatal("pending metadata accepted a full hash mismatch")
	}

	if _, err := outputFileHasPrefixSHA256(afero.NewMemMapFs(), "/missing.log", 1, ""); err == nil {
		t.Fatal("prefix hash accepted a missing output")
	}
	hashCheckFS := afero.NewMemMapFs()
	if err := afero.WriteFile(hashCheckFS, "/hash.log", []byte("abc"), 0o644); err != nil {
		t.Fatalf("seed hash output: %v", err)
	}
	if _, err := outputFileHasPrefixSHA256(hashCheckFS, "/hash.log", 4, ""); err == nil {
		t.Fatal("prefix hash accepted a short output")
	}
	if ok, err := outputFileHasPrefixSHA256(hashCheckFS, "/hash.log", 2, "bad"); err != nil || ok {
		t.Fatalf("prefix mismatch = %v/%v", ok, err)
	}
	if _, err := outputFileHasSuffixSHA256(&jcpHookFS{
		Fs:   hashCheckFS,
		wrap: func(f afero.File) afero.File { return &jcpHookFile{File: f, seekErr: jcpInjectedErr} },
	}, "/hash.log", 1, 2, ""); err == nil {
		t.Fatal("suffix hash accepted a seek failure")
	}
	if _, err := outputFileSHA256(&jcpHookFS{
		Fs:   hashCheckFS,
		wrap: func(f afero.File) afero.File { return &jcpHookFile{File: f, readErr: jcpInjectedErr} },
	}, "/hash.log"); err == nil {
		t.Fatal("full hash accepted a read failure")
	}

	if _, err := GrepFileLimitAt(filepath.Join(root, "missing.log"), regexp.MustCompile(`ready`), 4, 0, 4, 0); err == nil {
		t.Fatal("GrepFileLimitAt accepted a missing file")
	}
	if _, err := grepReaderLimit(bufio.NewReader(jcpErrorReader{}), regexp.MustCompile(`ready`), 16, 0, 16); err == nil {
		t.Fatal("grepReaderLimit accepted a reader failure")
	}
	_, _ = grepReaderLimit(bufio.NewReader(strings.NewReader(strings.Repeat("x", 5000)+"\nready\r\npartial")), regexp.MustCompile(`ready`), 7000, 1, 6000)
	_ = logicalLineContentLen(nil, nil)
	_ = logicalLineContentLen(nil, []byte("x\r"))
	_ = logicalLineContentLen(nil, []byte("x"))
	_ = logicalLineContentLen(nil, []byte("x\r\n"))
	_ = logicalLineContentLen([]byte("x\r"), []byte("\n"))
	matches := []Match{}
	budget := 1
	if !appendGrepLine(&matches, regexp.MustCompile(`ready`), 0, []byte("ready\n"), &budget, 0, false) {
		t.Fatal("appendGrepLine did not stop at exhausted budget")
	}
	if appendGrepLine(&matches, regexp.MustCompile(`ready`), 0, []byte("ready\n"), &budget, 0, true) {
		t.Fatal("appendGrepLine stopped for an overlong line")
	}

	if _, _, err := OutputFileStats(filepath.Join(root, "missing-output.log")); err == nil {
		t.Fatal("OutputFileStats accepted a missing output")
	}
	badStatsPath := filepath.Join(root, "bad-stats.log")
	if err := os.WriteFile(badStatsPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed bad stats output: %v", err)
	}
	if err := os.WriteFile(outputMetaPath(badStatsPath), []byte("not-json"), 0o644); err != nil {
		t.Fatalf("seed bad stats metadata: %v", err)
	}
	if _, _, err := OutputFileStats(badStatsPath); err == nil {
		t.Fatal("OutputFileStats accepted malformed metadata")
	}
	badRemovePath := filepath.Join(root, "nonempty-output")
	if err := os.Mkdir(badRemovePath, 0o755); err != nil {
		t.Fatalf("make nonempty output directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badRemovePath, "child"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed nonempty output directory: %v", err)
	}
	if err := RemoveOutputArtifacts(badRemovePath); err == nil {
		t.Fatal("RemoveOutputArtifacts accepted a nonempty directory")
	}
}

func jcpReadOutput(t *testing.T, data []byte, setup func(*jcpHookFS)) *OutputStore {
	t.Helper()
	base := afero.NewMemMapFs()
	const path = "/read.log"
	if err := afero.WriteFile(base, path, data, 0o644); err != nil {
		t.Fatalf("seed read output: %v", err)
	}
	fs := &jcpHookFS{Fs: base}
	setup(fs)
	return &OutputStore{path: path, fs: fs, total: int64(len(data))}
}

func jcpPruneWithHook(t *testing.T, hook *jcpHookFile) error {
	t.Helper()
	base := afero.NewMemMapFs()
	const path = "/prune.log"
	if err := afero.WriteFile(base, path, []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("seed prune output: %v", err)
	}
	raw := jcpOpenAferoFile(t, base, path)
	hook.File = raw
	o := &OutputStore{path: path, metaPath: "/prune.meta", fs: base, f: hook, capBytes: 2, total: 10, disableSync: true}
	err := o.pruneLocked()
	_ = raw.Close()
	return err
}

func jcpPendingScenario(t *testing.T) (afero.Fs, string, string, string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	const outputPath = "/pending-output.log"
	if err := afero.WriteFile(fs, outputPath, []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("seed pending output: %v", err)
	}
	finalPath := outputMetaPath(outputPath)
	pendingPath := outputPendingMetaPath(finalPath)
	if err := writeOutputMetaFileFs(fs, pendingPath, outputMeta{TotalBytes: 20, RetainedStart: 14, RetainedSHA256: outputBytesSHA256([]byte("456789"))}); err != nil {
		t.Fatalf("seed pending metadata: %v", err)
	}
	if err := writeOutputMetaFileFs(fs, finalPath, outputMeta{TotalBytes: 14, RetainedStart: 10, RetainedSHA256: outputBytesSHA256([]byte("0123"))}); err != nil {
		t.Fatalf("seed final metadata: %v", err)
	}
	return fs, pendingPath, finalPath, outputPath
}

func jcpStoreErrorPaths(t *testing.T, root string, r *jcpReader) {
	t.Helper()
	blocker := filepath.Join(root, "store-blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed OpenNoSync blocker: %v", err)
	}
	if _, err := OpenNoSync(filepath.Join(blocker, "jobs.jsonl")); err == nil {
		t.Fatal("OpenNoSync accepted a file parent")
	}
	if _, err := openFs(&jcpHookFS{Fs: afero.NewMemMapFs(), openFileErr: jcpInjectedErr}, "/open.jsonl"); err == nil {
		t.Fatal("openFs accepted an open failure")
	}
	if _, err := openFs(&jcpHookFS{
		Fs:   afero.NewMemMapFs(),
		wrap: func(f afero.File) afero.File { return &jcpHookFile{File: f, statErr: jcpInjectedErr} },
	}, "/stat.jsonl"); err == nil {
		t.Fatal("openFs accepted a stat failure")
	}

	marshalStore, marshalRaw := jcpStoreWithHook(t, &jcpHookFile{}, true)
	if err := marshalStore.Append(Event{Kind: EventJobStarted, StructuredResult: math.Inf(1)}); err == nil {
		t.Fatal("Append accepted an unmarshalable event")
	}
	_ = marshalRaw.Close()
	batchMarshalStore, batchMarshalRaw := jcpStoreWithHook(t, &jcpHookFile{}, true)
	if err := batchMarshalStore.AppendBatch([]Event{{Kind: EventJobStarted, StructuredResult: math.Inf(1)}}); err == nil {
		t.Fatal("AppendBatch accepted an unmarshalable event")
	}
	_ = batchMarshalRaw.Close()

	for _, test := range []struct {
		label string
		file  *jcpHookFile
		batch bool
	}{
		{"Append seek", &jcpHookFile{seekErr: jcpInjectedErr}, false},
		{"Append write", &jcpHookFile{writeErr: jcpInjectedErr}, false},
		{"Append sync", &jcpHookFile{syncErr: jcpInjectedErr}, false},
		{"AppendBatch seek", &jcpHookFile{seekErr: jcpInjectedErr}, true},
		{"AppendBatch write", &jcpHookFile{writeErr: jcpInjectedErr}, true},
		{"AppendBatch sync", &jcpHookFile{syncErr: jcpInjectedErr}, true},
	} {
		s, raw := jcpStoreWithHook(t, test.file, false)
		var err error
		if test.batch {
			err = s.AppendBatch([]Event{jcpStarted("batch-edge", r.next())})
		} else {
			err = s.Append(jcpStarted("append-edge", r.next()))
		}
		jcpRequireError(t, test.label, err)
		_ = raw.Close()
	}
	shortStore, shortRaw := jcpStoreWithHook(t, &jcpHookFile{shortWrite: true}, true)
	if err := shortStore.Append(jcpStarted("short-write", r.next())); err == nil {
		t.Fatal("Append accepted a short write")
	}
	_ = shortRaw.Close()

	for _, test := range []struct {
		label string
		file  *jcpHookFile
	}{
		{"rollback truncate and seek", &jcpHookFile{truncateErr: jcpInjectedErr, seekErr: jcpInjectedErr}},
		{"rollback truncate", &jcpHookFile{truncateErr: jcpInjectedErr}},
		{"rollback seek", &jcpHookFile{seekErr: jcpInjectedErr}},
		{"rollback sync", &jcpHookFile{syncErr: jcpInjectedErr}},
	} {
		s, raw := jcpStoreWithHook(t, test.file, false)
		jcpRequireError(t, test.label, s.appendFailureLocked("edge append", jcpInjectedErr, 0))
		_ = raw.Close()
	}

	closeStore, closeRaw := jcpStoreWithHook(t, &jcpHookFile{closeErr: jcpInjectedErr}, true)
	if err := closeStore.Close(); err == nil {
		t.Fatal("Store.Close accepted a close failure")
	}
	_ = closeRaw.Close()
	if err := (&Store{closed: true}).Close(); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("second Store.Close = %v, want ErrStoreClosed", err)
	}

	for _, operation := range []struct {
		label string
		call  func(*Store) error
	}{
		{"Load", func(s *Store) error { _, err := s.Load(); return err }},
		{"LoadOrdered", func(s *Store) error { _, err := s.LoadOrdered(); return err }},
		{"LoadDelegates", func(s *Store) error { _, err := s.LoadDelegates(); return err }},
		{"LoadWatches", func(s *Store) error { _, err := s.LoadWatches(); return err }},
		{"LoadWatchSends", func(s *Store) error { _, err := s.LoadWatchSends(); return err }},
		{"readAll", func(s *Store) error { _, err := s.readAll(); return err }},
	} {
		s := &Store{path: "/read-error.jsonl", fs: &jcpHookFS{Fs: afero.NewMemMapFs(), statErr: jcpInjectedErr}}
		jcpRequireError(t, operation.label, operation.call(s))
	}

	missingFS := afero.NewMemMapFs()
	missingRaw := jcpOpenAferoFile(t, missingFS, "/missing-read.jsonl")
	missingStore := &Store{path: "/missing-read.jsonl", fs: missingFS, f: missingRaw}
	if events, err := missingStore.readAllLocked(); err != nil || events != nil {
		t.Fatalf("readAllLocked missing = %#v/%v", events, err)
	}
	_ = missingRaw.Close()

	parseFS := afero.NewMemMapFs()
	if err := afero.WriteFile(parseFS, "/parse.jsonl", []byte("not-json\n"), 0o644); err != nil {
		t.Fatalf("seed parse failure: %v", err)
	}
	parseRaw := jcpOpenAferoFile(t, parseFS, "/parse.jsonl")
	if _, err := (&Store{path: "/parse.jsonl", fs: parseFS, f: parseRaw}).readAllLocked(); err == nil {
		t.Fatal("readAllLocked accepted malformed event")
	}
	_ = parseRaw.Close()

	scanFS := afero.NewMemMapFs()
	if err := afero.WriteFile(scanFS, "/scan.jsonl", append(jcpEventLine(t, jcpStarted("scan-edge", 0)), '\n'), 0o644); err != nil {
		t.Fatalf("seed scan failure: %v", err)
	}
	scanRaw := jcpOpenAferoFile(t, scanFS, "/scan.jsonl")
	scanStore := &Store{
		path: "/scan.jsonl",
		fs: &jcpHookFS{
			Fs:   scanFS,
			wrap: func(f afero.File) afero.File { return &jcpHookFile{File: f, readWithDataErr: true} },
		},
		f: scanRaw,
	}
	if _, err := scanStore.readAllLocked(); err == nil {
		t.Fatal("readAllLocked accepted a scanner failure")
	}
	_ = scanRaw.Close()

	validLine := jcpEventLine(t, jcpStarted("recovery-edge", r.next()))
	for _, test := range []struct {
		label string
		line  []byte
		file  *jcpHookFile
	}{
		{"finish seek", validLine, &jcpHookFile{seekErr: jcpInjectedErr}},
		{"finish write", validLine, &jcpHookFile{writeErr: jcpInjectedErr}},
		{"finish sync", validLine, &jcpHookFile{syncErr: jcpInjectedErr}},
		{"partial truncate", []byte(`{"kind":`), &jcpHookFile{truncateErr: jcpInjectedErr}},
		{"partial seek", []byte(`{"kind":`), &jcpHookFile{seekErr: jcpInjectedErr}},
		{"partial sync", []byte(`{"kind":`), &jcpHookFile{syncErr: jcpInjectedErr}},
	} {
		base := afero.NewMemMapFs()
		raw := jcpOpenAferoFile(t, base, "/recovery.jsonl")
		test.file.File = raw
		s := &Store{path: "/recovery.jsonl", fs: base, f: test.file}
		jcpRequireError(t, test.label, s.recoverTrailingJSONLineLocked(test.line, 0))
		_ = raw.Close()
	}

	for _, operation := range []struct {
		label string
		wrap  func(afero.File) afero.File
	}{
		{"trailing inspect seek", func(f afero.File) afero.File { return &jcpHookFile{File: f, seekErr: jcpInjectedErr} }},
		{"trailing inspect read", func(f afero.File) afero.File { return &jcpHookFile{File: f, readErr: jcpInjectedErr} }},
	} {
		base := afero.NewMemMapFs()
		if err := afero.WriteFile(base, "/partial.jsonl", []byte(`{"kind":`), 0o644); err != nil {
			t.Fatalf("seed %s: %v", operation.label, err)
		}
		raw := jcpOpenAferoFile(t, base, "/partial.jsonl")
		s := &Store{path: "/partial.jsonl", fs: &jcpHookFS{Fs: base, wrap: operation.wrap}, f: raw}
		jcpRequireError(t, operation.label, s.recoverTrailingPartialLineLocked())
		_ = raw.Close()
	}

	for _, raw := range [][]byte{nil, []byte("  "), []byte(`{"kind":`), []byte(`{"kind":true`), []byte(`{"kind":}`)} {
		var event Event
		err := json.Unmarshal(raw, &event)
		if err != nil {
			_ = isIncompleteTrailingJSON(raw, err)
		}
	}
}

func jcpStoreWithHook(t *testing.T, hook *jcpHookFile, disableSync bool) (*Store, afero.File) {
	t.Helper()
	base := afero.NewMemMapFs()
	raw := jcpOpenAferoFile(t, base, "/store.jsonl")
	hook.File = raw
	return &Store{path: "/store.jsonl", fs: base, f: hook, disableSync: disableSync}, raw
}

func jcpStoreRecoveryPaths(t *testing.T, r *jcpReader) {
	t.Helper()
	first := jcpStarted("job-recovery", r.next())
	line := jcpEventLine(t, first)

	completeFS := afero.NewMemMapFs()
	if err := afero.WriteFile(completeFS, "/jobs.jsonl", line, 0o644); err != nil {
		t.Fatalf("write complete trailing event: %v", err)
	}
	complete, err := openFs(completeFS, "/jobs.jsonl")
	if err != nil {
		t.Fatalf("open complete trailing event: %v", err)
	}
	complete.disableSync = true
	if events, err := complete.LoadEvents(); err != nil || len(events) != 1 {
		t.Fatalf("load complete trailing event = %#v/%v", events, err)
	}
	if got := jcpReadAfero(t, completeFS, "/jobs.jsonl"); !bytes.Equal(got, append(append([]byte(nil), line...), '\n')) {
		t.Fatalf("complete trailing recovery = %q", got)
	}
	if err := complete.Close(); err != nil {
		t.Fatalf("close complete recovery: %v", err)
	}

	incompleteFS := afero.NewMemMapFs()
	partial := []byte(`{"kind":"job_finished","job_id":"job-recovery"`)
	raw := append(append(append([]byte(nil), line...), '\n'), partial...)
	if err := afero.WriteFile(incompleteFS, "/jobs.jsonl", raw, 0o644); err != nil {
		t.Fatalf("write incomplete trailing event: %v", err)
	}
	incomplete, err := openFs(incompleteFS, "/jobs.jsonl")
	if err != nil {
		t.Fatalf("open incomplete trailing event: %v", err)
	}
	incomplete.disableSync = true
	if got := jcpReadAfero(t, incompleteFS, "/jobs.jsonl"); !bytes.Equal(got, append(append([]byte(nil), line...), '\n')) {
		t.Fatalf("incomplete trailing recovery = %q", got)
	}
	_ = incomplete.Close()

	corruptFS := afero.NewMemMapFs()
	if err := afero.WriteFile(corruptFS, "/jobs.jsonl", append(append(append([]byte(nil), line...), '\n'), []byte("not-json")...), 0o644); err != nil {
		t.Fatalf("write corrupt trailing event: %v", err)
	}
	if _, err := openFs(corruptFS, "/jobs.jsonl"); err == nil {
		t.Fatal("open corrupt trailing event succeeded")
	}

	for _, raw := range [][]byte{nil, []byte("  \t"), []byte(`{"kind":`), []byte(`{"resumable":trx`), []byte(`}garbage`), []byte(`{"seq":"bad"}`)} {
		var event Event
		err := json.Unmarshal(raw, &event)
		if err == nil {
			continue
		}
		_ = isIncompleteTrailingJSON(raw, err)
	}

	path := filepath.Join(t.TempDir(), "forensic.jsonl")
	if events, err := ReadEvents(path); err != nil || events != nil {
		t.Fatalf("ReadEvents missing = %#v/%v", events, err)
	}
	forensic := append(append(append([]byte(nil), line...), '\n'), []byte(`{"kind":"job_finished"`)...)
	if err := os.WriteFile(path, forensic, 0o644); err != nil {
		t.Fatalf("write forensic partial: %v", err)
	}
	if events, err := ReadEvents(path); err != nil || len(events) != 1 {
		t.Fatalf("ReadEvents partial = %#v/%v", events, err)
	}
	if err := os.WriteFile(path, append(append(append([]byte(nil), line...), '\n'), append(append([]byte("not-json\n"), line...), '\n')...), 0o644); err != nil {
		t.Fatalf("write forensic corrupt middle: %v", err)
	}
	if _, err := ReadEvents(path); err == nil {
		t.Fatal("ReadEvents accepted corrupt middle line")
	}
}

func jcpOutputMetadataPaths(t *testing.T, root string, r *jcpReader) {
	t.Helper()
	fs := afero.NewMemMapFs()
	const outputPath = "/meta-output.log"
	data := []byte(fmt.Sprintf("012345%02x", r.next()))
	if err := afero.WriteFile(fs, outputPath, data, 0o644); err != nil {
		t.Fatalf("seed metadata output: %v", err)
	}
	metaPath := outputMetaPath(outputPath)
	meta := outputMeta{TotalBytes: int64(len(data)), RetainedStart: 0, RetainedSHA256: outputBytesSHA256(data)}
	if err := writeOutputMetaFileFs(fs, metaPath, meta); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if got, ok, err := readOutputMeta(fs, metaPath); err != nil || !ok || got != meta {
		t.Fatalf("readOutputMeta = %#v/%v/%v", got, ok, err)
	}
	if got, ok, err := readValidOutputMetaFs(fs, metaPath, outputPath, int64(len(data))); err != nil || !ok || got != meta {
		t.Fatalf("readValidOutputMetaFs = %#v/%v/%v", got, ok, err)
	}
	if prefix := data[:len(data)-2]; len(prefix) > 0 {
		old := outputMeta{TotalBytes: int64(len(prefix)), RetainedStart: 0, RetainedSHA256: outputBytesSHA256(prefix)}
		if err := writeOutputMetaFileFs(fs, metaPath, old); err != nil {
			t.Fatalf("write old metadata: %v", err)
		}
		if got, ok, err := readValidOutputMetaFs(fs, metaPath, outputPath, int64(len(data))); err != nil || !ok || got.TotalBytes != int64(len(data)) {
			t.Fatalf("grown metadata = %#v/%v/%v", got, ok, err)
		}
	}
	publicOutputPath := filepath.Join(root, "metadata-output.log")
	if err := os.WriteFile(publicOutputPath, data, 0o644); err != nil {
		t.Fatalf("seed public metadata output: %v", err)
	}
	if err := writeOutputMetaFile(outputMetaPath(publicOutputPath), meta); err != nil {
		t.Fatalf("seed public output metadata: %v", err)
	}
	if _, _, err := readValidOutputMeta(outputMetaPath(publicOutputPath), publicOutputPath, int64(len(data))); err != nil {
		t.Fatalf("public readValidOutputMeta: %v", err)
	}

	pendingPath := outputPendingMetaPath(metaPath)
	if err := writeOutputMetaFileFs(fs, pendingPath, meta); err != nil {
		t.Fatalf("write pending metadata: %v", err)
	}
	if got, ok, err := readValidPendingOutputMeta(fs, pendingPath, metaPath, outputPath, int64(len(data))); err != nil || !ok || got.TotalBytes != int64(len(data)) {
		t.Fatalf("read pending metadata = %#v/%v/%v", got, ok, err)
	}
	if _, err := outputMetaRetainedBytes(outputMeta{TotalBytes: 1, RetainedStart: 2}); err == nil {
		t.Fatal("invalid retained metadata accepted")
	}
	if ok, err := outputFileHasPrefixSHA256(fs, outputPath, 2, outputBytesSHA256(data[:2])); err != nil || !ok {
		t.Fatalf("prefix hash = %v/%v", ok, err)
	}
	if ok, err := outputFileHasSuffixSHA256(fs, outputPath, int64(len(data)-2), 2, outputBytesSHA256(data[len(data)-2:])); err != nil || !ok {
		t.Fatalf("suffix hash = %v/%v", ok, err)
	}
	if hash, err := outputFileSHA256(fs, outputPath); err != nil || hash != outputBytesSHA256(data) {
		t.Fatalf("full hash = %q/%v", hash, err)
	}

	publicPath := filepath.Join(root, "metadata.json")
	if err := writeOutputMetaFile(publicPath, meta); err != nil {
		t.Fatalf("writeOutputMetaFile: %v", err)
	}
}

func jcpFaultPaths(t *testing.T, r *jcpReader) {
	t.Helper()
	jcpFaultOutputAppend(t)
	jcpFaultOutputPrune(t)
	jcpFaultStoreAppend(t, r.next())
}

func jcpFaultOutputAppend(t *testing.T) {
	t.Helper()
	errs := jcpFaultSweep(t, 64, nil, func(fs afero.Fs) error {
		o, err := openOutputFs(fs, "/fault-append.log", 0)
		if err != nil {
			return err
		}
		defer func() { _ = o.Close() }()
		_, err = o.Append([]byte("payload"))
		return err
	})
	for _, arm := range []string{
		"jobstore: append output",
		"jobstore: sync output before metadata",
		"jobstore: open output metadata",
		"jobstore: write output metadata",
		"jobstore: sync output metadata",
		"jobstore: replace output metadata",
	} {
		jcpRequireFaultArm(t, errs, arm)
	}
}

func jcpFaultOutputPrune(t *testing.T) {
	t.Helper()
	errs := jcpFaultSweep(t, 72, func(base afero.Fs) {
		if err := afero.WriteFile(base, "/fault-prune.log", bytes.Repeat([]byte("x"), 100), 0o644); err != nil {
			t.Fatalf("seed prune output: %v", err)
		}
	}, func(fs afero.Fs) error {
		o, err := openOutputFs(fs, "/fault-prune.log", 10)
		if err != nil {
			return err
		}
		return o.Close()
	})
	for _, arm := range []string{
		"jobstore: seek output prune tail",
		"jobstore: read output prune tail",
		"jobstore: truncate output",
		"jobstore: rewrite output tail",
		"jobstore: trim output tail",
	} {
		jcpRequireFaultArm(t, errs, arm)
	}
}

func jcpFaultStoreAppend(t *testing.T, b byte) {
	t.Helper()
	sawRollback := false
	for faultAt := 0; faultAt < 48; faultAt++ {
		base := afero.NewMemMapFs()
		const path = "/fault-jobs.jsonl"
		seed, err := openFs(base, path)
		if err != nil {
			t.Fatalf("open fault seed: %v", err)
		}
		seed.disableSync = true
		if err := seed.Append(jcpStarted("job-committed", b)); err != nil {
			t.Fatalf("append committed seed: %v", err)
		}
		if err := seed.Close(); err != nil {
			t.Fatalf("close committed seed: %v", err)
		}

		faulted, err := openFs(fault.FS(base, fault.FromBytes(jcpFaultPlan(faultAt))), path)
		if err == nil {
			appendErr := faulted.Append(jcpStarted("job-candidate", b))
			if appendErr != nil && errors.Is(appendErr, fault.ErrInjected) {
				sawRollback = true
			}
			_ = faulted.Close()
		}
		clean, err := openFs(base, path)
		if err != nil {
			t.Fatalf("clean reopen after fault %d: %v", faultAt, err)
		}
		clean.disableSync = true
		events, err := clean.LoadEvents()
		if err != nil {
			t.Fatalf("clean load after fault %d: %v", faultAt, err)
		}
		if len(events) == 0 || events[0].JobID != "job-committed" {
			t.Fatalf("fault %d lost committed event: %#v", faultAt, events)
		}
		_ = clean.Close()
	}
	if !sawRollback {
		t.Fatal("store fault sweep never reached an injected append error")
	}
}

func jcpFaultSweep(t *testing.T, max int, setup func(afero.Fs), drive func(afero.Fs) error) []error {
	t.Helper()
	errs := make([]error, max)
	for i := 0; i < max; i++ {
		base := afero.NewMemMapFs()
		if setup != nil {
			setup(base)
		}
		fs := fault.FS(base, fault.FromBytes(jcpFaultPlan(i)))
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("fault %d panicked: %v", i, recovered)
				}
			}()
			errs[i] = drive(fs)
		}()
	}
	return errs
}

func jcpFaultPlan(index int) []byte {
	plan := bytes.Repeat([]byte{1}, index+1)
	plan[index] = 0
	return plan
}

func jcpRequireFaultArm(t *testing.T, errs []error, want string) {
	t.Helper()
	for _, err := range errs {
		if err != nil && strings.Contains(err.Error(), want) {
			if !errors.Is(err, fault.ErrInjected) {
				t.Fatalf("fault arm %q did not wrap fault.ErrInjected: %v", want, err)
			}
			return
		}
	}
	t.Fatalf("fault sweep did not reach %q", want)
}

func jcpEventLine(t *testing.T, event Event) []byte {
	t.Helper()
	b, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return b
}

func jcpReadAfero(t *testing.T, fs afero.Fs, path string) []byte {
	t.Helper()
	b, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

var jcpInjectedErr = errors.New("jobstore program injected error")

// jcpHookFS is a deliberately small afero boundary double. It leaves all
// unmentioned operations on its backing filesystem, so each matrix case drives
// the real jobstore implementation until one chosen I/O boundary fails.
type jcpHookFS struct {
	afero.Fs
	openErr     error
	openFileErr error
	statErr     error
	removeErr   error
	renameErr   error
	wrap        func(afero.File) afero.File
}

func (fs *jcpHookFS) Open(name string) (afero.File, error) {
	if fs.openErr != nil {
		return nil, fs.openErr
	}
	f, err := fs.Fs.Open(name)
	if err != nil || fs.wrap == nil {
		return f, err
	}
	return fs.wrap(f), nil
}

func (fs *jcpHookFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if fs.openFileErr != nil {
		return nil, fs.openFileErr
	}
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil || fs.wrap == nil {
		return f, err
	}
	return fs.wrap(f), nil
}

func (fs *jcpHookFS) Stat(name string) (os.FileInfo, error) {
	if fs.statErr != nil {
		return nil, fs.statErr
	}
	return fs.Fs.Stat(name)
}

func (fs *jcpHookFS) Remove(name string) error {
	if fs.removeErr != nil {
		return fs.removeErr
	}
	return fs.Fs.Remove(name)
}

func (fs *jcpHookFS) Rename(oldname, newname string) error {
	if fs.renameErr != nil {
		return fs.renameErr
	}
	return fs.Fs.Rename(oldname, newname)
}

type jcpHookFile struct {
	afero.File
	statErr         error
	readErr         error
	readWithDataErr bool
	writeErr        error
	seekErr         error
	truncateErr     error
	syncErr         error
	closeErr        error
	shortWrite      bool
	seekFailAt      int
	truncateFailAt  int
	seekCalls       int
	truncateCalls   int
}

func (f *jcpHookFile) Stat() (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.File.Stat()
}

func (f *jcpHookFile) Read(p []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	n, err := f.File.Read(p)
	if n > 0 && f.readWithDataErr {
		return n, jcpInjectedErr
	}
	return n, err
}

func (f *jcpHookFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.shortWrite && len(p) > 0 {
		return 0, nil
	}
	return f.File.Write(p)
}

func (f *jcpHookFile) Seek(offset int64, whence int) (int64, error) {
	f.seekCalls++
	if f.seekErr != nil && (f.seekFailAt == 0 || f.seekCalls == f.seekFailAt) {
		return 0, f.seekErr
	}
	return f.File.Seek(offset, whence)
}

func (f *jcpHookFile) Truncate(size int64) error {
	f.truncateCalls++
	if f.truncateErr != nil && (f.truncateFailAt == 0 || f.truncateCalls == f.truncateFailAt) {
		return f.truncateErr
	}
	return f.File.Truncate(size)
}

func (f *jcpHookFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}

func (f *jcpHookFile) Close() error {
	_ = f.File.Close()
	if f.closeErr != nil {
		return f.closeErr
	}
	return nil
}

type jcpErrorReader struct{}

func (jcpErrorReader) Read([]byte) (int, error) { return 0, jcpInjectedErr }

func jcpRequireError(t *testing.T, label string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s succeeded", label)
	}
}

func jcpOpenAferoFile(t *testing.T, fs afero.Fs, path string) afero.File {
	t.Helper()
	f, err := fs.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return f
}
