package hub

// Tests for image-attachment round-trip across the evener wire surface (kata
// t5j6). Each test exercises one of the five inbound paths a client uses to
// submit an image-bearing user message, and asserts that the daemon's session
// either receives the bytes on the wire (for routed paths) or builds a
// ContentImage part on the in-memory user message (for direct-session paths).
//
// The image bytes used are an 8-byte PNG signature — too small to be a
// rendered image, but they're treated opaquely all the way through the wire
// so the content is irrelevant.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func imageInputItems(input []appwire.InputItem) []appwire.InputItem {
	var out []appwire.InputItem
	for _, item := range input {
		if item.Type == "image" || item.Type == "input_image" {
			out = append(out, item)
		}
	}
	return out
}

// testImageBytes is a tiny PNG signature used wherever a test needs an
// opaque payload; the size and content don't matter since the wire treats
// it as []byte.
var testImageBytes = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// TestWeb_Send_ImageAttachmentsForwardedToDaemonStartTurn drives Path 5:
// POST /s/<id>/send with an Images field must produce a TurnStart wire
// call to the daemon whose Items array contains an "image" InputItem
// carrying the same bytes. The daemon-side translation (InputItem →
// ContentImage in the user message) is covered by the server-package
// tests; this test scope is the hub's REST→appwire bridge.
func TestWeb_Send_ImageAttachmentsForwardedToDaemonStartTurn(t *testing.T) {
	dir := t.TempDir()
	// Stand up a fake daemon that records TurnStart params.
	daemon := appserver.NewServer(appserver.ServerConfig{
		ServerName: "daemon-test",
		SourceID:   "local",
		Features:   appwire.FeatureSet{},
	})
	gotItems := make(chan []appwire.InputItem, 1)
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		select {
		case gotItems <- imageInputItems(params.Input):
		default:
		}
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_send_img", Status: appwire.TurnStatusInProgress}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodInitialize, func(_ context.Context, _ appwire.InitializeParams) (appwire.InitializeResponse, error) {
		return appwire.InitializeResponse{
			ServerInfo:      appwire.ServerInfo{Name: "daemon-test"},
			ProtocolVersion: appwire.ProtocolVersion,
			Features:        appwire.FeatureSet{},
		}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "01SENDIMG",
			SessionID: "01SENDIMG",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:          "local:01SENDIMG",
				Capabilities: appwire.ThreadCapabilities{Send: true, Steer: true, Interrupt: true, Queue: true},
			},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	writeRendezvous(t, dir, rendezvous.Entry{
		PID:       100,
		Address:   strings.TrimPrefix(daemonHTTP.URL, "http://"),
		Endpoint:  "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"),
		Protocol:  appwire.ProtocolVersion,
		SourceID:  "local",
		ThreadID:  "01SENDIMG",
		SessionID: "01SENDIMG",
	})
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01SENDIMG", status: appwire.ThreadStatusAwaiting})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    hubcore.NewPastIndex(""),
	})

	body := sendRequest{
		Text: "look at this",
		Items: []appwire.InputItem{{
			Type:      "image",
			MediaType: "image/png",
			Data:      testImageBytes,
			Name:      "send.png",
		}},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:01SENDIMG/send", bytes.NewReader(payload))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var items []appwire.InputItem
	select {
	case items = <-gotItems:
	default:
		t.Fatalf("daemon TurnStart was not invoked")
	}
	if len(items) != 1 {
		t.Fatalf("Items: got %d, want 1 (%+v)", len(items), items)
	}
	got := items[0]
	if got.Type != "image" {
		t.Errorf("Item.Type=%q, want image", got.Type)
	}
	if got.MediaType != "image/png" {
		t.Errorf("Item.MediaType=%q, want image/png", got.MediaType)
	}
	if !bytes.Equal(got.Data, testImageBytes) {
		t.Errorf("Item.Data mismatch: got %x, want %x", got.Data, testImageBytes)
	}
	if got.Name != "send.png" {
		t.Errorf("Item.Name=%q, want send.png", got.Name)
	}
}

// TestWeb_Send_ItemsShapeForwardedToDaemonStartTurn drives the Codex input
// shape on /s/<id>/send: the JSON body carries an `items` array with
// base64-encoded Data on each image entry.
// The hub must decode the base64 and forward the bytes as appwire.InputItem
// entries on the daemon's TurnStart call. This is the canonical wire shape
// the browser composer-attachments pipeline emits.
func TestWeb_Send_ItemsShapeForwardedToDaemonStartTurn(t *testing.T) {
	dir := t.TempDir()
	daemon := appserver.NewServer(appserver.ServerConfig{
		ServerName: "daemon-test",
		SourceID:   "local",
		Features:   appwire.FeatureSet{},
	})
	gotItems := make(chan []appwire.InputItem, 1)
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		select {
		case gotItems <- imageInputItems(params.Input):
		default:
		}
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_items", Status: appwire.TurnStatusInProgress}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodInitialize, func(_ context.Context, _ appwire.InitializeParams) (appwire.InitializeResponse, error) {
		return appwire.InitializeResponse{ServerInfo: appwire.ServerInfo{Name: "daemon-test"}, ProtocolVersion: appwire.ProtocolVersion}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "01SENDITEMS",
			SessionID: "01SENDITEMS",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:          "local:01SENDITEMS",
				Capabilities: appwire.ThreadCapabilities{Send: true, Steer: true, Interrupt: true, Queue: true},
			},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	writeRendezvous(t, dir, rendezvous.Entry{
		PID:       101,
		Address:   strings.TrimPrefix(daemonHTTP.URL, "http://"),
		Endpoint:  "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"),
		Protocol:  appwire.ProtocolVersion,
		SourceID:  "local",
		ThreadID:  "01SENDITEMS",
		SessionID: "01SENDITEMS",
	})
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01SENDITEMS", status: appwire.ThreadStatusAwaiting})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    hubcore.NewPastIndex(""),
	})

	// Build the v80q wire body: prompt as text, plus an image item carrying
	// base64-encoded bytes. We hand-craft the JSON to match exactly what
	// the browser composer (appwire.startTurn) emits.
	reqBody := map[string]any{
		"text": "examine",
		"items": []map[string]any{
			{
				"type":      "image",
				"mediaType": "image/png",
				"data":      base64.StdEncoding.EncodeToString(testImageBytes),
				"name":      "items-send.png",
			},
		},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:01SENDITEMS/send", bytes.NewReader(payload))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var items []appwire.InputItem
	select {
	case items = <-gotItems:
	default:
		t.Fatalf("daemon TurnStart was not invoked")
	}
	if len(items) != 1 {
		t.Fatalf("Items: got %d, want 1 (%+v)", len(items), items)
	}
	got := items[0]
	if got.Type != "image" {
		t.Errorf("Item.Type=%q, want image", got.Type)
	}
	if got.MediaType != "image/png" {
		t.Errorf("Item.MediaType=%q, want image/png", got.MediaType)
	}
	if !bytes.Equal(got.Data, testImageBytes) {
		t.Errorf("Item.Data mismatch: got %x, want %x", got.Data, testImageBytes)
	}
	if got.Name != "items-send.png" {
		t.Errorf("Item.Name=%q, want items-send.png", got.Name)
	}
}
