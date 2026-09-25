package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
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
	// sessionCleanupStopTimeout bounds the cleanup's in-band stop, retries
	// included: one forwarded thread/shutdown can legitimately take the host
	// daemon's own drain budget (serve's is 30s), so the window matches that drain
	// and still fits the retries inside it. There is no second arm to share it
	// with — a stop that does not settle leaves the directory in place and says so.
	sessionCleanupStopTimeout = 30 * time.Second
	// sessionCleanupRetryWait is the pause between the cleanup's stop attempts.
	sessionCleanupRetryWait = 2 * time.Second
	// sessionReadTimeout bounds each post-spawn read the check makes through the
	// controller: the fleet list and the session read. Each is a forwarded call the
	// host's hub answers from its own roster or daemon, neither starts a turn, and
	// each takes its own clock from context.Background so the attach and the spawn —
	// which have already spent their budgets — cannot starve them.
	sessionReadTimeout = time.Minute
	// sessionResolveTimeout bounds the provenance resolve. evener/launch/resolve runs
	// the host's launch checks and enumerates its provider models, the same work the
	// spawn did and the reason the spawn's own budget is three minutes, so it is
	// sized like that resolution rather than like a plain read.
	sessionResolveTimeout = 3 * time.Minute
	// sessionStopTimeout bounds the STOP assertion's own call. The assertion is
	// judged on its own clock, not the run's: the outer context has already spent
	// the attach (up to hostAttachTimeout) and up to three minutes of spawn budget,
	// and a slow attach must not turn a stop that works into a failure. It is
	// generous for the same reason the call is not instant — it is forwarded to the
	// host's hub, and the host's daemon drains its session state on the way down
	// (serve's own shutdown drain budget is 30s).
	sessionStopTimeout = 2 * time.Minute
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

// sessionModelAmbiguousWithController reports whether the host's own resolved
// model leaves the session's record unable to prove provenance: the record drops
// the provider (see the const block), so a host whose bare model id is any
// spelling this controller's configuration would record produces the same value
// for a host-resolved launch and a controller-resolved one, and no comparison of
// the record can tell them apart.
//
// "Any spelling this controller would record" is deliberately the SAME question
// sessionModelShowsControllerResolution answers — the provider id, the bare model
// id, and the qualified ref — rather than a restatement of it: an earlier version
// compared the host's bare model only against the controller's MODEL id, so a host
// resolving "<provider>/fake" (the controller's provider id as its bare model) was
// not flagged and the check passed on evidence that could not tell the two apart.
//
// The check must fail on that rather than pass: passing would report provenance
// on evidence that cannot distinguish the two, which is the false green this
// judgement exists to close. It must not skip either — a skip reads as "not
// applicable" when the truth is "this run cannot prove its claim".
func sessionModelAmbiguousWithController(hostResolvedModel, controllerProvider, controllerModel string) bool {
	ref, err := hubParseModelRef(hostResolvedModel)
	if err != nil {
		return false
	}
	return sessionModelShowsControllerResolution(ref.Model, controllerProvider, controllerModel)
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
//     controller's configuration serves. If the host resolves the same bare model
//     id this controller would record, the record cannot tell a host-resolved
//     launch from a controller-resolved one, and the check FAILS rather than
//     claim provenance its evidence cannot support.
//   - FLEET VISIBILITY: the controller's thread/list carries the ref, which is
//     the half a user sees.
//   - STOP: thread/shutdown through the controller stops the session it did not
//     host. The controller forwards the call on the owning host's client
//     (appsource's remote capability mask names Shutdown), so the stop is in
//     band: the check never reaches for a process table on the host. The
//     assertion is judged on its own clock, not the run's spent budget.
//
// Cleanup is band-only and says what it can and cannot do. The stop is registered
// before the spawn is asked for, over a ref the closure reads once it is known: a
// session that came back with a ref is stopped through the controller, retrying
// inside one bounded window. A stop that never takes leaves the directory in
// place, with a failure naming the ref, the directory, and the fact that nothing
// was cleaned up — deleting a directory a running session still references would
// be the leak this cleanup exists to prevent. When NO ref came back the outcome
// decides: a start the host refused as a request (an answered request-refusal
// frame) never started anything, so the directory goes; anything else — a lost
// response, a timeout, a dropped connection, or any other answered frame — leaves
// it, and the check does not hunt for the session: it says the truth plainly (the
// host, the directory, the session that may be running there that this check
// cannot stop, and where the operator can stop it). There is no fleet sweep, no
// directory matcher, and no process table in any of this.
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
// starts a session in it, stops that session through the controller, and removes
// the directory — unless a session it started could not be stopped, or a start's
// outcome was unknown, in which case the directory stays and the run says so.
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
	// mkdir still removes the directory it made. The stop arm below runs first
	// (cleanups are LIFO) and sets sessionLeftBehind when a session it could not
	// stop may still be running here: a directory a running session still
	// references is better left than deleted out from under it.
	var sessionLeftBehind bool
	t.Cleanup(func() {
		if sessionLeftBehind {
			t.Logf("leaving %s in place: this run could not establish that nothing it started is still running there", hostDir)
			return
		}
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
	// outer budget covers one attach attempt. The spawn gets its own lease below, and
	// the reads that follow the spawn take their own clocks as well: sharing this one
	// would let a slow attach or spawn eat into theirs.
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
	// product bug. The reads that follow take their own clocks (sessionReadTimeout,
	// sessionResolveTimeout) rather than sharing this lease.
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
	// a timeout, a dropped connection — still leaves a session running on the host.
	//
	// When a ref came back, the session is stopped through the controller in band,
	// retrying inside one bounded window — unless the check's own STOP assertion
	// already settled it (stopSettled below). When none came back, the cleanup does
	// not hunt for it: it asks whether the host REFUSED the start — an answered
	// request-refusal means nothing was started, so the directory goes — and
	// otherwise treats the outcome as unknown, says the truth plainly, and leaves
	// the directory. Nothing here reaches for a process table, a kill, a pattern, or
	// a sweep of the fleet view.
	var ref string
	// stopSettled records that the body's own STOP assertion already stopped this
	// session, so the cleanup has no stop left to make. The remote handler tolerates
	// an exited session today (shutdownThreadTolerateExited, which the forwarding
	// path lands on), so a repeat would be a tolerated no-op — but the cleanup's job
	// must not depend on that tolerance holding: once the explicit stop has returned
	// success, the session is settled and only the directory removal is left.
	var stopSettled bool
	// startParams is the request this check sends, kept beside the cleanup so the
	// refusal classification can check the request's own shape rather than assume it
	// (it carries no Input: see the boundary comment above the start call).
	startParams := appwire.ThreadStartParams{
		Harness: "evener",
		Source:  hostE2EName,
		CWD:     hostDir,
	}
	var startErr error
	t.Cleanup(func() {
		if ref == "" {
			if startErrorIsDefiniteRefusal(startErr, startParams) {
				// The host answered and refused the request, so nothing was ever
				// started: there is no session the directory could belong to. The run
				// fails on the start error itself (thread/start's own error path).
				t.Logf("host %s refused the start (%v), so nothing was started there; removing %s", host.target, startErr, hostDir)
				return
			}
			// The run has already failed at this point (thread/start's error path, or
			// the SPAWN assertion that refuses an empty ref), so this reports rather
			// than adds a failure: the session the host may be running cannot be
			// addressed from here, and the operator is told where it is.
			sessionLeftBehind = true
			t.Logf("a session may be running on host %s under %s that this check cannot stop: thread/start never returned a ref — its response was lost, or its answer carried none — so there is nothing here to address it by. It is visible in the controller's own fleet view, as a session whose working directory is %s, and can be stopped from there. Nothing was cleaned up: the directory is left in place.", host.target, hostDir, hostDir)
			return
		}
		if stopSettled {
			return
		}
		stopCtx, cancelStop := context.WithTimeout(context.Background(), sessionCleanupStopTimeout)
		defer cancelStop()
		if err := stopSessionInBand(stopCtx, client, ref); err != nil {
			sessionLeftBehind = true
			t.Errorf("left a session running on host %s: thread/shutdown of %s kept failing (%v) for its %s window, so the session could not be stopped; nothing was cleaned up — the directory is left in place, because a running session still references it", host.target, ref, err, sessionCleanupStopTimeout)
		}
	})

	started, err := clientRequest[appwire.ThreadStartResponse](startCtx, client, appwire.MethodThreadStart, startParams)
	startErr = err
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
	// unreachable from the UI. Its own clock (sessionReadTimeout), like every read
	// below.
	listCtx, cancelList := context.WithTimeout(context.Background(), sessionReadTimeout)
	defer cancelList()
	listed, err := clientRequest[appwire.ThreadListResponse](listCtx, client, appwire.MethodThreadList, appwire.ThreadListParams{})
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
	// it starts no turn, so the host's provider is never called. Its own clock too.
	readCtx, cancelRead := context.WithTimeout(context.Background(), sessionReadTimeout)
	defer cancelRead()
	read, err := clientRequest[appwire.ThreadReadResponse](readCtx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
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
	// resolved, which is the whole claim — except when the host resolves the same
	// model id this controller would record, which the ambiguity guard below
	// refuses to pass. The controller-spelling predicate only sharpens the failure
	// message when the launch was resolved here. Its own clock (sessionResolveTimeout,
	// sized for the launch checks it runs), not the run's spent budget.
	resolveParams, err := json.Marshal(appwire.LaunchConfigResolveParams{CWD: hostDir})
	if err != nil {
		t.Fatalf("encode the evener/launch/resolve params for %s: %v", hostDir, err)
	}
	resolveCtx, cancelResolve := context.WithTimeout(context.Background(), sessionResolveTimeout)
	defer cancelResolve()
	rawResolve, err := forwardHostMethod(resolveCtx, client, hostE2EName, appwire.MethodEvenerLaunchResolve, resolveParams)
	if err != nil {
		t.Fatalf("step evener/host/request %s on %q for %s: %v (the provenance claim compares the session against the model the host itself resolves)", appwire.MethodEvenerLaunchResolve, hostE2EName, hostDir, err)
	}
	var hostLaunch appwire.LaunchConfigResolved
	if err := json.Unmarshal(rawResolve, &hostLaunch); err != nil {
		t.Fatalf("decode the host's %s answer for %s: %v (%s)", appwire.MethodEvenerLaunchResolve, hostDir, err, rawResolve)
	}
	hostModel := strings.TrimSpace(hostLaunch.Effective.Model)
	recorded := strings.TrimSpace(read.Thread.ModelProvider)
	// Refuse to claim provenance when the evidence cannot support it: a host whose
	// own configuration resolves the same bare model id this controller would
	// record makes a host-resolved launch indistinguishable from a
	// controller-resolved one, because the record drops the provider. That is a
	// failure, not a skip — the run happened, and reporting "not applicable" would
	// hide a check that could not prove its claim.
	if sessionModelAmbiguousWithController(hostModel, sessionCheckControllerProvider, fakellm.ModelID) {
		t.Fatalf("cannot prove provenance for %s: the host's own configuration resolves %q, whose model id (%q) is the one THIS controller's configuration would record for a session it resolved, and the session's record drops the provider — so a host-resolved launch and a controller-resolved one are indistinguishable here. Nothing is wrong with the session itself; this run simply cannot show where it was resolved. Configure the host with a model this controller does not resolve (or run this check against a host whose model differs) and run it again", ref, hostModel, fakellm.ModelID)
	}
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
	// or matched by pattern. Its own clock (sessionStopTimeout, from
	// context.Background) is deliberate: the outer context has already paid for the
	// attach and the spawn, and this assertion must not fail because of them.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), sessionStopTimeout)
	defer cancelShutdown()
	if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
		t.Fatalf("step thread/shutdown of %s through the controller: %v (the controller must be able to stop a session it did not host, in band)", ref, err)
	}
	// The session is settled by the check's own stop, so the cleanup has no stop
	// left to make — see stopSettled.
	stopSettled = true
	t.Logf("the controller stopped %s in band", ref)
}

// stopSessionInBand stops one session through the controller, retrying while the
// context's budget lasts: one refusal can be a transient transport or an
// unsettled daemon, and the cleanup's job is to settle the question rather than to
// report the first failure.
//
// The wait between attempts wakes at once when the budget ends (a select, not a
// sleep), so no attempt starts on a spent clock and the loop cannot overshoot its
// own deadline by one retry delay. What it returns then is the last SHUTDOWN
// error, chosen by stopFailureToReport: the operator needs to know why the stop
// kept failing, not that time ran out.
func stopSessionInBand(ctx context.Context, client *appwire.Client, ref string) error {
	var lastErr, lastSubstantive error
	for {
		if _, err := clientRequest[appwire.EmptyResponse](ctx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			lastErr = err
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				lastSubstantive = err
			}
		} else {
			return nil
		}
		select {
		case <-ctx.Done():
			return stopFailureToReport(lastErr, lastSubstantive)
		case <-time.After(sessionCleanupRetryWait):
			// Both arms can be ready at once and select picks at random, so the
			// deadline wins the race here rather than being trusted to have won it:
			// no attempt starts on a spent clock.
			if ctx.Err() != nil {
				return stopFailureToReport(lastErr, lastSubstantive)
			}
		}
	}
}

// stopFailureToReport picks what the cleanup reports when the stop budget runs
// out: the last failure that was not the context ending itself, because that is
// the one that says why the stop kept failing; or the last attempt's own error
// when every attempt was cut off by the deadline, which is then the only thing
// there is to report. The caller never asks this with both nil: the budget can
// only end after at least one failed attempt.
func stopFailureToReport(lastAttempt, lastSubstantive error) error {
	if lastSubstantive != nil {
		return lastSubstantive
	}
	return lastAttempt
}

// startErrorIsDefiniteRefusal reports whether a thread/start error is the HOST
// answering with a refusal, which means it never got as far as starting a
// session: the directory this check made then holds nothing, so it can be
// removed. Every other failure — a transport failure, a timeout, a dropped
// connection, or an answered frame that is not that refusal — leaves the outcome
// unknown, and an unknown outcome keeps the directory.
//
// The discriminator is the AppWire client's own, not one invented here: an
// answered error arrives as appwire.WireError (client.go's request returns
// msg.Error.Error verbatim), while a call that never got an answer arrives as a
// context error, a transport error, or appwire.RequestNotSentError. Within the
// answered frames only CodeInvalidParams is taken as definite, because "the host
// answered" does not by itself mean nothing was started: the start path can
// answer Unavailable after the daemon is up (app_threadlifecycle.go's post-spawn
// read failure, :251) and InvalidParams after it too (:281's skill-input gate,
// reachable only for a request carrying Input). The internal-error frame the
// client SYNTHESIZES when the connection dies mid-request (client.go) is likewise
// not a refusal, and neither is a bare error.
//
// That :281 gate is why the definite case requires the request's own shape as
// well: it fires only for a request carrying Input, so a refusal frame proves that
// nothing started only when the request carried none. The check asserts that about
// the params it actually sent rather than assuming it — a future request that
// carries input then cannot have the directory removed under a session that is
// still running.
func startErrorIsDefiniteRefusal(err error, params appwire.ThreadStartParams) bool {
	if len(params.Input) > 0 {
		return false
	}
	if err == nil {
		return false
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	return wire.Code == appwire.CodeInvalidParams
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

	// The shared-id case. The record drops the provider, so a host that resolves
	// the same bare model id this controller would record yields the same value
	// either way; the check must say the claim cannot be proven rather than pass on
	// a comparison that cannot tell the two resolutions apart.
	//
	// Which bare ids count as "this controller's" is the same question
	// sessionModelShowsControllerResolution answers, and it answers with three
	// spellings: the provider id, the bare model id, and the qualified ref's model
	// part. All three make a host-resolved launch indistinguishable here.
	ambiguity := []struct {
		hostResolvedModel string
		want              bool
		why               string
	}{
		{"zai/" + fakellm.ModelID, true, "the qualified ref's model part, behind another provider"},
		{"fake/" + fakellm.ModelID, true, "this controller's own qualified ref"},
		{"someprovider/" + sessionCheckControllerProvider, true, "the controller's PROVIDER id as the host's bare model"},
		{"zai/glm-5.3-vision", false, "a host model this controller does not serve"},
		{"", false, "nothing to judge"},
		// Not a value a spawn can resolve: ParseModelRef requires provider/model.
		{fakellm.ModelID, false, "an unparseable resolve"},
	}
	for _, tc := range ambiguity {
		if got := sessionModelAmbiguousWithController(tc.hostResolvedModel, sessionCheckControllerProvider, fakellm.ModelID); got != tc.want {
			t.Errorf("sessionModelAmbiguousWithController(%q, %q, %q) = %v, want %v (%s): a false here means the check passes while the record cannot distinguish a host-resolved launch from a controller-resolved one", tc.hostResolvedModel, sessionCheckControllerProvider, fakellm.ModelID, got, tc.want, tc.why)
		}
	}
}

// TestStopFailureToReport pins what the cleanup reports when its stop budget runs
// out: the last substantive shutdown failure, not the fact that time ran out. The
// operator needs to know why the stop kept failing; a context error only stands in
// when every attempt was cut off by the deadline and there is nothing else to say.
func TestStopFailureToReport(t *testing.T) {
	refusal := appwire.Unavailable("session is not addressable")
	tests := []struct {
		name            string
		lastAttempt     error
		lastSubstantive error
		want            error
	}{
		{"a substantive failure before the deadline", context.DeadlineExceeded, refusal, refusal},
		{"the last attempt was the failure", refusal, refusal, refusal},
		{"every attempt ended on the deadline", context.DeadlineExceeded, nil, context.DeadlineExceeded},
		{"a cancellation with no substantive failure", context.Canceled, nil, context.Canceled},
	}
	for _, tc := range tests {
		if got := stopFailureToReport(tc.lastAttempt, tc.lastSubstantive); !errors.Is(got, tc.want) {
			t.Errorf("stopFailureToReport(%v, %v) = %v, want %v (%s)", tc.lastAttempt, tc.lastSubstantive, got, tc.want, tc.name)
		}
	}
}

// TestStartErrorIsDefiniteRefusal pins the classification the cleanup's no-ref
// branch depends on: a start the host REFUSED as a request (its answer is a wire
// frame carrying the request-refusal code) lets the directory go, because nothing
// was started; every other outcome — any other answered frame, a transport
// failure, a timeout, a dropped connection — leaves the directory and reports,
// because a session may be running.
//
// The definite case requires the request's own shape too: the start path has a
// post-spawn refusal for a request that carries input (the skill-input gate), so a
// refusal frame is proof only when the request carried no input. That half is
// pinned here as well, because "this check sends no input" has to be checked, not
// assumed.
//
// The values are the shapes the client really produces: appwire.WireError frames
// for an answered error (client.go's request returns msg.Error.Error verbatim),
// and appwire.RequestNotSentError / context errors / net.ErrClosed for a call that
// never got an answer.
func TestStartErrorIsDefiniteRefusal(t *testing.T) {
	noInput := appwire.ThreadStartParams{Harness: "evener", Source: hostE2EName, CWD: "/tmp/evener-session-e2e-1-2"}
	tests := []struct {
		name   string
		err    error
		params appwire.ThreadStartParams
		want   bool
	}{
		{"the host's request refusal for an input-free request", appwire.InvalidParams("model is required"), noInput, true},
		{"a refusal wrapped by a caller", fmt.Errorf("step thread/start: %w", appwire.InvalidParams("model is required")), noInput, true},
		{"a refusal frame for a request that carried input", appwire.InvalidParams("input: skill input is not supported"), appwire.ThreadStartParams{Harness: "evener", Input: []appwire.InputItem{{Type: "text", Text: "go"}}}, false},
		{"another answered frame", appwire.Unavailable("session ownership changed"), noInput, false},
		{"a launch-path answer", appwire.HubLaunchError("evener launch-check timed out"), noInput, false},
		{"the client's synthesized internal-error frame", appwire.InternalError("appwire: client closed"), noInput, false},
		{"a call that never went out", appwire.RequestNotSentError{Err: context.Canceled}, noInput, false},
		{"a timeout", context.DeadlineExceeded, noInput, false},
		{"a dropped connection", net.ErrClosed, noInput, false},
		{"a bare error from the client's other paths", errors.New("appwire thread/start: expected response"), noInput, false},
		{"no error at all", nil, noInput, false},
	}
	for _, tc := range tests {
		if got := startErrorIsDefiniteRefusal(tc.err, tc.params); got != tc.want {
			t.Errorf("startErrorIsDefiniteRefusal(%v, %+v) = %v, want %v (%s)", tc.err, tc.params, got, tc.want, tc.name)
		}
	}
}
