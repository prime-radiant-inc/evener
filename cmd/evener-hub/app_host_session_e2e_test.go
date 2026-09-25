package hub

import (
	"context"
	"encoding/json"
	"fmt"
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
	// sessionCleanupBudget bounds the cleanup's in-band stop: long enough for a
	// reconciliation to catch a session the host registers after a lost start
	// response, short enough that a wedged host cannot park the run.
	sessionCleanupBudget = 45 * time.Second
	// sessionCleanupPollWait is the pause between the reconciliation's fleet
	// reads while that budget lasts.
	sessionCleanupPollWait = 2 * time.Second
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
// the provider (see the const block), so a host that resolves the same bare model
// id this controller's configuration would record produces the same value for a
// host-resolved launch and a controller-resolved one, and no comparison of the
// record can tell them apart.
//
// The check must fail on that rather than pass: passing would report provenance
// on evidence that cannot distinguish the two, which is the false green this
// judgement exists to close. It must not skip either — a skip reads as "not
// applicable" when the truth is "this run cannot prove its claim".
func sessionModelAmbiguousWithController(hostResolvedModel, controllerModel string) bool {
	ref, err := hubParseModelRef(hostResolvedModel)
	if err != nil {
		return false
	}
	return ref.Model == strings.TrimSpace(controllerModel)
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
// Cleanup settles its own question in band: the session is stopped through the
// controller (retried inside a cleanup budget) and, when that never takes,
// reconciled by the unique directory this check made. A session that still cannot
// be stopped leaves that directory in place, with a failure that names the ref,
// the directory, and the fact that nothing was cleaned up — deleting a directory
// a running session still references would be the leak this cleanup exists to
// prevent, one step later.
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
	// mkdir still removes the directory it made. The stop arm below runs first
	// (cleanups are LIFO) and sets sessionLeftBehind when a session it could not
	// stop may still be running here: a directory a running session still
	// references is better left than deleted out from under it.
	var sessionLeftBehind bool
	t.Cleanup(func() {
		if sessionLeftBehind {
			t.Logf("leaving %s in place: a session this run started may still be running there (see the failure above); remove it once the session is gone", hostDir)
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
	// and from here on this run owns whatever is running in hostDir.
	//
	// It stops the session by ref when one came back, retrying inside the cleanup
	// budget, and falls back to the reconciliation when that never takes: the
	// session is looked up through the controller's own fleet view by the working
	// directory this check made and stopped in band. If all of that still leaves a
	// session running, the directory is left in place and the failure says so —
	// cleanup that deleted the directory anyway would be the leak this exists to
	// prevent, one step later. No host-side kill, process listing, or pattern ever
	// enters the picture.
	var ref string
	t.Cleanup(func() {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), sessionCleanupBudget)
		defer cancelStop()
		// why is what the messages below report as the reason the direct stop did
		// not settle the session; it names the ref whenever there was one.
		why := "thread/start returned no ref"
		refStopped := false
		if ref != "" {
			if err := stopSessionInBand(stopCtx, client, ref); err == nil {
				refStopped = true
			} else {
				why = fmt.Sprintf("thread/shutdown of %s kept failing (%v) through the cleanup budget", ref, err)
			}
		}
		if refStopped {
			return
		}
		reconciled := reconcileSessionInDir(stopCtx, client, hostDir)
		switch judgeSessionCleanup(refStopped, reconciled.looked, reconciled.matched, reconciled.stopped) {
		case sessionCleanupSettled:
			if reconciled.matched > 0 {
				t.Logf("stopped %d session(s) under %s through the reconciliation (%s)", reconciled.stopped, hostDir, why)
				return
			}
			t.Logf("nothing is registered under %s, so nothing was left running there (%s)", hostDir, why)
		case sessionCleanupLeftBehind:
			sessionLeftBehind = true
			t.Errorf("left a session running on host %s: %s, and %d of %d session(s) under %s could not be stopped either; nothing was cleaned up — the directory is left in place, because a running session still references it", host.target, why, reconciled.matched-reconciled.stopped, reconciled.matched, hostDir)
		case sessionCleanupUnreachable:
			sessionLeftBehind = true
			t.Errorf("left work behind on host %s: %s, and the controller could not be asked whether a session still runs under %s; nothing was cleaned up — the directory is left in place", host.target, why, hostDir)
		}
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
	// resolved, which is the whole claim — except when the host resolves the same
	// model id this controller would record, which the ambiguity guard below
	// refuses to pass. The controller-spelling predicate only sharpens the failure
	// message when the launch was resolved here.
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
	// Refuse to claim provenance when the evidence cannot support it: a host whose
	// own configuration resolves the same bare model id this controller would
	// record makes a host-resolved launch indistinguishable from a
	// controller-resolved one, because the record drops the provider. That is a
	// failure, not a skip — the run happened, and reporting "not applicable" would
	// hide a check that could not prove its claim.
	if sessionModelAmbiguousWithController(hostModel, fakellm.ModelID) {
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
	t.Logf("the controller stopped %s in band", ref)
}

// sessionCleanupVerdict is what the cleanup's stop attempts established about the
// session this run started.
type sessionCleanupVerdict int

const (
	// sessionCleanupSettled: nothing this run started is still running under the
	// directory, so the directory may be removed.
	sessionCleanupSettled sessionCleanupVerdict = iota
	// sessionCleanupLeftBehind: a session this run started could not be stopped,
	// so the directory must stay.
	sessionCleanupLeftBehind
	// sessionCleanupUnreachable: the controller could not be asked whether a
	// session is still running, so nothing is known and the directory must stay.
	sessionCleanupUnreachable
)

func (v sessionCleanupVerdict) String() string {
	switch v {
	case sessionCleanupSettled:
		return "settled"
	case sessionCleanupLeftBehind:
		return "left-behind"
	case sessionCleanupUnreachable:
		return "unreachable"
	}
	return "unknown"
}

// judgeSessionCleanup decides what the cleanup established from its stop
// attempts: whether the directory may be removed, or whether a session may still
// be running under it and must be left alone.
//
//   - the known ref's own stop settling the session ends the question;
//   - otherwise the reconciliation's answer is what counts: every matching session
//     stopped, or none found, settles it; a match that would not stop is left
//     behind; and a fleet view that could not be read leaves it unknown.
func judgeSessionCleanup(refStopped, looked bool, matched, stopped int) sessionCleanupVerdict {
	if refStopped {
		return sessionCleanupSettled
	}
	if !looked {
		return sessionCleanupUnreachable
	}
	if stopped < matched {
		return sessionCleanupLeftBehind
	}
	return sessionCleanupSettled
}

// stopSessionInBand stops one session through the controller, retrying while the
// context's budget lasts: one refusal can be a transient transport or an
// unsettled daemon, and the cleanup's job is to settle the question rather than
// to report the first failure. The last error is returned when the budget runs
// out.
func stopSessionInBand(ctx context.Context, client *appwire.Client, ref string) error {
	var lastErr error
	for {
		if _, err := clientRequest[appwire.EmptyResponse](ctx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			lastErr = err
		} else {
			return nil
		}
		if ctx.Err() != nil {
			return lastErr
		}
		time.Sleep(sessionCleanupPollWait)
	}
}

// sessionReconciliation is what one reconciliation pass established. Looked is
// false when the fleet view could not be read at all, so nothing about the
// directory is known; Matched counts the sessions whose own records place them in
// this run's directory, and Stopped counts those the controller stopped in band.
type sessionReconciliation struct {
	looked  bool
	matched int
	stopped int
}

// reconcileSessionInDir stops sessions this run started whose start response
// never named them, so their refs were never known. The only handle on such a
// session is the directory this check made and spawned in — the spawn carried
// CWD: hostDir, a name unique to this run (pid + nanosecond timestamp) — so the
// controller's own fleet view is searched for sessions whose records place them
// in that directory, and each match is stopped in band through the controller,
// the same thread/shutdown a user's client would send.
//
// The search repeats while the budget lasts, because a response lost during the
// start can arrive before the host registered the session: one list immediately
// after the failure can legitimately be too early. The caller judges what it
// established — a match that would not stop is not silently dropped here.
func reconcileSessionInDir(ctx context.Context, client *appwire.Client, workingDir string) sessionReconciliation {
	var out sessionReconciliation
	deadline := time.Now().Add(sessionCleanupBudget)
	for {
		listed, err := clientRequest[appwire.ThreadListResponse](ctx, client, appwire.MethodThreadList, appwire.ThreadListParams{})
		if err != nil {
			return out
		}
		out.looked = true
		for _, thread := range listed.Data {
			if !sessionInDirectory(thread, workingDir) {
				continue
			}
			out.matched++
			if _, err := clientRequest[appwire.EmptyResponse](ctx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: thread.Evener.Ref}); err != nil {
				continue
			}
			out.stopped++
		}
		if out.matched > 0 || time.Now().After(deadline) || ctx.Err() != nil {
			return out
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
// this check addressed. After trimming a trailing separator the two are equal,
// or one is the other with a prefix in front: a host can record the canonical
// path of a directory this check addressed through a link (macOS resolves $HOME
// through one), and the canonical form is the check's path with a prefix ahead of
// it — so in either direction the shorter path is the tail of the longer one.
//
// The comparison is the whole path, not its leaf: a different parent with an
// equal leaf is no one's tail and does not match. Two different directories
// sharing the tail this matches would have to share this run's pid and nanosecond
// timestamp too, which is what makes the tail safe as the key.
func sameDirectory(recorded, want string) bool {
	recorded = strings.TrimRight(strings.TrimSpace(recorded), "/")
	want = strings.TrimRight(strings.TrimSpace(want), "/")
	if recorded == "" || want == "" {
		return false
	}
	if recorded == want {
		return true
	}
	return strings.HasSuffix(recorded, want) || strings.HasSuffix(want, recorded)
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
	ambiguity := []struct {
		hostResolvedModel string
		want              bool
	}{
		{"zai/" + fakellm.ModelID, true},
		{"fake/" + fakellm.ModelID, true},
		{"zai/glm-5.3-vision", false},
		{"", false},
		// Not a value a spawn can resolve: ParseModelRef requires provider/model.
		{fakellm.ModelID, false},
	}
	for _, tc := range ambiguity {
		if got := sessionModelAmbiguousWithController(tc.hostResolvedModel, fakellm.ModelID); got != tc.want {
			t.Errorf("sessionModelAmbiguousWithController(%q, %q) = %v, want %v: a false here means the check passes while the record cannot distinguish a host-resolved launch from a controller-resolved one", tc.hostResolvedModel, fakellm.ModelID, got, tc.want)
		}
	}
}

// TestSessionCleanupVerdict pins when the cleanup may remove the host directory
// and when it must leave it: a session this run started that could not be stopped
// must not have its directory deleted out from under it, and a controller that
// could not be asked at all is not evidence that nothing is running.
func TestSessionCleanupVerdict(t *testing.T) {
	tests := []struct {
		name       string
		refStopped bool
		looked     bool
		matched    int
		stopped    int
		want       sessionCleanupVerdict
	}{
		{"the known ref stopped", true, false, 0, 0, sessionCleanupSettled},
		{"no match found after the ref's stop failed", false, true, 0, 0, sessionCleanupSettled},
		{"every match stopped", false, true, 1, 1, sessionCleanupSettled},
		{"a match would not stop", false, true, 1, 0, sessionCleanupLeftBehind},
		{"one of two matches would not stop", false, true, 2, 1, sessionCleanupLeftBehind},
		{"the fleet view could not be read", false, false, 0, 0, sessionCleanupUnreachable},
	}
	for _, tc := range tests {
		got := judgeSessionCleanup(tc.refStopped, tc.looked, tc.matched, tc.stopped)
		if got != tc.want {
			t.Errorf("judgeSessionCleanup(refStopped=%v, looked=%v, matched=%d, stopped=%d) = %v, want %v (%s)", tc.refStopped, tc.looked, tc.matched, tc.stopped, got, tc.want, tc.name)
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
	pairs := []struct {
		recorded, wanted string
		want             bool
	}{
		{dir, dir, true},
		{dir + "/", dir, true},
		// A host can record the canonical path of a directory this check addressed
		// through a link (macOS resolves $HOME through one). The canonical form is
		// the check's path with a prefix in front, so the shorter path is the tail
		// of the longer one — in either direction.
		{"/System/Volumes/Data" + dir, dir, true},
		{dir, "/System/Volumes/Data" + dir, true},
		{"/private" + dir, dir, true},
		{dir, "/private" + dir, true},
		// A different parent is not a tail, however equal the leaf looks.
		{"/Users/other/evener-session-e2e-123-456", dir, false},
		{dir, "/Users/other/evener-session-e2e-123-456", false},
		{"/Users/jesse/evener-session-e2e-123-457", dir, false},
		{dir + "/sub", dir, false},
		{"/Users/jesse", dir, false},
		{"", dir, false},
	}
	for _, tc := range pairs {
		if got := sameDirectory(tc.recorded, tc.wanted); got != tc.want {
			t.Errorf("sameDirectory(%q, %q) = %v, want %v", tc.recorded, tc.wanted, got, tc.want)
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
