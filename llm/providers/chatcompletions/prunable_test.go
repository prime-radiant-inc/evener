package chatcompletions

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestPrunablePathsMatchRegistry(t *testing.T) {
	p, ok := llm.ProtocolFor(registry.ProtocolOpenAIChat)
	if !ok {
		t.Fatal("openai-chat not registered")
	}
	if got, want := p.PrunablePaths(), registry.PrunablePaths(registry.ProtocolOpenAIChat); !reflect.DeepEqual(got, want) {
		t.Fatalf("PrunablePaths = %v, want %v", got, want)
	}
}
