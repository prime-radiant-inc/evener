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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
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
}

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

func (a *skillBrowserAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
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
// shutdown. Never production RPC.
type skillBrowserControlCommand struct {
	Command string `json:"command"`
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
}

func newSkillBrowserControls(path string, adapter *skillBrowserAdapter) *skillBrowserControls {
	return &skillBrowserControls{
		path:     path,
		adapter:  adapter,
		shutdown: make(chan struct{}),
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
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
