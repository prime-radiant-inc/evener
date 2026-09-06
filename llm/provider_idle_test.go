package llm

import (
	"context"
	"io"
	"testing"
	"testing/synctest"
	"time"
)

func TestProviderIdleDefaults(t *testing.T) {
	at := DefaultAdapterTimeout()
	if at.Request != 0 {
		t.Errorf("default total deadline = %v, want disabled", at.Request)
	}
	if at.StreamRead != 10*time.Minute {
		t.Errorf("default byte idle = %v, want 10m", at.StreamRead)
	}
	for _, streaming := range []bool{false, true} {
		ctx, cancel := ApplyAdapterTimeout(context.Background(), &at, streaming)
		if _, ok := ctx.Deadline(); ok {
			t.Errorf("streaming=%v: default adds total deadline", streaming)
		}
		cancel()
	}
}

func TestSSEIdleResetsOnPartialHeartbeatBytes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, w := io.Pipe()
		done := make(chan struct{})
		defer func() { r.Close(); <-done }()
		go func() {
			defer close(done)
			defer w.Close()
			for _, chunk := range []string{":", " heartbeat", "\n", "data: ok\n\n"} {
				time.Sleep(40 * time.Second)
				if _, err := io.WriteString(w, chunk); err != nil {
					return
				}
			}
		}()
		var data string
		err := ParseSSE(context.Background(), r, func(ev SSEEvent) error { data = string(ev.Data); return nil }, WithStreamReadTimeout(time.Minute))
		if err != nil {
			t.Fatalf("active partial heartbeat timed out: %v", err)
		}
		if data != "ok" {
			t.Fatalf("data=%q", data)
		}
	})
}

func TestProviderIdleDefaultBoundsHeadersWithoutTotalDeadline(t *testing.T) {
	at := DefaultAdapterTimeout()
	tr := AdapterTransport(&at)
	if tr == nil || tr.ResponseHeaderTimeout != 10*time.Minute {
		t.Fatalf("default response-header timeout=%v", tr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, streaming := range []bool{false, true} {
		attempt, done := ApplyAdapterTimeout(ctx, &at, streaming)
		want, _ := ctx.Deadline()
		got, _ := attempt.Deadline()
		if got != want {
			t.Fatalf("caller deadline changed: %v, want %v", got, want)
		}
		done()
	}
}

func TestSSELateReaderCannotRearmStoppedTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		timer := &sseReadTimer{timer: time.NewTimer(time.Minute), timeout: time.Minute}
		timer.stop()
		// A Read already in flight can deliver bytes after parser cleanup.
		timer.progress()
		time.Sleep(2 * time.Minute)
		select {
		case <-timer.timer.C:
			t.Fatal("late reader rearmed timer after parser cleanup")
		default:
		}
	})
}
