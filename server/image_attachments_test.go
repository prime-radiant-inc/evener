package server

// Wire-level tests for image attachment round-trip (kata t5j6). Each test
// exercises an appwire entry point on the daemon side and asserts that an
// InputItem with type=image arrives at the session boundary as an
// ImageAttachment (which the session then translates into an llm.ContentImage
// part via buildUserInputMessage — covered by existing agent tests).

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/llm"
)

// pngSig is a tiny PNG signature used as opaque image bytes.
var pngSig = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// TestServerAppWireTurnQueueImageItemReachesQueueFunc exercises Path 3:
// a turn/queue request that carries an InputItem of type "image" must
// deliver the bytes to the daemon's queueFunc as an EnqueueWithImages-
// equivalent call. We observe via a queueFunc shim that captures the
// arguments.
func TestServerAppWireTurnQueueImageItemReachesQueueFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_qimg")
	srv.SetProcessing(true) // queue requires an in-flight turn

	var gotText string
	var gotImages []ImageAttachment
	srv.SetQueueWithImagesFunc(func(text string, images []ImageAttachment) error {
		gotText = text
		gotImages = images
		return nil
	})

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref: "local:th_qimg",
		Input: []appwire.InputItem{{Type: "text", Text: "queued describe"}, {
			Type:      "image",
			MediaType: "image/png",
			Data:      pngSig,
			Name:      "q.png",
		}},
	}))
	if resp.Kind() != appwire.MessageResponse {
		if raw, err := json.Marshal(resp); err == nil {
			t.Fatalf("turn/queue response=%s", raw)
		}
		t.Fatalf("turn/queue kind=%v", resp.Kind())
	}
	if gotText != "queued describe" {
		t.Errorf("queueFunc text=%q, want %q", gotText, "queued describe")
	}
	if len(gotImages) != 1 {
		t.Fatalf("queueFunc images: got %d, want 1", len(gotImages))
	}
	img := gotImages[0]
	if img.MediaType != "image/png" || !bytes.Equal(img.Data, pngSig) {
		t.Errorf("queueFunc image mismatch: media=%q data=%x", img.MediaType, img.Data)
	}
	if img.Name != "q.png" {
		t.Errorf("queueFunc image name=%q, want q.png", img.Name)
	}
}

// TestServerAppWireTurnDrainAsSteerThroughSessionProducesImageBearingSteer
// exercises Path 4 end-to-end: turn/queue with image items followed by
// turn/drainAsSteer must produce a steering queue entry on the actual
// agent session that carries the image bytes. We register a real
// agent.Session as the daemon's queue + drain backend so the wire path,
// the session's queue, and DrainAsSteer's SteerWithImages call all
// participate.
func TestServerAppWireTurnDrainAsSteerThroughSessionProducesImageBearingSteer(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &blockingServerAdapter{name: "openai", started: make(chan struct{}), done: make(chan error, 1)}
	c.Register(adapter)
	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("gpt-5.2"), agent.NewLocalExecutionEnvironment(dir), agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = sess.ProcessInput(ctx, "keep turn active", nil)
	}()
	select {
	case <-adapter.started:
	case <-time.After(time.Second):
		t.Fatal("session did not enter active turn")
	}

	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetProcessing(true)
	srv.SetQueueWithImagesFunc(func(text string, images []ImageAttachment) error {
		return sess.EnqueueWithImages(context.Background(), text, images)
	})
	srv.SetDrainAsSteerFunc(func() error { return sess.DrainAsSteer(context.Background()) })
	srv.SetQueueDepthFunc(sess.QueueDepth)

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	if r := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref: "local:" + sess.ID(),
		Input: []appwire.InputItem{{Type: "text", Text: "drain me"}, {
			Type:      "image",
			MediaType: "image/png",
			Data:      pngSig,
			Name:      "d.png",
		}},
	})); r.Kind() != appwire.MessageResponse {
		raw, _ := json.Marshal(r)
		t.Fatalf("turn/queue: %s", raw)
	}
	if r := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{
		Ref: "local:" + sess.ID(),
	})); r.Kind() != appwire.MessageResponse {
		raw, _ := json.Marshal(r)
		t.Fatalf("turn/drainAsSteer: %s", raw)
	}

	// At this point the session's steering queue must hold one entry
	// whose text contains "drain me" and whose images carry our bytes.
	preview := sess.SteeringQueueSnapshot()
	if len(preview) != 1 {
		t.Fatalf("steering queue: got %d entries, want 1", len(preview))
	}
	entry := preview[0]
	if !bytes.Contains([]byte(entry.Text), []byte("drain me")) {
		t.Errorf("steering text=%q, want to contain %q", entry.Text, "drain me")
	}
	if len(entry.Images) != 1 || entry.Images[0].MediaType != "image/png" || !bytes.Equal(entry.Images[0].Data, pngSig) {
		t.Errorf("steering image mismatch: %+v", entry.Images)
	}
	cancel()
	select {
	case <-adapter.done:
	case <-time.After(time.Second):
		t.Fatal("active turn did not stop after cancellation")
	}
}

// fakeServerAdapter is a minimal llm.Adapter shim used in this package's
// session-backed wire tests. The session never executes a turn here — we
// only need it to accept queue/steer mutations — so a no-op adapter is
// sufficient.
type fakeServerAdapter struct{ name string }

func (a *fakeServerAdapter) Name() string { return a.name }
func (a *fakeServerAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("noop")}, nil
}
func (a *fakeServerAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

type blockingServerAdapter struct {
	name    string
	started chan struct{}
	done    chan error
}

func (a *blockingServerAdapter) Name() string { return a.name }
func (a *blockingServerAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	close(a.started)
	<-ctx.Done()
	err := ctx.Err()
	a.done <- err
	return llm.Response{Provider: a.name, Model: req.Model}, err
}
func (a *blockingServerAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// TestServerAppWireTurnStartImageItemReachesInputCh exercises Path 2: a
// turn/start request with an InputItem of type "image" must put the
// matching ImageAttachment on the daemon's InputCh, so ProcessInput can
// build the ContentImage user-message part.
func TestServerAppWireTurnStartImageItemReachesInputCh(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_img1")

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref: "local:th_img1",
		Input: append([]appwire.InputItem{{Type: "text", Text: "describe this"}}, appwire.InputItem{
			Type:      "image",
			MediaType: "image/png",
			Data:      pngSig,
			Name:      "shot.png",
		}),
	}))
	if resp.Kind() != appwire.MessageResponse {
		// Surface the wire error so a regression in handler signature is obvious.
		if raw, err := json.Marshal(resp); err == nil {
			t.Fatalf("turn/start response=%s", raw)
		}
		t.Fatalf("turn/start kind=%v", resp.Kind())
	}

	select {
	case msg := <-srv.InputCh():
		if msg.Text != "describe this" {
			t.Errorf("text=%q, want %q", msg.Text, "describe this")
		}
		if len(msg.Images) != 1 {
			t.Fatalf("images: got %d, want 1", len(msg.Images))
		}
		img := msg.Images[0]
		if img.MediaType != "image/png" {
			t.Errorf("media_type=%q, want image/png", img.MediaType)
		}
		if !bytes.Equal(img.Data, pngSig) {
			t.Errorf("data mismatch: got %x, want %x", img.Data, pngSig)
		}
		if img.Name != "shot.png" {
			t.Errorf("name=%q, want shot.png", img.Name)
		}
	default:
		t.Fatal("InputCh was not signaled; image-bearing turn/start did not deliver an InputMessage")
	}
}
