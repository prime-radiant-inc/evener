package contextmgr

import (
	"fmt"
	"strings"
	"testing"
)

// A checkpoint's fixed metadata alone can exceed the 60k cap: 5000 modified
// paths used to render ~250k chars because only the variable content was ever
// shed. The metadata is now cut after the transcript pointer, which stays whole
// so the dropped tail remains recoverable (CORE-07).
func TestFormatCheckpoint_BoundsOversizedMetadata(t *testing.T) {
	data := checkpointData{fileWrites: map[string]checkpointWriteStatus{}}
	for i := range 5000 {
		data.fileWrites[fmt.Sprintf("internal/some/long/package/path/file_%04d.go", i)] = writeConfirmed
	}
	metas := []struct {
		name      string
		meta      *CompactionMeta
		hasRecord bool
	}{
		{"persistent transcript", persistentMeta, true},
		{"persistent session without a kept transcript", &CompactionMeta{SessionID: "SESSION_ID_SENTINEL"}, true},
		{"non-persistent session", &CompactionMeta{}, false},
		{"nil metadata", nil, false},
	}
	for _, tc := range metas {
		t.Run(tc.name, func(t *testing.T) {
			cp := formatCheckpoint(data, tc.meta, maxCheckpointChars)
			if len(cp) > maxCheckpointChars {
				t.Fatalf("checkpoint with oversized metadata is %d chars, want at most %d", len(cp), maxCheckpointChars)
			}
			if tc.hasRecord {
				pointer := tc.meta.compactionPointer("checkpoint")
				if !strings.HasPrefix(cp, "[CONTEXT CHECKPOINT]\n"+pointer) {
					t.Fatalf("checkpoint lost its leading transcript pointer:\n%.300s", cp)
				}
				if !strings.Contains(cp, tc.meta.SessionID) {
					t.Fatalf("checkpoint lost the session id that makes the full history recoverable:\n%.300s", cp)
				}
			}
			if !strings.Contains(cp, checkpointMetadataTruncated) {
				t.Fatalf("metadata was cut without the truncation marker:\n%.300s", cp)
			}
			for _, line := range strings.Split(cp, "\n") {
				entries, ok := strings.CutPrefix(line, "Files modified: ")
				if !ok {
					continue
				}
				for _, entry := range strings.Split(entries, ", ") {
					if !strings.HasSuffix(entry, ".go") {
						t.Fatalf("truncation left a partial path %q in the modified-files list", entry)
					}
				}
			}
		})
	}
}

// Metadata that fits must survive verbatim: the new bound only engages when the
// fixed sections alone would overrun the cap.
func TestFormatCheckpoint_KeepsFittingMetadata(t *testing.T) {
	data := checkpointData{
		fileWrites:      map[string]checkpointWriteStatus{"internal/agent/session.go": writeConfirmed},
		activatedSkills: map[string]bool{},
		toolCounts:      map[string]int{"read_file": 2},
	}
	cp := formatCheckpoint(data, persistentMeta, maxCheckpointChars)
	if strings.Contains(cp, checkpointMetadataTruncated) {
		t.Fatalf("fitting metadata was truncated:\n%s", cp)
	}
	for _, want := range []string{"internal/agent/session.go", "Actions taken: 2 tool calls", "read_file"} {
		if !strings.Contains(cp, want) {
			t.Fatalf("checkpoint lost metadata %q:\n%s", want, cp)
		}
	}
}

// A large partner instruction survives beside oversized metadata: the metadata
// is cut and disclosed, while the partner's message keeps its head and tail
// (the cap yields to partner messages, it never drops one).
func TestFormatCheckpoint_OversizedMetadataKeepsPartnerTask(t *testing.T) {
	task := "TASK_HEAD " + strings.Repeat("t", 50_000) + " TASK_TAIL"
	data := checkpointData{fileWrites: map[string]checkpointWriteStatus{}}
	for i := range 3000 {
		data.fileWrites[fmt.Sprintf("internal/package/file_%04d.go", i)] = writeConfirmed
	}
	data.conversation = []checkpointConversationEntry{{Role: "user", Text: task}}

	cp := formatCheckpoint(data, persistentMeta, maxCheckpointChars)
	if !strings.Contains(cp, checkpointMetadataTruncated) {
		t.Fatalf("oversized metadata was not disclosed as truncated:\n%.300s", cp)
	}
	if !strings.Contains(cp, "TASK_HEAD") || !strings.Contains(cp, "TASK_TAIL") {
		t.Fatalf("the partner's task lost its head or tail:\n%.300s", cp)
	}
	if !strings.HasPrefix(cp, "[CONTEXT CHECKPOINT]\n"+persistentMeta.compactionPointer("checkpoint")) {
		t.Fatalf("checkpoint lost its leading transcript pointer:\n%.300s", cp)
	}
}
