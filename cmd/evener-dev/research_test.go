package dev

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// captureResearchStderr runs runResearch while capturing os.Stderr, which is
// where the dev subcommands write usage (see covstmt's flag usage). runResearch
// takes no writer, so the test captures the stream it actually writes to
// instead of a buffer it would never touch.
func captureResearchStderr(t *testing.T, args []string) (int, string) {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	code := runResearch(args)
	w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return code, buf.String()
}

func TestResearchDispatch_RequiresSubcommand(t *testing.T) {
	code, stderr := captureResearchStderr(t, nil)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	for _, name := range []string{"oracle", "rollout"} {
		if !strings.Contains(stderr, name) {
			t.Errorf("usage does not name %q: %q", name, stderr)
		}
	}
}

func TestResearchDispatchUnknownSubcommand(t *testing.T) {
	code, stderr := captureResearchStderr(t, []string{"frobnicate"})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "frobnicate") {
		t.Errorf("stderr does not name the unknown subcommand: %q", stderr)
	}
}
