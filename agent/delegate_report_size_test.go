package agent

import (
	"context"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/agenttest"
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
// first generation's small report is released only after the send has begun
// waiting, so it is held for the parent's turn and carried ahead of the report.
func runLargeDelegateReportDelivery(t *testing.T, report string) (*Session, string) {
	t.Helper()
	releaseFirstReport := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirstReport) }) }
	t.Cleanup(release)

	adapter := newMessagingAdapter("Do the first part.")
	adapter.script("root",
		func(llm.Request) llm.Response { return delegateToolResponse("create", "Do the first part.") },
		func(req llm.Request) llm.Response {
			id := deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
			return delegateSendResponse("send", id, "Do the second part.", 60_000)
		})
	adapter.script("Do the first part.",
		func(llm.Request) llm.Response {
			<-releaseFirstReport
			return agenttest.ToolCallResponse(communicateCall("child_first", "first generation report"))
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("child_second", report))
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
			release()
			// The first generation's report must be held for this turn before
			// the send starts, so the send's reply carries it as an earlier
			// result. TRIPWIRE: every answer is scripted in process; only a
			// hang reaches it.
			select {
			case <-firstReportDeferred:
			case <-time.After(10 * time.Second): // TRIPWIRE: the scripted in-process child reports in milliseconds; only a genuine hang reaches this.
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
// whole: both sentinels present, no retained-artifact capability, and the
// earlier report still ahead of it.
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
	if ref := retainedArtifactRefPattern.FindString(sendResult); ref != "" {
		t.Fatalf("within-budget report was cut and retained at %s", ref)
	}
	earlierAt := strings.Index(sendResult, "first generation report")
	reportAt := strings.Index(sendResult, report)
	if earlierAt < 0 || reportAt < 0 || earlierAt > reportAt {
		t.Fatalf("delegate_send result did not carry the earlier report ahead of the report (earlier=%d report=%d)", earlierAt, reportAt)
	}
	if !strings.Contains(sendResult, report) {
		t.Fatalf("delegate_send result did not carry the report exactly")
	}
}

// Past the budget, the retained-artifact route must stay recoverable: the
// delivered text is the truncated tail of the complete reply, the artifact
// holds the complete reply, and the lost head and earlier report are findable
// through the real read_transcript search.
func TestDelegateReportPastDeliveryBudgetCarriesRetainedArtifact(t *testing.T) {
	t.Parallel()
	report := reportWithMarkers(delegateReportOverBudgetChars)
	root, sendResult := runLargeDelegateReportDelivery(t, report)
	ref := retainedArtifactRefPattern.FindString(sendResult)
	if ref == "" {
		t.Fatalf("over-budget report carried no retained-artifact capability:\n%.400s", sendResult)
	}
	if strings.Contains(sendResult, reportHeadMarker) {
		t.Fatalf("fixture delivered the report head, so this case did not exercise the cut")
	}
	if !strings.Contains(sendResult, reportTailMarker) {
		t.Fatalf("truncated result lost the report tail; the existing TruncTail shape did not hold")
	}
	complete := retainedArtifactText(t, root, ref)
	if kept := retainedSuffixLength(complete, sendResult); kept != delegateReportWholeTestMaxChars {
		t.Fatalf("truncation kept %d characters, want the documented %d", kept, delegateReportWholeTestMaxChars)
	}
	requireArtifactSearch(t, root, ref, reportHeadMarker)
	requireArtifactSearch(t, root, ref, "first generation report")
}

// retainedArtifactText reads the complete formatted reply the retained-artifact
// capability holds.
func retainedArtifactText(t *testing.T, root *Session, ref string) string {
	t.Helper()
	file, err := root.artifactStore.Open(ref)
	if err != nil {
		t.Fatalf("open retained artifact: %v", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read retained artifact: %v", err)
	}
	return string(data)
}

// retainedSuffixLength returns the longest suffix of complete that appears in
// delivered. With TruncTail that is exactly the delivered character budget.
func retainedSuffixLength(complete, delivered string) int {
	runes := []rune(complete)
	low, high, best := 1, len(runes), 0
	for low <= high {
		mid := (low + high) / 2
		if strings.Contains(delivered, string(runes[len(runes)-mid:])) {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return best
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
