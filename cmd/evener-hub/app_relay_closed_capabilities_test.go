package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// A daemon stamps every status frame with the action set that goes with the
// status it announces — except its own close, which it cannot describe: what a
// thread can still be asked to do once its daemon is gone is the hub's answer,
// not the corpse's (server/appwire_runtime.go's stampCapabilitiesOnStatusChange
// and kata 06t8). This is the hub giving that answer on the way past.
func TestRelayedCloseFrameCarriesTheHubsCapabilitiesForTheEndedThread(t *testing.T) {
	thread := appwire.Thread{
		ID:        "thread-closed",
		SessionID: "thread-closed",
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:thread-closed"},
	}
	deliveries := make(chan appsource.RelayDelivery, 1)
	acknowledged := make(chan struct{})
	handoff := &recordingRelayHandoff{
		committed: make(chan struct{}),
		aborted:   make(chan struct{}),
		onCommit: func() {
			deliveries <- appsource.RelayDelivery{
				Notification: appwire.Notification{
					Method: appwire.NotifyThreadStatusChanged,
					// Verbatim what a closing daemon sends: a status and nothing else.
					Params: testRawJSON(t, appwire.ThreadStatusChangedParams{
						ThreadID: thread.ID,
						Ref:      thread.Evener.Ref,
						Status:   appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
					}),
				},
				Acknowledge: func() { close(acknowledged) },
			}
		},
	}
	client := relayedNotificationClient(t, thread, deliveries, handoff)

	notification := <-client.Notifications()
	<-acknowledged

	if notification.Method != appwire.NotifyThreadStatusChanged {
		t.Fatalf("notification method = %q, want %q", notification.Method, appwire.NotifyThreadStatusChanged)
	}
	var params appwire.ThreadStatusChangedParams
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		t.Fatalf("unmarshal relayed close frame: %v", err)
	}
	if params.Status.Type != appwire.ThreadStatusClosed {
		t.Fatalf("relayed status = %q, want %q", params.Status.Type, appwire.ThreadStatusClosed)
	}
	if params.Capabilities == nil {
		t.Fatalf("relayed close frame carried no capabilities; the client keeps the mid-turn set the departing daemon left behind")
	}
	want := pastThreadCapabilities()
	if *params.Capabilities != want {
		t.Fatalf("relayed close capabilities = %+v, want %+v", *params.Capabilities, want)
	}
}

// The invariant, stated: what the close frame pushes is what the very next read
// returns. A reload is what used to heal a session that ended mid-turn, and it
// healed it by asking the hub — so the pushed set has to BE the hub's answer,
// not a second opinion that drifts away from it.
func TestClosedThreadCapabilitiesMatchTheReadThatWouldAnswerTheReload(t *testing.T) {
	entry := hubcore.PastEntry{
		ID:       "sess_ended",
		Meta:     schema.SessionMeta{ID: "sess_ended", Model: "claude-opus-4-5"},
		StateDir: t.TempDir(),
	}
	read := requirePastEntryThread(t, hubcore.WebConfig{}, entry, false)

	if got := pastThreadCapabilities(); got != read.Evener.Capabilities {
		t.Fatalf("close-frame capabilities = %+v, want the read's %+v", got, read.Evener.Capabilities)
	}
	if !read.Evener.Capabilities.Send {
		t.Fatalf("a cold thread's read advertises send=false; the composer this fix restores would have nothing to send with")
	}
}

// Every other status is the daemon's own to describe, and it already does. The
// hub rewriting those would be the same wedge in the other direction: a live
// daemon's set replaced by the set for a thread that is not running.
func TestRelayedStatusFramesOtherThanCloseAreLeftToTheDaemon(t *testing.T) {
	daemonSet := appwire.ThreadCapabilities{Send: true, Compact: true, Interrupt: true}
	original := appwire.Notification{
		Method: appwire.NotifyThreadStatusChanged,
		Params: testRawJSON(t, appwire.ThreadStatusChangedParams{
			ThreadID:     "thread-idle",
			Ref:          "local:thread-idle",
			Status:       appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Capabilities: &daemonSet,
		}),
	}

	got := stampClosedThreadCapabilities(original)

	if string(got.Params) != string(original.Params) {
		t.Fatalf("idle frame was rewritten to %s, want it untouched (%s)", got.Params, original.Params)
	}
}

// The stamp reaches into one field of one method. Anything else on the local
// relay's stream — every item, every turn — passes through byte for byte.
func TestNonStatusNotificationsPassTheCloseStampUntouched(t *testing.T) {
	original := appwire.Notification{
		Method: appwire.NotifyTurnCompleted,
		Params: testRawJSON(t, map[string]any{
			"threadId": "thread-turn",
			"ref":      "local:thread-turn",
			"turn":     appwire.Turn{ID: "turn_5", Status: appwire.TurnStatusCompleted},
		}),
	}

	got := stampClosedThreadCapabilities(original)

	if string(got.Params) != string(original.Params) {
		t.Fatalf("turn/completed was rewritten to %s, want it untouched (%s)", got.Params, original.Params)
	}
}

// A close frame carrying fields this hub does not know about keeps them: the
// stamp adds one key, it does not re-mint the payload from the fields it
// happens to understand (the shape enrichOutputImageNotification uses on the
// same stream, for the same forward-compatibility reason).
func TestCloseStampPreservesFieldsItDoesNotUnderstand(t *testing.T) {
	original := appwire.Notification{
		Method: appwire.NotifyThreadStatusChanged,
		Params: testRawJSON(t, map[string]any{
			"threadId":       "thread-closed",
			"ref":            "local:thread-closed",
			"status":         appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
			"somethingNewer": "keep me",
		}),
	}

	got := stampClosedThreadCapabilities(original)

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got.Params, &fields); err != nil {
		t.Fatalf("unmarshal stamped close frame: %v", err)
	}
	if string(fields["somethingNewer"]) != `"keep me"` {
		t.Fatalf("unknown field after stamp = %s, want %q", fields["somethingNewer"], "keep me")
	}
	if len(fields["capabilities"]) == 0 {
		t.Fatalf("stamped close frame carried no capabilities: %s", got.Params)
	}
}

func TestHubRelayRelayKeyImageMetadata(t *testing.T) {
	const (
		rootRef    = "local:image-root"
		childRef   = "local:image-child"
		missingRef = "local:image-missing"
	)
	rootDir := t.TempDir()
	childDir := t.TempDir()
	imageBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	for path, contents := range map[string][]byte{
		filepath.Join(rootDir, "root.png"):                    imageBytes,
		filepath.Join(rootDir, "borrowed-only-from-root.png"): imageBytes,
		filepath.Join(childDir, "child.png"):                  imageBytes,
	} {
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pool := &aliasRelayPool{}
	source := &aliasRelaySource{
		pool: pool,
		canonicalRefs: map[string]appwire.Ref{
			"image-root":    {SourceID: "local", ThreadID: "image-root"},
			"image-child":   {SourceID: "local", ThreadID: "image-root"},
			"image-missing": {SourceID: "local", ThreadID: "image-root"},
		},
		threads: map[string]appwire.Thread{
			"image-root": {
				ID: "image-root", SessionID: "root-session", Source: "local", CWD: rootDir,
				Evener: appwire.EvenerThread{Ref: rootRef},
			},
			"image-child": {
				ID: "image-child", SessionID: "child-session", Source: "local", CWD: childDir,
				Evener: appwire.EvenerThread{Ref: childRef},
			},
			"image-missing": {
				ID: "image-missing", Source: "local",
				Evener: appwire.EvenerThread{Ref: missingRef},
			},
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize client: %v", err)
	}
	for _, ref := range []string{rootRef, childRef, missingRef} {
		if _, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
			t.Fatalf("subscribe client to %s: %v", ref, err)
		}
	}
	awaitLiveHubSubscriptions(t, appServer, 3)

	tests := []struct {
		name       string
		ref        string
		threadID   string
		itemID     string
		callID     string
		path       string
		wantImages []appwire.OutputImage
	}{
		{name: "root uses root metadata", ref: rootRef, threadID: "image-root", itemID: "root-item", callID: "root-call", path: "root.png", wantImages: []appwire.OutputImage{{Source: "written-file", Path: "root.png"}}},
		{name: "child uses child metadata", ref: childRef, threadID: "image-child", itemID: "child-item", callID: "child-call", path: "child.png", wantImages: []appwire.OutputImage{{Source: "written-file", Path: "child.png"}}},
		{name: "missing metadata does not borrow root", ref: missingRef, threadID: "image-missing", itemID: "missing-item", callID: "missing-call", path: "borrowed-only-from-root.png"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			started := appwire.NotificationMessage(appwire.NotifyItemStarted, map[string]any{
				"ref": tc.ref, "threadId": tc.threadID, "turnId": "turn-image",
				"item": appwire.ThreadItem{
					Type: "commandExecution", ID: tc.itemID, ToolName: "write_file", CallID: tc.callID,
					ArgumentsJSON: `{"file_path":"` + tc.path + `"}`, Status: appwire.TurnStatusInProgress,
				},
			}).Notification
			pool.emit(t, *started)
			completed := appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
				"ref": tc.ref, "threadId": tc.threadID, "turnId": "turn-image",
				"item": appwire.ThreadItem{
					Type: "commandExecution", ID: tc.itemID, ToolName: "write_file", CallID: tc.callID,
					Output: "wrote", Status: appwire.TurnStatusCompleted,
				},
			}).Notification
			pool.emit(t, *completed)
			appServer.BroadcastAll("test/alias-barrier", map[string]any{})
			items := aliasCompletedItemsUntilBarrier(t, client, tc.itemID)
			if len(items) != 1 {
				t.Fatalf("completed item broadcasts = %d, want exactly one", len(items))
			}
			got := items[0].OutputImages
			if len(got) != len(tc.wantImages) {
				t.Fatalf("OutputImages = %+v, want %+v", got, tc.wantImages)
			}
			for i := range got {
				if got[i].Source != tc.wantImages[i].Source || got[i].Path != tc.wantImages[i].Path {
					t.Fatalf("OutputImages = %+v, want %+v", got, tc.wantImages)
				}
			}
		})
	}
}

// relayedNotificationClient stands up the hub in front of a scripted local
// relay session and subscribes to it, so a test can assert on the notification
// a browser really receives rather than on the daemon's own bytes.
func relayedNotificationClient(
	t *testing.T,
	thread appwire.Thread,
	deliveries chan appsource.RelayDelivery,
	handoff *recordingRelayHandoff,
) *appwire.Client {
	t.Helper()
	source := &relaySessionTestSource{
		thread: thread,
		lease: &scriptedRelaySessionLease{
			readResult: appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: thread},
				Handoff:  handoff,
			},
			deliveries: deliveries,
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       thread.Evener.Ref,
		Subscribe: true,
	}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	return client
}
