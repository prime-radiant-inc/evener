package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// capabilityReply scripts the five probe methods with the given launch model
// and plugin name so a test can tell two clients apart.
func capabilityReply(model, plugin string) func(method string, params json.RawMessage) scriptedReply {
	return func(method string, _ json.RawMessage) scriptedReply {
		switch method {
		case appwire.MethodEvenerLaunchGetLayer:
			return scriptedReply{result: appwire.LaunchConfigLayer{Model: model}}
		case appwire.MethodModelList:
			return scriptedReply{result: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "p", Model: model}}}}
		case appwire.MethodEvenerPluginList:
			return scriptedReply{result: appwire.PluginListResponse{Plugins: []appwire.PluginEntry{{Plugin: plugin}}}}
		case appwire.MethodEvenerAuthList:
			return scriptedReply{result: appwire.AuthListResponse{Providers: []appwire.AuthStatusResponse{{Provider: "auth"}}}}
		case appwire.MethodEvenerInstanceList:
			return scriptedReply{result: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{{Name: "inst"}}}}
		default:
			return scriptedReply{result: appwire.EmptyResponse{}}
		}
	}
}

// assertCallParams fails when the most recent call of method marshaled anything
// other than want.
func assertCallParams(t *testing.T, calls []remoteCall, method, want string) {
	t.Helper()
	if got := string(lastMethodCall(t, calls, method)); got != want {
		t.Fatalf("%s params = %s, want %s", method, got, want)
	}
}

func TestRemoteHubSourceHostCapabilities(t *testing.T) {
	client, calls := newScriptedClient(t, capabilityReply("gpt-x", "pl"))
	source := NewRemoteHubSource("host", []string{"/root/a", "/root/b"}, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	caps, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("HostCapabilities: %v", err)
	}

	// "/" is the placeholder the web and mobile clients send for the global
	// layer: GetLayer canonicalizes CWD before it dispatches on the layer, so an
	// empty CWD is rejected with InvalidParams before the global branch runs.
	assertCallParams(t, calls(), appwire.MethodEvenerLaunchGetLayer, `{"cwd":"/","layer":"global"}`)
	assertCallParams(t, calls(), appwire.MethodModelList, `{}`)
	assertCallParams(t, calls(), appwire.MethodEvenerPluginList, `{}`)
	assertCallParams(t, calls(), appwire.MethodEvenerAuthList, `{}`)
	assertCallParams(t, calls(), appwire.MethodEvenerInstanceList, `{}`)

	if caps.LaunchGlobal.Model != "gpt-x" {
		t.Fatalf("LaunchGlobal.Model = %q, want gpt-x", caps.LaunchGlobal.Model)
	}
	if len(caps.Models.Data) != 1 || caps.Models.Data[0].Model != "gpt-x" {
		t.Fatalf("Models = %+v, want one gpt-x model", caps.Models)
	}
	if len(caps.Plugins.Plugins) != 1 || caps.Plugins.Plugins[0].Plugin != "pl" {
		t.Fatalf("Plugins = %+v, want one pl plugin", caps.Plugins)
	}
	if len(caps.Auth.Providers) != 1 || caps.Auth.Providers[0].Provider != "auth" {
		t.Fatalf("Auth = %+v, want one auth provider", caps.Auth)
	}
	if len(caps.Instances.Instances) != 1 || caps.Instances.Instances[0].Name != "inst" {
		t.Fatalf("Instances = %+v, want one instance", caps.Instances)
	}
	if caps.HubSourceID != remoteHubNamespace {
		t.Fatalf("HubSourceID = %q, want %q", caps.HubSourceID, remoteHubNamespace)
	}
	if !reflect.DeepEqual(caps.Roots, []string{"/root/a", "/root/b"}) {
		t.Fatalf("Roots = %v, want the constructor roots", caps.Roots)
	}
}

func TestRemoteHubSourceHostCapabilitiesFacts(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", capabilityReply("gpt-x", "pl"))
	want := HostFacts{
		ProtocolVersion: appwire.ProtocolVersion,
		HubVersion:      "1.2.3",
		OS:              "linux",
		Arch:            "amd64",
		Features:        appwire.FeatureSet{ThreadList: true, TurnSteer: true},
	}
	var gotHost string
	source.SetHostFacts(func(_ context.Context, host string, _ *appwire.Client) (HostFacts, error) {
		gotHost = host
		return want, nil
	})

	caps, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("HostCapabilities: %v", err)
	}
	if gotHost != "host" {
		t.Fatalf("facts host = %q, want host", gotHost)
	}
	if caps.ProtocolVersion != want.ProtocolVersion || caps.HubVersion != want.HubVersion {
		t.Fatalf("version facts = %q/%q, want %q/%q", caps.ProtocolVersion, caps.HubVersion, want.ProtocolVersion, want.HubVersion)
	}
	if caps.OS != want.OS || caps.Arch != want.Arch {
		t.Fatalf("os/arch = %q/%q, want %q/%q", caps.OS, caps.Arch, want.OS, want.Arch)
	}
	if !reflect.DeepEqual(caps.Features, want.Features) {
		t.Fatalf("Features = %+v, want %+v", caps.Features, want.Features)
	}
}

func TestRemoteHubSourceHostCapabilitiesWithoutFacts(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", capabilityReply("gpt-x", "pl"))

	caps, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("HostCapabilities: %v", err)
	}
	if caps.ProtocolVersion != "" || caps.HubVersion != "" || caps.OS != "" || caps.Arch != "" {
		t.Fatalf("preflight fields = %q/%q/%q/%q, want all empty", caps.ProtocolVersion, caps.HubVersion, caps.OS, caps.Arch)
	}
	if !reflect.DeepEqual(caps.Features, appwire.FeatureSet{}) {
		t.Fatalf("Features = %+v, want zero", caps.Features)
	}
}

func TestRemoteHubSourceHostCapabilitiesCaches(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", capabilityReply("gpt-x", "pl"))

	first, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("first HostCapabilities: %v", err)
	}
	before := len(calls())

	second, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("second HostCapabilities: %v", err)
	}
	if got := len(calls()); got != before {
		t.Fatalf("wire requests after cached probe = %d, want %d", got, before)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cached capabilities = %+v, want %+v", second, first)
	}
}

func TestRemoteHubSourceHostCapabilitiesReprobesOnClientChange(t *testing.T) {
	firstClient, firstCalls := newScriptedClient(t, capabilityReply("gpt-x", "pl"))
	secondClient, secondCalls := newScriptedClient(t, capabilityReply("gpt-y", "pl2"))

	var mu sync.Mutex
	served := 0
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		served++
		if served == 1 {
			return firstClient, nil
		}
		return secondClient, nil
	})

	first, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("first HostCapabilities: %v", err)
	}
	if first.LaunchGlobal.Model != "gpt-x" {
		t.Fatalf("first LaunchGlobal.Model = %q, want gpt-x", first.LaunchGlobal.Model)
	}
	if got := wireCalls(firstCalls()); len(got) != 5 {
		t.Fatalf("first client wire calls = %v, want the five probe methods", got)
	}

	second, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("second HostCapabilities: %v", err)
	}
	if second.LaunchGlobal.Model != "gpt-y" {
		t.Fatalf("second LaunchGlobal.Model = %q, want gpt-y", second.LaunchGlobal.Model)
	}
	if got := wireCalls(secondCalls()); len(got) != 5 {
		t.Fatalf("second client wire calls = %v, want a fresh five-method probe", got)
	}
}

// wireCalls drops the initialize handshake so a test can count probe methods.
func wireCalls(calls []remoteCall) []remoteCall {
	out := make([]remoteCall, 0, len(calls))
	for _, call := range calls {
		if call.method != appwire.MethodInitialize {
			out = append(out, call)
		}
	}
	return out
}

func TestRemoteHubSourceHostCapabilitiesMapsWireError(t *testing.T) {
	semantic := appwire.InvalidParams("nope")
	source, _ := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodEvenerLaunchGetLayer {
			return scriptedReply{wireErr: &semantic}
		}
		return scriptedReply{result: appwire.EmptyResponse{}}
	})

	_, err := source.HostCapabilities(t.Context())
	if err == nil {
		t.Fatal("HostCapabilities succeeded despite a wire error")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("error = %T %v, want InvalidParams WireError", err, err)
	}
}

func TestRemoteHubSourceHostCapabilitiesClosedTransport(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{closeConn: true}
	})

	_, err := source.HostCapabilities(t.Context())
	if err == nil {
		t.Fatal("HostCapabilities succeeded after the remote closed the pipe")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeUnavailable)
	}
	if info := wireErrorInfo(wire); info != string(appwire.ErrorSessionUnavailable) {
		t.Fatalf("evenerErrorInfo = %q, want %q", info, appwire.ErrorSessionUnavailable)
	}
}

// TestRemoteHubSourceHostCapabilitiesMapsFactsError pins that a transport
// failure from the preflight facts seam goes through mapCallError like every
// wire call does, so it surfaces as SessionUnavailable with the host named
// rather than as a raw error the auto-resume gate cannot attribute.
func TestRemoteHubSourceHostCapabilitiesMapsFactsError(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", capabilityReply("gpt-x", "pl"))
	source.SetHostFacts(func(context.Context, string, *appwire.Client) (HostFacts, error) {
		return HostFacts{}, io.EOF
	})

	_, err := source.HostCapabilities(t.Context())
	if err == nil {
		t.Fatal("HostCapabilities succeeded despite a facts transport failure")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeUnavailable)
	}
	if info := wireErrorInfo(wire); info != string(appwire.ErrorSessionUnavailable) {
		t.Fatalf("evenerErrorInfo = %q, want %q", info, appwire.ErrorSessionUnavailable)
	}
}

// TestRemoteHubSourceHostCapabilitiesPreservesFactsWireError pins that a
// semantic wire error from the facts seam keeps its code, matching how the
// AppWire probe errors are mapped.
func TestRemoteHubSourceHostCapabilitiesPreservesFactsWireError(t *testing.T) {
	semantic := appwire.InvalidParams("bad facts")
	source, _ := newScriptedRemote(t, "host", capabilityReply("gpt-x", "pl"))
	source.SetHostFacts(func(context.Context, string, *appwire.Client) (HostFacts, error) {
		return HostFacts{}, semantic
	})

	_, err := source.HostCapabilities(t.Context())
	if err == nil {
		t.Fatal("HostCapabilities succeeded despite a facts wire error")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("error = %T %v, want InvalidParams WireError", err, err)
	}
}

// TestRemoteHubSourceSetHostFactsSynchronizesWithProbe pins that SetHostFacts
// takes the same lock HostCapabilities reads the facts seam under. Without it
// the install races every in-flight probe's read of s.facts.
func TestRemoteHubSourceSetHostFactsSynchronizesWithProbe(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", capabilityReply("gpt-x", "pl"))

	source.probeMu.Lock()
	installed := make(chan struct{})
	go func() {
		source.SetHostFacts(func(context.Context, string, *appwire.Client) (HostFacts, error) { return HostFacts{}, nil })
		close(installed)
	}()

	select {
	case <-installed:
		source.probeMu.Unlock()
		t.Fatal("SetHostFacts completed while probeMu was held; the write is unsynchronized")
	case <-time.After(50 * time.Millisecond):
	}
	source.probeMu.Unlock()

	select {
	case <-installed:
	case <-time.After(time.Second):
		t.Fatal("SetHostFacts did not complete after probeMu was released")
	}
}

// TestRemoteHubSourceHostCapabilitiesDoesNotHoldLockAcrossWire pins that
// probeMu guards only the cache and the facts seam, never the wire calls. If a
// probe held it across the five reads, another caller could not even reach the
// cache check — nor install facts — until an unresponsive hub answered.
func TestRemoteHubSourceHostCapabilitiesDoesNotHoldLockAcrossWire(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	reply := capabilityReply("gpt-x", "pl")
	source, _ := newScriptedRemote(t, "host", func(method string, params json.RawMessage) scriptedReply {
		if method == appwire.MethodEvenerLaunchGetLayer {
			close(entered)
			<-release
		}
		return reply(method, params)
	})

	probed := make(chan error, 1)
	go func() {
		_, err := source.HostCapabilities(context.Background())
		probed <- err
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe never reached the launch read")
	}

	// The launch read is parked in the wire call; probeMu must already be free.
	locked := make(chan struct{})
	go func() {
		source.probeMu.Lock()
		close(locked)
		source.probeMu.Unlock()
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("probeMu was held across the wire call")
	}

	close(release)
	select {
	case err := <-probed:
		if err != nil {
			t.Fatalf("HostCapabilities: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HostCapabilities did not return after the wire call was released")
	}
}

// TestRemoteHubSourceHostCapabilitiesFactsUseResolvedClient pins the seam that
// keeps the facts in one connection generation: the facts function receives the
// exact client the five wire reads ran on, so a component-04 reconnect that
// swaps the client between the reads and the facts cannot mix generations into
// the snapshot (the production implementation refuses on a mismatch).
func TestRemoteHubSourceHostCapabilitiesFactsUseResolvedClient(t *testing.T) {
	client, _ := newScriptedClient(t, capabilityReply("gpt-x", "pl"))
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	var got *appwire.Client
	source.SetHostFacts(func(_ context.Context, _ string, c *appwire.Client) (HostFacts, error) {
		got = c
		return HostFacts{}, nil
	})

	if _, err := source.HostCapabilities(t.Context()); err != nil {
		t.Fatalf("HostCapabilities: %v", err)
	}
	if got != client {
		t.Fatalf("facts client = %p, want the client the wire reads ran on (%p)", got, client)
	}
}

// TestRemoteHubSourceHostCapabilitiesToleratesMissingInstanceSurface pins that a
// hub with no user layer — which registers no evener/instance/list handler and
// answers CodeMethodNotFound — still yields a capability snapshot: the probe
// leaves Instances empty and keeps the surfaces it did read, instead of failing
// permanently for a healthy host.
func TestRemoteHubSourceHostCapabilitiesToleratesMissingInstanceSurface(t *testing.T) {
	notFound := appwire.MethodNotFound(appwire.MethodEvenerInstanceList)
	reply := capabilityReply("gpt-x", "pl")
	source, _ := newScriptedRemote(t, "host", func(method string, params json.RawMessage) scriptedReply {
		if method == appwire.MethodEvenerInstanceList {
			return scriptedReply{wireErr: &notFound}
		}
		return reply(method, params)
	})

	caps, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("HostCapabilities: %v", err)
	}
	if len(caps.Instances.Instances) != 0 || len(caps.Instances.AvailableProviders) != 0 {
		t.Fatalf("Instances = %+v, want empty", caps.Instances)
	}
	if caps.LaunchGlobal.Model != "gpt-x" {
		t.Fatalf("LaunchGlobal.Model = %q, want gpt-x: the probe must continue past the missing instance surface", caps.LaunchGlobal.Model)
	}
	if caps.HubSourceID != remoteHubNamespace {
		t.Fatalf("HubSourceID = %q, want %q", caps.HubSourceID, remoteHubNamespace)
	}
}

// TestRemoteHubSourceHostCapabilitiesInstanceErrorStillAborts pins that only
// CodeMethodNotFound is tolerated: any other instance-surface failure is a real
// probe failure and must not be swallowed.
func TestRemoteHubSourceHostCapabilitiesInstanceErrorStillAborts(t *testing.T) {
	semantic := appwire.InvalidParams("instance surface failed")
	reply := capabilityReply("gpt-x", "pl")
	source, _ := newScriptedRemote(t, "host", func(method string, params json.RawMessage) scriptedReply {
		if method == appwire.MethodEvenerInstanceList {
			return scriptedReply{wireErr: &semantic}
		}
		return reply(method, params)
	})

	_, err := source.HostCapabilities(t.Context())
	if err == nil {
		t.Fatal("HostCapabilities succeeded despite a non-MethodNotFound instance error")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("error = %T %v, want InvalidParams WireError", err, err)
	}
}

// richCapabilityReply scripts the five probe methods with a value in every
// mutable field the capability snapshot carries, so an isolation case can
// overwrite one through a returned snapshot and tell a deep copy from a shared
// slice, map, or pointer.
func richCapabilityReply() func(method string, params json.RawMessage) scriptedReply {
	return func(method string, _ json.RawMessage) scriptedReply {
		switch method {
		case appwire.MethodEvenerLaunchGetLayer:
			return scriptedReply{result: launchLayerFixture()}
		case appwire.MethodModelList:
			return scriptedReply{result: appwire.ModelListResponse{
				Data:        []appwire.ModelDescriptor{modelDescriptorFixture("gpt-x")},
				Diagnostics: []appwire.ModelListDiagnostic{{Message: "model-diag"}},
				Recent:      []appwire.ModelDescriptor{modelDescriptorFixture("gpt-recent")},
			}}
		case appwire.MethodEvenerPluginList:
			return scriptedReply{result: appwire.PluginListResponse{Plugins: []appwire.PluginEntry{{Plugin: "pl"}}}}
		case appwire.MethodEvenerAuthList:
			return scriptedReply{result: appwire.AuthListResponse{Providers: []appwire.AuthStatusResponse{{Provider: "auth", AuthModes: []string{"auth-mode"}}}}}
		case appwire.MethodEvenerInstanceList:
			return scriptedReply{result: instanceListFixture()}
		case appwire.MethodEvenerLaunchResolve:
			return scriptedReply{result: launchResolvedFixture()}
		default:
			return scriptedReply{result: appwire.EmptyResponse{}}
		}
	}
}

// launchLayerFixture populates every mutable field of the launch layer: the
// pointer-valued scalars, the string slices, the pointer-to-slice, the MCP
// specs with their own args, and the env map.
func launchLayerFixture() appwire.LaunchConfigLayer {
	enabledPlugins := []string{"enabled-plugin"}
	return appwire.LaunchConfigLayer{
		Schema:                     new(1),
		Model:                      "gpt-x",
		SandboxNet:                 new(false),
		MaxRounds:                  new(2),
		MaxSubagentDepth:           new(3),
		MaxConcurrentDelegateTurns: new(4),
		MaxRetainedTerminal:        new(5),
		NoProjectPrompts:           new(false),
		NonInteractive:             new(false),
		AppReplaySize:              new(6),
		Verbose:                    new(false),
		APILog:                     new(false),
		SkillsDirs:                 []string{"skills-dir"},
		PluginDirs:                 []string{"plugin-dir"},
		MCPConfigs:                 []string{"mcp-config"},
		SystemPromptAppend:         []string{"append"},
		ModelFallbacks:             []string{"fallback"},
		EnabledPlugins:             &enabledPlugins,
		MCPs:                       []appwire.MCPServerSpec{{Name: "mcp", Command: "mcp-cmd", Args: []string{"mcp-arg"}}},
		Env:                        map[string]string{"launch-env": "launch-env-value"},
	}
}

// modelDescriptorFixture populates every mutable field of one model row.
func modelDescriptorFixture(model string) appwire.ModelDescriptor {
	return appwire.ModelDescriptor{
		Provider:              "p",
		Model:                 model,
		DisplayName:           "display",
		ContextWindow:         new(1000),
		MaxInputTokens:        new(2000),
		SupportsTools:         new(true),
		SupportsVision:        new(false),
		MaxOutputTokens:       new(3000),
		SupportsWebSearch:     new(true),
		SupportsReasoning:     new(true),
		InputCostPerMillion:   new(1.5),
		OutputCostPerMillion:  new(2.5),
		ReasoningEffortLevels: []string{"low", "high"},
		Warnings:              []string{"model-warning"},
	}
}

// instanceListFixture populates every mutable field of the instance list: both
// instance entries with their vars, auth modes, warnings, and model inventory,
// and a provider descriptor whose Setup carries another entry's worth of the
// same fields.
func instanceListFixture() appwire.InstanceListResponse {
	return appwire.InstanceListResponse{
		Instances: []appwire.InstanceEntry{{
			Name:      "inst",
			Vars:      map[string]string{"inst-var": "inst-value"},
			AuthModes: []string{"inst-mode"},
			Warnings:  []string{"inst-warning"},
			Models:    []appwire.InstanceModelEntry{{ID: "inst-model", Disabled: true}},
		}},
		AvailableProviders: []appwire.ProviderDescriptor{{
			ID:        "prov",
			VarsEnv:   []string{"prov-env"},
			Vars:      map[string]string{"prov-var": "prov-value"},
			APIKeyEnv: []string{"prov-api-key"},
			AuthModes: []string{"prov-mode"},
			Setup: &appwire.InstanceEntry{
				Name:      "prov-setup",
				Vars:      map[string]string{"setup-var": "setup-value"},
				AuthModes: []string{"setup-mode"},
				Warnings:  []string{"setup-warning"},
				Models:    []appwire.InstanceModelEntry{{ID: "setup-model"}},
			},
		}},
		Diagnostics: []string{"inst-diag"},
	}
}

// launchResolvedFixture populates every mutable field of one per-root resolve
// result: the effective layer, the per-layer map, provenance, the repo status
// pointer, and the diagnostics slice.
func launchResolvedFixture() appwire.LaunchConfigResolved {
	return appwire.LaunchConfigResolved{
		Effective:  launchLayerFixture(),
		Layers:     map[string]appwire.LaunchConfigLayer{"global": launchLayerFixture()},
		Provenance: map[string]string{"model": "global"},
		Repo:       &appwire.RepoLaunchConfigStatus{Path: "/root/a", Hash: "abc", Trust: "trusted", Preview: "preview"},
		Diagnostics: []appwire.LaunchConfigDiagnostic{
			{Layer: "global", Field: "model", Message: "diag"},
		},
	}
}

// capabilityIsolationCase mutates one mutable field of a returned
// HostCapabilities through that returned value, then reads the same field back
// from a later cache-hit snapshot. want is what the fixture put there, so a
// case fails when the mutation reached the cached probe and passes its fixture
// guard when the probe never populated the field at all.
type capabilityIsolationCase struct {
	name   string
	mutate func(caps *HostCapabilities)
	read   func(caps HostCapabilities) any
	want   any
}

// scalarIsolationCase covers a scalar or pointer-scalar field: sel returns the
// address of the field, so the case can overwrite it and read it back.
func scalarIsolationCase[T comparable](name string, sel func(*HostCapabilities) *T, want, mutated T) capabilityIsolationCase {
	return capabilityIsolationCase{
		name:   name,
		mutate: func(caps *HostCapabilities) { *sel(caps) = mutated },
		read:   func(caps HostCapabilities) any { return *sel(&caps) },
		want:   want,
	}
}

// sliceIsolationCase covers a slice field through its first element: the clone
// must duplicate the backing array, not just the slice header.
func sliceIsolationCase[T any](name string, sel func(*HostCapabilities) []T, want, mutated T) capabilityIsolationCase {
	return capabilityIsolationCase{
		name:   name,
		mutate: func(caps *HostCapabilities) { sel(caps)[0] = mutated },
		read:   func(caps HostCapabilities) any { return sel(&caps)[0] },
		want:   want,
	}
}

// mapIsolationCase covers a map field through one key.
func mapIsolationCase[K comparable, V any](name string, sel func(*HostCapabilities) map[K]V, key K, want, mutated V) capabilityIsolationCase {
	return capabilityIsolationCase{
		name:   name,
		mutate: func(caps *HostCapabilities) { sel(caps)[key] = mutated },
		read:   func(caps HostCapabilities) any { return sel(&caps)[key] },
		want:   want,
	}
}

// capabilityIsolationCases names every mutable field the snapshot's clone must
// duplicate. A field the clone forgets shows up as that case's mutation
// reaching the cache-hit snapshot.
func capabilityIsolationCases() []capabilityIsolationCase {
	return []capabilityIsolationCase{
		// The launch layer's pointer-valued scalars.
		scalarIsolationCase("LaunchGlobal.Schema", func(c *HostCapabilities) *int { return c.LaunchGlobal.Schema }, 1, 99),
		scalarIsolationCase("LaunchGlobal.SandboxNet", func(c *HostCapabilities) *bool { return c.LaunchGlobal.SandboxNet }, false, true),
		scalarIsolationCase("LaunchGlobal.MaxRounds", func(c *HostCapabilities) *int { return c.LaunchGlobal.MaxRounds }, 2, 99),
		scalarIsolationCase("LaunchGlobal.MaxSubagentDepth", func(c *HostCapabilities) *int { return c.LaunchGlobal.MaxSubagentDepth }, 3, 99),
		scalarIsolationCase("LaunchGlobal.MaxConcurrentDelegateTurns", func(c *HostCapabilities) *int { return c.LaunchGlobal.MaxConcurrentDelegateTurns }, 4, 99),
		scalarIsolationCase("LaunchGlobal.MaxRetainedTerminal", func(c *HostCapabilities) *int { return c.LaunchGlobal.MaxRetainedTerminal }, 5, 99),
		scalarIsolationCase("LaunchGlobal.NoProjectPrompts", func(c *HostCapabilities) *bool { return c.LaunchGlobal.NoProjectPrompts }, false, true),
		scalarIsolationCase("LaunchGlobal.NonInteractive", func(c *HostCapabilities) *bool { return c.LaunchGlobal.NonInteractive }, false, true),
		scalarIsolationCase("LaunchGlobal.AppReplaySize", func(c *HostCapabilities) *int { return c.LaunchGlobal.AppReplaySize }, 6, 99),
		scalarIsolationCase("LaunchGlobal.Verbose", func(c *HostCapabilities) *bool { return c.LaunchGlobal.Verbose }, false, true),
		scalarIsolationCase("LaunchGlobal.APILog", func(c *HostCapabilities) *bool { return c.LaunchGlobal.APILog }, false, true),
		// ...and its slices, pointer to slice, MCP args, and env map.
		sliceIsolationCase("LaunchGlobal.SkillsDirs", func(c *HostCapabilities) []string { return c.LaunchGlobal.SkillsDirs }, "skills-dir", "mutated"),
		sliceIsolationCase("LaunchGlobal.PluginDirs", func(c *HostCapabilities) []string { return c.LaunchGlobal.PluginDirs }, "plugin-dir", "mutated"),
		sliceIsolationCase("LaunchGlobal.MCPConfigs", func(c *HostCapabilities) []string { return c.LaunchGlobal.MCPConfigs }, "mcp-config", "mutated"),
		sliceIsolationCase("LaunchGlobal.SystemPromptAppend", func(c *HostCapabilities) []string { return c.LaunchGlobal.SystemPromptAppend }, "append", "mutated"),
		sliceIsolationCase("LaunchGlobal.ModelFallbacks", func(c *HostCapabilities) []string { return c.LaunchGlobal.ModelFallbacks }, "fallback", "mutated"),
		sliceIsolationCase("LaunchGlobal.EnabledPlugins", func(c *HostCapabilities) []string { return *c.LaunchGlobal.EnabledPlugins }, "enabled-plugin", "mutated"),
		scalarIsolationCase("LaunchGlobal.MCPs[0].Command", func(c *HostCapabilities) *string { return &c.LaunchGlobal.MCPs[0].Command }, "mcp-cmd", "mutated"),
		sliceIsolationCase("LaunchGlobal.MCPs[0].Args", func(c *HostCapabilities) []string { return c.LaunchGlobal.MCPs[0].Args }, "mcp-arg", "mutated"),
		mapIsolationCase("LaunchGlobal.Env", func(c *HostCapabilities) map[string]string { return c.LaunchGlobal.Env }, "launch-env", "launch-env-value", "mutated"),
		// The model list: rows, their pointer scalars and slices, diagnostics,
		// and the recent group.
		scalarIsolationCase("Models.Data[0].Model", func(c *HostCapabilities) *string { return &c.Models.Data[0].Model }, "gpt-x", "mutated"),
		scalarIsolationCase("Models.Data[0].ContextWindow", func(c *HostCapabilities) *int { return c.Models.Data[0].ContextWindow }, 1000, 99),
		scalarIsolationCase("Models.Data[0].MaxInputTokens", func(c *HostCapabilities) *int { return c.Models.Data[0].MaxInputTokens }, 2000, 99),
		scalarIsolationCase("Models.Data[0].SupportsTools", func(c *HostCapabilities) *bool { return c.Models.Data[0].SupportsTools }, true, false),
		scalarIsolationCase("Models.Data[0].SupportsVision", func(c *HostCapabilities) *bool { return c.Models.Data[0].SupportsVision }, false, true),
		scalarIsolationCase("Models.Data[0].MaxOutputTokens", func(c *HostCapabilities) *int { return c.Models.Data[0].MaxOutputTokens }, 3000, 99),
		scalarIsolationCase("Models.Data[0].SupportsWebSearch", func(c *HostCapabilities) *bool { return c.Models.Data[0].SupportsWebSearch }, true, false),
		scalarIsolationCase("Models.Data[0].SupportsReasoning", func(c *HostCapabilities) *bool { return c.Models.Data[0].SupportsReasoning }, true, false),
		scalarIsolationCase("Models.Data[0].InputCostPerMillion", func(c *HostCapabilities) *float64 { return c.Models.Data[0].InputCostPerMillion }, 1.5, 99.0),
		scalarIsolationCase("Models.Data[0].OutputCostPerMillion", func(c *HostCapabilities) *float64 { return c.Models.Data[0].OutputCostPerMillion }, 2.5, 99.0),
		sliceIsolationCase("Models.Data[0].ReasoningEffortLevels", func(c *HostCapabilities) []string { return c.Models.Data[0].ReasoningEffortLevels }, "low", "mutated"),
		sliceIsolationCase("Models.Data[0].Warnings", func(c *HostCapabilities) []string { return c.Models.Data[0].Warnings }, "model-warning", "mutated"),
		scalarIsolationCase("Models.Diagnostics[0].Message", func(c *HostCapabilities) *string { return &c.Models.Diagnostics[0].Message }, "model-diag", "mutated"),
		scalarIsolationCase("Models.Recent[0].Model", func(c *HostCapabilities) *string { return &c.Models.Recent[0].Model }, "gpt-recent", "mutated"),
		scalarIsolationCase("Models.Recent[0].ContextWindow", func(c *HostCapabilities) *int { return c.Models.Recent[0].ContextWindow }, 1000, 99),
		sliceIsolationCase("Models.Recent[0].Warnings", func(c *HostCapabilities) []string { return c.Models.Recent[0].Warnings }, "model-warning", "mutated"),
		// Plugins, auth, and the constructor's roots.
		scalarIsolationCase("Plugins.Plugins[0].Plugin", func(c *HostCapabilities) *string { return &c.Plugins.Plugins[0].Plugin }, "pl", "mutated"),
		sliceIsolationCase("Auth.Providers[0].AuthModes", func(c *HostCapabilities) []string { return c.Auth.Providers[0].AuthModes }, "auth-mode", "mutated"),
		scalarIsolationCase("Roots[0]", func(c *HostCapabilities) *string { return &c.Roots[0] }, "/root/a", "/mutated"),
		// The per-root effective config: the map itself and the resolve shape
		// (effective layer, layer map, provenance, repo status, diagnostics) a
		// caller could mutate through it.
		mapIsolationCase("LaunchResolved", func(c *HostCapabilities) map[string]appwire.LaunchConfigResolved {
			return c.LaunchResolved
		}, "/root/a", launchResolvedFixture(), appwire.LaunchConfigResolved{Effective: appwire.LaunchConfigLayer{Model: "mutated"}}),
		scalarIsolationCase("LaunchResolved[/root/a].Effective.Schema", func(c *HostCapabilities) *int {
			return c.LaunchResolved["/root/a"].Effective.Schema
		}, 1, 99),
		sliceIsolationCase("LaunchResolved[/root/a].Effective.SkillsDirs", func(c *HostCapabilities) []string {
			return c.LaunchResolved["/root/a"].Effective.SkillsDirs
		}, "skills-dir", "mutated"),
		mapIsolationCase("LaunchResolved[/root/a].Effective.Env", func(c *HostCapabilities) map[string]string {
			return c.LaunchResolved["/root/a"].Effective.Env
		}, "launch-env", "launch-env-value", "mutated"),
		mapIsolationCase("LaunchResolved[/root/a].Layers", func(c *HostCapabilities) map[string]appwire.LaunchConfigLayer {
			return c.LaunchResolved["/root/a"].Layers
		}, "global", launchLayerFixture(), appwire.LaunchConfigLayer{Model: "mutated"}),
		mapIsolationCase("LaunchResolved[/root/a].Provenance", func(c *HostCapabilities) map[string]string {
			return c.LaunchResolved["/root/a"].Provenance
		}, "model", "global", "mutated"),
		{
			name:   "LaunchResolved[/root/a].Repo",
			mutate: func(c *HostCapabilities) { c.LaunchResolved["/root/a"].Repo.Trust = "mutated" },
			read:   func(c HostCapabilities) any { return c.LaunchResolved["/root/a"].Repo.Trust },
			want:   "trusted",
		},
		scalarIsolationCase("LaunchResolved[/root/a].Diagnostics[0].Message", func(c *HostCapabilities) *string {
			return &c.LaunchResolved["/root/a"].Diagnostics[0].Message
		}, "diag", "mutated"),
		// The instance list: both entry kinds, every field they own, and the
		// Setup entry a provider descriptor points at.
		scalarIsolationCase("Instances.Instances[0].Name", func(c *HostCapabilities) *string { return &c.Instances.Instances[0].Name }, "inst", "mutated"),
		mapIsolationCase("Instances.Instances[0].Vars", func(c *HostCapabilities) map[string]string { return c.Instances.Instances[0].Vars }, "inst-var", "inst-value", "mutated"),
		sliceIsolationCase("Instances.Instances[0].AuthModes", func(c *HostCapabilities) []string { return c.Instances.Instances[0].AuthModes }, "inst-mode", "mutated"),
		sliceIsolationCase("Instances.Instances[0].Warnings", func(c *HostCapabilities) []string { return c.Instances.Instances[0].Warnings }, "inst-warning", "mutated"),
		scalarIsolationCase("Instances.Instances[0].Models[0].ID", func(c *HostCapabilities) *string { return &c.Instances.Instances[0].Models[0].ID }, "inst-model", "mutated"),
		scalarIsolationCase("Instances.AvailableProviders[0].ID", func(c *HostCapabilities) *string { return &c.Instances.AvailableProviders[0].ID }, "prov", "mutated"),
		sliceIsolationCase("Instances.AvailableProviders[0].VarsEnv", func(c *HostCapabilities) []string { return c.Instances.AvailableProviders[0].VarsEnv }, "prov-env", "mutated"),
		mapIsolationCase("Instances.AvailableProviders[0].Vars", func(c *HostCapabilities) map[string]string { return c.Instances.AvailableProviders[0].Vars }, "prov-var", "prov-value", "mutated"),
		sliceIsolationCase("Instances.AvailableProviders[0].APIKeyEnv", func(c *HostCapabilities) []string { return c.Instances.AvailableProviders[0].APIKeyEnv }, "prov-api-key", "mutated"),
		sliceIsolationCase("Instances.AvailableProviders[0].AuthModes", func(c *HostCapabilities) []string { return c.Instances.AvailableProviders[0].AuthModes }, "prov-mode", "mutated"),
		mapIsolationCase("Instances.AvailableProviders[0].Setup.Vars", func(c *HostCapabilities) map[string]string {
			return c.Instances.AvailableProviders[0].Setup.Vars
		}, "setup-var", "setup-value", "mutated"),
		sliceIsolationCase("Instances.AvailableProviders[0].Setup.AuthModes", func(c *HostCapabilities) []string {
			return c.Instances.AvailableProviders[0].Setup.AuthModes
		}, "setup-mode", "mutated"),
		sliceIsolationCase("Instances.AvailableProviders[0].Setup.Warnings", func(c *HostCapabilities) []string {
			return c.Instances.AvailableProviders[0].Setup.Warnings
		}, "setup-warning", "mutated"),
		scalarIsolationCase("Instances.AvailableProviders[0].Setup.Models[0].ID", func(c *HostCapabilities) *string {
			return &c.Instances.AvailableProviders[0].Setup.Models[0].ID
		}, "setup-model", "mutated"),
		{
			// The Setup pointer itself must point at a copy, so writing through
			// it cannot rewrite the cached entry.
			name: "Instances.AvailableProviders[0].Setup",
			mutate: func(c *HostCapabilities) {
				*c.Instances.AvailableProviders[0].Setup = appwire.InstanceEntry{Name: "mutated"}
			},
			read: func(c HostCapabilities) any { return c.Instances.AvailableProviders[0].Setup.Name },
			want: "prov-setup",
		},
		scalarIsolationCase("Instances.Diagnostics[0]", func(c *HostCapabilities) *string { return &c.Instances.Diagnostics[0] }, "inst-diag", "mutated"),
	}
}

// TestRemoteHubSourceHostCapabilitiesCacheIsolated pins that the cache hit and
// the fresh probe hand back values that do not alias the stored snapshot, so a
// caller mutating a returned value cannot corrupt the cached capabilities for
// every later caller. The leading assertions keep the original coarse coverage;
// capabilityIsolationCases then takes every mutable field of every snapshot
// type in turn, mutates it through a returned snapshot, and checks the
// cache-hit snapshot still holds the value the probe read.
func TestRemoteHubSourceHostCapabilitiesCacheIsolated(t *testing.T) {
	client, _ := newScriptedClient(t, richCapabilityReply())
	source := NewRemoteHubSource("host", []string{"/root/a"}, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	first, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("first HostCapabilities: %v", err)
	}
	first.Models.Data[0].Model = "mutated"
	first.Plugins.Plugins[0].Plugin = "mutated"
	first.Auth.Providers[0].Provider = "mutated"
	first.Roots[0] = "/mutated"

	second, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("second HostCapabilities: %v", err)
	}
	if second.Models.Data[0].Model != "gpt-x" {
		t.Fatalf("cached Models.Data[0].Model = %q, want gpt-x: caller mutation reached the cache", second.Models.Data[0].Model)
	}
	if second.Plugins.Plugins[0].Plugin != "pl" {
		t.Fatalf("cached Plugins[0].Plugin = %q, want pl: caller mutation reached the cache", second.Plugins.Plugins[0].Plugin)
	}
	if second.Auth.Providers[0].Provider != "auth" {
		t.Fatalf("cached Auth.Providers[0].Provider = %q, want auth: caller mutation reached the cache", second.Auth.Providers[0].Provider)
	}
	if !reflect.DeepEqual(second.Roots, []string{"/root/a"}) {
		t.Fatalf("cached Roots = %v, want [/root/a]: caller mutation reached the cache", second.Roots)
	}

	for _, tc := range capabilityIsolationCases() {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh source per case: one case's mutation cannot mask or fake
			// another field's failure through a shared cached probe.
			client, _ := newScriptedClient(t, richCapabilityReply())
			source := NewRemoteHubSource("host", []string{"/root/a"}, func(context.Context, string) (*appwire.Client, error) {
				return client, nil
			})
			first, err := source.HostCapabilities(t.Context())
			if err != nil {
				t.Fatalf("first HostCapabilities: %v", err)
			}
			if got := tc.read(first); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("probed %s = %v, want %v: the fixture did not populate the field this case pins", tc.name, got, tc.want)
			}
			tc.mutate(&first)

			second, err := source.HostCapabilities(t.Context())
			if err != nil {
				t.Fatalf("second HostCapabilities: %v", err)
			}
			if got := tc.read(second); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("cached %s = %v, want %v: caller mutation reached the cache", tc.name, got, tc.want)
			}
		})
	}
}

// TestRemoteHubSourceHostCapabilitiesCacheIsolatedPointerScalars pins that the
// pointer-valued scalars the launch layer and model list carry are deep-copied
// too: mutating a returned *LaunchGlobal.Schema or *Models.Data[0].ContextWindow
// must not reach the cached snapshot.
func TestRemoteHubSourceHostCapabilitiesCacheIsolatedPointerScalars(t *testing.T) {
	schema := 1
	contextWindow := 1000
	reply := func(method string, _ json.RawMessage) scriptedReply {
		switch method {
		case appwire.MethodEvenerLaunchGetLayer:
			return scriptedReply{result: appwire.LaunchConfigLayer{Model: "gpt-x", Schema: &schema}}
		case appwire.MethodModelList:
			return scriptedReply{result: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "p", Model: "gpt-x", ContextWindow: &contextWindow}}}}
		default:
			return scriptedReply{result: appwire.EmptyResponse{}}
		}
	}
	client, _ := newScriptedClient(t, reply)
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	first, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("first HostCapabilities: %v", err)
	}
	if first.LaunchGlobal.Schema == nil || first.Models.Data[0].ContextWindow == nil {
		t.Fatalf("probe returned nil pointer scalars in %+v; the fixture did not exercise them", first)
	}
	*first.LaunchGlobal.Schema = 99
	*first.Models.Data[0].ContextWindow = 99

	second, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("second HostCapabilities: %v", err)
	}
	if second.LaunchGlobal.Schema == nil || *second.LaunchGlobal.Schema != 1 {
		t.Fatalf("cached LaunchGlobal.Schema = %v, want 1: caller mutation reached the cache", second.LaunchGlobal.Schema)
	}
	if second.Models.Data[0].ContextWindow == nil || *second.Models.Data[0].ContextWindow != 1000 {
		t.Fatalf("cached Models.Data[0].ContextWindow = %v, want 1000: caller mutation reached the cache", second.Models.Data[0].ContextWindow)
	}
}

// TestRemoteHubSourceHostCapabilitiesResolvesLaunchPerRoot pins the per-root
// effective-config probe row: for every configured root the probe calls
// evener/launch/resolve with that root as cwd and stores the result in
// LaunchResolved, keyed by the root path (component 05 spec, "Capability
// probe": `Roots[i]` -> `evener/launch/resolve` result for that root).
func TestRemoteHubSourceHostCapabilitiesResolvesLaunchPerRoot(t *testing.T) {
	base := capabilityReply("gpt-x", "pl")
	client, calls := newScriptedClient(t, func(method string, params json.RawMessage) scriptedReply {
		if method == appwire.MethodEvenerLaunchResolve {
			var resolve appwire.LaunchConfigResolveParams
			if err := json.Unmarshal(params, &resolve); err != nil {
				t.Fatalf("resolve params: %v", err)
			}
			return scriptedReply{result: appwire.LaunchConfigResolved{
				Effective: appwire.LaunchConfigLayer{Model: "model-" + path.Base(resolve.CWD)},
			}}
		}
		return base(method, params)
	})
	source := NewRemoteHubSource("host", []string{"/root/a", "/root/b"}, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	caps, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("HostCapabilities: %v", err)
	}
	if len(caps.LaunchResolved) != 2 {
		t.Fatalf("LaunchResolved = %+v, want one entry per configured root", caps.LaunchResolved)
	}
	for root, wantModel := range map[string]string{"/root/a": "model-a", "/root/b": "model-b"} {
		resolved, ok := caps.LaunchResolved[root]
		if !ok {
			t.Fatalf("LaunchResolved[%q] missing; map = %+v", root, caps.LaunchResolved)
		}
		if resolved.Effective.Model != wantModel {
			t.Fatalf("LaunchResolved[%q].Effective.Model = %q, want %q: the result must be keyed by its root", root, resolved.Effective.Model, wantModel)
		}
	}
	var cwds []string
	for _, call := range calls() {
		if call.method != appwire.MethodEvenerLaunchResolve {
			continue
		}
		var resolve appwire.LaunchConfigResolveParams
		if err := json.Unmarshal(call.params, &resolve); err != nil {
			t.Fatalf("resolve params: %v", err)
		}
		cwds = append(cwds, resolve.CWD)
	}
	if !slices.Equal(cwds, []string{"/root/a", "/root/b"}) {
		t.Fatalf("resolve cwds = %v, want the configured roots in order", cwds)
	}
}

// TestRemoteHubSourceHostCapabilitiesMissingRootStaysEmpty pins the spec
// comment "empty when a root has no effective layer": a configured root whose
// resolution cannot produce one — the hub answers InvalidParams because the
// root does not exist on the host — is left out of LaunchResolved while the
// rest of the snapshot still lands.
func TestRemoteHubSourceHostCapabilitiesMissingRootStaysEmpty(t *testing.T) {
	missing := appwire.InvalidParams("cwd: resolve: stat /missing: no such file or directory")
	base := capabilityReply("gpt-x", "pl")
	client, _ := newScriptedClient(t, func(method string, params json.RawMessage) scriptedReply {
		if method == appwire.MethodEvenerLaunchResolve {
			var resolve appwire.LaunchConfigResolveParams
			if err := json.Unmarshal(params, &resolve); err != nil {
				t.Fatalf("resolve params: %v", err)
			}
			if resolve.CWD == "/missing" {
				return scriptedReply{wireErr: &missing}
			}
			return scriptedReply{result: appwire.LaunchConfigResolved{Effective: appwire.LaunchConfigLayer{Model: "model-present"}}}
		}
		return base(method, params)
	})
	source := NewRemoteHubSource("host", []string{"/present", "/missing"}, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	caps, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("HostCapabilities: %v", err)
	}
	if resolved, ok := caps.LaunchResolved["/missing"]; ok {
		t.Fatalf("LaunchResolved[/missing] = %+v, want no entry for a root with no effective layer", resolved)
	}
	if resolved, ok := caps.LaunchResolved["/present"]; !ok || resolved.Effective.Model != "model-present" {
		t.Fatalf("LaunchResolved[/present] = %+v (present=%v), want the resolvable root's result", resolved, ok)
	}
	if caps.LaunchGlobal.Model != "gpt-x" {
		t.Fatalf("LaunchGlobal.Model = %q, want gpt-x: the rest of the snapshot must still land", caps.LaunchGlobal.Model)
	}
}

// TestRemoteHubSourceHostCapabilitiesResolveErrorStillAborts pins that the
// per-root tolerance is narrow: only the InvalidParams that means "no
// effective layer for this root" is skipped. Every other resolve failure —
// here a hub-side InternalError — is a real probe failure.
func TestRemoteHubSourceHostCapabilitiesResolveErrorStillAborts(t *testing.T) {
	failed := appwire.InternalError("resolve: global: bad toml")
	base := capabilityReply("gpt-x", "pl")
	client, _ := newScriptedClient(t, func(method string, params json.RawMessage) scriptedReply {
		if method == appwire.MethodEvenerLaunchResolve {
			return scriptedReply{wireErr: &failed}
		}
		return base(method, params)
	})
	source := NewRemoteHubSource("host", []string{"/root/a"}, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	_, err := source.HostCapabilities(t.Context())
	if err == nil {
		t.Fatal("HostCapabilities succeeded despite a non-InvalidParams resolve failure")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInternalError {
		t.Fatalf("error = %T %v, want InternalError WireError", err, err)
	}
}
