package agent

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/llm"
)

const (
	delegateReportWholeTestMaxChars = 50_000
	delegateReportWithinBudgetChars = 49_400
	delegateReportOverBudgetChars   = 50_400
	reportHeadMarker                = "REPORT_HEAD_MARKER_UNIQUE"
	reportTailMarker                = "REPORT_TAIL_MARKER_UNIQUE"
)

var retainedArtifactRefPattern = regexp.MustCompile(`artifact:[0-9a-f]{32}`)

// reportWithMarkers builds a report of exactly size characters with unique
// head and tail sentinels, so a delivery test can tell a whole report from a
// cut one without reading product copy.
func reportWithMarkers(size int) string {
	fill := size - len(reportHeadMarker) - len(reportTailMarker) - 1
	if fill < 0 {
		panic("report size too small")
	}
	return reportHeadMarker + strings.Repeat("r", fill) + "\n" + reportTailMarker
}

// runLargeDelegateReportDelivery drives the report's real delivery path: a root
// starts a background delegate with the real delegate tool, then sends it a
// second message with a positive wait through the real delegate_send tool. The
// first generation reports a small result and the second reports report, so the
// reply carries the first result ahead of the report.
func runLargeDelegateReportDelivery(t *testing.T, report string) (*Session, string) {
	t.Helper()
	adapter := newMessagingAdapter("Do the first part.")
	adapter.script("root",
		func(llm.Request) llm.Response { return delegateToolResponse("create", "Do the first part.") },
		func(req llm.Request) llm.Response {
			id := deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
			return delegateSendResponse("send", id, "Do the second part.", 60_000)
		})
	adapter.script("Do the first part.",
		func(llm.Request) llm.Response {
			return toolCallResponse(communicateCall("child_first", "first generation report"))
		},
		func(llm.Request) llm.Response {
			return toolCallResponse(communicateCall("child_second", report))
		})

	var sess *Session
	firstReportDeferred := make(chan struct{})
	var deferredOnce sync.Once
	sess = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.testOnly.delegateDeliveryClassified = func(s *Session, deferred bool) {
			if s == sess && deferred {
				deferredOnce.Do(func() { close(firstReportDeferred) })
			}
		}
		cfg.testOnly.delegateSendBeforePositiveWaitAdmission = func() {
			// The first generation's small report must be held for this turn
			// before the send starts, so the send's reply carries it as an
			// earlier result. TRIPWIRE: every answer is scripted in process;
			// only a hang reaches it.
			select {
			case <-firstReportDeferred:
			case <-time.After(10 * time.Second):
			}
		}
	})

	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	requests := adapter.requestsFor("root")
	if len(requests) == 0 {
		t.Fatal("the parent made no model requests")
	}
	sendResult := lastToolResultText(requests[len(requests)-1].Messages)
	if sendResult == "" {
		t.Fatal("the parent never received the delegate_send reply")
	}
	return sess, sendResult
}

// The report's real delivery path is the delegate_send tool result. A report
// whose complete formatted result fits the documented budget must arrive
// whole: both sentinels present, no truncation marker, and no artifact
// reference.
func TestDelegateReportArrivesWholeWithinDeliveryBudget(t *testing.T) {
	t.Parallel()
	report := reportWithMarkers(delegateReportWithinBudgetChars)
	_, sendResult := runLargeDelegateReportDelivery(t, report)
	if delivered := utf8.RuneCountInString(sendResult); delivered > delegateReportWholeTestMaxChars {
		t.Fatalf("fixture delivery is %d characters, over the %d budget; resize the report so this case proves a within-budget delivery", delivered, delegateReportWholeTestMaxChars)
	}
	for _, marker := range []string{reportHeadMarker, reportTailMarker} {
		if !strings.Contains(sendResult, marker) {
			t.Fatalf("delegate_send result lost %s (delivered %d characters)", marker, len(sendResult))
		}
	}
	if strings.Contains(sendResult, "Output truncated") || strings.Contains(sendResult, "Full output: artifact:") {
		t.Fatalf("within-budget report was cut (delivered %d characters):\n%.400s", len(sendResult), sendResult)
	}
	for _, earlier := range []string{"earlier result 1 of 1, not delivered before:", "first generation report"} {
		if !strings.Contains(sendResult, earlier) {
			t.Fatalf("delegate_send result lost the carried earlier result %q", earlier)
		}
	}
	if !strings.Contains(sendResult, report) {
		t.Fatalf("delegate_send result did not carry the report byte-for-byte")
	}
}

// Past the budget, the existing truncation path must remain legible: the
// result names the cut and carries the retained-artifact reference, and the
// artifact read returns the head sentinel the delivered result lost.
func TestDelegateReportPastDeliveryBudgetCarriesRetainedArtifact(t *testing.T) {
	t.Parallel()
	report := reportWithMarkers(delegateReportOverBudgetChars)
	root, sendResult := runLargeDelegateReportDelivery(t, report)
	if !strings.Contains(sendResult, "Output truncated") {
		t.Fatalf("over-budget report was not cut (delivered %d characters)", len(sendResult))
	}
	ref := retainedArtifactRefPattern.FindString(sendResult)
	if ref == "" {
		t.Fatalf("over-budget report carried no retained-artifact reference:\n%.400s", sendResult)
	}
	if !strings.Contains(sendResult, "Read with: read_transcript(transcript_ref=\""+ref+"\")") {
		t.Fatalf("retained-artifact reference is not a read_transcript route:\n%.400s", sendResult)
	}
	if strings.Contains(sendResult, reportHeadMarker) {
		t.Fatalf("fixture delivered the report head, so this case did not exercise the cut")
	}
	if !strings.Contains(sendResult, reportTailMarker) {
		t.Fatalf("truncated result lost the report tail; the existing TruncTail shape did not hold")
	}
	const markerEnd = "]\n\n"
	const truncatedSuffix = "\n[Tool output was truncated.]"
	bodyStart := strings.Index(sendResult, markerEnd)
	bodyEnd := strings.Index(sendResult, truncatedSuffix)
	if bodyStart < 0 || bodyEnd < bodyStart {
		t.Fatalf("truncated result does not carry the owned marker and cut suffix:\n%.400s", sendResult)
	}
	if kept := utf8.RuneCountInString(sendResult[bodyStart+len(markerEnd) : bodyEnd]); kept != delegateReportWholeTestMaxChars {
		t.Fatalf("truncation kept %d characters, want the documented %d", kept, delegateReportWholeTestMaxChars)
	}
	requireArtifactSearch(t, root, ref, reportHeadMarker)
	requireArtifactSearch(t, root, ref, "first generation report")
}

// requireArtifactSearch proves pattern survives in the retained artifact, so
// the delivered result's lost text is recoverable through the real
// read_transcript route.
func requireArtifactSearch(t *testing.T, root *Session, ref, pattern string) {
	t.Helper()
	page := execRead(t, newToolDeps(root), map[string]any{
		"transcript_ref": ref,
		"output_match":   pattern,
	})
	matches, _ := page["matches"].([]any)
	if len(matches) != 1 || page["search_complete"] != true {
		t.Fatalf("artifact search %q = %#v, want one match and a complete search", pattern, page)
	}
}
