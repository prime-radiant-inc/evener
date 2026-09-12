//go:build browserguard

// TestSkillComposerBrowser is the skill lifecycle's final integration proof:
// the PRODUCTION hub web app (embedded dist, real AppWire, real stores, native
// IndexedDB) driven in real Chrome through a REAL hub (httptest around
// web.Handler() with the real roster prober and past index) against TWO REAL
// `evener serve` daemons (cmd/evener's compiled browserguard helper binary),
// with the only scripted piece at the external LLM provider adapter.
//
// The Node driver (frontend/scripts/skillguard/run.mjs) owns the browser
// gestures and records milestones; this test owns the daemons, the hub, the
// fixture, and every assertion against what the daemons ACTUALLY received
// (their provider request logs and durable transcripts). Nothing here adds a
// testing endpoint to production serve.go or hub routes.
//
// The build tag keeps Chrome out of the default suites; scripts/web/
// test-web-browser.sh registers this test so `make test-web-browser` runs it.
package hub

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
)

// Fixture vocabulary shared with the Node driver — it appends the browser
// milestones, this file asserts the same strings in the daemons' actual
// provider requests and transcripts. Opaque sentinels, never prose to pin.
const (
	skillGuardSkillName        = "pkg:probe"
	skillGuardReply            = "skillguard turn complete"
	skillGuardChipRemovePrefix = "Remove skill pkg:probe"
	// The SKILL.md frontmatter description, asserted verbatim in the daemon's
	// rendered <skill-context> (skillGuardWriteSkill is its only other use, so
	// fixture and assertion cannot drift).
	skillGuardSkillDescription = "fixture procedure for the browser guard"

	proseCanonical   = "PROSE_ALPHA_14a run the fixture check on the gamma channel"
	proseDraft       = "PROSE_DRAFT_14b staged for the switch"
	proseQueueTurn   = "PROSE_QTURN_14c open a long turn for the queue"
	proseQueueFirst  = "PROSE_QUEUE_14c first pass"
	proseQueueSecond = "PROSE_QUEUE_14c second pass"
	proseAttachment  = "PROSE_ATTACH_14d inspect the attached image"
	proseSteerTurn   = "PROSE_STEER_TURN_14e open a long turn for steering"
	proseSteer       = "PROSE_STEER_14e redirect the running turn"
	proseCapLoss     = "PROSE_CAPLOSS_14f aimed at a lost capability"
	proseFailTurn    = "PROSE_FAIL_TURN_14g open a long turn for the failing claim"
	proseFail        = "PROSE_FAIL_14h request the missing source"
	proseDelay       = "PROSE_DELAY_14i submitted then edited while held"
	proseDelayExtra  = "PROSE_DELAY_EXTRA_14i typed after the submit"
	proseTransport   = "PROSE_NET_14j submitted while offline"
)

// The fixture skill body the test writes into each helper's plugin and then
// requires — complete and unchanged — inside the provider request.
const skillGuardSkillBody = "Return the opaque marker LIVE_SKILL_a983 when asked to run the fixture procedure.\n"

// skillGuardDriverTimeout bounds the whole browser run: a hung driver is a
// failure, never a hang of the gate.
const skillGuardDriverTimeout = 8 * time.Minute

// ---- wire shapes decoded from the helpers' request logs ----

type skillGuardRequestRecord struct {
	Event   string            `json:"event"`
	Seq     int               `json:"seq"`
	Kind    string            `json:"kind"`
	At      string            `json:"at"`
	Held    bool              `json:"held"`
	Request skillGuardLLMCall `json:"request"`
}

type skillGuardLLMCall struct {
	Provider string              `json:"provider"`
	Model    string              `json:"model"`
	Messages []skillGuardMessage `json:"messages"`
}

type skillGuardMessage struct {
	Role    string              `json:"role"`
	Content []skillGuardLLMPart `json:"content"`
}

type skillGuardLLMPart struct {
	Kind  string           `json:"kind"`
	Text  string           `json:"text,omitempty"`
	Image *skillGuardImage `json:"image,omitempty"`
}

type skillGuardImage struct {
	Data      []byte `json:"data,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

func (m skillGuardMessage) text() string {
	var b strings.Builder
	for _, part := range m.Content {
		if part.Kind == "text" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func (c skillGuardLLMCall) allText() string {
	var b strings.Builder
	for _, m := range c.Messages {
		b.WriteString(m.text())
		b.WriteString("\n")
	}
	return b.String()
}

// lastUserText returns the text of the FINAL user message — the request's
// own input. History replays every earlier input verbatim, so a prose match
// over the whole request counts later turns too; the last user message is
// the only honest identity for "this request was dispatched FOR this input".
func (c skillGuardLLMCall) lastUserText() string {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role == "user" {
			return c.Messages[i].text()
		}
	}
	return ""
}

func (c skillGuardLLMCall) imagePartCount() int {
	count := 0
	for _, m := range c.Messages {
		for _, part := range m.Content {
			if part.Kind == "image" && part.Image != nil && len(part.Image.Data) > 0 {
				count++
			}
		}
	}
	return count
}

// skillGuardDocument is skill.Render's data-safe carrier, decoded from the
// <skill-context> JSON in a recorded provider request.
type skillGuardDocument struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Source        string `json:"source"`
	BaseDirectory string `json:"base_directory"`
	Instructions  string `json:"instructions"`
}

func (c skillGuardLLMCall) skillContexts() []skillGuardDocument {
	var out []skillGuardDocument
	for _, m := range c.Messages {
		text := m.text()
		open := strings.Index(text, "<skill-context>")
		closing := strings.Index(text, "</skill-context>")
		if open < 0 || closing < open {
			continue
		}
		payload := strings.TrimSpace(text[open+len("<skill-context>") : closing])
		var doc skillGuardDocument
		if err := json.Unmarshal([]byte(payload), &doc); err != nil {
			continue
		}
		out = append(out, doc)
	}
	return out
}

// ---- milestones written by the Node driver ----

type skillGuardMilestone struct {
	Milestone string          `json:"milestone"`
	At        string          `json:"at"`
	Detail    json.RawMessage `json:"detail"`
}

// ---- durable mutation evidence the driver reads out of the page ----

type skillGuardDurableInputItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"`
}

type skillGuardDurableRecord struct {
	ClientMutationID string                       `json:"clientMutationId"`
	Method           string                       `json:"method"`
	State            string                       `json:"state"`
	RecoveryKind     string                       `json:"recoveryKind"`
	RecoveryReason   string                       `json:"recoveryReason"`
	ComposerText     string                       `json:"composerText"`
	TargetRef        string                       `json:"targetRef"`
	Input            []skillGuardDurableInputItem `json:"input"`
}

type skillGuardDurableDump struct {
	Outbox     []skillGuardDurableRecord `json:"outbox"`
	Optimistic []skillGuardDurableRecord `json:"optimistic"`
	Recovery   []skillGuardDurableRecord `json:"recovery"`
}

// skillGuardComposerSnapshot is the driver's composerState() dump embedded in
// milestones.
type skillGuardComposerSnapshot struct {
	Text         string   `json:"text"`
	Placeholder  string   `json:"placeholder"`
	Chips        []string `json:"chips"`
	RemoveLabels []string `json:"removeLabels"`
	Tiles        int      `json:"tiles"`
}

type skillGuardFixture struct {
	root       string
	artifact   string
	runDir     string
	stateRoot  string
	workDir    [2]string
	stateDir   [2]string
	requestLog [2]string
	control    [2]string
	skillFile  [2]string
	helperBin  string
}

func TestSkillComposerBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("browser guard requires a real Chrome and real daemons")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("node is required for the skill browser guard: %v", err)
	}
	// The embedded production frontend must exist — the hub serves it. A
	// missing dist is a build-prerequisite failure, not a pass.
	index, err := fs.ReadFile(distFS(), "index.html")
	if err != nil {
		t.Fatalf("the production frontend is not built (the hub would serve nothing): run `make build-web` first: %v", err)
	}
	_ = index

	fixture := skillGuardSetup(t)

	// Start both helper daemons.
	var entries [2]rendezvous.Entry
	for i := range entries {
		entries[i] = skillGuardStartHelper(t, fixture, i)
	}

	// Real hub: roster prober + past index over the helpers' state roots.
	ctx, cancel := context.WithTimeout(context.Background(), skillGuardDriverTimeout)
	defer cancel()
	roster := hubcore.NewRoster(fixture.runDir, &hubcore.StatusProber{Timeout: 500 * time.Millisecond})
	if err := roster.RefreshAndWait(ctx); err != nil {
		t.Fatalf("roster refresh: %v", err)
	}
	past := hubcore.NewPastIndex(filepath.Join(fixture.stateRoot, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatalf("past index rebuild: %v", err)
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		t.Fatalf("mint auth token: %v", err)
	}
	token := hex.EncodeToString(tokenBytes)
	hubStateRoot := filepath.Join(fixture.root, "hub-state")
	if err := os.MkdirAll(hubStateRoot, 0o700); err != nil {
		t.Fatalf("hub state root: %v", err)
	}
	web := NewWebServer(hubcore.WebConfig{
		AuthToken:     token,
		HubStateRoot:  hubStateRoot,
		RunDir:        fixture.runDir,
		Past:          past,
		Roster:        roster,
		StateDir:      fixture.stateRoot,
		PastIndexPath: filepath.Join(hubStateRoot, "index.db"),
	})
	// Navigation invalidation wiring mirrors runMain so the SPA actually
	// receives evener/navigation/invalidated when the roster or index changes.
	past.SetOnChange(func() { web.navigation.Invalidate(navigationChangeHint{}) })
	roster.SetOnChange(func() { web.navigation.Invalidate(navigationChangeHint{}) })
	hub := httptest.NewServer(web.Handler())
	defer hub.Close()
	authURL := hubedge.AuthURLFor(hub.URL, token)

	milestones := filepath.Join(fixture.artifact, "milestones.jsonl")
	if err := os.WriteFile(milestones, nil, 0o600); err != nil {
		t.Fatalf("create milestone file: %v", err)
	}

	// The driver + the live Go choreography run together: the driver owns the
	// browser, Go owns the two fixture events only it can produce (shutting
	// helper B down for the capability-loss scenario, and deleting/restoring
	// the skill source for the failed-activation scenario).
	choreo := newSkillGuardChoreography(t, fixture, roster, entries)
	driverDone := make(chan error, 1)
	go func() {
		driverDone <- runSkillGuardDriver(t, fixture, authURL, milestones, entries)
	}()
	choreoErr := make(chan error, 1)
	go func() { choreoErr <- choreo.run(milestones) }()

	var driverExit error
	select {
	case driverExit = <-driverDone:
	case <-time.After(skillGuardDriverTimeout):
		t.Fatalf("skill browser driver did not exit within %s; artifacts: %s", skillGuardDriverTimeout, fixture.artifact)
	}
	if err := choreo.stop(); err != nil {
		t.Errorf("fixture choreography: %v", err)
	}
	<-choreoErr
	if driverExit != nil {
		t.Fatalf("skill browser driver failed: %v\nfull artifacts (logs, screenshots, dumps): %s", driverExit, fixture.artifact)
	}

	// Stop the helpers deterministically; their request logs and transcripts
	// must be flushed for the assertions below.
	for i := range entries {
		skillGuardStopHelper(t, i)
	}

	skillGuardAssert(t, fixture, milestones, entries)
}

// ---- fixture ----

func skillGuardSetup(t *testing.T) *skillGuardFixture {
	t.Helper()
	// Not t.TempDir(): on failure the artifacts (driver log, screenshots,
	// request logs, transcripts) must SURVIVE for triage; they are removed
	// only when the test passes.
	root, err := os.MkdirTemp("", "skillguard-browser-")
	if err != nil {
		t.Fatalf("fixture root: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("test failed; keeping artifacts under %s", root)
			return
		}
		os.RemoveAll(root)
	})
	fixture := &skillGuardFixture{root: root}
	fixture.artifact = filepath.Join(root, "artifacts")
	if err := os.MkdirAll(fixture.artifact, 0o700); err != nil {
		t.Fatalf("artifact dir: %v", err)
	}
	fixture.runDir = filepath.Join(root, "run")
	fixture.stateRoot = filepath.Join(root, "state")
	for i := 0; i < 2; i++ {
		name := "alpha"
		if i == 1 {
			name = "beta"
		}
		fixture.workDir[i] = filepath.Join(root, fmt.Sprintf("helper-%s", name), "work")
		// Valid project ids keep the past index honest (testing.md's
		// "Seeding Hub Fixtures"): a hand-written dir name is silently skipped.
		fixture.stateDir[i] = hubtest.ProjectDir(t, filepath.Join(fixture.stateRoot, "projects"), name)
		fixture.requestLog[i] = filepath.Join(fixture.artifact, fmt.Sprintf("helper-%s-requests.jsonl", name))
		fixture.control[i] = filepath.Join(fixture.artifact, fmt.Sprintf("helper-%s-control.jsonl", name))
		for _, dir := range []string{
			fixture.workDir[i],
			fixture.stateDir[i],
			filepath.Dir(fixture.requestLog[i]),
		} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
		}
		if err := os.WriteFile(fixture.control[i], nil, 0o600); err != nil {
			t.Fatalf("create control file: %v", err)
		}
		// The fixture plugin: manifest "pkg" + skills/probe → catalog pkg:probe.
		pluginDir := filepath.Join(fixture.workDir[i], "fixture-plugin")
		for _, dir := range []string{
			filepath.Join(pluginDir, ".claude-plugin"),
			filepath.Join(pluginDir, "skills", "probe"),
		} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("mkdir plugin dir %s: %v", dir, err)
			}
		}
		manifest := `{"name":"pkg","version":"1.0.0","description":"fixture plugin for the skill browser guard"}`
		if err := os.WriteFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
			t.Fatalf("write plugin manifest: %v", err)
		}
		fixture.skillFile[i] = filepath.Join(pluginDir, "skills", "probe", "SKILL.md")
		if err := skillGuardWriteSkill(fixture.skillFile[i], skillGuardSkillBody); err != nil {
			t.Fatalf("write SKILL.md: %v", err)
		}
	}
	fixture.helperBin = filepath.Join(root, "evener-helper.test")
	repoRoot := skillGuardRepoRoot(t)
	build := exec.CommandContext(context.Background(), "go", "test", "-tags", "browserguard", "-c", "./"+filepath.Join("cmd", "evener"), "-o", fixture.helperBin)
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile the browserguard helper daemon (go test -tags browserguard -c ./cmd/evener): %v\n%s", err, out)
	}
	return fixture
}

func skillGuardWriteSkill(path, body string) error {
	content := "---\nname: probe\ndescription: " + skillGuardSkillDescription + "\n---\n" + body
	return os.WriteFile(path, []byte(content), 0o600)
}

func skillGuardRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// skillGuardSessionRef mirrors how the hub roster derives a session's hub ref
// from a rendezvous entry (roster.go: session id first, thread id as
// fallback, always "local:"-prefixed).
func skillGuardSessionRef(entry rendezvous.Entry) string {
	id := entry.SessionID
	if id == "" {
		id = entry.ThreadID
	}
	return "local:" + id
}

type skillGuardHelper struct {
	cmd      *exec.Cmd
	logPath  string
	control  string
	stopOnce sync.Once
	waitOnce sync.Once
	waitDone chan error
}

// wait returns the one shared cmd.Wait result. A raw second cmd.Wait fails
// immediately ("Wait was already called"), so every waiter — the
// choreography's exit watch, the explicit stop, and the cleanup fallback —
// reads this channel instead of calling cmd.Wait itself.
func (h *skillGuardHelper) wait() <-chan error {
	h.waitOnce.Do(func() {
		h.waitDone = make(chan error, 1)
		go func() {
			h.waitDone <- h.cmd.Wait()
			// Close after the one send: the first receiver gets the real
			// error, every later receiver (a second exit watch, the cleanup
			// fallback) gets the zero value immediately instead of blocking
			// forever on a drained channel.
			close(h.waitDone)
		}()
	})
	return h.waitDone
}

// exited reports whether the helper process has already been reaped.
func (h *skillGuardHelper) exited() bool {
	select {
	case <-h.wait():
		return true
	default:
		return false
	}
}

// stop shuts the helper down through its fixture IPC (the real thread/shutdown
// path that flushes the request log) with a bounded kill fallback. Idempotent:
// whichever caller runs first — the explicit stop or the test cleanup — does
// the work; the other is a no-op.
func (h *skillGuardHelper) stop(index int) error {
	var stopErr error
	h.stopOnce.Do(func() {
		if h.exited() {
			return
		}
		if err := appendFileLine(h.control, map[string]string{"command": "shutdown"}); err != nil {
			stopErr = fmt.Errorf("write shutdown command: %w", err)
		}
		select {
		case <-h.wait():
		case <-time.After(20 * time.Second):
			_ = h.cmd.Process.Kill()
			<-h.wait()
			if stopErr == nil {
				stopErr = fmt.Errorf("helper %d did not exit after the shutdown command; killed (its log: %s)", index, h.logPath)
			}
		}
	})
	return stopErr
}

var (
	skillGuardHelperMu sync.Mutex
	skillGuardHelpers  [2]*skillGuardHelper
)

func skillGuardStartHelper(t *testing.T, fixture *skillGuardFixture, index int) rendezvous.Entry {
	t.Helper()
	name := "alpha"
	if index == 1 {
		name = "beta"
	}
	cfgPath := filepath.Join(fixture.artifact, fmt.Sprintf("helper-%s-config.json", name))
	cfg, err := json.Marshal(map[string]string{
		"workDir":     fixture.workDir[index],
		"stateDir":    fixture.stateDir[index],
		"runDir":      fixture.runDir,
		"requestLog":  fixture.requestLog[index],
		"controlPath": fixture.control[index],
	})
	if err != nil {
		t.Fatalf("marshal helper config: %v", err)
	}
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatalf("write helper config: %v", err)
	}
	logPath := filepath.Join(fixture.artifact, fmt.Sprintf("helper-%s.log", name))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open helper log: %v", err)
	}
	cmd := exec.Command(fixture.helperBin, "-test.run", "^TestSkillBrowserDaemonHelper$", "-skill-browser-config="+cfgPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatalf("start helper %s: %v", name, err)
	}
	skillGuardHelperMu.Lock()
	skillGuardHelpers[index] = &skillGuardHelper{cmd: cmd, logPath: logPath, control: fixture.control[index]}
	helper := skillGuardHelpers[index]
	skillGuardHelperMu.Unlock()
	// Failure paths that abort before the explicit stop loop must not leak the
	// helper daemon: cleanup stops it too (a no-op when the explicit stop
	// already ran) before the log is closed.
	t.Cleanup(func() {
		if err := helper.stop(index); err != nil {
			t.Errorf("helper %d cleanup shutdown: %v", index, err)
		}
		_ = logFile.Close()
	})

	// Wait for this helper's own rendezvous registration (real registration,
	// not a synthetic one).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := rendezvous.List(fixture.runDir)
		if err == nil {
			for _, e := range entries {
				if e.PID == cmd.Process.Pid && e.Address != "" && e.SessionID != "" {
					return e
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("helper %s never registered its rendezvous entry (log: %s)", name, logPath)
	return rendezvous.Entry{}
}

func skillGuardStopHelper(t *testing.T, index int) {
	t.Helper()
	skillGuardHelperMu.Lock()
	helper := skillGuardHelpers[index]
	skillGuardHelperMu.Unlock()
	if helper == nil {
		return
	}
	if err := helper.stop(index); err != nil {
		t.Errorf("helper %d shutdown: %v", index, err)
	}
}

func appendFileLine(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

// ---- driver + choreography ----

func runSkillGuardDriver(t *testing.T, fixture *skillGuardFixture, authURL, milestones string, entries [2]rendezvous.Entry) error {
	t.Helper()
	frontend := filepath.Join(skillGuardRepoRoot(t), "cmd", "evener-hub", "frontend")
	driverLogPath := filepath.Join(fixture.artifact, "driver.log")
	driverLog, err := os.OpenFile(driverLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open driver log: %w", err)
	}
	defer driverLog.Close()
	// The driver must drive THE daemon each control command targets: rail row
	// order is not the helper start order, so its "session A" is pinned to
	// helper alpha's own session ref rather than to whichever row renders
	// first. (roster.go derives hub refs the same way: local:<thread id>.)
	cmd := exec.CommandContext(t.Context(), "node", filepath.Join("scripts", "skillguard", "run.mjs"),
		"--url", authURL,
		"--artifact-dir", fixture.artifact,
		"--control-path", fixture.control[0],
		"--milestone-path", milestones,
		"--session-a", skillGuardSessionRef(entries[0]),
		"--session-b", skillGuardSessionRef(entries[1]),
	)
	// The driver's Chrome is a detached process outside the driver's own
	// process group, unreachable from Go — cancellation must go through the
	// driver's SIGTERM handler, which closes the browser and exits. WaitDelay
	// force-kills a driver whose handler hangs, bounding the leak either way.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	cmd.Dir = frontend
	cmd.Stdout = driverLog
	cmd.Stderr = driverLog
	env := append([]string{"NODE_DISABLE_COMPILE_CACHE=1"}, os.Environ()...)
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("node scripts/skillguard/run.mjs exited %w (driver log: %s)", err, driverLogPath)
	}
	return nil
}

// skillGuardChoreography reacts to the driver's milestones with the two
// real-world events only the Go owner can produce: a daemon shut down (the
// capability-loss scenario) and a skill source disappearing before a queued
// input's claim (the failed-activation scenario).
type skillGuardChoreography struct {
	t           *testing.T
	fixture     *skillGuardFixture
	roster      *hubcore.Roster
	entries     [2]rendezvous.Entry
	stopOnce    sync.Once
	stopCh      chan struct{}
	doneCh      chan struct{}
	err         error
	failSeen    bool
	caplossDone bool
}

func newSkillGuardChoreography(t *testing.T, fixture *skillGuardFixture, roster *hubcore.Roster, entries [2]rendezvous.Entry) *skillGuardChoreography {
	return &skillGuardChoreography{
		t:       t,
		fixture: fixture,
		roster:  roster,
		entries: entries,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

func (c *skillGuardChoreography) stop() error {
	c.stopOnce.Do(func() { close(c.stopCh) })
	<-c.doneCh
	return c.err
}

func (c *skillGuardChoreography) run(milestones string) error {
	defer close(c.doneCh)
	if err := c.tailMilestones(milestones); err != nil {
		c.err = err
	}
	return c.err
}

// handleMilestone reacts to one driver milestone with the real-world fixture
// event only the Go owner can produce.
func (c *skillGuardChoreography) handleMilestone(m skillGuardMilestone) {
	switch m.Milestone {
	case "caploss-staged":
		if c.caplossDone {
			return
		}
		c.caplossDone = true
		// Shut the REAL daemon down through its fixture IPC and let the REAL
		// roster observe the departure.
		if err := appendFileLine(c.fixture.control[1], map[string]string{"command": "shutdown"}); err != nil {
			c.err = fmt.Errorf("caploss shutdown command: %w", err)
			return
		}
		if err := c.waitHelperExit(1, 30*time.Second); err != nil {
			c.err = fmt.Errorf("helper beta did not exit: %w", err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := c.roster.RefreshAndWait(ctx); err != nil {
			c.err = fmt.Errorf("roster refresh after helper beta exit: %w", err)
			return
		}
	case "fail-queued":
		if c.failSeen {
			return
		}
		c.failSeen = true
		// Remove the test-owned skill source BEFORE the daemon claims the
		// queued input: the still-running turn is released first, so the claim
		// happens under the deletion.
		if err := os.Remove(c.fixture.skillFile[0]); err != nil {
			c.err = fmt.Errorf("remove SKILL.md: %w", err)
			return
		}
		if err := appendFileLine(c.fixture.control[0], map[string]string{"command": "release"}); err != nil {
			c.err = fmt.Errorf("fail-activation release command: %w", err)
			return
		}
		// Wait for the failed input to be durably recorded in helper alpha's
		// transcript, then put the source back so the user's explicit retry
		// can succeed. Both the transcript write and the UI's failed-input
		// rendering are downstream of the same durable record, and the file
		// notification is the shorter path, so the restore lands before the
		// browser can render the record and the driver can click retry. A
		// premature retry would fail loudly, never pass.
		if err := c.waitForTranscriptText(0, proseFail, 30*time.Second); err != nil {
			c.err = fmt.Errorf("failed input never hit the transcript: %w", err)
			return
		}
		if err := skillGuardWriteSkill(c.fixture.skillFile[0], skillGuardSkillBody); err != nil {
			c.err = fmt.Errorf("restore SKILL.md: %w", err)
			return
		}
	}
}

// tailJSONLines follows a JSONL file from byte zero, dispatching each complete
// line to handler, until stop closes. Filesystem notifications drive the loop;
// each event re-reads whatever new bytes exist.
// tailMilestones follows the driver's milestone JSONL file from byte zero and
// dispatches every complete record to the choreography's handlers until stop
// closes. fsnotify drives the loop; each event re-reads whatever new bytes
// exist, so a burst of appends cannot be missed.
func (c *skillGuardChoreography) tailMilestones(path string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		return err
	}
	offset := int64(0)
	readNew := func() error {
		f, err := os.Open(path)
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
			// The file was replaced; start over rather than miss milestones.
			offset = 0
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var m skillGuardMilestone
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				continue // a torn partial line is re-read on the next event
			}
			c.handleMilestone(m)
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		offset = info.Size()
		return nil
	}
	if err := readNew(); err != nil {
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
			if err := readNew(); err != nil {
				return err
			}
		case werr := <-watcher.Errors:
			return werr
		}
	}
}

func (c *skillGuardChoreography) waitHelperExit(index int, timeout time.Duration) error {
	skillGuardHelperMu.Lock()
	helper := skillGuardHelpers[index]
	skillGuardHelperMu.Unlock()
	if helper == nil {
		return errors.New("helper not started")
	}
	select {
	case <-helper.wait():
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout after %s", timeout)
	}
}

// waitForTranscriptText waits until helper index's durable transcript contains
// the given text. fsnotify on the transcript file, with a pre-scan to close
// the TOCTOU window.
func (c *skillGuardChoreography) waitForTranscriptText(index int, text string, timeout time.Duration) error {
	sessionDir := filepath.Join(c.fixture.stateDir[index], "sessions")
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	if err := watcher.Add(sessionDir); err != nil {
		return err
	}
	contains := func() (string, bool) {
		entries, err := os.ReadDir(sessionDir)
		if err != nil {
			return "", false
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".transcript.jsonl") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(sessionDir, entry.Name()))
			if err == nil && strings.Contains(string(data), text) {
				return entry.Name(), true
			}
		}
		return "", false
	}
	if _, ok := contains(); ok {
		return nil
	}
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			return fmt.Errorf("no transcript under %s contained %q within %s", sessionDir, text, timeout)
		case _, ok := <-watcher.Events:
			if !ok {
				return errors.New("transcript watcher closed")
			}
			if _, found := contains(); found {
				return nil
			}
		case werr := <-watcher.Errors:
			return werr
		}
	}
}

// ---- post-run assertions ----

func skillGuardReadRequests(t *testing.T, path string) []skillGuardRequestRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read request log %s: %v", path, err)
	}
	var out []skillGuardRequestRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec skillGuardRequestRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func skillGuardTurnRequests(t *testing.T, path string) []skillGuardRequestRecord {
	t.Helper()
	var out []skillGuardRequestRecord
	for _, rec := range skillGuardReadRequests(t, path) {
		if rec.Event == "request" && rec.Kind == "turn" {
			out = append(out, rec)
		}
	}
	return out
}

func skillGuardReadMilestones(t *testing.T, path string) []skillGuardMilestone {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read milestones %s: %v", path, err)
	}
	var out []skillGuardMilestone
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m skillGuardMilestone
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("malformed milestone line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func skillGuardMilestoneDetail(t *testing.T, milestones []skillGuardMilestone, name string, into any) bool {
	t.Helper()
	for _, m := range milestones {
		if m.Milestone != name {
			continue
		}
		if err := json.Unmarshal(m.Detail, into); err != nil {
			t.Fatalf("milestone %s detail: %v", name, err)
		}
		return true
	}
	return false
}

func skillGuardAssert(t *testing.T, fixture *skillGuardFixture, milestonesPath string, entries [2]rendezvous.Entry) {
	t.Helper()
	turnsA := skillGuardTurnRequests(t, fixture.requestLog[0])
	turnsB := skillGuardTurnRequests(t, fixture.requestLog[1])
	milestones := skillGuardReadMilestones(t, milestonesPath)

	if len(turnsA) == 0 {
		t.Fatalf("helper alpha recorded no turn requests; the browser never reached a real daemon (artifacts: %s)", fixture.artifact)
	}

	// Every scenario must be present in the driver's milestones — the browser
	// report names each one and skips nothing silently.
	for _, name := range []string{
		"sessions-visible", "composer-mounted", "chip-added", "chip-removed", "chip-reselected",
		"chip-labels", "submitted-canonical", "durable-mutation", "draft-after-commit",
		"draft-staged", "thread-switched", "draft-remounted",
		"hold-turn-started", "queued", "queue-returned", "requeued", "drain-committed", "drain-released",
		"steer-turn-started", "steered", "steer-released",
		"attachment-preserved", "attachment-submitted",
		"caploss-staged", "caploss-ended", "caploss-refused",
		"fail-turn-started", "fail-queued", "fail-observed", "fail-retried",
		"delay-submitted", "delay-edited", "delay-commit-kept",
		"net-failed-kept", "net-restored",
		"done",
	} {
		if !skillGuardMilestonePresent(milestones, name) {
			t.Errorf("browser report is missing the %q scenario — the guard silently skipped it", name)
		}
	}

	// Scenario: canonical selection.
	var canonical skillGuardRequestRecord
	for _, rec := range turnsA {
		if strings.Contains(rec.Request.allText(), proseCanonical) {
			canonical = rec
			break
		}
	}
	if canonical.Seq == 0 {
		t.Fatalf("no provider request on helper alpha carried %q (artifacts: %s)", proseCanonical, fixture.artifact)
	}
	// The unchanged prose rode its own message, exactly as typed.
	if !skillGuardCallHasMessageText(canonical.Request, proseCanonical) {
		t.Errorf("canonical request lost or changed the typed prose: %s", skillGuardDumpCall(t, canonical.Request))
	}
	// The complete fixture body arrived via the canonical skill-context.
	docs := canonical.Request.skillContexts()
	if len(docs) != 1 {
		t.Errorf("canonical request carried %d skill-context documents, want exactly 1", len(docs))
	} else {
		if docs[0].Name != skillGuardSkillName {
			t.Errorf("skill-context name = %q, want %q", docs[0].Name, skillGuardSkillName)
		}
		if docs[0].Description != skillGuardSkillDescription {
			t.Errorf("skill-context description = %q, want %q", docs[0].Description, skillGuardSkillDescription)
		}
		if docs[0].Source != fixture.skillFile[0] {
			t.Errorf("skill-context source = %q, want %q", docs[0].Source, fixture.skillFile[0])
		}
		if docs[0].Instructions != skillGuardSkillBody {
			t.Errorf("skill-context instructions = %q, want the complete fixture body %q", docs[0].Instructions, skillGuardSkillBody)
		}
		if docs[0].BaseDirectory != filepath.Dir(fixture.skillFile[0]) {
			t.Errorf("skill-context base_directory = %q, want %q", docs[0].BaseDirectory, filepath.Dir(fixture.skillFile[0]))
		}
	}
	// No operative completion token: the typed "/probe" was consumed by the
	// selection. The check is over USER messages only — the token is a
	// user-side artifact, while the daemon's own system prompt legitimately
	// names the skill's source path (skills/probe/SKILL.md) and catalog entry.
	// The skill-context user message carries that same path inside the skill's
	// own document, so it is excluded too — a path is not a completion token.
	for _, m := range canonical.Request.Messages {
		if m.Role != "user" || strings.Contains(m.text(), "<skill-context>") {
			continue
		}
		if strings.Contains(m.text(), "/probe") && m.text() != proseCanonical {
			t.Errorf("a user message carries an operative completion token: %q", m.text())
		}
	}

	// The durable mutation the browser recorded for the canonical submit. The
	// IndexedDB outbox record is TRANSIENT — it is removed at the daemon's
	// turn/start ACK, which is also when the composer clears, so the driver
	// races the ACK to catch it and the milestone may carry an already-drained
	// outbox. The durable-evidence assertions that do NOT race are the
	// transcript's own skill_state record below and the transport scenario's
	// stalled-transport outbox capture; a record the driver did catch must
	// still carry the prose and the skill item.
	var durable skillGuardDurableDump
	if !skillGuardMilestoneDetail(t, milestones, "durable-mutation", &durable) {
		t.Fatal("no durable-mutation milestone")
	} else {
		for _, record := range append(append([]skillGuardDurableRecord{}, durable.Optimistic...), durable.Outbox...) {
			if record.ComposerText != proseCanonical {
				continue
			}
			if !durableInputHasText(record, proseCanonical) || !durableInputHasSkill(record, skillGuardSkillName) {
				t.Errorf("durable mutation record for the canonical submit: %s", skillGuardJSON(record))
			}
		}
	}

	// Accessible chip labels.
	var labels struct {
		ChipText     []string `json:"chipText"`
		RemoveLabels []string `json:"removeLabels"`
		Prose        string   `json:"prose"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "chip-labels", &labels) {
		t.Fatal("no chip-labels milestone")
	}
	if len(labels.ChipText) != 1 || !strings.Contains(labels.ChipText[0], skillGuardSkillName) {
		t.Errorf("chip label = %v, want one chip naming %q", labels.ChipText, skillGuardSkillName)
	}
	if len(labels.RemoveLabels) != 1 || !strings.HasPrefix(labels.RemoveLabels[0], skillGuardChipRemovePrefix) {
		t.Errorf("chip remove label = %v, want the accessible label starting %q", labels.RemoveLabels, skillGuardChipRemovePrefix)
	}

	// The durable transcript records the canonical selection with its names.
	if err := skillGuardRequireTranscriptInput(t, fixture.stateDir[0], proseCanonical, []string{skillGuardSkillName}); err != nil {
		t.Errorf("canonical selection missing from helper alpha's transcript: %v", err)
	}

	// Scenario: draft thread-switch/remount.
	var remount skillGuardComposerSnapshot
	if !skillGuardMilestoneDetail(t, milestones, "draft-remounted", &remount) {
		t.Fatal("no draft-remounted milestone")
	}
	if remount.Text != proseDraft {
		t.Errorf("draft text after thread switch/remount = %q, want %q", remount.Text, proseDraft)
	}
	if len(remount.Chips) != 1 || !strings.Contains(remount.Chips[0], skillGuardSkillName) {
		t.Errorf("draft chips after remount = %v, want %q", remount.Chips, skillGuardSkillName)
	}
	var remountDetail struct {
		Storage []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"storage"`
	}
	if skillGuardMilestoneDetail(t, milestones, "draft-remounted", &remountDetail) {
		var sawSelection bool
		for _, entry := range remountDetail.Storage {
			var record struct {
				Text       string   `json:"text"`
				SkillNames []string `json:"skillNames"`
			}
			if err := json.Unmarshal([]byte(entry.Value), &record); err != nil {
				continue
			}
			if record.Text == proseDraft && len(record.SkillNames) == 1 && record.SkillNames[0] == skillGuardSkillName {
				sawSelection = true
			}
		}
		if !sawSelection {
			t.Errorf("the persisted draft storage did not carry {text,skillNames} with %q: %s", skillGuardSkillName, skillGuardJSON(remountDetail.Storage))
		}
	}

	// Scenario: queue edit/return/drain. The held turn request exists, the
	// re-queued text reached a provider request with the skill body, and the
	// cancelled first pass never did. Requests are matched by their LAST user
	// message — history replays every earlier input verbatim, so a contains
	// match would let later turns answer for earlier ones.
	var heldQueueTurn, drainedDelivery bool
	for _, rec := range turnsA {
		switch {
		case rec.Request.lastUserText() == proseQueueTurn:
			heldQueueTurn = rec.Held
		case rec.Request.lastUserText() == proseQueueSecond:
			for _, doc := range rec.Request.skillContexts() {
				if doc.Name == skillGuardSkillName && doc.Instructions == skillGuardSkillBody {
					drainedDelivery = true
				}
			}
		}
		if strings.Contains(rec.Request.allText(), proseQueueFirst) {
			t.Errorf("the cancelled first queue pass reached a provider request (seq %d)", rec.Seq)
		}
	}
	if !heldQueueTurn {
		t.Errorf("the queue scenario's long turn was not held at its provider call; fixture IPC is broken")
	}
	if !drainedDelivery {
		t.Errorf("the re-queued %q never reached a provider request alongside the %q body", proseQueueSecond, skillGuardSkillName)
	}

	// Scenario: selected steering.
	var steeringDelivery bool
	for _, rec := range turnsA {
		if rec.Request.lastUserText() != proseSteer {
			continue
		}
		for _, doc := range rec.Request.skillContexts() {
			if doc.Name == skillGuardSkillName && doc.Instructions == skillGuardSkillBody {
				steeringDelivery = true
			}
		}
	}
	if !steeringDelivery {
		t.Errorf("the steering %q never reached a provider request with the %q body", proseSteer, skillGuardSkillName)
	}

	// Scenario: attachment preservation. The last user message carries the
	// "[image N]" anchor ahead of the prose, so the match is a contains on
	// that message alone — history still cannot answer for it.
	var attachmentDelivery bool
	for _, rec := range turnsA {
		if !strings.Contains(rec.Request.lastUserText(), proseAttachment) {
			continue
		}
		if rec.Request.imagePartCount() == 0 {
			t.Errorf("the attachment submit's provider request (seq %d) carried no image part", rec.Seq)
		} else if len(rec.Request.skillContexts()) == 0 {
			t.Errorf("the attachment submit's provider request (seq %d) lost the skill selection", rec.Seq)
		} else {
			attachmentDelivery = true
		}
	}
	if !attachmentDelivery {
		t.Errorf("no provider request carried %q with both an image part and the skill body", proseAttachment)
	}
	var attachSnapshot skillGuardComposerSnapshot
	if !skillGuardMilestoneDetail(t, milestones, "attachment-preserved", &attachSnapshot) {
		t.Fatal("no attachment-preserved milestone")
	}
	if attachSnapshot.Tiles != 1 {
		t.Errorf("attachment tiles across chip edits = %d, want 1", attachSnapshot.Tiles)
	}

	// Scenario: capability loss. Helper beta never received a turn request,
	// the draft survived, and nothing durable was written for it.
	if len(turnsB) != 0 {
		t.Errorf("helper beta received %d turn requests after its capability was lost; the composer gate leaked", len(turnsB))
	}
	var refused skillGuardComposerSnapshot
	var refusedDetail struct {
		skillGuardComposerSnapshot
		Toast   string                `json:"toast"`
		Durable skillGuardDurableDump `json:"durable"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "caploss-refused", &refusedDetail) {
		t.Fatal("no caploss-refused milestone")
	}
	refused = refusedDetail.skillGuardComposerSnapshot
	if !strings.Contains(refused.Text, proseCapLoss) || len(refused.Chips) != 1 {
		t.Errorf("the capability-loss refusal did not keep the staged draft: %s", skillGuardJSON(refused))
	}
	for _, record := range append(append([]skillGuardDurableRecord{}, refusedDetail.Durable.Outbox...), append([]skillGuardDurableRecord{}, refusedDetail.Durable.Recovery...)...) {
		if strings.Contains(record.TargetRef, entries[1].SessionID) {
			t.Errorf("a durable mutation record exists for the capability-lost session: %s", skillGuardJSON(record))
		}
	}

	// Scenario: failed activation + explicit retry. The failed input is durably
	// recorded with its names, and the ONLY provider request carrying its
	// prose was dispatched after the failure was observed — proving no
	// dependent request ran at the failed claim.
	if err := skillGuardRequireTranscriptInput(t, fixture.stateDir[0], proseFail, []string{skillGuardSkillName}); err != nil {
		t.Errorf("the failed input record is missing from helper alpha's transcript: %v", err)
	}
	var failObservedAt, failRequestAt string
	failRequests := 0
	for _, m := range milestones {
		if m.Milestone == "fail-observed" {
			failObservedAt = m.At
		}
	}
	for _, rec := range turnsA {
		if rec.Request.lastUserText() != proseFail {
			continue
		}
		failRequests++
		failRequestAt = rec.At
	}
	if failRequests != 1 {
		t.Errorf("found %d provider requests carrying %q, want exactly 1 (the explicit retry)", failRequests, proseFail)
	}
	if failObservedAt == "" || failRequestAt == "" {
		t.Errorf("missing the fail-activation ordering timestamps (request %q, observed %q)", failRequestAt, failObservedAt)
	} else {
		// Milestone timestamps are JS toISOString and request timestamps are Go
		// RFC3339Nano — lexicographic order across the two formats is
		// meaningless, so parse both and require the retry to land strictly
		// after the observed failure.
		observedAt, obsErr := time.Parse(time.RFC3339Nano, failObservedAt)
		requestAt, reqErr := time.Parse(time.RFC3339Nano, failRequestAt)
		if obsErr != nil || reqErr != nil {
			t.Errorf("parse the fail-activation ordering timestamps (observed %q: %v; request %q: %v)", failObservedAt, obsErr, failRequestAt, reqErr)
		} else if !requestAt.After(observedAt) {
			t.Errorf("the retry request (%q) did not strictly follow the observed failure (%q)", failRequestAt, failObservedAt)
		}
	}

	// Scenario: delayed accepted-send vs a newer chip edit. The submitted
	// request kept the original payload; the newer draft edits survived.
	var delaySubmitted, delayKept skillGuardComposerSnapshot
	var delayRequests int
	for _, rec := range turnsA {
		if rec.Request.lastUserText() == proseDelay {
			delayRequests++
			if len(rec.Request.skillContexts()) == 0 {
				t.Errorf("the delayed submit (seq %d) lost the chip selection from its payload", rec.Seq)
			}
		}
		if strings.Contains(rec.Request.allText(), proseDelayExtra) {
			t.Errorf("the post-submit draft edit %q reached a provider request (seq %d)", proseDelayExtra, rec.Seq)
		}
	}
	if delayRequests != 1 {
		t.Errorf("found %d provider requests dispatched for %q, want exactly 1", delayRequests, proseDelay)
	}
	if !skillGuardMilestoneDetail(t, milestones, "delay-edited", &delaySubmitted) {
		t.Fatal("no delay-edited milestone")
	}
	if !skillGuardMilestoneDetail(t, milestones, "delay-commit-kept", &delayKept) {
		t.Fatal("no delay-commit-kept milestone")
	}
	if !strings.Contains(delayKept.Text, proseDelayExtra) {
		t.Errorf("the delayed commit clobbered the newer draft text: %s", skillGuardJSON(delayKept))
	}
	if len(delayKept.Chips) != 0 {
		t.Errorf("the delayed commit resurrected the removed chip: %s", skillGuardJSON(delayKept))
	}
	_ = delaySubmitted

	// Scenario: transport loss + recovery. Exactly one dispatch, exactly one
	// durable transcript turn — the same mutation was not applied twice.
	transportRequests := 0
	for _, rec := range turnsA {
		if rec.Request.lastUserText() == proseTransport {
			transportRequests++
		}
	}
	if transportRequests != 1 {
		t.Errorf("found %d provider requests carrying %q, want exactly 1 (same-mutation delivery after the offline retry)", transportRequests, proseTransport)
	}
	transcriptTurns, err := skillGuardCountTranscriptInputs(t, fixture.stateDir[0], proseTransport)
	if err != nil {
		t.Fatalf("count transcript inputs: %v", err)
	}
	if transcriptTurns != 1 {
		t.Errorf("helper alpha's transcript recorded %d input turns with %q, want exactly 1", transcriptTurns, proseTransport)
	}
	var netFailed struct {
		Durable skillGuardDurableDump `json:"durable"`
	}
	if !skillGuardMilestoneDetail(t, milestones, "net-failed-kept", &netFailed) {
		t.Fatal("no net-failed-kept milestone")
	}
	var persistedOffline bool
	for _, record := range append(append([]skillGuardDurableRecord{}, netFailed.Durable.Outbox...), append([]skillGuardDurableRecord{}, netFailed.Durable.Recovery...)...) {
		if durableInputHasText(record, proseTransport) && durableInputHasSkill(record, skillGuardSkillName) {
			persistedOffline = true
		}
	}
	if !persistedOffline {
		t.Errorf("the offline submit's persisted input did not carry both the prose and the skill item: %s", skillGuardJSON(netFailed.Durable))
	}

	// Scenario report: name every scenario with its actual evidence.
	t.Logf("skillguard scenarios: canonical(seq=%d held=%v), draft-remount, queue(held turn + drain), steering, attachment(%d image parts), capability-loss(beta turns=%d), failed-activation(retry after %q), delayed-send(kept draft %q), transport-loss(1 dispatch)",
		canonical.Seq, canonical.Held, func() int {
			for _, rec := range turnsA {
				if strings.Contains(rec.Request.allText(), proseAttachment) {
					return rec.Request.imagePartCount()
				}
			}
			return 0
		}(), len(turnsB), failObservedAt, delayKept.Text)
}

func skillGuardMilestonePresent(milestones []skillGuardMilestone, name string) bool {
	for _, m := range milestones {
		if m.Milestone == name {
			return true
		}
	}
	return false
}

func skillGuardCallHasMessageText(call skillGuardLLMCall, text string) bool {
	for _, m := range call.Messages {
		if m.text() == text {
			return true
		}
	}
	return false
}

func durableInputHasText(record skillGuardDurableRecord, text string) bool {
	for _, item := range record.Input {
		if item.Type == "text" && item.Text == text {
			return true
		}
	}
	return false
}

func durableInputHasSkill(record skillGuardDurableRecord, name string) bool {
	for _, item := range record.Input {
		if item.Type == "skill" && item.Name == name {
			return true
		}
	}
	return false
}

// skillGuardRequireTranscriptInput asserts a durable transcript input turn
// exists with exactly the given prose and canonical skill names. The
// transcript is the daemon's own record, not the browser's.
func skillGuardRequireTranscriptInput(t *testing.T, stateDir, prose string, names []string) error {
	t.Helper()
	count, err := skillGuardCountTranscriptInputs(t, stateDir, prose)
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no transcript input turn carries %q", prose)
	}
	sessionDir := filepath.Join(stateDir, "sessions")
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".transcript.jsonl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sessionDir, entry.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, prose) || !strings.Contains(line, "skill_state") {
				continue
			}
			// Durable line shape: {"kind":"entry","turn":{"kind":"USER_INPUT",
			// "message":{"content":[{"text":…}]},"skill_state":{"input":{…}}}}
			// (agent/session transcript records).
			var record struct {
				Turn struct {
					Kind    string `json:"kind"`
					Message struct {
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"message"`
					SkillState *struct {
						Input *struct {
							OriginalText string   `json:"original_text"`
							Names        []string `json:"names"`
						} `json:"input"`
					} `json:"skill_state"`
				} `json:"turn"`
			}
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				continue
			}
			if record.Turn.Kind != "USER_INPUT" || record.Turn.SkillState == nil || record.Turn.SkillState.Input == nil {
				continue
			}
			if record.Turn.SkillState.Input.OriginalText != prose {
				continue
			}
			if strings.Join(record.Turn.SkillState.Input.Names, ",") != strings.Join(names, ",") {
				return fmt.Errorf("transcript input names = %v, want %v", record.Turn.SkillState.Input.Names, names)
			}
			return nil
		}
	}
	return fmt.Errorf("no transcript input turn carries skill_state for %q", prose)
}

func skillGuardCountTranscriptInputs(t *testing.T, stateDir, prose string) (int, error) {
	t.Helper()
	sessionDir := filepath.Join(stateDir, "sessions")
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".transcript.jsonl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sessionDir, entry.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			// The transcript's durable line shape is {"kind":"entry",
			// "turn":{"kind":"USER_INPUT","message":{"content":[{"text":…}]}}}
			// (agent/session transcript records); only USER_INPUT turns are
			// inputs — assistant turns replay nothing we want to count.
			var record struct {
				Turn struct {
					Kind    string `json:"kind"`
					Message struct {
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"message"`
				} `json:"turn"`
			}
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				continue
			}
			if record.Turn.Kind != "USER_INPUT" {
				continue
			}
			for _, part := range record.Turn.Message.Content {
				if strings.Contains(part.Text, prose) {
					count++
					break
				}
			}
		}
	}
	return count, nil
}

func skillGuardJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<marshal error: %v>", err)
	}
	return string(data)
}

func skillGuardDumpCall(t *testing.T, call skillGuardLLMCall) string {
	t.Helper()
	var parts []string
	for _, m := range call.Messages {
		parts = append(parts, fmt.Sprintf("%s: %q", m.Role, m.text()))
	}
	return strings.Join(parts, "\n")
}
