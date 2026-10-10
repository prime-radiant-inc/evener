package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/internal/appserver"
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

// A scheduled marketplace reconciliation read died with the dropped
// connection: its response will never arrive, so with the plugins panel open
// the reconnect itself must reissue the tagged read - the fence of an
// unconfirmed removal cannot wait for the user to happen to reopen the panel
// or for a notification to refresh.
func TestHubReconnectReissuesTaggedMarketplaceReconciliation(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, _, cleanupA := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
	})
	defer cleanupA()
	// The model sits on a connection that has dropped, feed included: a
	// scheduled reconciliation read on it died with the drop.
	oldClient, oldFeed, dropOldConnection := newTestHubClientWithFeed(t, nil)

	m := hubModel{
		client:                         oldClient,
		pluginsPanel:                   marketplacePanelWithEntries(t, appwire.MarketplaceEntry{Name: "removed"}),
		marketplaceRemovePending:       "removed",
		marketplaceReconcilePending:    true,
		marketplaceReconcileGeneration: 2,
		marketplaceListReadIssued:      2,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           1,
	}
	dropOldConnection()

	cmd := m.applyHubReconnect(hubReconnectMsg{client: client, frames: oldFeed})
	if cmd == nil {
		t.Fatal("reconnect should schedule its recovery reads")
	}
	var list launchconfig.MarketplaceListResultMsg
	seen := false
	for _, msg := range runBatchedCmds(t, cmd) {
		if result, ok := msg.(launchconfig.MarketplaceListResultMsg); ok {
			list, seen = result, true
		}
	}
	if !seen {
		t.Fatal("reconnect should reissue the tagged marketplace read for the open panel")
	}
	if list.Err != nil || list.ReconcileGeneration != m.marketplaceReconcileGeneration || list.ReconcileGeneration <= m.marketplaceListFloor {
		t.Fatalf("reissued read = %+v, want a fresh tagged generation (model now at %d)", list, m.marketplaceReconcileGeneration)
	}

	// The reissued read settles the fence the dead connection stranded.
	got, _ := m.handleMarketplaceListResult(list)
	after := got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("after the reissued read settled, pending = %q/%v, want cleared", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("settled read should leave the surviving marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("panel row after the reissued read = %q, want %q", remove.Name, kept.Name)
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

// Nothing in cmd/evener-tui reconnected, and the closed-feed branch returned
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
	replacementApp.BroadcastAll(appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		ThreadID: "01SEND",
		Ref:      "local:01SEND",
		Items: []appwire.ThreadItem{{
			Type:   "agentMessage",
			ID:     "item_agent_8",
			TurnID: "turn_4",
			Text:   "listening again",
			Status: "completed",
		}},
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
	replacementApp.Broadcast("01SEND", appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		ThreadID: "01SEND",
		Ref:      "local:01SEND",
		Items: []appwire.ThreadItem{{
			Type:   "agentMessage",
			ID:     "item_agent_9",
			TurnID: "turn_4",
			Text:   "back online",
			Status: "completed",
		}},
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

// A quit must end the reconnect work with the model, not after it: a retry
// parked in backoff would otherwise wake up, dial, and (with autostart on)
// launch a hub while no TUI exists to use it. Run owns the lifecycle context;
// canceling it has to abandon the backoff, cancel an in-flight dial, and stop
// the loop from re-arming.
func TestHubReconnectStopsOnLifecycleCancel(t *testing.T) {
	t.Run("a canceled backoff never dials", func(t *testing.T) {
		dialed := make(chan struct{}, 1)
		m := hubModel{dialHub: func(context.Context) (*appwire.Client, *hubFrameFeed, error) {
			dialed <- struct{}{}
			return nil, nil, errors.New("dialed after cancel")
		}}
		ctx, cancel := context.WithCancel(context.Background())
		m.lifecycleCtx = ctx

		// A long pending backoff stands in for the delay a real retry waits
		// through; canceling before it elapses must end the cmd promptly. The
		// retry runs under the model's own lifetime context, as Run wires it.
		cmd := reconnectHub(m.reconnectContext(), m.dialHub, 2, time.Hour)
		done := make(chan tea.Msg, 1)
		go func() { done <- cmd() }()
		cancel()

		select {
		case msg := <-done:
			result, ok := msg.(hubReconnectMsg)
			if !ok {
				t.Fatalf("cmd returned %T, want a hubReconnectMsg", msg)
			}
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("canceled backoff error = %v, want context.Canceled", result.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a canceled backoff kept sleeping; the retry would dial and autostart a hub after quit")
		}
		select {
		case <-dialed:
			t.Fatal("a canceled backoff still dialed; dialing with autostart launches a hub after the TUI is gone")
		default:
		}
	})

	t.Run("a blocked dial observes cancellation", func(t *testing.T) {
		dialEntered := make(chan error, 1)
		m := hubModel{dialHub: func(ctx context.Context) (*appwire.Client, *hubFrameFeed, error) {
			dialEntered <- ctx.Err()
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}}
		ctx, cancel := context.WithCancel(context.Background())
		m.lifecycleCtx = ctx

		// The first retry has no backoff, so the dial itself is the work in
		// flight when the TUI exits.
		cmd := reconnectHub(m.reconnectContext(), m.dialHub, 1, 0)
		done := make(chan tea.Msg, 1)
		go func() { done <- cmd() }()
		// Wait until the dial has actually started, then quit: canceling first
		// would (correctly) be caught before the dial, and the wait on ctx.Done
		// inside it is what this subtest exercises.
		select {
		case err := <-dialEntered:
			if err != nil {
				t.Fatalf("dial entered with ctx already canceled: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the dial never started")
		}
		cancel()

		select {
		case msg := <-done:
			result, ok := msg.(hubReconnectMsg)
			if !ok || !errors.Is(result.err, context.Canceled) {
				t.Fatalf("blocked dial result = %#v, want context.Canceled", msg)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the blocked dial did not observe cancellation; the dial goroutine outlives the model")
		}
	})

	t.Run("no retry is re-armed once the lifecycle ends", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		m := hubModel{
			dialHub:      staticHubDialer(nil, nil),
			lifecycleCtx: ctx,
		}
		if cmd := m.applyHubReconnect(hubReconnectMsg{attempt: 1, err: errors.New("dial failed")}); cmd != nil {
			t.Fatal("a canceled lifecycle still re-armed a retry; the loop outlives the TUI")
		}
	})

	t.Run("an already-canceled context never dials", func(t *testing.T) {
		dialed := make(chan struct{}, 1)
		m := hubModel{dialHub: func(context.Context) (*appwire.Client, *hubFrameFeed, error) {
			dialed <- struct{}{}
			return nil, nil, nil
		}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		m.lifecycleCtx = ctx

		// The delay-free first attempt skips the sleep's own ctx check, so an
		// already-canceled lifetime must be caught before the dial.
		msg := reconnectHub(m.reconnectContext(), m.dialHub, 1, 0)()
		result, ok := msg.(hubReconnectMsg)
		if !ok || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("reconnect result = %#v, want context.Canceled", msg)
		}
		select {
		case <-dialed:
			t.Fatal("an already-canceled lifetime still dialed; the delay-free attempt would be checked too late")
		default:
		}
	})

	t.Run("a connection that succeeds at exit is closed, not installed", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		transport := appwiretest.NewScriptedTransport()
		m := hubModel{lifecycleCtx: ctx}

		cmd := m.applyHubReconnect(hubReconnectMsg{client: appwire.NewClient(transport), attempt: 1})
		if cmd != nil {
			t.Fatal("a canceled lifecycle still produced a follow-up command")
		}
		if m.client != nil || m.frames != nil {
			t.Fatal("a canceled lifecycle still installed the connection that succeeded at exit")
		}
		assertTransportClosed(t, transport, "connection dialed at exit")
	})
}
