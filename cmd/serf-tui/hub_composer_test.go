package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
)

func TestHubModelSessionComposerIsVisibleWhenEmpty(t *testing.T) {
	m := newSessionHubModel(nil)

	got := m.sessionView()
	for _, want := range []string{"message", "> "} {
		if !strings.Contains(got, want) {
			t.Fatalf("session composer missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelSessionComposerShowsReadOnlyReasonAndDraft(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Steer = false
	m.session.setInputValue("keep this draft")

	got := m.sessionView()
	for _, want := range []string{"read-only:", "source does not support send", "> keep this draft"} {
		if !strings.Contains(got, want) {
			t.Fatalf("read-only composer missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "enter: send") {
		t.Fatalf("read-only composer advertised send:\n%s", got)
	}
}

func TestHubModelBusyComposerShowsQueueOrReadOnlyMode(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = "active"
	m.detail.Capabilities.Send = true
	m.detail.Capabilities.Steer = true
	m.detail.Capabilities.Queue = true
	m.session.processing = true
	m.session.setInputValue("nudge the running turn")

	got := m.sessionView()
	for _, want := range []string{"queue", "enter: queue", "ctrl+s: send as steer", "> nudge the running turn"} {
		if !strings.Contains(got, want) {
			t.Fatalf("busy queue composer missing %q:\n%s", want, got)
		}
	}

	// Queue without steer: no force-steer hint.
	m.detail.Capabilities.Steer = false
	got = m.sessionView()
	for _, want := range []string{"queue", "enter: queue"} {
		if !strings.Contains(got, want) {
			t.Fatalf("queue-without-steer composer missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ctrl+s: send as steer") {
		t.Fatalf("queue-without-steer composer must not advertise force-steer:\n%s", got)
	}

	// Send-capable active sources stay in send mode even when they do not
	// advertise queue.
	m.detail.Capabilities.Queue = false
	got = m.sessionView()
	for _, want := range []string{"message", "enter: send", "> nudge the running turn"} {
		if !strings.Contains(got, want) {
			t.Fatalf("busy send composer missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "read-only:") {
		t.Fatalf("busy send-capable composer must not be read-only:\n%s", got)
	}

	// Neither queue nor send: read-only.
	m.detail.Capabilities.Send = false
	got = m.sessionView()
	for _, want := range []string{"read-only:", "source does not advertise queue", "> nudge the running turn"} {
		if !strings.Contains(got, want) {
			t.Fatalf("busy read-only composer missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelBusyEnterWithoutQueuePreservesDraftAndExplainsReason(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = "active"
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Queue = false
	m.session.processing = true
	m.session.setInputValue("do not drop this")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("busy read-only enter should not call the hub")
	}
	got := updated.(hubModel)
	if got.session.input.Value() != "do not drop this" {
		t.Fatalf("draft=%q, want preserved", got.session.input.Value())
	}
	view := got.sessionView()
	for _, want := range []string{"Send is not available for this session.", "source does not advertise queue", "> do not drop this"} {
		if !strings.Contains(view, want) {
			t.Fatalf("busy unsupported queue view missing %q:\n%s", want, view)
		}
	}
}

func TestHubModelBusyEnterRoutesToQueueAndClearsDraft(t *testing.T) {
	var got appwire.TurnQueueParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
			got = params
			return appwire.EmptyResponse{}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.State = "active"
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Steer = true
	m.detail.Capabilities.Queue = true
	m.detail.ActiveTurnID = "turn_busy"
	m.session.processing = true
	m.session.setInputValue("please keep going")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("busy enter should queue through hub")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	queued := updated.(hubModel)

	if got.Ref != "local:01SEND" || testInputText(got.Input) != "please keep going" {
		t.Fatalf("queue params=%+v", got)
	}
	if queued.session.input.Value() != "" {
		t.Fatalf("busy queue should clear draft on success, got %q", queued.session.input.Value())
	}
	// The hub no longer mirrors locally on success (kata r80p). Simulate
	// the daemon emitting thread/queueChanged so the wire-sourced preview
	// state advances.
	updated2, _ := queued.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyThreadQueueChanged, appwire.ThreadQueueChangedParams{
			ThreadID: queued.detail.SessionID,
			Ref:      queued.detail.Ref,
			Queue:    appwire.QueueState{Depth: 1, Preview: []string{"please keep going"}},
		}).Notification,
	})
	queued = updated2.(hubModel)
	if len(queued.sessionQueue) != 1 || queued.sessionQueue[0] != "please keep going" {
		t.Fatalf("sessionQueue=%v, want [%q]", queued.sessionQueue, "please keep going")
	}
	view := queued.View()
	for _, want := range []string{"queued (1)", "please keep going"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing queue preview %q:\n%s", want, view)
		}
	}
}

func TestHubModelEnterSendsImageOnlySubmission(t *testing.T) {
	var got appwire.TurnStartParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			got = params
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_image"}}, nil
		})
	})
	defer cleanup()

	path := writeAttachmentTempFile(t, []byte("image-only"))
	m := newSessionHubModel(client)
	m.pendingAttachments = []*PastedImage{{Path: path, MediaType: "image/png", MarkerN: 1}}
	m.nextAttachmentMarker = 1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("image-only enter should call the hub")
	}
	inFlight := updated.(hubModel)
	if len(inFlight.pendingAttachments) != 0 {
		t.Fatalf("pendingAttachments len=%d while send in flight, want 0", len(inFlight.pendingAttachments))
	}
	if inFlight.nextAttachmentMarker != 1 {
		t.Fatalf("nextAttachmentMarker=%d while send in flight, want preserved high-water 1", inFlight.nextAttachmentMarker)
	}
	updated, _ = inFlight.Update(cmd())
	sent := updated.(hubModel)

	if got.Ref != "local:01SEND" || testInputText(got.Input) != "" {
		t.Fatalf("params=%+v, want image-only turn/start", got)
	}
	if len(got.Input) != 1 || got.Input[0].Type != "image" || string(got.Input[0].Data) != "image-only" {
		t.Fatalf("input=%+v, want one image item", got.Input)
	}
	if len(sent.pendingAttachments) != 0 {
		t.Fatalf("pendingAttachments len=%d, want cleared after success", len(sent.pendingAttachments))
	}
}

func TestHubModelBusyEnterQueuesImageOnlySubmission(t *testing.T) {
	var got appwire.TurnQueueParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
			got = params
			return appwire.EmptyResponse{}, nil
		})
	})
	defer cleanup()

	path := writeAttachmentTempFile(t, []byte("queued-image-only"))
	m := newSessionHubModel(client)
	m.detail.State = "active"
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Steer = true
	m.detail.Capabilities.Queue = true
	m.session.processing = true
	m.pendingAttachments = []*PastedImage{{Path: path, MediaType: "image/png", MarkerN: 1}}
	m.nextAttachmentMarker = 1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("image-only busy enter should queue through hub")
	}
	inFlight := updated.(hubModel)
	if len(inFlight.pendingAttachments) != 0 {
		t.Fatalf("pendingAttachments len=%d while queue in flight, want 0", len(inFlight.pendingAttachments))
	}
	if inFlight.nextAttachmentMarker != 1 {
		t.Fatalf("nextAttachmentMarker=%d while queue in flight, want preserved high-water 1", inFlight.nextAttachmentMarker)
	}
	updated, _ = inFlight.Update(cmd())
	queued := updated.(hubModel)

	if got.Ref != "local:01SEND" || testInputText(got.Input) != "" {
		t.Fatalf("params=%+v, want image-only turn/queue", got)
	}
	if len(got.Input) != 1 || got.Input[0].Type != "image" || string(got.Input[0].Data) != "queued-image-only" {
		t.Fatalf("input=%+v, want one image item", got.Input)
	}
	if len(queued.pendingAttachments) != 0 {
		t.Fatalf("pendingAttachments len=%d, want cleared after success", len(queued.pendingAttachments))
	}
}

func TestHubModelBusyEnterRestoresAttachmentsOnQueueFailure(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
			return appwire.EmptyResponse{}, errors.New("queue rejected")
		})
	})
	defer cleanup()

	path := writeAttachmentTempFile(t, []byte("queued-image-fail"))
	img := &PastedImage{Path: path, MediaType: "image/png", MarkerN: 7}
	m := newSessionHubModel(client)
	m.detail.State = "active"
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Steer = true
	m.detail.Capabilities.Queue = true
	m.session.processing = true
	m.pendingAttachments = []*PastedImage{img}
	m.nextAttachmentMarker = 7

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("image-only busy enter should queue through hub")
	}
	inFlight := updated.(hubModel)
	if len(inFlight.pendingAttachments) != 0 {
		t.Fatalf("pendingAttachments len=%d while queue in flight, want 0", len(inFlight.pendingAttachments))
	}
	if inFlight.nextAttachmentMarker != 7 {
		t.Fatalf("nextAttachmentMarker=%d while queue in flight, want preserved high-water 7", inFlight.nextAttachmentMarker)
	}
	updated, _ = inFlight.Update(cmd())
	failed := updated.(hubModel)
	if len(failed.pendingAttachments) != 1 || failed.pendingAttachments[0] != img {
		t.Fatalf("pendingAttachments after queue failure=%+v, want restored image", failed.pendingAttachments)
	}
	if failed.nextAttachmentMarker != 7 {
		t.Fatalf("nextAttachmentMarker=%d, want 7", failed.nextAttachmentMarker)
	}
}

func TestHubModelBusyCtrlSDrainsQueueAsSteer(t *testing.T) {
	var drainParams appwire.TurnDrainAsSteerParams
	var drainCalled bool
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
			t.Fatalf("turn/queue should not be called for force-steer: %+v", params)
			return appwire.EmptyResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.EmptyResponse, error) {
			drainParams = params
			drainCalled = true
			return appwire.EmptyResponse{}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.State = "active"
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Steer = true
	m.detail.Capabilities.Queue = true
	m.detail.ActiveTurnID = "turn_busy"
	m.session.processing = true
	// Pre-populate the local queue (as if we'd already pressed Enter
	// once during processing) and have composer text in flight too.
	m.sessionQueue = []string{"earlier queued line"}
	m.sessionQueueRef = m.detail.Ref
	m.session.setInputValue("composer text in flight")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("ctrl+s should issue drain command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	drained := updated.(hubModel)

	if !drainCalled {
		t.Fatal("drainAsSteer never invoked")
	}
	if drainParams.Ref != "local:01SEND" {
		t.Fatalf("drain params=%+v", drainParams)
	}
	if testInputText(drainParams.Input) != "composer text in flight" {
		t.Fatalf("drain input=%+v, want composer text in flight", drainParams.Input)
	}
	if drained.session.input.Value() != "" {
		t.Fatalf("force-steer should clear composer, got %q", drained.session.input.Value())
	}
	// Wire state advances via thread/queueChanged after the daemon
	// collapses the queue (kata r80p). Drive that notification so the
	// preview reflects the post-drain truth (depth=0).
	updated2, _ := drained.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyThreadQueueChanged, appwire.ThreadQueueChangedParams{
			ThreadID: drained.detail.SessionID,
			Ref:      drained.detail.Ref,
			Queue:    appwire.QueueState{},
		}).Notification,
	})
	drained = updated2.(hubModel)
	if len(drained.sessionQueue) != 0 {
		t.Fatalf("force-steer should clear local queue after queueChanged, got %v", drained.sessionQueue)
	}
	view := drained.View()
	if !strings.Contains(view, "Force-steer sent.") {
		t.Fatalf("missing force-steer confirmation:\n%s", view)
	}
}

func TestHubModelBusyCtrlSDrainFailureRestoresDraft(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
			t.Fatalf("turn/queue should not be called for force-steer: %+v", params)
			return appwire.EmptyResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.EmptyResponse, error) {
			return appwire.EmptyResponse{}, errors.New("drain failed")
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.State = "active"
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Steer = true
	m.detail.Capabilities.Queue = true
	m.detail.ActiveTurnID = "turn_busy"
	m.session.processing = true
	m.session.setInputValue("queued before failed drain")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("ctrl+s should issue drain command")
	}
	msg := cmd()
	updated, _ = updated.(hubModel).Update(msg)
	got := updated.(hubModel)

	if got.session.input.Value() != "queued before failed drain" {
		t.Fatalf("draft was not restored after failed atomic drain: %q", got.session.input.Value())
	}
	if len(got.sessionQueue) != 0 {
		t.Fatalf("sessionQueue=%v, want unchanged empty queue", got.sessionQueue)
	}
	view := got.View()
	if !strings.Contains(view, "Force-steer failed") {
		t.Fatalf("missing failure notice:\n%s", view)
	}
}

func TestHubModelBusyCtrlSQueuedDrainPartialDoesNotRestoreDraft(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, _ appwire.TurnDrainAsSteerParams) (appwire.EmptyResponse, error) {
			return appwire.EmptyResponse{}, appwire.QueuedDrainPartial("composer payload queued but drain failed")
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.State = "active"
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Steer = true
	m.detail.Capabilities.Queue = true
	m.detail.ActiveTurnID = "turn_busy"
	m.session.processing = true
	m.session.setInputValue("queued before failed drain")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("ctrl+s should issue drain command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel)

	if got.session.input.Value() != "" {
		t.Fatalf("draft was restored after queued partial: %q", got.session.input.Value())
	}
	if len(got.sessionQueue) != 1 || got.sessionQueue[0] != "queued before failed drain" {
		t.Fatalf("sessionQueue=%v, want queued payload preview", got.sessionQueue)
	}
	if !strings.Contains(got.View(), "Force-steer failed after queueing") {
		t.Fatalf("missing queued partial notice:\n%s", got.View())
	}
}

func TestHubModelBusyCtrlSWithEmptyQueueAndEmptyComposerBanner(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = "active"
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Steer = true
	m.detail.Capabilities.Queue = true
	m.detail.ActiveTurnID = "turn_busy"
	m.session.processing = true

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd != nil {
		t.Fatal("ctrl+s with empty queue+composer should not call the hub")
	}
	got := updated.(hubModel)
	if view := got.View(); !strings.Contains(view, "Nothing to steer") {
		t.Fatalf("missing empty-steer banner:\n%s", view)
	}
}

func TestHubModelSessionComposerAltEnterInsertsNewline(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.setInputValue("alpha")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if cmd != nil {
		t.Fatal("alt+enter newline should be synchronous")
	}
	m = updated.(hubModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("beta")})
	m = updated.(hubModel)

	if got := m.session.input.Value(); got != "alpha\nbeta" {
		t.Fatalf("composer draft=%q, want alt-enter multiline draft", got)
	}
}

func TestHubModelSessionComposerCtrlJInsertsNewline(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.setInputValue("line one")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if cmd != nil {
		t.Fatal("ctrl+j newline should be synchronous")
	}
	m = updated.(hubModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	m = updated.(hubModel)

	if got := m.session.input.Value(); got != "line one\nline two" {
		t.Fatalf("composer draft=%q, want multiline draft", got)
	}
	if got := m.sessionView(); !strings.Contains(got, "> line one\n  line two") {
		t.Fatalf("multiline composer did not render continuation line:\n%s", got)
	}
}

func TestHubModelSessionComposerUsesHistoryNavigationOnlyWhenEmpty(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.history = []string{"first request", "second request"}
	m.session.setInputValue("current draft")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if cmd != nil {
		t.Fatal("history up should be synchronous")
	}
	m = updated.(hubModel)
	if got := m.session.input.Value(); got != "current draft" {
		t.Fatalf("history up with non-empty draft input=%q, want current draft", got)
	}

	m.session.setInputValue("")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(hubModel)
	if got := m.session.input.Value(); got != "second request" {
		t.Fatalf("empty history up input=%q, want second request", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(hubModel)
	if got := m.session.input.Value(); got != "first request" {
		t.Fatalf("second empty history up input=%q, want first request", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(hubModel)
	if got := m.session.input.Value(); got != "second request" {
		t.Fatalf("history down input=%q, want second request", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(hubModel)
	if got := m.session.input.Value(); got != "" {
		t.Fatalf("history restored empty draft=%q, want empty", got)
	}
}

func TestHubModelSessionComposerAddsSentPromptsToHistory(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_sent", Status: appwire.TurnStatusInProgress}}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.setInputValue("remember this")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("send returned nil command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)

	if len(m.session.history) == 0 || unescapeHistory(m.session.history[len(m.session.history)-1]) != "remember this" {
		t.Fatalf("history=%v, want sent prompt appended", m.session.history)
	}
}

func TestHubModelFailedSendPreservesDraftExactly(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return appwire.TurnStartResponse{}, appwire.Conflict("session is busy")
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	draft := "  exact draft\nwith trailing spaces  "
	m.session.setInputValue(draft)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("send returned nil command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel)

	if got.session.input.Value() != draft {
		t.Fatalf("failed send draft=%q, want exact %q", got.session.input.Value(), draft)
	}
	view := got.sessionView()
	for _, want := range []string{"Send failed: appwire turn/start: session is busy", ">   exact draft\n  with trailing spaces  "} {
		if !strings.Contains(view, want) {
			t.Fatalf("failed send view missing %q:\n%s", want, view)
		}
	}
}

func TestHubModelFailureRestorePreservesNewerComposerDraft(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  tea.Msg
	}{
		{
			name: "send",
			msg: hubSendMsg{
				ref:   "local:01SEND",
				text:  "old send",
				draft: "old send",
				err:   errors.New("send failed"),
			},
		},
		{
			name: "queue",
			msg: hubQueueMsg{
				ref:   "local:01SEND",
				text:  "old queue",
				draft: "old queue",
				err:   errors.New("queue failed"),
			},
		},
		{
			name: "force steer",
			msg: hubDrainAsSteerMsg{
				ref:   "local:01SEND",
				text:  "old steer",
				draft: "old steer",
				err:   errors.New("steer failed"),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newSessionHubModel(nil)
			m.session.setInputValue("newer draft")

			updated, _ := m.Update(tt.msg)
			got := updated.(hubModel)
			if got.session.input.Value() != "newer draft" {
				t.Fatalf("input=%q, want newer draft", got.session.input.Value())
			}
			if !strings.Contains(got.sessionView(), "preserved current draft") {
				t.Fatalf("missing failed payload notice:\n%s", got.sessionView())
			}
		})
	}
}

func TestHubModelFailureRestorePreservesNewerAttachments(t *testing.T) {
	oldPath := writeAttachmentTempFile(t, []byte("old-image"))
	newPath := writeAttachmentTempFile(t, []byte("new-image"))
	oldImage := &PastedImage{Path: oldPath, MediaType: "image/png", MarkerN: 1}
	newImage := &PastedImage{Path: newPath, MediaType: "image/png", MarkerN: 2}

	m := newSessionHubModel(nil)
	m.pendingAttachments = []*PastedImage{newImage}

	updated, _ := m.Update(hubQueueMsg{
		ref:                  "local:01SEND",
		text:                 "old queue",
		draft:                "old queue",
		submittedAttachments: []*PastedImage{oldImage},
		err:                  errors.New("queue failed"),
	})
	got := updated.(hubModel)
	if len(got.pendingAttachments) != 1 || got.pendingAttachments[0] != newImage {
		t.Fatalf("pendingAttachments=%+v, want only newer attachment", got.pendingAttachments)
	}
	if got.session.input.Value() != "" {
		t.Fatalf("input=%q, want unchanged empty draft", got.session.input.Value())
	}
}

func TestHubModelSessionComposerBoundsRenderedDraftHeight(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.setInputValue("one\ntwo\nthree\nfour\nfive\nsix\nseven")

	got := m.sessionComposerPanel().View()
	renderedDraft := renderComposerDraft(m.session.input.Value(), m.session.input.MaxHeight)
	if gotLines := strings.Count(renderedDraft, "\n"); gotLines > m.session.input.MaxHeight {
		t.Fatalf("rendered draft lines=%d, want at most %d:\n%s", gotLines, m.session.input.MaxHeight, renderedDraft)
	}
	if strings.Contains(got, "one") || strings.Contains(got, "two") || strings.Contains(got, "three") {
		t.Fatalf("composer should scroll internally instead of rendering every old line:\n%s", got)
	}
	for _, want := range []string{"...", "four", "five", "six", "seven"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bounded composer missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelSessionComposerShowsStaticCursorWhenEmpty(t *testing.T) {
	got := renderComposerDraft("")
	if !strings.Contains(got, "> █") {
		t.Fatalf("empty composer should show a visible cursor:\n%q", got)
	}
}

func TestHubModelSessionPickerOverlayKeepsComposerDraftVisible(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.setInputValue("draft survives overlay")
	picker := newModelPicker([]modelPickerItem{{id: "openai/gpt-5", display: "openai/gpt-5"}}, "", 80)
	m.sessionModelPicker = &picker

	got := m.sessionView()
	for _, want := range []string{"Select model", "openai/gpt-5", "> draft survives overlay"} {
		if !strings.Contains(got, want) {
			t.Fatalf("session picker overlay missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelSpawnModelPickerKeepsFormDraftVisible(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.openSpawnForm()
	m.spawnModels = []modelPickerItem{{id: "openai/gpt-5", display: "openai/gpt-5"}}
	m.spawnModel = "openai/gpt-5"
	m.session.setInputValue("spawn draft survives overlay")
	m.openSpawnModelPicker(m.spawnModels)

	got := m.spawnView()
	for _, want := range []string{"Select spawn model", "Prompt", "> spawn draft survives overlay"} {
		if !strings.Contains(got, want) {
			t.Fatalf("spawn model picker overlay missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelSpawnPromptIsGroupedWithLaunchFields(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.openSpawnForm()
	m.height = 20
	m.spawnDir = "/tmp/serf"
	m.spawnDirInput.SetValue(m.spawnDir)
	m.session.setInputValue("launch task")

	got := m.spawnView()
	requireOrderedText(t, got, "Harness:", "Model:", "Dir:", "Prompt", "> launch task", "tab: next field")
	dirLine := renderedLineContaining(got, "Dir:")
	promptLine := renderedLineContaining(got, "Prompt")
	if dirLine < 0 || promptLine < 0 {
		t.Fatalf("spawn view missing dir or prompt:\n%s", got)
	}
	if promptLine-dirLine > 3 {
		t.Fatalf("prompt should be grouped with launch fields, dir line=%d prompt line=%d:\n%s", dirLine, promptLine, got)
	}
}

func renderedLineContaining(view, needle string) int {
	for i, line := range strings.Split(view, "\n") {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}
