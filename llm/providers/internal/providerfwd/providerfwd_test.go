package providerfwd

import (
	"testing"

	"primeradiant.com/serf/llm"
)

// Concrete embedding must promote the optional ModelLister capability from the
// backing adapter onto both forwarders. These assertions fail to compile if a
// forwarder ever loses that promotion (e.g. by switching to an interface
// embed).
var (
	_ llm.ProviderAdapter = (*OpenAICompat)(nil)
	_ llm.ModelLister     = (*OpenAICompat)(nil)
	_ llm.ProviderAdapter = (*Anthropic)(nil)
	_ llm.ModelLister     = (*Anthropic)(nil)
)

func TestOpenAICompat_Name(t *testing.T) {
	// Instance name wins when set.
	if got := NewOpenAICompat("inst", "deflt", nil).Name(); got != "inst" {
		t.Fatalf("Name() = %q, want inst", got)
	}
	// Falls back to the provider-type default when the instance name is empty.
	if got := NewOpenAICompat("", "deflt", nil).Name(); got != "deflt" {
		t.Fatalf("Name() = %q, want deflt", got)
	}
}

func TestAnthropic_Name(t *testing.T) {
	if got := NewAnthropic("inst", "deflt", nil).Name(); got != "inst" {
		t.Fatalf("Name() = %q, want inst", got)
	}
	if got := NewAnthropic("", "deflt", nil).Name(); got != "deflt" {
		t.Fatalf("Name() = %q, want deflt", got)
	}
}
