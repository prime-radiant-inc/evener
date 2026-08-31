package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/internal/e2ecap"

	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appserver"
)

var tuiE2EProjectDir = canonicalTUIE2EProjectDir()

func canonicalTUIE2EProjectDir() string {
	tmp := "/tmp"
	if canonical, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = canonical
	}
	return filepath.Join(tmp, "evener-e2e", "evener")
}

// Generous backstop, not a target: WaitFor returns the instant the expected text
// appears, so a large timeout costs nothing on the happy path. It is sized to
// tolerate the real-time rendering stalls these tmux+TUI tests see when the full
// suite runs concurrently and CPU is oversubscribed.
const tuiE2EWaitTimeout = 60 * time.Second

// tuiE2EPollInterval is how often WaitFor re-checks the rendered pane. Small
// so render-driven round-trips aren't rounded up; capture-pane is ~2.6ms so
// this does not oversubscribe CPU under the 6-way session cap.
var tuiE2EPollInterval = 10 * time.Millisecond
var tuiE2EDeadCheckInterval = 100 * time.Millisecond

// tmuxSessionCounter makes tmux session names unique even when parallel tests
// start within the same nanosecond.
var tmuxSessionCounter atomic.Int64

func uniqueTmuxSessionName() string {
	return fmt.Sprintf("evener-e2e-%d-%d", time.Now().UnixNano(), tmuxSessionCounter.Add(1))
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
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	// Ended sessions fold by default: awaited as an absence so the check
	// cannot race a partial repaint (see WaitForWithout).
	app.WaitForWithout([]string{"ended maintenance"},
		"EVENER LIVE", hub.URL(), "Launch New Session", "▾", "▍", "evener", "live task", "ops task", "1 recent")
	app.SendKeys("/")
	app.TypeText("ops")
	app.WaitForWithout([]string{"live task"}, "Command palette", "Filter: ops", "ops task")
	app.SendKeys("Escape")
	app.WaitFor("EVENER LIVE", "live task", "ops task")

	initialTreeRequests := hub.WaitForTreeRequests(t, 1)
	app.SendKeys("r")
	hub.WaitForTreeRequests(t, initialTreeRequests+1)

	// The cursor starts on the evener project row; Enter collapses the group
	// and folds its child sessions out of the tree.
	app.WaitFor("EVENER LIVE", "Project:  evener", "Action:   enter toggles project")
	app.SendKeys("Enter")
	app.WaitForWithout([]string{"live task", "ended maintenance"}, "EVENER LIVE", "▸ ● evener")
	app.SendKeys("Right")
	app.WaitFor("EVENER LIVE", "live task", "1 recent")
	// Down to the ended-sessions toggle, Enter to reveal the ended session.
	app.SendKeys("Down", "Down")
	app.WaitFor("EVENER LIVE", "Action:   enter toggles ended sessions")
	app.SendKeys("Enter")
	app.WaitFor("EVENER LIVE", "live task", "ended maintenance")
	app.SendKeys("/")
	app.TypeText("ended")
	app.WaitForWithout([]string{"live task"}, "Command palette", "Filter: ended", "ended maintenance")
	app.SendKeys("Escape")
	app.WaitFor("EVENER LIVE", "live task", "ended maintenance")

	app.SendKeys("n")
	// The form's model default arrives asynchronously (fetchHubSpawnOptions
	// round-trips to the hub); submitting before hubSpawnOptionsMsg lands sees
	// an empty spawnModel and renders "choose a model before starting" — the
	// startup race that flaked TestTUITmuxE2E_APIErrorsRenderInPlace in CI
	// (issue #656). Waiting for the populated Model field pins the
	// happens-before edge the way that test and the harness-cycling spawn
	// test do.
	app.WaitFor("evener / new session", "Dir:      "+tuiE2EProjectDir, "Prompt (optional):", "Model:    openai/gpt-5")
	app.SendKeys("Tab", "Tab", "Tab", "C-u")
	app.TypeText("/tmp/evener-e2e/custom")
	app.WaitFor("Dir:      /tmp/evener-e2e/custom")
	app.SendKeys("Enter", "Tab")
	app.TypeLine("spawn from dashboard")
	app.WaitFor("evener / session / spawned session 1")
	spawns := hub.WaitForSpawns(t, 1)
	if spawns[0].CWD != "/tmp/evener-e2e/custom" {
		t.Fatalf("dashboard spawn cwd=%q, want /tmp/evener-e2e/custom", spawns[0].CWD)
	}
	if testInputText(spawns[0].Input) != "spawn from dashboard" {
		t.Fatalf("dashboard spawn prompt=%q, want spawn from dashboard", testInputText(spawns[0].Input))
	}
	if spawns[0].ModelProvider != "" || spawns[0].Model != "openai/gpt-5" {
		t.Fatalf("dashboard spawn model=%s/%s, want openai/gpt-5", spawns[0].ModelProvider, spawns[0].Model)
	}

	app.SendKeys("C-o")
	app.WaitFor("EVENER LIVE", "live task")
	app.SendKeys("n")
	app.WaitFor("evener / new session", "Dir:      "+tuiE2EProjectDir, "Plugins:  2/2 enabled", "Prompt (optional):")
	app.SendKeys("Tab", "Tab", "Tab", "Enter", "Enter")
	app.WaitFor("Plugins for this session", "[x] alpha", "[x] beta")
	app.SendKeys("Space", "Enter")
	app.WaitFor("Plugins:  1/2 enabled")
	app.SendKeys("Tab")
	app.TypeLine("spawn from project")
	app.WaitFor("evener / session / spawned session 2")
	spawns = hub.WaitForSpawns(t, 2)
	if spawns[1].CWD != tuiE2EProjectDir {
		t.Fatalf("project spawn cwd=%q, want %q", spawns[1].CWD, tuiE2EProjectDir)
	}
	if testInputText(spawns[1].Input) != "spawn from project" {
		t.Fatalf("project spawn prompt=%q, want spawn from project", testInputText(spawns[1].Input))
	}
	if spawns[1].LaunchOverrides == nil || spawns[1].LaunchOverrides.EnabledPlugins == nil || !reflect.DeepEqual(*spawns[1].LaunchOverrides.EnabledPlugins, []string{"beta"}) {
		t.Fatalf("project spawn enabled plugins=%+v, want [beta]", spawns[1].LaunchOverrides)
	}
	if spawns[1].ModelProvider != "" || spawns[1].Model != "openai/gpt-5" {
		t.Fatalf("project spawn model=%s/%s, want openai/gpt-5", spawns[1].ModelProvider, spawns[1].Model)
	}

	app.SendKeys("C-o")
	app.WaitFor("EVENER LIVE", "live task")
	app.SendKeys("q")
	app.WaitForExit()
}

// A pty delivers rapid keystrokes as one read burst under CPU contention:
// tmux coalesces separate send-keys writes when the pane's reader lags, and
// bubbletea reports every printable rune of one read as a single KeyMsg.
// That is how a loaded CI runner turned SendKeys("/")+TypeText("ops") into
// one batched message the dashboard dropped on the floor, and k/k/f into
// "kk" typed at the composer (kata fazd). One literal send-keys call IS that
// burst, deterministically — no load required — so this pins that a
// coalesced "/ops" behaves exactly like "/", "o", "p", "s" typed one at a
// time.
func TestTUITmuxE2E_BurstTypedKeysApplyIndividually(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	app.WaitForWithout([]string{"ended maintenance"}, "EVENER LIVE", "live task", "ops task")
	app.TypeText("/ops")
	app.WaitForWithout([]string{"live task"}, "Command palette", "Filter: ops", "ops task")
	app.SendKeys("Escape")
	app.WaitFor("EVENER LIVE", "live task", "ops task")
}

func TestTUITmuxE2E_AppShellPreservesLayoutAcrossWidths(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	screen := app.WaitFor("EVENER LIVE", "live task", "dashboard")
	requirePaneOrder(t, screen, "EVENER LIVE", "live task", "dashboard")

	app.SendKeys("/")
	screen = app.WaitFor("Command palette", "dashboard")
	requirePaneOrder(t, screen, "EVENER LIVE", "Command palette", "dashboard")

	app.Resize(60, 30)
	screen = app.WaitFor("Command palette", "dashboard")
	requirePaneOrder(t, screen, "EVENER LIVE", "Command palette", "dashboard")

	app.SendKeys("C-o")
	screen = app.WaitFor("EVENER LIVE", "live task", "dashboard")
	requirePaneOrder(t, screen, "EVENER LIVE", "live task", "dashboard")

	app.SendKeys("n")
	screen = app.WaitFor("evener / new session", "Prompt (optional):", "ctrl+o: dashboard")
	requirePaneOrder(t, screen, "evener / new session", "Prompt (optional):", "ctrl+o: dashboard")

	app.SendKeys("C-o")
	screen = app.WaitFor("EVENER LIVE", "live task", "dashboard")
	requirePaneOrder(t, screen, "EVENER LIVE", "live task", "dashboard")
}

func TestTUITmuxE2E_DashboardNarrowWideStates(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetSessionTitle("01LIVE", "live dashboard task with a title long enough to truncate cleanly")
	registerTUIE2EHubCleanup(t, hub)

	wide := startTUITmuxSized(t, bin, hub, 140, 40)
	defer wide.Close()
	wideScreen := wide.WaitFor("EVENER LIVE", "details", "Project:  evener", "Live:     1", "Dir:      "+tuiE2EProjectDir)
	if strings.Contains(wideScreen, "Prompt (optional):") || strings.Contains(wideScreen, "enter: send") {
		t.Fatalf("wide dashboard rendered a composer:\n%s", wideScreen)
	}
	t.Logf("wide dashboard capture:\n%s", wideScreen)
	wide.SendKeys("q")
	wide.WaitForExit()

	narrow := startTUITmuxSized(t, bin, hub, 60, 30)
	defer narrow.Close()
	// The narrow dashboard collapses to a single column: the session list
	// still renders (with the long title hard-truncated to the pane width)
	// but the wide-only details drawer must not appear.
	narrowScreen := narrow.WaitFor("EVENER LIVE", "live dashboard task with a title long")
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
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmuxSized(t, bin, hub, 124, 18)
	defer app.Close()

	app.WaitFor("EVENER LIVE", "select")
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
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.EndDashboardSessions()
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	// Ended sessions fold by default and composer content belongs to session
	// view, so both are awaited as absences on the same frame as the positives
	// (WaitForWithout; mid-repaint shape from #694).
	screen := app.WaitForWithout([]string{"ended maintenance", "ops task", "Prompt (optional):", "enter: send"},
		"EVENER LIVE", "0 live", "2 recent", "1 recent", "filter")
	t.Logf("recent-only dashboard capture:\n%s", screen)

	app.SendKeys("Down", "Enter")
	app.WaitFor("EVENER LIVE", "ended maintenance")
	app.SendKeys("q")
	app.WaitForExit()
}

func TestTUITmuxE2E_ProjectHistoryReadOnlyAndResume(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	app.WaitFor("EVENER LIVE", "live task")
	screen := app.WaitFor("EVENER LIVE", "live task", "1 recent")
	for _, unwanted := range []string{"enter: send", "Prompt (optional):"} {
		if strings.Contains(screen, unwanted) {
			t.Fatalf("dashboard rendered composer/spawn text %q:\n%s", unwanted, screen)
		}
	}

	app.SendKeys("Down", "Down", "Enter", "Down", "Enter")
	screen = app.WaitFor("ended maintenance", "src evener", "send")
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
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetHarnesses([]appwire.HarnessDescriptor{
		{ID: "evener", Label: "evener", Kind: "evener"},
		{ID: "codex-local", Label: "codex-local", Kind: "codex"},
	})
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	app.WaitFor("EVENER LIVE", "live task")
	app.SendKeys("n")
	app.WaitFor("Harness:  evener", "Model:    openai/gpt-5")
	app.SendKeys("Tab", "Enter")
	app.WaitFor("Harness:  codex-local", "Model:    (harness default)")
	app.SendKeys("Tab", "Enter")
	// The picker groups by provider header ("CODEX-LOCAL") and shows the
	// prettified bare display name ("Gpt 5.3 Codex"), not a "provider/model"
	// string (Task 11).
	app.WaitFor("Select codex-local model", "CODEX-LOCAL", "Gpt 5.3 Codex")
	app.SendKeys("Enter")
	app.WaitFor("Harness:  codex-local", "Model:    codex-local/gpt-5.3-codex")
	app.SendKeys("Tab", "Enter")
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
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	// The /help output (all session slash commands + the browse keybindings)
	// is taller than the default 40-row pane's transcript viewport, which pins
	// to the bottom and scrolls the "Available commands:" header off the top.
	// Give this pane extra rows so the whole help block — header through the
	// final key line — fits on screen at once, which is what the WaitFor below
	// asserts (all three substrings visible simultaneously).
	app := startTUITmuxSized(t, bin, hub, 140, 52)
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("evener / session / live task", "initial question", "initial answer", "tool output from e2e")

	app.TypeLine("hello from tmux")
	app.WaitFor("hello from tmux")
	if sends := hub.WaitForSends(t, 1); sends[0] != "hello from tmux" {
		t.Fatalf("send text=%q, want hello from tmux", sends[0])
	}

	// /fork drops into browse mode with the fork prompt and footer hint.
	app.TypeLine("/fork")
	app.WaitFor("Select a user message, then press f to fork.", "f: fork selected user message")
	// The browse→compose transition must be synced on the DISAPPEARANCE of
	// the browse action bar, not on "enter send": the browse footer keeps the
	// composer panel — and with it the compose-mode "enter send" hint — on
	// screen (hub_session_view.go's scrollMode branch), so a plain
	// WaitFor("enter send") returns while the "i" can still be sitting
	// unread in the pty. If "/help" is then written before the TUI's input
	// reader consumes "i", tmux coalesces the two writes into one pty read,
	// bubbletea reports "i/help" as a single KeyMsg, and browse mode drops
	// it into the composer as draft text (kata fazd; fall-through pinned by
	// kata 7hh0) — the TUI never leaves browse mode and /help never runs,
	// which is the issue #540 flake. A frame showing "enter send" WITHOUT
	// the browse action bar can only have been rendered from the post-"i"
	// model, so this wait is a real happens-before edge: the "i" is provably
	// consumed before "/help" is written.
	app.SendKeys("i")
	app.WaitForWithout([]string{"esc/i/q: compose"}, "enter send")

	// /help lists the slash commands and the browse keybindings.
	app.TypeLine("/help")
	app.WaitFor("Available commands:", "/dashboard Go to live dashboard", "/theme")

	// /wat names no built-in command, so it forwards to the session (design
	// §10) instead of dead-ending with "Unknown command" — the plugin-command
	// catalog isn't the TUI's to know, so an unrecognized word is always
	// worth trying against the session's own expander.
	app.TypeLine("/wat")
	if sends := hub.WaitForSends(t, 2); sends[1] != "/wat" {
		t.Fatalf("send text=%q, want the forwarded literal %q (no trailing space for an arg-less command)", sends[1], "/wat")
	}

	// /project returns to the dashboard focused on this session's project.
	app.TypeLine("/project")
	app.WaitFor("EVENER LIVE", "Project:  evener", "live task")
	// Down to the ended-sessions toggle, Enter to reveal the ended session.
	app.SendKeys("Down", "Down")
	app.WaitFor("EVENER LIVE", "Action:   enter toggles ended sessions")
	app.SendKeys("Enter")
	app.WaitFor("EVENER LIVE", "ended maintenance")
	app.SendKeys("/")
	app.WaitFor("Command palette", "live task", "ended maintenance")
	app.TypeText("ended")
	app.WaitFor("Filter: ended", "ended maintenance")
	app.SendKeys("Escape")
	openLiveSession(t, app)
	app.WaitFor("evener / session / live task", "enter send")

	app.TypeLine("/auth openai-codex")
	app.WaitFor("openai-codex auth: not configured")

	app.TypeLine("/login openai-codex")
	app.WaitFor("Sign-in URL for openai-codex:", "https://auth.example/authorize", "Paste the full redirect URL")
	app.TypeLine("http://localhost:1455/auth/callback?code=abc&state=flow")
	app.WaitFor("Sign-in complete for openai-codex. openai-codex auth: OAuth (tmux@example.com)")
	completions := hub.WaitForAuthCompletions(t, 1)
	if completions[0].FlowID != "flow-1" || completions[0].RedirectURL == "" {
		t.Fatalf("auth completion=%+v, want flow-1 and redirect URL", completions[0])
	}

	app.TypeLine("/logout openai-codex")
	app.WaitFor("Removed the stored credential for openai-codex.")
	authCalls := hub.WaitForAuthCalls(t, 4)
	if got := strings.Join(authCalls, ","); got != "status:openai-codex,login-start:openai-codex,login-complete:openai-codex,logout:openai-codex" {
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
	// Same sync rule as the /fork exit above: overlays do not change the
	// footer, so "enter send" stays on screen while the details panel is
	// open and cannot prove the Escape was consumed. Sync on the panel
	// content's disappearance — a frame without it comes only from the
	// post-Escape model.
	app.SendKeys("Escape")
	app.WaitForWithout([]string{"Session:  01LIVE"}, "enter send")

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
	app.WaitFor("EVENER LIVE", "Project:  evener")
	openLiveSession(t, app)
	app.WaitFor("evener / session / live task")

	app.TypeLine("/dashboard")
	app.WaitFor("EVENER LIVE", "live task")
	openLiveSession(t, app)
	app.WaitFor("evener / session / live task")

	app.TypeLine("/clear")
	app.WaitFor("evener / session / cleared session")
	if got := hub.WaitForActionCount(t, "clear", 1); got != 1 {
		t.Fatalf("clear count=%d, want 1", got)
	}
}

func TestTUITmuxE2E_BrowseAndFork(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	// Browse-mode fork: k/j move the selection cursor across rows (auto-
	// scrolling to keep it visible) so a user message can be reached and forked.
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("evener / session / live task", "initial question", "initial answer")

	app.SendKeys("Escape")
	app.WaitFor("esc/i/q: compose", "f: fork selected user message", "▶ ▍ initial answer")
	app.SendKeys("f")
	app.WaitFor("Select a user message to fork.")
	if forks := hub.Forks(); len(forks) != 0 {
		t.Fatalf("invalid fork selection should not call hub: %+v", forks)
	}
	// Same sync rule as the /fork exit in SessionCommandsAndNavigation:
	// "enter send" is visible in browse mode, so sync the transition on the
	// action bar's disappearance, not on the hint text.
	app.SendKeys("i")
	app.WaitForWithout([]string{"esc/i/q: compose"}, "enter send")
	app.SendKeys("Escape")
	app.WaitFor("esc/i/q: compose")
	// These k presses must move the browse cursor up to the user message.
	app.SendKeys("k")
	app.WaitFor("▶ ▍ initial answer")
	app.SendKeys("k")
	app.WaitFor("▶ ▍ ✓ exec")
	app.SendKeys("k")
	app.WaitFor("▶ ┃  > initial question")
	app.SendKeys("f")
	app.WaitFor("Fork draft from transcript position 1", "> initial question")

	app.SendKeys("Enter")
	app.WaitFor("evener / session / fork child")
	forks := hub.WaitForForks(t, 1)
	if forks[0].SourceTurnID != "1" {
		t.Fatalf("fork source turn=%q, want 1", forks[0].SourceTurnID)
	}
	if forks[0].EditedInput != "initial question" {
		t.Fatalf("fork edited input=%q, want initial question", forks[0].EditedInput)
	}
	app.SendKeys("C-o")
	app.WaitFor("EVENER LIVE", "live task")
}

func TestTUITmuxE2E_FailedForkPreservesDraft(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetFailFork(true)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("evener / session / live task", "initial question", "initial answer")

	app.SendKeys("Escape")
	app.WaitFor("esc/i/q: compose", "f: fork selected user message")
	// Await each selection render before the next press, the way
	// TestTUITmuxE2E_BrowseAndFork does: consecutive printable command keys
	// sent back-to-back can coalesce into one pty read on a loaded machine
	// and arrive as a single batched KeyMsg, which browse mode reads as
	// composer text rather than three commands (kata fazd — this test's CI
	// failure pane had "kk" sitting in the composer).
	app.SendKeys("k")
	app.WaitFor("▶ ▍ ✓ exec")
	app.SendKeys("k")
	app.WaitFor("▶ ┃  > initial question")
	app.SendKeys("f")
	app.WaitFor("Fork draft from transcript position 1", "> initial question")
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
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetSessionCapabilities("01LIVE", appwire.ThreadCapabilities{})
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("evener / session / live task", "initial question")

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
		"/fork  browse and fork a user message  disabled: source does not advertise fork",
		"/shutdown  stop this resumable session  disabled: source does not advertise shutdown",
	)
	// "enter send" is on screen while the palette is open (overlays do not
	// change the footer), so it cannot prove the Escape was consumed — if
	// the next text lands in the same pty read, "\x1b<text>" parses as
	// alt+<key> and the palette stays open. Sync on the palette's
	// disappearance instead.
	app.SendKeys("Escape")
	app.WaitForWithout([]string{"Command palette"}, "enter send")

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
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
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
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	openLiveSession(t, app)
	app.TypeText("/")
	app.WaitFor("Command palette", "/help", "/model")
}

func TestTUITmuxE2E_CtrlCRequiresDoublePressFromSession(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	// Use a session with no in-flight turn so the first ctrl+c exercises the
	// pure quit-arming path. (With an active turn the first ctrl+c interrupts
	// the turn first — covered by the header/composer state behavior.)
	openEndedSession(t, app)
	sendFirstCtrlCAndAssertNoQuitWarning(t, app)
	// Positive gate: session must still be alive after the settling window.
	app.WaitFor("evener / session / ended maintenance")
	app.SendKeys("C-c")
	app.WaitForExit()
}

func TestTUITmuxE2E_CtrlCRestoreMessageSurvivesAltScreenExit(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmuxAltScreen(t, bin, hub, 120, 28)
	defer app.Close()

	openEndedSession(t, app)
	sendFirstCtrlCAndAssertNoQuitWarning(t, app)
	// Positive gate: session must still be alive after the settling window.
	app.WaitFor("evener / session / ended maintenance")
	app.SendKeys("C-c")
	// evener-tui exits the alternate screen and prints the restore instructions
	// to the normal screen on its way out. The pane is kept alive past
	// evener-tui's exit (see startTUITmuxAltScreen) so tmux reliably drains that
	// trailing output instead of dropping it when freezing a just-dead pane —
	// then we poll the scrollback until the message renders rather than racing
	// a single post-exit capture.
	app.WaitForHistory("Restore this session:", "evener-tui --hub-addr "+hub.URL(), "local:01PAST")
}

func TestTUITmuxE2E_ModelPickerShowsAuthRequiredModels(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetAuthRequiredModels(true)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("evener / session / live task", "initial question")

	app.TypeLine("/model")
	// The active model (session model "gpt-5") is marked with an "(active)" tag
	// on the provider-qualified "openai/gpt-5" row, and the row (qualified ID +
	// catalog Meta tail + "(active)" + disabled reason) is long enough to
	// word-wrap inside the popup — the wrap lands right after "disabled:", so
	// "disabled:" and its reason "Login required: ..." fall on separate lines.
	// Check the disabled label and the reason as independent substring.Contains
	// wants (plus the "(active)" tag) rather than a contiguous phrase.
	app.WaitFor("Select model", "openai/gpt-5", "(active)", "disabled:", "OpenAI login required (run /auth openai)")
	app.SendKeys("Enter")
	app.WaitFor("Select model", "disabled:", "OpenAI login required (run /auth openai)")
	if models := hub.Models(); len(models) != 0 {
		t.Fatalf("auth-required model should not be selected: %+v", models)
	}
}

func TestTUITmuxE2E_SessionHeaderStatusAndComposerStates(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetSessionState("01LIVE", appwire.ThreadStatusActive)
	hub.SetSessionContextPressure("01LIVE", 0.66)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor(
		"evener / session / live task",
		"● WORKING",
		"src evener",
		"model gpt-5",
		"dir "+tuiE2EProjectDir,
		"2 turns",
		"ctx 66%",
		"status: hub connected",
		"queue: ready",
		"busy: turn_active",
	)

	app.TypeLine("/auth openai-codex")
	app.WaitFor("openai-codex auth: not configured", "auth: openai-codex none")

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
	app.WaitFor("EVENER LIVE")
	openLiveSession(t, app)
	app.WaitFor("● IDLE", "send: ready")

	hub.SetFailSend(true)
	app.TypeLine("send failure draft")
	app.WaitFor("Send failed.", "error: Send failed: appwire turn/start: send failed", "cause appwire turn/start: send failed", "> send failure draft")
	app.SendKeys("C-x")
	app.WaitFor("enter send")

	app.SendKeys("C-o")
	app.WaitFor("EVENER LIVE")
	app.SendKeys("Up", "Up", "Up", "Up", "Up", "Up")
	app.WaitFor("Launch New Session", "hub default")
	app.SendKeys("Down", "Down", "Down")
	app.WaitFor("Action:   enter toggles ended sessions")
	app.SendKeys("Enter")
	app.WaitFor("ended maintenance")
	app.SendKeys("Down")
	app.WaitFor("Session:  01PAST")
	app.SendKeys("Enter")
	screen = app.WaitFor("evener / session / ended maintenance", "● NOT LOADED", "send: ready")
	if strings.Contains(screen, "read-only") || strings.Contains(screen, "source does not support send") {
		t.Fatalf("ended resumable session should not render read-only:\n%s", screen)
	}
}

func TestTUITmuxE2E_HubStreamingAssistantDeltaBeforeRefresh(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("evener / session / live task", "initial answer")
	app.TypeLine("stream please")
	hub.WaitForSends(t, 1)

	hub.BroadcastAgentDelta("01LIVE", "partial live answer")
	app.WaitFor("partial live answer")

	hub.AppendAssistantFinal("01LIVE", "partial live answer done")
	app.SendKeys("C-o")
	app.WaitFor("EVENER LIVE", "live task")
	openLiveSession(t, app)
	app.WaitFor("partial live answer done")
}

func TestTUITmuxE2E_HubStreamingToolGroupBeforeRefresh(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("evener / session / live task", "initial answer")

	hub.BroadcastToolStarted("01LIVE")
	hub.BroadcastToolOutputDelta("01LIVE", "tmux tool output\n")
	hub.BroadcastToolCompleted("01LIVE")
	app.WaitFor("read  /tmp/tmux-tool.txt", "tmux tool output")

	hub.AppendToolFinal("01LIVE")
	app.SendKeys("C-o")
	app.WaitFor("EVENER LIVE", "live task")
	openLiveSession(t, app)
	app.WaitFor("read  /tmp/tmux-tool.txt", "tmux tool output")
}

func TestTUITmuxE2E_APIErrorsRenderInPlace(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmux(t, bin, hub)
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("evener / session / live task", "initial question")

	// Backend failures surface as in-place notice overlays carrying the
	// source and cause; each is dismissed (ctrl+x) before the next action.
	hub.SetFailTasks(true)
	app.TypeLine("/tasks")
	app.WaitFor("Tasks failed.", "source evener", "cause appwire evener/tasks/list: tasks failed")
	app.SendKeys("C-x")
	app.WaitFor("enter send")

	hub.SetFailSend(true)
	app.TypeLine("send should fail")
	app.WaitFor("Send failed.", "cause appwire turn/start: send failed", "> send should fail")
	app.SendKeys("C-x")
	app.WaitFor("enter send")

	app.SendKeys("C-o")
	app.WaitFor("EVENER LIVE", "live task")
	hub.SetFailSpawn(true)
	app.SendKeys("n")
	// The form's model default arrives asynchronously (fetchHubSpawnOptions
	// round-trips to the hub); submitting before hubSpawnOptionsMsg lands sees
	// an empty spawnModel and renders "choose a model before starting" instead
	// of the scripted spawn failure — a startup race a loaded CI runner loses.
	// Waiting for the populated Model field pins the happens-before edge the
	// same way the harness-cycling spawn test does.
	app.WaitFor("evener / new session", "Prompt (optional):", "Model:    openai/gpt-5")
	app.TypeLine("spawn should fail")
	app.WaitFor("Hub session start failed.", "cause appwire thread/start: spawn failed", "> spawn should fail")
}

// TestTUITmuxE2E_CaptureStableDuringStream exercises CaptureStable under the
// exact condition kata nxq6 reported the pane going blank above the composer:
// a rapid burst of hub notifications re-rendering the pane in a tight loop,
// no keypresses. Every capture taken during the burst must be a complete
// frame — the session breadcrumb from the top of the pane and the composer's
// key hints from the bottom, never one without the other — which is the
// property a lone Capture() cannot promise.
//
// Stability alone cannot promise it either. Under CI load this test flaked as
// a "stable" torn frame: the pane never settles during the flood, and two
// consecutive captures agreed on a mid-write grid — 50 rows of streamed text,
// no breadcrumb, no composer — for longer than the poll interval. The fix is
// to require completeness evidence (all anchors present, including the
// last-written footer) before accepting stability, which is what
// CaptureStable's anchor arguments do.
func TestTUITmuxE2E_CaptureStableDuringStream(t *testing.T) {
	t.Parallel()
	requireTmux(t)
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	registerTUIE2EHubCleanup(t, hub)
	app := startTUITmuxSized(t, bin, hub, 200, 50)
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("evener / session / live task", "initial answer", "enter send")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			hub.BroadcastAgentDelta("01LIVE", "streamed word ")
		}
	})
	t.Cleanup(func() {
		close(stop)
		wg.Wait()
	})

	for range 5 {
		app.CaptureStable("evener / session / live task", "enter send")
	}
}

// openLiveSession navigates from the dashboard to the "live task" session and
// opens it. The dashboard sorts the evener project first (it owns the live
// session) so the fixed tree positions are: row 0 "Launch New Session",
// row 1 the evener project, row 2 the evener project's first live session
// ("live task"). We anchor to row 0 with a burst of Up presses (selection
// clamps at the top) so the helper works from any prior dashboard state, then
// step down to the live session and confirm the wide-layout details drawer
// before pressing Enter so the open is deterministic rather than racing the
// row cursor.
func openLiveSession(t *testing.T, app *tmuxTUI) {
	t.Helper()
	app.WaitFor("EVENER LIVE", "evener", "live task")
	app.SendKeys("Up", "Up", "Up", "Up", "Up", "Up")
	app.WaitFor("EVENER LIVE", "Launch New Session", "hub default")
	app.SendKeys("Down", "Down")
	app.WaitFor("Session:  01LIVE", "Action:   enter opens session")
	app.SendKeys("Enter")
	app.WaitFor("evener / session / live task")
}

// sendFirstCtrlCAndAssertNoQuitWarning sends a single ctrl+c and asserts that
// no in-app quit warning renders within the following 300ms — the window
// wide enough that a broken handler which defers the warning render is
// reliably caught.
//
// It streams the pane's raw output via `tmux pipe-pane` for that window
// rather than repeatedly forking `tmux capture-pane` in a poll loop (the
// original approach). This loop runs entirely inside hubCtrlCQuitWindow (the
// production 1s double-ctrl+c debounce, see hub_model.go), and the caller
// sends the second ctrl+c immediately after this returns, so every
// subprocess spawned here eats directly into that budget. Periodic
// capture-pane polling assumes ~2.6ms/call (see tuiE2EPollInterval), which
// holds when the machine is idle but not under CPU contention (a concurrent
// go test suite elsewhere): fork/exec scheduling delay can push the combined
// cost of dozens of calls past hubCtrlCQuitWindow, making the second real
// ctrl+c land too late and get treated as a fresh first press instead of a
// quit — this is the flake this helper replaces. pipe-pane costs exactly one
// subprocess for the whole window and captures every byte evener-tui writes,
// so a transient or deferred render can't be missed the way periodic
// sampling could.
func sendFirstCtrlCAndAssertNoQuitWarning(t *testing.T, app *tmuxTUI) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "ctrlc-settle.log")
	app.runTmux("pipe-pane", "-t", app.session, "cat >> "+shellQuote(logPath))
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", app.socket, "pipe-pane", "-t", app.session).Run() })
	app.SendKeys("C-c")
	time.Sleep(300 * time.Millisecond)
	// Stop the pipe before reading: this closes cat's stdin, so cat sees EOF
	// and flushes its output before exiting, guaranteeing a render from the
	// window's last few ms is on disk by the time we read it. Without this,
	// os.ReadFile could race a `cat` still buffering its final write. The
	// t.Cleanup toggle-off becomes a harmless no-op once this has run.
	_ = exec.Command("tmux", "-L", app.socket, "pipe-pane", "-t", app.session).Run()
	data, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read piped pane output: %v", err)
	}
	if screen := normalizePane(string(data)); strings.Contains(screen, "Press ctrl+c again") || strings.Contains(screen, "Restore this session:") {
		t.Fatalf("first ctrl+c should not render an in-app quit warning:\n%s", screen)
	}
}

// openEndedSession reveals and opens the folded ended "ended maintenance"
// session (01PAST), which carries no in-flight turn. Tree positions from the
// top: 0 Launch, 1 evener project, 2 live session, 3 the ended-sessions toggle.
// Anchoring to row 0 first keeps the helper robust to prior selection state.
func openEndedSession(t *testing.T, app *tmuxTUI) {
	t.Helper()
	app.WaitFor("EVENER LIVE", "evener", "live task")
	app.SendKeys("Up", "Up", "Up", "Up", "Up", "Up")
	app.WaitFor("EVENER LIVE", "Launch New Session", "hub default")
	app.SendKeys("Down", "Down", "Down")
	app.WaitFor("Action:   enter toggles ended sessions")
	app.SendKeys("Enter")
	app.WaitFor("ended maintenance")
	app.SendKeys("Down")
	app.WaitFor("Session:  01PAST", "Action:   enter opens session")
	app.SendKeys("Enter")
	app.WaitFor("evener / session / ended maintenance")
}

func requireTmux(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("tmux E2E test")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is required for TUI E2E tests")
	}
}

var (
	tuiBinaryOnce sync.Once
	tuiBinaryPath string
	errTUIBinary  error
)

// buildTUIBinary compiles the evener-tui binary once per test process and returns
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
		dir, err := os.MkdirTemp("", "evener-e2e-bin-")
		if err != nil {
			errTUIBinary = err
			return
		}
		bin := filepath.Join(dir, "evener")
		buildArgs := []string{"build", "-o", bin}
		// EVENER_E2E_COVER=<dir>: build an instrumented binary so the tmux'd TUI
		// subprocess emits coverage into that GOCOVERDIR (see tuiCoverEnvPrefix).
		// Unset (the default) leaves the build and the launch command unchanged.
		if os.Getenv("EVENER_E2E_COVER") != "" {
			buildArgs = append(buildArgs, "-cover")
		}
		buildArgs = append(buildArgs, "./cmd/evener/")
		cmd := exec.Command("go", buildArgs...)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			errTUIBinary = fmt.Errorf("build evener-tui: %w\n%s", err, out)
			return
		}
		tuiBinaryPath = bin
	})
	if errTUIBinary != nil {
		t.Fatalf("build evener-tui: %v", errTUIBinary)
	}
	return tuiBinaryPath
}

// tmuxTUI drives one evener-tui process on its own dedicated tmux server,
// connected via a unique "-L" socket rather than tmux's shared default server
// (the one at $TMUX_TMPDIR or /tmp/tmux-$UID/default). uniqueTmuxSessionName
// already rules out session-NAME collisions on any given server, but without
// a per-test socket every parallel test's session would still live on that
// one shared, stateful server process, which then fields every test's
// connect/capture-pane/send-keys/kill-session churn at once, plus whatever
// else on the machine happens to be talking to that same default socket — a
// real shared-mutable-resource risk under load, independent of naming.
// socket gives each test its own server process holding exactly one session
// (see closeTmuxServer for how that server and its socket file get torn
// down again).
type tmuxTUI struct {
	t       *testing.T
	session string
	socket  string
}

// runTmux runs a tmux subcommand against this session's dedicated socket.
func (a *tmuxTUI) runTmux(args ...string) {
	a.t.Helper()
	runTmux(a.t, a.socket, args...)
}

func startTUITmux(t *testing.T, bin string, hub *tuiE2EHub) *tmuxTUI {
	t.Helper()
	return startTUITmuxSized(t, bin, hub, 140, 40)
}

// tuiCoverEnvPrefix returns a "GOCOVERDIR=<dir> " shell prefix when
// EVENER_E2E_COVER is set, so the tmux-launched TUI (built with -cover, see
// buildTUIBinary) writes its coverage counters there; empty otherwise, leaving
// the launch command byte-identical on the default path.
func tuiCoverEnvPrefix() string {
	if dir := os.Getenv("EVENER_E2E_COVER"); dir != "" {
		return "GOCOVERDIR=" + shellQuote(dir) + " "
	}
	return ""
}

// tuiIsolatedEnvPrefix creates a throwaway HOME (with its own XDG config/
// state/cache subdirectories) for a tmux-launched evener-tui and returns the
// "HOME=... XDG_CONFIG_HOME=... ..." shell prefix that redirects it there.
// Without this, evener-tui's startup guard (cmdutil.EnsureUserConfigDirs,
// called from main.go before anything else) reads the real machine's HOME/
// XDG environment — which can refuse to start on a machine with an unmigrated
// legacy/interim Evener home root, or worse, touch the developer's real
// ~/.config/evener and ~/.local/state/evener.
func tuiIsolatedEnvPrefix(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	return "HOME=" + shellQuote(home) +
		" XDG_CONFIG_HOME=" + shellQuote(filepath.Join(home, "config")) +
		" XDG_STATE_HOME=" + shellQuote(filepath.Join(home, "state")) +
		" XDG_CACHE_HOME=" + shellQuote(filepath.Join(home, "cache")) + " "
}

func startTUITmuxSized(t *testing.T, bin string, hub *tuiE2EHub, width, height int) *tmuxTUI {
	t.Helper()
	acquireTmuxSlot(t)
	// A session name and a socket name are different tmux namespaces, so the
	// same unique string safely serves both — one test, one dedicated tmux
	// server, holding exactly one session (see tmuxTUI's socket-isolation
	// comment).
	session := uniqueTmuxSessionName()
	socket := session
	stateDir := t.TempDir()
	command := tuiCoverEnvPrefix() + tuiIsolatedEnvPrefix(t) + shellQuote(bin) + " tui -debug -no-auto-start-hub -hub-addr " + shellQuote(hub.URL()) + " -state-dir " + shellQuote(stateDir)
	runTmux(t, socket, "new-session", "-d", "-x", strconv.Itoa(width), "-y", strconv.Itoa(height), "-s", session, command)
	// Register cleanup the instant the session (and its dedicated server)
	// exist, before any later setup step below that can itself t.Fatalf: a
	// Fatalf there returns before the tmuxTUI value — and the caller's own
	// `defer app.Close()`, which only gets registered once this function
	// actually returns one — ever exists, which would otherwise leak the
	// whole dedicated server process and its socket file with nothing left
	// to clean either up.
	t.Cleanup(func() { closeTmuxServer(socket, session) })
	runTmux(t, socket, "set-option", "-t", session, "remain-on-exit", "on")
	pinTmuxWindowSize(t, socket, session, width, height)
	app := &tmuxTUI{t: t, session: session, socket: socket}
	app.WaitFor("EVENER LIVE")
	app.waitForInputReady(hub)
	return app
}

func startTUITmuxAltScreen(t *testing.T, bin string, hub *tuiE2EHub, width, height int) *tmuxTUI {
	t.Helper()
	acquireTmuxSlot(t)
	session := uniqueTmuxSessionName()
	socket := session
	stateDir := t.TempDir()
	// Keep the pane's shell alive past evener-tui's own exit. evener-tui leaves the
	// alternate screen and prints its restore instructions to the normal screen
	// as the very last thing it does before exiting; if the process then dies
	// immediately, a detached tmux pane under CPU starvation can freeze on death
	// before draining that trailing output, dropping the message entirely (the
	// scrollback comes back blank). Blocking the shell on a read it never
	// receives holds the pty open so tmux drains and renders the message; the
	// test reads it via WaitForHistory and Close() tears the session down.
	command := tuiCoverEnvPrefix() + tuiIsolatedEnvPrefix(t) + shellQuote(bin) + " tui -no-auto-start-hub -hub-addr " + shellQuote(hub.URL()) + " -state-dir " + shellQuote(stateDir) + "; read _"
	runTmux(t, socket, "new-session", "-d", "-x", strconv.Itoa(width), "-y", strconv.Itoa(height), "-s", session, command)
	// See the matching comment in startTUITmuxSized: register cleanup as
	// soon as the session/server exist, not only once the tmuxTUI value
	// (and the caller's `defer app.Close()`) is constructed.
	t.Cleanup(func() { closeTmuxServer(socket, session) })
	runTmux(t, socket, "set-option", "-t", session, "remain-on-exit", "on")
	pinTmuxWindowSize(t, socket, session, width, height)
	app := &tmuxTUI{t: t, session: session, socket: socket}
	app.WaitFor("EVENER LIVE")
	app.waitForInputReady(hub)
	return app
}

// waitForInputReady blocks until evener-tui is provably consuming pty input,
// closing the startup race between bubbletea's first paint and it attaching
// its stdin reader.
//
// In bubbletea v1.3.10's Program.Run (tea.go:681-707 in
// github.com/charmbracelet/bubbletea@v1.3.10), the renderer is started
// (tea.go:681, spinning up its own ticker goroutine at
// standard_renderer.go:99) and the first frame is queued for it to paint
// (tea.go:700, p.renderer.write(model.View())) BEFORE the input reader is
// attached (tea.go:703-706, p.initCancelReader). initCancelReader itself
// only launches the read-loop goroutine (tty.go:91, "go p.readLoop()") and
// returns immediately without waiting for that goroutine to actually start
// running — the read loop's first blocking read (via the kqueue/select
// cancelreader on macOS/Linux) executes whenever the Go scheduler next gets
// around to it. On an oversubscribed host (this fleet's shared worktree
// machine, 8 concurrent agents' worth of Go builds/tests) the independent
// ticker goroutine can flush the queued first frame to the pty — making
// "EVENER LIVE" appear in a tmux capture-pane — a good while before the
// read-loop goroutine is ever scheduled onto an OS thread. Any keys tmux
// sends to the pty in that window are not observed by the running program.
//
// A screen-based readiness probe can't close this gap: checking the SCREEN
// for a probe key's effect races the exact same window that loses the real
// keys. So this instead confirms an effect that only happens once a
// keystroke has been read and dispatched through the app's own Update
// loop: "r" (hub_keys.go's dashboard refresh binding) triggers a real
// fetchHubTree round trip to the test hub, observable via
// tuiE2EHub.treeGets — a signal entirely outside the tmux
// render/capture path. The probe is resent every poll tick (not sent once
// and merely awaited) because a single send can land in the same lost
// window as any other key; only a successful round trip proves the reader
// is live.
func (a *tmuxTUI) waitForInputReady(hub *tuiE2EHub) {
	a.t.Helper()
	// The baseline has to be taken AFTER the TUI's own startup tree fetch has
	// landed. Taken before it, that fetch is itself an increase over the
	// baseline, and the probe below returns satisfied by a round trip no
	// keystroke caused — declaring the input reader live while the window this
	// exists to close is still wide open.
	hub.WaitForTreeRequests(a.t, 1)
	baseline := hub.TreeRequests()
	deadline := time.Now().Add(tuiE2EWaitTimeout)
	nextDeadCheck := time.Now()
	for time.Now().Before(deadline) {
		now := time.Now()
		if !now.Before(nextDeadCheck) {
			if status, dead := a.PaneDeadStatus(); dead {
				a.t.Fatalf("evener-tui readiness probe: process exited before input reader became ready (status %s)\nvisible pane:\n%s\nrecent history:\n%s", status, a.Capture(), a.CaptureHistory())
			}
			nextDeadCheck = now.Add(tuiE2EDeadCheckInterval)
		}
		a.SendKeys("r")
		if hub.TreeRequests() > baseline {
			return
		}
		time.Sleep(tuiE2EPollInterval)
	}
	a.t.Fatalf("evener-tui readiness probe: input reader never observed to consume a keystroke within %s (sent %q repeatedly; hub tree requests stayed at %d)\nvisible pane:\n%s", tuiE2EWaitTimeout, "r", baseline, a.Capture())
}

// pinTmuxWindowSize forces the detached session's window to the exact
// requested geometry. tmux clamps a detached new-session to the size of the
// smallest attached client (typically ~80x24, or the harness default of
// 46x24 with no client), ignoring new-session -x/-y once the process draws.
// Switching the window-size option to manual and resizing makes the pane the
// geometry the TUI actually renders against.
func pinTmuxWindowSize(t *testing.T, socket, session string, width, height int) {
	t.Helper()
	runTmux(t, socket, "set-option", "-t", session, "window-size", "manual")
	runTmux(t, socket, "resize-window", "-t", session, "-x", strconv.Itoa(width), "-y", strconv.Itoa(height))
	waitForTmuxPaneSize(t, socket, session, width, height)
}

func waitForTmuxPaneSize(t *testing.T, socket, session string, width, height int) {
	t.Helper()
	deadline := time.Now().Add(tuiE2EWaitTimeout)
	wantWidth := strconv.Itoa(width)
	wantHeight := strconv.Itoa(height)
	var last string
	for time.Now().Before(deadline) {
		out, err := exec.Command("tmux", "-L", socket, "display-message", "-p", "-t", session, "#{pane_width} #{pane_height}").CombinedOutput()
		last = strings.TrimSpace(string(out))
		if err == nil {
			fields := strings.Fields(last)
			if len(fields) == 2 && fields[0] == wantWidth && fields[1] == wantHeight {
				return
			}
		}
		time.Sleep(tuiE2EPollInterval)
	}
	t.Fatalf("tmux (socket %s) pane size for %s = %q, want %dx%d", socket, session, last, width, height)
}

// closeTmuxServer kills the session on socket, then best-effort removes the
// dedicated server's own socket file. tmux does not unlink its own socket
// file when a dedicated server exits after its last session is killed —
// verified empirically on this host (tmux 3.5a/macOS): the server process
// exits, but a zero-byte socket file is left behind under the tmux socket
// directory. Left alone, every test run would leak one such file forever.
//
// The socket's resolved path must be queried BEFORE kill-session: tmux's own
// #{socket_path} format variable is the only reliable way to learn it (the
// default socket directory depends on $TMUX_TMPDIR and platform temp-dir
// conventions this file has no business guessing at), and once the session
// is killed the dedicated server exits (exit-empty) and can no longer answer
// the query. Every step here ignores errors: this is the single cleanup path
// shared by both an early t.Cleanup (registered by startTUITmuxSized/
// startTUITmuxAltScreen right after new-session, in case a later setup step
// fails before a tmuxTUI value — and its own eventual Close() — ever exists)
// and the ordinary (*tmuxTUI).Close(), so the session or server may
// legitimately already be gone by the time this runs a second time.
func closeTmuxServer(socket, session string) {
	pathOut, _ := exec.Command("tmux", "-L", socket, "display-message", "-p", "#{socket_path}").CombinedOutput()
	_ = exec.Command("tmux", "-L", socket, "kill-session", "-t", session).Run()
	if path := strings.TrimSpace(string(pathOut)); path != "" {
		_ = os.Remove(path)
	}
}

func (a *tmuxTUI) Close() {
	closeTmuxServer(a.socket, a.session)
}

func (a *tmuxTUI) SendKeys(keys ...string) {
	a.t.Helper()
	args := append([]string{"send-keys", "-t", a.session}, keys...)
	a.runTmux(args...)
}

func (a *tmuxTUI) TypeLine(text string) {
	a.t.Helper()
	a.TypeText(text)
	a.runTmux("send-keys", "-t", a.session, "Enter")
}

func (a *tmuxTUI) TypeText(text string) {
	a.t.Helper()
	a.runTmux("send-keys", "-t", a.session, "-l", text)
}

func (a *tmuxTUI) Capture() string {
	a.t.Helper()
	out, err := exec.Command("tmux", "-L", a.socket, "capture-pane", "-p", "-t", a.session).CombinedOutput()
	if err != nil {
		a.t.Fatalf("capture tmux pane: %v\n%s", err, out)
	}
	return normalizePane(string(out))
}

func (a *tmuxTUI) CaptureHistory() string {
	a.t.Helper()
	out, err := exec.Command("tmux", "-L", a.socket, "capture-pane", "-p", "-S", "-200", "-t", a.session).CombinedOutput()
	if err != nil {
		a.t.Fatalf("capture tmux history: %v\n%s", err, out)
	}
	return normalizePane(string(out))
}

// CaptureStable returns Capture()'s output once a frame that is BOTH complete
// and stable is seen: it contains every want (completeness) and is
// byte-identical to the capture one poll interval before it (stability).
//
// A single Capture() is a snapshot of tmux's OWN terminal-grid state, which
// updates incrementally as bytes arrive from the pty — not a snapshot of what
// evener-tui most recently rendered. bubbletea writes each frame as one
// unsynchronized ANSI byte stream (no terminal synchronized-output mode,
// bubbletea v1.3.10) with the composer/footer last; a frame is commonly
// several KB, well past any platform's atomic-pipe-write guarantee, so under
// load (rapid re-renders, CPU contention — e.g. right after a turn starts and
// notifications are streaming) tmux can legitimately still be mid-write when
// capture-pane samples it. The visible result (kata nxq6) is a pane that
// looks blank above the composer's last few lines: not a rendering bug, a
// read of tmux's grid caught between an erase and the redraw that follows it.
// It self-heals on its own within milliseconds, which is exactly what makes
// two matching captures a few ms apart trustworthy where one capture is not.
//
// NEITHER heuristic alone is sufficient — this is the lesson of the CI flake
// that added the anchors:
//   - Stability alone accepted a torn frame: during a continuous notification
//     stream the pane never settles, and two consecutive captures agreed on a
//     mid-write grid (all streamed text, no footer, no breadcrumb) for longer
//     than the poll interval — a stall long enough to defeat the two-sample
//     heuristic.
//   - Anchors alone accept a partial repaint: tmux's grid is incremental, so a
//     frame's extremes can still hold the PREVIOUS frame's content while the
//     middle is being rewritten — both anchors present on a torn grid — and
//     with no wants at all, completeness is vacuous.
//
// Pass wants drawn from the frame's extremes, including the composer's footer
// hints (written last). The two conditions cover each other's failure modes;
// a stalled mid-write carrying stale anchors for every want at once is the
// one residual gap, and nothing short of synchronized-output mode closes it.
//
// Use this instead of a lone Capture() for any assertion that a substring is
// ABSENT. WaitFor already retries until its wanted substrings appear, which
// makes it self-correcting for POSITIVE assertions the same way — but its
// returned screen is only guaranteed to contain what it waited for, not to be
// a complete frame, so a negative check (`strings.Contains(screen, unwanted)`
// on that same screen) can still land mid-render and read an absence that
// isn't real.
func (a *tmuxTUI) CaptureStable(wants ...string) string {
	a.t.Helper()
	deadline := time.Now().Add(tuiE2EWaitTimeout)
	prev := a.Capture()
	for time.Now().Before(deadline) {
		time.Sleep(tuiE2EPollInterval)
		cur := a.Capture()
		complete := true
		for _, want := range wants {
			if !strings.Contains(cur, want) {
				complete = false
				break
			}
		}
		if complete && cur == prev {
			return cur
		}
		prev = cur
	}
	a.t.Fatalf("pane never rendered a complete, stable frame (missing %q) within %s — either the pane is still changing every %s (a real, ongoing render), or the wanted anchors never rendered\nlast capture:\n%s", wants, tuiE2EWaitTimeout, tuiE2EPollInterval, prev)
	return ""
}

func (a *tmuxTUI) Resize(width, height int) {
	a.t.Helper()
	a.runTmux("resize-window", "-t", a.session, "-x", strconv.Itoa(width), "-y", strconv.Itoa(height))
	waitForTmuxPaneSize(a.t, a.socket, a.session, width, height)
}

func (a *tmuxTUI) WaitFor(wants ...string) string {
	a.t.Helper()
	deadline := time.Now().Add(tuiE2EWaitTimeout)
	nextDeadCheck := time.Now()
	var screen string
	for time.Now().Before(deadline) {
		now := time.Now()
		if !now.Before(nextDeadCheck) {
			if status, dead := a.PaneDeadStatus(); dead {
				a.t.Fatalf("evener-tui exited before %q (status %s)\nvisible pane:\n%s\nrecent history:\n%s", wants, status, a.Capture(), a.CaptureHistory())
			}
			nextDeadCheck = now.Add(tuiE2EDeadCheckInterval)
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

// WaitForWithout polls until every wanted substring is present AND every
// substring in without is absent, on the SAME captured frame. Checking
// absence on a frame chosen only by positive matches races the repaint: a
// capture can land after the filter line updates but before the list below
// it does, so "the old row is gone" must be part of the awaited condition,
// not a one-shot assertion on whichever frame satisfied the positives first.
func (a *tmuxTUI) WaitForWithout(without []string, wants ...string) string {
	a.t.Helper()
	deadline := time.Now().Add(tuiE2EWaitTimeout)
	nextDeadCheck := time.Now()
	var screen string
	for time.Now().Before(deadline) {
		now := time.Now()
		if !now.Before(nextDeadCheck) {
			if status, dead := a.PaneDeadStatus(); dead {
				a.t.Fatalf("evener-tui exited before %q without %q (status %s)\nvisible pane:\n%s\nrecent history:\n%s", wants, without, status, a.Capture(), a.CaptureHistory())
			}
			nextDeadCheck = now.Add(tuiE2EDeadCheckInterval)
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
			for _, absent := range without {
				if strings.Contains(screen, absent) {
					ok = false
					break
				}
			}
		}
		if ok {
			return screen
		}
		time.Sleep(tuiE2EPollInterval)
	}
	a.t.Fatalf("timed out waiting for %q without %q\nvisible pane:\n%s\nrecent history:\n%s", wants, without, screen, a.CaptureHistory())
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
	nextDeadCheck := time.Now()
	var screen string
	for time.Now().Before(deadline) {
		now := time.Now()
		if !now.Before(nextDeadCheck) {
			if status, dead := a.PaneDeadStatus(); dead {
				a.t.Fatalf("evener-tui exited before %s (status %s)\nvisible pane:\n%s\nrecent history:\n%s", desc, status, a.Capture(), a.CaptureHistory())
			}
			nextDeadCheck = now.Add(tuiE2EDeadCheckInterval)
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
		if err := exec.Command("tmux", "-L", a.socket, "has-session", "-t", a.session).Run(); err != nil {
			return
		}
		time.Sleep(tuiE2EPollInterval)
	}
	a.t.Fatalf("tmux session did not exit\nvisible pane:\n%s\nrecent history:\n%s", a.Capture(), a.CaptureHistory())
}

func (a *tmuxTUI) PaneDeadStatus() (string, bool) {
	a.t.Helper()
	out, err := exec.Command("tmux", "-L", a.socket, "display-message", "-p", "-t", a.session, "#{pane_dead} #{pane_dead_status}").CombinedOutput()
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

// runTmux runs a tmux subcommand against the given dedicated socket. "-L"
// (the socket-name flag) must precede the subcommand, per tmux's own flag
// parsing, which is why it's threaded through here rather than left to each
// call site.
func runTmux(t *testing.T, socket string, args ...string) {
	t.Helper()
	fullArgs := append([]string{"-L", socket}, args...)
	out, err := exec.Command("tmux", fullArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %s: %v\n%s", strings.Join(fullArgs, " "), err, out)
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
	t         *testing.T
	server    *httptest.Server
	app       *appserver.Server
	closeOnce sync.Once
	closed    chan struct{}
	// cleanupTrace is test-only instrumentation for the lifecycle regression;
	// it stays nil for every normal E2E fixture.
	cleanupTrace chan<- int
	rpcMu        sync.Mutex
	rpcWG        sync.WaitGroup
	rpcClosing   bool
	rpcStarts    atomic.Int32
	rpcExits     atomic.Int32
	rpcJoined    atomic.Bool
	// rpcExitGate is test-only instrumentation that keeps one handler in its
	// deferred exit path until the close event-driven rescue releases it.
	rpcExitGate <-chan struct{}
	// ready is closed by the HTTP handler, not by httptest.NewServer's
	// listener setup.  The latter only proves that a port was allocated; it
	// does not prove that the server goroutine has reached the handler under a
	// constrained scheduler.
	ready chan struct{}
	// changed is a coalescing notification for state observed by the wait
	// helpers below.  Waiting on handler events avoids turning a 5-second hang
	// backstop into a timer-driven polling loop.
	changed       chan struct{}
	readyRequests atomic.Int32

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
	ProjectID       string
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
		harnesses: []appwire.HarnessDescriptor{{ID: "evener", Label: "evener", Kind: "evener"}},
		ready:     make(chan struct{}),
		changed:   make(chan struct{}, 1),
		closed:    make(chan struct{}),
	}
	h.addSession(&tuiE2ESession{
		ID:           "01LIVE",
		Title:        "live task",
		State:        appwire.ThreadStatusIdle,
		Project:      "evener",
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
				// A real hub stamps every item with the transcript entry it
				// occupies; the browse-mode fork sends the user item's index as
				// its divergence position.
				{Type: "userMessage", ID: "user-1", TurnID: "turn_1", TranscriptEntryIndex: 1, Text: "initial question"},
				{Type: "commandExecution", ID: "tool-1", TurnID: "turn_1", TranscriptEntryIndex: 1, ToolName: "exec", ArgumentsJSON: `{"cmd":"echo e2e"}`, Output: "tool output from e2e", Status: "completed"},
				{Type: "agentMessage", ID: "agent-1", TurnID: "turn_1", TranscriptEntryIndex: 1, Text: "initial answer", Status: "completed"},
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
		Project:    "evener",
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
		Project:      "evener",
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
		WorkingDir:   "/tmp/evener-e2e/ops",
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

	app := appserver.NewServer(appserver.ServerConfig{ServerName: "evener-hub", SourceID: "local"})
	h.app = app
	h.registerHandlers(app)
	var readyOnce sync.Once
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			h.readyRequests.Add(1)
			readyOnce.Do(func() { close(h.ready) })
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		if !h.beginRPCHandler() {
			return
		}
		defer h.endRPCHandler()
		app.ServeWebSocket(w, r)
	}))
	// Complete one real request through the installed handler before handing
	// the address to the tmux child.  A bound listener alone is insufficient:
	// on a loaded runner the child can otherwise spend its entire dial context
	// waiting for net/http's serve goroutine to be scheduled.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.server.URL+"/ready", nil)
	if err != nil {
		cancel()
		h.server.Close()
		t.Fatalf("build hub readiness request: %v", err)
	}
	resp, err := h.server.Client().Do(req)
	cancel()
	if err != nil {
		h.server.Close()
		t.Fatalf("hub readiness request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		h.server.Close()
		t.Fatalf("hub readiness status=%d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	if got := h.readyRequests.Load(); got != 1 {
		h.server.Close()
		t.Fatalf("hub readiness handler calls=%d, want 1", got)
	}
	select {
	case <-h.ready:
	default:
		h.server.Close()
		t.Fatal("hub readiness request completed without handler readiness event")
	}
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
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginPreview, h.handlePluginPreview)
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerHarnessesList, h.handleHarnessList)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, h.handleThreadStart)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadResume, h.handleThreadResume)
	appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, h.handleTurnStart)
	appserver.HandleTyped(app.Router(), appwire.MethodTurnSteer, h.handleTurnSteer)
	appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, h.handleTurnQueue)
	appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, h.handleTurnDrainAsSteer)
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerTasksList, h.handleTasksList)
	appserver.HandleTyped(app.Router(), appwire.MethodTurnInterrupt, h.handleTurnInterrupt)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadCompactStart, h.handleThreadCompactStart)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadModelSet, h.handleThreadModelSet)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadClear, h.handleThreadClear)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadFork, h.handleThreadFork)
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthStatus, h.handleAuthStatus)
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthLoginStart, h.handleAuthLoginStart)
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthLoginComplete, h.handleAuthLoginComplete)
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthLogout, h.handleAuthLogout)
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerThreadTranscriptsList, h.handleThreadTranscriptList)
}

func (h *tuiE2EHub) handleHarnessList(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return appwire.HarnessListResponse{Data: append([]appwire.HarnessDescriptor(nil), h.harnesses...)}, nil
}

func (h *tuiE2EHub) handlePluginPreview(context.Context, appwire.PluginPreviewParams) (appwire.PluginPreviewResponse, error) {
	return appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{
		{Name: "alpha", Source: "project", Selected: true},
		{Name: "beta", Source: "marketplace", Selected: true},
	}}, nil
}

func (h *tuiE2EHub) URL() string {
	return h.server.URL
}

func (h *tuiE2EHub) Close() {
	h.closeOnce.Do(func() {
		if h.cleanupTrace != nil {
			h.cleanupTrace <- 2
		}
		h.rpcMu.Lock()
		h.rpcClosing = true
		h.rpcMu.Unlock()
		// Server.Close does not join hijacked WebSocket handlers. The explicit
		// wait is safe because beginRPCHandler closes the admission gate before
		// waiting, so no Add can race with Wait.
		h.server.Close()
		h.joinRPCHandlers()
		if h.cleanupTrace != nil {
			h.cleanupTrace <- 4
		}
		close(h.closed)
	})
}

func (h *tuiE2EHub) joinRPCHandlers() {
	h.rpcWG.Wait()
	h.rpcJoined.Store(true)
}

func (h *tuiE2EHub) beginRPCHandler() bool {
	h.rpcMu.Lock()
	defer h.rpcMu.Unlock()
	if h.rpcClosing {
		return false
	}
	h.rpcWG.Add(1)
	h.rpcStarts.Add(1)
	return true
}

func (h *tuiE2EHub) endRPCHandler() {
	if h.rpcExitGate != nil {
		<-h.rpcExitGate
	}
	if h.cleanupTrace != nil {
		h.cleanupTrace <- 3
	}
	h.rpcExits.Add(1)
	h.rpcWG.Done()
}

// registerTUIE2EHubCleanup deliberately uses t.Cleanup rather than defer.
// The tmux starter registers its cleanup after this function returns, so the
// testing package's LIFO cleanup order terminates the tmux client before this
// joining httptest server is closed. That ordering also applies when a later
// setup assertion calls Fatalf.
func registerTUIE2EHubCleanup(t *testing.T, hub *tuiE2EHub) {
	t.Helper()
	t.Cleanup(hub.Close)
}

// TestTUITmuxE2EHubCleanupJoinsClientBeforeServer exercises the real WebSocket
// handler lifetime separately from httptest.Server.Close: hijacked connections
// are not part of that server's close wait, so the fixture owns an explicit
// handler join. The event-driven rescue closes the client if a mutation starts
// hub cleanup first, turning the wrong order into a structural order failure
// instead of a test hang.
func TestTUITmuxE2EHubCleanupJoinsClientBeforeServer(t *testing.T) {
	var hub *tuiE2EHub
	trace := make(chan int, 4)
	observed := make(chan int, 4)
	rescueDone := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Run("setup-lifecycle", func(t *testing.T) {
		hub = newTUIE2EHub(t)
		hub.rpcExitGate = release
		transport, err := appwire.DialWebSocket(context.Background(), "ws"+strings.TrimPrefix(hub.URL(), "http")+"/rpc", hub.server.Client())
		if err != nil {
			t.Fatalf("dial test hub: %v", err)
		}
		hub.cleanupTrace = trace
		go func() {
			seen := [5]bool{}
			for !seen[1] || !seen[2] || !seen[3] || !seen[4] {
				event := <-trace
				observed <- event
				seen[event] = true
				if event == 2 {
					_ = transport.Close()
					releaseOnce.Do(func() { close(release) })
				}
			}
			close(rescueDone)
		}()
		registerTUIE2EHubCleanup(t, hub)
		t.Cleanup(func() {
			trace <- 1
			_ = transport.Close()
		})
	})
	order := [4]int{<-observed, <-observed, <-observed, <-observed}
	if order != [4]int{1, 2, 3, 4} {
		t.Fatalf("cleanup/handler order=%v, want client, hub-start, handler-exit, hub-complete", order)
	}
	<-rescueDone
	select {
	case <-hub.closed:
	default:
		t.Fatal("hub cleanup completed without the joined closed event")
	}
	if !hub.rpcJoined.Load() {
		t.Fatal("hub cleanup completed without joining RPC handlers")
	}
}

func TestTUITmuxE2EHubHandlerLifetimeEdges(t *testing.T) {
	t.Run("zero-handlers-and-repeated-close", func(t *testing.T) {
		hub := newTUIE2EHub(t)
		registerTUIE2EHubCleanup(t, hub)
		hub.Close()
		hub.Close()
		if got := hub.rpcStarts.Load(); got != 0 {
			t.Fatalf("RPC starts=%d, want 0", got)
		}
		if got := hub.rpcExits.Load(); got != 0 {
			t.Fatalf("RPC exits=%d, want 0", got)
		}
		if !hub.rpcJoined.Load() {
			t.Fatal("zero-handler close did not complete the join")
		}
	})

	t.Run("accept-error-returns-handler", func(t *testing.T) {
		hub := newTUIE2EHub(t)
		registerTUIE2EHubCleanup(t, hub)
		resp, err := hub.server.Client().Get(hub.URL() + "/rpc")
		if err != nil {
			t.Fatalf("GET /rpc: %v", err)
		}
		_ = resp.Body.Close()
		hub.Close()
		hub.Close()
		if got := hub.rpcStarts.Load(); got != 1 {
			t.Fatalf("RPC starts=%d, want 1", got)
		}
		if got := hub.rpcExits.Load(); got != 1 {
			t.Fatalf("RPC exits=%d, want 1", got)
		}
		if !hub.rpcJoined.Load() {
			t.Fatal("accept-error close did not complete the join")
		}
	})

	t.Run("multiple-handlers", func(t *testing.T) {
		hub := newTUIE2EHub(t)
		first, err := appwire.DialWebSocket(context.Background(), "ws"+strings.TrimPrefix(hub.URL(), "http")+"/rpc", hub.server.Client())
		if err != nil {
			t.Fatalf("dial first handler: %v", err)
		}
		second, err := appwire.DialWebSocket(context.Background(), "ws"+strings.TrimPrefix(hub.URL(), "http")+"/rpc", hub.server.Client())
		if err != nil {
			_ = first.Close()
			t.Fatalf("dial second handler: %v", err)
		}
		registerTUIE2EHubCleanup(t, hub)
		_ = first.Close()
		_ = second.Close()
		hub.Close()
		hub.Close()
		if got := hub.rpcStarts.Load(); got != 2 {
			t.Fatalf("RPC starts=%d, want 2", got)
		}
		if got := hub.rpcExits.Load(); got != 2 {
			t.Fatalf("RPC exits=%d, want 2", got)
		}
		if !hub.rpcJoined.Load() {
			t.Fatal("multiple-handler close did not complete the join")
		}
	})
}

// notify wakes a waiter after a handler has observed a request or changed
// fixture state. Notifications are intentionally coalesced: every waiter
// re-checks its predicate, so one wakeup is sufficient for a burst of RPCs.
func (h *tuiE2EHub) notify() {
	select {
	case h.changed <- struct{}{}:
	default:
	}
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
	if s.WorkingDir != "" {
		if err := os.MkdirAll(s.WorkingDir, 0o755); err != nil {
			h.t.Fatalf("create fixture project %q: %v", s.WorkingDir, err)
		}
		project, err := identifier.ResolveProject(s.WorkingDir)
		if err != nil {
			h.t.Fatalf("resolve fixture project %q: %v", s.WorkingDir, err)
		}
		s.WorkingDir = project.CanonicalPath
		s.ProjectID = project.ID
	}
	h.sessions[s.ID] = s
	h.order = append(h.order, s.ID)
}

func (h *tuiE2EHub) handleThreadList(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	defer h.notify()
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
	defer h.notify()
	h.mu.Lock()
	authRequired := h.authRequired
	h.mu.Unlock()
	if params.Harness == "codex-local" {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "codex-local", Model: "gpt-5.3-codex"}}}, nil
	}
	if authRequired {
		return appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{tuiE2EGPT5Descriptor()},
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Title:    "Login required",
				Message:  "OpenAI login required",
				Hint:     "run /auth openai",
			}},
		}, nil
	}
	return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{tuiE2EGPT5Descriptor()}}, nil
}

// tuiE2EGPT5Descriptor is the model row a real hub delivers: identity plus the
// capability and cost fields it fills from the registry's resolved row. The
// picker renders its meta tail from these, so the fixture has to carry them
// for the popup to lay out the way the daemon's does.
func tuiE2EGPT5Descriptor() appwire.ModelDescriptor {
	return appwire.ModelDescriptor{
		Provider:             "openai",
		Model:                "gpt-5",
		ContextWindow:        new(272_000),
		SupportsTools:        new(true),
		SupportsVision:       new(true),
		SupportsReasoning:    new(true),
		InputCostPerMillion:  new(1.25),
		OutputCostPerMillion: new(10.0),
	}
}

func (h *tuiE2EHub) handleAuthStatus(_ context.Context, params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
	defer h.notify()
	h.recordAuthCall("status", params.Provider)
	return appwire.AuthStatusResponse{Provider: "openai-codex", Supported: true, ActiveSource: "none", AuthModes: []string{"oauth"}}, nil
}

func (h *tuiE2EHub) handleAuthLoginStart(_ context.Context, params appwire.AuthLoginStartParams) (appwire.AuthLoginStartResponse, error) {
	defer h.notify()
	h.recordAuthCall("login-start", params.Provider)
	return appwire.AuthLoginStartResponse{
		Provider: "openai-codex",
		FlowID:   "flow-1",
		URL:      "https://auth.example/authorize",
	}, nil
}

func (h *tuiE2EHub) handleAuthLoginComplete(_ context.Context, params appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
	defer h.notify()
	h.recordAuthCall("login-complete", params.Provider)
	h.mu.Lock()
	h.authCompletions = append(h.authCompletions, params)
	h.mu.Unlock()
	return appwire.AuthLoginCompleteResponse{
		Status: appwire.AuthStatusResponse{
			Provider:     "openai-codex",
			Supported:    true,
			SignedIn:     true,
			ActiveSource: "oauth",
			AuthModes:    []string{"oauth"},
			Email:        "tmux@example.com",
		},
	}, nil
}

func (h *tuiE2EHub) handleAuthLogout(_ context.Context, params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
	defer h.notify()
	h.recordAuthCall("logout", params.Provider)
	return appwire.AuthLogoutResponse{
		Removed: true,
		Status:  appwire.AuthStatusResponse{Provider: "openai-codex", Supported: true, ActiveSource: "none", AuthModes: []string{"oauth"}},
	}, nil
}

func (h *tuiE2EHub) handleThreadStart(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	defer h.notify()
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
		Project:      "evener",
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
	defer h.notify()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resumes = append(h.resumes, params)
	s := &tuiE2ESession{
		ID:           "02RESUME",
		Title:        "resumed maintenance",
		State:        appwire.ThreadStatusIdle,
		Project:      "evener",
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
	defer h.notify()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failSend {
		return appwire.TurnStartResponse{}, errors.New("send failed")
	}
	h.sends = append(h.sends, testInputText(params.Input))
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_sent", Status: appwire.TurnStatusInProgress}}, nil
}

func (h *tuiE2EHub) handleTurnSteer(_ context.Context, params appwire.TurnSteerParams) (appwire.EmptyResponse, error) {
	defer h.notify()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.steers = append(h.steers, params)
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleTurnQueue(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
	defer h.notify()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queues = append(h.queues, params)
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleTurnDrainAsSteer(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.EmptyResponse, error) {
	defer h.notify()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.drains = append(h.drains, params)
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleTasksList(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	defer h.notify()
	h.mu.Lock()
	fail := h.failTasks
	h.mu.Unlock()
	if fail {
		return appwire.TaskListResponse{}, errors.New("tasks failed")
	}
	return appwire.TaskListResponse{Data: []task.Task{{ID: 1, Type: task.TaskTypeImplement, Description: "wire tui e2e", Status: task.TaskInProgress}}}, nil
}

func (h *tuiE2EHub) handleThreadTranscriptList(_ context.Context, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
	defer h.notify()
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
	defer h.notify()
	h.recordAction("interrupt")
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleThreadCompactStart(context.Context, appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
	defer h.notify()
	h.recordAction("compact")
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleThreadModelSet(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
	defer h.notify()
	h.mu.Lock()
	h.models = append(h.models, params.Model)
	h.mu.Unlock()
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleThreadClear(_ context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	defer h.notify()
	h.recordAction("clear")
	h.mu.Lock()
	defer h.mu.Unlock()
	id := "02CLEAR"
	s := &tuiE2ESession{
		ID:           id,
		Title:        "cleared session",
		State:        appwire.ThreadStatusIdle,
		Project:      "evener",
		WorkingDir:   tuiE2EProjectDir,
		Model:        "gpt-5",
		Live:         true,
		Capabilities: fullTUIE2ECapabilities(),
	}
	h.addSession(s)
	thread := h.threadFromSessionLocked(s)
	return appwire.ThreadClearResponse{
		Thread: thread,
		Ref:    thread.Evener.Ref,
		Receipt: appwire.MutationReceipt{
			ClientMutationID: params.ClientMutationID,
			Disposition:      appwire.MutationDispositionApplied,
			ThreadID:         thread.ID,
			InstanceID:       thread.Evener.InstanceID,
			ProjectionState:  appwire.MutationProjectionReflected,
		},
	}, nil
}

func (h *tuiE2EHub) handleThreadFork(_ context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	defer h.notify()
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
		Project:      "evener",
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
		ProjectID:     s.ProjectID,
		ProjectPath:   s.WorkingDir,
		Source:        "local",
		Status:        appwire.ThreadStatus{Type: status},
		Turns:         append([]appwire.Turn(nil), s.Turns...),
		Evener: appwire.EvenerThread{
			Ref:             appwire.Ref{SourceID: "local", ThreadID: s.ID}.String(),
			InstanceID:      s.ID,
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

// TreeRequests returns the current ThreadList round-trip count without
// waiting, so a caller can capture a baseline and later check for an
// increase (see waitForInputReady).
func (h *tuiE2EHub) TreeRequests() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.treeGets
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
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		if pred() {
			return
		}
		select {
		case <-h.changed:
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", desc)
		}
	}
}
