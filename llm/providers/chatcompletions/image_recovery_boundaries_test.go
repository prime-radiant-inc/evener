package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/internal/imagerepair"
	"primeradiant.com/evener/llm/registry"
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

type deadlineMessages struct {
	ctx context.Context
	raw json.RawMessage
}

func (m deadlineMessages) MarshalJSON() ([]byte, error) {
	<-m.ctx.Done()
	return m.raw, nil
}

type deadlineImagePreparer struct {
	imageRecoveryPreparer
	calls int
	body  map[string]any
}

func (p *deadlineImagePreparer) PrepareRequest(ctx context.Context, _ *http.Request, body map[string]any, _ llm.Request, _ registry.Resolved) error {
	p.calls++
	if p.calls == 2 {
		raw, err := json.Marshal(body["messages"])
		if err != nil {
			return err
		}
		// Expire the request deadline inside repair's message serialization.
		// The parent stays live, so using it would finish the image conversion.
		body["messages"] = deadlineMessages{ctx: ctx, raw: raw}
		p.body = body
	}
	return nil
}

func deadlineRecoveryRequest(t *testing.T) (llm.Request, registry.Resolved) {
	t.Helper()
	req := recoveryRequest(recoveryJPEG(t, nil))
	req.AdapterTimeout = &llm.AdapterTimeout{Request: time.Second}
	res := resolved(nil)
	res.Transport.Auth = registry.AuthOAuthOpenAICodex
	res.Transport.BaseURL = "https://provider.test"
	res.Transport.StreamEndpoint = res.Transport.Endpoint
	return req, res
}

func TestImageRecoveryAttemptDeadlineStopsRepair(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[stream], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				preparer := &deadlineImagePreparer{}
				ctx := llm.WithAuthenticatorOverride(t.Context(), preparer)
				req, res := deadlineRecoveryRequest(t)
				calls := 0
				client := &http.Client{Transport: listingRoundTripper(func(r *http.Request) (*http.Response, error) {
					calls++
					if err := r.Context().Err(); err != nil {
						return nil, err
					}
					return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(imageRejection)), Request: r}, nil
				})}
				_, err := recoveryCall(ctx, &Protocol{Client: client}, req, res, stream)
				if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || preparer.calls != 2 {
					t.Fatalf("error=%v parent=%v preparations=%d", err, ctx.Err(), preparer.calls)
				}
				if _, unchanged := preparer.body["messages"].(deadlineMessages); !unchanged {
					t.Error("repair replaced the image after the attempt deadline expired")
				}
				if calls != 1 {
					t.Fatalf("transport calls=%d, want only the initial rejection", calls)
				}
			})
		})
	}
}

func TestImageRecoveryDeadlineRetryOwnership(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, callerDeadline := range []bool{false, true} {
			name := map[bool]string{false: "complete", true: "stream"}[stream] + map[bool]string{false: "/attempt", true: "/caller"}[callerDeadline]
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					preparer := &deadlineImagePreparer{}
					ctx := llm.WithAuthenticatorOverride(t.Context(), preparer)
					if callerDeadline {
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, time.Second/2)
						defer cancel()
					}
					req, res := deadlineRecoveryRequest(t)
					calls := 0
					client := &http.Client{Transport: listingRoundTripper(func(r *http.Request) (*http.Response, error) {
						calls++
						if err := r.Context().Err(); err != nil {
							return nil, err
						}
						w := httptest.NewRecorder()
						if calls < 3 {
							w.WriteHeader(http.StatusBadRequest)
							_, _ = w.WriteString(imageRejection)
						} else {
							recoveryReply(w, stream)
						}
						return w.Result(), nil
					})}
					policy := llm.RetryPolicy{MaxRetries: 1}
					var resp llm.Response
					var err error
					p := &Protocol{Client: client}
					if stream {
						err = llm.RetryStream(ctx, llm.RetryStreamOptions{Policy: policy}, func(ctx context.Context) (llm.AttemptReport, error) {
							var attemptErr error
							resp, attemptErr = recoveryCall(ctx, p, req, res, true)
							return llm.AttemptReport{Phase: llm.PhaseOpen}, attemptErr
						})
					} else {
						resp, err = llm.Retry(ctx, policy, nil, nil, func() (llm.Response, error) {
							return recoveryCall(ctx, p, req, res, false)
						})
					}
					if callerDeadline {
						if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() == nil || calls != 1 || preparer.calls != 2 {
							t.Fatalf("caller deadline: error=%v parent=%v sends=%d preparations=%d", err, ctx.Err(), calls, preparer.calls)
						}
					} else if err != nil || ctx.Err() != nil || calls != 3 || preparer.calls != 4 || len(resp.ToolCalls()) != 1 {
						t.Fatalf("attempt retry: error=%v parent=%v sends=%d preparations=%d response=%+v", err, ctx.Err(), calls, preparer.calls, resp)
					}
				})
			})
		}
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
