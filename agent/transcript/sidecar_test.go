package transcript

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// seedWindowedTranscript writes a transcript with header + n user entries +
// one CHECKPOINT entry and returns its path. The checkpoint is what the
// opportunistic anchor keys its offset AT (ResumeHistory's window starts at
// the last checkpoint), so every windowed-resume fixture needs one.
func seedWindowedTranscript(t *testing.T, sessionID string, n int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, sessionID+".transcript.jsonl")
	w, err := NewWriter(path, Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	for i := 0; i < n; i++ {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User("turn text"))
		if err := w.Append(turn); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	checkpoint := schema.NewTurn(schema.TurnCheckpoint, llm.User("compaction summary"))
	if err := w.Append(checkpoint); err != nil {
		t.Fatalf("append checkpoint: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return path
}

// TestWriteSidecarFromWriterWindowsAtCheckpointEntryStart pins the
// compaction anchor's window: a sidecar written from a LIVE writer right
// after it appended a checkpoint must anchor at the START of that entry, so
// the windowed resume that validates it decodes [checkpoint, ...rest] — the
// same window ResumeHistory defines. The failure this pins: an anchor written
// at the post-checkpoint append position silently drops the compaction
// summary from every resumed session's live history.
func TestWriteSidecarFromWriterWindowsAtCheckpointEntryStart(t *testing.T) {
	const sessionID = "s_anchor"
	dir := t.TempDir()
	path := filepath.Join(dir, sessionID+".transcript.jsonl")
	w, err := NewWriter(path, Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("turn"))); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// The compaction moment: the checkpoint is the LAST entry appended.
	if err := w.Append(schema.NewTurn(schema.TurnCheckpoint, llm.User("compaction summary"))); err != nil {
		t.Fatalf("append checkpoint: %v", err)
	}
	// The compaction anchor. The writer must refuse to claim snapshots it
	// did not compute (complete=false); the fold falls back to its full read.
	if err := w.WriteSidecarFromWriter(path, nil, nil, nil, false); err != nil {
		t.Fatalf("write sidecar from writer: %v", err)
	}
	// Turn the writer's crank once more the way the session would, so the
	// file has a suffix entry AFTER the checkpoint too.
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("after compaction"))); err != nil {
		t.Fatalf("append after: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	resumed, view, err := OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("windowed resume: %v", err)
	}
	defer resumed.Close() //nolint:errcheck
	if !view.SidecarUsed {
		t.Fatalf("resume fell back to full scan — the compaction anchor did not validate")
	}
	if len(view.Entries) != 2 {
		t.Fatalf("suffix entries: got %d, want 2 (the checkpoint entry + the post-compaction entry)", len(view.Entries))
	}
	if view.Entries[0].Turn.Kind != schema.TurnCheckpoint {
		t.Fatalf("suffix's first entry: got kind %q, want the checkpoint entry — the compaction summary must survive resume", view.Entries[0].Turn.Kind)
	}
	if view.PrefixEntryCount != 3 {
		t.Fatalf("prefix entry count: got %d, want 3", view.PrefixEntryCount)
	}
	// The prefix facts must match what the post-full-scan anchor would write
	// for the same file: the boundary seq is the last PREFIX entry's seq, not
	// the checkpoint's.
	sidecar, ok := ReadSidecar(path)
	if !ok {
		t.Fatalf("sidecar missing")
	}
	if sidecar.BoundarySeq != sidecar.MaxSeq {
		t.Fatalf("boundary seq %d must equal the prefix max seq %d", sidecar.BoundarySeq, sidecar.MaxSeq)
	}
	if sidecar.SnapshotsComplete {
		t.Fatalf("compaction anchor must not claim complete snapshots it did not compute")
	}
}

// resumeViaSidecar runs OpenWriterForResume twice: the first call performs
// the full scan and writes the opportunistic sidecar; the second must use
// it. entryCount is the seeded entry count. Returns the second call's view
// and writer.
func resumeViaSidecar(t *testing.T, path, sessionID string, entryCount int) (ResumeView, *Writer) {
	t.Helper()
	first, view, err := OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	if view.SidecarUsed {
		t.Fatalf("first resume used a sidecar that does not exist yet")
	}
	if len(view.Entries) != entryCount {
		t.Fatalf("first resume entries: got %d, want %d", len(view.Entries), entryCount)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	second, view, err := OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("second resume: %v", err)
	}
	if !view.SidecarUsed {
		t.Fatalf("second resume did not use the sidecar (full scan fell back)")
	}
	return view, second
}

// TestOpenWriterForResumeWindowedSecondResumeIsSuffixOnly: the opportunistic
// anchor after a full scan must make the SECOND resume windowed — zero
// decoded prefix entries, correct prefix count, correct maxSeq floor, and a
// writer whose next append does not collide.
func TestOpenWriterForResumeWindowedSecondResumeIsSuffixOnly(t *testing.T) {
	const sessionID = "sidecar_window"
	path := seedWindowedTranscript(t, sessionID, 5)
	view, w := resumeViaSidecar(t, path, sessionID, 6)
	defer w.Close() //nolint:errcheck // test cleanup

	// The suffix is exactly ResumeHistory's window: the checkpoint entry
	// itself plus everything after it (nothing yet), and NOT the five user
	// entries before it.
	if len(view.Entries) != 1 || view.Entries[0].Turn.Kind != schema.TurnCheckpoint {
		t.Fatalf("windowed suffix: got %d entries, want 1 (the checkpoint)", len(view.Entries))
	}
	if view.PrefixEntryCount != 5 {
		t.Fatalf("prefix entry count: got %d, want 5", view.PrefixEntryCount)
	}
	// Appending after a windowed resume must not collide with the existing
	// seqs: the writer's seq must exceed the floor.
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("after resume"))
	if err := w.Append(turn); err != nil {
		t.Fatalf("append after windowed resume: %v", err)
	}
	// And a THIRD resume must now see exactly one suffix entry.
	third, view3, err := OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("third resume: %v", err)
	}
	defer third.Close() //nolint:errcheck
	if !view3.SidecarUsed {
		t.Fatalf("third resume fell back to full scan")
	}
	if len(view3.Entries) != 2 || view3.PrefixEntryCount != 5 {
		t.Fatalf("third resume: suffix=%d prefix=%d, want 2 (checkpoint + appended) and 5", len(view3.Entries), view3.PrefixEntryCount)
	}
	if view3.Entries[0].Seq <= view.Sidecar.BoundarySeq {
		t.Fatalf("suffix seq %d not above boundary %d", view3.Entries[0].Seq, view.Sidecar.BoundarySeq)
	}
}

// TestOpenWriterForResumeAppendsBetweenScansAdvanceTheSuffix: appends after
// the sidecar was written are append-only growth — anchors still validate,
// and the windowed read picks up the new entries.
func TestOpenWriterForResumeAppendsBetweenScansAdvanceTheSuffix(t *testing.T) {
	const sessionID = "sidecar_growth"
	path := seedWindowedTranscript(t, sessionID, 3)
	first, _, err := OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	// Append through a plain writer AFTER the sidecar exists.
	grow, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("open grow writer: %v", err)
	}
	if err := grow.Append(schema.NewTurn(schema.TurnUserInput, llm.User("grown"))); err != nil {
		t.Fatalf("grow append: %v", err)
	}
	if err := grow.Close(); err != nil {
		t.Fatalf("close grow: %v", err)
	}
	second, view, err := OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("second resume: %v", err)
	}
	defer second.Close() //nolint:errcheck
	if !view.SidecarUsed {
		t.Fatalf("append-only growth fell back to full scan")
	}
	if len(view.Entries) != 2 || view.PrefixEntryCount != 3 {
		t.Fatalf("windowed view: suffix=%d prefix=%d, want 2 (checkpoint + grown) and 3", len(view.Entries), view.PrefixEntryCount)
	}
}

// TestOpenWriterFallsBackToFullScanWhenSidecarMissing: no sidecar on disk
// means the legacy full-scan contract, byte for byte.
func TestOpenWriterFallsBackToFullScanWhenSidecarMissing(t *testing.T) {
	const sessionID = "sidecar_missing"
	path := seedWindowedTranscript(t, sessionID, 4)
	w, view, err := OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("resume without sidecar: %v", err)
	}
	defer w.Close() //nolint:errcheck
	if view.SidecarUsed {
		t.Fatalf("sidecar used with no sidecar file")
	}
	if len(view.Entries) != 5 || view.PrefixEntryCount != 0 {
		t.Fatalf("full-scan view: entries=%d prefix=%d, want 5 and 0", len(view.Entries), view.PrefixEntryCount)
	}
}

// TestOpenWriterFallsBackToFullScanWhenSidecarOffsetExceedsFileSize: a
// truncated transcript invalidates the sidecar's bounds — full scan.
func TestOpenWriterFallsBackToFullScanWhenSidecarOffsetExceedsFileSize(t *testing.T) {
	const sessionID = "sidecar_truncated"
	path := seedWindowedTranscript(t, sessionID, 6)
	w, _, err := OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	// Truncate the file below the sidecar's recorded size.
	if err := os.Truncate(path, 64); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// A truncated file may no longer parse at all; both outcomes (clean
	// fallback full-scan error, or a parse failure) must be errors, never a
	// silently accepted windowed read.
	second, view, err := OpenWriterForResume(path, sessionID)
	if err == nil {
		defer second.Close() //nolint:errcheck
		if view.SidecarUsed {
			t.Fatalf("windowed read accepted a truncated transcript")
		}
	}
}

// TestOpenWriterFallsBackToFullScanWhenSidecarFileIdentityMismatches: a
// different file incarnation (fork child: new file, replayed content) must
// fail the identity gate and fall back to the full scan.
func TestOpenWriterFallsBackToFullScanWhenSidecarFileIdentityMismatches(t *testing.T) {
	const sessionID = "sidecar_fork"
	path := seedWindowedTranscript(t, sessionID, 5)
	w, _, err := OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("parent first resume: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close parent first: %v", err)
	}
	// Fork child: copy the transcript bytes to a NEW file. Same content,
	// different incarnation — the sidecar's identity must not validate.
	childPath := path + ".child"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if err := os.WriteFile(childPath, data, 0o644); err != nil {
		t.Fatalf("write child: %v", err)
	}
	// Copy the sidecar too — the whole point is that it must be REFUSED
	// even though its content matches the child's bytes.
	sidecarData, err := os.ReadFile(SidecarPath(path))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if err := os.WriteFile(SidecarPath(childPath), sidecarData, 0o644); err != nil {
		t.Fatalf("write child sidecar: %v", err)
	}
	child, view, err := OpenWriterForResume(childPath, sessionID)
	if err != nil {
		t.Fatalf("child resume: %v", err)
	}
	defer child.Close() //nolint:errcheck
	if view.SidecarUsed {
		t.Fatalf("fork child accepted the parent's sidecar")
	}
	if len(view.Entries) != 6 {
		t.Fatalf("child full scan entries: got %d, want 6", len(view.Entries))
	}
}

// TestSidecarSeqFloorPreventsSeqCollision: the windowed writer's next seq
// must exceed the sidecar's floor even when the suffix is empty — resumed
// writes never collide with entries the windowed read never saw.
func TestSidecarSeqFloorPreventsSeqCollision(t *testing.T) {
	const sessionID = "sidecar_seqfloor"
	path := seedWindowedTranscript(t, sessionID, 4)
	view, w := resumeViaSidecar(t, path, sessionID, 5)
	defer w.Close() //nolint:errcheck
	if view.Sidecar.MaxSeq != 3 {
		t.Fatalf("sidecar max seq: got %d, want 3 (the last PREFIX entry; seqs are 0-based, 4 user entries = seqs 0-3, the checkpoint at seq 4 is in the suffix)", view.Sidecar.MaxSeq)
	}
	if w.seq != 5 {
		t.Fatalf("writer next seq: got %d, want 5 (above the suffix's checkpoint at seq 4)", w.seq)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("post"))); err != nil {
		t.Fatalf("append: %v", err)
	}
	if w.seq != 6 {
		t.Fatalf("seq after append: got %d, want 6", w.seq)
	}
}

// TestSidecarRoundTripPreservesFoldSnapshotFields: WriteSidecar → ReadSidecar
// preserves every snapshot field and rejects tampering.
func TestSidecarRoundTripPreservesFoldSnapshotFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.transcript.jsonl")
	if err := os.WriteFile(path, []byte("header\nentry\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sidecar := ResumeSidecar{
		Version:                 resumeSidecarVersion,
		TranscriptFormatVersion: FormatVersion,
		SessionID:               "s",
		TranscriptSize:          13,
		ValidBytes:              13,
		Offset:                  13,
		MaxSeq:                  1,
		EntryCount:              1,
		FailureFloor:            2,
		FileIdentity:            "dev:1:ino:2",
		BoundarySeq:             1,
		SnapshotsComplete:       true,
		PendingAttention: []SidecarPendingAttention{
			{AttentionID: "att1", Message: JSONMessage(`{"role":"user","content":[{"type":"text","text":"hi"}]}`), Resolution: "consumed", ResumeGeneration: 3},
		},
		DeliveryCommits:     []SidecarDeliveryCommit{{DeliveryID: "d1", ToolCallID: "tc1"}},
		ClientMutationTurns: map[string]string{"cm1": "turn_9"},
	}
	if err := WriteSidecar(path, sidecar); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	got, ok := ReadSidecar(path)
	if !ok {
		t.Fatalf("read sidecar failed")
	}
	if got.SessionID != "s" || got.Offset != 13 || got.MaxSeq != 1 || got.EntryCount != 1 || got.FailureFloor != 2 {
		t.Fatalf("scalar fields mismatch: %+v", got)
	}
	if len(got.PendingAttention) != 1 || got.PendingAttention[0].AttentionID != "att1" || got.PendingAttention[0].ResumeGeneration != 3 {
		t.Fatalf("pending attention mismatch: %+v", got.PendingAttention)
	}
	if string(got.PendingAttention[0].Message) == "" {
		t.Fatalf("pending attention message lost")
	}
	if len(got.DeliveryCommits) != 1 || got.DeliveryCommits[0].DeliveryID != "d1" {
		t.Fatalf("delivery commits mismatch: %+v", got.DeliveryCommits)
	}
	if got.ClientMutationTurns["cm1"] != "turn_9" {
		t.Fatalf("client mutation turns mismatch: %+v", got.ClientMutationTurns)
	}
	// Tampering must fail the integrity check.
	raw, err := os.ReadFile(SidecarPath(path))
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	tampered := []byte(string(raw[:len(raw)-3]) + "999")
	if err := os.WriteFile(SidecarPath(path), tampered, 0o644); err != nil {
		t.Fatalf("write tampered: %v", err)
	}
	if _, ok := ReadSidecar(path); ok {
		t.Fatalf("tampered sidecar accepted")
	}
}
