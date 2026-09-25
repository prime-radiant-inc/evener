package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/internal/shellquote"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// sessionCheckControllerProvider is the provider instance this check's own
// controller stack serves: startHubStack writes a providers.toml with the single
// instance "fake", whose base_url is the in-process fakellm and whose model is
// fakellm.ModelID. With that model id it gives the three spellings a session
// whose launch THIS controller resolved could carry into its record — the
// provider id, the bare model id, and the qualified "fake/"+fakellm.ModelID —
// which the provenance judgement below recognises as this side's: a session
// recording one of them was resolved here, which the check exists to rule out.
//
// Mind the wire's naming: Thread.ModelProvider carries the session's MODEL, not
// a provider. The daemon publishes the bare model of the ref it was launched
// with (serve.go's rendezvous entry: Model: modelRef.Model) and the hub reads it
// back through firstLocalNonEmpty(entry.Model, entry.Provider)
// (cmd/evener-hub/internal/appsource/local_daemon.go). A live run against a host
// recorded "glm-5.3-vision" for a "zai/glm-5.3-vision" launch — the bare model —
// which is why the judgement compares bare model ids and treats the qualified
// ref only as a spelling this controller could produce.
const (
	sessionCheckControllerProvider = "fake"
	// sessionCleanupBudget bounds the cleanup's in-band stop: long enough for a
	// reconciliation to catch a session the host registers after a lost start
	// response, short enough that a wedged host cannot park the run.
	sessionCleanupBudget = 45 * time.Second
	// sessionCleanupPollWait is the pause between the reconciliation's fleet
	// reads while that budget lasts.
	sessionCleanupPollWait = 2 * time.Second
)

// sessionModelShowsControllerResolution reports whether the model a session's
// own record names is one THIS controller's configuration could have produced.
// The wire's Thread.ModelProvider carries the session's MODEL (see the const
// block above), and a launch resolved by this check's controller can carry any
// of the spellings its single provider instance produces: the provider id, the
// bare model id, or the qualified ref. All three are named, and the bare one is
// load-bearing — it is what the record actually carries, so leaving it out made
// this judgement degenerate to "the field is non-empty" and let a
// controller-resolved launch pass green. A false here is the check accepting the
// value as the host's own resolution, so a launch this controller resolved must
// not answer false.
//
// An empty record answers false: it is no resolution at all, and the check
// fails it separately as unproven rather than folding it into this question.
func sessionModelShowsControllerResolution(recorded, controllerProvider, controllerModel string) bool {
	recorded = strings.TrimSpace(recorded)
	switch recorded {
	case controllerProvider, controllerProvider + "/" + controllerModel, controllerModel:
		return true
	}
	return false
}

// sessionModelMatchesHostResolution reports whether the model a session's own
// record names is the model the HOST's own configuration resolved for the
// directory it was spawned in — the positive form of the provenance claim.
//
// The two sides spell it differently, and deliberately not by hand: the record
// carries the bare model the daemon published from the ref it was launched with
// (modelRef.Model into serve.go's rendezvous entry), while the host's resolve
// answer is the provider-qualified ref the spawn itself parsed and passed
// verbatim to the daemon (hubThreadStart requires a non-empty Effective.Model,
// hubParseModelRef parses it, and launchconfig.ToArgs writes --model <it>). So
// this parses the host's value with the same parser the spawn applies
// (hubParseModelRef, which is cmdutil.ParseModelRef) and compares its model
// part. A host answer that is empty or unparseable matches nothing: the spawn
// cannot have come from it, so the claim fails rather than passing quietly.
func sessionModelMatchesHostResolution(recorded, hostResolvedModel string) bool {
	recorded = strings.TrimSpace(recorded)
	if recorded == "" {
		return false
	}
	ref, err := hubParseModelRef(hostResolvedModel)
	if err != nil {
		return false
	}
	return recorded == ref.Model
}

// TestHostSpawnSessionE2E is the live check for the session half of the
// multi-host contract: a session the CONTROLLER asks for, from a host attached
// through evener/host/attach, is spawned and served by that host's own hub,
// appears in the controller's fleet, and can be stopped again through the
// controller — no host-side kill is involved anywhere.
//
// The spawn request deliberately carries no model. A remote spawn is resolved by
// the host (hubThreadStart on the receiving hub), so an empty model is the
// interesting case: it is what proves the host resolved the launch from ITS OWN
// configuration rather than inheriting anything from this controller. A host with
// no usable model in its own launch.toml refuses that start, which is the failure
// this test exists to catch.
//
// The fake provider below belongs to the CONTROLLER's stack and is not what the
// remote session runs on: the session runs on the host, against the host's own
// provider and credentials. That is the point of the check — the controller can
// spawn somewhere else and read the result back.
//
// The four claims, each its own assertion below:
//
//   - SPAWN: the returned ref parses and belongs to the host, not `local:`.
//   - PROVENANCE: the session's own record names the model the HOST's own
//     configuration resolves for the directory it was spawned in, asked of the
//     host through the controller's own admin proxy — and never a spelling this
//     controller's configuration serves.
//   - FLEET VISIBILITY: the controller's thread/list carries the ref, which is
//     the half a user sees.
//   - STOP: thread/shutdown through the controller stops the session it did not
//     host. The controller forwards the call on the owning host's client
//     (appsource's remote capability mask names Shutdown), so the stop is in
//     band: the check never reaches for a process table on the host.
//
// What the stop does not remove is the session RECORD the host keeps in its own
// state root — thread/shutdown stops the daemon, it does not delete the session —
// and the session runs against the host's real provider and credentials. That is
// why the gate asks for a disposable host, the same contract the deploy check
// states.
//
// Gated by EVENER_SSH_E2E=1, EVENER_SSH_E2E_HOST, and EVENER_SSH_E2E_SESSION=1.
// The session gate is separate from the read-mostly add/attach check beside this
// file because this test WRITES to the host: it creates its own directory there,
// starts a session in it, and removes both when it finishes.
// EVENER_SSH_E2E_EVENER_PATH overrides the host's evener path (default
// ~/.local/bin/evener).
func TestHostSpawnSessionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("live remote-session test: builds the live-stack binaries, dials a remote host, and starts a session on it")
	}
	// The write gate is checked first, as the deploy live check does, so an
	// un-opted-in run's skip line states the contract plainly: this check WRITES to
	// the host, and it is a separate opt-in from the read-mostly sibling's.
	if os.Getenv("EVENER_SSH_E2E_SESSION") != "1" {
		t.Skip("set EVENER_SSH_E2E_SESSION=1 (with EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST) to run the live remote-session test; this check WRITES to the host — it creates its own directory there, starts a session in it, and stops that session through the controller when it finishes")
	}
	if os.Getenv("EVENER_SSH_E2E") != "1" {
		t.Skip("set EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST to run the live remote-session test")
	}
	dest := os.Getenv("EVENER_SSH_E2E_HOST")
	if dest == "" {
		t.Skip("set EVENER_SSH_E2E_HOST to a disposable ssh destination to run the live remote-session test")
	}
	// The local hub/daemon stack this check drives needs a loopback bind. Nothing
	// here inspects a process on either side — the stop is in band — so
	// e2ecap.RequireProcessInspect is not asked for.
	e2ecap.RequireLoopbackBind(t)

	// This check creates and removes its OWN directory on the host, and spawns the
	// session with it as the working directory. That is what makes cleanup safe: it
	// can never touch a directory it did not make, and the name carries this run's
	// pid and a timestamp for that reason.
	host := newHostSSH(t, dest, os.Getenv("EVENER_SSH_E2E_USER"))
	home := host.output(`printf '%s' "$HOME"`)
	if !strings.HasPrefix(home, "/") {
		t.Fatalf("host %s HOME = %q, want an absolute path for the session's working directory", host.target, home)
	}
	token := fmt.Sprintf("evener-session-e2e-%d-%d", os.Getpid(), time.Now().UnixNano())
	hostDir := home + "/" + token
	// Plain `mkdir`, not `test -e` followed by `mkdir -p`: the atomic form refuses an
	// existing path, while the two-step form would adopt a directory created in the
	// gap — and this check's cleanup deletes whatever is in that directory.
	if out, err := host.run("mkdir " + shellquote.RemoteWord(hostDir)); err != nil {
		t.Fatalf("host %s refused to create %s (%v: %s); this check creates and removes that directory itself, so it must not adopt an existing one", host.target, hostDir, err, strings.TrimSpace(string(out)))
	}
	// Registered before anything is spawned, so a run that dies anywhere after the
	// mkdir still removes the directory it made. The stop cleanup below runs first
	// (cleanups are LIFO), so the session is stopped before its directory goes away.
	t.Cleanup(func() {
		if err := host.tryRun("rm -rf " + shellquote.RemoteWord(hostDir)); err != nil {
			t.Errorf("remove the test-owned directory %s on host %s: %v", hostDir, host.target, err)
		}
	})

	evenerPath := os.Getenv("EVENER_SSH_E2E_EVENER_PATH")
	if evenerPath == "" {
		evenerPath = "~/.local/bin/evener"
	}

	// The controller's own provider. A remote session never reaches it, which is
	// why a green run here says nothing about this side's model configuration.
	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	// The attach inside awaitHostAttached bounds itself (hostAttachTimeout), so this
	// outer budget covers one attach attempt. The spawn and the fleet read that
	// follow are fast when they work at all, so they get their own short lease below
	// rather than hiding a hang for the length of the attach budget.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)

	row, err := clientRequest[appwire.HostRow](ctx, client, appwire.MethodEvenerHostAdd, appwire.HostAddParams{
		Entry: appwire.HostEntry{
			Name:       hostE2EName,
			Address:    dest,
			User:       os.Getenv("EVENER_SSH_E2E_USER"),
			EvenerPath: evenerPath,
		},
	})
	if err != nil {
		t.Fatalf("step evener/host/add (ssh destination %q): %v", dest, err)
	}
	if row.Origin != "sidecar" {
		t.Fatalf("step evener/host/add: added row origin = %q, want %q (a host added through the wire is a sidecar entry)", row.Origin, "sidecar")
	}
	attached := awaitHostAttached(ctx, t, client, hostE2EName)
	t.Logf("attached %s: os=%s arch=%s hubVersion=%s", hostE2EName, attached.OS, attached.Arch, attached.HubVersion)

	// The check (the premise is in the file comment): the controller spawns, the
	// HOST resolves. This lease has to be generous, because the host runs several
	// sequentially bounded shell-outs to resolve the launch (two launch-checks and
	// the daemon spawn): a short one can expire during legitimate work and surface as
	// "the host cannot resolve a model" — a misleading failure that reads like a
	// product bug. The reads below reuse the flow's own context so a slow spawn
	// cannot starve them.
	startCtx, cancelStart := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelStart()
	// No Input is sent, and that is the boundary this check keeps: an input item
	// starts a real TURN against the HOST's own provider and credentials — a live
	// model call this test would neither assert nor be entitled to spend. The spawn
	// is not free of provider traffic, though, and the difference matters to anyone
	// deciding whether to run it: resolving the launch makes the host enumerate its
	// own models, and that enumeration calls each configured provider's model
	// endpoint (launchCheckModels). So a run needs the host's credentials, network,
	// and quota to be healthy. What it never does is ask for a completion.
	//
	// The stop is registered BEFORE the call, over a ref the closure reads once it
	// is known. That ordering is the point: a start whose response never arrives —
	// a timeout, a dropped connection — still leaves a session running on the host,
	// and from here on this run owns whatever is running in hostDir. The cleanup
	// stops the session by ref when one came back, and when none did it reconciles
	// instead: it looks the session up through the controller's own fleet view by
	// the working directory this check made, and stops it in band. Either way it is
	// best-effort — the body's own STOP assertion is the check — and no host-side
	// kill, process listing, or pattern ever enters the picture.
	var ref string
	t.Cleanup(func() {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), sessionCleanupBudget)
		defer cancelStop()
		if ref != "" {
			if _, err := clientRequest[appwire.EmptyResponse](stopCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
				t.Logf("thread/shutdown of %s did not report success on the way out (%v); the host's daemon idles out on its own when the stop cannot reach it", ref, err)
			}
			return
		}
		reconcileSessionInDir(stopCtx, t, client, hostDir)
	})

	started, err := clientRequest[appwire.ThreadStartResponse](startCtx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener",
		Source:  hostE2EName,
		CWD:     hostDir,
	})
	if err != nil {
		t.Fatalf("step thread/start with source %q: %v (a host that cannot resolve a model from its own launch configuration refuses here)", hostE2EName, err)
	}
	ref = started.Thread.Evener.Ref

	// SPAWN. The ref says WHICH hub owns the session: a controller-local spawn
	// would answer "local:<id>". ParseRef validates the grammar too, so a malformed
	// ref fails here rather than reading as a foreign source.
	if ref == "" {
		t.Fatalf("thread/start on host %q returned no evener ref: %+v", hostE2EName, started.Thread)
	}
	parsed, err := appwire.ParseRef(ref)
	if err != nil {
		t.Fatalf("thread/start on host %q returned an unparseable ref %q: %v", hostE2EName, ref, err)
	}
	if parsed.SourceID != hostE2EName {
		t.Fatalf("thread/start ref %q has source %q, want %q: the session must belong to the host it was spawned on", ref, parsed.SourceID, hostE2EName)
	}
	t.Logf("spawned %s on host %q", ref, hostE2EName)

	// The session must also be visible from the controller, which is the half a
	// user sees: the fleet fan-out lists the host's sessions beside the local
	// ones. A spawn that worked but never appeared here would leave the session
	// unreachable from the UI.
	listed, err := clientRequest[appwire.ThreadListResponse](ctx, client, appwire.MethodThreadList, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("step thread/list after spawning %q: %v", ref, err)
	}
	var found bool
	for _, thread := range listed.Data {
		if thread.Evener.Ref == ref {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the controller's thread/list does not carry the session it just spawned on %q (ref %q); the fleet view would never show it", hostE2EName, ref)
	}
	t.Logf("the controller's fleet list carries %s", ref)

	// The list above is roster-derived — it can carry a session whose daemon is not
	// answering — so it cannot by itself say anything is running on the host. A read
	// goes to the session's own daemon there, which is the cheap proof of liveness:
	// it starts no turn, so the host's provider is never called.
	read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("step thread/read of %s: %v (the fleet list alone cannot prove the host's daemon is answering)", ref, err)
	}
	if read.Thread.Evener.Ref != ref {
		t.Fatalf("thread/read of %s returned ref %q, want that same session", ref, read.Thread.Evener.Ref)
	}
	t.Logf("the host's daemon answered a read of %s", ref)

	// PROVENANCE. The spawn carried no model, so nothing this controller holds was
	// asked to resolve the launch: the model the session runs on was resolved by
	// the HOST from its own configuration, and the check makes that a positive
	// claim — the session's record must name the model the host's own
	// configuration resolves for the directory the session was spawned in.
	//
	// The host's answer is read through the controller's own admin proxy
	// (evener/host/request forwarding evener/launch/resolve), the same call the
	// spawn form makes for a selected host, rather than by reading launch.toml
	// over ssh: the resolution is layered (config root, project, env floor) and
	// evener/launch/resolve is the product's own answer to "what would a session
	// started now run with" — the answer hubThreadStart itself applies to the
	// spawn. Comparing against it cannot be satisfied by a launch THIS controller
	// resolved, which is the whole claim; the controller-spelling predicate below
	// only sharpens the failure message when that is what happened.
	resolveParams, err := json.Marshal(appwire.LaunchConfigResolveParams{CWD: hostDir})
	if err != nil {
		t.Fatalf("encode the evener/launch/resolve params for %s: %v", hostDir, err)
	}
	rawResolve, err := forwardHostMethod(ctx, client, hostE2EName, appwire.MethodEvenerLaunchResolve, resolveParams)
	if err != nil {
		t.Fatalf("step evener/host/request %s on %q for %s: %v (the provenance claim compares the session against the model the host itself resolves)", appwire.MethodEvenerLaunchResolve, hostE2EName, hostDir, err)
	}
	var hostLaunch appwire.LaunchConfigResolved
	if err := json.Unmarshal(rawResolve, &hostLaunch); err != nil {
		t.Fatalf("decode the host's %s answer for %s: %v (%s)", appwire.MethodEvenerLaunchResolve, hostDir, err, rawResolve)
	}
	hostModel := strings.TrimSpace(hostLaunch.Effective.Model)
	recorded := strings.TrimSpace(read.Thread.ModelProvider)
	if !sessionModelMatchesHostResolution(recorded, hostModel) {
		if recorded == "" {
			t.Fatalf("the session on host %q records no model at all, so where its launch was resolved cannot be seen from here; the host's own configuration resolves %q for %s", hostE2EName, hostModel, hostDir)
		}
		if sessionModelShowsControllerResolution(recorded, sessionCheckControllerProvider, fakellm.ModelID) {
			t.Fatalf("the session on host %q records model %q, which is THIS controller's own configuration, while the host's own configuration resolves %q for %s: the launch was resolved here rather than by the host", hostE2EName, recorded, hostModel, hostDir)
		}
		t.Fatalf("the session on host %q records model %q, but the host's own configuration resolves %q for %s: the session is not running on the model the host's configuration chose", hostE2EName, recorded, hostModel, hostDir)
	}
	t.Logf("the host resolved the launch from its own configuration: %s records model %q, the model the host resolves for %s", ref, recorded, hostDir)

	// STOP. thread/shutdown for a ref the controller does not host used to be
	// refused locally while remote thread capabilities stayed masked, without the
	// host ever being asked; the mask now names Shutdown and the controller
	// forwards the call on the owning host's client, so this call is the
	// assertion. A refusal here is "the controller cannot stop a session it did
	// not host"; success is the capability. Nothing on the host is killed, listed,
	// or matched by pattern.
	shutdownCtx, cancelShutdown := context.WithTimeout(ctx, time.Minute)
	defer cancelShutdown()
	if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
		t.Fatalf("step thread/shutdown of %s through the controller: %v (the controller must be able to stop a session it did not host, in band)", ref, err)
	}
	t.Logf("the controller stopped %s in band", ref)
}

// reconcileSessionInDir stops a session this run started whose start response
// never named it, so its ref was never known. The only handle on such a session
// is the directory this check made and spawned in — the spawn carried
// CWD: hostDir, a name unique to this run (pid + nanosecond timestamp) — so the
// controller's own fleet view is searched for a session whose record places it
// in that directory, and each match is stopped in band through the controller,
// the same thread/shutdown a user's client would send.
//
// The search repeats while the budget lasts, because a response lost during the
// start can arrive before the host registered the session: one list immediately
// after the failure can legitimately be too early. It reports what it found
// rather than passing quietly, and a stop that fails is an error, not a log
// line — a session this run started must not be left behind.
func reconcileSessionInDir(ctx context.Context, t *testing.T, client *appwire.Client, workingDir string) {
	t.Helper()
	deadline := time.Now().Add(sessionCleanupBudget)
	for {
		listed, err := clientRequest[appwire.ThreadListResponse](ctx, client, appwire.MethodThreadList, appwire.ThreadListParams{})
		if err != nil {
			t.Logf("could not list sessions to reconcile the directory %s: %v", workingDir, err)
			return
		}
		matches := 0
		for _, thread := range listed.Data {
			if !sessionInDirectory(thread, workingDir) {
				continue
			}
			matches++
			if _, err := clientRequest[appwire.EmptyResponse](ctx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: thread.Evener.Ref}); err != nil {
				t.Errorf("stop %s, the session the lost start response left under %s: %v", thread.Evener.Ref, workingDir, err)
				continue
			}
			t.Logf("stopped %s, the session the lost start response left under %s", thread.Evener.Ref, workingDir)
		}
		if matches > 0 {
			return
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Logf("no session is registered under %s, so a lost start response left nothing to stop", workingDir)
			return
		}
		time.Sleep(sessionCleanupPollWait)
	}
}

// sessionInDirectory reports whether a listed session is the one a run working
// in workingDir started. Its ref must belong to the host the run attached —
// which is what keeps a local session with a coincidentally similar path out of
// the match — and its record must place it in that directory.
func sessionInDirectory(thread appwire.Thread, workingDir string) bool {
	if !sameDirectory(thread.CWD, workingDir) {
		return false
	}
	parsed, err := appwire.ParseRef(thread.Evener.Ref)
	return err == nil && parsed.SourceID == hostE2EName
}

// sameDirectory compares a session's recorded working directory with the one
// this check addressed: equal after trimming a trailing separator, or ending in
// the same leaf name. The leaf is the fallback for a host that records the
// canonical path of a directory this check addressed through a link (macOS
// resolves $HOME through one), and it is a safe key for exactly one run because
// it carries that run's pid and nanosecond timestamp.
func sameDirectory(recorded, want string) bool {
	recorded = strings.TrimRight(strings.TrimSpace(recorded), "/")
	want = strings.TrimRight(strings.TrimSpace(want), "/")
	if recorded == want {
		return true
	}
	leaf := path.Base(want)
	return leaf != "" && leaf != "." && leaf != "/" && strings.HasSuffix(recorded, "/"+leaf)
}

// TestSessionModelProvenance pins the provenance judgement the live check makes,
// with no host and no ssh: the seam is the pair of pure predicates above.
//
// The controller these tables name is the one startHubStack builds — a single
// provider instance "fake" serving the fakellm model — so its possible
// spellings are known exactly. The bare model id is the case that matters: the
// wire carries bare model ids (a live run recorded "glm-5.3-vision" for a
// "zai/glm-5.3-vision" launch), so a judgement that recognised only "fake" and
// "fake/fake-test-model" degenerated to "the field is non-empty", and a
// regression to a controller-resolved launch would have passed green.
func TestSessionModelProvenance(t *testing.T) {
	controllerResolution := []struct {
		recorded string
		want     bool
		why      string
	}{
		{fakellm.ModelID, true, "the bare model id — what a controller-resolved launch actually records"},
		{"fake", true, "the provider instance id, the record's fallback when no model is named"},
		{"fake/" + fakellm.ModelID, true, "the qualified ref this controller's configuration serves"},
		{"", false, "no value at all: not a controller resolution; the check fails it separately as unproven"},
		{"glm-5.3-vision", false, "a host-shaped model — the host's own resolution"},
	}
	for _, tc := range controllerResolution {
		got := sessionModelShowsControllerResolution(tc.recorded, sessionCheckControllerProvider, fakellm.ModelID)
		if got == tc.want {
			continue
		}
		t.Errorf("sessionModelShowsControllerResolution(%q) = %v, want %v (%s); a false here means the check accepts this value as the HOST's resolution, so a launch this controller resolved would pass green", tc.recorded, got, tc.want, tc.why)
	}

	hostResolution := []struct {
		recorded, hostResolvedModel string
		want                        bool
	}{
		{"glm-5.3-vision", "zai/glm-5.3-vision", true},
		{"claude-sonnet-4-5", "anthropic/claude-sonnet-4-5", true},
		{fakellm.ModelID, "zai/glm-5.3-vision", false},
		{"glm-5.3-vision", "anthropic/claude-sonnet-4-5", false},
		{"", "zai/glm-5.3-vision", false},
		{"glm-5.3-vision", "", false},
		// A bare resolved value cannot have produced a spawn: hubThreadStart
		// parses Effective.Model with ParseModelRef, which requires provider/model.
		{"glm-5.3-vision", "glm-5.3-vision", false},
	}
	for _, tc := range hostResolution {
		if got := sessionModelMatchesHostResolution(tc.recorded, tc.hostResolvedModel); got != tc.want {
			t.Errorf("sessionModelMatchesHostResolution(%q, %q) = %v, want %v", tc.recorded, tc.hostResolvedModel, got, tc.want)
		}
	}
}

// TestSessionDirectoryMatch pins the reconciliation's matching rule: which
// listed session the lost-response arm of the cleanup is allowed to stop. The
// directory is unique to one run, so the rule must match that run's session and
// nothing else — not a sibling directory whose name merely shares a prefix, and
// not work the run did not start.
func TestSessionDirectoryMatch(t *testing.T) {
	const dir = "/Users/jesse/evener-session-e2e-123-456"
	directories := []struct {
		recorded string
		want     bool
	}{
		{dir, true},
		{dir + "/", true},
		// A host can record the canonical path of a directory addressed through a
		// link (macOS resolves $HOME through one); the leaf is what survives.
		{"/System/Volumes/Data" + dir, true},
		{"/Users/jesse/evener-session-e2e-123-457", false},
		{dir + "/sub", false},
		{"/Users/jesse", false},
		{"", false},
	}
	for _, tc := range directories {
		if got := sameDirectory(tc.recorded, dir); got != tc.want {
			t.Errorf("sameDirectory(%q, %q) = %v, want %v", tc.recorded, dir, got, tc.want)
		}
	}

	hostSession := appwire.Thread{CWD: dir, Evener: appwire.EvenerThread{Ref: hostE2EName + ":t1"}}
	if !sessionInDirectory(hostSession, dir) {
		t.Errorf("sessionInDirectory(%+v, %q) = false, want true: the host's own session in this run's directory is what the reconciliation must stop", hostSession, dir)
	}
	for _, tc := range []struct {
		name   string
		thread appwire.Thread
	}{
		{"another host's ref", appwire.Thread{CWD: dir, Evener: appwire.EvenerThread{Ref: "other:t1"}}},
		{"a local ref", appwire.Thread{CWD: dir, Evener: appwire.EvenerThread{Ref: "local:t1"}}},
		{"an unparseable ref", appwire.Thread{CWD: dir, Evener: appwire.EvenerThread{Ref: "t1"}}},
		{"another directory", appwire.Thread{CWD: "/Users/jesse", Evener: appwire.EvenerThread{Ref: hostE2EName + ":t1"}}},
	} {
		if sessionInDirectory(tc.thread, dir) {
			t.Errorf("sessionInDirectory(%+v, %q) = true (%s), want false: the reconciliation may only stop this run's own session", tc.thread, dir, tc.name)
		}
	}
}
