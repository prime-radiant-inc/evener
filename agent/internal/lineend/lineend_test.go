package lineend

import "testing"

func TestNormalize(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"":                "",
		"a\nb\n":          "a\nb\n",
		"a\r\nb\r\n":      "a\nb\n",
		"a\rb\r":          "a\nb\n",
		"a\r\r\nb\n\r":    "a\n\nb\n\n",
		"no line endings": "no line endings",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
