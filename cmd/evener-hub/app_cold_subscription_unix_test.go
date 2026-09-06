//go:build darwin || linux

package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func TestHubRPCColdSnapshotOwnsResumeUntilSubscribed(t *testing.T) {
	var sessionID string
	cfg, id, _ := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			appserver.Subscribe(ctx, sessionID)
			return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
		})
		appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			daemon.Broadcast(sessionID, appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{ThreadID: sessionID, Ref: "local:" + sessionID, TurnID: "turn", ItemID: "item", Delta: "overlap-marker"})
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn"}}, nil
		})
	})
	sessionID = id
	cfg.ResumeLocks = hubcore.NewResumeLocks()
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	observer, actor := dialHubRPC(t, hub), dialHubRPC(t, hub)
	defer observer.Close()
	defer actor.Close()
	for _, client := range []*appwire.Client{observer, actor} {
		if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
	}
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("missing fixture")
	}
	path := filepath.Join(entry.StateDir, "sessions", sessionID, "delegates.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// A FIFO is the filesystem boundary: opening its writer proves the actual
	// saved projection has opened the journal. Withhold EOF to hold that read.
	writerReady := make(chan *os.File, 1)
	writerError := make(chan error, 1)
	go func() {
		writer, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			writerError <- err
			return
		}
		writerReady <- writer
	}()
	var writer *os.File
	defer func() {
		if writer == nil {
			// Unblock either FIFO opener on a failing test before closing the hub.
			fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
			if err == nil {
				_ = unix.Close(fd)
			}
			select {
			case writer = <-writerReady:
			case <-writerError:
			case <-time.After(time.Second):
			}
		}
		_ = os.Remove(path)
		if writer != nil {
			_ = writer.Close()
		}
	}()
	readDone := make(chan error, 1)
	go func() {
		_, err := observer.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, Subscribe: true})
		readDone <- err
	}()
	select {
	case writer = <-writerReady:
	case err := <-writerError:
		t.Fatal(err)
	case err := <-readDone:
		t.Fatalf("read returned before filesystem barrier: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("saved read never reached filesystem barrier")
	}
	// This is the same lock the real hub resume path must acquire, not a
	// scheduling-based assertion that a goroutine has not run yet.
	lock := cfg.ResumeLocks.For(sessionID)
	if lock.TryLock() {
		lock.Unlock()
		t.Fatal("saved snapshot permits a concurrent resume before subscription capture")
	}
	turnDone := make(chan error, 1)
	go func() {
		_, err := actor.TurnStart(t.Context(), appwire.TurnStartParams{Ref: "local:" + sessionID, ExpectedInstanceID: sessionID, ClientMutationID: "overlapping-resume", Input: []appwire.InputItem{{Type: "text", Text: "resume"}}})
		turnDone <- err
	}()
	// Remove the path before releasing the reader so later live metadata reads
	// cannot open the barrier again. The existing descriptor remains valid.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString("{\"version\":1}\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, done := range []<-chan error{readDone, turnDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("read/resume did not complete")
		}
	}
	select {
	case event := <-observer.Notifications():
		var delta appwire.AgentMessageDeltaParams
		if event.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("unexpected notification %q", event.Method)
		}
		if err := json.Unmarshal(event.Params, &delta); err != nil {
			t.Fatal(err)
		}
		if delta.Ref != "local:"+sessionID || delta.Delta != "overlap-marker" {
			t.Fatalf("unexpected delta %+v", delta)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("observer lost the overlapping resume event")
	}
}
