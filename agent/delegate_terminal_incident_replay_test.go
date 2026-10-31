package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// The provider scripts choose tools, never answer for the controller. The copy
// contains both the delegate/shell source journals and every receiver transcript.
func TestNotificationIncidentReplay(t *testing.T) {
	t.Parallel()
	t.Run("shell interruption after reopen", testNotificationIncidentReopen)
	t.Run("report while sibling still running", testNotificationIncidentSibling)
}

func incidentShellResponse(id, fifo string) llm.Response {
	args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
	return toolCallResponse(llm.ToolCallData{ID: id, Name: "shell", Type: "function", Arguments: args})
}

func incidentRequestHas(req llm.Request, payload string) bool {
	for _, frame := range incidentFrames(req) {
		if strings.Contains(frame.body, payload) {
			return true
		}
	}
	return false
}

type incidentFrame struct {
	tag, body string
	attrs     map[string]string
}

var incidentFramePattern = regexp.MustCompile(`(?s)<(job-notification|delegate-notification)\s+([^>]*)>(.*?)</(?:job-notification|delegate-notification)>`)
var incidentAttrPattern = regexp.MustCompile(`([a-z_]+)="([^"]*)"`)

func TestIncidentFrameBodyDecodesOnlyNotificationEntities(t *testing.T) {
	t.Parallel()
	frames := incidentFrames(llm.Request{Messages: []llm.Message{llm.User(
		`<delegate-notification delegate_id="dlg_entity" kind="report">&lt;|&amp;|&amp;lt;|&gt;|&quot;|&#65;|&#x41;</delegate-notification>`,
	)}})
	if len(frames) != 1 || frames[0].body != `<|&|&lt;|&gt;|&quot;|&#65;|&#x41;` {
		t.Fatalf("incident frames = %+v", frames)
	}
}

// Notification frames are user-role text, not tool results or assistant
// tool arguments. Delegate delivery does not set the Machinery flag.
func incidentFrames(req llm.Request) []incidentFrame {
	var frames []incidentFrame
	for _, msg := range req.Messages {
		if msg.Role != llm.RoleUser {
			continue
		}
		for _, part := range msg.Content {
			if part.Kind != llm.ContentText {
				continue
			}
			for _, match := range incidentFramePattern.FindAllStringSubmatch(part.Text, -1) {
				// This oracle is independent of the production codec. Body entities
				// are restricted and decoded once, unlike HTML attributes below.
				body := strings.NewReplacer("&lt;", "<", "&amp;", "&").Replace(match[3])
				frame := incidentFrame{tag: match[1], body: body, attrs: map[string]string{}}
				for _, attr := range incidentAttrPattern.FindAllStringSubmatch(match[2], -1) {
					frame.attrs[attr[1]] = html.UnescapeString(attr[2])
				}
				frames = append(frames, frame)
			}
		}
	}
	return frames
}

func incidentJobPayload(req llm.Request, jobID, status, reason string) error {
	for _, frame := range incidentFrames(req) {
		if frame.tag == "job-notification" && frame.attrs["job_id"] == jobID && frame.attrs["job_type"] == "shell" && frame.attrs["status"] == status && frame.attrs["event"] == status && frame.attrs["reason"] == reason && strings.TrimSpace(frame.body) != "" {
			return nil
		}
	}
	return fmt.Errorf("notification body missing shell=%s status=%s reason=%s, frames=%+v", jobID, status, reason, incidentFrames(req))
}

func incidentDelegatePayload(req llm.Request, delegateID, payload string) bool {
	for _, frame := range incidentFrames(req) {
		if frame.tag == "delegate-notification" && frame.attrs["delegate_id"] == delegateID && frame.attrs["kind"] == "report" {
			var packet struct {
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(frame.body), &packet) == nil && (payload == "" || packet.Message == payload) {
				return true
			}
		}
	}
	return false
}

func incidentWatchFields(req llm.Request, watchID string) map[string]string {
	for _, msg := range req.Messages {
		if msg.Role != llm.RoleUser {
			continue
		}
		for _, part := range msg.Content {
			if part.Kind != llm.ContentText || !part.Machinery {
				continue
			}
			_, frame, ok := strings.Cut(part.Text, "Watch frame\n")
			if !ok {
				continue
			}
			fields := map[string]string{}
			inEvent := false
			for line := range strings.SplitSeq(frame, "\n") {
				if line == "event:" {
					inEvent = true
					continue
				}
				key, value, ok := strings.Cut(line, ": ")
				if !ok {
					continue
				}
				if inEvent && strings.HasPrefix(key, "  ") {
					fields["event."+strings.TrimSpace(key)] = value
				} else if !strings.HasPrefix(key, " ") {
					fields[key] = value
				}
			}
			if fields["watch_id"] == watchID {
				return fields
			}
		}
	}
	return nil
}

// This counts physical records, not the production attention fold. Historical
// coverage can appear in multiple requests, but acceptance and consumption must
// each occur exactly once for this immutable source identity.
func incidentNotificationFacts(t *testing.T, stateDir, sessionID, id string) {
	t.Helper()
	data, err := readTranscriptFull(transcriptPath(stateDir, sessionID), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := incidentCustodyError(data.Entries, id); err != nil {
		t.Fatalf("receiver=%s: %v", sessionID, err)
	}
}

func incidentCustodyError(entries []transcript.Entry, id string) error {
	accepted, consumed, other := 0, 0, 0
	for _, e := range entries {
		if e.Turn.AttentionID == id {
			accepted++
		}
		if r := e.Turn.AttentionResolution; r != nil && r.AttentionID == id {
			if r.Disposition == "consumed" {
				consumed++
			} else {
				other++
			}
		}
	}
	if accepted != 1 || consumed != 1 || other != 0 {
		return fmt.Errorf("source=%s accepted=%d consumed=%d non-consumed=%d", id, accepted, consumed, other)
	}
	return nil
}

// Keys name the communicate response returned for a designated captured request.
// Check its entire receipt set, and forbid tracked IDs on any other response.
// By default all expected IDs are tracked. Callers with older historical updates
// track the new terminal ID, without forbidding earlier coverage of old updates.
func incidentCoverageError(entries []transcript.Entry, expected map[string][]string, tracked ...string) error {
	seen := map[string]int{}
	if len(tracked) == 0 {
		for _, ids := range expected {
			tracked = append(tracked, ids...)
		}
	}
	for _, entry := range entries {
		turn := entry.Turn
		if turn.Kind != schema.TurnAssistant {
			continue
		}
		marker := ""
		for _, part := range turn.Message.Content {
			if call := part.ToolCall; call != nil && call.Name == "communicate" {
				var args struct {
					Message string `json:"message"`
				}
				if err := json.Unmarshal(call.Arguments, &args); err != nil {
					return err
				}
				marker = args.Message
			}
		}
		want, designated := expected[marker]
		if designated {
			seen[marker]++
			got := slices.Clone(turn.ObservedNotificationIDs)
			want = slices.Clone(want)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				return fmt.Errorf("response=%s observed=%v want=%v", marker, got, want)
			}
		} else {
			for _, id := range turn.ObservedNotificationIDs {
				if slices.Contains(tracked, id) {
					return fmt.Errorf("source=%s covered by unexpected response=%q", id, marker)
				}
			}
		}
	}
	for marker := range expected {
		if seen[marker] != 1 {
			return fmt.Errorf("response=%s physical records=%d want1", marker, seen[marker])
		}
	}
	return nil
}

func incidentCopy[T any](t *testing.T, value T) T {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var cloned T
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

// These are mutations of captured evidence, not runtime failures. The source
// journals, receiver files and provider requests themselves stay untouched.
func incidentFalsifyOracles(t *testing.T, req llm.Request, entries []transcript.Entry, jobID, id, response string) {
	t.Helper()
	expected := map[string][]string{response: {id}}
	t.Run("oracle rejects missing payload with setup retained", func(t *testing.T) {
		cloned := incidentCopy(t, req)
		setupRetained, removed := false, false
		for i := range cloned.Messages {
			msg := &cloned.Messages[i]
			if msg.Role != llm.RoleUser {
				raw, err := json.Marshal(msg)
				if err != nil {
					t.Fatal(err)
				}
				setupRetained = setupRetained || strings.Contains(string(raw), jobID)
				continue
			}
			for j := range msg.Content {
				part := &msg.Content[j]
				if part.Kind == llm.ContentText && strings.Contains(part.Text, "<job-notification ") && strings.Contains(part.Text, jobID) {
					part.Text = incidentFramePattern.ReplaceAllString(part.Text, "")
					removed = true
				}
			}
		}
		if !removed || !setupRetained {
			t.Fatal("mutation did not preserve actual shell setup history")
		}
		if incidentJobPayload(cloned, jobID, "stopped", "runtime_lost") == nil {
			t.Fatal("missing payload survived strengthened oracle")
		}
		if err := incidentCoverageError(entries, expected); err != nil {
			t.Fatal("unmodified receipts must still pass", err)
		}
		t.Log("removed delivered shell payload, retained setup job ID and valid receipts, payload oracle rejected copy")
	})
	t.Run("oracle rejects discarded disposition", func(t *testing.T) {
		cloned := incidentCopy(t, entries)
		changed := 0
		for i := range cloned {
			if r := cloned[i].Turn.AttentionResolution; r != nil && r.AttentionID == id {
				r.Disposition = "discarded"
				changed++
			}
		}
		if changed != 1 {
			t.Fatalf("changed resolutions=%d", changed)
		}
		if incidentCustodyError(cloned, id) == nil {
			t.Fatal("discarded notification counted as consumed")
		}
		t.Log("changed consumed to discarded with acceptance and coverage retained, custody oracle rejected copy")
	})
	for _, mutation := range []string{"extra coverage ID", "coverage on wrong response"} {
		t.Run("oracle rejects "+mutation, func(t *testing.T) {
			cloned := incidentCopy(t, entries)
			covered, earlier := -1, -1
			for i, entry := range cloned {
				if earlier == -1 && entry.Turn.Kind == schema.TurnAssistant {
					earlier = i
				}
				if slices.Contains(entry.Turn.ObservedNotificationIDs, id) {
					if covered != -1 {
						t.Fatal("fixture has multiple source responses")
					}
					covered = i
				}
			}
			if covered < 0 || earlier < 0 || covered == earlier {
				t.Fatal("missing distinct actual response records")
			}
			if mutation == "extra coverage ID" {
				cloned[covered].Turn.ObservedNotificationIDs = append(cloned[covered].Turn.ObservedNotificationIDs, "foreign-notification-11")
			} else {
				cloned[covered].Turn.ObservedNotificationIDs = nil
				cloned[earlier].Turn.ObservedNotificationIDs = []string{id}
			}
			if err := incidentCustodyError(cloned, id); err != nil {
				t.Fatal("acceptance and consumption changed", err)
			}
			if incidentCoverageError(cloned, expected) == nil {
				t.Fatal("corrupt response coverage survived exact oracle")
			}
			t.Logf("%s with acceptance and consumption retained, exact response oracle rejected copy", mutation)
		})
	}
}

func incidentCoverage(t *testing.T, stateDir, sessionID string, expected map[string][]string, tracked ...string) {
	t.Helper()
	entries := notificationRawEntries(t, transcriptPath(stateDir, sessionID))
	if err := incidentCoverageError(entries, expected, tracked...); err != nil {
		t.Fatal(err)
	}
}

// Historical updates can legitimately recur. Map physical accepted payloads
// to this explicit request, without consulting receipt IDs or the runtime fold.
func incidentPresentedIDs(t *testing.T, stateDir, sessionID string, req llm.Request, required ...string) []string {
	t.Helper()
	var ids []string
	for _, entry := range notificationRawEntries(t, transcriptPath(stateDir, sessionID)) {
		if entry.Turn.AttentionID == "" {
			continue
		}
		body := entry.Turn.Message.Text()
		for _, msg := range req.Messages {
			if msg.Role != llm.RoleUser {
				continue
			}
			found := false
			for _, part := range msg.Content {
				if part.Kind == llm.ContentText && body != "" && strings.Contains(part.Text, body) {
					found = true
				}
			}
			if found {
				ids = append(ids, entry.Turn.AttentionID)
				break
			}
		}
	}
	for _, id := range required {
		if !slices.Contains(ids, id) {
			t.Fatalf("designated request omitted accepted payload %s", id)
		}
	}
	return ids
}

func incidentYielded(t *testing.T, owner *Session, childID string, receipt *activationReturn) {
	t.Helper()
	sub := owner.getSub(childID)
	if sub == nil {
		t.Fatal("missing real delegate runtime")
	}
	if receipt.sessionID != childID || receipt.delegateID != sub.sess.owningDelegateID {
		t.Fatalf("yield activation identity = %+v", receipt)
	}
	waitForTestSignal(t, receipt.returned, "incident yielded activation return")
	sub.mu.Lock()
	running := sub.running
	sub.mu.Unlock()
	if running {
		t.Fatal("incident executor still running after yield return")
	}
	run := delegateAggregateSnapshot(t, owner.delegateController, receipt.delegateID)
	if !run.CurrentRunOpen || run.Generation != receipt.lease.generation {
		t.Fatalf("yield generation = %+v", run)
	}
	activationStillOpen(t, receipt.generationDone)
}

func incidentSettled(t *testing.T, owner *Session, childID string, receipt *activationReturn) {
	t.Helper()
	waitForTestSignal(t, receipt.generationDone, "incident terminal generation")
	waitForTestSignal(t, receipt.returned, "incident terminal activation return")
	sub := owner.getSub(childID)
	if sub == nil || receipt.sessionID != childID || receipt.delegateID != sub.sess.owningDelegateID {
		t.Fatalf("terminal activation identity = %+v", receipt)
	}
	sub.mu.Lock()
	running := sub.running
	sub.mu.Unlock()
	if running {
		t.Fatal("incident executor still running after terminal return")
	}
	if run := delegateAggregateSnapshot(t, owner.delegateController, receipt.delegateID); run.CurrentRunOpen || run.Generation != receipt.lease.generation {
		t.Fatalf("terminal generation = %+v", run)
	}
}

func testNotificationIncidentReopen(t *testing.T) {
	const coordKey, workerKey = "INCIDENT-COORD-11", "INCIDENT-WORKER-11"
	const report, result = "report-4d489d7c", "result-b48f7e2a"
	a := newMessagingAdapter(coordKey, workerKey)
	returns := newActivationReturns()
	workerGate := make(chan struct{})
	releaseWorker := sync.OnceFunc(func() { close(workerGate) })
	defer releaseWorker()
	coordWaiting, coordRelease := make(chan struct{}, 4), make(chan struct{}, 4)
	defer close(coordRelease)
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	a.script("root", func(llm.Request) llm.Response { return delegateToolResponse("incident-coordinator", coordKey) }, func(llm.Request) llm.Response { return finalResponse("initial-root-11") })
	var workerTarget string
	var waitStep func(llm.Request) llm.Response
	coordReport := make(chan llm.Request, 1)
	waitStep = func(req llm.Request) llm.Response {
		if incidentRequestHas(req, report) {
			coordReport <- req
			return finalResponse(result)
		}
		if workerTarget == "" {
			workerTarget = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
		}
		a.script(coordKey, waitStep)
		coordWaiting <- struct{}{}
		awaitSignal(t, coordRelease, "incident coordinator activation capture")
		return waitResponseFor(fmt.Sprintf("incident-coord-wait-%d", len(a.requestsFor(coordKey))), workerTarget)
	}
	a.script(coordKey, func(llm.Request) llm.Response { return delegateToolResponse("incident-worker", workerKey) }, waitStep)
	a.script(workerKey, func(llm.Request) llm.Response { return incidentShellResponse("incident-shell", fifo) }, func(req llm.Request) llm.Response {
		awaitSignal(t, workerGate, "incident worker activation capture")
		return waitResponseFor("incident-worker-wait", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
	})
	root := newMessagingRoot(t, a, returns.configure)
	t.Cleanup(func() { returns.close(t, root) })
	if _, err := root.ProcessInput(context.Background(), "incident-start-11", nil); err != nil {
		t.Fatal(err)
	}
	a.waitForRequests(t, workerKey, 2)
	coord := root.subagents.sessions()[0]
	worker := coord.subagents.sessions()[0]
	workerActivation := returns.take(t, worker.ID())
	waitForTestSignal(t, coordWaiting, "initial incident coordinator wait")
	initialCoord := returns.take(t, coord.ID())
	coordRelease <- struct{}{}
	incidentYielded(t, root, coord.ID(), initialCoord)
	releaseWorker()
	incidentYielded(t, coord, worker.ID(), workerActivation)
	// The worker's waiting update resumes the coordinator in the same lease.
	waitForTestSignal(t, coordWaiting, "incident coordinator worker-update response")
	observingCoord := returns.take(t, coord.ID())
	if observingCoord.lease != initialCoord.lease {
		t.Fatalf("incident coordinator replaced its lease: %+v -> %+v", initialCoord, observingCoord)
	}
	coordRelease <- struct{}{}
	incidentYielded(t, root, coord.ID(), observingCoord)
	// Observe only real already-produced coordinator messages before the cut.
	a.script("root", func(llm.Request) llm.Response { return finalResponse("initial-notices-11") })
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	for _, child := range []*Session{coord, worker} {
		cp, err := readWaitCheckpoint(transcriptPath(root.stateDir, child.ID()), child.ID())
		if err != nil || cp == nil || cp.ResumedByTurnID != "" {
			t.Fatalf("precrash checkpoint=%+v err=%v", cp, err)
		}
		run := delegateAggregateSnapshot(t, root.delegateController, child.owningDelegateID)
		if run.Generation != 1 || !run.CurrentRunOpen || run.LatestOutcome != nil {
			t.Fatalf("precrash generation=%+v", run)
		}
	}
	workerJobs := producerSourceEvents(t, worker.jobManager, jobstore.EventJobStarted)
	if len(workerJobs) != 1 {
		t.Fatalf("shell starts=%d", len(workerJobs))
	}
	jobID := workerJobs[0].JobID
	copied := copyWaitingState(t, root.stateDir)
	root.Close()
	releaseShell()
	workerRequest, rootRequest := make(chan llm.Request, 1), make(chan llm.Request, 1)
	a.script(workerKey, func(req llm.Request) llm.Response { workerRequest <- req; return finalResponse(report) })
	var rootStep func(llm.Request) llm.Response
	rootStep = func(req llm.Request) llm.Response {
		if incidentRequestHas(req, result) {
			rootRequest <- req
		} else {
			a.script("root", rootStep)
		}
		return finalResponse("observed-root-11")
	}
	a.script("root", rootStep)
	restored := restoreWaitingCopy(t, root, copied)
	// The restored root has not admitted a child executor yet. Its descendants
	// inherit this factory when the addressed drain restores them.
	restored.cfg.testOnly.subagentActivationReturn = returns.capture
	t.Cleanup(func() { returns.close(t, restored) })
	wake, hint := newDrainWake()
	restored.SetNotifyFunc(hint)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	var drainErr error
	t.Cleanup(func() {
		cancel()
		awaitSignal(t, finished, "restored incident drain cleanup")
	})
	go func() {
		defer close(finished)
		_, drainErr = restored.DrainJobTree(ctx, wake)
	}()
	waitForTestSignal(t, finished, "restored incident drain return")
	if drainErr != nil {
		t.Fatal(drainErr)
	}
	var wr, cr, rr llm.Request
	select {
	case wr = <-workerRequest:
	default:
		t.Fatal("repaired shell did not wake exact worker")
	}
	select {
	case cr = <-coordReport:
	default:
		t.Fatal("worker report did not wake coordinator")
	}
	select {
	case rr = <-rootRequest:
	default:
		t.Fatal("coordinator report did not reach root")
	}
	if wr.SessionID != worker.ID() || cr.SessionID != coord.ID() || rr.SessionID != root.ID() {
		t.Fatal("notification reached wrong session")
	}
	restoredCoord := restored.getSub(coord.ID())
	if restoredCoord == nil {
		t.Fatal("coordinator was not restored")
	}
	restoredWorker := restoredCoord.sess.getSub(worker.ID())
	if restoredWorker == nil {
		t.Fatal("worker was not restored")
	}
	terminal := producerSourceEvents(t, restoredWorker.sess.jobManager, jobstore.EventJobFinished)
	if len(terminal) != 1 || terminal[0].JobID != jobID {
		t.Fatalf("recovered real source completions=%+v", terminal)
	}
	id := stableShellAttentionID(jobID, terminal[0].TerminalGen)
	if terminal[0].Status != jobstore.StatusStopped || terminal[0].Reason != "runtime_lost" {
		t.Fatalf("recovered interruption=%+v", terminal[0])
	}
	if err := incidentJobPayload(wr, jobID, "stopped", "runtime_lost"); err != nil {
		t.Fatal(err)
	}
	incidentNotificationFacts(t, copied, worker.ID(), id)
	incidentCoverage(t, copied, worker.ID(), map[string][]string{report: {id}})
	incidentFalsifyOracles(t, wr, notificationRawEntries(t, transcriptPath(copied, worker.ID())), jobID, id, report)
	requireNotificationState(t, restoredWorker.sess.jobManager, jobID, jobstore.NotifyDelivered)
	for _, child := range []*Session{worker, coord} {
		starts, finishes := 0, 0
		var delivery string
		for _, e := range publicationEvents(t, restored) {
			if e.DelegateID != child.owningDelegateID {
				continue
			}
			if e.RunStarted != nil {
				starts++
				if e.RunStarted.Generation != 1 {
					t.Fatal("restart replaced generation")
				}
			}
			if e.RunFinished != nil {
				finishes++
				delivery = e.RunFinished.DeliveryID
				if e.RunFinished.Generation != 1 || e.RunFinished.Outcome.Status != delegatestore.OutcomeCompleted {
					t.Fatalf("settlement=%+v", e.RunFinished)
				}
			}
		}
		if starts != 1 || finishes != 1 || delivery == "" {
			t.Fatalf("delegate=%s starts=%d finishes=%d delivery=%s", child.owningDelegateID, starts, finishes, delivery)
		}
		owner := root.ID()
		req, response, payload := rr, "observed-root-11", result
		if child == worker {
			owner = coord.ID()
			req, response, payload = cr, result, report
		}
		if !incidentDelegatePayload(req, child.owningDelegateID, payload) {
			t.Fatal("designated report request omitted exact source and packet message")
		}
		notificationID := "delegate:" + delivery
		incidentNotificationFacts(t, copied, owner, notificationID)
		expected := incidentPresentedIDs(t, copied, owner, req, notificationID)
		incidentCoverage(t, copied, owner, map[string][]string{response: expected}, notificationID)
		cp, err := readWaitCheckpoint(transcriptPath(copied, child.ID()), child.ID())
		if err != nil || cp == nil || cp.ResumedByTurnID == "" {
			t.Fatalf("missing original-generation resume claim: %+v %v", cp, err)
		}
	}
}

func testNotificationIncidentSibling(t *testing.T) {
	// The existing real provider fixture holds B until A's report is consumed.
	// It runs real delegates and checks exact requests and the report's raw ID.
	notificationReportWakesParent(t, false)
}
