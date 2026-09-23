package hub

import (
	"context"
	"os"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestHostSpawnSessionE2E is the live check for the session half of the
// multi-host contract: a session the CONTROLLER asks for, from a host attached
// through evener/host/attach, is spawned and served by that host's own hub.
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
// Gated by EVENER_SSH_E2E=1, EVENER_SSH_E2E_HOST, EVENER_SSH_E2E_SESSION=1, and
// EVENER_SSH_E2E_ROOT. The session gate is separate from the read-mostly
// add/attach check beside this file because this test WRITES to the host: it
// starts a session, and the hub spawns a daemon there that outlives the test hub.
// EVENER_SSH_E2E_ROOT names a working directory that exists on the host (the
// spawn's cwd) and EVENER_SSH_E2E_EVENER_PATH overrides the host's evener path
// (default ~/.local/bin/evener).
func TestHostSpawnSessionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("live remote-session test: builds the live-stack binaries, dials a remote host, and starts a session on it")
	}
	// The write gate is checked first, as the deploy live check does, so an
	// un-opted-in run's skip line states the contract plainly: this check WRITES to
	// the host, and it is a separate opt-in from the read-mostly sibling's.
	if os.Getenv("EVENER_SSH_E2E_SESSION") != "1" {
		t.Skip("set EVENER_SSH_E2E_SESSION=1 (with EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST) to run the live remote-session test; this check WRITES to the host — it starts a session there, and the hub spawns a daemon that outlives this test's hub")
	}
	if os.Getenv("EVENER_SSH_E2E") != "1" {
		t.Skip("set EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST to run the live remote-session test")
	}
	dest := os.Getenv("EVENER_SSH_E2E_HOST")
	if dest == "" {
		t.Skip("set EVENER_SSH_E2E_HOST to a disposable ssh destination to run the live remote-session test")
	}
	remoteRoot := os.Getenv("EVENER_SSH_E2E_ROOT")
	if remoteRoot == "" {
		t.Skip("set EVENER_SSH_E2E_ROOT to a directory that exists on the host to run the live remote-session test (the spawned session's working directory)")
	}
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)

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
	// HOST resolves. A spawn and a fleet read are fast when they work at all, so
	// they carry their own lease rather than the attach budget above.
	sessionCtx, cancelSession := context.WithTimeout(ctx, time.Minute)
	defer cancelSession()
	// No Input is sent, and that is the boundary this check keeps: an input item
	// starts a real turn against the HOST's own provider and credentials, which is
	// a live model call the test would neither assert nor be entitled to spend. The
	// spawn still makes the host resolve the launch, which is the claim here.
	started, err := clientRequest[appwire.ThreadStartResponse](sessionCtx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener",
		Source:  hostE2EName,
		CWD:     remoteRoot,
	})
	if err != nil {
		t.Fatalf("step thread/start with source %q: %v (a host that cannot resolve a model from its own launch configuration refuses here)", hostE2EName, err)
	}
	ref := started.Thread.Evener.Ref
	if ref == "" {
		t.Fatalf("thread/start on host %q returned no evener ref: %+v", hostE2EName, started.Thread)
	}
	// The ref says WHICH hub owns the session: a controller-local spawn would
	// answer "local:<id>". ParseRef validates the grammar too, so a malformed ref
	// fails here rather than reading as a foreign source.
	parsed, err := appwire.ParseRef(ref)
	if err != nil {
		t.Fatalf("thread/start on host %q returned an unparseable ref %q: %v", hostE2EName, ref, err)
	}
	if parsed.SourceID != hostE2EName {
		t.Fatalf("thread/start ref %q has source %q, want %q: the session must belong to the host it was spawned on", ref, parsed.SourceID, hostE2EName)
	}
	t.Logf("spawned %s on host %q", ref, hostE2EName)

	// Cleanup is best-effort, and deliberately says so: the controller cannot yet
	// SHUT DOWN a session it did not host. Remote thread capabilities are masked to
	// the actions this source can carry (appsource.maskRemoteThreadCapabilities,
	// whose comment names the host capability probe as what turns them on), so the
	// daemon started here stays on the host until its own idle timeout. The gate in
	// this test's file comment asks for a disposable host for exactly that reason.
	// Registered before the checks below, so it also runs when one of them fails.
	t.Cleanup(func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelShutdown()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Logf("thread/shutdown of %s is refused (%v); the controller cannot stop a remote session yet, so the host's daemon idles out on its own", ref, err)
		}
	})

	// The session must also be visible from the controller, which is the half a
	// user sees: the fleet fan-out lists the host's sessions beside the local
	// ones. A spawn that worked but never appeared here would leave the session
	// unreachable from the UI.
	listed, err := clientRequest[appwire.ThreadListResponse](sessionCtx, client, appwire.MethodThreadList, appwire.ThreadListParams{})
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
	read, err := clientRequest[appwire.ThreadReadResponse](sessionCtx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("step thread/read of %s: %v (the fleet list alone cannot prove the host's daemon is answering)", ref, err)
	}
	if read.Thread.Evener.Ref != ref {
		t.Fatalf("thread/read of %s returned ref %q, want that same session", ref, read.Thread.Evener.Ref)
	}
	t.Logf("the host's daemon answered a read of %s", ref)
}
