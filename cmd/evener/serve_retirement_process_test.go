package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/cmd/evener/internal/rvreg"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

// The daemon retirement process fixture (cmd/evener-hub's
// daemon_retirement_e2e_test.go) launches this test binary as a real daemon:
//
//	go test -c -o "$fixtureDir/evener-serve.test" ./cmd/evener
//	evener-serve.test -test.run=^TestDaemonRetirementProcessHelper$ serve --addr ...
//
// The Hub's external process-launch seam (cmd/evener-hub/spawn.go's
// daemonProcessCommand) replaces only the executable invocation, so this entry
// receives the real `serve` argv unchanged and hands it straight to
// runServeWithDeps. Everything below the LLM adapter and the retirement clock
// is the production serve lifecycle: the real flag parsing, listeners,
// session, rendezvous registration, AppWire server, retirement controller and
// release. The fixture controls the fake clock and the scripted provider over
// two inherited pipes, and the adapter reports each turn execution over the
// event pipe before blocking until the fixture releases it.
//
// The entry exposes no production endpoint or flag. It refuses to run unless
// the fixture marked the environment, so an ordinary `go test ./cmd/evener`
// reports it as an explicit skip rather than running a daemon.
const (
	// daemonRetirementProcessHelperVar gates the subprocess entry. It is not an
	// EVENER_* product variable, so TestMain's product-env scrub leaves it
	// alone — the same property that keeps EVENER_LIVE_TESTS working.
	daemonRetirementProcessHelperVar = "DAEMON_RETIREMENT_PROCESS_HELPER"
	// daemonRetirementEnvFileVar names a JSON file of the daemon environment the
	// Hub resolved. TestMain scrubs every EVENER_* variable and redirects
	// HOME/XDG; the entry restores the Hub's real environment from this file
	// before serve starts, so the daemon runs against the fixture's private
	// roots and hub token exactly as a production child would.
	daemonRetirementEnvFileVar = "DAEMON_RETIREMENT_PROCESS_ENV_FILE"
	// daemonRetirementCtlFDVar / daemonRetirementEvtFDVar name the inherited
	// pipe descriptors. ExtraFiles maps to 3 and 4; they are configurable so a
	// nested launch (a hub test-binary subprocess handing the same pipes on)
	// can preserve the numbers.
	daemonRetirementCtlFDVar = "DAEMON_RETIREMENT_PROCESS_CTL_FD"
	daemonRetirementEvtFDVar = "DAEMON_RETIREMENT_PROCESS_EVT_FD"
)

// daemonRetirementProcessEvent is one line on the helper's event pipe. The
// fixture decodes the same shape; the JSON field names are the contract.
type daemonRetirementProcessEvent struct {
	Kind      string            `json:"kind"`
	Root      string            `json:"root,omitempty"`
	Name      string            `json:"name,omitempty"`
	Entry     *rendezvous.Entry `json:"entry,omitempty"`
	Now       string            `json:"now,omitempty"`
	Armed     *bool             `json:"armed,omitempty"`
	Remaining int64             `json:"remaining_ns,omitempty"`
	Seq       int               `json:"seq,omitempty"`
	Input     string            `json:"input,omitempty"`
	Fired     *bool             `json:"fired,omitempty"`
	Err       string            `json:"err,omitempty"`
}

// daemonRetirementProcessCommand is one line on the helper's command pipe.
type daemonRetirementProcessCommand struct {
	Cmd   string `json:"cmd"`
	Nanos int64  `json:"d_ns,omitempty"`
	Seq   int    `json:"seq,omitempty"`
}

type daemonRetirementProcessHelper struct {
	ctl *os.File
	evt *os.File

	emitMu sync.Mutex
	enc    *json.Encoder

	clock *daemonRetirementProcessClock

	provMu   sync.Mutex
	provSeq  int
	releases map[int]chan struct{}
}

func newDaemonRetirementProcessHelper(ctl, evt *os.File) *daemonRetirementProcessHelper {
	h := &daemonRetirementProcessHelper{
		ctl:      ctl,
		evt:      evt,
		enc:      json.NewEncoder(evt),
		releases: map[int]chan struct{}{},
	}
	h.clock = &daemonRetirementProcessClock{h: h, now: time.Unix(2000, 0).UTC()}
	return h
}

// emit writes one event. The pipe is unbuffered, so a line is on its way to the
// fixture before the caller proceeds; the fixture is always draining.
func (h *daemonRetirementProcessHelper) emit(ev daemonRetirementProcessEvent) {
	h.emitMu.Lock()
	defer h.emitMu.Unlock()
	if err := h.enc.Encode(ev); err != nil {
		fmt.Fprintf(os.Stderr, "daemon retirement helper: emit %s: %v\n", ev.Kind, err)
	}
}

func (h *daemonRetirementProcessHelper) close() {
	if h.ctl != nil {
		_ = h.ctl.Close()
	}
	if h.evt != nil {
		_ = h.evt.Close()
	}
}

// runCommands reads the fixture's commands until the pipe closes. Advance and
// release are the only operations; both are pipe requests the fixture waits on
// an acknowledgement for, so nothing here is paced by a timer.
func (h *daemonRetirementProcessHelper) runCommands() {
	scanner := bufio.NewScanner(h.ctl)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var cmd daemonRetirementProcessCommand
		if err := json.Unmarshal(line, &cmd); err != nil {
			h.emit(daemonRetirementProcessEvent{Kind: "command_error", Err: err.Error()})
			continue
		}
		switch cmd.Cmd {
		case "advance":
			h.clock.advance(time.Duration(cmd.Nanos), cmd.Seq)
		case "release":
			h.releaseProvider(cmd.Seq)
		default:
			h.emit(daemonRetirementProcessEvent{Kind: "command_error", Err: "unknown command " + cmd.Cmd})
		}
	}
}

// nextProvider records a turn execution and returns the sequence the fixture
// must release. Only non-naming model calls reach here.
func (h *daemonRetirementProcessHelper) nextProvider(input string) int {
	h.provMu.Lock()
	h.provSeq++
	seq := h.provSeq
	ch := make(chan struct{})
	h.releases[seq] = ch
	h.provMu.Unlock()
	h.emit(daemonRetirementProcessEvent{Kind: "provider", Seq: seq, Input: input})
	return seq
}

func (h *daemonRetirementProcessHelper) releaseProvider(seq int) {
	h.provMu.Lock()
	ch := h.releases[seq]
	delete(h.releases, seq)
	h.provMu.Unlock()
	if ch != nil {
		close(ch)
	}
	h.emit(daemonRetirementProcessEvent{Kind: "released", Seq: seq})
}

func (h *daemonRetirementProcessHelper) releaseChannel(seq int) chan struct{} {
	h.provMu.Lock()
	defer h.provMu.Unlock()
	return h.releases[seq]
}

// daemonRetirementProcessClock is the helper-side retirement clock. Every
// assertion the fixture makes about evaluation, arming and resetting is an
// event this clock emits; every advance is a command it acknowledges. It never
// consults the wall clock for retirement, so the daemon's idle deadline is
// entirely fixture-driven.
type daemonRetirementProcessClock struct {
	h *daemonRetirementProcessHelper

	mu    sync.Mutex
	now   time.Time
	timer *daemonRetirementProcessTimer
}

func (c *daemonRetirementProcessClock) Now() time.Time {
	c.mu.Lock()
	now := c.now
	armed := c.timer != nil
	var remaining int64
	if armed {
		remaining = max(int64(c.timer.deadline.Sub(c.now)), 0)
	}
	c.mu.Unlock()
	c.h.emit(daemonRetirementProcessEvent{
		Kind:      "evaluated",
		Now:       now.UTC().Format(time.RFC3339Nano),
		Armed:     &armed,
		Remaining: remaining,
	})
	return now
}

func (c *daemonRetirementProcessClock) Sleep(d time.Duration)                  { time.Sleep(d) }
func (c *daemonRetirementProcessClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (c *daemonRetirementProcessClock) AfterFunc(d time.Duration, f func()) agent.RetirementTimer {
	return daemonRetirementProcessRealTimer{t: time.AfterFunc(d, f)}
}
func (c *daemonRetirementProcessClock) NewTicker(d time.Duration) agent.RetirementTicker {
	return daemonRetirementProcessRealTicker{t: time.NewTicker(d)}
}

func (c *daemonRetirementProcessClock) NewTimer(d time.Duration) agent.RetirementTimer {
	t := &daemonRetirementProcessTimer{clk: c, ch: make(chan time.Time, 1)}
	c.arm(t, d)
	return t
}

func (c *daemonRetirementProcessClock) arm(t *daemonRetirementProcessTimer, d time.Duration) {
	if d < 0 {
		d = 0
	}
	c.mu.Lock()
	c.timer = t
	t.deadline = c.now.Add(d)
	now := c.now
	c.mu.Unlock()
	c.h.emit(daemonRetirementProcessEvent{Kind: "armed", Now: now.UTC().Format(time.RFC3339Nano), Remaining: int64(d)})
}

// stop is the timer's Stop. It reports the disarm so the fixture can see the
// controller give up its interval when work arrives.
func (c *daemonRetirementProcessClock) stop(t *daemonRetirementProcessTimer) {
	c.mu.Lock()
	owned := c.timer == t
	if owned {
		c.timer = nil
	}
	c.mu.Unlock()
	if owned {
		armed := false
		c.h.emit(daemonRetirementProcessEvent{Kind: "disarmed", Armed: &armed})
	}
}

// advance moves virtual time and delivers the armed timer if its deadline has
// passed. It acknowledges with the post-command armed state, so the fixture's
// wait is a pipe acknowledgement rather than a quiet window.
func (c *daemonRetirementProcessClock) advance(d time.Duration, seq int) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	t := c.timer
	fired := false
	if t != nil && !c.now.Before(t.deadline) {
		fired = true
		select {
		case t.ch <- c.now:
		default:
		}
	}
	now := c.now
	armed := c.timer != nil
	var remaining int64
	if armed {
		remaining = max(int64(c.timer.deadline.Sub(c.now)), 0)
	}
	c.mu.Unlock()
	c.h.emit(daemonRetirementProcessEvent{
		Kind:      "advanced",
		Seq:       seq,
		Now:       now.UTC().Format(time.RFC3339Nano),
		Armed:     &armed,
		Remaining: remaining,
		Fired:     &fired,
	})
}

type daemonRetirementProcessTimer struct {
	clk      *daemonRetirementProcessClock
	ch       chan time.Time
	deadline time.Time
}

func (t *daemonRetirementProcessTimer) C() <-chan time.Time { return t.ch }
func (t *daemonRetirementProcessTimer) Reset(d time.Duration) bool {
	t.clk.arm(t, d)
	return true
}
func (t *daemonRetirementProcessTimer) Stop() bool {
	t.clk.stop(t)
	return true
}

type daemonRetirementProcessRealTimer struct{ t *time.Timer }

func (a daemonRetirementProcessRealTimer) C() <-chan time.Time        { return a.t.C }
func (a daemonRetirementProcessRealTimer) Stop() bool                 { return a.t.Stop() }
func (a daemonRetirementProcessRealTimer) Reset(d time.Duration) bool { return a.t.Reset(d) }

type daemonRetirementProcessRealTicker struct{ t *time.Ticker }

func (a daemonRetirementProcessRealTicker) C() <-chan time.Time   { return a.t.C }
func (a daemonRetirementProcessRealTicker) Stop()                 { a.t.Stop() }
func (a daemonRetirementProcessRealTicker) Reset(d time.Duration) { a.t.Reset(d) }

// daemonRetirementProcessAdapter is the scripted LLM boundary. A background
// session-naming call is answered immediately and never counted. Every other
// call is one turn execution: it reports the turn's input over the event pipe,
// blocks until the fixture releases it, and then ends the turn in a single
// round with communicate(end_turn=true) — one invocation is exactly one
// execution.
type daemonRetirementProcessAdapter struct {
	h *daemonRetirementProcessHelper
}

func (a *daemonRetirementProcessAdapter) Name() string { return "openai" }

func (a *daemonRetirementProcessAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *daemonRetirementProcessAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	// A turn's model call carries the session's tool definitions; the
	// background namer (and other schema-bound side calls) do not. Side work is
	// answered immediately and never counted, so one blocked invocation really
	// is one turn execution.
	if len(req.Tools) == 0 {
		if response, ok := scriptedSessionNamerResponse(a.Name(), req); ok {
			return response, nil
		}
		return llm.Response{
			Provider: a.Name(),
			Model:    req.Model,
			Message:  llm.Assistant(""),
			Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
		}, nil
	}
	seq := a.h.nextProvider(lastUserMessageText(req))
	release := a.h.releaseChannel(seq)
	if release == nil {
		return llm.Response{}, fmt.Errorf("daemon retirement helper: provider sequence %d has no release", seq)
	}
	select {
	case <-release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	resp := scriptedCommunicate("retirement execution complete")
	resp.Provider = a.Name()
	resp.Model = req.Model
	resp.Finish = llm.FinishReason{Reason: llm.FinishReasonToolCalls}
	return resp, nil
}

func lastUserMessageText(req llm.Request) string {
	for i := range slices.Backward(req.Messages) {
		if req.Messages[i].Role != llm.RoleUser {
			continue
		}
		return strings.TrimSpace(req.Messages[i].Text())
	}
	return ""
}

// deps wires the real serve lifecycle with only the LLM adapter and the
// retirement clock substituted. newSession, restoreSession, listen, newServer,
// the lossless event bridge and the rendezvous registration are the production
// defaults.
func (h *daemonRetirementProcessHelper) deps() serveDeps {
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	deps.newClient = func(string, io.Writer) (*llm.Client, func() error, error) {
		client := llm.NewClient()
		client.Register(&daemonRetirementProcessAdapter{h: h})
		return client, func() error { return nil }, nil
	}
	deps.buildProfile = func(*llm.Client, cmdutil.ModelRef, string) (*provider.Profile, error) {
		return provider.NewOpenAIProfile("gpt-test"), nil
	}
	deps.retirementClock = h.clock
	deps.retirementObserve = func(event, rootID string) {
		h.emit(daemonRetirementProcessEvent{Kind: "beat", Name: event, Root: rootID})
	}
	register := deps.register
	deps.register = func(reg *rvreg.Registration, dir string, entry rendezvous.Entry) error {
		if err := register(reg, dir, entry); err != nil {
			return err
		}
		h.emit(daemonRetirementProcessEvent{Kind: "registered", Entry: &entry})
		return nil
	}
	// The production bridge (ConsumeEventsLossless + server.BridgeEvent) with a
	// settlement observation added: the fixture awaits a real turn-ended event,
	// not merely the provider returning.
	bridge := deps.bridge
	deps.bridge = func(s serveServer, sess *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		srv, ok := s.(*server.Server)
		if !ok {
			bridge(s, sess, observer, onDrained)
			return
		}
		sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
			if ev.Kind == events.EventTurnEnded {
				h.emit(daemonRetirementProcessEvent{Kind: "settled", Root: sess.ID()})
			}
			server.BridgeEvent(srv, ev, observer)
		}, onDrained)
	}
	return deps
}

// TestDaemonRetirementProcessHelper is the test-binary subprocess entry. It
// skips unless the fixture marked the environment, so it is an explicit skip in
// an ordinary package run and a real daemon here.
func TestDaemonRetirementProcessHelper(t *testing.T) {
	if os.Getenv(daemonRetirementProcessHelperVar) == "" {
		t.Skip("test-binary subprocess entry for the daemon retirement process fixture")
	}
	if err := restoreDaemonRetirementEnv(); err != nil {
		t.Fatalf("restore daemon environment: %v", err)
	}
	ctl := os.NewFile(uintptr(daemonRetirementFD(daemonRetirementCtlFDVar, 3)), "daemon-retirement-ctl")
	evt := os.NewFile(uintptr(daemonRetirementFD(daemonRetirementEvtFDVar, 4)), "daemon-retirement-evt")
	if ctl == nil || evt == nil {
		t.Fatalf("daemon retirement helper: inherited control pipes missing (ctl=%v evt=%v)", ctl, evt)
	}
	h := newDaemonRetirementProcessHelper(ctl, evt)
	defer h.close()

	args := flag.Args()
	if len(args) == 0 || args[0] != "serve" {
		t.Fatalf("daemon retirement helper: argv = %v, want the real serve argv (spawnDaemon's args)", args)
	}
	go h.runCommands()

	err := runServeWithDeps(args[1:], h.deps())
	if err != nil {
		h.emit(daemonRetirementProcessEvent{Kind: "serve_error", Err: err.Error()})
		t.Fatalf("serve: %v", err)
	}
	h.emit(daemonRetirementProcessEvent{Kind: "serve_returned"})
}

// restoreDaemonRetirementEnv replays the environment the Hub resolved into this
// child, which TestMain's EVENER_* scrub and HOME/XDG redirect would otherwise
// have removed. The fixture writes fixture-owned roots, so nothing here
// touches the developer's real HOME.
func restoreDaemonRetirementEnv() error {
	path := os.Getenv(daemonRetirementEnvFileVar)
	if path == "" {
		return fmt.Errorf("%s is not set", daemonRetirementEnvFileVar)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var env map[string]string
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	for key, value := range env {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

func daemonRetirementFD(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 0 {
		return fallback
	}
	return fd
}
