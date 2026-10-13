package execenv

import "testing"

func TestNumberLines(t *testing.T) {
	t.Parallel()
	two, one := 2, 1
	if got := NumberLines("a\r\nb\nc", &two, &one); got != "   2\tb\n" {
		t.Fatalf("got %q", got)
	}
	if got := NumberLines("a", nil, nil); got != "   1\ta\n" {
		t.Fatalf("got %q", got)
	}
	five := 5
	if got := NumberLines("a\nb", &five, nil); got != "" {
		t.Fatalf("past the end: %q", got)
	}
}
