//go:build serffuzz

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

type finalWebspawnLister struct {
	name   string
	models []llm.ModelInfo
	err    error
}

func (a finalWebspawnLister) Name() string { return a.name }
func (a finalWebspawnLister) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}
func (a finalWebspawnLister) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("unused")
}
func (a finalWebspawnLister) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return a.models, a.err
}

type finalWebspawnSource struct {
	*scriptedAppSource
	resp appwire.ModelListResponse
	err  error
}

func (s *finalWebspawnSource) ListModels(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
	return s.resp, s.err
}

func FuzzFinalWebspawn(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		t.Setenv("SERF_MODEL", " final-model ")
		t.Setenv("SERF_REASONING_EFFORT", " high ")
		_ = safeSpawnEnv()

		client := llm.NewClient()
		client.Register(finalWebspawnLister{name: "openrouter-anthropic"})
		client.Register(finalWebspawnLister{name: "broken", err: errors.New("list")})
		client.Register(finalWebspawnLister{name: "openrouter", models: []llm.ModelInfo{
			{ID: "unknown-no-tools"},
			{ID: "gpt-4o", ContextWindow: 123, SupportsTools: true, SupportsReasoning: true, ReasoningEffortLevels: []string{"low"}},
		}})
		client.Register(finalWebspawnLister{name: "custom", models: []llm.ModelInfo{
			{ID: "text-embedding-3-small"}, {ID: "whisper-1"}, {ID: "tts-1"},
			{ID: "dall-e-3"}, {ID: "omni-moderation"}, {ID: "audio-model"},
			{ID: "transcribe-model"}, {ID: "image-model"},
			{ID: "gpt-4o"},
			{ID: "claude-fable-5"},
			{ID: "plain", ContextWindow: 7, SupportsTools: true, SupportsReasoning: true, ReasoningEffortLevels: []string{"medium"}},
		}})

		oldLoad := webSpawnLoadClient
		webSpawnLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
			return client, providercfg.Config{}, true, nil
		}
		t.Cleanup(func() { webSpawnLoadClient = oldLoad })

		reasoning := false
		cfg := hubcore.WebConfig{ProviderConfig: &providercfg.Config{Instances: []providercfg.InstanceConfig{{
			Name: "custom", Models: map[string]providercfg.ModelConfig{"plain": {Reasoning: &reasoning, ContextWindow: 99}},
		}}}}
		server := NewWebServer(cfg)
		models := server.fetchLiveModels(context.Background())
		_ = models
		_ = server.fetchLiveModels(context.Background())
		server.liveModels.expires = time.Time{}
		webSpawnLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
			return nil, providercfg.Config{}, false, errors.New("load")
		}
		_ = server.fetchLiveModels(context.Background())

		// The non-Serf source path covers successful and failed model listing.
		registry := appsource.NewRegistry()
		source := &finalWebspawnSource{scriptedAppSource: &scriptedAppSource{id: "remote"}, resp: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "custom", Model: "plain"}},
		}}
		registry.Add(source)
		server.sources = registry
		for _, rawURL := range []string{"/api/models?harness=remote", "/api/models?harness=remote&diagnostics=1"} {
			r := httptest.NewRequest(http.MethodGet, rawURL, nil)
			server.handleApiModels(httptest.NewRecorder(), r)
		}
		source.err = errors.New("remote")
		server.handleApiModels(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/models?harness=remote", nil))

		failedLaunch := NewWebServer(hubcore.WebConfig{Spawner: &fakeRPCModelContractSpawner{err: errors.New("launch")}})
		failedLaunch.handleApiModels(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/models", nil))

		entries := []map[string]any{{"provider": "z", "model": "same"}, {"provider": "z", "model": "same"}}
		sortModelEntriesDatedLast(entries)
		_ = modelDescriptorsToAPIModels([]appwire.ModelDescriptor{{Provider: "anthropic", Model: "claude-fable-5"}}, nil)
		_ = catalogModelInfo(llm.EmbeddedModelCatalog(), "ollama", "absent")
		_ = catalogModelInfo(llm.EmbeddedModelCatalog(), "", "definitely-absent")
	})
}
