package registry

import "testing"

// The ollama host rule's URL grammar refuses several shapes that parse far
// enough to fail only under the rule's own checks: a URL with a control
// character (net/url reports it as a *url.Error, and the refusal must not
// echo the URL it failed on), an authority with no host, and an out-of-range
// port.
func TestValidateOllamaURLRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"control character in host", "http://ho\x00st"},
		{"empty host with port", "http://:1/v1"},
		{"port zero", "http://h:0/"},
		{"port above 65535", "http://h:70000/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := validateOllamaURL(tc.raw, false); err == nil {
				t.Fatalf("validateOllamaURL(%q) = nil error, want refusal", tc.raw)
			}
		})
	}
}

// An escaped path survives normalization: the escaped spelling is kept as
// RawPath rather than collapsed to the decoded form. A URL that already has
// a scheme is used as written (no default port is added).
func TestNormalizeOllamaHostKeepsEscapedPath(t *testing.T) {
	t.Parallel()
	// "/v%31" unescapes to "/v1", so the rule appends another "/v1" segment
	// and records the escaped spelling in RawPath.
	got, err := resolveOllamaHost("", "http://h/v%31")
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://h/v%31/v1"; got != want {
		t.Fatalf("resolveOllamaHost escaped path = %q, want %q", got, want)
	}
}

func TestRedactURLWithoutUserinfo(t *testing.T) {
	t.Parallel()
	// An "@" in the path is not userinfo: the fallback redacts everything
	// before the last "@" rather than trusting url.Parse to find a user.
	if got := RedactURL("http://h/p@th"); got != "***@th" {
		t.Fatalf("RedactURL path-@ = %q", got)
	}
	if got := RedactURL("http://u:sekret@h/"); got != "http://u:xxxxx@h/" {
		t.Fatalf("RedactURL userinfo = %q", got)
	}
	if got := RedactURL("http://h/no-at"); got != "http://h/no-at" {
		t.Fatalf("RedactURL plain = %q", got)
	}
}

func TestFamilyCoversEmptyPattern(t *testing.T) {
	t.Parallel()
	if familyCovers("", "gemini-3-pro") {
		t.Fatal("an empty pattern must cover nothing")
	}
}

func TestValidVertexLocationShapes(t *testing.T) {
	t.Parallel()
	long := make([]byte, 64)
	for i := range long {
		long[i] = 'a'
	}
	for _, tc := range []struct {
		loc  string
		want bool
	}{
		{"us-central1", true},
		{"global", true},
		{"", false},
		{"-leading", false},
		{"trailing-", false},
		{string(long), false},
		{"has_underscore", false},
		{"has.dot", false},
	} {
		if got := validVertexLocation(tc.loc); got != tc.want {
			t.Errorf("validVertexLocation(%q) = %v, want %v", tc.loc, got, tc.want)
		}
	}
}
