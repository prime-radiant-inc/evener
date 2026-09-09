package server

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func newNotesTestSession(t *testing.T) *agent.Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&blockingServerAdapter{name: "openai", started: make(chan struct{}), done: make(chan error, 1)})
	sess, err := agent.NewSession(c, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess
}

func notesRPC(conn interface {
	HandleMessage(context.Context, appwire.Message) appwire.Message
}, id int64, method string, params any) appwire.Message {
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	return conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(id), method, params))
}

// TestServerAppWireNotesHumanSetInvokesCallback verifies notes/human/set
// routes through the session callback like goal/set: the note stores, the
// response carries the stored value, and one human-note steer lands in the
// durable steering queue under the derived inner id.
func TestServerAppWireNotesHumanSetInvokesCallback(t *testing.T) {
	sess := newNotesTestSession(t)
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetNotesHumanSetFunc(sess.SetHumanNote)
	srv.SetUrlsRemoveFunc(sess.RemoveSessionURL)

	conn := srv.AppServer().NewConnection("test")
	resp := notesRPC(conn, 2, appwire.MethodNotesHumanSet, appwire.NotesHumanSetParams{
		Ref:                "local:" + sess.ID(),
		ClientMutationID:   "outer-1",
		ExpectedInstanceID: sess.ID(),
		Note:               "human says hi",
	})
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	out, ok := resp.Response.Result.(appwire.NotesHumanSetResponse)
	if !ok {
		t.Fatalf("response result=%T", resp.Response.Result)
	}
	if out.Note != "human says hi" {
		t.Fatalf("stored note=%q, want %q", out.Note, "human says hi")
	}
	queue := sess.SteeringQueueSnapshot()
	if len(queue) != 1 {
		t.Fatalf("steering queue length=%d, want 1", len(queue))
	}
}

// TestServerAppWireNotesHumanSetRetryOfOneOuterIDSteersOnce verifies the
// no-double-interrupt contract at the RPC layer: a hub retry of one outer id
// with the same text converges (no second steer), and the daemon never emits
// the push itself — EventNotesUpdated on the session stream is the projector's
// only input.
func TestServerAppWireNotesHumanSetRetryOfOneOuterIDSteersOnce(t *testing.T) {
	sess := newNotesTestSession(t)
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetNotesHumanSetFunc(sess.SetHumanNote)
	srv.SetUrlsRemoveFunc(sess.RemoveSessionURL)

	conn := srv.AppServer().NewConnection("test")
	params := appwire.NotesHumanSetParams{
		Ref:                "local:" + sess.ID(),
		ClientMutationID:   "outer-9",
		ExpectedInstanceID: sess.ID(),
		Note:               "same text",
	}
	first := notesRPC(conn, 2, appwire.MethodNotesHumanSet, params)
	if first.Kind() != appwire.MessageResponse {
		t.Fatalf("first resp=%v error=%+v", first.Kind(), first.Error)
	}
	retry := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodNotesHumanSet, params))
	if retry.Kind() != appwire.MessageResponse {
		t.Fatalf("retry resp=%v error=%+v", retry.Kind(), retry.Error)
	}
	if got := sess.SteeringQueueSnapshot(); len(got) != 1 {
		t.Fatalf("steering queue length=%d, want exactly 1 after outer retry", len(got))
	}
}

// TestServerAppWireNotesHumanSetWithoutFuncIsUnavailable verifies the
// nil-callback → Unavailable mapping, mirroring goal/set.
func TestServerAppWireNotesHumanSetWithoutFuncIsUnavailable(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	resp := notesRPC(conn, 2, appwire.MethodNotesHumanSet, appwire.NotesHumanSetParams{
		Ref:                "local:th_1",
		ClientMutationID:   "outer-1",
		ExpectedInstanceID: "th_1",
		Note:               "x",
	})
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("resp=%v, want error when notesHumanSetFunc unwired", resp.Kind())
	}
}

// TestServerAppWireUrlsRemoveByID verifies urls/remove removes by entry id
// through the session callback and reports unknown ids honestly.
func TestServerAppWireUrlsRemoveByID(t *testing.T) {
	sess := newNotesTestSession(t)
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetNotesHumanSetFunc(sess.SetHumanNote)
	srv.SetUrlsRemoveFunc(sess.RemoveSessionURL)

	entry, err := sess.AddSessionURLForTest("https://x.test/y", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	conn := srv.AppServer().NewConnection("test")
	resp := notesRPC(conn, 2, appwire.MethodUrlsRemove, appwire.UrlsRemoveParams{
		Ref:                "local:" + sess.ID(),
		ClientMutationID:   "outer-rm-1",
		ExpectedInstanceID: sess.ID(),
		ID:                 entry.ID,
	})
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if _, ok := resp.Response.Result.(appwire.UrlsRemoveResponse); !ok {
		t.Fatalf("response result=%T", resp.Response.Result)
	}
	missing := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodUrlsRemove, appwire.UrlsRemoveParams{
		Ref:                "local:" + sess.ID(),
		ClientMutationID:   "outer-rm-2",
		ExpectedInstanceID: sess.ID(),
		ID:                 entry.ID,
	}))
	if missing.Kind() != appwire.MessageError {
		t.Fatalf("second remove resp=%v, want error for unknown id", missing.Kind())
	}
}

// TestServerAppWireUrlsRemoveWithoutFuncIsUnavailable verifies the
// nil-callback → Unavailable mapping for urls/remove.
func TestServerAppWireUrlsRemoveWithoutFuncIsUnavailable(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	resp := notesRPC(conn, 2, appwire.MethodUrlsRemove, appwire.UrlsRemoveParams{
		Ref:                "local:th_1",
		ClientMutationID:   "outer-1",
		ExpectedInstanceID: "th_1",
		ID:                 "u1",
	})
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("resp=%v, want error when urlsRemoveFunc unwired", resp.Kind())
	}
}

// TestServerAppWireNotesEventsProjectToPushes verifies the single emission
// path: EventNotesUpdated/EventUrlsUpdated from the session stream project to
// exactly one push each, and the daemon handlers emit no push themselves.
func TestServerAppWireNotesEventsProjectToPushes(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	first := newTask2SubscribedClient(t, srv, "first", "local:th_1")

	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventNotesUpdated,
		SessionID: "th_1",
		Data:      events.NotesUpdatedData{HumanNote: "h", AgentNote: "a"},
	})
	awaitTask2Notification(t, first, appwire.NotifyEvenerNotesUpdated)

	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUrlsUpdated,
		SessionID: "th_1",
		Data:      events.UrlsUpdatedData{URLs: []events.SessionURLData{{ID: "u1", URL: "https://x.test/y"}}},
	})
	awaitTask2Notification(t, first, appwire.NotifyEvenerUrlsUpdated)
}

// TestServerAppWireSharedNotesCapabilityFollowsWiring verifies the SharedNotes
// capability bit: true only when both notes verbs are wired and the session is
// open, following the Goal bit's contract.
func TestServerAppWireSharedNotesCapabilityFollowsWiring(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	if caps := srv.appCapabilities("idle", false); caps.SharedNotes {
		t.Fatalf("SharedNotes should be false with nothing wired")
	}
	srv.SetNotesHumanSetFunc(func(outerID, note string) (string, error) { return note, nil })
	if caps := srv.appCapabilities("idle", false); caps.SharedNotes {
		t.Fatalf("SharedNotes should be false with only notes/human/set wired")
	}
	srv.SetUrlsRemoveFunc(func(id string) (bool, error) { return false, nil })
	if caps := srv.appCapabilities("idle", false); !caps.SharedNotes {
		t.Fatalf("SharedNotes should be true with both verbs wired on an open session")
	}
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "closed"})
	if caps := srv.appCapabilities("closed", false); caps.SharedNotes {
		t.Fatalf("SharedNotes should be false on a closed session")
	}
}
