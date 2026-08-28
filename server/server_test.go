package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

func TestInterruptEndpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(ServerConfig{})
	srv.SetCancelFunc(cancel)

	req := httptest.NewRequest(http.MethodPost, "/interrupt", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status code: got %d, want 204", w.Code)
	}

	select {
	case <-ctx.Done():
		// expected
	default:
		t.Error("context should be cancelled after interrupt")
	}
}

// TestInterruptEndpoint_NoCancelFunc verifies the daemon's REST
// /interrupt handler returns 503 (mirroring the appwire path's
// Unavailable error) instead of silently 204'ing when no cancel
// function is registered. Without this, callers can't tell whether
// the turn was actually cancelled. Regression test for kata k7t8.
func TestInterruptEndpoint_NoCancelFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/interrupt", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code: got %d, want 503", w.Code)
	}
}

func TestInputEndpoint_Accepted(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)

	body := strings.NewReader(`{"text":"hello world"}`)
	req := httptest.NewRequest(http.MethodPost, "/input", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status code: got %d, want 202", w.Code)
	}

	select {
	case msg := <-srv.InputCh():
		if msg.Text != "hello world" {
			t.Errorf("input: got %q, want %q", msg.Text, "hello world")
		}
		if len(msg.Images) != 0 {
			t.Errorf("images: got %d, want 0", len(msg.Images))
		}
	case <-time.After(time.Second):
		t.Fatal("timeout reading input")
	}
}

func TestInputEndpoint_Conflict(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)

	body := strings.NewReader(`{"text":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/input", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status code: got %d, want 409", w.Code)
	}
}

func TestInputEndpoint_ClosedSessionConflict(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetState("closed")

	body := strings.NewReader(`{"text":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/input", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status code: got %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "session is closed") {
		t.Fatalf("body=%q", w.Body.String())
	}
}

func TestInputEndpoint_EmptyTextAndNoImages(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)

	body := strings.NewReader(`{"text":""}`)
	req := httptest.NewRequest(http.MethodPost, "/input", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status code: got %d, want 400", w.Code)
	}
}

func TestInputEndpoint_TextAndImage(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)

	// 1x1 transparent PNG
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	reqBody := InputRequest{
		Text: "caption",
		Images: []ImageAttachment{
			{MediaType: "image/png", Data: pngBytes, Name: "x.png"},
		},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/input", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status code: got %d, want 202", w.Code)
	}

	select {
	case msg := <-srv.InputCh():
		if msg.Text != "caption" {
			t.Errorf("text: got %q, want %q", msg.Text, "caption")
		}
		if len(msg.Images) != 1 {
			t.Fatalf("images: got %d, want 1", len(msg.Images))
		}
		if msg.Images[0].MediaType != "image/png" {
			t.Errorf("media_type: got %q, want image/png", msg.Images[0].MediaType)
		}
		if msg.Images[0].Name != "x.png" {
			t.Errorf("name: got %q, want x.png", msg.Images[0].Name)
		}
		if !bytes.Equal(msg.Images[0].Data, pngBytes) {
			t.Errorf("data mismatch")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout reading input")
	}
}

func TestInputEndpoint_ImageOnly(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)

	reqBody := InputRequest{
		Text: "",
		Images: []ImageAttachment{
			{MediaType: "image/png", Data: []byte{0x89, 0x50}, Name: "x.png"},
		},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/input", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status code: got %d, want 202", w.Code)
	}

	select {
	case msg := <-srv.InputCh():
		if msg.Text != "" {
			t.Errorf("text: got %q, want empty", msg.Text)
		}
		if len(msg.Images) != 1 {
			t.Fatalf("images: got %d, want 1", len(msg.Images))
		}
	case <-time.After(time.Second):
		t.Fatal("timeout reading input")
	}
}

func TestCompactEndpoint(t *testing.T) {
	srv := NewServer(ServerConfig{})

	called := false
	srv.SetCompactFunc(func(ctx context.Context) error {
		called = true
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, "/compact", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status code: got %d, want 204", w.Code)
	}
	if !called {
		t.Error("compact function should have been called")
	}
}

func TestCompactEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/compact", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code: got %d, want 503", w.Code)
	}
}

func TestCompactEndpoint_Error(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetCompactFunc(func(ctx context.Context) error {
		return errors.New("compaction failed")
	})

	req := httptest.NewRequest(http.MethodPost, "/compact", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status code: got %d, want 500", w.Code)
	}
}

func TestCompactEndpoint_MethodNotAllowed(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/compact", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code: got %d, want 405", w.Code)
	}
}

func TestClearEndpoint(t *testing.T) {
	srv := NewServer(ServerConfig{})

	called := false
	srv.SetClearFunc(func(ctx context.Context) error {
		called = true
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, "/clear", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status code: got %d, want 204", w.Code)
	}
	if !called {
		t.Error("clear function not called")
	}
}

func TestClearEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/clear", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code: got %d, want 503", w.Code)
	}
}

func TestClear_409WhileProcessing(t *testing.T) {
	srv := NewServer(ServerConfig{})
	called := false
	srv.SetClearFunc(func(ctx context.Context) error {
		called = true
		return nil
	})
	srv.SetProcessing(true)

	req := httptest.NewRequest(http.MethodPost, "/clear", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want %d (409)", rec.Code, http.StatusConflict)
	}
	if called {
		t.Fatal("clearFunc should not have been called while processing")
	}
}

func TestClear_OKWhenIdle(t *testing.T) {
	srv := NewServer(ServerConfig{})
	called := false
	srv.SetClearFunc(func(ctx context.Context) error {
		called = true
		return nil
	})
	srv.SetProcessing(false)

	req := httptest.NewRequest(http.MethodPost, "/clear", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusNoContent)
	}
	if !called {
		t.Fatal("clearFunc should have been called when idle")
	}
}

func TestModelEndpoint(t *testing.T) {
	srv := NewServer(ServerConfig{})

	var gotModel string
	srv.SetModelFunc(func(model string) error {
		gotModel = model
		return nil
	})

	body := strings.NewReader(`{"model":"gpt-4o-mini"}`)
	req := httptest.NewRequest(http.MethodPost, "/model", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status code: got %d, want 204", w.Code)
	}
	if gotModel != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", gotModel)
	}
}

func TestModelEndpoint_EmptyModel(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetModelFunc(func(model string) error { return nil })

	body := strings.NewReader(`{"model":""}`)
	req := httptest.NewRequest(http.MethodPost, "/model", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status code: got %d, want 400", w.Code)
	}
}

func TestModelEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})

	body := strings.NewReader(`{"model":"gpt-4o"}`)
	req := httptest.NewRequest(http.MethodPost, "/model", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code: got %d, want 503", w.Code)
	}
}

func TestShutdown_InvokesCallback(t *testing.T) {
	srv := NewServer(ServerConfig{})
	called := make(chan struct{}, 1)
	srv.SetShutdownFunc(func() {
		called <- struct{}{}
	})

	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d (202)", rec.Code, http.StatusAccepted)
	}
	if got := rec.Result().Header.Get("Content-Length"); got != "0" {
		t.Fatalf("Content-Length = %q, want 0", got)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not invoked within 1s")
	}
}

func TestShutdown_WritesResponseBeforeCallback(t *testing.T) {
	srv := NewServer(ServerConfig{})
	called := make(chan struct{}, 1)
	srv.SetShutdownFunc(func() {
		called <- struct{}{}
	})

	rec := &blockingHeaderRecorder{
		header:        http.Header{},
		headerStarted: make(chan struct{}, 1),
		releaseHeader: make(chan struct{}),
		flushStarted:  make(chan struct{}, 1),
		releaseFlush:  make(chan struct{}),
	}
	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-rec.headerStarted:
	case <-time.After(time.Second):
		t.Fatal("shutdown response was not started")
	}
	select {
	case <-called:
		t.Fatal("shutdown callback ran before the response write completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(rec.releaseHeader)
	select {
	case <-rec.flushStarted:
	case <-time.After(time.Second):
		t.Fatal("shutdown response was not flushed")
	}
	select {
	case <-called:
		t.Fatal("shutdown callback ran before the response flush completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(rec.releaseFlush)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown handler did not complete")
	}
	if rec.status != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d (202)", rec.status, http.StatusAccepted)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not invoked after response completed")
	}
}

type blockingHeaderRecorder struct {
	header        http.Header
	headerStarted chan struct{}
	releaseHeader chan struct{}
	flushStarted  chan struct{}
	releaseFlush  chan struct{}
	status        int
}

func (r *blockingHeaderRecorder) Header() http.Header {
	return r.header
}

func (r *blockingHeaderRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return len(p), nil
}

func (r *blockingHeaderRecorder) WriteHeader(status int) {
	r.status = status
	select {
	case r.headerStarted <- struct{}{}:
	default:
	}
	<-r.releaseHeader
}

func (r *blockingHeaderRecorder) Flush() {
	select {
	case r.flushStarted <- struct{}{}:
	default:
	}
	<-r.releaseFlush
}

func TestShutdown_503WhenUnregistered(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want %d (503)", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestShutdown_RejectsGET(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetShutdownFunc(func() {})

	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestQueueEndpoint_Accepted(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	var received []string
	srv.SetQueueFunc(func(text string) error {
		received = append(received, text)
		return nil
	})

	body := strings.NewReader(`{"text":"queued msg"}`)
	req := httptest.NewRequest(http.MethodPost, "/queue", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want 202; body=%q", rec.Code, rec.Body.String())
	}
	if len(received) != 1 || received[0] != "queued msg" {
		t.Fatalf("received=%v", received)
	}
}

func TestQueueEndpoint_RejectsWhenIdle(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)
	srv.SetQueueFunc(func(string) error { return nil })

	body := strings.NewReader(`{"text":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/queue", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body=%q", rec.Code, rec.Body.String())
	}
}

func TestQueueEndpoint_RejectsEmptyText(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	srv.SetQueueFunc(func(string) error { return nil })
	body := strings.NewReader(`{"text":""}`)
	req := httptest.NewRequest(http.MethodPost, "/queue", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}

func TestQueueEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	body := strings.NewReader(`{"text":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/queue", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503; body=%q", rec.Code, rec.Body.String())
	}
}

func TestDrainAsSteerEndpoint_NoContent(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	called := 0
	srv.SetDrainAsSteerFunc(func() error { called++; return nil })
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.queue.Depth = 2 })

	req := httptest.NewRequest(http.MethodPost, "/drain-as-steer", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204; body=%q", rec.Code, rec.Body.String())
	}
	if called != 1 {
		t.Fatalf("called=%d, want 1", called)
	}
}

func TestDrainAsSteerEndpoint_WithInputBypassesEmptyQueue(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	srv.SetDrainAsSteerFunc(func() error {
		t.Fatal("classic drain callback should not be used for input-bearing drain")
		return nil
	})
	var gotText string
	var gotImages []ImageAttachment
	srv.SetDrainAsSteerWithInputFunc(func(text string, images []ImageAttachment) error {
		gotText = text
		gotImages = append([]ImageAttachment(nil), images...)
		return nil
	})
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.queue.Depth = 0 })

	body := strings.NewReader(`{"text":"composer payload","images":[{"media_type":"image/png","data":"cG5n","name":"shot.png"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/drain-as-steer", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204; body=%q", rec.Code, rec.Body.String())
	}
	if gotText != "composer payload" {
		t.Fatalf("text=%q, want composer payload", gotText)
	}
	if len(gotImages) != 1 || gotImages[0].Name != "shot.png" || string(gotImages[0].Data) != "png" {
		t.Fatalf("images=%+v", gotImages)
	}
}

func TestDrainAsSteerEndpoint_RejectsEmpty(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	srv.SetDrainAsSteerFunc(func() error { return nil })
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.queue.Depth = 0 })

	req := httptest.NewRequest(http.MethodPost, "/drain-as-steer", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409 (empty queue); body=%q", rec.Code, rec.Body.String())
	}
}

func TestDrainAsSteerEndpoint_RejectsWhenIdle(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)
	srv.SetDrainAsSteerFunc(func() error { return nil })

	req := httptest.NewRequest(http.MethodPost, "/drain-as-steer", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409 (idle); body=%q", rec.Code, rec.Body.String())
	}
}

func TestSubmitNotification_PushesEntryNotification(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SubmitNotification()
	select {
	case msg := <-srv.InputCh():
		if msg.Kind != agent.EntryNotification {
			t.Errorf("Kind: got %v, want EntryNotification", msg.Kind)
		}
		if msg.Text != "" {
			t.Errorf("Text: got %q, want empty", msg.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: SubmitNotification did not deliver a message")
	}
}

func TestSubmitNotification_DropIfFull(t *testing.T) {
	srv := NewServer(ServerConfig{})
	// Fill the 1-slot buffer.
	srv.SubmitNotification()
	// Second call must not block even though the channel is full.
	done := make(chan struct{})
	go func() {
		srv.SubmitNotification()
		close(done)
	}()
	select {
	case <-done:
		// expected: returned without blocking
	case <-time.After(time.Second):
		t.Fatal("SubmitNotification blocked on full channel")
	}
}

func TestSteerEndpoint(t *testing.T) {
	srv := NewServer(ServerConfig{})
	var gotText string
	srv.SetSteerFunc(func(text string) {
		gotText = text
	})

	body := strings.NewReader(`{"text":"steer this"}`)
	req := httptest.NewRequest(http.MethodPost, "/steer", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status code: got %d, want 204", w.Code)
	}
	if gotText != "steer this" {
		t.Errorf("text = %q, want steer this", gotText)
	}
}

func TestSteerEndpoint_EmptyText(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetSteerFunc(func(text string) {})

	body := strings.NewReader(`{"text":""}`)
	req := httptest.NewRequest(http.MethodPost, "/steer", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status code: got %d, want 400", w.Code)
	}
}

func TestSteerEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})

	body := strings.NewReader(`{"text":"steer this"}`)
	req := httptest.NewRequest(http.MethodPost, "/steer", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status code: got %d, want 204", w.Code)
	}
}

func TestSteerEndpoint_MethodNotAllowed(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetSteerFunc(func(text string) {})

	req := httptest.NewRequest(http.MethodGet, "/steer", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code: got %d, want 405", w.Code)
	}
}

func TestTasksEndpoint(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetTasksFunc(func() any {
		return []map[string]any{
			{"id": "1", "name": "task one"},
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", w.Code)
	}
	var resp []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 1 || resp[0]["id"] != "1" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestTasksEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", w.Code)
	}
	if w.Body.String() != "[]\n" {
		t.Errorf("body = %q, want []\\n", w.Body.String())
	}
}

func TestTasksEndpoint_MethodNotAllowed(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetTasksFunc(func() any { return nil })

	req := httptest.NewRequest(http.MethodPost, "/tasks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code: got %d, want 405", w.Code)
	}
}

func TestInterruptEndpoint_MethodNotAllowed(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetCancelFunc(func() {})

	req := httptest.NewRequest(http.MethodGet, "/interrupt", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code: got %d, want 405", w.Code)
	}
}

func TestQueueEndpoint_InvalidJSON(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	srv.SetQueueFunc(func(string) error { return nil })

	req := httptest.NewRequest(http.MethodPost, "/queue", strings.NewReader(`{bad`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}

func TestQueueEndpoint_FuncError(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	srv.SetQueueFunc(func(string) error { return errors.New("queue fail") })

	body := strings.NewReader(`{"text":"queued msg"}`)
	req := httptest.NewRequest(http.MethodPost, "/queue", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500; body=%q", rec.Code, rec.Body.String())
	}
}

func TestDrainAsSteerEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.queue.Depth = 2 })

	req := httptest.NewRequest(http.MethodPost, "/drain-as-steer", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503; body=%q", rec.Code, rec.Body.String())
	}
}

func TestDrainAsSteerEndpoint_ClosedSession(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)
	srv.SetState("closed")
	srv.SetDrainAsSteerFunc(func() error { return nil })

	req := httptest.NewRequest(http.MethodPost, "/drain-as-steer", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body=%q", rec.Code, rec.Body.String())
	}
}

func TestDrainAsSteerEndpoint_InvalidJSON(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	srv.SetDrainAsSteerFunc(func() error { return nil })
	srv.SetDrainAsSteerWithInputFunc(func(string, []ImageAttachment) error { return nil })
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.queue.Depth = 0 })

	req := httptest.NewRequest(http.MethodPost, "/drain-as-steer", strings.NewReader(`{bad`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}

func TestModelEndpoint_InvalidJSON(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetModelFunc(func(string) error { return nil })

	req := httptest.NewRequest(http.MethodPost, "/model", strings.NewReader(`{bad`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}

func TestClearEndpoint_FuncError(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)
	srv.SetClearFunc(func(ctx context.Context) error {
		return errors.New("clear failed")
	})

	req := httptest.NewRequest(http.MethodPost, "/clear", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500; body=%q", rec.Code, rec.Body.String())
	}
}

func TestInputEndpoint_InvalidJSON(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)

	req := httptest.NewRequest(http.MethodPost, "/input", strings.NewReader(`{bad`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}

func TestInputEndpoint_FullChannel(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)
	// Fill the 1-slot buffer.
	select {
	case srv.inputCh <- InputMessage{Text: "blocked"}:
	default:
	}

	body := strings.NewReader(`{"text":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/input", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body=%q", rec.Code, rec.Body.String())
	}
}

func TestSubmitContinuation(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SubmitContinuation("continue")
	select {
	case msg := <-srv.InputCh():
		if msg.Kind != agent.EntryContinuation {
			t.Errorf("Kind: got %v, want EntryContinuation", msg.Kind)
		}
		if msg.Text != "continue" {
			t.Errorf("Text: got %q, want continue", msg.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: SubmitContinuation did not deliver a message")
	}
}

func TestSubmitContinuation_DropIfFull(t *testing.T) {
	srv := NewServer(ServerConfig{})
	// Fill the 1-slot buffer.
	srv.SubmitContinuation("first")
	// Second call must not block even though the channel is full.
	done := make(chan struct{})
	go func() {
		srv.SubmitContinuation("second")
		close(done)
	}()
	select {
	case <-done:
		// expected: returned without blocking
	case <-time.After(time.Second):
		t.Fatal("SubmitContinuation blocked on full channel")
	}
}

func TestServerAppWireThreadList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadList, appwire.ThreadListParams{}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	out, ok := resp.Response.Result.(appwire.ThreadListResponse)
	if !ok {
		t.Fatalf("thread/list result=%T (%+v)", resp.Response.Result, resp)
	}
	if len(out.Data) != 1 {
		t.Fatalf("thread/list data: got %d threads, want 1", len(out.Data))
	}
	thread := out.Data[0]
	if thread.ID != "th_1" {
		t.Errorf("thread ID: got %q, want th_1", thread.ID)
	}
	if thread.Source != "local" {
		t.Errorf("thread Source: got %q, want local", thread.Source)
	}
	if thread.Evener.Ref != "local:th_1" {
		t.Errorf("thread Evener.Ref: got %q, want local:th_1", thread.Evener.Ref)
	}
}

func TestServerAppWireTasksList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetTasksFunc(func() any {
		return []map[string]any{{"id": "1"}}
	})

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerTasksList, appwire.TaskListParams{}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	out, ok := resp.Response.Result.(appwire.TaskListResponse)
	if !ok {
		t.Fatalf("evener/tasks/list result=%T (%+v)", resp.Response.Result, resp)
	}
	tasks, ok := out.Data.([]map[string]any)
	if !ok {
		t.Fatalf("task data type=%T, want []map[string]any", out.Data)
	}
	if len(tasks) != 1 {
		t.Fatalf("task data: got %d tasks, want 1", len(tasks))
	}
	if tasks[0]["id"] != "1" {
		t.Errorf("task id: got %v, want 1", tasks[0]["id"])
	}
}

func TestHandleAppJobsListNilFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerJobsList, appwire.JobsListParams{}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v (%+v)", resp.Kind(), resp.Error)
	}
	out, ok := resp.Response.Result.(appwire.JobsListResponse)
	if !ok {
		t.Fatalf("evener/jobs/list result=%T (%+v)", resp.Response.Result, resp)
	}
	if out.Data != nil {
		t.Errorf("data: got %+v, want nil", out.Data)
	}
}

func TestHandleAppJobsList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetJobsFunc(func(got appwire.JobsListParams) (any, error) {
		if got.Ref != "local:root" || got.Continuation != "next" {
			t.Fatalf("params=%+v", got)
		}
		return appwire.JobActivityTree{Root: appwire.JobActivitySession{SessionID: "root", Ref: "local:root"}}, nil
	})

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerJobsList, appwire.JobsListParams{Ref: "local:root", Continuation: "next"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v (%+v)", resp.Kind(), resp.Error)
	}
	out, ok := resp.Response.Result.(appwire.JobsListResponse)
	if !ok {
		t.Fatalf("evener/jobs/list result=%T (%+v)", resp.Response.Result, resp)
	}
	tree, ok := out.Data.(appwire.JobActivityTree)
	if !ok {
		t.Fatalf("jobs data type=%T, want appwire.JobActivityTree", out.Data)
	}
	if tree.Root.SessionID != "root" || tree.Root.Ref != "local:root" {
		t.Fatalf("tree=%+v", tree)
	}
}

// A jobs source that cannot read its store answers the wire with a failure,
// not with the empty list a job-less session answers: "no jobs ran" and "I
// can't tell you what ran" must not arrive as the same response.
func TestHandleAppJobsListSourceError(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetJobsFunc(func(appwire.JobsListParams) (any, error) {
		return nil, errors.New("jobstore: parse event line 3: unexpected end of JSON input")
	})

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerJobsList, appwire.JobsListParams{}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("resp=%v (%+v), want an error response", resp.Kind(), resp.Response)
	}
	if !strings.Contains(resp.Error.Error.Message, "parse event line 3") {
		t.Errorf("error message: %+v", resp.Error.Error)
	}
}

func TestHandleAppJobsOutputNilFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerJobsOutput, appwire.JobsOutputParams{JobID: "job_1"}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("resp=%v, want error", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeUnavailable {
		t.Errorf("error code: got %d, want %d", resp.Error.Error.Code, appwire.CodeUnavailable)
	}
	data, ok := resp.Error.Error.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("error data type=%T, want appwire.ErrorData", resp.Error.Error.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorActionUnavailable {
		t.Errorf("evenerErrorInfo: got %q, want actionUnavailable", data.EvenerErrorInfo)
	}
}

func TestHandleAppJobsOutputNotFound(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetJobOutputFunc(func(string, int64, int64) (any, bool, error) { return nil, false, nil })

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerJobsOutput, appwire.JobsOutputParams{JobID: "job_missing"}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("resp=%v, want error", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeInvalidParams {
		t.Errorf("error code: got %d, want %d", resp.Error.Error.Code, appwire.CodeInvalidParams)
	}
	if !strings.Contains(resp.Error.Error.Message, "job_missing") {
		t.Errorf("error message %q does not carry the job id", resp.Error.Error.Message)
	}
}

func TestHandleAppJobsOutput(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetJobOutputFunc(func(jobID string, beforeBytes, maxBytes int64) (any, bool, error) {
		if jobID != "job_1" {
			t.Errorf("jobID = %q, want job_1", jobID)
		}
		if beforeBytes != 7 {
			t.Errorf("beforeBytes = %d, want 7", beforeBytes)
		}
		if maxBytes != 99 {
			t.Errorf("maxBytes = %d, want 99", maxBytes)
		}
		return agent.JobOutputTail{Tail: "hi", TotalBytes: 2}, true, nil
	})

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodEvenerJobsOutput, appwire.JobsOutputParams{JobID: "job_1", BeforeBytes: 7, MaxBytes: 99}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v (%+v)", resp.Kind(), resp.Error)
	}
	out, ok := resp.Response.Result.(appwire.JobsOutputResponse)
	if !ok {
		t.Fatalf("evener/jobs/output result=%T (%+v)", resp.Response.Result, resp)
	}
	tail, ok := out.Data.(agent.JobOutputTail)
	if !ok {
		t.Fatalf("output data type=%T, want agent.JobOutputTail", out.Data)
	}
	if tail.Tail != "hi" {
		t.Errorf("tail: got %q, want hi", tail.Tail)
	}
}

func TestServerAppWireModelList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetListModelsFunc(func(ctx context.Context) ([]appwire.ModelDescriptor, error) {
		return []appwire.ModelDescriptor{{Model: "gpt-4o"}}, nil
	})

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodModelList, appwire.ModelListParams{}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	out, ok := resp.Response.Result.(appwire.ModelListResponse)
	if !ok {
		t.Fatalf("model/list result=%T (%+v)", resp.Response.Result, resp)
	}
	if len(out.Data) != 1 {
		t.Fatalf("model/list data: got %d models, want 1", len(out.Data))
	}
	if out.Data[0].Model != "gpt-4o" {
		t.Errorf("model: got %q, want gpt-4o", out.Data[0].Model)
	}
	if out.Data[0].Provider != "" {
		t.Errorf("provider: got %q, want empty (no profile set)", out.Data[0].Provider)
	}
}

func TestServerAppWireModelListEmptyDataIsArray(t *testing.T) {
	srv := NewServer(ServerConfig{})
	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodModelList, appwire.ModelListParams{}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	out, ok := resp.Response.Result.(appwire.ModelListResponse)
	if !ok {
		t.Fatalf("model/list result=%T (%+v)", resp.Response.Result, resp)
	}
	if out.Data == nil {
		t.Fatal("model/list data = nil, want an empty JSON array")
	}
}
