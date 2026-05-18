package main

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

// hubQueueMsg reports the result of a turn/queue call. On success the TUI's
// local queue preview is appended with the queued text; on failure the
// composer draft is restored so the user can retry without retyping.
type hubQueueMsg struct {
	text  string
	draft string
	err   error
}

// hubDrainAsSteerMsg reports the result of a turn/drainAsSteer call. On
// success the local queue preview is cleared and a "Steering sent." notice
// fires. On failure the composer draft is restored if the call was carrying
// composer text.
type hubDrainAsSteerMsg struct {
	draft string
	err   error
}

// sendHubQueue issues turn/queue (kata 111a) to enqueue text for processing
// after the active turn completes.
func sendHubQueue(client *appwire.Client, ref appwire.Ref, text, draft string) tea.Cmd {
	return func() tea.Msg {
		err := client.TurnQueue(context.Background(), appwire.TurnQueueParams{
			Ref:  ref.String(),
			Text: text,
		})
		return hubQueueMsg{text: text, draft: draft, err: err}
	}
}

// sendHubDrainAsSteer issues turn/drainAsSteer (kata 0bq1) to drain every
// queued message into a single STEERING message for the in-flight turn.
func sendHubDrainAsSteer(client *appwire.Client, ref appwire.Ref, draft string) tea.Cmd {
	return func() tea.Msg {
		err := client.TurnDrainAsSteer(context.Background(), appwire.TurnDrainAsSteerParams{
			Ref: ref.String(),
		})
		return hubDrainAsSteerMsg{draft: draft, err: err}
	}
}

// sendHubQueueThenDrain queues `pending` then immediately drains every queued
// message as a single STEERING entry (kata 0bq1 with composer text). Both
// appwire calls run inside the same Cmd so the daemon sees them strictly in
// order; the resulting message reports the drain outcome (which is what the
// user cares about). On queue failure, the drain is skipped.
func sendHubQueueThenDrain(client *appwire.Client, ref appwire.Ref, pending, draft string) tea.Cmd {
	return func() tea.Msg {
		if pending != "" {
			if err := client.TurnQueue(context.Background(), appwire.TurnQueueParams{
				Ref:  ref.String(),
				Text: pending,
			}); err != nil {
				return hubDrainAsSteerMsg{draft: draft, err: err}
			}
		}
		err := client.TurnDrainAsSteer(context.Background(), appwire.TurnDrainAsSteerParams{
			Ref: ref.String(),
		})
		return hubDrainAsSteerMsg{draft: draft, err: err}
	}
}
