package agent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/modelavailability"
	"primeradiant.com/evener/llm"
)

type modelAvailabilityAdapter struct {
	fakeAdapter
	models []llm.ModelInfo
	calls  atomic.Int32
}

func (a *modelAvailabilityAdapter) ListModels(context.Context) ([]llm.ModelInfo, error) {
	a.calls.Add(1)
	return append([]llm.ModelInfo(nil), a.models...), nil
}

func TestNewSessionReusesValidatedModelsAndBoundsDelegateSchema(t *testing.T) {
	models := make([]llm.ModelInfo, modelavailability.DefaultInlineMaxCount)
	for i := range models {
		models[i].ID = fmt.Sprintf("model-%03d-%s", i, strings.Repeat("x", 12))
	}
	adapter := &modelAvailabilityAdapter{
		fakeAdapter: fakeAdapter{name: "openai"},
		models:      models,
	}
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
