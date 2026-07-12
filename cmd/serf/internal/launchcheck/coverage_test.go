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
