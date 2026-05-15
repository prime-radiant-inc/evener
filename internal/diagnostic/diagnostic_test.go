package diagnostic

import "testing"

func TestClassifyUnknownProviderAsSerfConfiguration(t *testing.T) {
	info := Classify("configuration error: unknown provider: openrouter")
	if info.Source != SourceSerf {
		t.Fatalf("Source=%q, want %q", info.Source, SourceSerf)
	}
	if info.Title != "Serf configuration error" {
		t.Fatalf("Title=%q", info.Title)
	}
	if info.Hint == "" {
		t.Fatal("expected launch/config hint")
	}
}

func TestClassifyProviderHTTPFailureAsProvider(t *testing.T) {
	info := Classify("openai error (status=401): invalid API key")
	if info.Source != SourceProvider {
		t.Fatalf("Source=%q, want %q", info.Source, SourceProvider)
	}
	if info.Title != "Provider error" {
		t.Fatalf("Title=%q", info.Title)
	}
}

func TestClassifySpawnFailureAsHub(t *testing.T) {
	info := Classify("daemon spawn timed out: process exited before rendezvous")
	if info.Source != SourceHub {
		t.Fatalf("Source=%q, want %q", info.Source, SourceHub)
	}
}
