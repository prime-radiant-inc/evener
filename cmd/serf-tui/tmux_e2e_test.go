package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
)

const tuiE2EProjectDir = "/tmp/serf-tui-e2e/serf"
const tuiE2EWaitTimeout = 20 * time.Second

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func TestTUITmuxE2E_DashboardProjectAndSpawn(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	screen := app.WaitFor("serf live", hub.URL(), "serf", "live task", "ops task")
	if strings.Contains(screen, "ended maintenance") {
		t.Fatalf("dashboard should not render ended sessions:\n%s", screen)
	}
	app.SendKeys("/")
	app.TypeText("ops")
	screen = app.WaitFor("Command palette", "Filter: ops", "ops task")
	if strings.Contains(screen, "live task") {
		t.Fatalf("dashboard palette should hide non-matching sessions:\n%s", screen)
	}
	app.SendKeys("Escape")
	app.WaitFor("serf live", "live task", "ops task")

	initialTreeRequests := hub.WaitForTreeRequests(t, 1)
	app.SendKeys("r")
	hub.WaitForTreeRequests(t, initialTreeRequests+1)

	app.SendKeys("Enter")
	app.WaitFor("serf / project / serf", "Live now", "live task", "Recent in this project", "ended maintenance")
	app.SendKeys("/")
	app.TypeText("ended")
	screen = app.WaitFor("Command palette", "Filter: ended", "ended maintenance")
	if strings.Contains(screen, "live task") {
		t.Fatalf("project palette should hide non-matching live session:\n%s", screen)
	}
	app.SendKeys("Escape")
	app.WaitFor("serf / project / serf", "live task", "ended maintenance")

	app.SendKeys("Escape")
	app.WaitFor("serf live", "live task")
	app.SendKeys("p")
	app.WaitFor("serf / project / serf", "Recent in this project")
	app.SendKeys("Escape")
	app.WaitFor("serf live", "live task")

	app.SendKeys("n")
	app.WaitFor("serf / new session", "Dir:      "+tuiE2EProjectDir, "Prompt (optional):")
	app.TypeLine("spawn from dashboard")
	app.WaitFor("spawned session 1", "local:02SPAWN1")
	spawns := hub.WaitForSpawns(t, 1)
	if spawns[0].CWD != tuiE2EProjectDir {
		t.Fatalf("dashboard spawn cwd=%q, want %q", spawns[0].CWD, tuiE2EProjectDir)
	}
	if spawns[0].Prompt != "spawn from dashboard" {
		t.Fatalf("dashboard spawn prompt=%q, want spawn from dashboard", spawns[0].Prompt)
	}
	if spawns[0].ModelProvider != "" || spawns[0].Model != "openai/gpt-5" {
		t.Fatalf("dashboard spawn model=%s/%s, want openai/gpt-5", spawns[0].ModelProvider, spawns[0].Model)
	}

	app.SendKeys("C-o")
	app.WaitFor("serf live", "live task")
	app.SendKeys("Enter")
	app.WaitFor("serf / project / serf", "Recent in this project")
	app.SendKeys("n")
	app.WaitFor("serf / new session", "Dir:      "+tuiE2EProjectDir, "Prompt (optional):")
	app.TypeLine("spawn from project")
	app.WaitFor("spawned session 2", "local:02SPAWN2")
	spawns = hub.WaitForSpawns(t, 2)
	if spawns[1].CWD != tuiE2EProjectDir {
		t.Fatalf("project spawn cwd=%q, want %q", spawns[1].CWD, tuiE2EProjectDir)
	}
	if spawns[1].Prompt != "spawn from project" {
		t.Fatalf("project spawn prompt=%q, want spawn from project", spawns[1].Prompt)
	}
	if spawns[1].ModelProvider != "" || spawns[1].Model != "openai/gpt-5" {
		t.Fatalf("project spawn model=%s/%s, want openai/gpt-5", spawns[1].ModelProvider, spawns[1].Model)
	}

	app.SendKeys("C-o")
	app.WaitFor("serf live", "live task")
	app.SendKeys("q")
	app.WaitForExit()
}

func TestTUITmuxE2E_AppShellPreservesLayoutAcrossWidths(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	screen := app.WaitFor("serf live", "live task", "ctrl+o dashboard")
	requirePaneOrder(t, screen, "serf live", "live task", "ctrl+o dashboard")

	app.SendKeys("/")
	screen = app.WaitFor("Command palette", "ctrl+o dashboard")
	requirePaneOrder(t, screen, "serf live", "Command palette", "ctrl+o dashboard")

	app.Resize(60, 30)
	screen = app.WaitFor("Command palette", "ctrl+o dashboard")
	requirePaneOrder(t, screen, "serf live", "Command palette", "ctrl+o dashboard")

	app.SendKeys("C-o")
	screen = app.WaitFor("serf live", "live task", "ctrl+o dashboard")
	requirePaneOrder(t, screen, "serf live", "live task", "ctrl+o dashboard")

	app.SendKeys("n")
	screen = app.WaitFor("serf / new session", "Prompt (optional):", "ctrl+o: dashboard")
	requirePaneOrder(t, screen, "serf / new session", "Prompt (optional):", "ctrl+o: dashboard")

	app.SendKeys("C-o")
	screen = app.WaitFor("serf live", "live task", "ctrl+o dashboard")
	requirePaneOrder(t, screen, "serf live", "live task", "ctrl+o dashboard")
}

func TestTUITmuxE2E_DashboardNarrowWideStates(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetSessionTitle("01LIVE", "live dashboard task with a title long enough to truncate cleanly")
	defer hub.Close()

	wide := startTUITmuxSized(t, bin, hub.URL(), 140, 40)
	defer wide.Close()
	wideScreen := wide.WaitFor("serf live", "details", "Project:  serf", "Live:     1", "Dir:      "+tuiE2EProjectDir)
	if strings.Contains(wideScreen, "Prompt (optional):") || strings.Contains(wideScreen, "enter: send") {
		t.Fatalf("wide dashboard rendered a composer:\n%s", wideScreen)
	}
	t.Logf("wide dashboard capture:\n%s", wideScreen)
	wide.SendKeys("q")
	wide.WaitForExit()

	narrow := startTUITmuxSized(t, bin, hub.URL(), 60, 30)
	defer narrow.Close()
	narrowScreen := narrow.WaitFor("serf live", "keys: up/down enter p n new / palette ctrl+o dashboard q", "...")
	if strings.Contains(narrowScreen, "details") {
		t.Fatalf("narrow dashboard rendered details drawer:\n%s", narrowScreen)
	}
	if strings.Contains(narrowScreen, "Prompt (optional):") || strings.Contains(narrowScreen, "enter: send") {
		t.Fatalf("narrow dashboard rendered a composer:\n%s", narrowScreen)
	}
	t.Logf("narrow dashboard capture:\n%s", narrowScreen)
	narrow.SendKeys("q")
	narrow.WaitForExit()
}

func TestTUITmuxE2E_DashboardEmptyState(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.EndDashboardSessions()
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	screen := app.WaitFor("No live sessions are running", "n new session", "p project history", "/ palette")
	if strings.Contains(screen, "live task") || strings.Contains(screen, "ops task") || strings.Contains(screen, "Prompt (optional):") || strings.Contains(screen, "enter: send") {
		t.Fatalf("empty dashboard rendered live/session composer content:\n%s", screen)
	}
	t.Logf("empty dashboard capture:\n%s", screen)

	app.SendKeys("p")
	app.WaitFor("serf / project / serf", "Recent in this project", "live task")
	app.SendKeys("Escape")
	app.WaitFor("No live sessions are running")
	app.SendKeys("q")
	app.WaitForExit()
}

func TestTUITmuxE2E_ProjectHistoryReadOnlyAndResume(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	app.WaitFor("serf live", "live task")
	app.SendKeys("Enter")
	screen := app.WaitFor("serf / project / serf", "Live now", "live task", "Recent in this project", "ended maintenance")
	for _, unwanted := range []string{"enter: send", "Prompt (optional):"} {
		if strings.Contains(screen, unwanted) {
			t.Fatalf("project view rendered composer/spawn text %q:\n%s", unwanted, screen)
		}
	}

	app.SendKeys("Down", "Enter")
	app.WaitFor("ended maintenance", "local:01PAST", "read-only", "source does not support send")

	app.SendKeys("C-o")
	app.WaitFor("serf live", "live task")
	app.SendKeys("p")
	app.WaitFor("serf / project / serf", "ended maintenance")
	app.SendKeys("Down", "r")
	app.WaitFor("resumed maintenance", "local:02RESUME", "enter: send")
	resumes := hub.WaitForResumes(t, 1)
	if resumes[0].Ref != "local:01PAST" {
		t.Fatalf("resume ref=%q, want local:01PAST", resumes[0].Ref)
	}
}

func TestTUITmuxE2E_CodexSpawnUsesHarnessModelPicker(t *testing.T) {
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

	app.WaitFor("serf live", "live task")
	app.SendKeys("n")
	app.WaitFor("Harness:  serf", "Model:    openai/gpt-5")
	app.SendKeys("Tab", "Enter")
	app.WaitFor("Harness:  codex-local", "Model:    (harness default)")
	app.SendKeys("Tab", "Enter")
	app.WaitFor("Select codex-local model", "codex-local/gpt-5.3-codex")
	app.SendKeys("Enter")
	app.WaitFor("Harness:  codex-local", "Model:    codex-local/gpt-5.3-codex")
	app.SendKeys("Tab")
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
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("live task", "local:01LIVE", "initial question", "initial answer", "tool output from e2e")

	app.TypeLine("hello from tmux")
	app.WaitFor("hello from tmux")
	if sends := hub.WaitForSends(t, 1); sends[0] != "hello from tmux" {
		t.Fatalf("send text=%q, want hello from tmux", sends[0])
	}

	app.TypeLine("/help")
	app.WaitFor("Available commands:", "/dashboard Go to live dashboard", "/theme")

	app.TypeLine("/wat")
	app.WaitFor("Unknown command: /wat. Type /help for available commands.")

	app.TypeLine("/search")
	app.WaitFor("Command palette", "live task", "ended maintenance")
	app.TypeText("ended")
	app.WaitFor("Filter: ended", "ended maintenance")
	app.SendKeys("Escape")
	app.WaitFor("enter: send")

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
	app.WaitFor("Select theme", "dark", "light")
	app.SendKeys("Escape")
	app.WaitFor("/help")

	app.TypeLine("/tasks")
	app.WaitFor("Tasks (1):", "wire tui e2e")

	app.TypeLine("/agents")
	app.WaitFor("Select transcript", "subagent inspect")
	app.SendKeys("Down", "Enter")
	app.WaitFor("Viewing subagent inspect", "subagent transcript from e2e")
	app.SendKeys("Escape")
	app.WaitFor("enter: send")

	app.TypeLine("/details")
	app.WaitFor("Session:  01LIVE", "Dir:      "+tuiE2EProjectDir)

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
	app.WaitFor("Model updated.")
	if models := hub.WaitForModels(t, 1); models[0] != "gpt-5" {
		t.Fatalf("model request=%q, want gpt-5", models[0])
	}

	app.TypeLine("/model gpt-5-mini")
	app.WaitFor("Model updated.")
	if models := hub.WaitForModels(t, 2); models[1] != "gpt-5-mini" {
		t.Fatalf("model request=%q, want gpt-5-mini", models[1])
	}

	app.TypeLine("/project")
	app.WaitFor("serf / project / serf", "Recent in this project")
	app.SendKeys("Enter")
	app.WaitFor("live task", "local:01LIVE")

	app.TypeLine("/dashboard")
	app.WaitFor("serf live", "live task")
	app.SendKeys("Enter")
	app.WaitFor("serf / project / serf")
	app.SendKeys("Enter")
	app.WaitFor("live task", "local:01LIVE")

	app.TypeLine("/clear")
	app.WaitFor("cleared session", "local:02CLEAR")
	if got := hub.WaitForActionCount(t, "clear", 1); got != 1 {
		t.Fatalf("clear count=%d, want 1", got)
	}
}

func TestTUITmuxE2E_BrowseAndFork(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("live task", "initial question", "initial answer")

	app.SendKeys("Escape")
	app.WaitFor("esc/i/q: compose", "f: fork", "▶ initial answer")
	app.SendKeys("f")
	app.WaitFor("Select a user turn to fork.")
	if forks := hub.Forks(); len(forks) != 0 {
		t.Fatalf("invalid fork selection should not call hub: %+v", forks)
	}
	app.SendKeys("i")
	app.WaitFor("enter: send")
	app.SendKeys("Escape")
	app.WaitFor("esc/i/q: compose", "▶ Select a user turn to fork.")
	app.SendKeys("k")
	app.WaitFor("▶ initial answer")
	app.SendKeys("k")
	app.WaitFor("▶ exec")
	app.SendKeys("k")
	app.WaitFor("▶  > initial question")
	app.SendKeys("f")
	app.WaitFor("Fork draft for turn 1", "> initial question")

	app.SendKeys("Enter")
	app.WaitFor("fork child", "local:02FORK")
	forks := hub.WaitForForks(t, 1)
	if forks[0].SourceTurnID != "1" {
		t.Fatalf("fork source turn=%q, want 1", forks[0].SourceTurnID)
	}
	if forks[0].EditedInput != "initial question" {
		t.Fatalf("fork edited input=%q, want initial question", forks[0].EditedInput)
	}
	app.SendKeys("C-o")
	app.WaitFor("serf live", "live task")
}

func TestTUITmuxE2E_FailedForkPreservesDraft(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetFailFork(true)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("live task", "initial question", "initial answer")

	app.SendKeys("Escape")
	app.WaitFor("esc/i/q: compose", "f: fork")
	app.SendKeys("k")
	app.SendKeys("k")
	app.SendKeys("f")
	app.WaitFor("Fork draft for turn 1", "> initial question")
	app.TypeText(" with edit")
	app.SendKeys("Enter")
	app.WaitFor("Fork failed: appwire thread/fork: fork failed", "fork draft", "> initial question with edit")
	forks := hub.WaitForForks(t, 1)
	if forks[0].EditedInput != "initial question with edit" {
		t.Fatalf("failed fork edited input=%q, want edited text", forks[0].EditedInput)
	}
	if forks[0].Label != "original before fork" {
		t.Fatalf("failed fork label=%q, want original before fork", forks[0].Label)
	}
}

func TestTUITmuxE2E_CapabilityGates(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetSessionCapabilities("01LIVE", appwire.ThreadCapabilities{})
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("live task", "initial question")

	app.SendKeys("Escape")
	app.WaitFor("esc/i/q: compose", "ctrl+o: dashboard")
	app.SendKeys("k")
	app.SendKeys("k")
	app.SendKeys("f")
	app.WaitFor("Fork is not available for this session.")
	if forks := hub.Forks(); len(forks) != 0 {
		t.Fatalf("fork should not call hub when capability is disabled: %+v", forks)
	}
	app.SendKeys("i")
	app.WaitFor("/help")

	app.TypeLine("/interrupt")
	app.WaitFor("Interrupt is not available for this session.")
	if got := hub.ActionCount("interrupt"); got != 0 {
		t.Fatalf("interrupt should not call hub when capability is disabled: %d", got)
	}

	app.TypeLine("/compact")
	app.WaitFor("Compact is not available for this session.")
	if got := hub.ActionCount("compact"); got != 0 {
		t.Fatalf("compact should not call hub when capability is disabled: %d", got)
	}

	app.TypeLine("/clear")
	app.WaitFor("Clear is not available for this session.")
	if got := hub.ActionCount("clear"); got != 0 {
		t.Fatalf("clear should not call hub when capability is disabled: %d", got)
	}

	app.TypeLine("/model gpt-5-mini")
	app.WaitFor("Model change is not available for this session.")
	if models := hub.Models(); len(models) != 0 {
		t.Fatalf("model should not call hub when capability is disabled: %+v", models)
	}

	app.TypeLine("/search")
	app.WaitFor("Command palette", "/clear", "disabled: source does not advertise clear", "/shutdown", "disabled: source does not advertise shutdown")
	app.SendKeys("Escape")

	app.TypeLine("blocked send")
	app.WaitFor("Send is not available for this session.", "> blocked send")
	if sends := hub.Sends(); len(sends) != 0 {
		t.Fatalf("send should not call hub when capability is disabled: %+v", sends)
	}
}

func TestTUITmuxE2E_SessionSearchPalettePreservesDraft(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.TypeText("draft before search")
	app.SendKeys("C-p")
	app.WaitFor("Command palette", "/search", "> draft before search")
	app.TypeText("search")
	app.SendKeys("Enter")
	app.WaitFor("Command palette", "live task", "ended maintenance", "> draft before search")
	app.SendKeys("Escape")
	app.WaitFor("enter: send", "> draft before search")
}

func TestTUITmuxE2E_ModelPickerShowsAuthRequiredModels(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetAuthRequiredModels(true)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("live task", "initial question")

	app.TypeLine("/model")
	app.WaitFor("Select model", "openai/gpt-5", "disabled: Login required", "/auth openai")
	app.SendKeys("Enter")
	app.WaitFor("Select model", "disabled: Login required")
	if models := hub.Models(); len(models) != 0 {
		t.Fatalf("auth-required model should not be selected: %+v", models)
	}
}

func TestTUITmuxE2E_SessionHeaderStatusAndComposerStates(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetSessionState("01LIVE", appwire.ThreadStatusProcessing)
	hub.SetSessionContextPressure("01LIVE", 0.66)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor(
		"live task",
		"source: serf",
		"state: processing",
		"model: gpt-5",
		"project: serf",
		"cwd: "+tuiE2EProjectDir,
		"turns: 2",
		"ctx: 66%",
		"status: hub connected",
		"steer: ready",
		"busy: turn_active",
	)

	app.TypeLine("/auth openai")
	app.WaitFor("OpenAI auth: signed out", "auth: openai signed out")

	app.TypeText("first line")
	app.SendKeys("C-j")
	app.TypeLine("second line")
	app.WaitFor("Steering sent.")
	steers := hub.WaitForSteers(t, 1)
	if steers[0].Text != "first line\nsecond line" {
		t.Fatalf("steer text=%q, want multiline input", steers[0].Text)
	}

	hub.SetSessionState("01LIVE", appwire.ThreadStatusIdle)
	app.SendKeys("C-o")
	app.WaitFor("serf live")
	openLiveSession(t, app)
	app.WaitFor("state: idle", "send: ready")

	hub.SetFailSend(true)
	app.TypeLine("send failure draft")
	app.WaitFor("Send failed: appwire turn/start: send failed", "error: Send failed: appwire turn/start: send failed", "> send failure draft")

	app.SendKeys("C-o")
	app.WaitFor("serf live")
	app.SendKeys("Enter")
	app.WaitFor("serf / project / serf")
	app.SendKeys("Down", "Enter")
	app.WaitFor("ended maintenance", "state: ended", "read-only: source does not support send")
}

func TestTUITmuxE2E_HubStreamingAssistantDeltaBeforeRefresh(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("live task", "initial answer")
	app.TypeLine("stream please")
	hub.WaitForSends(t, 1)

	hub.BroadcastAgentDelta("01LIVE", "partial live answer")
	app.WaitFor("partial live answer")

	hub.AppendAssistantFinal("01LIVE", "partial live answer done")
	app.SendKeys("C-o")
	app.WaitFor("serf live", "live task")
	openLiveSession(t, app)
	app.WaitFor("partial live answer done")
}

func TestTUITmuxE2E_HubStreamingToolGroupBeforeRefresh(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("live task", "initial answer")

	hub.BroadcastToolStarted("01LIVE")
	hub.BroadcastToolOutputDelta("01LIVE", "tmux tool output\n")
	hub.BroadcastToolCompleted("01LIVE")
	app.WaitFor("read_file", "tmux tool output")

	hub.AppendToolFinal("01LIVE")
	app.SendKeys("C-o")
	app.WaitFor("serf live", "live task")
	openLiveSession(t, app)
	app.WaitFor("read_file", "tmux tool output")
}

func TestTUITmuxE2E_APIErrorsRenderInPlace(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	defer hub.Close()
	app := startTUITmux(t, bin, hub.URL())
	defer app.Close()

	openLiveSession(t, app)
	app.WaitFor("live task", "initial question")

	hub.SetFailTasks(true)
	app.TypeLine("/tasks")
	app.WaitFor("Tasks failed", "category: appwire", "reason: appwire serf/tasks/list: tasks failed")

	hub.SetFailSend(true)
	app.TypeLine("send should fail")
	app.WaitFor("Send failed", "category: appwire", "reason: appwire turn/start: send failed", "> send should fail")

	app.SendKeys("C-o")
	app.WaitFor("serf live", "live task")
	hub.SetFailSpawn(true)
	app.SendKeys("n")
	app.WaitFor("serf / new session", "Prompt (optional):")
	app.TypeLine("spawn should fail")
	app.WaitFor("Spawn failed", "category: launch", "reason: appwire thread/start: spawn failed", "> spawn should fail")
}

func openLiveSession(t *testing.T, app *tmuxTUI) {
	t.Helper()
	app.WaitFor("serf live", "serf", "live task")
	app.SendKeys("Enter")
	screen := app.WaitFor("serf / project /", "Live now")
	if strings.Contains(screen, "serf / project / ops") {
		app.SendKeys("C-o")
		app.WaitFor("serf live", "live task")
		app.SendKeys("Down", "Down", "Enter")
		app.WaitFor("serf / project / serf", "Live now")
	}
	app.SendKeys("Enter")
}

func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is required for TUI E2E tests")
	}
}

func buildTUIBinary(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "serf-tui")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/serf-tui")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build serf-tui: %v\n%s", err, out)
	}
	return bin
}

type tmuxTUI struct {
	t       *testing.T
	session string
}

func startTUITmux(t *testing.T, bin, hubURL string) *tmuxTUI {
	t.Helper()
	return startTUITmuxSized(t, bin, hubURL, 120, 40)
}

func startTUITmuxSized(t *testing.T, bin, hubURL string, width, height int) *tmuxTUI {
	t.Helper()
	session := fmt.Sprintf("serf-tui-e2e-%d", time.Now().UnixNano())
	command := shellQuote(bin) + " -debug -no-auto-start-hub -hub-addr " + shellQuote(hubURL)
	runTmux(t, "new-session", "-d", "-x", fmt.Sprint(width), "-y", fmt.Sprint(height), "-s", session, command)
	runTmux(t, "set-option", "-t", session, "remain-on-exit", "on")
	app := &tmuxTUI{t: t, session: session}
	app.WaitFor("serf live")
	return app
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
	runTmux(a.t, "resize-window", "-t", a.session, "-x", fmt.Sprint(width), "-y", fmt.Sprint(height))
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
		time.Sleep(50 * time.Millisecond)
	}
	a.t.Fatalf("timed out waiting for %q\nvisible pane:\n%s\nrecent history:\n%s", wants, screen, a.CaptureHistory())
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
		time.Sleep(50 * time.Millisecond)
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
				{Type: "user_message", ID: "user-1", TurnID: "turn_1", Text: "initial question"},
				{Type: "tool_call", ID: "tool-1", TurnID: "turn_1", ToolName: "exec", ArgumentsJSON: `{"cmd":"echo e2e"}`, Output: "tool output from e2e", Status: "completed"},
				{Type: "agent_message", ID: "agent-1", TurnID: "turn_1", Text: "initial answer", Status: "completed"},
			},
		}, {
			ID:     "turn_active",
			Status: appwire.TurnStatusRunning,
		}},
	})
	h.sessions["01SUB"] = &tuiE2ESession{
		ID:         "01SUB",
		Title:      "subagent inspect",
		State:      appwire.ThreadStatusEnded,
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
				{Type: "agent_message", ID: "sub-agent-1", TurnID: "turn_1", Text: "subagent transcript from e2e", Status: "completed"},
			},
		}},
	}
	h.addSession(&tuiE2ESession{
		ID:           "01PAST",
		Title:        "ended maintenance",
		State:        appwire.ThreadStatusEnded,
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
				{Type: "agent_message", ID: "ops-agent-1", TurnID: "turn_1", Text: "ops transcript", Status: "completed"},
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
			s.State = appwire.ThreadStatusEnded
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
			Type:          "tool_call",
			ID:            "tool_stream",
			CallID:        "call_stream",
			TurnID:        "turn_tool",
			ToolName:      "read_file",
			ArgumentsJSON: `{"file_path":"/tmp/tmux-tool.txt"}`,
			Status:        "running",
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
			Type:          "tool_call",
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
				{Type: "agent_message", ID: "agent_stream", TurnID: "turn_stream", Text: text, Status: "completed"},
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
					Type:          "tool_call",
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
		return appwire.ThreadStartResponse{}, fmt.Errorf("spawn failed")
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
				{Type: "agent_message", ID: "spawn-agent-1", TurnID: "turn_1", Text: "spawn transcript ready", Status: "completed"},
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
				{Type: "agent_message", ID: "resume-agent-1", TurnID: "turn_1", Text: "resume transcript ready", Status: "completed"},
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
		return appwire.TurnStartResponse{}, fmt.Errorf("send failed")
	}
	h.sends = append(h.sends, params.Prompt)
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_sent", Status: appwire.TurnStatusRunning}}, nil
}

func (h *tuiE2EHub) handleTurnSteer(_ context.Context, params appwire.TurnSteerParams) (appwire.EmptyResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.steers = append(h.steers, params)
	return appwire.EmptyResponse{}, nil
}

func (h *tuiE2EHub) handleTasksList(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	h.mu.Lock()
	fail := h.failTasks
	h.mu.Unlock()
	if fail {
		return appwire.TaskListResponse{}, fmt.Errorf("tasks failed")
	}
	return appwire.TaskListResponse{Data: []agent.Task{{ID: 1, Type: agent.TaskTypeImplement, Description: "wire tui e2e", Status: agent.TaskInProgress}}}, nil
}

func (h *tuiE2EHub) handleThreadTranscriptList(_ context.Context, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
	id := threadIDFromParams(params.Ref, "")
	if id != "01LIVE" {
		return appwire.ThreadTranscriptListResponse{}, appwire.Unavailable("thread not found: " + id)
	}
	return appwire.ThreadTranscriptListResponse{Data: []appwire.ThreadTranscriptTarget{
		{Ref: "local:01LIVE", ThreadID: "01LIVE", Title: "main session (live)", Kind: "main", Status: appwire.ThreadStatusProcessing, Source: "local"},
		{Ref: "local:01SUB", ThreadID: "01SUB", Title: "subagent inspect", Kind: "subagent", Status: appwire.ThreadStatusEnded, Source: "local", TurnsUsed: 1},
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
		return appwire.ThreadForkResponse{}, fmt.Errorf("fork failed")
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
				{Type: "user_message", ID: "fork-user-1", TurnID: "turn_1", Text: params.EditedInput},
				{Type: "agent_message", ID: "fork-agent-1", TurnID: "turn_1", Text: "fork answer", Status: "completed"},
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
