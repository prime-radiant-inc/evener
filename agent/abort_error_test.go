package agent

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func TestIsAbortError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "direct AbortError",
			err:  llm.NewAbortError("aborted", nil),
			want: true,
		},
		{
			name: "fmt.Errorf wrapped AbortError",
			err:  fmt.Errorf("during turn: %w", llm.NewAbortError("aborted", nil)),
			want: true,
		},
		{
			name: "ProviderUnhealthyError wrapping AbortError",
			err: &llm.ProviderUnhealthyError{
				Shape:    "stall",
				Attempts: 3,
				Elapsed:  time.Second,
				LastErr:  llm.NewAbortError("aborted", nil),
			},
			want: true,
		},
		{
			name: "plain unrelated error",
			err:  errors.New("boom"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAbortError(tt.err)
			if got != tt.want {
				t.Errorf("isAbortError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
