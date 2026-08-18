package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
)

// TestReadTranscriptDescriptionNamesRetainedOperationRules keys the tool's own
// words to the ref-kind dispatch rules. A model reads the description, not the
// executor, so every operation and compatibility exception must be explicit.
func TestReadTranscriptDescriptionNamesRetainedOperationRules(t *testing.T) {
	t.Parallel()
	desc := tool.DefReadTranscript().Description
	for _, want := range []string{
		"artifact:", "job:", "session ref", "output_match", "context_lines",
		"offset_bytes", "range", "expand_turn", "format", "16 KiB",
		"retained_start_bytes", "job_status",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("read_transcript description does not name %q:\n%s", want, desc)
		}
	}
}

// TestReadTranscriptDescriptionAdmitsTheWatchFrameAsARefSource pins the second
// thing the shipped description hid. An observer's watch frame carries the call
// verbatim (appendWatchFrameJobRead), while job_status remains scoped. A description
// that names job_status/job_list as the sources tells that observer its ref
// came from somewhere it cannot reach (kata w2fk).
func TestReadTranscriptDescriptionAdmitsTheWatchFrameAsARefSource(t *testing.T) {
	t.Parallel()
	desc := tool.DefReadTranscript().Description
	if !strings.Contains(desc, "watch frame") {
		t.Fatalf("read_transcript description = %q, want the watch frame named as a job: ref source", desc)
	}
	// The frame's own wording is the contract the description has to match: it
	// hands the observer a job: ref, not a session ref.
	frame := appendWatchFrameJobRead("watched something\n", "job_abc")
	if !strings.Contains(frame, `read_transcript(transcript_ref="job:job_abc")`) {
		t.Fatalf("watch frame = %q, want it to teach the job: read this description describes", frame)
	}
}
