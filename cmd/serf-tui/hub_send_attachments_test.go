package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
)

// writeAttachmentTempFile writes bytes to a temp PNG and returns the path.
// The test cleans up the file via t.Cleanup. Used to drive the on-submit
// file-read path for pending attachments.
func writeAttachmentTempFile(t *testing.T, bytesPayload []byte) string {
	t.Helper()
	f, err := os.CreateTemp("", "serf-attachments-test-*.png")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.Write(bytesPayload); err != nil {
		f.Close()
		os.Remove(f.Name())
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		t.Fatalf("close: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	return f.Name()
}

// TestSendHubInputIncludesAttachments verifies kata re91 submit flow:
// sendHubInput must build []appwire.InputItem entries from the staged
// pendingAttachments and ship them alongside the composer text on
// turn/start.
func TestSendHubInputIncludesAttachments(t *testing.T) {
	first := writeAttachmentTempFile(t, []byte("\x89PNG\r\n\x1a\nfirst-png-bytes"))
	second := writeAttachmentTempFile(t, []byte("\x89PNG\r\n\x1a\nsecond-png-bytes"))

	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	var got appwire.TurnStartParams
	appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		got = params
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_attach"}}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	atts := []*PastedImage{
		{Path: first, MediaType: "image/png"},
		{Path: second, MediaType: "image/png"},
	}

	msg := sendHubInput(client, appwire.Ref{SourceID: "local", ThreadID: "th_1"}, "hi", "hi", atts)()
	sendMsg, ok := msg.(hubSendMsg)
	if !ok || sendMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, sendMsg.err)
	}
	if got.Ref != "local:th_1" || got.Prompt != "hi" {
		t.Fatalf("params=%+v, want ref=local:th_1 prompt=hi", got)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items len=%d, want 2: %+v", len(got.Items), got.Items)
	}
	for i, want := range []struct {
		path  string
		bytes []byte
	}{
		{first, []byte("\x89PNG\r\n\x1a\nfirst-png-bytes")},
		{second, []byte("\x89PNG\r\n\x1a\nsecond-png-bytes")},
	} {
		item := got.Items[i]
		if item.Type != "image" {
			t.Errorf("items[%d].Type=%q, want image", i, item.Type)
		}
		if item.MediaType != "image/png" {
			t.Errorf("items[%d].MediaType=%q, want image/png", i, item.MediaType)
		}
		if !bytes.Equal(item.Data, want.bytes) {
			t.Errorf("items[%d].Data=%x, want %x", i, item.Data, want.bytes)
		}
		if item.Name != filepath.Base(want.path) {
			t.Errorf("items[%d].Name=%q, want %q", i, item.Name, filepath.Base(want.path))
		}
	}
}

// TestSendHubQueueIncludesAttachments verifies sendHubQueue threads
// attachments onto turn/queue the same way as turn/start.
func TestSendHubQueueIncludesAttachments(t *testing.T) {
	path := writeAttachmentTempFile(t, []byte("\x89PNG\r\n\x1a\nqueue-png-bytes"))

	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	var got appwire.TurnQueueParams
	appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
		got = params
		return appwire.EmptyResponse{}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	atts := []*PastedImage{{Path: path, MediaType: "image/png"}}

	msg := sendHubQueue(client, appwire.Ref{SourceID: "local", ThreadID: "th_q"}, "queue me", "queue me", atts)()
	queueMsg, ok := msg.(hubQueueMsg)
	if !ok || queueMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, queueMsg.err)
	}
	if got.Ref != "local:th_q" || got.Text != "queue me" {
		t.Fatalf("params=%+v, want ref=local:th_q text=queue me", got)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items len=%d, want 1: %+v", len(got.Items), got.Items)
	}
	item := got.Items[0]
	if item.Type != "image" || item.MediaType != "image/png" {
		t.Errorf("item=%+v, want type=image mediaType=image/png", item)
	}
	if !bytes.Equal(item.Data, []byte("\x89PNG\r\n\x1a\nqueue-png-bytes")) {
		t.Errorf("item.Data=%x, want png signature payload", item.Data)
	}
	if item.Name != filepath.Base(path) {
		t.Errorf("item.Name=%q, want %q", item.Name, filepath.Base(path))
	}
}

// TestSendHubDrainAsSteerIncludesAttachments verifies the drain-as-steer
// path queues composer text + attachments before draining, so the
// resulting STEERING message carries the image bytes alongside any
// already-queued items.
func TestSendHubDrainAsSteerIncludesAttachments(t *testing.T) {
	path := writeAttachmentTempFile(t, []byte("\x89PNG\r\n\x1a\nsteer-png-bytes"))

	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	var queueParams []appwire.TurnQueueParams
	var drainCalled bool
	appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
		queueParams = append(queueParams, params)
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, _ appwire.TurnDrainAsSteerParams) (appwire.EmptyResponse, error) {
		drainCalled = true
		return appwire.EmptyResponse{}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	atts := []*PastedImage{{Path: path, MediaType: "image/png"}}

	msg := sendHubDrainAsSteer(client, appwire.Ref{SourceID: "local", ThreadID: "th_s"}, "steer me", "steer me", atts)()
	drainMsg, ok := msg.(hubDrainAsSteerMsg)
	if !ok || drainMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, drainMsg.err)
	}
	if !drainCalled {
		t.Fatal("drainAsSteer never invoked")
	}
	if len(queueParams) != 1 {
		t.Fatalf("queueParams len=%d, want 1: %+v", len(queueParams), queueParams)
	}
	q := queueParams[0]
	if q.Ref != "local:th_s" || q.Text != "steer me" {
		t.Fatalf("queue params=%+v, want ref=local:th_s text=steer me", q)
	}
	if len(q.Items) != 1 {
		t.Fatalf("queue items len=%d, want 1: %+v", len(q.Items), q.Items)
	}
	item := q.Items[0]
	if item.Type != "image" || item.MediaType != "image/png" {
		t.Errorf("item=%+v, want type=image mediaType=image/png", item)
	}
	if !bytes.Equal(item.Data, []byte("\x89PNG\r\n\x1a\nsteer-png-bytes")) {
		t.Errorf("item.Data=%x, want steer png bytes", item.Data)
	}
}

// TestSendClearsPendingAttachmentsOnSuccess verifies the hub session
// model wipes pendingAttachments after a successful send (so the next
// turn doesn't re-attach the same images).
func TestSendClearsPendingAttachmentsOnSuccess(t *testing.T) {
	m := newSessionHubModel(nil)
	m.pendingAttachments = []*PastedImage{
		{Path: "/tmp/sent-one.png", MediaType: "image/png"},
		{Path: "/tmp/sent-two.png", MediaType: "image/png"},
	}

	updated, _ := m.Update(hubSendMsg{text: "hi", turnID: "turn_done"})
	got := updated.(hubModel)

	if len(got.pendingAttachments) != 0 {
		t.Fatalf("pendingAttachments after success len=%d, want 0", len(got.pendingAttachments))
	}
}

// TestSendKeepsPendingAttachmentsOnError verifies that a failed send
// preserves pendingAttachments so the user can retry without re-pasting.
func TestSendKeepsPendingAttachmentsOnError(t *testing.T) {
	m := newSessionHubModel(nil)
	first := &PastedImage{Path: "/tmp/keep-one.png", MediaType: "image/png"}
	second := &PastedImage{Path: "/tmp/keep-two.png", MediaType: "image/png"}
	m.pendingAttachments = []*PastedImage{first, second}

	updated, _ := m.Update(hubSendMsg{text: "hi", draft: "hi", err: errors.New("boom")})
	got := updated.(hubModel)

	if len(got.pendingAttachments) != 2 {
		t.Fatalf("pendingAttachments after error len=%d, want 2", len(got.pendingAttachments))
	}
	if got.pendingAttachments[0] != first || got.pendingAttachments[1] != second {
		t.Fatalf("pendingAttachments order mutated: %+v", got.pendingAttachments)
	}
}

// TestAttachmentBytesReadFromTempFile verifies that the bytes shipped on
// the wire are read from PastedImage.Path at submit time, not captured
// when the attachment was first staged. We mutate the temp file after
// staging but before submit; the sent bytes must match the mutated
// contents.
func TestAttachmentBytesReadFromTempFile(t *testing.T) {
	path := writeAttachmentTempFile(t, []byte("initial-bytes"))

	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	var got appwire.TurnStartParams
	appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		got = params
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_late"}}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	att := &PastedImage{Path: path, MediaType: "image/png"}

	// Mutate the file AFTER staging the attachment but BEFORE sendHubInput
	// fires. If the send path reads bytes early (e.g. at attachment-stage
	// time), this update will be missed and the assertion below fails.
	want := []byte("\x89PNG\r\n\x1a\nlate-bytes")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	msg := sendHubInput(client, appwire.Ref{SourceID: "local", ThreadID: "th_late"}, "hi", "hi", []*PastedImage{att})()
	if sendMsg, ok := msg.(hubSendMsg); !ok || sendMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, sendMsg.err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items len=%d, want 1: %+v", len(got.Items), got.Items)
	}
	if !bytes.Equal(got.Items[0].Data, want) {
		t.Fatalf("item.Data=%x, want late-bytes %x", got.Items[0].Data, want)
	}
}
