package responses

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestPrunablePathsMatchRegistry(t *testing.T) {
	p, ok := llm.ProtocolFor(registry.ProtocolOpenAIResponses)
	if !ok {
		t.Fatal("openai-responses not registered")
	}
	if got, want := p.PrunablePaths(), registry.PrunablePaths(registry.ProtocolOpenAIResponses); !reflect.DeepEqual(got, want) {
		t.Fatalf("PrunablePaths = %v, want %v", got, want)
	}
}
