package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/internal/selfupdate"
)

// errSandboxAuthOffline is returned by the sandbox's stubbed OAuth network
// seams. A device/login handler that reaches a real provider call gets this
// instead, so the fuzz can never dial an issuer.
var errSandboxAuthOffline = errors.New("sandbox: auth network disabled")

// installDenyTransportTB swaps http.DefaultTransport for a recording deny-dialer
// for the lifetime of tb and restores it on cleanup. It is the network tripwire
// for the *testing.F fuzz targets (the self-test uses installDenyTransport with
// a *testing.T). The hub's source registry and the auth controller both dial
// through http.DefaultClient (nil Transport → http.DefaultTransport), so any
// stray dial lands here and is both blocked and counted.
func installDenyTransportTB(tb testing.TB) *denyTransport {
	tb.Helper()
	deny := &denyTransport{}
	orig := http.DefaultTransport
	http.DefaultTransport = deny
	tb.Cleanup(func() { http.DefaultTransport = orig })
	return deny
}

// installSandboxAuthSeam points a freshly built hub auth controller at contained
// temp dirs and replaces its four OAuth network seams with offline stubs, so the
// device/login methods exercise their param/flow logic without ever dialing a
// provider or writing into the real OAuth state/credentials files. The hook is
// default-off in production (newHubAuthControllerWithStore leaves it nil); the
// fuzz sets it before the hub is built and clears it on cleanup.
func installSandboxAuthSeam(f *testing.F) {
	f.Helper()
	stateDir := f.TempDir()
	credsPath := filepath.Join(f.TempDir(), "credentials.toml")
	hubAuthControllerSetup = func(c *hubAuthController) {
		c.stateDir = stateDir
		if store, err := credentials.LoadStore(credsPath); err == nil {
			c.creds = store
		}
		c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
			return authopenai.DeviceCode{UserCode: "FUZZ", VerificationURL: "http://sandbox.invalid", Interval: time.Second}, nil
		}
		c.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
			return authopenai.DeviceCodeSuccess{}, true, nil // always pending: never exchanges, never writes
		}
		c.exchangeDevice = func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error) {
			return authopenai.TokenSet{}, errSandboxAuthOffline
		}
		c.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
			return authopenai.TokenSet{}, errSandboxAuthOffline
		}
	}
	f.Cleanup(func() { hubAuthControllerSetup = nil })
}

// stubSelfUpgrade replaces the serf/upgrade seam with an offline stub for the
// lifetime of f and restores the real one on cleanup. It returns a benign
// "nothing installed" result (no network, no error) so the upgrade handler runs
// its real success-mapping path rather than manufacturing an upstream error.
func stubSelfUpgrade(f *testing.F) {
	f.Helper()
	runHubSelfUpgrade = func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{}, nil
	}
	f.Cleanup(func() { runHubSelfUpgrade = selfupdate.Upgrade })
}

// hubMethodNames returns the wire names of every catalog method, in order. The
// B1 fuzz indexes into this so every one of the 46 appwire.Methods is reachable:
// the routed hub methods run their real app_*.go handler; connection-level
// (initialize/ping) and unimplemented methods resolve to a structured
// MethodNotFound through the same Dispatch path.
func hubMethodNames() []string {
	names := make([]string, len(appwire.Methods))
	for i, m := range appwire.Methods {
		names[i] = m.Name
	}
	return names
}

// pinSandboxCWD rewrites the "cwd" field of a fuzzed JSON object to the sandbox
// working dir. The launch handlers (serf/launch/setLayer with layer "project",
// and thread spawn) derive a filesystem write path from a caller-supplied,
// already-existing cwd; an arbitrary fuzzed cwd pointing at a real external dir
// (e.g. "/tmp") would let serf/launch/setLayer write <cwd>/.serf/launch.local.toml
// OUTSIDE the sandbox. Pinning cwd into the sandbox keeps every cwd-derived FS
// write contained while still fuzzing every other field. Non-object params and
// params without a cwd are passed through untouched.
func pinSandboxCWD(raw []byte, cwd string) []byte {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return raw
	}
	if _, ok := obj["cwd"]; !ok {
		return raw
	}
	enc, err := json.Marshal(cwd)
	if err != nil {
		return raw
	}
	obj["cwd"] = enc
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

// FuzzAppWireDispatch drives the REAL hub app handlers (cmd/serf-hub/app_*.go)
// over every appwire catalog method through the real Router.Dispatch, against
// the B0 sandbox. Unlike the Phase-2 appserver fuzz (which routes stub handlers
// to MethodNotFound), this exercises the actual hub logic: thread/turn
// lifecycle, auth, launch config, model list, instances, upgrade.
//
// Containment: the sandbox's recording spawner stands in for real fork/exec; the
// auth seam redirects OAuth state/creds to temp dirs and stubs the four OAuth
// network calls; the self-upgrade seam is stubbed; cwd is pinned into the
// sandbox so no cwd-derived launch write escapes; and a deny-transport blocks
// and counts any stray dial.
//
// Oracles, per fuzzed (methodIndex, paramsBytes):
//   - never panic (an unrecovered panic in any handler crashes the worker → a
//     reported crasher);
//   - never wedge (Dispatch must return within a bounded window);
//   - structured error: a bad param yields a wire-serializable error, never an
//     un-encodable value;
//   - a success result must JSON-serialize and must never carry the out-of-root
//     secret (path-escape);
//   - zero network attempts (the deny-transport tripwire).
func FuzzAppWireDispatch(f *testing.F) {
	stubSelfUpgrade(f)
	installSandboxAuthSeam(f)
	deny := installDenyTransportTB(f)

	s := newSandbox(f)
	router := s.Web.appRPC.Router()
	methods := hubMethodNames()

	ref := appwire.Ref{SourceID: "local", ThreadID: sandboxSessionID}.String()
	idx := func(name string) int {
		for i, m := range methods {
			if m == name {
				return i
			}
		}
		return 0
	}
	seeds := []struct {
		method string
		params string
	}{
		{appwire.MethodThreadList, `{}`},
		{appwire.MethodThreadRead, `{"ref":"` + ref + `"}`},
		{appwire.MethodThreadTurnsList, `{"ref":"` + ref + `"}`},
		{appwire.MethodThreadStart, `{"harness":"serf","cwd":"x","model":"openai/gpt-5.5"}`},
		{appwire.MethodThreadResume, `{"session":"` + sandboxSessionID + `"}`},
		{appwire.MethodThreadFork, `{"ref":"` + ref + `","sourceTurnId":"turn_1","editedInput":"hi"}`},
		{appwire.MethodTurnStart, `{"ref":"` + ref + `","input":[]}`},
		{appwire.MethodTurnSteer, `{"ref":"` + ref + `","text":"go"}`},
		{appwire.MethodThreadClear, `{"ref":"` + ref + `"}`},
		{appwire.MethodThreadModelSet, `{"ref":"` + ref + `","model":"gpt-5.5"}`},
		{appwire.MethodThreadCompactStart, `{"ref":"` + ref + `"}`},
		{appwire.MethodModelList, `{}`},
		{appwire.MethodSerfAuthStatus, `{"provider":"openai"}`},
		{appwire.MethodSerfAuthLoginStart, `{"provider":"openai"}`},
		{appwire.MethodSerfAuthLoginComplete, `{"provider":"openai","flowId":"x","redirectUrl":"http://localhost/?code=a&state=b"}`},
		{appwire.MethodSerfAuthDeviceStart, `{"provider":"openai"}`},
		{appwire.MethodSerfAuthDevicePoll, `{"provider":"openai","flowId":"x"}`},
		{appwire.MethodSerfAuthApiKeySet, `{"provider":"anthropic","value":"sk-test"}`},
		{appwire.MethodSerfAuthLogout, `{"provider":"openai"}`},
		{appwire.MethodSerfLaunchResolve, `{"cwd":"x"}`},
		{appwire.MethodSerfLaunchSetLayer, `{"cwd":"x","layer":"project","config":{}}`},
		{appwire.MethodSerfLaunchGetLayer, `{"cwd":"x","layer":"global"}`},
		{appwire.MethodSerfLaunchTrustRepo, `{"cwd":"x","hash":"deadbeef"}`},
		{appwire.MethodSerfDirsComplete, `{"prefix":"/tmp"}`},
		{appwire.MethodSerfPathValidate, `{"path":"/tmp","kind":"dir"}`},
		{appwire.MethodSerfHarnessesList, `{}`},
		{appwire.MethodSerfUpgrade, `{"requested":"latest"}`},
		{appwire.MethodSerfTasksList, `{"ref":"` + ref + `"}`},
		{appwire.MethodSerfThreadTranscriptsList, `{"ref":"` + ref + `"}`},
		{appwire.MethodSerfSubagentPreview, `{"ref":"` + ref + `"}`},
		{appwire.MethodSerfInstanceList, `{}`},
		{appwire.MethodInitialize, `{}`},
		{"totally/unknown", `{}`},
	}
	for _, seed := range seeds {
		f.Add(uint8(idx(seed.method)), []byte(seed.params))
	}

	f.Fuzz(func(t *testing.T, methodIdx uint8, params []byte) {
		method := methods[int(methodIdx)%len(methods)]
		raw := pinSandboxCWD(params, s.CWD)
		req := appwire.Request{ID: appwire.NewIntID(1), Method: method, Params: raw}

		// Oracle: never wedge. Run Dispatch under a bounded ctx, and fail if it
		// does not return promptly even so (a handler ignoring ctx would hang the
		// worker otherwise). A panic in the handler propagates and crashes the
		// worker — the no-panic floor, reported by the fuzzer as a crasher.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		type result struct {
			out any
			err error
		}
		done := make(chan result, 1)
		go func() {
			out, err := router.Dispatch(ctx, req)
			done <- result{out, err}
		}()
		var res result
		select {
		case res = <-done:
		case <-time.After(15 * time.Second):
			t.Fatalf("wedge: %s did not return", method)
		}

		// Oracle: structured error — every error must reduce to a WireError that
		// serializes onto the wire, never an un-encodable value.
		if res.err != nil {
			wire := appserver.WireError(res.err)
			if _, merr := json.Marshal(wire); merr != nil {
				t.Fatalf("error from %s not wire-serializable: %v", method, merr)
			}
		} else {
			// Oracle: a success result must serialize and must never carry the
			// out-of-root secret.
			body, merr := json.Marshal(res.out)
			if merr != nil {
				t.Fatalf("result from %s not serializable: %v", method, merr)
			}
			if bytes.Contains(body, s.Secret) {
				t.Fatalf("path escape: %s result carried the out-of-root secret", method)
			}
		}

		// Oracle: no handler may have dialed the network.
		if att := deny.Attempts(); len(att) != 0 {
			t.Fatalf("network attempt during %s: %v", method, att)
		}
	})
}
