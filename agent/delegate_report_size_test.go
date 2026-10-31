package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
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
// first generation's small report is released at positive-wait admission and
// joined through its real finalization, so canonical custody precedes the send.
// Inspect the next provider request, never a status-only inline placeholder.
func runLargeDelegateReportDelivery(t *testing.T, report string) (*Session, string) {
	t.Helper()
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
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
		},
		func(llm.Request) llm.Response { return finalResponse("reports received") })
	adapter.script("Do the first part.",
		func(llm.Request) llm.Response {
			select {
			case <-releaseFirstReport:
			case <-ctx.Done():
				t.Errorf("first report was never released: %v", ctx.Err())
				return finalResponse("fixture cancelled")
			}
			return toolCallResponse(communicateCall("child_first", "first generation report"))
		},
		func(llm.Request) llm.Response {
			return toolCallResponse(communicateCall("child_second", report))
		})

	var sess *Session
	firstReportAnnounced := make(chan (<-chan struct{}), 1)
	sess = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.testOnly.disableDelegateIdleRelease = true
		cfg.testOnly.subagentBeforeGenerationReleased = func(sub *subagent, lease delegateLease) {
			if lease.generation == 1 {
				sub.mu.Lock()
				done := sub.done
				sub.mu.Unlock()
				firstReportAnnounced <- done
			}
		}
		cfg.testOnly.delegateSendBeforePositiveWaitAdmission = func() {
			release()
			// Announcement follows real receiver custody. Joining done also
			// keeps the successor send behind the generation's release.
			var done <-chan struct{}
			select {
			case done = <-firstReportAnnounced:
			case <-ctx.Done():
				t.Errorf("first report was never announced: %v", ctx.Err())
				return
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Errorf("first report generation never finished: %v", ctx.Err())
				return
			}
			ids, err := sess.pendingNotificationIDs()
			if err != nil || len(ids) != 1 {
				t.Errorf("first report lacks held canonical custody before send: ids=%v err=%v", ids, err)
				return
			}
			fold, err := readDelegateAttentionFold(transcriptPath(sess.stateDir, sess.ID()), sess.ID())
			if err != nil {
				t.Errorf("read held canonical custody: %v", err)
				return
			}
			if !strings.Contains(fold.content[ids[0]].Text(), "first generation report") {
				t.Error("held canonical notification lost the first report")
			}
		}
	})
	toolEnded := make(chan events.ToolCallEndData, 1)
	sess.ConsumeEventsLossless(func(event events.SessionEvent) {
		if end, ok := event.Data.(events.ToolCallEndData); ok && end.CallID == "send" {
			toolEnded <- end
		}
	}, func() {})

	if _, err := sess.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	var end events.ToolCallEndData
	select {
	case end = <-toolEnded:
	case <-ctx.Done():
		t.Fatalf("send tool-end event missing: %v", ctx.Err())
	}
	if end.OutputRef != "" {
		complete := retainedArtifactText(t, sess, end.OutputRef)
		var state delegateSendResult
		decoder := json.NewDecoder(bytes.NewReader(end.ToolState))
		decoder.UseNumber()
		if err := decoder.Decode(&state); err != nil {
			t.Fatal(err)
		}
		if complete != end.Output || complete != formatDelegateSend(state) || len(state.EarlierResults) != 1 || !strings.Contains(complete, report) || !strings.Contains(complete, "first generation report") {
			t.Fatal("real retained artifact differs from complete aggregate tool output/state")
		}
		requireArtifactSearch(t, sess, end.OutputRef, reportHeadMarker)
		requireArtifactSearch(t, sess, end.OutputRef, "first generation report")
		t.Logf("pre-append artifact verified: bytes=%d sha256=%x earlier_results=%d", len(complete), sha256.Sum256([]byte(complete)), len(state.EarlierResults))
	}
	requests := adapter.requestsFor("root")
	if len(requests) != 3 {
		t.Fatalf("parent made %d model requests, want create, send and report presentation", len(requests))
	}
	fold := rootAttentionFold(t, sess)
	if len(fold.order) != 2 || len(fold.pendingIDs()) != 0 {
		t.Fatalf("canonical report settlement: order=%v pending=%v, want two observed reports", fold.order, fold.pendingIDs())
	}
	observations := make(map[string]int)
	for _, entry := range notificationRawEntries(t, transcriptPath(sess.stateDir, sess.ID())) {
		if entry.Turn.Kind == schema.TurnAssistant {
			for _, id := range entry.Turn.ObservedNotificationIDs {
				observations[id]++
			}
		}
	}
	for _, id := range fold.order {
		if observations[id] != 1 {
			t.Fatalf("canonical report %s has %d synced response observations, want exactly one", id, observations[id])
		}
	}
	var sendResult string
	for _, message := range requests[len(requests)-1].Messages {
		for _, part := range message.Content {
			if part.ToolResult != nil && part.ToolResult.ToolCallID == "send" && part.ToolResult.Name == "delegate_send" {
				if sendResult != "" {
					t.Fatal("duplicate send result in actual provider request")
				}
				sendResult, _ = part.ToolResult.Content.(string)
			}
		}
	}
	if strings.Contains(sendResult, report) || retainedArtifactRefPattern.MatchString(sendResult) {
		// A complete inline result is itself presentation. Canonical records
		// must not be presented a second time alongside this projection.
		var bound []string
		for _, entry := range notificationRawEntries(t, transcriptPath(sess.stateDir, sess.ID())) {
			for _, commit := range entry.Turn.DelegateDeliveryCommits {
				if commit.ToolCallID == "send" {
					bound = append(bound, delegateAttentionID(commit.DeliveryID))
				}
			}
		}
		if len(bound) != len(fold.order) {
			t.Fatalf("send carrier bindings=%v, want %v", bound, fold.order)
		}
		for i, id := range fold.order {
			if bound[i] != id {
				t.Fatalf("send carrier binding order=%v, want %v", bound, fold.order)
			}
			for _, message := range requests[len(requests)-1].Messages {
				if message.Role == llm.RoleUser && message.Text() == fold.content[id].Text() {
					t.Fatalf("canonical %s duplicated the selected inline carrier", id)
				}
			}
		}
		return sess, sendResult
	}
	// A shaped inline result is status only. Locate the actual canonical
	// records by their durable identity/content, in provider presentation order.
	var presented []string
	var presentedIDs []string
	for _, message := range requests[len(requests)-1].Messages {
		if message.Role != llm.RoleUser {
			continue
		}
		for _, id := range fold.order {
			if message.Text() == fold.content[id].Text() {
				presented = append(presented, message.Text())
				presentedIDs = append(presentedIDs, id)
			}
		}
	}
	if len(presented) != 2 {
		t.Fatalf("provider received %d canonical reports, want both held generations", len(presented))
	}
	for i, id := range fold.order {
		if presentedIDs[i] != id {
			t.Fatalf("provider report order=%v, want canonical order=%v", presentedIDs, fold.order)
		}
	}
	return sess, strings.Join(presented, "\n")
}

// The actual selected provider presentation, inline or canonical fallback,
// must preserve a complete formatted reply that fits the documented budget:
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
		t.Fatalf("over-budget report carried no retained-artifact capability (presented_chars=%d head=%t tail=%t earlier=%t):\n%.400s", utf8.RuneCountInString(sendResult), strings.Contains(sendResult, reportHeadMarker), strings.Contains(sendResult, reportTailMarker), strings.Contains(sendResult, "first generation report"), sendResult)
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
	delegateID, _, _ := strings.Cut(strings.TrimPrefix(rootAttentionFold(t, root).order[0], "delegate:"), "/delivery/")
	aggregate := delegateAggregateSnapshot(t, root.delegateController, delegateID)
	child := root.getSub(aggregate.Descriptor.ChildSessionID)
	if child == nil || child.sess == nil {
		t.Fatal("real delegate runtime unavailable for descendant recovery proof")
	}
	if got := retainedArtifactText(t, child.sess, ref); got != complete {
		t.Fatal("same-root descendant did not recover exact aggregate bytes")
	}
	child.sess.Close()
	if got := retainedArtifactText(t, root, ref); got != complete {
		t.Fatal("closing the child expired the live root's artifact capability")
	}
	root.Close()
	if _, err := execReadTranscript(newToolDeps(root), map[string]any{"transcript_ref": ref}); err == nil || !strings.Contains(err.Error(), "artifact_expired") {
		t.Fatalf("root-close artifact read=%v, want artifact_expired", err)
	}
}

// retainedArtifactText reads the complete formatted reply the retained-artifact
// capability holds.
func retainedArtifactText(t *testing.T, root *Session, ref string) string {
	t.Helper()
	complete, err := readRetainedArtifactText(root, ref)
	if err != nil {
		t.Fatal(err)
	}
	return complete
}

// Worker setup returns errors rather than calling Fatal, which would exit the
// worker before its caller can join completion. Both callers use the real tool.
func readRetainedArtifactText(root *Session, ref string) (string, error) {
	var complete strings.Builder
	for offset := 0; ; {
		value, err := execReadTranscript(newToolDeps(root), map[string]any{"transcript_ref": ref, "offset_bytes": float64(offset)})
		if err != nil {
			return "", err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		var page map[string]any
		if err := json.Unmarshal(encoded, &page); err != nil {
			return "", err
		}
		body, ok := page["page"].(map[string]any)
		if !ok {
			return "", fmt.Errorf("missing artifact page: %#v", page)
		}
		data, ok := body["data"].(string)
		if !ok || body["offset_bytes"] != float64(offset) || body["bytes_returned"] != float64(len(data)) || page["retained_start_bytes"] != float64(0) {
			return "", fmt.Errorf("invalid exact artifact page: offset=%v (%T) want=%d returned=%v (%T) data_bytes=%d retained_start=%v (%T)", body["offset_bytes"], body["offset_bytes"], offset, body["bytes_returned"], body["bytes_returned"], len(data), page["retained_start_bytes"], page["retained_start_bytes"])
		}
		complete.WriteString(data)
		offset += len(data)
		if float64(offset) == body["total_bytes"] {
			return complete.String(), nil
		}
		continuation, ok := page["continuation"].(map[string]any)
		if !ok || continuation["offset_bytes"] != float64(offset) || len(data) == 0 {
			return "", fmt.Errorf("invalid artifact continuation: %#v", page)
		}
	}
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
