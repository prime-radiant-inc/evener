package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

const tuiE2EProjectDir = "/tmp/serf-tui-e2e/serf"

// Generous backstop, not a target: WaitFor returns the instant the expected text
// appears, so a large timeout costs nothing on the happy path. It is sized to
// tolerate the real-time rendering stalls these tmux+TUI tests see when the full
// suite runs concurrently and CPU is oversubscribed.
const tuiE2EWaitTimeout = 60 * time.Second

// tuiE2EPollInterval is how often WaitFor re-checks the rendered pane. Small
// so render-driven round-trips aren't rounded up; capture-pane is ~2.6ms so
// this does not oversubscribe CPU under the 6-way session cap.
var tuiE2EPollInterval = 10 * time.Millisecond

// tmuxSessionCounter makes tmux session names unique even when parallel tests
// start within the same nanosecond.
var tmuxSessionCounter atomic.Int64

func uniqueTmuxSessionName() string {
	return fmt.Sprintf("serf-tui-e2e-%d-%d", time.Now().UnixNano(), tmuxSessionCounter.Add(1))
}

// tmuxSessionSlots bounds how many tmux+TUI sessions run concurrently. The TUI
// renders in real time; with every E2E test marked t.Parallel(), too many live
// sessions at once starve each other's render and (especially) alt-screen exit
// sequences, intermittently failing timing-sensitive scrollback assertions. The
// cap keeps most of the parallel speedup while leaving each session enough CPU
// to render promptly. Only one test opens two sessions, so a cap of 6 cannot
// deadlock on a single test's acquisitions.
var tmuxSessionSlots = make(chan struct{}, 6)

// acquireTmuxSlot blocks until a session slot is free and releases it when the
// test ends.
func acquireTmuxSlot(t *testing.T) {
	tmuxSessionSlots <- struct{}{}
	t.Cleanup(func() { <-tmuxSessionSlots })
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[\x20-\x2f]*[\x40-\x7e]`)

func TestTUITmuxE2E_DashboardProjectAndSpawn(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	screen := app.WaitFor("SERF LIVE", hub.URL(), "Launch New Session", "▾", "▍", "serf", "live task", "ops task", "1 recent")
	if strings.Contains(screen, "ended maintenance") {
		t.Fatalf("dashboard should fold ended sessions by default:\n%s", screen)
	}
	app.SendKeys("/")
	app.TypeText("ops")
	screen = app.WaitFor("Command palette", "Filter: ops", "ops task")
	if strings.Contains(screen, "live task") {
		t.Fatalf("dashboard palette should hide non-matching sessions:\n%s", screen)
	}
	app.SendKeys("Escape")
	app.WaitFor("SERF LIVE", "live task", "ops task")

	initialTreeRequests := hub.WaitForTreeRequests(t, 1)
	app.SendKeys("r")
	hub.WaitForTreeRequests(t, initialTreeRequests+1)

	// The cursor starts on the serf project row; Enter collapses the group
	// and folds its child sessions out of the tree.
	app.WaitFor("SERF LIVE", "Project:  serf", "Action:   enter toggles project")
	app.SendKeys("Enter")
	screen = app.WaitFor("SERF LIVE", "▸ ● serf")
	if strings.Contains(screen, "live task") || strings.Contains(screen, "ended maintenance") {
		t.Fatalf("collapsed project should hide child sessions:\n%s", screen)
	}
	app.SendKeys("Right")
	app.WaitFor("SERF LIVE", "live task", "1 recent")
	// Down to the ended-sessions toggle, Enter to reveal the ended session.
	app.SendKeys("Down", "Down")
	app.WaitFor("SERF LIVE", "Action:   enter toggles ended sessions")
	app.SendKeys("Enter")
	app.WaitFor("SERF LIVE", "live task", "ended maintenance")
	app.SendKeys("/")
	app.TypeText("ended")
	screen = app.WaitFor("Command palette", "Filter: ended", "ended maintenance")
	if strings.Contains(screen, "live task") {
		t.Fatalf("dashboard palette should hide non-matching live session:\n%s", screen)
	}
	app.SendKeys("Escape")
	app.WaitFor("SERF LIVE", "live task", "ended maintenance")

	app.SendKeys("n")
	app.WaitFor("serf / new session", "Dir:      "+tuiE2EProjectDir, "Prompt (optional):")
	app.SendKeys("Tab", "Tab", "Tab", "C-u")
	app.TypeText("/tmp/serf-tui-e2e/custom")
	app.WaitFor("Dir:      /tmp/serf-tui-e2e/custom")
	app.SendKeys("Tab")
	app.TypeLine("spawn from dashboard")
	app.WaitFor("serf / session / spawned session 1")
	spawns := hub.WaitForSpawns(t, 1)
	if spawns[0].CWD != "/tmp/serf-tui-e2e/custom" {
		t.Fatalf("dashboard spawn cwd=%q, want /tmp/serf-tui-e2e/custom", spawns[0].CWD)
	}
	if testInputText(spawns[0].Input) != "spawn from dashboard" {
		t.Fatalf("dashboard spawn prompt=%q, want spawn from dashboard", testInputText(spawns[0].Input))
	}
	if spawns[0].ModelProvider != "" || spawns[0].Model != "openai/gpt-5" {
		t.Fatalf("dashboard spawn model=%s/%s, want openai/gpt-5", spawns[0].ModelProvider, spawns[0].Model)
	}

	app.SendKeys("C-o")
	app.WaitFor("SERF LIVE", "live task")
	app.SendKeys("n")
	app.WaitFor("serf / new session", "Dir:      "+tuiE2EProjectDir, "Prompt (optional):")
	app.TypeLine("spawn from project")
	app.WaitFor("serf / session / spawned session 2")
	spawns = hub.WaitForSpawns(t, 2)
	if spawns[1].CWD != tuiE2EProjectDir {
		t.Fatalf("project spawn cwd=%q, want %q", spawns[1].CWD, tuiE2EProjectDir)
	}
	if testInputText(spawns[1].Input) != "spawn from project" {
		t.Fatalf("project spawn prompt=%q, want spawn from project", testInputText(spawns[1].Input))
	}
	if spawns[1].ModelProvider != "" || spawns[1].Model != "openai/gpt-5" {
		t.Fatalf("project spawn model=%s/%s, want openai/gpt-5", spawns[1].ModelProvider, spawns[1].Model)
	}

	app.SendKeys("C-o")
	app.WaitFor("SERF LIVE", "live task")
	app.SendKeys("q")
	app.WaitForExit()
}

func TestTUITmuxE2E_AppShellPreservesLayoutAcrossWidths(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	screen := app.WaitFor("SERF LIVE", "live task", "dashboard")
	requirePaneOrder(t, screen, "SERF LIVE", "live task", "dashboard")

	app.SendKeys("/")
	screen = app.WaitFor("Command palette", "dashboard")
	requirePaneOrder(t, screen, "SERF LIVE", "Command palette", "dashboard")

	app.Resize(60, 30)
	screen = app.WaitFor("Command palette", "dashboard")
	requirePaneOrder(t, screen, "SERF LIVE", "Command palette", "dashboard")

	app.SendKeys("C-o")
	screen = app.WaitFor("SERF LIVE", "live task", "dashboard")
	requirePaneOrder(t, screen, "SERF LIVE", "live task", "dashboard")

	app.SendKeys("n")
	screen = app.WaitFor("serf / new session", "Prompt (optional):", "ctrl+o: dashboard")
	requirePaneOrder(t, screen, "serf / new session", "Prompt (optional):", "ctrl+o: dashboard")

	app.SendKeys("C-o")
	screen = app.WaitFor("SERF LIVE", "live task", "dashboard")
	requirePaneOrder(t, screen, "SERF LIVE", "live task", "dashboard")
}

func TestTUITmuxE2E_DashboardNarrowWideStates(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetSessionTitle("01LIVE", "live dashboard task with a title long enough to truncate cleanly")
	defer hub.Close()

	wide := startTUITmuxSized(t, bin, hub.URL(), 140, 40)
	defer wide.Close()
	wideScreen := wide.WaitFor("SERF LIVE", "details", "Project:  serf", "Live:     1", "Dir:      "+tuiE2EProjectDir)
	if strings.Contains(wideScreen, "Prompt (optional):") || strings.Contains(wideScreen, "enter: send") {
		t.Fatalf("wide dashboard rendered a composer:\n%s", wideScreen)
	}
	t.Logf("wide dashboard capture:\n%s", wideScreen)
	wide.SendKeys("q")
	wide.WaitForExit()

	narrow := startTUITmuxSized(t, bin, hub.URL(), 60, 30)
	defer narrow.Close()
	// The narrow dashboard collapses to a single column: the session list
	// still renders (with the long title hard-truncated to the pane width)
	// but the wide-only details drawer must not appear.
	narrowScreen := narrow.WaitFor("SERF LIVE", "live dashboard task with a title long")
	if strings.Contains(narrowScreen, "truncate cleanly") {
		t.Fatalf("narrow dashboard did not truncate the long session title:\n%s", narrowScreen)
	}
	if strings.Contains(narrowScreen, "details") || strings.Contains(narrowScreen, "Action:   enter") {
		t.Fatalf("narrow dashboard rendered details drawer:\n%s", narrowScreen)
	}
	if strings.Contains(narrowScreen, "Prompt (optional):") || strings.Contains(narrowScreen, "enter: send") {
		t.Fatalf("narrow dashboard rendered a composer:\n%s", narrowScreen)
	}
	t.Logf("narrow dashboard capture:\n%s", narrowScreen)
	narrow.SendKeys("q")
	narrow.WaitForExit()
}

func TestTUITmuxE2E_DashboardFooterAnchorsToBottom(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmuxSized(t, bin, hub.URL(), 124, 18)
	defer app.Close()

	app.WaitFor("SERF LIVE", "select")
	footerAnchored := func(screen string) bool {
		lines := strings.Split(strings.TrimSuffix(screen, "\n"), "\n")
		lastNonEmpty := -1
		for i, line := range lines {
			if strings.TrimSpace(line) != "" {
				lastNonEmpty = i
			}
		}
		return lastNonEmpty == len(lines)-1
	}
	// The dashboard's first paint can briefly leave the footer mid-screen before
	// the layout settles; wait for it to anchor rather than racing the initial
	// render (which flaked under parallel load).
	app.WaitUntil("footer/action line anchors to the bottom row", footerAnchored)
}

func TestTUITmuxE2E_DashboardRecentOnlyState(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.EndDashboardSessions()
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	screen := app.WaitFor("SERF LIVE", "0 live", "2 recent", "1 recent", "filter")
	if strings.Contains(screen, "ended maintenance") || strings.Contains(screen, "ops task") {
		t.Fatalf("recent-only dashboard should fold ended sessions by default:\n%s", screen)
	}
	if strings.Contains(screen, "Prompt (optional):") || strings.Contains(screen, "enter: send") {
		t.Fatalf("recent-only dashboard rendered session composer content:\n%s", screen)
	}
	t.Logf("recent-only dashboard capture:\n%s", screen)

	app.SendKeys("Down", "Enter")
	app.WaitFor("SERF LIVE", "ended maintenance")
	app.SendKeys("q")
	app.WaitForExit()
}

func TestTUITmuxE2E_ProjectHistoryReadOnlyAndResume(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	app.WaitFor("SERF LIVE", "live task")
	screen := app.WaitFor("SERF LIVE", "live task", "1 recent")
	for _, unwanted := range []string{"enter: send", "Prompt (optional):"} {
		if strings.Contains(screen, unwanted) {
			t.Fatalf("dashboard rendered composer/spawn text %q:\n%s", unwanted, screen)
		}
	}

	app.SendKeys("Down", "Down", "Enter", "Down", "Enter")
	screen = app.WaitFor("ended maintenance", "src serf", "send")
	if strings.Contains(screen, "read-only") || strings.Contains(screen, "source does not support send") {
		t.Fatalf("ended resumable session should not render read-only:\n%s", screen)
	}

	app.TypeLine("resume from ended")
	if sends := hub.WaitForSends(t, 1); sends[0] != "resume from ended" {
		t.Fatalf("resumed send text=%q, want resume from ended", sends[0])
	}
}

func TestTUITmuxE2E_CodexSpawnUsesHarnessModelPicker(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetHarnesses([]appwire.HarnessDescriptor{
		{ID: "serf", Label: "serf", Kind: "serf"},
		{ID: "codex-local", Label: "codex-local", Kind: "codex"},
	})
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	app.WaitFor("SERF LIVE", "live task")
	app.SendKeys("n")
	app.WaitFor("Harness:  serf", "Model:    openai/gpt-5")
	app.SendKeys("Tab", "Enter")
	app.WaitFor("Harness:  codex-local", "Model:    (harness default)")
	app.SendKeys("Tab", "Enter")
	app.WaitFor("Select codex-local model", "codex-local/gpt-5.3-codex")
	app.SendKeys("Enter")
	app.WaitFor("Harness:  codex-local", "Model:    codex-local/gpt-5.3-codex")
	app.SendKeys("Tab", "Tab")
	app.TypeLine("spawn via codex")
	app.WaitFor("spawned session 1")
	spawns := hub.WaitForSpawns(t, 1)
	if spawns[0].Harness != "codex-local" {
		t.Fatalf("harness=%q, want codex-local", spawns[0].Harness)
	}
	if spawns[0].ModelProvider != "" || spawns[0].Model != "gpt-5.3-codex" {
		t.Fatalf("codex spawn model=%s/%s, want raw gpt-5.3-codex", spawns[0].ModelProvider, spawns[0].Model)
	}
}

func TestTUITmuxE2E_SessionCommandsAndNavigation(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("serf / session / live task", "initial question", "initial answer", "tool output from e2e")

	app.TypeLine("hello from tmux")
	app.WaitFor("hello from tmux")
	if sends := hub.WaitForSends(t, 1); sends[0] != "hello from tmux" {
		t.Fatalf("send text=%q, want hello from tmux", sends[0])
	}

	// /fork drops into browse mode with the fork prompt and footer hint.
	app.TypeLine("/fork")
	app.WaitFor("Select a user turn, then press f to fork.", "f: fork selected user turn")
	app.SendKeys("i")
	app.WaitFor("enter send")

	// /help lists the slash commands and the browse keybindings.
	app.TypeLine("/help")
	app.WaitFor("Available commands:", "/dashboard Go to live dashboard", "/theme")

	// /wat names no built-in command, so it forwards to the session (design
	// §10) instead of dead-ending with "Unknown command" — the plugin-command
	// catalog isn't the TUI's to know, so an unrecognized word is always
	// worth trying against the session's own expander.
	app.TypeLine("/wat")
	if sends := hub.WaitForSends(t, 2); sends[1] != "/wat " {
		t.Fatalf("send text=%q, want the forwarded literal %q", sends[1], "/wat ")
	}

	// /project returns to the dashboard focused on this session's project.
	app.TypeLine("/project")
	app.WaitFor("SERF LIVE", "Project:  serf", "live task")
	// Down to the ended-sessions toggle, Enter to reveal the ended session.
	app.SendKeys("Down", "Down")
	app.WaitFor("SERF LIVE", "Action:   enter toggles ended sessions")
	app.SendKeys("Enter")
	app.WaitFor("SERF LIVE", "ended maintenance")
	app.SendKeys("/")
	app.WaitFor("Command palette", "live task", "ended maintenance")
	app.TypeText("ended")
	app.WaitFor("Filter: ended", "ended maintenance")
	app.SendKeys("Escape")
	openLiveSession(t, app)
	app.WaitFor("serf / session / live task", "enter send")

	app.TypeLine("/auth openai")
	app.WaitFor("OpenAI auth: signed out")

	app.TypeLine("/login openai")
	app.WaitFor("OpenAI sign-in URL:", "https://auth.example/authorize", "Paste the full OpenAI redirect URL")
	app.TypeLine("http://localhost:1455/auth/callback?code=abc&state=flow")
	app.WaitFor("OpenAI login complete. OpenAI auth: oauth (tmux@example.com)")
	completions := hub.WaitForAuthCompletions(t, 1)
	if completions[0].FlowID != "flow-1" || completions[0].RedirectURL == "" {
		t.Fatalf("auth completion=%+v, want flow-1 and redirect URL", completions[0])
	}

	app.TypeLine("/logout openai")
	app.WaitFor("OpenAI sign-out complete.")
	authCalls := hub.WaitForAuthCalls(t, 4)
	if got := strings.Join(authCalls, ","); got != "status:openai,login-start:openai,login-complete:openai,logout:openai" {
		t.Fatalf("auth calls=%s", got)
	}

	app.TypeLine("/theme")
	app.WaitFor("Select theme", "system", "dark", "light")
	app.SendKeys("Down", "Enter")
	app.WaitFor("Switched to dark theme.")

	app.TypeLine("/tasks")
	app.WaitFor("Tasks (1):", "wire tui e2e")

	app.TypeLine("/agents")
	app.WaitFor("Select transcript", "subagent inspect")
	app.SendKeys("Down", "Enter")
	app.WaitFor("Viewing subagent inspect", "subagent transcript from e2e")
	app.SendKeys("Escape")
	app.WaitFor("enter send")

	app.TypeLine("/details")
	app.WaitFor("Session:  01LIVE", "Dir:      "+tuiE2EProjectDir)
	app.SendKeys("Escape")
	app.WaitFor("enter send")

	app.TypeLine("/interrupt")
	app.WaitFor("Interrupt sent.")
	if got := hub.WaitForActionCount(t, "interrupt", 1); got != 1 {
		t.Fatalf("interrupt count=%d, want 1", got)
	}

	app.TypeLine("/compact")
	app.WaitFor("Context compacted.")
	if got := hub.WaitForActionCount(t, "compact", 1); got != 1 {
		t.Fatalf("compact count=%d, want 1", got)
	}

	app.TypeLine("/model")
	app.WaitFor("Select model", "openai/gpt-5")
	app.SendKeys("Enter")
	app.WaitFor("Switching to model openai/gpt-5")
	if models := hub.WaitForModels(t, 1); models[0] != "gpt-5" {
		t.Fatalf("model request=%q, want gpt-5", models[0])
	}

	app.TypeLine("/model gpt-5-mini")
	if models := hub.WaitForModels(t, 2); models[1] != "gpt-5-mini" {
		t.Fatalf("model request=%q, want gpt-5-mini", models[1])
	}

	app.TypeLine("/project")
	app.WaitFor("SERF LIVE", "Project:  serf")
	openLiveSession(t, app)
	app.WaitFor("serf / session / live task")

	app.TypeLine("/dashboard")
	app.WaitFor("SERF LIVE", "live task")
	openLiveSession(t, app)
	app.WaitFor("serf / session / live task")

	app.TypeLine("/clear")
	app.WaitFor("serf / session / cleared session")
	if got := hub.WaitForActionCount(t, "clear", 1); got != 1 {
		t.Fatalf("clear count=%d, want 1", got)
	}
}

func TestTUITmuxE2E_BrowseAndFork(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	// Browse-mode fork: k/j move the selection cursor across turns (auto-
	// scrolling to keep it visible) so a user turn can be reached and forked.
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("serf / session / live task", "initial question", "initial answer")

	app.SendKeys("Escape")
	app.WaitFor("esc/i/q: compose", "f: fork selected user turn", "▶ ▍ initial answer")
	app.SendKeys("f")
	app.WaitFor("Select a user turn to fork.")
	if forks := hub.Forks(); len(forks) != 0 {
		t.Fatalf("invalid fork selection should not call hub: %+v", forks)
	}
	app.SendKeys("i")
	app.WaitFor("enter send")
	app.SendKeys("Escape")
	app.WaitFor("esc/i/q: compose")
	// These k presses must move the browse cursor up to the user turn.
	app.SendKeys("k")
	app.WaitFor("▶ ▍ initial answer")
	app.SendKeys("k")
	app.WaitFor("▶ ▍ ✓ exec")
	app.SendKeys("k")
	app.WaitFor("▶ ┃  > initial question")
	app.SendKeys("f")
	app.WaitFor("Fork draft for turn 1", "> initial question")

	app.SendKeys("Enter")
	app.WaitFor("serf / session / fork child")
	forks := hub.WaitForForks(t, 1)
	if forks[0].SourceTurnID != "1" {
		t.Fatalf("fork source turn=%q, want 1", forks[0].SourceTurnID)
	}
	if forks[0].EditedInput != "initial question" {
		t.Fatalf("fork edited input=%q, want initial question", forks[0].EditedInput)
	}
	app.SendKeys("C-o")
	app.WaitFor("SERF LIVE", "live task")
}

func TestTUITmuxE2E_FailedForkPreservesDraft(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetFailFork(true)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("serf / session / live task", "initial question", "initial answer")

	app.SendKeys("Escape")
	app.WaitFor("esc/i/q: compose", "f: fork selected user turn")
	app.SendKeys("k")
	app.SendKeys("k")
	app.SendKeys("f")
	app.WaitFor("Fork draft for turn 1", "> initial question")
	app.TypeText(" with edit")
	app.SendKeys("Enter")
	app.WaitFor("Fork failed: appwire thread/fork: fork failed", "> initial question with edit")
	forks := hub.WaitForForks(t, 1)
	if forks[0].EditedInput != "initial question with edit" {
		t.Fatalf("failed fork edited input=%q, want edited text", forks[0].EditedInput)
	}
	if forks[0].Label != "original before fork" {
		t.Fatalf("failed fork label=%q, want original before fork", forks[0].Label)
	}
}

func TestTUITmuxE2E_CapabilityGates(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetSessionCapabilities("01LIVE", appwire.ThreadCapabilities{})
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("serf / session / live task", "initial question")

	// Disabled actions surface as dismissable notice overlays. Each must be
	// cleared (ctrl+x) before the next command so the composer is reachable.
	app.TypeLine("/interrupt")
	app.WaitFor("Interrupt is not available for this session.", "ctrl+x: dismiss notice")
	if got := hub.ActionCount("interrupt"); got != 0 {
		t.Fatalf("interrupt should not call hub when capability is disabled: %d", got)
	}
	app.SendKeys("C-x")
	app.WaitFor("enter send")

	app.TypeLine("/compact")
	app.WaitFor("Compact is not available for this session.", "ctrl+x: dismiss notice")
	if got := hub.ActionCount("compact"); got != 0 {
		t.Fatalf("compact should not call hub when capability is disabled: %d", got)
	}
	app.SendKeys("C-x")
	app.WaitFor("enter send")

	app.TypeLine("/clear")
	app.WaitFor("Clear is not available for this session.", "ctrl+x: dismiss notice")
	if got := hub.ActionCount("clear"); got != 0 {
		t.Fatalf("clear should not call hub when capability is disabled: %d", got)
	}
	app.SendKeys("C-x")
	app.WaitFor("enter send")

	app.TypeLine("/model gpt-5-mini")
	app.WaitFor("Model change is not available for this session.", "ctrl+x: dismiss notice")
	if models := hub.Models(); len(models) != 0 {
		t.Fatalf("model should not call hub when capability is disabled: %+v", models)
	}
	app.SendKeys("C-x")
	app.WaitFor("enter send")

	// The session command palette marks every gated action as disabled,
	// including fork, with the source's advertised reason.
	app.SendKeys("C-p")
	app.WaitFor(
		"Command palette",
		"/clear  clear current session  disabled: source does not advertise clear",
		"/fork  browse and fork a user turn  disabled: source does not advertise fork",
		"/shutdown  stop this resumable session  disabled: source does not advertise shutdown",
	)
	app.SendKeys("Escape")
	app.WaitFor("enter send")

	// Send is read-only when the source does not advertise it: the composer
	// keeps the draft and the hub never receives the turn.
	app.TypeText("blocked send")
	app.WaitFor("> blocked send", "read-only: source does not support send")
	app.SendKeys("Enter")
	app.WaitFor("> blocked send", "read-only: source does not support send")
	if sends := hub.Sends(); len(sends) != 0 {
		t.Fatalf("send should not call hub when capability is disabled: %+v", sends)
	}
}

func TestTUITmuxE2E_SessionCommandPalettePreservesDraft(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.TypeText("draft before palette")
	app.WaitFor("> draft before palette")
	app.SendKeys("C-p")
	screen := app.WaitFor("Command palette", "/help", "/model", "> draft before palette")
	if strings.Contains(screen, "/search") || strings.Contains(screen, "ended maintenance") {
		t.Fatalf("session palette should not expose cross-session search:\n%s", screen)
	}
	app.TypeText("model")
	app.WaitFor("Filter: model", "/model", "> draft before palette")
	app.SendKeys("Escape")
	app.WaitFor("enter send", "> draft before palette")
}

func TestTUITmuxE2E_SessionLeadingSlashOpensPalette(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.TypeText("/")
	app.WaitFor("Command palette", "/help", "/model")
}

func TestTUITmuxE2E_CtrlCRequiresDoublePressFromSession(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	// Use a session with no in-flight turn so the first ctrl+c exercises the
	// pure quit-arming path. (With an active turn the first ctrl+c interrupts
	// the turn first — covered by the header/composer state behavior.)
	openEndedSession(t, app)
	app.SendKeys("C-c")
	// Poll over a window wider than any realistic render delay so a broken
	// handler that defers the warning render is reliably caught. A single
	// fixed sleep misses warnings that render after the sleep completes;
	// repeated captures over 300 ms cannot.
	settleDeadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(settleDeadline) {
		if screen := app.Capture(); strings.Contains(screen, "Press ctrl+c again") || strings.Contains(screen, "Restore this session:") {
			t.Fatalf("first ctrl+c should not render an in-app quit warning:\n%s", screen)
		}
		time.Sleep(tuiE2EPollInterval)
	}
	// Positive gate: session must still be alive after the settling window.
	app.WaitFor("serf / session / ended maintenance")
	app.SendKeys("C-c")
	app.WaitForExit()
}

func TestTUITmuxE2E_CtrlCRestoreMessageSurvivesAltScreenExit(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmuxAltScreen(t, bin, hub.URL(), 120, 28)
	defer app.Close()

	openEndedSession(t, app)
	app.SendKeys("C-c")
	// Poll over a window wider than any realistic render delay so a broken
	// handler that defers the warning render is reliably caught.
	settleDeadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(settleDeadline) {
		if screen := app.Capture(); strings.Contains(screen, "Press ctrl+c again") || strings.Contains(screen, "Restore this session:") {
			t.Fatalf("first ctrl+c should not render an in-app quit warning:\n%s", screen)
		}
		time.Sleep(tuiE2EPollInterval)
	}
	// Positive gate: session must still be alive after the settling window.
	app.WaitFor("serf / session / ended maintenance")
	app.SendKeys("C-c")
	// serf-tui exits the alternate screen and prints the restore instructions
	// to the normal screen on its way out. The pane is kept alive past
	// serf-tui's exit (see startTUITmuxAltScreen) so tmux reliably drains that
	// trailing output instead of dropping it when freezing a just-dead pane —
	// then we poll the scrollback until the message renders rather than racing
	// a single post-exit capture.
	app.WaitForHistory("Restore this session:", "serf-tui --hub-addr "+hub.URL(), "local:01PAST")
}

func TestTUITmuxE2E_ModelPickerShowsAuthRequiredModels(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetAuthRequiredModels(true)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("serf / session / live task", "initial question")

	app.TypeLine("/model")
	app.WaitFor("Select model", "openai/gpt-5", "disabled: Login required", "/auth openai")
	app.SendKeys("Enter")
	app.WaitFor("Select model", "disabled: Login required")
	if models := hub.Models(); len(models) != 0 {
		t.Fatalf("auth-required model should not be selected: %+v", models)
	}
}

func TestTUITmuxE2E_SessionHeaderStatusAndComposerStates(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetSessionState("01LIVE", appwire.ThreadStatusActive)
	hub.SetSessionContextPressure("01LIVE", 0.66)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor(
		"serf / session / live task",
		"● ACTIVE",
		"src serf",
		"model gpt-5",
		"dir "+tuiE2EProjectDir,
		"2 turns",
		"ctx 66%",
		"status: hub connected",
		"queue: ready",
		"busy: turn_active",
	)

	app.TypeLine("/auth openai")
	app.WaitFor("OpenAI auth: signed out", "auth: openai signed out")

	// While a turn is active the composer is in queue mode: Enter enqueues
	// the multiline draft via turn/queue rather than starting a new turn.
	app.TypeText("first line")
	app.WaitFor("> first line")
	app.SendKeys("C-j")
	app.TypeText("second line")
	app.WaitFor("second line")
	screen := app.WaitFor("● QUEUE", "enter queue", "ctrl+s steer")
	if strings.Contains(screen, "enter send") {
		t.Fatalf("active session composer should be in queue mode, not send mode:\n%s", screen)
	}
	app.SendKeys("Enter")
	queues := hub.WaitForQueues(t, 1)
	if testInputText(queues[0].Input) != "first line\nsecond line" {
		t.Fatalf("queue input=%+v, want multiline input", queues[0].Input)
	}

	hub.SetSessionState("01LIVE", appwire.ThreadStatusIdle)
	app.SendKeys("C-o")
	app.WaitFor("SERF LIVE")
	openLiveSession(t, app)
	app.WaitFor("● IDLE", "send: ready")

	hub.SetFailSend(true)
	app.TypeLine("send failure draft")
	app.WaitFor("Send failed.", "error: Send failed: appwire turn/start: send failed", "cause appwire turn/start: send failed", "> send failure draft")
	app.SendKeys("C-x")
	app.WaitFor("enter send")

	app.SendKeys("C-o")
	app.WaitFor("SERF LIVE")
	app.SendKeys("Up", "Up", "Up", "Up", "Up", "Up")
	app.WaitFor("Launch New Session", "hub default")
	app.SendKeys("Down", "Down", "Down")
	app.WaitFor("Action:   enter toggles ended sessions")
	app.SendKeys("Enter")
	app.WaitFor("ended maintenance")
	app.SendKeys("Down")
	app.WaitFor("Session:  01PAST")
	app.SendKeys("Enter")
	screen = app.WaitFor("serf / session / ended maintenance", "● NOTLOADED", "send: ready")
	if strings.Contains(screen, "read-only") || strings.Contains(screen, "source does not support send") {
		t.Fatalf("ended resumable session should not render read-only:\n%s", screen)
	}
}

func TestTUITmuxE2E_HubStreamingAssistantDeltaBeforeRefresh(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("serf / session / live task", "initial answer")
	app.TypeLine("stream please")
	hub.WaitForSends(t, 1)

	hub.BroadcastAgentDelta("01LIVE", "partial live answer")
	app.WaitFor("partial live answer")

	hub.AppendAssistantFinal("01LIVE", "partial live answer done")
	app.SendKeys("C-o")
	app.WaitFor("SERF LIVE", "live task")
	openLiveSession(t, app)
	app.WaitFor("partial live answer done")
}

func TestTUITmuxE2E_HubStreamingToolGroupBeforeRefresh(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("serf / session / live task", "initial answer")

	hub.BroadcastToolStarted("01LIVE")
	hub.BroadcastToolOutputDelta("01LIVE", "tmux tool output\n")
	hub.BroadcastToolCompleted("01LIVE")
	app.WaitFor("read  /tmp/tmux-tool.txt", "tmux tool output")

	hub.AppendToolFinal("01LIVE")
	app.SendKeys("C-o")
	app.WaitFor("SERF LIVE", "live task")
	openLiveSession(t, app)
	app.WaitFor("read  /tmp/tmux-tool.txt", "tmux tool output")
}

func TestTUITmuxE2E_APIErrorsRenderInPlace(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("serf / session / live task", "initial question")

	// Backend failures surface as in-place notice overlays carrying the
	// source and cause; each is dismissed (ctrl+x) before the next action.
	hub.SetFailTasks(true)
	app.TypeLine("/tasks")
	app.WaitFor("Tasks failed.", "source serf", "cause appwire serf/tasks/list: tasks failed")
	app.SendKeys("C-x")
	app.WaitFor("enter send")

	hub.SetFailSend(true)
	app.TypeLine("send should fail")
	app.WaitFor("Send failed.", "cause appwire turn/start: send failed", "> send should fail")
	app.SendKeys("C-x")
	app.WaitFor("enter send")

	app.SendKeys("C-o")
	app.WaitFor("SERF LIVE", "live task")
	hub.SetFailSpawn(true)
	app.SendKeys("n")
	app.WaitFor("serf / new session", "Prompt (optional):")
	app.TypeLine("spawn should fail")
	app.WaitFor("Hub spawn failed.", "cause appwire thread/start: spawn failed", "> spawn should fail")
}

// openLiveSession navigates from the dashboard to the "live task" session and
// opens it. The dashboard sorts the serf project first (it owns the live
// session) so the fixed tree positions are: row 0 "Launch New Session",
// row 1 the serf project, row 2 the serf project's first live session
// ("live task"). We anchor to row 0 with a burst of Up presses (selection
// clamps at the top) so the helper works from any prior dashboard state, then
// step down to the live session and confirm the wide-layout details drawer
// before pressing Enter so the open is deterministic rather than racing the
// row cursor.
func openLiveSession(t *testing.T, app *tmuxTUI) {
	t.Helper()
	app.WaitFor("SERF LIVE", "serf", "live task")
	app.SendKeys("Up", "Up", "Up", "Up", "Up", "Up")
	app.WaitFor("SERF LIVE", "Launch New Session", "hub default")
	app.SendKeys("Down", "Down")
	app.WaitFor("Session:  01LIVE", "Action:   enter opens session")
	app.SendKeys("Enter")
	app.WaitFor("serf / session / live task")
}

// openEndedSession reveals and opens the folded ended "ended maintenance"
// session (01PAST), which carries no in-flight turn. Tree positions from the
// top: 0 Launch, 1 serf project, 2 live session, 3 the ended-sessions toggle.
// Anchoring to row 0 first keeps the helper robust to prior selection state.
func openEndedSession(t *testing.T, app *tmuxTUI) {
	t.Helper()
	app.WaitFor("SERF LIVE", "serf", "live task")
	app.SendKeys("Up", "Up", "Up", "Up", "Up", "Up")
	app.WaitFor("SERF LIVE", "Launch New Session", "hub default")
	app.SendKeys("Down", "Down", "Down")
	app.WaitFor("Action:   enter toggles ended sessions")
	app.SendKeys("Enter")
	app.WaitFor("ended maintenance")
	app.SendKeys("Down")
	app.WaitFor("Session:  01PAST", "Action:   enter opens session")
	app.SendKeys("Enter")
	app.WaitFor("serf / session / ended maintenance")
}

func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is required for TUI E2E tests")
	}
}

var (
	tuiBinaryOnce sync.Once
	tuiBinaryPath string
	errTUIBinary  error
)

// buildTUIBinary compiles the serf-tui binary once per test process and returns
// the shared path. The E2E tests only execute the binary (never mutate it), so a
// single build is safe — and it avoids ~20 redundant compiles competing for CPU
// with the latency-sensitive tmux render loop when the suite runs in parallel.
func buildTUIBinary(t *testing.T) string {
	t.Helper()
	tuiBinaryOnce.Do(func() {
		repoRoot, err := filepath.Abs("../..")
		if err != nil {
			errTUIBinary = err
			return
		}
		dir, err := os.MkdirTemp("", "serf-tui-e2e-bin-")
		if err != nil {
			errTUIBinary = err
			return
		}
		bin := filepath.Join(dir, "serf-tui")
		buildArgs := []string{"build", "-o", bin}
		// SERF_E2E_COVER=<dir>: build an instrumented binary so the tmux'd TUI
		// subprocess emits coverage into that GOCOVERDIR (see tuiCoverEnvPrefix).
		// Unset (the default) leaves the build and the launch command unchanged.
		if os.Getenv("SERF_E2E_COVER") != "" {
			buildArgs = append(buildArgs, "-cover")
		}
		buildArgs = append(buildArgs, "./cmd/serf-tui")
		cmd := exec.Command("go", buildArgs...)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			errTUIBinary = fmt.Errorf("build serf-tui: %w\n%s", err, out)
			return
		}
		tuiBinaryPath = bin
	})
	if errTUIBinary != nil {
		t.Fatalf("build serf-tui: %v", errTUIBinary)
	}
	return tuiBinaryPath
}

type tmuxTUI struct {
	t       *testing.T
	session string
}

func startTUITmux(t *testing.T, bin, hubURL string) *tmuxTUI {
	t.Helper()
	return startTUITmuxSized(t, bin, hubURL, 140, 40)
}

// tuiCoverEnvPrefix returns a "GOCOVERDIR=<dir> " shell prefix when
// SERF_E2E_COVER is set, so the tmux-launched TUI (built with -cover, see
// buildTUIBinary) writes its coverage counters there; empty otherwise, leaving
// the launch command byte-identical on the default path.
func tuiCoverEnvPrefix() string {
	if dir := os.Getenv("SERF_E2E_COVER"); dir != "" {
		return "GOCOVERDIR=" + shellQuote(dir) + " "
	}
	return ""
}

func startTUITmuxSized(t *testing.T, bin, hubURL string, width, height int) *tmuxTUI {
	t.Helper()
	acquireTmuxSlot(t)
	session := uniqueTmuxSessionName()
	command := tuiCoverEnvPrefix() + shellQuote(bin) + " -debug -no-auto-start-hub -hub-addr " + shellQuote(hubURL)
	runTmux(t, "new-session", "-d", "-x", strconv.Itoa(width), "-y", strconv.Itoa(height), "-s", session, command)
	runTmux(t, "set-option", "-t", session, "remain-on-exit", "on")
	pinTmuxWindowSize(t, session, width, height)
	app := &tmuxTUI{t: t, session: session}
	app.WaitFor("SERF LIVE")
	return app
}

func startTUITmuxAltScreen(t *testing.T, bin, hubURL string, width, height int) *tmuxTUI {
	t.Helper()
	acquireTmuxSlot(t)
	session := uniqueTmuxSessionName()
	// Keep the pane's shell alive past serf-tui's own exit. serf-tui leaves the
	// alternate screen and prints its restore instructions to the normal screen
	// as the very last thing it does before exiting; if the process then dies
	// immediately, a detached tmux pane under CPU starvation can freeze on death
	// before draining that trailing output, dropping the message entirely (the
	// scrollback comes back blank). Blocking the shell on a read it never
	// receives holds the pty open so tmux drains and renders the message; the
	// test reads it via WaitForHistory and Close() tears the session down.
	command := tuiCoverEnvPrefix() + shellQuote(bin) + " -no-auto-start-hub -hub-addr " + shellQuote(hubURL) + "; read _"
	runTmux(t, "new-session", "-d", "-x", strconv.Itoa(width), "-y", strconv.Itoa(height), "-s", session, command)
	runTmux(t, "set-option", "-t", session, "remain-on-exit", "on")
	pinTmuxWindowSize(t, session, width, height)
	app := &tmuxTUI{t: t, session: session}
	app.WaitFor("SERF LIVE")
	return app
}

// pinTmuxWindowSize forces the detached session's window to the exact
// requested geometry. tmux clamps a detached new-session to the size of the
// smallest attached client (typically ~80x24, or the harness default of
// 46x24 with no client), ignoring new-session -x/-y once the process draws.
// Switching the window-size option to manual and resizing makes the pane the
// geometry the TUI actually renders against.
func pinTmuxWindowSize(t *testing.T, session string, width, height int) {
	t.Helper()
	runTmux(t, "set-option", "-t", session, "window-size", "manual")
	runTmux(t, "resize-window", "-t", session, "-x", strconv.Itoa(width), "-y", strconv.Itoa(height))
}

func (a *tmuxTUI) Close() {
	_ = exec.Command("tmux", "kill-session", "-t", a.session).Run()
}

func (a *tmuxTUI) SendKeys(keys ...string) {
	a.t.Helper()
	args := append([]string{"send-keys", "-t", a.session}, keys...)
	runTmux(a.t, args...)
}

func (a *tmuxTUI) TypeLine(text string) {
	a.t.Helper()
	a.TypeText(text)
	runTmux(a.t, "send-keys", "-t", a.session, "Enter")
}

func (a *tmuxTUI) TypeText(text string) {
	a.t.Helper()
	runTmux(a.t, "send-keys", "-t", a.session, "-l", text)
}

func (a *tmuxTUI) Capture() string {
	a.t.Helper()
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", a.session).CombinedOutput()
	if err != nil {
		a.t.Fatalf("capture tmux pane: %v\n%s", err, out)
	}
	return normalizePane(string(out))
}

func (a *tmuxTUI) CaptureHistory() string {
	a.t.Helper()
	out, err := exec.Command("tmux", "capture-pane", "-p", "-S", "-200", "-t", a.session).CombinedOutput()
	if err != nil {
		a.t.Fatalf("capture tmux history: %v\n%s", err, out)
	}
	return normalizePane(string(out))
}

func (a *tmuxTUI) Resize(width, height int) {
	a.t.Helper()
	runTmux(a.t, "resize-window", "-t", a.session, "-x", strconv.Itoa(width), "-y", strconv.Itoa(height))
}

func (a *tmuxTUI) WaitFor(wants ...string) string {
	a.t.Helper()
	deadline := time.Now().Add(tuiE2EWaitTimeout)
	var screen string
	for time.Now().Before(deadline) {
		if status, dead := a.PaneDeadStatus(); dead {
			a.t.Fatalf("serf-tui exited before %q (status %s)\nvisible pane:\n%s\nrecent history:\n%s", wants, status, a.Capture(), a.CaptureHistory())
		}
		screen = a.Capture()
		ok := true
		for _, want := range wants {
			if !strings.Contains(screen, want) {
				ok = false
				break
			}
		}
		if ok {
			return screen
		}
		time.Sleep(tuiE2EPollInterval)
	}
	a.t.Fatalf("timed out waiting for %q\nvisible pane:\n%s\nrecent history:\n%s", wants, screen, a.CaptureHistory())
	return ""
}

// WaitForHistory polls the scrollback until every wanted substring is present,
// returning the settled history. Mirrors WaitFor but reads scrollback so it can
// assert on content a program prints as it exits the alternate screen on its way
// out, returning the instant the message lands rather than racing a single
// post-exit capture.
func (a *tmuxTUI) WaitForHistory(wants ...string) string {
	a.t.Helper()
	deadline := time.Now().Add(tuiE2EWaitTimeout)
	var history string
	for time.Now().Before(deadline) {
		history = a.CaptureHistory()
		ok := true
		for _, want := range wants {
			if !strings.Contains(history, want) {
				ok = false
				break
			}
		}
		if ok {
			return history
		}
		time.Sleep(tuiE2EPollInterval)
	}
	a.t.Fatalf("timed out waiting for scrollback %q\nrecent history:\n%s", wants, history)
	return ""
}

// WaitUntil polls the captured pane until check returns true, returning the
// settled capture. Mirrors WaitFor but for layout/structural conditions that
// substring matching can't express (e.g. the footer anchoring to the bottom
// only after the dashboard's first paint settles), so tests don't race the
// initial render.
func (a *tmuxTUI) WaitUntil(desc string, check func(screen string) bool) string {
	a.t.Helper()
	deadline := time.Now().Add(tuiE2EWaitTimeout)
	var screen string
	for time.Now().Before(deadline) {
		if status, dead := a.PaneDeadStatus(); dead {
			a.t.Fatalf("serf-tui exited before %s (status %s)\nvisible pane:\n%s\nrecent history:\n%s", desc, status, a.Capture(), a.CaptureHistory())
		}
		screen = a.Capture()
		if check(screen) {
			return screen
		}
		time.Sleep(tuiE2EPollInterval)
	}
	a.t.Fatalf("timed out waiting until %s\nvisible pane:\n%s\nrecent history:\n%s", desc, screen, a.CaptureHistory())
	return ""
}

func requirePaneOrder(t *testing.T, screen string, parts ...string) {
	t.Helper()
	pos := -1
	for _, part := range parts {
		next := strings.Index(screen, part)
		if next < 0 {
			t.Fatalf("pane missing %q:\n%s", part, screen)
		}
		if next < pos {
			t.Fatalf("pane rendered %q before prior parts %v:\n%s", part, parts, screen)
		}
		pos = next
	}
}

func (a *tmuxTUI) WaitForExit() {
	a.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, dead := a.PaneDeadStatus(); dead {
			return
		}
		if err := exec.Command("tmux", "has-session", "-t", a.session).Run(); err != nil {
			return
		}
		time.Sleep(tuiE2EPollInterval)
	}
	a.t.Fatalf("tmux session did not exit\nvisible pane:\n%s\nrecent history:\n%s", a.Capture(), a.CaptureHistory())
}

func (a *tmuxTUI) PaneDeadStatus() (string, bool) {
	a.t.Helper()
	out, err := exec.Command("tmux", "display-message", "-p", "-t", a.session, "#{pane_dead} #{pane_dead_status}").CombinedOutput()
	if err != nil {
		return "", false
	}
	return parsePaneDeadStatus(string(out))
}

func parsePaneDeadStatus(raw string) (string, bool) {
	fields := strings.Fields(raw)
	if len(fields) == 0 || fields[0] != "1" {
		return "", false
	}
	if len(fields) > 1 && fields[1] != "" {
		return fields[1], true
	}
	return "unknown", true
}

func TestParsePaneDeadStatus(t *testing.T) {
	tests := []struct {
		raw    string
		status string
		dead   bool
	}{
		{raw: "0 \n", dead: false},
		{raw: "1 0\n", status: "0", dead: true},
		{raw: "1 2\n", status: "2", dead: true},
		{raw: "1\n", status: "unknown", dead: true},
	}
	for _, tt := range tests {
		t.Run(strings.TrimSpace(tt.raw), func(t *testing.T) {
			status, dead := parsePaneDeadStatus(tt.raw)
			if status != tt.status || dead != tt.dead {
				t.Fatalf("parsePaneDeadStatus(%q) = %q, %v; want %q, %v", tt.raw, status, dead, tt.status, tt.dead)
			}
		})
	}
}

func runTmux(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func normalizePane(s string) string {
	s = ansiPattern.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

type tuiE2EHub struct {
	t      *testing.T
	server *httptest.Server
	app    *appserver.Server

	mu              sync.Mutex
	order           []string
	sessions        map[string]*tuiE2ESession
	spawns          []appwire.ThreadStartParams
	sends           []string
	actions         map[string]int
	models          []string
	forks           []appwire.ThreadForkParams
	resumes         []appwire.ThreadResumeParams
	steers          []appwire.TurnSteerParams
	queues          []appwire.TurnQueueParams
	drains          []appwire.TurnDrainAsSteerParams
	authCalls       []string
	authCompletions []appwire.AuthLoginCompleteParams
	harnesses       []appwire.HarnessDescriptor
	spawnCount      int
	treeGets        int
	authRequired    bool
	failTasks       bool
	failSend        bool
	failSpawn       bool
	failFork        bool
}

type tuiE2ESession struct {
	ID              string
	Title           string
	State           string
	Project         string
	WorkingDir      string
	Model           string
	ContextPressure float64
	Live            bool
	CreatedAt       int64
	UpdatedAt       int64
	Kind            string
	ParentRef       string
	Capabilities    appwire.ThreadCapabilities
	Turns           []appwire.Turn
}

func newTUIE2EHub(t *testing.T) *tuiE2EHub {
	t.Helper()
	h := &tuiE2EHub{
		t:         t,
		sessions:  map[string]*tuiE2ESession{},
		actions:   map[string]int{},
		harnesses: []appwire.HarnessDescriptor{{ID: "serf", Label: "serf", Kind: "serf"}},
	}
	h.addSession(&tuiE2ESession{
		ID:           "01LIVE",
		Title:        "live task",
		State:        appwire.ThreadStatusIdle,
		Project:      "serf",
		WorkingDir:   tuiE2EProjectDir,
		Model:        "gpt-5",
		Live:         true,
		CreatedAt:    100,
		UpdatedAt:    300,
		Capabilities: fullTUIE2ECapabilities(),
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{
				{Type: "userMessage", ID: "user-1", TurnID: "turn_1", Text: "initial question"},
				{Type: "commandExecution", ID: "tool-1", TurnID: "turn_1", ToolName: "exec", ArgumentsJSON: `{"cmd":"echo e2e"}`, Output: "tool output from e2e", Status: "completed"},
				{Type: "agentMessage", ID: "agent-1", TurnID: "turn_1", Text: "initial answer", Status: "completed"},
			},
		}, {
			ID:     "turn_active",
			Status: appwire.TurnStatusInProgress,
		}},
	})
	h.sessions["01SUB"] = &tuiE2ESession{
		ID:         "01SUB",
		Title:      "subagent inspect",
		State:      appwire.ThreadStatusNotLoaded,
		Project:    "serf",
		WorkingDir: tuiE2EProjectDir,
		Model:      "gpt-5",
		Live:       false,
		CreatedAt:  50,
		UpdatedAt:  50,
		Kind:       "subagent",
		ParentRef:  "local:01LIVE",
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{
				{Type: "agentMessage", ID: "sub-agent-1", TurnID: "turn_1", Text: "subagent transcript from e2e", Status: "completed"},
			},
		}},
	}
	h.addSession(&tuiE2ESession{
		ID:           "01PAST",
		Title:        "ended maintenance",
		State:        appwire.ThreadStatusNotLoaded,
		Project:      "serf",
		WorkingDir:   tuiE2EProjectDir,
		Model:        "gpt-5",
		Live:         false,
		CreatedAt:    10,
		UpdatedAt:    10,
		Capabilities: fullTUIE2ECapabilities(),
	})
	h.addSession(&tuiE2ESession{
		ID:           "01OPS",
		Title:        "ops task",
		State:        appwire.ThreadStatusIdle,
		Project:      "ops",
		WorkingDir:   "/tmp/serf-tui-e2e/ops",
		Model:        "gpt-5",
		Live:         true,
		CreatedAt:    80,
		UpdatedAt:    200,
		Capabilities: fullTUIE2ECapabilities(),
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{
				{Type: "agentMessage", ID: "ops-agent-1", TurnID: "turn_1", Text: "ops transcript", Status: "completed"},
			},
		}},
	})

	app := appserver.NewServer(appserver.ServerConfig{ServerName: "serf-hub", SourceID: "local"})
	h.app = app
	h.registerHandlers(app)
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		app.ServeWebSocket(w, r)
	}))
	return h
}

func fullTUIE2ECapabilities() appwire.ThreadCapabilities {
	return appwire.ThreadCapabilities{
		Send:         true,
		Steer:        true,
		Queue:        true,
		Interrupt:    true,
		Compact:      true,
		Clear:        true,
		ForkFromTurn: true,
		Shutdown:     true,
		ChangeModel:  true,
	}
}

func (h *tuiE2EHub) registerHandlers(app *appserver.Server) {
	appserver.HandleTyped(app.Router(), appwire.MethodThreadList, h.handleThreadList)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, h.handleThreadRead)
	appserver.HandleTyped(app.Router(), appwire.MethodModelList, h.handleModelList)
	appserver.HandleTyped(app.Router(), appwire.MethodSerfHarnessesList, h.handleHarnessList)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, h.handleThreadStart)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadResume, h.handleThreadResume)
	appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, h.handleTurnStart)
	appserver.HandleTyped(app.Router(), appwire.MethodTurnSteer, h.handleTurnSteer)
	appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, h.handleTurnQueue)
	appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, h.handleTurnDrainAsSteer)
	appserver.HandleTyped(app.Router(), appwire.MethodSerfTasksList, h.handleTasksList)
	appserver.HandleTyped(app.Router(), appwire.MethodTurnInterrupt, h.handleTurnInterrupt)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadCompactStart, h.handleThreadCompactStart)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadModelSet, h.handleThreadModelSet)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadClear, h.handleThreadClear)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadFork, h.handleThreadFork)
	appserver.HandleTyped(app.Router(), appwire.MethodSerfAuthStatus, h.handleAuthStatus)
	appserver.HandleTyped(app.Router(), appwire.MethodSerfAuthLoginStart, h.handleAuthLoginStart)
	appserver.HandleTyped(app.Router(), appwire.MethodSerfAuthLoginComplete, h.handleAuthLoginComplete)
	appserver.HandleTyped(app.Router(), appwire.MethodSerfAuthLogout, h.handleAuthLogout)
	appserver.HandleTyped(app.Router(), appwire.MethodSerfThreadTranscriptsList, h.handleThreadTranscriptList)
}

func (h *tuiE2EHub) handleHarnessList(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return appwire.HarnessListResponse{Data: append([]appwire.HarnessDescriptor(nil), h.harnesses...)}, nil
}

func (h *tuiE2EHub) URL() string {
	return h.server.URL
}

func (h *tuiE2EHub) Close() {
	h.server.Close()
}

func (h *tuiE2EHub) SetHarnesses(harnesses []appwire.HarnessDescriptor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.harnesses = append([]appwire.HarnessDescriptor(nil), harnesses...)
}

func (h *tuiE2EHub) SetSessionTitle(id, title string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[id]; s != nil {
		s.Title = title
	}
}

func (h *tuiE2EHub) EndDashboardSessions() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range h.order {
		if s := h.sessions[id]; s != nil {
			s.State = appwire.ThreadStatusNotLoaded
			s.Live = false
		}
	}
}

func (h *tuiE2EHub) BroadcastAgentDelta(threadID, delta string) {
	h.app.Broadcast(threadID, appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: threadID,
		Ref:      "local:" + threadID,
		TurnID:   "turn_stream",
		ItemID:   "agent_stream",
		Delta:    delta,
	})
}

func (h *tuiE2EHub) BroadcastToolStarted(threadID string) {
	h.app.Broadcast(threadID, appwire.NotifyItemStarted, map[string]any{
		"threadId": threadID,
		"ref":      "local:" + threadID,
		"turnId":   "turn_tool",
		"item": appwire.ThreadItem{
			Type:          "commandExecution",
			ID:            "tool_stream",
			CallID:        "call_stream",
			TurnID:        "turn_tool",
			ToolName:      "read_file",
			ArgumentsJSON: `{"file_path":"/tmp/tmux-tool.txt"}`,
			Status:        appwire.TurnStatusInProgress,
		},
	})
}

func (h *tuiE2EHub) BroadcastToolOutputDelta(threadID, delta string) {
	h.app.Broadcast(threadID, appwire.NotifyToolOutputDelta, map[string]any{
		"threadId": threadID,
		"ref":      "local:" + threadID,
		"turnId":   "turn_tool",
		"itemId":   "tool_stream",
		"delta":    delta,
	})
}

func (h *tuiE2EHub) BroadcastToolCompleted(threadID string) {
	h.app.Broadcast(threadID, appwire.NotifyItemCompleted, map[string]any{
		"threadId": threadID,
		"ref":      "local:" + threadID,
		"turnId":   "turn_tool",
		"item": appwire.ThreadItem{
			Type:          "commandExecution",
			ID:            "tool_stream",
			CallID:        "call_stream",
			TurnID:        "turn_tool",
			ToolName:      "read_file",
			ArgumentsJSON: `{"file_path":"/tmp/tmux-tool.txt"}`,
			Output:        "tmux tool output\n",
			Status:        "completed",
		},
	})
}

func (h *tuiE2EHub) AppendAssistantFinal(threadID, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[threadID]; s != nil {
		s.Turns = append(s.Turns, appwire.Turn{
			ID:     "turn_stream",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{
				{Type: "agentMessage", ID: "agent_stream", TurnID: "turn_stream", Text: text, Status: "completed"},
			},
		})
	}
}

func (h *tuiE2EHub) AppendToolFinal(threadID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[threadID]; s != nil {
		s.Turns = append(s.Turns, appwire.Turn{
			ID:     "turn_tool",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{
				{
					Type:          "commandExecution",
					ID:            "tool_stream",
					CallID:        "call_stream",
					TurnID:        "turn_tool",
					ToolName:      "read_file",
					ArgumentsJSON: `{"file_path":"/tmp/tmux-tool.txt"}`,
					Output:        "tmux tool output\n",
					Status:        "completed",
				},
			},
		})
	}
}

func (h *tuiE2EHub) addSession(s *tuiE2ESession) {
	h.sessions[s.ID] = s
	h.order = append(h.order, s.ID)
}

func (h *tuiE2EHub) handleThreadList(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.treeGets++
	out := appwire.ThreadListResponse{}
	for _, id := range h.order {
		if s := h.sessions[id]; s != nil {
			out.Data = append(out.Data, h.threadFromSessionLocked(s))
		}
	}
	return out, nil
}

func (h *tuiE2EHub) handleThreadRead(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	id := threadIDFromParams(params.Ref, params.ThreadID)
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[id]
	if s == nil {
		return appwire.ThreadReadResponse{}, appwire.Unavailable("thread not found: " + id)
	}
	appserver.Subscribe(ctx, id)
	return appwire.ThreadReadResponse{Thread: h.threadFromSessionLocked(s)}, nil
}

func (h *tuiE2EHub) handleModelList(_ context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
	h.mu.Lock()
	authRequired := h.authRequired
	h.mu.Unlock()
	if params.Harness == "codex-local" {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "codex-local", Model: "gpt-5.3-codex"}}}, nil
	}
	if authRequired {
		return appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}},
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Title:    "Login required",
				Message:  "OpenAI login required",
				Hint:     "run /auth openai",
			}},
		}, nil
	}
	return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
}

func (h *tuiE2EHub) handleAuthStatus(_ context.Context, params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
	h.recordAuthCall("status", params.Provider)
	return appwire.AuthStatusResponse{Provider: "openai", Supported: true, ActiveSource: "signed-out"}, nil
}

func (h *tuiE2EHub) handleAuthLoginStart(_ context.Context, params appwire.AuthLoginStartParams) (appwire.AuthLoginStartResponse, error) {
	h.recordAuthCall("login-start", params.Provider)
	return appwire.AuthLoginStartResponse{
		Provider: "openai",
		FlowID:   "flow-1",
		URL:      "https://auth.example/authorize",
	}, nil
}

func (h *tuiE2EHub) handleAuthLoginComplete(_ context.Context, params appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
	h.recordAuthCall("login-complete", params.Provider)
	h.mu.Lock()
	h.authCompletions = append(h.authCompletions, params)
	h.mu.Unlock()
	return appwire.AuthLoginCompleteResponse{
		Status: appwire.AuthStatusResponse{
			Provider:     "openai",
			Supported:    true,
			SignedIn:     true,
			ActiveSource: "oauth",
			Email:        "tmux@example.com",
		},
	}, nil
}

func (h *tuiE2EHub) handleAuthLogout(_ context.Context, params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
	h.recordAuthCall("logout", params.Provider)
	return appwire.AuthLogoutResponse{
		Removed: true,
		Status:  appwire.AuthStatusResponse{Provider: "openai", Supported: true, ActiveSource: "signed-out"},
	}, nil
}

func (h *tuiE2EHub) handleThreadStart(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failSpawn {
		return appwire.ThreadStartResponse{}, errors.New("spawn failed")
	}
	h.spawnCount++
	id := fmt.Sprintf("02SPAWN%d", h.spawnCount)
	h.spawns = append(h.spawns, params)
	model := params.Model
	if model == "" {
		model = "gpt-5"
	}
	s := &tuiE2ESession{
		ID:           id,
		Title:        fmt.Sprintf("spawned session %d", h.spawnCount),
		State:        appwire.ThreadStatusIdle,
		Project:      "serf",
		WorkingDir:   params.CWD,
		Model:        model,
		Live:         true,
		Capabilities: fullTUIE2ECapabilities(),
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{
				{Type: "agentMessage", ID: "spawn-agent-1", TurnID: "turn_1", Text: "spawn transcript ready", Status: "completed"},
			},
		}},
	}
	h.addSession(s)
	return appwire.ThreadStartResponse{Thread: h.threadFromSessionLocked(s)}, nil
}

func (h *tuiE2EHub) handleThreadResume(_ context.Context, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resumes = append(h.resumes, params)
	s := &tuiE2ESession{
		ID:           "02RESUME",
		Title:        "resumed maintenance",
		State:        appwire.ThreadStatusIdle,
		Project:      "serf",
		WorkingDir:   tuiE2EProjectDir,
		Model:        "gpt-5",
		Live:         true,
		Capabilities: fullTUIE2ECapabilities(),
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{
				{Type: "agentMessage", ID: "resume-agent-1", TurnID: "turn_1", Text: "resume transcript ready", Status: "completed"},
			},
		}},
	}
	h.addSession(s)
	return appwire.ThreadResumeResponse{Thread: h.threadFromSessionLocked(s)}, nil
}

func (h *tuiE2EHub) handleTurnStart(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failSend {
		return appwire.TurnStartResponse{}, errors.New("send failed")
	}
	h.sends = append(h.sends, testInputText(params.Input))
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_sent", Status: appwire.TurnStatusInProgress}}, nil
}

func (h *tuiE2EHub) handleTurnSteer(_ context.Context, params appwire.TurnSteerParams) (appwire.EmptyResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.steers = append(h.steers, params)
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleTurnQueue(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queues = append(h.queues, params)
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleTurnDrainAsSteer(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.EmptyResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.drains = append(h.drains, params)
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleTasksList(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	h.mu.Lock()
	fail := h.failTasks
	h.mu.Unlock()
	if fail {
		return appwire.TaskListResponse{}, errors.New("tasks failed")
	}
	return appwire.TaskListResponse{Data: []task.Task{{ID: 1, Type: task.TaskTypeImplement, Description: "wire tui e2e", Status: task.TaskInProgress}}}, nil
}

func (h *tuiE2EHub) handleThreadTranscriptList(_ context.Context, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
	id := threadIDFromParams(params.Ref, "")
	if id != "01LIVE" {
		return appwire.ThreadTranscriptListResponse{}, appwire.Unavailable("thread not found: " + id)
	}
	return appwire.ThreadTranscriptListResponse{Data: []appwire.ThreadTranscriptTarget{
		{Ref: "local:01LIVE", ThreadID: "01LIVE", Title: "main session (live)", Kind: "main", Status: appwire.ThreadStatusActive, Source: "local"},
		{Ref: "local:01SUB", ThreadID: "01SUB", Title: "subagent inspect", Kind: "subagent", Status: appwire.ThreadStatusNotLoaded, Source: "local", TurnsUsed: 1},
	}}, nil
}

func (h *tuiE2EHub) handleTurnInterrupt(context.Context, appwire.TurnInterruptParams) (appwire.EmptyResponse, error) {
	h.recordAction("interrupt")
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleThreadCompactStart(context.Context, appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
	h.recordAction("compact")
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleThreadModelSet(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
	h.mu.Lock()
	h.models = append(h.models, params.Model)
	h.mu.Unlock()
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleThreadClear(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	h.recordAction("clear")
	h.mu.Lock()
	defer h.mu.Unlock()
	id := "02CLEAR"
	s := &tuiE2ESession{
		ID:           id,
		Title:        "cleared session",
		State:        appwire.ThreadStatusIdle,
		Project:      "serf",
		WorkingDir:   tuiE2EProjectDir,
		Model:        "gpt-5",
		Live:         true,
		Capabilities: fullTUIE2ECapabilities(),
	}
	h.addSession(s)
	thread := h.threadFromSessionLocked(s)
	return appwire.ThreadClearResponse{Thread: thread, Ref: thread.Serf.Ref}, nil
}

func (h *tuiE2EHub) handleThreadFork(_ context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failFork {
		h.forks = append(h.forks, params)
		return appwire.ThreadForkResponse{}, errors.New("fork failed")
	}
	id := "02FORK"
	h.forks = append(h.forks, params)
	s := &tuiE2ESession{
		ID:           id,
		Title:        "fork child",
		State:        appwire.ThreadStatusIdle,
		Project:      "serf",
		WorkingDir:   tuiE2EProjectDir,
		Model:        "gpt-5",
		Live:         true,
		Capabilities: fullTUIE2ECapabilities(),
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{
				{Type: "userMessage", ID: "fork-user-1", TurnID: "turn_1", Text: params.EditedInput},
				{Type: "agentMessage", ID: "fork-agent-1", TurnID: "turn_1", Text: "fork answer", Status: "completed"},
			},
		}},
	}
	h.addSession(s)
	return appwire.ThreadForkResponse{Thread: h.threadFromSessionLocked(s)}, nil
}

func (h *tuiE2EHub) threadFromSessionLocked(s *tuiE2ESession) appwire.Thread {
	status := s.State
	if status == "" {
		status = appwire.ThreadStatusIdle
	}
	return appwire.Thread{
		ID:            s.ID,
		SessionID:     s.ID,
		Preview:       s.Title,
		Name:          s.Title,
		ModelProvider: s.Model,
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
		CWD:           s.WorkingDir,
		Source:        "local",
		Status:        appwire.ThreadStatus{Type: status},
		Turns:         append([]appwire.Turn(nil), s.Turns...),
		Serf: appwire.SerfThread{
			Ref:             appwire.Ref{SourceID: "local", ThreadID: s.ID}.String(),
			ParentRef:       s.ParentRef,
			Kind:            s.Kind,
			Profile:         "default",
			ContextPressure: s.ContextPressure,
			Capabilities:    s.Capabilities,
		},
	}
}

func threadIDFromParams(rawRef, threadID string) string {
	if rawRef != "" {
		ref, err := appwire.ParseRef(rawRef)
		if err == nil {
			return ref.ThreadID
		}
	}
	return threadID
}

func (h *tuiE2EHub) recordAction(action string) {
	h.mu.Lock()
	h.actions[action]++
	h.mu.Unlock()
}

func (h *tuiE2EHub) recordAuthCall(action, provider string) {
	h.mu.Lock()
	h.authCalls = append(h.authCalls, action+":"+provider)
	h.mu.Unlock()
}

func (h *tuiE2EHub) SetSessionCapabilities(id string, caps appwire.ThreadCapabilities) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[id]; s != nil {
		s.Capabilities = caps
	}
}

func (h *tuiE2EHub) SetSessionContextPressure(id string, pressure float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[id]; s != nil {
		s.ContextPressure = pressure
	}
}

func (h *tuiE2EHub) SetSessionState(id string, state string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[id]; s != nil {
		s.State = state
	}
}

func (h *tuiE2EHub) SetFailTasks(fail bool) {
	h.mu.Lock()
	h.failTasks = fail
	h.mu.Unlock()
}

func (h *tuiE2EHub) SetFailSend(fail bool) {
	h.mu.Lock()
	h.failSend = fail
	h.mu.Unlock()
}

func (h *tuiE2EHub) SetFailSpawn(fail bool) {
	h.mu.Lock()
	h.failSpawn = fail
	h.mu.Unlock()
}

func (h *tuiE2EHub) SetFailFork(fail bool) {
	h.mu.Lock()
	h.failFork = fail
	h.mu.Unlock()
}

func (h *tuiE2EHub) SetAuthRequiredModels(required bool) {
	h.mu.Lock()
	h.authRequired = required
	h.mu.Unlock()
}

func (h *tuiE2EHub) Sends() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.sends...)
}

func (h *tuiE2EHub) Models() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.models...)
}

func (h *tuiE2EHub) Forks() []appwire.ThreadForkParams {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]appwire.ThreadForkParams(nil), h.forks...)
}

func (h *tuiE2EHub) ActionCount(action string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.actions[action]
}

func (h *tuiE2EHub) WaitForTreeRequests(t *testing.T, count int) int {
	t.Helper()
	var got int
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		got = h.treeGets
		return got >= count
	}, fmt.Sprintf("%d tree requests", count))
	return got
}

func (h *tuiE2EHub) WaitForSpawns(t *testing.T, count int) []appwire.ThreadStartParams {
	t.Helper()
	var out []appwire.ThreadStartParams
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		out = append([]appwire.ThreadStartParams(nil), h.spawns...)
		return len(out) >= count
	}, fmt.Sprintf("%d spawn requests", count))
	return out
}

func (h *tuiE2EHub) WaitForSends(t *testing.T, count int) []string {
	t.Helper()
	var out []string
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		out = append([]string(nil), h.sends...)
		return len(out) >= count
	}, fmt.Sprintf("%d send requests", count))
	return out
}

func (h *tuiE2EHub) WaitForModels(t *testing.T, count int) []string {
	t.Helper()
	var out []string
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		out = append([]string(nil), h.models...)
		return len(out) >= count
	}, fmt.Sprintf("%d model requests", count))
	return out
}

func (h *tuiE2EHub) WaitForForks(t *testing.T, count int) []appwire.ThreadForkParams {
	t.Helper()
	var out []appwire.ThreadForkParams
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		out = append([]appwire.ThreadForkParams(nil), h.forks...)
		return len(out) >= count
	}, fmt.Sprintf("%d fork requests", count))
	return out
}

func (h *tuiE2EHub) WaitForResumes(t *testing.T, count int) []appwire.ThreadResumeParams {
	t.Helper()
	var out []appwire.ThreadResumeParams
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		out = append([]appwire.ThreadResumeParams(nil), h.resumes...)
		return len(out) >= count
	}, fmt.Sprintf("%d resume requests", count))
	return out
}

func (h *tuiE2EHub) WaitForSteers(t *testing.T, count int) []appwire.TurnSteerParams {
	t.Helper()
	var out []appwire.TurnSteerParams
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		out = append([]appwire.TurnSteerParams(nil), h.steers...)
		return len(out) >= count
	}, fmt.Sprintf("%d steer requests", count))
	return out
}

func (h *tuiE2EHub) WaitForQueues(t *testing.T, count int) []appwire.TurnQueueParams {
	t.Helper()
	var out []appwire.TurnQueueParams
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		out = append([]appwire.TurnQueueParams(nil), h.queues...)
		return len(out) >= count
	}, fmt.Sprintf("%d queue requests", count))
	return out
}

func (h *tuiE2EHub) WaitForDrains(t *testing.T, count int) []appwire.TurnDrainAsSteerParams {
	t.Helper()
	var out []appwire.TurnDrainAsSteerParams
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		out = append([]appwire.TurnDrainAsSteerParams(nil), h.drains...)
		return len(out) >= count
	}, fmt.Sprintf("%d drain-as-steer requests", count))
	return out
}

func (h *tuiE2EHub) WaitForAuthCalls(t *testing.T, count int) []string {
	t.Helper()
	var out []string
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		out = append([]string(nil), h.authCalls...)
		return len(out) >= count
	}, fmt.Sprintf("%d auth calls", count))
	return out
}

func (h *tuiE2EHub) WaitForAuthCompletions(t *testing.T, count int) []appwire.AuthLoginCompleteParams {
	t.Helper()
	var out []appwire.AuthLoginCompleteParams
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		out = append([]appwire.AuthLoginCompleteParams(nil), h.authCompletions...)
		return len(out) >= count
	}, fmt.Sprintf("%d auth completions", count))
	return out
}

func (h *tuiE2EHub) WaitForActionCount(t *testing.T, action string, count int) int {
	t.Helper()
	var got int
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		got = h.actions[action]
		return got >= count
	}, fmt.Sprintf("%d %s actions", count, action))
	return got
}

func (h *tuiE2EHub) waitFor(t *testing.T, pred func() bool, desc string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}
