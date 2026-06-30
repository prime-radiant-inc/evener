package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/fuzz/schemagen"
	"primeradiant.com/serf/fuzz/typegen"
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
// device/login methods (and the instances controller, which shares this auth
// controller's stateDir + creds store) exercise their real logic without ever
// dialing a provider or writing into the real OAuth state/credentials files. The
// hook is default-off in production (newHubAuthControllerWithStore leaves it nil);
// the test sets it before the hub is built and clears it on cleanup.
//
// It returns the path of a path-traversal canary: a .json file planted ONE LEVEL
// ABOVE the contained OAuth state dir. serf/instance/remove forwards its fuzzed
// name into authopenai.DeleteAuth, which joins it straight into a filesystem path
// (stateDir/auth/<name>.json); a name like "../../canary" therefore resolves to
// this canary. The oracle re-arms it before every input and fails if a handler
// deleted it — that is an arbitrary-file-deletion path escape.
func installSandboxAuthSeam(tb testing.TB) (canaryPath string) {
	tb.Helper()
	base := tb.TempDir()
	stateDir := filepath.Join(base, "state")
	credsPath := filepath.Join(base, "credentials.toml")
	canaryPath = filepath.Join(base, "canary.json")
	if err := os.WriteFile(canaryPath, []byte("canary"), 0o600); err != nil {
		tb.Fatal(err)
	}
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
	tb.Cleanup(func() { hubAuthControllerSetup = nil })
	return canaryPath
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

// buildParamsRegistry reflects every catalog method's Params type into a
// serf-free typegen Registry keyed by "<method>#params". FuzzAppWireDispatch
// uses it to generate schema-shaped Params values (Valid and schema-adjacent)
// for the selected method, so dispatch exercises typed param shapes that the
// fuzzer's raw bytes rarely form cleanly — driving the real handler logic past
// the json.Unmarshal decode boundary. Reflection is sufficient here: there is no
// round-trip fixed-point oracle on this path (that lives in appwire's decode-only
// FuzzWireTypes), only the dispatch post-condition oracles, so the lone custom
// marshaler in the catalog (LaunchConfigLayer, nested in setLayer params) needs
// no hand-authored schema override — its reflected field shape decodes fine.
func buildParamsRegistry() *typegen.Registry {
	reg := typegen.NewRegistry()
	for _, m := range appwire.Methods {
		if t := reflect.TypeOf(m.Params); t != nil {
			reg.RegisterType(m.Name+"#params", t)
		}
	}
	return reg
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
//
// Focus note: the focus seam newHubAppServer is a constructor whose bulk is the
// per-thread RELAY closures (startRelay / startTurn / the idle-exit + broadcast
// goroutine). Those run only across a live, send-capable subscription lifecycle —
// a source that returns "send" as available, a real SubscribeThread channel, and
// the 250ms idle ticker firing — none of which a single-shot Dispatch replay
// reaches. The focus % therefore plateaus at the dispatch-reachable construction
// and method-routing lines; the relay machinery is exercised by the live hub, not
// this target.
func FuzzAppWireDispatch(f *testing.F) {
	stubSelfUpgrade(f)
	canary := installSandboxAuthSeam(f)
	deny := installDenyTransportTB(f)

	s := newSandbox(f)
	router := s.Web.appRPC.Router()
	methods := hubMethodNames()
	paramsReg := buildParamsRegistry()

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
		{appwire.MethodSerfInstanceCreate, `{"name":"new","type":"anthropic"}`},
		{appwire.MethodSerfInstanceEdit, `{"name":"work","apiStyle":"chat_completions"}`},
		{appwire.MethodSerfInstanceSetDefault, `{"name":"key"}`},
		{appwire.MethodSerfInstanceRemove, `{"name":"key"}`},
		// Path-traversal attack shape: serf/instance/remove forwards this name to
		// authopenai.DeleteAuth, which joins it into a filesystem path. "../../canary"
		// resolves to the canary planted above the contained auth state dir; a hub
		// that does not reject the name deletes a file outside its sandbox.
		{appwire.MethodSerfInstanceRemove, `{"name":"../../canary"}`},
		{appwire.MethodInitialize, `{}`},
		{"totally/unknown", `{}`},
	}
	for _, seed := range seeds {
		f.Add(uint8(idx(seed.method)), []byte(seed.params))
	}

	// dispatchAndCheck runs one (method, params) pair through the real
	// Router.Dispatch under the full sandbox oracle gauntlet. It is invoked twice
	// per fuzz input: once with the fuzzer's raw bytes as JSON params, and once
	// with a schema-shaped value built from the same bytes by the wire-type
	// generator.
	dispatchAndCheck := func(t *testing.T, method string, params []byte) {
		raw := pinSandboxCWD(params, s.CWD)
		req := appwire.Request{ID: appwire.NewIntID(1), Method: method, Params: raw}

		// Re-arm the path-traversal canary so every dispatch is judged
		// independently: each starts with the out-of-state file present.
		if err := os.WriteFile(canary, []byte("canary"), 0o600); err != nil {
			t.Fatalf("re-arm canary: %v", err)
		}

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

		// Oracle: no handler may have deleted a file outside the contained auth
		// state dir. A missing canary means a fuzzed name escaped through a
		// filesystem path join (the serf/instance/remove → DeleteAuth surface).
		if _, err := os.Stat(canary); err != nil {
			t.Fatalf("path escape via %s: out-of-state canary was deleted (%v)", method, err)
		}
	}

	f.Fuzz(func(t *testing.T, methodIdx uint8, params []byte) {
		method := methods[int(methodIdx)%len(methods)]

		// Raw-byte path: the fuzzer's own bytes as JSON params — the tokenizer and
		// custom-UnmarshalJSON surface a structured value never reaches.
		dispatchAndCheck(t, method, params)

		// Structured path: feed the same bytes to the wire-type generator to build
		// a schema-shaped Params value for this method, so dispatch exercises typed
		// param shapes — valid and schema-adjacent — that raw bytes rarely form
		// cleanly, reaching handler logic past the json.Unmarshal decode boundary.
		// The low bit of the first byte steers valid vs adjacent, the rest is
		// generator entropy.
		mode := schemagen.Valid
		if len(params) > 0 && params[0]&1 == 1 {
			mode = schemagen.Adjacent
		}
		if val, ok := paramsReg.Value(method+"#params", mode, schemagen.NewByteSource(params)); ok {
			if structured, err := json.Marshal(val); err == nil {
				dispatchAndCheck(t, method, structured)
			}
		}
	})
}
