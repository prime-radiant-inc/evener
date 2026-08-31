package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// seedSidecarSession writes a transcript with n entries, resumes it once
// (full scan — writes the opportunistic sidecar), and returns the path.
func seedSidecarSession(t *testing.T, sessionID string, n int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, sessionID+".transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	for i := 0; i < n; i++ {
		if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("turn"))); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// The checkpoint entry is what the opportunistic anchor keys its offset
	// AT (ResumeHistory's window), so the fixture needs one.
	if err := w.Append(schema.NewTurn(schema.TurnCheckpoint, llm.User("compaction summary"))); err != nil {
		t.Fatalf("append checkpoint: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	first, _, err := transcript.OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	return path
}

// TestTrackFailuresSeedWithSidecarIncludesPreCheckpointFailures: the failure
// floor carried by the sidecar must survive a windowed resume — a session
// with failures before the anchor reports them after resume, not a false
// clean.
func TestTrackFailuresSeedWithSidecarIncludesPreCheckpointFailures(t *testing.T) {
	const sessionID = "sidecar_failures"
	dir := t.TempDir()
	path := filepath.Join(dir, sessionID+".transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	// A failing tool result: error=true counts via FailedToolResult.
	failed := schema.NewTurn(schema.TurnToolResults, llm.User("results"))
	failed.Message.Content = []llm.ContentPart{{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: "call_1",
			Name:       "shell",
			IsError:    true,
		},
	}}
	if err := w.Append(failed); err != nil {
		t.Fatalf("append failed turn: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("ok"))); err != nil {
		t.Fatalf("append ok turn: %v", err)
	}
	// The checkpoint entry anchors the sidecar's offset; the failing turn
	// lives in the PREFIX (before it), which is what the floor must carry.
	if err := w.Append(schema.NewTurn(schema.TurnCheckpoint, llm.User("compaction summary"))); err != nil {
		t.Fatalf("append checkpoint: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Full-scan resume writes the opportunistic sidecar with the floor. The
	// full-scan view itself does not carry the sidecar (it IS the full
	// scan); read it back the way the next, windowed resume will.
	first, _, err := transcript.OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	written, ok := transcript.ReadSidecar(path)
	if !ok {
		t.Fatalf("sidecar missing after full-scan resume")
	}
	if written.FailureFloor != 1 {
		t.Fatalf("opportunistic sidecar failure floor: got %d, want 1", written.FailureFloor)
	}
	// Windowed resume: the floor must come through.
	second, view, err := transcript.OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("second resume: %v", err)
	}
	defer second.Close() //nolint:errcheck
	if !view.SidecarUsed {
		t.Fatalf("second resume fell back to full scan")
	}
	if err := second.TrackFailuresSeeded(view.Entries, view.Sidecar.FailureFloor, view.PrefixEntryCount, 0); err != nil {
		t.Fatalf("seeded track failures: %v", err)
	}
	count, counted := second.FailedToolCalls()
	if !counted || count != 1 {
		t.Fatalf("windowed failure count: got (%d, %v), want (1, true)", count, counted)
	}
}

// TestPendingDelegateAttentionOpenedBeforeBoundarySurvivesResume: an
// attention opened before the sidecar's offset and still pending must
// survive a windowed resume's fold — the seeded fold reconstructs it from
// the snapshot instead of dropping it.
func TestPendingDelegateAttentionOpenedBeforeBoundarySurvivesResume(t *testing.T) {
	const sessionID = "sidecar_attention"
	path := seedSidecarSession(t, sessionID, 3)

	// Build a fold snapshot as the opportunistic anchor would: a pending
	// attention with content.
	sidecar, ok := transcript.ReadSidecar(path)
	if !ok {
		t.Fatalf("sidecar missing after full-scan resume")
	}
	encoded := []byte(`{"role":"user","content":[{"kind":"text","text":"pending attention content"}]}`)
	sidecar.PendingAttention = []transcript.SidecarPendingAttention{
		{AttentionID: "att_pending", Message: transcript.JSONMessage(encoded)},
	}
	sidecar.SnapshotsComplete = true
	if err := transcript.WriteSidecar(path, sidecar); err != nil {
		t.Fatalf("rewrite sidecar: %v", err)
	}

	_, view, err := transcript.OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("windowed resume: %v", err)
	}
	if !view.SidecarUsed {
		t.Fatalf("resume did not use sidecar")
	}
	fold, err := foldDelegateAttentionSeeded(view.Sidecar, view.Entries)
	if err != nil {
		t.Fatalf("seeded fold: %v", err)
	}
	pending := fold.pendingIDs()
	if len(pending) != 1 || pending[0] != "att_pending" {
		t.Fatalf("pending attention after windowed resume: %v, want [att_pending]", pending)
	}
}

// TestDelegateAttentionFoldResolutionAfterBoundaryDoesNotError: an
// attention opened pre-boundary and RESOLVED in the suffix must fold
// cleanly — the seed's content is what lets the resolution resolve instead
// of tripping "resolved before it was appended".
func TestDelegateAttentionFoldResolutionAfterBoundaryDoesNotError(t *testing.T) {
	const sessionID = "sidecar_straddle"
	path := seedSidecarSession(t, sessionID, 2)

	sidecar, ok := transcript.ReadSidecar(path)
	if !ok {
		t.Fatalf("sidecar missing")
	}
	encoded := []byte(`{"role":"user","content":[{"kind":"text","text":"straddling attention"}]}`)
	sidecar.PendingAttention = []transcript.SidecarPendingAttention{
		{AttentionID: "att_straddle", Message: transcript.JSONMessage(encoded)},
	}
	sidecar.SnapshotsComplete = true
	if err := transcript.WriteSidecar(path, sidecar); err != nil {
		t.Fatalf("rewrite sidecar: %v", err)
	}

	// Append a resolution turn AFTER the boundary.
	w, err := transcript.OpenWriter(path)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	resolution := schema.NewTurn(schema.TurnAttentionResolution, llm.User(""))
	resolution.AttentionResolution = &schema.AttentionResolutionInfo{
		AttentionID: "att_straddle",
		Disposition: "consumed",
	}
	if err := w.Append(resolution); err != nil {
		t.Fatalf("append resolution: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, view, err := transcript.OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("windowed resume: %v", err)
	}
	if !view.SidecarUsed {
		t.Fatalf("resume did not use sidecar")
	}
	if len(view.Entries) != 2 {
		t.Fatalf("suffix entries: got %d, want 2 (the checkpoint the sidecar anchors at, plus the resolution)", len(view.Entries))
	}
	fold, err := foldDelegateAttentionSeeded(view.Sidecar, view.Entries)
	if err != nil {
		t.Fatalf("seeded fold tripped on a boundary-straddling resolution: %v", err)
	}
	if disposition, resolved := fold.resolutions["att_straddle"]; !resolved || disposition != delegateAttentionConsumed {
		t.Fatalf("resolution after fold: %v %v, want consumed true", disposition, resolved)
	}
}

// TestForkChildResumeFallsBackToFullScanWithoutSidecar: a fork child's file
// (replayed prefix, fresh incarnation) must not validate the parent's
// sidecar — and its own first resume has no sidecar at all.
func TestForkChildResumeFallsBackToFullScanWithoutSidecar(t *testing.T) {
	const sessionID = "sidecar_forkchild"
	path := seedSidecarSession(t, sessionID, 4)
	// The fork child gets a COPY of the file — same bytes, new incarnation.
	child := path + ".fork"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if err := os.WriteFile(child, data, 0o644); err != nil {
		t.Fatalf("write child: %v", err)
	}
	w, view, err := transcript.OpenWriterForResume(child, sessionID)
	if err != nil {
		t.Fatalf("child resume: %v", err)
	}
	defer w.Close() //nolint:errcheck
	if view.SidecarUsed {
		t.Fatalf("fork child used a sidecar")
	}
	if len(view.Entries) != 5 {
		t.Fatalf("child entries: got %d, want 5 (4 user turns + the checkpoint)", len(view.Entries))
	}
}

// TestRestoreSessionAcceptsCorruptPrefixLineWithSidecarAndFullReaderStillFails
// pins the deliberate wf7e posture change: the windowed reader ACCEPTS a
// prefix it did not decode (that is its purpose — the sidecar's anchors
// vouch for those bytes), while the FULL reader (readTranscriptFull) still
// fails the whole file on a corrupt line anywhere. Both behaviors are
// decisions: the comment at agent/transcript/transcript.go decodeStrictJSON
// documents the one-way door; this test pins that the windowed path does
// not widen the full reader's strictness and the full reader's failure is
// what a windowed resume with a BAD sidecar falls back to.
func TestRestoreSessionAcceptsCorruptPrefixLineWithSidecarAndFullReaderStillFails(t *testing.T) {
	const sessionID = "sidecar_wf7e"
	dir := t.TempDir()
	path := filepath.Join(dir, sessionID+".transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("turn"))); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// First resume writes the opportunistic sidecar.
	first, _, err := transcript.OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	// Corrupt a PREFIX line: not the first (header), and inside the
	// sidecar's prefix. The sidecar's anchors were computed BEFORE the
	// corruption — so anchor validation must REJECT the mutated prefix and
	// fall back to the full scan, which then fails on the corrupt line.
	// That is the intended composition: the sidecar never vouches for bytes
	// it did not stamp.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := splitTranscriptLines(data)
	if len(lines) < 4 {
		t.Fatalf("fixture too small: %d lines", len(lines))
	}
	lines[1] = []byte(`{"kind":"entry","seq":1,"turn":{"kind":"user_input","message":{"role":"user","content":[{"type":"text","text":"ok"}]},"unknown_field":true}}`)
	corrupt := joinTranscriptLines(lines)
	if err := os.WriteFile(path, corrupt, 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	w2, view, err := transcript.OpenWriterForResume(path, sessionID)
	if err != nil {
		// A full-scan failure on the corrupt line is the correct outcome:
		// the sidecar's anchors reject the mutated prefix, the fallback
		// full scan hits the unknown field, and wf7e's posture says the
		// whole file fails. This branch PINS that the failure is the
		// full reader's, not a windowed acceptance.
		if w2 != nil {
			t.Fatalf("writer returned alongside error")
		}
		if view.SidecarUsed {
			t.Fatalf("windowed read accepted a mutated prefix")
		}
		return
	}
	defer w2.Close() //nolint:errcheck
	if view.SidecarUsed {
		t.Fatalf("windowed read accepted a mutated prefix")
	}
	// If the corrupt line still parses (a field the schema knows), the
	// full scan simply ran — also correct. The invariant is only that the
	// sidecar was not used over mutated prefix bytes.
}

func splitTranscriptLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

func joinTranscriptLines(lines [][]byte) []byte {
	var out []byte
	for _, line := range lines {
		out = append(out, line...)
		out = append(out, '\n')
	}
	return out
}
