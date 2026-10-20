package chatcompletions

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestImageRecoveryNeverReplaysAnExposedStream(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, imageRejection)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, twoChatChunks+"data: {\"error\":{\"message\":\"Image normalization failed\",\"code\":400}}\n\n")
	}))
	defer srv.Close()
	_, err := recoveryCall(t.Context(), &Protocol{Client: srv.Client()}, recoveryRequest(recoveryJPEG(t, nil)), liveRes(srv, nil), true)
	if err == nil || calls != 2 {
		t.Fatalf("requests=%d error=%v", calls, err)
	}
}

func TestImageRecoveryCancellationPreventsResend(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[stream], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				cancel()
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, imageRejection)
			}))
			defer srv.Close()
			_, err := recoveryCall(ctx, &Protocol{Client: srv.Client()}, recoveryRequest(recoveryJPEG(t, nil)), liveRes(srv, nil), stream)
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("requests=%d error=%v", calls, err)
			}
		})
	}
}
