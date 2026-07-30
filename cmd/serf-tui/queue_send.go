package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/clipboard"
)

// hubQueueMsg reports the result of a turn/queue call. On success the TUI's
// local queue preview is appended with the queued text; on failure the
// composer draft is restored so the user can retry without retyping.
type hubQueueMsg struct {
	ref                     string
	text                    string
	draft                   string
	trackedAttachmentSubmit bool
	submittedAttachments    []*clipboard.PastedImage
	err                     error
}

// hubDrainAsSteerMsg reports the result of a turn/drainAsSteer call.
type hubDrainAsSteerMsg struct {
	ref                     string
	text                    string
	draft                   string
	queued                  bool
	preQueueDepth           int
	hadAttachment           bool
	trackedAttachmentSubmit bool
	submittedAttachments    []*clipboard.PastedImage
	err                     error
}

// sendHubQueue issues turn/queue (kata 111a) to enqueue text for processing
// after the active turn completes. When attachments are supplied (kata re91)
// they are read from disk at submit time and shipped as image InputItems
// alongside the text.
// expectedTurnID is the turn the queue is being appended behind
// (appwire.ValidateMutationParams requires it): the daemon rejects the enqueue
// rather than attaching it to a turn that has since been replaced.
func sendHubQueue(client *appwire.Client, ref appwire.Ref, text, draft string, attachments []*clipboard.PastedImage, expectedTurnID string) tea.Cmd {
	trackedAttachmentSubmit := len(attachments) > 0
	mutationID, idErr := newClientMutationID()
	return func() tea.Msg {
		if idErr != nil {
			return hubQueueMsg{ref: ref.String(), text: text, draft: draft, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: idErr}
		}
		if strings.TrimSpace(expectedTurnID) == "" {
			return hubQueueMsg{ref: ref.String(), text: text, draft: draft, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: errNoActiveTurnToQueueBehind}
		}
		items, err := buildAttachmentItems(attachments)
		if err != nil {
			return hubQueueMsg{ref: ref.String(), text: text, draft: draft, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: err}
		}
		err = client.TurnQueue(context.Background(), appwire.TurnQueueParams{
			Ref:              ref.String(),
			ClientMutationID: mutationID,
			ExpectedTurnID:   expectedTurnID,
			Input:            appendTextInput(text, items),
		})
		return hubQueueMsg{ref: ref.String(), text: text, draft: draft, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: err}
	}
}

// sendHubDrainAsSteer issues turn/drainAsSteer (kata 0bq1) to drain every
// queued message into a single STEERING message for the in-flight turn.
// When the composer carries text or attachments (kata re91) they ride on
// the drain request so the daemon appends and drains atomically.
// expectedTurnID and expectedQueueRevision are the preconditions
// appwire.ValidateMutationParams requires. The revision is a CAS token: draining
// is destructive, so a queue that changed since the user saw it must be rejected
// rather than silently swallowed into a steer they did not intend.
func sendHubDrainAsSteer(client *appwire.Client, ref appwire.Ref, text, draft string, attachments []*clipboard.PastedImage, expectedTurnID string, expectedQueueRevision uint64, preQueueDepth ...int) tea.Cmd {
	trackedAttachmentSubmit := len(attachments) > 0
	mutationID, idErr := newClientMutationID()
	return func() tea.Msg {
		depth := 0
		if len(preQueueDepth) > 0 {
			depth = preQueueDepth[0]
		}
		if idErr != nil {
			return hubDrainAsSteerMsg{ref: ref.String(), text: text, draft: draft, preQueueDepth: depth, hadAttachment: len(attachments) > 0, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: idErr}
		}
		if strings.TrimSpace(expectedTurnID) == "" {
			return hubDrainAsSteerMsg{ref: ref.String(), text: text, draft: draft, preQueueDepth: depth, hadAttachment: len(attachments) > 0, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: errNoActiveTurnToSteer}
		}
		items, err := buildAttachmentItems(attachments)
		if err != nil {
			return hubDrainAsSteerMsg{ref: ref.String(), text: text, draft: draft, preQueueDepth: depth, hadAttachment: len(attachments) > 0, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: err}
		}
		err = client.TurnDrainAsSteer(context.Background(), appwire.TurnDrainAsSteerParams{
			Ref:                   ref.String(),
			ClientMutationID:      mutationID,
			ExpectedTurnID:        expectedTurnID,
			ExpectedQueueRevision: expectedQueueRevision,
			Input:                 appendTextInput(text, items),
		})
		return hubDrainAsSteerMsg{ref: ref.String(), text: text, draft: draft, preQueueDepth: depth, hadAttachment: len(items) > 0, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: err}
	}
}

// buildAttachmentItems reads each PastedImage's temp file at submit time
// and produces wire-ready []appwire.InputItem entries. Reading on submit
// (rather than caching bytes at paste time) keeps memory low while
// composer drafts live, and matches the codex reference impl. An error on
// any file aborts the build — the caller surfaces it through the usual
// hubSendMsg/hubQueueMsg error path so the user can retry.
func buildAttachmentItems(attachments []*clipboard.PastedImage) ([]appwire.InputItem, error) {
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

// A queue or drain-as-steer is scoped to a specific in-flight turn: the daemon
// requires expectedTurnId so the message cannot silently attach to a turn other
// than the one the user was looking at. When the composer has no active turn to
// name — the session went idle or its turn failed while queue mode was still on
// screen — the mutation cannot succeed, and sending it earns a raw
// "expectedTurnId is required" from the wire for something the client already
// knew. Refuse locally and say so in the composer's own terms instead.
var (
	errNoActiveTurnToQueueBehind = errors.New("no active turn to queue behind — the session is idle, so send instead")
	errNoActiveTurnToSteer       = errors.New("no active turn to steer — the session is idle, so send instead")
)
