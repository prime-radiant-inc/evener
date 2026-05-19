package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

// hubQueueMsg reports the result of a turn/queue call. On success the TUI's
// local queue preview is appended with the queued text; on failure the
// composer draft is restored so the user can retry without retyping.
type hubQueueMsg struct {
	text                    string
	draft                   string
	trackedAttachmentSubmit bool
	err                     error
}

// hubDrainAsSteerMsg reports the result of a turn/drainAsSteer call. If a
// composer payload was queued before drain, queued is true so the UI does not
// restore the already-accepted draft on a later drain failure.
type hubDrainAsSteerMsg struct {
	text                    string
	draft                   string
	queued                  bool
	preQueueDepth           int
	hadAttachment           bool
	trackedAttachmentSubmit bool
	err                     error
}

// sendHubQueue issues turn/queue (kata 111a) to enqueue text for processing
// after the active turn completes. When attachments are supplied (kata re91)
// they are read from disk at submit time and shipped as image InputItems
// alongside the text.
func sendHubQueue(client *appwire.Client, ref appwire.Ref, text, draft string, attachments []*PastedImage) tea.Cmd {
	trackedAttachmentSubmit := len(attachments) > 0
	return func() tea.Msg {
		items, err := buildAttachmentItems(attachments)
		if err != nil {
			return hubQueueMsg{text: text, draft: draft, trackedAttachmentSubmit: trackedAttachmentSubmit, err: err}
		}
		err = client.TurnQueue(context.Background(), appwire.TurnQueueParams{
			Ref:   ref.String(),
			Text:  text,
			Items: items,
		})
		return hubQueueMsg{text: text, draft: draft, trackedAttachmentSubmit: trackedAttachmentSubmit, err: err}
	}
}

// sendHubDrainAsSteer issues turn/drainAsSteer (kata 0bq1) to drain every
// queued message into a single STEERING message for the in-flight turn.
// When the composer carries text or attachments (kata re91) they are queued
// first so the daemon's atomic drain folds them into the same STEERING
// payload alongside everything already queued.
func sendHubDrainAsSteer(client *appwire.Client, ref appwire.Ref, text, draft string, attachments []*PastedImage, preQueueDepth ...int) tea.Cmd {
	trackedAttachmentSubmit := len(attachments) > 0
	return func() tea.Msg {
		depth := 0
		if len(preQueueDepth) > 0 {
			depth = preQueueDepth[0]
		}
		if text != "" || len(attachments) > 0 {
			items, err := buildAttachmentItems(attachments)
			if err != nil {
				return hubDrainAsSteerMsg{text: text, draft: draft, preQueueDepth: depth, hadAttachment: len(attachments) > 0, trackedAttachmentSubmit: trackedAttachmentSubmit, err: err}
			}
			if err := client.TurnQueue(context.Background(), appwire.TurnQueueParams{
				Ref:   ref.String(),
				Text:  text,
				Items: items,
			}); err != nil {
				return hubDrainAsSteerMsg{text: text, draft: draft, preQueueDepth: depth, hadAttachment: len(attachments) > 0, trackedAttachmentSubmit: trackedAttachmentSubmit, err: err}
			}
			if text != "" || len(items) > 0 {
				queued := hubDrainAsSteerMsg{text: text, draft: draft, queued: true, preQueueDepth: depth, hadAttachment: len(items) > 0, trackedAttachmentSubmit: trackedAttachmentSubmit}
				err := client.TurnDrainAsSteer(context.Background(), appwire.TurnDrainAsSteerParams{
					Ref: ref.String(),
				})
				queued.err = err
				return queued
			}
		}
		err := client.TurnDrainAsSteer(context.Background(), appwire.TurnDrainAsSteerParams{
			Ref: ref.String(),
		})
		return hubDrainAsSteerMsg{text: text, draft: draft, preQueueDepth: depth, trackedAttachmentSubmit: trackedAttachmentSubmit, err: err}
	}
}

// buildAttachmentItems reads each PastedImage's temp file at submit time
// and produces wire-ready []appwire.InputItem entries. Reading on submit
// (rather than caching bytes at paste time) keeps memory low while
// composer drafts live, and matches the codex reference impl. An error on
// any file aborts the build — the caller surfaces it through the usual
// hubSendMsg/hubQueueMsg error path so the user can retry.
func buildAttachmentItems(attachments []*PastedImage) ([]appwire.InputItem, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	items := make([]appwire.InputItem, 0, len(attachments))
	for _, att := range attachments {
		if att == nil {
			continue
		}
		data, err := os.ReadFile(att.Path)
		if err != nil {
			return nil, fmt.Errorf("read attachment %s: %w", att.Path, err)
		}
		media := att.MediaType
		if media == "" {
			media = "image/png"
		}
		items = append(items, appwire.InputItem{
			Type:      "image",
			MediaType: media,
			Data:      data,
			Name:      filepath.Base(att.Path),
		})
	}
	return items, nil
}
