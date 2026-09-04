package tui

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// captureReadServer answers thread/read with the persisted transcript through
// the real subscription capture, so a subscribed read is an exact cut on the
// wire: the response carries every record the source had projected, and
// Broadcast allocates its sequence above every cut the server has handed out.
func captureReadServer(app *appserver.Server, reads *[]appwire.ThreadReadParams) {
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if reads != nil {
			*reads = append(*reads, params)
		}
		if params.Subscribe {
			appserver.CaptureSubscription(
				ctx,
				params.ReplaceSubscription,
				func() string { return "01SEND" },
				func() uint64 { return 0 },
				func() bool { return true },
			)
		}
		return appwire.ThreadReadResponse{Thread: deadGenerationThread()}, nil
	})
}

// awaitPostCutFrame waits until the feed has actually observed the broadcast
// and filed it above the open read's cut. That is the condition the test needs,
// and a request round trip does not establish it: while a subscription is still
// buffering after a subscribed read, Subscriptions.Route holds a concurrent
// broadcast in the subscription buffer, and the buffer is flushed by
// Server.releaseHydration from the finalizer that runs AFTER the read response
// is already queued. A later request can be answered out of that window, so its
// response says nothing about where the broadcast is.
func awaitPostCutFrame(t *testing.T, feed *hubFrameFeed, text string) {
	t.Helper()
	// TRIPWIRE: the broadcast is one loopback hop away and normally lands in
	// microseconds; this bound only fires when it is never coming at all.
	deadline := time.Now().Add(30 * time.Second)
	for !feedHoldsPostCutFrame(feed, text) {
		if time.Now().After(deadline) {
			t.Fatalf("broadcast %q never reached the read capture's post-cut side", text)
		}
		time.Sleep(time.Millisecond)
	}
}

// feedHoldsPostCutFrame reports whether the open capture is already holding a
// frame carrying text on the far side of its cut.
func feedHoldsPostCutFrame(feed *hubFrameFeed, text string) bool {
	feed.mu.Lock()
	defer feed.mu.Unlock()
	if feed.capture == nil {
		return false
	}
	for _, notification := range feed.capture.after {
		if strings.Contains(string(notification.Params), text) {
			return true
		}
	}
	return false
}

// pendingHubNotifications reports every notification the model's feed is
// willing to hand it right now, without waiting for one to arrive.
func pendingHubNotifications(m hubModel) []appwire.Notification {
	var out []appwire.Notification
	for {
		select {
		case notification, ok := <-m.frames.Notifications():
			if !ok {
				return out
			}
			out = append(out, notification)
		default:
			return out
		}
	}
}

// A re-read's response is an exact cut, so a frame the source commits after it
// belongs strictly AFTER the snapshot. appwire hands the response to the
// requesting goroutine and the notification to a shared channel, and the two
// reach a bubbletea model from different goroutines with no ordering between
// them — so a post-cut frame folded first is destroyed by the
// replaceSessionTranscript that follows it, leaving it in neither the snapshot
// nor the transcript (kata 0vk2).
func TestHubModelReReadHoldsPostCutFrameUntilSnapshotApplied(t *testing.T) {
	var app *appserver.Server
	client, frames, cleanup := newTestHubClientWithFeed(t, func(server *appserver.Server) {
		app = server
		captureReadServer(server, nil)
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.frames = frames
	m.session.messages = preRelaunchMessages()

	cmd := m.applyHubNotification(*appwire.NotificationMessage(appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
		ThreadID: "01SEND",
		Ref:      "local:01SEND",
	}).Notification)
	if cmd == nil {
		t.Fatal("evener/thread/resync issued no command")
	}
	// The read has answered: the source-side snapshot is taken and the cut is
	// closed, but this model has not applied either yet.
	snapshot := cmd()

	app.Broadcast("01SEND", appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
		ThreadID: "01SEND",
		Ref:      "local:01SEND",
		TurnID:   "turn_4",
		Item: appwire.ThreadItem{
			Type:   "agentMessage",
			ID:     "item_agent_7",
			TurnID: "turn_4",
			Text:   "rollback finished",
			Status: "completed",
		},
	})
	awaitPostCutFrame(t, frames, "rollback finished")

	// Fold whatever the feed offers BEFORE the snapshot. Nothing forbids that
	// order: bubbletea takes the two goroutines' messages in whichever order
	// they land, so this is an interleaving the TUI must survive.
	for _, notification := range pendingHubNotifications(m) {
		m.applyHubNotification(notification)
	}

	updated, _ := m.Update(snapshot)
	m = updated.(hubModel)
	for _, notification := range pendingHubNotifications(m) {
		m.applyHubNotification(notification)
	}

	var texts []string
	for _, message := range m.session.messages {
		texts = append(texts, message.Text)
	}
	if !strings.Contains(strings.Join(texts, "\n"), "rollback finished") {
		t.Fatalf("transcript=%v, want the post-cut frame applied on top of the snapshot", texts)
	}
}

// The other side of the cut. Frames delivered ahead of the response are already
// in the snapshot's projection as transcript records — but that is all the
// snapshot represents, and a hub frame can do more than write a record. Holding
// them and dropping them at the cut, which is what the web store does with its
// own pre-cut buffer, would lose everything else they carry.
func TestHubModelReReadFoldsPreCutFrameUnderSnapshot(t *testing.T) {
	// armed makes exactly the resync read's capture broadcast from inside the
	// subscription gate: a frame sequenced below that read's cut, and enqueued
	// on the connection ahead of its response.
	armed := make(chan struct{}, 1)
	client, frames, cleanup := newTestHubClientWithFeed(t, func(server *appserver.Server) {
		captureReadServer(server, nil)
		server.SetBeforeSubscriptionGate(func() {
			select {
			case <-armed:
				server.Broadcast("01SEND", appwire.NotifyEvenerSandboxEscalationRequested, appwire.SandboxEscalationRequested{
					ThreadID:     "01SEND",
					Ref:          "local:01SEND",
					EscalationID: "esc_precut",
					Tool:         "read_file",
					DeniedPath:   "/etc/hosts",
				})
			default:
			}
		})
	})
	defer cleanup()

	// The broadcast above only routes to a connection that is already
	// subscribed, which the resync read's own capture has not done yet.
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:01SEND", Subscribe: true}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	m := newSessionHubModel(client)
	m.frames = frames
	m.session.messages = preRelaunchMessages()

	armed <- struct{}{}
	cmd := m.applyHubNotification(*appwire.NotificationMessage(appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
		ThreadID: "01SEND",
		Ref:      "local:01SEND",
	}).Notification)
	if cmd == nil {
		t.Fatal("evener/thread/resync issued no command")
	}
	updated, _ := m.Update(cmd())
	m = updated.(hubModel)
	for _, notification := range pendingHubNotifications(m) {
		m.applyHubNotification(notification)
	}

	escalations := m.escalationsByRef["local:01SEND"]
	if len(escalations) != 1 || escalations[0].id != "esc_precut" {
		t.Fatalf("escalations=%+v, want the pre-cut escalation folded; the snapshot carries no record of it", escalations)
	}
}

func feedDelta(t *testing.T, notification appwire.Notification) string {
	t.Helper()
	var params appwire.AgentMessageDeltaParams
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		t.Fatalf("decode %s: %v", notification.Method, err)
	}
	return params.Delta
}

func feedDeltas(t *testing.T, notifications []appwire.Notification) []string {
	t.Helper()
	out := make([]string, 0, len(notifications))
	for _, notification := range notifications {
		out = append(out, feedDelta(t, notification))
	}
	return out
}

func drainFeed(t *testing.T, feed *hubFrameFeed) []string {
	t.Helper()
	var out []appwire.Notification
	for {
		select {
		case notification := <-feed.Notifications():
			out = append(out, notification)
		default:
			return feedDeltas(t, out)
		}
	}
}

func deltaFrame(delta string) appwire.Message {
	return appwire.NotificationMessage(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{Delta: delta})
}

// The connection carries concurrent requests, so the feed sees response frames
// that are not its cut. Cutting on the first one closes the capture early and
// moves frames the snapshot already accounts for to the far side of it, where
// the model applies them a second time on top of the snapshot.
func TestHubFrameFeedCutsOnTheCallersOwnResponseFrame(t *testing.T) {
	feed := newHubFrameFeed()
	capture := feed.BeginCapture()
	capture.CutOn(appwire.NewIntID(7))

	feed.Observe(deltaFrame("under"), nil)
	// Another request, answered while this read is still in flight.
	feed.Observe(appwire.ResponseMessage(appwire.NewIntID(5), appwire.HarnessListResponse{}), nil)
	feed.Observe(deltaFrame("still under"), nil)
	feed.Observe(appwire.ResponseMessage(appwire.NewIntID(7), appwire.ThreadReadResponse{}), nil)
	feed.Observe(deltaFrame("over"), nil)

	if got := feedDeltas(t, capture.BeforeCut()); !slices.Equal(got, []string{"under", "still under"}) {
		t.Fatalf("pre-cut frames=%v, want both frames the source committed before this read's own response", got)
	}
	capture.Release()
	if got := drainFeed(t, feed); !slices.Equal(got, []string{"over"}) {
		t.Fatalf("post-cut frames=%v, want only the frame the source committed after this read's own response", got)
	}
}
