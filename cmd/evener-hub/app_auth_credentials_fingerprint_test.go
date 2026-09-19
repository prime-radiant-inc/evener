package hub

// Regression tests for the reviewed-endpoint assertion on evener/auth/test
// (roborev finding on PR #1136): the probe must bind its model-list request to
// the endpoint the caller reviewed, validate that assertion against the
// snapshot the probe itself will dial, and refuse before dialing when the name
// no longer resolves there.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm/registry"
)

// newRawProbeRegistry loads a bare registry - the value credentialProbeClient
// hands back from Registry() - over exactly instances, keyed under stateDir.
// Tests that assert an endpoint fingerprint build the probe's registry with it
// so the assertion is validated against the snapshot the probe would dial,
// which may differ from the hub's own registry.
func newRawProbeRegistry(t *testing.T, stateDir string, env map[string]string, instances map[string]registry.Provider) *registry.Registry {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true),
		registry.WithoutCache(),
		registry.WithNoUserLayer(),
		registry.WithStateRoot(stateDir),
		registry.WithEnv(func(name string) (string, bool) {
			v, ok := env[name]
			return v, ok
		}),
		registry.WithInstances(instances),
	)
	if err != nil {
		t.Fatalf("probe registry: %v", err)
	}
	return r
}

// assertConflict fails unless err is an appwire.WireError carrying CodeConflict.
func assertConflict(t *testing.T, err error) {
	t.Helper()
	wireErr, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("error = %v (%T), want appwire.WireError with CodeConflict", err, err)
	}
	if wireErr.Code != appwire.CodeConflict {
		t.Fatalf("error code = %d (%q), want conflict", wireErr.Code, wireErr.Message)
	}
}

// assertEndpointConflict fails unless err carries the endpoint-conflict
// discriminant: CodeConflict alone is shared with genuine conflicts (a name
// collision, an expired flow), so a client matching only the code would read
// those as a moved endpoint.
func assertEndpointConflict(t *testing.T, err error) {
	t.Helper()
	wireErr, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("error = %v (%T), want appwire.WireError", err, err)
	}
	if wireErr.Code != appwire.CodeConflict {
		t.Fatalf("error code = %d (%q), want conflict", wireErr.Code, wireErr.Message)
	}
	data, ok := wireErr.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("error data = %#v, want appwire.ErrorData", wireErr.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorEndpointConflict {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorEndpointConflict)
	}
}

func TestAuthTestCredentialsRefusesMismatchedAssertionWithoutDialing(t *testing.T) {
	instances := map[string]registry.Provider{"custom": {
		Base:      "openai-compatible",
		APIKey:    "configured",
		Transport: registry.Transport{BaseURL: "http://provider.test/v1"},
	}}
	client := &credentialProbeFakeClient{}
	c := newCredentialProbeController(t, client, instances, nil)
	client.reg = newRawProbeRegistry(t, c.stateDir, nil, instances)

	_, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{
		Provider:                    "custom",
		ExpectedEndpointFingerprint: "an-endpoint-this-name-does-not-resolve-to",
	})
	assertEndpointConflict(t, err)
	if got := client.callCount(); got != 0 {
		t.Fatalf("probe calls=%d, want 0: a mismatched assertion must be refused before the probe dials", got)
	}
}

func TestAuthTestCredentialsDialsWhenAssertionMatchesProbeRegistry(t *testing.T) {
	instances := map[string]registry.Provider{"custom": {
		Base:      "openai-compatible",
		APIKey:    "configured",
		Transport: registry.Transport{BaseURL: "http://provider.test/v1"},
	}}
	client := &credentialProbeFakeClient{}
	c := newCredentialProbeController(t, client, instances, nil)
	probeReg := newRawProbeRegistry(t, c.stateDir, nil, instances)
	client.reg = probeReg

	// Compute the fingerprint exactly the way the probe computes it: resolve
	// the instance from the fake's registry and digest its destination.
	inst, ok := probeReg.Instance("custom")
	if !ok {
		t.Fatal("the probe registry has no instance custom")
	}
	fingerprint := destinationFingerprint(c.stateDir, probeReg, inst)
	if fingerprint == "" {
		t.Fatal("destinationFingerprint is empty; this case cannot assert a real endpoint")
	}

	resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{
		Provider:                    "custom",
		ExpectedEndpointFingerprint: fingerprint,
	})
	if err != nil {
		t.Fatalf("TestCredentials: %v", err)
	}
	if resp.Status != appwire.AuthTestStatusSuccess {
		t.Fatalf("status=%q (%q), want success", resp.Status, resp.Message)
	}
	if got := client.callCount(); got != 1 {
		t.Fatalf("probe calls=%d, want exactly 1", got)
	}
}

// TestAuthTestCredentialsDialsWithEmptyAssertion: a caller that captured no
// fingerprint - the TUI's credential panel - asserts nothing, which is not
// checked, so the probe still dials.
func TestAuthTestCredentialsDialsWithEmptyAssertion(t *testing.T) {
	instances := map[string]registry.Provider{"custom": {
		Base:      "openai-compatible",
		APIKey:    "configured",
		Transport: registry.Transport{BaseURL: "http://provider.test/v1"},
	}}
	client := &credentialProbeFakeClient{}
	c := newCredentialProbeController(t, client, instances, nil)

	resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "custom"})
	if err != nil {
		t.Fatalf("TestCredentials: %v", err)
	}
	if resp.Status != appwire.AuthTestStatusSuccess {
		t.Fatalf("status=%q (%q), want success", resp.Status, resp.Message)
	}
	if got := client.callCount(); got != 1 {
		t.Fatalf("probe calls=%d, want exactly 1", got)
	}
}

// TestAuthTestCredentialsValidatesAssertionAgainstProbeRegistry is the reported
// scenario: the hub lists the name against http://hub.test/v1, but the probe's
// own snapshot resolves the same name to http://probe.test/v1. The assertion is
// the hub-side fingerprint, so the check must refuse rather than send the
// stored credential to the probe's endpoint.
func TestAuthTestCredentialsValidatesAssertionAgainstProbeRegistry(t *testing.T) {
	hubInstances := map[string]registry.Provider{"custom": {
		Base:      "openai-compatible",
		APIKey:    "configured",
		Transport: registry.Transport{BaseURL: "http://hub.test/v1"},
	}}
	probeInstances := map[string]registry.Provider{"custom": {
		Base:      "openai-compatible",
		APIKey:    "configured",
		Transport: registry.Transport{BaseURL: "http://probe.test/v1"},
	}}
	client := &credentialProbeFakeClient{}
	c := newCredentialProbeController(t, client, hubInstances, nil)
	// Same state root as the hub, so both registries key fingerprints with one
	// key and only the resolved endpoint can differ.
	client.reg = newRawProbeRegistry(t, c.stateDir, nil, probeInstances)

	hubInst, ok := c.registry().Instance("custom")
	if !ok {
		t.Fatal("the hub registry has no instance custom")
	}
	hubFingerprint := destinationFingerprint(c.stateDir, c.registry(), hubInst)
	if hubFingerprint == "" {
		t.Fatal("hub fingerprint is empty; this case cannot assert the reviewed endpoint")
	}

	_, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{
		Provider:                    "custom",
		ExpectedEndpointFingerprint: hubFingerprint,
	})
	assertConflict(t, err)
	if got := client.callCount(); got != 0 {
		t.Fatalf("probe calls=%d, want 0: the check must never dial the probe's endpoint", got)
	}
}

// TestAuthTestCredentialsScopesInFlightSharingByAssertion: two callers checking
// the same name against different endpoints must not share one probe - a joined
// result would answer about a destination one of them never asserted. Both
// assertions here are wrong, so neither call dials.
func TestAuthTestCredentialsScopesInFlightSharingByAssertion(t *testing.T) {
	instances := map[string]registry.Provider{"custom": {
		Base:      "openai-compatible",
		APIKey:    "configured",
		Transport: registry.Transport{BaseURL: "http://provider.test/v1"},
	}}
	client := &credentialProbeFakeClient{}
	c := newCredentialProbeController(t, client, instances, nil)
	client.reg = newRawProbeRegistry(t, c.stateDir, nil, instances)

	joined := make(chan struct{})
	c.credentialTestJoined = func() {
		select {
		case <-joined:
		default:
			close(joined)
		}
	}

	// Hold the first call inside the loader so it is registered and in flight
	// when the second arrives; only then can the two share one probe.
	firstLoaded := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	defer release()
	var loads atomic.Int32
	c.credentialTestLoader = func(string, bool) (credentialProbeClient, error) {
		if loads.Add(1) == 1 {
			close(firstLoaded)
			<-releaseFirst
		}
		return client, nil
	}

	type outcome struct {
		resp appwire.AuthTestResponse
		err  error
	}
	run := func(asserted string, done chan<- outcome) {
		resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{
			Provider:                    "custom",
			ExpectedEndpointFingerprint: asserted,
		})
		done <- outcome{resp: resp, err: err}
	}

	first := make(chan outcome, 1)
	go run("wrong-endpoint-one", first)
	<-firstLoaded

	second := make(chan outcome, 1)
	go run("wrong-endpoint-two", second)

	select {
	case <-joined:
		t.Fatal("two callers asserting different endpoints shared one in-flight probe")
	case got := <-second:
		assertConflict(t, got.err)
		if got.resp.Status == appwire.AuthTestStatusSuccess {
			t.Fatalf("second call status=%q, want conflict: its assertion is wrong", got.resp.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second call neither completed nor joined; the sharing key is not scoping by assertion")
	}

	release()
	got := <-first
	assertConflict(t, got.err)
	if got.resp.Status == appwire.AuthTestStatusSuccess {
		t.Fatalf("first call status=%q, want conflict: its assertion is wrong", got.resp.Status)
	}
	if calls := client.callCount(); calls != 0 {
		t.Fatalf("probe calls=%d, want 0: both assertions were wrong, so neither may dial", calls)
	}
}
