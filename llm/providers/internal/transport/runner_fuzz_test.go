package transport

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func FuzzStreamRunner(f *testing.F) {
	f.Add(uint8(0), "data: partial\n\n")
	f.Add(uint8(1), "data: finish\n\n")
	f.Add(uint8(2), "data: stop\n\n")
	f.Add(uint8(3), "data: raw\n\n")

	f.Fuzz(func(t *testing.T, mode uint8, payload string) {
		if len(payload) > 4096 {
			t.Skip()
		}
		ctx := context.Background()
		if mode%4 == 2 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}

		finished := false
		r := &StreamRunner{
			Provider: "fuzz-provider",
			Resp:     respFrom(payload),
			Stream:   llm.NewChanStream(nil),
			OnEvent: func(ev llm.SSEEvent) error {
				if mode%4 == 1 || strings.Contains(string(ev.Data), "finish") {
					finished = true
				}
				if strings.Contains(string(ev.Data), "stop") {
					return context.Canceled
				}
				return nil
			},
			Finished:      &finished,
			IncompleteMsg: "fuzz stream incomplete",
		}
		events := drain(ctx, t, r)
		errorsSeen := 0
		for _, ev := range events {
			if ev.Type == llm.StreamEventError {
				errorsSeen++
			}
		}
		wantErrors := 1
		if finished {
			wantErrors = 0
		}
		if errorsSeen != wantErrors {
			t.Fatalf("error events = %d, want %d", errorsSeen, wantErrors)
		}
	})
}
