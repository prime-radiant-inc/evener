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

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/rendezvous"
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

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    hubcore.NewPastIndex(""),
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
			ServerInfo: appwire.ServerInfo{Name: "daemon-test"},
			Features:   appwire.FeatureSet{},
		}, nil
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
		return appwire.InitializeResponse{ServerInfo: appwire.ServerInfo{Name: "daemon-test"}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "01SENDITEMS",
			SessionID: "01SENDITEMS",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Source:    "local",
			Serf: appwire.SerfThread{
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
	req := httptest.NewRequest(http.MethodPost, "/s/01SENDITEMS/send", bytes.NewReader(payload))
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

// TestWeb_Queue_ItemsShapeForwardedToDaemonQueueTurn drives the kata v80q
// shape on /s/<id>/queue: POST with `items` carrying base64 image data and
// assert the daemon's TurnQueue call receives the decoded bytes as
// appwire.InputItem entries. Queue is a separate code path from /send so
// it gets its own coverage.
func TestWeb_Queue_ItemsShapeForwardedToDaemonQueueTurn(t *testing.T) {
	dir := t.TempDir()
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon-test", SourceID: "local"})
	gotItems := make(chan []appwire.InputItem, 1)
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
		select {
		case gotItems <- imageInputItems(params.Input):
		default:
		}
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodInitialize, func(_ context.Context, _ appwire.InitializeParams) (appwire.InitializeResponse, error) {
		return appwire.InitializeResponse{ServerInfo: appwire.ServerInfo{Name: "daemon-test"}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "01QUEUEITEMS",
			SessionID: "01QUEUEITEMS",
			// Queue requires the session to be processing.
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
			Source: "local",
			Serf: appwire.SerfThread{
				Ref:          "local:01QUEUEITEMS",
				Capabilities: appwire.ThreadCapabilities{Send: false, Steer: true, Interrupt: true, Queue: true},
			},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	writeRendezvous(t, dir, rendezvous.Entry{
		PID:       102,
		Address:   strings.TrimPrefix(daemonHTTP.URL, "http://"),
		Endpoint:  "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"),
		Protocol:  appwire.ProtocolVersion,
		SourceID:  "local",
		ThreadID:  "01QUEUEITEMS",
		SessionID: "01QUEUEITEMS",
	})
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01QUEUEITEMS", status: "GENERATING"})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    hubcore.NewPastIndex(""),
	})

	reqBody := map[string]any{
		"text": "queue with image",
		"items": []map[string]any{
			{
				"type":      "image",
				"mediaType": "image/png",
				"data":      base64.StdEncoding.EncodeToString(testImageBytes),
				"name":      "items-queue.png",
			},
		},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/s/01QUEUEITEMS/queue", bytes.NewReader(payload))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var items []appwire.InputItem
	select {
	case items = <-gotItems:
	default:
		t.Fatalf("daemon TurnQueue was not invoked")
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
	if got.Name != "items-queue.png" {
		t.Errorf("Item.Name=%q, want items-queue.png", got.Name)
	}
}

// TestWeb_DrainAsSteer_ItemsShapeSendsAtomicDrain drives the kata v80q
// shape on /s/<id>/drain-as-steer: when the request carries an `items`
// array, the hub forwards them on the drain RPC so the daemon appends and
// drains atomically.
func TestWeb_DrainAsSteer_ItemsShapeSendsAtomicDrain(t *testing.T) {
	dir := t.TempDir()
	daemon := appserver.NewServer(appserver.ServerConfig{
		ServerName: "daemon-test",
		SourceID:   "local",
		Features:   appwire.FeatureSet{},
	})
	queued := make(chan struct{}, 1)
	drained := make(chan appwire.TurnDrainAsSteerParams, 1)
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
		select {
		case queued <- struct{}{}:
		default:
		}
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.EmptyResponse, error) {
		select {
		case drained <- params:
		default:
		}
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodInitialize, func(_ context.Context, _ appwire.InitializeParams) (appwire.InitializeResponse, error) {
		return appwire.InitializeResponse{
			ServerInfo: appwire.ServerInfo{Name: "daemon-test"},
			Features:   appwire.FeatureSet{},
		}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "01DRAINITEMS",
			SessionID: "01DRAINITEMS",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
			Source:    "local",
			Serf: appwire.SerfThread{
				Ref:          "local:01DRAINITEMS",
				Capabilities: appwire.ThreadCapabilities{Send: false, Steer: true, Interrupt: true, Queue: true},
			},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	writeRendezvous(t, dir, rendezvous.Entry{
		PID:       103,
		Address:   strings.TrimPrefix(daemonHTTP.URL, "http://"),
		Endpoint:  "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"),
		Protocol:  appwire.ProtocolVersion,
		SourceID:  "local",
		ThreadID:  "01DRAINITEMS",
		SessionID: "01DRAINITEMS",
	})
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01DRAINITEMS", status: "GENERATING"})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    hubcore.NewPastIndex(""),
	})

	reqBody := map[string]any{
		"text": "drain with image",
		"items": []map[string]any{
			{
				"type":      "image",
				"mediaType": "image/png",
				"data":      base64.StdEncoding.EncodeToString(testImageBytes),
				"name":      "items-drain.png",
			},
		},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/s/01DRAINITEMS/drain-as-steer", bytes.NewReader(payload))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	select {
	case <-queued:
		t.Fatalf("daemon TurnQueue was invoked; drain payload must be atomic")
	default:
	}
	var params appwire.TurnDrainAsSteerParams
	select {
	case params = <-drained:
	default:
		t.Fatalf("daemon TurnDrainAsSteer was not invoked")
	}
	if inputTextForTest(params.Input) != "drain with image" {
		t.Fatalf("Input=%+v, want drain with image", params.Input)
	}
	items := imageInputItems(params.Input)
	if len(items) != 1 {
		t.Fatalf("Items: got %d, want 1 (%+v)", len(items), items)
	}
	got := items[0]
	if got.Type != "image" {
		t.Errorf("Item.Type=%q, want image", got.Type)
	}
	if !bytes.Equal(got.Data, testImageBytes) {
		t.Errorf("Item.Data mismatch: got %x, want %x", got.Data, testImageBytes)
	}
	if got.Name != "items-drain.png" {
		t.Errorf("Item.Name=%q, want items-drain.png", got.Name)
	}
}
