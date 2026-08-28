//go:build evenerfuzz

package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
	llmregistry "primeradiant.com/evener/llm/registry"
)

type finalWebspawnLister struct {
	name   string
	models []llmregistry.Model
	err    error
}

func (a finalWebspawnLister) Name() string { return a.name }
func (a finalWebspawnLister) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}
func (a finalWebspawnLister) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("unused")
}
func (a finalWebspawnLister) LiveModels(context.Context) ([]llmregistry.Model, error) {
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
		client := llm.NewClient()
		client.Register(finalWebspawnLister{name: "openrouter-anthropic"})
		client.Register(finalWebspawnLister{name: "broken", err: errors.New("list")})
		client.Register(finalWebspawnLister{name: "openrouter", models: []llmregistry.Model{
			{ID: "unknown-no-tools", Caps: llmregistry.Caps{Tools: new(false)}},
			{ID: "gpt-4o", Caps: llmregistry.Caps{ContextWindow: new(123), Tools: new(true), Reasoning: new(true), EffortValues: []string{"low"}}},
		}})
		client.Register(finalWebspawnLister{name: "custom", models: []llmregistry.Model{
			{ID: "text-embedding-3-small"}, {ID: "whisper-1"}, {ID: "tts-1"},
			{ID: "dall-e-3"}, {ID: "omni-moderation"}, {ID: "audio-model"},
			{ID: "transcribe-model"}, {ID: "image-model"},
			{ID: "gpt-4o"},
			{ID: "claude-fable-5"},
			{ID: "plain", Caps: llmregistry.Caps{ContextWindow: new(7), Tools: new(true), Reasoning: new(true), EffortValues: []string{"medium"}}},
		}})

		oldLoad := liveModelLoadClient
		liveModelLoadClient = func(string) (*llm.Client, error) { return client, nil }
		t.Cleanup(func() { liveModelLoadClient = oldLoad })

		server := NewWebServer(hubcore.WebConfig{})
		models := server.fetchLiveModels(context.Background())
		_ = models
		_ = server.fetchLiveModels(context.Background())
		server.liveModels.expires = time.Time{}
		liveModelLoadClient = func(string) (*llm.Client, error) { return nil, errors.New("load") }
		_ = server.fetchLiveModels(context.Background())

		// The non-Evener source path covers successful and failed model listing.
		registry := appsource.NewRegistry()
		source := &finalWebspawnSource{scriptedAppSource: &scriptedAppSource{id: "remote"}, resp: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "custom", Model: "plain"}},
		}}
		registry.Add(source)
		server.sources = registry
		_, _ = hubModelList(context.Background(), server.cfg, registry, appwire.ModelListParams{Harness: "remote"})
		source.err = errors.New("remote")
		_, _ = hubModelList(context.Background(), server.cfg, registry, appwire.ModelListParams{Harness: "remote"})

		failedLaunch := NewWebServer(hubcore.WebConfig{Spawner: &fakeRPCModelContractSpawner{err: errors.New("launch")}})
		_, _ = hubModelList(context.Background(), failedLaunch.cfg, failedLaunch.sources, appwire.ModelListParams{})

		entries := []appwire.ModelDescriptor{{Provider: "z", Model: "same"}, {Provider: "z", Model: "same"}}
		sortModelDescriptors(entries)
		_ = withDisplayNames([]appwire.ModelDescriptor{{Provider: "anthropic", Model: "claude-fable-5"}})
	})
}
