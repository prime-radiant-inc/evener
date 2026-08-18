package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestE2E_TurnControlReachesTheSession drives the three mid-turn controls the
// web composer exposes — Steer, Send (which routes to turn/queue while a turn
// is running), and Stop — over the hub's AppWire socket against a real
// evener-hub process and the real evener daemon it spawns, and asserts each one
// reaches the session rather than merely returning an applied receipt.
//
// "Reaches the session" is asserted at the model boundary, which is the only
// place that cannot lie: a steer must appear in the NEXT model request the
// session makes, a queued message must open a turn of its own once the
// active turn finishes, and a stop must end the in-flight turn. A receipt
// says the hub accepted the mutation; only the next request proves the
// running loop consumed it.
//
// The provider is fakellm, so the turn stays in flight for exactly as long as
// the test declines to answer the model call — no pacing prompt, no sleeps,
// no credential, no network (AGENTS.md: evener plumbing gets a scripted
// provider; only model behaviour stays live).
func TestE2E_TurnControlReachesTheSession(t *testing.T) {
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)

	const (
		openingPrompt = "EVENER-E2E-OPENING-PROMPT"
		steerText     = "EVENER-E2E-STEER-TEXT"
		queuedText    = "EVENER-E2E-QUEUED-TEXT"
	)

	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness:         "evener",
		CWD:             stack.workDir,
		Input:           []appwire.InputItem{{Type: "text", Text: openingPrompt}},
		Model:           stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	ref := started.Thread.Evener.Ref
	if ref == "" {
		t.Fatalf("thread/start returned no evener ref: %+v", started.Thread)
	}
	// The daemon is a grandchild process that deliberately outlives the hub,
	// so killing the hub alone leaks it. Shut the thread down first —
	// t.Cleanup is LIFO, so this runs before startHubStack's hub kill.
	t.Cleanup(func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelShutdown()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Errorf("thread/shutdown left the daemon running: %v", err)
		}
	})

	// Round 1: held open, so the turn is genuinely in flight while the three
	// controls fire. This is the state the composer calls "busy".
	round1, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the session's first model request: %v", err)
	}
	if !round1.Contains(openingPrompt) {
		t.Fatalf("first model request does not carry the opening prompt; messages:\n%s",
			strings.Join(round1.Texts(), "\n"))
	}

	firstTurn := awaitActiveTurn(ctx, t, client, ref, "")
	t.Logf("turn in flight: %s", firstTurn)

	// --- Steer ------------------------------------------------------------
	steerReceipt, err := clientRequest[appwire.TurnSteerResponse](ctx, client, appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: steerText}},
	})
	if err != nil {
		t.Fatalf("turn/steer against the in-flight turn: %v", err)
	}
	if steerReceipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/steer disposition = %q, want %q", steerReceipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}

	// --- Send, which routes to turn/queue while a turn is running ---------
	queueReceipt, err := clientRequest[appwire.TurnQueueResponse](ctx, client, appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: queuedText}},
	})
	if err != nil {
		t.Fatalf("turn/queue against the in-flight turn: %v", err)
	}
	if queueReceipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/queue disposition = %q, want %q", queueReceipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
	awaitThread(ctx, t, client, ref, "queue depth 1", func(thread appwire.Thread) bool {
		return thread.Evener.Queue.Depth == 1
	})

	// Let the round finish with a tool call: steering is injected between the
	// tool round and the next model call (agent/session_tool_round.go), so
	// round 2 is where a steer that reached the session becomes visible.
	round1.RespondToolCall("read_file", map[string]any{"file_path": stack.readableFile})

	round2, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the model request after the tool round: %v", err)
	}
	if !round2.Contains(steerText) {
		t.Fatalf("the steer never reached the running loop: the model request after the tool round does not carry %q; messages:\n%s",
			steerText, strings.Join(round2.Texts(), "\n"))
	}

	// End the first turn. A evener turn ends on communicate(end_turn=true), not
	// on bare assistant text — bare text earns a repair round instead.
	round2.RespondToolCall("communicate", communicateArgs("first turn done"))

	round3, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the queued message's own turn: %v", err)
	}
	if !round3.Contains(queuedText) {
		t.Fatalf("the queued message never reached the session: no turn carried %q; messages:\n%s",
			queuedText, strings.Join(round3.Texts(), "\n"))
	}

	// --- Stop -------------------------------------------------------------
	secondTurn := awaitActiveTurn(ctx, t, client, ref, firstTurn)
	interruptReceipt, err := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	})
	if err != nil {
		t.Fatalf("turn/interrupt against the in-flight turn: %v", err)
	}
	if interruptReceipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/interrupt disposition = %q, want %q", interruptReceipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
	awaitThread(ctx, t, client, ref, "the interrupted turn to stop", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID != secondTurn
	})
}

// TestE2E_TurnControlReachesAnAgentStartedTurn is the regression its sibling
// above could not see: a turn the agent starts for itself. A goal
// continuation is the cheapest one to provoke, and it took the same path as
// every job, watch and delegate-attention wake — the daemon published a
// projector-minted turn_<n> while its mutation preconditions still held a
// turn_m<n>, so steer and stop came back Conflict("turn is not active") and
// the composer showed nothing at all.
func TestE2E_TurnControlReachesAnAgentStartedTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)

	const (
		openingPrompt = "EVENER-E2E-GOAL-OPENING"
		steerText     = "EVENER-E2E-GOAL-STEER"
	)

	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness:         "evener",
		CWD:             stack.workDir,
		Input:           []appwire.InputItem{{Type: "text", Text: openingPrompt}},
		Model:           stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	ref := started.Thread.Evener.Ref
	t.Cleanup(func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelShutdown()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Errorf("thread/shutdown left the daemon running: %v", err)
		}
	})

	// Settle the opening turn, so what starts the next one is the goal's own
	// idle kick and not a client mutation.
	opening, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the opening model request: %v", err)
	}
	firstTurn := awaitActiveTurn(ctx, t, client, ref, "")
	opening.RespondToolCall("communicate", communicateArgs("opening turn done"))
	awaitThread(ctx, t, client, ref, "the opening turn to finish", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == ""
	})

	if _, err := clientRequest[appwire.GoalSetResponse](ctx, client, appwire.MethodGoalSet, appwire.GoalSetParams{
		Ref:       ref,
		Objective: "count to ten, one number per message",
	}); err != nil {
		t.Fatalf("goal/set: %v", err)
	}

	goalRound, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the goal continuation's model request: %v", err)
	}
	goalTurn := awaitActiveTurn(ctx, t, client, ref, firstTurn)
	t.Logf("goal continuation turn in flight: %s", goalTurn)

	steerReceipt, err := clientRequest[appwire.TurnSteerResponse](ctx, client, appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: steerText}},
	})
	if err != nil {
		t.Fatalf("turn/steer against goal continuation turn %q: %v", goalTurn, err)
	}
	if steerReceipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/steer disposition = %q, want %q", steerReceipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}

	goalRound.RespondToolCall("read_file", map[string]any{"file_path": stack.readableFile})
	afterSteer, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the model request after the tool round: %v", err)
	}
	if !afterSteer.Contains(steerText) {
		t.Fatalf("the steer never reached the goal continuation's loop; messages:\n%s",
			strings.Join(afterSteer.Texts(), "\n"))
	}

	interruptReceipt, err := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	})
	if err != nil {
		t.Fatalf("turn/interrupt against goal continuation turn %q: %v", goalTurn, err)
	}
	if interruptReceipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/interrupt disposition = %q, want %q", interruptReceipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
	awaitThread(ctx, t, client, ref, "the interrupted goal turn to stop", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID != goalTurn
	})
}

// TestE2E_TurnControlReachesANotificationTurn is the regression for a turn the
// agent starts for itself with no input of its own: a job the model launched in
// the background finishes while the session is idle, and the completion
// notification wakes it.
//
// The job blocks on a file this test creates only after the session has gone
// idle. Without that barrier a short job finishes inside the same
// ProcessInputKind and the drain loop takes the wake as an interleave, so the
// test would silently exercise a different path than the one it names.
//
// It waits for a turn_m<n> rather than any active id: the daemon publishes a
// projector-minted turn_<n> from the moment of the wake until the turn's own
// boundary lands (kata c2ty keeps that window open deliberately), and steering
// against that id is the bug, not the test.
func TestE2E_TurnControlReachesANotificationTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)

	const steerText = "EVENER-E2E-NOTIFICATION-STEER"
	// A bare name, not a path: the shell tool runs in the session's working
	// directory, so nothing is interpolated into the command and a temp dir
	// with a space in it cannot break the wait loop — which would exit
	// immediately and silently test the interleave path instead.
	const releaseName = "release-the-job"
	releasePath := filepath.Join(stack.workDir, releaseName)

	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness:         "evener",
		CWD:             stack.workDir,
		Input:           []appwire.InputItem{{Type: "text", Text: "EVENER-E2E-NOTIFICATION-OPENING"}},
		Model:           stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	ref := started.Thread.Evener.Ref
	t.Cleanup(func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelShutdown()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Errorf("thread/shutdown left the daemon running: %v", err)
		}
	})

	// Round 1: launch a background job that waits on a file that does not exist
	// yet. Round 2: end the turn, so the session is idle when the job finishes.
	round1, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the opening model request: %v", err)
	}
	firstTurn := awaitActiveTurn(ctx, t, client, ref, "")
	round1.RespondToolCall("shell", map[string]any{
		"command": "while [ ! -f " + releaseName + " ]; do sleep 0.2; done",
		"mode":    "background",
	})
	round2, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the round after the background job launched: %v", err)
	}
	round2.RespondToolCall("communicate", communicateArgs("job launched"))

	awaitThread(ctx, t, client, ref, "the opening turn to finish", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == ""
	})

	// Idle. Releasing the job now makes its completion an idle wake.
	if err := os.WriteFile(releasePath, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("release the background job: %v", err)
	}

	notificationRound, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the notification turn's model request: %v", err)
	}
	var notificationTurn string
	awaitThread(ctx, t, client, ref, "the notification turn to be named", func(thread appwire.Thread) bool {
		id := thread.Evener.ActiveTurnID
		if id == "" || id == firstTurn || !strings.HasPrefix(id, "turn_m") {
			return false
		}
		notificationTurn = id
		return true
	})
	t.Logf("notification turn in flight: %s", notificationTurn)

	steerReceipt, err := clientRequest[appwire.TurnSteerResponse](ctx, client, appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: steerText}},
	})
	if err != nil {
		t.Fatalf("turn/steer against notification turn %q: %v", notificationTurn, err)
	}
	if steerReceipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/steer disposition = %q, want %q", steerReceipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}

	notificationRound.RespondToolCall("read_file", map[string]any{"file_path": stack.readableFile})
	afterSteer, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the model request after the tool round: %v", err)
	}
	if !afterSteer.Contains(steerText) {
		t.Fatalf("the steer never reached the notification turn's loop; messages:\n%s",
			strings.Join(afterSteer.Texts(), "\n"))
	}

	interruptReceipt, err := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	})
	if err != nil {
		t.Fatalf("turn/interrupt against notification turn %q: %v", notificationTurn, err)
	}
	if interruptReceipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/interrupt disposition = %q, want %q", interruptReceipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
	awaitThread(ctx, t, client, ref, "the interrupted notification turn to stop", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID != notificationTurn
	})
}

// awaitActiveTurn waits for the thread to report an active turn whose id is
// not `excluding`, and returns it. The status flip and the turn/started
// notification that populates activeTurnId land separately, which is exactly
// why the composer's own isTurnActive requires both.
func awaitActiveTurn(ctx context.Context, t *testing.T, client *appwire.Client, ref, excluding string) string {
	t.Helper()
	var turnID string
	awaitThread(ctx, t, client, ref, "an active turn id", func(thread appwire.Thread) bool {
		if thread.Status.Type != "active" {
			return false
		}
		if thread.Evener.ActiveTurnID == "" || thread.Evener.ActiveTurnID == excluding {
			return false
		}
		turnID = thread.Evener.ActiveTurnID
		return true
	})
	return turnID
}

func awaitThread(ctx context.Context, t *testing.T, client *appwire.Client, ref, what string, ok func(appwire.Thread) bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last appwire.Thread
	for time.Now().Before(deadline) {
		read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
		if err == nil {
			last = read.Thread
			if ok(last) {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", what, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for %s; last thread status=%q activeTurnId=%q queueDepth=%d",
		what, last.Status.Type, last.Evener.ActiveTurnID, last.Evener.Queue.Depth)
}

// communicateArgs is a schema-valid communicate(end_turn=true) call: evener's
// tool schema requires the full output envelope, and a call missing it is
// rejected and answered with a repair round instead of ending the turn.
func communicateArgs(message string) map[string]any {
	return map[string]any{
		"message":  message,
		"end_turn": true,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []any{},
		},
	}
}

func clientRequest[T any](ctx context.Context, client *appwire.Client, method string, params any) (T, error) {
	var out T
	err := client.Request(ctx, method, params, &out)
	return out, err
}

func newMutationID(t *testing.T) string {
	t.Helper()
	id, err := identifier.NewClientMutationID()
	if err != nil {
		t.Fatalf("new client mutation id: %v", err)
	}
	return id
}

// hubStack is a running evener-hub process on an isolated HOME, pointed at a
// fake provider, plus the workspace sessions are spawned into.
type hubStack struct {
	addr         string
	token        string
	workDir      string
	readableFile string
	model        string
	// home and binDir let a test start a daemon of its OWN alongside the hub,
	// on the same isolated HOME, so the hub discovers it through the
	// rendezvous directory rather than having spawned it.
	home   string
	binDir string
}

func (s hubStack) dialRPC(ctx context.Context, t *testing.T) *appwire.Client {
	t.Helper()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+s.token)
	transport, err := appwire.DialWebSocketWithHeaders(ctx, "ws://"+s.addr+"/rpc", nil, header)
	if err != nil {
		t.Fatalf("dial hub rpc: %v", err)
	}
	client := appwire.NewClient(transport)
	// Registered before any thread exists, so the LIFO cleanup order leaves
	// the socket open for the thread/shutdown the test registers later.
	t.Cleanup(func() { _ = client.Close() })
	// The read loop's context must outlive the test body's bounded ctx, or
	// cancelling that ctx closes the socket before the cleanup can shut the
	// thread down. Close is what ends the loop here.
	client.Start(context.Background())
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "evener-e2e-turn-control", Version: "test"},
	}); err != nil {
		t.Fatalf("appwire initialize: %v", err)
	}
	return client
}

// liveStackBinaries builds evener and evener-hub once per test binary and hands
// every stack the same directory. Each `go build` is a heavy compile running
// inside an already-parallel `go test ./...`; doing it per test spiked the
// load enough to starve an unrelated package's FIFO handshake into its
// 10-minute package timeout. The binaries are read-only to their users, so
// sharing them is safe, and the directory outlives every test in the run.
var liveStackBuild struct {
	once sync.Once
	dir  string
	err  error
}

func liveStackBinaries(t *testing.T, repoRoot string) string {
	t.Helper()
	liveStackBuild.once.Do(func() {
		// Under TestMain's root, so its RemoveAll is the only cleanup path.
		dir := filepath.Join(testEnvRoot, "live-stack-bin")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			liveStackBuild.err = err
			return
		}
		liveStackBuild.dir = dir
		for _, target := range []string{"./cmd/evener", "./cmd/evener-hub"} {
			build := exec.Command("go", "build", "-o", filepath.Join(dir, filepath.Base(target)), target)
			build.Dir = repoRoot
			if out, buildErr := build.CombinedOutput(); buildErr != nil {
				liveStackBuild.err = fmt.Errorf("build %s: %w\n%s", target, buildErr, out)
				return
			}
		}
	})
	if liveStackBuild.err != nil {
		t.Fatalf("build live-stack binaries: %v", liveStackBuild.err)
	}
	return liveStackBuild.dir
}

func startHubStack(t *testing.T, provider *fakellm.Server) hubStack {
	t.Helper()
	return startHubStackOnProvider(t, fmt.Sprintf(`schema = 1
default = "fake"

[instances.fake]
type = "openai"
api_style = "chat-completions"
base_url = %q
api_key = "fakellm-not-a-secret"
`, provider.BaseURL()), "fake/"+fakellm.ModelID)
}

// startHubStackOnProvider is startHubStack with the provider left to the
// caller, so a live-model test can point the same stack at a real instance
// whose credential comes from the environment.
func startHubStackOnProvider(t *testing.T, providersTOML, model string) hubStack {
	t.Helper()

	home := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}

	binDir := liveStackBinaries(t, repoRoot)

	evenerDir := filepath.Join(home, ".evener")
	if err := os.MkdirAll(evenerDir, 0o700); err != nil {
		t.Fatalf("create hub state root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(evenerDir, "providers.toml"), []byte(providersTOML), 0o600); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}

	workDir := t.TempDir()
	readable := filepath.Join(workDir, "NOTES.md")
	if err := os.WriteFile(readable, []byte("notes for the fake tool round\n"), 0o600); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}

	// Bind, read the address back, then release it so the hub can take it.
	// The window is tiny and far smaller than the collision risk of a fixed
	// port (the same reasoning as e2e_test.go and kata 68fm).
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate hub port: %v", err)
	}
	hubAddr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release hub port: %v", err)
	}

	hub := exec.Command(filepath.Join(binDir, "evener-hub"), "--addr", hubAddr, "--evener", filepath.Join(binDir, "evener"))
	hub.Env = append(os.Environ(), "HOME="+home)
	hubLog, err := os.Create(filepath.Join(home, "hub.log"))
	if err != nil {
		t.Fatalf("create hub log: %v", err)
	}
	hub.Stdout = hubLog
	hub.Stderr = hubLog
	if err := hub.Start(); err != nil {
		t.Fatalf("start hub: %v", err)
	}
	logPath := filepath.Join(home, "hub.log")
	t.Cleanup(func() {
		_ = hub.Process.Kill()
		_ = hub.Wait()
		_ = hubLog.Close()
		body, readErr := os.ReadFile(logPath)
		if readErr == nil {
			// Daemons are grandchildren the hub deliberately outlives, and
			// thread/shutdown is asynchronous — a daemon parked in a held
			// model round can still be alive here. Reap by the pid the hub
			// announced, so a passing test leaves no stray evener processes.
			reapDaemons(t, string(body))
		}
		if t.Failed() && readErr == nil {
			t.Logf("hub log:\n%s", body)
		}
	})

	awaitHubReady(t, hubAddr)

	token, err := os.ReadFile(filepath.Join(evenerDir, "auth-token"))
	if err != nil {
		t.Fatalf("read hub auth token: %v", err)
	}

	return hubStack{
		addr:         hubAddr,
		token:        strings.TrimSpace(string(token)),
		workDir:      workDir,
		readableFile: readable,
		model:        model,
		home:         home,
		binDir:       binDir,
	}
}

// daemonPIDPattern matches the hub's own spawn line, e.g.
// "[hub] daemon session=0348... pid=51030 log=...".
var daemonPIDPattern = regexp.MustCompile(`\bdaemon session=\S+ pid=(\d+)\b`)

func reapDaemons(t *testing.T, hubLog string) {
	t.Helper()
	for _, match := range daemonPIDPattern.FindAllStringSubmatch(hubLog, -1) {
		pid, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		_ = proc.Signal(syscall.SIGTERM)
	}
}

func awaitHubReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("hub never became reachable on %s: %v", addr, lastErr)
}
