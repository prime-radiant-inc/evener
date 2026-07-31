package main

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

// Kata nxq6: serf-tui under tmux repeatedly rendered a pane blank except the
// bottom three composer lines (label / prompt / key hints), with the
// transcript and chip strip missing above them; any keypress restored the
// full frame including unchanged content. Two candidates: (a) a real
// partial-repaint bug in the TUI's own render path, or (b) a tmux
// capture-pane artifact (reading mid-render, between an erase and its
// redraw).
//
// This file drives hubModel.Update directly — no terminal, no tmux — through
// realistic turn-started/streaming-notification bursts at the reported pane
// geometry (200x50) plus resizes, and checks View()'s output after every
// single step. If the composer's footer ever renders while the session
// breadcrumb (topBar — always present whenever ANY body height is available,
// per sessionChromeText/AppShell) does not, that is candidate (a),
// deterministically, with no tmux involved. See hub_notifications_fuzz_test.go
// for the sister approach this borrows (client=nil, mode=hubModeSession,
// apply notifications, no panic floor); this file's floor is "never a
// partially rendered frame" instead of "never panics".

// nxq6SessionRef is the fixed session ref used by every notification below so
// notificationMatchesCurrentSession never filters a step out.
const nxq6SessionRef = "local:01SESSION"

// nxq6NewSessionModel builds a hubModel already parked in session mode with a
// populated header (so topBar/the rule are never trivially empty) and queue+
// steer capabilities (so the composer can reach the exact "queue" mode shown
// in the kata's captured pane: label "queue", empty draft, and the
// "enter queue  ctrl+s steer  esc browse  ⌘P palette  ⌘O dashboard" hint
// line).
func nxq6NewSessionModel(t *testing.T) hubModel {
	t.Helper()
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{
		Ref:        nxq6SessionRef,
		SessionID:  "01SESSION",
		Title:      "nxq6 investigation session",
		State:      appwire.ThreadStatusActive,
		Model:      "openai/gpt-5",
		Profile:    "openai",
		Branch:     "main",
		WorkingDir: "/tmp/nxq6-project",
		Capabilities: hubSessionCapabilities{
			Send:      true,
			Steer:     true,
			Queue:     true,
			Interrupt: true,
			Compact:   true,
			Clear:     true,
		},
	}
	m.session.sessionID = "01SESSION"
	m.session.processing = true
	// Seed a little prior transcript, matching the bug report's "content
	// that had been on screen before and had not changed": the invariant
	// below must hold even when there is real content behind the composer.
	seed := []appwire.Notification{
		nxq6Notify(t, appwire.NotifyItemStarted, appwire.ItemLifecycleParams{
			Item: appwire.ThreadItem{Type: "userMessage", ID: "seed-user-1", TurnID: "turn_1", Text: "seed question before the burst"},
		}),
		nxq6Notify(t, appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
			Item: appwire.ThreadItem{Type: "agentMessage", ID: "seed-agent-1", TurnID: "turn_1", Text: "seed answer before the burst", Status: "completed"},
		}),
	}
	for _, n := range seed {
		next, _ := m.Update(hubNotificationMsg{ok: true, notification: n})
		m = next.(hubModel)
	}
	return m
}

func nxq6Notify(t *testing.T, method string, params any) appwire.Notification {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s params: %v", method, err)
	}
	return appwire.Notification{Method: method, Params: raw}
}

// nxq6AssertViewComplete fails the test if m.View() shows the composer's
// footer hint line without also showing the session breadcrumb (topBar).
// That combination — composer chrome rendered, everything above it missing —
// is exactly the kata's observed symptom. It also asserts the cruder,
// implementation-independent shape from the bug report directly: the
// rendered pane must not be blank across (height-3) leading lines while a
// trailing line carries real composer text.
func nxq6AssertViewComplete(t *testing.T, m hubModel, step string) {
	t.Helper()
	topBar, _, footer := m.sessionChromeText()
	view := m.View()

	if topBar == "" {
		t.Fatalf("step %s: topBar is empty (title fallback should prevent this)", step)
	}
	chromeVisible := strings.Contains(view, topBar)

	footerLines := strings.Split(strings.TrimRight(footer, "\n"), "\n")
	lastFooterLine := ""
	if n := len(footerLines); n > 0 {
		lastFooterLine = strings.TrimSpace(footerLines[n-1])
	}
	composerVisible := lastFooterLine != "" && strings.Contains(view, lastFooterLine)

	if composerVisible && !chromeVisible {
		t.Fatalf("step %s: composer footer rendered without the session breadcrumb — partial-repaint shape\ntopBar=%q\nfooter=%q\nview:\n%s", step, topBar, footer, view)
	}

	// Cruder, implementation-independent backstop: count leading blank
	// (post-ANSI-strip, whitespace-only) lines. The kata's captured pane was
	// height 50 with all but the bottom 3 lines blank. Flag anything that
	// blank-heavy while the composer text is present, independent of the
	// topBar/footer bookkeeping above.
	if composerVisible {
		lines := strings.Split(view, "\n")
		blankPrefix := 0
		for _, l := range lines {
			if strings.TrimSpace(ansiPattern.ReplaceAllString(l, "")) != "" {
				break
			}
			blankPrefix++
		}
		if len(lines) >= 6 && blankPrefix >= len(lines)-3 {
			t.Fatalf("step %s: %d of %d rendered lines are a blank prefix, leaving only the last few — matches the kata's captured pane shape\nview:\n%s", step, blankPrefix, len(lines), view)
		}
	}
}

// TestSessionViewNeverBlankAboveComposer_RealisticBurst replays a
// turn-started + streaming-notification burst at the kata's reported
// geometry (tmux 200x50), including a mid-burst resize (tmux clients resize
// panes live), and checks the invariant after every step.
func TestSessionViewNeverBlankAboveComposer_RealisticBurst(t *testing.T) {
	m := nxq6NewSessionModel(t)

	apply := func(step string, msg tea.Msg) {
		t.Helper()
		next, _ := m.Update(msg)
		m = next.(hubModel)
		nxq6AssertViewComplete(t, m, step)
	}

	apply("resize-200x50", tea.WindowSizeMsg{Width: 200, Height: 50})
	apply("turn-started", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyTurnStarted, appwire.TurnStartedParams{
		Ref: nxq6SessionRef, Turn: appwire.Turn{ID: "turn_2", Status: appwire.TurnStatusInProgress},
	})})
	apply("status-active", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		Ref: nxq6SessionRef, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
	})})
	apply("item-started-agent", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyItemStarted, appwire.ItemLifecycleParams{
		Ref: nxq6SessionRef, TurnID: "turn_2", Item: appwire.ThreadItem{Type: "agentMessage", ID: "agent-2", TurnID: "turn_2"},
	})})

	// Streaming deltas: the exact condition the kata calls out ("shortly
	// after a turn started and notifications were arriving"). Growing text
	// forces the transcript to reflow every render, which is the highest-
	// churn case for the viewport/AppShell composition.
	var built strings.Builder
	words := strings.Fields("the quick brown fox jumps over the lazy dog while notifications keep arriving during this turn and the composer stays in queue mode the whole time")
	for i, w := range words {
		built.WriteString(w)
		built.WriteString(" ")
		apply("agent-delta", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
			Ref: nxq6SessionRef, TurnID: "turn_2", ItemID: "agent-2", Delta: w + " ",
		})})
		// A resize partway through the burst: tmux resize-window mid-stream
		// is exactly when a stale-geometry race would show up, if one exists.
		if i == len(words)/2 {
			apply("resize-120x40", tea.WindowSizeMsg{Width: 120, Height: 40})
			apply("resize-back-200x50", tea.WindowSizeMsg{Width: 200, Height: 50})
		}
	}

	apply("tool-started", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyItemStarted, appwire.ItemLifecycleParams{
		Ref: nxq6SessionRef, TurnID: "turn_2", Item: appwire.ThreadItem{Type: "commandExecution", ID: "tool-2", CallID: "call-2", TurnID: "turn_2", ToolName: "read_file", ArgumentsJSON: `{"file_path":"/tmp/x.txt"}`, Status: appwire.TurnStatusInProgress},
	})})
	for i := 0; i < 10; i++ {
		apply("tool-output-delta", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyToolOutputDelta, appwire.ToolOutputDeltaParams{
			Ref: nxq6SessionRef, TurnID: "turn_2", ItemID: "tool-2", CallID: "call-2", Delta: "line of tool output\n",
		})})
	}
	apply("tool-completed", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
		Ref: nxq6SessionRef, TurnID: "turn_2", Item: appwire.ThreadItem{Type: "commandExecution", ID: "tool-2", CallID: "call-2", TurnID: "turn_2", ToolName: "read_file", Output: "line of tool output\n", Status: "completed"},
	})})

	// Queue depth growing, as it would while the composer sits in queue
	// mode and the user (or another client) enqueues more turns.
	for depth := 1; depth <= 3; depth++ {
		preview := make([]string, depth)
		for i := range preview {
			preview[i] = "queued message "
		}
		apply("queue-changed", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyThreadQueueChanged, appwire.ThreadQueueChangedParams{
			Ref: nxq6SessionRef, Queue: appwire.QueueState{Depth: depth, Preview: preview},
		})})
	}

	apply("job-started", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifySerfJobStarted, appwire.SerfJobParams{
		Ref: nxq6SessionRef, Job: appwire.SerfJobInfo{JobID: "job-1", JobType: "delegate", Status: "running"},
	})})
	apply("job-finished", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifySerfJobFinished, appwire.SerfJobParams{
		Ref: nxq6SessionRef, Job: appwire.SerfJobInfo{JobID: "job-1", JobType: "delegate", Status: "completed"},
	})})

	apply("item-completed-agent", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
		Ref: nxq6SessionRef, TurnID: "turn_2", Item: appwire.ThreadItem{Type: "agentMessage", ID: "agent-2", TurnID: "turn_2", Text: built.String(), Status: "completed"},
	})})
	apply("turn-completed", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyTurnCompleted, appwire.TurnCompletedParams{
		Ref: nxq6SessionRef, TurnID: "turn_2", Turn: appwire.Turn{ID: "turn_2", Status: appwire.TurnStatusCompleted},
	})})
	apply("status-idle", hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		Ref: nxq6SessionRef, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
	})})
}

// nxq6FuzzSteps is a small vocabulary of realistic per-step messages a real
// tmux session could see in quick succession: streaming deltas, tool output,
// status/queue changes, and resizes. The fuzzer picks a sequence from this
// table (rather than fuzzing raw JSON, which hub_notifications_fuzz_test.go
// already covers for the no-panic floor) so every explored program stays
// semantically realistic while its ORDER and INTERLEAVING are what varies.
func nxq6FuzzSteps(t *testing.T) []tea.Msg {
	t.Helper()
	return []tea.Msg{
		tea.WindowSizeMsg{Width: 200, Height: 50},
		tea.WindowSizeMsg{Width: 80, Height: 24},
		tea.WindowSizeMsg{Width: 40, Height: 12},
		tea.WindowSizeMsg{Width: 200, Height: 6},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyTurnStarted, appwire.TurnStartedParams{
			Ref: nxq6SessionRef, Turn: appwire.Turn{ID: "turn_f", Status: appwire.TurnStatusInProgress},
		})},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
			Ref: nxq6SessionRef, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		})},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
			Ref: nxq6SessionRef, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusAwaiting},
		})},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyItemStarted, appwire.ItemLifecycleParams{
			Ref: nxq6SessionRef, TurnID: "turn_f", Item: appwire.ThreadItem{Type: "agentMessage", ID: "agent-f", TurnID: "turn_f"},
		})},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
			Ref: nxq6SessionRef, TurnID: "turn_f", ItemID: "agent-f", Delta: "streamed chunk of text arriving now ",
		})},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyItemStarted, appwire.ItemLifecycleParams{
			Ref: nxq6SessionRef, TurnID: "turn_f", Item: appwire.ThreadItem{Type: "commandExecution", ID: "tool-f", CallID: "call-f", TurnID: "turn_f", ToolName: "exec", Status: appwire.TurnStatusInProgress},
		})},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyToolOutputDelta, appwire.ToolOutputDeltaParams{
			Ref: nxq6SessionRef, TurnID: "turn_f", ItemID: "tool-f", CallID: "call-f", Delta: "tool output line\n",
		})},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
			Ref: nxq6SessionRef, TurnID: "turn_f", Item: appwire.ThreadItem{Type: "commandExecution", ID: "tool-f", CallID: "call-f", TurnID: "turn_f", ToolName: "exec", Output: "tool output line\n", Status: "completed"},
		})},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyThreadQueueChanged, appwire.ThreadQueueChangedParams{
			Ref: nxq6SessionRef, Queue: appwire.QueueState{Depth: 2, Preview: []string{"queued one", "queued two"}},
		})},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyThreadQueueChanged, appwire.ThreadQueueChangedParams{
			Ref: nxq6SessionRef, Queue: appwire.QueueState{},
		})},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyItemCompleted, appwire.ItemLifecycleParams{
			Ref: nxq6SessionRef, TurnID: "turn_f", Item: appwire.ThreadItem{Type: "agentMessage", ID: "agent-f", TurnID: "turn_f", Text: "final streamed text", Status: "completed"},
		})},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyTurnCompleted, appwire.TurnCompletedParams{
			Ref: nxq6SessionRef, TurnID: "turn_f", Turn: appwire.Turn{ID: "turn_f", Status: appwire.TurnStatusCompleted},
		})},
		hubNotificationMsg{ok: true, notification: nxq6Notify(t, appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
			Ref: nxq6SessionRef, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		})},
		tea.KeyMsg{Type: tea.KeyEscape},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")},
	}
}

// FuzzSessionViewNeverBlankAboveComposer explores orderings/interleavings of
// the realistic step vocabulary above (turn lifecycle, streaming deltas, tool
// output, queue/status changes, resizes, keypresses) and checks the same
// completeness invariant after every step. A random byte string selects a
// sequence of up to 40 steps from the table; go test's default run (no
// -fuzz) replays only the seed corpus below, so this runs in the normal
// suite the same way FuzzApplyHubNotification does.
func FuzzSessionViewNeverBlankAboveComposer(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{4, 6, 4, 6, 4, 6, 3, 5, 7, 9})
	f.Add([]byte{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 3, 14, 15})
	f.Add([]byte{4, 8, 3, 6, 9, 2, 7, 1, 10, 0, 11, 5, 12})

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) == 0 {
			return
		}
		if len(program) > 40 {
			program = program[:40]
		}
		m := nxq6NewSessionModel(t)
		steps := nxq6FuzzSteps(t)
		for i, b := range program {
			msg := steps[int(b)%len(steps)]
			next, _ := m.Update(msg)
			m = next.(hubModel)
			nxq6AssertViewComplete(t, m, "fuzz-step-"+string(rune('a'+i%26)))
		}
	})
}
