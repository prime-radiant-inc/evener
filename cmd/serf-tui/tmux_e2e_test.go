package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/hubapi"
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
	if spawns[0].WorkingDir != tuiE2EProjectDir {
		t.Fatalf("dashboard spawn working_dir=%q, want %q", spawns[0].WorkingDir, tuiE2EProjectDir)
	}
	if spawns[0].Task != "spawn from dashboard" {
		t.Fatalf("dashboard spawn task=%q, want spawn from dashboard", spawns[0].Task)
	}
	if spawns[0].Model != "openai/gpt-5" {
		t.Fatalf("dashboard spawn model=%q, want openai/gpt-5", spawns[0].Model)
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
	if spawns[1].WorkingDir != tuiE2EProjectDir {
		t.Fatalf("project spawn working_dir=%q, want %q", spawns[1].WorkingDir, tuiE2EProjectDir)
	}
	if spawns[1].Task != "spawn from project" {
		t.Fatalf("project spawn task=%q, want spawn from project", spawns[1].Task)
	}
	if spawns[1].Model != "openai/gpt-5" {
		t.Fatalf("project spawn model=%q, want openai/gpt-5", spawns[1].Model)
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
	if forks[0].Turn != 1 {
		t.Fatalf("fork turn=%d, want 1", forks[0].Turn)
	}
	if forks[0].EditedMessage != "initial question" {
		t.Fatalf("fork edited_message=%q, want initial question", forks[0].EditedMessage)
	}
}

func TestTUITmuxE2E_CapabilityGates(t *testing.T) {
	requireTmux(t)
	bin := buildTUIBinary(t)
	hub := newTUIE2EHub(t)
	hub.SetSessionCapabilities("01LIVE", hubapi.SessionCapabilities{})
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
	app.WaitFor("Tasks error: hub returned 500")

	hub.SetFailSend(true)
	app.TypeLine("send should fail")
	app.WaitFor("Send failed: hub returned 500", "> send should fail")

	app.SendKeys("C-o")
	app.WaitFor("serf live", "live task")
	hub.SetFailSpawn(true)
	app.SendKeys("s")
	app.WaitFor("serf / new session", "Task (optional):")
	app.TypeLine("spawn should fail")
	app.WaitFor("error: spawn failed: hub returned 500")
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
	sessions   map[string]*tuiE2ESession
	spawns     []hubapi.SpawnRequest
	sends      []string
	actions    map[string]int
	models     []string
	forks      []hubapi.ForkRequest
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
	Capabilities hubapi.SessionCapabilities
	Events       []tuiE2ESSEEvent
}

type tuiE2ESSEEvent struct {
	Name string
	Data any
}

func newTUIE2EHub(t *testing.T) *tuiE2EHub {
	t.Helper()
	h := &tuiE2EHub{
		t:        t,
		sessions: map[string]*tuiE2ESession{},
		actions:  map[string]int{},
	}
	h.sessions["01LIVE"] = &tuiE2ESession{
		ID:         "01LIVE",
		Title:      "live task",
		State:      "idle",
		Project:    "serf",
		WorkingDir: tuiE2EProjectDir,
		Model:      "gpt-5",
		Live:       true,
		Capabilities: hubapi.SessionCapabilities{
			Send:        true,
			Interrupt:   true,
			Compact:     true,
			Clear:       true,
			Fork:        true,
			ChangeModel: true,
		},
		Events: []tuiE2ESSEEvent{
			{Name: "SESSION_START", Data: map[string]any{"session_id": "01LIVE", "model": "gpt-5", "profile": "default", "context_window_size": 200000}},
			{Name: "USER_INPUT", Data: map[string]any{"text": "initial question", "turn": 1}},
			{Name: "TOOL_CALL_START", Data: map[string]any{"call_id": "tool-1", "tool_name": "exec", "arguments_json": `{"cmd":"echo e2e"}`}},
			{Name: "TOOL_CALL_OUTPUT_DELTA", Data: map[string]any{"call_id": "tool-1", "delta": "tool output from e2e"}},
			{Name: "TOOL_CALL_END", Data: map[string]any{"call_id": "tool-1", "tool_name": "exec", "duration_ms": 10}},
			{Name: "ASSISTANT_TEXT_END", Data: map[string]any{"text": "initial answer", "usage": map[string]any{"input_tokens": 12, "output_tokens": 4}}},
			{Name: "REPLAY_DONE", Data: map[string]any{}},
		},
	}
	h.sessions["01PAST"] = &tuiE2ESession{
		ID:         "01PAST",
		Title:      "ended maintenance",
		State:      "ended",
		Project:    "serf",
		WorkingDir: tuiE2EProjectDir,
		Model:      "gpt-5",
		Live:       false,
	}
	h.sessions["01OPS"] = &tuiE2ESession{
		ID:         "01OPS",
		Title:      "ops task",
		State:      "processing",
		Project:    "ops",
		WorkingDir: "/tmp/serf-tui-e2e/ops",
		Model:      "gpt-5",
		Live:       true,
		Capabilities: hubapi.SessionCapabilities{
			Send:        true,
			Interrupt:   true,
			Compact:     true,
			Clear:       true,
			Fork:        true,
			ChangeModel: true,
		},
		Events: []tuiE2ESSEEvent{
			{Name: "SESSION_START", Data: map[string]any{"session_id": "01OPS", "model": "gpt-5", "profile": "default"}},
			{Name: "ASSISTANT_TEXT_END", Data: map[string]any{"text": "ops transcript"}},
			{Name: "REPLAY_DONE", Data: map[string]any{}},
		},
	}
	h.server = httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	return h
}

func (h *tuiE2EHub) URL() string {
	return h.server.URL
}

func (h *tuiE2EHub) Close() {
	h.server.Close()
}

func (h *tuiE2EHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/health":
		writeTUIE2EJSON(w, hubapi.HealthResponse{
			Version: "e2e",
			HubAddr: strings.TrimPrefix(h.server.URL, "http://"),
			Capabilities: hubapi.HealthCapabilities{
				Tree:             true,
				TranscriptFollow: true,
				Spawn:            true,
				Fork:             true,
			},
		})
	case r.Method == http.MethodGet && r.URL.Path == "/api/tree":
		h.mu.Lock()
		h.treeGets++
		h.mu.Unlock()
		writeTUIE2EJSON(w, h.tree())
	case r.Method == http.MethodGet && r.URL.Path == "/api/models":
		writeTUIE2EJSON(w, []hubapi.ModelOption{{Provider: "openai", Model: "gpt-5"}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/spawn":
		h.handleSpawn(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/sessions/"):
		h.handleSession(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *tuiE2EHub) tree() hubapi.TreeResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	projects := map[string]*hubapi.TreeProject{}
	for _, s := range h.sessions {
		p := projects[s.Project]
		if p == nil {
			p = &hubapi.TreeProject{
				Key:         hubProjectKey(s.Project),
				Name:        s.Project,
				WorkingDir:  s.WorkingDir,
				RollupState: s.State,
			}
			projects[s.Project] = p
		}
		p.Sessions = append(p.Sessions, hubapi.TreeNode{
			Ref:       hubapi.LocalRef(s.ID).String(),
			HostID:    "local",
			SessionID: s.ID,
			Title:     s.Title,
			Project:   s.Project,
			State:     s.State,
			Live:      s.Live,
			Model:     s.Model,
			Age:       "now",
		})
	}
	out := hubapi.TreeResponse{
		Sources: []hubapi.Source{{ID: "local", Label: "local", Kind: "local", Online: true}},
	}
	for _, name := range []string{"serf", "ops"} {
		if p := projects[name]; p != nil {
			out.Projects = append(out.Projects, *p)
		}
	}
	return out
}

func (h *tuiE2EHub) handleSpawn(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	fail := h.failSpawn
	h.mu.Unlock()
	if fail {
		http.Error(w, "spawn failed", http.StatusInternalServerError)
		return
	}
	var req hubapi.SpawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	h.spawnCount++
	id := fmt.Sprintf("02SPAWN%d", h.spawnCount)
	h.spawns = append(h.spawns, req)
	h.sessions[id] = &tuiE2ESession{
		ID:         id,
		Title:      fmt.Sprintf("spawned session %d", h.spawnCount),
		State:      "idle",
		Project:    "serf",
		WorkingDir: req.WorkingDir,
		Model:      req.Model,
		Live:       true,
		Capabilities: hubapi.SessionCapabilities{
			Send:        true,
			Interrupt:   true,
			Compact:     true,
			Clear:       true,
			Fork:        true,
			ChangeModel: true,
		},
		Events: []tuiE2ESSEEvent{
			{Name: "SESSION_START", Data: map[string]any{"session_id": id, "model": "gpt-5", "profile": "default"}},
			{Name: "ASSISTANT_TEXT_END", Data: map[string]any{"text": "spawn transcript ready"}},
			{Name: "REPLAY_DONE", Data: map[string]any{}},
		},
	}
	h.mu.Unlock()
	ref := hubapi.LocalRef(id)
	writeTUIE2EJSON(w, hubapi.SpawnResponse{Ref: ref.String(), HostID: ref.HostID, SessionID: ref.SessionID})
}

func (h *tuiE2EHub) handleSession(w http.ResponseWriter, r *http.Request) {
	ref, rest, ok := parseTUIE2ESessionPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && rest == "":
		h.writeSessionDetail(w, ref.SessionID)
	case r.Method == http.MethodGet && rest == "events":
		h.writeSessionEvents(w, ref.SessionID)
	case r.Method == http.MethodPost && rest == "send":
		h.handleSend(w, r, ref.SessionID)
	case r.Method == http.MethodGet && rest == "tasks":
		h.mu.Lock()
		fail := h.failTasks
		h.mu.Unlock()
		if fail {
			http.Error(w, "tasks failed", http.StatusInternalServerError)
			return
		}
		writeTUIE2EJSON(w, []agent.Task{{ID: 1, Type: agent.TaskTypeImplement, Description: "wire tui e2e", Status: agent.TaskInProgress}})
	case r.Method == http.MethodPost && rest == "interrupt":
		h.recordAction("interrupt")
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && rest == "compact":
		h.recordAction("compact")
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && rest == "model":
		h.handleModel(w, r)
	case r.Method == http.MethodPost && rest == "clear":
		h.handleClear(w)
	case r.Method == http.MethodPost && rest == "fork":
		h.handleFork(w, r)
	default:
		http.NotFound(w, r)
	}
}

func parseTUIE2ESessionPath(path string) (hubapi.Ref, string, bool) {
	rest := strings.TrimPrefix(path, "/api/sessions/")
	refPart, suffix, _ := strings.Cut(rest, "/")
	rawRef, err := url.PathUnescape(refPart)
	if err != nil {
		return hubapi.Ref{}, "", false
	}
	ref, err := hubapi.ParseRef(rawRef)
	if err != nil {
		return hubapi.Ref{}, "", false
	}
	return ref, suffix, true
}

func (h *tuiE2EHub) writeSessionDetail(w http.ResponseWriter, id string) {
	h.mu.Lock()
	s := h.sessions[id]
	h.mu.Unlock()
	if s == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	ref := hubapi.LocalRef(s.ID)
	writeTUIE2EJSON(w, hubapi.SessionDetail{
		Ref:          ref.String(),
		HostID:       ref.HostID,
		SessionID:    ref.SessionID,
		Title:        s.Title,
		State:        s.State,
		Live:         s.Live,
		Project:      s.Project,
		WorkingDir:   s.WorkingDir,
		Model:        s.Model,
		Profile:      "default",
		TurnCount:    1,
		Capabilities: s.Capabilities,
		Streams: hubapi.SessionStreams{
			TranscriptFollow: "/api/sessions/" + ref.PathEscaped() + "/events?mode=transcript-follow",
		},
	})
}

func (h *tuiE2EHub) writeSessionEvents(w http.ResponseWriter, id string) {
	h.mu.Lock()
	s := h.sessions[id]
	h.mu.Unlock()
	if s == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	for i, ev := range s.Events {
		data, err := json.Marshal(ev.Data)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", i+1, ev.Name, data)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (h *tuiE2EHub) handleSend(w http.ResponseWriter, r *http.Request, _ string) {
	h.mu.Lock()
	fail := h.failSend
	h.mu.Unlock()
	if fail {
		http.Error(w, "send failed", http.StatusInternalServerError)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	h.sends = append(h.sends, body.Text)
	h.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (h *tuiE2EHub) handleModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	h.models = append(h.models, body.Model)
	h.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (h *tuiE2EHub) handleClear(w http.ResponseWriter) {
	h.recordAction("clear")
	id := "02CLEAR"
	h.mu.Lock()
	h.sessions[id] = &tuiE2ESession{
		ID:         id,
		Title:      "cleared session",
		State:      "idle",
		Project:    "serf",
		WorkingDir: tuiE2EProjectDir,
		Model:      "gpt-5",
		Live:       true,
		Capabilities: hubapi.SessionCapabilities{
			Send:        true,
			Interrupt:   true,
			Compact:     true,
			Clear:       true,
			Fork:        true,
			ChangeModel: true,
		},
		Events: []tuiE2ESSEEvent{
			{Name: "SESSION_START", Data: map[string]any{"session_id": id, "model": "gpt-5", "profile": "default"}},
			{Name: "REPLAY_DONE", Data: map[string]any{}},
		},
	}
	h.mu.Unlock()
	ref := hubapi.LocalRef(id)
	writeTUIE2EJSON(w, hubapi.RefResponse{Ref: ref.String(), HostID: ref.HostID, SessionID: ref.SessionID})
}

func (h *tuiE2EHub) handleFork(w http.ResponseWriter, r *http.Request) {
	var req hubapi.ForkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := "02FORK"
	h.mu.Lock()
	h.forks = append(h.forks, req)
	h.sessions[id] = &tuiE2ESession{
		ID:         id,
		Title:      "fork child",
		State:      "idle",
		Project:    "serf",
		WorkingDir: tuiE2EProjectDir,
		Model:      "gpt-5",
		Live:       true,
		Capabilities: hubapi.SessionCapabilities{
			Send:        true,
			Interrupt:   true,
			Compact:     true,
			Clear:       true,
			Fork:        true,
			ChangeModel: true,
		},
		Events: []tuiE2ESSEEvent{
			{Name: "SESSION_START", Data: map[string]any{"session_id": id, "model": "gpt-5", "profile": "default"}},
			{Name: "USER_INPUT", Data: map[string]any{"text": req.EditedMessage, "turn": 1}},
			{Name: "ASSISTANT_TEXT_END", Data: map[string]any{"text": "fork answer"}},
			{Name: "REPLAY_DONE", Data: map[string]any{}},
		},
	}
	h.mu.Unlock()
	ref := hubapi.LocalRef(id)
	writeTUIE2EJSON(w, hubapi.RefResponse{Ref: ref.String(), HostID: ref.HostID, SessionID: ref.SessionID})
}

func (h *tuiE2EHub) recordAction(action string) {
	h.mu.Lock()
	h.actions[action]++
	h.mu.Unlock()
}

func (h *tuiE2EHub) SetSessionCapabilities(id string, caps hubapi.SessionCapabilities) {
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

func (h *tuiE2EHub) Forks() []hubapi.ForkRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hubapi.ForkRequest(nil), h.forks...)
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

func (h *tuiE2EHub) WaitForSpawns(t *testing.T, count int) []hubapi.SpawnRequest {
	t.Helper()
	var out []hubapi.SpawnRequest
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		out = append([]hubapi.SpawnRequest(nil), h.spawns...)
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

func (h *tuiE2EHub) WaitForForks(t *testing.T, count int) []hubapi.ForkRequest {
	t.Helper()
	var out []hubapi.ForkRequest
	h.waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		out = append([]hubapi.ForkRequest(nil), h.forks...)
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

func writeTUIE2EJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(err)
	}
}
