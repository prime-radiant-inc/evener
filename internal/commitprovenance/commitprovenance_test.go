package commitprovenance

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", ModeCoauthor, false},
		{"coauthor", ModeCoauthor, false},
		{" off ", ModeOff, false},
		{"off", ModeOff, false},
		{"bogus", "", true},
	}
	for _, tt := range tests {
		got, err := Normalize(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("Normalize(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if got != tt.want {
			t.Fatalf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestValidateAllowsEmpty(t *testing.T) {
	for _, in := range []string{"", "coauthor", "off"} {
		if err := Validate(in); err != nil {
			t.Fatalf("Validate(%q) = %v", in, err)
		}
	}
	if err := Validate("bogus"); err == nil {
		t.Fatal("Validate(bogus) = nil, want error")
	}
}

func TestTrailer(t *testing.T) {
	got, err := Trailer("0.9.12", "anthropic/claude-opus-4.6", "magic-kingdom", "034uOsahfdjKENyvpnJexm")
	if err != nil {
		t.Fatalf("Trailer: %v", err)
	}
	want := "Co-authored-by: Evener 0.9.12 (anthropic/claude-opus-4.6) <034uOsahfdjKENyvpnJexm@magic-kingdom>"
	if got != want {
		t.Fatalf("Trailer = %q, want %q", got, want)
	}
}

func TestTrailerRejectsUnsafeFields(t *testing.T) {
	cases := []struct {
		name    string
		version string
		model   string
		host    string
		session string
	}{
		{"empty version", "", "m", "host", "s"},
		{"empty model", "v", "", "host", "s"},
		{"empty host", "v", "m", "", "s"},
		{"empty session", "v", "m", "host", ""},
		{"version newline", "v\nx", "m", "host", "s"},
		{"model angle", "v", "m<x", "host", "s"},
		{"host space", "v", "m", "two words", "s"},
		{"host at", "v", "m", "host@x", "s"},
		{"session angle", "v", "m", "host", "s<x"},
		{"session newline", "v", "m", "host", "s\nx"},
	}
	for _, tt := range cases {
		if _, err := Trailer(tt.version, tt.model, tt.host, tt.session); err == nil {
			t.Errorf("%s: Trailer unexpectedly succeeded", tt.name)
		}
	}
}

func TestTrailerAllowsModelPunctuation(t *testing.T) {
	got, err := Trailer("0.9.12", "openrouter/anthropic/claude-3.5:beta", "host-1", "session_1")
	if err != nil {
		t.Fatalf("Trailer: %v", err)
	}
	if !strings.Contains(got, "openrouter/anthropic/claude-3.5:beta") {
		t.Fatalf("Trailer lost model punctuation: %q", got)
	}
}
