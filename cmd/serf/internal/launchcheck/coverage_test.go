package launchcheck

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

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
		withLaunchCheckLoadClient(t, func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
			return nil, providercfg.Config{}, false, errors.New("unavailable")
		})
		var stdout bytes.Buffer
		if err := RunLaunchCheck([]string{"--model", "openrouter/free"}, &stdout, io.Discard); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout.String(), "provider=openrouter model=free") {
			t.Fatalf("stdout=%q", stdout.String())
		}
	})
	t.Run("json writer error", func(t *testing.T) {
		if err := RunLaunchCheck([]string{"--json"}, errorWriter{}, io.Discard); err == nil {
			t.Fatal("expected writer error")
		}
	})
}

func TestLaunchCheckLoaderErrors(t *testing.T) {
	t.Run("profile config", func(t *testing.T) {
		old := launchCheckLoadConfig
		launchCheckLoadConfig = func() (providercfg.Config, bool, error) {
			return providercfg.Config{}, false, errors.New("config failed")
		}
		t.Cleanup(func() { launchCheckLoadConfig = old })
		if err := RunLaunchCheck([]string{"--model", "openai/gpt"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "config failed") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("model config", func(t *testing.T) {
		old := launchCheckLoadProviderConfig
		launchCheckLoadProviderConfig = func(...llm.EnvOption) (providercfg.Config, bool, error) {
			return providercfg.Config{}, false, errors.New("providers failed")
		}
		t.Cleanup(func() { launchCheckLoadProviderConfig = old })
		if err := RunLaunchCheck([]string{"--models"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "providers failed") {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestLaunchCheckModelsSkipsAndSorts(t *testing.T) {
	old := launchCheckLoadProviderConfig
	launchCheckLoadProviderConfig = func(...llm.EnvOption) (providercfg.Config, bool, error) {
		return providercfg.Config{Instances: []providercfg.InstanceConfig{
			{Name: "   ", Type: "openai"},
			{Name: "skip", Type: "openrouter-anthropic"},
			{Name: "fake", Type: "test-launchcheck"},
		}}, true, nil
	}
	t.Cleanup(func() { launchCheckLoadProviderConfig = old })

	llm.RegisterInstanceAdapterFactory("test-launchcheck", "", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return &launchCheckFakeAdapter{name: inst.Name, models: []llm.ModelInfo{
			{ID: "z-chat"}, {ID: "text-embedding-3-small"}, {ID: "a-chat"},
		}}, nil
	})
	models, diagnostics, err := launchCheckModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	if got, want := models, []launchCheckModel{{Provider: "fake", Model: "a-chat"}, {Provider: "fake", Model: "z-chat"}}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("models=%+v, want %+v", got, want)
	}
}

func TestValidateLaunchCheckModelRemainingBranches(t *testing.T) {
	t.Run("provider not configured", func(t *testing.T) {
		client := llm.NewClient()
		client.Register(&launchCheckFakeAdapter{name: "other"})
		withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))
		if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "missing", Model: "m"}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("definite listing error", func(t *testing.T) {
		client := llm.NewClient()
		client.Register(&launchCheckFakeAdapter{name: "fake", err: errors.New("invalid response")})
		withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))
		err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "fake", Model: "m"})
		if err == nil || !strings.Contains(err.Error(), "validate model") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("visible model", func(t *testing.T) {
		client := llm.NewClient()
		client.Register(&launchCheckFakeAdapter{name: "fake", models: []llm.ModelInfo{{ID: "m"}}})
		withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))
		if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "fake", Model: "m"}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestLaunchCheckPureHelpersRemainingBranches(t *testing.T) {
	if launchCheckModelListUnavailable(nil) {
		t.Fatal("nil error reported unavailable")
	}
	for _, id := range []string{"embedding", "whisper", "tts", "dall-e", "moderation", "audio", "transcribe", "image"} {
		if launchCheckModelVisible("other", id, nil) {
			t.Fatalf("media model %q visible", id)
		}
	}
	if launchCheckCatalogModelInfo(nil, "anything") != nil {
		t.Fatal("nil catalog returned model info")
	}
}

func FuzzLaunchCheckClassifiers(f *testing.F) {
	f.Add("openrouter", "text-embedding-3-small", "http 403")
	f.Add("openai", "gpt-5", "connection refused")
	f.Fuzz(func(t *testing.T, tag, modelID, message string) {
		_ = launchCheckModelVisible(tag, modelID, llm.EmbeddedModelCatalog())
		_ = launchCheckModelListUnavailable(errors.New(message))
	})
}

func FuzzLaunchCheckProgram(f *testing.F) {
	llm.RegisterInstanceAdapterFactory("fuzz-launchcheck", "", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		if inst.BaseURL == "factory-error" {
			return nil, errors.New("factory failed")
		}
		return &launchCheckFakeAdapter{
			name: inst.Name,
			models: []llm.ModelInfo{
				{ID: "z-chat"},
				{ID: "text-embedding-3-small"},
				{ID: "a-chat"},
			},
			err: errorForString(inst.BaseURL),
		}, nil
	})
	for scenario := range uint8(27) {
		f.Add(scenario, "credential-value-123")
	}
	f.Fuzz(func(t *testing.T, scenario uint8, value string) {
		withLaunchCheckHooks(t)
		switch scenario % 27 {
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
			launchCheckLoadConfig = func() (providercfg.Config, bool, error) {
				return providercfg.Config{}, false, errors.New("config failed")
			}
			if err := validateLaunchCheckProfile(cmdutil.ModelRef{Provider: "openai", Model: "gpt"}); err == nil {
				t.Fatal("config failure ignored")
			}
		case 6:
			launchCheckLoadConfig = func() (providercfg.Config, bool, error) {
				return providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "custom", Type: "openai"}}}, true, nil
			}
			if err := validateLaunchCheckProfile(cmdutil.ModelRef{Provider: "custom", Model: "gpt"}); err != nil {
				t.Fatal(err)
			}
		case 7:
			launchCheckLoadProviderConfig = func(...llm.EnvOption) (providercfg.Config, bool, error) {
				return providercfg.Config{}, false, errors.New("providers failed")
			}
			if _, _, err := launchCheckModels(); err == nil {
				t.Fatal("provider config failure ignored")
			}
		case 8:
			launchCheckLoadProviderConfig = fuzzProviderConfig(
				providercfg.InstanceConfig{Name: " "},
				providercfg.InstanceConfig{Name: "skip", Type: "openrouter-anthropic"},
				providercfg.InstanceConfig{Name: "bad", Type: "fuzz-launchcheck", BaseURL: "factory-error"},
				providercfg.InstanceConfig{Name: "good", Type: "fuzz-launchcheck"},
			)
			models, diagnostics, err := launchCheckModels()
			if err != nil || len(models) != 2 || len(diagnostics) != 1 {
				t.Fatalf("models=%+v diagnostics=%+v err=%v", models, diagnostics, err)
			}
		case 9:
			launchCheckLoadProviderConfig = fuzzProviderConfig(providercfg.InstanceConfig{Name: "bad", Type: "fuzz-launchcheck", BaseURL: "list-error"})
			_, diagnostics, err := launchCheckModels()
			if err != nil || len(diagnostics) != 1 {
				t.Fatalf("diagnostics=%+v err=%v", diagnostics, err)
			}
		case 10:
			launchCheckLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
				return nil, providercfg.Config{}, false, errors.New("unavailable")
			}
			if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "p", Model: "m"}); err != nil {
				t.Fatal(err)
			}
		case 11:
			client := llm.NewClient()
			client.Register(&launchCheckFakeAdapter{name: "other"})
			launchCheckLoadClient = fixedLaunchCheckClient(client)
			if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "missing", Model: "m"}); err != nil {
				t.Fatal(err)
			}
		case 12:
			client := fuzzLaunchCheckClient(errors.New("HTTP 403"))
			launchCheckLoadClient = fixedLaunchCheckClient(client)
			if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "fake", Model: "m"}); err != nil {
				t.Fatal(err)
			}
		case 13:
			client := fuzzLaunchCheckClient(errors.New("invalid response"))
			launchCheckLoadClient = fixedLaunchCheckClient(client)
			if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "fake", Model: "m"}); err == nil {
				t.Fatal("definite listing error ignored")
			}
		case 14:
			client := fuzzLaunchCheckClient(nil, llm.ModelInfo{ID: "m"})
			launchCheckLoadClient = fixedLaunchCheckClient(client)
			if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "fake", Model: "m"}); err != nil {
				t.Fatal(err)
			}
		case 15:
			client := fuzzLaunchCheckClient(nil, llm.ModelInfo{ID: "other"})
			launchCheckLoadClient = fixedLaunchCheckClient(client)
			if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "fake", Model: "m"}); err == nil {
				t.Fatal("missing model accepted")
			}
		case 16:
			for _, err := range []error{nil, context.DeadlineExceeded, launchCheckTimeoutError{}, errors.New("no such host")} {
				_ = launchCheckModelListUnavailable(err)
			}
		case 17:
			for _, id := range []string{"embedding", "whisper", "tts", "dall-e", "moderation", "audio", "transcribe", "image"} {
				if launchCheckModelVisible("other", id, nil) {
					t.Fatalf("media model %q visible", id)
				}
			}
		case 18:
			_ = launchCheckModelVisible("openrouter", "not-in-catalog", llm.EmbeddedModelCatalog())
			_ = launchCheckModelVisible("openrouter", "gpt-5", llm.EmbeddedModelCatalog())
			_ = launchCheckCatalogModelInfo(nil, "model")
		case 19:
			if len(value) < 8 {
				value = "credential-value-123"
			}
			t.Setenv("FUZZ_LAUNCH_TOKEN", value)
			got := redactLaunchCheckDiagnostic("rejected " + value)
			if strings.Contains(got, value) {
				t.Fatalf("secret not redacted: %q", got)
			}
		case 20:
			launchCheckLoadConfig = func() (providercfg.Config, bool, error) { return providercfg.Config{}, false, nil }
			if err := validateLaunchCheckProfile(cmdutil.ModelRef{Provider: "openrouter", Model: "free"}); err != nil {
				t.Fatal(err)
			}
		case 21:
			launchCheckLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
				return nil, providercfg.Config{}, false, errors.New("unavailable")
			}
			var out bytes.Buffer
			if err := RunLaunchCheck([]string{"--model", "openrouter/free"}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "provider=openrouter") {
				t.Fatalf("output=%q err=%v", out.String(), err)
			}
		case 22:
			launchCheckLoadProviderConfig = fuzzProviderConfig(providercfg.InstanceConfig{Name: "good", Type: "fuzz-launchcheck"})
			var out bytes.Buffer
			if err := RunLaunchCheck([]string{"--models", "--json"}, &out, io.Discard); err != nil || out.Len() == 0 {
				t.Fatalf("output=%q err=%v", out.String(), err)
			}
		case 23:
			if _, _, err := launchCheckLoadConfig(); err != nil {
				t.Fatal(err)
			}
		case 24:
			launchCheckLoadProviderConfig = func(...llm.EnvOption) (providercfg.Config, bool, error) {
				return providercfg.Config{}, false, errors.New("providers failed")
			}
			if err := RunLaunchCheck([]string{"--models"}, io.Discard, io.Discard); err == nil {
				t.Fatal("models config failure ignored")
			}
		case 25:
			launchCheckLoadConfig = func() (providercfg.Config, bool, error) {
				return providercfg.Config{}, false, errors.New("config failed")
			}
			if err := RunLaunchCheck([]string{"--model", "openai/gpt"}, io.Discard, io.Discard); err == nil {
				t.Fatal("profile validation failure ignored")
			}
		case 26:
			client := llm.NewClient()
			client.Register(&launchCheckFakeAdapter{name: "openrouter", models: []llm.ModelInfo{{ID: "other"}}})
			launchCheckLoadClient = fixedLaunchCheckClient(client)
			if err := RunLaunchCheck([]string{"--model", "openrouter/missing"}, io.Discard, io.Discard); err == nil {
				t.Fatal("model validation failure ignored")
			}
		}
	})
}

func withLaunchCheckHooks(t *testing.T) {
	t.Helper()
	oldClient := launchCheckLoadClient
	oldProviderConfig := launchCheckLoadProviderConfig
	oldConfig := launchCheckLoadConfig
	t.Cleanup(func() {
		launchCheckLoadClient = oldClient
		launchCheckLoadProviderConfig = oldProviderConfig
		launchCheckLoadConfig = oldConfig
	})
}

func fuzzProviderConfig(instances ...providercfg.InstanceConfig) func(...llm.EnvOption) (providercfg.Config, bool, error) {
	return func(...llm.EnvOption) (providercfg.Config, bool, error) {
		return providercfg.Config{Instances: instances}, true, nil
	}
}

func fuzzLaunchCheckClient(err error, models ...llm.ModelInfo) *llm.Client {
	client := llm.NewClient()
	client.Register(&launchCheckFakeAdapter{name: "fake", err: err, models: models})
	return client
}

func errorForString(value string) error {
	if value == "list-error" {
		return errors.New(value)
	}
	return nil
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

type launchCheckFakeAdapter struct {
	name   string
	models []llm.ModelInfo
	err    error
}

func (a *launchCheckFakeAdapter) Name() string { return a.name }
func (a *launchCheckFakeAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("unused")
}
func (a *launchCheckFakeAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("unused")
}
func (a *launchCheckFakeAdapter) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return a.models, a.err
}

func fixedLaunchCheckClient(client *llm.Client) func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
	return func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
		return client, providercfg.Config{}, true, nil
	}
}

func withLaunchCheckLoadClient(t *testing.T, load func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error)) {
	t.Helper()
	old := launchCheckLoadClient
	launchCheckLoadClient = load
	t.Cleanup(func() { launchCheckLoadClient = old })
}
