package main

// How an instance authenticates is decided by its behavior tag —
// providercfg.BehaviorTag, "the internal behavior identity every
// provider-conditional behavior keys on" — and not by the raw type it was
// declared with. An openai instance carrying api_style = "chat-completions" is
// behaviorally an openai-compatible endpoint: its key is
// OPENAI_COMPATIBLE_API_KEY and it has no OAuth at all. The spawn preflight has
// keyed on the tag since kata nrv4; these tests hold the auth-status and
// OAuth-entry paths to the same rule, so launch and the credentials pane cannot
// hold opposite beliefs about one instance (kata jd5s).

import (
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/auth/openai/oaitest"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm/providercfg"
)

// localCompatProvidersToml is the config jd5s measured: one llama.cpp-shaped
// endpoint declared with type "openai" and the chat-completions api_style, so
// its behavior tag is "openai-compatible".
const localCompatProvidersToml = `schema = 1

[instances.local]
type = "openai"
api_style = "chat-completions"
base_url = "http://127.0.0.1:8080/v1"
`

// newCompatAuthController builds an isolated controller over
// localCompatProvidersToml with no OpenAI-compatible key in the environment.
// Callers that want one set it themselves.
func newCompatAuthController(t *testing.T) *hubAuthController {
	t.Helper()
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv(envvars.OpenAICompatibleAPIKey.Name, "")
	dir := t.TempDir()
	return newTestAuthController(t, dir, t.TempDir(), writeProvidersToml(t, dir, localCompatProvidersToml))
}

// TestAuthStatus_AuthModesFollowBehaviorTag walks every api_style an openai
// instance may declare and requires the reported auth modes to be the registry
// row for that instance's behavior tag. Advertising "oauth" for a
// chat-completions endpoint puts a "Sign in with ChatGPT" affordance on a local
// llama.cpp server (InstanceRow.tsx keys that button on authModes).
func TestAuthStatus_AuthModesFollowBehaviorTag(t *testing.T) {
	// The api_style values providercfg.ValidateAPIStyle accepts, plus unset.
	for _, tt := range []struct {
		name  string
		style providercfg.APIStyle
	}{
		{"unset", ""},
		{"responses", providercfg.StyleResponses},
		{"chat-completions", providercfg.StyleChatCompletions},
		{"auto", providercfg.StyleAuto},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oaitest.IsolateOpenAIAuth(t)
			dir := t.TempDir()
			cfgPath := writeProvidersConfig(t, dir, providercfg.Config{
				Instances: []providercfg.InstanceConfig{{
					Name:     "local",
					Type:     "openai",
					APIStyle: tt.style,
					BaseURL:  "http://127.0.0.1:8080/v1",
				}},
			})
			c := newTestAuthController(t, dir, t.TempDir(), cfgPath)

			got, err := c.Status(appwire.AuthStatusParams{Provider: "local"})
			if err != nil {
				t.Fatalf("Status(local) with api_style %q: %v", tt.style, err)
			}
			tag := providercfg.BehaviorTag("openai", string(tt.style))
			want := envvars.AuthModes(tag)
			if !reflect.DeepEqual(got.AuthModes, want) {
				t.Errorf("Status(local).AuthModes = %v for api_style %q, want the registry row for behavior tag %q: %v",
					got.AuthModes, tt.style, tag, want)
			}
		})
	}
}

// TestAuthStatus_ChatCompletionsInstanceReportsResolvedCredential requires the
// credential Status reports to be the one the launch path actually resolves.
// credentials.Store.ResolveKey keyed by the behavior tag is what spawn and the
// adapter use, so a status computed any other way tells the credentials pane a
// different story than the one launch acts on.
func TestAuthStatus_ChatCompletionsInstanceReportsResolvedCredential(t *testing.T) {
	for _, tt := range []struct {
		name      string
		compatKey string
	}{
		{"key in the environment", "sk-compat-probe"},
		{"no key anywhere", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newCompatAuthController(t)
			t.Setenv(envvars.OpenAICompatibleAPIKey.Name, tt.compatKey)

			wantValue, wantSource := c.creds.ResolveKey("local", "openai-compatible")
			_, wantEnvVar := c.creds.InstanceLayers("local", "openai-compatible")

			got, err := c.Status(appwire.AuthStatusParams{Provider: "local"})
			if err != nil {
				t.Fatalf("Status(local): %v", err)
			}
			if got.SignedIn != (wantValue != "") {
				t.Errorf("Status(local).SignedIn = %v, want %v: ResolveKey(local, openai-compatible) resolved %q from %q",
					got.SignedIn, wantValue != "", wantValue, wantSource)
			}
			if got.ActiveSource != string(wantSource) {
				t.Errorf("Status(local).ActiveSource = %q, want %q from ResolveKey", got.ActiveSource, wantSource)
			}
			if got.EnvVar != wantEnvVar {
				t.Errorf("Status(local).EnvVar = %q, want %q", got.EnvVar, wantEnvVar)
			}
		})
	}
}

// TestAuthLoginStart_RefusesChatCompletionsInstance holds the OAuth entry point
// to the same tag. The hub's login flow is OpenAI's: it hands back a
// chatgpt.com authorize URL. Starting it for a local endpoint that has no OAuth
// sends the user somewhere that can never authenticate this instance, so the
// honest answer is the refusal the hub already gives every other non-OAuth
// instance.
func TestAuthLoginStart_RefusesChatCompletionsInstance(t *testing.T) {
	c := newCompatAuthController(t)

	got, err := c.LoginStart(appwire.AuthLoginStartParams{Provider: "local"})
	if err == nil {
		t.Fatalf("LoginStart(local) = %+v with no error; an openai-compatible endpoint has no OAuth to start", got)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Errorf("LoginStart(local) error = %v, want an InvalidParams wire error", err)
	}
	if got.URL != "" {
		t.Errorf("LoginStart(local).URL = %q, want no URL at all", got.URL)
	}
}

// TestAuthLogout_ChatCompletionsInstanceReportsCompatibleStatus covers the other
// mutation the tag decides. Logout branches on the same predicate as LoginStart:
// an OAuth instance has its stored record deleted, everything else has its key
// cleared. Either branch removes the stored key here, so the status the call
// returns is what distinguishes them.
func TestAuthLogout_ChatCompletionsInstanceReportsCompatibleStatus(t *testing.T) {
	c := newCompatAuthController(t)
	if err := c.creds.Set("local", "sk-local"); err != nil {
		t.Fatalf("Set(local): %v", err)
	}

	got, err := c.Logout(appwire.AuthLogoutParams{Provider: "local"})
	if err != nil {
		t.Fatalf("Logout(local): %v", err)
	}
	if !got.Removed {
		t.Errorf("Logout(local).Removed = false, want the stored key removed")
	}
	want := envvars.AuthModes("openai-compatible")
	if !reflect.DeepEqual(got.Status.AuthModes, want) {
		t.Errorf("Logout(local).Status.AuthModes = %v, want %v", got.Status.AuthModes, want)
	}
	if got.Status.ActiveSource != string(credentials.SourceAbsent) {
		t.Errorf("Logout(local).Status.ActiveSource = %q, want %q", got.Status.ActiveSource, credentials.SourceAbsent)
	}
	if got.Status.HasStoredFile {
		t.Errorf("Logout(local).Status.HasStoredFile = true after clearing the key")
	}
}

// TestInstanceList_ChatCompletionsInstanceReportsCompatibleCredential covers the
// settings surface. hubInstancesController.List enriches each row with
// instanceStatus, so it is the second call site that must pass the behavior tag
// rather than the configured type — the credentials pane renders exactly these
// fields.
func TestInstanceList_ChatCompletionsInstanceReportsCompatibleCredential(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv(envvars.OpenAICompatibleAPIKey.Name, "sk-compat-probe")
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, localCompatProvidersToml)
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir())

	resp := ctl.List()
	var got *appwire.InstanceEntry
	for i := range resp.Instances {
		if resp.Instances[i].Name == "local" {
			got = &resp.Instances[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("List() did not include instance %q: %+v", "local", resp.Instances)
	}
	want := envvars.AuthModes("openai-compatible")
	if !reflect.DeepEqual(got.AuthModes, want) {
		t.Errorf("List() entry for local: AuthModes = %v, want %v", got.AuthModes, want)
	}
	if got.ActiveSource != string(credentials.SourceEnv) {
		t.Errorf("List() entry for local: ActiveSource = %q, want %q", got.ActiveSource, credentials.SourceEnv)
	}
	if got.EnvVar != envvars.OpenAICompatibleAPIKey.Name {
		t.Errorf("List() entry for local: EnvVar = %q, want %q", got.EnvVar, envvars.OpenAICompatibleAPIKey.Name)
	}
}
