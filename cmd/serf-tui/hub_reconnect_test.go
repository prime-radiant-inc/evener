package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

// dropHubConnection closes the hub connection and waits for the model's feed to
// end, which is what the TUI sees of a hub that went away: appwire's receive
// loop errors out and takes the notification feed with it. Waiting on the feed
// is an await, not a timeout — the closed channel is the event.
func dropHubConnection(t *testing.T, m hubModel, cleanup func()) (hubModel, tea.Cmd) {
	t.Helper()
	cleanup()
	msg := waitHubNotification(m.frames)()
	notification, ok := msg.(hubNotificationMsg)
	if !ok || notification.ok {
		t.Fatalf("feed produced %T %+v, want the end of the notification stream", msg, msg)
	}
	updated, cmd := m.Update(msg)
	return updated.(hubModel), cmd
}

// The status chip is the one surface a user checks to explain a session that
// stopped moving. Computed from a client pointer that is assigned once and
// never cleared, it said "connected" for the rest of the process — actively
// denying the problem (kata zkrr).
func TestHubModelStopsReportingConnectedWhenHubConnectionDrops(t *testing.T) {
	client, frames, cleanup := newTestHubClientWithFeed(t, nil)
	m := newSessionHubModel(client)
	m.frames = frames

	if !m.sessionComposerPanel().ChipContext.Connected {
		t.Fatal("chip reported disconnected while the hub connection was live")
	}

	m, _ = dropHubConnection(t, m, cleanup)

	if m.sessionComposerPanel().ChipContext.Connected {
		t.Fatal("chip still reports connected after the hub connection dropped; the one surface that explains a frozen session denies the problem")
	}
}

// staticHubDialer stands in for main's dialer: it hands back a connection that
// was opened the same way, against a second real hub.
func staticHubDialer(client *appwire.Client, frames *hubFrameFeed) hubDialer {
	return func(context.Context) (*appwire.Client, *hubFrameFeed, error) {
		return client, frames, nil
	}
}

// runBatchedCmds runs cmd and flattens one level of tea.Batch, which is how
// bubbletea would deliver the follow-ups a reconnect returns.
func runBatchedCmds(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, batched := range msg {
			if batched == nil {
				continue
			}
			out = append(out, batched())
		}
		return out
	case nil:
		return nil
	default:
		return []tea.Msg{msg}
	}
}

// Nothing in cmd/serf-tui reconnected, and the closed-feed branch returned
// without re-arming, so a single dropped connection made the TUI deaf for the
// rest of the process while every key still worked.
func TestHubModelReconnectsAfterHubConnectionDrops(t *testing.T) {
	var replacementApp *appserver.Server
	var reads []appwire.ThreadReadParams
	replacementClient, replacementFrames, replacementCleanup := newTestHubClientWithFeed(t, func(server *appserver.Server) {
		replacementApp = server
		captureReadServer(server, &reads)
	})
	defer replacementCleanup()

	client, frames, cleanup := newTestHubClientWithFeed(t, nil)
	m := newSessionHubModel(client)
	m.frames = frames
	m.session.messages = preRelaunchMessages()
	m.dialHub = staticHubDialer(replacementClient, replacementFrames)

	cleanup()
	msg := waitHubNotification(m.frames)()
	updated, cmd := m.Update(msg)
	m = updated.(hubModel)
	if cmd == nil {
		t.Fatal("the ended feed produced no command; nothing reconnects and the TUI stays deaf for the rest of the process")
	}
	if m.sessionComposerPanel().ChipContext.Connected {
		t.Fatal("chip reports connected while the reconnect is still in flight")
	}

	updated, cmd = m.Update(cmd())
	m = updated.(hubModel)
	if !m.sessionComposerPanel().ChipContext.Connected {
		t.Fatal("chip still reports disconnected after the reconnect succeeded")
	}
	if m.client != replacementClient || m.frames != replacementFrames {
		t.Fatal("model kept the dead connection after reconnecting")
	}
	if cmd == nil {
		t.Fatal("reconnecting issued no follow-up; the new connection carries no subscriptions until the session is read again")
	}

	// One frame waiting in the replacement feed, sent to every connection
	// rather than routed, because the read that re-subscribes has not run yet.
	// Its only job is to show that the reconnect listens to the new feed at
	// all: without a re-armed wait, nothing consumes it.
	replacementApp.BroadcastAll(appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
		ThreadID: "01SEND",
		Ref:      "local:01SEND",
		TurnID:   "turn_4",
		Item: appwire.ThreadItem{
			Type:   "agentMessage",
			ID:     "item_agent_8",
			TurnID: "turn_4",
			Text:   "listening again",
			Status: "completed",
		},
	})
	listening := false
	for _, followUp := range runBatchedCmds(t, cmd) {
		if _, ok := followUp.(hubNotificationMsg); ok {
			listening = true
		}
		updated, _ = m.Update(followUp)
		m = updated.(hubModel)
	}
	if !listening {
		t.Fatal("reconnecting re-armed no notification wait; the replacement connection's frames reach nobody")
	}
	if len(reads) == 0 {
		t.Fatalf("reads=%+v, want the viewed session re-read on the replacement connection", reads)
	}
	if !reads[0].Subscribe || reads[0].ReplaceSubscription {
		t.Fatalf("re-read params=%+v, want an additive subscribing read: the replacement connection carries no subscriptions, and a replacing one would race the rail's child subscribes", reads[0])
	}

	// Now that the re-read has re-subscribed, a frame ROUTED by subscription
	// has to land in the transcript. That is the recovery the kata is about:
	// live updates resume without restarting the TUI.
	replacementApp.Broadcast("01SEND", appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
		ThreadID: "01SEND",
		Ref:      "local:01SEND",
		TurnID:   "turn_4",
		Item: appwire.ThreadItem{
			Type:   "agentMessage",
			ID:     "item_agent_9",
			TurnID: "turn_4",
			Text:   "back online",
			Status: "completed",
		},
	})
	updated, _ = m.Update(waitHubNotification(m.frames)())
	m = updated.(hubModel)

	var texts []string
	for _, message := range m.session.messages {
		texts = append(texts, message.Text)
	}
	if !strings.Contains(strings.Join(texts, "\n"), "back online") {
		t.Fatalf("transcript=%v, want a frame from the replacement connection's re-established subscription folded in", texts)
	}
}

// A dial that fails must not end the loop. A hub that is down is one somebody
// is about to restart, and a TUI that stopped trying is the same silence this
// whole path exists to end — the web client never gives up either
// (protocol/client.ts scheduleReconnect has no attempt ceiling).
func TestHubModelKeepsRetryingWhileTheHubStaysDown(t *testing.T) {
	client, frames, cleanup := newTestHubClientWithFeed(t, nil)
	m := newSessionHubModel(client)
	m.frames = frames
	m.dialHub = func(context.Context) (*appwire.Client, *hubFrameFeed, error) {
		return nil, nil, errors.New("dial tcp 127.0.0.1:7777: connect: connection refused")
	}

	m, cmd := dropHubConnection(t, m, cleanup)
	if cmd == nil {
		t.Fatal("the ended feed produced no command")
	}

	updated, retry := m.Update(cmd())
	m = updated.(hubModel)
	if retry == nil {
		t.Fatal("a failed dial ended the reconnect loop; the TUI stays deaf until it is restarted")
	}
	if m.hubConnected() {
		t.Fatal("chip reports connected while every dial is failing")
	}
	if m.reconnectAttempt != 2 {
		t.Fatalf("reconnect attempt=%d, want the second attempt pending", m.reconnectAttempt)
	}
	notice, ok := noticeByCategory(m, noticeCategoryConnection)
	if !ok {
		t.Fatalf("notices=%+v, want one explaining the lost connection", m.notices)
	}
	if !strings.Contains(notice.Reason, "connection refused") || !strings.Contains(notice.NextAction, "attempt 2") {
		t.Fatalf("notice=%+v, want the dial failure as the cause and the next attempt as the next action", notice)
	}
}

func noticeByCategory(m hubModel, category string) (noticePanel, bool) {
	for _, notice := range m.notices {
		if notice.Category == category {
			return notice, true
		}
	}
	return noticePanel{}, false
}

// The backoff shape: immediate first retry, then the web client's 250ms
// doubling, capped so a hub that comes back is found promptly and a hub that
// cannot start is not relaunched in a tight loop.
func TestHubReconnectDelayBacksOffAndCaps(t *testing.T) {
	var got []time.Duration
	for attempt := 1; attempt <= 9; attempt++ {
		got = append(got, hubReconnectDelay(attempt))
	}
	want := []time.Duration{
		0,
		250 * time.Millisecond,
		500 * time.Millisecond,
		time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		hubReconnectMaxDelay,
		hubReconnectMaxDelay,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("delays=%v, want %v", got, want)
	}
}
