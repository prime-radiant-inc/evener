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
	initialTreeRequests := hub.WaitForTreeRequests(t, 1)
	app.SendKeys("r")
	hub.WaitForTreeRequests(t, initialTreeRequests+1)

	app.SendKeys("Enter")
	app.WaitFor("serf / project / serf", "Live now", "live task", "Recent in this project", "ended maintenance")

	app.SendKeys("Escape")
	app.WaitFor("serf live", "live task")
	app.SendKeys("p")
	app.WaitFor("serf / project / serf", "Recent in this project")
	app.SendKeys("Escape")
	app.WaitFor("serf live", "live task")

	app.SendKeys("s")
	app.WaitFor("serf / new session", "Dir:      "+tuiE2EProjectDir, "Task (optional):")
	app.TypeLine("spawn from dashboard")
	app.WaitFor("spawned session 1", "local:02SPAWN1")
	spawns := hub.WaitForSpawns(t, 1)
	if spawns[0].CWD != tuiE2EProjectDir {
		t.Fatalf("dashboard spawn cwd=%q, want %q", spawns[0].CWD, tuiE2EProjectDir)
	}
	if spawns[0].Prompt != "spawn from dashboard" {
		t.Fatalf("dashboard spawn prompt=%q, want spawn from dashboard", spawns[0].Prompt)
	}
	if spawns[0].ModelProvider != "openai" || spawns[0].Model != "gpt-5" {
		t.Fatalf("dashboard spawn model=%s/%s, want openai/gpt-5", spawns[0].ModelProvider, spawns[0].Model)
	}

	app.SendKeys("C-o")
	app.WaitFor("serf live", "live task")
	app.SendKeys("Enter")
	app.WaitFor("serf / project / serf", "Recent in this project")
	app.SendKeys("s")
	app.WaitFor("serf / new session", "Dir:      "+tuiE2EProjectDir, "Task (optional):")
	app.TypeLine("spawn from project")
	app.WaitFor("spawned session 2", "local:02SPAWN2")
	spawns = hub.WaitForSpawns(t, 2)
	if spawns[1].CWD != tuiE2EProjectDir {
		t.Fatalf("project spawn cwd=%q, want %q", spawns[1].CWD, tuiE2EProjectDir)
	}
	if spawns[1].Prompt != "spawn from project" {
		t.Fatalf("project spawn prompt=%q, want spawn from project", spawns[1].Prompt)
	}
	if spawns[1].ModelProvider != "openai" || spawns[1].Model != "gpt-5" {
		t.Fatalf("project spawn model=%s/%s, want openai/gpt-5", spawns[1].ModelProvider, spawns[1].Model)
	}

	app.SendKeys("C-o")
	app.WaitFor("serf live", "live task")
	app.SendKeys("q")
	app.WaitForExit()
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
	app.WaitFor("Available commands:", "/dashboard Go to live dashboard")

	app.TypeLine("/tasks")
	app.WaitFor("Tasks (1):", "wire tui e2e")

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

	app.TypeLine("/model gpt-5-mini")
	app.WaitFor("Model updated.")
	if models := hub.WaitForModels(t, 1); models[0] != "gpt-5-mini" {
		t.Fatalf("model request=%q, want gpt-5-mini", models[0])
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
	app.WaitFor("esc/i/q: compose", "f: fork")
	app.SendKeys("k")
	app.SendKeys("k")
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
	app.WaitFor("esc/i/q: compose", "f: fork")
	app.SendKeys("k")
	app.SendKeys("k")
	app.SendKeys("f")
	app.WaitFor("Fork is not available for this session.")
	if forks := hub.Forks(); len(forks) != 0 {
		t.Fatalf("fork should not call hub when capability is disabled: %+v", forks)
	}
	app.SendKeys("i")
	app.WaitFor("enter: send", "/help")

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

	app.TypeLine("blocked send")
	app.WaitFor("Send is not available for this session.", "> blocked send")
	if sends := hub.Sends(); len(sends) != 0 {
		t.Fatalf("send should not call hub when capability is disabled: %+v", sends)
	}
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
	app.WaitFor("Tasks error: appwire serf/tasks/list: tasks failed")

	hub.SetFailSend(true)
	app.TypeLine("send should fail")
	app.WaitFor("Send failed: appwire turn/start: send failed", "> send should fail")

	app.SendKeys("C-o")
	app.WaitFor("serf live", "live task")
	hub.SetFailSpawn(true)
	app.SendKeys("s")
	app.WaitFor("serf / new session", "Task (optional):")
	app.TypeLine("spawn should fail")
	app.WaitFor("error: spawn failed: appwire thread/start: spawn failed")
}

func openLiveSession(t *testing.T, app *tmuxTUI) {
	t.Helper()
	app.WaitFor("serf live", "serf", "live task")
	app.SendKeys("Enter")
	app.WaitFor("serf / project / serf", "Live now")
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
	session := fmt.Sprintf("serf-tui-e2e-%d", time.Now().UnixNano())
	command := shellQuote(bin) + " -debug -no-auto-start-hub -hub-addr " + shellQuote(hubURL)
	runTmux(t, "new-session", "-d", "-x", "120", "-y", "40", "-s", session, command)
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
	runTmux(a.t, "send-keys", "-t", a.session, "-l", text)
	runTmux(a.t, "send-keys", "-t", a.session, "Enter")
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

func (a *tmuxTUI) WaitFor(wants ...string) string {
	a.t.Helper()
	deadline := time.Now().Add(tuiE2EWaitTimeout)
	var screen string
	for time.Now().Before(deadline) {
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

func (a *tmuxTUI) WaitForExit() {
	a.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("tmux", "has-session", "-t", a.session).Run(); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	a.t.Fatalf("tmux session did not exit\nvisible pane:\n%s\nrecent history:\n%s", a.Capture(), a.CaptureHistory())
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

	mu         sync.Mutex
	order      []string
	sessions   map[string]*tuiE2ESession
	spawns     []appwire.ThreadStartParams
	sends      []string
	actions    map[string]int
	models     []string
	forks      []appwire.ThreadForkParams
	spawnCount int
	treeGets   int
	failTasks  bool
	failSend   bool
	failSpawn  bool
}

type tuiE2ESession struct {
	ID           string
	Title        string
	State        string
	Project      string
	WorkingDir   string
	Model        string
	Live         bool
	Capabilities appwire.ThreadCapabilities
	Turns        []appwire.Turn
}

func newTUIE2EHub(t *testing.T) *tuiE2EHub {
	t.Helper()
	h := &tuiE2EHub{
		t:        t,
		sessions: map[string]*tuiE2ESession{},
		actions:  map[string]int{},
	}
	h.addSession(&tuiE2ESession{
		ID:           "01LIVE",
		Title:        "live task",
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
				{Type: "user_message", ID: "user-1", TurnID: "turn_1", Text: "initial question"},
				{Type: "tool_call", ID: "tool-1", TurnID: "turn_1", ToolName: "exec", ArgumentsJSON: `{"cmd":"echo e2e"}`, Output: "tool output from e2e", Status: "completed"},
				{Type: "agent_message", ID: "agent-1", TurnID: "turn_1", Text: "initial answer", Status: "completed"},
			},
		}},
	})
	h.addSession(&tuiE2ESession{
		ID:         "01PAST",
		Title:      "ended maintenance",
		State:      appwire.ThreadStatusEnded,
		Project:    "serf",
		WorkingDir: tuiE2EProjectDir,
		Model:      "gpt-5",
		Live:       false,
	})
	h.addSession(&tuiE2ESession{
		ID:           "01OPS",
		Title:        "ops task",
		State:        appwire.ThreadStatusProcessing,
		Project:      "ops",
		WorkingDir:   "/tmp/serf-tui-e2e/ops",
		Model:        "gpt-5",
		Live:         true,
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
	appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, h.handleThreadStart)
	appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, h.handleTurnStart)
	appserver.HandleTyped(app.Router(), appwire.MethodSerfTasksList, h.handleTasksList)
	appserver.HandleTyped(app.Router(), appwire.MethodTurnInterrupt, h.handleTurnInterrupt)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadCompactStart, h.handleThreadCompactStart)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadModelSet, h.handleThreadModelSet)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadClear, h.handleThreadClear)
	appserver.HandleTyped(app.Router(), appwire.MethodThreadFork, h.handleThreadFork)
}

func (h *tuiE2EHub) URL() string {
	return h.server.URL
}

func (h *tuiE2EHub) Close() {
	h.server.Close()
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

func (h *tuiE2EHub) handleThreadRead(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	id := threadIDFromParams(params.Ref, params.ThreadID)
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[id]
	if s == nil {
		return appwire.ThreadReadResponse{}, appwire.Unavailable("thread not found: " + id)
	}
	return appwire.ThreadReadResponse{Thread: h.threadFromSessionLocked(s)}, nil
}

func (h *tuiE2EHub) handleModelList(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
	return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
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

func (h *tuiE2EHub) handleTurnStart(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failSend {
		return appwire.TurnStartResponse{}, fmt.Errorf("send failed")
	}
	h.sends = append(h.sends, params.Prompt)
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_sent", Status: appwire.TurnStatusRunning}}, nil
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
		CWD:           s.WorkingDir,
		Source:        "local",
		Status:        appwire.ThreadStatus{Type: status},
		Turns:         append([]appwire.Turn(nil), s.Turns...),
		Serf: appwire.SerfThread{
			Ref:          appwire.Ref{SourceID: "local", ThreadID: s.ID}.String(),
			Profile:      "default",
			Capabilities: s.Capabilities,
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

func (h *tuiE2EHub) SetSessionCapabilities(id string, caps appwire.ThreadCapabilities) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[id]; s != nil {
		s.Capabilities = caps
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
