package cliprompt

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRead_PrefersArgs(t *testing.T) {
	got := Read([]string{"hello", "world"}, false, strings.NewReader("ignored"), false)
	if got != "hello world" {
		t.Fatalf("prompt=%q, want %q", got, "hello world")
	}
}

func TestRead_ResumeReadsStdinWhenPiped(t *testing.T) {
	got := Read(nil, false, strings.NewReader("repair prompt\n"), false)
	if got != "repair prompt" {
		t.Fatalf("prompt=%q, want %q", got, "repair prompt")
	}
}

func TestRead_ResumeDoesNotReadWhenCharDevice(t *testing.T) {
	got := Read(nil, false, strings.NewReader("would block in real life"), true)
	if got != "" {
		t.Fatalf("prompt=%q, want empty", got)
	}
}

func TestRead_ListSessionsDoesNotReadStdin(t *testing.T) {
	got := Read(nil, true, strings.NewReader("ignore"), false)
	if got != "" {
		t.Fatalf("prompt=%q, want empty", got)
	}
}

func TestRead_StdinError(t *testing.T) {
	got := Read(nil, false, &errReader{err: errors.New("read error")}, false)
	if got != "" {
		t.Fatalf("prompt=%q, want empty on stdin error", got)
	}
}

type errReader struct{ err error }

func (r *errReader) Read(p []byte) (int, error) { return 0, r.err }

var _ io.Reader = (*errReader)(nil)
