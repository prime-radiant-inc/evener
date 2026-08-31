package launchcheck

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// launchCheckClient builds a client on a hermetic registry carrying
// instances, with the supplied adapters registered as overrides.
// launchCheckEnv is the whole environment these fixtures resolve against —
// WithEnv is the only source, so an ambient OLLAMA_HOST cannot reach them.
// The curated ollama instance is implicit and needs no variable to resolve,
// so an env-less fixture still probes whatever answers on localhost: green on
// a developer machine running ollama, red on CI. Pinning the host to a dead
// port makes the refusal the same everywhere.
var launchCheckEnv = map[string]string{"OLLAMA_HOST": "127.0.0.1:1"}

func launchCheckClient(t *testing.T, instances map[string]registry.Provider, adapters ...llm.ProviderAdapter) *llm.Client {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(name string) (string, bool) { v, ok := launchCheckEnv[name]; return v, ok }),
		registry.WithInstances(instances),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	c := llm.NewClient(llm.WithRegistry(r))
	for _, a := range adapters {
		c.Register(a)
	}
	return c
}

// gatewayInstances is one visible custom instance named "fake".
func gatewayInstances() map[string]registry.Provider {
	return map[string]registry.Provider{
		"fake": {Base: "openai-compatible", APIKey: "k", Transport: registry.Transport{BaseURL: "http://fake.invalid/v1"}},
	}
}

func withLaunchCheckLoadClient(t *testing.T, load func(string) (*llm.Client, error)) {
	t.Helper()
	old := launchCheckLoadClient
	launchCheckLoadClient = load
	t.Cleanup(func() { launchCheckLoadClient = old })
}

func fixedLaunchCheckClient(client *llm.Client) func(string) (*llm.Client, error) {
	return func(string) (*llm.Client, error) { return client, nil }
}

func TestRunLaunchCheckRemainingBranches(t *testing.T) {
	t.Run("flag parse error", func(t *testing.T) {
		if err := RunLaunchCheck([]string{"--unknown"}, io.Discard, io.Discard); err == nil {
			t.Fatal("expected flag error")
		}
	})
	t.Run("model parse error", func(t *testing.T) {
		if err := RunLaunchCheck([]string{"--model", "invalid"}, io.Discard, io.Discard); err == nil {
			t.Fatal("expected model-ref error")
		}
	})
	t.Run("plain protocol output", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := RunLaunchCheck(nil, &stdout, io.Discard); err != nil {
			t.Fatal(err)
		}
		if got, want := stdout.String(), "ok protocol="+appwire.ProtocolVersion+"\n"; got != want {
			t.Fatalf("stdout=%q, want %q", got, want)
		}
	})
	t.Run("plain model output", func(t *testing.T) {
		client := launchCheckClient(t, gatewayInstances(),
			&launchCheckFakeAdapter{name: "fake", models: []registry.Model{{ID: "free"}}})
		withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))
		var stdout bytes.Buffer
		if err := RunLaunchCheck([]string{"--model", "fake/free"}, &stdout, io.Discard); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout.String(), "provider=fake model=free") {
			t.Fatalf("stdout=%q", stdout.String())
		}
	})
	t.Run("json writer error", func(t *testing.T) {
		if err := RunLaunchCheck([]string{"--json"}, errorWriter{}, io.Discard); err == nil {
			t.Fatal("expected writer error")
		}
	})
}

// A client that will not load is fatal to both validation paths: without a
// registry there is nothing to validate against.
func TestLaunchCheckLoaderErrors(t *testing.T) {
	withLaunchCheckLoadClient(t, func(string) (*llm.Client, error) {
		return nil, errors.New("registry failed")
	})
	if err := RunLaunchCheck([]string{"--model", "openai/gpt-5.5"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "registry failed") {
		t.Fatalf("profile validation error=%v", err)
	}
	if err := RunLaunchCheck([]string{"--models"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "registry failed") {
		t.Fatalf("model listing error=%v", err)
	}
}

// launchCheckModels lists every visible instance and reports one diagnostic
// per instance that could not be listed.
func TestLaunchCheckModelsListsVisibleInstances(t *testing.T) {
	client := launchCheckClient(t, map[string]registry.Provider{
		"fake": {Base: "openai-compatible", APIKey: "k", Transport: registry.Transport{BaseURL: "http://fake.invalid/v1"}},
		"bad":  {Base: "openai-compatible", APIKey: "k", Transport: registry.Transport{BaseURL: "http://bad.invalid/v1"}},
	},
		&launchCheckFakeAdapter{name: "fake", models: []registry.Model{{ID: "z-chat"}, {ID: "a-chat"}}},
		&launchCheckFakeAdapter{name: "bad", err: errors.New("listing refused")},
	)
	withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))

	models, diagnostics, err := launchCheckModels()
	if err != nil {
		t.Fatal(err)
	}
	// Two diagnostics, one per instance that could not be listed: the fixture's
	// own "bad", and the curated implicit ollama, which is visible here because
	// its localhost default resolves without any variable and which
	// launchCheckEnv pins to a dead port so the refusal is the same on every
	// machine. Asserted by name rather than by index — launchCheckModels walks
	// the registry's instances, whose order is not this test's to fix.
	byProvider := map[string]bool{}
	for _, d := range diagnostics {
		byProvider[d.Provider] = true
	}
	if len(diagnostics) != 2 || !byProvider["bad"] || !byProvider["ollama"] {
		t.Fatalf("diagnostics=%+v, want one each for bad and ollama", diagnostics)
	}
	got := map[string]bool{}
	for _, m := range models {
		if m.Provider == "fake" {
			got[m.Model] = true
		}
	}
	if !got["a-chat"] || !got["z-chat"] {
		t.Fatalf("models=%+v, want both fake rows", models)
	}
}

func TestValidateLaunchCheckModelRemainingBranches(t *testing.T) {
	t.Run("client unavailable passes", func(t *testing.T) {
		withLaunchCheckLoadClient(t, func(string) (*llm.Client, error) { return nil, errors.New("unavailable") })
		if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "fake", Model: "m"}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unservable ref reports the resolver error", func(t *testing.T) {
		client := launchCheckClient(t, gatewayInstances())
		withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))
		err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "openai-codex", Model: "not-on-the-allowlist"})
		if err == nil || !strings.Contains(err.Error(), "Codex transport") {
			t.Fatalf("error=%v, want the Codex allowlist message", err)
		}
	})
	t.Run("listing failure passes", func(t *testing.T) {
		client := launchCheckClient(t, gatewayInstances(),
			&launchCheckFakeAdapter{name: "fake", err: errors.New("invalid response")})
		withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))
		if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "fake", Model: "m"}); err != nil {
			t.Fatalf("a listing the instance could not fetch is not evidence of absence: %v", err)
		}
	})
	t.Run("live listing without the model fails", func(t *testing.T) {
		client := launchCheckClient(t, gatewayInstances(),
			&launchCheckFakeAdapter{name: "fake", models: []registry.Model{{ID: "other"}}})
		withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))
		err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "fake", Model: "m"})
		if err == nil || !strings.Contains(err.Error(), "not available") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("live listing with the model passes", func(t *testing.T) {
		client := launchCheckClient(t, gatewayInstances(),
			&launchCheckFakeAdapter{name: "fake", models: []registry.Model{{ID: "m"}}})
		withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))
		if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "fake", Model: "m"}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRedactLaunchCheckDiagnostic(t *testing.T) {
	const secret = "credential-value-123"
	t.Setenv("LAUNCH_CHECK_TOKEN", secret)
	if got := redactLaunchCheckDiagnostic("rejected " + secret); strings.Contains(got, secret) {
		t.Fatalf("secret not redacted: %q", got)
	}
	// A short value is not redacted: it would swallow unrelated text.
	t.Setenv("LAUNCH_CHECK_TOKEN", "abc")
	if got := redactLaunchCheckDiagnostic("abc"); got != "abc" {
		t.Fatalf("short value redacted: %q", got)
	}
}

func FuzzLaunchCheckProgram(f *testing.F) {
	for scenario := range uint8(11) {
		f.Add(scenario, "credential-value-123")
	}
	f.Fuzz(func(t *testing.T, scenario uint8, value string) {
		switch scenario % 11 {
		case 0:
			if err := RunLaunchCheck([]string{"--unknown"}, io.Discard, io.Discard); err == nil {
				t.Fatal("unknown flag accepted")
			}
		case 1:
			if err := RunLaunchCheck([]string{"--protocol", "old"}, io.Discard, io.Discard); err == nil {
				t.Fatal("unsupported protocol accepted")
			}
		case 2:
			var out bytes.Buffer
			if err := RunLaunchCheck(nil, &out, io.Discard); err != nil || out.Len() == 0 {
				t.Fatalf("plain launch check: output=%q err=%v", out.String(), err)
			}
		case 3:
			if err := RunLaunchCheck([]string{"--json"}, errorWriter{}, io.Discard); err == nil {
				t.Fatal("JSON writer failure ignored")
			}
		case 4:
			if err := RunLaunchCheck([]string{"--model", "invalid"}, io.Discard, io.Discard); err == nil {
				t.Fatal("invalid model accepted")
			}
		case 5:
			withLaunchCheckLoadClient(t, func(string) (*llm.Client, error) { return nil, errors.New("unavailable") })
			if err := validateLaunchCheckProfile(cmdutil.ModelRef{Provider: "openai", Model: "gpt-5.5"}); err == nil {
				t.Fatal("client failure ignored")
			}
			if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "p", Model: "m"}); err != nil {
				t.Fatal(err)
			}
		case 6:
			client := launchCheckClient(t, gatewayInstances())
			withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))
			if err := validateLaunchCheckProfile(cmdutil.ModelRef{Provider: "fake", Model: "anything"}); err != nil {
				t.Fatal(err)
			}
			if err := validateLaunchCheckProfile(cmdutil.ModelRef{Provider: "nope", Model: "m"}); err == nil {
				t.Fatal("unknown instance accepted")
			}
		case 7:
			client := launchCheckClient(t, gatewayInstances(),
				&launchCheckFakeAdapter{name: "fake", models: []registry.Model{{ID: "m"}}})
			withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))
			var out bytes.Buffer
			if err := RunLaunchCheck([]string{"--models", "--json"}, &out, io.Discard); err != nil || out.Len() == 0 {
				t.Fatalf("output=%q err=%v", out.String(), err)
			}
		case 8:
			client := launchCheckClient(t, gatewayInstances(),
				&launchCheckFakeAdapter{name: "fake", models: []registry.Model{{ID: "other"}}})
			withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))
			if err := RunLaunchCheck([]string{"--model", "fake/missing"}, io.Discard, io.Discard); err == nil {
				t.Fatal("model validation failure ignored")
			}
		case 9:
			if len(value) < 8 {
				value = "credential-value-123"
			}
			t.Setenv("FUZZ_LAUNCH_TOKEN", value)
			if got := redactLaunchCheckDiagnostic("rejected " + value); strings.Contains(got, value) {
				t.Fatalf("secret not redacted: %q", got)
			}
		case 10:
			for _, key := range []string{"API_KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "PLAIN"} {
				_ = launchCheckSensitiveEnvKey(key)
			}
		}
	})
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

type launchCheckFakeAdapter struct {
	name   string
	models []registry.Model
	err    error
}

func (a *launchCheckFakeAdapter) Name() string { return a.name }
func (a *launchCheckFakeAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("unused")
}
func (a *launchCheckFakeAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("unused")
}
func (a *launchCheckFakeAdapter) LiveModels(context.Context) ([]registry.Model, error) {
	return a.models, a.err
}
