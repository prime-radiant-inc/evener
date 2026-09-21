package hub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// hostE2EName is the registry name the live check adds the environment's host
// under. A fixed name keeps the failure output and the sidecar a test run leaves
// in its throwaway config dir readable.
const hostE2EName = "e2e"

// The bounds on the add-then-attach wait. The attach RPC is synchronous — it
// returns only once sshconn.Manager.Ensure has probed, maybe bootstrapped the
// host's hub, and installed the bridge — so the generous whole-step timeout is
// the real bound and the poll interval only decides how often the row is
// re-read.
const (
	hostAttachTimeout  = 4 * time.Minute
	hostAttachPollWait = 10 * time.Second
)

// TestHostAddAttachForwardedDiscoveryE2E is the live check for component 08
// slice 2's final criterion
// (docs/superpowers/specs/2026-09-20-multi-host-host-edit-slice.md): a host
// added through evener/host/add, attached through evener/host/attach, serves a
// spawn-form discovery call through evener/host/request — the answer comes from
// the attached remote hub, never from the controller's own handlers.
//
// It is the hub-side companion to internal/sshconn's TestLiveSSHAttachE2E
// (design §7's live test, docs/superpowers/specs/2026-09-14-multi-host-evener-design.md):
// that one proves the attach channel itself, this one drives the browser path
// the criterion names, over the hub's own AppWire client.
//
// It is gated by BOTH EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST, so default
// `make test` and `go test ./...` perform no ssh and need no host. The optional
// EVENER_SSH_E2E_USER sets the registry entry's ssh user (omit it when the
// destination's ssh_config already names one), and EVENER_SSH_E2E_EVENER_PATH
// overrides the host's evener path (default ~/.local/bin/evener).
//
// The host must already carry a matching evener build at that path: a test hub
// has no BuildSource, so the version-match ladder refuses to attach a host whose
// launch-check reports a version the controller does not carry, rather than
// deploying one. See docs/developing-evener/testing.md for the cross-compile
// recipe.
func TestHostAddAttachForwardedDiscoveryE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("live SSH host add/attach test: builds the live-stack binaries and dials a remote host")
	}
	if os.Getenv("EVENER_SSH_E2E") != "1" {
		t.Skip("set EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST to run the live host add/attach/forward test")
	}
	dest := os.Getenv("EVENER_SSH_E2E_HOST")
	if dest == "" {
		t.Skip("set EVENER_SSH_E2E_HOST to a disposable ssh destination (an ssh alias or user@host) to run the live host add/attach/forward test; the host must already run a matching evener build")
	}
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)

	evenerPath := os.Getenv("EVENER_SSH_E2E_EVENER_PATH")
	if evenerPath == "" {
		evenerPath = "~/.local/bin/evener"
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

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

	// Before the attach there is no remote channel. The proxy must refuse the
	// discovery call rather than answer it from the controller's own
	// evener/harnesses/list handler, and a locally-served method would answer
	// here — so this refusal is what makes the post-attach success below
	// evidence of a forward rather than of a local read.
	assertHostRequestRefused(ctx, t, client, appwire.MethodEvenerHarnessesList,
		appwire.CodeUnavailable, "is not attached",
		"step evener/host/request before attach: a discovery call for an unattached host must be refused as not attached, not served locally")

	// The same is true of a method the controller's own hub DOES serve but the
	// proxy may never forward: a fallback to local execution would answer this.
	assertHostRequestRefused(ctx, t, client, appwire.MethodEvenerHostAttach,
		appwire.CodeInvalidParams, "is not a permitted remote admin method",
		"step evener/host/request: a controller-local method must be refused by the allow-list, not served locally")

	attached := awaitHostAttached(ctx, t, client, hostE2EName)
	t.Logf("attached %s: os=%s arch=%s hubVersion=%s serverVersion=%s", hostE2EName, attached.OS, attached.Arch, attached.HubVersion, attached.ServerVersion)

	// The criterion: one spawn-form discovery call, through the forward, coming
	// back as the remote hub's own result.
	raw, err := forwardHostMethod(ctx, client, hostE2EName, appwire.MethodEvenerHarnessesList, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("step evener/host/request (forwarded %s) after attach: %v", appwire.MethodEvenerHarnessesList, err)
	}
	var harnesses appwire.HarnessListResponse
	if err := json.Unmarshal(raw, &harnesses); err != nil {
		t.Fatalf("step evener/host/request (forwarded %s) after attach: result %s does not decode as the forwarded method's own result: %v", appwire.MethodEvenerHarnessesList, raw, err)
	}
	var found bool
	for _, h := range harnesses.Data {
		if h.ID == "evener" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("forwarded %s returned %#v, want the remote hub's harness list (carrying the evener harness)", appwire.MethodEvenerHarnessesList, harnesses.Data)
	}
	t.Logf("forwarded %s through host %q: %+v", appwire.MethodEvenerHarnessesList, hostE2EName, harnesses.Data)
}

// assertHostRequestRefused fails unless forwarding method to host is refused
// with a WireError carrying code and a message containing wantMessage. It is the
// negative control for the criterion: an unforwarded call must come back a typed
// refusal, not a locally-served result.
func assertHostRequestRefused(ctx context.Context, t *testing.T, client *appwire.Client, method string, code int, wantMessage, what string) {
	t.Helper()
	raw, err := forwardHostMethod(ctx, client, hostE2EName, method, json.RawMessage(`{}`))
	if err == nil {
		t.Fatalf("%s: forwarding %s returned %s, want a refusal", what, method, raw)
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != code || !strings.Contains(wire.Message, wantMessage) {
		t.Fatalf("%s: forwarding %s: error = %v (%T), want appwire.WireError code %d whose message contains %q", what, method, err, err, code, wantMessage)
	}
}

// forwardHostMethod issues one evener/host/request forwarding method to host and
// returns the remote hub's own result verbatim. A non-nil error is the proxy's
// or the remote's refusal, unchanged.
func forwardHostMethod(ctx context.Context, client *appwire.Client, host, method string, params json.RawMessage) (json.RawMessage, error) {
	var raw json.RawMessage
	err := client.Request(ctx, appwire.MethodEvenerHostRequest, appwire.HostRequestParams{
		Host:   host,
		Method: method,
		Params: params,
	}, &raw)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// awaitHostAttached drives evener/host/attach until the host's row reports
// attached, and returns that row. The attach RPC is idempotent and synchronous,
// so it is retried while the row is still offline: a transient ssh start failure
// recovers on the next attempt instead of failing the run, and the whole-step
// timeout stays the bound. A failure names the step and the row's own
// lastAttachError, which is what the settings pane would render.
func awaitHostAttached(ctx context.Context, t *testing.T, client *appwire.Client, name string) appwire.HostRow {
	t.Helper()
	deadline := time.Now().Add(hostAttachTimeout)
	var lastAttachErr error
	for {
		if resp, err := clientRequest[appwire.HostAttachResponse](ctx, client, appwire.MethodEvenerHostAttach, appwire.HostAttachParams{Host: name}); err != nil {
			lastAttachErr = err
		} else if resp.Attached {
			lastAttachErr = nil
		}
		row, rowErr := clientRequest[appwire.HostStatusResponse](ctx, client, appwire.MethodEvenerHostStatus, appwire.HostStatusParams{Name: name})
		if rowErr == nil && row.Host.Attached {
			return row.Host
		}
		if time.Now().After(deadline) {
			if rowErr != nil {
				t.Fatalf("step evener/host/attach: host %q did not report attached within %s and its row could not be read: %v (last attach error: %v)", name, hostAttachTimeout, rowErr, lastAttachErr)
			}
			t.Fatalf("step evener/host/attach: host %q did not report attached within %s: lastAttachError=%q midAttach=%v attached=%v (last attach call error: %v)",
				name, hostAttachTimeout, row.Host.LastAttachErr, row.Host.MidAttach, row.Host.Attached, lastAttachErr)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("step evener/host/attach: context ended before host %q reported attached: %v (last attach error: %v)", name, ctx.Err(), lastAttachErr)
		case <-time.After(hostAttachPollWait):
		}
	}
}
