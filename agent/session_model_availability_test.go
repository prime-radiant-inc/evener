package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/modelavailability"
	"primeradiant.com/evener/llm"
)

type modelAvailabilityAdapter struct {
	fakeAdapter
	models  []llm.ModelInfo
	calls   atomic.Int32
	observe func(context.Context)
	listErr error
}

func (a *modelAvailabilityAdapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	a.calls.Add(1)
	if a.observe != nil {
		a.observe(ctx)
	}
	if a.listErr != nil {
		return nil, a.listErr
	}
	return append([]llm.ModelInfo(nil), a.models...), nil
}

func TestNewSessionReusesValidatedModelsAndBoundsDelegateSchema(t *testing.T) {
	models := make([]llm.ModelInfo, modelavailability.DefaultInlineMaxCount)
	for i := range models {
		models[i].ID = fmt.Sprintf("model-%03d-%s", i, strings.Repeat("x", 12))
	}
	adapter := &modelAvailabilityAdapter{models: models}
	adapter.name = "openai"
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile(models[0].ID), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if got := adapter.calls.Load(); got != 1 {
		t.Fatalf("selected provider ListModels calls = %d, want one validation fetch reused by startup snapshot", got)
	}
	props := sess.delegateToolDefinition().Parameters["properties"].(map[string]any)
	modelDescription := props["model"].(map[string]any)["description"].(string)
	if got := len([]byte(modelDescription)); got > modelavailability.DefaultInlineMaxBytes {
		t.Fatalf("delegate model description = %d bytes, exceeds %d-byte contract", got, modelavailability.DefaultInlineMaxBytes)
	}
	if !sess.reg.RegisteredNames()["model_list"] {
		t.Fatal("model_list is not registered when the verified choices do not fit in the bounded schema description")
	}
}

func TestNewSessionEnumeratesOtherProvidersUnderLifetimeContext(t *testing.T) {
	type ownerContextKey struct{}
	const ownerMarker = "one-shot-run"

	selected := &modelAvailabilityAdapter{models: []llm.ModelInfo{{ID: "gpt-5.5"}}}
	selected.name = "openai"
	var inheritedOwner atomic.Bool
	other := &modelAvailabilityAdapter{
		models: []llm.ModelInfo{{ID: "claude-opus-4-6"}},
		observe: func(ctx context.Context) {
			inheritedOwner.Store(ctx.Value(ownerContextKey{}) == ownerMarker)
		},
	}
	other.name = "anthropic"
	client := llm.NewClient()
	client.Register(selected)
	client.Register(other)
	owner := context.WithValue(context.Background(), ownerContextKey{}, ownerMarker)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.5"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{LifetimeContext: owner})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if !inheritedOwner.Load() {
		t.Fatal("other-provider model enumeration did not inherit the session lifetime context")
	}
}

func TestModelListToolReturnsEveryChoiceExactlyOnceWithinPageBound(t *testing.T) {
	models := make([]llm.ModelInfo, modelavailability.DefaultInlineMaxCount)
	for i := range models {
		models[i].ID = fmt.Sprintf("model-%03d-%s", i, strings.Repeat("x", 12))
	}
	adapter := &modelAvailabilityAdapter{models: models}
	adapter.name = "openai"
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, NewOpenAIProfile(models[0].ID), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	registered := sess.reg.Get("model_list")
	if registered == nil {
		t.Fatal("model_list is not registered")
	}
	const maxBytes = 512
	var got []string
	cursor := ""
	pages := 0
	for {
		raw, err := registered.Exec(context.Background(), sess.env, map[string]any{
			"cursor": cursor, "max_count": float64(modelavailability.DefaultPageMaxCount), "max_bytes": float64(maxBytes),
		})
		if err != nil {
			t.Fatalf("model_list page %d: %v", pages, err)
		}
		page := raw.(modelavailability.Page)
		encoded, err := json.Marshal(page)
		if err != nil {
			t.Fatalf("marshal page %d: %v", pages, err)
		}
		if len(encoded) > maxBytes {
			t.Fatalf("page %d = %d bytes, exceeds %d", pages, len(encoded), maxBytes)
		}
		if len(page.Oversized) != 0 {
			t.Fatalf("page %d marked ordinary choices oversized: %v", pages, page.Oversized)
		}
		got = append(got, page.Choices...)
		pages++
		if page.Terminal {
			if page.Next != "" {
				t.Fatalf("terminal page has continuation %q", page.Next)
			}
			break
		}
		if page.Next == "" {
			t.Fatalf("non-terminal page %d has no continuation", pages-1)
		}
		cursor = page.Next
		if pages > len(models) {
			t.Fatal("model_list did not terminate")
		}
	}
	if pages < 3 {
		t.Fatalf("model_list returned %d pages, want first, middle, and terminal pages", pages)
	}
	if !slices.Equal(got, sess.modelSnapshot.Choices) {
		t.Fatalf("paged choices differ from snapshot:\n got %q\nwant %q", got, sess.modelSnapshot.Choices)
	}
}

func TestModelListToolReturnsBoundedEmptyTerminalPage(t *testing.T) {
	adapter := &modelAvailabilityAdapter{listErr: errors.New("enumeration unavailable")}
	adapter.name = "openai"
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.5"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	registered := sess.reg.Get("model_list")
	if registered == nil {
		t.Fatal("model_list is not registered")
	}
	const maxBytes = 512
	raw, err := registered.Exec(context.Background(), sess.env, map[string]any{"max_bytes": float64(maxBytes)})
	if err != nil {
		t.Fatalf("model_list empty page: %v", err)
	}
	page := raw.(modelavailability.Page)
	if !page.Terminal || len(page.Choices) != 0 || page.Next != "" {
		t.Fatalf("empty page = %#v", page)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxBytes {
		t.Fatalf("empty terminal page = %d bytes, exceeds %d", len(encoded), maxBytes)
	}
}
