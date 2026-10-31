package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

func TestAnswerContinuationReadRoutes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const id = "02wMz5Txv5aIxgf9yVdd0N"
	root := t.TempDir()
	state := filepath.Join(root, "projects", "project-answer-0000000000")
	if err := os.MkdirAll(filepath.Join(state, "sessions"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(state, schema.SessionMeta{ID: id}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "sessions", id+".transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		id    string
		links []string
	}{{"A", nil}, {"B", []string{"A"}}, {"X", nil}, {"C", []string{"B"}}} {
		if err := w.Append(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: v.id, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User(v.id), AnswerToTurnIDs: v.links}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	indexPastTranscriptForTest(t, state, id)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Past: past}
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{})
	defer daemon.Close()
	identity, err := daemonserver.PrepareAppIdentity("local", id, path)
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(identity.WithBootGeneration("7"), nil)
	transport, err := daemon.AppServer().NewInProcessTransport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	client.Start(ctx)
	defer client.Close()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	defer upstream.Close()
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: upstream.URL, SourceID: "local", SessionID: id, ThreadID: id, WorkspaceRef: "local:" + id, InstanceID: id}, SessionID: id}}
	}, upstream.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	hub := newHubAppServer(cfg, sources)
	defer hub.Shutdown(ctx)
	hubTransport, err := hub.NewInProcessTransport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hubClient := appwire.NewClient(hubTransport)
	hubClient.Start(ctx)
	defer hubClient.Close()
	if _, err := hubClient.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := hubClient.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + id, IncludeTurns: true, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	remote := appsource.NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return hubClient, nil })
	for _, route := range []string{"saved", "live", "hub", "remote"} {
		t.Run(route, func(t *testing.T) {
			ref := "local:" + id
			if route == "remote" {
				ref = "remote:" + id
			}
			for _, candidate := range []string{"C", "X", "missing"} {
				raw := []byte(`{"ref":"` + ref + `","includeTurns":false,"answerContinuation":{"ancestorTurnId":"A","candidateTurnId":"` + candidate + `"}}`)
				var p appwire.ThreadReadParams
				if err := json.Unmarshal(raw, &p); err != nil {
					t.Fatal(err)
				}
				var response appwire.ThreadReadResponse
				var err error
				switch route {
				case "saved":
					var ok bool
					response, ok, err = pastThreadReadResponse(ctx, cfg, p)
					if !ok {
						t.Fatal("saved route not found")
					}
				case "live":
					response, err = client.ThreadRead(ctx, p)
				case "hub":
					response, err = hubClient.ThreadRead(ctx, p)
				case "remote":
					response, err = remote.ReadThread(ctx, p)
				}
				if candidate == "missing" {
					if err == nil {
						t.Fatal("missing candidate returned success")
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(response)
				var verdict appwire.ThreadReadResponse
				if err := json.Unmarshal(data, &verdict); err != nil {
					t.Fatal(err)
				}
				v := verdict.AnswerContinuation
				if v == nil || v.AncestorTurnID != "A" || v.CandidateTurnID != candidate || v.Matches != (candidate == "C") {
					t.Fatalf("%s candidate %s verdict=%s", route, candidate, data)
				}
				if response.Snapshot == nil || response.Snapshot.Incarnation == "" || response.Snapshot.Length == 0 || response.Thread.Evener.Ref != ref || response.Thread.Evener.InstanceID == "" || response.BootGeneration == "" {
					t.Fatalf("missing identity: %+v", response)
				}
				if route != "saved" && response.BootGeneration != "7" {
					t.Fatalf("live query fell back: ref=%s instance=%s boot=%s epoch=%d snapshot=%+v verdict=%+v", response.Thread.Evener.Ref, response.Thread.Evener.InstanceID, response.BootGeneration, response.Epoch, response.Snapshot, response.AnswerContinuation)
				}
			}
		})
	}
	for _, invalid := range []appwire.ThreadReadParams{
		{Ref: "local:" + id, Subscribe: true, AnswerContinuation: &appwire.AnswerContinuationQuery{AncestorTurnID: "A", CandidateTurnID: "C"}},
		{Ref: "local:" + id, IncludeTurns: true, AnswerContinuation: &appwire.AnswerContinuationQuery{AncestorTurnID: "A", CandidateTurnID: "C"}},
		{Ref: "local:" + id, ReplaceSubscription: true, AnswerContinuation: &appwire.AnswerContinuationQuery{AncestorTurnID: "A", CandidateTurnID: "C"}},
		{Ref: "local:" + id, AnswerContinuation: &appwire.AnswerContinuationQuery{AncestorTurnID: "A"}},
		{Ref: "local:" + id, ItemLimit: 41, AnswerContinuation: &appwire.AnswerContinuationQuery{AncestorTurnID: "A", CandidateTurnID: "C"}},
	} {
		if _, err := hubClient.ThreadRead(ctx, invalid); err == nil {
			t.Fatal("incompatible query accepted")
		}
		if _, err := client.ThreadRead(ctx, invalid); err == nil {
			t.Fatal("live incompatible query accepted")
		}
		if _, _, err := pastThreadReadResponse(ctx, cfg, invalid); err == nil {
			t.Fatal("saved incompatible query accepted")
		}
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		var readCtx context.Context
		var stop context.CancelFunc
		if errors.Is(cause, context.Canceled) {
			readCtx, stop = context.WithCancel(ctx)
			stop()
		} else {
			readCtx, stop = context.WithDeadline(ctx, time.Now().Add(-time.Second))
		}
		_, err := hubClient.ThreadRead(readCtx, appwire.ThreadReadParams{Ref: "local:" + id, AnswerContinuation: &appwire.AnswerContinuationQuery{AncestorTurnID: "A", CandidateTurnID: "C"}})
		stop()
		if !errors.Is(err, cause) {
			t.Fatalf("read cancellation: got %v want %v", err, cause)
		}
	}
	if got := daemon.AppServer().SubscriberCount("local:" + id); got != 1 {
		t.Fatalf("human upstream subscriptions=%d want 1", got)
	}
	daemon.RecordAppEvent(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: id, Data: events.NotesUpdatedData{HumanNote: "after-continuation-query"}})
	for {
		select {
		case n := <-hubClient.Notifications():
			if n.Method == appwire.NotifyEvenerNotesUpdated {
				var p appwire.NotesUpdatedParams
				if err := json.Unmarshal(n.Params, &p); err != nil {
					t.Fatal(err)
				}
				if p.HumanNote == "after-continuation-query" {
					return
				}
			}
		case <-ctx.Done():
			t.Fatal("human subscription lost after verdict query")
		}
	}
}
