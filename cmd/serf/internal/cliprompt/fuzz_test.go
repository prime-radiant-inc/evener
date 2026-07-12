package cliprompt

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func FuzzRead(f *testing.F) {
	f.Add("hello", "stdin", false, false)
	f.Add("   ", " piped prompt\n", false, false)
	f.Add("", "ignored", true, false)
	f.Add("", "ignored", false, true)
	f.Fuzz(func(t *testing.T, arg, input string, listSessions, charDevice bool) {
		got := Read([]string{arg}, listSessions, strings.NewReader(input), charDevice)
		trimmedArg := strings.TrimSpace(arg)
		switch {
		case trimmedArg != "":
			if got != trimmedArg {
				t.Fatalf("Read()=%q, want argument %q", got, trimmedArg)
			}
		case listSessions || charDevice:
			if got != "" {
				t.Fatalf("Read()=%q, want empty", got)
			}
		case got != strings.TrimSpace(input):
			t.Fatalf("Read()=%q, want stdin %q", got, strings.TrimSpace(input))
		}
	})
}

func FuzzReadError(f *testing.F) {
	f.Add([]byte("read failure"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if got := Read(nil, false, errorReader{data: data}, false); got != "" {
			t.Fatalf("Read()=%q, want empty after read error", got)
		}
	})
}

type errorReader struct{ data []byte }

func (r errorReader) Read(p []byte) (int, error) {
	return copy(p, r.data), errors.New("read failed")
}

var _ io.Reader = errorReader{}
