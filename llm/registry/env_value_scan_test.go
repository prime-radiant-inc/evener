package registry

import "testing"

// expandEnv's contract for a malformed value: an empty value and no missing
// pieces — every field that reaches it was validated at load time, so a
// syntax error is not the caller's to diagnose here.
func TestExpandEnvSyntaxErrorReturnsEmpty(t *testing.T) {
	t.Parallel()
	got, missing := expandEnv("${unclosed", mapEnv(nil))
	if got != "" || len(missing) != 0 {
		t.Fatalf("expandEnv malformed = (%q, %v), want (\"\", nil)", got, missing)
	}
}

func TestIsAuthSchemeWord(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		token string
		want  bool
	}{
		{"Bearer", true},
		{"basic", true},
		{"", false},
		{"Bearer2", false},
		{"X-Key", false},
	} {
		if got := isAuthSchemeWord(tc.token); got != tc.want {
			t.Errorf("isAuthSchemeWord(%q) = %v, want %v", tc.token, got, tc.want)
		}
	}
}

// A value the scanner cannot read owns neither command material nor a
// scheme-word default; the expansion paths report malformed values.
func TestMalformedValuesAreNeitherCommandNorSchemeDefault(t *testing.T) {
	t.Parallel()
	r := &Registry{env: mapEnv(nil)}
	if hasCommandMaterial("$(unclosed") {
		t.Fatal("a scan error is not command material")
	}
	if r.schemeWordDefault("${unclosed") {
		t.Fatal("a scan error is not a scheme-word default")
	}
	if !hasCommandMaterial("pre $(cmd) post") {
		t.Fatal("a command piece is command material")
	}
}
