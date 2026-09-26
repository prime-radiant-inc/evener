package hub

// A sign-in flow's instance can move while the flow is open: the token
// exchange is a browser round trip long, and an edit that re-points the
// instance at another endpoint lands inside it. Only the auth scheme is asked
// again under the credential lock, and an edit to base_url keeps the scheme -
// so without the endpoint being bound too, the record the user just authorized
// is filed against a destination they never signed in for, and the transport
// sends it there.
//
// These tests pin that binding on both flows: the login flow's authorize round
// trip and the device flow's poll.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
)

// One Codex instance at two endpoints: base_url is what the record's transport
// talks to, so an edit to it moves the whole destination the credential is
// used against.
const (
	codexEndpointToml      = "[providers.work]\nbase = \"openai-codex\"\nbase_url = \"https://codex-a.example.test/v1\"\n"
	codexMovedEndpointToml = "[providers.work]\nbase = \"openai-codex\"\nbase_url = \"https://codex-b.example.test/v1\"\n"
)

// moveEndpointDuring is what an edit to providers.toml leaves behind inside a
// flow's long step: the file re-pointed and the registry reloaded onto it.
func moveEndpointDuring(t *testing.T, c *hubAuthController, path string) {
	t.Helper()
	renameProvidersEntry(t, c, path, codexMovedEndpointToml)
}

// loginCallbackURL builds the redirect a browser would hand back, carrying the
// flow's own state.
func loginCallbackURL(t *testing.T, authorizeURL, state string) string {
	t.Helper()
	if authorizeURL == "" {
		t.Fatal("LoginStart returned no authorize URL")
	}
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	if got := parsed.Query().Get("state"); got != state {
		t.Fatalf("authorize URL state = %q, want %q", got, state)
	}
	return "http://localhost:1455/auth/callback?code=auth-code&state=" + url.QueryEscape(state)
}

func TestAuth_LoginCompleteRefusesARecordForAnEndpointThatMoved(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexEndpointToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}
	ctrl.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		moveEndpointDuring(t, ctrl, tomlPath)
		return authopenai.TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			TokenType:    "Bearer",
			Scope:        "openid profile email",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	}

	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("LoginStart: %v", err)
	}
	authorizeURL, err := url.Parse(start.URL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}

	_, err = ctrl.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
		Provider:    "work",
		FlowID:      start.FlowID,
		RedirectURL: loginCallbackURL(t, start.URL, authorizeURL.Query().Get("state")),
	})
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("LoginComplete = %v, want the refusal for the endpoint the flow was not started on", err)
	}
	if _, err := authopenai.LoadAuth(stateDir, "work"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth(work) err = %v, want ErrAuthNotFound: the record must not land on the moved endpoint", err)
	}
}

// TestAuth_LoginCompleteSavesForTheEndpointTheFlowStartedOn is the control:
// the same flow, unchanged, still saves the record - the binding refuses a
// moved destination, not a sign-in.
func TestAuth_LoginCompleteSavesForTheEndpointTheFlowStartedOn(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexEndpointToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}
	ctrl.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		return authopenai.TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			TokenType:    "Bearer",
			Scope:        "openid profile email",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	}

	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("LoginStart: %v", err)
	}
	authorizeURL, err := url.Parse(start.URL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}

	if _, err := ctrl.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
		Provider:    "work",
		FlowID:      start.FlowID,
		RedirectURL: loginCallbackURL(t, start.URL, authorizeURL.Query().Get("state")),
	}); err != nil {
		t.Fatalf("LoginComplete: %v", err)
	}
	if _, err := authopenai.LoadAuth(stateDir, "work"); err != nil {
		t.Fatalf("LoadAuth(work) err = %v, want the freshly saved record", err)
	}
}

// TestAuth_DeviceFlowBindsTheEndpointItStartedOn pins where the device flow's
// endpoint is captured: the device-code request is itself a network round trip,
// so a capture made after it describes whatever the instance was re-pointed at
// during the request, and the flow then holds the record it is about to file
// under that new endpoint as though the user had asked for it. The binding is
// only worth anything if it is the endpoint the flow started on.
func TestAuth_DeviceFlowBindsTheEndpointItStartedOn(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexEndpointToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		moveEndpointDuring(t, ctrl, tomlPath)
		return authopenai.DeviceCode{UserCode: "U", VerificationURL: "https://x", DeviceAuthID: "d", Interval: time.Second}, nil
	}
	ctrl.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
		return authopenai.DeviceCodeSuccess{AuthorizationCode: "ac", CodeVerifier: "cv"}, false, nil
	}
	ctrl.exchangeDevice = func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error) {
		return authopenai.TokenSet{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
	}

	start, err := ctrl.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	_, err = ctrl.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "work", FlowID: start.FlowID})
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("DevicePoll = %v, want the refusal for the endpoint the flow was started on", err)
	}
	if _, err := authopenai.LoadAuth(stateDir, "work"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth(work) err = %v, want ErrAuthNotFound: the record must not land on the endpoint that arrived during the device-code request", err)
	}
}

// TestAuth_EndpointAssertionsTheHubCannotResolveAreRefused pins the rule the two
// checks share: an empty assertion is a client that was shown no endpoint, or an
// older peer, and is nothing to check, while an assertion the hub resolves no
// current value for is refused. Landing a credential on a destination nobody can
// describe is what the assertion exists to prevent, and waving it through would
// let an unusable state root switch the guard off without a word.
//
// The flow's half is pinned here rather than end to end: a flow cannot outlive
// the process that captured it, and that process holds the key it digested
// with, so the end-to-end window in which a current fingerprint disappears
// belongs to the credential write (TestInstances_ApiKeySetRefusesAnAssertionTheHubCannotCheck).
func TestAuth_EndpointAssertionsTheHubCannotResolveAreRefused(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	ctrl := newTestAuthController(t, dir, t.TempDir(), writeProvidersToml(t, dir, codexInstanceToml))
	// No view to answer from: the fingerprint resolves nothing, which is the
	// state an unusable key or an unloaded registry leaves.
	ctrl.reg = nil

	if err := ctrl.verifyEndpointFingerprint("work", ""); err != nil {
		t.Fatalf("verifyEndpointFingerprint with no assertion = %v, want nil", err)
	}
	if err := ctrl.verifyFlowEndpoint("work", ""); err != nil {
		t.Fatalf("verifyFlowEndpoint with no capture = %v, want nil", err)
	}

	err := ctrl.verifyEndpointFingerprint("work", "asserted-by-a-form")
	if err == nil || !strings.Contains(err.Error(), "cannot be checked against the endpoint") {
		t.Fatalf("verifyEndpointFingerprint = %v, want the refusal for an assertion the hub cannot resolve", err)
	}
	err = ctrl.verifyFlowEndpoint("work", "captured-when-the-flow-started")
	if err == nil || !strings.Contains(err.Error(), "cannot be checked against the endpoint") {
		t.Fatalf("verifyFlowEndpoint = %v, want the refusal for a flow the hub cannot place", err)
	}
}

// TestAuth_DevicePollRefusesARecordForAnEndpointThatMoved is the same binding
// on the device flow, whose own exchange is the long step.
func TestAuth_DevicePollRefusesARecordForAnEndpointThatMoved(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexEndpointToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{UserCode: "U", VerificationURL: "https://x", DeviceAuthID: "d", Interval: time.Second}, nil
	}
	ctrl.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
		return authopenai.DeviceCodeSuccess{AuthorizationCode: "ac", CodeVerifier: "cv"}, false, nil
	}
	ctrl.exchangeDevice = func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error) {
		moveEndpointDuring(t, ctrl, tomlPath)
		return authopenai.TokenSet{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
	}

	start, err := ctrl.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	_, err = ctrl.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "work", FlowID: start.FlowID})
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("DevicePoll = %v, want the refusal for the endpoint the flow was not started on", err)
	}
	if _, err := authopenai.LoadAuth(stateDir, "work"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth(work) err = %v, want ErrAuthNotFound: the record must not land on the moved endpoint", err)
	}
}

// codexUnresolvedEndpointToml is one Codex instance with no destination this
// hub can describe: its base URL names a variable nothing supplies, so the
// registry hides it. The auth scheme is still Codex, so a flow can be started
// for the name - which is what the "no destination to name" cases need: a
// start that is allowed, and a completion with no endpoint to compare.
const codexUnresolvedEndpointToml = "[providers.work]\nbase = \"openai-codex\"\nbase_url = \"{EVENER_TEST_CODEX_BASE}\"\n"

// assertFlowEndpointRefusal fails unless err is the appwire Conflict the flow
// guards answer a destination the hub cannot key with, and names it as such.
func assertFlowEndpointRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want the endpoint refusal, got nil")
	}
	// The discriminant, not only the code: EndpointConflict shares CodeConflict
	// with genuine conflicts (a name collision, an expired flow), and every
	// client keys on the discriminant - so asserting the code here would let a
	// guard regress to a plain Conflict with the whole suite still green.
	assertEndpointConflict(t, err)
	if msg := err.Error(); !strings.Contains(msg, "endpoint") || !strings.Contains(msg, "fingerprint") {
		t.Fatalf("err = %q, want it to name the endpoint/fingerprint refusal", msg)
	}
}

// TestAuth_LoginStartRefusesACodexDestinationItCannotKey pins the start half of
// verifyFlowEndpoint's rule: an empty capture means "nothing to check" only
// when the name has no destination. Here it has one - a real base URL - and the
// state root cannot yield its fingerprint key, so the completion would have no
// comparison at all and would file the record wherever the instance points by
// then. The flow is refused before it is recorded.
func TestAuth_LoginStartRefusesACodexDestinationItCannotKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	unkeyableStateRoot(t, stateDir)
	tomlPath := writeProvidersToml(t, dir, codexEndpointToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}

	// The premise: a destination that is real, and a hub that cannot key it.
	if fp := ctrl.endpointFingerprintFor("work"); fp != "" {
		t.Fatalf("endpointFingerprintFor(work) = %q, want empty while the state root cannot be keyed", fp)
	}
	if !ctrl.endpointHasDestination("work") {
		t.Fatal("endpointHasDestination(work) = false, want a real destination")
	}

	_, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "work"})
	assertFlowEndpointRefusal(t, err)
	ctrl.mu.Lock()
	flows := len(ctrl.flows)
	ctrl.mu.Unlock()
	if flows != 0 {
		t.Fatalf("flows recorded = %d, want none: the refusal must land before the flow is recorded", flows)
	}
}

// TestAuth_LoginStartStartsForANameWithNoDestination is the control for the
// refusal above: same unkeyable state root, but a Codex name that resolves to
// no destination at all (it is hidden - its base URL names an unset variable).
// There is no endpoint to check, so the flow starts, and its capture is empty
// with nothing to compare at completion.
func TestAuth_LoginStartStartsForANameWithNoDestination(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	unkeyableStateRoot(t, stateDir)
	tomlPath := writeProvidersToml(t, dir, codexUnresolvedEndpointToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}

	if ctrl.endpointHasDestination("work") {
		t.Fatal("endpointHasDestination(work) = true, want no destination for the unresolvable fixture")
	}
	if !ctrl.instanceIsCodex("work") {
		t.Fatal("instanceIsCodex(work) = false, want the fixture to pass the Codex gate so the start is refused for the endpoint alone")
	}

	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("LoginStart = %v, want a flow for a name with no destination to check", err)
	}
	if start.FlowID == "" || start.URL == "" {
		t.Fatalf("start = %+v, want a recorded flow and an authorize URL", start)
	}
	ctrl.mu.Lock()
	_, recorded := ctrl.flows[start.FlowID]
	ctrl.mu.Unlock()
	if !recorded {
		t.Fatal("the flow was not recorded")
	}
}

// TestAuth_DeviceStartRefusesACodexDestinationItCannotKey is DeviceStart's half
// of the same rule: the refusal lands before the device-code request, so no
// round trip is spent on a flow whose poll would have nothing to compare.
func TestAuth_DeviceStartRefusesACodexDestinationItCannotKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	unkeyableStateRoot(t, stateDir)
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, codexEndpointToml))
	requestCalls := 0
	ctrl.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		requestCalls++
		return authopenai.DeviceCode{UserCode: "U", VerificationURL: "https://x", DeviceAuthID: "d", Interval: time.Second}, nil
	}

	_, err := ctrl.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "work"})
	assertFlowEndpointRefusal(t, err)
	if requestCalls != 0 {
		t.Fatalf("requestDeviceCode called %d times, want 0: the refusal must land before the device-code request", requestCalls)
	}
	ctrl.mu.Lock()
	flows := len(ctrl.deviceFlows)
	ctrl.mu.Unlock()
	if flows != 0 {
		t.Fatalf("device flows recorded = %d, want none", flows)
	}
}

// TestAuth_LoginCompleteRefusesAnEmptyCaptureThatGainedADestination is the
// completion half of the same rule through the real LoginComplete. The flow
// starts while the name has no destination to name (the entry's base URL is
// unresolvable, so the capture is empty and the start is allowed), and the
// exchange - the browser round trip long step - is where an edit gives the
// name a destination. The empty capture no longer means "nothing to check":
// the record would land on an endpoint the user was never shown.
func TestAuth_LoginCompleteRefusesAnEmptyCaptureThatGainedADestination(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexUnresolvedEndpointToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}
	ctrl.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		renameProvidersEntry(t, ctrl, tomlPath, codexEndpointToml)
		return authopenai.TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			TokenType:    "Bearer",
			Scope:        "openid profile email",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	}

	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("LoginStart: %v", err)
	}
	authorizeURL, err := url.Parse(start.URL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	ctrl.mu.Lock()
	captured := ctrl.flows[start.FlowID].EndpointFingerprint
	ctrl.mu.Unlock()
	if captured != "" {
		t.Fatalf("captured endpoint = %q, want empty: the flow must start with no destination to name", captured)
	}

	_, err = ctrl.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
		Provider:    "work",
		FlowID:      start.FlowID,
		RedirectURL: loginCallbackURL(t, start.URL, authorizeURL.Query().Get("state")),
	})
	assertFlowEndpointRefusal(t, err)
	if _, err := authopenai.LoadAuth(stateDir, "work"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("LoadAuth(work) err = %v, want ErrAuthNotFound: the record must not land on the destination that arrived after the flow started", err)
	}
}

// TestAuth_LoginCompleteAcceptsAnEmptyCaptureWithNoDestination is the
// acceptance control for the refusal above: the same empty capture, but the
// name still resolves to no destination at completion - there is no endpoint
// the user was shown and none to compare, so the record is saved.
func TestAuth_LoginCompleteAcceptsAnEmptyCaptureWithNoDestination(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexUnresolvedEndpointToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}
	ctrl.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		return authopenai.TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			TokenType:    "Bearer",
			Scope:        "openid profile email",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	}

	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("LoginStart: %v", err)
	}
	authorizeURL, err := url.Parse(start.URL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}

	if _, err := ctrl.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
		Provider:    "work",
		FlowID:      start.FlowID,
		RedirectURL: loginCallbackURL(t, start.URL, authorizeURL.Query().Get("state")),
	}); err != nil {
		t.Fatalf("LoginComplete = %v, want the record saved: there is no destination for the empty capture to check", err)
	}
	if _, err := authopenai.LoadAuth(stateDir, "work"); err != nil {
		t.Fatalf("LoadAuth(work) err = %v, want the freshly saved record", err)
	}
}

// TestAuth_FlowStartToleratesAnEmptyCaptureWithoutAStateRoot is the carve-out
// control for the two start guards: a controller with no state root has
// nothing to key with, so a destination is not something it can check - the
// write path accepts an empty assertion there
// (TestInstances_ApiKeySetAcceptsAnEmptyAssertionWithoutAStateRoot) - and both
// starts must run rather than refuse a flow this hub was never able to bind.
// The empty capture is the only thing those flows can carry.
func TestAuth_FlowStartToleratesAnEmptyCaptureWithoutAStateRoot(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexEndpointToml)
	ctrl := newTestAuthController(t, dir, "", tomlPath)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}
	ctrl.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{UserCode: "U", VerificationURL: "https://x", DeviceAuthID: "d", Interval: time.Second}, nil
	}

	// The premise: a real destination, and no state root to key it under.
	if ctrl.stateDir != "" {
		t.Fatalf("stateDir = %q, want the bare controller this control is about", ctrl.stateDir)
	}
	if !ctrl.endpointHasDestination("work") {
		t.Fatal("endpointHasDestination(work) = false, want a real destination")
	}
	if fp := ctrl.endpointFingerprintFor("work"); fp != "" {
		t.Fatalf("endpointFingerprintFor(work) = %q, want empty without a state root", fp)
	}

	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("LoginStart = %v, want a flow: with no state root there is nothing to refuse", err)
	}
	if start.FlowID == "" || start.URL == "" {
		t.Fatalf("start = %+v, want a recorded flow and an authorize URL", start)
	}

	device, err := ctrl.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "work"})
	if err != nil {
		t.Fatalf("DeviceStart = %v, want a flow: with no state root there is nothing to refuse", err)
	}
	if device.FlowID == "" || device.UserCode == "" {
		t.Fatalf("device = %+v, want a recorded device flow and user code", device)
	}
}

// TestAuth_VerifyFlowEndpointEmptyCaptureFollowsTheStateRoot pins the branch
// behind those guards deterministically, without a flow: verifyFlowEndpoint is
// called directly with started == "". A bare controller accepts the empty
// capture - it has nothing to key with, so there is nothing to compare against
// and nothing to refuse. The same name and the same empty capture under a
// controller WITH a state root is the endpoint Conflict: that is the state a
// state root that cannot yield its key leaves behind (the root is made
// unkeyable here so the fixture is literally that state), and it is refused
// because the completion would have no comparison for a destination that is
// real. The branch keys off the root's presence, not whether it can key right
// now, which is why this call is deterministic.
func TestAuth_VerifyFlowEndpointEmptyCaptureFollowsTheStateRoot(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, codexEndpointToml)

	bare := newTestAuthController(t, dir, "", tomlPath)
	if bare.stateDir != "" || !bare.endpointHasDestination("work") {
		t.Fatalf("bare fixture: stateDir = %q, endpointHasDestination = %v, want \"\"/true", bare.stateDir, bare.endpointHasDestination("work"))
	}
	if err := bare.verifyFlowEndpoint("work", ""); err != nil {
		t.Fatalf("verifyFlowEndpoint on a controller with no state root = %v, want nil: there is nothing to key with, so nothing to refuse", err)
	}

	stateDir := t.TempDir()
	unkeyableStateRoot(t, stateDir)
	rooted := newTestAuthController(t, dir, stateDir, tomlPath)
	if rooted.stateDir == "" || rooted.endpointFingerprintFor("work") != "" {
		t.Fatalf("rooted fixture: stateDir = %q, fingerprint = %q, want a root and an empty fingerprint", rooted.stateDir, rooted.endpointFingerprintFor("work"))
	}
	assertFlowEndpointRefusal(t, rooted.verifyFlowEndpoint("work", ""))
}
