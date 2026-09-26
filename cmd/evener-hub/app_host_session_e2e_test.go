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
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/e2ecap"
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
	// sessionSpawnTimeout bounds the spawn: the host resolves the launch with
	// several sequentially bounded shell-outs (two launch-checks and the daemon
	// spawn), so the lease is generous for one spawn. It counts from
	// context.Background, not from the run's outer budget, so a slow attach cannot
	// shorten it — the attach bounds itself inside awaitHostAttached.
	sessionSpawnTimeout = 3 * time.Minute
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
	// sessionStopTimeout bounds one thread/shutdown through the controller: the STOP
	// assertion's call, and the cleanup's single attempt when the assertion did not
	// settle the session first. The call is forwarded to the host's hub and is not
	// instant — the session's daemon drains its state on the way down, a drain the
	// daemon itself bounds at 30 seconds (cmd/evener/serve.go's
	// shutdownDrainWaitBudget) — so the window is generous. Each use takes its own
	// clock from context.Background, not the run's: the outer context has already
	// spent the attach and the spawn, and a slow attach must not turn a stop that
	// works into a failure.
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
// Cleanup is band-only, and its rules are small enough to state whole:
//
//   - The directory this run creates on the host is removed only once the mkdir
//     that made it has answered success. A creation this run cannot confirm — the
//     name already exists, or ssh never said whether it ran — is not removed: the
//     run fails there, before the spawn, and names the path. Nothing is ever
//     deleted on a guess, and a creation whose answer was lost can leave one empty
//     directory behind, which the failure names.
//   - A stop is registered before the spawn is asked for, over a target the
//     closure reads once the SPAWN assertion has armed it: a session that came
//     back with a ref this check may stop — one that parses as the host's own,
//     never the raw response — is stopped through the controller in one attempt
//     on its own clock; an earlier design retried inside a window, which is not
//     needed for a cleanup whose outcome the operator reads. A stop that does not
//     succeed leaves the directory in place, with a failure naming the ref, the
//     directory, and the fact that nothing was cleaned up — deleting a directory
//     a running session still references would be the leak this cleanup exists to
//     prevent.
//   - When NO such ref came back, whatever the start's failure was, the directory
//     stays: no answer this check reads proves nothing started there, so it does
//     not hunt for the session — and it no longer sorts "the host refused, nothing
//     started" from "the answer was lost, something may have": that sorting needed
//     the wire's error taxonomy to be right, and its only prize was removing an
//     empty directory on a run that had already failed. The run says the truth
//     plainly (the host, the directory, the session that may be running there that
//     this check cannot stop, and where the operator can stop it). There is no
//     fleet sweep, no directory matcher, and no process table in any of this.
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
// the directory — unless it could not confirm the directory's creation, or a
// session it started (or may have started but cannot address) could not be
// stopped, in which case the directory stays and the run says so.
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
	// Plain `mkdir`, not `mkdir -p`: the atomic form refuses a path that already
	// exists. A failure here is fatal and removes nothing — the name may be someone
	// else's directory, and when ssh never said whether the command ran, this run
	// cannot prove the directory is its own. Either way the failure names the path,
	// so an empty directory this run did leave behind is the operator's to remove.
	if out, err := host.run("mkdir " + shellquote.RemoteWord(hostDir)); err != nil {
		t.Fatalf("could not create %s on host %s (%v: %s); this run removes only a directory whose creation it watched succeed, so the path is left untouched", hostDir, host.target, err, strings.TrimSpace(string(out)))
	}
	// The removal is registered now that the mkdir above has answered success: the
	// directory is this run's, and rm -rf may take it with whatever the session
	// leaves inside. The stop's own cleanup below runs first (cleanups are LIFO) and
	// sets sessionLeftBehind when it could not establish that nothing it started is
	// still running here: a directory a running session still references is better
	// left than deleted out from under it.
	var sessionLeftBehind bool
	t.Cleanup(func() {
		if sessionLeftBehind {
			t.Logf("leaving %s in place: this run could not establish that nothing it started is still running there", hostDir)
			return
		}
		if out, err := host.run("rm -rf " + shellquote.RemoteWord(hostDir)); err != nil {
			t.Errorf("remove the test-owned directory %s on host %s: %v (%s)", hostDir, host.target, err, strings.TrimSpace(string(out)))
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

	// This budget covers getting the host ready and nothing that happens after the
	// host answers: the local dial, the evener/host/add that reaches it, and one
	// attach attempt — which awaitHostAttached bounds internally at hostAttachTimeout
	// (four minutes). Six minutes is that four plus room for the dial and the add.
	// Everything after the attach counts from context.Background instead — the spawn,
	// the reads, the resolve and the stop each take their own clock — so a slow attach
	// can spend this budget but never theirs.
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
	if row.Origin != "hub.toml" {
		t.Fatalf("step evener/host/add: added row origin = %q, want %q (every host lives in the machine-managed hub.toml)", row.Origin, "hub.toml")
	}
	attached := awaitHostAttached(ctx, t, client, hostE2EName)
	t.Logf("attached %s: os=%s arch=%s hubVersion=%s", hostE2EName, attached.OS, attached.Arch, attached.HubVersion)

	// The spawn's own lease, counted from context.Background: it must not be shortened
	// by however long the attach took. It has to be generous on its own terms, because
	// the host runs several sequentially bounded shell-outs to resolve the launch (two
	// launch-checks and the daemon spawn): a short one can expire during legitimate
	// work and surface as "the host cannot resolve a model" — a misleading failure
	// that reads like a product bug. The reads that follow take their own clocks
	// (sessionReadTimeout, sessionResolveTimeout) rather than sharing this lease.
	startCtx, cancelStart := context.WithTimeout(context.Background(), sessionSpawnTimeout)
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
	// The cleanup's stop is one attempt on sessionStopTimeout, the same call and
	// clock the STOP assertion uses, and it is skipped once that assertion has
	// settled the session (stopSettled below). A stop that fails is reported with
	// the ref, the host and the reason, and leaves the directory in place. When no
	// ref this check may stop came back, nothing here tries to find the session: no
	// answer the check reads proves nothing started, so the directory stays and the
	// run says the truth plainly. Nothing here reaches for a process table, a kill,
	// a pattern, or a sweep of the fleet view.
	var ref string
	// stopRef is the cleanup's target, and it is NOT the raw response: it is armed
	// only from sessionStopTarget, once the SPAWN assertion has judged the ref parses
	// and belongs to the host that was asked. A `local:` ref or another host's ref
	// leaves it empty, so a response this check must not act on is never shut down.
	var stopRef string
	// stopSettled records that the body's own STOP assertion already stopped this
	// session: the cleanup has no stop left to make, and it does not lean on the
	// remote handler tolerating a repeat for an already-exited session.
	var stopSettled bool
	t.Cleanup(func() {
		if stopRef == "" {
			// The run has already failed at this point (thread/start's error path, or
			// the SPAWN assertion that refuses a ref this check may not stop), so this
			// reports rather than adds a failure: no answer read here can prove nothing
			// started, so the session the host may be running cannot be addressed from
			// here, and the operator is told where it is.
			sessionLeftBehind = true
			t.Logf("a session may be running on host %s under %s that this check cannot stop: thread/start named no ref this check may stop — its response was lost, its answer carried none, or the ref it named was not this host's — so nothing here can address it. If one is running, the controller's own fleet view lists it by its working directory %s, and it can be stopped from there. Nothing was cleaned up: the directory is left in place.", host.target, hostDir, hostDir)
			return
		}
		if stopSettled {
			return
		}
		stopCtx, cancelStop := context.WithTimeout(context.Background(), sessionStopTimeout)
		defer cancelStop()
		if _, err := clientRequest[appwire.EmptyResponse](stopCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: stopRef}); err != nil {
			sessionLeftBehind = true
			t.Errorf("left a session running on host %s: thread/shutdown of %s failed (%v), so the session could not be stopped; nothing was cleaned up — the directory is left in place, because a running session still references it", host.target, stopRef, err)
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

	// SPAWN. The ref says WHICH hub owns the session: a controller-local spawn would
	// answer "local:<id>", and another host's ref names someone else's session —
	// neither is a session this check may stop. sessionStopTarget is that judgement
	// (it parses the ref and checks its source), and the cleanup's target is armed
	// from the same call, so the cleanup can never stop a ref this assertion refuses.
	if ref == "" {
		t.Fatalf("thread/start on host %q returned no evener ref: %+v", hostE2EName, started.Thread)
	}
	stopRef, err = sessionStopTarget(ref)
	if err != nil {
		t.Fatalf("thread/start on host %q: %v — the session must belong to the host it was spawned on, and the cleanup must not stop a ref this assertion refuses", hostE2EName, err)
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
	if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: stopRef}); err != nil {
		t.Fatalf("step thread/shutdown of %s through the controller: %v (the controller must be able to stop a session it did not host, in band)", stopRef, err)
	}
	// The session is settled by the check's own stop, so the cleanup has no stop
	// left to make — see stopSettled.
	stopSettled = true
	t.Logf("the controller stopped %s in band", stopRef)
}

// sessionStopTarget returns the ref the cleanup may stop for a thread/start
// response, or an error naming why the response's ref is not one this check may
// touch. A ref that does not parse names nothing; a ref whose source is not the
// host that was asked — a controller-local `local:` ref, or another host's — names
// a session this check must not stop. The SPAWN assertion judges this rule and arms
// the cleanup's target from it, so the cleanup can never stop a ref that assertion
// would refuse.
func sessionStopTarget(ref string) (string, error) {
	parsed, err := appwire.ParseRef(ref)
	if err != nil {
		return "", fmt.Errorf("ref %q does not parse: %w", ref, err)
	}
	if parsed.SourceID != hostE2EName {
		return "", fmt.Errorf("ref %q names source %q, want %q", ref, parsed.SourceID, hostE2EName)
	}
	return ref, nil
}

// TestSessionModelProvenance pins the provenance judgement the live check makes,
// with no host and no ssh: the seam is the three pure predicates above.
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

// TestSessionStopTarget pins which thread/start refs the cleanup may stop: only a
// ref that parses and names the host that was asked. A controller-local ref,
// another host's ref, a malformed ref and an empty one all leave the cleanup with
// no target, so it can never shut down an unrelated session — and the run that drew
// such a ref has already failed at the SPAWN assertion.
func TestSessionStopTarget(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		want    string
		wantErr bool
	}{
		{"the host's own ref", hostE2EName + ":t1", hostE2EName + ":t1", false},
		{"a controller-local ref", "local:t1", "", true},
		{"another host's ref", "other:t1", "", true},
		{"a malformed ref", "t1", "", true},
		{"an empty ref", "", "", true},
	}
	for _, tc := range tests {
		got, err := sessionStopTarget(tc.ref)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("sessionStopTarget(%q) = (%q, %v), want (%q, err=%v) (%s)", tc.ref, got, err, tc.want, tc.wantErr, tc.name)
		}
	}
}
