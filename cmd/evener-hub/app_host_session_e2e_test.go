package hub

import (
	"context"
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

// sessionCheckControllerProvider and sessionCheckControllerModel are the
// provider instance and the model this check's own controller stack serves:
// startHubStack writes a providers.toml with the single instance "fake" whose
// base_url is the in-process fakellm, and callers address its model as
// "fake/"+fakellm.ModelID. They are the values a session whose launch THIS
// controller resolved would name, so the provenance assertion below compares the
// host's session against them: a session that names either one was resolved by
// this side's configuration, which is exactly what the check exists to rule out.
const (
	sessionCheckControllerProvider = "fake"
	sessionCheckControllerModel    = "fake/" + fakellm.ModelID
)

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
//   - PROVENANCE: the session's own record names the host's model provider, not
//     the provider or model this controller's configuration serves.
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
	started, err := clientRequest[appwire.ThreadStartResponse](startCtx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener",
		Source:  hostE2EName,
		CWD:     hostDir,
	})
	if err != nil {
		t.Fatalf("step thread/start with source %q: %v (a host that cannot resolve a model from its own launch configuration refuses here)", hostE2EName, err)
	}
	ref := started.Thread.Evener.Ref

	// The stop on the way out is in band too: the controller stops the session it
	// did not host. Registered before the ref is judged, because a ref this test
	// ends up rejecting still names a session it started, and a start whose
	// response was lost still leaves one running. It is best-effort only because
	// the body's own STOP assertion is the check; a request that fails here is
	// logged, and a daemon the stop cannot reach exits on its own idle timeout.
	t.Cleanup(func() {
		if ref == "" {
			t.Logf("thread/start returned no ref, so the session it started cannot be addressed here; any daemon it spawned idles out on its own")
			return
		}
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelShutdown()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Logf("thread/shutdown of %s did not report success on the way out (%v); the host's daemon idles out on its own when the stop cannot reach it", ref, err)
		}
	})

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
	// the HOST from its own configuration. The session's record is where that
	// shows, because a daemon publishes the model ref it was launched with
	// (serve.go's rendezvous entry) and the hub reads it back as the thread's model
	// provider. A session naming THIS controller's provider or model was resolved
	// here instead — the failure this check exists to catch — and one naming
	// nothing at all leaves the claim unproven, so both fail rather than pass
	// quietly.
	modelProvider := strings.TrimSpace(read.Thread.ModelProvider)
	switch modelProvider {
	case "":
		t.Fatalf("the session on host %q records no model provider, so where its launch was resolved cannot be seen from here; the host was supposed to resolve a model from its own configuration", hostE2EName)
	case sessionCheckControllerProvider, sessionCheckControllerModel:
		t.Fatalf("the session on host %q names model provider %q, which is THIS controller's own configuration; the launch was resolved here rather than by the host", hostE2EName, modelProvider)
	}
	t.Logf("the host resolved the launch from its own configuration: %s runs on model provider %q", ref, modelProvider)

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
