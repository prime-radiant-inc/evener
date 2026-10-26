//go:build browserguard

// TestSkillBrowserDaemonHelper is the daemon half of the skill browser guard:
// a compiled `go test -tags browserguard -c` binary of this package that, when
// launched with -skill-browser-config <file>, runs a REAL `evener serve`
// daemon (runServeWithDeps with the production dependency set) whose only
// injected piece is the external LLM provider adapter. The adapter records
// every actual provider request to a JSONL log and answers with scripted model
// output; a control JSONL file synchronizes its calls (hold/release) and the
// daemon's shutdown. All of this is fixture IPC owned by cmd/evener-hub's
// TestSkillComposerBrowser — never production RPC, never a testing endpoint
// in serve.go itself.
//
// The helper NEVER runs without its explicit test flag: a plain
// `go test -tags browserguard ./cmd/evener` skips it, so the only way a daemon
// starts is the browser guard's own fixture handing it a config file.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

// skillBrowserConfigFlag is the helper's explicit opt-in flag. Registering it
// on flag.CommandLine is what lets the compiled test binary accept it on its
// own command line; the testing package's own flag parse then fills it.
var skillBrowserConfigFlag = flag.String("skill-browser-config", "", "private JSON config file naming workDir/stateDir/runDir/requestLog/controlPath; without it this helper does nothing")

// skillBrowserFixturePluginDir is the plugin root the Go owner lays out under
// each helper's workDir before launching it: a plugin named "pkg" whose
// skills/probe/SKILL.md is the catalog skill "pkg:probe" the browser scenarios
// select. Deriving it from workDir keeps the config file's shape exactly the
// brief's five fields.
const skillBrowserFixturePluginDir = "fixture-plugin"

// skillBrowserFixturePluginName matches the fixture plugin's manifest name
// and the --enabled-plugins selection the helper passes to serve.
const skillBrowserFixturePluginName = "pkg"

// serveTestTurnSeq mints a UNIQUE client mutation ID per fixture turn. The
// daemon dedupes turn/start by that ID and rejects a reused ID carrying a
// different payload, so a constant ID would strand every turn after the first
// (the background guard starts six sustained turns on one daemon).
var serveTestTurnSeq atomic.Int64

// serveTestCommunicateSeq mints a unique communicate tool-call ID per turn so
// the daemon's per-call bookkeeping never confuses two turns' terminal calls.
var serveTestCommunicateSeq atomic.Int64

// skillBrowserHelperConfig is the private JSON file -skill-browser-config
// names. The Go owner (TestSkillComposerBrowser) writes it before launching
// this binary and owns every path in it.
type skillBrowserHelperConfig struct {
	WorkDir     string `json:"workDir"`
	StateDir    string `json:"stateDir"`
	RunDir      string `json:"runDir"`
	RequestLog  string `json:"requestLog"`
	ControlPath string `json:"controlPath"`
}

// skillBrowserReply is the scripted model output every ordinary turn returns.
// The reply's presence in the UI is how the driver observes a completed turn;
// its content is fixture data, not an assertion oracle.
const skillBrowserReply = "skillguard turn complete"

// TestSkillBrowserDaemonHelper starts the serve daemon when (and only when)
// the browser guard's config flag is present. See the file comment for the
// contract.
func TestSkillBrowserDaemonHelper(t *testing.T) {
	if *skillBrowserConfigFlag == "" {
		t.Skip("skill browser helper daemon runs only with -skill-browser-config (fixture for TestSkillComposerBrowser)")
	}
	raw, err := os.ReadFile(*skillBrowserConfigFlag)
	if err != nil {
		t.Fatalf("read -skill-browser-config file: %v", err)
	}
	var cfg skillBrowserHelperConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse helper config: %v", err)
	}
	for name, value := range map[string]string{
		"workDir":     cfg.WorkDir,
		"stateDir":    cfg.StateDir,
		"runDir":      cfg.RunDir,
		"requestLog":  cfg.RequestLog,
		"controlPath": cfg.ControlPath,
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("helper config field %q is empty", name)
		}
	}
	isolateSkillBrowserHelperEnv(t, cfg)

	adapter, err := newSkillBrowserAdapter(cfg.RequestLog)
	if err != nil {
		t.Fatalf("open request log %s: %v", cfg.RequestLog, err)
	}
	t.Cleanup(adapter.close)

	// The ONLY injection: the external provider adapter. Default session
	// creation, server, registration, bridge, routing and persistence all stay
	// production.
	deps := defaultServeDeps()
	deps.newClient = func(string, io.Writer) (*llm.Client, func() error, error) {
		client := llm.NewClient()
		client.Register(adapter)
		return client, func() error { return nil }, nil
	}

	args := []string{
		"--model", "openai/gpt-test",
		"--addr", "127.0.0.1:0",
		"--dir", cfg.WorkDir,
		"--state-dir", cfg.StateDir,
		"--run-dir", cfg.RunDir,
		"--plugin-root", filepath.Join(cfg.StateDir, "plugin-store"),
	}
	// Comic-only fixture configuration at the external CLI/provider boundary.
	// Other guards keep their original five-field config and unsandboxed daemon.
	if raw, err := os.ReadFile(filepath.Join(cfg.WorkDir, "comic-interactions.json")); err == nil {
		var config struct{ OutsideDir string }
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		adapter.comicOutsideDir = config.OutsideDir
		args = append(args, "--sandbox", "workspace-write")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(cfg.WorkDir, skillBrowserFixturePluginDir)
	if info, statErr := os.Stat(filepath.Join(pluginDir, ".claude-plugin", "plugin.json")); statErr == nil && !info.IsDir() {
		args = append(args,
			"--plugin-dir", pluginDir,
			"--enabled-plugins="+skillBrowserFixturePluginName,
		)
	}

	serveDone := make(chan error, 1)
	go func() { serveDone <- runServeWithDeps(args, deps) }()

	entry := waitForSkillBrowserRendezvous(t, cfg.RunDir)

	controls := newSkillBrowserControls(cfg.ControlPath, adapter)
	controls.startTurn = func(ctx context.Context, prompt string) error {
		if prompt == "" {
			prompt = backgroundTargetPrompt
		}
		return startServeTestDaemonTurn(ctx, controls.observerCtx, entry.Address, entry.SessionID, prompt,
			func(ref, turnID, status string, ok bool, errMsg string) {
				adapter.ackTurnComplete(ref, turnID, prompt, ok, status, errMsg)
			})
	}
	controlDone := controls.run(t)

	select {
	case <-controls.shutdown:
		// Fixture IPC shutdown: the Go owner asked this daemon to stop. Use the
		// real AppWire thread/shutdown path (the same one the production web UI
		// uses), bounded by a context deadline.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := shutdownServeTestDaemon(ctx, entry.Address, entry.SessionID); err != nil {
			cancel()
			t.Fatalf("helper self-shutdown: %v", err)
		}
		cancel()
	case err := <-serveDone:
		controls.stop()
		<-controlDone
		if err != nil {
			t.Fatalf("serve daemon exited early: %v", err)
		}
		t.Fatalf("serve daemon exited before the shutdown control command")
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("runServeWithDeps: %v", err)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("serve daemon did not exit within 45s of the shutdown control command")
	}
	controls.stop()
	<-controlDone
}

// isolateSkillBrowserHelperEnv gives this daemon its own HOME/XDG roots under
// its configured state dir. TestMain already moved the process off the
// developer's real home; this makes the two helpers the browser guard starts
// isolated from each other too, so neither's config/cache/plugin state can
// collide with the other's.
// startServeTestDaemonTurn dials THIS daemon's own AppWire and dispatches a
// real turn/start, exactly as a UI client would. The fixture uses it to produce
// the target session's text while its web pane is never opened, so the web app
// observes it only through its background subscription.
func startServeTestDaemonTurn(
	setupCtx, observeCtx context.Context,
	address, sessionID, text string,
	ack func(ref, turnID, status string, ok bool, errMsg string),
) error {
	transport, err := appwire.DialWebSocket(setupCtx, "ws://"+address+"/rpc", http.DefaultClient)
	if err != nil {
		return err
	}
	client := appwire.NewClient(transport)
	defer client.Close()
	client.Start(context.WithoutCancel(setupCtx))
	if _, err := client.Initialize(setupCtx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serve-test-turn", Version: "test"},
	}); err != nil {
		return err
	}
	threads, err := client.ThreadList(setupCtx, appwire.ThreadListParams{})
	if err != nil {
		return err
	}
	if len(threads.Data) > 0 {
		sessionID = threads.Data[0].ID
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: sessionID}.String()
	// Subscribe BEFORE TurnStart so this connection captures the thread's
	// subscription and receives its lifecycle pushes (thread/status/changed);
	// a plain thread/read is snapshot-only and pushes nothing.
	read, err := client.ThreadRead(setupCtx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, Subscribe: true})
	if err != nil {
		return err
	}
	started, err := client.TurnStart(setupCtx, appwire.TurnStartParams{
		Ref:                ref,
		ClientMutationID:   fmt.Sprintf("fixture_background_turn_start_%d", serveTestTurnSeq.Add(1)),
		ExpectedInstanceID: read.Thread.Evener.InstanceID,
		Input:              []appwire.InputItem{{Type: "text", Text: text}},
	})
	if err != nil {
		return err
	}
	turnID := started.Turn.ID
	if turnID == "" {
		ack(ref, "", "", false, "turn/start returned no turn id")
		return nil
	}
	status, ok, errMsg := observeServeTestTurn(observeCtx, client, ref, turnID)
	ack(ref, turnID, status, ok, errMsg)
	return nil
}

// observeServeTestTurn waits until the SUBMITTED turn reaches a terminal state,
// read from the daemon's own thread/status/changed push and a thread/read of
// that exact turn. An accepted turn/start, a historical prompt match, a global
// write, or a bare idle is not success: only the turn's own terminal status is.
func observeServeTestTurn(ctx context.Context, client *appwire.Client, ref, turnID string) (string, bool, string) {
	for {
		select {
		case n, ok := <-client.Notifications():
			if !ok {
				return "", false, "appwire notifications closed before the turn completed"
			}
			if n.Method != appwire.NotifyThreadStatusChanged {
				continue
			}
			var params appwire.ThreadStatusChangedParams
			if err := json.Unmarshal(n.Params, &params); err != nil {
				continue
			}
			if params.Ref != ref || params.Status.Type == appwire.ThreadStatusActive {
				continue
			}
			// The session left active. The idle push can precede the
			// transcript settling, so poll the turn's own status briefly; this
			// is a bounded condition-watch, not a widened deadline.
			for attempt := 0; attempt < 20; attempt++ {
				status, found, errMsg := readServeTestTurnOutcome(ctx, client, ref, turnID)
				if errMsg != "" {
					return "", false, errMsg
				}
				if found && status != appwire.TurnStatusInProgress {
					if status == appwire.TurnStatusCompleted {
						return status, true, ""
					}
					return status, false, fmt.Sprintf("turn %s ended %s", turnID, status)
				}
				select {
				case <-ctx.Done():
					return "", false, "completion observer cancelled"
				case <-time.After(250 * time.Millisecond):
				}
			}
			return "", false, fmt.Sprintf("turn %s never reached a terminal transcript state", turnID)
		case <-ctx.Done():
			return "", false, "completion observer cancelled"
		}
	}
}

// readServeTestTurnOutcome reads the submitted turn's status from a fresh
// thread/read, bounded so a stalled read cannot outlive teardown.
func readServeTestTurnOutcome(ctx context.Context, client *appwire.Client, ref, turnID string) (string, bool, string) {
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	read, err := client.ThreadRead(readCtx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
	if err != nil {
		return "", false, fmt.Sprintf("thread/read after idle: %v", err)
	}
	for _, turn := range read.Thread.Turns {
		if turn.ID == turnID {
			return turn.Status, true, ""
		}
	}
	return "", false, ""
}

func isolateSkillBrowserHelperEnv(t *testing.T, cfg skillBrowserHelperConfig) {
	t.Helper()
	home := filepath.Join(cfg.StateDir, "helper-home")
	for _, dir := range []string{
		home,
		filepath.Join(home, "config"),
		filepath.Join(home, "state"),
		filepath.Join(home, "cache"),
		filepath.Join(cfg.StateDir, "tmp"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("prepare helper env dir %s: %v", dir, err)
		}
	}
	for key, value := range map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, "config"),
		"XDG_STATE_HOME":  filepath.Join(home, "state"),
		"XDG_CACHE_HOME":  filepath.Join(home, "cache"),
		"TMPDIR":          filepath.Join(cfg.StateDir, "tmp"),
	} {
		if err := os.Setenv(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	// --state-dir is authoritative for this daemon; drop TestMain's shared
	// throwaway override so no path resolution can fall back to it.
	_ = os.Unsetenv("EVENER_STATE_DIR")
}

// waitForSkillBrowserRendezvous waits for THIS process's own rendezvous entry
// (both browser-guard helpers share one run dir, so the entry is matched by
// PID, not by first-found). Filesystem notifications instead of sleep-polling,
// mirroring waitForServeTestRendezvous.
func waitForSkillBrowserRendezvous(t *testing.T, runDir string) rendezvous.Entry {
	t.Helper()
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run dir %s: %v", runDir, err)
	}
	pid := os.Getpid()
	find := func() (rendezvous.Entry, bool) {
		entries, _ := rendezvous.List(runDir)
		for _, e := range entries {
			if e.PID == pid && e.Address != "" && e.SessionID != "" {
				return e, true
			}
		}
		return rendezvous.Entry{}, false
	}
	if e, ok := find(); ok {
		return e
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	defer watcher.Close()
	if err := watcher.Add(runDir); err != nil {
		t.Fatalf("watch %s: %v", runDir, err)
	}
	// Close the TOCTOU window the same way waitForServeTestRendezvous does.
	if e, ok := find(); ok {
		return e
	}
	deadline := time.After(20 * time.Second)
	for {
		select {
		case _, ok := <-watcher.Events:
			if !ok {
				t.Fatal("rendezvous watcher closed unexpectedly")
			}
			if e, found := find(); found {
				return e
			}
		case werr := <-watcher.Errors:
			t.Fatalf("rendezvous watcher error: %v", werr)
		case <-deadline:
			t.Fatalf("this helper's rendezvous entry never appeared in %s after 20s", runDir)
			return rendezvous.Entry{}
		}
	}
}

// skillBrowserRequestRecord is one JSONL line the adapter writes per ACTUAL
// provider request. Kind separates the session-namer side call (a json_schema
// naming request with no tools) from ordinary turn dispatches, so the Go
// owner can count real turns without the naming call polluting the count.
type skillBrowserRequestRecord struct {
	Event   string      `json:"event"` // "request"
	Seq     int         `json:"seq"`
	Kind    string      `json:"kind"` // "turn" | "session-namer"
	At      string      `json:"at"`
	Held    bool        `json:"held"` // this request found the hold gate armed
	Request llm.Request `json:"request"`
}

// skillBrowserAdapter implements the real llm.ProviderAdapter interface. It
// records every actual request it receives and returns scripted model output;
// nothing else about the daemon is faked.
type skillBrowserAdapter struct {
	name string

	mu        sync.Mutex
	log       io.Writer
	seq       int
	holdArmed bool
	gate      chan struct{}

	// streaming is the explicit fixture mode for the whole-journey guard.
	// The skill guard never arms it (it writes no background command), so its
	// turns keep taking the Complete path exactly as before.
	streaming bool
	// phases holds one one-shot release channel per named background phase.
	// The Go owner releases a phase through the control file only after it has
	// observed the milestone the phase gates on.
	phases map[string]chan struct{}
	// role selects the scripted turn shape: "sustained" emits the large
	// workload before any phase release (so it fills the resident budget), and
	// "target" (the default) emits the before text small and then a real tool
	// call after the after-eviction release.
	role string
	// autoPhases, when set, releases every background phase immediately, so
	// history and gap turns complete without the driver gating each one. The
	// batching scenario leaves it off and uses acknowledged held phases.
	autoPhases      bool
	comicOutsideDir string
}

// Fixture sentinels scripted across the provider boundary by the whole-journey
// guard. They are opaque data, never prose: the Go owner
// (TestBackgroundTranscriptsEntrySliceBrowser) asserts its own copies
// independently.
const (
	backgroundBeforeEviction = "BACKGROUND_BEFORE_EVICTION"
	backgroundAfterEviction  = "BACKGROUND_AFTER_EVICTION"
	backgroundToolOutput     = "BACKGROUND_TOOL_OUTPUT"
	backgroundToolDone       = "BACKGROUND_TOOL_DONE"
	// backgroundToolCommand is the real shell invocation the scripted model
	// requests; its stdout is the tool output the driver asserts in the pane.
	backgroundToolCommand = "printf " + backgroundToolOutput
	// backgroundTargetPrompt is the user text the fixture sends to start the
	// target session's turn while its web pane stays unopened.
	backgroundTargetPrompt = "BACKGROUND_TARGET_PROMPT"
	// backgroundTurnDone is the short text the terminal communicate carries.
	// It is distinct from every asserted sentinel so it cannot double a
	// before/after/tool count, and it never repeats the streamed payload.
	backgroundTurnDone = "BACKGROUND_TURN_DONE"
	// backgroundHistoryReply is the history role's neutral assistant text. It
	// must NOT reuse the target's before/after sentinels, or those gates would
	// pass on history content instead of the target's own turn.
	backgroundHistoryReply = "BACKGROUND_HISTORY_REPLY"
)

// backgroundWorkload is the deterministic payload one sustained turn appends.
// It stays under the injected 32 KiB PENDING budget on purpose: a single
// recorded assistant update larger than that is rejected by the residency
// (`accept` revokes the ref), so the pressure must accumulate across several
// completing turns to exceed the injected 64 KiB RESIDENT budget.
var backgroundWorkload = strings.Repeat("W", 16*1024)

func newSkillBrowserAdapter(requestLog string) (*skillBrowserAdapter, error) {
	f, err := os.OpenFile(requestLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &skillBrowserAdapter{name: "openai", log: f}, nil
}

func (a *skillBrowserAdapter) Name() string { return a.name }

func (a *skillBrowserAdapter) close() {
	if closer, ok := a.log.(io.Closer); ok {
		_ = closer.Close()
	}
}

func (a *skillBrowserAdapter) isSessionNamerRequest(req llm.Request) bool {
	_, ok := scriptedSessionNamerResponse(a.name, req)
	return ok
}

// record appends one request record and reports whether the caller must wait
// on the hold gate.
func (a *skillBrowserAdapter) record(req llm.Request) (gate <-chan struct{}, held bool, seq int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	kind := "turn"
	if a.isSessionNamerRequest(req) {
		kind = "session-namer"
	}
	rec := skillBrowserRequestRecord{
		Event:   "request",
		Seq:     a.seq,
		Kind:    kind,
		At:      time.Now().UTC().Format(time.RFC3339Nano),
		Request: req,
	}
	if kind == "turn" && a.holdArmed {
		rec.Held = true
		held = true
		gate = a.gate
	}
	data, err := json.Marshal(rec)
	if err != nil {
		// The request types are JSON-shaped by construction (llm/types.go); a
		// marshal failure is a fixture bug, not a runtime condition.
		fmt.Fprintf(os.Stderr, "skill browser helper: marshal request record: %v\n", err)
		return nil, false, a.seq
	}
	if _, err := a.log.Write(append(data, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "skill browser helper: write request record: %v\n", err)
	}
	return gate, held, a.seq
}

func (a *skillBrowserAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	gate, held, _ := a.record(req)
	if a.isSessionNamerRequest(req) {
		resp, _ := scriptedSessionNamerResponse(a.name, req)
		return resp, nil
	}
	if held && gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			// Shutdown cancels the in-flight turn; the daemon exits cleanly.
			return llm.Response{}, ctx.Err()
		}
	}
	_ = a.writeEvent("completed")
	return scriptedCommunicate(skillBrowserReply), nil
}

// Stream is the channel-backed boundary the whole-journey guard scripts. It is
// inert (ErrStreamUnsupported, so the daemon falls back to Complete) unless the
// explicit background fixture mode has been armed through the control file, so
// the existing skill guard's Complete behavior is unchanged.
func (a *skillBrowserAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if !a.isStreamingRequest(req) {
		return nil, llm.ErrStreamUnsupported
	}
	a.record(req)
	// Fail at the external provider boundary, before any assistant content. The
	// real runtime owns the resulting failed turn and its canonical turn error.
	if a.backgroundRole() == "precontent-error" {
		return nil, llm.NewAuthenticationError(a.name, "scripted comic provider refusal", nil)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	st := llm.NewChanStream(cancel)
	go a.scriptBackgroundStream(streamCtx, st, req)
	return st, nil
}

// isStreamingRequest reports whether the background fixture mode is armed and
// this request is an ordinary turn (never the session namer, which always
// resolves through Complete).
func (a *skillBrowserAdapter) isStreamingRequest(req llm.Request) bool {
	a.mu.Lock()
	streaming := a.streaming
	a.mu.Unlock()
	if !streaming {
		return false
	}
	return !a.isSessionNamerRequest(req)
}

// scriptBackgroundStream produces one scripted turn. The initial request emits
// the before-eviction text, waits for the fixture's after-eviction phase, emits
// the after-eviction text and a real structured shell tool call; the follow-up
// request (carrying the tool result) waits for the finish phase and completes.
// Every wait returns false only when the stream context is cancelled, so a
// cancelled turn never emits a stray terminal event.
func (a *skillBrowserAdapter) scriptBackgroundStream(ctx context.Context, st *llm.ChanStream, req llm.Request) {
	defer st.CloseSend()
	role := a.backgroundRole()
	st.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
	if role == "comic-roster" {
		a.scriptComicRoster(st, req)
		return
	}
	if role == "comic-subchats" {
		a.scriptComicSubchats(ctx, st, req)
		return
	}
	if role == "comic-authorship" {
		a.scriptComicAuthorship(st, req)
		return
	}
	if role == "comic-interactions" {
		a.scriptComicInteractions(st, req)
		return
	}
	if role == "comic-composer" {
		queued := false
		for _, message := range req.Messages {
			for _, part := range message.Content {
				if message.Role == llm.RoleUser && strings.TrimSpace(part.Text) == "COMIC_COMPOSER_QUEUE literal queued input" {
					queued = true
				}
			}
		}
		if queued {
			st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: "COMIC_COMPOSER_QUEUE streaming before Stop"})
			if !a.waitBackgroundPhase(ctx, "comic-composer-never-release") {
				_ = a.writeEvent("comic-composer-cancelled")
			}
			return
		}
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: "COMIC_COMPOSER_REPLY streamed first"})
		if !a.waitBackgroundPhase(ctx, "comic-composer-finish") {
			return
		}
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: " and second"})
		sendScriptedCommunicate(st)
		return
	}
	if role == "history" {
		// History turns exist to build volume and carry their own user sentinel
		// (BACKGROUND_HISTORY_NN); their assistant text is neutral so the
		// target's before/after/tool gates and exactly-once checks cannot pass
		// on history content. They complete immediately, without phase gates.
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: backgroundHistoryReply})
		sendScriptedCommunicate(st)
		return
	}
	if hasToolResults(req) {
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: backgroundToolDone})
		if !a.waitBackgroundPhase(ctx, "finish") {
			return
		}
		sendScriptedCommunicate(st)
		return
	}
	st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: backgroundBeforeEviction})
	if role == "sustained" {
		// The sustained session's large payload lands BEFORE any phase release,
		// so its resident model alone exceeds the injected budget and evicts the
		// unopened target before the after-eviction phase is ever released.
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: backgroundWorkload})
	}
	if !a.waitBackgroundPhase(ctx, "after-eviction") {
		return
	}
	st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: backgroundAfterEviction})
	if role == "sustained" {
		sendScriptedCommunicate(st)
		return
	}
	call := llm.ToolCallData{
		ID:        "background_tool_call",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"` + backgroundToolCommand + `","mode":"foreground"}`),
		Type:      "function",
	}
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &call})
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
	st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &llm.FinishReason{Reason: llm.FinishReasonToolCalls}})
}

func (a *skillBrowserAdapter) scriptComicInteractions(st *llm.ChanStream, req llm.Request) {
	marker := ""
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if message.Role == llm.RoleUser && strings.HasPrefix(part.Text, "COMIC_INTERACTION_") {
				marker = part.Text
			}
		}
	}
	id := "comic_interaction_" + marker
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.ToolCall != nil && part.ToolCall.ID == id {
				st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: marker + " completed"})
				sendScriptedCommunicate(st)
				return
			}
		}
	}
	name := "write_file"
	args := map[string]any{"file_path": filepath.Join(a.comicOutsideDir, marker), "content": marker, "intent": "Testing an explicit one-action sandbox decision"}
	if marker == "COMIC_INTERACTION_ASK" {
		name = "ask_user"
		args = map[string]any{"questions": []any{
			map[string]any{"question": "COMIC_QUESTION_SINGLE", "options": []any{map[string]string{"label": "COMIC_SINGLE_A", "detail": "first"}, map[string]string{"label": "COMIC_SINGLE_B", "detail": "second"}}},
			map[string]any{"question": "COMIC_QUESTION_MULTI", "options": []any{map[string]string{"label": "COMIC_MULTI_A", "detail": "first"}, map[string]string{"label": "COMIC_MULTI_B", "detail": "second"}}, "multi_select": true},
		}}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	call := llm.ToolCallData{ID: id, Name: name, Arguments: raw, Type: "function"}
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &call})
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
	st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &llm.FinishReason{Reason: llm.FinishReasonToolCalls}})
}

// This opt-in script asks production tools to create actual descendants. Child
// refs, activity generations and transcript records are all runtime output.
// scriptComicRoster produces actual delegates, not activity rows. The normal
// session-scoped activity endpoint supplies its own 50-row continuation pages.
func (a *skillBrowserAdapter) scriptComicRoster(st *llm.ChanStream, req llm.Request) {
	created := 0
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if message.Role == llm.RoleUser && strings.TrimSpace(part.Text) == "COMIC_ROSTER_LEAF" {
				st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: "COMIC_ROSTER_LEAF literal answer"})
				sendScriptedCommunicate(st)
				return
			}
			if message.Role == llm.RoleAssistant && part.ToolCall != nil && strings.HasPrefix(part.ToolCall.ID, "comic_roster_") {
				created++
			}
		}
	}
	if created >= 101 {
		sendScriptedCommunicate(st)
		return
	}
	for index := created; index < min(created+10, 101); index++ {
		args := map[string]any{"prompt": "COMIC_ROSTER_LEAF", "intent": "Producing real bounded activity pages", "delegation_allowance": 0}
		if index < 2 {
			args["name"] = "comic-same-name"
		}
		encoded, _ := json.Marshal(args)
		call := llm.ToolCallData{ID: fmt.Sprintf("comic_roster_%03d", index), Name: "delegate", Arguments: encoded, Type: "function"}
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &call})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
	}
	st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &llm.FinishReason{Reason: llm.FinishReasonToolCalls}})
}

func (a *skillBrowserAdapter) scriptComicSubchats(ctx context.Context, st *llm.ChanStream, req llm.Request) {
	level := "ROOT"
	for _, message := range req.Messages {
		if message.Role != llm.RoleUser {
			continue
		}
		for _, part := range message.Content {
			if strings.TrimSpace(part.Text) == "COMIC_SUBCHAT_GRANDCHILD" {
				level = "GRANDCHILD"
			} else if strings.TrimSpace(part.Text) == "COMIC_SUBCHAT_CHILD" {
				level = "CHILD"
			}
		}
	}
	launched, answered, instructed := false, false, false
	grandchildID := ""
	historyCalls := 0
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.ToolResult != nil && part.ToolResult.ToolCallID == "comic_delegate_GRANDCHILD" {
				var receipt struct {
					DelegateID string `json:"delegate_id"`
				}
				text, _ := part.ToolResult.Content.(string)
				if json.Unmarshal([]byte(text), &receipt) == nil {
					grandchildID = receipt.DelegateID
				}
			}
		}
		if message.Role != llm.RoleAssistant {
			continue
		}
		for _, part := range message.Content {
			if part.ToolCall != nil && strings.HasPrefix(part.ToolCall.ID, "comic_delegate_") {
				launched = true
			}
			if part.ToolCall != nil && strings.HasPrefix(part.ToolCall.ID, "comic_history_") {
				historyCalls++
			}
			if part.ToolCall != nil && part.ToolCall.ID == "comic_parent_instruction" {
				instructed = true
			}
			if strings.Contains(part.Text, "COMIC_ANSWER_"+level) {
				answered = true
			}
		}
	}
	if answered {
		sendScriptedCommunicate(st)
		return
	}
	if level == "CHILD" && launched && historyCalls < 45 {
		text := fmt.Sprintf("COMIC_CHILD_SOURCE_%02d literal child history with *source* characters.", historyCalls)
		if historyCalls == 5 {
			text += strings.Repeat(" Later-page child text preserves its literal reading offset. ", 30)
		}
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: text})
		call := llm.ToolCallData{ID: fmt.Sprintf("comic_history_%02d", historyCalls), Name: "shell", Arguments: json.RawMessage(`{"command":"printf child-page","mode":"foreground"}`), Type: "function"}
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &call})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &llm.FinishReason{Reason: llm.FinishReasonToolCalls}})
		return
	}
	if level == "CHILD" && grandchildID != "" && !instructed {
		args, _ := json.Marshal(map[string]any{"to": grandchildID, "message": "COMIC_PARENT_INSTRUCTION literal parent steering", "intent": "Proving canonical named parent attribution", "max_wait_ms": 1000})
		call := llm.ToolCallData{ID: "comic_parent_instruction", Name: "delegate_send", Arguments: args, Type: "function"}
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &call})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &llm.FinishReason{Reason: llm.FinishReasonToolCalls}})
		return
	}
	if launched || level == "GRANDCHILD" {
		st.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
		st.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "COMIC_THOUGHT_" + level + " available literal reasoning"})
		st.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
		if level == "CHILD" {
			_ = a.writeEvent("comic-child-produced")
			if !a.waitBackgroundPhase(ctx, "comic-child-grow") {
				_ = a.writeEvent("comic-child-cancelled-before-growth")
				return
			}
			_ = a.writeEvent("comic-child-growth-released")
		}
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: "COMIC_ANSWER_" + level + " literal attributed answer"})
		if level == "CHILD" && !a.waitBackgroundPhase(ctx, "comic-child-finish") {
			return
		}
		sendScriptedCommunicate(st)
		return
	}
	next := "CHILD"
	allowance := 1
	if level == "CHILD" {
		next = "GRANDCHILD"
		allowance = 0
	}
	arguments := map[string]any{"prompt": "COMIC_SUBCHAT_" + next, "intent": "Producing a real nested comic fixture", "delegation_allowance": allowance}
	if next == "CHILD" {
		arguments["name"] = "comic-named-parent"
	}
	args, _ := json.Marshal(arguments)
	call := llm.ToolCallData{ID: "comic_delegate_" + next, Name: "delegate", Arguments: args, Type: "function"}
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &call})
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
	st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &llm.FinishReason{Reason: llm.FinishReasonToolCalls}})
}

// scriptComicAuthorship forks twice through the real delegate tool. Each fork
// receives completed ancestor turns, including the child's published update.
func (a *skillBrowserAdapter) scriptComicAuthorship(st *llm.ChanStream, req llm.Request) {
	level := "ROOT"
	for _, message := range req.Messages {
		if message.Role != llm.RoleUser {
			continue
		}
		for _, part := range message.Content {
			if strings.TrimSpace(part.Text) == "COMIC_AUTH_CHILD" {
				level = "CHILD"
			} else if strings.TrimSpace(part.Text) == "COMIC_AUTH_LEAF" {
				level = "LEAF"
			}
		}
	}
	seeded, launched := false, false
	history := 0
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if message.Role == llm.RoleAssistant && part.ToolCall != nil {
				seeded = seeded || part.ToolCall.ID == "comic_auth_seed_"+level
				launched = launched || part.ToolCall.ID == "comic_auth_spawn_"+level
				if strings.HasPrefix(part.ToolCall.ID, "comic_auth_history_"+level+"_") {
					history++
				}
			}
		}
	}
	if level == "LEAF" || launched {
		sendScriptedCommunicate(st)
		return
	}
	call := llm.ToolCallData{Type: "function"}
	if !seeded {
		st.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
		st.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "COMIC_AUTH_THOUGHT_" + level + " literal original thought"})
		st.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: "COMIC_AUTH_ANSWER_" + level + " literal original answer"})
		call.ID = "comic_auth_seed_" + level
		call.Name = "shell"
		call.Arguments = json.RawMessage(`{"command":"printf authored-root","mode":"foreground"}`)
		if level == "CHILD" {
			call.Name = "communicate"
			call.Arguments = json.RawMessage(`{"message":"COMIC_AUTH_UPDATE_CHILD literal original update","end_turn":false,"output":{"message":"","data":{},"artifacts":[]}}`)
		}
	} else if level == "CHILD" && history < 35 {
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: fmt.Sprintf("COMIC_AUTH_PAGE_%02d", history)})
		call.ID = fmt.Sprintf("comic_auth_history_CHILD_%02d", history)
		call.Name = "shell"
		call.Arguments = json.RawMessage(`{"command":"printf authored-page","mode":"foreground"}`)
	} else {
		next, allowance := "CHILD", 1
		if level == "CHILD" {
			next, allowance = "LEAF", 0
		}
		call.ID = "comic_auth_spawn_" + level
		call.Name = "delegate"
		call.Arguments, _ = json.Marshal(map[string]any{"prompt": "COMIC_AUTH_" + next, "name": "comic-auth-" + strings.ToLower(next), "intent": "Proving original authors through nested opt-in forks", "fork_context": true, "delegation_allowance": allowance})
	}
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &call})
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
	st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &llm.FinishReason{Reason: llm.FinishReasonToolCalls}})
}

// sendScriptedCommunicate ends a scripted streaming turn the way the daemon
// requires: a real `communicate` tool call, the provider schema
// scriptedCommunicate (scripted_provider_test.go) uses. decideNoToolCalls
// (agent/session_tool_round.go) steers a bare-text finish back and eventually
// fails the turn, so a streaming script MUST end with a tool call. The
// already-streamed text is preserved; this only ends the turn and carries a
// short, distinct message that never repeats the streamed payload.
func sendScriptedCommunicate(st *llm.ChanStream) {
	args, _ := json.Marshal(map[string]any{
		"message":  backgroundTurnDone,
		"end_turn": true,
		"output":   map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}},
	})
	call := llm.ToolCallData{
		ID:        fmt.Sprintf("background_communicate_%d", serveTestCommunicateSeq.Add(1)),
		Name:      "communicate",
		Arguments: args,
		Type:      "function",
	}
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &call})
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
	st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &llm.FinishReason{Reason: llm.FinishReasonToolCalls}})
}

// backgroundRole returns the adapter's scripted role under the lock.
func (a *skillBrowserAdapter) backgroundRole() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.role
}

// setBackgroundRole selects the scripted turn shape. It must run before the
// turn starts; the control tail processes it at daemon startup.
func (a *skillBrowserAdapter) setBackgroundRole(role string) {
	a.mu.Lock()
	a.role = role
	a.mu.Unlock()
}

// setAutoPhases releases every background phase immediately, for the history
// and gap turns that must complete without driver gating.
func (a *skillBrowserAdapter) setAutoPhases(auto bool) {
	a.mu.Lock()
	a.autoPhases = auto
	var toRelease []string
	if auto {
		for phase := range a.phases {
			toRelease = append(toRelease, phase)
		}
	}
	a.mu.Unlock()
	for _, phase := range toRelease {
		a.releaseBackgroundPhase(phase)
	}
}

// resetPhases clears the one-shot phase channels at the start of a new turn,
// so a channel closed for a previous turn cannot release a phase of this one.
// It runs when a start-turn control command is dispatched, BEFORE the turn's
// stream waits; an early release for THIS turn is still remembered because
// releaseBackgroundPhase creates the channel when the waiter has not yet.
func (a *skillBrowserAdapter) resetPhases() {
	a.mu.Lock()
	a.phases = nil
	a.mu.Unlock()
}

// hasToolResults reports whether THIS request's current turn already carries a
// tool result (the follow-up leg after the scripted shell call). It scans only
// from the LATEST user message, so a previous turn's shell or communicate
// result cannot select the follow-up branch of a new turn.
func hasToolResults(req llm.Request) bool {
	start := 0
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleUser {
			start = i
			break
		}
	}
	for _, message := range req.Messages[start:] {
		if message.Role == llm.RoleTool {
			return true
		}
		for _, part := range message.Content {
			if part.Kind == llm.ContentToolResult {
				return true
			}
		}
	}
	return false
}

// waitBackgroundPhase waits for the named one-shot phase release, or the stream
// context's cancellation. It returns true only when that phase was released.
func (a *skillBrowserAdapter) waitBackgroundPhase(ctx context.Context, phase string) bool {
	a.mu.Lock()
	if a.autoPhases {
		a.mu.Unlock()
		return true
	}
	if a.phases == nil {
		a.phases = map[string]chan struct{}{}
	}
	ch, ok := a.phases[phase]
	if !ok {
		ch = make(chan struct{})
		a.phases[phase] = ch
	}
	a.mu.Unlock()
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}

// releaseBackgroundPhase closes the named phase channel, creating and closing
// it if the release arrived before the waiter did, so the one-shot release is
// never lost to ordering.
func (a *skillBrowserAdapter) releaseBackgroundPhase(phase string) {
	a.mu.Lock()
	if a.phases == nil {
		a.phases = map[string]chan struct{}{}
	}
	ch, ok := a.phases[phase]
	if !ok {
		ch = make(chan struct{})
		a.phases[phase] = ch
	}
	a.mu.Unlock()
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// enableStreaming arms the explicit background fixture mode.
func (a *skillBrowserAdapter) enableStreaming() {
	a.mu.Lock()
	a.streaming = true
	a.mu.Unlock()
}

// ackBackgroundPhase records the fixture's acknowledgment of a released phase
// in the request log the Go owner reads, so the release has observable
// evidence independent of the driver.
func (a *skillBrowserAdapter) ackBackgroundPhase(phase string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	data, err := json.Marshal(map[string]any{
		"event": "background-phase-ack",
		"phase": phase,
		"at":    time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return
	}
	if _, err := a.log.Write(append(data, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "skill browser helper: write phase ack: %v\n", err)
	}
}

// ackTurnComplete records one submitted turn's REAL outcome in the request log
// the Go owner reads, so completion is correlated to the particular turn rather
// than to a historical prompt match, an accepted turn/start, or an unrelated
// global write. ok is true only when the daemon reported the turn completed.
func (a *skillBrowserAdapter) ackTurnComplete(ref, turnID, prompt string, ok bool, status, errMsg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	data, err := json.Marshal(map[string]any{
		"event":  "turn-complete",
		"ref":    ref,
		"turnId": turnID,
		"prompt": prompt,
		"ok":     ok,
		"status": status,
		"error":  errMsg,
		"at":     time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return
	}
	if _, err := a.log.Write(append(data, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "skill browser helper: write turn-complete ack: %v\n", err)
	}
}

// writeEvent appends a non-request lifecycle event to the same log so the Go
// owner can tell a held dispatch from a completed one.
func (a *skillBrowserAdapter) writeEvent(event string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	data, err := json.Marshal(map[string]any{"event": event, "at": time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	_, err = a.log.Write(append(data, '\n'))
	return err
}

// armHold arms the one-shot gate: the NEXT turn request blocks (after being
// recorded) until releaseHold. The Go owner and the Node driver both write
// these commands through the fixture's control file.
func (a *skillBrowserAdapter) armHold() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.holdArmed {
		return
	}
	a.holdArmed = true
	a.gate = make(chan struct{})
}

func (a *skillBrowserAdapter) releaseHold() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.holdArmed && a.gate != nil {
		close(a.gate)
	}
	a.holdArmed = false
	a.gate = nil
}

// skillBrowserControlCommand is the fixture IPC command set: hold, release,
// shutdown, and the whole-journey guard's background-mode/background-phase.
// Never production RPC.
type skillBrowserControlCommand struct {
	Command string `json:"command"`
	// Phase names the background phase a "background-phase" command releases.
	Phase string `json:"phase,omitempty"`
}

// skillBrowserControls tails the control JSONL file and dispatches commands to
// the adapter, signalling shutdown through the channel the helper test waits
// on.
type skillBrowserControls struct {
	path     string
	adapter  *skillBrowserAdapter
	shutdown chan struct{}
	stopCh   chan struct{}
	done     chan struct{}
	// startTurn, when set, starts a REAL turn on this daemon's own session
	// through its real AppWire — the unopened producer the whole-journey guard
	// needs, so the target's text is produced without its web pane ever opening.
	startTurn func(context.Context, string) error
	// observerCtx outlives one turn: a held turn (the target waits for phases)
	// must not be cancelled by the short setup deadline. stop() cancels it and
	// joins every observer so teardown settles owned work.
	observerCtx    context.Context
	observerCancel context.CancelFunc
	observerWg     sync.WaitGroup
}

func newSkillBrowserControls(path string, adapter *skillBrowserAdapter) *skillBrowserControls {
	observerCtx, observerCancel := context.WithCancel(context.Background())
	return &skillBrowserControls{
		path:           path,
		adapter:        adapter,
		shutdown:       make(chan struct{}),
		stopCh:         make(chan struct{}),
		done:           make(chan struct{}),
		observerCtx:    observerCtx,
		observerCancel: observerCancel,
	}
}

func (c *skillBrowserControls) run(t *testing.T) <-chan struct{} {
	go func() {
		defer close(c.done)
		if err := c.tail(t); err != nil {
			fmt.Fprintf(os.Stderr, "skill browser helper: control tail: %v\n", err)
		}
	}()
	return c.done
}

func (c *skillBrowserControls) stop() {
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
	if c.observerCancel != nil {
		c.observerCancel()
	}
	c.observerWg.Wait()
}

// tail reads the control file from byte zero and then follows appends via
// fsnotify, dispatching each complete JSON line. The Go owner creates the file
// (possibly with commands already in it) before launching this process.
func (c *skillBrowserControls) tail(t *testing.T) error {
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	if err := watcher.Add(dir); err != nil {
		return err
	}

	offset := int64(0)
	dispatch := func() error {
		f, err := os.Open(c.path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if info.Size() < offset {
			// The file was replaced; start over rather than miss commands.
			offset = 0
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		buf := make([]byte, 4096)
		var pending []byte
		for {
			n, readErr := f.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				for len(chunk) > 0 {
					idx := indexByte(chunk, '\n')
					if idx < 0 {
						pending = append(pending, chunk...)
						break
					}
					pending = append(pending, chunk[:idx]...)
					c.dispatchLine(t, pending)
					pending = nil
					chunk = chunk[idx+1:]
				}
				offset += int64(n)
			}
			if readErr != nil {
				break
			}
		}
		if len(pending) > 0 {
			c.dispatchLine(t, pending)
		}
		return nil
	}

	if err := dispatch(); err != nil {
		return err
	}
	for {
		select {
		case <-c.stopCh:
			return nil
		case _, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if err := dispatch(); err != nil {
				return err
			}
		case werr := <-watcher.Errors:
			return werr
		}
	}
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func (c *skillBrowserControls) dispatchLine(t *testing.T, line []byte) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return
	}
	var cmd skillBrowserControlCommand
	if err := json.Unmarshal([]byte(trimmed), &cmd); err != nil {
		fmt.Fprintf(os.Stderr, "skill browser helper: ignoring malformed control line %q: %v\n", trimmed, err)
		return
	}
	switch cmd.Command {
	case "hold":
		c.adapter.armHold()
	case "release":
		c.adapter.releaseHold()
	case "background-mode":
		// The explicit fixture mode: scripted channel-backed streaming turns
		// replace Complete for the whole-journey guard. The skill guard never
		// sends this, so its behavior is unchanged.
		c.adapter.enableStreaming()
	case "background-phase":
		if cmd.Phase == "" {
			fmt.Fprintln(os.Stderr, "skill browser helper: background-phase command without a phase")
			return
		}
		c.adapter.releaseBackgroundPhase(cmd.Phase)
		c.adapter.ackBackgroundPhase(cmd.Phase)
	case "background-start-turn":
		// The unopened producer: start a real turn on this daemon's own session
		// through its real AppWire, so the web app only ever sees it as a
		// background subscription (its pane is never opened to drive it). Each
		// turn gets fresh one-shot phase channels; the short ctx below bounds
		// only the AppWire setup, while the observer runs until the turn is
		// terminal or teardown cancels it.
		if c.startTurn == nil {
			fmt.Fprintln(os.Stderr, "skill browser helper: background-start-turn is not wired for this helper")
			return
		}
		c.adapter.resetPhases()
		c.observerWg.Add(1)
		go func() {
			defer c.observerWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := c.startTurn(ctx, cmd.Phase); err != nil {
				fmt.Fprintf(os.Stderr, "skill browser helper: background-start-turn: %v\n", err)
			}
		}()
	case "background-role":
		// Selects this helper's scripted turn shape ("sustained" or "target").
		c.adapter.setBackgroundRole(cmd.Phase)
	case "background-auto":
		// Releases every background phase immediately, so history/gap turns
		// complete without the driver gating each one. phase:"off" turns it off
		// again for the held-phase batching scenario.
		c.adapter.setAutoPhases(cmd.Phase != "off")
	case "shutdown":
		select {
		case <-c.shutdown:
		default:
			close(c.shutdown)
		}
	default:
		fmt.Fprintf(os.Stderr, "skill browser helper: unknown control command %q\n", cmd.Command)
	}
}
