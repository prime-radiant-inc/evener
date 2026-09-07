package llm_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/providers/anthropic"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/google"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

type modelListTransport func(*http.Request) (*http.Response, error)

// A client-owned provider deadline must not become the protocol's caller deadline.
func TestModelsRequestTimeoutAttemptOwnership(t *testing.T) {
	for _, provider := range wireProviders() {
		for _, scenario := range []string{"request", "caller-deadline", "caller-cancel"} {
			t.Run(provider.name+"/"+scenario, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					httpClient := &http.Client{Transport: modelListTransport(func(req *http.Request) (*http.Response, error) {
						<-req.Context().Done()
						return nil, req.Context().Err()
					})}
					client := provider.wireClient(t, "http://example.invalid", httpClient, nil)
					ctx := context.Background()
					want := apilog.AttemptProviderTimeout
					cause := context.DeadlineExceeded
					switch scenario {
					case "caller-deadline":
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, time.Second)
						defer cancel()
						want = apilog.AttemptCallerCancel
					case "caller-cancel":
						var cancel context.CancelFunc
						ctx, cancel = context.WithCancel(ctx)
						defer cancel()
						time.AfterFunc(time.Second, cancel)
						want, cause = apilog.AttemptCallerCancel, context.Canceled
					}
					caller := ctx
					sink := &lockedAttemptSink{}
					ctx = llm.WithAPIAttemptSink(ctx, sink)
					ctx = llm.WithAPIAttemptGroup(ctx, llm.NewAPIAttemptGroup("listing-ownership"))
					ctx = llm.WithModelListingTimeout(ctx, llm.AdapterTimeout{Request: time.Minute})
					_, err := client.Models(ctx, provider.name)
					if !errors.Is(err, cause) {
						t.Fatalf("error=%v want %v", err, cause)
					}
					attempts := sink.snapshot()
					if len(attempts) != 1 {
						t.Fatalf("attempts=%d want 1", len(attempts))
					}
					if attempts[0].Outcome != want {
						t.Errorf("outcome=%s want %s", attempts[0].Outcome, want)
					}
					if scenario == "request" && caller.Err() != nil {
						t.Errorf("provider deadline canceled original caller: %v", caller.Err())
					}
				})
			})
		}
	}
}

func (f modelListTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type timeoutListingAdapter struct {
	recordingAdapter
	list func(context.Context) ([]registry.Model, error)
}

func (a *timeoutListingAdapter) LiveModels(ctx context.Context) ([]registry.Model, error) {
	return a.list(ctx)
}

func TestModelsOverrideRequestTimeoutOwnership(t *testing.T) {
	for _, scenario := range []string{"request", "caller-deadline", "caller-cancel"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				want := time.Minute
				cause := context.DeadlineExceeded
				switch scenario {
				case "caller-deadline":
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, time.Second)
					defer cancel()
					want = time.Second
				case "caller-cancel":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					defer cancel()
					time.AfterFunc(time.Second, cancel)
					want, cause = time.Second, context.Canceled
				}
				caller := ctx
				ctx = llm.WithModelListingTimeout(ctx, llm.AdapterTimeout{Request: time.Minute})
				a := &timeoutListingAdapter{list: func(ctx context.Context) ([]registry.Model, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}}
				a.name = "fake"
				client := llm.NewClient()
				client.Register(a)
				start := time.Now()
				_, err := client.Models(ctx, "fake")
				if !errors.Is(err, cause) {
					t.Fatalf("error=%v want %v", err, cause)
				}
				if elapsed := time.Since(start); elapsed != want {
					t.Errorf("elapsed=%v want %v", elapsed, want)
				}
				if scenario == "request" && caller.Err() != nil {
					t.Errorf("provider deadline canceled original caller: %v", caller.Err())
				}
			})
		})
	}
}

func TestModelsTimeoutPolicy(t *testing.T) {
	for _, name := range []string{"fake", "openai"} {
		for _, explicit := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/default", true: "/explicit"}[explicit], func(t *testing.T) {
				c := llm.NewClient()
				want := llm.DefaultAdapterTimeout()
				ctx := context.Background()
				if explicit {
					want = llm.AdapterTimeout{}
					ctx = llm.WithModelListingTimeout(ctx, want)
				}
				called := false
				a := &timeoutListingAdapter{list: func(ctx context.Context) ([]registry.Model, error) {
					called = true
					if got := *llm.ModelListingTimeout(ctx); got != want {
						t.Errorf("policy=%+v want %+v", got, want)
					}
					if _, ok := ctx.Deadline(); ok {
						t.Error("listing acquired an unwanted total deadline")
					}
					return nil, nil
				}}
				a.name = name
				c.Register(a)
				if _, err := c.Models(ctx, name); err != nil {
					t.Fatal(err)
				}
				if !called {
					t.Fatal("override not called")
				}
			})
		}
	}
}

func TestListingHTTPTimeoutPolicies(t *testing.T) {
	for _, provider := range []string{"chat", "responses", "google", "anthropic"} {
		for _, scenario := range []string{"default-body", "default-headers", "explicit-idle", "disabled", "active", "caller", "client", "request", "cancel"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx := context.Background()
					want := 10 * time.Minute
					wantErr := true
					switch scenario {
					case "explicit-idle":
						ctx = llm.WithModelListingTimeout(ctx, llm.AdapterTimeout{StreamRead: time.Minute})
						want = time.Minute
					case "disabled":
						ctx = llm.WithModelListingTimeout(ctx, llm.AdapterTimeout{})
						want = 11 * time.Minute
						wantErr = false
					case "active":
						want = 12 * time.Minute
						wantErr = false
					case "caller":
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, time.Minute)
						defer cancel()
						want = time.Minute
					case "request":
						ctx = llm.WithModelListingTimeout(ctx, llm.AdapterTimeout{Request: time.Minute})
						want = time.Minute
					case "cancel":
						var cancel context.CancelFunc
						ctx, cancel = context.WithCancel(ctx)
						defer cancel()
						time.AfterFunc(time.Minute, cancel)
						want = time.Minute
					case "client":
						want = time.Minute
					}
					r, w := io.Pipe()
					defer r.Close()
					done := make(chan struct{})
					defer func() { <-done }()
					go func() {
						defer close(done)
						defer w.Close()
						if scenario == "default-headers" {
							return
						}
						if scenario == "active" {
							for range 3 {
								if _, err := io.WriteString(w, " "); err != nil {
									return
								}
								time.Sleep(4 * time.Minute)
							}
							io.WriteString(w, `{"data":[],"models":[]}`)
							return
						}
						if _, err := io.WriteString(w, `{"data":[],"models":[]}`); err != nil {
							return
						}
						time.Sleep(11 * time.Minute)
					}()
					client := &http.Client{Transport: modelListTransport(func(req *http.Request) (*http.Response, error) {
						if scenario == "default-headers" {
							<-req.Context().Done()
							return nil, req.Context().Err()
						}
						go func() {
							select {
							case <-req.Context().Done():
								w.CloseWithError(req.Context().Err())
							case <-done:
							}
						}()
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: r, Request: req}, nil
					})}
					if scenario == "default-headers" {
						transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
							a, b := net.Pipe()
							go func() {
								defer b.Close()
								req, err := http.ReadRequest(bufio.NewReader(b))
								if err != nil {
									return
								}
								req.Body.Close()
								io.Copy(io.Discard, b)
							}()
							return a, nil
						}}
						defer transport.CloseIdleConnections()
						client.Transport = transport
					}
					if scenario == "client" {
						client.Timeout = time.Minute
					}
					var p llm.Protocol
					switch provider {
					case "chat":
						p = &chatcompletions.Protocol{Client: client}
					case "responses":
						p = &responses.Protocol{Client: client}
					case "google":
						p = &google.Protocol{Client: client}
					case "anthropic":
						p = &anthropic.Protocol{Client: client}
					}
					res := registry.Resolved{Transport: registry.Transport{Auth: registry.AuthBearer, BaseURL: "http://example.invalid", ModelsEndpoint: "/models"}, Credential: registry.Credential{Value: "test"}}
					start := time.Now()
					_, err := p.ListModels(ctx, res)
					if wantErr {
						cause := llm.ErrResponseIdleTimeout
						class := llm.ErrorClassRetryable
						switch scenario {
						case "default-headers", "caller", "client", "request":
							cause = context.DeadlineExceeded
						case "cancel":
							cause = context.Canceled
							class = llm.ErrorClassPermanent
						}
						if !errors.Is(err, cause) {
							t.Errorf("error %v does not preserve %v", err, cause)
						}
						if got := llm.Classify(err); got != class {
							t.Errorf("class=%v want %v", got, class)
						}
					}
					if (err != nil) != wantErr {
						t.Errorf("err=%v wantErr=%v", err, wantErr)
					}
					if elapsed := time.Since(start); elapsed != want {
						t.Errorf("elapsed=%v want %v (err=%v)", elapsed, want, err)
					}
				})
			})
		}
	}
}
