package chatcompletions

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"primeradiant.com/evener/llm/internal/imagerepair"
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

func TestImageRecoverySharesDecodeBudget(t *testing.T) {
	t.Parallel()
	profile, err := os.ReadFile("../../internal/imagerepair/testdata/p3.icc")
	if err != nil {
		t.Fatal(err)
	}
	original := recoveryJPEG(t, nil)
	uri := recoveryURI(recoveryWithProfile(original, profile))
	pixelsLeft := int64(6)
	first, err := repairImageURI(t.Context(), uri, imagerepair.PreferColorManaged, &pixelsLeft)
	if err != nil || first.MediaType != "image/png" || first.Method != imagerepair.ColorManaged {
		t.Fatalf("first repair method=%v remaining=%d error=%v", first.Method, pixelsLeft, err)
	}
	second, err := repairImageURI(t.Context(), uri, imagerepair.PreferColorManaged, &pixelsLeft)
	if err != nil || second.Method != imagerepair.ProfileRemoved || !bytes.Equal(second.Data, original) {
		t.Fatalf("budget fallback method=%v error=%v", second.Method, err)
	}
	third, err := repairImageURI(t.Context(), recoveryURI(original), imagerepair.PreferColorManaged, &pixelsLeft)
	if err != nil || third.Method != imagerepair.Unchanged || !bytes.Equal(third.Data, original) {
		t.Fatalf("unchanged fallback method=%v error=%v", third.Method, err)
	}
}
