package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
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
	source.SetHostFacts(func(_ context.Context, host string) (HostFacts, error) {
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
	source.SetHostFacts(func(context.Context, string) (HostFacts, error) {
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
	source.SetHostFacts(func(context.Context, string) (HostFacts, error) {
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
		source.SetHostFacts(func(context.Context, string) (HostFacts, error) { return HostFacts{}, nil })
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
