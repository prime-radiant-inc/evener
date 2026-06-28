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
	// errReader yields "partial" before failing, so a missing error check would
	// leak that partial data through io.ReadAll instead of returning empty.
	got := Read(nil, false, &errReader{data: []byte("partial"), err: errors.New("read error")}, false)
	if got != "" {
		t.Fatalf("prompt=%q, want empty on stdin error", got)
	}
}

type errReader struct {
	data []byte
	err  error
}

func (r *errReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, r.err
}

var _ io.Reader = (*errReader)(nil)
