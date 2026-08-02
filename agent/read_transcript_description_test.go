package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/tool"
)

// TestReadTranscriptDescriptionNamesEveryJobRefRejection keys the tool's own
// words to the code that enforces them. A model reads the description, not
// session_tools_transcript.go: a rejection the description does not name is a
// call the model makes once, gets invalid_request for, and has to repair — and
// on a watch grant, with no job_status to fall back on, there is nothing else
// to try. Adding a fifth entry to jobRefRejectedParams fails this test until
// the description admits it (kata w2fk).
func TestReadTranscriptDescriptionNamesEveryJobRefRejection(t *testing.T) {
	t.Parallel()
	desc := tool.DefReadTranscript().Description
	for _, name := range jobRefRejectedParams {
		if !strings.Contains(desc, name) {
			t.Fatalf("read_transcript description does not name the rejected job: parameter %q:\n%s", name, desc)
		}
	}
	if !strings.Contains(desc, "invalid_request") {
		t.Fatalf("read_transcript description names the rejected parameters without naming the error they raise (%q):\n%s",
			"invalid_request", desc)
	}
}

// TestReadTranscriptDescriptionTeachesBothJobKinds pins the fact the shipped
// description hid: a job: ref serves DELEGATE jobs too, and
// renderDelegateJobTranscript gives them their own heading, report, and
// structured_result. Calling the ref "shell output logs" left a model with no
// reason to spend it on a delegate's report — the surface that replaced
// job_read_output for exactly that job (kata w2fk).
func TestReadTranscriptDescriptionTeachesBothJobKinds(t *testing.T) {
	t.Parallel()
	desc := tool.DefReadTranscript().Description
	for _, want := range []string{"delegate job", "structured_result"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("read_transcript description = %q, want it to teach the delegate job: read (%q)", desc, want)
		}
	}
}

// TestReadTranscriptDescriptionAdmitsTheWatchFrameAsARefSource pins the second
// thing the shipped description hid. A granted observer's watch frame carries
// the call verbatim (appendWatchFrameJobRead) and job_status on that job is
// denied, so the frame is the observer's ONLY source for the ref. A description
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
