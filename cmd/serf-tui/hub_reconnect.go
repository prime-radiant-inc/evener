package main

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

// hubDialer opens a fresh hub connection together with the ordered frame feed
// installed on it. main builds one from the same startup options the first
// connection used, so a reconnect gets the same address, the same auth, and
// the same autostart behaviour — which is what recovers a hub that exited
// rather than one that merely blipped.
type hubDialer func(context.Context) (*appwire.Client, *hubFrameFeed, error)

// hubReconnectMsg reports the outcome of one reconnect attempt.
type hubReconnectMsg struct {
	client  *appwire.Client
	frames  *hubFrameFeed
	attempt int
	err     error
}

const noticeCategoryConnection = "connection"

const (
	hubReconnectBaseDelay = 250 * time.Millisecond
	// hubReconnectMaxDelay caps the backoff. A hub that exited stays gone until
	// somebody starts it, and the retry that finds it started should be soon
	// enough that the user does not go looking for a restart command.
	hubReconnectMaxDelay = 15 * time.Second
)

// hubConnected reports whether hub frames can still reach this model. The
// client pointer alone cannot answer it: it is assigned once and never
// cleared, so it stays non-nil across a connection that died underneath it.
func (m hubModel) hubConnected() bool {
	return m.client != nil && !m.connectionLost
}

// hubConnectionLost answers the end of the notification feed. appwire ends it
// on any transport error, on a failed keepalive, and on notification-buffer
// overflow — every way a hub connection dies — and the model that owns it
// receives nothing ever again: the transcript, the composer's turn state, and
// the queue preview freeze at whatever they last held while every key still
// works. Say so, and dial again.
func (m *hubModel) hubConnectionLost() tea.Cmd {
	m.connectionLost = true
	m.reconnectAttempt = 1
	if m.dialHub == nil {
		m.addNotice(m.connectionNotice("nothing on this model can dial the hub again", "Restart serf-tui to reconnect."))
		return nil
	}
	m.addNotice(m.connectionNotice("the hub's notification stream ended", "Reconnecting…"))
	return reconnectHub(m.dialHub, 1, 0)
}

// applyHubReconnect folds one attempt's outcome. Retries do not give up: a hub
// that is down is a hub somebody is about to restart, and a TUI that stopped
// trying looks exactly like the silent failure this whole path exists to end.
func (m *hubModel) applyHubReconnect(msg hubReconnectMsg) tea.Cmd {
	if msg.err != nil {
		attempt := msg.attempt + 1
		m.reconnectAttempt = attempt
		delay := hubReconnectDelay(attempt)
		m.addNotice(m.connectionNotice(msg.err.Error(), fmt.Sprintf("Retrying in %s (attempt %d).", delay, attempt)))
		return reconnectHub(m.dialHub, attempt, delay)
	}
	if m.client != nil {
		_ = m.client.Close()
	}
	m.client = msg.client
	m.frames = msg.frames
	m.connectionLost = false
	m.reconnectAttempt = 0
	if m.pending != nil {
		m.client.SetPendingCoordinator(m.pending)
	}
	m.clearNoticesByCategory(noticeCategoryConnection)

	cmds := []tea.Cmd{waitHubNotification(m.frames), fetchHubTree(m.client)}
	// A replacement connection carries no subscriptions: every one the dead
	// connection held died with it. Re-read the viewed session to re-subscribe
	// it, and re-issue the subagent rail's child subscriptions — the same-ref
	// read does not re-issue those, and it is additive precisely so it cannot
	// drop them if it lands second.
	if m.mode == hubModeSession {
		if ref, ok := m.currentRef(); ok {
			m.watchedChildRefs = nil
			cmds = append(cmds, resyncHubSession(m.frames, m.client, ref), m.subscribeNewChildren())
		}
	}
	return tea.Batch(cmds...)
}

func (m hubModel) connectionNotice(reason, nextAction string) noticePanel {
	return noticePanel{
		Title:      "Hub connection lost",
		Category:   noticeCategoryConnection,
		Summary:    "The hub connection dropped, so this session stopped receiving updates.",
		Source:     m.hubURL,
		Reason:     reason,
		NextAction: nextAction,
		State:      "awaiting",
	}
}

func reconnectHub(dial hubDialer, attempt int, delay time.Duration) tea.Cmd {
	return func() tea.Msg {
		if delay > 0 {
			time.Sleep(delay)
		}
		client, frames, err := dial(context.Background())
		return hubReconnectMsg{client: client, frames: frames, attempt: attempt, err: err}
	}
}

// hubReconnectDelay is the web client's backoff shape — 250ms doubling with no
// jitter (protocol/client.ts RECONNECT_BASE_MS) — with a higher ceiling than
// its 5s. A TUI reconnect re-runs the whole startup path, which can LAUNCH a
// hub, so what the floor bounds here is process spawns rather than sockets.
// The first attempt is immediate: a dropped connection is a blip until proven
// otherwise.
func hubReconnectDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 0
	}
	delay := hubReconnectBaseDelay << min(attempt-2, 16)
	return min(delay, hubReconnectMaxDelay)
}
