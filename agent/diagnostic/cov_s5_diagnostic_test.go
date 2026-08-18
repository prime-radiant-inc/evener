package diagnostic

import (
	"errors"
	"testing"

	"primeradiant.com/evener/llm"
)

func TestCov_Classify(t *testing.T) {
	cases := []struct {
		msg  string
		want Source
	}{
		{"unknown provider: foo", SourceSerf},   // evener configuration
		{"rendezvous timed out", SourceHub},     // hub failure
		{"rate limit exceeded", SourceProvider}, // provider failure
		{"something unexpected blew up", SourceSerf},
	}
	for _, c := range cases {
		if got := Classify(c.msg).Source; got != c.want {
			t.Errorf("Classify(%q).Source = %q, want %q", c.msg, got, c.want)
		}
	}
}

func TestCov_FromError(t *testing.T) {
	if got := FromError(nil).Source; got != SourceSerf {
		t.Errorf("nil error → %q, want evener", got)
	}
	cfg := &llm.ConfigurationError{Message: "unknown provider: x"}
	if got := FromError(cfg).Title; got != "Evener configuration error" {
		t.Errorf("ConfigurationError → %q", got)
	}
	provErr := llm.ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)
	if got := FromError(provErr).Source; got != SourceProvider {
		t.Errorf("llm.Error → %q, want provider", got)
	}
	if got := FromError(errors.New("rendezvous lost")).Source; got != SourceHub {
		t.Errorf("plain hub message → %q, want hub", got)
	}
}

func TestCov_NormalizeSource(t *testing.T) {
	for _, s := range []string{"provider", "evener", "hub", "ui", "hook"} {
		if normalizeSource(s) == "" {
			t.Errorf("normalizeSource(%q) should be recognized", s)
		}
	}
	if normalizeSource("  HUB ") != SourceHub {
		t.Error("normalizeSource should trim + lowercase")
	}
	if normalizeSource("bogus") != "" {
		t.Error("unknown source should normalize to empty")
	}
}

func TestCov_DefaultForSource(t *testing.T) {
	if defaultForSource(SourceProvider, "").Source != SourceProvider {
		t.Error("provider default")
	}
	if defaultForSource(SourceHub, "").Source != SourceHub {
		t.Error("hub default")
	}
	if defaultForSource(SourceUI, "").Title != "UI error" {
		t.Error("ui default")
	}
	if defaultForSource(SourceHook, "").Title != "Hook message" {
		t.Error("hook default")
	}
	// Evener with a configuration message → configuration; otherwise plain failure.
	if defaultForSource(SourceSerf, "unknown provider: x").Title != "Evener configuration error" {
		t.Error("evener config default")
	}
	if defaultForSource(SourceSerf, "boom").Title != "Evener error" {
		t.Error("evener plain default")
	}
	if defaultForSource(Source("weird"), "rate limit").Source != SourceProvider {
		t.Error("unknown source should fall back to Classify")
	}
}

func TestCov_FromFields(t *testing.T) {
	// Source override wins over message classification.
	info := FromFields("provider", "", "", "some evener-looking message")
	if info.Source != SourceProvider {
		t.Errorf("source override → %q, want provider", info.Source)
	}
	// Title + hint overrides applied.
	info = FromFields("", "Custom Title", "Custom Hint", "boom")
	if info.Title != "Custom Title" || info.Hint != "Custom Hint" {
		t.Errorf("title/hint overrides not applied: %+v", info)
	}
}
