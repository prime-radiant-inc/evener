package main

// Tests for image-attachment round-trip across the serf wire surface (kata
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

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/rendezvous"
)

// testImageBytes is a tiny PNG signature used wherever a test needs an
// opaque payload; the size and content don't matter since the wire treats
// it as []byte.
var testImageBytes = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// TestWeb_ApiSpawn_ForwardsImageItemsToThreadStart drives the
// /api/spawn HTTP entry point with an "items" array containing an image
// attachment and asserts that the resulting ThreadStart appwire call (and
// downstream TurnStart) carries the same image bytes through to the
// configured source (kata t5j6 path 1).
func TestWeb_ApiSpawn_ForwardsImageItemsToThreadStart(t *testing.T) {
	workDir := t.TempDir()
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})

	// The codex source maps the wire-level "items" array into the turn's
	// input. We observe MethodTurnStart on the downstream codex server using
	// the raw map shape so we can read the codex-native "input" array that
	// codexInput() emits for image items.
	gotTurnInput := make(chan []any, 1)
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadStart, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"thread": map[string]any{
			"id":        "th_codex_img",
			"sessionId": "th_codex_img",
			"status":    map[string]any{"type": "idle"},
			"source":    "appServer",
		}}, nil
	})
	appserver.HandleTyped(codex.Router(), appwire.MethodTurnStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		if input, ok := params["input"].([]any); ok {
			select {
			case gotTurnInput <- input:
			default:
			}
		}
		return map[string]any{"turn": map[string]any{
			"id":        "turn_codex",
			"items":     []any{},
			"itemsView": "full",
			"status":    "inProgress",
		}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})

	reqBody := map[string]any{
		"harness":     "codex",
		"prompt":      "describe this",
		"model":       "gpt-5.1-codex",
		"working_dir": workDir,
		"items": []map[string]any{{
			"type":      "image",
			"mediaType": "image/png",
			"data":      testImageBytes,
			"name":      "shot.png",
		}},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/spawn", bytes.NewReader(payload))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var input []any
	select {
	case input = <-gotTurnInput:
	default:
		t.Fatalf("codex TurnStart was not invoked with an input array")
	}
	// Codex shape: input is a list of {type, ...} entries. There must be one
	// image entry whose payload matches what we sent. codex represents image
	// items as {type:"input_image", image_url:"data:<media>;base64,<...>"} —
	// the exact field name depends on codexInput; we just need any entry
	// whose marshaled bytes contain our base64-encoded payload.
	wantB64 := base64.StdEncoding.EncodeToString(testImageBytes)
	sawImage := false
	for _, entry := range input {
		raw, _ := json.Marshal(entry)
		if strings.Contains(string(raw), wantB64) {
			sawImage = true
			break
		}
	}
	if !sawImage {
		raw, _ := json.Marshal(input)
		t.Fatalf("codex TurnStart input did not include the attachment bytes; got input=%s want base64=%s", raw, wantB64)
	}
}

// TestWeb_Send_ImageAttachmentsForwardedToDaemonStartTurn drives Path 5:
// POST /s/<id>/send with an Images field must produce a TurnStart wire
// call to the daemon whose Items array contains an "image" InputItem
// carrying the same bytes. The daemon-side translation (InputItem →
// ContentImage in the user message) is covered by the server-package
// tests; this test scope is the hub's REST→appwire bridge.
func TestWeb_Send_ImageAttachmentsForwardedToDaemonStartTurn(t *testing.T) {
	dir := t.TempDir()
	// Stand up a fake daemon that records TurnStart params.
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon-test", SourceID: "local"})
	gotItems := make(chan []appwire.InputItem, 1)
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		select {
		case gotItems <- params.Items:
		default:
		}
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_send_img", Status: appwire.TurnStatusRunning}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodInitialize, func(_ context.Context, _ appwire.InitializeParams) (appwire.InitializeResponse, error) {
		return appwire.InitializeResponse{ServerInfo: appwire.ServerInfo{Name: "daemon-test"}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "01SENDIMG",
			SessionID: "01SENDIMG",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Source:    "local",
			Serf: appwire.SerfThread{
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
	r := NewRoster(dir, fakeProber{sessionID: "01SENDIMG", status: "AWAITING_REPLY"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})

	body := sendRequest{
		Text: "look at this",
		Images: []agent.ImageAttachment{{
			MediaType: "image/png",
			Data:      testImageBytes,
			Name:      "send.png",
		}},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/s/01SENDIMG/send", bytes.NewReader(payload))
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
