package llm

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type borrowedSSEReader struct {
	io.Reader
	closes int
}

func (r *borrowedSSEReader) Close() error { r.closes++; return nil }

func TestParseSSEPreservesBorrowedReaderOwnership(t *testing.T) {
	callbackErr := errors.New("callback stopped")
	for _, tc := range []struct {
		name        string
		callbackErr error
	}{{"EOF", nil}, {"callback error", callbackErr}} {
		t.Run(tc.name, func(t *testing.T) {
			r := &borrowedSSEReader{Reader: strings.NewReader("data: hello\n\n")}
			err := ParseSSE(context.Background(), r, func(SSEEvent) error { return tc.callbackErr }, WithStreamReadTimeout(time.Hour))
			if !errors.Is(err, tc.callbackErr) {
				t.Fatalf("error = %v, want %v", err, tc.callbackErr)
			}
			if r.closes != 0 {
				t.Fatalf("parser closed borrowed reader %d times", r.closes)
			}
		})
	}
}
