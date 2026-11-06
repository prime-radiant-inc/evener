package server

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Pairing must wait for complete: a read-created idle history has no recorded
// hook, and a restore can bind its producer but fail before attachTranscript.
func TestHistoryPublicationWaitsForRestoreAttach(t *testing.T) {
	const managerInput = "restore-manager-391"
	responses := make(chan llm.Response, 8)
	client := llm.NewClient()
	client.Register(&readFrontierProvider{complete: func(req llm.Request) llm.Response {
		if !requestHasTool(req, "communicate") {
			return llm.Response{Message: llm.Assistant("fixture-title-391")}
		}
		if slices.Contains(userTexts(req), managerInput) {
			return <-responses
		}
		return parityCommunicate("child-result-391", "child-output-391", true)
	}})
	dir := t.TempDir()
	sess, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir, MaxRetainedTerminal: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	wireTranscriptHistory(t, sess, srv)
	path := func(id string) string { return filepath.Join(dir, "sessions", id+".transcript.jsonl") }
	srv.SetDescendantTranscriptPathFunc(path)
	idle := make(chan events.DelegateUpdatedData, 8)
	sess.SetDescendantEventFunc(func(ev events.SessionEvent) {
		srv.RecordDescendantAppEvent(sess.ID(), ev)
	})
	// Root events carry the stable delegate identity used by delegate_send.
	rootEventsDone := make(chan struct{})
	go func() {
		defer close(rootEventsDone)
		for ev := range sess.Events() {
			if data, ok := ev.Data.(events.DelegateUpdatedData); ok && data.Lifecycle == "idle" && data.Outcome == "completed" {
				idle <- data
			}
		}
	}()
	t.Cleanup(func() { sess.Close(); <-rootEventsDone })
	run := func(callID, tool string, args map[string]any) {
		t.Helper()
		responses <- parityResponse(parityCall(callID, tool, args))
		responses <- parityCommunicate(callID+"-done", "manager-output-391", true)
		if _, err := sess.ProcessInput(t.Context(), managerInput, nil); err != nil {
			t.Fatal(err)
		}
	}
	run("target-create-391", "delegate", map[string]any{"prompt": "target-input-391"})
	child := <-idle
	// Creating another delegate reclaims the first runtime through its real
	// resource owner, so the next send must run restoreIdle rather than reuse it.
	run("other-create-391", "delegate", map[string]any{"prompt": "other-input-391"})
	<-idle
	if child.DelegateID == "" || child.ChildSessionID == "" || child.Lifecycle != "idle" {
		t.Fatalf("target did not settle idle: %+v", child)
	}
	srv.appHistories.drop(child.ChildSessionID)
	h := srv.appHistoryForID(child.ChildSessionID)
	if h == nil {
		t.Fatal("idle descendant read did not recreate history")
	}
	h.mu.Lock()
	unhooked := !h.hooked && h.recordedLength == 0
	h.mu.Unlock()
	if !unhooked {
		t.Fatal("idle read did not exercise stat-backed history")
	}
	baseline, err := h.RecordedLength()
	if err != nil || baseline == 0 {
		t.Fatalf("idle recorded length = %d, err = %v", baseline, err)
	}
	assertSavedInput := func(h *threadHistory) {
		t.Helper()
		turns, _, _, _, _, err := h.latest(h.capture(), h.ref, appwire.TranscriptItemPageLimit)
		if err != nil {
			t.Errorf("read saved transcript: %v", err)
			return
		}
		for _, turn := range turns {
			for _, item := range turn.Items {
				if item.Type == "userMessage" && strings.Contains(item.Text, "target-input-391") {
					return
				}
			}
		}
		t.Error("saved delegate input disappeared from history")
	}
	assertSavedInput(h)

	var binds, completes atomic.Int64
	sess.SetDescendantTranscriptPublicationFunc(func(id string) agent.TranscriptPublicationHooks {
		hooks, _ := srv.transcriptPublicationHooks(id)
		if id == child.ChildSessionID {
			binds.Add(1)
			length, readErr := srv.appHistoryForID(id).RecordedLength()
			t.Logf("restore bound before attach: bind=%d complete=%d length=%d baseline=%d", binds.Load(), completes.Load(), length, baseline)
			if readErr != nil || length < baseline {
				t.Errorf("binding must defer pairing until complete: length=%d baseline=%d err=%v", length, baseline, readErr)
			}
			assertSavedInput(srv.appHistoryForID(id))
			published := hooks.Published
			hooks.Published = func(length int64, state *appwire.ConversationProvenanceState) {
				completes.Add(1)
				published(length, state)
			}
		}
		return hooks
	})
	// The real job-store open follows publication binding and precedes attach.
	jobsPath := filepath.Join(dir, "sessions", child.ChildSessionID, "jobs.jsonl")
	if err := os.Rename(jobsPath, jobsPath+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(jobsPath, 0o700); err != nil {
		t.Fatal(err)
	}
	run("failed-restore-391", "delegate_send", map[string]any{"to": child.DelegateID, "message": "retry-input-391", "max_wait_ms": 10000})
	if binds.Load() != 1 || completes.Load() != 0 {
		t.Fatalf("restore did not fail between bind and attach: binds=%d completes=%d", binds.Load(), completes.Load())
	}
	if length, err := h.RecordedLength(); err != nil || length < baseline {
		t.Errorf("failed restore hid saved history: length=%d baseline=%d err=%v", length, baseline, err)
	}
	assertSavedInput(h)
	if err := os.Remove(jobsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(jobsPath+".saved", jobsPath); err != nil {
		t.Fatal(err)
	}
	run("successful-restore-391", "delegate_send", map[string]any{"to": child.DelegateID, "message": "retry-input-392", "max_wait_ms": 10000})
	if binds.Load() != 2 || completes.Load() == 0 {
		t.Fatalf("retry did not reach real attach: binds=%d completes=%d", binds.Load(), completes.Load())
	}
	after := srv.appHistoryForID(child.ChildSessionID)
	length, err := after.RecordedLength()
	if err != nil || length <= baseline || after.conversationProvenance() == nil {
		t.Fatalf("successful restore did not publish new history and provenance: length=%d baseline=%d err=%v", length, baseline, err)
	}
	assertSavedInput(after)
}

func TestHistoryPublicationIgnoresIncompleteReplacement(t *testing.T) {
	r := newTestThreadHistories(t)
	const id = "replacement-393"
	path := writeDelegateTranscript(t, id, "saved-input-394")
	h := r.ensure(id, "local:"+id, path, 0, 0, "1", noopHistoryPublish, noopHistoryResync, nil)
	length, err := h.RecordedLength()
	if err != nil {
		t.Fatal(err)
	}
	old := r.publicationForProducer(id)
	old.complete(length, &appwire.ConversationProvenanceState{InstanceID: id, Revision: 1})
	fresh := r.publicationForProducer(id)
	if fresh == old {
		t.Fatal("replacement reused the completed producer")
	}
	if got := r.ensure(id, "local:"+id, path, 0, 0, "1", noopHistoryPublish, noopHistoryResync, nil); got != h {
		t.Fatal("ensure must ignore a replacement until complete")
	}
	if got, err := h.RecordedLength(); err != nil || got != length {
		t.Fatalf("pending replacement lost readable history: length=%d want=%d err=%v", got, length, err)
	}
	fresh.complete(length, &appwire.ConversationProvenanceState{InstanceID: id, Revision: 2})
	next := r.ensure(id, "local:"+id, path, 0, 0, "1", noopHistoryPublish, noopHistoryResync, nil)
	if next == h || next.conversationProvenance().Revision != 2 {
		t.Fatal("completed replacement did not take over history")
	}
}
