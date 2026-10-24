package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
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
	for _, msg := range req.Messages {
		if strings.Contains(msg.Text(), payload) {
			return true
		}
	}
	return false
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
	accepted, observed, consumed := 0, 0, 0
	for _, e := range data.Entries {
		if e.Turn.AttentionID == id {
			accepted++
		}
		if slices.Contains(e.Turn.ObservedNotificationIDs, id) {
			observed++
		}
		if r := e.Turn.AttentionResolution; r != nil && r.AttentionID == id {
			consumed++
		}
	}
	if accepted != 1 || observed < 1 || consumed != 1 {
		t.Fatalf("receiver=%s id=%s accepted=%d successful coverage=%d consumed=%d", sessionID, id, accepted, observed, consumed)
	}
	t.Logf("receiver=%s source=%s accepted=%d successful coverage=%d consumed=%d", sessionID, id, accepted, observed, consumed)
}

func incidentYielded(t *testing.T, owner *Session, childID string) {
	t.Helper()
	sub := owner.getSub(childID)
	if sub == nil {
		t.Fatal("missing real delegate runtime")
	}
	waitForCondition(t, 3*time.Second, "incident safe yield", func() bool { sub.mu.Lock(); defer sub.mu.Unlock(); return !sub.running })
}

func testNotificationIncidentReopen(t *testing.T) {
	const coordKey, workerKey = "INCIDENT-COORD-11", "INCIDENT-WORKER-11"
	const report, result = "report-4d489d7c", "result-b48f7e2a"
	a := newMessagingAdapter(coordKey, workerKey)
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
		return waitResponseFor(fmt.Sprintf("incident-coord-wait-%d", len(a.requestsFor(coordKey))), workerTarget)
	}
	a.script(coordKey, func(llm.Request) llm.Response { return delegateToolResponse("incident-worker", workerKey) }, waitStep)
	a.script(workerKey, func(llm.Request) llm.Response { return incidentShellResponse("incident-shell", fifo) }, func(req llm.Request) llm.Response {
		return waitResponseFor("incident-worker-wait", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
	})
	root := newMessagingRoot(t, a, nil)
	if _, err := root.ProcessInput(context.Background(), "incident-start-11", nil); err != nil {
		t.Fatal(err)
	}
	a.waitForRequests(t, workerKey, 2)
	coord := root.subagents.sessions()[0]
	worker := coord.subagents.sessions()[0]
	incidentYielded(t, coord, worker.ID())
	incidentYielded(t, root, coord.ID())
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
	wake, hint := newDrainWake()
	restored.SetNotifyFunc(hint)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := restored.DrainJobTree(ctx, wake); err != nil {
		t.Fatal(err)
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
	if !incidentRequestHas(wr, jobID) {
		t.Fatal("designated worker request omitted its shell identity")
	}
	incidentNotificationFacts(t, copied, worker.ID(), id)
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
		if child == worker {
			owner = coord.ID()
		}
		incidentNotificationFacts(t, copied, owner, "delegate:"+delivery)
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
